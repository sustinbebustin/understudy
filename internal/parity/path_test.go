package parity

import (
	"errors"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func TestParsePathReadsTheGrammar(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"status",
		"error",
		"error/code",
		"error/message",
		"header",
		"header/content-type",
		"header/x-amzn-query-error",
		"body",
		// JSON: RFC 6901 reference tokens, array indexes in decimal.
		"body/Messages/0/ReceiptHandle",
		"body/a~1b",
		"body/a~0b",
		// XML: the root element first, name[i] for a later sibling, @attr.
		"body/ErrorResponse/Error/Code",
		"body/ListResponse/member[1]",
		"body/AssumeRoleResponse/@xmlns",
		"body/r/@xmlns:p",
		// An exchange before the final one of a compare: all step.
		"exchange/0/status",
		"exchange/2/body/__type",
		"exchange/0/error",
	} {
		p, err := ParsePath(s)
		if err != nil || string(p) != s {
			t.Errorf("ParsePath(%q) = %q, %v; want %q, nil", s, p, err, s)
		}
	}
}

func TestParsePathRefusesWhatIsNotAPath(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"",
		"Status",
		"/status",
		"status/x",
		"error/type",
		"error/code/x",
		"headers",
		"header/Content-Type", // header names are lower-case
		"bodies",
		"body/~2",
		"exchange/x/status",
		"exchange/-1/status",
		"exchange/0",
	} {
		_, err := ParsePath(s)
		var pe *PathError
		if !errors.As(err, &pe) {
			t.Errorf("ParsePath(%q) error = %v; want a *PathError", s, err)
			continue
		}
		if pe.Text != s || pe.Reason == "" {
			t.Errorf("ParsePath(%q): PathError{Text: %q, Reason: %q}; want the input and a reason", s, pe.Text, pe.Reason)
		}
	}
}

// A waiver on a path covers that path and every path below it, on a "/"
// boundary only.
func TestPathCovers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		p, q Path
		want bool
	}{
		{"status", "status", true},
		{"header", "header/content-type", true},
		{"error", "error/code", true},
		{"error", "error/message", true},
		{"body", "body/__type", true},
		{"body/a", "body/a/b/0", true},
		{"body/a", "body/ab", false},
		{"body/a/b", "body/a", false},
		{"body", "status", false},
		{"header/content-type", "header/content-length", false},
		{"exchange/0/body", "exchange/0/body/x", true},
		{"body", "exchange/0/body", false},
	}
	for _, c := range cases {
		if got := c.p.Covers(c.q); got != c.want {
			t.Errorf("Path(%q).Covers(%q) = %v; want %v", c.p, c.q, got, c.want)
		}
	}
}

// jsonPathGen draws a valid JSON body path: body, then reference tokens and
// indexes.
func jsonPathGen() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		toks := rapid.SliceOfN(rapid.OneOf(
			rapid.StringMatching(`[A-Za-z_][A-Za-z0-9_.-]{0,8}`),
			rapid.StringMatching(`[A-Za-z]{1,3}(~0|~1)[a-z]{0,3}`),
			rapid.StringMatching(`0|[1-9][0-9]{0,2}`),
		), 0, 5).Draw(t, "tokens")
		return strings.Join(append([]string{"body"}, toks...), "/")
	})
}

// Every path the grammar produces parses to itself and covers itself and
// what lies below it.
func TestPathProperties(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := jsonPathGen().Draw(t, "path")
		p, err := ParsePath(s)
		if err != nil {
			t.Fatalf("ParsePath(%q): %v", s, err)
		}
		if string(p) != s {
			t.Fatalf("ParsePath(%q) = %q", s, p)
		}
		if !p.Covers(p) {
			t.Fatalf("Path(%q).Covers itself = false", p)
		}
		child := Path(s + "/" + rapid.StringMatching(`[A-Za-z]{1,6}`).Draw(t, "child"))
		if !p.Covers(child) {
			t.Fatalf("Path(%q).Covers(%q) = false", p, child)
		}
		if child.Covers(p) {
			t.Fatalf("Path(%q).Covers(%q) = true", child, p)
		}
	})
}
