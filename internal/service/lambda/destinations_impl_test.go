package lambda_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// A dead-letter topic gets the event as sent with the same attributes a
// dead-letter queue gets, as SNS message attributes: the guide says the
// event goes "as-is, with additional information in attributes" to a queue
// or topic alike (lambda-destinations-layers.md 5.2; the SNS form is
// inferred, LDL-05).
func TestDeadLetterTopicReceivesTheEventAsSent(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		in := zipFunction(h, "fn")
		in.DeadLetterConfig = &lambdaapi.DeadLetterConfig{TargetArn: new(alertsTopic)}
		call(h.harness, lambdaapi.OpCreateFunction, in)
		call(h.harness, lambdaapi.OpPutFunctionEventInvokeConfig, &lambdaapi.PutFunctionEventInvokeConfigInput{FunctionName: "fn", MaximumRetryAttempts: new(int32(0))})
		const event = `{"action": "fail"}`
		h.invoke(fnEvent(event))
		h.settle()
		d := h.onlyDelivery()
		h.fromFn(d, delivery.DeadLetter)
		topic, ok := d.Request.Target.(delivery.SNSTopic)
		if d.Request.Mechanism != delivery.LambdaToSNS || !ok || topic.ARN.String() != alertsTopic || topic.Message != event || topic.Subject != "" {
			t.Fatalf("delivery = %s %#v, want lambda-to-sns of the event as sent to %s", d.Request.Mechanism, d.Request.Target, alertsTopic)
		}
		want := map[string]string{"ErrorCode": "Number 200", "ErrorMessage": "String boom", "RequestID": "String " + string(s.invoked()[0].RequestID)}
		if got := deadLetterAttributes(topic.Attributes); !equalMaps(got, want) {
			t.Errorf("dead letter attributes = %v, want %v", got, want)
		}
	})
}

// A queued event keeps the Invoke's trace header across a snapshot, so the
// SQS record sent after the load still carries it as AWSTraceHeader
// (lambda-destinations-layers.md 4.1, 11.3.5).
func TestQueuedEventKeepsItsTraceHeaderAcrossASnapshot(t *testing.T) {
	const trace = "Root=1-5759e988-bd862e3fe1be46a994272793;Sampled=1"
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createFunction("fn")
		h.configure("", 1, "", failureQueue)
		h.invokeWithTrace(fnEvent(`{"action":"fail"}`), trace)
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
		release = h.Kernel.Gate().Exclusive()
		err = q.Import(sections)
		release()
		if err != nil {
			t.Fatal(err)
		}
		q.Rearm(h.Clock.Now())
		h.x.RearmPollers()
		h.advance(time.Minute)
		if got := queueTarget(t, h.onlyDelivery(), failureQueue); got.TraceHeader != trace {
			t.Errorf("TraceHeader after the load = %q, want %q", got.TraceHeader, trace)
		}
	})
}

// With neither a destination nor a dead-letter queue, a failed event
// delivers nothing: Lambda discards it (lambda-destinations-layers.md 5.3).
func TestFailedEventWithoutDestinationsDeliversNothing(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		h.createFunction("fn")
		call(h.harness, lambdaapi.OpPutFunctionEventInvokeConfig, &lambdaapi.PutFunctionEventInvokeConfigInput{FunctionName: "fn", MaximumRetryAttempts: new(int32(0))})
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		if got := h.sink.lambda(); len(got) != 0 {
			h.Errorf("deliveries = %+v, want none", got)
		}
	})
}

// An event whose function is deleted while it waits for a retry is dropped
// with no record and a journal entry naming its request id
// (lambda-destinations-layers.md 11.3.4).
func TestFunctionDeletedWhileQueuedIsJournaled(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createWithDLQ()
		h.configure("", 2, successQueue, failureQueue)
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		request := string(s.invoked()[0].RequestID)
		call(h.harness, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "fn"})
		h.advance(10 * time.Minute)
		var found []journal.Advisory
		var causes []ref.Cause
		for _, e := range h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory, Limit: 10}).Events {
			if a, ok := e.Detail.(journal.Advisory); ok && a.Kind == journal.AdvisoryAsyncEventAbandoned {
				found = append(found, a)
				causes = append(causes, e.Cause)
			}
		}
		if len(found) != 1 || found[0].Resource.String() != fnUnqualified || !strings.Contains(found[0].Text, request) {
			t.Fatalf("async-event-abandoned advisories = %+v, want one on %s naming request %s", found, fnUnqualified, request)
		}
		if want := h.invokeCause(); causes[0] != want {
			t.Errorf("the advisory's cause = %+v, want the Invoke's %+v", causes[0], want)
		}
	})
}

// invokeCause is the cause of what the one Invoke call in the journal
// caused: its event, in its trace.
func (h *destHarness) invokeCause() ref.Cause {
	h.Helper()
	invoke := h.Journal.Query(journal.Filter{Kind: journal.KindAPICall, Operation: lambdaapi.OpInvoke.Descriptor().ID}).Events
	if len(invoke) != 1 {
		h.Fatalf("journal holds %d Invoke calls, want 1", len(invoke))
	}
	return ref.Cause{Parent: invoke[0].ID, Trace: invoke[0].Cause.Trace}
}

// A failed event's record and dead letter continue the Invoke's trace, as
// its attempts do (ARCHITECTURE.md 10: state carries causes across time),
// not the trace the retry timer's TimerFired event roots.
func TestRecordAndDeadLetterStayInTheInvokesTrace(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createWithDLQ()
		h.configure("", 1, "", failureQueue)
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		h.advance(10 * time.Minute)
		want := h.invokeCause()
		got := h.sink.lambda()
		if len(got) != 2 {
			t.Fatalf("deliveries = %+v, want a record and a dead letter", got)
		}
		for _, d := range got {
			if d.Cause != want {
				t.Errorf("%s delivery cause = %+v, want the Invoke's %+v", d.Request.Mechanism, d.Cause, want)
			}
		}
	})
}

// A dead-letter target whose account understudy cannot hold passes
// DeadLetterConfig.TargetArn's pattern (lambda-destinations-layers.md 5.1)
// but cannot be delivered to: it is left out, and the on-failure record of
// the same event still goes. Configuration refuses such a target, as no
// queue can exist there, so it arrives here the way it still can: in a
// snapshot written before that check.
func TestUnservedDeadLetterTargetLeavesTheRecordGoing(t *testing.T) {
	s := newScript()
	runDestinations(t, s.handle, func(h *destHarness) {
		t := h.T
		h.createWithDLQ()
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		for i := range sections {
			sections[i].Data = bytes.ReplaceAll(sections[i].Data, []byte(dlqA), []byte("arn:aws:sqs:us-east-1:12345:dlq"))
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		h.configure("", 0, "", failureQueue)
		h.invoke(fnEvent(`{"action":"fail"}`))
		h.settle()
		queueTarget(t, h.onlyDelivery(), failureQueue)
	})
}
