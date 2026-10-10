package fault

import (
	"strings"
	"unicode/utf8"
)

// ARNGlob matches ARN strings: * matches any run of characters, colons and
// slashes included, and ? matches exactly one character; every other
// character matches itself. The zero ARNGlob matches every ARN, so a rule
// without a resource dimension applies to calls that touch nothing.
type ARNGlob struct{ pattern string }

// NewARNGlob returns the glob for pattern; the empty pattern is the zero
// ARNGlob, which matches everything.
func NewARNGlob(pattern string) ARNGlob { return ARNGlob{pattern: pattern} }

// String returns the pattern.
func (g ARNGlob) String() string { return g.pattern }

// IsZero reports whether g is the match-everything glob.
func (g ARNGlob) IsZero() bool { return g.pattern == "" }

// MarshalText encodes the pattern.
func (g ARNGlob) MarshalText() ([]byte, error) { return []byte(g.pattern), nil }

// UnmarshalText decodes a pattern; every string is a glob.
func (g *ARNGlob) UnmarshalText(text []byte) error {
	g.pattern = string(text)
	return nil
}

// Match reports whether arn matches the whole pattern. It backtracks only to
// the most recent *, so it runs in O(len(pattern) * len(arn)).
func (g ARNGlob) Match(arn string) bool {
	if g.IsZero() {
		return true
	}
	p, s := g.pattern, arn
	star, resume := -1, 0 // pattern index after the last *, and where its match ends in s
	pi, si := 0, 0
	for si < len(s) {
		if pi < len(p) {
			switch pc, pw := utf8.DecodeRuneInString(p[pi:]); pc {
			case '*':
				star, resume = pi+pw, si
				pi += pw
				continue
			case '?':
				_, sw := utf8.DecodeRuneInString(s[si:])
				pi, si = pi+pw, si+sw
				continue
			default:
				if strings.HasPrefix(s[si:], p[pi:pi+pw]) {
					pi, si = pi+pw, si+pw
					continue
				}
			}
		}
		if star < 0 {
			return false
		}
		_, sw := utf8.DecodeRuneInString(s[resume:])
		resume += sw
		pi, si = star, resume
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// arnPrefix starts every ARN string.
const arnPrefix = "arn:"

// canMatchARN reports whether some ARN string could match g: the pattern up
// to its first * must agree with "arn:", so "orders-*" never matches while
// "*:orders-*" and "a?n:*" can.
func (g ARNGlob) canMatchARN() bool {
	if g.IsZero() {
		return true
	}
	for i := range len(arnPrefix) {
		switch {
		case i == len(g.pattern):
			return false
		case g.pattern[i] == '*':
			return true
		case g.pattern[i] != '?' && g.pattern[i] != arnPrefix[i]:
			return false
		}
	}
	return true
}
