package asl_test

import (
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// passMachine is one Pass state S whose fields are state.
func passMachine(state string) string {
	return `{"StartAt":"S","States":{"S":{"Type":"Pass",` + state + `"End":true}}}`
}

// TestASL03ResultPathIntoNonObject: AWS's error for a ResultPath applied to
// an input it cannot hold a member is unrecorded (stepfunctions-asl.md 4.4);
// understudy fails the execution with States.Runtime and its own reason,
// after the state's entered event, like the recorded path failures (4.6).
func TestASL03ResultPathIntoNonObject(t *testing.T) {
	// inferred, ASL-03
	for _, tt := range []struct{ input, reason string }{
		{`"foo"`, "$ is a string, not an object"},
		{`[1]`, "$ is an array, not an object"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			d := newDriver(t, passMachine(`"Result":1,"ResultPath":"$.x",`), rules())
			d.start(tt.input)
			o := d.runToEnd(10)
			want := runtimePrefix("S", 2) + "Unable to apply ResultPath '$.x' to the input: " + tt.reason
			failedWith(t, o, "States.Runtime", func(c string) bool { return c == want })
			d.wantShape("stepfunctions-asl.md 4.6", "ExecutionStarted 1<-0", "PassStateEntered 2<-0", "ExecutionFailed 3<-2")
		})
	}
}

// TestASL04MissingPathQuotesCompactInput: the recorded "could not be found
// in the input '...'" causes hide the JSON they quote (stepfunctions-asl.md
// 4.6); understudy quotes the template's input, after InputPath, as compact
// JSON in member order.
func TestASL04MissingPathQuotesCompactInput(t *testing.T) {
	// inferred, ASL-04
	d := newDriver(t, passMachine(`"InputPath":"$.in","Parameters":{"v.$":"$.nope"},`), rules())
	d.start(`{"in": {"k": "v", "n": [1, 2]}, "other": true}`)
	o := d.runToEnd(10)
	want := runtimePrefix("S", 2) + `The JSONPath '$.nope' specified for the field 'v.$' could not be found in the input '{"k":"v","n":[1,2]}'`
	failedWith(t, o, "States.Runtime", func(c string) bool { return c == want })
}

// TestASL02TemplateKeyOrder: the order of a payload template's output keys
// is unrecorded (stepfunctions-asl.md 4.3); understudy keeps template order,
// nested objects included, in the history's JSON text. The template is
// recording case ASL-02's.
func TestASL02TemplateKeyOrder(t *testing.T) {
	// inferred, ASL-02
	d := newDriver(t, passMachine(`"Parameters":{"z.$":"$.a","static1":1,"m.$":"$.a","b":{"y.$":"$.a","x":2},"a.$":"$.a","static0":0},`), rules())
	d.start(`{"a":1}`)
	d.runToEnd(10)
	if got := detailString(d.event("PassStateExited"), "output"); got != `{"z":1,"static1":1,"m":1,"b":{"y":1,"x":2},"a":1,"static0":0}` {
		t.Fatalf("PassStateExited output = %s, want template order", got)
	}
}

// Recorded and documented input and output processing beyond the blind
// cases (stepfunctions-asl.md 4.1-4.7).
func TestInputOutputProcessingMore(t *testing.T) {
	tests := []struct {
		name, def, input, want, source string
	}{
		{
			"Succeed filters by InputPath then OutputPath",
			`{"StartAt":"S","States":{"S":{"Type":"Succeed","InputPath":"$.a","OutputPath":"$.b"}}}`,
			`{"a":{"b":{"c":1}},"x":0}`, `{"c":1}`, "spec#succeed-state",
		},
		{
			"Succeed OutputPath null",
			`{"StartAt":"S","States":{"S":{"Type":"Succeed","OutputPath":null}}}`,
			`{"a":1}`, `{}`, "stepfunctions-asl.md 4.2",
		},
		{
			"nested template objects are evaluated",
			passMachine(`"Parameters":{"o":{"x.$":"$.a","lit":"$.a"}},`),
			`{"a":1}`, `{"o":{"x":1,"lit":"$.a"}}`, "spec#payload-template",
		},
		{
			"missing wildcard in a template gives []",
			passMachine(`"Parameters":{"v.$":"$.items[*]"},`),
			`{}`, `{"v":[]}`, "localstack@8b9a79f v2/base/test_base.snapshot.json:L1432",
		},
		{
			"Parameters is the result when there is no Result",
			passMachine(`"Parameters":{"v.$":"$.a"},"ResultPath":"$.r",`),
			`{"a":1}`, `{"a":1,"r":{"v":1}}`, "stepfunctions-asl.md 4.1",
		},
		{
			"ResultPath into an array element",
			passMachine(`"Result":9,"ResultPath":"$.a[1]",`),
			`{"a":[1,2]}`, `{"a":[1,9]}`, "stepfunctions-asl.md 4.4",
		},
		{
			"$$ in InputPath",
			passMachine(`"InputPath":"$$.State.Name",`),
			`{}`, `"S"`, "aws-doc dg/input-output-contextobject",
		},
		{
			"$$ in Parameters reads the execution input",
			passMachine(`"Parameters":{"in.$":"$$.Execution.Input"},`),
			`{"a":1}`, `{"in":{"a":1}}`, "stepfunctions-asl.md 4.7",
		},
		{
			"OutputPath reads the placed result",
			passMachine(`"Result":{"k":1},"ResultPath":"$.r","OutputPath":"$.r.k",`),
			`{}`, `1`, "stepfunctions-asl.md 4.1 step 4",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDriver(t, tt.def, rules())
			d.start(tt.input)
			o := d.runToEnd(10)
			if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, tt.want)) {
				t.Fatalf("outcome = %#v, want output %s (%s)", o, tt.want, tt.source)
			}
		})
	}
}

// An OutputPath through a missing member fails with the recorded "Missing
// property" text (stepfunctions-asl.md 4.6; aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L11164).
func TestOutputPathThroughMissingParent(t *testing.T) {
	d := newDriver(t, passMachine(`"OutputPath":"$.no.such.result.path",`), rules())
	d.start(`{"a":1}`)
	o := d.runToEnd(10)
	want := runtimePrefix("S", 2) + "Invalid path '$.no.such.result.path' : Missing property in path $['no']"
	failedWith(t, o, "States.Runtime", func(c string) bool { return c == want })
	d.wantShape("test_base_scenarios.snapshot.json:L11164", "ExecutionStarted 1<-0", "PassStateEntered 2<-0", "ExecutionFailed 3<-2")
}

// A Succeed state's InputPath failure is the recorded States.Runtime
// (stepfunctions-asl.md 4.6) after SucceedStateEntered.
func TestSucceedInputPathMissing(t *testing.T) {
	d := newDriver(t, `{"StartAt":"S","States":{"S":{"Type":"Succeed","InputPath":"$.x"}}}`, rules())
	d.start(`{}`)
	o := d.runToEnd(10)
	want := runtimePrefix("S", 2) + "Invalid path '$.x' : No results for path: $['x']"
	failedWith(t, o, "States.Runtime", func(c string) bool { return c == want })
	d.wantShape("stepfunctions-asl.md 4.6", "ExecutionStarted 1<-0", "SucceedStateEntered 2<-0", "ExecutionFailed 3<-2")
}

// Over generated Parameters templates, Parse refuses a template exactly
// when two keys are equal after their ".$" suffixes are removed, and
// otherwise the output has the renamed keys, a ".$" member holding the
// value its path selects and a plain member its literal
// (stepfunctions-asl.md 4.3).
func TestTemplateRenamingAndDuplicates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(t, "members")
		members := make([]string, 0, n)
		renamed := make([]string, 0, n)
		for range n {
			// Keys are distinct as written, so only renaming can collide.
			key := rapid.SampledFrom([]string{"a", "b", "c", "d"}).Draw(t, "key")
			member := `"` + key + `":"lit"`
			if rapid.Bool().Draw(t, "path") {
				member = `"` + key + `.$":"$.v"`
			}
			if slices.Contains(members, member) {
				continue
			}
			members = append(members, member)
			renamed = append(renamed, key)
		}
		def := passMachine(`"Parameters":{` + strings.Join(members, ",") + `},`)
		duplicate := len(slices.Compact(slices.Sorted(slices.Values(renamed)))) != len(renamed)

		parsed, diags := asl.Parse(def, allFeatures)
		if duplicate != (parsed == nil) {
			t.Fatalf("Parse(%s) = %+v, want refused %v", def, diags, duplicate)
		}
		if duplicate {
			if !slices.ContainsFunc(diags, func(d asl.Diagnostic) bool {
				return d.Code == "SCHEMA_VALIDATION_FAILED" && d.Location == "/States/S/Parameters" && strings.Contains(d.Message, "appears more than once")
			}) {
				t.Fatalf("Parse(%s) = %+v, want a duplicate-field diagnostic at /States/S/Parameters", def, diags)
			}
			return
		}
		d := newDriver(t, def, rules())
		d.start(`{"v":42}`)
		s, ok := d.runToEnd(10).(asl.Succeeded)
		if !ok {
			t.Fatalf("execution of %s did not succeed", def)
		}
		if got := keys(s.Output); !slices.Equal(got, slices.Sorted(slices.Values(renamed))) {
			t.Fatalf("output keys = %v, want %v", got, renamed)
		}
		for _, m := range members {
			key := m[1:2]
			want := document.Value(document.String("lit"))
			if strings.Contains(m, ".$") {
				want = document.Number("42")
			}
			if !equal(at(s.Output, key), want) {
				t.Fatalf("output %s = %s, want %s for member %s", key, encode(at(s.Output, key)), encode(want), m)
			}
		}
	})
}
