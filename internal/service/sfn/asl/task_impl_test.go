package asl_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// A Lambda result over 262,144 bytes is a task failure, not a
// TaskSucceeded: States.DataLimitExceeded naming 'lambda' for lambda:invoke
// and the function ARN for the legacy form (aws-recorded localstack@8b9a79f
// v2/error_handling/test_states_errors.snapshot.json:L65, L352;
// stepfunctions-asl.md 8.5).
func TestTaskResultDataLimitExceeded(t *testing.T) {
	big := document.String(strings.Repeat("x", 262_145))
	for _, tt := range []struct {
		def, failed, resource string
	}{
		{fmt.Sprintf(invokeTask, "", ""), "TaskFailed", "lambda"},
		{fmt.Sprintf(legacyTask, "", ""), "LambdaFunctionFailed", functionArn},
	} {
		d := newDriver(t, tt.def, rules())
		d.answer = succeedWith(big)
		d.start(`{}`)
		o := d.runToEnd(10)
		started := d.history[3].Type
		d.wantShape("test_states_errors.snapshot.json:L65",
			"ExecutionStarted 1<-0", "TaskStateEntered 2<-0", d.history[2].Type+" 3<-2", started+" 4<-3",
			tt.failed+" 5<-4", "ExecutionFailed 6<-5")
		cause := "The state/task '" + tt.resource + "' returned a result with a size exceeding the maximum number of bytes service limit."
		failedWith(t, o, "States.DataLimitExceeded", func(c string) bool { return c == cause })
		if got := detailString(d.event(tt.failed), "cause"); got != cause {
			t.Errorf("%s cause = %q, want %q", tt.failed, got, cause)
		}
	}
}

// ResultSelector reads the result, ResultPath places it into the raw input
// (not the effective one), and OutputPath selects; $$ in a Task holds
// State.RetryCount 0 and the entered instant, not the result's
// (stepfunctions-asl.md 4.1; aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L10427-L10431).
func TestTaskResultProcessing(t *testing.T) {
	def := `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke",
		"InputPath":"$.in","Parameters":{"FunctionName":"` + functionArn + `","Payload.$":"$"},
		"ResultSelector":{"p.$":"$.Payload","state.$":"$$.State"},"ResultPath":"$.out","OutputPath":"$.out","End":true}}}`
	d := newDriver(t, def, rules())
	d.answer = func(inv asl.InvokeLambda, at time.Time) asl.Event {
		return asl.LambdaSucceeded{At: at.Add(time.Second), Task: inv.Task, Output: document.Map{{Key: "Payload", Value: inv.Payload}}}
	}
	d.start(`{"in":{"v":1}}`)
	o := d.runToEnd(10)
	want := decode(t, `{"p":{"v":1},"state":{"Name":"T","EnteredTime":"2024-01-02T03:04:05.678Z","RetryCount":0}}`)
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, want) {
		t.Fatalf("outcome = %#v, want %s", o, encode(want))
	}
}

// A path that fails after the result arrived is States.Runtime after the
// succeeded event, its cause naming the entered event (11.2 rule 8).
func TestTaskResultPathFailure(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(invokeTask, `"ResultSelector":{"x.$":"$.nope"},`, ""), rules())
	d.answer = succeedWith(decode(t, `{"a":1}`))
	d.start(`{}`)
	o := d.runToEnd(10)
	d.wantShape("11.2 rule 8", "ExecutionStarted 1<-0", "TaskStateEntered 2<-0", "TaskScheduled 3<-2", "TaskStarted 4<-3",
		"TaskSucceeded 5<-4", "ExecutionFailed 6<-5")
	failedWith(t, o, "States.Runtime", func(c string) bool { return strings.HasPrefix(c, runtimePrefix("T", 2)) })
}

// TimeoutSecondsPath reads the input after InputPath; the scheduled event
// shows the value and the timeout fires then.
func TestTaskTimeoutSecondsPath(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(legacyTask, `"InputPath":"$.in","TimeoutSecondsPath":"$.t",`, ""), rules())
	d.start(`{"in":{"t":7}}`)
	d.runUntilWaiting()
	if len(d.pending) != 1 || d.pending[0].Timeout != 7*time.Second {
		t.Fatalf("pending = %#v, want one invocation with Timeout 7s", d.pending)
	}
	if v, ok := detail(d.event("LambdaFunctionScheduled"), "timeoutInSeconds"); !ok || !equal(v, document.Number("7")) {
		t.Errorf("LambdaFunctionScheduled details = %s, want timeoutInSeconds 7", encode(d.event("LambdaFunctionScheduled").Details))
	}
	d.pending = nil
	failedWith(t, d.runToEnd(10), "States.Timeout", func(c string) bool { return c == "" })
	if last := d.history[len(d.history)-1]; !last.Timestamp.Equal(t0.Add(7 * time.Second)) {
		t.Errorf("ExecutionFailed at %v, want +7s", last.Timestamp.Sub(t0))
	}
}

// A TimeoutSecondsPath that selects nothing, or no positive integer, fails
// with States.Runtime and understudy's cause (UNVERIFIED: AWS's text is
// unrecorded).
func TestTaskTimeoutSecondsPathFailures(t *testing.T) {
	for _, tt := range []struct{ input, rest string }{
		{`{}`, "The TimeoutSecondsPath parameter does not reference an input value: $.t"},
		{`{"t":"5"}`, "The TimeoutSecondsPath parameter cannot be parsed as a long value: $.t == 5"},
		{`{"t":0}`, "The TimeoutSecondsPath parameter references a value that is not positive: $.t == 0"},
	} {
		d := newDriver(t, fmt.Sprintf(legacyTask, `"TimeoutSecondsPath":"$.t",`, ""), rules())
		d.start(tt.input)
		failedWith(t, d.runToEnd(10), "States.Runtime", func(c string) bool { return c == runtimePrefix("T", 2)+tt.rest })
		if len(d.invoked) != 0 {
			t.Errorf("%s: invoked %d times, want never", tt.input, len(d.invoked))
		}
	}
}

// An InvocationType path that resolves to anything but RequestResponse
// fails the Task with States.Runtime before it invokes; RequestResponse
// invokes.
func TestInvocationTypePathSFN13(t *testing.T) {
	// inferred, SFN-13
	def := `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke",
		"Parameters":{"FunctionName":"` + functionArn + `","InvocationType.$":"$.it"},"End":true}}}`
	refused := newDriver(t, def, rules())
	refused.start(`{"it":"Event"}`)
	failedWith(t, refused.runToEnd(10), "States.Runtime", func(c string) bool {
		return strings.HasPrefix(c, runtimePrefix("T", 2)+"understudy does not implement lambda:invoke with InvocationType \"Event\"")
	})
	if len(refused.invoked) != 0 {
		t.Errorf("an Event invocation was asked for: %#v", refused.invoked)
	}
	allowed := newDriver(t, def, rules())
	allowed.start(`{"it":"RequestResponse"}`)
	if _, ok := allowed.runToEnd(10).(asl.Succeeded); !ok || len(allowed.invoked) != 1 {
		t.Errorf("RequestResponse: %d invocations, history:\n%s", len(allowed.invoked), allowed.shape())
	}
}

// lambda:invoke sends FunctionName with its Qualifier, and no payload when
// Parameters have none.
func TestLambdaInvokeQualifierAndNoPayload(t *testing.T) {
	def := `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke",
		"Parameters":{"FunctionName":"fn","Qualifier":"live"},"End":true}}}`
	d := newDriver(t, def, rules())
	d.start(`{"a":1}`)
	d.runToEnd(10)
	if inv := d.invoked[0]; inv.Function != "fn:live" || inv.Payload != nil {
		t.Errorf("InvokeLambda = %#v, want fn:live without a payload", inv)
	}
}

// Stop and MachineDeleting while a task is in flight end the execution
// ABORTED, leave nothing in flight, and drop the late result.
func TestTaskInFlightAborts(t *testing.T) {
	stopped := newDriver(t, fmt.Sprintf(legacyTask, "", ""), rules())
	stopped.start(`{}`)
	stopped.runUntilWaiting()
	inv := stopped.pending[0]
	effs := stopped.apply(asl.Stop{At: t0})
	if len(stopped.x.InFlight()) != 0 || len(stopped.timers) != 0 {
		t.Errorf("after Stop: in flight %v, timers %v, want none (effects %#v)", stopped.x.InFlight(), stopped.timers, effs)
	}
	before := stopped.x
	if x, hs, _ := asl.Step(stopped.def, stopped.rules, fixedSource{}, stopped.x, asl.LambdaSucceeded{At: t0, Task: inv.Task, Output: document.Map{}}); len(hs) != 0 || !sameExecution(t, x, before) {
		t.Error("a result after Stop changed the execution")
	}

	deleting := newDriver(t, fmt.Sprintf(legacyTask, "", ""), rules())
	deleting.start(`{}`)
	deleting.runUntilWaiting()
	deleting.apply(asl.MachineDeleting{At: t0})
	if o := deleting.runToEnd(10); o != (asl.Aborted{}) {
		t.Errorf("outcome = %#v, want Aborted after the deletion", o)
	}
	if last := deleting.history[len(deleting.history)-1]; last.Type != "ExecutionAborted" {
		t.Errorf("history:\n%s\nwant ExecutionAborted last", deleting.shape())
	}
}

// A result that reaches the engine at or after the task's timeout instant,
// before the timeout timer fired, loses to the timeout: the function ran on
// wall time past an emulator-time deadline (spec "Runtime decisions").
func TestLateResultTimesOut(t *testing.T) {
	for _, late := range []time.Duration{5 * time.Second, time.Hour} {
		d := newDriver(t, fmt.Sprintf(legacyTask, `"TimeoutSeconds":5,`, ""), rules())
		d.start(`{}`)
		d.runUntilWaiting()
		inv := d.pending[0]
		d.pending = nil
		d.apply(asl.LambdaSucceeded{At: t0.Add(late), Task: inv.Task, Output: document.Map{}})
		d.wantShape("contract 5.5 item 3", "ExecutionStarted 1<-0", "TaskStateEntered 2<-0", "LambdaFunctionScheduled 3<-2",
			"LambdaFunctionStarted 4<-3", "LambdaFunctionTimedOut 5<-4", "ExecutionFailed 6<-5")
		if at := d.event("LambdaFunctionTimedOut").Timestamp; !at.Equal(t0.Add(5 * time.Second)) {
			t.Errorf("result at +%v: timed out at +%v, want the deadline +5s", late, at.Sub(t0))
		}
		if len(d.timers) != 0 {
			t.Errorf("result at +%v: timers left armed: %v", late, d.timers)
		}
	}
}

// An execution saved with a task in flight and loaded again takes the
// result the way the original does, the task timeout included.
func TestTaskPersistenceRoundTrip(t *testing.T) {
	a := newDriver(t, fmt.Sprintf(invokeTask, `"TimeoutSeconds":30,"ResultPath":"$.r",`, ""), rules())
	a.start(`{"in":1}`)
	a.runUntilWaiting()
	raw, err := a.x.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	b := newDriver(t, fmt.Sprintf(invokeTask, `"TimeoutSeconds":30,"ResultPath":"$.r",`, ""), rules())
	if err := b.x.UnmarshalJSON(raw); err != nil {
		t.Fatal(err)
	}
	b.history, b.now = append(b.history, a.history...), a.now
	for _, tm := range b.x.Timers() {
		b.timers[tm.Timer] = tm.At
	}
	b.pending = b.x.InFlight()
	if want := a.pending; len(b.pending) != 1 || b.pending[0].Task != want[0].Task || b.pending[0].Timeout != 30*time.Second ||
		b.pending[0].Function != want[0].Function || !equal(b.pending[0].Payload, want[0].Payload) {
		t.Fatalf("InFlight after load = %#v, want %#v", b.pending, want)
	}
	oa, okA := a.runToEnd(10).(asl.Succeeded)
	ob, okB := b.runToEnd(10).(asl.Succeeded)
	if !okA || !okB || a.shape() != b.shape() || !equal(oa.Output, ob.Output) {
		t.Fatalf("after load:\n%s\nwant:\n%s", b.shape(), a.shape())
	}
}
