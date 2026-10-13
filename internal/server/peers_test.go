package server

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The container listener serves a peer inside the network of the
// interface holding its address and closes a connection from any other
// peer without serving it. The peer dials from 127.0.0.2; the interface
// holds 127.0.0.1 in a /8, which contains it, or in a /32, which does not.
func TestContainerListenerServesOnlyItsNetwork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ones    int
		allowed bool
	}{
		{"a peer inside the network", 8, true},
		{"a peer outside it", 32, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			interfaces := func() ([]net.Addr, error) {
				return []net.Addr{&net.IPNet{IP: net.IPv4(127, 0, 0, 1), Mask: net.CIDRMask(tc.ones, 32)}}, nil
			}
			ln, err := containerListen(t.Context(), netip.MustParseAddrPort("127.0.0.1:0"), interfaces, slog.New(slog.NewTextHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = ln.Close() })
			accepted := make(chan net.Conn, 1)
			go func() {
				defer close(accepted)
				if conn, err := ln.Accept(); err == nil {
					accepted <- conn
				}
			}()
			dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 2)}}
			conn, err := dialer.DialContext(t.Context(), "tcp4", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if tc.allowed {
				select {
				case c := <-accepted:
					_ = c.Close()
				case <-time.After(5 * time.Second):
					t.Fatal("a peer inside the network was not accepted")
				}
				return
			}
			if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
				t.Fatalf("read from a refused connection = %v, want it closed", err)
			}
			// Closing the listener ends Accept, so the accepting goroutine
			// has logged the refusal once it closes accepted.
			_ = ln.Close()
			if c, ok := <-accepted; ok {
				_ = c.Close()
				t.Fatal("a peer outside the network was accepted")
			}
			if !strings.Contains(logs.String(), "refused a connection from outside the container network") || !strings.Contains(logs.String(), "listener=\"container listener\"") ||
				!strings.Contains(logs.String(), "127.0.0.2") {
				t.Errorf("logs = %q, want the refusal logged with the peer", logs.String())
			}
		})
	}
}

// An address no interface of this host holds has no container network,
// and the container listener refuses to open on it.
func TestContainerListenerNeedsTheBridgeInterface(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.IPv4(127, 0, 0, 1), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.IPv4(172, 17, 0, 1), Mask: net.CIDRMask(16, 32)},
	}
	_, err := containerListen(t.Context(), netip.MustParseAddrPort("172.18.0.1:0"), func() ([]net.Addr, error) { return addrs, nil }, slog.New(slog.DiscardHandler))
	if ne, ok := errors.AsType[*NoBridgeInterfaceError](err); !ok || ne.Addr != netip.MustParseAddr("172.18.0.1") {
		t.Errorf("containerListen on an address no interface holds = %v, want a NoBridgeInterfaceError", err)
	}
}
