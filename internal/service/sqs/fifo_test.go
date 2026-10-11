package sqs_test

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
)

// fifoQueue creates a FIFO queue with attrs and returns its URL.
func (h *harness) fifoQueue(name string, attrs map[sqsapi.QueueAttributeName]string) string {
	h.Helper()
	all := map[sqsapi.QueueAttributeName]string{"FifoQueue": "true"}
	maps.Copy(all, attrs)
	return h.queue(name, all)
}

// sendTo sends body to group with an optional deduplication id.
func (h *harness) sendTo(url, group, body string, dedup *string) *sqsapi.SendMessageOutput {
	h.Helper()
	out, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: body, MessageGroupId: &group, MessageDeduplicationId: dedup})
	if err != nil {
		h.Fatalf("SendMessage(%s, %s) = %s: %s", group, body, err.ID.QueryCode, err.Message)
	}
	return out
}

func bodies(ms []sqsapi.Message) []string {
	out := make([]string, len(ms))
	for i := range ms {
		out[i] = val(ms[i].Body)
	}
	return out
}

func (h *harness) receiveBodies(url string, limit int32) []string {
	h.Helper()
	return bodies(h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, MaxNumberOfMessages: &limit}))
}

func (h *harness) mustDelete(url string, m *sqsapi.Message) {
	h.Helper()
	if err := h.remove(&sqsapi.DeleteMessageInput{QueueUrl: url, ReceiptHandle: val(m.ReceiptHandle)}); err != nil {
		h.Fatalf("DeleteMessage(%s) = %s: %s", val(m.Body), err.ID.QueryCode, err.Message)
	}
}

func (h *harness) changeVisibility(url string, m *sqsapi.Message, vt int32) {
	h.Helper()
	if _, err := h.Invoke(h.Context(), sqsapi.OpChangeMessageVisibility, &sqsapi.ChangeMessageVisibilityInput{QueueUrl: url, ReceiptHandle: val(m.ReceiptHandle), VisibilityTimeout: vt}); err != nil {
		h.Fatalf("ChangeMessageVisibility(%s) = %s: %s", val(m.Body), err.ID.QueryCode, err.Message)
	}
}

// sqs.md 8.1: a receive drains one group in send order before the next; a
// group with a message in flight ahead of the rest yields nothing more
// until that message is deleted; an emptied group takes new sends.
func TestFIFOGroupsDeliverInOrderAndBlockWhileInFlight(t *testing.T) {
	run(t, func(h *harness) {
		url := h.fifoQueue("orders.fifo", map[sqsapi.QueueAttributeName]string{"ContentBasedDeduplication": "true"})
		h.sendTo(url, "g1", "g1-m1", nil)
		h.sendTo(url, "g2", "g2-m1", nil)
		h.sendTo(url, "g1", "g1-m2", nil)
		h.sendTo(url, "g1", "g1-m3", nil)
		got := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, MaxNumberOfMessages: new(int32(10))})
		if want := []string{"g1-m1", "g1-m2", "g1-m3", "g2-m1"}; !slices.Equal(bodies(got), want) {
			h.Fatalf("first receive = %q, want %q", bodies(got), want)
		}
		if again := h.receiveBodies(url, 10); len(again) != 0 {
			h.Fatalf("a receive while every group is in flight = %q, want none", again)
		}
		h.mustDelete(url, &got[0])
		if again := h.receiveBodies(url, 10); len(again) != 0 {
			h.Fatalf("after deleting g1-m1 only = %q, want none: g1-m2 still holds g1", again)
		}
		h.mustDelete(url, &got[3])
		h.sendTo(url, "g2", "g2-m2", nil)
		if again := h.receiveBodies(url, 10); !slices.Equal(again, []string{"g2-m2"}) {
			h.Fatalf("after g2 emptied = %q, want [g2-m2]", again)
		}
	})
}

// sqs.md 8.1 rule 3: delivery inside a group stops at the first message
// that is not receivable.
func TestFIFOGroupDeliveryStopsAtTheFirstHiddenMessage(t *testing.T) {
	run(t, func(h *harness) {
		url := h.fifoQueue("prefix.fifo", map[sqsapi.QueueAttributeName]string{"ContentBasedDeduplication": "true", "VisibilityTimeout": "1"})
		for _, b := range []string{"A", "B", "C"} {
			h.sendTo(url, "p", b, nil)
		}
		got := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, MaxNumberOfMessages: new(int32(10))})
		if len(got) != 3 {
			h.Fatalf("received %q, want A, B and C", bodies(got))
		}
		h.changeVisibility(url, &got[1], 4)
		h.advance(time.Second)
		if again := h.receiveBodies(url, 10); !slices.Equal(again, []string{"A"}) {
			h.Fatalf("with B still hidden = %q, want [A]", again)
		}
	})
}

// sqs.md 4 and 8.1: messages queued behind an in-flight head count as
// available; only received ones count as not visible.
func TestFIFOApproximateCounts(t *testing.T) {
	run(t, func(h *harness) {
		url := h.fifoQueue("counts.fifo", map[sqsapi.QueueAttributeName]string{"ContentBasedDeduplication": "true"})
		for _, g := range []string{"g1", "g2", "g3"} {
			h.sendTo(url, g, g+"-a", nil)
			h.sendTo(url, g, g+"-b", nil)
		}
		h.receiveBodies(url, 1)
		if got := h.receiveBodies(url, 1); !slices.Equal(got, []string{"g2-a"}) {
			h.Fatalf("second receive = %q, want the head of g2", got)
		}
		out, err := h.Invoke(h.Context(), sqsapi.OpGetQueueAttributes, &sqsapi.GetQueueAttributesInput{QueueUrl: url, AttributeNames: []sqsapi.QueueAttributeName{"All"}})
		if err != nil {
			h.Fatal(err.Message)
		}
		if a := out.Attributes; a["ApproximateNumberOfMessages"] != "4" || a["ApproximateNumberOfMessagesNotVisible"] != "2" {
			h.Errorf("counts = %s available, %s not visible; want 4 and 2", a["ApproximateNumberOfMessages"], a["ApproximateNumberOfMessagesNotVisible"])
		}
		if a := out.Attributes; a["FifoQueue"] != "true" || a["DeduplicationScope"] != "queue" || a["FifoThroughputLimit"] != "perQueue" || a["ContentBasedDeduplication"] != "true" {
			h.Errorf("FIFO attributes = %v", a)
		}
	})
}

// sqs.md 8.2: a duplicate within five minutes is acknowledged with the
// original's id and sequence number and not enqueued, even after the
// original was deleted; the window runs on the emulator clock.
func TestFIFODuplicatesAreSuppressedWithinTheWindow(t *testing.T) {
	run(t, func(h *harness) {
		url := h.fifoQueue("dedup.fifo", nil)
		first := h.sendTo(url, "g1", "one", new("d1"))
		dup := h.sendTo(url, "g2", "two", new("d1"))
		if val(dup.MessageId) != val(first.MessageId) || val(dup.SequenceNumber) != val(first.SequenceNumber) {
			h.Fatalf("duplicate = %s/%s, want the original %s/%s", val(dup.MessageId), val(dup.SequenceNumber), val(first.MessageId), val(first.SequenceNumber))
		}
		got := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, MaxNumberOfMessages: new(int32(10))})
		if !slices.Equal(bodies(got), []string{"one"}) {
			h.Fatalf("received %q, want the original only", bodies(got))
		}
		h.mustDelete(url, &got[0])
		if again := h.sendTo(url, "g1", "three", new("d1")); val(again.MessageId) != val(first.MessageId) {
			h.Fatalf("a duplicate after the delete = %s, want %s", val(again.MessageId), val(first.MessageId))
		}
		if none := h.receiveBodies(url, 10); len(none) != 0 {
			h.Fatalf("a suppressed duplicate was delivered: %q", none)
		}
		h.advance(5 * time.Minute)
		later := h.sendTo(url, "g1", "four", new("d1"))
		if val(later.MessageId) == val(first.MessageId) {
			h.Fatal("the deduplication id was still remembered after five minutes")
		}
		if val(later.SequenceNumber) <= val(first.SequenceNumber) || len(val(later.SequenceNumber)) != 20 {
			h.Errorf("SequenceNumber %s after %s, want a larger 20-digit number", val(later.SequenceNumber), val(first.SequenceNumber))
		}
	})
}

// sqs.md 8.2: content-based deduplication hashes the body alone, and an
// explicit id equal to that hash is the same id.
func TestFIFOContentBasedDeduplication(t *testing.T) {
	run(t, func(h *harness) {
		url := h.fifoQueue("content.fifo", map[sqsapi.QueueAttributeName]string{"ContentBasedDeduplication": "true"})
		first := h.sendTo(url, "g", "same", nil)
		if dup := h.sendTo(url, "g", "same", nil); val(dup.MessageId) != val(first.MessageId) {
			h.Errorf("the same body again = %s, want %s", val(dup.MessageId), val(first.MessageId))
		}
		sum := sha256.Sum256([]byte("same"))
		if dup := h.sendTo(url, "g", "other body", new(hex.EncodeToString(sum[:]))); val(dup.MessageId) != val(first.MessageId) {
			h.Errorf("an explicit id equal to the body hash = %s, want %s", val(dup.MessageId), val(first.MessageId))
		}
		msgs := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, MessageSystemAttributeNames: []sqsapi.MessageSystemAttributeName{"All"}})
		if len(msgs) != 1 {
			h.Fatalf("received %d messages, want 1", len(msgs))
		}
		a := msgs[0].Attributes
		if a["MessageDeduplicationId"] != hex.EncodeToString(sum[:]) || a["MessageGroupId"] != "g" || a["SequenceNumber"] != val(first.SequenceNumber) {
			h.Errorf("FIFO system attributes = %v", a)
		}
	})
}

// sqs.md 8.2: the deduplication scope is fixed when the queue is created;
// a later SetQueueAttributes changes only what GetQueueAttributes reports.
func TestFIFODeduplicationScopeIsFixedAtCreation(t *testing.T) {
	run(t, func(h *harness) {
		perGroup := h.fifoQueue("group-scope.fifo", map[sqsapi.QueueAttributeName]string{"DeduplicationScope": "messageGroup"})
		a := h.sendTo(perGroup, "g1", "a", new("same"))
		if b := h.sendTo(perGroup, "g2", "b", new("same")); val(b.MessageId) == val(a.MessageId) {
			h.Error("messageGroup scope suppressed the same id in another group")
		}
		perQueue := h.fifoQueue("queue-scope.fifo", nil)
		if _, err := h.Invoke(h.Context(), sqsapi.OpSetQueueAttributes, &sqsapi.SetQueueAttributesInput{QueueUrl: perQueue, Attributes: map[sqsapi.QueueAttributeName]string{"DeduplicationScope": "messageGroup"}}); err != nil {
			h.Fatal(err.Message)
		}
		x := h.sendTo(perQueue, "g1", "x", new("fresh"))
		if y := h.sendTo(perQueue, "g2", "y", new("fresh")); val(y.MessageId) != val(x.MessageId) {
			h.Error("a queue created with queue scope stopped suppressing across groups after SetQueueAttributes")
		}
	})
}

// sqs.md 2.4 and 6.1: what FIFO and standard queues refuse in a send.
func TestFIFOSendRules(t *testing.T) {
	run(t, func(h *harness) {
		url := h.fifoQueue("rules.fifo", nil)
		_, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b", MessageDeduplicationId: new("d")})
		wantError(h.T, err, "MissingParameter", "The request must contain the parameter MessageGroupId.")
		_, err = h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b", MessageGroupId: new("g")})
		wantError(h.T, err, "InvalidParameterValue", "The queue should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly")
		_, err = h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b", MessageGroupId: new("g"), MessageDeduplicationId: new("d"), DelaySeconds: new(int32(2))})
		wantError(h.T, err, "InvalidParameterValue", "Value 2 for parameter DelaySeconds is invalid. Reason: The request include parameter that is not valid for this queue type.")
		if _, err := h.send(&sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "b", MessageGroupId: new("g"), MessageDeduplicationId: new("d"), DelaySeconds: new(int32(0))}); err != nil {
			h.Errorf("DelaySeconds 0 on a FIFO queue = %s", err.Message)
		}
		standard := h.queue("plain", nil)
		_, err = h.send(&sqsapi.SendMessageInput{QueueUrl: standard, MessageBody: "b", MessageDeduplicationId: new("d")})
		wantError(h.T, err, "InvalidParameterValue", "Value d for parameter MessageDeduplicationId is invalid. Reason: The request include parameter that is not valid for this queue type.")
	})
}

// sqs.md 7.2: a FIFO receipt handle expires with its visibility timeout;
// extending the timeout first keeps it valid.
func TestFIFOReceiptHandlesExpireWithTheirVisibility(t *testing.T) {
	run(t, func(h *harness) {
		url := h.fifoQueue("expiry.fifo", map[sqsapi.QueueAttributeName]string{"ContentBasedDeduplication": "true", "VisibilityTimeout": "1"})
		h.sendTo(url, "g", "late", nil)
		got := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url})
		h.advance(2 * time.Second)
		handle := val(got[0].ReceiptHandle)
		err := h.remove(&sqsapi.DeleteMessageInput{QueueUrl: url, ReceiptHandle: handle})
		wantError(h.T, err, "InvalidParameterValue", "Value "+handle+" for parameter ReceiptHandle is invalid. Reason: The receipt handle has expired.")
		h.sendTo(url, "h", "extended", nil)
		ext := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, MaxNumberOfMessages: new(int32(10))})
		i := slices.IndexFunc(ext, func(m sqsapi.Message) bool { return val(m.Body) == "extended" })
		if i < 0 {
			h.Fatalf("received %q, want the extended message", bodies(ext))
		}
		h.changeVisibility(url, &ext[i], 10)
		h.advance(2 * time.Second)
		h.mustDelete(url, &ext[i])
	})
}

// sqs.md 8.3: a retry with the same ReceiveRequestAttemptId returns the
// same messages and receipt handles while none of them changed.
func TestFIFOReceiveRequestAttemptIDRetries(t *testing.T) {
	run(t, func(h *harness) {
		url := h.fifoQueue("attempts.fifo", map[sqsapi.QueueAttributeName]string{"ContentBasedDeduplication": "true"})
		h.sendTo(url, "g", "m1", nil)
		h.sendTo(url, "g", "m2", nil)
		attempt := &sqsapi.ReceiveMessageInput{QueueUrl: url, ReceiveRequestAttemptId: new("try-1")}
		first := h.mustReceive(attempt)
		retry := h.mustReceive(attempt)
		if len(first) != 1 || len(retry) != 1 || val(retry[0].ReceiptHandle) != val(first[0].ReceiptHandle) || val(retry[0].Body) != "m1" {
			h.Fatalf("retry = %+v, want the first attempt's message and handle %+v", retry, first)
		}
		h.changeVisibility(url, &first[0], 30)
		if after := h.mustReceive(attempt); len(after) != 0 {
			h.Fatalf("a retry after the message changed = %q, want a new attempt that finds g closed", bodies(after))
		}
		_, err := h.receive(&sqsapi.ReceiveMessageInput{QueueUrl: url, ReceiveRequestAttemptId: new("bad id")})
		wantError(h.T, err, "InvalidParameterValue", "Value bad id for parameter ReceiveRequestAttemptId is invalid. Reason: ReceiveRequestAttemptId can only include alphanumeric and punctuation characters. 1 to 128 in length.")
	})
}

// sqs.md 4: a FIFO queue's delay applies to messages already waiting.
func TestFIFODelayIsRetroactive(t *testing.T) {
	run(t, func(h *harness) {
		url := h.fifoQueue("delay.fifo", map[sqsapi.QueueAttributeName]string{"ContentBasedDeduplication": "true"})
		h.Clock.Freeze()
		h.sendTo(url, "g", "waiting", nil)
		if _, err := h.Invoke(h.Context(), sqsapi.OpSetQueueAttributes, &sqsapi.SetQueueAttributesInput{QueueUrl: url, Attributes: map[sqsapi.QueueAttributeName]string{"DelaySeconds": "5"}}); err != nil {
			h.Fatal(err.Message)
		}
		if got := h.receiveBodies(url, 1); len(got) != 0 {
			h.Fatalf("received %q during the new delay", got)
		}
		h.advance(5 * time.Second)
		if got := h.receiveBodies(url, 1); !slices.Equal(got, []string{"waiting"}) {
			h.Fatalf("after the delay = %q", got)
		}
	})
}
