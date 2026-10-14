package ruler

import "strconv"

// CompileError is a pattern the profile's service rejects. Detail is the
// reason text: Ruler's own message for grammar and operator errors (the text
// EventBridge and SNS put on the wire), or the service's message for its
// limits. Cause says what failed in a form tests and callers can switch on.
type CompileError struct {
	Cause  Cause
	Detail string
	// message is the profile's wording, rendered by Compile.
	message string
}

// Message renders the error as the profile's service words it:
//
//   - EventBridge: the full InvalidEventPatternException message,
//     "Event pattern is not valid. Reason: <detail>".
//   - SNS: the text after "Invalid parameter: " on SetSubscriptionAttributes
//     ("FilterPolicy: <detail>", or the bare scope message for a nested policy
//     under MessageAttributes scope); Subscribe puts "Attributes Reason: "
//     before it.
//   - LambdaESM: the generic InvalidParameterValueException text, which hides
//     the reason.
//
// A CompileError that Compile did not return, such as a test double, has
// no profile wording, and Message returns its Detail.
func (e *CompileError) Message() string {
	if e.message == "" {
		return e.Detail
	}
	return e.message
}

// Error returns Message, so a CompileError can travel as an error.
func (e *CompileError) Error() string { return e.Message() }

// Cause is what made a pattern invalid.
//
//sumtype:decl
type Cause interface{ isCause() }

// InvalidJSON is a pattern that is not well-formed JSON.
type InvalidJSON struct{}

// InvalidStructure is well-formed JSON that is not a pattern: a top level
// that is not an object, an empty object or array, a non-array leaf, a match
// value of the wrong type, or content after the pattern.
type InvalidStructure struct{}

// InvalidOperand is an operator the profile supports, used with an operand
// Ruler rejects.
type InvalidOperand struct{ Operator Operator }

// UnknownOperator is an operator name Ruler does not know (names are
// case-sensitive, so "EXISTS" is unknown).
type UnknownOperator struct{ Name string }

// UnsupportedOperator is an operator Ruler knows that the profile's service
// does not accept.
type UnsupportedOperator struct{ Operator Operator }

// LimitExceeded is a valid pattern over one of the service's limits.
type LimitExceeded struct{ Limit Limit }

func (InvalidJSON) isCause()         {}
func (InvalidStructure) isCause()    {}
func (InvalidOperand) isCause()      {}
func (UnknownOperator) isCause()     {}
func (UnsupportedOperator) isCause() {}
func (LimitExceeded) isCause()       {}

// Operator is one operator form of the pattern grammar. Bare values
// (strings, numbers, true, false, null) are not operators: every profile
// accepts them.
type Operator uint8

const (
	// OperatorPrefix is {"prefix": "s"}.
	OperatorPrefix Operator = iota + 1
	// OperatorPrefixEqualsIgnoreCase is {"prefix": {"equals-ignore-case": "s"}}.
	OperatorPrefixEqualsIgnoreCase
	// OperatorSuffix is {"suffix": "s"}.
	OperatorSuffix
	// OperatorSuffixEqualsIgnoreCase is {"suffix": {"equals-ignore-case": "s"}}.
	OperatorSuffixEqualsIgnoreCase
	// OperatorEqualsIgnoreCase is {"equals-ignore-case": "s"}.
	OperatorEqualsIgnoreCase
	// OperatorWildcard is {"wildcard": "a*b"}.
	OperatorWildcard
	// OperatorAnythingBut is {"anything-but": value-or-list}.
	OperatorAnythingBut
	// OperatorAnythingButPrefix is {"anything-but": {"prefix": ...}}.
	OperatorAnythingButPrefix
	// OperatorAnythingButSuffix is {"anything-but": {"suffix": ...}}.
	OperatorAnythingButSuffix
	// OperatorAnythingButEqualsIgnoreCase is {"anything-but": {"equals-ignore-case": ...}}.
	OperatorAnythingButEqualsIgnoreCase
	// OperatorAnythingButWildcard is {"anything-but": {"wildcard": ...}}.
	OperatorAnythingButWildcard
	// OperatorNumeric is {"numeric": [...]}.
	OperatorNumeric
	// OperatorExists is {"exists": true|false}.
	OperatorExists
	// OperatorCIDR is {"cidr": "10.0.0.0/24"}.
	OperatorCIDR
	// OperatorOr is the "$or" relationship.
	OperatorOr
)

// String returns the operator as written in a pattern.
func (o Operator) String() string {
	switch o {
	case OperatorPrefix:
		return "prefix"
	case OperatorPrefixEqualsIgnoreCase:
		return "prefix/equals-ignore-case"
	case OperatorSuffix:
		return "suffix"
	case OperatorSuffixEqualsIgnoreCase:
		return "suffix/equals-ignore-case"
	case OperatorEqualsIgnoreCase:
		return "equals-ignore-case"
	case OperatorWildcard:
		return "wildcard"
	case OperatorAnythingBut:
		return "anything-but"
	case OperatorAnythingButPrefix:
		return "anything-but/prefix"
	case OperatorAnythingButSuffix:
		return "anything-but/suffix"
	case OperatorAnythingButEqualsIgnoreCase:
		return "anything-but/equals-ignore-case"
	case OperatorAnythingButWildcard:
		return "anything-but/wildcard"
	case OperatorNumeric:
		return "numeric"
	case OperatorExists:
		return "exists"
	case OperatorCIDR:
		return "cidr"
	case OperatorOr:
		return "$or"
	}
	return "unknown"
}

// Limit is a service limit on a valid pattern.
type Limit uint8

const (
	// LimitSize is the pattern's length (SNS: 256 KB).
	LimitSize Limit = iota + 1
	// LimitNesting is the nesting depth (SNS MessageAttributes scope: none).
	LimitNesting
	// LimitKeys is the number of keys (SNS: 5 leaf keys per $or branch).
	LimitKeys
	// LimitCombinations is the expanded size of the pattern (SNS: 150
	// combinations; EventBridge: 1000 $or combinations; every profile: the
	// engine's expansion budget).
	LimitCombinations
	// LimitWildcardComplexity is the wildcard cost (EventBridge: Ruler's
	// complexity evaluator; SNS: 100 points).
	LimitWildcardComplexity
	// LimitWildcardsPerPattern is the "*" count of one pattern (SNS: 3).
	LimitWildcardsPerPattern
)

// String names the limit.
func (l Limit) String() string {
	switch l {
	case LimitSize:
		return "size"
	case LimitNesting:
		return "nesting"
	case LimitKeys:
		return "keys"
	case LimitCombinations:
		return "combinations"
	case LimitWildcardComplexity:
		return "wildcard-complexity"
	case LimitWildcardsPerPattern:
		return "wildcards-per-pattern"
	}
	return "unknown"
}

// EventError is an event Match cannot evaluate: malformed JSON, or JSON whose
// top level is not an object.
type EventError struct {
	Detail string
}

// Error returns the detail; "Event must be a JSON object" is Ruler's text.
func (e *EventError) Error() string { return e.Detail }

// SearchBudgetError is a Match or MatchAttributes call that gave up before
// deciding: it needed more than Steps units of work (maxSearchSteps), because
// the event joins enough arrays that an array-consistent search grows
// exponentially, or because a pattern with many "$or" branches looks at many
// fields of the event in each of them. It is an understudy engine bound with
// no AWS counterpart. The event neither matched nor failed to match; the
// pattern and the event are unchanged. Callers record it (for example in the
// journal, naming the pattern's owner) and do not deliver.
type SearchBudgetError struct {
	Steps int
}

// Error says what was given up and why.
func (e *SearchBudgetError) Error() string {
	return "ruler: matching gave up after " + strconv.Itoa(e.Steps) +
		" search steps without a result; either the event nests arrays under one path deeply enough that " +
		"array-consistent matching would take exponential time, or the pattern's \"$or\" branches each examine " +
		"many fields of a large event (an understudy engine bound, not an AWS limit)"
}

// Input is what a compiled pattern is matched against, fixed by its profile.
type Input uint8

const (
	// InputEvent is a JSON event, matched with Match: EventBridge, Lambda
	// event source mappings and the SNS MessageBody scope.
	InputEvent Input = iota + 1
	// InputAttributes is a set of SNS message attributes, matched with
	// MatchAttributes: the SNS MessageAttributes scope.
	InputAttributes
)

// InputError is a pattern matched against the wrong kind of input: Match was
// called with a pattern for message attributes, or MatchAttributes with a
// pattern for JSON events. It is a caller bug; the pattern is unchanged and
// still valid for its own input.
type InputError struct {
	// Want is the input the pattern's profile compiles for.
	Want Input
}

// Error names the call that fits the pattern's input.
func (e *InputError) Error() string {
	if e.Want == InputAttributes {
		return "ruler: the pattern was compiled for the SNS MessageAttributes scope and matches message attributes; " +
			"call MatchAttributes instead of Match"
	}
	return "ruler: the pattern was compiled for JSON events (EventBridge, Lambda or the SNS MessageBody scope); " +
		"call Match instead of MatchAttributes"
}
