package topology_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
	"github.com/sustinbebustin/understudy/internal/topology"
)

const probeImage = "public.ecr.aws/lambda/provided:al2023"

// onHost is a filesystem without container markers.
func onHost() fs.FS { return fstest.MapFS{"etc/hostname": {Data: []byte("devbox\n")}} }

func probeLabels() docker.Labels {
	return docker.Labels{Ownership: docker.Ownership{Instance: "i", Owner: "o"}}
}

// ahostsv4 is what `getent ahostsv4 host.docker.internal` prints when the
// name resolves to addr: one line per socket type, the name on the first.
func ahostsv4(addr string) string {
	return addr + "  STREAM host.docker.internal\n" + addr + "  DGRAM  \n" + addr + "  RAW    \n"
}

// desktopProbing scripts getent in the probe image to print stdout.
func desktopProbing(t *testing.T, stdout string) *dockertest.Daemon {
	t.Helper()
	d := dockertest.New(t, dockertest.Options{Info: docker.Info{OperatingSystem: "Docker Desktop"}})
	d.AddImage(probeImage, docker.Image{})
	d.SetProgram(probeImage, dockertest.Exit{Output: []docker.Frame{{Stream: docker.Stdout, Payload: []byte(stdout)}}})
	return d
}

// probesStarted counts the probe containers created so far.
func probesStarted(d *dockertest.Daemon) int {
	n := 0
	for _, e := range d.Events() {
		if e.Type == "container" && e.Action == "create" {
			n++
		}
	}
	return n
}

// startSelf runs understudy's own container on network and returns its ID
// and address.
func startSelf(t *testing.T, d *dockertest.Daemon, c *docker.Client, network, hostname string) (string, netip.Addr) {
	t.Helper()
	ctx := t.Context()
	d.AddImage("understudy:dev", docker.Image{})
	if network != "bridge" && network != "host" {
		if _, err := c.NetworkCreate(ctx, network, docker.NetworkOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	created, err := c.ContainerCreate(ctx, "understudy", docker.Config{Image: "understudy:dev", Hostname: hostname}, docker.HostConfig{NetworkMode: network}, docker.NetworkingConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerStart(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	self, err := c.ContainerInspect(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := netip.ParseAddr(self.NetworkSettings.Networks[network].IPAddress)
	if err != nil && network != "host" {
		t.Fatalf("own address on %s: %v", network, err)
	}
	return created.ID, addr
}

func TestDetect(t *testing.T) {
	rootless := []string{"name=seccomp,profile=builtin", "name=rootless", "name=cgroupns"}
	podman := docker.Podman{LibpodAPIVersion: "5.4.0"}
	cases := []struct {
		name  string
		opts  dockertest.Options
		setup func(t *testing.T, d *dockertest.Daemon, c *docker.Client) (fs.FS, topology.Topology)
	}{
		{
			name: "linux bridge at the default bridge gateway",
			setup: func(*testing.T, *dockertest.Daemon, *docker.Client) (fs.FS, topology.Topology) {
				return onHost(), topology.LinuxBridge{Gateway: netip.MustParseAddr("172.17.0.1")}
			},
		},
		{
			name: "linux bridge on a configured bridge subnet",
			opts: dockertest.Options{BridgeSubnet: netip.MustParsePrefix("10.200.0.0/24")},
			setup: func(*testing.T, *dockertest.Daemon, *docker.Client) (fs.FS, topology.Topology) {
				return onHost(), topology.LinuxBridge{Gateway: netip.MustParseAddr("10.200.0.1")}
			},
		},
		{
			name: "docker desktop with the probed host address",
			opts: dockertest.Options{Info: docker.Info{OperatingSystem: "Docker Desktop"}},
			setup: func(_ *testing.T, d *dockertest.Daemon, _ *docker.Client) (fs.FS, topology.Topology) {
				d.AddImage(probeImage, docker.Image{})
				d.SetProgram(probeImage, dockertest.Exit{Output: []docker.Frame{{Stream: docker.Stdout, Payload: []byte(ahostsv4("192.168.65.2"))}}})
				return onHost(), topology.DockerDesktop{Host: topology.ProbedHost{Addr: netip.MustParseAddr("192.168.65.2")}}
			},
		},
		{
			name: "rootless docker from the security options",
			opts: dockertest.Options{Info: docker.Info{SecurityOptions: rootless}},
			setup: func(*testing.T, *dockertest.Daemon, *docker.Client) (fs.FS, topology.Topology) {
				return onHost(), topology.RootlessDocker{HostAddr: netip.MustParseAddr("172.17.0.1")}
			},
		},
		{
			name: "rootful podman from the libpod header",
			opts: dockertest.Options{APIVersion: "1.44", Engine: podman, BridgeSubnet: netip.MustParsePrefix("10.88.0.0/16")},
			setup: func(*testing.T, *dockertest.Daemon, *docker.Client) (fs.FS, topology.Topology) {
				return onHost(), topology.Podman{Gateway: netip.MustParseAddr("10.88.0.1")}
			},
		},
		{
			name: "rootful podman serving its default network only as podman",
			opts: dockertest.Options{APIVersion: "1.44", Engine: podman, DefaultNetwork: "podman", BridgeSubnet: netip.MustParsePrefix("10.89.0.0/16")},
			setup: func(*testing.T, *dockertest.Daemon, *docker.Client) (fs.FS, topology.Topology) {
				return onHost(), topology.Podman{Gateway: netip.MustParseAddr("10.89.0.1")}
			},
		},
		{
			name: "rootless podman",
			opts: dockertest.Options{APIVersion: "1.44", Engine: podman, Info: docker.Info{SecurityOptions: rootless}},
			setup: func(*testing.T, *dockertest.Daemon, *docker.Client) (fs.FS, topology.Topology) {
				return onHost(), topology.Podman{Rootless: true, Gateway: netip.MustParseAddr("172.17.0.1")}
			},
		},
		{
			name: "in a container found by its hostname",
			opts: dockertest.Options{Info: docker.Info{OperatingSystem: "Docker Desktop"}},
			setup: func(t *testing.T, d *dockertest.Daemon, c *docker.Client) (fs.FS, topology.Topology) {
				t.Helper()
				id, addr := startSelf(t, d, c, "understudy-net", "")
				root := fstest.MapFS{".dockerenv": {}, "etc/hostname": {Data: []byte(id[:12] + "\n")}}
				return root, topology.InContainer{Network: "understudy-net", Self: addr}
			},
		},
		{
			name: "in a container with a custom hostname found by its mounts",
			setup: func(t *testing.T, d *dockertest.Daemon, c *docker.Client) (fs.FS, topology.Topology) {
				t.Helper()
				id, addr := startSelf(t, d, c, "bridge", "emulator")
				mountinfo := "1 2 0:1 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw - ext4 /dev/sda1 rw\n"
				root := fstest.MapFS{
					"proc/1/cgroup":       {Data: []byte("0::/system.slice/docker-" + id + ".scope\n")},
					"proc/self/mountinfo": {Data: []byte(mountinfo)},
					"etc/hostname":        {Data: []byte("emulator\n")},
				}
				return root, topology.InContainer{Network: "bridge", Self: addr}
			},
		},
		{
			name: "in a podman container with a custom hostname found by its mounts",
			opts: dockertest.Options{APIVersion: "1.44", Engine: podman, BridgeSubnet: netip.MustParsePrefix("10.88.0.0/16")},
			setup: func(t *testing.T, d *dockertest.Daemon, c *docker.Client) (fs.FS, topology.Topology) {
				t.Helper()
				id, addr := startSelf(t, d, c, "bridge", "emulator")
				mountinfo := "1 2 0:1 /run/containers/storage/overlay-containers/" + id + "/userdata/hostname /etc/hostname rw - tmpfs rw\n"
				root := fstest.MapFS{
					"run/.containerenv":   {},
					"proc/self/mountinfo": {Data: []byte(mountinfo)},
					"etc/hostname":        {Data: []byte("emulator\n")},
				}
				return root, topology.InContainer{Network: "bridge", Self: addr}
			},
		},
		{
			// The daemon resolves "redis" to the container named redis, which
			// is not understudy: its hostname is its short ID.
			name: "a container named like understudy's custom hostname is not understudy",
			setup: func(t *testing.T, d *dockertest.Daemon, c *docker.Client) (fs.FS, topology.Topology) {
				t.Helper()
				startSelf(t, d, c, "understudy-net", "redis")
				other, err := c.ContainerCreate(t.Context(), "redis", docker.Config{Image: "understudy:dev"}, docker.HostConfig{}, docker.NetworkingConfig{})
				if err != nil {
					t.Fatal(err)
				}
				if err := c.ContainerStart(t.Context(), other.ID); err != nil {
					t.Fatal(err)
				}
				root := fstest.MapFS{".dockerenv": {}, "etc/hostname": {Data: []byte("redis\n")}}
				return root, topology.LinuxBridge{Gateway: netip.MustParseAddr("172.17.0.1")}
			},
		},
		{
			name: "in a container on the host network is the host",
			setup: func(t *testing.T, d *dockertest.Daemon, c *docker.Client) (fs.FS, topology.Topology) {
				t.Helper()
				id, _ := startSelf(t, d, c, "host", "")
				root := fstest.MapFS{".dockerenv": {}, "etc/hostname": {Data: []byte(id[:12])}}
				return root, topology.LinuxBridge{Gateway: netip.MustParseAddr("172.17.0.1")}
			},
		},
		{
			name: "docker-in-docker ignores the inner daemon's container mounts",
			setup: func(t *testing.T, d *dockertest.Daemon, c *docker.Client) (fs.FS, topology.Topology) {
				t.Helper()
				inner, _ := startSelf(t, d, c, "bridge", "")
				outer := strings.Repeat("f", 64)
				mountinfo := "1 2 0:1 /var/lib/docker/containers/" + outer + "/hostname /etc/hostname rw - ext4 /dev/sda1 rw\n" +
					"3 1 0:60 / /var/lib/docker/containers/" + inner + "/mounts/shm rw,nosuid - tmpfs shm rw,size=65536k\n"
				root := fstest.MapFS{
					".dockerenv":          {},
					"proc/self/mountinfo": {Data: []byte(mountinfo)},
					"etc/hostname":        {Data: []byte(outer[:12] + "\n")},
				}
				return root, topology.LinuxBridge{Gateway: netip.MustParseAddr("172.17.0.1")}
			},
		},
		{
			name: "in a container the daemon does not know is docker-in-docker on its bridge",
			setup: func(*testing.T, *dockertest.Daemon, *docker.Client) (fs.FS, topology.Topology) {
				root := fstest.MapFS{".dockerenv": {}, "etc/hostname": {Data: []byte("0123456789ab\n")}}
				return root, topology.LinuxBridge{Gateway: netip.MustParseAddr("172.17.0.1")}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := dockertest.New(t, tc.opts)
			c := d.Client(t)
			root, want := tc.setup(t, d, c)
			got, err := new(topology.Detector).Detect(t.Context(), c, topology.Options{ProbeImage: probeImage, ProbeLabels: probeLabels(), Root: root})
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if got != want {
				t.Errorf("Detect = %v, want %v", got, want)
			}
		})
	}
}

func TestDetectDesktopProbeRunsALabeledContainerAndRemovesIt(t *testing.T) {
	d := desktopProbing(t, ahostsv4("192.168.65.254"))
	if _, err := new(topology.Detector).Detect(t.Context(), d.Client(t), topology.Options{ProbeImage: probeImage, ProbeLabels: probeLabels(), Root: onHost()}); err != nil {
		t.Fatal(err)
	}
	var created, destroyed int
	for _, e := range d.Events() {
		switch e.Action {
		case "create":
			created++
			if e.Actor.Attributes[docker.LabelOwner] != "o" || e.Actor.Attributes[docker.LabelManaged] != "true" {
				t.Errorf("probe container labels = %v, want the managed probe labels", e.Actor.Attributes)
			}
		case "destroy":
			destroyed++
		}
	}
	if created != 1 || destroyed != 1 {
		t.Errorf("probe containers created %d, destroyed %d; want one of each", created, destroyed)
	}
	// ARCHITECTURE 14.5, ADR 0011: getent replaces the entrypoint, since the
	// base images' entrypoint rejects more than one argument, and its output
	// is read back from a readable log driver.
	probes := d.Created()
	if len(probes) != 1 {
		t.Fatalf("daemon recorded %d created containers, want the one probe", len(probes))
	}
	for _, probe := range probes {
		if !slices.Equal(probe.Config.Entrypoint, []string{"getent", "ahostsv4", "host.docker.internal"}) ||
			len(probe.Config.Cmd) != 0 || probe.HostConfig.LogConfig.Type != "json-file" {
			t.Errorf("probe Entrypoint %q, Cmd %q, log driver %q; want getent ahostsv4 host.docker.internal, no Cmd, json-file",
				probe.Config.Entrypoint, probe.Config.Cmd, probe.HostConfig.LogConfig.Type)
		}
	}
}

// A probe that printed its address succeeded even when its container could
// not be removed: the address is used and remembered, and the leftover is
// reported for doctor rather than turned into a fallback.
func TestDetectDesktopKeepsTheProbedAddressWhenRemovalFails(t *testing.T) {
	d := desktopProbing(t, ahostsv4("192.168.65.3"))
	d.FailNext("DELETE /containers/{id}", 500, "removal failed")
	var detector topology.Detector
	opts := topology.Options{ProbeImage: probeImage, ProbeLabels: probeLabels(), Root: onHost()}
	got, err := detector.Detect(t.Context(), d.Client(t), opts)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	desktop, ok := got.(topology.DockerDesktop)
	probed, isProbed := desktop.Host.(topology.ProbedHost)
	if !ok || !isProbed || probed.Addr != netip.MustParseAddr("192.168.65.3") {
		t.Fatalf("Detect = %v, want Docker Desktop at the probed 192.168.65.3", got)
	}
	if probed.Cleanup == nil {
		t.Error("ProbedHost.Cleanup = nil, want the removal failure")
	}
	again, err := detector.Detect(t.Context(), d.Client(t), opts)
	if err != nil {
		t.Fatalf("second Detect: %v", err)
	}
	if want := (topology.DockerDesktop{Host: topology.ProbedHost{Addr: probed.Addr}}); again != want {
		t.Errorf("second Detect = %v, want the remembered %v", again, want)
	}
	if n := probesStarted(d); n != 1 {
		t.Errorf("probe containers created = %d, want 1: the successful probe is remembered", n)
	}
}

func TestDetectDesktopFallsBackToTheDocumentedAddress(t *testing.T) {
	cases := []struct {
		name    string
		program dockertest.Program
		image   string
	}{
		{name: "probe image absent", image: ""},
		{name: "getent fails", image: probeImage, program: dockertest.Exit{Code: 2}},
		{name: "getent prints nothing usable", image: probeImage, program: dockertest.Exit{Output: []docker.Frame{{Stream: docker.Stdout, Payload: []byte("host.docker.internal\n")}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := dockertest.New(t, dockertest.Options{Info: docker.Info{OperatingSystem: "Docker Desktop"}})
			if tc.image != "" {
				d.AddImage(tc.image, docker.Image{})
				d.SetProgram(tc.image, tc.program)
			}
			got, err := new(topology.Detector).Detect(t.Context(), d.Client(t), topology.Options{ProbeImage: probeImage, ProbeLabels: probeLabels(), Root: onHost()})
			if err != nil {
				t.Fatal(err)
			}
			desktop, ok := got.(topology.DockerDesktop)
			fallback, isFallback := desktop.Host.(topology.FallbackHost)
			if !ok || !isFallback || fallback.Cause == nil || fallback.HostAddr() != topology.DesktopDefaultHostAddr() {
				t.Errorf("Detect = %v, want Docker Desktop at 192.168.65.254 with the probe failure", got)
			}
			if a, err := got.Addresses(); err != nil || a.FunctionEndpointHost != topology.DesktopDefaultHostAddr() {
				t.Errorf("Addresses = %+v, %v; want functions sent to 192.168.65.254", a, err)
			}
		})
	}
}

func TestDetectDesktopProbesOncePerDaemonButRetriesAFailure(t *testing.T) {
	d := desktopProbing(t, "")
	d.SetProgram(probeImage, dockertest.Exit{Code: 2})
	c := d.Client(t)
	var detector topology.Detector
	opts := topology.Options{ProbeImage: probeImage, ProbeLabels: probeLabels(), Root: onHost()}
	detect := func() topology.Topology {
		t.Helper()
		got, err := detector.Detect(t.Context(), c, opts)
		if err != nil {
			t.Fatalf("Detect: %v", err)
		}
		return got
	}

	if got, ok := detect().(topology.DockerDesktop); !ok || got.Host == nil {
		t.Fatalf("Detect = %v, want Docker Desktop", got)
	} else if _, fell := got.Host.(topology.FallbackHost); !fell {
		t.Errorf("Detect with a failing getent = %v, want the fallback", got)
	}
	d.SetProgram(probeImage, dockertest.Exit{Output: []docker.Frame{{Stream: docker.Stdout, Payload: []byte(ahostsv4("192.168.65.3"))}}})
	want := topology.DockerDesktop{Host: topology.ProbedHost{Addr: netip.MustParseAddr("192.168.65.3")}}
	for range 2 {
		if got := detect(); got != want {
			t.Errorf("Detect = %v, want %v", got, want)
		}
	}
	if n := probesStarted(d); n != 2 {
		t.Errorf("probe containers created = %d, want 2: the failed probe is retried, the successful one remembered", n)
	}
}

func TestDetectDesktopRefusesToProbeWithoutOwnershipLabels(t *testing.T) {
	d := desktopProbing(t, ahostsv4("192.168.65.254"))
	_, err := new(topology.Detector).Detect(t.Context(), d.Client(t), topology.Options{ProbeImage: probeImage, Root: onHost()})
	if oe, ok := errors.AsType[*docker.OwnershipError](err); !ok || oe.Ownership != (docker.Ownership{}) {
		t.Errorf("Detect without probe labels = %v, want an *OwnershipError for the empty ownership", err)
	}
	if n := probesStarted(d); n != 0 {
		t.Errorf("probe containers created = %d, want none: a sweep could not find an unowned probe", n)
	}
}

// routedConn is a connection whose ends are the given addresses, as a TCP
// connection to a daemon on another host has.
type routedConn struct {
	net.Conn
	local, remote netip.AddrPort
}

func (c routedConn) LocalAddr() net.Addr  { return net.TCPAddrFromAddrPort(c.local) }
func (c routedConn) RemoteAddr() net.Addr { return net.TCPAddrFromAddrPort(c.remote) }

// routedTo dials connections that appear to run from local to remote.
func routedTo(local, remote string) func(context.Context, string, string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		if err := b.Close(); err != nil {
			return nil, err
		}
		return routedConn{Conn: a, local: netip.MustParseAddrPort(local), remote: netip.MustParseAddrPort(remote)}, nil
	}
}

// A tcp:// daemon on another host is Remote: functions reach understudy at
// the address its route to the daemon leaves from, whatever the daemon's
// engine, and even from a container the daemon does not run (a CI job
// beside a docker:dind service). A daemon reached on loopback, or on an
// address of this host, is detected as on a unix socket.
func TestDetectRemoteDaemons(t *testing.T) {
	dind := fstest.MapFS{".dockerenv": {}, "etc/hostname": {Data: []byte("0123456789ab\n")}}
	cases := []struct {
		name string
		opts dockertest.Options
		dial func(context.Context, string, string) (net.Conn, error)
		root fs.FS
		want topology.Topology
	}{
		{name: "loopback", root: onHost(), want: topology.LinuxBridge{Gateway: netip.MustParseAddr("172.17.0.1")}},
		{name: "this host's own address", dial: routedTo("10.1.0.5:40000", "10.1.0.5:2375"), root: onHost(), want: topology.LinuxBridge{Gateway: netip.MustParseAddr("172.17.0.1")}},
		{
			name: "docker-in-docker service beside a CI job", dial: routedTo("172.30.0.2:40000", "172.30.0.3:2375"), root: dind,
			want: topology.Remote{Daemon: netip.MustParseAddr("172.30.0.3"), Self: netip.MustParseAddr("172.30.0.2")},
		},
		{
			name: "remote Docker Desktop", opts: dockertest.Options{Info: docker.Info{OperatingSystem: "Docker Desktop"}}, dial: routedTo("10.1.0.5:40000", "10.1.0.9:2376"), root: onHost(),
			want: topology.Remote{Daemon: netip.MustParseAddr("10.1.0.9"), Self: netip.MustParseAddr("10.1.0.5")},
		},
	}
	for _, transport := range []dockertest.Transport{dockertest.PlainTCP{}, dockertest.MutualTLS{}} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%T/%s", transport, tc.name), func(t *testing.T) {
				opts := tc.opts
				opts.Transport = transport
				c := dockertest.New(t, opts).Client(t)
				got, err := new(topology.Detector).Detect(t.Context(), c, topology.Options{Root: tc.root, Dial: tc.dial})
				if err != nil {
					t.Fatalf("Detect: %v", err)
				}
				if got != tc.want {
					t.Errorf("Detect = %v, want %v", got, tc.want)
				}
			})
		}
	}
}

func TestDetectPropagatesAnUnreachableDaemon(t *testing.T) {
	c, err := docker.New(docker.Options{Endpoint: docker.Unix{Path: "/nonexistent/understudy/docker.sock"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := new(topology.Detector).Detect(t.Context(), c, topology.Options{Root: onHost()}); !docker.IsUnavailable(err) {
		t.Errorf("Detect = %v, want an unavailable daemon error", err)
	}
}
