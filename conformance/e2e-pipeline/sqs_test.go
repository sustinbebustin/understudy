package e2e_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// TestSQSReportedFailuresRedriveToTheDLQ publishes a good and a bad
// message to the Go worker, which reports the bad one in
// batchItemFailures. The good one is deleted after one delivery; the bad
// one returns after each 60 second visibility timeout, is delivered
// exactly maxReceiveCount (3) times, and the receive that would be the
// fourth moves it to the dead-letter queue with its id, body and
// attributes, DeadLetterQueueSourceArn and a receive count of 4
// (sqs.md 8, lambda-events.md 2.3).
func TestSQSReportedFailuresRedriveToTheDLQ(t *testing.T) {
	p := pipe(t)
	w := p.workers[goWorker]
	good, bad := newID(t), newID(t)
	publish(t, message{ID: good}, map[string]string{"kind": "order"})
	badPub := publish(t, message{ID: bad, Fail: true}, map[string]string{"kind": "order"})
	maxReceive := spec(goWorker).maxReceive

	waitProcessed(t, goWorker, good)
	waitLines(t, lambdaLogGroup(goWorker), eventPattern("failed", bad), 1)
	for attempt := 2; attempt <= maxReceive; attempt++ {
		advance(t, visibilityOf(goWorker))
		waitLines(t, lambdaLogGroup(goWorker), eventPattern("failed", bad), attempt)
	}
	advance(t, visibilityOf(goWorker))

	var dead *sqstypes.Message
	eventually(t, "the bad message reached "+w.dlq.arn, func() (bool, string) {
		out, err := env.sqs.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(w.dlq.url), MaxNumberOfMessages: 10,
			MessageAttributeNames:       []string{"All"},
			MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameAll},
		})
		if err != nil {
			t.Fatalf("sqs ReceiveMessage %s: %v", w.dlq.url, err)
		}
		for _, m := range out.Messages {
			if aws.ToString(m.Body) == badPub.Body {
				dead = &m
			}
		}
		return dead != nil, fmt.Sprintf("received %d messages, none with the bad body", len(out.Messages))
	})

	failed, _ := filterLines(t, lambdaLogGroup(goWorker), eventPattern("failed", bad))
	var counts []string
	for _, l := range failed {
		counts = append(counts, l.ReceiveCount)
	}
	slices.Sort(counts)
	if !slices.Equal(counts, []string{"1", "2", "3"}) {
		t.Errorf("the worker saw the bad message with receive counts %v, want exactly [1 2 3] for maxReceiveCount %d", counts, maxReceive)
	}
	if len(failed) > 0 && aws.ToString(dead.MessageId) != failed[0].MessageID {
		t.Errorf("DLQ MessageId %s, want the source queue's %s", aws.ToString(dead.MessageId), failed[0].MessageID)
	}
	if got := dead.Attributes["DeadLetterQueueSourceArn"]; got != w.queue.arn {
		t.Errorf("DeadLetterQueueSourceArn %q, want %s", got, w.queue.arn)
	}
	if got := dead.Attributes["ApproximateReceiveCount"]; got != strconv.Itoa(maxReceive+1) {
		t.Errorf("ApproximateReceiveCount on the first DLQ receive %q, want %d (it carries over)", got, maxReceive+1)
	}
	if kind, ok := dead.MessageAttributes["kind"]; !ok || aws.ToString(kind.StringValue) != "order" || aws.ToString(kind.DataType) != "String" {
		t.Errorf("DLQ message attribute kind = %+v, want String order", dead.MessageAttributes["kind"])
	}
	if _, err := env.sqs.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(w.dlq.url), ReceiptHandle: dead.ReceiptHandle}); err != nil {
		t.Errorf("sqs DeleteMessage on the DLQ: %v", err)
	}

	goodLines, _ := filterLines(t, lambdaLogGroup(goWorker), eventPattern("processed", good))
	if len(goodLines) != 1 || goodLines[0].ReceiveCount != "1" {
		t.Errorf("the good message was processed %d times (%+v), want once on its first receive", len(goodLines), goodLines)
	}
}

// TestSQSQueueAttributesKeepTheRedrivePolicy reads each worker queue's
// attributes: the visibility timeout as set, and the redrive policy with
// maxReceiveCount as a JSON number (sqs.md 8).
func TestSQSQueueAttributesKeepTheRedrivePolicy(t *testing.T) {
	p := pipe(t)
	for _, sp := range specs {
		w := p.workers[sp.name]
		out, err := env.sqs.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{
			QueueUrl: aws.String(w.queue.url), AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameAll},
		})
		if err != nil {
			t.Errorf("sqs GetQueueAttributes %s: %v", w.queue.url, err)
			continue
		}
		a := out.Attributes
		if a["VisibilityTimeout"] != strconv.Itoa(sp.visibility) || a["QueueArn"] != w.queue.arn || a["Policy"] == "" {
			t.Errorf("%s: VisibilityTimeout %q, QueueArn %q, Policy %q; want %d, %s and the SNS policy", sp.name, a["VisibilityTimeout"], a["QueueArn"], a["Policy"], sp.visibility, w.queue.arn)
		}
		var rp map[string]json.RawMessage
		if err := json.Unmarshal([]byte(a["RedrivePolicy"]), &rp); err != nil {
			t.Errorf("%s: RedrivePolicy %q is not JSON: %v", sp.name, a["RedrivePolicy"], err)
			continue
		}
		if string(rp["maxReceiveCount"]) != strconv.Itoa(sp.maxReceive) || string(rp["deadLetterTargetArn"]) != fmt.Sprintf("%q", w.dlq.arn) {
			t.Errorf("%s: RedrivePolicy %s, want deadLetterTargetArn %s and maxReceiveCount the number %d", sp.name, a["RedrivePolicy"], w.dlq.arn, sp.maxReceive)
		}
	}
}
