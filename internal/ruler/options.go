package ruler

import (
	"fmt"
)

// Options selects the four behaviors where Event Ruler and a service's
// documentation disagree (matching-engines.md section 10). The zero value is
// Ruler's behavior for each. A profile fixes the fields its service's
// recordings settle and leaves the rest open; the owning service lifts each
// open field into a fidelity switch with the metadata Switches returns. This
// package stays a leaf and does not import the fidelity ledger.
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

	// stringRule and wildcardRule are fixed by a profile from its service's
	// recordings and never open, so a caller cannot set them.
	stringRule   stringRule
	wildcardRule wildcardRule
}

// stringRule is which values a string rule such as ["10"] matches.
type stringRule uint8

const (
	// stringRuleStringOnly is Ruler: only a string with that text (V10).
	stringRuleStringOnly stringRule = iota
	// stringRuleNumberText is Lambda: a number with the same source text as
	// well, so ["10"] matches 10 but not 10.0 (lambda/R03 H8-02,
	// logs/R03/H8-02-01.json).
	stringRuleNumberText
)

// wildcardRule is what a {"wildcard": ...} rule matches.
type wildcardRule uint8

const (
	// wildcardRuleGlob is Ruler: the strings the glob matches.
	wildcardRuleGlob wildcardRule = iota
	// wildcardRuleNothing is Lambda: it accepts {"wildcard":"*.png"} but
	// invoked for neither a.png nor a.jpg (lambda/R03 H13-02,
	// logs/R03/H13-02-01.json). The step had no positive control, so this
	// rests on one observation.
	wildcardRuleNothing
)

// ExistsTrue is the meaning of {"exists": true} for null and empty-string
// values (matching-engines.md 4.4, V30, V31; human recording H2).
type ExistsTrue uint8

const (
	// ExistsTrueAnyValue is Ruler: any value exists, including null and "".
	ExistsTrueAnyValue ExistsTrue = iota
	// ExistsTrueNonEmpty is the SNS documentation: the key "must have a
	// non-null and non-empty value". SNS does this in MessageAttributes
	// scope, where an empty String value does not exist (sns/R09 H2-attr).
	ExistsTrueNonEmpty
	// ExistsTrueNonNull is SNS in MessageBody scope: null does not exist,
	// while "", 0 and false do (sns/R09 H2-body). [] and {} hold no value,
	// so they fail under every setting.
	ExistsTrueNonNull
)

// String returns the switch value as the coverage report prints it.
func (e ExistsTrue) String() string {
	switch e {
	case ExistsTrueAnyValue:
		return "any-value"
	case ExistsTrueNonEmpty:
		return "non-empty"
	case ExistsTrueNonNull:
		return "non-null"
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
	// MalformedOrOrdinaryKey is EventBridge (events/R14 H10): a rejected
	// pattern is read again with "$or" as a plain key, as Ruler's
	// Machine.addRule falls back to, and the plain reading's message is
	// reported when it rejects the pattern too, as for {"$or":[{"m":["x"]}]}
	// ("Unrecognized match type m"). A shape the plain reading accepts,
	// such as {"$or":{"m":["x"]}}, is still rejected with the strict
	// message: no service accepts a malformed "$or".
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

// Switch is the fidelity-ledger metadata of one behavior choice point of a
// profile. The owning service turns it into a fidelity.Switch and labels
// it, aws-recorded where its service's recordings settle the value. The
// Options switches follow executed Ruler source; the fixed ones follow
// service documentation, inference or a recording.
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

const switchRef = "docs/research/matching-engines.md#10"

// optionSwitch is the Switch of the Options field named field, which a
// profile with switch prefix leaves open at value.
func optionSwitch(prefix, field string, value fmt.Stringer, record string) Switch {
	return Switch{Name: prefix + "." + field, Value: value.String(), Ref: switchRef, Record: record}
}

// Switches returns the metadata of every switch p names: the Options
// fields p leaves open, as p sets them, then the fixed allow-list and limit
// choices of p's service.
func Switches(p Profile) []Switch {
	return append(p.optionSwitches(), p.fixedSwitches()...)
}
