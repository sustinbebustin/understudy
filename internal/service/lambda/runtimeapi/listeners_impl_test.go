package runtimeapi_test

import (
	"net/netip"
	"slices"
	"testing"
)

// ListenerAddrs lists each open environment's listener at the address it
// accepts on, and drops one once it closes, so egress refuses exactly the
// Runtime APIs open when it checks (sns-http-delivery.md 7.3).
func TestListenerAddrsFollowOpenEnvironments(t *testing.T) {
	s := newServer(t)
	if got := s.ListenerAddrs(); len(got) != 0 {
		t.Fatalf("ListenerAddrs before any environment = %v", got)
	}
	first, err := s.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// The test topology binds and advertises the same loopback gateway.
	want := []netip.AddrPort{first.Addr(), second.Addr()}
	got := s.ListenerAddrs()
	if len(got) != 2 || !slices.Contains(got, want[0]) || !slices.Contains(got, want[1]) {
		t.Fatalf("ListenerAddrs = %v, want %v", got, want)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if got := s.ListenerAddrs(); !slices.Equal(got, []netip.AddrPort{second.Addr()}) {
		t.Errorf("ListenerAddrs after the first closed = %v, want %v", got, second.Addr())
	}
}
