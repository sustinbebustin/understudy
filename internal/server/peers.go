package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"

	"github.com/sustinbebustin/understudy/internal/topology"
)

// NoBridgeInterfaceError is a container listener address that no interface
// of this host holds, so the container network it serves is unknown.
type NoBridgeInterfaceError struct {
	Addr netip.Addr
	// Err is why the interfaces could not be read; nil when none holds
	// Addr.
	Err error
}

func (e *NoBridgeInterfaceError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("server: reading this host's interfaces to find the container network of %s: %v; Lambda functions cannot run until the container listener opens", e.Addr, e.Err)
	}
	return fmt.Sprintf("server: no interface of this host holds %s, the bridge gateway function containers reach understudy at, so the container listener cannot open; "+
		"check that Docker's bridge is up (ip addr), or set lambda.network to bridge with the gateway it has", e.Addr)
}

func (e *NoBridgeInterfaceError) Unwrap() error { return e.Err }

// bridgeSubnet is the network of the host interface that holds gateway,
// the container network the gateway's listeners serve. interfaces lists
// the host's interface addresses (net.InterfaceAddrs).
func bridgeSubnet(gateway netip.Addr, interfaces func() ([]net.Addr, error)) (netip.Prefix, error) {
	addrs, err := interfaces()
	if err != nil {
		return netip.Prefix{}, &NoBridgeInterfaceError{Addr: gateway, Err: err}
	}
	subnet, ok := topology.InterfacePrefix(addrs, gateway)
	if !ok {
		return netip.Prefix{}, &NoBridgeInterfaceError{Addr: gateway}
	}
	return subnet, nil
}

// RemoteRouteError is a remote daemon outside the network of the interface
// understudy reaches it from. Its function containers connect from the
// daemon's address, which the container listener, scoped to that network,
// would refuse.
type RemoteRouteError struct {
	Daemon netip.Addr
	Self   netip.Addr
	Subnet netip.Prefix
}

func (e *RemoteRouteError) Error() string {
	return fmt.Sprintf("server: the Docker daemon at %s is not on %s, the network understudy reaches it from at %s, so function containers would connect from outside the network the container listener serves and Lambda functions cannot run; "+
		"run understudy on the daemon's network, such as a CI job container beside its docker:dind service, or on the daemon's host with a unix:// socket", e.Daemon, e.Subnet, e.Self)
}

// containerPeers is the network the container listener and the Runtime
// API listeners on bind serve: the network of the interface holding bind,
// which for a remote daemon must hold the daemon too.
func containerPeers(top topology.Topology, bind netip.Addr, interfaces func() ([]net.Addr, error)) (netip.Prefix, error) {
	subnet, err := bridgeSubnet(bind, interfaces)
	if err != nil {
		return netip.Prefix{}, err
	}
	if remote, ok := top.(topology.Remote); ok && !subnet.Contains(remote.Daemon) {
		return netip.Prefix{}, &RemoteRouteError{Daemon: remote.Daemon, Self: remote.Self, Subnet: subnet}
	}
	return subnet, nil
}

// containerListen opens the container listener on addr, scoped to the
// network of the host interface that holds addr.
func containerListen(ctx context.Context, addr netip.AddrPort, interfaces func() ([]net.Addr, error), log *slog.Logger) (net.Listener, error) {
	subnet, err := bridgeSubnet(addr.Addr(), interfaces)
	if err != nil {
		return nil, err
	}
	ln, err := listen(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("server: cannot open the container listener on %s, which function containers reach understudy at: %w", addr, err)
	}
	log.LogAttrs(ctx, slog.LevelInfo, "opened the container listener", slog.String("addr", ln.Addr().String()), slog.String("peers", subnet.String()))
	return topology.ScopePeers(ln, subnet, "container listener", log), nil
}
