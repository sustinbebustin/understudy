package outbox_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// The schedule, its execution role and the session of one invocation. The
// role id and session name have the forms of the dead-letter example's
// SenderId, AROA2DZE3W4CTL5ZR7EIN:ff00212d8c453aaaae644bc6846d4723 (aws-doc
// https://docs.aws.amazon.com/scheduler/latest/UserGuide/configuring-schedule-dlq.html;
// scheduler.md 7.2, 9).
const (
	schedRoleID  = "AROA2DZE3W4CTL5ZR7EIN"
	schedSession = "ff00212d8c453aaaae644bc6846d4723"
	schedExecID  = "ad06616e51cdf74a"
)

func schedARN() aws.ARN { return arn("scheduler", "schedule/default/tick") }

func schedRole() aws.ARN {
	return aws.ARN{Partition: "aws", Service: "iam", Account: "000000000000", Resource: "role/sch-role"}
}

var schedScheduled = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// toSchedulerTarget is a Scheduler delivery of one invocation whose Input
// is tmpl, as the fire write hands it over; targetARN
// is the target's ARN as configured.
func toSchedulerTarget(mech delivery.Mechanism, target delivery.Target, targetARN aws.ARN, tmpl string, onPermanent delivery.PermanentErrors, retries int, dlq *aws.ARN) delivery.Request {
	return delivery.Request{
		Source:    schedARN(),
		Mechanism: mech,
		Target:    target,
		Policy:    delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: retries},
		DLQ:       dlq,
		As:        delivery.ExecutionRole{Role: schedRole(), RoleID: schedRoleID, Session: schedSession},
		Invocation: &delivery.SchedulerInvocation{
			ScheduleARN: schedARN(),
			TargetARN:   targetARN.String(),
			Scheduled:   schedScheduled,
			ExecutionID: schedExecID,
			Template:    tmpl,
			OnPermanent: onPermanent,
		},
	}
}

// toSchedulerQueue is a Scheduler delivery to the standard queue name.
func toSchedulerQueue(name, tmpl string, onPermanent delivery.PermanentErrors, retries int, dlq *aws.ARN) delivery.Request {
	q := arn("sqs", name)
	return toSchedulerTarget(delivery.SchedulerToSQS, delivery.SQSQueue{ARN: q, Body: tmpl}, q, tmpl, onPermanent, retries, dlq)
}

// wantRoleSession fails unless p is a session of the schedule's execution
// role with the invocation's session name: Scheduler calls targets as the
// assumed execution role, never as a service principal (aws-doc
// https://docs.aws.amazon.com/scheduler/latest/UserGuide/setting-up.html;
// scheduler.md 1 item 1, 9, 14.4.4).
func wantRoleSession(k *kerneltest.K, what string, p identity.Principal) {
	k.Helper()
	r, ok := p.(identity.AssumedRole)
	if !ok || r.Account != aws.DefaultAccount() || r.Role.Name != "sch-role" || r.Role.ID != schedRoleID || r.Session != schedSession {
		k.Errorf("%s ran as %#v, want a session %s of role sch-role (%s)", what, p, schedSession, schedRoleID)
	}
}

var wantSchedulerOrigin ref.Origin = ref.Internal{Via: ref.SchedulerTarget}

// noAdvisories fails on any advisory: a role-session delivery needs no
// resource-policy grant on its target, so none is advised (scheduler.md 1
// item 1, 14.4.4).
func noAdvisories(k *kerneltest.K) {
	k.Helper()
	if page := k.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}); len(page.Events) != 0 {
		k.Errorf("advisories = %+v, want none for a role-session delivery", page.Events)
	}
}

// A Scheduler SQS delivery runs as the execution role's session, and each
// attempt carries its own <aws.scheduler.attempt-number>: "A counter that
// identifies the attempt number for the current invocation", 1 first
// (aws-doc
// https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-context-attributes.html;
// scheduler.md 6.3, 14.4.2). A throttled first attempt is retried.
func TestSchedulerDeliveryRendersEachAttemptAsTheRole(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(n int, _ *sqsapi.SendMessageInput) *awserr.Error {
			if n == 0 {
				return throttled("")
			}
			return nil
		})
		tmpl := "hello <aws.scheduler.attempt-number>"
		enqueue(k, o, toSchedulerQueue("tracer", tmpl, delivery.PermanentNotRetried, 185, nil), source(k))
		settled(k)
		for range 10 {
			advance(k, time.Minute)
		}
		got := q.sent()
		if len(got) != 2 {
			k.Fatalf("sent %d messages, want the throttled attempt and its retry", len(got))
		}
		for i, s := range got {
			if want := []string{"hello 1", "hello 2"}[i]; s.queue != "tracer" || s.in.MessageBody != want {
				k.Errorf("attempt %d sent %q to %s, want %q to tracer", i+1, s.in.MessageBody, s.queue, want)
			}
			wantRoleSession(k, "SendMessage", s.principal)
			if s.origin != wantSchedulerOrigin {
				k.Errorf("attempt %d origin = %v, want %v", i+1, s.origin, wantSchedulerOrigin)
			}
		}
		noAdvisories(k)
	})
}

// A FIFO queue target is sent SqsParameters.MessageGroupId and no
// deduplication id: the queue must have content-based deduplication
// (aws-doc https://docs.aws.amazon.com/scheduler/latest/APIReference/API_SqsParameters.html;
// scheduler.md 6.4).
func TestSchedulerFIFOQueueGetsTheGroup(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		target := delivery.SQSQueue{ARN: arn("sqs", "orders.fifo"), Body: "x", GroupID: "g1"}
		enqueue(k, o, toSchedulerTarget(delivery.SchedulerToSQS, target, target.ARN, "x", delivery.PermanentNotRetried, 185, nil), source(k))
		settled(k)
		got := q.sent()
		if len(got) != 1 {
			k.Fatalf("sent %d messages, want 1", len(got))
		}
		if in := got[0].in; deref(in.MessageGroupId) != "g1" || in.MessageDeduplicationId != nil || in.MessageBody != "x" {
			k.Errorf("FIFO send = group %v dedup %v body %q, want group g1, no dedup id, body x", in.MessageGroupId, in.MessageDeduplicationId, in.MessageBody)
		}
	})
}

// wantDeadLetterAttributes checks the String attributes every Scheduler
// dead letter carries (aws-doc configuring-schedule-dlq; SCH-08, recorded
// 2026-11-03; scheduler.md 7.2): CONTAINS_INVALID_PAYLOAD false and the
// eight of the doc but EXHAUSTED_RETRY_CONDITION, which "will be present
// if the error from the target API is a retryable error, and not a
// permanent error", with TARGET_ARN the target as configured.
func wantDeadLetterAttributes(k *kerneltest.K, attrs map[string]sqsapi.MessageAttributeValue, errorCode string, target aws.ARN) {
	k.Helper()
	for _, name := range []string{
		"CONTAINS_INVALID_PAYLOAD", "ERROR_CODE", "ERROR_MESSAGE", "EXECUTION_ID", "IS_PAYLOAD_TRUNCATED", "RETRY_ATTEMPTS", "SCHEDULED_TIME", "SCHEDULE_ARN", "TARGET_ARN",
	} {
		a, ok := attrs[name]
		if !ok || a.DataType != "String" || a.StringValue == nil {
			k.Errorf("dead-letter attribute %s = %+v (present %v), want a String", name, a, ok)
		}
	}
	str := func(name string) string { return deref(attrs[name].StringValue) }
	if got := str("ERROR_CODE"); got != errorCode {
		// "the error code that EventBridge Scheduler receives from the
		// target's service API", AWS.SimpleQueueService.NonExistentQueue
		// in the example (aws-doc configuring-schedule-dlq).
		k.Errorf("ERROR_CODE = %q, want %q", got, errorCode)
	}
	if got := str("EXECUTION_ID"); got != schedExecID {
		k.Errorf("EXECUTION_ID = %q, want the invocation's %q", got, schedExecID)
	}
	if got := str("SCHEDULED_TIME"); got != "2000-01-01T00:00:00Z" {
		// "2022-05-14T01:12:00Z" in the example: RFC 3339 seconds in UTC.
		k.Errorf("SCHEDULED_TIME = %q, want 2000-01-01T00:00:00Z", got)
	}
	if got := str("SCHEDULE_ARN"); got != schedARN().String() {
		k.Errorf("SCHEDULE_ARN = %q, want %q", got, schedARN())
	}
	if got := str("IS_PAYLOAD_TRUNCATED"); got != "false" {
		k.Errorf("IS_PAYLOAD_TRUNCATED = %q, want false for a small payload", got)
	}
	if got := str("CONTAINS_INVALID_PAYLOAD"); got != "false" {
		k.Errorf("CONTAINS_INVALID_PAYLOAD = %q, want false", got)
	}
	if got := str("TARGET_ARN"); got != target.String() {
		k.Errorf("TARGET_ARN = %q, want the target as configured, %s", got, target)
	}
}

// sqsDeadLetterBody is a templated SQS target's dead-letter body: the
// SendMessage request Scheduler built, members in name order, the group
// only for a FIFO queue (SCH-08 08-recv-dlq-*, recorded 2026-11-03;
// scheduler.md 7.2).
func sqsDeadLetterBody(payload, group string, queue aws.ARN) string {
	g := ""
	if group != "" {
		g = `"MessageGroupId":"` + group + `",`
	}
	return `{"MessageBody":"` + payload + `",` + g + `"QueueUrl":"https://sqs.` + queue.Region + `.amazonaws.com/` + queue.Account + `/` + queue.Resource + `"}`
}

// A missing target queue is a client-side error: under
// scheduler.retry.permanent-errors not-retried it is dead-lettered at once,
// without retries, carrying the SQS error code, RETRY_ATTEMPTS 0 and no
// EXHAUSTED_RETRY_CONDITION. The body is the SendMessage request built
// from the Input (SCH-08, recorded 2026-11-03), and the dead letter is sent by the
// same role session (SenderId AROA...:<session>, aws-doc
// configuring-schedule-dlq; scheduler.md 7.1, 7.2).
func TestSchedulerMissingQueueDeadLettersAtOnce(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
			if in.QueueUrl[len(in.QueueUrl)-len("/gone"):] == "/gone" {
				return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
			}
			return nil
		})
		dlq := arn("sqs", "sch-dlq")
		const input = "Hello, world!"
		t0 := k.Clock.Now()
		enqueue(k, o, toSchedulerQueue("gone", input, delivery.PermanentNotRetried, 185, &dlq), source(k))
		settled(k)

		got := q.sent()
		if len(got) != 2 || got[0].queue != "gone" || got[1].queue != "sch-dlq" {
			k.Fatalf("sent %+v, want one attempt to gone, then the dead letter", got)
		}
		dl := got[1]
		if want := sqsDeadLetterBody(input, "", arn("sqs", "gone")); dl.at != t0 || dl.in.MessageBody != want {
			k.Errorf("dead letter %q at %v, want %q at once (%v)", dl.in.MessageBody, dl.at, want, t0)
		}
		wantRoleSession(k, "dead-letter SendMessage", dl.principal)
		wantDeadLetterAttributes(k, dl.in.MessageAttributes, "AWS.SimpleQueueService.NonExistentQueue", arn("sqs", "gone"))
		if got := deref(dl.in.MessageAttributes["RETRY_ATTEMPTS"].StringValue); got != "0" {
			k.Errorf("RETRY_ATTEMPTS = %q, want 0: a permanent error is not retried", got)
		}
		if a, ok := dl.in.MessageAttributes["EXHAUSTED_RETRY_CONDITION"]; ok {
			k.Errorf("EXHAUSTED_RETRY_CONDITION = %+v, want absent for a permanent error", a)
		}
		events := deliveries(k)
		if len(events) != 1 {
			k.Fatalf("delivery events = %+v, want one", events)
		}
		if d := detail[journal.Delivery](k, events[0]); d.Outcome != (journal.DeadLettered{DLQ: dlq}) || d.From != schedARN() {
			k.Errorf("delivery = %+v, want from the schedule, dead-lettered to %v", d, dlq)
		}
	})
}

// A FIFO queue without content-based deduplication refuses the send with
// SQS InvalidParameterValue, a permanent error: dead-lettered at once with
// RETRY_ATTEMPTS 0, its body the request with MessageGroupId (SCH-08 x3,
// 08-recv-dlq-*, recorded 2026-11-03; scheduler.md 7.2).
func TestSchedulerFIFODeadLetterCarriesTheGroup(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		const refusal = "The queue should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly"
		q := stubQueues(k, func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
			if strings.HasSuffix(in.QueueUrl, ".fifo") {
				return sqsapi.ErrInvalidParameterValue(refusal)
			}
			return nil
		})
		dlq := arn("sqs", "sch-dlq")
		fifo := arn("sqs", "nodedup.fifo")
		target := delivery.SQSQueue{ARN: fifo, Body: "nodedup", GroupID: "g"}
		enqueue(k, o, toSchedulerTarget(delivery.SchedulerToSQS, target, fifo, "nodedup", delivery.PermanentNotRetried, 185, &dlq), source(k))
		settled(k)
		got := q.sent()
		if len(got) != 2 || got[1].queue != "sch-dlq" {
			k.Fatalf("sent %+v, want the attempt and the dead letter", got)
		}
		if want := sqsDeadLetterBody("nodedup", "g", fifo); got[1].in.MessageBody != want {
			k.Errorf("dead-letter body = %q, want %q", got[1].in.MessageBody, want)
		}
		wantDeadLetterAttributes(k, got[1].in.MessageAttributes, "InvalidParameterValue", fifo)
		if v := deref(got[1].in.MessageAttributes["ERROR_MESSAGE"].StringValue); v != refusal {
			k.Errorf("ERROR_MESSAGE = %q, want %q", v, refusal)
		}
	})
}

// Under scheduler.retry.permanent-errors retried, the missing queue is
// retried until MaximumRetryAttempts runs out, and the dead letter then
// names that condition (aws-doc configuring-schedule-dlq; scheduler.md 7.1).
func TestSchedulerPermanentErrorRetriedWhenFlipped(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
			if in.QueueUrl[len(in.QueueUrl)-len("/gone"):] == "/gone" {
				return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
			}
			return nil
		})
		dlq := arn("sqs", "sch-dlq")
		enqueue(k, o, toSchedulerQueue("gone", "x", delivery.PermanentRetried, 2, &dlq), source(k))
		settled(k)
		for range 60 {
			advance(k, time.Minute)
		}
		got := q.sent()
		if len(got) != 4 || got[3].queue != "sch-dlq" {
			k.Fatalf("sent %d messages (%+v), want 3 attempts (2 retries) then the dead letter", len(got), got)
		}
		attrs := got[3].in.MessageAttributes
		wantDeadLetterAttributes(k, attrs, "AWS.SimpleQueueService.NonExistentQueue", arn("sqs", "gone"))
		if got := deref(attrs["EXHAUSTED_RETRY_CONDITION"].StringValue); got != "MaximumRetryAttempts" {
			k.Errorf("EXHAUSTED_RETRY_CONDITION = %q, want MaximumRetryAttempts", got)
		}
	})
}

// A retryable error (throttling) is retried whatever the switch, and the
// dead letter after the last retry carries EXHAUSTED_RETRY_CONDITION
// MaximumRetryAttempts (aws-doc configuring-schedule-dlq; scheduler.md 7.1,
// 7.2).
func TestSchedulerRetryableErrorExhaustsTheRetries(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
			if in.QueueUrl[len(in.QueueUrl)-len("/busy"):] == "/busy" {
				return throttled("")
			}
			return nil
		})
		dlq := arn("sqs", "sch-dlq")
		tmpl := "try <aws.scheduler.attempt-number>"
		enqueue(k, o, toSchedulerQueue("busy", tmpl, delivery.PermanentNotRetried, 1, &dlq), source(k))
		settled(k)
		for range 60 {
			advance(k, time.Minute)
		}
		got := q.sent()
		if len(got) != 3 || got[0].queue != "busy" || got[1].queue != "busy" || got[2].queue != "sch-dlq" {
			k.Fatalf("sent %+v, want 2 attempts to busy then the dead letter", got)
		}
		if got[0].in.MessageBody != "try 1" || got[1].in.MessageBody != "try 2" {
			k.Errorf("attempt bodies = %q, %q; want try 1, try 2", got[0].in.MessageBody, got[1].in.MessageBody)
		}
		attrs := got[2].in.MessageAttributes
		wantDeadLetterAttributes(k, attrs, sqsapi.IdentityRequestThrottled().QueryCode, arn("sqs", "busy"))
		if want := sqsDeadLetterBody("try 2", "", arn("sqs", "busy")); got[2].in.MessageBody != want {
			k.Errorf("dead-letter body = %q, want %q", got[2].in.MessageBody, want)
		}
		if got := deref(attrs["EXHAUSTED_RETRY_CONDITION"].StringValue); got != "MaximumRetryAttempts" {
			k.Errorf("EXHAUSTED_RETRY_CONDITION = %q, want MaximumRetryAttempts", got)
		}
	})
}

// A Lambda target is invoked asynchronously with the rendered payload:
// "EventBridge Scheduler invokes your Lambda Event Function asynchronously"
// (aws-doc https://docs.aws.amazon.com/lambda/latest/dg/with-eventbridge-scheduler.html;
// scheduler.md 6.4), as the execution role.
func TestSchedulerLambdaTargetIsInvokedAsynchronously(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		type invoke struct {
			in        lambdaapi.InvokeInput
			principal identity.Principal
			origin    ref.Origin
		}
		var calls recorder[invoke]
		k.Stub(lambdaapi.OpInvoke, func(_ context.Context, c *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			calls.add(invoke{in: *in, principal: c.Principal(), origin: c.Origin()})
			return &lambdaapi.InvokeOutput{StatusCode: 202}, nil
		})
		tmpl := `{"attempt":"<aws.scheduler.attempt-number>"}`
		fn := arn("lambda", "function:tick")
		target := delivery.LambdaFunction{ARN: fn, Event: []byte(tmpl), Invocation: delivery.InvocationEvent}
		enqueue(k, o, toSchedulerTarget(delivery.SchedulerToLambda, target, fn, tmpl, delivery.PermanentNotRetried, 185, nil), source(k))
		settled(k)
		got := calls.all()
		if len(got) != 1 {
			k.Fatalf("%d invokes, want 1", len(got))
		}
		in := got[0].in
		if in.InvocationType == nil || *in.InvocationType != lambdaapi.InvocationTypeEvent || string(in.Payload) != `{"attempt":"1"}` {
			k.Errorf("Invoke = type %v payload %s, want Event with {\"attempt\":\"1\"}", in.InvocationType, in.Payload)
		}
		wantRoleSession(k, "Invoke", got[0].principal)
		if got[0].origin != wantSchedulerOrigin {
			k.Errorf("Invoke origin = %v, want %v", got[0].origin, wantSchedulerOrigin)
		}
		noAdvisories(k)
	})
}

// An SNS target is a Publish of the payload to the topic (aws-doc
// https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-targets-templated.html;
// scheduler.md 6.4), as the execution role.
func TestSchedulerSNSTargetPublishes(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		type publish struct {
			in        snsapi.PublishInput
			principal identity.Principal
		}
		var calls recorder[publish]
		k.Stub(snsapi.OpPublish, func(_ context.Context, c *kernel.Call, in *snsapi.PublishInput) (*snsapi.PublishOutput, *awserr.Error) {
			calls.add(publish{in: *in, principal: c.Principal()})
			return &snsapi.PublishOutput{MessageId: new("m")}, nil
		})
		tmpl := "tick <aws.scheduler.attempt-number>"
		topicARN := arn("sns", "ticks")
		enqueue(k, o, toSchedulerTarget(delivery.SchedulerToSNS, delivery.SNSTopic{ARN: topicARN, Message: tmpl}, topicARN, tmpl, delivery.PermanentNotRetried, 185, nil), source(k))
		settled(k)
		got := calls.all()
		if len(got) != 1 {
			k.Fatalf("%d publishes, want 1", len(got))
		}
		if in := got[0].in; deref(in.TopicArn) != topicARN.String() || in.Message != "tick 1" {
			k.Errorf("Publish = topic %v message %q, want %s and tick 1", in.TopicArn, in.Message, topicARN)
		}
		wantRoleSession(k, "Publish", got[0].principal)
		noAdvisories(k)
	})
}

// An event bus target is a PutEvents of one entry to the bus with the
// target's EventBridgeParameters Source and DetailType (aws-doc
// managing-targets-templated; scheduler.md 6.4, 14.4.3), as the execution
// role.
func TestSchedulerEventBusTargetPutsOneEntry(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		calls := stubBus(k, func(int) string { return "" })
		bus := arn("events", "event-bus/default")
		tmpl := `{"attempt":"<aws.scheduler.attempt-number>"}`
		target := delivery.EventBus{ARN: bus, Entries: []delivery.EventEntry{{Source: "my.app", DetailType: "tick", Detail: tmpl}}}
		enqueue(k, o, toSchedulerTarget(delivery.SchedulerToEvents, target, bus, tmpl, delivery.PermanentNotRetried, 185, nil), source(k))
		settled(k)
		got := calls.all()
		if len(got) != 1 || len(got[0].in.Entries) != 1 {
			k.Fatalf("PutEvents calls = %+v, want one call with one entry", got)
		}
		e := got[0].in.Entries[0]
		if deref(e.Source) != "my.app" || deref(e.DetailType) != "tick" || deref(e.Detail) != `{"attempt":"1"}` {
			k.Errorf("entry = source %q detail-type %q detail %q, want my.app, tick, {\"attempt\":\"1\"}", deref(e.Source), deref(e.DetailType), deref(e.Detail))
		}
		if name := deref(e.EventBusName); name != bus.String() && name != "default" {
			k.Errorf("entry bus = %q, want the target bus %s", name, bus)
		}
		wantRoleSession(k, "PutEvents", got[0].principal)
		noAdvisories(k)
	}, kerneltest.Services(eventbridgeapi.Service()))
}

// The outbox takes a Scheduler delivery only with the role it runs as and
// the invocation it renders from, and refuses either on another mechanism.
func TestSchedulerRequestFieldsAreChecked(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		cause := source(k)
		full := toSchedulerQueue("q", "x", delivery.PermanentNotRetried, 185, nil)
		if _, err := o.Enqueue(full, cause, k.Clock.Now()); err != nil {
			k.Errorf("Enqueue of a complete Scheduler request = %v, want accepted", err)
		}
		noRole := full
		noRole.As = nil
		if _, err := o.Enqueue(noRole, cause, k.Clock.Now()); err == nil {
			k.Error("Enqueue of a Scheduler request without As accepted")
		}
		noInvocation := full
		noInvocation.Invocation = nil
		if _, err := o.Enqueue(noInvocation, cause, k.Clock.Now()); err == nil {
			k.Error("Enqueue of a Scheduler request without Invocation accepted")
		}
		other := toQueue("q", delivery.SNSManaged{})
		other.Invocation = full.Invocation
		if _, err := o.Enqueue(other, cause, k.Clock.Now()); err == nil {
			k.Error("Enqueue of an SNS delivery with a Scheduler Invocation accepted")
		}
	})
}
