package normalize

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// Name names one normalizer of the closed set.
type Name string

// The snapshot set, whose behavior snapshot diff --normalize fixes.
const (
	RequestID     Name = "request-id"
	ReceiptHandle Name = "receipt-handle"
	WallTime      Name = "wall-time"
	Duration      Name = "duration"
	Host          Name = "host"
	Account       Name = "account"
	AccessKey     Name = "access-key"
	EventID       Name = "event-id"
)

// The normalizers added for recorded HTTP exchanges.
const (
	// Bound replaces each value understudy returned for a bound AWS value
	// with that AWS value (Substitute), before every other rule.
	Bound       Name = "bound"
	HostID      Name = "host-id"
	ISOTime     Name = "iso-time"
	EpochTime   Name = "epoch-time"
	HTTPDate    Name = "http-date"
	Caller      Name = "caller"
	UniqueID    Name = "unique-id"
	CanonicalID Name = "canonical-id"
	PrincipalID Name = "principal-id"
	SourceIP    Name = "source-ip"
	Credential  Name = "credential"
)

// Names returns every normalizer, snapshot set first.
func Names() []Name {
	return []Name{
		RequestID, ReceiptHandle, WallTime, Duration, Host, Account, AccessKey, EventID,
		Bound, HostID, ISOTime, EpochTime, HTTPDate, Caller, UniqueID, CanonicalID, PrincipalID, SourceIP, Credential,
	}
}

// NameError reports text that names no normalizer.
type NameError struct {
	Text string
}

func (e *NameError) Error() string {
	return fmt.Sprintf("normalizer %q is unknown; use one of %q", e.Text, Names())
}

// ParseName returns the normalizer spelled s, exactly as Names spells it.
func ParseName(s string) (Name, error) {
	if i := slices.Index(Names(), Name(s)); i >= 0 {
		return Names()[i], nil
	}
	return "", &NameError{Text: s}
}

// Mask is the placeholder n writes in place of what it masks: its name in
// angle brackets, so a masked value says which rule hid it.
func (n Name) Mask() string { return "<" + string(n) + ">" }

// Set is a selection of normalizers, applied in its order. Its names are
// never written, so the sets of one kind share one table and building one
// allocates nothing, however often a caller does.
type Set struct {
	names []Name
}

// snapshotNames and exchangeNames are the orders of Snapshot and Exchange.
//
//nolint:gochecknoglobals // constant tables; Go has no constant arrays.
var (
	snapshotNames = [...]Name{RequestID, ReceiptHandle, WallTime, Duration, Host, Account, AccessKey, EventID}
	exchangeNames = [...]Name{
		Bound, RequestID, HostID, ISOTime, EpochTime, HTTPDate, Caller, UniqueID,
		CanonicalID, PrincipalID, SourceIP, Credential, Account, ReceiptHandle,
	}
)

// Snapshot is the set snapshot diff --normalize applies (ARCHITECTURE.md 12).
func Snapshot() Set { return Set{names: snapshotNames[:]} }

// Exchange is the set the parity replayer applies to recorded HTTP
// exchanges and understudy's answers (ARCHITECTURE.md 19). Bound comes
// first, so a bound value is replaced before any rule can mask part of it;
// Caller precedes UniqueID and Account, so the recording caller and the
// replay caller (the masked account's root, ADR 0012) are one placeholder
// rather than an id or ARN with a masked account. It keeps URL hosts, which
// a scenario's bindings settle, and masks the account a URL path starts with.
func Exchange() Set { return Set{names: exchangeNames[:]} }

// Names returns the set's normalizers in the order their rules apply.
func (s Set) Names() []Name { return slices.Clone(s.names) }

// Has reports whether the set holds n.
func (s Set) Has(n Name) bool { return slices.Contains(s.names, n) }

// Kind is the JSON kind of a scalar a member rule reads.
type Kind uint8

// The scalar kinds.
const (
	String Kind = iota + 1
	Number
)

// Member masks a scalar by the name of the JSON member or XML element that
// holds it: the first member rule of the set, in set order, that matches
// gives its mask. ok is false when none matches. The name is folded first:
// lower-cased, with '_' and '-' dropped, so RequestId, requestId and
// request_id are one name.
func (s Set) Member(name string, kind Kind, value string) (masked string, ok bool) {
	key := FoldName(name)
	for _, n := range s.names {
		if n.masksMember(key, kind, value) {
			return n.Mask(), true
		}
	}
	return value, false
}

// Text applies every text rule of the set, in set order, to free text: a
// JSON string value, XML text or attribute value, or a header value.
func (s Set) Text(value string) string {
	for _, n := range s.names {
		value = n.maskText(value)
	}
	return value
}

// Replacement is one substitution of the bound normalizer.
type Replacement struct {
	Old, New string
}

// Substitute replaces every occurrence of each Old in s by its New, longest
// Old first (ties in slice order), never rescanning a replaced span: a New
// that holds another Old, or its own, stays as written. An empty Old
// replaces nothing.
func Substitute(rs []Replacement, s string) string {
	order := slices.DeleteFunc(slices.Clone(rs), func(r Replacement) bool { return r.Old == "" })
	if len(order) == 0 || s == "" {
		return s
	}
	slices.SortStableFunc(order, func(a, b Replacement) int { return cmp.Compare(len(b.Old), len(a.Old)) })
	spans := []span{{text: s}}
	for _, r := range order {
		spans = r.apply(spans)
	}
	var b strings.Builder
	for _, sp := range spans {
		b.WriteString(sp.text)
	}
	return b.String()
}

// span is a piece of the text Substitute rewrites; a replaced span is a New
// and is not scanned again.
type span struct {
	text     string
	replaced bool
}

// apply replaces every occurrence of r.Old in the spans not yet replaced.
func (r Replacement) apply(spans []span) []span {
	next := make([]span, 0, len(spans))
	for _, sp := range spans {
		if sp.replaced || !strings.Contains(sp.text, r.Old) {
			next = append(next, sp)
			continue
		}
		for i, part := range strings.Split(sp.text, r.Old) {
			if i > 0 {
				next = append(next, span{text: r.New, replaced: true})
			}
			if part != "" {
				next = append(next, span{text: part})
			}
		}
	}
	return next
}

// FoldName is the member name the member rules read: lower-cased, with '_'
// and '-' dropped, so requestId, RequestId and request_id compare alike.
// snapshot folds the names its scope rules read with it too, so the two
// kinds of rule cannot read one name two ways.
func FoldName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '_' || r == '-':
			return -1
		case 'A' <= r && r <= 'Z':
			return r + 'a' - 'A'
		}
		return r
	}, name)
}
