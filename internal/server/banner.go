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
// pipeline never sees a line it cannot parse.
func (s *Server) banner(ctx context.Context, eps Endpoints) {
	env := []string{
		"AWS_ENDPOINT_URL=" + eps.Main,
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"AWS_REGION=us-east-1",
	}
	status := components{host: s.wired.host, local: s.wired.local}.Components(ctx)
	services := s.services(status)
	notes := s.warnings(ctx, status)
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

// warnings are the banner's notes: what does not work on this host, Docker
// when it is not there or does not answer, Lambda when lambda.network is
// disabled, and DynamoDB Local's licence line, which the banner always
// shows.
func (s *Server) warnings(ctx context.Context, status serveradmin.ComponentStatus) []string {
	var out []string
	if _, ok := s.cfg.Exposure.(ContainerPublished); ok && !s.cfg.Listen.Addr().IsLoopback() {
		out = append(out, "warning: the admin API answers anyone who reaches the published port; publish it on loopback only, such as -p 127.0.0.1:4566:4566")
	}
	lookup, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	if _, err := net.DefaultResolver.LookupHost(lookup, localhostProbe); err != nil {
		out = append(out, "warning: *.localhost does not resolve on this host, so virtual-hosted S3 addresses and function URL hosts do not either; use path-style S3 (UsePathStyle, s3_use_path_style, forcePathStyle) or the /_understudy/lambda-url/ path")
	}
	if status.Docker.Status != admin.Enabled {
		out = append(out, "warning: "+status.Docker.Reason)
	}
	// Without Docker, Lambda takes Docker's reason, which is shown above.
	if lambda, ok := status.Services["lambda"]; ok && lambda.Reason != status.Docker.Reason {
		out = append(out, "warning: "+lambda.Reason)
	}
	return append(out, ddblocal.BannerLine(s.wired.licence))
}

// services names the services the registry wires, marking those that
// cannot serve on this host with their state, as health reports them.
func (s *Server) services(status serveradmin.ComponentStatus) []string {
	services := s.wired.kernel.Registry().Services()
	out := make([]string, 0, len(services))
	for _, svc := range services {
		name := string(svc.ID)
		if st, ok := status.Services[svc.ID]; ok && st.Status != admin.Enabled {
			name += " (" + string(st.Status) + ")"
		}
		out = append(out, name)
	}
	return out
}
