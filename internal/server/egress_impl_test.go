package server

import (
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/docker"
)

// ipNet is an interface address as net.InterfaceAddrs lists it: the
// interface's own address with its network's mask.
func ipNet(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return &net.IPNet{IP: ip, Mask: n.Mask}
}

func sortedAddrs(a []netip.AddrPort) []netip.AddrPort {
	out := slices.Clone(a)
	slices.SortFunc(out, func(x, y netip.AddrPort) int { return x.Compare(y) })
	return out
}

// understudy's own addresses are what Serve recorded, a listener on an
// unspecified address once per interface address of its family (every
// family for [::]), and each Runtime API listener open when egress asks
// (sns-http-delivery.md 7.3).
func TestOwnAddrsListEveryAddressAListenerAcceptsOn(t *testing.T) {
	runtime := []netip.AddrPort{netip.MustParseAddrPort("172.17.0.1:40001")}
	o := &ownAddrs{
		runtime: func() []netip.AddrPort { return runtime },
		interfaces: func() ([]net.Addr, error) {
			return []net.Addr{ipNet(t, "127.0.0.1/8"), ipNet(t, "::1/128"), ipNet(t, "172.17.0.1/16"), ipNet(t, "fd00::5/64")}, nil
		},
	}
	o.add(netip.MustParseAddrPort("0.0.0.0:4566"))
	o.add(netip.MustParseAddrPort("[::]:4567"))
	o.add(netip.MustParseAddrPort("[::ffff:10.0.0.2]:2375"))
	want := sortedAddrs([]netip.AddrPort{
		netip.MustParseAddrPort("127.0.0.1:4566"), netip.MustParseAddrPort("172.17.0.1:4566"),
		netip.MustParseAddrPort("127.0.0.1:4567"), netip.MustParseAddrPort("[::1]:4567"), netip.MustParseAddrPort("172.17.0.1:4567"), netip.MustParseAddrPort("[fd00::5]:4567"),
		netip.MustParseAddrPort("10.0.0.2:2375"),
		netip.MustParseAddrPort("172.17.0.1:40001"),
	})
	if got := sortedAddrs(o.ListenerAddrs()); !slices.Equal(got, want) {
		t.Errorf("ListenerAddrs = %v\nwant %v", got, want)
	}
	runtime = nil
	if got := o.ListenerAddrs(); slices.Contains(got, netip.MustParseAddrPort("172.17.0.1:40001")) {
		t.Errorf("ListenerAddrs = %v still lists a Runtime API listener that closed", got)
	}
	o.interfaces = func() ([]net.Addr, error) { return nil, errors.New("no interfaces here") }
	if got := o.ListenerAddrs(); !slices.Contains(got, netip.MustParseAddrPort("127.0.0.1:4566")) || !slices.Contains(got, netip.MustParseAddrPort("[::1]:4567")) {
		t.Errorf("ListenerAddrs with unreadable interfaces = %v, want the loopback addresses at least", got)
	}
}

// SNS's links name the TLS listener's port only once Serve records it
// (sns-http-delivery.md 8.3.3).
func TestOwnAddrsReportTheTLSPort(t *testing.T) {
	o := &ownAddrs{}
	if port, ok := o.TLSPort(); ok {
		t.Fatalf("TLSPort before any TLS listener = %d, true", port)
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := o.addTLS(ln); err != nil {
		t.Fatal(err)
	}
	bound := netip.MustParseAddrPort(ln.Addr().String())
	if port, ok := o.TLSPort(); !ok || port != bound.Port() {
		t.Errorf("TLSPort = %d, %v; want %d", port, ok, bound.Port())
	}
	if got := o.ListenerAddrs(); !slices.Equal(got, []netip.AddrPort{bound}) {
		t.Errorf("ListenerAddrs = %v, want the TLS listener %v", got, bound)
	}
}

// A tcp:// Docker Engine is refused at the addresses its host resolves to;
// a unix socket, or Docker disabled, adds nothing (sns-http-delivery.md 7.1,
// 7.3).
func TestOwnAddrsRefuseATCPDockerEngine(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	cases := []struct {
		name     string
		endpoint docker.Endpoint
		want     []netip.AddrPort
	}{
		{"plain TCP at a literal", docker.TCP{Addr: "10.0.0.9:2375"}, []netip.AddrPort{netip.MustParseAddrPort("10.0.0.9:2375")}},
		{"TLS at a literal", docker.TLS{Addr: "[fd00::9]:2376", CertDir: "/certs"}, []netip.AddrPort{netip.MustParseAddrPort("[fd00::9]:2376")}},
		{"a unix socket", docker.Unix{Path: "/var/run/docker.sock"}, nil},
		{"Docker disabled", nil, nil},
	}
	for _, c := range cases {
		o := &ownAddrs{}
		o.addDocker(t.Context(), c.endpoint, log)
		if got := o.ListenerAddrs(); !slices.Equal(got, c.want) {
			t.Errorf("%s: ListenerAddrs = %v, want %v", c.name, got, c.want)
		}
	}
	o := &ownAddrs{}
	o.addDocker(t.Context(), docker.TCP{Addr: "localhost:2375"}, log)
	if got := o.ListenerAddrs(); !slices.Contains(got, netip.MustParseAddrPort("127.0.0.1:2375")) {
		t.Errorf("tcp://localhost:2375: ListenerAddrs = %v, want 127.0.0.1:2375 among them", got)
	}
}
