package logsfilter

import (
	"regexp"
	"strconv"
	"strings"
)

// scanner is a byte cursor over a filter pattern. The lexical forms shared by
// the three pattern kinds (quoted strings, regular expressions, comparison
// values and operators) live here.
type scanner struct {
	src string
	pos int
}

func (s *scanner) eof() bool { return s.pos >= len(s.src) }

func (s *scanner) peek() byte {
	if s.eof() {
		return 0
	}
	return s.src[s.pos]
}

func (s *scanner) skipSpace() {
	for !s.eof() && isSpace(s.src[s.pos]) {
		s.pos++
	}
}

func (s *scanner) accept(tok string) bool {
	if strings.HasPrefix(s.src[s.pos:], tok) {
		s.pos += len(tok)
		return true
	}
	return false
}

// acceptWord accepts a keyword only when no name character follows it.
func (s *scanner) acceptWord(word string) bool {
	end := s.pos + len(word)
	if !strings.HasPrefix(s.src[s.pos:], word) || (end < len(s.src) && isNameChar(s.src[end])) {
		return false
	}
	s.pos = end
	return true
}

func (s *scanner) fail(detail string) *Error { return invalidPattern(detail, s.pos) }

// name reads a selector key or a space-delimited field name.
func (s *scanner) name() string {
	start := s.pos
	for !s.eof() && isNameChar(s.src[s.pos]) {
		s.pos++
	}
	return s.src[start:s.pos]
}

// quoted reads a double-quoted string; a backslash makes the next byte
// literal, so \" is a quote and \\ a backslash (rule N11).
func (s *scanner) quoted() (string, *Error) {
	start := s.pos
	s.pos++
	var b strings.Builder
	for s.pos < len(s.src) {
		switch c := s.src[s.pos]; c {
		case '"':
			s.pos++
			return b.String(), nil
		case '\\':
			if s.pos+1 < len(s.src) {
				b.WriteByte(s.src[s.pos+1])
			}
			s.pos += 2
		default:
			b.WriteByte(c)
			s.pos++
		}
	}
	return "", invalidPattern("unterminated quoted string", start)
}

// regex reads a %regex% term or value.
func (s *scanner) regex() (*regexp.Regexp, *Error) {
	start := s.pos
	s.pos++
	for s.pos < len(s.src) {
		switch s.src[s.pos] {
		case '%':
			body := s.src[start+1 : s.pos]
			s.pos++
			return compileRegex(body, start)
		case '\\':
			s.pos += 2
		default:
			s.pos++
		}
	}
	return nil, invalidPattern("unterminated regular expression", start)
}

// op reads a comparison operator.
func (s *scanner) op() (op, bool) {
	for _, o := range [...]op{opNe, opLe, opGe, opEq, opLt, opGt} {
		if s.accept(o.String()) {
			return o, true
		}
	}
	return opEq, false
}

// value reads the right-hand side of a comparison: a regex, a quoted string,
// a lone * (exists), a number, or a bare string where * is a wildcard. A
// quoted lone "*" is also exists: AWS documents { $.eventType = "*" } and
// { $.latency = * } alike as "the property is present".
func (s *scanner) value() (value, *Error) {
	switch s.peek() {
	case '%':
		re, err := s.regex()
		if err != nil {
			return nil, err
		}
		return regexValue{re: re}, nil
	case '"':
		str, err := s.quoted()
		if err != nil {
			return nil, err
		}
		if str == "*" {
			return anyValue{}, nil
		}
		return globValue{glob: str}, nil
	}
	start := s.pos
	for !s.eof() && isBareValueChar(s.src[s.pos]) {
		s.pos++
	}
	run := s.src[start:s.pos]
	switch run {
	case "":
		return nil, invalidPattern("comparison value expected", start)
	case "*":
		return anyValue{}, nil
	}
	switch n, form := parseNumber(run); form {
	case finiteNumber:
		return numberValue{n: n}, nil
	case overflowNumber:
		return nil, invalidPattern("number "+run+" is out of range", start)
	case notNumber:
	}
	if strings.ContainsAny(run, "+-") {
		return nil, invalidPattern("value "+run+" is neither a number nor a bare string; quote it", start)
	}
	return globValue{glob: run}, nil
}

// compileRegex checks the character set of rule N4 and compiles the body
// with RE2, whose unanchored search and end-of-text $ match AWS's examples.
func compileRegex(body string, offset int) (*regexp.Regexp, *Error) {
	if body == "" {
		return nil, invalidPattern("empty regular expression", offset)
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			if !isRegexChar(c) {
				return nil, invalidPattern("character "+strconv.QuoteRuneToASCII(rune(c))+" is not allowed in a regular expression", offset)
			}
			continue
		}
		switch {
		case i+1 < len(body) && strings.IndexByte(`dDsSwW^$?[]{}|\*+.`, body[i+1]) >= 0:
			i++
		case i+3 < len(body) && body[i+1] == 'x' && isHex(body[i+2]) && isHex(body[i+3]):
			i += 3
		default:
			return nil, invalidPattern("unsupported escape in regular expression", offset)
		}
	}
	re, err := regexp.Compile(body)
	if err != nil {
		return nil, invalidPattern("invalid regular expression", offset)
	}
	return re, nil
}

// numberForm is how a run of text reads under the number rule.
type numberForm uint8

const (
	notNumber numberForm = iota
	finiteNumber
	// overflowNumber fits the grammar but is beyond float64; its value is
	// ±Inf. Too-small numbers round to zero and are finite.
	overflowNumber
)

// parseNumber accepts [+-]digits[.digits][(e|E)[+-]digits].
func parseNumber(s string) (float64, numberForm) {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	intStart := i
	if i = digits(s, i); i == intStart {
		return 0, notNumber
	}
	if i < len(s) && s[i] == '.' {
		if j := digits(s, i+1); j > i+1 {
			i = j
		} else {
			return 0, notNumber
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		j := digits(s, i)
		if j == i {
			return 0, notNumber
		}
		i = j
	}
	if i != len(s) {
		return 0, notNumber
	}
	// The grammar above admits only what ParseFloat parses, so ErrRange is
	// its one possible error.
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return f, overflowNumber
	}
	return f, finiteNumber
}

func digits(s string, i int) int {
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return i
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func isHex(c byte) bool { return isDigit(c) || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F') }

func isAlnum(c byte) bool { return isDigit(c) || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') }

func isNameChar(c byte) bool { return isAlnum(c) || c == '_' || c == '-' }

func isBareTermChar(c byte) bool { return isAlnum(c) || strings.IndexByte("_=:#@/;,.", c) >= 0 }

func isBareValueChar(c byte) bool { return isAlnum(c) || strings.IndexByte("_.*+-", c) >= 0 }

func isRegexChar(c byte) bool {
	return isAlnum(c) || strings.IndexByte(":_#=@/;,- ^$?[]{}|*+.", c) >= 0
}
