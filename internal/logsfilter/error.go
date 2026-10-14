package logsfilter

import (
	"strconv"
	"unicode/utf8"
)

// maxPatternLength is the Smithy length constraint on filterPattern.
const maxPatternLength = 1024

// Error is a pattern AWS would reject, which the logs service returns as an
// InvalidParameterException (HTTP 400). Cause says which rule rejected it;
// Message is the wire text. Only Compile builds one, so both are always set.
type Error struct {
	cause   Cause
	message string
}

// Cause returns the rule the pattern broke.
func (e *Error) Cause() Cause { return e.cause }

// Message returns the InvalidParameterException text.
func (e *Error) Message() string { return e.message }

// Error returns the wire message.
func (e *Error) Error() string { return e.message }

// Cause is the rule a rejected pattern broke.
type Cause uint8

const (
	// CauseTooLong is a pattern over the 1024 character length constraint.
	CauseTooLong Cause = iota + 1
	// CauseInvalidTerm is an unquoted term with a character outside the
	// bare-term set.
	CauseInvalidTerm
	// CauseSyntax is every other grammar or quota violation.
	CauseSyntax
)

// String names the cause for logs and test output.
func (c Cause) String() string {
	switch c {
	case CauseTooLong:
		return "too-long"
	case CauseInvalidTerm:
		return "invalid-term"
	case CauseSyntax:
		return "syntax"
	}
	return "unknown"
}

// tooLong is the recorded validation text for an over-long pattern
// (logs-filter-patterns.md section 7, recorded for PutMetricFilter).
func tooLong(pattern string) *Error {
	return &Error{cause: CauseTooLong, message: "1 validation error detected: Value '" + pattern +
		"' at 'filterPattern' failed to satisfy constraint: Member must have length less than or equal to " +
		strconv.Itoa(maxPatternLength)}
}

func checkLength(pattern string) *Error {
	if utf8.RuneCountInString(pattern) > maxPatternLength {
		return tooLong(pattern)
	}
	return nil
}

// invalidTerm refuses an unquoted term with characters outside the
// bare-term set, quoting only those characters, each once in order of
// first appearance. The single-character form is recorded
// (testdata/parity/logs/R10/03-filter-12.json, 03-filter-13.json); how
// AWS lists several is inferred.
func invalidTerm(offending string) *Error {
	return &Error{cause: CauseInvalidTerm, message: "Invalid character(s) in term '" + offending + "'"}
}

// invalidPattern reports every other syntax error. AWS's texts for these are
// unrecorded, so the message names the problem and its byte offset.
func invalidPattern(detail string, offset int) *Error {
	return &Error{cause: CauseSyntax, message: "Invalid filter pattern: " + detail + " at offset " + strconv.Itoa(offset) + "."}
}
