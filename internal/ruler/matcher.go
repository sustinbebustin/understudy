package ruler

import (
	"net/netip"
	"slices"
	"strings"
)

// matcher is one element of a leaf's rule array: the leaf matches when any of
// its matchers accepts any value at the leaf's path.
//
//sumtype:decl
type matcher interface {
	// accepts reports whether one event value satisfies the matcher.
	accepts(v value, o Options) bool
}

// matchEquals is a string rule, ["abc"]: the value is exactly that string.
type matchEquals struct{ text string }

// matchEqualsIP is a string rule that is an IP literal, ["10.0.0.1"]: Ruler
// compiles it to an address range, so any string spelling of the same
// address matches ("2001:db8::1" matches "2001:0db8:0:0:0:0:0:1").
type matchEqualsIP struct{ addr netip.Addr }

// matchEqualsNumber is a bare number rule Ruler can compare, [300]: numerically
// equal numbers match (300.0, 3.0e2), and so does identical text.
type matchEqualsNumber struct {
	text  string
	value float64
}

// matchEqualsNumberText is a bare number rule Ruler cannot compare, such as
// [12345678901234567890]: only identical number text matches.
type matchEqualsNumberText struct{ text string }

// matchLiteral is a [true], [false] or [null] rule.
type matchLiteral struct{ literal jsonLiteral }

// jsonLiteral is a JSON literal.
type jsonLiteral uint8

const (
	// literalTrue is true.
	literalTrue jsonLiteral = iota + 1
	// literalFalse is false.
	literalFalse
	// literalNull is null.
	literalNull
)

// matchPrefix is {"prefix": "s"}.
type matchPrefix struct{ text string }

// matchSuffix is {"suffix": "s"}.
type matchSuffix struct{ text string }

// matchPrefixIgnoreCase is {"prefix": {"equals-ignore-case": "s"}}.
type matchPrefixIgnoreCase struct{ fold caseless }

// matchSuffixIgnoreCase is {"suffix": {"equals-ignore-case": "s"}}.
type matchSuffixIgnoreCase struct{ fold caseless }

// matchEqualsIgnoreCase is {"equals-ignore-case": "s"}.
type matchEqualsIgnoreCase struct{ fold caseless }

// matchWildcard is {"wildcard": "a*b"}.
type matchWildcard struct{ pattern glob }

// matchAnythingBut is {"anything-but": "s"} or a list of strings: any value except
// those strings, including every non-string value.
type matchAnythingBut struct{ texts []string }

// matchAnythingButNumbers is {"anything-but": 5} or a list of numbers: any value
// except numbers equal to those, including every non-number value.
type matchAnythingButNumbers struct{ values []float64 }

// matchAnythingButPrefix is {"anything-but": {"prefix": ...}}: any value except
// strings with one of the prefixes.
type matchAnythingButPrefix struct{ prefixes []string }

// matchAnythingButSuffix is {"anything-but": {"suffix": ...}}.
type matchAnythingButSuffix struct{ suffixes []string }

// matchAnythingButEqualsIgnoreCase is {"anything-but": {"equals-ignore-case": ...}}.
type matchAnythingButEqualsIgnoreCase struct{ folds []caseless }

// matchAnythingButWildcard is {"anything-but": {"wildcard": ...}}.
type matchAnythingButWildcard struct{ patterns []glob }

// matchNumeric is {"numeric": [...]}: a comparable number in the interval. An
// unbounded side is an infinity.
type matchNumeric struct {
	low, high                   float64
	lowInclusive, highInclusive bool
}

// matchExists is {"exists": true}: the path has a value.
type matchExists struct{}

// matchNotExists is {"exists": false}: the path has no value. It never accepts a
// value; Match treats it as the leaf's absence alternative.
type matchNotExists struct{}

// matchCIDR is {"cidr": "10.0.0.0/24"}: a string that is an address in the prefix.
type matchCIDR struct{ prefix netip.Prefix }

// valueKind is the JSON type of an event value. Ruler keeps strings quoted
// and everything else as its source text, so the types never compare equal.
type valueKind uint8

const (
	kindString valueKind = iota + 1
	kindNumber
	kindTrue
	kindFalse
	kindNull
)

// value is one event field value. text is the decoded string or the source
// text; num is set when numOK, for a number Ruler can compare.
type value struct {
	text  string
	num   float64
	kind  valueKind
	numOK bool
}

func newValue(t token) value {
	switch t.kind {
	case tokString:
		return value{kind: kindString, text: t.text}
	case tokNumber:
		f, ok := comparableNumber(t.text)
		return value{kind: kindNumber, text: t.text, num: f, numOK: ok}
	case tokTrue:
		return value{kind: kindTrue, text: t.text}
	case tokFalse:
		return value{kind: kindFalse, text: t.text}
	case tokNull, tokEOF, tokStartObject, tokEndObject, tokStartArray, tokEndArray, tokFieldName:
	}
	return value{kind: kindNull, text: t.text}
}

func (m matchEquals) accepts(v value, _ Options) bool {
	return v.kind == kindString && v.text == m.text
}

func (m matchEqualsIP) accepts(v value, _ Options) bool {
	if v.kind != kindString {
		return false
	}
	a, ok := parseIP(v.text)
	return ok && a == m.addr
}

func (m matchEqualsNumber) accepts(v value, o Options) bool {
	if v.kind != kindNumber {
		return false
	}
	if o.BareNumber == BareNumberNumericOrText && v.numOK && v.num == m.value {
		return true
	}
	return v.text == m.text
}

func (m matchEqualsNumberText) accepts(v value, _ Options) bool {
	return v.kind == kindNumber && v.text == m.text
}

func (m matchLiteral) accepts(v value, _ Options) bool {
	switch m.literal {
	case literalTrue:
		return v.kind == kindTrue
	case literalFalse:
		return v.kind == kindFalse
	case literalNull:
		return v.kind == kindNull
	}
	return false
}

func (m matchPrefix) accepts(v value, _ Options) bool {
	return v.kind == kindString && strings.HasPrefix(v.text, m.text)
}

func (m matchSuffix) accepts(v value, _ Options) bool {
	return v.kind == kindString && strings.HasSuffix(v.text, m.text)
}

func (m matchPrefixIgnoreCase) accepts(v value, _ Options) bool {
	return v.kind == kindString && m.fold.prefixOf(v.text)
}

func (m matchSuffixIgnoreCase) accepts(v value, _ Options) bool {
	return v.kind == kindString && m.fold.suffixOf(v.text)
}

func (m matchEqualsIgnoreCase) accepts(v value, _ Options) bool {
	return v.kind == kindString && m.fold.equal(v.text)
}

func (m matchWildcard) accepts(v value, _ Options) bool {
	return v.kind == kindString && m.pattern.matches(v.text)
}

func (m matchAnythingBut) accepts(v value, _ Options) bool {
	return v.kind != kindString || !slices.Contains(m.texts, v.text)
}

func (m matchAnythingButNumbers) accepts(v value, _ Options) bool {
	return v.kind != kindNumber || !v.numOK || !slices.Contains(m.values, v.num)
}

func (m matchAnythingButPrefix) accepts(v value, _ Options) bool {
	return v.kind != kindString || !slices.ContainsFunc(m.prefixes, func(p string) bool {
		return strings.HasPrefix(v.text, p)
	})
}

func (m matchAnythingButSuffix) accepts(v value, _ Options) bool {
	return v.kind != kindString || !slices.ContainsFunc(m.suffixes, func(s string) bool {
		return strings.HasSuffix(v.text, s)
	})
}

func (m matchAnythingButEqualsIgnoreCase) accepts(v value, _ Options) bool {
	return v.kind != kindString || !slices.ContainsFunc(m.folds, func(f caseless) bool {
		return f.equal(v.text)
	})
}

func (m matchAnythingButWildcard) accepts(v value, _ Options) bool {
	return v.kind != kindString || !slices.ContainsFunc(m.patterns, func(g glob) bool {
		return g.matches(v.text)
	})
}

func (m matchNumeric) accepts(v value, _ Options) bool {
	if v.kind != kindNumber || !v.numOK {
		return false
	}
	aboveLow := v.num > m.low || (m.lowInclusive && v.num == m.low)
	belowHigh := v.num < m.high || (m.highInclusive && v.num == m.high)
	return aboveLow && belowHigh
}

func (matchExists) accepts(v value, o Options) bool {
	if o.ExistsTrue == ExistsTrueNonEmpty {
		return v.kind != kindNull && (v.kind != kindString || v.text != "")
	}
	return true
}

func (matchNotExists) accepts(value, Options) bool { return false }

func (m matchCIDR) accepts(v value, _ Options) bool {
	if v.kind != kindString {
		return false
	}
	a, ok := parseIP(v.text)
	return ok && m.prefix.Contains(a)
}
