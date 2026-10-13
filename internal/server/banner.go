package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/clock"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

// resolveTimeout bounds the *.localhost resolution check of the banner.
const resolveTimeout = time.Second

// localhostProbe is a name under *.localhost the banner checks, as a
// virtual-hosted bucket or a function URL host would be.
const localhostProbe = "understudy-probe.s3.localhost"

// banner announces the endpoints, the environment that points an SDK at
// them, the admin URL, the services and the warnings (ARCHITECTURE.md 17):
// a block of text with text logs, one record with JSON logs, so a log
// pipeline never sees a line it cannot parse. bound is the main listener's
// address as bound, with the port a requested port 0 resolved to.
func (s *Server) banner(ctx context.Context, eps Endpoints, bound net.Addr) {
	env := []string{
		"AWS_ENDPOINT_URL=" + eps.Main,
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"AWS_REGION=us-east-1",
	}
	status := components{host: s.wired.host, local: s.wired.local}.Components(ctx)
	services := s.services(status)
	notes := s.warnings(ctx, status, bound)
	clockLine := clockNote(s.wired.kernel.Clock(), s.wired.clockFrom)
	if s.cfg.LogFormat == LogJSON {
		attrs := []slog.Attr{
			slog.String("endpoint", eps.Main),
			slog.String("admin", eps.Main+"/_understudy/v1/"),
			slog.Any("services", services),
			slog.Any("env", env),
			slog.Any("warnings", notes),
		}
		if eps.TLS != "" {
			attrs = append(attrs, slog.String("tls", eps.TLS))
		}
		if clockLine != "" {
			attrs = append(attrs, slog.String("clock", clockLine))
		}
		s.log.LogAttrs(ctx, slog.LevelInfo, "understudy is serving", attrs...)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nunderstudy %s is serving\n\n", s.cfg.Version)
	fmt.Fprintf(&b, "  endpoint  %s\n", eps.Main)
	if eps.TLS != "" {
		fmt.Fprintf(&b, "  tls       %s (self-signed for localhost, *.localhost and 127.0.0.1)\n", eps.TLS)
	}
	fmt.Fprintf(&b, "  admin     %s/_understudy/v1/\n", eps.Main)
	fmt.Fprintf(&b, "  services  %s\n", strings.Join(services, ", "))
	if clockLine != "" {
		fmt.Fprintf(&b, "  clock     %s\n", clockLine)
	}
	if s.wired.data != nil {
		fmt.Fprintf(&b, "  data      %s\n", s.wired.data.path)
	}
	b.WriteString("\n  Point an SDK, the AWS CLI or Terraform at it:\n\n")
	for _, kv := range env {
		fmt.Fprintf(&b, "    export %s\n", kv)
	}
	b.WriteString("\n")
	for _, n := range notes {
		fmt.Fprintf(&b, "  %s\n", n)
	}
	b.WriteString("\n")
	if _, err := io.WriteString(s.cfg.LogOutput, b.String()); err != nil {
		s.log.LogAttrs(ctx, slog.LevelWarn, "the startup banner was not written to the log output", slog.String("error", err.Error()))
	}
}

// warnings are the banner's notes: what a listener beyond loopback exposes,
// what does not work on this host, Docker when it is not there or does not
// answer, Lambda when lambda.network is disabled, and DynamoDB Local's
// licence line, which the banner always shows.
func (s *Server) warnings(ctx context.Context, status serveradmin.ComponentStatus, bound net.Addr) []string {
	var out []string
	if !s.cfg.Listen.Addr().IsLoopback() {
		out = append(out, exposureWarning(s.cfg, bound))
	}
	lookup, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	if _, err := net.DefaultResolver.LookupHost(lookup, localhostProbe); err != nil {
		out = append(out, "warning: *.localhost does not resolve on this host, so virtual-hosted S3 addresses and function URL hosts do not either; use path-style S3 (UsePathStyle, s3_use_path_style, forcePathStyle) or the /_understudy/lambda-url/ path")
	}
	if status.Docker.Status != admin.Enabled {
		out = append(out, "warning: "+status.Docker.Reason)
	}
	// Docker's warning above already says functions cannot run without
	// it; lambda.network disabled is a reason of its own.
	if reason := s.wired.host.lambdaOff(); reason != "" {
		out = append(out, "warning: "+reason)
	}
	return append(out, ddblocal.BannerLine(s.wired.licence))
}

// exposureWarning is the banner's note for a listener beyond loopback,
// which checkExposure accepted: without a token only the published
// container port guards it, and a token guards the admin API alone. bound
// is the listener's address as bound.
func exposureWarning(cfg Config, bound net.Addr) string {
	if cfg.AdminToken == "" {
		return "warning: the AWS API and the admin API answer anyone who reaches the published port; publish it on loopback only, such as -p 127.0.0.1:4566:4566"
	}
	return fmt.Sprintf("warning: the admin token guards only the admin API under /_understudy/; the AWS API on %s answers anyone who reaches it without checking signatures, "+
		"including reads of every secret and object and Lambda functions that run any container image", bound)
}

// clockOrigin is what set the clock serve starts with.
//
//sumtype:decl
type clockOrigin interface{ isClockOrigin() }

// clockSetting is the clock setting serve was started with.
type clockSetting struct{}

// clockRestored is the saved state --persist restored from dir.
type clockRestored struct{ dir string }

// clockLoaded is the snapshot loaded at start from path.
type clockLoaded struct{ path string }

func (clockSetting) isClockOrigin()  {}
func (clockRestored) isClockOrigin() {}
func (clockLoaded) isClockOrigin()   {}

// clockNote is the banner's clock line, empty while clk follows wall
// time: a frozen or scaled clock leaves visibility timeouts, delays,
// retention, schedules and TTL firing late or never, which nothing else
// at start would tell, least of all when a restart restored the mode.
func clockNote(clk *clock.Clock, from clockOrigin) string {
	now := clk.Now().Wire().UTC().Format(time.RFC3339)
	var state, next string
	switch m := clk.Mode().(type) {
	case clock.Frozen:
		state, next = "frozen at "+now, "'understudy clock resume' runs it"
	case clock.Scaled:
		state = fmt.Sprintf("scaled to %g emulator seconds per wall second, now %s", m.Factor, now)
		next = "'understudy clock resume' runs it at wall-time speed"
	case clock.Running:
		return ""
	}
	var source string
	switch f := from.(type) {
	case clockSetting:
		source = "set by --clock, --clock-scale, UNDERSTUDY_CLOCK, UNDERSTUDY_CLOCK_SCALE or the configuration file"
	case clockRestored:
		source = "restored with the saved state of --persist " + f.dir
	case clockLoaded:
		source = "loaded with the snapshot " + f.path
	}
	return state + " (" + source + "); " + next
}

// services names the services the registry wires, marking those that
// cannot serve on this host with their state, as health reports them.
func (s *Server) services(status serveradmin.ComponentStatus) []string {
	services := s.wired.kernel.Registry().Services()
	out := make([]string, 0, len(services))
	for _, svc := range services {
		name := string(svc.ID)
		if st, ok := status.Services[svc.ID]; ok && st.Status != admin.Enabled {
			label := string(st.Status)
			if svc.ID == "lambda" {
				// Lambda is degraded only for running functions.
				label += ": " + controlPlaneOnlyLabel
			}
			name += " (" + label + ")"
		}
		out = append(out, name)
	}
	return out
}
