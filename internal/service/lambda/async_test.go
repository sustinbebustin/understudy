package lambda_test

import (
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// fnEvent is an Event Invoke of fn with payload.
func fnEvent(payload string) *lambdaapi.InvokeInput {
	return &lambdaapi.InvokeInput{FunctionName: "fn", InvocationType: new(lambdaapi.InvocationTypeEvent), Payload: []byte(payload)}
}

// advance moves the emulator clock and waits for the work it starts.
func (h *execHarness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
	h.settle()
}

func TestEventInvokeAnswers202AndRunsTheFunctionFromTheQueue(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		out := h.invoke(fnEvent(`{"n":1}`))
		if out.StatusCode != http.StatusAccepted || out.Payload != nil || out.FunctionError != nil {
			t.Fatalf("Event Invoke = %d %q %v, want 202 with no body", out.StatusCode, out.Payload, out.FunctionError)
		}
		h.settle()
		calls := s.invoked()
		invoke := h.Journal.Query(journal.Filter{Kind: journal.KindAPICall, Operation: lambdaapi.OpInvoke.Descriptor().ID}).Events
		if len(calls) != 1 || string(calls[0].Payload) != `{"n":1}` || len(invoke) != 1 {
			t.Fatalf("the function received %d invocations, want 1 with the event", len(calls))
		}
		api, ok := invoke[0].Detail.(journal.APICall)
		if !ok || calls[0].RequestID != ids.LambdaRequestID(api.RequestID) {
			t.Errorf("the attempt runs as request %s, want the Invoke call's %s", calls[0].RequestID, api.RequestID)
		}
		if inv := h.invocations(); len(inv) != 1 || inv[0].Cause.Parent != invoke[0].ID || invocationOf(t, inv[0]).Outcome != (journal.InvocationOK{}) {
			t.Errorf("Invocation events = %+v, want one OK, caused by the Invoke call", inv)
		}
	})
}

// A function error is retried twice, one minute and then two minutes of
// emulator time later, under the Invoke's request id with a new invocation
// id each time (lambda-api.md 8.6).
func TestAsyncFunctionErrorsRetryAfterOneAndThenTwoMinutes(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		for _, step := range []struct {
			advance time.Duration
			want    int
		}{
			{59 * time.Second, 1},
			{time.Second, 2},
			{119 * time.Second, 2},
			{time.Second, 3},
			{6 * time.Hour, 3},
		} {
			h.advance(step.advance)
			if n := len(s.invoked()); n != step.want {
				t.Fatalf("after %s more: %d attempts, want %d", step.advance, n, step.want)
			}
		}
		calls := s.invoked()
		if calls[0].RequestID != calls[2].RequestID || calls[0].InvocationID == calls[1].InvocationID {
			t.Errorf("attempts = %+v, want one request id and an invocation id per attempt", calls)
		}
	})
}

func TestAsyncRetriesFollowTheEventInvokeConfig(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		call(h.harness, lambdaapi.OpPutFunctionEventInvokeConfig, &lambdaapi.PutFunctionEventInvokeConfigInput{FunctionName: "fn", MaximumRetryAttempts: new(int32(0))})
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		h.advance(10 * time.Minute)
		if n := len(s.invoked()); n != 1 {
			t.Errorf("attempts with MaximumRetryAttempts 0 = %d, want 1", n)
		}
	})
}

// A throttled event goes back to the queue with a backoff of 1, 2, 4
// seconds and runs once concurrency frees up.
func TestThrottledAsyncEventIsRequeuedWithBackoff(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		call(h.harness, lambdaapi.OpPutFunctionConcurrency, &lambdaapi.PutFunctionConcurrencyInput{FunctionName: "fn", ReservedConcurrentExecutions: 1})
		done := h.invokeInBackground(invokeInput("fn", `{"action":"block"}`))
		synctest.Wait()
		h.invoke(fnEvent(`{"n":1}`))
		h.settle()
		h.advance(time.Second)
		h.advance(2 * time.Second)
		h.advance(3 * time.Second)
		throttled := 0
		for _, e := range h.invocations() {
			if invocationOf(t, e).Outcome == (journal.Throttled{}) {
				throttled++
			}
		}
		if throttled != 3 || len(s.invoked()) != 1 {
			t.Fatalf("throttled attempts = %d with %d invocations run, want 3 at 0, 1 and 3 s and only the blocking one run", throttled, len(s.invoked()))
		}
		s.unblock()
		h.wait(done)
		h.advance(time.Second)
		if calls := s.invoked(); len(calls) != 2 || string(calls[1].Payload) != `{"n":1}` {
			t.Errorf("after the backoff of 4 s: %d invocations, want the queued event run", len(calls))
		}
	})
}

func TestZeroReservedConcurrencyDiscardsAsyncEvents(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		call(h.harness, lambdaapi.OpPutFunctionConcurrency, &lambdaapi.PutFunctionConcurrencyInput{FunctionName: "fn", ReservedConcurrentExecutions: 0})
		if out := h.invoke(fnEvent(`{}`)); out.StatusCode != http.StatusAccepted {
			t.Fatalf("Event Invoke = %d, want 202: the queue accepts the event", out.StatusCode)
		}
		h.settle()
		if n := h.Kernel.Timers().Scheduler().Len(); n != 0 {
			t.Errorf("%d timers armed after the discard, want none: no retries", n)
		}
		h.advance(time.Hour)
		if n := len(s.invoked()); n != 0 {
			t.Errorf("invocations = %d, want none", n)
		}
	})
}

// The queue is saved and loaded like the services' state: a retry pending
// when the snapshot was cut runs on schedule after the load.
func TestAsyncQueueSurvivesASnapshotLoad(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		q := h.x.AsyncQueue()
		release := h.Kernel.Gate().Exclusive()
		sections, err := q.Export()
		release()
		if err != nil {
			t.Fatal(err)
		}
		// A load drains the Execution and clears every timer, then imports
		// and rearms.
		if err := h.x.Drain(h.Context(), kernel.ResetScope{}); err != nil {
			t.Fatal(err)
		}
		h.Kernel.Timers().Scheduler().Clear()
		release = h.Kernel.Gate().Exclusive()
		err = q.Import(sections)
		release()
		if err != nil {
			t.Fatal(err)
		}
		q.Rearm(h.Clock.Now())
		if res := q.Resources(h.Scope); len(res) != 1 || res[0].Summary != "1 queued asynchronous invocation" {
			t.Errorf("resources after the load = %+v, want fn with one queued invocation", res)
		}
		h.advance(59 * time.Second)
		if n := len(s.invoked()); n != 1 {
			t.Fatalf("attempts 59 s after the first = %d, want 1", n)
		}
		h.advance(time.Second)
		if n := len(s.invoked()); n != 2 {
			t.Errorf("attempts 60 s after the first = %d, want the loaded retry run", n)
		}
	})
}

// An event loaded after its maximum age (six hours by default) has passed
// is discarded at its next attempt rather than run (lambda-api.md 8.6).
func TestAsyncEventPastItsMaximumAgeIsDiscardedAfterALoad(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		q := h.x.AsyncQueue()
		release := h.Kernel.Gate().Exclusive()
		sections, err := q.Export()
		release()
		if err != nil {
			t.Fatal(err)
		}
		if err := h.x.Drain(h.Context(), kernel.ResetScope{}); err != nil {
			t.Fatal(err)
		}
		h.Kernel.Timers().Scheduler().Clear()
		h.advance(7 * time.Hour)
		release = h.Kernel.Gate().Exclusive()
		err = q.Import(sections)
		release()
		if err != nil {
			t.Fatal(err)
		}
		q.Rearm(h.Clock.Now())
		h.settle()
		h.advance(time.Hour)
		if n := len(s.invoked()); n != 1 {
			t.Errorf("attempts = %d, want the first only: the loaded event was past its maximum age", n)
		}
		if res := q.Resources(h.Scope); len(res) != 0 {
			t.Errorf("resources after the discard = %+v, want none queued", res)
		}
	})
}

func TestDrainEmptiesTheAsyncQueue(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		if err := h.x.Drain(h.Context(), kernel.ResetScope{}); err != nil {
			t.Fatal(err)
		}
		h.advance(time.Hour)
		if n := len(s.invoked()); n != 1 {
			t.Errorf("attempts = %d, want the first only: the drain dropped the queued retry", n)
		}
	})
}
