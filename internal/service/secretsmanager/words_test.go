package secretsmanager

import (
	"slices"
	"strings"
	"testing"
	"unicode"

	"pgregory.net/rapid"
)

// The all filter's words: the user guide's credsDatabase#892 example, the
// My_Secret example, and acronyms and digits in names.
func TestWordsExamples(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"credsDatabase#892", []string{"creds", "Database", "892"}},
		{"My_Secret", []string{"My", "Secret"}},
		{"app/prod/db", []string{"app", "prod", "db"}},
		{"DBPassword", []string{"DB", "Password"}},
		{"v2beta", []string{"v", "2", "beta"}},
		{"ALLCAPS", []string{"ALLCAPS"}},
		{" -/_+=.@! ", nil},
		{"", nil},
	} {
		if got := words(c.in); !slices.Equal(got, c.want) {
			t.Errorf("words(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// words keeps every letter and digit of its input, in order, drops every
// other character, and never returns a word that mixes letters and digits
// or has a lower-case letter followed by an upper-case one.
func TestWordsProperties(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		in := rapid.StringOf(rapid.SampledFrom([]rune("aZz9B0 _-/#.éÉ1qQ"))).Draw(rt, "in")
		ws := words(in)
		alnum := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return r
			}
			return -1
		}, in)
		if got := strings.Join(ws, ""); got != alnum {
			rt.Fatalf("words(%q) joined = %q, want the letters and digits %q", in, got, alnum)
		}
		for _, w := range ws {
			rs := []rune(w)
			if len(rs) == 0 {
				rt.Fatalf("words(%q) has an empty word", in)
			}
			for i := 1; i < len(rs); i++ {
				if unicode.IsDigit(rs[i-1]) != unicode.IsDigit(rs[i]) || unicode.IsLower(rs[i-1]) && unicode.IsUpper(rs[i]) {
					rt.Fatalf("words(%q) has %q, which spans a word boundary", in, w)
				}
			}
		}
	})
}
