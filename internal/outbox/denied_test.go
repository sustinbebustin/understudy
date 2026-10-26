package outbox_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
)

// A delivery the target's policy refuses (an attempt answered with an
// authorization denial, awserr.Denial) is not retried: "Amazon SNS doesn't
// retry the message delivery that fails because of a client-side error"
// and discards it "unless a dead-letter queue is attached"
// (iam-enforcement-surfaces.md 8.2, aws-doc); for "missing permissions to a
// target ... no retry attempts will happen ... EventBridge sends these
// events directly to the target DLQ" (8.4, aws-doc). The attempt is
// journaled dead-lettered or dropped (12.4).

// denied is the answer a target gives a delivery its policy refuses.
func denied(string) *awserr.Error {
	return &awserr.Error{ID: sqsapi.DenialError(), Message: "denied", Origin: awserr.Denial{}}
}

func TestADeniedDeliveryIsNotRetried(t *testing.T) {
	cases := []struct {
		name   string
		req    func() delivery.Request
		dlq    bool
		letter journal.DeliveryOutcome
	}{
		{"SNS without a dead-letter queue", func() delivery.Request { return toQueue("refused", delivery.SNSManaged{}) }, false, journal.Dropped{}},
		{"SNS with a dead-letter queue", func() delivery.Request {
			r := toQueue("refused", delivery.SNSManaged{})
			dlq := arn("sqs", "dlq")
			r.DLQ = &dlq
			return r
		}, true, journal.DeadLettered{DLQ: arn("sqs", "dlq")}},
		{"EventBridge with a dead-letter queue", func() delivery.Request {
			dlq := arn("sqs", "dlq")
			return delivery.Request{
				Source: arn("events", "rule/nightly"), Mechanism: delivery.EventsToSQS,
				Target: delivery.SQSQueue{ARN: arn("sqs", "refused"), Body: `{"version":"0","detail":{}}`},
				Policy: delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: 185}, DLQ: &dlq,
			}
		}, true, journal.DeadLettered{DLQ: arn("sqs", "dlq")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run(t, func(k *kerneltest.K, o *outbox.Outbox) {
				q := stubQueues(k, func(_ int, in *sqsapi.SendMessageInput) *awserr.Error {
					if in.QueueUrl[len(in.QueueUrl)-len("refused"):] == "refused" {
						return denied("")
					}
					return nil
				})
				enqueue(k, o, c.req(), source(k))
				settled(k)
				advance(k, time.Hour)

				attempts, letters := 0, 0
				for _, s := range q.sent() {
					switch s.queue {
					case "refused":
						attempts++
					case "dlq":
						letters++
					}
				}
				wantLetters := 0
				if c.dlq {
					wantLetters = 1
				}
				if attempts != 1 || letters != wantLetters {
					k.Errorf("attempts %d, dead letters %d; want one attempt and %d dead letters", attempts, letters, wantLetters)
				}
				events := deliveries(k)
				if len(events) != 1 {
					k.Fatalf("delivery events = %+v, want one", events)
				}
				if got := detail[journal.Delivery](k, events[0]).Outcome; !sameOutcome(got, c.letter) {
					k.Errorf("delivery outcome = %#v, want %#v", got, c.letter)
				}
			})
		})
	}
}

// sameOutcome compares the variant, and a dead letter's queue.
func sameOutcome(got, want journal.DeliveryOutcome) bool {
	switch w := want.(type) {
	case journal.DeadLettered:
		g, ok := got.(journal.DeadLettered)
		return ok && g.DLQ == w.DLQ
	case journal.Dropped:
		_, ok := got.(journal.Dropped)
		return ok
	case journal.Delivered, journal.Filtered, journal.Refused, journal.Retrying:
	}
	return false
}
