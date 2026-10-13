package topology

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
)

// ScopePeers wraps ln so it serves only peers inside allowed, closing and
// logging every other connection; name says which listener in the log.
//
// The listeners function containers reach bind the bridge gateway, and
// Linux accepts packets for a local address on any interface (the weak
// host model), so without this check a machine on the LAN that routes the
// bridge subnet through this host would reach them. Such a peer's source
// address is outside the subnet; a spoofed one inside it never completes
// the handshake, since the replies go to the bridge.
func ScopePeers(ln net.Listener, allowed netip.Prefix, name string, log *slog.Logger) net.Listener {
	return peerScoped{Listener: ln, allowed: allowed, name: name, log: log}
}

type peerScoped struct {
	net.Listener
	allowed netip.Prefix
	name    string
	log     *slog.Logger
}

// Accept returns the next connection from inside the allowed network,
// closing every other one.
func (l peerScoped) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		peer, err := netip.ParseAddrPort(conn.RemoteAddr().String())
		if err == nil && l.allowed.Contains(peer.Addr().Unmap()) {
			return conn, nil
		}
		attrs := []slog.Attr{
			slog.String("peer", conn.RemoteAddr().String()),
			slog.String("listener", l.name),
			slog.String("addr", l.Addr().String()),
			slog.String("allowed", l.allowed.String()),
		}
		if cerr := conn.Close(); cerr != nil {
			attrs = append(attrs, slog.String("close_error", cerr.Error()))
		}
		l.log.LogAttrs(context.Background(), slog.LevelWarn,
			"refused a connection from outside the container network; only function containers on the bridge may use this listener", attrs...)
	}
}

// InterfacePrefix is the subnet of the host interface that holds addr:
// for the bridge gateway, the bridge network function containers are on.
// addrs lists the host's interface addresses (net.InterfaceAddrs).
func InterfacePrefix(addrs []net.Addr, addr netip.Addr) (netip.Prefix, bool) {
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipnet.IP)
		if !ok || ip.Unmap() != addr.Unmap() {
			continue
		}
		ones, bits := ipnet.Mask.Size()
		if bits == 0 {
			continue
		}
		if ip.Is4In6() {
			ip, ones = ip.Unmap(), ones-(bits-32)
		}
		return netip.PrefixFrom(ip, ones).Masked(), true
	}
	return netip.Prefix{}, false
}
