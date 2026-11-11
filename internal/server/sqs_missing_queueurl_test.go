package server_test

import (
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
)

// sqsJSONOperationsWithQueueURL are the wire operations whose input requires
// QueueUrl.
var sqsJSONOperationsWithQueueURL = []string{
	"AddPermission", "ChangeMessageVisibility", "ChangeMessageVisibilityBatch",
	"DeleteMessage", "DeleteMessageBatch", "DeleteQueue", "GetQueueAttributes",
	"ListDeadLetterSourceQueues", "ListQueueTags", "PurgeQueue", "ReceiveMessage",
	"RemovePermission", "SendMessage", "SendMessageBatch", "SetQueueAttributes",
	"TagQueue", "UntagQueue",
}

// An SQS JSON request without QueueUrl answers as an unknown queue does,
// recorded for ReceiveMessage with body {} (IAM-ENF-13,
// testdata/parity/sqs/IAM-ENF/13-sqs-noqueue.json, 2026-11-03): 400,
// x-amzn-query-error AWS.SimpleQueueService.NonExistentQueue;Sender, and a
// body with __type com.amazonaws.sqs#QueueDoesNotExist and the key "message".
// The answer comes before authorization, so it holds under enforcement for a
// session that holds no policy. Every other operation taking QueueUrl is
// inferred to answer the same (the overlay labels it so).
func TestSQSMissingQueueURLAnswersQueueDoesNotExist(t *testing.T) {
	for _, mode := range []server.IAMEnforce{server.IAMEnforceOff, server.IAMEnforceEnforce} {
		_, endpoint, u := enforced(t, mode)
		s := u
		if mode == server.IAMEnforceEnforce {
			s = sessionOf(t, endpoint, u)
		}
		for _, op := range sqsJSONOperationsWithQueueURL {
			w := jsonCall(t, s, endpoint, "sqs", "1.0", "AmazonSQS."+op, `{}`)
			if w.status != http.StatusBadRequest {
				t.Errorf("iam=%v %s: status = %d, want 400 (%s)", mode, op, w.status, w.body)
			}
			if got, want := w.header.Get("X-Amzn-Query-Error"), "AWS.SimpleQueueService.NonExistentQueue;Sender"; got != want {
				t.Errorf("iam=%v %s: x-amzn-query-error = %q, want %q", mode, op, got, want)
			}
			if got := jsonType(w); got != "QueueDoesNotExist" {
				t.Errorf("iam=%v %s: __type = %q, want QueueDoesNotExist (%s)", mode, op, got, w.body)
			}
			if got, want := w.body, `{"__type":"com.amazonaws.sqs#QueueDoesNotExist","message":"The specified queue does not exist."}`; got != want {
				t.Errorf("iam=%v %s: body = %s, want %s", mode, op, got, want)
			}
		}
	}
}

// A request that names a QueueUrl but omits another required member is still
// MissingParameter, as the recorded and third-party texts show
// (sqs.md 2.4); only the absent QueueUrl reads as an unknown queue.
func TestSQSMissingOtherMemberStaysMissingParameter(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceOff)
	w := jsonCall(t, u, endpoint, "sqs", "1.0", "AmazonSQS.SendMessage", `{"QueueUrl":"`+endpoint+`/000000000000/q"}`)
	if w.status != http.StatusBadRequest || jsonType(w) != "MissingRequiredParameterException" {
		t.Fatalf("SendMessage without MessageBody = %d %s, want 400 MissingRequiredParameterException", w.status, w.body)
	}
}
