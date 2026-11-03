package policydoc

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// family is how a condition test compares one request value with one policy
// value (iam-evaluation.md 6.3 to 6.9).
type family uint8

const (
	familyString     family = iota + 1 // exact, case-sensitive
	familyStringFold                   // exact, ignoring case
	familyStringLike                   // glob with * and ?
	familyNumeric
	familyDate
	familyBool
	familyBinary
	familyIP
	familyArn
)

// order is the comparison a Numeric or Date test makes, request value first.
type order uint8

const (
	orderEqual order = iota
	orderLess
	orderLessOrEqual
	orderGreater
	orderGreaterOrEqual
)

// shape is a test as its family, its order and whether it negates.
type shape struct {
	family  family
	order   order
	negated bool
}

// shape returns how t compares. A negated test is NOR over the policy values
// of its positive test (iam-evaluation.md 6.1, 6.14). Null has no shape; it
// is evaluated on its own (nullHolds).
func (t Test) shape() shape {
	switch t {
	case StringEquals, StringNotEquals:
		return shape{family: familyString, negated: t == StringNotEquals}
	case StringEqualsIgnoreCase, StringNotEqualsIgnoreCase:
		return shape{family: familyStringFold, negated: t == StringNotEqualsIgnoreCase}
	case StringLike, StringNotLike:
		return shape{family: familyStringLike, negated: t == StringNotLike}
	case NumericEquals, NumericNotEquals:
		return shape{family: familyNumeric, negated: t == NumericNotEquals}
	case NumericLessThan:
		return shape{family: familyNumeric, order: orderLess}
	case NumericLessThanEquals:
		return shape{family: familyNumeric, order: orderLessOrEqual}
	case NumericGreaterThan:
		return shape{family: familyNumeric, order: orderGreater}
	case NumericGreaterThanEquals:
		return shape{family: familyNumeric, order: orderGreaterOrEqual}
	case DateEquals, DateNotEquals:
		return shape{family: familyDate, negated: t == DateNotEquals}
	case DateLessThan:
		return shape{family: familyDate, order: orderLess}
	case DateLessThanEquals:
		return shape{family: familyDate, order: orderLessOrEqual}
	case DateGreaterThan:
		return shape{family: familyDate, order: orderGreater}
	case DateGreaterThanEquals:
		return shape{family: familyDate, order: orderGreaterOrEqual}
	case Bool:
		return shape{family: familyBool}
	case BinaryEquals:
		return shape{family: familyBinary}
	case IPAddress, NotIPAddress:
		return shape{family: familyIP, negated: t == NotIPAddress}
	case ArnEquals, ArnLike, ArnNotEquals, ArnNotLike:
		return shape{family: familyArn, negated: t == ArnNotEquals || t == ArnNotLike}
	case Null:
	}
	return shape{}
}

// takesVariables reports whether the family's policy values may hold policy
// variables: String and ARN operators (iam-evaluation.md 7.1). Bool takes
// its value literally (switch iam.condition.bool-variables "literal",
// aws-recorded, IAM-EVAL-01).
func (f family) takesVariables() bool {
	return f == familyString || f == familyStringFold || f == familyStringLike || f == familyArn
}

// policyValue is one policy value ready to compare.
type policyValue struct {
	// text is the value as written, or with its variables substituted and
	// escapes decoded for a family that takes variables.
	text   string
	tokens []token
	// resolved is false for a value whose variable has no value: it never
	// satisfies a positive test and contributes no match to a negated one
	// (switch iam.variables.unresolved "value-null", IAM-EVAL-16).
	resolved bool
}

// holds evaluates cl by the truth table of iam-evaluation.md 6.14, reporting
// to s every key and variable the context marks Unsupplied, which then
// evaluates as absent.
func (s *statementEval) holds(cl Clause) bool {
	value, presence := s.lookup(cl.Key)
	op := cl.Operator
	if op.Test == Null {
		return nullHolds(cl.Values, presence == Present, value.Values)
	}
	if presence != Present {
		switch {
		case op.IfExists, op.Set == ForAllValues:
			return true
		case op.Set == ForAnyValue:
			return false
		default:
			return op.Test.shape().negated
		}
	}
	sh := op.Test.shape()
	policy := s.policyValues(sh.family, cl.Values)
	forValue := func(r string) bool { return sh.holdsFor(r, policy) }
	switch op.Set {
	case ForAllValues:
		// "" is a value, not an empty set (IAM-EVAL-04, aws-recorded).
		return !slices.ContainsFunc(value.Values, func(r string) bool { return !forValue(r) })
	case SingleValued:
		// A plain operator on a multivalued key never holds, negated or
		// not (switch iam.condition.multivalued-plain-operator "no-match":
		// aws-recorded for StringEquals, IAM-EVAL-02; inferred for the
		// negated operators).
		if value.Multivalued {
			return false
		}
		return slices.ContainsFunc(value.Values, forValue)
	case ForAnyValue:
		// A set qualifier on a single-valued key reads it as a set of one
		// (IAM-EVAL-03, aws-recorded).
		return slices.ContainsFunc(value.Values, forValue)
	}
	return false
}

// policyValues prepares a clause's values for family f.
func (s *statementEval) policyValues(f family, values []Value) []policyValue {
	out := make([]policyValue, 0, len(values))
	for _, v := range values {
		text := valueText(v)
		if !f.takesVariables() {
			out = append(out, policyValue{text: text, resolved: true})
			continue
		}
		e := expand(text, s.version, s.ctx)
		for _, name := range e.unsupplied {
			s.report(name)
		}
		out = append(out, policyValue{text: e.text(), tokens: e.tokens, resolved: e.resolved})
	}
	return out
}

// holdsFor is the operator for one request value r: OR over the policy values
// of the test, negated for a negated operator.
func (sh shape) holdsFor(r string, policy []policyValue) bool {
	matched := slices.ContainsFunc(policy, func(p policyValue) bool { return p.resolved && sh.test(r, p) })
	return matched != sh.negated
}

// test is the positive test of one request value against one policy value.
func (sh shape) test(r string, p policyValue) bool {
	switch sh.family {
	case familyString:
		return p.text == r
	case familyStringFold:
		// Go's Unicode simple folding (IAM-EVAL-08, inferred).
		return strings.EqualFold(p.text, r)
	case familyStringLike:
		return match(p.tokens, r)
	case familyNumeric:
		return compareNumbers(r, p.text, sh.order)
	case familyDate:
		return compareDates(r, p.text, sh.order)
	case familyBool:
		rb, rok := parseBool(r)
		pb, pok := parseBool(p.text)
		return rok && pok && rb == pb
	case familyBinary:
		return binaryEquals(r, p.text)
	case familyIP:
		return ipContains(p.text, r)
	case familyArn:
		return arnTokensMatch(p.tokens, r)
	}
	return false
}

// nullHolds is the Null operator, whose set qualifier and IfExists suffix
// are ignored: "true" holds when the key is null and "false" when it is not,
// several values OR, and any other value never holds. A key is null when it
// is absent or present with no values (inferred); "" is a value (switch
// iam.condition.empty-value "value", aws-recorded, IAM-EVAL-05).
func nullHolds(values []Value, present bool, request []string) bool {
	isNull := !present || len(request) == 0
	return slices.ContainsFunc(values, func(v Value) bool {
		b, ok := parseBool(valueText(v))
		return ok && b == isNull
	})
}

// parseBool reads true or false in any case (IAM-EVAL-08, inferred).
func parseBool(s string) (value, ok bool) {
	switch {
	case strings.EqualFold(s, "true"):
		return true, true
	case strings.EqualFold(s, "false"):
		return false, true
	}
	return false, false
}

// compareNumbers compares r with p when both are decimal numbers: an
// optional sign, digits, an optional fraction and an optional exponent
// (IAM-EVAL-09, inferred). Either unparsable gives false.
func compareNumbers(r, p string, o order) bool {
	rn, rok := parseNumber(r)
	pn, pok := parseNumber(p)
	if !rok || !pok {
		return false
	}
	switch {
	case rn < pn:
		return o == orderLess || o == orderLessOrEqual
	case rn > pn:
		return o == orderGreater || o == orderGreaterOrEqual
	}
	return o == orderEqual || o == orderLessOrEqual || o == orderGreaterOrEqual
}

func parseNumber(s string) (float64, bool) {
	if !isDecimal(s) {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return n, true
}

// isDecimal reports whether s is [+-]digits[.digits][(e|E)[+-]digits].
func isDecimal(s string) bool {
	digits := func(i int) int {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		return j
	}
	sign := func(i int) int {
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			return i + 1
		}
		return i
	}
	i := sign(0)
	j := digits(i)
	if j == i {
		return false
	}
	if j < len(s) && s[j] == '.' {
		k := digits(j + 1)
		if k == j+1 {
			return false
		}
		j = k
	}
	if j < len(s) && (s[j] == 'e' || s[j] == 'E') {
		i = sign(j + 1)
		k := digits(i)
		if k == i {
			return false
		}
		j = k
	}
	return j == len(s)
}

// compareDates compares r with p as instants when both are dates in the W3C
// profile of ISO 8601 or integer epoch seconds (IAM-EVAL-06, inferred).
// Either unparsable gives false.
func compareDates(r, p string, o order) bool {
	rt, rok := parseDate(r)
	pt, pok := parseDate(p)
	if !rok || !pok {
		return false
	}
	switch c := rt.Compare(pt); {
	case c < 0:
		return o == orderLess || o == orderLessOrEqual
	case c > 0:
		return o == orderGreater || o == orderGreaterOrEqual
	}
	return o == orderEqual || o == orderLessOrEqual || o == orderGreaterOrEqual
}

// parseDate reads YYYY, YYYY-MM, YYYY-MM-DD, YYYY-MM-DDThh:mmTZD,
// YYYY-MM-DDThh:mm:ssTZD or YYYY-MM-DDThh:mm:ss.sTZD (the W3C profile,
// https://www.w3.org/TR/NOTE-datetime), then integer epoch seconds; four
// digits read as a year.
func parseDate(s string) (time.Time, bool) {
	// The seconds layout also reads a fractional second (time.Parse).
	for _, layout := range []string{"2006", "2006-01", "2006-01-02", "2006-01-02T15:04Z07:00", "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0), true
	}
	return time.Time{}, false
}

// binaryEquals compares the bytes r and p encode in base64; when either does
// not decode, it compares the texts.
func binaryEquals(r, p string) bool {
	rb, rerr := base64.StdEncoding.DecodeString(r)
	pb, perr := base64.StdEncoding.DecodeString(p)
	if rerr != nil || perr != nil {
		return r == p
	}
	return bytes.Equal(rb, pb)
}

// ipContains reports whether the address r lies in p, a CIDR block or an
// address: IPv4 as /32 (iam-evaluation.md 6.8), IPv6 as /128 (switch
// iam.condition.ipv6-default-prefix "128", IAM-EVAL-07).
func ipContains(p, r string) bool {
	addr, err := netip.ParseAddr(r)
	if err != nil {
		return false
	}
	var block netip.Prefix
	if strings.Contains(p, "/") {
		if block, err = netip.ParsePrefix(p); err != nil {
			return false
		}
	} else {
		a, err := netip.ParseAddr(p)
		if err != nil {
			return false
		}
		block = netip.PrefixFrom(a, a.BitLen())
	}
	return block.IsValid() && block.Masked().Contains(addr)
}
