package awsjson_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
)

// messageKey spells id's message key as awsJson writes it, which for
// TransactionCanceledException is AWS's Message (smithy-protocol-tests.md
// 12.4, PROTO-01).
func messageKey(t *testing.T, id awserr.Identity) string {
	t.Helper()
	switch id.MessageKey {
	case awserr.MessageUpper:
		return "Message"
	case awserr.MessageLower:
		return "message"
	case awserr.NoMessage:
	}
	t.Fatalf("%s carries no message key", id.Shape)
	return ""
}

// paddedReasons are the reasons of a three-item transaction whose second
// item conflicted: one reason per item in request order, None for an item
// that did not fail with no Message at all (smithy-protocol-tests.md 12.2,
// aws-doc), and the TransactionConflict text the API reference lists
// (12.2).
func paddedReasons() []dynamodbapi.CancellationReason {
	return []dynamodbapi.CancellationReason{
		{Code: new("None")},
		{Code: new("TransactionConflict"), Message: new("Transaction is ongoing for the item.")},
		{Code: new("None")},
	}
}

// paddedReasonsJSON is paddedReasons on the wire: members sorted by name
// (smithy-protocol-tests.md 12.4, PROTO-04), and a None entry as exactly
// {"Code":"None"} (12.2, 12.4).
const paddedReasonsJSON = `[{"Code":"None"},{"Code":"TransactionConflict","Message":"Transaction is ongoing for the item."},{"Code":"None"}]`

// The message of a cancelled transaction: the codes in request order
// joined by ", " in AWS's frame (smithy-protocol-tests.md 12.3,
// third-party-recorded; 12.4).
const paddedMessage = "Transaction cancelled, please refer cancellation reasons for specific reasons [None, TransactionConflict, None]"

func dynamoRequest() *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", http.NoBody)
	r.Header.Set("X-Amz-Target", "DynamoDB_20120810.TransactWriteItems")
	return r
}

// A TransactionCanceledException carrying its reasons answers 400 with
// __type in DynamoDB's wire namespace, the message under the shape's key,
// then the reasons as the output serializer writes them
// (smithy-protocol-tests.md 11.2, 12.1, 12.4).
func TestTransactionCanceledWireForm(t *testing.T) {
	id := dynamodbapi.IdentityTransactionCanceledException()
	e := dynamodbapi.ErrTransactionCanceledExceptionMembers(paddedMessage, dynamodbapi.TransactionCanceledException{CancellationReasons: paddedReasons()})
	resp := awsjson.Codec{Version: awsjson.V10}.EncodeError(dynamoRequest(), dynamodbapi.Service(), requestID, e)
	want := `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","` + messageKey(t, id) + `":"` + paddedMessage + `",` +
		`"CancellationReasons":` + paddedReasonsJSON + `}`
	if resp.Status != http.StatusBadRequest || string(resp.Body) != want {
		t.Errorf("EncodeError = %d %s\nwant 400 %s", resp.Status, resp.Body, want)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/x-amz-json-1.0" {
		t.Errorf("Content-Type = %q, want application/x-amz-json-1.0", got)
	}
	if got := resp.Header.Get("X-Amzn-Requestid"); got != requestID {
		t.Errorf("X-Amzn-Requestid = %q, want %q", got, requestID)
	}
}

// A structure nested in an error body has its members sorted by name, as
// AWS writes a reason carrying Item: Code, Item, Message, where the model's
// order is Item, Code, Message (smithy-protocol-tests.md 12.4,
// testdata/parity/dynamodb/PROTO-04/01-ccf-all-old.json). The envelope and
// the top-level members keep their order.
func TestNestedErrorStructuresAreSortedByName(t *testing.T) {
	id := dynamodbapi.IdentityTransactionCanceledException()
	const message = "Transaction cancelled, please refer cancellation reasons for specific reasons [ConditionalCheckFailed, None]"
	e := dynamodbapi.ErrTransactionCanceledExceptionMembers(message, dynamodbapi.TransactionCanceledException{CancellationReasons: []dynamodbapi.CancellationReason{
		{
			Item:    map[string]dynamodbapi.AttributeValue{"pk": dynamodbapi.AttributeValueMemberS{Value: "held"}},
			Code:    new("ConditionalCheckFailed"),
			Message: new("The conditional request failed"),
		},
		{Code: new("None")},
	}})
	resp := awsjson.Codec{Version: awsjson.V10}.EncodeError(dynamoRequest(), dynamodbapi.Service(), requestID, e)
	want := `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","` + messageKey(t, id) + `":"` + message + `",` +
		`"CancellationReasons":[{"Code":"ConditionalCheckFailed","Item":{"pk":{"S":"held"}},"Message":"The conditional request failed"},{"Code":"None"}]}`
	if string(resp.Body) != want {
		t.Errorf("EncodeError body = %s\nwant %s", resp.Body, want)
	}
}

// Flat Fields still go out, after the value's members (the envelope, then
// the structure's members, smithy-protocol-tests.md 11.2).
func TestFieldsFollowTheMembers(t *testing.T) {
	id := dynamodbapi.IdentityTransactionCanceledException()
	e := dynamodbapi.ErrTransactionCanceledExceptionMembers(paddedMessage, dynamodbapi.TransactionCanceledException{CancellationReasons: paddedReasons()})
	e.Fields = append(e.Fields, awserr.Field{Name: "Extra", Value: "x"})
	resp := awsjson.Codec{Version: awsjson.V10}.EncodeError(dynamoRequest(), dynamodbapi.Service(), requestID, e)
	want := `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","` + messageKey(t, id) + `":"` + paddedMessage + `",` +
		`"CancellationReasons":` + paddedReasonsJSON + `,"Extra":"x"}`
	if string(resp.Body) != want {
		t.Errorf("EncodeError body = %s\nwant %s", resp.Body, want)
	}
}

// The message has one source: the error's message, under the identity's
// key. The value's own message member is never written, so the key cannot
// appear twice or disagree (smithy-protocol-tests.md 14.7).
func TestTheValuesMessageMemberIsNeverWritten(t *testing.T) {
	id := dynamodbapi.IdentityTransactionCanceledException()
	key := messageKey(t, id)
	reasons := `"CancellationReasons":[{"Code":"TransactionConflict"}]`
	for _, c := range []struct {
		message, want string
	}{
		{"error text", `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","` + key + `":"error text",` + reasons + `}`},
		{"", `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException",` + reasons + `}`},
	} {
		v := dynamodbapi.TransactionCanceledException{
			Message:             new("value text"),
			CancellationReasons: []dynamodbapi.CancellationReason{{Code: new("TransactionConflict")}},
		}
		e := awserr.WithMembers(&v, c.message, awserr.Modeled{})
		resp := awsjson.Codec{Version: awsjson.V10}.EncodeError(dynamoRequest(), dynamodbapi.Service(), requestID, e)
		if got := string(resp.Body); got != c.want || strings.Contains(got, "value text") {
			t.Errorf("message %q: EncodeError body = %s\nwant %s", c.message, got, c.want)
		}
	}
}
