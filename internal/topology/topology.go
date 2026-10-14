package topology

import (
	"errors"
	"fmt"
	"net/netip"
)

// Topology is how containers on the detected daemon reach understudy.
//
//sumtype:decl
type Topology interface {
	// Addresses derives the listener and function addresses, or an
	// *UnsupportedError for a topology phase 1 cannot serve.
	Addresses() (Addresses, error)
	// String names the topology in diagnostics.
	String() string
	isTopology()
}

// Addresses are what the Lambda runtime binds and hands to function
// containers.
type Addresses struct {
	// ListenerBind is the address the container listener and the
	// per-environment Runtime API listeners bind. Never 0.0.0.0.
	ListenerBind netip.Addr
	// FunctionEndpointHost is the IPv4 address functions use, as a literal,
	// for AWS_ENDPOINT_URL and AWS_LAMBDA_RUNTIME_API; a literal keeps S3
	// path-style in every SDK.
	FunctionEndpointHost netip.Addr
	// ExtraHosts are added to every function container. On Linux and Podman
	// localhost:host-gateway makes localhost:<port> URLs minted on the host
	// reachable from functions (ADR 0011).
	ExtraHosts []string
	// NeedsContainerListener reports whether understudy must open an extra
	// listener on ListenerBind at its main port, because the main listener
	// is not reachable from containers.
	NeedsContainerListener bool
	// FunctionNetwork is the network function containers join; empty means
	// the daemon's default bridge.
	FunctionNetwork string
}

func bridgeAddresses(gateway netip.Addr) Addresses {
	return Addresses{
		ListenerBind:           gateway,
		FunctionEndpointHost:   gateway,
		ExtraHosts:             []string{"localhost:host-gateway"},
		NeedsContainerListener: true,
	}
}

// LinuxBridge is native Docker on Linux: containers reach the host at the
// default bridge's gateway, which understudy listens on.
type LinuxBridge struct {
	Gateway netip.Addr
}

// Addresses binds the gateway and hands it to functions.
func (t LinuxBridge) Addresses() (Addresses, error) { return bridgeAddresses(t.Gateway), nil }

func (t LinuxBridge) String() string { return "Linux bridge (gateway " + t.Gateway.String() + ")" }

func (LinuxBridge) isTopology() {}

// DockerDesktop forwards a container's connection to host.docker.internal
// to the host's loopback, so understudy keeps listening on 127.0.0.1. Host
// is what host.docker.internal resolves to inside Desktop's VM; nil means
// no probe ran, so the documented default applies.
type DockerDesktop struct {
	Host DesktopHost
}

// Addresses keeps every listener on loopback and hands functions the host
// address; no extra hosts, because Desktop resolves its own.
func (t DockerDesktop) Addresses() (Addresses, error) {
	return Addresses{
		ListenerBind:         netip.AddrFrom4([4]byte{127, 0, 0, 1}),
		FunctionEndpointHost: t.host().HostAddr(),
	}, nil
}

func (t DockerDesktop) String() string {
	h := t.host()
	if fallback, ok := h.(FallbackHost); ok {
		return fmt.Sprintf("Docker Desktop (host %s, the documented default: probing host.docker.internal failed: %v)", fallback.HostAddr(), fallback.Cause)
	}
	return "Docker Desktop (host " + h.HostAddr().String() + ")"
}

func (DockerDesktop) isTopology() {}

// host returns Host, or a FallbackHost for a nil Host.
func (t DockerDesktop) host() DesktopHost {
	if t.Host == nil {
		return FallbackHost{Cause: errors.New("no probe ran")}
	}
	return t.Host
}

// DesktopHost is where host.docker.internal points inside Docker Desktop's
// VM, and how that was learned.
//
//sumtype:decl
type DesktopHost interface {
	HostAddr() netip.Addr
	isDesktopHost()
}

// ProbedHost is the address getent printed inside Desktop's VM. Cleanup is
// set only by the Detect call that ran the probe, when the probe container
// could not be removed afterwards; the container carries ProbeLabels, so a
// later sweep removes it, and doctor reports it.
type ProbedHost struct {
	Addr    netip.Addr
	Cleanup error
}

// HostAddr returns the probed address.
func (h ProbedHost) HostAddr() netip.Addr { return h.Addr }

func (ProbedHost) isDesktopHost() {}

// FallbackHost is the documented DesktopDefaultHostAddr, used because the
// probe could not run or printed no IPv4 address, for the reason in Cause;
// doctor reports it.
type FallbackHost struct {
	Cause error
}

// HostAddr returns DesktopDefaultHostAddr.
func (FallbackHost) HostAddr() netip.Addr { return DesktopDefaultHostAddr() }

func (FallbackHost) isDesktopHost() {}

// RootlessDocker blocks containers from reaching the host's loopback, and
// host-gateway resolves inside rootlesskit's namespace. HostAddr is that
// in-namespace gateway. Lambda execution is unsupported here in phase 1.
type RootlessDocker struct {
	HostAddr netip.Addr
}

// Addresses always fails with an *UnsupportedError.
func (t RootlessDocker) Addresses() (Addresses, error) {
	return Addresses{}, &UnsupportedError{
		Topology: t,
		Reason:   "rootlesskit blocks containers from reaching the host's loopback (--disable-host-loopback is the default) and host-gateway resolves inside rootlesskit's network namespace, so function containers cannot reach understudy",
		Remedy:   "run understudy against rootful Docker or Docker Desktop",
	}
}

func (t RootlessDocker) String() string {
	return "rootless Docker (namespace gateway " + t.HostAddr.String() + ")"
}

func (RootlessDocker) isTopology() {}

// Podman is Podman's Docker-compatible service. Rootful Podman behaves
// like a Linux bridge at Gateway; rootless Podman is unsupported in phase 1.
type Podman struct {
	Rootless bool
	Gateway  netip.Addr
}

// Addresses binds the gateway for rootful Podman and fails with an
// *UnsupportedError for rootless Podman.
func (t Podman) Addresses() (Addresses, error) {
	if t.Rootless {
		return Addresses{}, &UnsupportedError{
			Topology: t,
			Reason:   "with rootless networking the gateway address is not a path to the host, and host.containers.internal maps to the host's external address, which understudy does not listen on",
			Remedy:   "run understudy against rootful Podman or Docker",
		}
	}
	return bridgeAddresses(t.Gateway), nil
}

func (t Podman) String() string {
	if t.Rootless {
		return "rootless Podman (gateway " + t.Gateway.String() + ")"
	}
	return "Podman (gateway " + t.Gateway.String() + ")"
}

func (Podman) isTopology() {}

// InContainer is understudy itself running in a container on Network at
// address Self; function containers join that network and reach Self
// directly.
type InContainer struct {
	Network string
	Self    netip.Addr
}

// Addresses binds understudy's own container address and puts functions on
// its network. No container listener opens: the main listener already holds
// the main port on every interface inside the container
// (Exposed{ContainerPublished}), so functions call it at Self. Unlike the
// container listener, it does not hold callers to health and clock from the
// admin prefix, and ContainerPublished asks for no admin token, so function
// containers reach the whole admin API here.
func (t InContainer) Addresses() (Addresses, error) {
	return Addresses{
		ListenerBind:         t.Self,
		FunctionEndpointHost: t.Self,
		FunctionNetwork:      t.Network,
	}, nil
}

func (t InContainer) String() string {
	return "in a container on network " + t.Network + " (address " + t.Self.String() + ")"
}

func (InContainer) isTopology() {}

// Remote is a daemon on another host, reached over TCP. Function containers
// leave the daemon's host through its own network, so they reach
// understudy at Self, the address this host's route to Daemon leaves from,
// where understudy opens the container listener.
type Remote struct {
	Daemon netip.Addr
	Self   netip.Addr
}

// Addresses binds Self and hands it to functions, on the daemon's default
// bridge. No extra hosts: host-gateway names the daemon's host, not
// understudy's.
func (t Remote) Addresses() (Addresses, error) {
	if !t.Self.Is4() {
		return Addresses{}, &UnsupportedError{
			Topology: t,
			Reason:   "understudy reaches the daemon over IPv6, and function containers are handed an IPv4 address for understudy",
			Remedy:   "name the daemon by an IPv4 address or a host name that resolves to one in DOCKER_HOST",
		}
	}
	return Addresses{
		ListenerBind:           t.Self,
		FunctionEndpointHost:   t.Self,
		NeedsContainerListener: true,
	}, nil
}

func (t Remote) String() string {
	return "remote daemon at " + t.Daemon.String() + " (understudy at " + t.Self.String() + ")"
}

func (Remote) isTopology() {}

// UnsupportedError reports a topology on which phase 1 cannot run Lambda
// functions.
type UnsupportedError struct {
	Topology Topology
	Reason   string
	Remedy   string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("Lambda execution is not supported on %s in phase 1: %s. The Lambda control plane keeps working, but Invoke fails until you %s; run `understudy doctor` for details",
		e.Topology, e.Reason, e.Remedy)
}
