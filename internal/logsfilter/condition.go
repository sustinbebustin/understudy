package logsfilter

import (
	"regexp"
	"strings"
)

// op is a comparison operator of a JSON or space-delimited condition.
type op uint8

const (
	opEq op = iota
	opNe
	opLt
	opLe
	opGt
	opGe
)

func (o op) String() string {
	switch o {
	case opEq:
		return "="
	case opNe:
		return "!="
	case opLt:
		return "<"
	case opLe:
		return "<="
	case opGt:
		return ">"
	case opGe:
		return ">="
	}
	return "?"
}

// value is the right-hand side of a condition.
//
//sumtype:decl
type value interface{ isValue() }

// anyValue is a bare *: the subject exists as a scalar.
type anyValue struct{}

// numberValue compares numerically.
type numberValue struct{ n float64 }

// globValue is a whole-string comparison where * matches any run of
// characters, including none.
type globValue struct{ glob string }

// regexValue is an unanchored regular expression search.
type regexValue struct{ re *regexp.Regexp }

func (anyValue) isValue()    {}
func (numberValue) isValue() {}
func (globValue) isValue()   {}
func (regexValue) isValue()  {}

// condition is one comparison. Ordering operators take only numbers
// (aws-cdk validateStringOperator).
type condition struct {
	op    op
	value value
}

func newCondition(o op, v value, offset int) (condition, *Error) {
	if _, isNumber := v.(numberValue); !isNumber && o != opEq && o != opNe {
		return condition{}, invalidPattern("operator "+o.String()+" needs a number", offset)
	}
	return condition{op: o, value: v}, nil
}

// countRegex enforces the quota of two regular expressions per JSON or
// space-delimited pattern.
func countRegex(v value, count *int, offset int) *Error {
	if _, isRegex := v.(regexValue); !isRegex {
		return nil
	}
	*count++
	if *count > maxRegexPerPattern {
		return invalidPattern("at most 2 regular expressions are allowed in a pattern", offset)
	}
	return nil
}

// subject is what a condition is tested against.
//
//sumtype:decl
type subject interface{ isSubject() }

// fieldSubject is a space-delimited field: text that compares numerically
// when it reads as a number.
type fieldSubject string

// stringNode is a JSON string.
type stringNode string

// numberNode is a JSON number with its source text.
type numberNode string

// boolNode is a JSON boolean.
type boolNode bool

// nullNode is a JSON null.
type nullNode struct{}

func (fieldSubject) isSubject() {}
func (stringNode) isSubject()   {}
func (numberNode) isSubject()   {}
func (boolNode) isSubject()     {}
func (nullNode) isSubject()     {}

// holds reports whether the condition is true of one subject. != is the
// negation of = on a present scalar.
func (c condition) holds(sub subject) bool {
	n, isNumeric := numeric(sub)
	v, isNumber := c.value.(numberValue)
	ordered := isNumeric && isNumber
	switch c.op {
	case opEq:
		return c.equal(sub)
	case opNe:
		return !c.equal(sub)
	case opLt:
		return ordered && n < v.n
	case opLe:
		return ordered && n <= v.n
	case opGt:
		return ordered && n > v.n
	case opGe:
		return ordered && n >= v.n
	}
	return false
}

func (c condition) equal(sub subject) bool {
	switch v := c.value.(type) {
	case anyValue:
		return true
	case numberValue:
		n, ok := numeric(sub)
		return ok && n == v.n
	case globValue:
		t, ok := text(sub)
		return ok && globMatch(v.glob, t)
	case regexValue:
		t, ok := text(sub)
		return ok && v.re.MatchString(t)
	}
	return false
}

// numeric reads a subject as a number: a delimited field that parses as one,
// or a JSON number. JSON strings never compare numerically. A number beyond
// float64 range reads as ±Inf, so it still orders against every finite value.
func numeric(sub subject) (float64, bool) {
	switch s := sub.(type) {
	case fieldSubject:
		n, form := parseNumber(string(s))
		return n, form != notNumber
	case numberNode:
		// JSON's number syntax is a subset of the pattern number rule.
		n, form := parseNumber(string(s))
		return n, form != notNumber
	case stringNode, boolNode, nullNode:
	}
	return 0, false
}

// text reads a subject as a string: a field, a JSON string, or a JSON
// number's source text.
func text(sub subject) (string, bool) {
	switch s := sub.(type) {
	case fieldSubject:
		return string(s), true
	case stringNode:
		return string(s), true
	case numberNode:
		return string(s), true
	case boolNode, nullNode:
	}
	return "", false
}

// globMatch matches s against a pattern whose only metacharacter is *.
func globMatch(glob, s string) bool {
	parts := strings.Split(glob, "*")
	if len(parts) == 1 {
		return glob == s
	}
	head, last := parts[0], parts[len(parts)-1]
	if !strings.HasPrefix(s, head) {
		return false
	}
	s = s[len(head):]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(s, part)
		if i < 0 {
			return false
		}
		s = s[i+len(part):]
	}
	return strings.HasSuffix(s, last)
}
