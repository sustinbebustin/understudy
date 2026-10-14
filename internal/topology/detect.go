package topology

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// Options configures Detect.
type Options struct {
	// ProbeImage runs the Docker Desktop probe; use the function's own
	// image, which is already present. Empty skips the probe and uses the
	// documented default address.
	ProbeImage string
	// ProbeLabels label the probe container so a sweep removes it if
	// understudy dies mid-probe. With a ProbeImage, the ownership must be
	// set.
	ProbeLabels docker.Labels
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

// DesktopDefaultHostAddr is the address host.docker.internal resolves to in
// Docker Desktop's VM unless configured otherwise; it is used when the probe
// cannot run.
func DesktopDefaultHostAddr() netip.Addr { return netip.AddrFrom4([4]byte{192, 168, 65, 254}) }

// Detector detects topologies and remembers the host address each Docker
// Desktop daemon's probe found, keyed by Info.ID, so the probe container
// runs once per daemon. A failed probe is not remembered: the next Detect,
// perhaps with another image, tries again. Concurrent first detections on
// one daemon may each probe. The zero value is ready to use; a Detector
// must not be copied after first use.
type Detector struct {
	mu      sync.Mutex
	desktop map[string]netip.Addr
}

// Detect works out the topology at first Lambda use, following
// containers.md 8.1 to 8.3: understudy in a container (markers plus a self
// inspect), then Podman (the Libpod-Api-Version header), Docker Desktop
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
		return d.desktopTopology(ctx, c, info.ID, opts)
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
	var addr string
	switch e := endpoint.(type) {
	case docker.TCP:
		addr = e.Addr
	case docker.TLS:
		addr = e.Addr
	case docker.Unix:
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
	for _, marker := range []string{".dockerenv", "run/.containerenv"} {
		if _, err := fs.Stat(root, marker); err == nil {
			return true
		}
	}
	cgroup, err := fs.ReadFile(root, "proc/1/cgroup")
	if err != nil {
		return false
	}
	return slices.ContainsFunc([]string{"docker", "kubepods", "containerd", "libpod"}, func(word string) bool {
		return bytes.Contains(cgroup, []byte(word))
	})
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

// desktopTopology returns daemonID's remembered probe address, or resolves
// host.docker.internal inside Desktop's VM by running getent in
// ProbeImage. A probe that cannot run falls back to the documented address
// and says why in FallbackHost; only a cancelled context or probe labels
// without ownership fail detection.
func (d *Detector) desktopTopology(ctx context.Context, c *docker.Client, daemonID string, opts Options) (Topology, error) {
	d.mu.Lock()
	addr, ok := d.desktop[daemonID]
	d.mu.Unlock()
	if ok {
		return DockerDesktop{Host: ProbedHost{Addr: addr}}, nil
	}
	if opts.ProbeImage == "" {
		return DockerDesktop{Host: FallbackHost{Cause: errors.New("no probe image was given")}}, nil
	}
	labels, err := opts.ProbeLabels.Map()
	if err != nil {
		return nil, fmt.Errorf("detect container topology: Docker Desktop probe refused, because a sweep could not remove a probe container left by a crash: ProbeLabels: %w", err)
	}
	host, err := runProbe(ctx, c, opts.ProbeImage, labels)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("detect container topology: Docker Desktop probe: %w", ctx.Err())
		}
		return DockerDesktop{Host: FallbackHost{Cause: err}}, nil
	}
	d.mu.Lock()
	if d.desktop == nil {
		d.desktop = map[string]netip.Addr{}
	}
	d.desktop[daemonID] = host.Addr
	d.mu.Unlock()
	return DockerDesktop{Host: host}, nil
}

// probeRemoveTimeout bounds removing the probe container.
const probeRemoveTimeout = 10 * time.Second

// runProbe runs getent in a labelled container and removes it. The daemon
// names the container; a sweep finds a leftover by its labels. A removal
// failure fails the probe only when the probe itself failed; after a
// successful probe it is reported in ProbedHost.Cleanup.
func runProbe(ctx context.Context, c *docker.Client, image string, labels map[string]string) (ProbedHost, error) {
	created, err := c.ContainerCreate(ctx, "",
		docker.Config{
			Image: image,
			// The Lambda base images' entrypoint insists on exactly one
			// argument, so the probe replaces it instead of passing Cmd.
			// ahostsv4, not hosts: glibc's hosts prints only the IPv6
			// address of a dual-stack name.
			Entrypoint: []string{"getent", "ahostsv4", "host.docker.internal"},
			Labels:     labels,
		},
		docker.HostConfig{LogConfig: docker.LogConfig{Type: "json-file"}},
		docker.NetworkingConfig{})
	if err != nil {
		return ProbedHost{}, err
	}
	out, err := runToExit(ctx, c, created.ID)
	rmErr := removeProbe(ctx, c, created.ID)
	if err != nil {
		return ProbedHost{}, errors.Join(err, rmErr)
	}
	addr, err := parseGetent(out)
	if err != nil {
		return ProbedHost{}, errors.Join(err, rmErr)
	}
	return ProbedHost{Addr: addr, Cleanup: rmErr}, nil
}

// removeProbe force-removes the probe container. Removal outlives a
// cancelled ctx but not a hung daemon; a probe left behind carries
// ProbeLabels, so a later sweep removes it.
func removeProbe(ctx context.Context, c *docker.Client, id string) error {
	rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeRemoveTimeout)
	defer cancel()
	err := c.ContainerRemove(rmCtx, id, docker.RemoveOptions{Force: true, Volumes: true})
	if err != nil && !docker.IsNotFound(err) {
		return fmt.Errorf("remove probe container %s (it carries the probe labels, so the next sweep removes it): %w", id, err)
	}
	return nil
}

// runToExit starts a created container, waits for it and returns its
// stdout; a non-zero exit is an error.
func runToExit(ctx context.Context, c *docker.Client, id string) (string, error) {
	wait, err := c.ContainerWait(ctx, id, docker.WaitNextExit)
	if err != nil {
		return "", err
	}
	defer wait.Close()
	if err := c.ContainerStart(ctx, id); err != nil {
		return "", err
	}
	code, err := wait.Result()
	if err != nil {
		return "", err
	}
	logs, err := c.ContainerLogs(ctx, id, docker.LogsOptions{})
	if err != nil {
		return "", err
	}
	defer logs.Close()
	var stdout strings.Builder
	for {
		f, err := logs.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if f.Stream == docker.Stdout {
			stdout.Write(f.Payload)
		}
	}
	if code != 0 {
		return "", fmt.Errorf("getent exited %d (output %q)", code, stdout.String())
	}
	return stdout.String(), nil
}

// parseGetent reads the first IPv4 address from `getent ahostsv4` output:
// lines of an address, a socket type and, on the first line of each
// address, the name.
func parseGetent(out string) (netip.Addr, error) {
	for line := range strings.Lines(out) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if a, err := netip.ParseAddr(fields[0]); err == nil && a.Is4() {
			return a, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("getent ahostsv4 host.docker.internal printed no IPv4 address: %q", out)
}
