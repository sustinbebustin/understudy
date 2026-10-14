package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/config"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// checkStatus is a doctor check's verdict.
type checkStatus string

const (
	checkOK   checkStatus = "ok"
	checkWarn checkStatus = "warn"
	checkFail checkStatus = "fail"
	checkSkip checkStatus = "skip"
)

// check is one doctor finding: what was checked, the verdict, what was
// found and, unless it is ok, how to fix it.
type check struct {
	Name   string      `json:"name"`
	Status checkStatus `json:"status"`
	Detail string      `json:"detail"`
	Fix    string      `json:"fix,omitempty"`
}

// doctorReport is doctor's result.
type doctorReport struct {
	Checks []check `json:"checks"`
}

// localhostProbe is the *.localhost name doctor resolves.
const localhostProbe = "understudy-doctor.localhost"

// desktopSharedPaths are the host paths Docker Desktop shares with its VM
// by default; hot-reload directories elsewhere are invisible to functions.
func desktopSharedPaths() []string {
	return []string{"/Users", "/Volumes", "/private", "/tmp", "/var/folders"}
}

func doctorCmd() *command {
	return leaf("doctor", "Check what understudy needs from this machine: Docker, networking, *.localhost, binfmt, hot-reload paths, DynamoDB",
		"doctor [--config FILE]",
		[]string{"understudy doctor", "understudy doctor --output json"},
		func(fs *flag.FlagSet) action {
			configFile := fs.String("config", "", "configuration file to check (default UNDERSTUDY_CONFIG, else ./understudy.json when it exists)")
			return func(ctx context.Context, r *runner, args []string) error {
				if err := wantArgs(args, 0, "no arguments"); err != nil {
					return err
				}
				var cargs []string
				if *configFile != "" {
					cargs = []string{"--" + config.ConfigFlag, *configFile}
				}
				report := r.doctor(ctx, cargs)
				if err := r.emit(report, func(w io.Writer) error { return writeChecks(w, report.Checks) }); err != nil {
					return err
				}
				for _, c := range report.Checks {
					if c.Status == checkFail {
						return fmt.Errorf("the %s check failed: %s; %s", c.Name, c.Detail, c.Fix)
					}
				}
				return nil
			}
		})
}

func (r *runner) doctor(ctx context.Context, configArgs []string) doctorReport {
	var report doctorReport
	add := func(c check) { report.Checks = append(report.Checks, c) }
	health := r.healthNow(ctx)
	add(serverCheck(health, r.endpointURL()))
	host, cerr := config.LoadHost(configArgs, r.o.Env, r.o.ReadFile)
	switch {
	case cerr != nil:
		add(check{Name: "configuration", Status: checkFail, Detail: cerr.Error(), Fix: "fix the setting it names"})
		host = config.Defaults(config.DetectPlacement(r.o.ReadFile, r.o.Env)).Host()
	case len(host.Warnings) == 0:
		add(check{Name: "configuration", Status: checkOK, Detail: "loads without warnings"})
	}
	for _, w := range host.Warnings {
		add(check{Name: "configuration", Status: checkWarn, Detail: w.Message, Fix: w.Fix})
	}
	client, dockerCheck := dockerReachable(ctx, host.Docker)
	if client != nil {
		defer client.Close()
	}
	add(dockerCheck)
	topo, topoCheck := detectTopology(ctx, client)
	add(topoCheck)
	add(localhostCheck(ctx))
	add(r.binfmtCheck())
	for _, c := range hotReloadChecks(host.HotReload, topo) {
		add(c)
	}
	add(dynamoDBCheck(host.DynamoDB, health))
	return report
}

// healthNow asks the server for its health, waiting at most a second; nil
// when none answers.
func (r *runner) healthNow(ctx context.Context) *admin.Health {
	c, err := r.client()
	if err != nil {
		return nil
	}
	quick, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	h, err := c.Health(quick)
	if err != nil {
		return nil
	}
	return h
}

func serverCheck(h *admin.Health, endpoint string) check {
	switch {
	case h == nil:
		return check{Name: "server", Status: checkWarn, Detail: "no understudy answers at " + endpoint, Fix: "start it with 'understudy serve', or point --endpoint at it"}
	case !h.Ready:
		return check{Name: "server", Status: checkWarn, Detail: "understudy at " + endpoint + " is still starting " + strings.Join(h.Waiting, ", "), Fix: "wait with 'understudy health --wait 30s'"}
	case h.Topology != "":
		return check{Name: "server", Status: checkOK, Detail: "understudy " + h.Version + " is ready at " + endpoint + "; its containers' topology is " + string(h.Topology)}
	default:
		return check{Name: "server", Status: checkOK, Detail: "understudy " + h.Version + " is ready at " + endpoint}
	}
}

// dockerReachable pings the configured Docker Engine; the client is nil
// unless it answered.
func dockerReachable(ctx context.Context, d config.Docker) (*docker.Client, check) {
	socket := ""
	switch d := d.(type) {
	case config.DockerDisabled:
		return nil, check{Name: "docker", Status: checkSkip, Detail: "disabled by configuration; Lambda execution and DynamoDB are off"}
	case config.DockerSocket:
		socket = d.Path
	}
	c, err := docker.New(docker.Options{Socket: socket})
	if err != nil {
		return nil, check{Name: "docker", Status: checkFail, Detail: err.Error(), Fix: "set UNDERSTUDY_DOCKER to unix://<socket>"}
	}
	quick, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := c.Ping(quick); err != nil {
		c.Close()
		return nil, check{
			Name: "docker", Status: checkFail, Detail: err.Error(),
			Fix: "start Docker, or give this user access to " + socket + " (access to the socket is root-equivalent on this host), or set UNDERSTUDY_DOCKER=disabled to run without Lambda execution and DynamoDB",
		}
	}
	info, err := c.Info(quick)
	if err != nil {
		c.Close()
		return nil, check{Name: "docker", Status: checkFail, Detail: err.Error(), Fix: "check the daemon's logs"}
	}
	return c, check{Name: "docker", Status: checkOK, Detail: fmt.Sprintf("Docker %s (%s) at %s; access to the socket is root-equivalent on this host", info.ServerVersion, info.OperatingSystem, socket)}
}

func detectTopology(ctx context.Context, c *docker.Client) (topology.Topology, check) {
	if c == nil {
		return nil, check{Name: "topology", Status: checkSkip, Detail: "needs a reachable Docker"}
	}
	var d topology.Detector
	t, err := d.Detect(ctx, c, topology.Options{})
	if err != nil {
		return nil, check{Name: "topology", Status: checkFail, Detail: err.Error(), Fix: "check that the daemon answers 'docker info'"}
	}
	if _, err := t.Addresses(); err != nil {
		if unsupported, ok := errors.AsType[*topology.UnsupportedError](err); ok {
			return t, check{Name: "topology", Status: checkFail, Detail: t.String() + ": " + unsupported.Reason, Fix: unsupported.Remedy}
		}
		return t, check{Name: "topology", Status: checkFail, Detail: err.Error(), Fix: "check that the daemon answers 'docker info', then run 'understudy doctor' again"}
	}
	return t, check{Name: "topology", Status: checkOK, Detail: t.String()}
}

func localhostCheck(ctx context.Context) check {
	quick, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(quick, localhostProbe)
	loopback := err == nil && len(addrs) > 0
	for _, a := range addrs {
		ip, perr := netip.ParseAddr(a)
		loopback = loopback && perr == nil && ip.IsLoopback()
	}
	if loopback {
		return check{Name: "*.localhost", Status: checkOK, Detail: localhostProbe + " resolves to loopback, so virtual-hosted S3 and function URLs work"}
	}
	return check{
		Name: "*.localhost", Status: checkWarn, Detail: localhostProbe + " does not resolve to loopback here",
		Fix: "use path-style S3 (Go UsePathStyle, JS forcePathStyle, Terraform s3_use_path_style; boto3 and the CLI already do) and the /_understudy/lambda-url/<id>/ path for function URLs",
	}
}

// binfmtCheck reports whether this Linux host can run containers of the
// other architecture, which functions declaring it need.
func (r *runner) binfmtCheck() check {
	if runtime.GOOS != "linux" {
		return check{Name: "binfmt", Status: checkSkip, Detail: "Docker Desktop emulates the other architecture itself"}
	}
	handler, arch := "qemu-aarch64", "arm64"
	if runtime.GOARCH == "arm64" {
		handler, arch = "qemu-x86_64", "x86_64"
	}
	data, err := r.o.ReadFile("/proc/sys/fs/binfmt_misc/" + handler)
	if err == nil && strings.HasPrefix(string(data), "enabled") {
		return check{Name: "binfmt", Status: checkOK, Detail: handler + " is registered, so " + arch + " functions run under emulation"}
	}
	return check{
		Name: "binfmt", Status: checkWarn, Detail: "no " + handler + " handler is registered, so functions declaring " + arch + " fail with an exec format error",
		Fix: "register it with 'docker run --privileged --rm tonistiigi/binfmt --install " + arch + "'",
	}
}

func hotReloadChecks(mappings []config.HotReload, t topology.Topology) []check {
	if len(mappings) == 0 {
		return []check{{Name: "hot reload", Status: checkSkip, Detail: "no lambda.hotReload mappings are configured"}}
	}
	_, desktop := t.(topology.DockerDesktop)
	checks := make([]check, 0, len(mappings))
	for _, m := range mappings {
		name := "hot reload " + m.Function
		info, err := os.Stat(m.Dir)
		switch {
		case err != nil || !info.IsDir():
			checks = append(checks, check{Name: name, Status: checkFail, Detail: m.Dir + " is not a directory", Fix: "create it or fix lambda.hotReload"})
		case desktop && !underAny(m.Dir, desktopSharedPaths()):
			checks = append(checks, check{
				Name: name, Status: checkWarn, Detail: m.Dir + " is outside Docker Desktop's default shared paths " + strings.Join(desktopSharedPaths(), ", "),
				Fix: "add it under Settings > Resources > File sharing",
			})
		default:
			checks = append(checks, check{Name: name, Status: checkOK, Detail: m.Dir})
		}
	}
	return checks
}

func underAny(dir string, roots []string) bool {
	for _, root := range roots {
		if dir == root || strings.HasPrefix(dir, root+"/") {
			return true
		}
	}
	return false
}

// dynamoDBCheck reports whether DynamoDB Local can run: the server's state
// when one answers, else the configuration's. With Docker up, the server
// disables DynamoDB Local only when its configuration turns DynamoDB off; a
// degraded DynamoDB Local is a start failure the server log explains.
func dynamoDBCheck(m config.DynamoDBMode, h *admin.Health) check {
	if h != nil && h.DynamoDBLocal.Status != "" {
		c := check{Name: "dynamodb", Status: checkOK, Detail: "server reports DynamoDB Local " + string(h.DynamoDBLocal.Status)}
		if h.DynamoDBLocal.Reason != "" {
			c.Detail += ": " + h.DynamoDBLocal.Reason
		}
		switch {
		case h.DynamoDBLocal.Status == admin.Enabled:
		case h.Docker.Status != admin.Enabled:
			// DynamoDB Local is down because Docker is.
			c.Status, c.Fix = checkWarn, "start Docker, or set UNDERSTUDY_DOCKER to its unix:// socket, and restart understudy"
		default:
			c.Status = checkSkip
		}
		return c
	}
	if _, off := m.(config.DynamoDBDisabled); off {
		return check{Name: "dynamodb", Status: checkSkip, Detail: "DynamoDB is disabled by configuration"}
	}
	return check{Name: "dynamodb", Status: checkOK, Detail: "DynamoDB Local is pulled and started on first use"}
}

func writeChecks(w io.Writer, checks []check) error {
	tw := newTable(w)
	for _, c := range checks {
		fmt.Fprintf(tw, "[%s]\t%s\t%s\n", strings.ToUpper(string(c.Status)), c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(tw, "\t\tfix: %s\n", c.Fix)
		}
	}
	return tw.Flush()
}
