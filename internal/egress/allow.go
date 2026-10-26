package egress

import (
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

// ParseAllowList parses the entries of setting (sns-http-delivery.md 7.2),
// refusing a malformed entry and one naming a host that is always refused
// (7.3). It is not implemented yet.
func ParseAllowList(setting Setting, entries []string) (AllowList, *EntryError) {
	if len(entries) == 0 {
		return AllowList{setting: setting}, nil
	}
	return AllowList{}, &EntryError{Setting: setting, Index: 0, Entry: entries[0], Reason: "allow-list parsing is not implemented yet"}
}

// Setting returns the setting the list was parsed for.
func (a AllowList) Setting() Setting { return a.setting }

// Entries returns the entries in the order given, duplicates kept.
func (a AllowList) Entries() []Entry { return append([]Entry(nil), a.entries...) }

// Match returns the first entry that allows host and port: host,
// normalized as Entry.Host, equal to the entry's host, and port equal to
// the entry's or the entry's being AnyPort. There are no host wildcards and
// no CIDR ranges (sns-http-delivery.md 7.2). It is not implemented yet and
// matches nothing.
func (a AllowList) Match(host string, port uint16) (Entry, bool) {
	_, _ = host, port
	return Entry{}, false
}

// IsAWSName reports whether name, normalized, is or ends in "." plus a
// suffix that is always refused: a partition DNS suffix, the aws top-level
// domain or execute-api.com (sns-http-delivery.md 7.3). It is not
// implemented yet and reports false.
func IsAWSName(name string) bool {
	_ = name
	return false
}
