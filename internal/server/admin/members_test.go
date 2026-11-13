package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// putRule sends PUT /faults/<id> with body and returns the answer.
func putRule(k *kerneltest.K, id, body string) *httptest.ResponseRecorder {
	k.Helper()
	req := httptest.NewRequestWithContext(k.Context(), http.MethodPut, admin.Prefix+"/faults/"+id, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(k, "").ServeHTTP(rec, req)
	return rec
}

// tceRuleJSON is a rule injecting TransactionCanceledException with
// members on TransactWriteItems, in the admin API's JSON form.
func tceRuleJSON(members string) string {
	return `{"phase": "request", "match": {"operations": ["dynamodb.TransactWriteItems"]}, ` +
		`"action": {"kind": "error", "shape": "TransactionCanceledException", "members": ` + members + `}, "trigger": {"kind": "always"}}`
}

// txWrites is a TransactWriteItems input of n Puts.
func txWrites(n int) *dynamodbapi.TransactWriteItemsInput {
	in := &dynamodbapi.TransactWriteItemsInput{}
	for range n {
		in.TransactItems = append(in.TransactItems, dynamodbapi.TransactWriteItem{Put: &dynamodbapi.Put{
			TableName: "Orders",
			Item:      map[string]dynamodbapi.AttributeValue{"pk": dynamodbapi.AttributeValueMemberS{Value: "a"}},
		}})
	}
	return in
}

// The documented TransactionConflict reason (smithy-protocol-tests.md
// 12.2, aws-doc).
const conflictReason = `{"Code":"TransactionConflict","Message":"Transaction is ongoing for the item."}`

// A TransactionCanceledException rule whose members set CancellationReasons
// is accepted through the admin API, reads back with its members and no
// retry warning (AWS SDKs do not retry it, smithy-protocol-tests.md 12.2),
// and cancels a call of at least as many items as it gives reasons: status
// 400, __type in DynamoDB's wire namespace, one reason per item with None
// for the items the rule does not name, and the message listing the codes
// (12.1, 12.2, 12.4, 14.9). A call of fewer items reaches the handler.
func TestTransactionCanceledRuleThroughTheAdminAPI(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		handled := 0
		stub := func(context.Context, *kernel.Call, *dynamodbapi.TransactWriteItemsInput, kernel.Wire) (kernel.RawResponse, *awserr.Error) {
			handled++
			return kernel.RawResponse{Status: http.StatusOK, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: []byte("{}")}, nil
		}
		kernel.BindRaw(k.Kernel.Binder(dynamodbapi.Service()), dynamodbapi.OpTransactWriteItemsRaw, stub, registry.Full{},
			kernel.WithItemCount(func(in *dynamodbapi.TransactWriteItemsInput) int { return len(in.TransactItems) }))

		sent := `{"CancellationReasons": [{"Code": "None"}, ` + conflictReason + `]}`
		rec := putRule(k, "tx", tceRuleJSON(sent))
		if rec.Code != http.StatusOK {
			k.Fatalf("PUT the rule = %d %s, want 200", rec.Code, rec.Body)
		}
		var f admin.Fault
		if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
			k.Fatal(err)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(sent)); err != nil {
			k.Fatal(err)
		}
		a, ok := f.Rule.Action.(admin.InjectError)
		if !ok || a.Shape != "TransactionCanceledException" || string(a.Members) != compact.String() {
			k.Errorf("the rule reads back with action %#v, want the shape and members %s", f.Rule.Action, compact.String())
		}
		if f.Warning != "" {
			k.Errorf("warning %q, want none: the SDKs do not retry TransactionCanceledException", f.Warning)
		}

		call := func(n int) *awserr.Error {
			_, err := kerneltest.Dispatch[dynamodbapi.TransactWriteItemsInput, kernel.RawOutput](k.Context(), k, k.Call(dynamodbapi.OpTransactWriteItemsRaw.Descriptor()), txWrites(n))
			return err
		}
		if err := call(1); err != nil || handled != 1 {
			k.Fatalf("a 1-item call = %v, handler ran %d times; want the handler", err, handled)
		}
		e := call(3)
		if e == nil {
			k.Fatal("a 3-item call reached the handler; want it cancelled")
		}
		r := httptest.NewRequestWithContext(k.Context(), http.MethodPost, "/", http.NoBody)
		resp := awsjson.Codec{Version: awsjson.V10}.EncodeError(r, dynamodbapi.Service(), "rid", e)
		msg := "Transaction cancelled, please refer cancellation reasons for specific reasons [None, TransactionConflict, None]"
		// AWS's key is Message, after the reasons (smithy-protocol-tests.md
		// 12.4, PROTO-01).
		want := `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException",` +
			`"CancellationReasons":[{"Code":"None"},` + conflictReason + `,{"Code":"None"}],"Message":"` + msg + `"}`
		if resp.Status != http.StatusBadRequest || string(resp.Body) != want {
			k.Errorf("the cancelled call answers %d %s\nwant 400 %s", resp.Status, resp.Body, want)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/x-amz-json-1.0" || resp.Header.Get("X-Amzn-Requestid") != "rid" {
			k.Errorf("headers %v, want Content-Type application/x-amz-json-1.0 and the request id", resp.Header)
		}
		if handled != 1 {
			k.Errorf("the handler ran %d times; want only for the 1-item call", handled)
		}
	}, kerneltest.Services(dynamodbapi.Service()))
}

// A rule whose members are wrong is refused naming the member at fault,
// with understudy's own texts (smithy-protocol-tests.md 14.12), and no rule
// is added.
func TestMembersRefusalsThroughTheAdminAPI(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		for _, c := range []struct {
			name, body, field, message string
		}{
			{
				"unknown member", tceRuleJSON(`{"CancellationReasons": [` + conflictReason + `], "Reasons": []}`),
				"action.members", `fault rule rejected, no rule was added or changed: action.members: com.amazonaws.dynamodb#TransactionCanceledException has no member "Reasons"; its members are Message and CancellationReasons`,
			},
			{
				"zero reasons", tceRuleJSON(`{"CancellationReasons": []}`),
				"action.members.CancellationReasons", "fault rule rejected, no rule was added or changed: action.members.CancellationReasons: has 0 entries; com.amazonaws.dynamodb#CancellationReasonList holds 1 to 100; give at least one reason",
			},
		} {
			rec := putRule(k, "bad", c.body)
			if rec.Code != http.StatusBadRequest {
				k.Errorf("%s: PUT = %d %s, want 400", c.name, rec.Code, rec.Body)
				continue
			}
			e := envelope(k, rec)
			if e.Code != admin.InvalidRequest || e.Field != c.field || e.Message != c.message || len(e.Known) != 0 {
				k.Errorf("%s: refusal %+v\nwant invalid_request on %s: %q", c.name, e, c.field, c.message)
			}
		}
		rec := putRule(k, "bad", tceRuleJSON(`[`+conflictReason+`]`))
		if e := envelope(k, rec); rec.Code != http.StatusBadRequest || e.Field != "action.members" || !strings.Contains(e.Message, "is a JSON array; expected an object") {
			k.Errorf("members given as an array = %d %+v, want 400 on action.members saying an object is expected", rec.Code, e)
		}
		if active := k.Faults.List(); len(active) != 0 {
			k.Errorf("after the refusals the store holds %+v", active)
		}
	}, kerneltest.Services(dynamodbapi.Service()))
}
