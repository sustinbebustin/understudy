package outbox_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// goneQueues answers QueueDoesNotExist for every queue named in gone.
func goneQueues(k *kerneltest.K, gone ...string) *queues {
	return stubQueues(k, func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
		for _, g := range gone {
			if strings.HasSuffix(in.QueueUrl, "/"+g) {
				return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
			}
		}
		return nil
	})
}

// stringAttr is the String message attribute name of m, "" when absent.
func stringAttr(m sent, name string) string {
	return deref(m.in.MessageAttributes[name].StringValue)
}

// A dead letter whose payload exceeds SQS's 256 KiB is cut to it and says
// so in IS_PAYLOAD_TRUNCATED (aws-doc monitoring-cloudwatch; scheduler.md
// 7.2, 14.5.3). The cut falls on a character boundary, so a character
// straddling the limit is left out whole. UNVERIFIED (SCH-08): the cut
// size and boundary are understudy's.
func TestSchedulerDeadLetterTruncatesALargePayload(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := goneQueues(k, "gone")
		dlq := arn("sqs", "sch-dlq")
		tmpl := strings.Repeat("x", 262_143) + "é<aws.scheduler.execution-id>"
		enqueue(k, o, toSchedulerQueue("gone", tmpl, delivery.PermanentNotRetried, 185, &dlq), source(k))
		settled(k)
		got := q.sent()
		if len(got) != 2 || got[1].queue != "sch-dlq" {
			k.Fatalf("sent %d messages, want the attempt and the dead letter", len(got))
		}
		dl := got[1]
		if body := dl.in.MessageBody; body != strings.Repeat("x", 262_143) {
			k.Errorf("dead-letter body is %d bytes ending %q, want the 262,143 bytes before the straddling character", len(body), body[max(0, len(body)-8):])
		}
		if v := stringAttr(dl, "IS_PAYLOAD_TRUNCATED"); v != "true" {
			k.Errorf("IS_PAYLOAD_TRUNCATED = %q, want true", v)
		}
	})
}

// A Scheduler event bus target may name a dead-letter queue; its dead
// letter's body is the payload the entry's Detail carried, its context
// attributes substituted, not an event envelope (scheduler.md 14.5.3).
// UNVERIFIED (SCH-08): the bus body and the universal TARGET_ARN spelling
// eventbridge:putEvents are inferred.
func TestSchedulerBusTargetDeadLetterIsThePayload(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		stubBus(k, func(int) string { return "InvalidArgument" })
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		bus := arn("events", "event-bus/default")
		dlq := arn("sqs", "sch-dlq")
		tmpl := `{"attempt":"<aws.scheduler.attempt-number>"}`
		target := delivery.EventBus{ARN: bus, Entries: []delivery.EventEntry{{Source: "my.app", DetailType: "tick", Detail: tmpl}}}
		enqueue(k, o, toSchedulerTarget(delivery.SchedulerToEvents, target, bus, tmpl, delivery.PermanentNotRetried, 185, &dlq), source(k))
		settled(k)
		got := q.sent()
		if len(got) != 1 || got[0].queue != "sch-dlq" {
			k.Fatalf("sent %+v, want one dead letter", got)
		}
		if body := got[0].in.MessageBody; body != `{"attempt":"1"}` {
			k.Errorf("dead-letter body = %q, want the payload {\"attempt\":\"1\"}", body)
		}
		if v := stringAttr(got[0], "TARGET_ARN"); v != "arn:aws:scheduler:::aws-sdk:eventbridge:putEvents" {
			k.Errorf("TARGET_ARN = %q, want arn:aws:scheduler:::aws-sdk:eventbridge:putEvents", v)
		}
		if v := stringAttr(got[0], "RETRY_ATTEMPTS"); v != "0" {
			k.Errorf("RETRY_ATTEMPTS = %q, want 0", v)
		}
	}, kerneltest.Services(eventbridgeapi.Service()))
}

// A Lambda target's dead letter names the function's own error code, the
// shape name of a restJson1 error, and the universal lambda:invoke target
// (scheduler.md 7.2, 14.5.3). UNVERIFIED (SCH-08): the spelling is
// inferred from the documented sqs:sendMessage form.
func TestSchedulerLambdaTargetDeadLetter(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		k.Stub(lambdaapi.OpInvoke, func(context.Context, *kernel.Call, *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			return nil, lambdaapi.ErrResourceNotFoundException("Function not found: tick")
		})
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		dlq := arn("sqs", "sch-dlq")
		fn := arn("lambda", "function:tick")
		target := delivery.LambdaFunction{ARN: fn, Event: []byte("{}"), Invocation: delivery.InvocationEvent}
		enqueue(k, o, toSchedulerTarget(delivery.SchedulerToLambda, target, fn, "{}", delivery.PermanentNotRetried, 185, &dlq), source(k))
		settled(k)
		got := q.sent()
		if len(got) != 1 {
			k.Fatalf("sent %d messages, want one dead letter", len(got))
		}
		for name, want := range map[string]string{
			"ERROR_CODE":    "ResourceNotFoundException",
			"ERROR_MESSAGE": "Function not found: tick",
			"TARGET_ARN":    "arn:aws:scheduler:::aws-sdk:lambda:invoke",
		} {
			if v := stringAttr(got[0], name); v != want {
				k.Errorf("%s = %q, want %q", name, v, want)
			}
		}
	})
}

// A Throttle fault rule on sqs:SendMessage is a retryable failure: the
// delivery is retried until MaximumRetryAttempts runs out, and the dead
// letter says so in EXHAUSTED_RETRY_CONDITION (aws-doc
// configuring-schedule-dlq; scheduler.md 7.1, 7.2). The rule throttles the
// two attempts, then is spent, so the dead letter goes through.
func TestSchedulerThrottleFaultExhaustsTheRetries(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		if _, rerr := k.Faults.Add(fault.Rule{
			Match:   fault.Match{Operations: []ref.OperationID{sqsapi.OpSendMessage.Descriptor().ID}},
			Phase:   fault.Request,
			Action:  fault.Throttle{},
			Trigger: fault.NextN{N: 2},
		}); rerr != nil {
			k.Fatal(rerr)
		}
		dlq := arn("sqs", "sch-dlq")
		enqueue(k, o, toSchedulerQueue("busy", "x", delivery.PermanentNotRetried, 1, &dlq), source(k))
		settled(k)
		for range 60 {
			advance(k, time.Minute)
		}
		got := q.sent()
		if len(got) != 1 || got[0].queue != "sch-dlq" {
			k.Fatalf("sent %+v, want only the dead letter: the rule throttled both attempts", got)
		}
		for name, want := range map[string]string{"EXHAUSTED_RETRY_CONDITION": "MaximumRetryAttempts", "RETRY_ATTEMPTS": "1"} {
			if v := stringAttr(got[0], name); v != want {
				k.Errorf("%s = %q, want %q", name, v, want)
			}
		}
		if v := stringAttr(got[0], "ERROR_CODE"); v == "" {
			k.Error("ERROR_CODE is empty, want the throttling error's code")
		}
	})
}

// A delivery whose next retry would come after MaximumEventAgeInSeconds
// ends there, and the dead letter names that condition (aws-doc
// configuring-schedule-dlq; scheduler.md 7.1, 7.2).
func TestSchedulerEventAgeExhaustsTheRetries(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
			if strings.HasSuffix(in.QueueUrl, "/busy") {
				return throttled("")
			}
			return nil
		})
		dlq := arn("sqs", "sch-dlq")
		req := toSchedulerQueue("busy", "x", delivery.PermanentNotRetried, 185, &dlq)
		req.Policy = delivery.EventBridgeTarget{MaxAge: time.Minute, MaxAttempts: 185}
		enqueue(k, o, req, source(k))
		settled(k)
		for range 10 {
			advance(k, time.Minute)
		}
		dead := 0
		for _, m := range q.sent() {
			if m.queue != "sch-dlq" {
				continue
			}
			dead++
			if v := stringAttr(m, "EXHAUSTED_RETRY_CONDITION"); v != "MaximumEventAgeInSeconds" {
				k.Errorf("EXHAUSTED_RETRY_CONDITION = %q, want MaximumEventAgeInSeconds", v)
			}
		}
		if dead != 1 {
			k.Errorf("dead letters = %d, want 1", dead)
		}
	})
}

// A dead-letter queue that refuses the send, or none at all, drops the
// invocation and journals why (scheduler.md 14.5.4).
func TestSchedulerDeadLetterDroppedAndJournaled(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		goneQueues(k, "gone", "gone-dlq")
		dlq := arn("sqs", "gone-dlq")
		cause := source(k)
		enqueue(k, o, toSchedulerQueue("gone", "x", delivery.PermanentNotRetried, 185, &dlq), cause)
		enqueue(k, o, toSchedulerQueue("gone", "y", delivery.PermanentNotRetried, 185, nil), cause)
		settled(k)
		events := deliveries(k)
		if len(events) != 2 {
			k.Fatalf("delivery events = %+v, want two", events)
		}
		for i, want := range []string{"the dead-letter queue " + dlq.String() + " refused it", "no dead-letter queue is set"} {
			d := detail[journal.Delivery](k, events[i])
			dropped, ok := d.Outcome.(journal.Dropped)
			if !ok || !strings.Contains(dropped.Reason, want) {
				k.Errorf("delivery %d outcome = %+v, want dropped with %q", i+1, d.Outcome, want)
			}
		}
	})
}
