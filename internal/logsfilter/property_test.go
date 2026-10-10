package logsfilter

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// A small alphabet makes terms occur in messages often enough to exercise
// both outcomes.
func termGen() *rapid.Generator[string] {
	return rapid.OneOf(
		rapid.StringMatching(`[ABab_:=]{1,3}`),
		rapid.Map(rapid.StringMatching(`[ABab ,.]{1,4}`), func(s string) string { return `"` + s + `"` }),
		rapid.Map(rapid.StringMatching(`[ABab]{1,2}`), func(s string) string { return "%" + s + "%" }),
	)
}

func messageGen() *rapid.Generator[string] { return rapid.StringMatching(`[ABab_:=,. \t]{0,16}`) }

func optionsGen() *rapid.Generator[Options] {
	return rapid.Custom(func(t *rapid.T) Options {
		return Options{
			TermBoundary: rapid.SampledFrom([]TermBoundary{TermSubstring, TermToken}).Draw(t, "boundary"),
			FieldCount:   rapid.SampledFrom([]FieldCount{FieldCountLastAbsorbs, FieldCountExact}).Draw(t, "count"),
		}
	})
}

func mustCompile(t *rapid.T, o Options, pattern string) *Filter {
	f, err := o.Compile(pattern)
	if err != nil {
		t.Fatalf("Compile(%q): %v", pattern, err)
	}
	return f
}

func TestPropertyOptionalTermsAreOr(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		o := optionsGen().Draw(t, "options")
		a, b := termGen().Draw(t, "a"), termGen().Draw(t, "b")
		msg := messageGen().Draw(t, "message")
		either := Match(mustCompile(t, o, a), msg) || Match(mustCompile(t, o, b), msg)
		if got := Match(mustCompile(t, o, "?"+a+" ?"+b), msg); got != either {
			t.Fatalf("?%s ?%s on %q = %v, want %v", a, b, msg, got, either)
		}
	})
}

func TestPropertyOptionalTermsBesidePlainTermAreIgnored(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		o := optionsGen().Draw(t, "options")
		a, b := termGen().Draw(t, "a"), termGen().Draw(t, "b")
		msg := messageGen().Draw(t, "message")
		want := Match(mustCompile(t, o, a), msg)
		if got := Match(mustCompile(t, o, a+" ?"+b), msg); got != want {
			t.Fatalf("%s ?%s on %q = %v, want %v as for %s alone", a, b, msg, got, want, a)
		}
	})
}

func TestPropertyBlankPatternMatchesEverything(t *testing.T) {
	t.Parallel()
	blank := rapid.OneOf(
		rapid.StringMatching(`[ \t\r\n]{0,8}`),
		rapid.Map(rapid.StringMatching(`[ \t\r\n]{0,8}`), func(s string) string { return `"` + s + `"` }),
	)
	rapid.Check(t, func(t *rapid.T) {
		o := optionsGen().Draw(t, "options")
		p := blank.Draw(t, "pattern")
		msg := rapid.String().Draw(t, "message")
		if !Match(mustCompile(t, o, p), msg) {
			t.Fatalf("blank pattern %q does not match %q", p, msg)
		}
	})
}

func TestPropertyExclusionIsComplement(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		o := optionsGen().Draw(t, "options")
		a := termGen().Draw(t, "a")
		msg := messageGen().Draw(t, "message")
		if Match(mustCompile(t, o, "-"+a), msg) == Match(mustCompile(t, o, a), msg) {
			t.Fatalf("-%s and %s agree on %q", a, a, msg)
		}
	})
}

func TestPropertyTermOrderIsIrrelevant(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		o := optionsGen().Draw(t, "options")
		a, b := termGen().Draw(t, "a"), termGen().Draw(t, "b")
		if rapid.Bool().Draw(t, "exclude-a") {
			a = "-" + a
		}
		msg := messageGen().Draw(t, "message")
		ab, ba := a+" "+b, b+" "+a
		if x, y := Match(mustCompile(t, o, ab), msg), Match(mustCompile(t, o, ba), msg); x != y {
			t.Fatalf("on %q: %s = %v but %s = %v", msg, ab, x, ba, y)
		}
	})
}

// jsonConditionGen and jsonMessageGen draw from small fixed sets chosen so
// that each condition holds on some messages and fails on others.
func jsonConditionGen() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{
		`$.a = 1`, `$.a > 1`, `$.a != 2`, `$.b = "x*"`, `$.b NOT EXISTS`, `$.a IS NULL`, `$.c[*] = %y%`, `$.b = *`,
	})
}

func jsonMessageGen() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{
		`{"a":1}`, `{"a":2,"b":"xy"}`, `{"a":null}`, `{"b":"z","c":["y"]}`, `{"c":[1]}`, `not json`, `[1]`,
	})
}

func matchJSON(t *rapid.T, expr, msg string) bool {
	return Match(mustCompile(t, Options{}, "{ "+expr+" }"), msg)
}

func TestPropertyJSONOrIsDisjunction(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		a, b, m := jsonConditionGen().Draw(t, "a"), jsonConditionGen().Draw(t, "b"), jsonMessageGen().Draw(t, "message")
		either := matchJSON(t, a, m) || matchJSON(t, b, m)
		if got := matchJSON(t, a+" || "+b, m); got != either {
			t.Fatalf("{ %s || %s } on %q = %v, want %v", a, b, m, got, either)
		}
	})
}

func TestPropertyJSONAndIsCommutative(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		a, b, m := jsonConditionGen().Draw(t, "a"), jsonConditionGen().Draw(t, "b"), jsonMessageGen().Draw(t, "message")
		ab, ba := a+" && "+b, b+" && "+a
		if x, y := matchJSON(t, ab, m), matchJSON(t, ba, m); x != y {
			t.Fatalf("on %q: { %s } = %v but { %s } = %v", m, ab, x, ba, y)
		}
	})
}

func TestPropertyJSONAndBindsTighterThanOr(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		a, b, c := jsonConditionGen().Draw(t, "a"), jsonConditionGen().Draw(t, "b"), jsonConditionGen().Draw(t, "c")
		m := jsonMessageGen().Draw(t, "message")
		bare, grouped := a+" || "+b+" && "+c, a+" || ("+b+" && "+c+")"
		// Three regex conditions break the two-regex quota; both forms must
		// then be rejected alike.
		_, bareErr := Compile("{ " + bare + " }")
		_, groupedErr := Compile("{ " + grouped + " }")
		if (bareErr == nil) != (groupedErr == nil) {
			t.Fatalf("{ %s } err = %v but { %s } err = %v", bare, bareErr, grouped, groupedErr)
		}
		if bareErr != nil {
			return
		}
		if x, y := matchJSON(t, bare, m), matchJSON(t, grouped, m); x != y {
			t.Fatalf("on %q: { %s } = %v but { %s } = %v", m, bare, x, grouped, y)
		}
	})
}

func TestPropertyCompileNeverPanics(t *testing.T) {
	t.Parallel()
	pattern := rapid.OneOf(
		rapid.String(),
		rapid.StringMatching(`[ \t{}\[\]()$.*%"'\\a-c0-9=!<>&|?,+-]{0,40}`),
		rapid.Map(rapid.StringMatching(`[ $.*%"'\\a-c0-9=!<>&|()\[\]-]{0,30}`), func(s string) string { return "{" + s }),
		rapid.Map(rapid.StringMatching(`[ .*%"\\a-c0-9=!<>&|,\]-]{0,30}`), func(s string) string { return "[" + s }),
	)
	rapid.Check(t, func(t *rapid.T) {
		p := pattern.Draw(t, "pattern")
		o := optionsGen().Draw(t, "options")
		f, err := o.Compile(p)
		if err != nil {
			if msg := err.Message(); !strings.HasPrefix(msg, "Invalid ") && !strings.HasPrefix(msg, "1 validation error") {
				t.Fatalf("Compile(%q) error message %q has no known prefix", p, msg)
			}
			return
		}
		Match(f, messageGen().Draw(t, "message"))
	})
}
