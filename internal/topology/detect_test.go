package topology_test

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// onHost is a filesystem without container markers.
func onHost() fs.FS { return fstest.MapFS{"etc/hostname": {Data: []byte("devbox\n")}} }

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
			name: "docker desktop from the operating system",
			opts: dockertest.Options{Info: docker.Info{OperatingSystem: "Docker Desktop"}},
			setup: func(*testing.T, *dockertest.Daemon, *docker.Client) (fs.FS, topology.Topology) {
				return onHost(), topology.DockerDesktop{}
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
			got, err := new(topology.Detector).Detect(t.Context(), c, topology.Options{Root: root})
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if got != want {
				t.Errorf("Detect = %v, want %v", got, want)
			}
		})
	}
}

// Docker Desktop hands functions Desktop's documented address for
// host.docker.internal and runs no container to find it (ADR 0011,
// amendment 2026-11-06).
func TestDetectDesktopHandsFunctionsTheDocumentedAddress(t *testing.T) {
	d := dockertest.New(t, dockertest.Options{Info: docker.Info{OperatingSystem: "Docker Desktop"}})
	got, err := new(topology.Detector).Detect(t.Context(), d.Client(t), topology.Options{Root: onHost()})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got != (topology.DockerDesktop{}) {
		t.Fatalf("Detect = %v, want Docker Desktop", got)
	}
	a, err := got.Addresses()
	if err != nil || a.FunctionEndpointHost != netip.MustParseAddr("192.168.65.254") || a.ListenerBind != netip.MustParseAddr("127.0.0.1") {
		t.Errorf("Addresses = %+v, %v; want functions sent to 192.168.65.254 and listeners on loopback", a, err)
	}
	if want := "Docker Desktop (host 192.168.65.254)"; got.String() != want {
		t.Errorf("String = %q, want %q", got.String(), want)
	}
	if n := len(d.Created()); n != 0 {
		t.Errorf("detection created %d containers, want none", n)
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
