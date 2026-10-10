package logsfilter

import (
	"slices"
	"strings"
	"testing"
)

// residual is a case from logs-filter-patterns.md section 9 that no AWS
// recording settles yet. want is the package default; when a fidelity switch
// governs the case, sw is its name and alt is the outcome under the switch's
// other value. A recording that disagrees changes the default here and in
// the code.
type residual struct {
	record, sw, pattern, event string
	want, alt                  outcome
}

// noSwitch marks cases decided by the package itself rather than a switch;
// the decision is in the package doc. Their alt equals want.
const noSwitch = ""

// residuals covers C1 to C7. C8 to C10 (backlog R10/C8-C10: FilterLogEvents
// time bounds, paging and tie order) are request semantics, not pattern
// semantics, so they are tested in internal/service/logs (T31).
var residuals = []residual{
	// C1: token boundary.
	{"C1", termBoundarySwitch, `ERROR`, "ERRORS", match, noMatch},
	{"C1", termBoundarySwitch, `ERROR`, "xERROR", match, noMatch},
	{"C1", termBoundarySwitch, `ERROR`, "MY_ERROR", match, noMatch},
	{"C1", termBoundarySwitch, `ERROR`, "ERROR_CODE=5", match, noMatch},
	{"C1", termBoundarySwitch, `ERROR`, "a,ERROR,b", match, match},
	{"C1", termBoundarySwitch, `ERROR`, "ERROR.x", match, match},
	{"C1", termBoundarySwitch, `ERROR`, "ERROR\tTAB", match, match},
	{"C1", termBoundarySwitch, `ERROR`, "ERROR\nNEXT", match, match},
	{"C1", termBoundarySwitch, `ROR`, "ERROR", match, noMatch},
	{"C1", termBoundarySwitch, `Exception`, "java.lang.NullPointerException", match, noMatch},
	{"C1", termBoundarySwitch, `"Exception in"`, "Exception in thread main", match, match},
	{"C1", termBoundarySwitch, `status=ERROR`, "status=ERROR", match, match},
	{"C1", termBoundarySwitch, `"(ERROR)"`, "(ERROR)", match, match},
	// [ERROR] is a one-field space-delimited pattern, not a term, so the
	// field-count rule decides it.
	{"C1", fieldCountSwitch, `[ERROR]`, "Exception in thread main", match, noMatch},
	{"C1", noSwitch, `ERROR*`, "ERRORS", badTerm, badTerm},
	{"C1", noSwitch, `*ROR`, "ERROR", badTerm, badTerm},
	{"C1", noSwitch, `ERROR-1`, "ERROR-1", badTerm, badTerm},
	{"C1", noSwitch, `(ERROR)`, "(ERROR)", badTerm, badTerm},

	// C2: phrases and blank terms. A phrase is matched as one substring, so
	// its inner blanks must match exactly; the token rule cuts at blanks and
	// so ignores how many there are and which kind.
	{"C2", termBoundarySwitch, `"INTERNAL  SERVER"`, "INTERNAL SERVER ERROR", noMatch, match},
	{"C2", termBoundarySwitch, `"INTERNAL SERVER"`, "INTERNAL\tSERVER ERROR", noMatch, match},
	{"C2", noSwitch, `"  "`, "hello", match, match},
	{"C2", noSwitch, `""`, "hello", match, match},
	{"C2", noSwitch, `" " ERROR`, "hello", noMatch, noMatch},
	{"C2", noSwitch, `" " ERROR`, "ERROR", match, match},
	// A no-break space (U+00A0) or an ideographic space (U+3000) is not
	// pattern whitespace, so the phrase is not blank; under the token rule it
	// has no tokens and so matches every event. Both are written as escapes
	// because in source they look like the ASCII space of the rows above.
	{"C2", termBoundarySwitch, "\"\U000000a0\"", "hello", noMatch, match},
	{"C2", termBoundarySwitch, "\"\U000000a0\"", "hello\U000000a0world", match, match},
	{"C2", termBoundarySwitch, "\"\U00003000\"", "hello", noMatch, match},

	// C3: ? and - composition.
	{"C3", noSwitch, `-A -A`, "B", match, match},
	{"C3", noSwitch, `A -A`, "A", noMatch, noMatch},
	{"C3", noSwitch, `?"A" "B" ?"C"`, "B", match, match},
	{"C3", noSwitch, `?"A" "B" ?"C"`, "A C", noMatch, noMatch},
	{"C3", noSwitch, `?A ?B ?C`, "C", match, match},
	{"C3", noSwitch, `-A ?B ?C`, "none", match, match},

	// C4: regex anchors, flags, escapes.
	{"C4", noSwitch, `%^ERROR$%`, "ERROR", match, match},
	{"C4", noSwitch, `%foo$%`, "foo\n", noMatch, noMatch},
	{"C4", noSwitch, `%^line2%`, "line1\nline2", noMatch, noMatch},
	{"C4", noSwitch, `%ERROR%i`, "ERROR", badSyntax, badSyntax},
	{"C4", noSwitch, `%(?i)error%`, "ERROR", badSyntax, badSyntax},
	{"C4", noSwitch, `%%`, "x", badSyntax, badSyntax},
	{"C4", noSwitch, `%\(paren\)%`, "(paren)", badSyntax, badSyntax},
	{"C4", noSwitch, `%\x28paren\x29%`, "(paren)", match, match},
	{"C4", noSwitch, "%é%", "é", badSyntax, badSyntax},
	{"C4", noSwitch, `%ERROR% %x%`, "ERROR x", match, match},
	{"C4", noSwitch, `%A% %B% %C%`, "ABC", match, match},

	// C5: JSON typing, missing fields, roots.
	{"C5", noSwitch, `{ $.a = 1 }`, `{"a":"1"}`, noMatch, noMatch},
	{"C5", noSwitch, `{ $.a = "1" }`, `{"a":1}`, match, match},
	{"C5", noSwitch, `{ $.a = true }`, `{"a":true}`, noMatch, noMatch},
	{"C5", noSwitch, `{ $.a = true }`, `{"a":"true"}`, match, match},
	{"C5", noSwitch, `{ $.a != 5 }`, `{"b":1}`, noMatch, noMatch},
	{"C5", noSwitch, `{ $.a = * }`, `{"a":null}`, match, match},
	{"C5", noSwitch, `{ $.a = * }`, `{"a":{"b":1}}`, noMatch, noMatch},
	{"C5", noSwitch, `{ $.a = * }`, `{"a":[1]}`, noMatch, noMatch},
	{"C5", noSwitch, `{ $.a = "*" }`, `{"a":true}`, match, match},
	{"C5", noSwitch, `{ $.a = "*" }`, `{"a":null}`, match, match},
	{"C5", noSwitch, `{ $.a = "x*z" }`, `{"a":"xyz"}`, match, match},
	{"C5", noSwitch, `{ $.a = 1 }`, `prefix {"a":1}`, noMatch, noMatch},
	{"C5", noSwitch, `{ $.a = 1 }`, `{"a":1} suffix`, noMatch, noMatch},
	{"C5", noSwitch, `{ $.a = 1 }`, "{\"a\":1}\n", match, match},
	{"C5", noSwitch, `{ $.a = 1 }`, `{"a":1,"a":2}`, noMatch, noMatch},
	{"C5", noSwitch, `{ $[0] = 1 }`, `[1,2]`, match, match},
	{"C5", noSwitch, `{ $ = 1 }`, `1`, badSyntax, badSyntax},
	{"C5", noSwitch, `{ $.a-b = 1 }`, `{"a-b":1}`, match, match},
	{"C5", noSwitch, `{ $.A = 1 }`, `{"a":1}`, noMatch, noMatch},
	{"C5", noSwitch, `{ $.a = 1.50 }`, `{"a":1.5}`, match, match},
	{"C5", noSwitch, `{ $.a[*].b = 2 }`, `{"a":[{"b":1},{"b":2}]}`, match, match},
	{"C5", noSwitch, `{ $.a > 1 }`, `{"a":1e999}`, match, match},
	{"C5", noSwitch, `{ $.a < -1 }`, `{"a":-1e999}`, match, match},

	// C6: space-delimited field count, grouping and comparison.
	{"C6", fieldCountSwitch, `[w1=ERROR]`, "ERROR x y", noMatch, noMatch},
	{"C6", fieldCountSwitch, `[w1=ERROR, w2]`, "ERROR x y", match, noMatch},
	{"C6", fieldCountSwitch, `[w1, w2=y]`, "ERROR x y", noMatch, noMatch},
	{"C6", fieldCountSwitch, `[w1, w2="x y"]`, "ERROR x y", match, noMatch},
	{"C6", fieldCountSwitch, `[w1=ERROR, w2]`, "ERROR x", match, match},
	{"C6", fieldCountSwitch, `[w1=ERROR, w2]`, "ERROR", noMatch, noMatch},
	{"C6", noSwitch, `[a, b, c]`, `"q r" [s t] u`, match, match},
	{"C6", noSwitch, `[a, b, c]`, "[unclosed a b", match, match},
	{"C6", noSwitch, `[a, b, c]`, `"unclosed a b`, match, match},
	{"C6", fieldCountSwitch, `[a, b, c]`, "[a [b] c] d e", match, noMatch},
	{"C6", noSwitch, `[a, b]`, "x\ty", match, match},
	{"C6", noSwitch, `[a, b]`, "  x y  ", match, match},
	{"C6", noSwitch, `[a=x, a=y]`, "x y", match, match},
	{"C6", fieldCountSwitch, `[a, b, c,]`, "x y z", noMatch, noMatch},
	{"C6", fieldCountSwitch, `[a, b, c,]`, "x y z w", match, match},
	{"C6", noSwitch, `[a=%127\.0\.0\.[1-9]%, b, c]`, "127.0.0.10 a b", match, match},
	{"C6", noSwitch, `[a, b=404, c]`, "x 0404 y", match, match},
	{"C6", noSwitch, `[a, b=404, c]`, "x 404.0 y", match, match},
	{"C6", noSwitch, `[a, b>400, c]`, "x abc y", noMatch, noMatch},
	{"C6", noSwitch, `[a, b="404", c]`, "x 0404 y", noMatch, noMatch},
	{"C6", noSwitch, `[a, b>1, c]`, "x 1e999 y", match, match},
	{"C6", noSwitch, `[1a, b]`, "x y", match, match},
	{"C6", noSwitch, `[a, ..., ...]`, "x y", badSyntax, badSyntax},

	// C7: malformed patterns.
	{"C7", noSwitch, `{ $.n = }`, "x", badSyntax, badSyntax},
	{"C7", noSwitch, `[a, b`, "x", badSyntax, badSyntax},
	{"C7", noSwitch, `"unterminated`, "x", badSyntax, badSyntax},
	{"C7", noSwitch, `ERROR {`, "x", badTerm, badTerm},
	{"C7", noSwitch, `a&b`, "x", badTerm, badTerm},
	{"C7", noSwitch, `a|b`, "x", badTerm, badTerm},
	{"C7", noSwitch, `a!b`, "x", badTerm, badTerm},
	{"C7", noSwitch, `-`, "x", badSyntax, badSyntax},
	{"C7", noSwitch, `?`, "x", badSyntax, badSyntax},
	{"C7", noSwitch, `- A`, "x", badSyntax, badSyntax},
	{"C7", noSwitch, `{}`, "x", badSyntax, badSyntax},
	{"C7", noSwitch, `{ }`, "x", badSyntax, badSyntax},
	{"C7", noSwitch, `[ ]`, "x", match, match},
	{"C7", noSwitch, `{ $.a = 1 } B`, `{"a":1}`, badSyntax, badSyntax},
	{"C7", noSwitch, `A { $.a = 1 }`, "x", badTerm, badTerm},
	{"C7", noSwitch, `%A%B`, "AB", badSyntax, badSyntax},
	{"C7", noSwitch, `A%B%`, "AB", badTerm, badTerm},
	{"C7", noSwitch, `{ $.a = %a% && $.b = %b% && $.c = %c% }`, `{"a":"a"}`, badSyntax, badSyntax},
	{"C7", noSwitch, `{ $.a IS NOT NULL }`, `{"a":1}`, badSyntax, badSyntax},
	{"C7", noSwitch, `{ $.a = 1e999 }`, `{"a":"1e999"}`, badSyntax, badSyntax},
	{"C7", noSwitch, `{ $.a = 1e+999 }`, `{"a":"1e+999"}`, badSyntax, badSyntax},
	{"C7", noSwitch, `[a, b>1e999, c]`, "x 1e999 y", badSyntax, badSyntax},
	{"C7", noSwitch, `{ $.a = %a{1001}% }`, `{"a":"a"}`, badSyntax, badSyntax},
	{"C7", noSwitch, `ERROR:`, "ERROR: x", match, match},
	{"C7", noSwitch, strings.Repeat("A", 1024), strings.Repeat("A", 1024), match, match},
}

// optionSpace is every Options value: each switch at each of its settings.
var optionSpace = []Options{
	{},
	{TermBoundary: TermToken},
	{FieldCount: FieldCountExact},
	{TermBoundary: TermToken, FieldCount: FieldCountExact},
}

// expected is the row's outcome under o: alt when o moves the row's own
// switch off its default, want otherwise. A row that changes under any other
// setting is mislabeled.
func (r residual) expected(o Options) outcome {
	switch r.sw {
	case termBoundarySwitch:
		if o.TermBoundary != TermSubstring {
			return r.alt
		}
	case fieldCountSwitch:
		if o.FieldCount != FieldCountLastAbsorbs {
			return r.alt
		}
	}
	return r.want
}

// Every row is checked under every Options value, so a row marked noSwitch
// (or with one switch) that another setting flips fails here.
func TestResidualCasesDependOnlyOnTheirSwitch(t *testing.T) {
	t.Parallel()
	for _, r := range residuals {
		name := r.record + "/" + r.pattern + "/" + r.event
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if r.sw == noSwitch && r.alt != r.want {
				t.Fatalf("row has no switch but alt %v differs from want %v", r.alt, r.want)
			}
			for _, o := range optionSpace {
				if got, want := evaluate(o, r.pattern, r.event), r.expected(o); got != want {
					t.Errorf("%+v: pattern %q on %q: got %v, want %v (switch %q)", o, r.pattern, r.event, got, want, r.sw)
				}
			}
		})
	}
}

// Rejections AWS does not document carry this package's own text, which must
// name the problem without leaking RE2's or strconv's wording.
func TestUndocumentedRejectionTexts(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ pattern, want string }{
		{`{ $.a = 1e+999 }`, "Invalid filter pattern: number 1e+999 is out of range at offset 8."},
		{`%a{1001}%`, "Invalid filter pattern: invalid regular expression at offset 0."},
	} {
		_, err := Compile(c.pattern)
		if err == nil {
			t.Errorf("Compile(%q) accepted", c.pattern)
			continue
		}
		if err.Cause() != CauseSyntax || err.Message() != c.want {
			t.Errorf("Compile(%q) = %v %q, want %v %q", c.pattern, err.Cause(), err.Message(), CauseSyntax, c.want)
		}
	}
}

func TestResidualSwitchNamesAreExported(t *testing.T) {
	t.Parallel()
	switches := Options{}.Switches()
	names := make([]string, 0, len(switches))
	for _, s := range switches {
		names = append(names, s.Name)
	}
	for _, r := range residuals {
		if r.sw != noSwitch && !slices.Contains(names, r.sw) {
			t.Errorf("row %s/%q names switch %q, which Switches does not return", r.record, r.pattern, r.sw)
		}
	}
}

func TestSwitchesReportTheCurrentSettings(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		o    Options
		want []Switch
	}{
		{Options{}, []Switch{
			{Name: "logs.filter.term-boundary", Value: "substring", Ref: "docs/research/phase1/logs-filter-patterns.md#5", Record: "R10/C1"},
			{Name: "logs.filter.delimited-field-count", Value: "last-absorbs", Ref: "docs/research/phase1/logs-filter-patterns.md#3", Record: "R10/C6"},
		}},
		{Options{TermBoundary: TermToken, FieldCount: FieldCountExact}, []Switch{
			{Name: "logs.filter.term-boundary", Value: "token", Ref: "docs/research/phase1/logs-filter-patterns.md#5", Record: "R10/C1"},
			{Name: "logs.filter.delimited-field-count", Value: "exact", Ref: "docs/research/phase1/logs-filter-patterns.md#3", Record: "R10/C6"},
		}},
	} {
		if got := c.o.Switches(); !slices.Equal(got, c.want) {
			t.Errorf("%+v.Switches() = %+v, want %+v", c.o, got, c.want)
		}
	}
}
