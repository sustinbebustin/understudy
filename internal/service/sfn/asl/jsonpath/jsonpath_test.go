package jsonpath_test

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/jsonpath"
)

// decode reads one JSON text into a document value.
func decode(tb testing.TB, s string) document.Value {
	tb.Helper()
	d := jsonx.NewDecoder([]byte(s))
	v, err := d.ReadValue()
	if err == nil {
		err = d.End()
	}
	if err != nil {
		tb.Fatalf("decode %q: %v", s, err)
	}
	return v
}

// encode writes v as compact JSON in its own member order, for messages.
func encode(v document.Value) string {
	e := jsonx.NewEncoder(jsonx.WireOrder)
	e.Value(v)
	b, err := e.Bytes()
	if err != nil {
		return fmt.Sprintf("<unencodable %#v: %v>", v, err)
	}
	return string(b)
}

// equal compares JSON values as a client does: objects as member sets,
// arrays in order, numbers by value.
func equal(a, b document.Value) bool {
	switch a := a.(type) {
	case document.Map:
		b, ok := b.(document.Map)
		if !ok || len(a) != len(b) {
			return false
		}
		for _, ea := range a {
			found := false
			for _, eb := range b {
				if ea.Key == eb.Key {
					found = equal(ea.Value, eb.Value)
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	case document.List:
		b, ok := b.(document.List)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !equal(a[i], b[i]) {
				return false
			}
		}
		return true
	case document.Number:
		b, ok := b.(document.Number)
		if !ok {
			return false
		}
		x, okx := new(big.Float).SetString(string(a))
		y, oky := new(big.Float).SetString(string(b))
		return okx && oky && x.Cmp(y) == 0
	case document.String:
		b, ok := b.(document.String)
		return ok && a == b
	case document.Bool:
		b, ok := b.(document.Bool)
		return ok && a == b
	case document.Null:
		_, ok := b.(document.Null)
		return ok
	}
	return false
}

func parse(tb testing.TB, s string, k jsonpath.Kind) jsonpath.Path {
	tb.Helper()
	p, err := jsonpath.Parse(s, k)
	if err != nil {
		tb.Fatalf("Parse(%q, %v) = %v, want a path", s, k, err)
	}
	return p
}

// Each row is a documented or recorded evaluation of a Plain Path
// (stepfunctions-asl.md 4.5).
func TestEvalDocumentedAndRecordedResults(t *testing.T) {
	tests := []struct {
		name, path, input, want, source string
	}{
		{"root", "$", `{"a":1}`, `{"a":1}`, "spec#ref-paths: $ is the whole input"},
		{"dot name", "$.a.b", `{"a":{"b":[1,2]}}`, `[1,2]`, "spec#ref-paths"},
		{"index", "$.vals[0]", `{"vals":[10,20,30,40,50]}`, `10`, "stepfunctions-asl.md 4.5, spec#payload-template"},
		{"negative slice", "$.vals[-3:]", `{"vals":[10,20,30,40,50]}`, `[30,40,50]`, "stepfunctions-asl.md 4.5, spec#payload-template"},
		{"index union", "$.a[0,1]", `{"a":[1,2,3]}`, `[1,2]`, "stepfunctions-asl.md 4.5, spec input-output example"},
		{"slice", "$.a[0:2]", `{"a":[1,2,3]}`, `[1,2]`, "stepfunctions-asl.md 4.5, aws-doc dg/input-output-inputpath-params"},
		{"wildcard flatten", "$[*][*]", `[[1,2],[3]]`, `[1,2,3]`, "stepfunctions-asl.md 4.5, aws-doc dg/amazon-states-language-paths"},
		{"recursive descent", "$.order..product", `{"order":{"items":[{"product":"a"},{"product":"b"}]}}`, `["a","b"]`, "stepfunctions-asl.md 4.5, aws-doc dg/workflow-variables"},
		{"bracket name with space", "$.abc.['def ghi']", `{"abc":{"def ghi":7}}`, `7`, "stepfunctions-asl.md 4.5, aws-doc paths page"},
		{"escaped dot", `$.store\.book`, `{"store.book":1,"store":{"book":2}}`, `1`, "stepfunctions-asl.md 4.5, aws-doc paths page"},
		{"escaped at", `$.foo\@bar`, `{"foo@bar":3}`, `3`, "stepfunctions-asl.md 4.5, aws-doc paths page"},
		{"bracket quote escapes", `$['Test\\\"\"Name\"']`, `{"Test\\\"\"Name\"":"found"}`, `"found"`, "aws-recorded, localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L27682-L27703"},
		{"filter", "$.users[?(@.status == 0)]", `{"users":[{"year":1997,"status":0},{"year":1998,"status":1}]}`, `[{"year":1997,"status":0}]`, "aws-recorded, localstack@8b9a79f v2/base/test_base.snapshot.json:L1066"},
		{"index ten of eleven", "$.items[10]", `{"items":[{"item_key":0},{"item_key":1},{"item_key":2},{"item_key":3},{"item_key":4},{"item_key":5},{"item_key":6},{"item_key":7},{"item_key":8},{"item_key":9},{"item_key":10}]}`, `{"item_key":10}`, "aws-recorded, localstack@8b9a79f v2/base/test_base.snapshot.json:L1360"},
		{"wildcard on missing root items", "$[*]", `{}`, `[]`, "aws-recorded, localstack@8b9a79f v2/base/test_base.snapshot.json:L1432-L2180"},
		{"wildcard on missing member", "$.items[*]", `{}`, `[]`, "aws-recorded, same lines"},
		{"slice on missing member", "$.items[1:]", `{}`, `[]`, "aws-recorded, same lines"},
		{"slice through missing parents", "$.item.items[1:5].itemValue", `{}`, `[]`, "aws-recorded, same lines"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := parse(t, tt.path, jsonpath.Plain)
			got, err := p.Eval(decode(t, tt.input), nil)
			if err != nil {
				t.Fatalf("Eval(%s) on %s = %v, want %s (%s)", tt.path, tt.input, err, tt.want, tt.source)
			}
			if want := decode(t, tt.want); !equal(got, want) {
				t.Fatalf("Eval(%s) on %s = %s, want %s (%s)", tt.path, tt.input, encode(got), tt.want, tt.source)
			}
		})
	}
}

// A "$$" path reads the context object, under both kinds
// (stepfunctions-asl.md 4.7).
func TestEvalContextPath(t *testing.T) {
	ctx := decode(t, `{"Execution":{"Input":{"k":"v"},"Name":"run-1"},"StateMachine":{"Name":"sm"}}`)
	for _, k := range []jsonpath.Kind{jsonpath.Reference, jsonpath.Plain} {
		p := parse(t, "$$.Execution.Input.k", k)
		got, err := p.Eval(decode(t, `{"k":"input, not context"}`), ctx)
		if err != nil || !equal(got, document.String("v")) {
			t.Errorf("kind %v: Eval($$.Execution.Input.k) = %v, %v; want \"v\" from the context", k, got, err)
		}
	}
}

// The recorded runtime texts of a missing definite path
// (stepfunctions-asl.md 4.6).
func TestEvalMissingDefinitePath(t *testing.T) {
	tests := []struct {
		path, input, reason, message, source string
	}{
		{
			"$.NoSuchInputPath", `{"a":1}`,
			"No results for path: $['NoSuchInputPath']",
			"Invalid path '$.NoSuchInputPath' : No results for path: $['NoSuchInputPath']",
			"aws-recorded, localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L26552",
		},
		{
			"$.NoSuchOutputPath", `{}`,
			"No results for path: $['NoSuchOutputPath']",
			"Invalid path '$.NoSuchOutputPath' : No results for path: $['NoSuchOutputPath']",
			"aws-recorded, same file L26605",
		},
		{
			"$.no.such.result.path", `{"a":1}`,
			"Missing property in path $['no']",
			"Invalid path '$.no.such.result.path' : Missing property in path $['no']",
			"aws-recorded, same file L11164",
		},
	}
	for _, tt := range tests {
		for _, k := range []jsonpath.Kind{jsonpath.Reference, jsonpath.Plain} {
			t.Run(fmt.Sprintf("%s/%v", tt.path, k), func(t *testing.T) {
				p := parse(t, tt.path, k)
				got, err := p.Eval(decode(t, tt.input), nil)
				var miss *jsonpath.MissingError
				if !errors.As(err, &miss) {
					t.Fatalf("Eval(%s) = %v, %v; want a *MissingError (%s)", tt.path, got, err, tt.source)
				}
				if miss.Path != tt.path || miss.Reason != tt.reason || err.Error() != tt.message {
					t.Fatalf("Eval(%s) error = %+v %q\nwant reason %q, message %q (%s)", tt.path, miss, err.Error(), tt.reason, tt.message, tt.source)
				}
			})
		}
	}
}

// A Reference Path is dot and bracket notation only: no "@ , : ?",
// no wildcard, no filters, no functions (stepfunctions-asl.md 4.5, aws-doc
// dg/amazon-states-language-paths; spec#ref-paths).
func TestReferencePathRefusesPlainConstructs(t *testing.T) {
	for _, s := range []string{
		"$.a[*]", "$.*", "$..a", "$.a[0:1]", "$.a[0,1]", "$.a[?(@.b == 1)]", "$.a[-1]",
	} {
		t.Run(s, func(t *testing.T) {
			if _, err := jsonpath.Parse(s, jsonpath.Plain); err != nil {
				t.Fatalf("Parse(%q, Plain) = %v, want it accepted (stepfunctions-asl.md 4.5)", s, err)
			}
			_, err := jsonpath.Parse(s, jsonpath.Reference)
			var syn *jsonpath.SyntaxError
			if !errors.As(err, &syn) || syn.Path != s {
				t.Fatalf("Parse(%q, Reference) = %v, want a *SyntaxError naming the path", s, err)
			}
		})
	}
}

// JSONPath functions, multi-clause filters and filter scripts are refused
// with the construct named; the coverage note declares the refusal
// (understudy's choice while AWS's acceptance is unknown, ASL-19).
func TestPlainPathRefusesUnsupportedConstructs(t *testing.T) {
	for _, s := range []string{
		"$.a.length()",
		"$.a[?(@.b == 1 && @.c == 2)]",
		"$.a[?(@.b == 1 || @.c == 2)]",
		"$.a[(@.length-1)]",
	} {
		t.Run(s, func(t *testing.T) {
			_, err := jsonpath.Parse(s, jsonpath.Plain)
			var un *jsonpath.UnsupportedError
			if !errors.As(err, &un) || un.Path != s || un.Construct == "" || !strings.Contains(s, un.Construct) {
				t.Fatalf("Parse(%q, Plain) = %v, want an *UnsupportedError naming a construct of the path", s, err)
			}
		})
	}
}

func TestParseRefusesMalformedPaths(t *testing.T) {
	for _, s := range []string{"", "a.b", "$.", "$[", "$['a'", "$.a[x]", "$$$"} {
		for _, k := range []jsonpath.Kind{jsonpath.Reference, jsonpath.Plain} {
			if p, err := jsonpath.Parse(s, k); err == nil {
				t.Errorf("Parse(%q, %v) = %q, want an error", s, k, p.String())
			}
		}
	}
}

// identifier draws a name that shorthand dot notation can carry.
func identifier() *rapid.Generator[string] {
	return rapid.StringMatching(`[A-Za-z_][A-Za-z0-9_]{0,8}`)
}

// referencePath draws a Reference Path of shorthand names and
// non-negative indexes, written the way String must return it unchanged.
func referencePath() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		var b strings.Builder
		if rapid.Bool().Draw(t, "context") {
			b.WriteString("$$")
		} else {
			b.WriteString("$")
		}
		n := rapid.IntRange(0, 6).Draw(t, "steps")
		for i := range n {
			if rapid.Bool().Draw(t, fmt.Sprintf("index%d", i)) {
				fmt.Fprintf(&b, "[%d]", rapid.IntRange(0, 99).Draw(t, fmt.Sprintf("n%d", i)))
			} else {
				b.WriteString("." + identifier().Draw(t, fmt.Sprintf("name%d", i)))
			}
		}
		return b.String()
	})
}

// Reference Path parse and print round trip.
func TestReferencePathRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := referencePath().Draw(t, "path")
		for _, k := range []jsonpath.Kind{jsonpath.Reference, jsonpath.Plain} {
			p, err := jsonpath.Parse(s, k)
			if err != nil {
				t.Fatalf("Parse(%q, %v) = %v", s, k, err)
			}
			if p.String() != s {
				t.Fatalf("Parse(%q, %v).String() = %q, want the input unchanged", s, k, p.String())
			}
		}
	})
}

// Bracket names with any characters print canonically: the printed form
// parses again to the same path.
func TestBracketPathCanonicalForm(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		key := rapid.StringN(1, 12, -1).Draw(t, "key")
		s := "$['" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(key) + "']"
		p, err := jsonpath.Parse(s, jsonpath.Reference)
		if err != nil {
			t.Fatalf("Parse(%q) = %v", s, err)
		}
		q, err := jsonpath.Parse(p.String(), jsonpath.Reference)
		if err != nil {
			t.Fatalf("Parse(String() = %q) = %v", p.String(), err)
		}
		if q.String() != p.String() {
			t.Fatalf("String not canonical: %q then %q", p.String(), q.String())
		}
		in := document.Map{{Key: key, Value: document.String("v")}}
		for _, x := range []jsonpath.Path{p, q} {
			got, err := x.Eval(in, nil)
			if err != nil || !equal(got, document.String("v")) {
				t.Fatalf("Eval(%q) on {%q: \"v\"} = %v, %v", x.String(), key, got, err)
			}
		}
	})
}

// tree draws a JSON value whose object keys are identifiers.
func tree(t *rapid.T, depth int, label string) document.Value {
	kind := rapid.IntRange(0, 5).Draw(t, label+"kind")
	if depth == 0 {
		kind = rapid.IntRange(0, 3).Draw(t, label+"leaf")
	}
	switch kind {
	case 0:
		return document.Null{}
	case 1:
		return document.Bool(rapid.Bool().Draw(t, label+"b"))
	case 2:
		return document.Number(strconv.Itoa(rapid.IntRange(-1000, 1000).Draw(t, label+"n")))
	case 3:
		return document.String(rapid.String().Draw(t, label+"s"))
	case 4:
		n := rapid.IntRange(0, 4).Draw(t, label+"len")
		out := document.List{}
		for i := range n {
			out = append(out, tree(t, depth-1, fmt.Sprintf("%s[%d]", label, i)))
		}
		return out
	default:
		keys := rapid.SliceOfNDistinct(identifier(), 0, 4, func(s string) string { return s }).Draw(t, label+"keys")
		out := document.Map{}
		for _, k := range keys {
			out = append(out, document.Entry{Key: k, Value: tree(t, depth-1, label+"."+k)})
		}
		return out
	}
}

// walk draws a definite path that exists in v and returns it with the
// node it selects.
func walk(t *rapid.T, v document.Value) (string, document.Value) {
	path := "$"
	for i := 0; ; i++ {
		switch x := v.(type) {
		case document.Map:
			if len(x) == 0 || !rapid.Bool().Draw(t, fmt.Sprintf("descend%d", i)) {
				return path, v
			}
			e := x[rapid.IntRange(0, len(x)-1).Draw(t, fmt.Sprintf("key%d", i))]
			path += "." + e.Key
			v = e.Value
		case document.List:
			if len(x) == 0 || !rapid.Bool().Draw(t, fmt.Sprintf("descend%d", i)) {
				return path, v
			}
			n := rapid.IntRange(0, len(x)-1).Draw(t, fmt.Sprintf("index%d", i))
			path += fmt.Sprintf("[%d]", n)
			v = x[n]
		case document.Null, document.Bool, document.Number, document.String:
			return path, v
		}
	}
}

// A definite path returns the one node it names, never a list
// of matches (stepfunctions-asl.md 4.5: a Reference Path "identifies a
// single node").
func TestDefinitePathReturnsOneNode(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := tree(t, 4, "in")
		s, want := walk(t, in)
		for _, k := range []jsonpath.Kind{jsonpath.Reference, jsonpath.Plain} {
			p, err := jsonpath.Parse(s, k)
			if err != nil {
				t.Fatalf("Parse(%q, %v) = %v", s, k, err)
			}
			got, err := p.Eval(in, nil)
			if err != nil {
				t.Fatalf("Eval(%q) on %s = %v", s, encode(in), err)
			}
			if !equal(got, want) {
				t.Fatalf("Eval(%q) on %s = %s, want the node %s", s, encode(in), encode(got), encode(want))
			}
		}
	})
}

// norm writes names as the recorded texts do: $['a']['b']
// (stepfunctions-asl.md 4.6).
func norm(names []string) string {
	var b strings.Builder
	b.WriteString("$")
	for _, n := range names {
		b.WriteString("['" + n + "']")
	}
	return b.String()
}

// A missing name at the end of a path of names is "No results for path",
// a missing name before the end is "Missing property in path" up to it
// (recorded forms, stepfunctions-asl.md 4.6).
func TestMissingNameReasons(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		prefix := rapid.SliceOfN(identifier(), 0, 3).Draw(t, "prefix")
		// Build an object along prefix so every prefix step exists.
		var in document.Value = document.Map{}
		for _, p := range slices.Backward(prefix) {
			in = document.Map{{Key: p, Value: in}}
		}
		missing := "zz" + identifier().Draw(t, "missing")
		rest := rapid.SliceOfN(identifier(), 0, 2).Draw(t, "rest")
		names := append(append(append([]string{}, prefix...), missing), rest...)
		s := "$." + strings.Join(names, ".")
		p, err := jsonpath.Parse(s, jsonpath.Reference)
		if err != nil {
			t.Fatalf("Parse(%q) = %v", s, err)
		}
		_, err = p.Eval(in, nil)
		var miss *jsonpath.MissingError
		if !errors.As(err, &miss) {
			t.Fatalf("Eval(%q) on %s = %v, want a *MissingError", s, encode(in), err)
		}
		upTo := append(append([]string{}, prefix...), missing)
		want := "Missing property in path " + norm(upTo)
		if len(rest) == 0 {
			want = "No results for path: " + norm(upTo)
		}
		if miss.Reason != want {
			t.Fatalf("Eval(%q) reason = %q, want %q", s, miss.Reason, want)
		}
	})
}
