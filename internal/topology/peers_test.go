package topology_test

import (
	"net"
	"net/netip"
	"testing"

	"github.com/sustinbebustin/understudy/internal/topology"
)

// The container network is the subnet of the interface holding the
// gateway; an address no interface holds has none.
func TestInterfacePrefixFindsTheBridgeSubnet(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.IPv4(127, 0, 0, 1), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.IPv4(172, 17, 0, 1), Mask: net.CIDRMask(16, 32)},
		&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
	}
	got, ok := topology.InterfacePrefix(addrs, netip.MustParseAddr("172.17.0.1"))
	if want := netip.MustParsePrefix("172.17.0.0/16"); !ok || got != want {
		t.Errorf("InterfacePrefix(172.17.0.1) = %v, %v; want %v", got, ok, want)
	}
	if got, ok := topology.InterfacePrefix(addrs, netip.MustParseAddr("172.18.0.1")); ok {
		t.Errorf("InterfacePrefix(172.18.0.1) = %v; no interface holds it", got)
	}
}
