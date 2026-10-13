package codegen_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"testing"
	"unicode"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/codegen"
)

// esc spells the ECMA escape backslash, u, then four hex digits.
func esc(hex string) string { return "\\" + "u" + hex }

func TestTranslatePattern(t *testing.T) {
	tests := []struct{ ecma, re2 string }{
		{`^[a-z]+$`, `^[a-z]+$`},
		{"^" + esc("00e9") + "$", `^\x{E9}$`},
		{"^" + esc("D83D") + esc("DE00") + "$", `^\x{1F600}$`},
		// A lone surrogate never matches a Go string: dropped from a class
		// with the range it bounds, an empty class outside one.
		{"^[a" + esc("D800") + "-" + esc("DBFF") + "b]$", `^[ab]$`},
		{"^x" + esc("DC00") + "$", `^x[^\x00-\x{10FFFF}]$`},
		// ECMA reads exactly four hex digits: STS arnType's five-digit
		// escapes are U+1000 then "0", U+10FF then "FF" (smithy-models.md 6.2).
		{"^[" + esc("1000") + "0-" + esc("10FF") + "FF]$", `^[\x{1000}0-\x{10FF}FF]$`},
		{`^\x41[\x2d]$`, `^\x{41}[\x{2D}]$`},
		{`^\p{Print}*$`, `^[ -~]*$`},
		{`^a.b$`, `^a[^\n\r\x{2028}\x{2029}]b$`},
		{`^[.]$`, `^[.]$`},
		{`^\d\w$`, `^\d\w$`},
		{`^[\s,]+$`, `^[\t\n\x0B\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF},]+$`},
		{`^[a-]$`, `^[a\-]$`},
		{`^\/$`, `^/$`},
		// A hyphen right after a completed range is a literal (ClassRanges);
		// so is one next to a class escape (Annex B).
		{`^[a-zA-Z0-9-_]+$`, `^[a-zA-Z0-9\-_]+$`},
		{`[a-c-e]`, `[a-c\-e]`},
		{`^[a-z0-9-.]+$`, `^[a-z0-9\-.]+$`},
		{`^[\w-.]+$`, `^[\w\-.]+$`},
		{`^[.-\d]+$`, `^[.\-\d]+$`},
		{`^[a-c--e]$`, `^[a-c\--e]$`},
		// \0 then an octal digit is an Annex B legacy octal escape of up to
		// three digits; \0 then 8, 9 or a non-digit is NUL.
		{`^\01$`, `^\x{1}$`},
		{`^\012$`, `^\x{A}$`},
		{`^\0123$`, `^\x{A}3$`},
		{`^[\077]$`, `^[\x{3F}]$`},
		{`^\08$`, `^\x008$`},
		{`^\0a$`, `^\x00a$`},
	}
	for _, tt := range tests {
		got, err := codegen.TranslatePattern(tt.ecma)
		if err != nil || got != tt.re2 {
			t.Errorf("TranslatePattern(%q) = %q, %v; want %q", tt.ecma, got, err, tt.re2)
		}
	}
}

// TestTranslatedClassesKeepALiteralHyphen pins the ECMA reading of a hyphen
// that cannot be a range operator: the class must match "-" itself.
func TestTranslatedClassesKeepALiteralHyphen(t *testing.T) {
	for _, p := range []string{`^[a-zA-Z0-9-_]+$`, `^[a-c-e]+$`, `^[a-z0-9-.]+$`, `^[0-9a-z-:]+$`, `^[\w-.]+$`, `^[.-\d]+$`} {
		re2, err := codegen.TranslatePattern(p)
		if err != nil {
			t.Fatalf("TranslatePattern(%q): %v", p, err)
		}
		if !regexp.MustCompile(re2).MatchString("-") {
			t.Errorf("TranslatePattern(%q) = %q, which does not match \"-\"", p, re2)
		}
	}
}

func TestTranslatePatternRefusesWhatRE2CannotExpress(t *testing.T) {
	for _, p := range []string{`^(?!x).+$`, `^a(?=b)`, `^[a-z]*(?<!\.)$`, `(?<=a)b`} {
		var la *codegen.LookaroundError
		if _, err := codegen.TranslatePattern(p); !errors.As(err, &la) {
			t.Errorf("TranslatePattern(%q) = %v, want a *LookaroundError", p, err)
		}
	}
	for _, p := range []string{`(a)\1`, `[]`, `[^]`, `[a`, `a\`, `\Aa`, `a\z`, `\Qa.b\E`, `a\E`, `\C`, `\pL`, `[\PN]`, `^\a$`, `[\a]`, `\x{41}`, `\x4`, `[\xG0]`, `a\x`} {
		var pe *codegen.PatternError
		if _, err := codegen.TranslatePattern(p); !errors.As(err, &pe) {
			t.Errorf("TranslatePattern(%q) = %v, want a *PatternError", p, err)
		}
	}
}

// ecmaSpace is ECMA-262 \s: WhiteSpace (TAB, VT, FF, SP, NBSP, ZWNBSP and
// every Zs code point) plus LineTerminator (LF, CR, LS, PS).
func ecmaSpace(r rune) bool {
	switch r {
	case '\t', '\v', '\f', ' ', 0xA0, 0xFEFF, '\n', '\r', 0x2028, 0x2029:
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

// ecmaDot is ECMA-262 . without the s flag: anything but a line terminator.
func ecmaDot(r rune) bool { return r != '\n' && r != '\r' && r != 0x2028 && r != 0x2029 }

func runes() *rapid.Generator[rune] {
	return rapid.OneOf(
		rapid.Rune(),
		rapid.RuneFrom([]rune{'\t', '\v', '\f', '\n', '\r', ' ', 0x85, 0xA0, 0x1680, 0x180E, 0x2007, 0x200B, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF, '0', '9', 'a', '_', 0x0660}),
		rapid.RuneFrom(nil, unicode.Zs),
	)
}

// TestTranslatedClassesHaveECMASemantics is the differential test of
// ARCHITECTURE.md 5.4: each translated class matches exactly the code
// points ECMA-262 says it does, which RE2's own \s and . do not.
func TestTranslatedClassesHaveECMASemantics(t *testing.T) {
	oracles := map[string]func(rune) bool{
		`\s`:     ecmaSpace,
		`\S`:     func(r rune) bool { return !ecmaSpace(r) },
		`[\s]`:   ecmaSpace,
		`.`:      ecmaDot,
		`\d`:     func(r rune) bool { return r >= '0' && r <= '9' },
		`\w`:     func(r rune) bool { return r == '_' || r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r)) },
		`[^\s]`:  func(r rune) bool { return !ecmaSpace(r) },
		`[\S]`:   func(r rune) bool { return !ecmaSpace(r) },
		`[^\S]`:  ecmaSpace,
		`[\s\S]`: func(rune) bool { return true },
	}
	compiled := map[string]*regexp.Regexp{}
	for class := range oracles {
		re2, err := codegen.TranslatePattern("^" + class + "$")
		if err != nil {
			t.Fatal(err)
		}
		compiled[class] = regexp.MustCompile(re2)
	}
	rapid.Check(t, func(t *rapid.T) {
		r := runes().Draw(t, "rune")
		for class, want := range oracles {
			if got := compiled[class].MatchString(string(r)); got != want(r) {
				t.Fatalf("%s on U+%04X: matched %v, ECMA says %v", class, r, got, want(r))
			}
		}
	})
}

// rewritten reports whether the translator deliberately changes the meaning
// RE2 would give pattern p read verbatim: \s, \S, ., \u escapes and
// \p{Print} all mean something else in the two dialects.
func rewritten(p string) bool {
	for _, s := range []string{`\s`, `\S`, `.`, `\u`, `\p{Print}`} {
		if strings.Contains(p, s) {
			return true
		}
	}
	return false
}

// simplified is the canonical rendering of an RE2 pattern's parse tree, so
// two spellings of one language compare equal.
func simplified(p string) (string, error) {
	re, err := syntax.Parse(p, syntax.Perl)
	if err != nil {
		return "", err
	}
	return re.Simplify().String(), nil
}

// TestEveryVendoredPatternTranslates walks the 319 patterns of the twelve
// vendored models: each translates to a compiling RE2 pattern, except the
// lookarounds of smithy-models.md 6.2, which need a hand validator. Where
// RE2 and ECMA agree on the source syntax, the translation must keep its
// meaning exactly.
func TestEveryVendoredPatternTranslates(t *testing.T) {
	paths, err := filepath.Glob("../../third_party/api-models-aws/models/*/service/*/*.json")
	if err != nil || len(paths) != 12 {
		t.Fatalf("found %d vendored models (%v), want 12", len(paths), err)
	}
	var count int
	var lookarounds []string
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		m, err := codegen.Load(f, filepath.Base(p))
		if cerr := f.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		if err != nil {
			t.Fatal(err)
		}
		for id, s := range m.Shapes {
			raw, ok := s.Traits["smithy.api#pattern"]
			if !ok {
				continue
			}
			var src string
			if err := json.Unmarshal(raw, &src); err != nil {
				t.Fatal(err)
			}
			count++
			re2, err := codegen.TranslatePattern(src)
			var la *codegen.LookaroundError
			switch {
			case errors.As(err, &la):
				lookarounds = append(lookarounds, string(id))
			case err != nil:
				t.Errorf("%s: %v", id, err)
			case !rewritten(src):
				want, perr := simplified(src)
				if perr != nil {
					t.Errorf("%s: RE2 cannot parse source %q: %v", id, src, perr)
					continue
				}
				got, gerr := simplified(re2)
				if gerr != nil || got != want {
					t.Errorf("%s: %q translates to %q, which RE2 reads as a different language (parse error: %v)", id, src, re2, gerr)
				}
			}
		}
	}
	if count != 319 {
		t.Errorf("%d patterns, smithy-models.md 4.1 counts 319", count)
	}
	slices.Sort(lookarounds)
	want := []string{
		"com.amazonaws.eventbridge#PathParameter",
		"com.amazonaws.iam#accountAliasType",
		"com.amazonaws.lambda#Alias",
		"com.amazonaws.lambda#CollectionName",
		"com.amazonaws.lambda#FunctionUrlFunctionName",
		"com.amazonaws.lambda#FunctionUrlQualifier",
		"com.amazonaws.lambda#S3Bucket",
	}
	if !slices.Equal(lookarounds, want) {
		t.Errorf("lookaround patterns = %v\nwant %v", lookarounds, want)
	}
}
