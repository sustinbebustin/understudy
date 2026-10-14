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

// ipRange is an address range as Ruler matches it (a Range with isCIDR):
// bottom and top are hex digit strings of one length, 8 for IPv4 and 32 for
// IPv6, and ByteMachine.addRangePattern builds a byte machine that matches
// at the first digit that places a value inside them. It never checks the
// value's length, so the IPv4 form of an address can satisfy an IPv6 range
// and the other way round, and so can the source text of a number.
type ipRange struct {
	bottom, top         string
	openBottom, openTop bool
}

// cidrRange is the closed range of a prefix's addresses (CIDR.cidr).
func cidrRange(p netip.Prefix) ipRange {
	first := p.Masked().Addr()
	last := first.AsSlice()
	for i := range last {
		if network := p.Bits() - 8*i; network < 8 {
			last[i] |= byte(0xff >> max(network, 0))
		}
	}
	return ipRange{bottom: hexDigits(first.AsSlice()), top: hexDigits(last)}
}

// addressRange is the range a string rule that is an IP literal compiles to
// (CIDR.ipToRangeIfPossible): the address, with its last digit's successor
// as an open top, or its predecessor as an open bottom when the last digit
// is F.
func addressRange(a netip.Addr) ipRange {
	d := []byte(hexDigits(a.AsSlice()))
	r := ipRange{bottom: string(d), top: string(d)}
	switch last := len(d) - 1; d[last] {
	case 'F':
		d[last] = 'E'
		r.bottom, r.openBottom = string(d), true
	case '9':
		d[last] = 'A'
		r.top, r.openTop = string(d), true
	default:
		d[last]++
		r.top, r.openTop = string(d), true
	}
	return r
}

// accepts reports whether Ruler's range machine matches v. A string is
// walked in its address form when it is one; its quoted source text never
// starts with a digit. Any other value is walked in its source text, except
// that a comparable number is read as a number, never walked, when a numeric
// rule sits at the same path (numericBeside; ByteMachine.transitionOn).
func (r ipRange) accepts(v value, numericBeside bool) bool {
	if v.kind == kindString {
		a, ok := parseIP(v.text)
		return ok && r.walk(hexDigits(a.AsSlice()))
	}
	if v.kind == kindNumber && v.numOK && numericBeside {
		return false
	}
	return r.walk(v.text)
}

// walk runs s through the machine addRangePattern builds: past the digits
// bottom and top share, a digit strictly between theirs matches; one equal
// to bottom's (top's) leads to states where a digit above bottom's (below
// top's) matches and an equal one goes on; at the last digit an equal one
// matches when that end is closed. Anything else, or the end of s, fails.
func (r ipRange) walk(s string) bool {
	last := len(r.bottom) - 1
	fork := 0
	for fork < last && r.bottom[fork] == r.top[fork] {
		if fork >= len(s) || s[fork] != r.bottom[fork] {
			return false
		}
		fork++
	}
	if fork >= len(s) || !isHexDigit(s[fork]) {
		return false
	}
	switch c := s[fork]; {
	case c > r.bottom[fork] && c < r.top[fork]:
		return true
	case fork == last:
		return (c == r.bottom[fork] && !r.openBottom) || (c == r.top[fork] && !r.openTop)
	case c == r.bottom[fork]:
		return walkSide(s, r.bottom, fork+1, r.openBottom, func(c, b byte) bool { return c > b })
	case c == r.top[fork]:
		return walkSide(s, r.top, fork+1, r.openTop, func(c, b byte) bool { return c < b })
	}
	return false
}

// walkSide follows one bound from offset from: a digit beyond it in the
// direction that stays inside the range matches, an equal one goes on.
func walkSide(s, bound string, from int, open bool, inside func(c, b byte) bool) bool {
	last := len(bound) - 1
	for o := from; o <= last; o++ {
		if o >= len(s) || !isHexDigit(s[o]) {
			return false
		}
		switch c := s[o]; {
		case inside(c, bound[o]):
			return true
		case c != bound[o]:
			return false
		case o == last:
			return !open
		}
	}
	return false
}

// isHexDigit is a digit of Ruler's HEX_DIGITS, which are upper case.
func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'A' && c <= 'F')
}

// hexDigits is CIDR.toHexDigits: two upper-case hex digits per byte.
func hexDigits(b []byte) string {
	const digits = "0123456789ABCDEF"
	out := make([]byte, 0, 2*len(b))
	for _, x := range b {
		out = append(out, digits[x>>4], digits[x&0x0f])
	}
	return string(out)
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
