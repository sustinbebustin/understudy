package jsonpath_test

import (
	"errors"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/jsonpath"
)

// TestASL19PlainPathResults pins what understudy returns for the Plain
// constructs whose AWS results are unrecorded (stepfunctions-asl.md 4.5:
// "UNVERIFIED: whether plain Paths accept .., unions, negative indexes ...
// and what they return"). The choices follow Jayway JsonPath, whose texts
// AWS's recorded errors carry, and Python slice rules.
func TestASL19PlainPathResults(t *testing.T) {
	// inferred, ASL-19
	tests := []struct{ name, path, input, want string }{
		// A single index is definite whatever its sign, as in Jayway.
		{"negative index", "$.a[-1]", `{"a":[1,2,3]}`, `3`},
		{"union skips out of range", "$.a[2,7,0]", `{"a":[1,2,3]}`, `[3,1]`},
		{"union with negative", "$.a[0,-1]", `{"a":[1,2,3]}`, `[1,3]`},
		{"slice with step", "$.a[::2]", `{"a":[1,2,3,4,5]}`, `[1,3,5]`},
		{"slice with negative step", "$.a[::-1]", `{"a":[1,2,3]}`, `[3,2,1]`},
		{"slice bounds clamp", "$.a[-10:10]", `{"a":[1,2]}`, `[1,2]`},
		{"slice empty range", "$.a[2:1]", `{"a":[1,2,3]}`, `[]`},
		{"wildcard over object members", "$.*", `{"x":1,"y":[2]}`, `[1,[2]]`},
		{"wildcard over scalar", "$.a[*]", `{"a":1}`, `[]`},
		{"descent preorder", "$..b", `{"b":1,"c":{"b":2,"d":[{"b":3}]}}`, `[1,2,3]`},
		{"descent wildcard", "$..*", `{"a":{"b":1}}`, `[{"b":1},1]`},
		{"descent index", "$..[0]", `{"a":[[1],2]}`, `[[1],1]`},
		{"filter string", "$.u[?(@.n == 'x')]", `{"u":[{"n":"x"},{"n":"y"}]}`, `[{"n":"x"}]`},
		{"filter not equal keeps other types, drops missing", "$.u[?(@.n != 1)]", `{"u":[{"n":1},{"n":"1"},{"m":1}]}`, `[{"n":"1"}]`},
		{"filter number by value", "$.u[?(@.n == 1.0)]", `{"u":[{"n":1},{"n":2}]}`, `[{"n":1}]`},
		{"filter ordered", "$.u[?(@.n >= 2)]", `{"u":[{"n":1},{"n":2},{"n":3},{"n":"9"}]}`, `[{"n":2},{"n":3}]`},
		{"filter less on strings", "$.u[?(@.s < 'b')]", `{"u":[{"s":"a"},{"s":"c"}]}`, `[{"s":"a"}]`},
		{"filter bool", "$.u[?(@.b == true)]", `{"u":[{"b":true},{"b":false}]}`, `[{"b":true}]`},
		{"filter null", "$.u[?(@.b == null)]", `{"u":[{"b":null},{"b":0}]}`, `[{"b":null}]`},
		{"filter bool unordered", "$.u[?(@.b > false)]", `{"u":[{"b":true}]}`, `[]`},
		{"filter nested ref", "$.u[?(@.p.q[0] == 1)]", `{"u":[{"p":{"q":[1]}},{"p":{"q":[2]}}]}`, `[{"p":{"q":[1]}}]`},
		{"filter on object", "$.o[?(@.k == 1)]", `{"o":{"k":1}}`, `[{"k":1}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parse(t, tt.path, jsonpath.Plain).Eval(decode(t, tt.input), nil)
			if err != nil {
				t.Fatalf("Eval(%s) on %s = %v, want %s", tt.path, tt.input, err, tt.want)
			}
			if want := decode(t, tt.want); !equal(got, want) {
				t.Fatalf("Eval(%s) on %s = %s, want %s", tt.path, tt.input, encode(got), tt.want)
			}
		})
	}
}

// TestASL19DefiniteMissTexts pins the reason of a definite miss that is not
// a missing object member, which no recording shows: understudy uses the
// recorded "No results for path" form with the whole path
// (stepfunctions-asl.md 4.6).
func TestASL19DefiniteMissTexts(t *testing.T) {
	// inferred, ASL-19
	tests := []struct{ path, input, reason string }{
		{"$.a[3]", `{"a":[1]}`, "No results for path: $['a'][3]"},
		{"$.a[-4]", `{"a":[1,2,3]}`, "No results for path: $['a'][-4]"},
		{"$.a[3].b", `{"a":[1]}`, "No results for path: $['a'][3]['b']"},
		{"$.a.b", `{"a":5}`, "No results for path: $['a']['b']"},
		{"$.a.b.c", `{"a":"s"}`, "No results for path: $['a']['b']['c']"},
		{"$[0]", `{"a":1}`, "No results for path: $[0]"},
		{"$['x y'].z", `{}`, "Missing property in path $['x y']"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			_, err := parse(t, tt.path, jsonpath.Plain).Eval(decode(t, tt.input), nil)
			var miss *jsonpath.MissingError
			if !errors.As(err, &miss) || miss.Reason != tt.reason || miss.Path != tt.path {
				t.Fatalf("Eval(%s) on %s = %v, want a *MissingError for %q with reason %q", tt.path, tt.input, err, tt.path, tt.reason)
			}
		})
	}
}

// A missing path quotes the path as written, not its canonical form
// (stepfunctions-asl.md 4.6: "Invalid path '$.NoSuchInputPath'").
func TestMissingErrorQuotesPathAsWritten(t *testing.T) {
	const s = "$.abc.['def ghi']"
	_, err := parse(t, s, jsonpath.Plain).Eval(decode(t, `{"abc":{}}`), nil)
	want := "Invalid path '" + s + "' : No results for path: $['abc']['def ghi']"
	if err == nil || err.Error() != want {
		t.Fatalf("Eval(%s) = %v, want %q", s, err, want)
	}
}

// An indefinite path always returns an array, and a definite one never
// wraps its node (stepfunctions-asl.md 4.5).
func TestIndefinitePathAlwaysReturnsAList(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := tree(t, 3, "in")
		s := rapid.SampledFrom([]string{"$[*]", "$..*", "$[0:]", "$[0,1]", "$[?(@.a == 1)]", "$..a", "$.*.*"}).Draw(t, "path")
		p, err := jsonpath.Parse(s, jsonpath.Plain)
		if err != nil {
			t.Fatalf("Parse(%q) = %v", s, err)
		}
		got, err := p.Eval(in, nil)
		if err != nil {
			t.Fatalf("Eval(%q) on %s = %v, want a list", s, encode(in), err)
		}
		if _, ok := got.(document.List); !ok {
			t.Fatalf("Eval(%q) on %s = %s, want a list", s, encode(in), encode(got))
		}
	})
}

// "$..*" selects every descendant once: its count is the number of
// members and elements in the tree.
func TestDescendWildcardCountsEveryDescendant(t *testing.T) {
	p := parse(t, "$..*", jsonpath.Plain)
	rapid.Check(t, func(t *rapid.T) {
		in := tree(t, 4, "in")
		got, err := p.Eval(in, nil)
		if err != nil {
			t.Fatal(err)
		}
		l, ok := got.(document.List)
		if !ok {
			t.Fatalf("Eval($..*) = %s, want a list", encode(got))
		}
		if want := descendants(in); len(l) != want {
			t.Fatalf("Eval($..*) on %s has %d nodes, want %d", encode(in), len(l), want)
		}
	})
}

func descendants(v document.Value) int {
	n := 0
	switch x := v.(type) {
	case document.Map:
		for _, e := range x {
			n += 1 + descendants(e.Value)
		}
	case document.List:
		for _, e := range x {
			n += 1 + descendants(e)
		}
	case document.Null, document.Bool, document.Number, document.String:
	}
	return n
}

// A "$$" path with no context object to read is a definite miss, not a
// crash.
func TestContextPathWithoutContext(t *testing.T) {
	_, err := parse(t, "$$.Execution.Id", jsonpath.Reference).Eval(decode(t, `{}`), nil)
	if _, ok := errors.AsType[*jsonpath.MissingError](err); !ok {
		t.Fatalf("Eval($$.Execution.Id) with no context = %v, want a *MissingError", err)
	}
}
