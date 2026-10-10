package codegen

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ecmaSpace is the body of a character class holding exactly the code points
// ECMA-262 \s matches: WhiteSpace plus LineTerminator. RE2's \s is ASCII
// only, so \s and \S are rewritten to this class.
const ecmaSpace = `\t\n\x0B\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`

// ecmaNonSpace is the complement of ecmaSpace over all code points, as
// ranges: the rendering of \S inside a class, where RE2 has no negation
// (Lambda's [\s\S]).
const ecmaNonSpace = `\x00-\x08\x0E-\x1F\x21-\x{9F}\x{A1}-\x{167F}\x{1681}-\x{1FFF}\x{200B}-\x{2027}` +
	`\x{202A}-\x{202E}\x{2030}-\x{205E}\x{2060}-\x{2FFF}\x{3001}-\x{FEFE}\x{FF00}-\x{10FFFF}`

// ecmaDot is what ECMA-262 . matches without the s flag: everything but the
// four line terminators. RE2's . excludes only \n.
const ecmaDot = `[^\n\r\x{2028}\x{2029}]`

// neverMatches is an empty class: the rendering of a lone surrogate outside
// a class, which no Go string can contain.
const neverMatches = `[^\x00-\x{10FFFF}]`

// LookaroundError reports an ECMA pattern RE2 cannot express. The string
// shape needs dev.understudy#patternValidator.
type LookaroundError struct {
	Pattern string
	Offset  int
}

func (e *LookaroundError) Error() string {
	return fmt.Sprintf("pattern %q has a lookaround at byte %d, which RE2 cannot compile; give the string shape dev.understudy#patternValidator {allOf, noneOf} with an RE2 rendering", e.Pattern, e.Offset)
}

// PatternError reports an ECMA construct the translator does not handle.
type PatternError struct {
	Pattern string
	Offset  int
	Reason  string
}

func (e *PatternError) Error() string {
	return fmt.Sprintf("pattern %q at byte %d: %s", e.Pattern, e.Offset, e.Reason)
}

// TranslatePattern rewrites an ECMA-262 pattern (Smithy's dialect, no flags)
// into RE2 syntax with ECMA semantics (ARCHITECTURE.md 5.4):
//
//   - \uXXXX becomes \x{XXXX}; a surrogate pair becomes its code point; a lone
//     surrogate is dropped inside a class (with any range it bounds) and never
//     matches outside one, as no Go string holds one. Exactly four hex digits
//     are read, as ECMA does: STS arnType's five-digit escape for U+10000 is
//     U+1000 followed by "0".
//   - \p{Print} (Java's ASCII printable class) becomes the range space to ~.
//     Any other \p{Name} or \P{Name} keeps its Unicode-property meaning, as
//     the Java services that wrote the models apply it, although ECMA without
//     the u flag would read it as literal text.
//   - \0 followed by up to two octal digits is Annex B's legacy octal escape
//     and becomes \x{..} of its value (\012 is U+000A); \0 followed by
//     anything else is NUL.
//   - \xHH becomes \x{HH}. RE2-only escapes ECMA reads as identity escapes
//     (\A, \z, \Q, \E, \C, \a, \x not followed by exactly two hex digits,
//     and \p or \P without braces) fail with *PatternError rather than
//     compiling with different semantics.
//   - \s and \S become the ECMA whitespace class and its complement; inside
//     a class \S becomes the complement's explicit ranges.
//   - . becomes the class of everything but \n, \r, U+2028 and U+2029.
//   - a class hyphen that ECMA reads as itself (first, last, after a
//     completed range, or next to a class escape) becomes \-.
//   - \d, \D, \w, \W, \b and \B pass through: RE2 gives them the same ASCII
//     meaning ECMA does without the u flag.
//   - lookarounds fail with *LookaroundError; backreferences and empty classes
//     fail with *PatternError.
//
// The result always compiles under regexp; a pattern that does not is an
// error.
func TranslatePattern(p string) (string, error) {
	t := translator{src: p}
	if err := t.run(); err != nil {
		return "", err
	}
	out := t.out.String()
	if _, err := regexp.Compile(out); err != nil {
		return "", &PatternError{Pattern: p, Reason: "the RE2 rendering " + strconv.Quote(out) + " does not compile: " + err.Error()}
	}
	return out, nil
}

// PatternEcho is the text AWS prints for a pattern violation: the pattern
// minus one leading ^ and one trailing $ (error-matrix.md 7, D3).
func PatternEcho(p string) string {
	p = strings.TrimPrefix(p, "^")
	if strings.HasSuffix(p, "$") && !strings.HasSuffix(p, `\$`) {
		p = p[:len(p)-1]
	}
	return p
}

type translator struct {
	src     string
	i       int
	out     strings.Builder
	inClass bool
	// classItems collects the rendered items of the current class so a lone
	// surrogate can drop the range it bounds.
	classItems []classItem
}

// classItem is one class member: a single atom, or the start or end of a
// range when rangeLink is set on the item after the hyphen.
type classItem struct {
	text      string
	lone      bool // a lone surrogate
	set       bool // a class escape (\d \w \s \p{...}), which cannot bound a range
	rangeLink bool // this item ends a range started by the previous item
}

// literalHyphen is a class '-' that ECMA-262 reads as itself rather than as
// a range operator.
func literalHyphen() classItem { return classItem{text: `\-`} }

func (t *translator) fail(reason string) error {
	return &PatternError{Pattern: t.src, Offset: t.i, Reason: reason}
}

func (t *translator) run() error {
	for t.i < len(t.src) {
		if t.inClass {
			if err := t.classStep(); err != nil {
				return err
			}
			continue
		}
		if err := t.step(); err != nil {
			return err
		}
	}
	if t.inClass {
		return t.fail("unterminated character class")
	}
	return nil
}

func (t *translator) step() error {
	c := t.src[t.i]
	switch {
	case c == '\\':
		text, lone, err := t.escape()
		if err != nil {
			return err
		}
		if lone {
			text = neverMatches
		}
		t.out.WriteString(text)
	case c == '.':
		t.out.WriteString(ecmaDot)
		t.i++
	case c == '(' && isLookaround(t.src[t.i:]):
		return &LookaroundError{Pattern: t.src, Offset: t.i}
	case c == '[':
		return t.openClass()
	default:
		_, size := utf8.DecodeRuneInString(t.src[t.i:])
		t.out.WriteString(t.src[t.i : t.i+size])
		t.i += size
	}
	return nil
}

func (t *translator) openClass() error {
	t.i++
	negated := strings.HasPrefix(t.src[t.i:], "^")
	if negated {
		t.i++
	}
	if strings.HasPrefix(t.src[t.i:], "]") {
		return t.fail("an empty ECMA class ([] or [^]) has no RE2 rendering")
	}
	t.inClass = true
	t.classItems = nil
	if negated {
		t.out.WriteString("[^")
	} else {
		t.out.WriteString("[")
	}
	return nil
}

func (t *translator) classStep() error {
	c := t.src[t.i]
	switch c {
	case ']':
		t.i++
		t.inClass = false
		t.flushClass()
		return nil
	case '-':
		// ECMA-262 ClassRanges: a hyphen is a range operator only between
		// two atoms. At the start or end of the class, or right after a
		// completed range ([0-9-_]), it is a literal. Annex B also reads it
		// as a literal when either endpoint is a class escape ([\w-.]).
		t.i++
		n := len(t.classItems)
		if n == 0 || strings.HasPrefix(t.src[t.i:], "]") || t.classItems[n-1].rangeLink || t.classItems[n-1].set {
			t.classItems = append(t.classItems, literalHyphen())
			return nil
		}
		item, err := t.classAtom()
		if err != nil {
			return err
		}
		if item.set {
			t.classItems = append(t.classItems, literalHyphen(), item)
			return nil
		}
		item.rangeLink = true
		t.classItems = append(t.classItems, item)
		return nil
	default:
		item, err := t.classAtom()
		if err != nil {
			return err
		}
		t.classItems = append(t.classItems, item)
		return nil
	}
}

func (t *translator) classAtom() (classItem, error) {
	c := t.src[t.i]
	if c == '\\' {
		if strings.HasPrefix(t.src[t.i:], `\S`) {
			t.i += 2
			return classItem{text: ecmaNonSpace, set: true}, nil
		}
		if strings.HasPrefix(t.src[t.i:], `\s`) {
			t.i += 2
			return classItem{text: ecmaSpace, set: true}, nil
		}
		set := t.i+1 < len(t.src) && strings.IndexByte("dDwWpP", t.src[t.i+1]) >= 0
		text, lone, err := t.escape()
		if err != nil {
			return classItem{}, err
		}
		if strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") && !strings.HasPrefix(text, "[^") {
			text = text[1 : len(text)-1]
		}
		return classItem{text: text, lone: lone, set: set}, nil
	}
	if c == '[' {
		t.i++
		return classItem{text: `\[`}, nil
	}
	_, size := utf8.DecodeRuneInString(t.src[t.i:])
	text := t.src[t.i : t.i+size]
	t.i += size
	return classItem{text: text}, nil
}

// flushClass writes the class items, dropping lone surrogates and every
// range one of them bounds.
func (t *translator) flushClass() {
	items := t.classItems
	for k := 0; k < len(items); k++ {
		if k+1 < len(items) && items[k+1].rangeLink {
			if !items[k].lone && !items[k+1].lone {
				t.out.WriteString(items[k].text + "-" + items[k+1].text)
			}
			k++
			continue
		}
		if !items[k].lone {
			t.out.WriteString(items[k].text)
		}
	}
	t.out.WriteString("]")
}

// escape reads one backslash escape at t.i and returns its RE2 rendering
// and whether it is a lone surrogate.
func (t *translator) escape() (text string, lone bool, err error) {
	if t.i+1 >= len(t.src) {
		return "", false, t.fail("a trailing backslash")
	}
	c := t.src[t.i+1]
	switch {
	case c == 'u':
		return t.unicodeEscape()
	case c == 's':
		t.i += 2
		return "[" + ecmaSpace + "]", false, nil
	case c == 'S':
		t.i += 2
		return "[^" + ecmaSpace + "]", false, nil
	case c == 'p' && strings.HasPrefix(t.src[t.i:], `\p{Print}`):
		t.i += len(`\p{Print}`)
		return `[ -~]`, false, nil
	case (c == 'p' || c == 'P') && !strings.HasPrefix(t.src[t.i+2:], "{"):
		return "", false, t.fail(`\` + string(c) + ` without {Name} is RE2's one-letter property class; ECMA reads it as the letter ` + string(c))
	case strings.IndexByte("AzQECa", c) >= 0:
		return "", false, t.fail(`\` + string(c) + ` is an RE2-only escape; ECMA reads it as the letter ` + string(c) + `, so the two engines would disagree`)
	case c == 'x':
		r, ok := hexDigits(t.src, t.i+2, 2)
		if !ok {
			return "", false, t.fail(`\x needs exactly two hex digits; ECMA reads any other \x as the letter x, while RE2 reads \x{...} as a code point`)
		}
		t.i += 4
		return fmt.Sprintf(`\x{%X}`, r), false, nil
	case c >= '1' && c <= '9':
		return "", false, t.fail("backreferences have no RE2 rendering")
	case c == '0':
		return t.zeroEscape(), false, nil
	case c == 'c':
		if t.i+2 >= len(t.src) || !isASCIILetter(t.src[t.i+2]) {
			return "", false, t.fail(`\c needs a letter`)
		}
		code := t.src[t.i+2] % 32
		t.i += 3
		return fmt.Sprintf(`\x{%X}`, code), false, nil
	case c == 'k':
		return "", false, t.fail("named backreferences have no RE2 rendering")
	case c == '/':
		t.i += 2
		return "/", false, nil
	default:
		_, size := utf8.DecodeRuneInString(t.src[t.i+1:])
		text := t.src[t.i : t.i+1+size]
		t.i += 1 + size
		return text, false, nil
	}
}

// zeroEscape reads \0 at t.i. Without the u flag, Annex B reads \0 followed
// by octal digits as a LegacyOctalEscapeSequence: the 0 and at most two more
// octal digits, so the value never exceeds 077. Followed by 8, 9 or a
// non-digit it is NUL.
func (t *translator) zeroEscape() string {
	end := t.i + 2
	var v rune
	for end < len(t.src) && end < t.i+4 && t.src[end] >= '0' && t.src[end] <= '7' {
		v = v*8 + rune(t.src[end]-'0')
		end++
	}
	digits := end - (t.i + 2)
	t.i = end
	if digits == 0 {
		return `\x00`
	}
	return fmt.Sprintf(`\x{%X}`, v)
}

func (t *translator) unicodeEscape() (text string, lone bool, err error) {
	hi, ok := hexDigits(t.src, t.i+2, 4)
	if !ok {
		return "", false, t.fail(`\u needs four hex digits`)
	}
	t.i += 6
	if hi >= 0xD800 && hi <= 0xDBFF {
		if lo, ok := hex4After(t.src, t.i); ok && lo >= 0xDC00 && lo <= 0xDFFF {
			t.i += 6
			r := 0x10000 + (hi-0xD800)<<10 + (lo - 0xDC00)
			return fmt.Sprintf(`\x{%X}`, r), false, nil
		}
		return "", true, nil
	}
	if hi >= 0xDC00 && hi <= 0xDFFF {
		return "", true, nil
	}
	return fmt.Sprintf(`\x{%X}`, hi), false, nil
}

// hex4After reads \uXXXX at i.
func hex4After(s string, i int) (rune, bool) {
	if !strings.HasPrefix(s[i:], `\u`) {
		return 0, false
	}
	return hexDigits(s, i+2, 4)
}

// hexDigits reads exactly n hex digits at i, as ECMA's \xHH (n 2) and
// \uXXXX (n 4) do.
func hexDigits(s string, i, n int) (rune, bool) {
	if i+n > len(s) {
		return 0, false
	}
	var r rune
	for _, c := range []byte(s[i : i+n]) {
		var d rune
		switch {
		case c >= '0' && c <= '9':
			d = rune(c - '0')
		case c >= 'a' && c <= 'f':
			d = rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = rune(c-'A') + 10
		default:
			return 0, false
		}
		r = r<<4 | d
	}
	return r, true
}

// isLookaround reports whether s starts with an ECMA lookahead or
// lookbehind group.
func isLookaround(s string) bool {
	for _, p := range []string{"(?=", "(?!", "(?<=", "(?<!"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
