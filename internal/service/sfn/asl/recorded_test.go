package asl_test

import (
	"fmt"
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

// A ResultPath into a string or an array fails the execution with
// States.ReferencePathConflict and a cause without the 4.6 prefix (ASL-03,
// recorded 2026-11-03; stepfunctions-asl.md 4.4).
func TestASL03ResultPathConflict(t *testing.T) {
	for input, cause := range map[string]string{
		`"foo"`: `Unable to apply ReferencePath $.x to input "foo"`,
		`[1]`:   `Unable to apply step "x" to input [1]`,
	} {
		d := newDriver(t, `{"StartAt":"A","States":{"A":{"Type":"Pass","Result":1,"ResultPath":"$.x","End":true}}}`, rules())
		d.start(input)
		failedWith(t, d.runToEnd(10), "States.ReferencePathConflict", func(c string) bool { return c == cause })
		d.wantShape("ASL-03 "+input, "ExecutionStarted 1<-0", "PassStateEntered 2<-0", "ExecutionFailed 3<-2")
	}
}

// A caught error with no cause gives "Cause": null in the error output,
// Error first (ASL-13 05, recorded 2026-11-03; stepfunctions-asl.md 8.4).
func TestASL13ErrorOutputCauseNull(t *testing.T) {
	d := newDriver(t, `{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[
		{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"NoCause"}}}],"Catch":[{"ErrorEquals":["States.ALL"],"Next":"S"}],"End":true},"S":{"Type":"Succeed"}}}`, rules())
	d.start(`{}`)
	o := d.runToEnd(20)
	if s, ok := o.(asl.Succeeded); !ok || encode(s.Output) != `{"Error":"NoCause","Cause":null}` {
		t.Errorf("outcome = %#v, want the error output {\"Error\":\"NoCause\",\"Cause\":null}", o)
	}
	d.wantShape("ASL-13 05-error-without-cause-history",
		"ExecutionStarted 1<-0", "ParallelStateEntered 2<-0", "ParallelStateStarted 3<-2", "FailStateEntered 4<-3",
		"ParallelStateFailed 5<-4", "ParallelStateExited 6<-5", "SucceedStateEntered 7<-6", "SucceedStateExited 8<-7", "ExecutionSucceeded 9<-8")
}

// A failing branch fails the Parallel with the branch's own error, and a
// sibling branch that has not entered a state yet never does (ASL-13 02,
// recorded 2026-11-03).
func TestASL13ParallelBranchFailure(t *testing.T) {
	d := newDriver(t, `{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[
		{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"BranchErr","Cause":"c"}}},
		{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":30,"End":true}}}],"End":true}}}`, rules())
	d.start(`{}`)
	failedWith(t, d.runToEnd(20), "BranchErr", func(c string) bool { return c == "c" })
	d.wantShape("ASL-13 02-parallel-fail-history",
		"ExecutionStarted 1<-0", "ParallelStateEntered 2<-0", "ParallelStateStarted 3<-2", "FailStateEntered 4<-3",
		"ParallelStateFailed 5<-4", "ExecutionFailed 6<-5")
}

// A failing Map iteration stops its siblings: one waiting in a Wait state
// writes MapIterationAborted and WaitStateAborted, both pointing at the
// failure, before MapIterationFailed; one between states writes nothing
// (ASL-13 04, recorded 2026-11-03).
func TestASL13MapIterationFailureAbortsWaitingSibling(t *testing.T) {
	d := newDriver(t, `{"StartAt":"M","States":{"M":{"Type":"Map","ItemProcessor":{"StartAt":"C","States":{
		"C":{"Type":"Choice","Choices":[{"Variable":"$","NumericEquals":1,"Next":"F"}],"Default":"W"},
		"F":{"Type":"Fail","Error":"ItemErr"},"W":{"Type":"Wait","Seconds":30,"End":true}}},"End":true}}}`, rules())
	d.start(`[0,1,2]`)
	failedWith(t, d.runToEnd(50), "ItemErr", func(c string) bool { return c == "" })
	d.wantShape("ASL-13 04-map-fail-history",
		"ExecutionStarted 1<-0", "MapStateEntered 2<-0", "MapStateStarted 3<-2",
		"MapIterationStarted 4<-3", "MapIterationStarted 5<-3", "MapIterationStarted 6<-3",
		"ChoiceStateEntered 7<-4", "ChoiceStateExited 8<-7", "ChoiceStateEntered 9<-5", "ChoiceStateExited 10<-9",
		"ChoiceStateEntered 11<-6", "ChoiceStateExited 12<-11", "WaitStateEntered 13<-8", "FailStateEntered 14<-10",
		"MapIterationAborted 15<-14", "WaitStateAborted 16<-14", "MapIterationFailed 17<-14", "MapStateFailed 18<-14", "ExecutionFailed 19<-18")
	if got := encode(d.event("MapIterationAborted").Details); got != `{"name":"M","index":0}` {
		t.Errorf("MapIterationAborted details = %s, want the aborted iteration's name and index", got)
	}
	if got := encode(d.event("WaitStateAborted").Details); got != `{}` {
		t.Errorf("WaitStateAborted details = %s, want {}", got)
	}
}

// Concurrent Parallel branches and Map iterations write their events in
// the order AWS recorded for these two runs: ParallelStateSucceeded points
// at the last branch to finish, and every MapIterationStarted precedes the
// iterations' states, which then run in index order (ASL-13 01 and 03,
// recorded 2026-11-03).
func TestASL13ConcurrentOrder(t *testing.T) {
	par := newDriver(t, `{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[
		{"StartAt":"A1","States":{"A1":{"Type":"Pass","Next":"A2"},"A2":{"Type":"Pass","End":true}}},
		{"StartAt":"B1","States":{"B1":{"Type":"Wait","Seconds":2,"End":true}}},
		{"StartAt":"C1","States":{"C1":{"Type":"Pass","End":true}}}],"End":true}}}`, rules())
	par.start(`{}`)
	par.runToEnd(50)
	par.wantShape("ASL-13 01-parallel-ok-history",
		"ExecutionStarted 1<-0", "ParallelStateEntered 2<-0", "ParallelStateStarted 3<-2",
		"PassStateEntered 4<-3", "PassStateExited 5<-4", "WaitStateEntered 6<-3", "PassStateEntered 7<-3", "PassStateExited 8<-7",
		"PassStateEntered 9<-5", "PassStateExited 10<-9", "WaitStateExited 11<-6",
		"ParallelStateSucceeded 12<-11", "ParallelStateExited 13<-11", "ExecutionSucceeded 14<-13")

	m := newDriver(t, `{"StartAt":"M","States":{"M":{"Type":"Map","MaxConcurrency":0,"ItemProcessor":{"StartAt":"S","States":{"S":{"Type":"Pass","End":true}}},"End":true}}}`, rules())
	m.start(`[1,2,3,4,5]`)
	m.runToEnd(80)
	want := make([]string, 0, 26)
	want = append(want, "ExecutionStarted 1<-0", "MapStateEntered 2<-0", "MapStateStarted 3<-2")
	for i := range 5 {
		want = append(want, fmt.Sprintf("MapIterationStarted %d<-3", 4+i))
	}
	for i := range 5 {
		id := 9 + 3*i
		want = append(want, fmt.Sprintf("PassStateEntered %d<-%d", id, 4+i), fmt.Sprintf("PassStateExited %d<-%d", id+1, id),
			fmt.Sprintf("MapIterationSucceeded %d<-%d", id+2, id+1))
	}
	m.wantShape("ASL-13 03-map-unbounded-history", append(want, "MapStateSucceeded 24<-23", "MapStateExited 25<-23", "ExecutionSucceeded 26<-25")...)
}

// A retried Parallel writes a new ParallelStateStarted pointing at the
// failed attempt's last event, no ParallelStateFailed between attempts, and
// after the last attempt ParallelStateFailed and the branch's own error
// (ASL-10 01, recorded 2026-11-03).
func TestASL10ParallelRetryEvents(t *testing.T) {
	d := newDriver(t, `{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"E1","Cause":"c"}}}],
		"Retry":[{"ErrorEquals":["E1"],"IntervalSeconds":1,"BackoffRate":1.5,"MaxAttempts":4}],"End":true}}}`, rules())
	d.start(`{}`)
	failedWith(t, d.runToEnd(50), "E1", func(c string) bool { return c == "c" })
	d.wantShape("ASL-10 01-fractional-history",
		"ExecutionStarted 1<-0", "ParallelStateEntered 2<-0", "ParallelStateStarted 3<-2", "FailStateEntered 4<-3",
		"ParallelStateStarted 5<-4", "FailStateEntered 6<-5", "ParallelStateStarted 7<-6", "FailStateEntered 8<-7",
		"ParallelStateStarted 9<-8", "FailStateEntered 10<-9", "ParallelStateStarted 11<-10", "FailStateEntered 12<-11",
		"ParallelStateFailed 13<-12", "ExecutionFailed 14<-13")
}

// A Map ItemSelector path that selects nothing fails the execution with
// States.Runtime after MapStateStarted, pointing at it, with no
// MapIterationStarted (ASL-04 02, recorded 2026-11-03). AWS quotes the
// Map's input as sent, spaces kept; understudy quotes its compact text, a
// coverage note of StartExecution.
func TestASL04MapItemSelectorMissingPath(t *testing.T) {
	d := newDriver(t, `{"StartAt":"M","States":{"M":{"Type":"Map","ItemsPath":"$.items","ItemSelector":{"v.$":"$.nope"},
		"ItemProcessor":{"StartAt":"S","States":{"S":{"Type":"Succeed"}}},"End":true}}}`, rules())
	d.start(`{"items": [1], "k": "v"}`)
	failedWith(t, d.runToEnd(20), "States.Runtime", func(c string) bool {
		return c == runtimePrefix("M", 2)+`The JSONPath '$.nope' specified for the field 'v.$' could not be found in the input '{"items":[1],"k":"v"}'`
	})
	d.wantShape("ASL-04 02-map-history", "ExecutionStarted 1<-0", "MapStateEntered 2<-0", "MapStateStarted 3<-2", "ExecutionFailed 4<-3")
}

// A TimestampPath with a numeric offset or a lower-case "z" and a
// SecondsPath holding the string "1" are accepted (ASL-09 01-03, recorded
// 2026-11-03; stepfunctions-asl.md 7.1).
func TestASL09WaitPathsAccepted(t *testing.T) {
	for _, tt := range []struct {
		field, input string
		wait         time.Duration
	}{
		{`"TimestampPath":"$.t"`, `{"t":"2016-03-14T01:59:00+01:00"}`, 0},
		{`"TimestampPath":"$.t"`, `{"t":"2016-03-14T01:59:00z"}`, 0},
		{`"SecondsPath":"$.s"`, `{"s":"1"}`, time.Second},
	} {
		d := newDriver(t, `{"StartAt":"W","States":{"W":{"Type":"Wait",`+tt.field+`,"End":true}}}`, rules())
		d.start(tt.input)
		o := d.runToEnd(10)
		if _, ok := o.(asl.Succeeded); !ok {
			t.Fatalf("%s: outcome = %#v, want Succeeded", tt.input, o)
		}
		if got := d.event("WaitStateExited").Timestamp.Sub(d.event("WaitStateEntered").Timestamp); got != tt.wait {
			t.Errorf("%s: waited %v, want %v", tt.input, got, tt.wait)
		}
	}
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
