package admin_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// putErrorRule sends PUT /faults/f1 with a rule that injects shape on op
// and returns the refusal it answers.
func putErrorRule(k *kerneltest.K, op, shape string) *admin.Error {
	k.Helper()
	return putShapeRule(k, "error", op, shape)
}

// putShapeRule is putErrorRule for any action kind that injects a shape.
func putShapeRule(k *kerneltest.K, kind, op, shape string) *admin.Error {
	k.Helper()
	rule := `{"phase": "request", "match": {"operations": ["` + op + `"]}, "action": {"kind": "` + kind + `", "shape": "` + shape + `"}, "trigger": {"kind": "always"}}`
	req := httptest.NewRequestWithContext(k.Context(), http.MethodPut, admin.Prefix+"/faults/f1", strings.NewReader(rule))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(k, "").ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		k.Fatalf("PUT a rule injecting %s on %s = %d %s; want 400", shape, op, rec.Code, rec.Body)
	}
	return envelope(k, rec)
}

// A rule naming an error the operation never sends lists the errors it
// does send by the short shape names action.shape takes.
func TestRejectedShapeListsShortNames(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		e := putErrorRule(k, "sqs.SendMessage", "NoSuchThing")
		if e.Code != admin.InvalidRequest || e.Field != "action.shape" ||
			e.Message != `fault rule rejected, no rule was added or changed: action.shape: sqs.SendMessage never sends "NoSuchThing"` {
			k.Errorf("PUT a rule injecting NoSuchThing on SendMessage = %+v; want invalid_request on action.shape saying SendMessage never sends it", e)
		}
		for _, want := range []string{"QueueDoesNotExist", "InvalidMessageContents"} {
			if !slices.Contains(e.Known, want) {
				k.Errorf("known = %v; want it to list %s", e.Known, want)
			}
		}
		if i := slices.IndexFunc(e.Known, func(s string) bool { return strings.Contains(s, "#") }); i >= 0 {
			k.Errorf("known = %v; want short names only, not %s", e.Known, e.Known[i])
		}
	}, kerneltest.Services(sqsapi.Service()))
}

// A bare name that two errors of the operation share is refused with the
// two full shape ids, the only form that tells them apart.
func TestAmbiguousShapeListsFullIDs(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		e := putErrorRule(k, "dynamodb.GetItem", "ThrottlingException")
		want := []string{"com.amazonaws.dynamodb#ThrottlingException", string(awserr.ThrottlingException().Shape)}
		if e.Code != admin.InvalidRequest || e.Field != "action.shape" || !slices.Equal(e.Known, want) ||
			e.Message != `fault rule rejected, no rule was added or changed: action.shape: "ThrottlingException" names 2 different errors dynamodb.GetItem sends; write the full shape id` {
			k.Errorf("PUT a rule injecting ThrottlingException on GetItem = %+v; want invalid_request on action.shape with known %v", e, want)
		}
	}, kerneltest.Services(dynamodbapi.Service()))
}

// AWS sends TransactionCanceledException with CancellationReasons, one per
// item (smithy-protocol-tests.md 12.2; the model's
// com.amazonaws.dynamodb#TransactionCanceledException), so an error rule
// injecting it without them is refused, by short name or full id, saying
// how to set them; and a cancelled transaction changed nothing, so an
// after-commit rule injecting it is refused too (smithy-protocol-tests.md
// 14.9).
func TestTransactionCanceledRuleIsRefused(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		withoutReasons := `com.amazonaws.dynamodb#TransactionCanceledException always carries CancellationReasons on AWS, one per item of the transaction in item order; ` +
			`set them in action.members, such as {"CancellationReasons": [{"Code": "TransactionConflict", "Message": "Transaction is ongoing for the item."}]}`
		afterCommit := "com.amazonaws.dynamodb#TransactionCanceledException means the transaction changed nothing, so AWS never sends it after a commit; " +
			"inject it with an error action whose members set CancellationReasons"
		for _, c := range []struct{ kind, op, shape, field, reason string }{
			{"error", "dynamodb.TransactWriteItems", "TransactionCanceledException", "action.members", withoutReasons},
			{"error", "dynamodb.TransactGetItems", "com.amazonaws.dynamodb#TransactionCanceledException", "action.members", withoutReasons},
			{"after-commit", "dynamodb.ExecuteTransaction", "TransactionCanceledException", "action.shape", afterCommit},
		} {
			e := putShapeRule(k, c.kind, c.op, c.shape)
			want := "fault rule rejected, no rule was added or changed: " + c.field + ": " + c.reason
			if e.Code != admin.InvalidRequest || e.Field != c.field || e.Message != want {
				k.Errorf("PUT a %s rule injecting %s on %s = %+v; want invalid_request on %s: %q", c.kind, c.shape, c.op, e, c.field, want)
			}
		}
	}, kerneltest.Services(dynamodbapi.Service()))
}

// Among the errors an operation sends, a short name two of them share is
// listed as both full ids, while the names that stand alone stay short.
func TestRejectedShapeListsSharedNamesInFull(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		e := putErrorRule(k, "dynamodb.GetItem", "NoSuchThing")
		for _, want := range []string{"ResourceNotFoundException", "com.amazonaws.dynamodb#ThrottlingException", string(awserr.ThrottlingException().Shape)} {
			if !slices.Contains(e.Known, want) {
				k.Errorf("known = %v; want it to list %s", e.Known, want)
			}
		}
		for _, none := range []string{"ThrottlingException", "com.amazonaws.dynamodb#ResourceNotFoundException"} {
			if slices.Contains(e.Known, none) {
				k.Errorf("known = %v; want it not to list %s", e.Known, none)
			}
		}
	}, kerneltest.Services(dynamodbapi.Service()))
}
