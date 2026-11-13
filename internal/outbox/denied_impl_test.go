package outbox_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
)

// modeCall is one SendMessage a mode stub received: the queue's name, the
// call's principal mode and its message attributes.
type modeCall struct {
	queue      string
	mode       kernel.PrincipalMode
	attributes map[string]sqsapi.MessageAttributeValue
}

// stubModes stubs SendMessage, recording each call's mode; answer decides
// each call's error by queue name.
func stubModes(k *kerneltest.K, answer func(queue string) *awserr.Error) *recorder[modeCall] {
	var r recorder[modeCall]
	k.Stub(sqsapi.OpSendMessage, func(_ context.Context, c *kernel.Call, in *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
		name := in.QueueUrl[strings.LastIndexByte(in.QueueUrl, '/')+1:]
		r.add(modeCall{queue: name, mode: c.Mode(), attributes: in.MessageAttributes})
		if err := answer(name); err != nil {
			return nil, err
		}
		id := "m"
		return &sqsapi.SendMessageOutput{MessageId: &id}, nil
	})
	return &r
}

// Every call of an attempt, a dead letter included, runs as the delivering
// service with the aws:SourceAccount the request carries, so pipeline step
// 8 decides it with the key AWS sends (iam-enforcement-surfaces.md 8.1,
// 12.4).
func TestEveryCallOfADeliveryCarriesItsSourceAccount(t *testing.T) {
	account, err := aws.ParseAccountID("000000000000")
	if err != nil {
		t.Fatal(err)
	}
	for _, sends := range []aws.AccountID{account, {}} {
		t.Run("source account "+sends.String(), func(t *testing.T) {
			run(t, func(k *kerneltest.K, o *outbox.Outbox) {
				calls := stubModes(k, func(queue string) *awserr.Error {
					if queue == "refused" {
						return denied("")
					}
					return nil
				})
				req := toQueue("refused", delivery.SNSManaged{})
				req.SourceAccount = sends
				dlq := arn("sqs", "dlq")
				req.DLQ = &dlq
				enqueue(k, o, req, source(k))
				settled(k)
				advance(k, time.Hour)

				want := kernel.AsService{Name: "sns.amazonaws.com", Source: topic(), SourceAccount: sends}
				got := calls.all()
				if len(got) != 2 || got[0].queue != "refused" || got[1].queue != "dlq" {
					k.Fatalf("calls = %+v, want one attempt and one dead letter", got)
				}
				for _, c := range got {
					if c.mode != want {
						k.Errorf("the call to %s ran as %#v, want %#v", c.queue, c.mode, want)
					}
				}
			})
		})
	}
}

// A refusal by the target's policy is not retried whatever status its
// denial identity carries, since AWS treats a missing permission as a
// client-side error (iam-enforcement-surfaces.md 8.2, 8.4), and an
// EventBridge dead letter of it carries ERROR_CODE NO_PERMISSIONS (switch
// events.delivery.denied-code) even when the identity's name says nothing
// of access, and no retry attributes (aws-recorded,
// testdata/parity/sqs/IAM-ENF/22-dlq-sa.json).
func TestARefusalIsNotRetriedWhateverItsStatus(t *testing.T) {
	refusal := &awserr.Error{
		ID:      awserr.Identity{Shape: "test#Refused", Status: 503, Fault: awserr.Receiver},
		Message: "refused by policy",
		Origin:  awserr.Denial{},
	}
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		calls := stubModes(k, func(queue string) *awserr.Error {
			if queue == "refused" {
				return refusal
			}
			return nil
		})
		dlq := arn("sqs", "dlq")
		enqueue(k, o, delivery.Request{
			Source: arn("events", "rule/nightly"), Mechanism: delivery.EventsToSQS,
			Target: delivery.SQSQueue{ARN: arn("sqs", "refused"), Body: `{"version":"0","detail":{}}`},
			Policy: delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: 185}, DLQ: &dlq,
		}, source(k))
		settled(k)
		advance(k, time.Hour)

		got := calls.all()
		if len(got) != 2 || got[0].queue != "refused" || got[1].queue != "dlq" {
			k.Fatalf("calls = %+v, want one attempt, never retried, and one dead letter", got)
		}
		attr := func(name string) string {
			v, ok := got[1].attributes[name]
			if !ok || v.StringValue == nil {
				return ""
			}
			return *v.StringValue
		}
		if attr("ERROR_CODE") != delivery.EventBridgeDeniedCode || !strings.HasPrefix(attr("ERROR_MESSAGE"), "refused by policy (Service: AmazonSQS; ") {
			k.Errorf("dead letter attributes = %+v, want ERROR_CODE %s and the denial's message with SQS's client suffix", got[1].attributes, delivery.EventBridgeDeniedCode)
		}
		for _, name := range []string{"RETRY_ATTEMPTS", "EXHAUSTED_RETRY_CONDITION"} {
			if _, ok := got[1].attributes[name]; ok {
				k.Errorf("dead letter attributes = %+v, want no %s: a refusal's dead letter carries none (22-dlq-sa.json)", got[1].attributes, name)
			}
		}
		events := deliveries(k)
		if len(events) != 1 {
			k.Fatalf("delivery events = %+v, want one", events)
		}
		if got := detail[journal.Delivery](k, events[0]).Outcome; got != (journal.DeadLettered{DLQ: dlq}) {
			k.Errorf("delivery outcome = %#v, want dead-lettered to %s", got, dlq)
		}
	})
}
