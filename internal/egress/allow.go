package egress

import (
	"net/netip"
	"strconv"
	"strings"
)

// Setting names one producer's allow-list in the one key shape
// <service>.<feature>.allow (ADR 0015 amendment): Setting{"sns",
// "httpDelivery"} is sns.httpDelivery.allow.
type Setting struct {
	Service, Feature string
}

// String returns the setting's key, such as "sns.httpDelivery.allow".
func (s Setting) String() string { return s.Service + "." + s.Feature + ".allow" }

// Port is an allow-list entry's port: 1 to 65535, or AnyPort for "*", which
// exists for test receivers on ephemeral ports (sns-http-delivery.md 7.2).
type Port uint16

// AnyPort is the "*" port, which matches every port.
const AnyPort Port = 0

// Entry is one allow-list entry, host ":" port (sns-http-delivery.md 7.2).
// Host is normalized: lowercase, without a trailing dot, an IPv6 literal
// without brackets, an IPv4-mapped IPv6 literal unmapped, and IP literals
// in their canonical form.
type Entry struct {
	Host string
	Port Port
}

// String returns the entry as written in the setting: "127.0.0.1:8080",
// "[::1]:*", "localhost:9000".
func (e Entry) String() string {
	port := "*"
	if e.Port != AnyPort {
		port = strconv.Itoa(int(e.Port))
	}
	host := e.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return host + ":" + port
}

// AllowList is a parsed allow-list. The zero value is empty and allows
// nothing, which is every producer's default.
type AllowList struct {
	setting Setting
	entries []Entry
}

// EntryError is an allow-list entry ParseAllowList refuses; nothing of the
// list is parsed.
type EntryError struct {
	Setting Setting
	// Index is the entry's index in the list given.
	Index int
	// Entry is the entry as given.
	Entry string
	// Reason says what is wrong, as a clause.
	Reason string
}

func (e *EntryError) Error() string {
	return e.Setting.String() + "[" + strconv.Itoa(e.Index) + "]: " + e.Reason +
		"; entries are host:port, the host localhost, an IP literal ([v6] for IPv6) or a DNS name, and the port 1-65535 or *"
}

// ParseAllowList parses the entries of setting (sns-http-delivery.md 7.2).
// It refuses the first entry that is malformed or names a host that is
// always refused (7.3), so a bad entry stops the load instead of being
// ignored; nothing of the list is parsed then.
func ParseAllowList(setting Setting, entries []string) (AllowList, *EntryError) {
	out := AllowList{setting: setting, entries: make([]Entry, 0, len(entries))}
	for i, text := range entries {
		e, reason := parseEntry(text)
		if reason != "" {
			return AllowList{}, &EntryError{Setting: setting, Index: i, Entry: text, Reason: reason}
		}
		out.entries = append(out.entries, e)
	}
	return out, nil
}

// parseEntry parses one entry, or returns why it is refused.
func parseEntry(text string) (e Entry, reason string) {
	hostText, portText, ok := splitEntry(text)
	if !ok {
		return Entry{}, strconv.Quote(text) + " is not host:port"
	}
	port, ok := parsePort(portText)
	if !ok {
		return Entry{}, "the port " + strconv.Quote(portText) + " is not 1-65535 or *"
	}
	h, ok := parseHost(hostText)
	if !ok {
		return Entry{}, "the host " + strconv.Quote(hostText) + " is not localhost, an IPv4 literal, a bracketed IPv6 literal or a DNS name"
	}
	if why, refused := h.refused(); refused {
		return Entry{}, "the host " + strconv.Quote(hostText) + " " + why + ", which understudy never reaches, so no entry may name it"
	}
	return Entry{Host: h.name, Port: port}, ""
}

// splitEntry splits host ":" port, where an IPv6 host is bracketed. The
// brackets stay on the host, so parseHost can tell "[::1]" from "::1".
func splitEntry(text string) (host, port string, ok bool) {
	if strings.HasPrefix(text, "[") {
		end := strings.IndexByte(text, ']')
		if end < 0 || !strings.HasPrefix(text[end+1:], ":") {
			return "", "", false
		}
		return text[:end+1], text[end+2:], true
	}
	host, port, ok = strings.Cut(text, ":")
	return host, port, ok && host != "" && !strings.Contains(port, ":")
}

// parsePort parses "*" or a decimal port 1 to 65535.
func parsePort(text string) (Port, bool) {
	if text == "*" {
		return AnyPort, true
	}
	n, err := strconv.ParseUint(text, 10, 16)
	if err != nil || n == 0 {
		return 0, false
	}
	return Port(n), true
}

// host is an entry's host, parsed: its normalized name and, for an IP
// literal, the address.
type host struct {
	name string
	// addr is valid for an IP literal and the zero Addr otherwise.
	addr netip.Addr
}

// refused reports whether h is a host no entry may name, and why: an AWS
// name or an always-refused address (sns-http-delivery.md 7.3).
func (h host) refused() (string, bool) {
	if h.addr.IsValid() {
		return forbiddenAddress(h.addr)
	}
	if IsAWSName(h.name) {
		return "names an AWS endpoint", true
	}
	return "", false
}

// parseHost parses an entry's host by the grammar of
// sns-http-delivery.md 7.2: localhost, an IPv4 literal, a bracketed IPv6
// literal or a DNS name, any case, with at most one trailing dot.
func parseHost(text string) (host, bool) {
	if inner, ok := strings.CutPrefix(text, "["); ok {
		inner, ok = strings.CutSuffix(inner, "]")
		a, err := netip.ParseAddr(inner)
		if !ok || err != nil || !a.Is6() || a.Zone() != "" {
			return host{}, false
		}
		a = a.Unmap()
		return host{name: a.String(), addr: a}, true
	}
	if a, err := netip.ParseAddr(text); err == nil {
		// An IPv6 literal must be bracketed.
		return host{name: a.String(), addr: a}, a.Is4()
	}
	name := strings.ToLower(strings.TrimSuffix(text, "."))
	return host{name: name}, isDNSName(name)
}

// isDNSName reports whether name, lowercased and without its trailing dot,
// is a DNS name of ASCII letters, digits and "-" in dot-separated labels
// of 1 to 63 characters, at most 253 in all. A name whose last label is
// all digits is not one: resolvers read such a name as an IPv4 address in
// a form netip does not parse, such as "127.1".
func isDNSName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	labels := strings.Split(name, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return false
		}
		for _, r := range l {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return strings.Trim(labels[len(labels)-1], "0123456789") != ""
}

// Setting returns the setting the list was parsed for.
func (a AllowList) Setting() Setting { return a.setting }

// Entries returns the entries in the order given, duplicates kept.
func (a AllowList) Entries() []Entry { return append([]Entry(nil), a.entries...) }

// Match returns the first entry that allows host and port: host,
// normalized as Entry.Host, equal to the entry's host, and port equal to
// the entry's or the entry's being AnyPort. There are no host wildcards and
// no CIDR ranges (sns-http-delivery.md 7.2).
func (a AllowList) Match(host string, port uint16) (Entry, bool) {
	name := normalizeHost(host)
	for _, e := range a.entries {
		if e.Host == name && (e.Port == AnyPort || uint16(e.Port) == port) {
			return e, true
		}
	}
	return Entry{}, false
}

// normalizeHost puts a URL's host in Entry.Host's form: lowercase, without
// brackets or a trailing dot, an IP literal canonical and unmapped. It
// does not check the grammar: a host the grammar refuses matches no entry.
func normalizeHost(h string) string {
	if inner, ok := strings.CutPrefix(h, "["); ok {
		h = strings.TrimSuffix(inner, "]")
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Unmap().String()
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// IsAWSName reports whether name, lowercased and without trailing dots, is
// or ends in "." plus a suffix that is always refused: a partition DNS
// suffix, the aws top-level domain or execute-api.com
// (sns-http-delivery.md 7.3).
func IsAWSName(name string) bool {
	name = strings.ToLower(strings.TrimRight(name, "."))
	for name != "" {
		if isAWSSuffix(name) {
			return true
		}
		_, name, _ = strings.Cut(name, ".")
	}
	return false
}

// isAWSSuffix reports whether s is a suffix that is always refused with
// every name under it: a partition's DNS or dual-stack suffix
// (sns-http-delivery.md 7.3, from aws-sdk-go-v2's partitions.json), the
// aws top-level domain, which holds on.aws, or execute-api.com, API
// Gateway's example domain (ADR 0015).
func isAWSSuffix(s string) bool {
	switch s {
	case "amazonaws.com", "api.aws",
		"amazonaws.com.cn", "api.amazonwebservices.com.cn",
		"amazonaws.eu", "api.amazonwebservices.eu",
		"c2s.ic.gov", "api.aws.ic.gov",
		"sc2s.sgov.gov", "api.aws.scloud",
		"cloud.adc-e.uk", "api.cloud-aws.adc-e.uk",
		"csp.hci.ic.gov", "api.aws.hci.ic.gov",
		"aws",
		"execute-api.com":
		return true
	}
	return false
}

// forbiddenAddress reports whether a, unmapped, is an address understudy
// never connects to whatever the allow-list says, and names its kind: the
// instance metadata endpoints (169.254.169.254, fd00:ec2::254), the ECS
// credentials endpoint (169.254.170.2), and link-local, unspecified and
// multicast addresses (sns-http-delivery.md 7.3).
func forbiddenAddress(a netip.Addr) (string, bool) {
	a = a.Unmap()
	switch {
	case a == netip.AddrFrom4([4]byte{169, 254, 169, 254}),
		a == netip.AddrFrom16([16]byte{0xfd, 0x00, 0x0e, 0xc2, 14: 0x02, 15: 0x54}):
		return "is an instance metadata address", true
	case a == netip.AddrFrom4([4]byte{169, 254, 170, 2}):
		return "is the ECS credentials address", true
	case a.IsLinkLocalUnicast():
		return "is a link-local address", true
	case a.IsUnspecified():
		return "is an unspecified address", true
	case a.IsMulticast():
		return "is a multicast address", true
	}
	return "", false
}
