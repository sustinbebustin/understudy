package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"

	"github.com/sustinbebustin/understudy/internal/edge"
)

// listeners are the HTTP servers of one Serve: the main listener, the TLS
// listener, and the container listener the first function environment
// opens. Shutdown drains them together.
type listeners struct {
	log  *slog.Logger
	base func(net.Listener) context.Context

	mu sync.Mutex
	// closed is set once draining began; a listener opened after that is
	// closed at once.
	closed bool
	open   []listening
	// failed receives the first error of a server that stopped on its own.
	failed chan error
}

// listening is one served listener.
type listening struct {
	srv     *http.Server
	tracked *conns
}

// errShuttingDown is a listener opened, or DynamoDB Local started, after
// shutdown began.
var errShuttingDown = errors.New("server: understudy is shutting down, so no listener or container starts any more")

func newListeners(log *slog.Logger, base func(net.Listener) context.Context) *listeners {
	return &listeners{log: log, base: base, failed: make(chan error, 1)}
}

// serve serves h on ln until drain. name says which listener it is in the
// error of one that fails.
func (l *listeners) serve(name string, h *edge.Handler, ln net.Listener) error {
	srv := h.Server(l.base)
	tracked := newConns(l.log)
	srv.ConnState = tracked.track
	srv.RegisterOnShutdown(tracked.closeUnused)
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return errors.Join(errShuttingDown, ln.Close())
	}
	l.open = append(l.open, listening{srv: srv, tracked: tracked})
	l.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			select {
			case l.failed <- fmt.Errorf("server: serving the %s on %s stopped: %w", name, ln.Addr(), err):
			default:
			}
		}
	}()
	return nil
}

// listen opens a TCP listener on addr, IPv4 only for an IPv4 address, so
// 0.0.0.0 does not also take the IPv6 wildcard.
func listen(ctx context.Context, addr netip.AddrPort) (net.Listener, error) {
	network := "tcp"
	if addr.Addr().Is4() {
		network = "tcp4"
	}
	return (&net.ListenConfig{}).Listen(ctx, network, addr.String())
}

// advertised is the URL a client on this host reaches ln at: the wildcard
// address is advertised as loopback, which it includes.
func advertised(scheme string, ln net.Listener) string {
	addr, err := netip.ParseAddrPort(ln.Addr().String())
	if err != nil || !addr.Addr().IsUnspecified() {
		return scheme + "://" + ln.Addr().String()
	}
	loopback := netip.IPv6Loopback()
	if addr.Addr().Is4() {
		loopback = netip.AddrFrom4([4]byte{127, 0, 0, 1})
	}
	return scheme + "://" + netip.AddrPortFrom(loopback, addr.Port()).String()
}

// drain closes every listener and waits up to shutdownGrace for requests
// in flight, all listeners at once.
func (l *listeners) drain(ctx context.Context) error {
	l.mu.Lock()
	l.closed = true
	open := l.open
	l.mu.Unlock()
	errs := make([]error, len(open))
	var wg sync.WaitGroup
	for i, o := range open {
		wg.Go(func() { errs[i] = drain(ctx, o.srv, o.tracked) })
	}
	wg.Wait()
	return errors.Join(errs...)
}
