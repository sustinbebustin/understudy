package server

import (
	"context"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/egress"
)

// snsHTTPAllow names SNS HTTP delivery's allow-list, sns.httpDelivery.allow.
func snsHTTPAllow() egress.Setting { return egress.Setting{Service: "sns", Feature: "httpDelivery"} }

// snsEgress is the client SNS HTTP deliveries go out through: the
// configured allow-list, none by default, and CA bundle, a deadline on the
// emulator clock's wall time, and own's addresses refused whatever the list
// says (sns-http-delivery.md 7.2-7.4).
func snsEgress(d SNSHTTPDelivery, clk *clock.Clock, own *ownAddrs) (*egress.Client, error) {
	allow := d.Allow
	if allow.Setting() == (egress.Setting{}) {
		// The zero list allows nothing as well; parsed for its setting, a
		// refusal names sns.httpDelivery.allow as the setting to change.
		var err *egress.EntryError
		if allow, err = egress.ParseAllowList(snsHTTPAllow(), nil); err != nil {
			return nil, err
		}
	}
	var roots *x509.CertPool
	if len(d.CABundle) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("server: reading the system root certificates, beside which sns.httpDelivery.caBundle's certificates verify https subscription endpoints: %w", err)
		}
		for _, c := range d.CABundle {
			pool.AddCert(c)
		}
		roots = pool
	}
	c, err := egress.New(egress.Options{Allow: allow, Clock: clk, RootCAs: roots, Listeners: own})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ownAddrs are the addresses of understudy's own listeners and of the
// Docker Engine it drives, which egress reads at each delivery's check so
// none is ever delivered to (sns-http-delivery.md 7.3): the main and TLS
// listeners and a tcp:// Engine once Serve starts, the container listener
// once a function environment opens it, and each Runtime API listener open
// now. It also reports the TLS listener's port, which SNS bases an HTTP
// delivery's links on (8.3.3).
type ownAddrs struct {
	// runtime returns the open Runtime API listeners' addresses.
	runtime func() []netip.AddrPort
	// interfaces lists this host's interface addresses (net.InterfaceAddrs),
	// for a listener bound to an unspecified address.
	interfaces func() ([]net.Addr, error)

	mu    sync.Mutex
	fixed []netip.AddrPort
	// tlsPort is the TLS listener's port; 0 while there is none.
	tlsPort uint16
}

var _ egress.Listeners = (*ownAddrs)(nil)

// TLSPort reports the TLS listener's port once Serve binds it; it is
// sns.Options.TLSPort.
func (o *ownAddrs) TLSPort() (uint16, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.tlsPort, o.tlsPort != 0
}

// ListenerAddrs returns every address recorded and each open Runtime API
// listener's. A listener bound to an unspecified address is listed once per
// interface address it accepts on: those of its family, or every family
// for [::], which accepts IPv4 too. Only the loopback addresses 127.0.0.1
// and ::1 are listed when the interfaces cannot be read. Another 127/8
// address also reaches such a listener, but only an entry naming that
// address as an IP literal lists it.
func (o *ownAddrs) ListenerAddrs() []netip.AddrPort {
	o.mu.Lock()
	addrs := slices.Clone(o.fixed)
	o.mu.Unlock()
	if o.runtime != nil {
		addrs = append(addrs, o.runtime()...)
	}
	out := make([]netip.AddrPort, 0, len(addrs))
	var local []netip.Addr
	for _, a := range addrs {
		if !a.Addr().IsUnspecified() {
			out = append(out, a)
			continue
		}
		if local == nil {
			local = o.localAddrs()
		}
		for _, l := range local {
			if a.Addr().Is4() && !l.Is4() {
				continue
			}
			out = append(out, netip.AddrPortFrom(l, a.Port()))
		}
	}
	return out
}

// add records a listener's bound address, or the Docker Engine's.
func (o *ownAddrs) add(a netip.AddrPort) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.fixed = append(o.fixed, netip.AddrPortFrom(a.Addr().Unmap(), a.Port()))
}

// addListener records the bound address of ln.
func (o *ownAddrs) addListener(ln net.Listener) error {
	a, err := netip.ParseAddrPort(ln.Addr().String())
	if err != nil {
		return fmt.Errorf("server: reading the listener's address %s, which SNS HTTP delivery must refuse: %w", ln.Addr(), err)
	}
	o.add(a)
	return nil
}

// addTLS records the TLS listener ln, whose port SNS's links then name.
func (o *ownAddrs) addTLS(ln net.Listener) error {
	a, err := netip.ParseAddrPort(ln.Addr().String())
	if err != nil {
		return fmt.Errorf("server: reading the TLS listener's address %s, which SNS HTTP delivery must refuse and its links name: %w", ln.Addr(), err)
	}
	o.add(a)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.tlsPort = a.Port()
	return nil
}

// addDocker records the addresses a tcp:// Docker Engine listens at, its
// host resolved now; a unix socket, or Docker disabled (nil), has none. A
// host that does not resolve is logged: its addresses cannot be refused
// then, and the Engine is not reached by that name either.
func (o *ownAddrs) addDocker(ctx context.Context, e docker.Endpoint, log *slog.Logger) {
	if e == nil {
		return
	}
	hostPort, ok := e.TCPAddr()
	if !ok {
		return
	}
	if a, err := netip.ParseAddrPort(hostPort); err == nil {
		o.add(a)
		return
	}
	host, portText, err := net.SplitHostPort(hostPort)
	port, perr := strconv.ParseUint(portText, 10, 16)
	if err != nil || perr != nil {
		log.LogAttrs(ctx, slog.LevelWarn, "the Docker Engine's address is not host:port, so SNS HTTP delivery cannot refuse it", slog.String("docker", e.String()))
		return
	}
	resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		log.LogAttrs(ctx, slog.LevelWarn, "the Docker Engine's host did not resolve, so SNS HTTP delivery cannot refuse its addresses",
			slog.String("docker", e.String()), slog.String("error", err.Error()))
		return
	}
	for _, a := range resolved {
		o.add(netip.AddrPortFrom(a, uint16(port)))
	}
}

// localAddrs returns this host's interface addresses, unmapped, or the
// loopback addresses when they cannot be read.
func (o *ownAddrs) localAddrs() []netip.Addr {
	loopback := []netip.Addr{netip.AddrFrom4([4]byte{127, 0, 0, 1}), netip.IPv6Loopback()}
	if o.interfaces == nil {
		return loopback
	}
	ifaces, err := o.interfaces()
	if err != nil {
		return loopback
	}
	out := make([]netip.Addr, 0, len(ifaces))
	for _, ia := range ifaces {
		n, ok := ia.(*net.IPNet)
		if !ok {
			continue
		}
		if a, ok := netip.AddrFromSlice(n.IP); ok {
			out = append(out, a.Unmap())
		}
	}
	return out
}
