package ruler

// Options selects the four behaviors where Event Ruler and a service's
// documentation disagree and no AWS recording settles which one the service
// has (matching-engines.md section 10). The zero value is Ruler's behavior for
// each. The owning service lifts each field into a fidelity switch with the
// metadata Switches returns; this package stays a leaf and does not import
// the fidelity ledger.
type Options struct {
	// ExistsTrue decides whether {"exists": true} accepts null and "".
	ExistsTrue ExistsTrue
	// EmptyInput decides whether an event with no fields can match.
	EmptyInput EmptyInput
	// BareNumber decides how a bare number rule such as [300] compares.
	BareNumber BareNumber
	// MalformedOr decides what a "$or" that is not an array of two or more
	// objects means.
	MalformedOr MalformedOr
}

// ExistsTrue is the meaning of {"exists": true} for null and empty-string
// values (matching-engines.md 4.4, V30, V31; human recording H2).
type ExistsTrue uint8

const (
	// ExistsTrueAnyValue is Ruler: any value exists, including null and "".
	ExistsTrueAnyValue ExistsTrue = iota
	// ExistsTrueNonEmpty is the SNS documentation: the key "must have a
	// non-null and non-empty value".
	ExistsTrueNonEmpty
)

// String returns the switch value as the coverage report prints it.
func (e ExistsTrue) String() string {
	switch e {
	case ExistsTrueAnyValue:
		return "any-value"
	case ExistsTrueNonEmpty:
		return "non-empty"
	}
	return "unknown"
}

// EmptyInput is the outcome for an event or attribute set with no fields
// (matching-engines.md 4.4, V33; human recording H1). Only an
// {"exists": false} rule can tell the two apart.
type EmptyInput uint8

const (
	// EmptyInputMatchable is Ruler: {"exists": false} matches {}.
	EmptyInputMatchable EmptyInput = iota
	// EmptyInputNeverMatches is the SNS documentation: "An empty set of
	// attributes results in the filter not matching".
	EmptyInputNeverMatches
)

// String returns the switch value as the coverage report prints it.
func (e EmptyInput) String() string {
	switch e {
	case EmptyInputMatchable:
		return "matchable"
	case EmptyInputNeverMatches:
		return "never-matches"
	}
	return "unknown"
}

// BareNumber is how a bare number rule compares with event numbers
// (matching-engines.md 4.5, V14; human recordings H6 for SNS, H7 for Lambda).
type BareNumber uint8

const (
	// BareNumberNumericOrText is Ruler: numeric equality, or identical text
	// when the rule number is not comparable, so [300] matches 3.0e2.
	BareNumberNumericOrText BareNumber = iota
	// BareNumberTextOnly is the Lambda documentation: "300, 300.0, and 3.0e2
	// are not considered equal".
	BareNumberTextOnly
)

// String returns the switch value as the coverage report prints it.
func (b BareNumber) String() string {
	switch b {
	case BareNumberNumericOrText:
		return "numeric-or-text"
	case BareNumberTextOnly:
		return "text-only"
	}
	return "unknown"
}

// MalformedOr is what a malformed "$or" means (matching-engines.md 4.8,
// V70 to V72; human recording H10).
type MalformedOr uint8

const (
	// MalformedOrRejected is Ruler's strict compiler: the pattern is rejected
	// with its "$or" message.
	MalformedOrRejected MalformedOr = iota
	// MalformedOrOrdinaryKey is the documented fallback, which Ruler's
	// Machine.addRule implements by recompiling with "$or" as a plain key:
	// {"$or": {"m": ["x"]}} becomes the key path "$or.m", and shapes that are
	// not valid as a plain key still fail with the plain grammar's message.
	MalformedOrOrdinaryKey
)

// String returns the switch value as the coverage report prints it.
func (m MalformedOr) String() string {
	switch m {
	case MalformedOrRejected:
		return "rejected"
	case MalformedOrOrdinaryKey:
		return "ordinary-key"
	}
	return "unknown"
}

// Switch is the fidelity-ledger metadata of one behavior a profile chooses
// without AWS-recorded evidence for its service. The owning service turns it
// into a fidelity.Switch. The Options switches follow executed Ruler source;
// the fixed ones follow service documentation or inference.
type Switch struct {
	// Name is the switch name, prefixed by the profile ("sns.filter-policy.").
	Name string
	// Value is the profile's current setting, as the String methods print it.
	Value string
	// Ref is the research section that documents the conflict.
	Ref string
	// Record is the record-backlog item ("R09/6") or the human recording item
	// of matching-engines.md 8 that settles it for the profile's service, or
	// empty when no item covers that service.
	Record string
}

// switchRecords holds, per Options field, the Switch.Record of one profile.
type switchRecords struct {
	existsTrue, emptyInput, bareNumber, malformedOr string
}

const switchRef = "docs/research/phase1/matching-engines.md#10"

// Switches returns the metadata of every unverified choice p makes: the four
// Options fields as p sets them, then the fixed allow-list and limit choices
// of p's service.
func Switches(p Profile) []Switch {
	prefix := p.switchPrefix()
	o := p.options()
	r := p.switchRecords()
	return append([]Switch{
		{Name: prefix + ".exists-true-empty-value", Value: o.ExistsTrue.String(), Ref: switchRef, Record: r.existsTrue},
		{Name: prefix + ".empty-input", Value: o.EmptyInput.String(), Ref: switchRef, Record: r.emptyInput},
		{Name: prefix + ".bare-number", Value: o.BareNumber.String(), Ref: switchRef, Record: r.bareNumber},
		{Name: prefix + ".malformed-or", Value: o.MalformedOr.String(), Ref: switchRef, Record: r.malformedOr},
	}, p.fixedSwitches()...)
}
