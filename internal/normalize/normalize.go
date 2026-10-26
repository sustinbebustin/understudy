package normalize

import "fmt"

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

// ParseName returns the normalizer spelled s. Not implemented yet: it
// refuses every spelling.
func ParseName(s string) (Name, error) {
	return "", &NameError{Text: s}
}

// Mask is the placeholder n writes. Not implemented yet: it returns "".
func (n Name) Mask() string { return "" }

// Set is a selection of normalizers, applied in its order.
type Set struct {
	names []Name
}

// Snapshot is the set snapshot diff --normalize applies. Not implemented
// yet: it is empty.
func Snapshot() Set { return Set{} }

// Exchange is the set the parity replayer applies to recorded HTTP
// exchanges and understudy's answers. Not implemented yet: it is empty.
func Exchange() Set { return Set{} }

// Names returns the set's normalizers in the order their rules apply.
func (s Set) Names() []Name { return s.names }

// Has reports whether the set holds n. Not implemented yet: it reports
// false.
func (s Set) Has(Name) bool { return false }

// Kind is the JSON kind of a scalar a member rule reads.
type Kind uint8

// The scalar kinds.
const (
	String Kind = iota + 1
	Number
)

// Member masks a scalar by the name of the JSON member or XML element that
// holds it: the first member rule of the set that matches gives its mask.
// Not implemented yet: no rule matches.
func (s Set) Member(_ string, _ Kind, value string) (string, bool) { return value, false }

// Text applies every text rule of the set, in order, to free text: a JSON
// string value, XML text or attribute value, or a header value. Not
// implemented yet: it returns value unchanged.
func (s Set) Text(value string) string { return value }

// Replacement is one substitution of the bound normalizer.
type Replacement struct {
	Old, New string
}

// Substitute replaces every occurrence of each Old in s by its New, longest
// Old first, never rescanning a replaced span. Not implemented yet: it
// returns s unchanged.
func Substitute(_ []Replacement, s string) string { return s }
