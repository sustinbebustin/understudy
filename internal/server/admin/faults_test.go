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
	rule := `{"phase": "request", "match": {"operations": ["` + op + `"]}, "action": {"kind": "error", "shape": "` + shape + `"}, "trigger": {"kind": "always"}}`
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
