package outbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// An events-to-events dead letter is the forwarded event's envelope
// (eb-rule-dlq.html: the event the target failed to receive), so a delivery
// whose entry has none has no dead letter to send. Enqueue and a snapshot
// load refuse such a delivery (checkForwarding); one that reaches its dead
// letter anyway is dropped with a reason naming the missing envelope, and
// the queue receives nothing rather than an empty or made-up body.
func TestEventsToEventsDeadLetterWithoutAnEnvelopeIsDropped(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		var sent calls[string]
		k.Stub(sqsapi.OpSendMessage, func(_ context.Context, _ *kernel.Call, in *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
			sent.add(in.MessageBody)
			return &sqsapi.SendMessageOutput{MessageId: new("m")}, nil
		})
		k.Stub(eventbridgeapi.OpPutEvents, func(_ context.Context, _ *kernel.Call, in *eventbridgeapi.PutEventsInput) (*eventbridgeapi.PutEventsOutput, *awserr.Error) {
			out := &eventbridgeapi.PutEventsOutput{}
			for range in.Entries {
				out.FailedEntryCount++
				out.Entries = append(out.Entries, eventbridgeapi.PutEventsResultEntry{ErrorCode: new("InternalFailure"), ErrorMessage: new("the stub failed it")})
			}
			return out, nil
		})
		o := New()
		if err := o.Bind(k.Kernel, Options{Faults: k.Faults}); err != nil {
			k.Fatal(err)
		}
		rule := aws.ARN{Partition: "aws", Service: "events", Region: "us-east-1", Account: "000000000000", Resource: "rule/forward"}
		dlq := aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Account: "000000000000", Resource: "dlq"}
		plant(k, o, delivery.Request{
			Source: rule, Mechanism: delivery.EventsToEvents, Target: busCode(""),
			Policy: delivery.EventBridgeTarget{MaxAge: time.Hour, MaxAttempts: 0}, DLQ: &dlq,
		})
		if p, err := k.Kernel.Settle(k.Context(), time.Minute); err != nil {
			k.Fatalf("Settle = %v, %v", p, err)
		}
		if got := sent.all(); len(got) != 0 {
			k.Errorf("dead letters %q, want none", got)
		}
		events := k.Journal.Query(journal.Filter{Kind: journal.KindDelivery, Limit: 100}).Events
		if len(events) != 1 {
			k.Fatalf("delivery events = %+v, want one", events)
		}
		d, ok := events[0].Detail.(journal.Delivery)
		dropped, isDrop := d.Outcome.(journal.Dropped)
		if !ok || !isDrop || !strings.Contains(dropped.Reason, delivery.ErrNotForwarded.Error()) || !strings.Contains(dropped.Reason, dlq.String()) {
			k.Errorf("delivery = %#v, want dropped naming the missing envelope and %s", events[0].Detail, dlq)
		}
	}, kerneltest.Services(eventbridgeapi.Service(), sqsapi.Service()))
}
