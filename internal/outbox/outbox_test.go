package outbox_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

const settle = time.Minute

// run runs fn against a kerneltest kernel with SQS in its registry and an
// outbox bound to it with the kernel's fault store.
func run(t *testing.T, fn func(k *kerneltest.K, o *outbox.Outbox), opts ...kerneltest.Option) {
	t.Helper()
	runWith(t, outbox.Options{}, fn, opts...)
}

func runWith(t *testing.T, oo outbox.Options, fn func(k *kerneltest.K, o *outbox.Outbox), opts ...kerneltest.Option) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		o := outbox.New()
		oo.Faults = k.Faults
		if err := o.Bind(k.Kernel, oo); err != nil {
			k.Fatal(err)
		}
		fn(k, o)
	}, append([]kerneltest.Option{kerneltest.Services(sqsapi.Service(), lambdaapi.Service(), snsapi.Service())}, opts...)...)
}

// sent is one SendMessage a stub received.
type sent struct {
	at    clock.Instant
	queue string
	in    sqsapi.SendMessageInput
}

// recorder collects what a stub target received. Stubs run on attempt
// goroutines, so it locks.
type recorder[T any] struct {
	mu    sync.Mutex
	items []T
}

// add records v and returns how many items came before it.
func (r *recorder[T]) add(v T) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, v)
	return len(r.items) - 1
}

func (r *recorder[T]) all() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.items)
}

// queues stubs SendMessage: answer decides each call's error, and every
// call is recorded.
type queues struct {
	calls  recorder[sent]
	answer func(n int, in *sqsapi.SendMessageInput) *awserr.Error
}

func stubQueues(k *kerneltest.K, answer func(n int, in *sqsapi.SendMessageInput) *awserr.Error) *queues {
	q := &queues{answer: answer}
	k.Stub(sqsapi.OpSendMessage, func(_ context.Context, c *kernel.Call, in *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
		n := q.calls.add(sent{at: c.Admitted(), queue: in.QueueUrl[strings.LastIndexByte(in.QueueUrl, '/')+1:], in: *in})
		if err := q.answer(n, in); err != nil {
			return nil, err
		}
		id := "m"
		return &sqsapi.SendMessageOutput{MessageId: &id}, nil
	})
	return q
}

func (q *queues) sent() []sent { return q.calls.all() }

// source stands for the call that produced the deliveries: a client call
// whose event is their parent.
func source(k *kerneltest.K) ref.Cause {
	var cause ref.Cause
	k.Stub(sqsapi.OpListQueues, func(_ context.Context, c *kernel.Call, _ *sqsapi.ListQueuesInput) (*sqsapi.ListQueuesOutput, *awserr.Error) {
		cause = ref.Cause{Parent: c.Event(), Trace: c.Cause().Trace}
		if cause.Trace == 0 {
			cause.Trace = c.Event()
		}
		return &sqsapi.ListQueuesOutput{}, nil
	})
	if _, err := k.Invoke(k.Context(), sqsapi.OpListQueues, &sqsapi.ListQueuesInput{}); err != nil {
		k.Fatal(err.AsError())
	}
	return cause
}

func arn(service, resource string) aws.ARN {
	return aws.ARN{Partition: "aws", Service: service, Region: "us-east-1", Account: "000000000000", Resource: resource}
}

func topic() aws.ARN { return arn("sns", "orders") }

func toQueue(name string, policy delivery.RetryPolicy) delivery.Request {
	return delivery.Request{
		Source:    topic(),
		Mechanism: delivery.SNSToSQS,
		Target:    delivery.SQSQueue{ARN: arn("sqs", name), Body: `{"Type":"Notification","Message":"hello"}`},
		Policy:    policy,
	}
}

func enqueue(k *kerneltest.K, o *outbox.Outbox, req delivery.Request, cause ref.Cause) ids.DeliveryID {
	k.Helper()
	id, err := o.Enqueue(req, cause, k.Clock.Now())
	if err != nil {
		k.Fatal(err)
	}
	return id
}

func settled(k *kerneltest.K) {
	k.Helper()
	if p, err := k.Kernel.Settle(k.Context(), settle); err != nil {
		k.Fatalf("Settle = %v, %v", p, err)
	}
}

func advance(k *kerneltest.K, d time.Duration) {
	k.Helper()
	if _, err := k.Clock.Advance(d); err != nil {
		k.Fatal(err)
	}
	settled(k)
}

// deliveries returns the Delivery events, in journal order.
func deliveries(k *kerneltest.K) []journal.Event {
	return k.Journal.Query(journal.Filter{Kind: journal.KindDelivery, Limit: 1000}).Events
}

func throttled(string) *awserr.Error { return sqsapi.ErrRequestThrottled("slow down") }

// A target that fails n times is retried on SNS's AWS-managed schedule: 3
// retries at once, 2 one second apart, then the exponential backoff, each
// attempt stamped with its due instant, until it succeeds.
func TestSNSManagedRetriesOnSchedule(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		const failures = 8
		q := stubQueues(k, func(n int, _ *sqsapi.SendMessageInput) *awserr.Error {
			if n < failures {
				return throttled("")
			}
			return nil
		})
		cause := source(k)
		t0 := k.Clock.Now()
		enqueue(k, o, toQueue("orders-queue", delivery.SNSManaged{}), cause)
		settled(k)
		advance(k, time.Minute)

		ms := time.Millisecond
		offsets := []time.Duration{0, 0, 0, 0, 1000 * ms, 2000 * ms, 3000 * ms, 4394 * ms, 6339 * ms}
		got := q.sent()
		if len(got) != len(offsets) {
			k.Fatalf("%d attempts, want %d", len(got), len(offsets))
		}
		for i, s := range got {
			if want := t0.Add(offsets[i]); s.at != want {
				k.Errorf("attempt %d at %v, want %v", i+1, s.at.Sub(t0), offsets[i])
			}
		}
		events := deliveries(k)
		if len(events) != len(offsets) {
			k.Fatalf("%d delivery events, want %d", len(events), len(offsets))
		}
		for i, ev := range events {
			d := detail[journal.Delivery](k, ev)
			if d.Attempt != i+1 || ev.Cause.Parent != cause.Parent || ev.At != t0.Add(offsets[i]) {
				k.Errorf("event %d = attempt %d at %v caused by %d; want attempt %d at %v caused by %d",
					i, d.Attempt, ev.At.Sub(t0), ev.Cause.Parent, i+1, offsets[i], cause.Parent)
			}
			var want journal.DeliveryOutcome = journal.Delivered{}
			if i < failures {
				want = journal.Retrying{Next: t0.Add(offsets[i+1])}
			}
			if d.Outcome != want {
				k.Errorf("attempt %d outcome = %#v, want %#v", i+1, d.Outcome, want)
			}
		}
		if r := o.Resources(k.Scope); len(r) != 0 {
			k.Errorf("Resources after delivery = %v, want none", r)
		}
	})
}

// storm makes deliveries that keep failing under several policies and
// returns each delivery's attempt instants, offset from the start.
func storm(t *testing.T, seed uint64) map[string][]time.Duration {
	t.Helper()
	var out map[string][]time.Duration
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return throttled("") })
		cause := source(k)
		t0 := k.Clock.Now()
		eb := delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: 185}
		for _, name := range []string{"a", "b", "c"} {
			enqueue(k, o, toQueue("events-"+name, eb), cause)
			enqueue(k, o, toQueue("sns-"+name, delivery.SNSManaged{}), cause)
		}
		settled(k)
		for range 120 {
			advance(k, 10*time.Second)
		}
		out = map[string][]time.Duration{}
		for _, s := range q.sent() {
			out[s.queue] = append(out[s.queue], s.at.Sub(t0))
		}
		for _, at := range out {
			slices.Sort(at)
		}
	}, kerneltest.Seed(seed))
	return out
}

// A retry storm replays identically under the same seed: the jitter of
// every EventBridge retry comes from the seeded source.
func TestRetryStormReplaysWithTheSameSeed(t *testing.T) {
	first, second := storm(t, 7), storm(t, 7)
	if !maps.EqualFunc(first, second, slices.Equal) {
		t.Fatalf("the same seed replayed differently:\n%v\n%v", first, second)
	}
	other := storm(t, 8)
	if slices.Equal(first["events-a"], other["events-a"]) {
		t.Errorf("seeds 7 and 8 drew the same EventBridge jitter: %v", first["events-a"])
	}
	if !slices.Equal(first["sns-a"], other["sns-a"]) {
		t.Errorf("the SNS schedule has no jitter, yet seeds 7 and 8 differ: %v, %v", first["sns-a"], other["sns-a"])
	}
}

// A missing queue is a client-side failure: no retry, and the message goes
// to the subscription's dead-letter queue at once, as the subscription would
// have received it (sns.md 7.1, 7.4).
func TestMissingQueueGoesToTheDLQAtOnce(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
			if strings.HasSuffix(in.QueueUrl, "/gone") {
				return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
			}
			return nil
		})
		cause := source(k)
		dlq := arn("sqs", "orders-dlq")
		envelope := `{"Type":"Notification","MessageId":"m-1","TopicArn":"arn:aws:sns:us-east-1:000000000000:orders","Message":"hello","Timestamp":"2000-01-01T00:00:00.000Z"}`
		req := delivery.Request{
			Source:    topic(),
			Mechanism: delivery.SNSToSQS,
			Target:    delivery.SQSQueue{ARN: arn("sqs", "gone"), Body: envelope},
			Policy:    delivery.SNSManaged{},
			DLQ:       &dlq,
		}
		t0 := k.Clock.Now()
		enqueue(k, o, req, cause)
		settled(k)

		got := q.sent()
		if len(got) != 2 || got[0].queue != "gone" || got[1].queue != "orders-dlq" {
			k.Fatalf("sent %+v, want one attempt to gone, then the DLQ", got)
		}
		if dl := got[1]; dl.in.MessageBody != envelope || dl.at != t0 || len(dl.in.MessageAttributes) != 0 {
			k.Errorf("DLQ message = %q at %v with attributes %v, want the envelope at the attempt's instant", dl.in.MessageBody, dl.at, dl.in.MessageAttributes)
		}
		events := deliveries(k)
		if len(events) != 1 {
			k.Fatalf("delivery events = %+v, want one", events)
		}
		d := detail[journal.Delivery](k, events[0])
		if d.Outcome != (journal.DeadLettered{DLQ: dlq}) || d.Attempt != 1 || d.From != topic() || d.To != arn("sqs", "gone") {
			k.Errorf("delivery = %+v, want attempt 1 from the topic to gone, dead-lettered to %v", d, dlq)
		}
	})
}

// Under raw delivery the dead letter is the raw message with its
// attributes as SQS message attributes (sns.md 7.4).
func TestRawDeadLetterKeepsAttributes(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
			if strings.HasSuffix(in.QueueUrl, "/gone") {
				return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
			}
			return nil
		})
		dlq := arn("sqs", "orders-dlq")
		req := delivery.Request{
			Source:    topic(),
			Mechanism: delivery.SNSToSQS,
			Target: delivery.SQSQueue{ARN: arn("sqs", "gone"), Body: "hello", Attributes: []delivery.MessageAttribute{
				{Name: "n", Value: delivery.NumberAttribute{Value: "99.12"}},
				{Name: "b", Value: delivery.BinaryAttribute{Value: []byte{2, 3, 4}}},
			}},
			Policy: delivery.SNSManaged{},
			DLQ:    &dlq,
		}
		enqueue(k, o, req, source(k))
		settled(k)
		got := q.sent()
		if len(got) != 2 {
			k.Fatalf("sent %+v, want the attempt and the dead letter", got)
		}
		attrs := got[1].in.MessageAttributes
		if n := attrs["n"]; n.DataType != "Number" || n.StringValue == nil || *n.StringValue != "99.12" {
			k.Errorf("attribute n = %+v, want Number 99.12", n)
		}
		if b := attrs["b"]; b.DataType != "Binary" || string(b.BinaryValue) != "\x02\x03\x04" {
			k.Errorf("attribute b = %+v, want Binary 02 03 04", b)
		}
		if got[1].in.MessageBody != "hello" {
			k.Errorf("DLQ body = %q, want the raw message", got[1].in.MessageBody)
		}
	})
}

// Without a dead-letter queue a client-side failure drops the message.
func TestMissingQueueWithoutDLQIsDropped(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error {
			return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
		})
		enqueue(k, o, toQueue("gone", delivery.SNSManaged{}), source(k))
		settled(k)
		if n := len(q.sent()); n != 1 {
			k.Errorf("%d attempts, want 1", n)
		}
		events := deliveries(k)
		if len(events) != 1 {
			k.Fatalf("delivery events = %+v, want one", events)
		}
		if d := detail[journal.Delivery](k, events[0]); !isDropped(d.Outcome) {
			k.Errorf("outcome = %#v, want Dropped", d.Outcome)
		}
	})
}

// A dead letter the dead-letter queue refuses is lost (sns.md 7.4): the
// delivery is dropped, and the journal gives both the target's and the
// dead-letter queue's errors.
func TestRefusedDeadLetterIsDroppedWithBothReasons(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
			if strings.HasSuffix(in.QueueUrl, "/orders-dlq") {
				return sqsapi.ErrQueueDoesNotExist("the dead-letter queue is gone too")
			}
			return sqsapi.ErrQueueDoesNotExist("the target queue is gone")
		})
		dlq := arn("sqs", "orders-dlq")
		req := toQueue("gone", delivery.SNSManaged{})
		req.DLQ = &dlq
		enqueue(k, o, req, source(k))
		settled(k)
		advance(k, time.Hour)

		if got := q.sent(); len(got) != 2 || got[0].queue != "gone" || got[1].queue != "orders-dlq" {
			k.Fatalf("sent %+v, want one attempt to gone, then one to the DLQ", got)
		}
		events := deliveries(k)
		if len(events) != 1 {
			k.Fatalf("delivery events = %+v, want one", events)
		}
		d, ok := detail[journal.Delivery](k, events[0]).Outcome.(journal.Dropped)
		if !ok || !strings.Contains(d.Reason, "the target queue is gone") ||
			!strings.Contains(d.Reason, "the dead-letter queue "+dlq.String()+" refused it") ||
			!strings.Contains(d.Reason, "the dead-letter queue is gone too") {
			k.Errorf("outcome = %#v, want Dropped naming the target's error and the DLQ's refusal", d)
		}
		if r := o.Resources(k.Scope); len(r) != 0 {
			k.Errorf("Resources = %v, want the delivery gone", r)
		}
	})
}

// A FIFO delivery's dead letter keeps its group and deduplication ids in a
// FIFO dead-letter queue (sns.md 7.4).
func TestFIFODeadLetterKeepsGroupAndDedupIDs(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
			if strings.HasSuffix(in.QueueUrl, "/orders.fifo") {
				return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
			}
			return nil
		})
		dlq := arn("sqs", "orders-dlq.fifo")
		req := toFIFO("g", "g1")
		req.DLQ = &dlq
		enqueue(k, o, req, source(k))
		settled(k)

		got := q.sent()
		if len(got) != 2 || got[1].queue != "orders-dlq.fifo" {
			k.Fatalf("sent %+v, want the attempt, then the dead letter", got)
		}
		in := got[1].in
		if in.MessageGroupId == nil || *in.MessageGroupId != "g" || in.MessageDeduplicationId == nil || *in.MessageDeduplicationId != "g1" {
			k.Errorf("dead letter group %v, dedup %v; want g and g1", in.MessageGroupId, in.MessageDeduplicationId)
		}
		if d := detail[journal.Delivery](k, deliveries(k)[0]); d.Outcome != (journal.DeadLettered{DLQ: dlq}) {
			k.Errorf("outcome = %#v, want DeadLettered to %v", d.Outcome, dlq)
		}
	})
}

// detail returns ev's detail as a D, failing the test when it is another
// kind.
func detail[D journal.Detail](k *kerneltest.K, ev journal.Event) D {
	k.Helper()
	d, ok := ev.Detail.(D)
	if !ok {
		k.Fatalf("event %d is %T, want %T", ev.ID, ev.Detail, d)
	}
	return d
}

func isDropped(o journal.DeliveryOutcome) bool {
	_, ok := o.(journal.Dropped)
	return ok
}

// A target operation understudy does not implement fails loudly: the
// delivery is dropped with the unimplemented message, never retried.
func TestUnimplementedTargetIsDroppedWithItsMessage(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		enqueue(k, o, toQueue("orders-queue", delivery.SNSManaged{}), source(k))
		settled(k)
		advance(k, time.Hour)
		events := deliveries(k)
		if len(events) != 1 {
			k.Fatalf("delivery events = %+v, want one", events)
		}
		d, ok := detail[journal.Delivery](k, events[0]).Outcome.(journal.Dropped)
		if !ok || !strings.Contains(d.Reason, "does not implement sqs.SendMessage") {
			k.Errorf("outcome = %#v, want Dropped naming the unimplemented operation", d)
		}
	})
}

// A Delivery-phase Drop rule discards the attempt: the target never sees
// it, the journal records the fault hit and a Dropped delivery, and the
// delivery is not retried.
func TestDeliveryPhaseDropRule(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		rule, rerr := k.Faults.Add(fault.Rule{
			Match:   fault.Match{Mechanisms: []delivery.Mechanism{delivery.SNSToSQS}},
			Phase:   fault.Delivery,
			Action:  fault.DropDelivery{},
			Trigger: fault.NextN{N: 1},
		})
		if rerr != nil {
			k.Fatal(rerr)
		}
		cause := source(k)
		enqueue(k, o, toQueue("orders-queue", delivery.SNSManaged{}), cause)
		settled(k)
		advance(k, time.Hour)

		if got := q.sent(); len(got) != 0 {
			k.Errorf("the target received %+v, want nothing", got)
		}
		events := deliveries(k)
		if len(events) != 1 {
			k.Fatalf("delivery events = %+v, want one", events)
		}
		if d := detail[journal.Delivery](k, events[0]); !isDropped(d.Outcome) || events[0].Cause.Parent != cause.Parent {
			k.Errorf("delivery = %+v caused by %d, want Dropped caused by %d", d, events[0].Cause.Parent, cause.Parent)
		}
		hits := k.Journal.Query(journal.Filter{Kind: journal.KindFaultInjected}).Events
		if len(hits) != 1 || hits[0].Detail != (journal.FaultInjected{Rule: rule, Hit: 1, Of: 1}) {
			k.Errorf("fault events = %+v, want one hit of %s", hits, rule)
		}
	})
}

// call is one internal call a stub target received: who made it and how.
type call[In any] struct {
	in        In
	at        clock.Instant
	principal identity.Principal
	origin    ref.Origin
}

// SNS invokes a subscribed function asynchronously with the Records event
// as sns.amazonaws.com (lambda-events.md 4); a missing function is a
// client-side failure that goes to the subscription's dead-letter queue.
// The dead letter's body is not asserted: AWS puts the SQS-style envelope
// there, which the outbox cannot build (a known gap, see the package doc).
func TestSNSToLambda(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		var invokes recorder[call[lambdaapi.InvokeInput]]
		k.Stub(lambdaapi.OpInvoke, func(_ context.Context, c *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			invokes.add(call[lambdaapi.InvokeInput]{in: *in, at: c.Admitted(), principal: c.Principal(), origin: c.Origin()})
			if strings.HasSuffix(in.FunctionName, ":gone") {
				return nil, lambdaapi.ErrResourceNotFoundException("Function not found: " + in.FunctionName)
			}
			return &lambdaapi.InvokeOutput{StatusCode: 202}, nil
		})
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		cause := source(k)
		dlq := arn("sqs", "orders-dlq")
		event := []byte(`{"Records":[{"EventSource":"aws:sns"}]}`)
		toFunction := func(name string) delivery.Request {
			return delivery.Request{
				Source:    topic(),
				Mechanism: delivery.SNSToLambda,
				Target:    delivery.LambdaFunction{ARN: arn("lambda", "function:"+name), Event: event, Invocation: delivery.InvocationEvent},
				Policy:    delivery.SNSManaged{},
				DLQ:       &dlq,
			}
		}
		enqueue(k, o, toFunction("worker"), cause)
		settled(k)
		enqueue(k, o, toFunction("gone"), cause)
		settled(k)

		calls := invokes.all()
		if len(calls) != 2 {
			k.Fatalf("invokes = %+v, want one per delivery", calls)
		}
		got := calls[0]
		if got.in.FunctionName != arn("lambda", "function:worker").String() || got.in.InvocationType == nil ||
			*got.in.InvocationType != lambdaapi.InvocationTypeEvent || !bytes.Equal(got.in.Payload, event) {
			k.Errorf("Invoke input = %+v, want an Event invoke of worker with the event", got.in)
		}
		if got.principal != (identity.ServicePrincipal{Name: "sns.amazonaws.com"}) || got.origin != (ref.Internal{Via: ref.SNSDelivery}) {
			k.Errorf("Invoke by %v from %v, want sns.amazonaws.com over SNS delivery", got.principal, got.origin)
		}
		sentToDLQ := q.sent()
		if len(sentToDLQ) != 1 || sentToDLQ[0].queue != "orders-dlq" {
			k.Errorf("dead letters = %+v, want one in orders-dlq", sentToDLQ)
		}
	})
}

// An EventBridge target topic gets a Publish of the event as
// events.amazonaws.com.
func TestEventsToSNS(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		var publishes recorder[call[snsapi.PublishInput]]
		k.Stub(snsapi.OpPublish, func(_ context.Context, c *kernel.Call, in *snsapi.PublishInput) (*snsapi.PublishOutput, *awserr.Error) {
			publishes.add(call[snsapi.PublishInput]{in: *in, at: c.Admitted(), principal: c.Principal(), origin: c.Origin()})
			id := "m"
			return &snsapi.PublishOutput{MessageId: &id}, nil
		})
		rule := arn("events", "rule/nightly")
		req := delivery.Request{
			Source:    rule,
			Mechanism: delivery.EventsToSNS,
			Target: delivery.SNSTopic{ARN: topic(), Message: `{"version":"0"}`, Subject: "nightly", Attributes: []delivery.MessageAttribute{
				{Name: "kind", Value: delivery.StringAttribute{Value: "scheduled"}},
			}},
			Policy: delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: 185},
		}
		t0 := k.Clock.Now()
		enqueue(k, o, req, source(k))
		settled(k)
		calls := publishes.all()
		if len(calls) != 1 {
			k.Fatalf("publishes = %+v, want one", calls)
		}
		got := calls[0]
		if got.in.TopicArn == nil || *got.in.TopicArn != topic().String() || got.in.Message != `{"version":"0"}` ||
			got.in.Subject == nil || *got.in.Subject != "nightly" || got.at != t0 {
			k.Errorf("Publish input = %+v at %v, want the event to orders at %v", got.in, got.at, t0)
		}
		if a := got.in.MessageAttributes["kind"]; a.DataType != "String" || a.StringValue == nil || *a.StringValue != "scheduled" {
			k.Errorf("attribute kind = %+v, want String scheduled", a)
		}
		if got.principal != (identity.ServicePrincipal{Name: "events.amazonaws.com"}) || got.origin != (ref.Internal{Via: ref.EventsTarget}) {
			k.Errorf("Publish by %v from %v, want events.amazonaws.com over an EventBridge target", got.principal, got.origin)
		}
	})
}

// A Delivery-phase latency rule holds the attempt for wall time, which a
// frozen emulator clock cannot stretch, then lets it through.
func TestDeliveryPhaseLatencyRule(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		if _, rerr := k.Faults.Add(fault.Rule{Phase: fault.Delivery, Action: fault.Latency{Wall: 2 * time.Second}, Trigger: fault.Always{}}); rerr != nil {
			k.Fatal(rerr)
		}
		t0 := k.Clock.Now()
		start := k.Clock.Wall()
		enqueue(k, o, toQueue("orders-queue", delivery.SNSManaged{}), source(k))
		settled(k)
		if waited := k.Clock.Wall().Sub(start); waited < 2*time.Second {
			k.Errorf("the attempt waited %v of wall time, want 2s", waited)
		}
		if got := q.sent(); len(got) != 1 || got[0].at != t0 {
			k.Errorf("sent %+v, want one attempt stamped %v", got, t0)
		}
		if hits := k.Journal.Query(journal.Filter{Kind: journal.KindFaultInjected}).Events; len(hits) != 1 {
			k.Errorf("fault events = %+v, want one", hits)
		}
	})
}

// Pending deliveries survive a snapshot: after a load into a new kernel,
// Rearm fires each at the instant it was due, with its attempt count kept.
func TestSnapshotThenRearmFiresAtTheRightInstants(t *testing.T) {
	var sections []store.Section
	var t0, ebRetry clock.Instant
	eb := delivery.EventBridgeTarget{MaxAge: time.Hour, MaxAttempts: 3}
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return throttled("") })
		cause := source(k)
		t0 = k.Clock.Now()
		enqueue(k, o, toQueue("sns-queue", delivery.SNSManaged{}), cause)
		enqueue(k, o, toQueue("events-queue", eb), cause)
		settled(k)
		if r := o.Resources(k.Scope); len(r) != 2 {
			k.Fatalf("Resources = %v, want both deliveries pending", r)
		}
		events := deliveries(k)
		for i := range events {
			if d := detail[journal.Delivery](k, events[i]); d.To.Resource == "events-queue" {
				r, ok := d.Outcome.(journal.Retrying)
				if !ok || d.Attempt != 1 {
					k.Fatalf("EventBridge delivery = %+v, want attempt 1 retrying", d)
				}
				ebRetry = r.Next
			}
		}
		if ebRetry.IsZero() {
			k.Fatal("no EventBridge attempt was journaled")
		}
		var err error
		if sections, err = o.Export(); err != nil {
			k.Fatal(err)
		}
	})
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		if err := o.Import(sections); err != nil {
			k.Fatal(err)
		}
		if k.Clock.Now() != t0 {
			k.Fatalf("the second kernel starts at %v, want %v", k.Clock.Now(), t0)
		}
		o.Rearm(k.Clock.Now())
		settled(k)
		if got := q.sent(); len(got) != 0 {
			k.Fatalf("attempts before they are due: %+v", got)
		}
		advance(k, time.Hour)
		got := q.sent()
		if len(got) != 2 {
			k.Fatalf("sent %+v, want one attempt per delivery", got)
		}
		for _, s := range got {
			switch s.queue {
			case "sns-queue":
				if want := t0.Add(time.Second); s.at != want {
					k.Errorf("SNS retry at %v, want %v: the fifth attempt, 1 s after the immediate retries", s.at, want)
				}
			case "events-queue":
				if s.at != ebRetry {
					k.Errorf("EventBridge retry at %v, want %v, the instant the first kernel scheduled", s.at, ebRetry)
				}
			}
		}
		for _, ev := range deliveries(k) {
			d := detail[journal.Delivery](k, ev)
			if want := map[string]int{"sns-queue": 5, "events-queue": 2}[d.To.Resource]; d.Attempt != want {
				k.Errorf("attempt to %s numbered %d, want %d", d.To.Resource, d.Attempt, want)
			}
		}
		if r := o.Resources(k.Scope); len(r) != 0 {
			k.Errorf("Resources after delivery = %v, want none", r)
		}
	})
}

// Settle counts an attempt in progress and returns once it completes.
func TestSettleWaitsForAnAttempt(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		release := make(chan struct{})
		stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error {
			<-release
			return nil
		})
		enqueue(k, o, toQueue("orders-queue", delivery.SNSManaged{}), source(k))
		done := make(chan error, 1)
		go func() {
			_, err := k.Kernel.Settle(context.Background(), settle)
			done <- err
		}()
		synctest.Wait()
		select {
		case err := <-done:
			k.Fatalf("Settle returned (%v) while the attempt was in progress", err)
		default:
		}
		close(release)
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil {
				k.Errorf("Settle = %v, want nothing pending", err)
			}
		default:
			k.Fatal("Settle did not return once the attempt completed")
		}
		if n := len(deliveries(k)); n != 1 {
			k.Errorf("%d delivery events, want 1", n)
		}
	})
}

// A reset that drops a delivery while its attempt runs discards the
// attempt's outcome: nothing claims a retry that was never scheduled, and
// the delivery is not attempted again.
func TestResetDuringAnAttemptJournalsNothing(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		release := make(chan struct{})
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error {
			<-release
			return throttled("")
		})
		enqueue(k, o, toQueue("orders-queue", delivery.SNSManaged{}), source(k))
		synctest.Wait()
		if n := len(q.sent()); n != 1 {
			k.Fatalf("%d attempts in progress, want 1", n)
		}
		o.Reset(kernel.ResetScope{})
		close(release)
		settled(k)
		advance(k, time.Hour)

		if events := deliveries(k); len(events) != 0 {
			k.Errorf("delivery events = %+v, want none for a delivery reset mid-attempt", events)
		}
		if n := len(q.sent()); n != 1 {
			k.Errorf("%d attempts, want only the one the reset interrupted", n)
		}
		if r := o.Resources(k.Scope); len(r) != 0 {
			k.Errorf("Resources = %v, want nothing pending after the reset", r)
		}
	})
}

// A snapshot load while an attempt runs can bring back the same delivery at
// the same attempt. The attempt's outcome belongs to the world it started
// in, so it changes and journals nothing, and the reloaded delivery stays
// pending until Rearm attempts it afresh.
func TestSnapshotLoadDuringAnAttemptJournalsNothing(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		release := make(chan struct{})
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error {
			<-release
			return nil
		})
		enqueue(k, o, toQueue("orders-queue", delivery.SNSManaged{}), source(k))
		synctest.Wait()
		if n := len(q.sent()); n != 1 {
			k.Fatalf("%d attempts in progress, want 1", n)
		}
		sections, err := o.Export()
		if err != nil {
			k.Fatal(err)
		}
		if err := o.Import(sections); err != nil {
			k.Fatal(err)
		}
		close(release)
		settled(k)
		if events := deliveries(k); len(events) != 0 {
			k.Errorf("delivery events = %+v, want none from the attempt the load overtook", events)
		}
		if r := o.Resources(k.Scope); len(r) != 1 {
			k.Fatalf("Resources = %v, want the reloaded delivery pending", r)
		}

		o.Rearm(k.Clock.Now())
		settled(k)
		if n := len(q.sent()); n != 2 {
			k.Errorf("%d attempts, want the overtaken one and one after Rearm", n)
		}
		if events := deliveries(k); len(events) != 1 {
			k.Errorf("delivery events = %+v, want one, from the attempt after Rearm", events)
		}
		if r := o.Resources(k.Scope); len(r) != 0 {
			k.Errorf("Resources = %v, want nothing pending once delivered", r)
		}
	})
}

// policies is a PolicySource over a fixed table.
type policies map[aws.ARN]*policydoc.Document

func (p policies) ResourcePolicy(target aws.ARN) (*policydoc.Document, bool) {
	doc, ok := p[target]
	return doc, ok
}

// A queue whose stored policy would not let SNS send to it in AWS gets an
// Advisory, and understudy delivers anyway.
func TestAdviseWouldDenyStillDelivers(t *testing.T) {
	queue := arn("sqs", "locked")
	runWith(t, outbox.Options{Policies: policies{queue: nil}}, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		cause := source(k)
		enqueue(k, o, toQueue("locked", delivery.SNSManaged{}), cause)
		settled(k)
		advisories := k.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}).Events
		if len(advisories) != 1 {
			k.Fatalf("advisories = %+v, want one", advisories)
		}
		a := detail[journal.Advisory](k, advisories[0])
		if a.Kind != journal.AdvisoryPolicyWouldDeny || a.Resource != queue || advisories[0].Cause.Parent != cause.Parent ||
			!strings.Contains(a.Text, "sqs:SendMessage") || !strings.Contains(a.Text, "sns.amazonaws.com") {
			k.Errorf("advisory = %+v caused by %d", a, advisories[0].Cause.Parent)
		}
		if n := len(q.sent()); n != 1 {
			k.Errorf("%d attempts, want the delivery made anyway", n)
		}
		if d := detail[journal.Delivery](k, deliveries(k)[0]); d.Outcome != (journal.Delivered{}) {
			k.Errorf("outcome = %#v, want Delivered", d.Outcome)
		}
	})
}

// A policy that allows the topic to send gives no advisory.
func TestAdviseAllowedIsSilent(t *testing.T) {
	queue := arn("sqs", "open")
	doc, perr := policydoc.Parse([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},` +
		`"Action":"sqs:SendMessage","Resource":"` + queue.String() + `","Condition":{"ArnEquals":{"aws:SourceArn":"` + topic().String() + `"}}}]}`))
	if perr != nil {
		t.Fatal(perr)
	}
	runWith(t, outbox.Options{Policies: policies{queue: &doc}}, func(k *kerneltest.K, o *outbox.Outbox) {
		stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		enqueue(k, o, toQueue("open", delivery.SNSManaged{}), source(k))
		settled(k)
		if a := k.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}).Events; len(a) != 0 {
			k.Errorf("advisories = %+v, want none", a)
		}
	})
}

// An EventBridge target stops retrying after MaxAttempts retries and drops
// the event when it has no dead-letter queue.
func TestEventBridgeRetriesThenDrops(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return throttled("") })
		enqueue(k, o, toQueue("events-queue", delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: 4}), source(k))
		settled(k)
		advance(k, time.Hour)
		if n := len(q.sent()); n != 5 {
			k.Errorf("%d attempts, want 1 + 4 retries", n)
		}
		events := deliveries(k)
		if d := detail[journal.Delivery](k, events[len(events)-1]); !isDropped(d.Outcome) {
			k.Errorf("last outcome = %#v, want Dropped", d.Outcome)
		}
	})
}

// A delivery the outbox cannot attempt is refused before anything is
// queued, naming the request member at fault. An ARN without an account
// keeps the parse error behind it.
func TestEnqueueRefusesMalformedRequests(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		lambda := arn("lambda", "function:f")
		noAccount := aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Resource: "q"}
		bad := map[string]struct {
			req   delivery.Request
			field string
		}{
			"no target":        {delivery.Request{Source: topic(), Mechanism: delivery.SNSToSQS, Policy: delivery.None{}}, "Target"},
			"no policy":        {delivery.Request{Source: topic(), Mechanism: delivery.SNSToSQS, Target: delivery.SQSQueue{ARN: arn("sqs", "q")}}, "Policy"},
			"wrong service":    {delivery.Request{Source: topic(), Mechanism: delivery.SNSToSQS, Target: delivery.LambdaFunction{ARN: lambda, Invocation: delivery.InvocationEvent}, Policy: delivery.None{}}, "Target.ARN"},
			"no account":       {delivery.Request{Source: topic(), Mechanism: delivery.SNSToSQS, Target: delivery.SQSQueue{ARN: noAccount}, Policy: delivery.None{}}, "Target.ARN"},
			"no DLQ account":   {delivery.Request{Source: topic(), Mechanism: delivery.SNSToSQS, Target: delivery.SQSQueue{ARN: arn("sqs", "q")}, Policy: delivery.None{}, DLQ: &noAccount}, "DLQ"},
			"too many retries": {delivery.Request{Source: topic(), Mechanism: delivery.EventsToSQS, Target: delivery.SQSQueue{ARN: arn("sqs", "q")}, Policy: delivery.EventBridgeTarget{MaxAge: time.Hour, MaxAttempts: 186}}, "Policy"},
			"age under 60 s":   {delivery.Request{Source: topic(), Mechanism: delivery.EventsToSQS, Target: delivery.SQSQueue{ARN: arn("sqs", "q")}, Policy: delivery.EventBridgeTarget{MaxAge: 59 * time.Second, MaxAttempts: 3}}, "Policy"},
			"age over a day":   {delivery.Request{Source: topic(), Mechanism: delivery.EventsToSQS, Target: delivery.SQSQueue{ARN: arn("sqs", "q")}, Policy: delivery.EventBridgeTarget{MaxAge: 24*time.Hour + time.Second, MaxAttempts: 3}}, "Policy"},
		}
		for name, c := range bad {
			_, err := o.Enqueue(c.req, ref.Cause{}, k.Clock.Now())
			if rerr, ok := errors.AsType[*outbox.RequestError](err); !ok || rerr.Field != c.field {
				k.Errorf("%s: Enqueue = %v, want a *RequestError for %s", name, err, c.field)
			}
		}
		_, err := o.Enqueue(bad["no account"].req, ref.Cause{}, k.Clock.Now())
		if ae, ok := errors.AsType[*aws.AccountIDError](err); !ok || ae.Input != "" {
			k.Errorf("Enqueue to a queue with no account = %v, want it to wrap the *aws.AccountIDError for the empty account", err)
		}
		if r := o.Resources(k.Scope); len(r) != 0 {
			k.Errorf("Resources = %v, want nothing queued", r)
		}
	})
}

// toFIFO is an SNS FIFO delivery of body to group g of orders.fifo.
func toFIFO(group, body string) delivery.Request {
	return delivery.Request{
		Source:    arn("sns", "orders.fifo"),
		Mechanism: delivery.SNSToSQS,
		Target:    delivery.SQSQueue{ARN: arn("sqs", "orders.fifo"), Body: body, GroupID: group, DedupID: body},
		Policy:    delivery.SNSManaged{},
	}
}

// failFirst answers a SendMessage of body "g1" with throttling the first n
// times and accepts everything else.
func failFirst(n int) func(int, *sqsapi.SendMessageInput) *awserr.Error {
	var failed atomic.Int64
	return func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
		if in.MessageBody == "g1" && failed.Add(1) <= int64(n) {
			return throttled("")
		}
		return nil
	}
}

// fifoGroup delivers g1 and g2 to one FIFO group, g1 failing its first n
// attempts, and returns what the queue received and each Delivery event as
// "attempt outcome", in journal order.
func fifoGroup(t *testing.T, n int) (got []sent, t0 clock.Instant, journaled []string) {
	t.Helper()
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, failFirst(n))
		cause := source(k)
		t0 = k.Clock.Now()
		enqueue(k, o, toFIFO("g", "g1"), cause)
		enqueue(k, o, toFIFO("g", "g2"), cause)
		settled(k)
		advance(k, time.Minute)
		got = q.sent()
		events := deliveries(k)
		for i := range events {
			d := detail[journal.Delivery](k, events[i])
			journaled = append(journaled, fmt.Sprintf("%d %T", d.Attempt, d.Outcome))
		}
	})
	return got, t0, journaled
}

// Deliveries to one FIFO message group arrive in acceptance order (sns.md
// 10): a later message waits while an earlier one retries, and is stamped
// no earlier than the attempt that delivered the earlier one.
func TestFIFOGroupDeliversInOrder(t *testing.T) {
	// Four failures spend the three immediate retries, so g1 lands 1 s in.
	got, t0, _ := fifoGroup(t, 4)
	bodies := make([]string, len(got))
	for i, s := range got {
		bodies[i] = s.in.MessageBody
	}
	if want := []string{"g1", "g1", "g1", "g1", "g1", "g2"}; !slices.Equal(bodies, want) {
		t.Fatalf("SendMessage bodies = %v, want %v", bodies, want)
	}
	last := got[len(got)-1]
	if last.at != t0.Add(time.Second) || last.in.MessageGroupId == nil || *last.in.MessageGroupId != "g" {
		t.Errorf("g2 sent at %v in group %v, want 1s in group g, when g1 was delivered", last.at.Sub(t0), last.in.MessageGroupId)
	}
}

// The journal of one FIFO group is the same on every run.
func TestFIFOGroupJournalIsStable(t *testing.T) {
	_, _, first := fifoGroup(t, 2)
	want := []string{"1 journal.Retrying", "2 journal.Retrying", "3 journal.Delivered", "1 journal.Delivered"}
	if !slices.Equal(first, want) {
		t.Fatalf("journal = %v, want %v", first, want)
	}
	for range 5 {
		if _, _, again := fifoGroup(t, 2); !slices.Equal(again, first) {
			t.Fatalf("journal = %v, then %v", first, again)
		}
	}
}

// Another group of the same queue is not held up by a retrying one.
func TestFIFOGroupsAreIndependent(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, failFirst(4))
		cause := source(k)
		t0 := k.Clock.Now()
		enqueue(k, o, toFIFO("g", "g1"), cause)
		enqueue(k, o, toFIFO("h", "h1"), cause)
		settled(k)
		var h []sent
		for _, s := range q.sent() {
			if s.in.MessageBody == "h1" {
				h = append(h, s)
			}
		}
		if len(h) != 1 || h[0].at != t0 {
			k.Errorf("h1 sent %+v, want once at the start while g1 retries", h)
		}
	})
}

// A message group of a standard queue (a fair queue's tenant label)
// promises no order (sqs.md 8.5): a retrying message does not hold up the
// next one of its group.
func TestStandardQueueGroupIsNotOrdered(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, failFirst(4))
		cause := source(k)
		t0 := k.Clock.Now()
		for _, body := range []string{"g1", "g2"} {
			enqueue(k, o, delivery.Request{
				Source:    topic(),
				Mechanism: delivery.SNSToSQS,
				Target:    delivery.SQSQueue{ARN: arn("sqs", "orders"), Body: body, GroupID: "g"},
				Policy:    delivery.SNSManaged{},
			}, cause)
		}
		settled(k)
		var g2 []sent
		for _, s := range q.sent() {
			if s.in.MessageBody == "g2" {
				g2 = append(g2, s)
			}
		}
		if len(g2) != 1 || g2[0].at != t0 {
			k.Errorf("g2 sent %+v, want once at the start while g1 retries", g2)
		}
	})
}

// A pending FIFO group survives a snapshot in order: Rearm arms only its
// first delivery.
func TestFIFOGroupSurvivesRearm(t *testing.T) {
	var sections []store.Section
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return throttled("") })
		cause := source(k)
		enqueue(k, o, toFIFO("g", "g1"), cause)
		enqueue(k, o, toFIFO("g", "g2"), cause)
		settled(k)
		var err error
		if sections, err = o.Export(); err != nil {
			k.Fatal(err)
		}
	})
	// A snapshot load restores the id streams; this kernel does not, so
	// another seed keeps g3's delivery id clear of the loaded ones.
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, failFirst(1))
		if err := o.Import(sections); err != nil {
			k.Fatal(err)
		}
		o.Rearm(k.Clock.Now())
		enqueue(k, o, toFIFO("g", "g3"), source(k))
		advance(k, time.Minute)
		bodies := make([]string, 0, 4)
		for _, s := range q.sent() {
			bodies = append(bodies, s.in.MessageBody)
		}
		if want := []string{"g1", "g1", "g2", "g3"}; !slices.Equal(bodies, want) {
			k.Errorf("SendMessage bodies = %v, want %v", bodies, want)
		}
	}, kerneltest.Seed(2))
}

// An EventBridge dead letter carries the failure as String message
// attributes (eb-rule-dlq), naming the retry limit that ran out.
func TestEventBridgeDeadLetterAttributes(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
			switch {
			case strings.HasSuffix(in.QueueUrl, "/events-dlq"):
				return nil
			case strings.HasSuffix(in.QueueUrl, "/gone"):
				return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
			}
			return throttled("")
		})
		rule := arn("events", "rule/nightly")
		dlq := arn("sqs", "events-dlq")
		toTarget := func(queue string, p delivery.EventBridgeTarget) delivery.Request {
			return delivery.Request{
				Source:    rule,
				Mechanism: delivery.EventsToSQS,
				Target:    delivery.SQSQueue{ARN: arn("sqs", queue), Body: `{"version":"0","detail":{}}`},
				Policy:    p,
				DLQ:       &dlq,
			}
		}
		cause := source(k)
		enqueue(k, o, toTarget("by-count", delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: 2}), cause)
		enqueue(k, o, toTarget("by-age", delivery.EventBridgeTarget{MaxAge: time.Minute, MaxAttempts: 185}), cause)
		enqueue(k, o, toTarget("gone", delivery.EventBridgeTarget{MaxAge: time.Hour, MaxAttempts: 185}), cause)
		settled(k)
		advance(k, time.Hour)

		attempts := map[string]int{}
		letters := map[string]map[string]string{}
		for _, s := range q.sent() {
			if s.queue != "events-dlq" {
				attempts[s.queue]++
				continue
			}
			if s.in.MessageBody != `{"version":"0","detail":{}}` {
				k.Errorf("dead letter body = %q, want the event", s.in.MessageBody)
			}
			attrs := map[string]string{}
			for name, v := range s.in.MessageAttributes {
				if v.DataType != "String" || v.StringValue == nil {
					k.Errorf("attribute %s = %+v, want a String", name, v)
					continue
				}
				attrs[name] = *v.StringValue
			}
			letters[attrs["TARGET_ARN"]] = attrs
		}
		want := map[string]map[string]string{
			"by-count": {"ERROR_CODE": "THROTTLING", "ERROR_MESSAGE": "slow down", "EXHAUSTED_RETRY_CONDITION": "MaximumRetryAttempts", "RETRY_ATTEMPTS": "2"},
			"by-age":   {"ERROR_CODE": "THROTTLING", "ERROR_MESSAGE": "slow down", "EXHAUSTED_RETRY_CONDITION": "MaximumEventAgeInSeconds", "RETRY_ATTEMPTS": strconv.Itoa(attempts["by-age"] - 1)},
			"gone":     {"ERROR_CODE": "NO_RESOURCE", "ERROR_MESSAGE": "The specified queue does not exist.", "RETRY_ATTEMPTS": "0"},
		}
		if len(letters) != len(want) {
			k.Fatalf("dead letters for %v, want one per target", slices.Sorted(maps.Keys(letters)))
		}
		for queue, w := range want {
			w["RULE_ARN"] = rule.String()
			w["TARGET_ARN"] = arn("sqs", queue).String()
			if got := letters[w["TARGET_ARN"]]; !maps.Equal(got, w) {
				k.Errorf("%s dead letter attributes = %v, want %v", queue, got, w)
			}
		}
		if attempts["by-age"] < 2 {
			k.Errorf("by-age made %d attempts, want retries until the minute ran out", attempts["by-age"])
		}
	})
}

func TestUnboundOutboxRefusesDeliveries(t *testing.T) {
	var unbound *outbox.UnboundError
	if err := outbox.New().Deliver(kernel.Delivery{Request: toQueue("q", delivery.None{})}); !errors.As(err, &unbound) {
		t.Errorf("Deliver = %v, want an *UnboundError", err)
	}
}
