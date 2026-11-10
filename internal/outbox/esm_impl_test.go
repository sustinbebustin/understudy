package outbox_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/ref"
)

func executionRole() aws.ARN {
	return aws.ARN{Partition: "aws", Service: "iam", Account: "000000000000", Resource: "role/service/worker-role"}
}

// onFailure is an event source mapping's on-failure record to a queue, as
// the stream poller hands it to the outbox, sent as role; a
// nil role leaves the sender out.
func onFailure(role *aws.ARN) delivery.Request {
	req := delivery.Request{
		Source:    arn("lambda", "event-source-mapping:6a1f0c4e-0000-4000-8000-000000000000"),
		Mechanism: delivery.ESMToSQS,
		Target:    delivery.SQSQueue{ARN: arn("sqs", "failures"), Body: `{"version":"1.0"}`},
		Policy:    delivery.None{},
	}
	if role != nil {
		req.Sender = &delivery.LambdaSender{Role: *role, Carries: delivery.DestinationRecord}
	}
	return req
}

// An on-failure record is sent as the mapping's execution role, under
// Via lambda-esm (lambda-stream-sources.md 5.6).
func TestESMToSQSSendsAsTheExecutionRole(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		role := executionRole()
		enqueue(k, o, onFailure(&role), source(k))
		settled(k)
		got := q.sent()
		if len(got) != 1 {
			k.Fatalf("%d sends, want 1", len(got))
		}
		p, assumed := got[0].principal.(identity.AssumedRole)
		if !assumed || p.Account.String() != role.Account || p.Role != (identity.RoleRef{Name: "worker-role", Path: "/service/"}) || got[0].origin != (ref.Internal{Via: ref.LambdaESM}) || got[0].in.MessageBody != `{"version":"1.0"}` {
			k.Errorf("sent as %v via %v with %q; want the execution role via lambda-esm with the record", identity.Render(got[0].principal), got[0].origin, got[0].in.MessageBody)
		}
	})
}

// A failed on-failure send is journaled and never retried: Policy None
// makes one attempt, and with no dead-letter queue the record is dropped.
func TestESMToSQSFailureIsNotRetried(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error {
			return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
		})
		role := executionRole()
		enqueue(k, o, onFailure(&role), source(k))
		settled(k)
		advance(k, 24*time.Hour)
		if n := len(q.sent()); n != 1 {
			k.Errorf("%d attempts, want 1", n)
		}
		events := deliveries(k)
		if len(events) != 1 {
			k.Fatalf("%d delivery events, want 1", len(events))
		}
		d := detail[journal.Delivery](k, events[0])
		if _, dropped := d.Outcome.(journal.Dropped); !dropped || d.Mechanism != delivery.ESMToSQS {
			k.Errorf("delivery %+v, want one dropped esm-to-sqs attempt", d)
		}
	})
}

// Enqueue refuses an on-failure record without a sender, a sender on any
// other mechanism, a sender role ARN that names no IAM role, and a
// dead-letter queue on an on-failure record; nothing is queued.
func TestEnqueueChecksTheESMSender(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		role := executionRole()
		notRole := arn("sqs", "q")
		withSender := toQueue("orders", delivery.SNSManaged{})
		withSender.Sender = &delivery.LambdaSender{Role: role, Carries: delivery.DestinationRecord}
		withDLQ := onFailure(&role)
		dlq := arn("sqs", "dlq")
		withDLQ.DLQ = &dlq
		for name, c := range map[string]struct {
			req   delivery.Request
			field string
		}{
			"no sender":    {onFailure(nil), "Sender"},
			"sns-to-sqs":   {withSender, "Sender"},
			"not a role":   {onFailure(&notRole), "Sender.Role"},
			"with the DLQ": {withDLQ, "DLQ"},
		} {
			_, err := o.Enqueue(c.req, ref.Cause{}, k.Clock.Now())
			var rerr *outbox.RequestError
			if !errors.As(err, &rerr) || rerr.Field != c.field {
				k.Errorf("%s: Enqueue = %v, want a *RequestError for %s", name, err, c.field)
			}
		}
		if r := o.Resources(k.Scope); len(r) != 0 {
			k.Errorf("Resources = %v, want nothing queued", r)
		}
	})
}
