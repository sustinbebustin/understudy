package jsonpath_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/jsonpath"
)

// Place puts a ResultPath's value into the raw input: missing intermediate
// objects are created and an existing member is overwritten in place
// (stepfunctions-asl.md 4.4; spec#inputoutput-processing-examples).
func TestPlaceDocumentedExamples(t *testing.T) {
	tests := []struct{ name, path, root, value, want string }{
		{"creates parents", "$.b.greeting", `{"a":1}`, `"Hi!"`, `{"a":1,"b":{"greeting":"Hi!"}}`},
		{"overwrites in place", "$.a", `{"a":1,"b":0}`, `2`, `{"a":2,"b":0}`},
		{"root replaces", "$", `{"a":1}`, `[1]`, `[1]`},
		{"bracket name", "$['x y']", `{}`, `1`, `{"x y":1}`},
		{"into an existing object", "$.a.b", `{"a":{"c":1}}`, `2`, `{"a":{"c":1,"b":2}}`},
		{"array element", "$.a[1]", `{"a":[1,2,3]}`, `9`, `{"a":[1,9,3]}`},
		{"below an array element", "$.a[0].x", `{"a":[{}]}`, `9`, `{"a":[{"x":9}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mustParse(t, tt.path)
			root := value(t, tt.root)
			before := text(root)
			got, err := p.Place(root, value(t, tt.value))
			if err != nil || text(got) != tt.want {
				t.Fatalf("Place(%s) = %s, %v, want %s", tt.path, text(got), err, tt.want)
			}
			if text(root) != before {
				t.Errorf("Place changed its input to %s, want %s", text(root), before)
			}
		})
	}
}

// TestASL03PlaceIntoNonObject: AWS's error for a ResultPath that cannot be
// applied to its input is unrecorded (stepfunctions-asl.md 4.4; the spec
// names States.ResultPathMatchFailure). Place refuses a name step into
// anything but an object and an index step into anything but an array
// holding the index; the engine turns the refusal into States.Runtime.
func TestASL03PlaceIntoNonObject(t *testing.T) {
	// inferred, ASL-03
	tests := []struct{ name, path, root, reason string }{
		{"string input", "$.x", `"foo"`, "$ is a string, not an object"},
		{"array input", "$.x", `[1]`, "$ is an array, not an object"},
		{"through a number", "$.a.b", `{"a":1}`, "$['a'] is a number, not an object"},
		{"through null", "$.a.b", `{"a":null}`, "$['a'] is null, not an object"},
		{"index into object", "$.a[0]", `{"a":{}}`, "$['a'] is an object, not an array"},
		{"index into missing member", "$.a[0]", `{}`, "$['a'] is missing, not an array"},
		{"index out of range", "$.a[3]", `{"a":[1]}`, "$['a'] has no element 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mustParse(t, tt.path).Place(value(t, tt.root), document.Number("1"))
			var pe *jsonpath.PlaceError
			if !errors.As(err, &pe) || pe.Path != tt.path || pe.Reason != tt.reason {
				t.Fatalf("Place(%s) on %s = %v, want a PlaceError %q", tt.path, tt.root, err, tt.reason)
			}
			if want := "Unable to apply ResultPath '" + tt.path + "' to the input: " + tt.reason; err.Error() != want {
				t.Errorf("error = %q, want %q", err.Error(), want)
			}
		})
	}
}

// Over generated objects and name paths, Place succeeds exactly when every
// node the path passes through is an object or missing; then the path reads
// back the placed value, every other top-level member is unchanged, and the
// input is not modified (stepfunctions-asl.md 4.4).
func TestPlaceCreatesIntermediatesAndOverwrites(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		root := objectGen(3).Draw(t, "root")
		names := rapid.SliceOfN(rapid.SampledFrom([]string{"a", "b", "c"}), 1, 4).Draw(t, "names")
		v := document.Number(strconv.Itoa(rapid.IntRange(0, 99).Draw(t, "value")))
		p := mustParse(t, "$."+strings.Join(names, "."))
		before := text(root)

		// Each name steps from an object, or from a node that is missing and
		// so is created as one.
		placeable := true
		var node document.Value = root
		for _, n := range names {
			if node == nil {
				continue
			}
			m, isMap := node.(document.Map)
			if !isMap {
				placeable = false
				break
			}
			node = lookup(m, n)
		}

		got, err := p.Place(root, v)
		if (err == nil) != placeable {
			t.Fatalf("Place(%s) on %s = %v, want success %v", p.String(), before, err, placeable)
		}
		if text(root) != before {
			t.Fatalf("Place changed its input to %s, want %s", text(root), before)
		}
		if err != nil {
			return
		}
		if read, err := p.Eval(got, nil); err != nil || text(read) != text(v) {
			t.Fatalf("Eval(%s) after Place = %s, %v, want %s", p.String(), text(read), err, text(v))
		}
		placed, isMap := got.(document.Map)
		if !isMap {
			t.Fatalf("Place(%s) = %s, want an object", p.String(), text(got))
		}
		for _, e := range root {
			if now := lookup(placed, e.Key); e.Key != names[0] && text(now) != text(e.Value) {
				t.Fatalf("member %q changed from %s to %s", e.Key, text(e.Value), text(now))
			}
		}
	})
}

// objectGen generates objects over the keys a, b and c whose values are
// numbers, strings, null or, depth permitting, objects.
func objectGen(depth int) *rapid.Generator[document.Map] {
	return rapid.Custom(func(t *rapid.T) document.Map {
		keys := rapid.SliceOfNDistinct(rapid.SampledFrom([]string{"a", "b", "c"}), 0, 3, rapid.ID[string]).Draw(t, "keys")
		out := make(document.Map, 0, len(keys))
		for _, k := range keys {
			var v document.Value
			kind := rapid.IntRange(0, 3).Draw(t, "kind")
			switch {
			case kind == 0 && depth > 0:
				v = objectGen(depth-1).Draw(t, "object")
			case kind == 1:
				v = document.String("s")
			case kind == 2:
				v = document.Null{}
			default:
				v = document.Number("7")
			}
			out = append(out, document.Entry{Key: k, Value: v})
		}
		return out
	})
}

// lookup is m's member key; nil when m has none.
func lookup(m document.Map, key string) document.Value {
	for _, e := range m {
		if e.Key == key {
			return e.Value
		}
	}
	return nil
}

type fataler interface{ Fatalf(string, ...any) }

func mustParse(f fataler, s string) jsonpath.Path {
	p, err := jsonpath.Parse(s, jsonpath.Reference)
	if err != nil {
		f.Fatalf("Parse(%q) = %v", s, err)
	}
	return p
}

func value(f fataler, s string) document.Value {
	d := jsonx.NewDecoder([]byte(s))
	v, err := d.ReadValue()
	if err == nil {
		err = d.End()
	}
	if err != nil {
		f.Fatalf("decode %q: %v", s, err)
	}
	return v
}

func text(v document.Value) string {
	if v == nil {
		return "<nil>"
	}
	e := jsonx.NewEncoder(jsonx.WireOrder)
	e.Value(v)
	b, err := e.Bytes()
	if err != nil {
		return "<unencodable>"
	}
	return string(b)
}
