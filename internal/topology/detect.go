package topology

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/containermarker"
	"github.com/sustinbebustin/understudy/internal/docker"
)

// Options configures Detect.
type Options struct {
	// Root is the filesystem the in-container markers are read from; nil
	// means the host's root. Tests substitute an fstest.MapFS.
	Root fs.FS
	// Dial opens the connection that shows the route to a tcp:// daemon;
	// nil means a net.Dialer. Tests substitute one whose connection runs
	// between addresses of their choosing.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// routeTimeout bounds the connection that shows the route to the daemon.
const routeTimeout = 5 * time.Second

// DesktopHostAddr is the address host.docker.internal resolves to in Docker
// Desktop's VM (containers.md 8.1), which functions are handed on Desktop.
// It is Desktop's documented address, not probed: ADR 0011 (amendment
// 2026-11-06).
func DesktopHostAddr() netip.Addr { return netip.AddrFrom4([4]byte{192, 168, 65, 254}) }

// Detector detects topologies. The zero value is ready to use.
type Detector struct{}

// Detect works out the topology at first Lambda use, following
// containers.md 8.1 to 8.3: understudy in a container (markers plus a self
// inspect), then a tcp:// daemon on another host (Remote, by the route to
// it), then Podman (the Libpod-Api-Version header), Docker Desktop
// (Info.OperatingSystem), rootless Docker (Info.SecurityOptions), and
// otherwise a Linux bridge at the default bridge network's gateway. Markers
// without a matching container on the daemon (Docker-in-Docker, where the
// daemon runs inside understudy's own container) fall through to the
// engine checks.
func (d *Detector) Detect(ctx context.Context, c *docker.Client, opts Options) (Topology, error) {
	ping, err := c.Ping(ctx)
	if err != nil {
		return nil, fmt.Errorf("detect container topology: %w", err)
	}
	info, err := c.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("detect container topology: %w", err)
	}
	root := opts.Root
	if root == nil {
		root = os.DirFS("/")
	}
	if RunsInContainer(root) {
		t, found, err := detectSelf(ctx, c, root)
		if err != nil || found {
			return t, err
		}
	}
	if remote, ok, err := detectRemote(ctx, c.Endpoint(), opts.Dial); err != nil || ok {
		return remote, err
	}
	switch ping.Engine.(type) {
	case docker.Podman:
		gw, err := podmanGateway(ctx, c)
		if err != nil {
			return nil, err
		}
		return Podman{Rootless: info.Rootless(), Gateway: gw}, nil
	case docker.Moby:
	}
	if strings.Contains(info.OperatingSystem, "Docker Desktop") {
		return DockerDesktop{}, nil
	}
	gw, err := gateway(ctx, c, "bridge")
	if err != nil {
		return nil, err
	}
	if info.Rootless() {
		return RootlessDocker{HostAddr: gw}, nil
	}
	return LinuxBridge{Gateway: gw}, nil
}

// detectRemote finds a tcp:// daemon on another host by the ends of a
// connection to it: a daemon at a loopback address, or at the address the
// connection leaves from, is on this host. ok is false for a daemon on
// this host and for a unix socket.
func detectRemote(ctx context.Context, endpoint docker.Endpoint, dial func(context.Context, string, string) (net.Conn, error)) (t Topology, ok bool, err error) {
	addr, onTCP := endpoint.TCPAddr()
	if !onTCP {
		return nil, false, nil
	}
	if dial == nil {
		dial = (&net.Dialer{Timeout: routeTimeout}).DialContext
	}
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil, false, fmt.Errorf("detect container topology: connect to the daemon at %s to find the route to it: %w", addr, err)
	}
	local, daemon := hostOf(conn.LocalAddr()), hostOf(conn.RemoteAddr())
	if err := conn.Close(); err != nil {
		return nil, false, fmt.Errorf("detect container topology: close the connection to the daemon at %s: %w", addr, err)
	}
	if !local.IsValid() || !daemon.IsValid() {
		return nil, false, fmt.Errorf("detect container topology: the connection to the daemon at %s runs from %v to %v, not between IP addresses", addr, conn.LocalAddr(), conn.RemoteAddr())
	}
	if daemon.IsLoopback() || daemon == local {
		return nil, false, nil
	}
	return Remote{Daemon: daemon, Self: local}, true, nil
}

// hostOf is the IP address of a TCP address, IPv4-mapped addresses
// unmapped; invalid for any other address.
func hostOf(a net.Addr) netip.Addr {
	tcp, ok := a.(*net.TCPAddr)
	if !ok {
		return netip.Addr{}
	}
	return tcp.AddrPort().Addr().Unmap()
}

// RunsInContainer reports whether the filesystem root (os.DirFS("/") for
// this process) is a container's, by the markers Docker and Podman leave:
// /.dockerenv, /run/.containerenv, or a container runtime named in
// /proc/1/cgroup (containers.md 8.3).
func RunsInContainer(root fs.FS) bool {
	return containermarker.Read(func(path string) ([]byte, error) {
		return fs.ReadFile(root, strings.TrimPrefix(path, "/"))
	}).Any()
}

// detectSelf finds understudy's own container and the network functions
// should join. found is false for a container on the host network, which is
// the host's own namespace, and when the daemon knows no candidate: then
// the daemon runs in understudy's own namespace (Docker-in-Docker) and its
// bridge is reachable as on a host. The daemon also resolves a reference as
// a name or an ID prefix, so a container that fails its candidate's check
// (one named like understudy's custom hostname, say) is skipped.
func detectSelf(ctx context.Context, c *docker.Client, root fs.FS) (t Topology, found bool, err error) {
	for _, cand := range selfCandidates(root) {
		self, err := c.ContainerInspect(ctx, cand.ref())
		if docker.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("detect container topology: inspect own container %s: %w", cand.ref(), err)
		}
		if !cand.is(self) {
			continue
		}
		network, addr, ok := pickNetwork(self.NetworkSettings.Networks)
		if !ok {
			return nil, false, nil
		}
		return InContainer{Network: network, Self: addr}, true, nil
	}
	return nil, false, nil
}

// selfCandidates are the container IDs in the roots of the files the engine
// bind mounts into its containers (/etc/hostname, /etc/hosts and
// /etc/resolv.conf, from .../containers/<id>/), followed by the hostname,
// which is the short ID unless --hostname was given. Other mounts are
// ignored: under Docker-in-Docker the inner daemon's own
// containers/<id>/mounts/shm mounts would name its containers, not this one.
func selfCandidates(root fs.FS) []selfCandidate {
	var out []selfCandidate
	if mounts, err := fs.ReadFile(root, "proc/self/mountinfo"); err == nil {
		for line := range strings.Lines(string(mounts)) {
			// Fields: mount ID, parent ID, major:minor, root, mount point, ...
			fields := strings.Fields(line)
			if len(fields) < 5 || !slices.Contains([]string{"/etc/hostname", "/etc/hosts", "/etc/resolv.conf"}, fields[4]) {
				continue
			}
			if id, ok := containerIDInPath(fields[3]); ok && !slices.Contains(out, selfCandidate(mountedID(id))) {
				out = append(out, mountedID(id))
			}
		}
	}
	if host, err := fs.ReadFile(root, "etc/hostname"); err == nil {
		if h := strings.TrimSpace(string(host)); h != "" {
			out = append(out, hostname(h))
		}
	}
	return out
}

// selfCandidate is a reference to understudy's own container and the check
// that the container the daemon resolves it to is that one.
type selfCandidate interface {
	ref() string
	is(self docker.Container) bool
}

// mountedID is the full container ID read from mountinfo.
type mountedID string

func (id mountedID) ref() string                   { return string(id) }
func (id mountedID) is(self docker.Container) bool { return self.ID == string(id) }

// hostname is the contents of /etc/hostname: the short ID by default, or
// the --hostname value, which the engine reports as Config.Hostname either
// way.
type hostname string

func (h hostname) ref() string                   { return string(h) }
func (h hostname) is(self docker.Container) bool { return self.Config.Hostname == string(h) }

// containerIDInPath extracts <id> from a path containing the segments
// containers/<64 hex>/ (Docker: /var/lib/docker/containers/<id>/hostname) or
// overlay-containers/<64 hex>/ (Podman:
// <runroot>/overlay-containers/<id>/userdata/hostname). Every segment is
// checked, since a Podman path holds an earlier containers/ segment that is
// not followed by an ID.
func containerIDInPath(path string) (string, bool) {
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if seg != "containers" && seg != "overlay-containers" || i+2 >= len(segs) {
			continue
		}
		if id := segs[i+1]; len(id) == 64 && strings.Trim(id, "0123456789abcdef") == "" {
			return id, true
		}
	}
	return "", false
}

// pickNetwork prefers a user-defined network (sorted by name) over the
// default bridge; host and none carry no address of their own.
func pickNetwork(networks map[string]docker.EndpointSettings) (name string, addr netip.Addr, ok bool) {
	var fallback string
	var fallbackAddr netip.Addr
	for _, n := range slices.Sorted(maps.Keys(networks)) {
		if n == "host" || n == "none" {
			continue
		}
		a, err := netip.ParseAddr(networks[n].IPAddress)
		if err != nil || !a.Is4() {
			continue
		}
		if n != "bridge" {
			return n, a, true
		}
		fallback, fallbackAddr = n, a
	}
	return fallback, fallbackAddr, fallback != ""
}

// gateway returns a network's first IPv4 gateway.
func gateway(ctx context.Context, c *docker.Client, network string) (netip.Addr, error) {
	n, err := c.NetworkInspect(ctx, network)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("detect container topology: inspect network %s: %w", network, err)
	}
	for _, cfg := range n.IPAM.Config {
		if a, err := netip.ParseAddr(cfg.Gateway); err == nil && a.Is4() {
			return a, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("detect container topology: network %s reports no IPv4 gateway (IPAM %+v); containers need an IPv4 path to understudy", network, n.IPAM.Config)
}

// podmanGateway reads Podman's default network, which its compatibility
// layer may serve as "bridge" or only under its own name "podman".
func podmanGateway(ctx context.Context, c *docker.Client) (netip.Addr, error) {
	gw, err := gateway(ctx, c, "bridge")
	if docker.IsNotFound(err) {
		return gateway(ctx, c, "podman")
	}
	return gw, err
}
