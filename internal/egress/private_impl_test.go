package egress_test

import (
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"testing"
	"testing/synctest"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/egress"
)

// serviceName draws a name a container network resolves to a private
// address: a compose service or a Kubernetes service.
func serviceName() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{"receiver", "hooks", "receiver.default.svc", "receiver.default.svc.cluster.local"})
}

// privateAddr draws an RFC 1918 or ULA address.
func privateAddr() *rapid.Generator[netip.Addr] {
	return rapid.Custom(func(t *rapid.T) netip.Addr {
		b := func(label string) byte { return rapid.Byte().Draw(t, label) }
		switch rapid.IntRange(0, 3).Draw(t, "range") {
		case 0:
			return netip.AddrFrom4([4]byte{10, b("b"), b("c"), b("d")})
		case 1:
			return netip.AddrFrom4([4]byte{172, rapid.ByteRange(16, 31).Draw(t, "b"), b("c"), b("d")})
		case 2:
			return netip.AddrFrom4([4]byte{192, 168, b("c"), b("d")})
		default:
			return netip.AddrFrom16([16]byte{0xfd, 1: b("1"), 2: b("2"), 7: b("7"), 15: b("15")})
		}
	})
}

// forbiddenAddr draws a link-local address, IPv4 or IPv6, which covers
// the metadata and ECS endpoints, or the IPv6 metadata endpoint.
func forbiddenAddr() *rapid.Generator[netip.Addr] {
	return rapid.Custom(func(t *rapid.T) netip.Addr {
		b := func(label string) byte { return rapid.Byte().Draw(t, label) }
		switch rapid.IntRange(0, 2).Draw(t, "range") {
		case 0:
			return netip.AddrFrom4([4]byte{169, 254, b("c"), b("d")})
		case 1:
			return netip.AddrFrom16([16]byte{0xfe, 0x80 | rapid.ByteMax(0x3f).Draw(t, "1"), 8: b("8"), 15: b("15")})
		default:
			return netip.MustParseAddr("fd00:ec2::254")
		}
	})
}

// A DNS-name entry, such as a compose or Kubernetes service name, reaches
// the private address it resolves to, while a name resolving to a
// link-local or metadata address is refused whatever the list says
// (sns-http-delivery.md 7.3, ADR 0015 amendment).
func TestServiceNamesReachPrivateAddressesOnly(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		name := serviceName().Draw(rt, "name")
		private := rapid.Bool().Draw(rt, "private")
		var addr netip.Addr
		if private {
			addr = privateAddr().Draw(rt, "address")
		} else {
			addr = forbiddenAddr().Draw(rt, "address")
		}
		dial := netip.AddrPortFrom(addr, 8080)
		var (
			res    egress.Response
			err    error
			dialed []netip.AddrPort
		)
		// The exchange runs in its own bubble; rapid reads the test's
		// deadline, which a bubble may not.
		synctest.Test(t, func(t *testing.T) {
			n := newNetwork(map[string]*receiver{dial.String(): answering(http.StatusOK)})
			cl := newClient(t, egress.Options{Dialer: n, Resolver: resolver{name: to(name, addr.String())}}, name+":8080")
			res, err = send(t, cl, "http://"+name+":8080/hook", nil, "hello")
			dialed = n.dialed()
		})
		if private {
			if err != nil || res.Status != http.StatusOK || !slices.Equal(dialed, []netip.AddrPort{dial}) {
				rt.Fatalf("%s resolving to %s: Send = %+v, %v, dialed %v; want the receiver at %s", name, addr, res, err, dialed, dial)
			}
			return
		}
		var re *egress.RefusedError
		if !errors.As(err, &re) || re.Refusal != egress.ForbiddenAddress || re.Name != addr.String() || len(dialed) != 0 {
			rt.Fatalf("%s resolving to %s: Send error %v, dialed %v; want it refused as a forbidden address before dialing", name, addr, err, dialed)
		}
	})
}
