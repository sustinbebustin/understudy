package asl

import (
	"errors"
	"math/big"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// choice runs a Choice state (stepfunctions-asl.md 6, 11.3): the rules test
// the effective input in order, the first that holds names the next state,
// else Default does; the output is the effective input through OutputPath.
func (s *stepper) choice(st choiceState, j stepJob) {
	entered := s.emit("ChoiceStateEntered", j.previous, stateEntered(st.name, j.input))
	sc := s.scopeOf(st.name)
	effective, f := sc.selectFrom(st.io.input, j.input)
	if f != nil {
		s.stateFailed(st.name, entered, f)
		return
	}
	next, err := sc.choose(st, effective)
	var bad *patternError
	switch {
	case errors.As(err, &bad):
		s.stateFailed(st.name, entered, Terminal{Name: errRuntime, Cause: bad.Error()})
		return
	case err != nil:
		// The switch's declared cause carries the 4.6 prefix whatever its
		// error name (inferred, ASL-08).
		s.raise(ruleFailure(s.rules.MissingVariable, runtimeCause(st.name, entered, err.Error())), entered)
		return
	case next == "":
		s.stateFailed(st.name, entered, ruleFailure(s.rules.NoMatch,
			"No Choice rule of the state '"+st.name+"' matched its input, and the state has no Default."))
		return
	}
	output, f := sc.selectFrom(st.io.output, effective)
	if f == nil {
		f = dataLimit(st.name, output)
	}
	if f != nil {
		s.stateFailed(st.name, entered, f)
		return
	}
	exited := s.emit("ChoiceStateExited", entered, stateExited(st.name, output))
	s.arm(s.at, stepJob{fr: s.fr, state: next, input: output, previous: exited})
}

// choose is the state the first matching rule names, else Default; "" when
// neither applies. The error is a Variable or Path operand that selects
// nothing, or a *patternError.
func (sc scope) choose(st choiceState, input document.Value) (string, error) {
	for _, r := range st.choices {
		ok, err := sc.holds(r.when, input)
		if err != nil {
			return "", err
		}
		if ok {
			return r.next, nil
		}
	}
	return st.otherwise, nil
}

// holds evaluates a rule on input; And and Or short-circuit
// (stepfunctions-asl.md 6.1).
func (sc scope) holds(c condition, input document.Value) (bool, error) {
	switch c := c.(type) {
	case compare:
		v, err := c.variable.Eval(input, sc.context)
		if err != nil {
			// IsPresent is the one test of an absent Variable
			// (spec#data-test-expression-for-jsonpath-choice-rules).
			if want, ok := c.operand.(document.Bool); ok && c.op == "IsPresent" {
				return !bool(want), nil
			}
			return false, err
		}
		return test(c.op, v, c.operand)
	case comparePath:
		v, err := c.variable.Eval(input, sc.context)
		if err != nil {
			return false, err
		}
		w, err := c.operand.Eval(input, sc.context)
		if err != nil {
			return false, err
		}
		return test(c.op, v, w)
	case allOf:
		return sc.shortCircuit(c.rules, input, false)
	case anyOf:
		return sc.shortCircuit(c.rules, input, true)
	case not:
		ok, err := sc.holds(c.rule, input)
		return !ok, err
	}
	return false, nil
}

// shortCircuit evaluates rules in order and returns stop at the first that
// evaluates to stop, else !stop: And stops on false, Or on true.
func (sc scope) shortCircuit(rules []condition, input document.Value, stop bool) (bool, error) {
	for _, r := range rules {
		ok, err := sc.holds(r, input)
		if err != nil {
			return false, err
		}
		if ok == stop {
			return stop, nil
		}
	}
	return !stop, nil
}

// test applies operator op to the Variable's value v and the operand w. Two
// values not both of the operator's type compare false, never an error
// (stepfunctions-asl.md 6.2).
func test(op string, v, w document.Value) (bool, error) {
	if want, ok := w.(document.Bool); ok && strings.HasPrefix(op, "Is") {
		return is(op, v) == bool(want), nil
	}
	switch {
	case op == "StringMatches":
		a, okA := v.(document.String)
		p, okP := w.(document.String)
		if !okA || !okP {
			return false, nil
		}
		return matches(string(p), string(a))
	case op == "BooleanEquals":
		a, okA := v.(document.Bool)
		b, okB := w.(document.Bool)
		return okA && okB && a == b, nil
	case strings.HasPrefix(op, "String"):
		a, okA := v.(document.String)
		b, okB := w.(document.String)
		return okA && okB && relation(op, utf16Compare(string(a), string(b))), nil
	case strings.HasPrefix(op, "Numeric"):
		c, ok := numericCompare(v, w)
		return ok && relation(op, c), nil
	case strings.HasPrefix(op, "Timestamp"):
		a, okA := timestampOf(v)
		b, okB := timestampOf(w)
		return okA && okB && relation(op, a.Compare(b)), nil
	}
	return false, nil
}

// is the type test of an Is* operator on a present value.
func is(op string, v document.Value) bool {
	switch op {
	case "IsNull":
		_, ok := v.(document.Null)
		return ok
	case "IsPresent":
		return true
	case "IsNumeric":
		_, ok := v.(document.Number)
		return ok
	case "IsString":
		_, ok := v.(document.String)
		return ok
	case "IsBoolean":
		_, ok := v.(document.Bool)
		return ok
	case "IsTimestamp":
		_, ok := timestampOf(v)
		return ok
	}
	return false
}

// relation reports whether c, a three-way comparison, satisfies op's
// relation suffix.
func relation(op string, c int) bool {
	switch {
	case strings.HasSuffix(op, "LessThanEquals"):
		return c <= 0
	case strings.HasSuffix(op, "GreaterThanEquals"):
		return c >= 0
	case strings.HasSuffix(op, "LessThan"):
		return c < 0
	case strings.HasSuffix(op, "GreaterThan"):
		return c > 0
	}
	return c == 0
}

// numericCompare compares two numbers by value: 0 and 0.0 are equal
// (stepfunctions-asl.md 6.2).
func numericCompare(v, w document.Value) (int, bool) {
	a, okA := v.(document.Number)
	b, okB := w.(document.Number)
	if !okA || !okB {
		return 0, false
	}
	x, okX := new(big.Rat).SetString(string(a))
	y, okY := new(big.Rat).SetString(string(b))
	if !okX || !okY {
		return 0, false
	}
	return x.Cmp(y), true
}

// utf16Compare orders strings by UTF-16 code unit, as Java's compareTo does
// (stepfunctions-asl.md 6.2).
func utf16Compare(a, b string) int {
	x, y := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := range min(len(x), len(y)) {
		if x[i] != y[i] {
			if x[i] < y[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(x) < len(y):
		return -1
	case len(x) > len(y):
		return 1
	}
	return 0
}

// timestampOf reads v as a timestamp of the documented RFC 3339 profile;
// Timestamp operators compare instants, across offsets and precisions
// (inferred, ASL-07).
func timestampOf(v document.Value) (time.Time, bool) {
	s, ok := v.(document.String)
	if !ok {
		return time.Time{}, false
	}
	return parseTimestamp(string(s))
}

// patternError is a StringMatches pattern with an unpaired backslash, which
// the spec says throws at run time (stepfunctions-asl.md 6.3); the error and
// cause are understudy's (ASL-07).
type patternError struct{ pattern string }

func (e *patternError) Error() string {
	return "The StringMatches pattern '" + e.pattern + "' has a backslash that escapes neither '*' nor '\\'"
}

// matches reports whether s matches pattern: '*' matches zero or more
// characters, '\*' and '\\' are a literal star and backslash, and nothing
// else is special (stepfunctions-asl.md 6.3).
func matches(pattern, s string) (bool, error) {
	// segments are the literal runs between unescaped stars.
	segments := []string{""}
	var lit strings.Builder
	escaped := false
	for _, r := range pattern {
		switch {
		case escaped:
			if r != '*' && r != '\\' {
				return false, &patternError{pattern: pattern}
			}
			lit.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '*':
			segments[len(segments)-1] = lit.String()
			segments = append(segments, "")
			lit.Reset()
		default:
			lit.WriteRune(r)
		}
	}
	if escaped {
		return false, &patternError{pattern: pattern}
	}
	segments[len(segments)-1] = lit.String()
	return glob(segments, s), nil
}

// glob matches s against the literal segments of a pattern joined by stars:
// the first is a prefix, the last a suffix, and each one between is taken at
// its leftmost place after the one before, which is never worse for the
// rest.
func glob(segments []string, s string) bool {
	if len(segments) == 1 {
		return s == segments[0]
	}
	first, last := segments[0], segments[len(segments)-1]
	if len(s) < len(first)+len(last) || !strings.HasPrefix(s, first) || !strings.HasSuffix(s, last) {
		return false
	}
	rest := s[len(first) : len(s)-len(last)]
	for _, seg := range segments[1 : len(segments)-1] {
		i := strings.Index(rest, seg)
		if i < 0 {
			return false
		}
		rest = rest[i+len(seg):]
	}
	return true
}
