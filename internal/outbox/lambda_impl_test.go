package outbox_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
)

// A Delivery-phase Drop rule on a Lambda delivery loses the record as a
// failed delivery does: the journal says the rule dropped it and that
// Lambda counts it in DestinationDeliveryFailures
// (lambda-destinations-layers.md 4.6).
func TestDropRuleOnALambdaDeliveryCountsTheMetric(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		rule, rerr := k.Faults.Add(fault.Rule{
			Match:   fault.Match{Mechanisms: []delivery.Mechanism{delivery.LambdaToSQS}},
			Phase:   fault.Delivery,
			Action:  fault.DropDelivery{},
			Trigger: fault.NextN{N: 1},
		})
		if rerr != nil {
			k.Fatal(rerr)
		}
		enqueue(k, o, fromWorker(delivery.LambdaToSQS, delivery.SQSQueue{ARN: arn("sqs", "records"), Body: "{}"}, delivery.DestinationRecord), source(k))
		settled(k)
		if n := len(q.sent()); n != 0 {
			k.Errorf("%d sends, want none", n)
		}
		reason := dropped(k)
		if want := "fault rule " + string(rule) + " dropped the attempt" + recordClause; reason != want {
			k.Errorf("reason = %q, want %q", reason, want)
		}
	})
}

// A delivery that is not Lambda's keeps the outbox's reason alone: the
// metric clauses belong to Lambda's deliveries.
func TestOtherDeliveriesGetNoLambdaMetricClause(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error {
			return sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
		})
		enqueue(k, o, toQueue("gone", delivery.SNSManaged{}), source(k))
		settled(k)
		if reason := dropped(k); strings.Contains(reason, "; Lambda ") {
			k.Errorf("reason = %q, want no Lambda metric clause", reason)
		}
	})
}
