package asl_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// inferred, ASL-13
// Branches start at the same instant and their steps due together run in
// branch order; ParallelStateSucceeded and ParallelStateExited both point
// at the last branch event (stepfunctions-asl.md 11.2 rule 5, inferred
// for Parallel from the id gap LocalStack leaves).
func TestParallelOrderAndLinkageASL13(t *testing.T) {
	branch := `{"StartAt":"%[1]sa","States":{"%[1]sa":{"Type":"Pass","Next":"%[1]sb"},"%[1]sb":{"Type":"Pass","End":true}}}`
	def := fmt.Sprintf(`{"StartAt":"P","States":{"P":{"Type":"Parallel","End":true,"Branches":[%s,%s]}}}`,
		fmt.Sprintf(branch, "X"), fmt.Sprintf(branch, "Y"))
	d := newDriver(t, def, rules())
	d.start(`{}`)
	d.runToEnd(50)
	d.wantShape("understudy's order, ASL-13",
		"ExecutionStarted 1<-0", "ParallelStateEntered 2<-0", "ParallelStateStarted 3<-2",
		"PassStateEntered 4<-3", "PassStateExited 5<-4", "PassStateEntered 6<-3", "PassStateExited 7<-6",
		"PassStateEntered 8<-5", "PassStateExited 9<-8", "PassStateEntered 10<-7", "PassStateExited 11<-10",
		"ParallelStateSucceeded 12<-11", "ParallelStateExited 13<-11", "ExecutionSucceeded 14<-13")
	entered := d.events("PassStateEntered")
	names := make([]string, 0, len(entered))
	for _, e := range entered {
		names = append(names, detailString(e, "name"))
	}
	if got := strings.Join(names, ","); got != "Xa,Ya,Xb,Yb" {
		t.Errorf("entered order = %s, want branch order at each instant", got)
	}
}

// inferred, ASL-13
// A Fail state in a branch fails the Parallel with the branch's own error
// (not States.BranchFailed): ParallelStateFailed points at FailStateEntered
// and the other branches stop without events, their timers withdrawn.
func TestParallelBranchFailASL13(t *testing.T) {
	def := `{"StartAt":"P","States":{"P":{"Type":"Parallel","End":true,"Branches":[
		{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":5,"Next":"F"},"F":{"Type":"Fail","Error":"BranchErr","Cause":"why"}}},
		{"StartAt":"Long","States":{"Long":{"Type":"Wait","Seconds":100,"End":true}}}]}}}`
	d := newDriver(t, def, rules())
	d.start(`{}`)
	o := d.runToEnd(50)
	if f, ok := o.(asl.Failed); !ok || f.Error != "BranchErr" || f.Cause != "why" {
		t.Fatalf("outcome = %#v, want the branch's error", o)
	}
	d.wantShape("understudy's choice, ASL-13",
		"ExecutionStarted 1<-0", "ParallelStateEntered 2<-0", "ParallelStateStarted 3<-2",
		"WaitStateEntered 4<-3", "WaitStateEntered 5<-3", "WaitStateExited 6<-4", "FailStateEntered 7<-6",
		"ParallelStateFailed 8<-7", "ExecutionFailed 9<-8")
	if last := d.history[len(d.history)-1]; !last.Timestamp.Equal(t0.Add(5 * time.Second)) {
		t.Errorf("ExecutionFailed at %v, want at +5s, when the branch failed", last.Timestamp)
	}
	if len(d.timers) != 0 {
		t.Errorf("timers still armed: %v", d.timers)
	}
}

// A catcher on the Parallel sees the branch's error: ParallelStateExited
// points at ParallelStateFailed (stepfunctions-asl.md 11.2 rule 5, as
// recorded for Map).
func TestParallelCaughtFailure(t *testing.T) {
	def := `{"StartAt":"P","States":{
		"P":{"Type":"Parallel","Next":"Done","Catch":[{"ErrorEquals":["BranchErr"],"ResultPath":"$.err","Next":"Done"}],
			"Branches":[{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"BranchErr"}}}]},
		"Done":{"Type":"Pass","End":true}}}`
	d := newDriver(t, def, rules())
	d.start(`{"a":1}`)
	o := d.runToEnd(50)
	d.wantShape("stepfunctions-asl.md 11.2 rule 5",
		"ExecutionStarted 1<-0", "ParallelStateEntered 2<-0", "ParallelStateStarted 3<-2", "FailStateEntered 4<-3",
		"ParallelStateFailed 5<-4", "ParallelStateExited 6<-5", "PassStateEntered 7<-6", "PassStateExited 8<-7", "ExecutionSucceeded 9<-8")
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `{"a":1,"err":{"Error":"BranchErr","Cause":null}}`)) {
		t.Fatalf("outcome = %#v, want the error output placed at $.err", o)
	}
}

// Parameters builds each branch's input; ResultSelector, ResultPath and
// OutputPath process the branch results (stepfunctions-asl.md 4.1, 9.1).
func TestParallelInputAndOutputProcessing(t *testing.T) {
	branch := `{"StartAt":"B%[1]d","States":{"B%[1]d":{"Type":"Pass","End":true}}}`
	def := fmt.Sprintf(`{"StartAt":"P","States":{"P":{"Type":"Parallel","End":true,
		"InputPath":"$.in","Parameters":{"v.$":"$.v"},"ResultSelector":{"first.$":"$[0].v"},"ResultPath":"$.out","OutputPath":"$.out",
		"Branches":[%s,%s]}}}`, fmt.Sprintf(branch, 1), fmt.Sprintf(branch, 2))
	d := newDriver(t, def, rules())
	d.start(`{"in":{"v":7}}`)
	o := d.runToEnd(50)
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `{"first":7}`)) {
		t.Fatalf("outcome = %#v, want {\"first\":7}", o)
	}
	for _, e := range d.events("PassStateEntered") {
		if !equal(detailJSON(t, e, "input"), decode(t, `{"v":7}`)) {
			t.Errorf("branch input = %s, want the Parameters output", detailString(e, "input"))
		}
	}
}

// States.DataLimitExceeded from a Pass in a branch stays terminal: neither a
// catcher naming it nor the Parallel's States.ALL catcher sees it, and
// ExecutionFailed follows the Pass's entered event with no
// ParallelStateFailed (ASL-15, recorded 2026-11-03).
func TestParallelDataLimitIsTerminalASL15(t *testing.T) {
	big := strings.Repeat("a", 262_144)
	def := `{"StartAt":"P","States":{
		"P":{"Type":"Parallel","Next":"Done","Catch":[{"ErrorEquals":["States.DataLimitExceeded"],"Next":"Done"},{"ErrorEquals":["States.ALL"],"Next":"Done"}],
			"Branches":[{"StartAt":"Big","States":{"Big":{"Type":"Pass","Result":"` + big + `","End":true}}}]},
		"Done":{"Type":"Pass","End":true}}}`
	d := newDriver(t, def, rules())
	d.start(`{}`)
	o := d.runToEnd(50)
	failedWith(t, o, "States.DataLimitExceeded", func(c string) bool { return strings.Contains(c, "'Big'") })
	d.wantShape("ASL-15 03-pass-in-parallel-history",
		"ExecutionStarted 1<-0", "ParallelStateEntered 2<-0", "ParallelStateStarted 3<-2", "PassStateEntered 4<-3",
		"ExecutionFailed 5<-4")
}

// inferred, ASL-10
// A retried Parallel starts every branch again: ParallelStateStarted
// points at the failure retried and no ParallelStateFailed is written in
// between, as recorded for a retried Map (aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L1484).
func TestParallelRetryRerunsEveryBranchASL10(t *testing.T) {
	def := `{"StartAt":"P","States":{"P":{"Type":"Parallel","End":true,
		"Retry":[{"ErrorEquals":["E"],"MaxAttempts":1,"IntervalSeconds":2}],
		"Branches":[{"StartAt":"ok","States":{"ok":{"Type":"Pass","End":true}}},{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"E"}}}]}}}`
	d := newDriver(t, def, rules())
	d.start(`{}`)
	o := d.runToEnd(50)
	failedWith(t, o, "E", func(string) bool { return true })
	d.wantShape("understudy's choice, ASL-10",
		"ExecutionStarted 1<-0", "ParallelStateEntered 2<-0", "ParallelStateStarted 3<-2",
		"PassStateEntered 4<-3", "PassStateExited 5<-4", "FailStateEntered 6<-3",
		"ParallelStateStarted 7<-6", "PassStateEntered 8<-7", "PassStateExited 9<-8", "FailStateEntered 10<-7",
		"ParallelStateFailed 11<-10", "ExecutionFailed 12<-11")
	if at := d.event("PassStateEntered").Timestamp; !at.Equal(t0) {
		t.Errorf("first attempt at %v, want t0", at)
	}
	if retried := d.history[6]; !retried.Timestamp.Equal(t0.Add(2 * time.Second)) {
		t.Errorf("retry started at %v, want after IntervalSeconds 2", retried.Timestamp)
	}
}

// inferred, ASL-13
// Without MaxConcurrency every iteration starts at once: the
// MapIterationStarted events all point at MapStateStarted, and the
// iterations' steps due together run in item order.
func TestMapConcurrentOrderASL13(t *testing.T) {
	def := `{"StartAt":"M","States":{"M":{"Type":"Map","End":true,
		"ItemProcessor":{"StartAt":"I","States":{"I":{"Type":"Pass","End":true}}}}}}`
	d := newDriver(t, def, rules())
	d.start(`["a","b","c"]`)
	o := d.runToEnd(50)
	d.wantShape("understudy's order, ASL-13",
		"ExecutionStarted 1<-0", "MapStateEntered 2<-0", "MapStateStarted 3<-2",
		"MapIterationStarted 4<-3", "MapIterationStarted 5<-3", "MapIterationStarted 6<-3",
		"PassStateEntered 7<-4", "PassStateExited 8<-7", "MapIterationSucceeded 9<-8",
		"PassStateEntered 10<-5", "PassStateExited 11<-10", "MapIterationSucceeded 12<-11",
		"PassStateEntered 13<-6", "PassStateExited 14<-13", "MapIterationSucceeded 15<-14",
		"MapStateSucceeded 16<-15", "MapStateExited 17<-15", "ExecutionSucceeded 18<-17")
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `["a","b","c"]`)) {
		t.Fatalf("outcome = %#v, want the items in order", o)
	}
}

// MaxConcurrency bounds the iterations running at once; a waiting
// iteration starts when one finishes (stepfunctions-asl.md 9.2). Values
// over 40 and 0 run 40 at once, inline mode's limit (aws-doc
// dg/state-map-inline).
func TestMapMaxConcurrency(t *testing.T) {
	for _, tc := range []struct {
		limit, items, wantAtOnce int
	}{
		{2, 3, 2},
		{0, 41, 40},
		{100, 41, 40},
	} {
		t.Run(fmt.Sprintf("MaxConcurrency %d", tc.limit), func(t *testing.T) {
			def := fmt.Sprintf(`{"StartAt":"M","States":{"M":{"Type":"Map","MaxConcurrency":%d,"End":true,
				"ItemProcessor":{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":10,"End":true}}}}}}`, tc.limit)
			items := make([]string, tc.items)
			for i := range items {
				items[i] = strconv.Itoa(i)
			}
			d := newDriver(t, def, rules())
			d.start("[" + strings.Join(items, ",") + "]")
			o := d.runToEnd(1000)
			if _, ok := o.(asl.Succeeded); !ok {
				t.Fatalf("outcome = %#v", o)
			}
			atOnce := 0
			for _, e := range d.events("MapIterationStarted") {
				if e.Timestamp.Equal(t0) {
					atOnce++
				} else if !e.Timestamp.Equal(t0.Add(10 * time.Second)) {
					t.Errorf("iteration started at %v, want t0 or +10s", e.Timestamp)
				}
			}
			if atOnce != tc.wantAtOnce {
				t.Errorf("%d iterations started at once, want %d", atOnce, tc.wantAtOnce)
			}
		})
	}
}

// MaxConcurrencyPath reads the limit from the effective input: 0 and 1
// succeed, and a value that is not an integer fails with States.Runtime
// and the recorded text straight after MapStateEntered (aws-recorded
// localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L15655-L16130).
func TestMapMaxConcurrencyPath(t *testing.T) {
	def := `{"StartAt":"MapState","States":{"MapState":{"Type":"Map","ItemsPath":"$.Values","MaxConcurrencyPath":"$.MaxConcurrencyValue",
		"ItemProcessor":{"ProcessorConfig":{"Mode":"INLINE"},"StartAt":"HandleItem","States":{"HandleItem":{"Type":"Pass","End":true}}},"Next":"Final"},
		"Final":{"Type":"Pass","End":true}}}`
	for _, ok := range []string{"0", "1"} {
		d := newDriver(t, def, rules())
		d.start(`{"MaxConcurrencyValue":` + ok + `,"Values":["HelloWorld"]}`)
		o := d.runToEnd(50)
		if s, succeeded := o.(asl.Succeeded); !succeeded || !equal(s.Output, decode(t, `["HelloWorld"]`)) {
			t.Errorf("MaxConcurrencyPath %s: outcome = %#v", ok, o)
		}
	}
	for value, shown := range map[string]string{`{}`: `{}`, `"NoNumber"`: `NoNumber`} {
		d := newDriver(t, def, rules())
		d.start(`{"MaxConcurrencyValue":` + value + `,"Values":["HelloWorld"]}`)
		want := runtimePrefix("MapState", 2) + `The MaxConcurrencyPath field refers to value "` + shown + `" which is not a valid integer: $.MaxConcurrencyValue`
		failedWith(t, d.runToEnd(50), "States.Runtime", func(c string) bool { return c == want })
		d.wantShape("test_base_scenarios.snapshot.json:L16110", "ExecutionStarted 1<-0", "MapStateEntered 2<-0", "ExecutionFailed 3<-2")
	}
}

// An ItemsPath that selects anything but an array fails the Map with
// States.Runtime: inline mode accepts only arrays (aws-doc
// dg/state-map-inline); the cause is understudy's (UNVERIFIED).
func TestMapItemsMustBeAnArray(t *testing.T) {
	def := `{"StartAt":"M","States":{"M":{"Type":"Map","ItemsPath":"$.items","End":true,
		"ItemProcessor":{"StartAt":"I","States":{"I":{"Type":"Pass","End":true}}}}}}`
	d := newDriver(t, def, rules())
	d.start(`{"items":{"a":1}}`)
	failedWith(t, d.runToEnd(50), "States.Runtime", func(c string) bool { return strings.HasPrefix(c, runtimePrefix("M", 2)) })
}

// A Map over no items succeeds at once with [] (stepfunctions-asl.md 9.2:
// the result is the array of iteration outputs); MapStateSucceeded points
// at MapStateStarted, as there is no inner event (inferred, ASL-13).
func TestMapWithoutItems(t *testing.T) {
	def := `{"StartAt":"M","States":{"M":{"Type":"Map","End":true,
		"ItemProcessor":{"StartAt":"I","States":{"I":{"Type":"Pass","End":true}}}}}}`
	d := newDriver(t, def, rules())
	d.start(`[]`)
	o := d.runToEnd(50)
	d.wantShape("understudy's choice, ASL-13", "ExecutionStarted 1<-0", "MapStateEntered 2<-0", "MapStateStarted 3<-2",
		"MapStateSucceeded 4<-3", "MapStateExited 5<-3", "ExecutionSucceeded 6<-5")
	if v, _ := detail(d.event("MapStateStarted"), "length"); !equal(v, document.Number("0")) {
		t.Errorf("MapStateStarted details = %s, want length 0", encode(d.event("MapStateStarted").Details))
	}
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `[]`)) {
		t.Errorf("outcome = %#v, want []", o)
	}
}

// When a branch fails, a Lambda task still running in another branch is
// withdrawn with its timeout timer, and its late result changes nothing
// (stepfunctions-asl.md 9.1: running functions are not stopped).
func TestStoppedBranchDropsItsTask(t *testing.T) {
	def := `{"StartAt":"P","States":{
		"P":{"Type":"Parallel","Next":"Hold","Catch":[{"ErrorEquals":["States.ALL"],"Next":"Hold"}],"Branches":[
			{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"` + functionArn + `","End":true}}},
			{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"E"}}}]},
		"Hold":{"Type":"Wait","Seconds":60,"End":true}}}`
	d := newDriver(t, def, rules())
	d.start(`{}`)
	// Fire every step due now, leaving the invocation unanswered.
	for {
		_, at, ok := d.nextTimer()
		if !ok || at.After(d.now) {
			break
		}
		d.fire()
	}
	if len(d.invoked) != 1 {
		t.Fatalf("invocations = %d, want the task's one", len(d.invoked))
	}
	if d.events("WaitStateEntered") == nil {
		t.Fatalf("the Parallel was not caught:\n%s", d.shape())
	}
	before := len(d.history)
	if effs := d.apply(asl.LambdaSucceeded{At: d.now, Task: d.invoked[0].Task, Output: document.Map{}}); len(effs) != 0 || len(d.history) != before {
		t.Errorf("late result wrote %d events and %d effects, want none", len(d.history)-before, len(effs))
	}
	if len(d.timers) != 2 {
		t.Errorf("armed timers = %v, want only the execution deadline and the Wait (the task timeout withdrawn)", d.timers)
	}
}

// A failure in a nested Parallel reaches the enclosing Map's catcher: each
// level writes its failed events, innermost first.
func TestNestedFailureReachesOuterCatcher(t *testing.T) {
	def := `{"StartAt":"M","States":{
		"M":{"Type":"Map","MaxConcurrency":1,"Next":"Done","Catch":[{"ErrorEquals":["Inner"],"Next":"Done"}],
			"ItemProcessor":{"StartAt":"P","States":{"P":{"Type":"Parallel","End":true,
				"Branches":[{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"Inner","Cause":"deep"}}}]}}}},
		"Done":{"Type":"Pass","End":true}}}`
	d := newDriver(t, def, rules())
	d.start(`[1]`)
	o := d.runToEnd(50)
	d.wantShape("stepfunctions-asl.md 7.2, 11.2 rule 5",
		"ExecutionStarted 1<-0", "MapStateEntered 2<-0", "MapStateStarted 3<-2", "MapIterationStarted 4<-3",
		"ParallelStateEntered 5<-4", "ParallelStateStarted 6<-5", "FailStateEntered 7<-6", "ParallelStateFailed 8<-7",
		"MapIterationFailed 9<-8", "MapStateFailed 10<-8", "MapStateExited 11<-10",
		"PassStateEntered 12<-11", "PassStateExited 13<-12", "ExecutionSucceeded 14<-13")
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `{"Error":"Inner","Cause":"deep"}`)) {
		t.Fatalf("outcome = %#v, want the inner error caught by the Map", o)
	}
}

// A Map with waits inside survives a jsonx round trip mid-run: the rest of
// the run is what it would have been (contract 5.4, ADR 0009).
func TestRunPersistsMidIteration(t *testing.T) {
	def := `{"StartAt":"M","States":{"M":{"Type":"Map","MaxConcurrency":2,"ItemsPath":"$.items",
		"ItemSelector":{"i.$":"$$.Map.Item.Index","v.$":"$$.Map.Item.Value"},"Retry":[{"ErrorEquals":["E"],"MaxAttempts":1}],
		"ItemProcessor":{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":5,"End":true}}},"End":true}}}`
	whole := newDriver(t, def, rules())
	whole.start(`{"items":[1,2,3]}`)
	whole.runToEnd(100)

	d := newDriver(t, def, rules())
	d.start(`{"items":[1,2,3]}`)
	d.runUntilWaiting()
	raw, err := d.x.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var loaded asl.Execution
	if err := loaded.UnmarshalJSON(raw); err != nil {
		t.Fatalf("UnmarshalJSON(%s): %v", raw, err)
	}
	d.x = loaded
	d.runToEnd(100)
	if fingerprint(d.history) != fingerprint(whole.history) {
		t.Fatalf("history after a reload mid-run:\n%s\nwant\n%s", d.shape(), whole.shape())
	}
	o, _ := d.x.Outcome()
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `[{"i":0,"v":1},{"i":1,"v":2},{"i":2,"v":3}]`)) {
		t.Errorf("outcome = %#v", o)
	}
}
