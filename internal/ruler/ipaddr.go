package ruler

import (
	"net/netip"
	"strconv"
	"strings"
)

// looksLikeIP is Ruler's gate before it parses an address: the whole string
// is four dot-separated digit runs ([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+) or only
// hex digits and colons with at least one colon ([0-9a-fA-F:]*:[0-9a-fA-F:]*).
func looksLikeIP(s string) bool {
	return looksLikeIPv4(s) || looksLikeIPv6(s)
}

func looksLikeIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || digitsEnd(p, 0) != len(p) {
			return false
		}
	}
	return true
}

func looksLikeIPv6(s string) bool {
	colon := false
	for i := range len(s) {
		switch c := s[i]; {
		case c == ':':
			colon = true
		case isDigit(c), c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return colon
}

// parseIP parses an IP literal the way Ruler does: only strings that pass
// looksLikeIP are tried, and an IPv4-mapped IPv6 address is its IPv4 form
// (Java's InetAddress returns an Inet4Address for it).
func parseIP(s string) (netip.Addr, bool) {
	if !looksLikeIP(s) {
		return netip.Addr{}, false
	}
	return ipLiteral(s)
}

// maxIPv4Literal is the longest dotted IPv4 text Java's
// IPAddressUtil.textToNumericFormatV4 accepts.
const maxIPv4Literal = 15

// ipLiteral parses a string that passed looksLikeIP as Java's
// InetAddress.getByName does. Each IPv4 octet is decimal, leading zeros
// included, so "010.0.0.1" is 10.0.0.1, within Java's limit on the length
// of the whole text.
func ipLiteral(s string) (netip.Addr, bool) {
	if !looksLikeIPv4(s) {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return netip.Addr{}, false
		}
		return a.Unmap(), true
	}
	if len(s) > maxIPv4Literal {
		return netip.Addr{}, false
	}
	var octets [4]byte
	for i, part := range strings.Split(s, ".") {
		n, err := strconv.ParseUint(part, 10, 8)
		if err != nil {
			return netip.Addr{}, false
		}
		octets[i] = byte(n)
	}
	return netip.AddrFrom4(octets), true
}

// parseCIDR compiles a cidr operand with Ruler's checks and texts
// (CIDR.cidr): exactly one '/', an integer mask, a standard address, and a
// mask shorter than the address (so /32 and /128 are rejected).
func parseCIDR(s string) (prefix netip.Prefix, detail string) {
	slashed := splitLikeJava(s, '/')
	if len(slashed) != 2 {
		return netip.Prefix{}, "Malformed CIDR, one '/' required"
	}
	// Java's Integer.parseInt: a value outside int32 is not an integer.
	bits, err := strconv.ParseInt(slashed[1], 10, 32)
	if err != nil {
		return netip.Prefix{}, "Malformed CIDR, mask bits must be an integer"
	}
	if bits < 0 {
		return netip.Prefix{}, "Malformed CIDR, mask bits must not be negative"
	}
	addr, detail := cidrAddress(slashed[0])
	if detail != "" {
		return netip.Prefix{}, detail
	}
	if addr.Is4() && bits > 31 {
		return netip.Prefix{}, "IPv4 mask bits must be < 32"
	}
	if addr.Is6() && bits > 127 {
		return netip.Prefix{}, "IPv6 mask bits must be < 128"
	}
	return netip.PrefixFrom(addr, int(bits)).Masked(), ""
}

func cidrAddress(s string) (addr netip.Addr, detail string) {
	if !looksLikeIP(s) {
		return netip.Addr{}, "Nonstandard IP address: " + s
	}
	a, ok := ipLiteral(s)
	if !ok {
		return netip.Addr{}, "Invalid IP address: " + s
	}
	return a, ""
}

// splitLikeJava splits as Java's String.split does with a one-character
// pattern: trailing empty strings are dropped.
func splitLikeJava(s string, sep byte) []string {
	parts := strings.Split(s, string(sep))
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}
