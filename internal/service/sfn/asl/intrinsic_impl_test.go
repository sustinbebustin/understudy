package asl_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// A function failing in a top-level Pass ends the execution with switch
// sfn.intrinsic.failure-error's error, after the state's entered event; the
// cause quotes the call and names the function (stepfunctions-asl.md 5.4;
// ASL-05, recorded 2026-11-03).
func TestIntrinsicFailureEndsTopLevelExecution(t *testing.T) {
	d := newDriver(t, passMachine(`"Parameters":{"x.$":"States.ArrayGetItem($.a, -1)"},`), rules())
	d.start(`{"a":[1]}`)
	o := d.runToEnd(10)
	want := runtimePrefix("S", 2) + "There was an error while evaluating the intrinsic function: States.ArrayGetItem($.a, -1). Invalid arguments in States.ArrayGetItem"
	failedWith(t, o, sfn.IntrinsicFailure().Value.Name, func(c string) bool { return c == want })
	d.wantShape("stepfunctions-asl.md 4.6", "ExecutionStarted 1<-0", "PassStateEntered 2<-0", "ExecutionFailed 3<-2")
}

// A missing path argument is the missing ".$" path of the template, with
// the recorded States.Runtime text (stepfunctions-asl.md 4.6).
func TestIntrinsicMissingPathArgumentIsRuntime(t *testing.T) {
	d := newDriver(t, passMachine(`"Parameters":{"x.$":"States.Format('{}', $.nope)"},`), rules())
	d.start(`{"a":1}`)
	o := d.runToEnd(10)
	want := runtimePrefix("S", 2) + `The JSONPath '$.nope' specified for the field 'x.$' could not be found in the input '{"a":1}'`
	failedWith(t, o, "States.Runtime", func(c string) bool { return c == want })
}

// Intrinsics in a Pass state's Parameters read "$" and "$$" and draw
// States.UUID and unseeded States.MathRandom from the engine's source
// (stepfunctions-asl.md 5.1, 5.2).
func TestIntrinsicsInTemplate(t *testing.T) {
	d := newDriver(t, passMachine(`"Parameters":{"id.$":"States.UUID()","n.$":"States.MathRandom(3, 9)","s.$":"States.Format('{}-{}', $$.State.Name, $.v)"},`), rules())
	d.start(`{"v":"x"}`)
	o := d.runToEnd(10)
	want := document.Map{
		{Key: "id", Value: document.String(fixedSource{}.UUID())},
		{Key: "n", Value: document.Number("3")},
		{Key: "s", Value: document.String("S-x")},
	}
	s, ok := o.(asl.Succeeded)
	if !ok || !equal(s.Output, want) {
		t.Fatalf("outcome = %#v, want Succeeded with %s", o, encode(want))
	}
}
