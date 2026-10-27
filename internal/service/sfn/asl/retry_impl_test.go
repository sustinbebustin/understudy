package asl_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// failWith answers every invocation with the error name and cause "c".
func failWith(name string) lambdaAnswer {
	return func(inv asl.InvokeLambda, at time.Time) asl.Event {
		return asl.LambdaFailed{At: at, Task: inv.Task, Err: asl.StateError{Name: name, Cause: "c"}}
	}
}

// inferred, ASL-10
// Fractional delays are kept (BackoffRate 1.5 waits 1.5 and 2.25 s, not
// whole seconds), and FULL jitter applies after MaxDelaySeconds, so a
// capped FULL delay is the draw times the cap.
func TestRetryDelayFractionsAndJitterAfterCapASL10(t *testing.T) {
	r := asl.Retrier{IntervalSeconds: 1, MaxAttempts: 3, BackoffRate: 1.5, Jitter: asl.JitterNone}
	for k, want := range []time.Duration{time.Second, 1500 * time.Millisecond, 2250 * time.Millisecond} {
		if got := asl.RetryDelay(r, k+1, 0); got != want {
			t.Errorf("RetryDelay(BackoffRate 1.5, %d) = %v, want %v", k+1, got, want)
		}
	}
	capped := asl.Retrier{IntervalSeconds: 10, MaxAttempts: 3, BackoffRate: 2, MaxDelaySeconds: 4, Jitter: asl.JitterFull}
	if got := asl.RetryDelay(capped, 3, 0.5); got != 2*time.Second {
		t.Errorf("RetryDelay(FULL, cap 4, jitter 0.5) = %v, want 2s (half the cap)", got)
	}
}

// inferred, ASL-10
// $$.State.RetryCount counts the retries of every retrier of the state:
// errors A then B against [{A}, {B}] give 0, 1, 2.
func TestRetryCountAcrossRetriersASL10(t *testing.T) {
	def := `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke",
		"Parameters":{"FunctionName":"` + functionArn + `","Payload":{"n.$":"$$.State.RetryCount"}},
		"Retry":[{"ErrorEquals":["A"],"MaxAttempts":1},{"ErrorEquals":["B"],"MaxAttempts":1}],"End":true}}}`
	d := newDriver(t, def, rules())
	errs := []string{"A", "B", "C"}
	d.answer = func(inv asl.InvokeLambda, at time.Time) asl.Event {
		return asl.LambdaFailed{At: at, Task: inv.Task, Err: asl.StateError{Name: errs[len(d.invoked)-1], Cause: "c"}}
	}
	d.start(`{}`)
	failedWith(t, d.runToEnd(50), "C", func(c string) bool { return c == "c" })
	for i, inv := range d.invoked {
		if want := decode(t, fmt.Sprintf(`{"n":%d}`, i)); !equal(inv.Payload, want) {
			t.Errorf("attempt %d payload = %s, want %s", i+1, encode(inv.Payload), encode(want))
		}
	}
}

// inferred, ASL-10
// FULL jitter draws come from the execution's seeded stream: the same seed
// waits the same, every wait lies within the formula's bound, and a
// snapshot in between does not change the stream.
func TestFullJitterIsSeededASL10(t *testing.T) {
	def := fmt.Sprintf(invokeTask, `"Retry":[{"ErrorEquals":["States.ALL"],"IntervalSeconds":8,"MaxAttempts":3,"JitterStrategy":"FULL"}],`, "")
	run := func(seed uint64, persist bool) []time.Time {
		d := newDriver(t, def, rules())
		d.answer = failWith("E")
		in := document.Map{}
		d.apply(asl.Start{At: t0, Input: in, Context: context(in), Jitter: seed})
		for range 50 {
			if persist {
				b, err := json.Marshal(d.x)
				if err != nil {
					t.Fatal(err)
				}
				var y asl.Execution
				if err := json.Unmarshal(b, &y); err != nil {
					t.Fatal(err)
				}
				d.x = y
			}
			if !d.next() {
				break
			}
		}
		return d.invokes
	}
	a, b, c := run(7, false), run(7, true), run(8, false)
	if len(a) != 4 {
		t.Fatalf("invocations = %d, want 4", len(a))
	}
	for i := range a {
		if !a[i].Equal(b[i]) {
			t.Errorf("attempt %d at %v after a snapshot, %v without", i+1, b[i].Sub(t0), a[i].Sub(t0))
		}
	}
	for i := 1; i < len(a); i++ {
		if wait, bound := a[i].Sub(a[i-1]), 8*time.Second<<(i-1); wait < 0 || wait > bound {
			t.Errorf("retry %d waited %v, want within [0, %v]", i, wait, bound)
		}
	}
	if a[3].Equal(c[3]) {
		t.Errorf("seeds 7 and 8 retried at the same instants %v", a)
	}
}

// inferred, ASL-13
// A caught error without a cause, a task's States.Timeout, gives an error
// output with Error alone.
func TestCatchOutputWithoutCauseASL13(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(legacyTask, `"TimeoutSeconds":5,"Catch":[{"ErrorEquals":["States.Timeout"],"Next":"Done"}],`, doneState), rules())
	d.start(`{}`)
	d.runUntilWaiting()
	d.pending = nil
	o := d.runToEnd(10)
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `{"Error":"States.Timeout"}`)) {
		t.Errorf("outcome = %#v, want the error output without Cause", o)
	}
}

// A catcher's ResultPath places the error output into the state's raw
// input (stepfunctions-asl.md 8.4), and the first catcher naming the error
// is the one taken.
func TestCatcherResultPathAndOrder(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(invokeTask,
		`"Catch":[{"ErrorEquals":["Other"],"Next":"Done"},{"ErrorEquals":["E"],"ResultPath":"$.err","Next":"Second"},{"ErrorEquals":["States.ALL"],"Next":"Done"}],`,
		doneState+`,"Second":{"Type":"Pass","End":true}`), rules())
	d.answer = failWith("E")
	d.start(`{"in":1}`)
	o := d.runToEnd(10)
	d.wantShape("test_states_errors.snapshot.json:L65-L93",
		"ExecutionStarted 1<-0", "TaskStateEntered 2<-0", "TaskScheduled 3<-2", "TaskStarted 4<-3",
		"TaskFailed 5<-4", "TaskStateExited 6<-5", "PassStateEntered 7<-6", "PassStateExited 8<-7", "ExecutionSucceeded 9<-8")
	if name := detailString(d.event("PassStateEntered"), "name"); name != "Second" {
		t.Errorf("caught into %q, want Second (the first catcher naming E)", name)
	}
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `{"in":1,"err":{"Error":"E","Cause":"c"}}`)) {
		t.Errorf("outcome = %#v, want the input with the error output at $.err", o)
	}
}

// States.TaskFailed matches every error but States.Timeout (aws-doc
// dg/concepts-error-handling); States.ALL catches States.DataLimitExceeded
// from a task result (aws-recorded localstack@8b9a79f
// v2/error_handling/test_states_errors.snapshot.json:L210-L238).
func TestCatchWildcards(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(invokeTask, `"Catch":[{"ErrorEquals":["States.TaskFailed"],"Next":"Done"}],`, doneState), rules())
	d.answer = failWith("Lambda.Unknown")
	d.start(`{}`)
	if _, ok := d.runToEnd(10).(asl.Succeeded); !ok {
		t.Errorf("States.TaskFailed did not catch Lambda.Unknown:\n%s", d.shape())
	}
	big := newDriver(t, fmt.Sprintf(invokeTask, `"Catch":[{"ErrorEquals":["States.ALL"],"Next":"Done"}],`, doneState), rules())
	big.answer = succeedWith(document.String(string(make([]byte, 262_145))))
	big.start(`{}`)
	o := big.runToEnd(10)
	if s, ok := o.(asl.Succeeded); !ok || detailString(big.event("TaskFailed"), "error") != "States.DataLimitExceeded" ||
		at(s.Output, "Error") != document.String("States.DataLimitExceeded") {
		t.Errorf("outcome = %#v, want States.ALL to catch States.DataLimitExceeded:\n%s", o, big.shape())
	}
}

// States.Runtime is never retried, even by a States.ALL retrier
// (stepfunctions-asl.md 8.1, finding 1).
func TestRuntimeErrorIsNotRetried(t *testing.T) {
	d := newDriver(t, fmt.Sprintf(invokeTask, `"ResultSelector":{"x.$":"$.nope"},"Retry":[{"ErrorEquals":["States.ALL"]}],`, ""), rules())
	d.answer = succeedWith(decode(t, `{"a":1}`))
	d.start(`{}`)
	failedWith(t, d.runToEnd(10), "States.Runtime", func(string) bool { return true })
	if len(d.invoked) != 1 {
		t.Errorf("invocations = %d, want 1", len(d.invoked))
	}
}

// A catchable error raised before the task is scheduled, an intrinsic
// failure in Parameters, goes to Catch: TaskStateExited points at
// TaskStateEntered (11.2 rule 4).
func TestCatchBeforeScheduling(t *testing.T) {
	def := `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke",
		"Parameters":{"FunctionName":"` + functionArn + `","Payload.$":"States.StringToJson('{')"},
		"Catch":[{"ErrorEquals":["States.ALL"],"Next":"Done"}],"End":true}` + doneState + `}}`
	d := newDriver(t, def, rules())
	d.start(`{}`)
	o := d.runToEnd(10)
	d.wantShape("11.2 rule 4", "ExecutionStarted 1<-0", "TaskStateEntered 2<-0", "TaskStateExited 3<-2",
		"PassStateEntered 4<-3", "PassStateExited 5<-4", "ExecutionSucceeded 6<-5")
	if s, ok := o.(asl.Succeeded); !ok || at(s.Output, "Error") != document.String(rules().IntrinsicFailure.Name) {
		t.Errorf("outcome = %#v, want the intrinsic failure's error output", o)
	}
}

// A retry waiting for its delay survives a snapshot: the saved execution
// re-arms the retry and makes the same attempts as one never saved.
func TestRetryWaitPersists(t *testing.T) {
	def := fmt.Sprintf(invokeTask, `"Retry":[{"ErrorEquals":["E"],"IntervalSeconds":2,"MaxAttempts":2}],"Catch":[{"ErrorEquals":["States.ALL"],"Next":"Done"}],`, doneState)
	d := newDriver(t, def, rules())
	d.answer = failWith("E")
	d.start(`{}`)
	d.next() // the first attempt fails; the retry timer is armed
	if len(d.x.Timers()) != 2 {
		t.Fatalf("timers = %v, want the deadline and the retry", d.x.Timers())
	}
	b, err := json.Marshal(d.x)
	if err != nil {
		t.Fatal(err)
	}
	var y asl.Execution
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	if !sameExecution(t, y, d.x) {
		t.Fatal("the round trip changed the execution")
	}
	d.x = y
	o := d.runToEnd(20)
	if len(d.invoked) != 3 || !d.invokes[1].Equal(t0.Add(2*time.Second)) || !d.invokes[2].Equal(t0.Add(6*time.Second)) {
		t.Errorf("attempts at %v, want +0s, +2s, +6s", d.invokes)
	}
	if _, ok := o.(asl.Succeeded); !ok {
		t.Errorf("outcome = %#v, want caught after the retries", o)
	}
}

// Spec T12: over generated retriers and error sequences, a state retries
// error e exactly while the first retrier naming e has attempts left, each
// wait is the 8.3 formula for that retrier's count, and an exhausted or
// unmatched error is caught.
func TestRetrierSequenceProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		names := []string{"A", "B", "C"}
		n := rapid.IntRange(1, 3).Draw(rt, "retriers")
		retriers := make([]asl.Retrier, 0, n)
		defs := make([]string, 0, n)
		for i := range n {
			r := asl.Retrier{
				ErrorEquals:     []string{names[i]},
				MaxAttempts:     rapid.IntRange(0, 3).Draw(rt, "max"),
				IntervalSeconds: rapid.IntRange(1, 5).Draw(rt, "interval"),
				BackoffRate:     2,
				Jitter:          asl.JitterNone,
			}
			retriers = append(retriers, r)
			defs = append(defs, fmt.Sprintf(`{"ErrorEquals":[%q],"MaxAttempts":%d,"IntervalSeconds":%d}`, names[i], r.MaxAttempts, r.IntervalSeconds))
		}
		seq := rapid.SliceOfN(rapid.SampledFrom(names), 1, 12).Draw(rt, "errors")
		d := newDriver(rt, fmt.Sprintf(invokeTask, `"Retry":[`+strings.Join(defs, ",")+`],"Catch":[{"ErrorEquals":["States.ALL"],"Next":"Done"}],`, doneState), rules())
		d.answer = func(inv asl.InvokeLambda, at time.Time) asl.Event {
			return asl.LambdaFailed{At: at, Task: inv.Task, Err: asl.StateError{Name: seq[min(len(d.invoked), len(seq))-1], Cause: "c"}}
		}
		d.start(`{}`)
		d.runToEnd(100)

		// The model: walk the sequence the way 8.2 describes.
		counts := make([]int, len(retriers))
		want := []time.Duration{0}
		now := time.Duration(0)
		caught := ""
		for i := 0; caught == ""; i++ {
			e := seq[min(i, len(seq)-1)]
			r := slices.IndexFunc(retriers, func(r asl.Retrier) bool { return r.ErrorEquals[0] == e })
			if r < 0 || counts[r] >= retriers[r].MaxAttempts {
				caught = e
				break
			}
			counts[r]++
			now += time.Duration(retriers[r].IntervalSeconds) * time.Second << (counts[r] - 1)
			want = append(want, now)
		}
		if len(d.invokes) != len(want) {
			rt.Fatalf("invocations = %d, want %d; history:\n%s", len(d.invokes), len(want), d.shape())
		}
		for i := range want {
			if !d.invokes[i].Equal(t0.Add(want[i])) {
				rt.Fatalf("attempt %d at +%v, want +%v", i+1, d.invokes[i].Sub(t0), want[i])
			}
		}
		o, _ := d.x.Outcome()
		if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(rt, `{"Error":"`+caught+`","Cause":"c"}`)) {
			rt.Fatalf("outcome = %#v, want the catcher's output for %s", o, caught)
		}
	})
}
