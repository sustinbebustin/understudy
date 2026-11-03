package asl_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// A CausePath that selects nothing is States.Runtime with the recorded
// "could not be found" text, after FailStateEntered (stepfunctions-asl.md
// 4.6; aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L26499).
func TestFailCausePathMissing(t *testing.T) {
	d := newDriver(t, `{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"E","CausePath":"$.CauseX"}}}`, rules())
	d.start(`{"a":1}`)
	o := d.runToEnd(10)
	failedWith(t, o, "States.Runtime", func(c string) bool {
		return c == runtimePrefix("F", 2)+`The JSONPath '$.CauseX' specified for the field 'CausePath' could not be found in the input '{"a":1}'`
	})
	d.wantShape("test_base_scenarios.snapshot.json:L26499", "ExecutionStarted 1<-0", "FailStateEntered 2<-0", "ExecutionFailed 3<-2")
}

// An ErrorPath call that fails at run time fails with switch
// sfn.intrinsic.failure-error's error (stepfunctions-asl.md 5.4); a Fail
// state has no Catch, so the execution ends with it either way.
func TestFailErrorPathIntrinsicFailure(t *testing.T) {
	const def = `{"StartAt":"F","States":{"F":{"Type":"Fail","ErrorPath":"States.ArrayGetItem($.list, -1)"}}}`
	r := rules()
	d := newDriver(t, def, r)
	d.start(`{"list":["a"]}`)
	failedWith(t, d.runToEnd(10), r.IntrinsicFailure.Name, func(string) bool { return true })

	r.IntrinsicFailure = asl.ErrorRule{Name: "Flipped.Error", Terminal: true}
	flipped := newDriver(t, def, r)
	flipped.start(`{"list":["a"]}`)
	failedWith(t, flipped.runToEnd(10), "Flipped.Error", func(string) bool { return true })
}

// A path or call that yields something other than a string fails the
// state with States.Runtime. AWS's answer is unrecorded; this is
// understudy's choice.
func TestFailMessageNotAString(t *testing.T) {
	for _, def := range []string{
		`{"StartAt":"F","States":{"F":{"Type":"Fail","ErrorPath":"$.n"}}}`,
		`{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"E","CausePath":"States.ArrayGetItem($.list, 0)"}}}`,
	} {
		d := newDriver(t, def, rules())
		d.start(`{"n":1,"list":[{"k":1}]}`)
		failedWith(t, d.runToEnd(10), "States.Runtime", func(c string) bool {
			return strings.HasPrefix(c, runtimePrefix("F", 2)) && strings.HasSuffix(c, "which is not a string")
		})
	}
}

// Fail ignores its input otherwise: literal Error and Cause pass through
// unchanged (stepfunctions-asl.md 7.2).
func TestFailLiteralsIgnoreInput(t *testing.T) {
	d := newDriver(t, `{"StartAt":"P","States":{"P":{"Type":"Pass","Next":"F"},"F":{"Type":"Fail","Error":"$.x","Cause":"States.Format('{}', $.x)"}}}`, rules())
	d.start(`{"x":"v"}`)
	o := d.runToEnd(10)
	if f, ok := o.(asl.Failed); !ok || f.Error != "$.x" || f.Cause != "States.Format('{}', $.x)" {
		t.Fatalf("outcome = %#v, want the literals", o)
	}
	d.wantShape("stepfunctions-asl.md 11.2 rule 4, 11.3 Fail row",
		"ExecutionStarted 1<-0", "PassStateEntered 2<-0", "PassStateExited 3<-2", "FailStateEntered 4<-3", "ExecutionFailed 5<-4")
}

// The data limit counts UTF-8 bytes of the output's JSON text: exactly
// 262,144 passes, one byte more fails (stepfunctions-asl.md 8.5, 12).
// A Succeed state's output is limited like a Pass state's, and the error
// is terminal, which no recording shows for a Pass state.
func TestDataLimitBoundaryAndSucceed(t *testing.T) {
	// inferred, ASL-15
	// "é" is two UTF-8 bytes; the quotes add two more.
	fits := strings.Repeat("é", (262_144-2)/2)
	d := newDriver(t, `{"StartAt":"P","States":{"P":{"Type":"Pass","Result":"`+fits+`","End":true}}}`, rules())
	d.start(`{}`)
	if o, ok := d.runToEnd(10).(asl.Succeeded); !ok {
		t.Fatalf("outcome = %#v, want 262,144 bytes to fit", o)
	}

	// 262,143 characters and two quotes: one byte over.
	over := `"` + strings.Repeat("x", 262_143) + `"`
	s := newDriver(t, `{"StartAt":"S","States":{"S":{"Type":"Succeed"}}}`, rules())
	s.start(over)
	failedWith(t, s.runToEnd(10), "States.DataLimitExceeded", func(c string) bool {
		return c == "The state/task 'S' returned a result with a size exceeding the maximum number of bytes service limit."
	})
	s.wantShape("test_states_errors.snapshot.json:L608", "ExecutionStarted 1<-0", "SucceedStateEntered 2<-0", "ExecutionFailed 3<-2")
}
