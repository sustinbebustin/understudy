package asl_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// These tests run the definitions of the 2026-11-03 recording session
// (testdata/parity/sfn/ASL-*) with the service's rules and check the
// outcome and history AWS recorded.

// choiceInParallel is ASL-08's definition: a Choice without Default inside
// a Parallel whose catcher would catch any catchable error.
const choiceInParallel = `{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[{"StartAt":"C","States":{
	"C":{"Type":"Choice","Choices":[{"Variable":"$.x","NumericEquals":1,"Next":"S"}]},"S":{"Type":"Succeed"}}}],
	"Catch":[{"ErrorEquals":["States.ALL"],"Next":"Caught"}],"End":true},"Caught":{"Type":"Succeed"}}}`

// A Choice no rule matches, without Default, fails the execution with an
// uncatchable States.Runtime; a numeric string never matches a numeric rule
// (ASL-08, recorded 2026-11-03; stepfunctions-asl.md 6.4).
func TestASL08ChoiceNoMatchIsRuntime(t *testing.T) {
	for _, input := range []string{`{"x":2}`, `{"x":"1"}`} {
		d := newDriver(t, choiceInParallel, rules())
		d.start(input)
		o := d.runToEnd(20)
		failedWith(t, o, "States.Runtime", func(c string) bool {
			return c == runtimePrefix("C", 4)+"Failed to transition out of the state. The state does not point to a next state."
		})
		d.wantShape("ASL-08 01-no-match-history",
			"ExecutionStarted 1<-0", "ParallelStateEntered 2<-0", "ParallelStateStarted 3<-2", "ChoiceStateEntered 4<-3", "ExecutionFailed 5<-4")
	}
}

// intrinsicInParallel is ASL-05's definition for one expression: a Pass in
// a Parallel whose catcher would catch any catchable error.
func intrinsicInParallel(expr string) string {
	return `{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[{"StartAt":"A","States":{
		"A":{"Type":"Pass","Parameters":{"r.$":"` + expr + `"},"End":true}}}],
		"Catch":[{"ErrorEquals":["States.ALL"],"Next":"C"}],"End":true},"C":{"Type":"Succeed"}}}`
}

// asl05Input is the execution input of every ASL-05 run.
const asl05Input = `{"a":[1],"s":"%%%","n":2147483647,"o":{"k":{"x":1}}}`

// An intrinsic function failing at run time fails the execution with an
// uncatchable States.Runtime whose cause quotes the expression as written
// (ASL-05, recorded 2026-11-03; stepfunctions-asl.md 5.4).
func TestASL05IntrinsicFailuresAreRuntime(t *testing.T) {
	for _, tt := range []struct{ expr, detail string }{
		{"States.ArrayGetItem($.a, -1)", "Invalid arguments in States.ArrayGetItem"},
		{"States.ArrayPartition($.a, 0)", "Invalid arguments in States.ArrayPartition"},
		{"States.ArrayRange(1, 1001, 1)", "Invalid arguments in States.ArrayRange"},
		{"States.ArrayRange(1, 5, 0)", "Invalid arguments in States.ArrayRange"},
		{"States.Hash('x', 'SHA-3')", "Invalid arguments in States.Hash"},
		{"States.JsonMerge($.o, $.o, true)", "Deep merge not supported in States.JsonMerge"},
		{"States.MathAdd($.n, 1)", "Invalid arguments in States.MathAdd"},
		{"States.Format('{} {}', 1)", "Invalid arguments in States.Format: number of arguments do not match the occurrences of {}."},
	} {
		d := newDriver(t, intrinsicInParallel(tt.expr), rules())
		d.start(asl05Input)
		failedWith(t, d.runToEnd(20), "States.Runtime", func(c string) bool {
			return c == runtimePrefix("A", 4)+"There was an error while evaluating the intrinsic function: "+tt.expr+". "+tt.detail
		})
		d.wantShape("ASL-05 "+tt.expr,
			"ExecutionStarted 1<-0", "ParallelStateEntered 2<-0", "ParallelStateStarted 3<-2", "PassStateEntered 4<-3", "ExecutionFailed 5<-4")
	}
	// The Jackson text after "caused by: " is not reproduced; the prefix is.
	d := newDriver(t, intrinsicInParallel("States.StringToJson($.s)"), rules())
	d.start(asl05Input)
	failedWith(t, d.runToEnd(20), "States.Runtime", func(c string) bool {
		return strings.HasPrefix(c, runtimePrefix("A", 4)+"There was an error while evaluating the intrinsic function: States.StringToJson($.s). "+
			"Cannot convert String to Json in States.StringToJson, caused by: ")
	})
}

// An index past the end of the array gives null, and Base64Decode of text
// with no Base64 character gives "" (ASL-05 01 and 06, recorded
// 2026-11-03).
func TestASL05IntrinsicLenientResults(t *testing.T) {
	for expr, want := range map[string]string{
		"States.ArrayGetItem($.a, 5)": `[{"r":null}]`,
		"States.Base64Decode($.s)":    `[{"r":""}]`,
	} {
		d := newDriver(t, intrinsicInParallel(expr), rules())
		d.start(asl05Input)
		o := d.runToEnd(20)
		if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, want)) {
			t.Errorf("%s: outcome = %#v, want Succeeded with %s", expr, o, want)
		}
	}
}

// An execution running when its machine is deleted fails at its next
// transition with States.Runtime, the cause naming the machine;
// ExecutionFailed points at the Wait's entered event and no WaitStateExited
// is written (SFN-06 06 and 07, recorded 2026-11-03; stepfunctions-api.md
// 5.4).
func TestSFN06DeletedMachineFailsRunningExecution(t *testing.T) {
	d := newDriver(t, `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":30,"End":true}}}`, rules())
	d.start(`{}`)
	d.runUntilWaiting()
	d.apply(asl.MachineDeleting{At: t0.Add(time.Second)})
	failedWith(t, d.runToEnd(10), "States.Runtime", func(c string) bool { return c == "State machine sm has been deleted" })
	d.wantShape("SFN-06 07-history", "ExecutionStarted 1<-0", "WaitStateEntered 2<-0", "ExecutionFailed 3<-2")
}

// A Choice Variable that selects nothing fails the execution with an
// uncatchable States.Runtime and the recorded cause (ASL-08, recorded
// 2026-11-03; stepfunctions-asl.md 6.4).
func TestASL08ChoiceMissingVariableIsRuntime(t *testing.T) {
	d := newDriver(t, choiceInParallel, rules())
	d.start(`{}`)
	failedWith(t, d.runToEnd(20), "States.Runtime", func(c string) bool {
		return c == runtimePrefix("C", 4)+"Invalid path '$.x': The choice state's condition path references an invalid value."
	})
	d.wantShape("ASL-08 02-missing-history",
		"ExecutionStarted 1<-0", "ParallelStateEntered 2<-0", "ParallelStateStarted 3<-2", "ChoiceStateEntered 4<-3", "ExecutionFailed 5<-4")
}
