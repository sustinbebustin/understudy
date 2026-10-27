package lambda_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/kernel"
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
