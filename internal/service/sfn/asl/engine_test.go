package asl_test

import (
	"bytes"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// One Pass state writes ExecutionStarted 1<-0, PassStateEntered 2<-0,
// PassStateExited 3<-2 and ExecutionSucceeded 4<-3 (stepfunctions-asl.md
// 11.2 rules 1, 2 and 4; 11.3 Pass row, aws-recorded localstack@8b9a79f
// v2/base/test_base.snapshot.json:L264-L277).
func TestPassHistory(t *testing.T) {
	d := newDriver(t, `{"StartAt":"P","States":{"P":{"Type":"Pass","Result":{"x":1},"End":true}}}`, rules())
	effs := d.start(`{"a":1}`)
	armedNow := false
	for _, e := range effs {
		if a, ok := e.(asl.ArmTimer); ok && a.At.Equal(t0) {
			armedNow = true
		}
	}
	if !armedNow {
		t.Fatalf("Start effects = %#v, want an ArmTimer at the start instant for the first step (contract 5.5)", effs)
	}
	o := d.runToEnd(10)
	d.wantShape("test_base.snapshot.json:L264-L277",
		"ExecutionStarted 1<-0", "PassStateEntered 2<-0", "PassStateExited 3<-2", "ExecutionSucceeded 4<-3")

	s, ok := o.(asl.Succeeded)
	if !ok || !equal(s.Output, decode(t, `{"x":1}`)) {
		t.Fatalf("outcome = %#v, want Succeeded with the Result {\"x\":1} (spec#jsonpath-pass-state)", o)
	}
	started := d.event("ExecutionStarted")
	if !equal(detailJSON(t, started, "input"), decode(t, `{"a":1}`)) || detailString(started, "roleArn") != roleArn {
		t.Errorf("ExecutionStarted details = %s, want input and roleArn (stepfunctions-asl.md 11.3)", encode(started.Details))
	}
	entered := d.event("PassStateEntered")
	if detailString(entered, "name") != "P" || !equal(detailJSON(t, entered, "input"), decode(t, `{"a":1}`)) {
		t.Errorf("PassStateEntered details = %s, want name P and the input", encode(entered.Details))
	}
	exited := d.event("PassStateExited")
	if detailString(exited, "name") != "P" || !equal(detailJSON(t, exited, "output"), decode(t, `{"x":1}`)) {
		t.Errorf("PassStateExited details = %s, want name P and the output", encode(exited.Details))
	}
	if !equal(detailJSON(t, d.event("ExecutionSucceeded"), "output"), decode(t, `{"x":1}`)) {
		t.Errorf("ExecutionSucceeded details = %s, want the output", encode(d.event("ExecutionSucceeded").Details))
	}
	for _, e := range d.history {
		if !e.Timestamp.Equal(t0) {
			t.Errorf("%s timestamp = %v, want %v: every step ran at the start instant (contract 5.4)", e.Type, e.Timestamp, t0)
		}
	}
	last := d.effects[len(d.effects)-1]
	f, ok := last[len(last)-1].(asl.Finish)
	if fs, isSucceeded := f.Outcome.(asl.Succeeded); !ok || !isSucceeded || !equal(fs.Output, s.Output) {
		t.Errorf("last effects = %#v, want Finish last (contract 5.4)", last)
	}
}

// Pass copies its input when it has no Result, and Result is a literal
// whose ".$" keys are not evaluated (spec#jsonpath-pass-state;
// aws-recorded localstack@8b9a79f v2/base/test_base.snapshot.json:L345).
func TestPassResultIsLiteral(t *testing.T) {
	d := newDriver(t, `{"StartAt":"P","States":{"P":{"Type":"Pass","Next":"Q"},"Q":{"Type":"Pass","Result":{"unsupported1.$":"$.jsonpath"},"ResultPath":"$.r","End":true}}}`, rules())
	d.start(`{"jsonpath":"v"}`)
	o := d.runToEnd(10)
	want := decode(t, `{"jsonpath":"v","r":{"unsupported1.$":"$.jsonpath"}}`)
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, want) {
		t.Fatalf("outcome = %#v, want Succeeded %s", o, encode(want))
	}
}

// Succeed writes SucceedStateEntered and SucceedStateExited, each after
// the one before (stepfunctions-asl.md 11.3 Succeed row, aws-recorded
// localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L13613-L13626).
func TestSucceedHistory(t *testing.T) {
	d := newDriver(t, `{"StartAt":"P","States":{"P":{"Type":"Pass","Next":"S"},"S":{"Type":"Succeed"}}}`, rules())
	d.start(`{"k":"v"}`)
	o := d.runToEnd(10)
	d.wantShape("test_base_scenarios.snapshot.json:L13613-L13626",
		"ExecutionStarted 1<-0", "PassStateEntered 2<-0", "PassStateExited 3<-2",
		"SucceedStateEntered 4<-3", "SucceedStateExited 5<-4", "ExecutionSucceeded 6<-5")
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `{"k":"v"}`)) {
		t.Fatalf("outcome = %#v, want the input passed through (spec#succeed-state)", o)
	}
}

// Fail writes FailStateEntered 2<-0 and ExecutionFailed 3<-2 with the
// error and cause (aws-recorded localstack@8b9a79f
// v2/base/test_base.snapshot.json:L41-L58); with neither, the details are
// {} and the outcome carries neither (same file L181-L232).
func TestFailHistory(t *testing.T) {
	d := newDriver(t, `{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"SomeFailure","Cause":"This state machines raises a 'SomeFailure' failure."}}}`, rules())
	d.start(`{}`)
	o := d.runToEnd(10)
	d.wantShape("test_base.snapshot.json:L41-L58", "ExecutionStarted 1<-0", "FailStateEntered 2<-0", "ExecutionFailed 3<-2")
	f, ok := o.(asl.Failed)
	if !ok || f.Error != "SomeFailure" || f.Cause != "This state machines raises a 'SomeFailure' failure." {
		t.Fatalf("outcome = %#v, want Failed with the state's error and cause", o)
	}
	ev := d.event("ExecutionFailed")
	if detailString(ev, "error") != f.Error || detailString(ev, "cause") != f.Cause {
		t.Errorf("ExecutionFailed details = %s", encode(ev.Details))
	}

	empty := newDriver(t, `{"StartAt":"F","States":{"F":{"Type":"Fail"}}}`, rules())
	empty.start(`{}`)
	o = empty.runToEnd(10)
	if f, ok := o.(asl.Failed); !ok || f.Error != "" || f.Cause != "" {
		t.Fatalf("outcome = %#v, want Failed with no error or cause (test_base.snapshot.json:L181-L232)", o)
	}
	if got := empty.event("ExecutionFailed").Details; !equal(got, document.Map{}) {
		t.Errorf("ExecutionFailed details = %s, want {} (test_base.snapshot.json:L181-L232)", encode(got))
	}
}

// Fail ErrorPath and CausePath take a path or a string intrinsic
// (aws-recorded localstack@8b9a79f v2/base/test_base.snapshot.json:L168).
func TestFailPaths(t *testing.T) {
	d := newDriver(t, `{"StartAt":"F","States":{"F":{"Type":"Fail","ErrorPath":"$.Error","CausePath":"States.Format('This is a custom error message for {}, caused by {}.', $.Error, $.Cause)"}}}`, rules())
	d.start(`{"Error":"MyError","Cause":"MyCause"}`)
	o := d.runToEnd(10)
	f, ok := o.(asl.Failed)
	if !ok || f.Error != "MyError" || f.Cause != "This is a custom error message for MyError, caused by MyCause." {
		t.Fatalf("outcome = %#v, want the path error and formatted cause", o)
	}
}

const choiceMachine = `{"StartAt":"C","States":{
	"C":{"Type":"Choice","Choices":[%s],"Default":"No"},
	"Yes":{"Type":"Pass","Result":"yes","End":true},
	"No":{"Type":"Pass","Result":"no","End":true}}}`

// choose runs a Choice with one rule (or several, comma-joined JSON) on
// input and reports which branch it took.
func choose(t *testing.T, rule, input string) string {
	t.Helper()
	d := newDriver(t, fmt.Sprintf(choiceMachine, rule), rules())
	d.start(input)
	o := d.runToEnd(20)
	s, ok := o.(asl.Succeeded)
	if !ok {
		t.Fatalf("rule %s on %s: outcome %#v, want a branch taken", rule, input, o)
	}
	str, ok := s.Output.(document.String)
	if !ok {
		t.Fatalf("rule %s on %s: output %s", rule, input, encode(s.Output))
	}
	return string(str)
}

// Choice writes ChoiceStateEntered 2<-0 and ChoiceStateExited 3<-2 with
// its input as output; the next state enters 4<-3 (aws-recorded
// localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L20657-L20689,
// L20673).
func TestChoiceHistory(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(choiceMachine, `{"Variable":"$.v","NumericEquals":1,"Next":"Yes"}`), rules())
	d.start(`{"v":1}`)
	d.runToEnd(10)
	d.wantShape("test_base_scenarios.snapshot.json:L20657-L20689",
		"ExecutionStarted 1<-0", "ChoiceStateEntered 2<-0", "ChoiceStateExited 3<-2",
		"PassStateEntered 4<-3", "PassStateExited 5<-4", "ExecutionSucceeded 6<-5")
	if got := detailJSON(t, d.event("ChoiceStateExited"), "output"); !equal(got, decode(t, `{"v":1}`)) {
		t.Errorf("ChoiceStateExited output = %s, want the input", encode(got))
	}
}

// The recorded operator results of stepfunctions-asl.md 6.2 (aws-recorded,
// localstack@8b9a79f v2/choice_operators/*.snapshot.json, 2023-03-01 to
// 2023-06-22), plus documented semantics.
func TestChoiceOperatorsRecorded(t *testing.T) {
	tests := []struct {
		name, rule, input string
		match             bool
		source            string
	}{
		{"NumericEquals 0 and 0.0", `"NumericEquals":0.0`, `{"v":0}`, true, "test_numeric.snapshot.json:L2"},
		{"NumericEquals 2.22", `"NumericEquals":2.22`, `{"v":2.22}`, true, "test_numeric.snapshot.json:L2"},
		{"NumericEquals null", `"NumericEquals":0`, `{"v":null}`, false, "test_numeric.snapshot.json:L2"},
		{"NumericEquals string", `"NumericEquals":0`, `{"v":"0"}`, false, "test_numeric.snapshot.json:L2"},
		{"NumericEquals array", `"NumericEquals":0`, `{"v":[0]}`, false, "test_numeric.snapshot.json:L2"},
		{"NumericEquals object", `"NumericEquals":0`, `{"v":{"a":0}}`, false, "test_numeric.snapshot.json:L2"},
		{"NumericEquals boolean", `"NumericEquals":0`, `{"v":false}`, false, "test_numeric.snapshot.json:L2"},
		{"BooleanEquals true", `"BooleanEquals":true`, `{"v":true}`, true, "test_boolean_equals.snapshot.json:L2"},
		{"BooleanEquals 1", `"BooleanEquals":true`, `{"v":1}`, false, "test_boolean_equals.snapshot.json:L2"},
		{"BooleanEquals 0", `"BooleanEquals":false`, `{"v":0}`, false, "test_boolean_equals.snapshot.json:L2"},
		{"BooleanEquals string", `"BooleanEquals":true`, `{"v":"true"}`, false, "test_boolean_equals.snapshot.json:L2"},
		{"IsPresent null", `"IsPresent":true`, `{"v":null}`, true, "test_is_operators.snapshot.json:L1019"},
		{"IsPresent absent", `"IsPresent":true`, `{}`, false, "spec#data-test-expression-for-jsonpath-choice-rules"},
		{"IsNull null", `"IsNull":true`, `{"v":null}`, true, "test_is_operators.snapshot.json:L341"},
		{"IsNull string", `"IsNull":true`, `{"v":"null"}`, false, "test_is_operators.snapshot.json:L341"},
		{"IsNull zero", `"IsNull":true`, `{"v":0}`, false, "test_is_operators.snapshot.json:L341"},
		{"IsNumeric number", `"IsNumeric":true`, `{"v":1.5}`, true, "test_is_operators.snapshot.json:L680"},
		{"IsNumeric numeric string", `"IsNumeric":true`, `{"v":"1"}`, false, "test_is_operators.snapshot.json:L680"},
		{"IsString empty", `"IsString":true`, `{"v":""}`, true, "test_is_operators.snapshot.json:L1358"},
		{"IsString timestamp", `"IsString":true`, `{"v":"2023-02-24T12:15:56Z"}`, true, "test_is_operators.snapshot.json:L1358"},
		{"IsString number", `"IsString":true`, `{"v":1}`, false, "test_is_operators.snapshot.json:L1358"},
		{"IsBoolean true", `"IsBoolean":true`, `{"v":false}`, true, "test_is_operators.snapshot.json:L2"},
		{"IsBoolean string", `"IsBoolean":true`, `{"v":"true"}`, false, "test_is_operators.snapshot.json:L2"},
		{"IsBoolean false matches IsBoolean false", `"IsBoolean":false`, `{"v":1}`, true, "spec#data-test-expression-for-jsonpath-choice-rules"},
		{"StringEquals no trim spaces", `"StringEquals":"     "`, `{"v":" "}`, false, "test_string_operators.snapshot.json:L45"},
		{"StringEquals no newline folding", `"StringEquals":"\t\r\n"`, `{"v":"\t\n"}`, false, "test_string_operators.snapshot.json:L45"},
		{"StringEquals equal", `"StringEquals":"abc"`, `{"v":"abc"}`, true, "spec#data-test-expression-for-jsonpath-choice-rules"},
		{"StringGreaterThan prefix", `"StringGreaterThan":"ABC"`, `{"v":"AB"}`, false, "test_string_operators.snapshot.json:L2"},
		{"StringGreaterThanEquals empty", `"StringGreaterThanEquals":""`, `{"v":""}`, true, "test_string_operators.snapshot.json:L277"},
		{"StringEquals on a number", `"StringEquals":"1"`, `{"v":1}`, false, "spec: operands of another type are false"},
		{"TimestampEquals space separator", `"TimestampEquals":"2023-02-24 12:15:56.832957"`, `{"v":"2023-02-24 12:15:56.832957"}`, false, "test_timestamp_operators.snapshot.json:L36"},
		{"TimestampEquals same instant", `"TimestampEquals":"2016-03-14T01:59:00Z"`, `{"v":"2016-03-14T01:59:00Z"}`, true, "spec#data-test-expression-for-jsonpath-choice-rules"},
		{"TimestampLessThan", `"TimestampLessThan":"2016-03-14T02:00:00Z"`, `{"v":"2016-03-14T01:59:00Z"}`, true, "spec"},
		{"StringMatches star", `"StringMatches":"log-*.txt"`, `{"v":"log-2024-01.txt"}`, true, "aws-doc dg/state-choice"},
		{"StringMatches no match", `"StringMatches":"log-*.txt"`, `{"v":"log.txt"}`, false, "aws-doc dg/state-choice"},
		{"NumericLessThanPath", `"NumericLessThanPath":"$.w"`, `{"v":1,"w":2}`, true, "spec: ...Path compares with the value at the path"},
		{"StringEqualsPath", `"StringEqualsPath":"$.w"`, `{"v":"a","w":"b"}`, false, "spec"},
		{"NumericGreaterThanEquals", `"NumericGreaterThanEquals":3`, `{"v":3}`, true, "spec"},
		{"NumericLessThanEquals", `"NumericLessThanEquals":2.5`, `{"v":3}`, false, "spec"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := `{"Variable":"$.v",` + tt.rule + `,"Next":"Yes"}`
			want := map[bool]string{true: "yes", false: "no"}[tt.match]
			if got := choose(t, rule, tt.input); got != want {
				t.Fatalf("%s on %s took %q, want %q (%s)", tt.rule, tt.input, got, want, tt.source)
			}
		})
	}
}

// Rules are evaluated in order and And, Or and Not combine nested rules
// (spec#boolean-expression-for-jsonpath-choice-rules).
func TestChoiceBooleanRules(t *testing.T) {
	tests := []struct {
		rule, input, want string
	}{
		{`{"And":[{"Variable":"$.a","NumericEquals":1},{"Variable":"$.b","NumericEquals":2}],"Next":"Yes"}`, `{"a":1,"b":2}`, "yes"},
		{`{"And":[{"Variable":"$.a","NumericEquals":1},{"Variable":"$.b","NumericEquals":2}],"Next":"Yes"}`, `{"a":1,"b":3}`, "no"},
		{`{"Or":[{"Variable":"$.a","NumericEquals":9},{"Variable":"$.b","NumericEquals":2}],"Next":"Yes"}`, `{"a":1,"b":2}`, "yes"},
		{`{"Not":{"Variable":"$.a","StringEquals":"x"},"Next":"Yes"}`, `{"a":"y"}`, "yes"},
		{`{"Variable":"$.a","NumericEquals":1,"Next":"No"},{"Variable":"$.a","NumericEquals":1,"Next":"Yes"}`, `{"a":1}`, "no"},
	}
	for _, tt := range tests {
		if got := choose(t, tt.rule, tt.input); got != tt.want {
			t.Errorf("rules %s on %s took %q, want %q", tt.rule, tt.input, got, tt.want)
		}
	}
}

// A Wait with Seconds arms its step that many seconds later and writes
// WaitStateEntered 2<-0, WaitStateExited 3<-2 (aws-doc dg/state-wait;
// aws-recorded localstack@8b9a79f v2/base/test_wait.snapshot.json:L443-L460).
func TestWaitSeconds(t *testing.T) {
	d := newDriver(t, `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":30,"End":true}}}`, rules())
	d.start(`{}`)
	d.runUntilWaiting()
	_, at, ok := d.nextTimer()
	if !ok || !at.Equal(t0.Add(30*time.Second)) {
		t.Fatalf("after entering the Wait the next timer is at %v (%v), want %v", at, ok, t0.Add(30*time.Second))
	}
	d.runToEnd(10)
	d.wantShape("test_wait.snapshot.json:L443-L460", "ExecutionStarted 1<-0", "WaitStateEntered 2<-0", "WaitStateExited 3<-2", "ExecutionSucceeded 4<-3")
	if ts := d.event("WaitStateExited").Timestamp; !ts.Equal(t0.Add(30 * time.Second)) {
		t.Errorf("WaitStateExited at %v, want %v", ts, t0.Add(30*time.Second))
	}
}

// Wait with a Timestamp in the past ends at once (aws-recorded
// localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L25652-L26051).
func TestWaitTimestampPathInPast(t *testing.T) {
	for _, ts := range []string{"2016-03-14T01:59:00Z", "2016-03-05T21:29:29.243167252Z"} {
		d := newDriver(t, `{"StartAt":"W","States":{"W":{"Type":"Wait","TimestampPath":"$.ts","End":true}}}`, rules())
		d.start(`{"ts":"` + ts + `"}`)
		if _, ok := d.runToEnd(10).(asl.Succeeded); !ok {
			t.Errorf("TimestampPath %s: execution did not succeed:\n%s", ts, d.shape())
		}
	}
}

// The recorded runtime causes of path-valued Wait fields: States.Runtime,
// cause after the 4.6 prefix, ExecutionFailed pointing at WaitStateEntered
// (stepfunctions-asl.md 4.6, 11.2 rule 8).
func TestWaitPathFailures(t *testing.T) {
	tests := []struct {
		name, field, input, rest, source string
	}{
		{"SecondsPath negative", `"SecondsPath":"$.input.waitSeconds"`, `{"input":{"waitSeconds":-1}}`, "The SecondsPath parameter references a negative value: $.input.waitSeconds == -1", "localstack@8b9a79f v2/base/test_wait.snapshot.json:L544"},
		{"SecondsPath fraction", `"SecondsPath":"$.input.waitSeconds"`, `{"input":{"waitSeconds":1.5}}`, "The SecondsPath parameter cannot be parsed as a long value: $.input.waitSeconds == 1.5", "same file L761"},
		{"SecondsPath missing", `"SecondsPath":"$.input.waitSeconds"`, `{"input":{}}`, "The SecondsPath parameter does not reference an input value: $.input.waitSeconds", "localstack@8b9a79f v2/logs/test_logs.snapshot.json:L458"},
		{"TimestampPath without zone", `"TimestampPath":"$.TimestampValue"`, `{"TimestampValue":"2016-12-05T21:29:29"}`, "The TimestampPath parameter does not reference a valid ISO-8601 extended offset date-time format string: $.TimestampValue == 2016-12-05T21:29:29", "localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L25833-L25992"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDriver(t, `{"StartAt":"W","States":{"W":{"Type":"Wait",`+tt.field+`,"End":true}}}`, rules())
			d.start(tt.input)
			o := d.runToEnd(10)
			failedWith(t, o, "States.Runtime", func(c string) bool { return c == runtimePrefix("W", 2)+tt.rest })
			d.wantShape(tt.source, "ExecutionStarted 1<-0", "WaitStateEntered 2<-0", "ExecutionFailed 3<-2")
		})
	}
}

// StopExecution during a Wait ends the execution ABORTED with
// ExecutionAborted 3<-0 and details {} (aws-recorded localstack@8b9a79f
// v2/test_sfn_api.snapshot.json:L527-L531; stepfunctions-asl.md 11.2
// rule 10). The Wait's timer is withdrawn and Finish comes last.
func TestStopDuringWait(t *testing.T) {
	d := newDriver(t, `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":3600,"End":true}}}`, rules())
	d.start(`{}`)
	d.runUntilWaiting()
	effs := d.apply(asl.Stop{At: t0.Add(time.Minute)})
	d.wantShape("test_sfn_api.snapshot.json:L527-L531", "ExecutionStarted 1<-0", "WaitStateEntered 2<-0", "ExecutionAborted 3<-0")
	if got := d.event("ExecutionAborted").Details; !equal(got, document.Map{}) {
		t.Errorf("ExecutionAborted details = %s, want {}", encode(got))
	}
	o, done := d.x.Outcome()
	if a, ok := o.(asl.Aborted); !done || !ok || a.Error != "" || a.Cause != "" {
		t.Fatalf("outcome = %#v, %v; want Aborted with no error or cause", o, done)
	}
	if len(d.timers) != 0 {
		t.Errorf("timers still armed after the stop: %v (contract 5.4: ending cancels every armed timer)", d.timers)
	}
	if _, ok := effs[len(effs)-1].(asl.Finish); !ok {
		t.Errorf("Stop effects = %#v, want Finish last", effs)
	}
}

// A stop with an error and a cause keeps both (stepfunctions-asl.md 11.3:
// ExecutionAborted {error?, cause?}).
func TestStopWithErrorAndCause(t *testing.T) {
	d := newDriver(t, `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":3600,"End":true}}}`, rules())
	d.start(`{}`)
	d.runUntilWaiting()
	d.apply(asl.Stop{At: t0, Error: "Halt", Cause: "operator"})
	ev := d.event("ExecutionAborted")
	if detailString(ev, "error") != "Halt" || detailString(ev, "cause") != "operator" {
		t.Errorf("ExecutionAborted details = %s, want error and cause", encode(ev.Details))
	}
	if o, _ := d.x.Outcome(); o != (asl.Aborted{Error: "Halt", Cause: "operator"}) {
		t.Errorf("outcome = %#v", o)
	}
}

// An event that does not apply changes nothing (contract 5.4): a second
// Start, an unknown timer, anything after the outcome.
func TestEventsThatDoNotApply(t *testing.T) {
	d := newDriver(t, `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":10,"End":true}}}`, rules())
	d.start(`{}`)
	d.runUntilWaiting()
	n := len(d.history)
	for _, ev := range []asl.Event{
		asl.Start{At: t0, Input: document.Map{}, Context: context(document.Map{})},
		asl.TimerFired{At: t0, Timer: 9999},
		asl.LambdaSucceeded{At: t0, Task: 9999, Output: document.Map{}},
	} {
		before := d.x
		x, hs, effs := asl.Step(d.def, d.rules, fixedSource{}, d.x, ev)
		if len(hs) != 0 || len(effs) != 0 || !sameExecution(t, x, before) {
			t.Errorf("Step(%T) on a running execution wrote %d events and %d effects, want none", ev, len(hs), len(effs))
		}
	}
	d.runToEnd(10)
	if len(d.history) != n+2 {
		t.Fatalf("history:\n%s", d.shape())
	}
	x, hs, effs := asl.Step(d.def, d.rules, fixedSource{}, d.x, asl.Stop{At: t0.Add(time.Hour)})
	if len(hs) != 0 || len(effs) != 0 || !sameExecution(t, x, d.x) {
		t.Errorf("Stop after the outcome wrote %d events and %d effects, want none", len(hs), len(effs))
	}
}

// sameExecution compares two executions through their persisted form.
func sameExecution(t *testing.T, a, b asl.Execution) bool {
	t.Helper()
	ja, err := a.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	jb, err := b.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	return bytes.Equal(ja, jb)
}

// The recorded States.Runtime path failures of stepfunctions-asl.md 4.6:
// ExecutionFailed points at the state's *Entered event, with no *Exited.
func TestPathFailuresAreRuntime(t *testing.T) {
	tests := []struct {
		name, state, input string
		check              func(cause string) bool
		source             string
	}{
		{
			"InputPath missing", `{"Type":"Pass","InputPath":"$.NoSuchInputPath","End":true}`, `{"a":1}`,
			func(c string) bool {
				return c == runtimePrefix("S", 2)+"Invalid path '$.NoSuchInputPath' : No results for path: $['NoSuchInputPath']"
			},
			"localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L26552",
		},
		{
			"OutputPath missing", `{"Type":"Pass","OutputPath":"$.NoSuchOutputPath","End":true}`, `{"a":1}`,
			func(c string) bool {
				return c == runtimePrefix("S", 2)+"Invalid path '$.NoSuchOutputPath' : No results for path: $['NoSuchOutputPath']"
			},
			"same file L26605",
		},
		{
			// The input's serialisation inside the quotes is UNVERIFIED (ASL-04).
			"Parameters path missing", `{"Type":"Pass","Parameters":{"value.$":"$.no_such_jsonpath"},"End":true}`, `{"a":1}`,
			func(c string) bool {
				return strings.HasPrefix(c, runtimePrefix("S", 2)+"The JSONPath '$.no_such_jsonpath' specified for the field 'value.$' could not be found in the input '")
			},
			"same file L26363",
		},
		{
			"Parameters context path missing", `{"Type":"Pass","Parameters":{"value.$":"$$.Execution.Input.no_such_jsonpath"},"End":true}`, `{"a":1}`,
			func(c string) bool {
				return strings.HasPrefix(c, runtimePrefix("S", 2)+"The JSONPath '$$.Execution.Input.no_such_jsonpath' specified for the field 'value.$' could not be found in the input '")
			},
			"same file L26416",
		},
		{
			"Fail ErrorPath missing", `{"Type":"Fail","ErrorPath":"$.ErrorX","Cause":"c"}`, `{"a":1}`,
			func(c string) bool {
				return strings.HasPrefix(c, runtimePrefix("S", 2)+"The JSONPath '$.ErrorX' specified for the field 'ErrorPath' could not be found in the input '")
			},
			"same file L26310",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDriver(t, `{"StartAt":"S","States":{"S":`+tt.state+`}}`, rules())
			d.start(tt.input)
			o := d.runToEnd(10)
			failedWith(t, o, "States.Runtime", tt.check)
			entered := d.history[1].Type
			d.wantShape(tt.source, "ExecutionStarted 1<-0", entered+" 2<-0", "ExecutionFailed 3<-2")
		})
	}
}

// InputPath and OutputPath null give {}; ResultPath null keeps the raw
// input; ResultPath creates missing objects and overwrites
// (stepfunctions-asl.md 4.2, 4.4; aws-recorded localstack@8b9a79f
// v2/base/test_base.snapshot.json:L948, L980; spec#inputoutput-processing-examples).
func TestInputOutputProcessing(t *testing.T) {
	tests := []struct {
		name, state, input, want, source string
	}{
		{"InputPath null", `{"Type":"Pass","InputPath":null,"End":true}`, `{"a":1}`, `{}`, "test_base.snapshot.json:L948"},
		{"OutputPath null", `{"Type":"Pass","OutputPath":null,"End":true}`, `{"a":1}`, `{}`, "test_base.snapshot.json:L980"},
		{"ResultPath null", `{"Type":"Pass","Result":"r","ResultPath":null,"End":true}`, `{"a":1}`, `{"a":1}`, "spec#defaults"},
		{"ResultPath creates parents", `{"Type":"Pass","Result":"Hi!","ResultPath":"$.b.greeting","End":true}`, `{"a":1}`, `{"a":1,"b":{"greeting":"Hi!"}}`, "spec#inputoutput-processing-examples"},
		{"ResultPath overwrites", `{"Type":"Pass","Result":2,"ResultPath":"$.a","End":true}`, `{"a":1,"b":0}`, `{"a":2,"b":0}`, "spec#using-inputpath-parameters-resultselector-resultpath-and-outputpath"},
		{"InputPath then Parameters", `{"Type":"Pass","InputPath":"$.in","Parameters":{"x.$":"$.v","lit":"$customerName"},"End":true}`, `{"in":{"v":3}}`, `{"x":3,"lit":"$customerName"}`, "aws-doc dg/transforming-data: values without .$ are copied"},
		{"Parameters then ResultPath into raw input", `{"Type":"Pass","InputPath":"$.in","Parameters":{"x.$":"$.v"},"ResultPath":"$.out","End":true}`, `{"in":{"v":3}}`, `{"in":{"v":3},"out":{"x":3}}`, "stepfunctions-asl.md 4.1 step 3"},
		{"OutputPath selects", `{"Type":"Pass","Result":{"k":"v"},"ResultPath":"$.r","OutputPath":"$.r","End":true}`, `{}`, `{"k":"v"}`, "stepfunctions-asl.md 4.1 step 4"},
		{"Parameters intrinsic", `{"Type":"Pass","Parameters":{"sum.$":"States.MathAdd($.a, 2)"},"End":true}`, `{"a":1}`, `{"sum":3}`, "aws-doc dg/intrinsic-functions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDriver(t, `{"StartAt":"S","States":{"S":`+tt.state+`}}`, rules())
			d.start(tt.input)
			o := d.runToEnd(10)
			if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, tt.want)) {
				t.Fatalf("outcome = %#v, want output %s (%s)", o, tt.want, tt.source)
			}
		})
	}
}

// In a Pass state "$$" holds Execution, StateMachine and State {Name,
// EnteredTime}, no RetryCount and no Task (aws-recorded localstack@8b9a79f
// v2/base/test_base.snapshot.json:L597-L615); EnteredTime has zero, three,
// six or nine fraction digits (aws-doc dg/input-output-contextobject).
func TestContextObjectInPass(t *testing.T) {
	d := newDriver(t, `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"ctx.$":"$$"},"End":true}}}`, rules())
	d.start(`{"a":1}`)
	o := d.runToEnd(10)
	s, ok := o.(asl.Succeeded)
	if !ok {
		t.Fatalf("outcome = %#v", o)
	}
	ctx := at(s.Output, "ctx")
	if got := keys(ctx); strings.Join(got, ",") != "Execution,State,StateMachine" {
		t.Fatalf("$$ keys = %v, want Execution, State, StateMachine", got)
	}
	state := at(ctx, "State")
	if got := keys(state); strings.Join(got, ",") != "EnteredTime,Name" {
		t.Errorf("$$.State keys = %v, want EnteredTime and Name", got)
	}
	if !equal(at(state, "Name"), document.String("P")) || !equal(at(state, "EnteredTime"), document.String("2024-01-02T03:04:05.678Z")) {
		t.Errorf("$$.State = %s, want Name P and EnteredTime 2024-01-02T03:04:05.678Z", encode(state))
	}
	if exec := at(ctx, "Execution"); !equal(exec, executionContext(decode(t, `{"a":1}`))) {
		t.Errorf("$$.Execution = %s, want the start context's members", encode(exec))
	}
}

// Over 262,144 bytes of output from a Pass state ends the execution with
// States.DataLimitExceeded straight after PassStateEntered, with the
// recorded cause and no prefix (aws-recorded localstack@8b9a79f
// v2/error_handling/test_states_errors.snapshot.json:L608; stepfunctions-asl.md 8.5).
func TestPassDataLimitExceeded(t *testing.T) {
	big := strings.Repeat("x", 262_145)
	d := newDriver(t, `{"StartAt":"State_1","States":{"State_1":{"Type":"Pass","Result":"`+big+`","End":true}}}`, rules())
	d.start(`{}`)
	o := d.runToEnd(10)
	failedWith(t, o, "States.DataLimitExceeded", func(c string) bool {
		return c == "The state/task 'State_1' returned a result with a size exceeding the maximum number of bytes service limit."
	})
	d.wantShape("test_states_errors.snapshot.json:L608", "ExecutionStarted 1<-0", "PassStateEntered 2<-0", "ExecutionFailed 3<-2")
}

const legacyTask = `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"` + functionArn + `",%s"End":true}%s}}`

// A function ARN Task writes TaskStateEntered, LambdaFunctionScheduled
// {resource, input}, LambdaFunctionStarted, LambdaFunctionSucceeded
// {output}, TaskStateExited, each after the one before
// (stepfunctions-asl.md 11.2 rules 3 and 4, 11.3; aws-recorded
// localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L13677-L13809).
func TestLambdaFunctionTask(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(legacyTask, "", ""), rules())
	d.answer = succeedWith(decode(t, `{"result":"ok"}`))
	d.start(`{"in":1}`)
	o := d.runToEnd(10)
	d.wantShape("test_base_scenarios.snapshot.json:L13677-L13809",
		"ExecutionStarted 1<-0", "TaskStateEntered 2<-0", "LambdaFunctionScheduled 3<-2", "LambdaFunctionStarted 4<-3",
		"LambdaFunctionSucceeded 5<-4", "TaskStateExited 6<-5", "ExecutionSucceeded 7<-6")
	if len(d.invoked) != 1 {
		t.Fatalf("invocations = %#v, want one", d.invoked)
	}
	inv := d.invoked[0]
	if inv.Resource != asl.FunctionARN || inv.Function != functionArn || !equal(inv.Payload, decode(t, `{"in":1}`)) {
		t.Errorf("InvokeLambda = %#v, want the function ARN with the effective input (contract 5.5)", inv)
	}
	if inv.Timeout != 99_999_999*time.Second {
		t.Errorf("InvokeLambda.Timeout = %v, want the default 99,999,999 s (aws-doc dg/state-task)", inv.Timeout)
	}
	sched := d.event("LambdaFunctionScheduled")
	if detailString(sched, "resource") != functionArn || !equal(detailJSON(t, sched, "input"), decode(t, `{"in":1}`)) {
		t.Errorf("LambdaFunctionScheduled details = %s", encode(sched.Details))
	}
	if _, has := detail(sched, "timeoutInSeconds"); has {
		t.Errorf("LambdaFunctionScheduled details = %s, want no timeoutInSeconds when the definition sets none (stepfunctions-asl.md 8.6)", encode(sched.Details))
	}
	if d.event("LambdaFunctionStarted").Details != nil {
		t.Errorf("LambdaFunctionStarted details = %s, want none (stepfunctions-asl.md 11.3)", encode(d.event("LambdaFunctionStarted").Details))
	}
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `{"result":"ok"}`)) {
		t.Errorf("outcome = %#v, want the function's output", o)
	}
}

// An uncaught function error writes LambdaFunctionFailed {error, cause}
// and ExecutionFailed, no TaskStateExited (stepfunctions-asl.md 11.3;
// aws-recorded localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L13781-L13809).
func TestLambdaFunctionTaskUncaughtFailure(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(legacyTask, "", ""), rules())
	cause := `{"errorMessage":"boom","errorType":"Exception","requestId":"r","stackTrace":[]}`
	d.answer = func(inv asl.InvokeLambda, at time.Time) asl.Event {
		return asl.LambdaFailed{At: at, Task: inv.Task, Err: asl.StateError{Name: "Exception", Cause: cause}}
	}
	d.start(`{}`)
	o := d.runToEnd(10)
	d.wantShape("test_base_scenarios.snapshot.json:L13781-L13809",
		"ExecutionStarted 1<-0", "TaskStateEntered 2<-0", "LambdaFunctionScheduled 3<-2", "LambdaFunctionStarted 4<-3",
		"LambdaFunctionFailed 5<-4", "ExecutionFailed 6<-5")
	failed := d.event("LambdaFunctionFailed")
	if detailString(failed, "error") != "Exception" || detailString(failed, "cause") != cause {
		t.Errorf("LambdaFunctionFailed details = %s", encode(failed.Details))
	}
	failedWith(t, o, "Exception", func(c string) bool { return c == cause })
}

// invokeTask takes the Task's extra fields and extra states; doneState is
// the catch target, added only where a catcher reaches it, so no
// definition holds an unreachable state (whether AWS accepts one is
// UNVERIFIED, stepfunctions-asl.md 3.2).
const (
	invokeTask = `{"StartAt":"T","States":{
	"T":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","Parameters":{"FunctionName":"` + functionArn + `","Payload.$":"$"},%s"End":true}%s}}`
	doneState = `,"Done":{"Type":"Pass","End":true}`
)

// lambda:invoke writes TaskScheduled {resourceType "lambda", resource
// "invoke", region, parameters}, TaskStarted, TaskSucceeded {output}
// (stepfunctions-asl.md 11.3 optimized row; aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L10389-L10622) and invokes
// Parameters.FunctionName with Parameters.Payload (contract 5.5).
func TestLambdaInvokeTask(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(invokeTask, "", ""), rules())
	result := decode(t, `{"ExecutedVersion":"$LATEST","Payload":{"r":1},"StatusCode":200}`)
	d.answer = succeedWith(result)
	d.start(`{"in":1}`)
	o := d.runToEnd(10)
	d.wantShape("test_base_scenarios.snapshot.json:L10389-L10622",
		"ExecutionStarted 1<-0", "TaskStateEntered 2<-0", "TaskScheduled 3<-2", "TaskStarted 4<-3",
		"TaskSucceeded 5<-4", "TaskStateExited 6<-5", "ExecutionSucceeded 7<-6")
	inv := d.invoked[0]
	if inv.Resource != asl.LambdaInvoke || inv.Function != functionArn || !equal(inv.Payload, decode(t, `{"in":1}`)) {
		t.Errorf("InvokeLambda = %#v, want FunctionName and the resolved Payload", inv)
	}
	sched := d.event("TaskScheduled")
	if detailString(sched, "resourceType") != "lambda" || detailString(sched, "resource") != "invoke" || detailString(sched, "region") != "us-east-1" {
		t.Errorf("TaskScheduled details = %s, want resourceType lambda, resource invoke, the region", encode(sched.Details))
	}
	if !equal(detailJSON(t, sched, "parameters"), decode(t, `{"FunctionName":"`+functionArn+`","Payload":{"in":1}}`)) {
		t.Errorf("TaskScheduled parameters = %s", encode(sched.Details))
	}
	started := d.event("TaskStarted")
	if detailString(started, "resourceType") != "lambda" || detailString(started, "resource") != "invoke" {
		t.Errorf("TaskStarted details = %s", encode(started.Details))
	}
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, result) {
		t.Errorf("outcome = %#v, want the integration result", o)
	}
}

// A caught Task failure: TaskFailed 5<-4, TaskStateExited 6<-5 with the
// error output {Error, Cause}, the catcher's target entered 7<-6
// (aws-recorded localstack@8b9a79f v2/error_handling/test_states_errors.snapshot.json:L65-L93;
// stepfunctions-asl.md 8.4).
func TestCaughtTaskFailure(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(invokeTask, `"Catch":[{"ErrorEquals":["States.ALL"],"Next":"Done"}],`, doneState), rules())
	d.answer = func(inv asl.InvokeLambda, at time.Time) asl.Event {
		return asl.LambdaFailed{At: at, Task: inv.Task, Err: asl.StateError{Name: "Exception", Cause: "c"}}
	}
	d.start(`{}`)
	o := d.runToEnd(10)
	d.wantShape("test_states_errors.snapshot.json:L65-L93",
		"ExecutionStarted 1<-0", "TaskStateEntered 2<-0", "TaskScheduled 3<-2", "TaskStarted 4<-3",
		"TaskFailed 5<-4", "TaskStateExited 6<-5", "PassStateEntered 7<-6", "PassStateExited 8<-7", "ExecutionSucceeded 9<-8")
	failed := d.event("TaskFailed")
	if detailString(failed, "error") != "Exception" || detailString(failed, "cause") != "c" ||
		detailString(failed, "resourceType") != "lambda" || detailString(failed, "resource") != "invoke" {
		t.Errorf("TaskFailed details = %s, want error, cause, resource, resourceType", encode(failed.Details))
	}
	want := decode(t, `{"Error":"Exception","Cause":"c"}`)
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, want) {
		t.Errorf("outcome = %#v, want the error output %s (catcher ResultPath defaults to $)", o, encode(want))
	}
}

// States.Runtime is never caught, even by a catcher naming it: a Task's
// OutputPath through a missing parent fails the execution 6<-5 after
// LambdaFunctionSucceeded (aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L11164; stepfunctions-asl.md
// finding 1, 4.6).
func TestRuntimeErrorIsNotCaught(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(legacyTask, `"OutputPath":"$.no.such.result.path","Catch":[{"ErrorEquals":["States.Runtime"],"Next":"Done"},{"ErrorEquals":["States.ALL"],"Next":"Done"}],`, `,"Done":{"Type":"Pass","End":true}`), rules())
	d.answer = succeedWith(decode(t, `{"a":1}`))
	d.start(`{}`)
	o := d.runToEnd(10)
	d.wantShape("test_base_scenarios.snapshot.json:L11164",
		"ExecutionStarted 1<-0", "TaskStateEntered 2<-0", "LambdaFunctionScheduled 3<-2", "LambdaFunctionStarted 4<-3",
		"LambdaFunctionSucceeded 5<-4", "ExecutionFailed 6<-5")
	failedWith(t, o, "States.Runtime", func(c string) bool {
		return c == runtimePrefix("T", 2)+"Invalid path '$.no.such.result.path' : Missing property in path $['no']"
	})
}

// Retriers count their own attempts across one state visit: errors A, B,
// C, B against [{A,B; Interval 1; Backoff 2; Max 2}, {C; Interval 5}] wait
// 1, 2 and 5 seconds, then go to Catch (spec#complex-retry-scenarios;
// stepfunctions-asl.md 8.2). A retry writes no event: each TaskScheduled
// after the first points at the TaskFailed before it (11.2 rule 7).
func TestRetrierCountsAndDelays(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(invokeTask, `"Retry":[{"ErrorEquals":["A","B"],"IntervalSeconds":1,"BackoffRate":2,"MaxAttempts":2},{"ErrorEquals":["C"],"IntervalSeconds":5}],"Catch":[{"ErrorEquals":["States.ALL"],"Next":"Done"}],`, doneState), rules())
	errs := []string{"A", "B", "C", "B"}
	d.answer = func(inv asl.InvokeLambda, at time.Time) asl.Event {
		// d.invoked already holds this invocation.
		name := errs[min(len(d.invoked), len(errs))-1]
		return asl.LambdaFailed{At: at, Task: inv.Task, Err: asl.StateError{Name: name, Cause: "c"}}
	}
	d.start(`{}`)
	o := d.runToEnd(50)
	if len(d.invoked) != 4 {
		t.Fatalf("invocations = %d, want 4 (errors A, B, C, B); history:\n%s", len(d.invoked), d.shape())
	}
	wantAt := []time.Duration{0, 1 * time.Second, 3 * time.Second, 8 * time.Second}
	for i, at := range d.invokes {
		if !at.Equal(t0.Add(wantAt[i])) {
			t.Errorf("invocation %d at +%v, want +%v", i+1, at.Sub(t0), wantAt[i])
		}
	}
	scheduled := d.events("TaskScheduled")
	failed := d.events("TaskFailed")
	for i := 1; i < len(scheduled); i++ {
		if scheduled[i].PreviousEventID != failed[i-1].ID {
			t.Errorf("TaskScheduled %d points at %d, want the TaskFailed %d before it (11.2 rule 7)", scheduled[i].ID, scheduled[i].PreviousEventID, failed[i-1].ID)
		}
	}
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `{"Error":"B","Cause":"c"}`)) {
		t.Errorf("outcome = %#v, want the catcher's error output for B", o)
	}
}

// $$.State.RetryCount is 0 on the first attempt and 1, 2, 3 on retries
// (aws-recorded localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L10430-L10434);
// MaxAttempts 3 is the default (aws-doc dg/concepts-error-handling).
func TestRetryCountInContext(t *testing.T) {
	def := `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke",
		"Parameters":{"FunctionName":"` + functionArn + `","Payload":{"n.$":"$$.State.RetryCount"}},
		"Retry":[{"ErrorEquals":["States.ALL"],"IntervalSeconds":1}],"End":true}}}`
	d := newDriver(t, def, rules())
	d.answer = func(inv asl.InvokeLambda, at time.Time) asl.Event {
		return asl.LambdaFailed{At: at, Task: inv.Task, Err: asl.StateError{Name: "E", Cause: "c"}}
	}
	d.start(`{}`)
	o := d.runToEnd(50)
	if len(d.invoked) != 4 {
		t.Fatalf("invocations = %d, want 4 (one try, three retries)", len(d.invoked))
	}
	for i, inv := range d.invoked {
		if want := decode(t, fmt.Sprintf(`{"n":%d}`, i)); !equal(inv.Payload, want) {
			t.Errorf("attempt %d payload = %s, want %s", i+1, encode(inv.Payload), encode(want))
		}
	}
	failedWith(t, o, "E", func(c string) bool { return c == "c" })
}

// MaxAttempts 0 never retries (spec#retrying-after-error).
func TestMaxAttemptsZero(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(invokeTask, `"Retry":[{"ErrorEquals":["States.ALL"],"MaxAttempts":0}],`, ""), rules())
	d.answer = func(inv asl.InvokeLambda, at time.Time) asl.Event {
		return asl.LambdaFailed{At: at, Task: inv.Task, Err: asl.StateError{Name: "E", Cause: "c"}}
	}
	d.start(`{}`)
	d.runToEnd(20)
	if len(d.invoked) != 1 {
		t.Fatalf("invocations = %d, want 1", len(d.invoked))
	}
}

// A Task past TimeoutSeconds fails with States.Timeout and no cause
// (aws-recorded localstack@8b9a79f v2/timeouts/test_timeouts.snapshot.json:L60
// for the function ARN form, L151-L165 for lambda:invoke); a late result is
// dropped (contract 5.4; stepfunctions-asl.md 9.1). States.TaskFailed does
// not match States.Timeout (aws-doc dg/concepts-error-handling).
func TestTaskTimeout(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(legacyTask, `"TimeoutSeconds":5,"Catch":[{"ErrorEquals":["States.TaskFailed"],"Next":"Done"}],`, `,"Done":{"Type":"Pass","End":true}`), rules())
	d.start(`{}`)
	d.runUntilWaiting()
	if len(d.pending) != 1 || d.pending[0].Timeout != 5*time.Second {
		t.Fatalf("pending invocations = %#v, want one with Timeout 5s", d.pending)
	}
	inv := d.pending[0]
	d.pending = nil
	_, at, ok := d.nextTimer()
	if !ok || !at.Equal(t0.Add(5*time.Second)) {
		t.Fatalf("next timer at %v (%v), want the task timeout at +5s", at, ok)
	}
	o := d.runToEnd(10)
	timedOut := d.event("LambdaFunctionTimedOut")
	if detailString(timedOut, "error") != "States.Timeout" {
		t.Errorf("LambdaFunctionTimedOut details = %s, want error States.Timeout", encode(timedOut.Details))
	}
	if _, has := detail(timedOut, "cause"); has {
		t.Errorf("LambdaFunctionTimedOut details = %s, want no cause", encode(timedOut.Details))
	}
	if f, ok := o.(asl.Failed); !ok || f.Error != "States.Timeout" {
		t.Fatalf("outcome = %#v, want Failed States.Timeout (States.TaskFailed does not catch it)", o)
	}
	x, hs, effs := asl.Step(d.def, d.rules, fixedSource{}, d.x, asl.LambdaSucceeded{At: at, Task: inv.Task, Output: document.Map{}})
	if len(hs) != 0 || len(effs) != 0 || !sameExecution(t, x, d.x) {
		t.Errorf("a late result changed the execution: %d events, %d effects", len(hs), len(effs))
	}

	caught := newDriver(t, fmt.Sprintf(legacyTask, `"TimeoutSeconds":5,"Catch":[{"ErrorEquals":["States.Timeout"],"Next":"Done"}],`, `,"Done":{"Type":"Pass","End":true}`), rules())
	caught.start(`{}`)
	caught.runUntilWaiting()
	caught.pending = nil
	if _, ok := caught.runToEnd(10).(asl.Succeeded); !ok {
		t.Errorf("a catcher on States.Timeout did not catch the task timeout:\n%s", caught.shape())
	}
}

// lambda:invoke past TimeoutSeconds writes TaskTimedOut {error
// States.Timeout, resource invoke, resourceType lambda} then ExecutionFailed
// {error States.Timeout} with no cause (aws-recorded localstack@8b9a79f
// v2/timeouts/test_timeouts.snapshot.json:L151-L165).
func TestLambdaInvokeTimeout(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(invokeTask, `"TimeoutSeconds":5,`, ""), rules())
	d.start(`{}`)
	d.runUntilWaiting()
	d.pending = nil
	d.runToEnd(10)
	ev := d.event("TaskTimedOut")
	if detailString(ev, "error") != "States.Timeout" || detailString(ev, "resource") != "invoke" || detailString(ev, "resourceType") != "lambda" {
		t.Errorf("TaskTimedOut details = %s", encode(ev.Details))
	}
	last := d.history[len(d.history)-1]
	if last.Type != "ExecutionFailed" || last.PreviousEventID != ev.ID || detailString(last, "error") != "States.Timeout" {
		t.Errorf("last event = %s %d<-%d %s, want ExecutionFailed after TaskTimedOut", last.Type, last.ID, last.PreviousEventID, encode(last.Details))
	}
	if _, has := detail(last, "cause"); has {
		t.Errorf("ExecutionFailed details = %s, want no cause (test_timeouts.snapshot.json:L165)", encode(last.Details))
	}
	sched := d.event("TaskScheduled")
	if v, ok := detail(sched, "timeoutInSeconds"); !ok || !equal(v, document.Number("5")) {
		t.Errorf("TaskScheduled details = %s, want timeoutInSeconds 5 (test_timeouts.snapshot.json:L125)", encode(sched.Details))
	}
}

// Execution TimeoutSeconds ends the execution TIMED_OUT with
// ExecutionTimedOut (stepfunctions-asl.md 3.1, 8.6; [S]
// com.amazonaws.sfn#HistoryEventType).
func TestExecutionTimeout(t *testing.T) {
	d := newDriver(t, `{"StartAt":"W","TimeoutSeconds":10,"States":{"W":{"Type":"Wait","Seconds":100,"End":true}}}`, rules())
	d.start(`{}`)
	o := d.runToEnd(10)
	if _, ok := o.(asl.TimedOut); !ok {
		t.Fatalf("outcome = %#v, want TimedOut", o)
	}
	last := d.history[len(d.history)-1]
	if last.Type != "ExecutionTimedOut" || !last.Timestamp.Equal(t0.Add(10*time.Second)) {
		t.Errorf("last event = %s at %v, want ExecutionTimedOut at +10s", last.Type, last.Timestamp)
	}
}

// Parallel gives each branch the input and returns one result per branch
// in Branches order (aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L246); it starts with
// ParallelStateEntered 2<-0 and ParallelStateStarted 3<-2 (same file
// L19944-L20043) and ends with ParallelStateExited.
func TestParallel(t *testing.T) {
	branch := `{"StartAt":"B%d","States":{"B%d":{"Type":"Pass","End":true}}}`
	def := fmt.Sprintf(`{"StartAt":"Par","States":{"Par":{"Type":"Parallel","End":true,"Branches":[%s,%s,%s]}}}`,
		fmt.Sprintf(branch, 1, 1), fmt.Sprintf(branch, 2, 2), fmt.Sprintf(branch, 3, 3))
	d := newDriver(t, def, rules())
	d.start(`[1,2,3,4]`)
	o := d.runToEnd(50)
	if got := shape(d.history[:3]); got != "ExecutionStarted 1<-0\nParallelStateEntered 2<-0\nParallelStateStarted 3<-2" {
		t.Errorf("history starts:\n%s", got)
	}
	if d.event("ParallelStateStarted").Details != nil {
		t.Errorf("ParallelStateStarted has details %s, want none", encode(d.event("ParallelStateStarted").Details))
	}
	if n := len(d.history); d.history[n-2].Type != "ParallelStateExited" || d.history[n-1].Type != "ExecutionSucceeded" {
		t.Errorf("history ends:\n%s", shape(d.history[n-2:]))
	}
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `[[1,2,3,4],[1,2,3,4],[1,2,3,4]]`)) {
		t.Errorf("outcome = %#v, want one copy per branch", o)
	}
}

// An inline Map with MaxConcurrency 1 runs iterations in order with the
// recorded linkage (stepfunctions-asl.md 11.2 rule 6, 11.3 Inline Map row;
// aws-recorded localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L11352-L11524).
func TestInlineMapSequential(t *testing.T) {
	def := `{"StartAt":"MapState","States":{"MapState":{"Type":"Map","MaxConcurrency":1,"End":true,
		"ItemProcessor":{"StartAt":"Inner","States":{"Inner":{"Type":"Pass","End":true}}}}}}`
	d := newDriver(t, def, rules())
	d.start(`[1,2,3]`)
	o := d.runToEnd(100)
	d.wantShape("test_base_scenarios.snapshot.json:L11352-L11524",
		"ExecutionStarted 1<-0", "MapStateEntered 2<-0", "MapStateStarted 3<-2",
		"MapIterationStarted 4<-3", "PassStateEntered 5<-4", "PassStateExited 6<-5", "MapIterationSucceeded 7<-6",
		"MapIterationStarted 8<-6", "PassStateEntered 9<-8", "PassStateExited 10<-9", "MapIterationSucceeded 11<-10",
		"MapIterationStarted 12<-10", "PassStateEntered 13<-12", "PassStateExited 14<-13", "MapIterationSucceeded 15<-14",
		"MapStateSucceeded 16<-15", "MapStateExited 17<-15", "ExecutionSucceeded 18<-17")
	if v, _ := detail(d.event("MapStateStarted"), "length"); !equal(v, document.Number("3")) {
		t.Errorf("MapStateStarted details = %s, want length 3", encode(d.event("MapStateStarted").Details))
	}
	for i, ev := range d.events("MapIterationStarted") {
		idx, _ := detail(ev, "index")
		if !equal(idx, document.Number(strconv.Itoa(i))) || detailString(ev, "name") != "MapState" {
			t.Errorf("MapIterationStarted %d details = %s, want index %d and name MapState", ev.ID, encode(ev.Details), i)
		}
	}
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `[1,2,3]`)) {
		t.Errorf("outcome = %#v, want the iteration outputs in item order (spec#writing-results)", o)
	}
}

// ItemSelector builds each iteration's input from the Map's input and
// $$.Map.Item (aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L21872).
func TestInlineMapItemSelector(t *testing.T) {
	def := `{"StartAt":"M","States":{"M":{"Type":"Map","ItemsPath":"$.items","End":true,
		"ItemSelector":{"bucketName.$":"$.content.bucket","value.$":"$$.Map.Item.Value","index.$":"$$.Map.Item.Index"},
		"ItemProcessor":{"StartAt":"Inner","States":{"Inner":{"Type":"Pass","End":true}}}}}}`
	d := newDriver(t, def, rules())
	d.start(`{"content":{"bucket":"test-bucket"},"items":["1","2"]}`)
	o := d.runToEnd(100)
	want := decode(t, `[{"bucketName":"test-bucket","value":"1","index":0},{"bucketName":"test-bucket","value":"2","index":1}]`)
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, want) {
		t.Fatalf("outcome = %#v, want %s", o, encode(want))
	}
}

// A Fail state in an iteration fails the Map with its error; a catcher on
// the Map sees it, with the recorded linkage (aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L1375-L1430;
// stepfunctions-asl.md 7.2, 11.3 Inline Map caught failure row).
func TestInlineMapCaughtFailure(t *testing.T) {
	def := `{"StartAt":"M","States":{
		"M":{"Type":"Map","Next":"Done","Catch":[{"ErrorEquals":["HandleItemFailError"],"Next":"Done"}],
			"ItemProcessor":{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"HandleItemFailError","Cause":"HandleItemFailCause"}}}},
		"Done":{"Type":"Pass","End":true}}}`
	d := newDriver(t, def, rules())
	d.start(`[1]`)
	o := d.runToEnd(100)
	d.wantShape("test_base_scenarios.snapshot.json:L1375-L1414",
		"ExecutionStarted 1<-0", "MapStateEntered 2<-0", "MapStateStarted 3<-2", "MapIterationStarted 4<-3",
		"FailStateEntered 5<-4", "MapIterationFailed 6<-5", "MapStateFailed 7<-5", "MapStateExited 8<-7",
		"PassStateEntered 9<-8", "PassStateExited 10<-9", "ExecutionSucceeded 11<-10")
	want := decode(t, `{"Error":"HandleItemFailError","Cause":"HandleItemFailCause"}`)
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, want) {
		t.Fatalf("outcome = %#v, want %s (test_base_scenarios.snapshot.json:L1398-L1430)", o, encode(want))
	}
}

// The execution survives a jsonx round trip at any point: every later Step
// returns what it would have (contract 5.4, ADR 0009).
func TestExecutionPersistsMidWait(t *testing.T) {
	def := `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":60,"Next":"P"},"P":{"Type":"Pass","Result":1,"End":true}}}`
	a := newDriver(t, def, rules())
	a.start(`{}`)
	a.runUntilWaiting()
	b := newDriver(t, def, rules())
	b.history = append(b.history, a.history...)
	b.now = a.now
	maps.Copy(b.timers, a.timers)
	raw, err := a.x.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if err := b.x.UnmarshalJSON(raw); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	wantTimers := a.x.Timers()
	if got := b.x.Timers(); len(got) == 0 || fmt.Sprint(got) != fmt.Sprint(wantTimers) {
		t.Errorf("Timers after load = %v, want %v (what Rearm re-arms)", got, wantTimers)
	}
	a.runToEnd(10)
	b.runToEnd(10)
	if a.shape() != b.shape() {
		t.Fatalf("history after reload:\n%s\nwant:\n%s", b.shape(), a.shape())
	}
}

// A Lambda task in flight survives the round trip in InFlight, so the
// service can invoke it again after a load (contract 5.4).
func TestInFlightSurvivesPersistence(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(legacyTask, "", ""), rules())
	d.start(`{"a":1}`)
	d.runUntilWaiting()
	if len(d.pending) != 1 {
		t.Fatalf("pending = %#v, want one invocation", d.pending)
	}
	raw, err := d.x.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var loaded asl.Execution
	if err := loaded.UnmarshalJSON(raw); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	got := loaded.InFlight()
	if len(got) != 1 || got[0].Task != d.pending[0].Task || got[0].Function != functionArn || !equal(got[0].Payload, d.pending[0].Payload) {
		t.Fatalf("InFlight after load = %#v, want %#v", got, d.pending)
	}
}
