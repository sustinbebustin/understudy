package sqs_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

func (h *harness) sendBatch(url string, entries ...sqsapi.SendMessageBatchRequestEntry) (*sqsapi.SendMessageBatchOutput, *awserr.Error) {
	return h.Invoke(h.Context(), sqsapi.OpSendMessageBatch, &sqsapi.SendMessageBatchInput{QueueUrl: url, Entries: entries})
}

func entry(id, body string) sqsapi.SendMessageBatchRequestEntry {
	return sqsapi.SendMessageBatchRequestEntry{Id: id, MessageBody: body}
}

// failure renders a Failed entry for comparison.
func failure(f sqsapi.BatchResultErrorEntry) string {
	return f.Id + " " + strconv.FormatBool(f.SenderFault) + " " + f.Code + " " + val(f.Message)
}

// sqs.md 6.2: what fails the whole batch request.
func TestBatchRequestLimits(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("batches", nil)
		eleven := make([]sqsapi.SendMessageBatchRequestEntry, 11)
		for i := range eleven {
			eleven[i] = entry("e"+strconv.Itoa(i), "b")
		}
		for _, tt := range []struct {
			name          string
			entries       []sqsapi.SendMessageBatchRequestEntry
			code, message string
		}{
			{"empty", []sqsapi.SendMessageBatchRequestEntry{}, "AWS.SimpleQueueService.EmptyBatchRequest", "There should be at least one SendMessageBatchRequestEntry in the request."},
			{"eleven", eleven, "AWS.SimpleQueueService.TooManyEntriesInBatchRequest", "Maximum number of entries per request are 10. You have sent 11."},
			{"bad id", []sqsapi.SendMessageBatchRequestEntry{entry("invalid:id", "b")}, "AWS.SimpleQueueService.InvalidBatchEntryId", "A batch entry id can only contain alphanumeric characters, hyphens and underscores. It can be at most 80 letters long."},
			{"long id", []sqsapi.SendMessageBatchRequestEntry{entry(strings.Repeat("i", 81), "b")}, "AWS.SimpleQueueService.InvalidBatchEntryId", "A batch entry id can only contain alphanumeric characters, hyphens and underscores. It can be at most 80 letters long."},
			{"repeated id", []sqsapi.SendMessageBatchRequestEntry{entry("a", "b"), entry("a", "c")}, "AWS.SimpleQueueService.BatchEntryIdsNotDistinct", "Id a repeated."},
			{"too long", []sqsapi.SendMessageBatchRequestEntry{entry("a", strings.Repeat("x", 1<<19)), entry("b", strings.Repeat("x", 1<<19+1))}, "AWS.SimpleQueueService.BatchRequestTooLong", "Batch requests cannot be longer than 1048576 bytes. You have sent 1048577 bytes."},
		} {
			_, err := h.sendBatch(url, tt.entries...)
			if err == nil || err.ID.QueryCode != tt.code || err.Message != tt.message {
				h.Errorf("%s: error = %v, want %s %q", tt.name, err, tt.code, tt.message)
			}
		}
		_, err := h.Invoke(h.Context(), sqsapi.OpDeleteMessageBatch, &sqsapi.DeleteMessageBatchInput{QueueUrl: url, Entries: []sqsapi.DeleteMessageBatchRequestEntry{}})
		wantError(h.T, err, "AWS.SimpleQueueService.EmptyBatchRequest", "There should be at least one DeleteMessageBatchRequestEntry in the request.")
		_, err = h.Invoke(h.Context(), sqsapi.OpChangeMessageVisibilityBatch, &sqsapi.ChangeMessageVisibilityBatchInput{QueueUrl: url, Entries: []sqsapi.ChangeMessageVisibilityBatchRequestEntry{{Id: "a", ReceiptHandle: "h"}, {Id: "a", ReceiptHandle: "h"}}})
		wantError(h.T, err, "AWS.SimpleQueueService.BatchEntryIdsNotDistinct", "Id a repeated.")
	})
}

// sqs.md 6.2: an entry over the queue's MaximumMessageSize or with a bad
// body fails alone; the others are sent, with the digests of a single send.
func TestSendMessageBatchFailsEntriesAlone(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("entries", map[sqsapi.QueueAttributeName]string{"MaximumMessageSize": "1024"})
		x := "x"
		out, err := h.sendBatch(url,
			sqsapi.SendMessageBatchRequestEntry{Id: "ok", MessageBody: "message", MessageAttributes: map[string]sqsapi.MessageAttributeValue{"k": {DataType: "String", StringValue: &x}}},
			entry("big", strings.Repeat("b", 1025)),
			entry("ctrl", "a\x01b"),
			sqsapi.SendMessageBatchRequestEntry{Id: "delay", MessageBody: "m", DelaySeconds: new(int32(901))},
			entry("late", "later"),
		)
		if err != nil {
			h.Fatal(err.Message)
		}
		if len(out.Successful) != 2 || out.Successful[0].Id != "ok" || out.Successful[1].Id != "late" {
			h.Fatalf("Successful = %+v", out.Successful)
		}
		if s := out.Successful[0]; s.MD5OfMessageBody != "78e731027d8fd50ed642340b7c9a63b3" || val(s.MD5OfMessageAttributes) != "a45daa70926828a2f0a1db3418e6feb4" || s.SequenceNumber != nil {
			h.Errorf("digests = %+v", s)
		}
		want := []string{
			"big true InvalidParameterValue One or more parameters cannot be validated. Reason: Message must be shorter than 1024 bytes.",
			"ctrl true InvalidMessageContents Invalid characters found. Valid unicode characters are #x9 | #xA | #xD | #x20 to #xD7FF | #xE000 to #xFFFD | #x10000 to #x10FFFF",
			// SendMessage's text, testdata/parity/sqs/R11/02-send-delay-901.json
			"delay true InvalidParameterValue Value 901 for parameter DelaySeconds is invalid. Reason: DelaySeconds must be >= 0 and <= 900.",
		}
		if len(out.Failed) != len(want) {
			h.Fatalf("Failed = %+v", out.Failed)
		}
		for i, f := range out.Failed {
			if failure(f) != want[i] {
				h.Errorf("Failed[%d] = %s\nwant %s", i, failure(f), want[i])
			}
		}
		if got := h.receiveBodies(url, 10); len(got) != 2 {
			h.Errorf("queued %q, want the two good entries", got)
		}
	})
}

// sqs.md 6.2: a FIFO batch entry without a MessageGroupId fails the whole
// request, under InvalidParameterValue rather than the single send's code.
func TestFIFOBatchWithoutAGroupFailsWhole(t *testing.T) {
	run(t, func(h *harness) {
		url := h.fifoQueue("batch.fifo", map[sqsapi.QueueAttributeName]string{"ContentBasedDeduplication": "true"})
		_, err := h.sendBatch(url, sqsapi.SendMessageBatchRequestEntry{Id: "a", MessageBody: "a", MessageGroupId: new("g")}, entry("b", "b"))
		wantError(h.T, err, "InvalidParameterValue", "The request must contain the parameter MessageGroupId.")
		out, err := h.sendBatch(url,
			sqsapi.SendMessageBatchRequestEntry{Id: "a", MessageBody: "first", MessageGroupId: new("g")},
			sqsapi.SendMessageBatchRequestEntry{Id: "b", MessageBody: "second", MessageGroupId: new("g")})
		if err != nil || len(out.Successful) != 2 || val(out.Successful[0].SequenceNumber) >= val(out.Successful[1].SequenceNumber) {
			h.Fatalf("FIFO batch = %+v, %v; want two entries in order", out, err)
		}
		if got := h.receiveBodies(url, 10); strings.Join(got, ",") != "first,second" {
			h.Errorf("received %q, want the batch order", got)
		}
	})
}

// sqs.md 6.2 and 7.2: DeleteMessageBatch and ChangeMessageVisibilityBatch
// report each entry on its own.
func TestDeleteAndChangeVisibilityBatches(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("handles-batch", nil)
		h.mustSend(url, "one")
		h.mustSend(url, "two")
		got := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url, MaxNumberOfMessages: new(int32(10))})
		if len(got) != 2 {
			h.Fatalf("received %d messages", len(got))
		}
		change, err := h.Invoke(h.Context(), sqsapi.OpChangeMessageVisibilityBatch, &sqsapi.ChangeMessageVisibilityBatchInput{QueueUrl: url, Entries: []sqsapi.ChangeMessageVisibilityBatchRequestEntry{
			{Id: "zero", ReceiptHandle: val(got[0].ReceiptHandle), VisibilityTimeout: new(int32(0))},
			{Id: "range", ReceiptHandle: val(got[1].ReceiptHandle), VisibilityTimeout: new(int32(43201))},
			{Id: "garbage", ReceiptHandle: "garbage", VisibilityTimeout: new(int32(5))},
		}})
		if err != nil {
			h.Fatal(err.Message)
		}
		if len(change.Successful) != 1 || change.Successful[0].Id != "zero" || len(change.Failed) != 2 {
			h.Fatalf("ChangeMessageVisibilityBatch = %+v", change)
		}
		if f := failure(change.Failed[0]); f != "range true InvalidParameterValue Value 43201 for parameter VisibilityTimeout is invalid. Reason: Must be >= 0 and <= 43200, if provided." {
			h.Errorf("Failed[0] = %s", f)
		}
		if f := failure(change.Failed[1]); f != `garbage true ReceiptHandleIsInvalid The input receipt handle "garbage" is not a valid receipt handle.` {
			h.Errorf("Failed[1] = %s", f)
		}
		again := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url})
		if len(again) != 1 || val(again[0].Body) != "one" {
			h.Fatalf("after a zero timeout = %q, want one", bodies(again))
		}
		del, err := h.Invoke(h.Context(), sqsapi.OpDeleteMessageBatch, &sqsapi.DeleteMessageBatchInput{QueueUrl: url, Entries: []sqsapi.DeleteMessageBatchRequestEntry{
			{Id: "current", ReceiptHandle: val(again[0].ReceiptHandle)},
			{Id: "stale", ReceiptHandle: val(got[0].ReceiptHandle)},
			{Id: "bad", ReceiptHandle: "garbage"},
		}})
		if err != nil {
			h.Fatal(err.Message)
		}
		if len(del.Successful) != 2 || len(del.Failed) != 1 || del.Failed[0].Code != "ReceiptHandleIsInvalid" {
			h.Fatalf("DeleteMessageBatch = %+v", del)
		}
		gone, err := h.Invoke(h.Context(), sqsapi.OpChangeMessageVisibilityBatch, &sqsapi.ChangeMessageVisibilityBatchInput{QueueUrl: url, Entries: []sqsapi.ChangeMessageVisibilityBatchRequestEntry{
			{Id: "deleted", ReceiptHandle: val(again[0].ReceiptHandle), VisibilityTimeout: new(int32(5))},
		}})
		if err != nil || len(gone.Failed) != 1 || failure(gone.Failed[0]) != "deleted true ReceiptHandleIsInvalid Message does not exist or is not available for visibility timeout change" {
			h.Fatalf("changing a deleted message = %+v, %v", gone, err)
		}
	})
}
