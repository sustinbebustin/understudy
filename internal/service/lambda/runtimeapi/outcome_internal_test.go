package runtimeapi

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Lambda keeps an error type containing Runtime.Xy or Function.Xy (a
// capital, then at least one more letter) anywhere in it, and replaces any
// other with Function.Unknown or Runtime.Unknown by its prefix (RIE
// fatalerror.go, lambda-api.md 8.3).
func TestNormalizeErrorType(t *testing.T) {
	examples := map[string]string{
		"Runtime.NoSuchHandler":  "Runtime.NoSuchHandler",
		"Runtime.X":              "Runtime.Unknown",
		"Runtime.Foo_Bar":        "Runtime.Foo_Bar",
		"Function.Err1":          "Function.Err1",
		"Function.bad":           "Function.Unknown",
		"Function.":              "Function.Unknown",
		"":                       "Runtime.Unknown",
		"Unhandled":              "Runtime.Unknown",
		"custom/Runtime.Crashed": "custom/Runtime.Crashed",
	}
	for in, want := range examples {
		if got := normalizeErrorType(in); got != want {
			t.Errorf("normalizeErrorType(%q) = %q, want %q", in, got, want)
		}
	}
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.OneOf(
			rapid.String(),
			rapid.StringMatching(`[a-z/]{0,3}(Runtime|Function)\.[A-Z][a-zA-Z]{0,12}[_0-9]?`),
			rapid.StringMatching(`(Runtime|Function)\.[a-zA-Z0-9._ -]{0,12}`),
		).Draw(t, "in")
		out := normalizeErrorType(in)
		switch {
		case containsValidType(in):
			if out != in {
				t.Fatalf("normalizeErrorType(%q) = %q, want it kept", in, out)
			}
		case strings.HasPrefix(in, "Function."):
			if out != "Function.Unknown" {
				t.Fatalf("normalizeErrorType(%q) = %q, want Function.Unknown", in, out)
			}
		default:
			if out != "Runtime.Unknown" {
				t.Fatalf("normalizeErrorType(%q) = %q, want Runtime.Unknown", in, out)
			}
		}
		if normalizeErrorType(out) != out {
			t.Fatalf("normalizeErrorType is not idempotent on %q", out)
		}
	})
}

// containsValidType is an oracle written apart from the implementation's
// regular expression: some "Runtime." or "Function." is followed by a
// capital letter and at least one more letter.
func containsValidType(s string) bool {
	upper := func(b byte) bool { return b >= 'A' && b <= 'Z' }
	letter := func(b byte) bool { return upper(b) || b >= 'a' && b <= 'z' }
	for _, category := range []string{"Runtime.", "Function."} {
		for rest := s; ; {
			i := strings.Index(rest, category)
			if i < 0 {
				break
			}
			reason := rest[i+len(category):]
			if len(reason) >= 2 && upper(reason[0]) && letter(reason[1]) {
				return true
			}
			rest = rest[i+1:]
		}
	}
	return false
}
