package jsonpath_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/jsonpath"
)

// Each Plain construct of stepfunctions-asl.md 4.5 prints in a canonical
// form that parses again to the same form; the canonical spelling is
// understudy's.
func TestPlainPathCanonicalForm(t *testing.T) {
	tests := []struct{ in, want string }{
		{"$.vals[-3:]", "$.vals[-3:]"},
		{"$.a[0,1]", "$.a[0,1]"},
		{"$.a[0:2]", "$.a[0:2]"},
		{"$.a[::2]", "$.a[::2]"},
		{"$[*][*]", "$[*][*]"},
		{"$.*", "$[*]"},
		{"$.order..product", "$.order..product"},
		{"$..*", "$..*"},
		{"$..['a b']", "$..['a b']"},
		{"$.abc.['def ghi']", "$.abc['def ghi']"},
		{`$.store\.book`, "$['store.book']"},
		{`$['it\'s']`, `$['it\'s']`},
		{`$["dq"]`, "$.dq"},
		{"$.users[?(@.status == 0)]", "$.users[?(@.status == 0)]"},
		{`$.a[?(@['x y'] != "s")]`, `$.a[?(@['x y'] != 's')]`},
		{"$.a[?(@.b>=1.5)]", "$.a[?(@.b >= 1.5)]"},
		{"$.a[?(@ == null)]", "$.a[?(@ == null)]"},
		{"$$.Execution.Input", "$$.Execution.Input"},
	}
	for _, tt := range tests {
		p, err := jsonpath.Parse(tt.in, jsonpath.Plain)
		if err != nil {
			t.Errorf("Parse(%q) = %v", tt.in, err)
			continue
		}
		if p.String() != tt.want {
			t.Errorf("Parse(%q).String() = %q, want %q", tt.in, p.String(), tt.want)
		}
		q, err := jsonpath.Parse(p.String(), jsonpath.Plain)
		if err != nil || q.String() != p.String() {
			t.Errorf("Parse(%q) = %q, %v; want the same canonical form", p.String(), q.String(), err)
		}
	}
}

// A "$$" path reads the context object (stepfunctions-asl.md 4.7).
func TestIsContext(t *testing.T) {
	for s, want := range map[string]bool{"$$.Map.Item.Value": true, "$$": true, "$.a": false, "$": false} {
		p, err := jsonpath.Parse(s, jsonpath.Reference)
		if err != nil || p.IsContext() != want {
			t.Errorf("Parse(%q).IsContext() = %v, %v; want %v", s, p.IsContext(), err, want)
		}
	}
}

// Malformed filters and slices do not parse.
func TestParseRefusesMalformedPlainConstructs(t *testing.T) {
	for _, s := range []string{
		"$.a[?(@.b)]", "$.a[?(@.b == )]", "$.a[?(@.b == 1 2)]", "$.a[?(@.b == x)]", "$.a[?(@[*] == 1)]",
		"$.a[0::0]", "$.a[1,]", "$.a[?@.b == 1]", "$.a[*", "$...a", "$.a b",
	} {
		if p, err := jsonpath.Parse(s, jsonpath.Plain); err == nil {
			t.Errorf("Parse(%q) = %q, want an error", s, p.String())
		}
	}
}
