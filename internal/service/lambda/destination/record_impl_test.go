package destination_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/service/lambda/destination"
)

// documentedRecord is the guide's example invocation record as printed,
// spaced, abridged in requestPayload only
// (https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html,
// "Example invocation record"; lambda-destinations-layers.md 3.1).
const documentedRecord = `{
  "version": "1.0",
  "timestamp": "2019-11-14T18:16:05.568Z",
  "requestContext": {
    "requestId": "e4b46cbf-b738-xmpl-8880-a18cdf61200e",
    "functionArn": "arn:aws:lambda:us-east-1:123456789012:function:my-function:$LATEST",
    "condition": "RetriesExhausted",
    "approximateInvokeCount": 3
  },
  "requestPayload": { "ORDER_IDS": ["9e07af03-ce31-4ff3-xmpl-36dce652cb4f"] },
  "responseContext": { "statusCode": 200, "executedVersion": "$LATEST", "functionError": "Unhandled" },
  "responsePayload": { "errorMessage": "RequestId: e4b46cbf-b738-xmpl-8880-a18cdf61200e Process exited before completing request" }
}`

// Build writes the guide's example record from its own inputs, compacted:
// three attempts, a crashed process's error object, the documented key
// order (lambda-destinations-layers.md 3.1, 11.4). The guide pretty-prints
// the whole record, so the payload bytes its caller and function wrote are
// unknown; the inputs are its payloads compacted, which Build embeds as
// they are.
func TestBuildMatchesTheDocumentedExample(t *testing.T) {
	r := destination.Record{
		At:          time.Date(2019, 11, 14, 18, 16, 5, 568_000_000, time.UTC),
		RequestID:   ids.LambdaRequestID("e4b46cbf-b738-xmpl-8880-a18cdf61200e"),
		FunctionARN: "arn:aws:lambda:us-east-1:123456789012:function:my-function:$LATEST",
		Condition:   destination.RetriesExhausted,
		Invocation: destination.Ran{Attempts: 3, Response: destination.Response{
			ExecutedVersion: "$LATEST",
			FunctionError:   "Unhandled",
			Payload:         []byte(`{"errorMessage":"RequestId: e4b46cbf-b738-xmpl-8880-a18cdf61200e Process exited before completing request"}`),
		}},
		Payload: []byte(`{"ORDER_IDS":["9e07af03-ce31-4ff3-xmpl-36dce652cb4f"]}`),
	}
	var want bytes.Buffer
	if err := json.Compact(&want, []byte(documentedRecord)); err != nil {
		t.Fatal(err)
	}
	got, err := destination.Build(r)
	if err != nil {
		t.Fatalf("Build = %v", err)
	}
	if !bytes.Equal(got.JSON, want.Bytes()) {
		t.Errorf("Build =\n%s\nwant the documented example compacted\n%s", got.JSON, want.Bytes())
	}
}

// For any Condition value, declared or not: Build accepts it exactly when
// it is one of the four AWS writes, and the record's condition is its wire
// value (lambda-destinations-layers.md 3.2); Failed is every declared
// value but Success (5.3); the EventBridge detail-type follows Failed
// (4.4).
func TestConditionProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := destination.Condition(rapid.Uint8().Draw(t, "condition"))
		declared := slices.Contains(destination.Conditions(), c)
		inv := destination.Invocation(destination.NeverRan{})
		if c == destination.Success {
			inv = destination.Ran{Attempts: 1, Response: destination.Response{ExecutedVersion: "$LATEST", Payload: []byte(`null`)}}
		}
		r := destination.Record{
			At: goldenAt, RequestID: goldenRequestID, FunctionARN: goldenQualified,
			Condition: c, Invocation: inv, Payload: []byte(`{}`),
		}
		got, err := destination.Build(r)
		if !declared {
			if re, ok := errors.AsType[*destination.RecordError](err); !ok || re.Field != "Condition" {
				t.Fatalf("Build with undeclared %v = %v, want a *RecordError on Condition", c, err)
			}
			if c.Failed() || c.DetailType() != "" {
				t.Fatalf("undeclared %v: Failed = %t, DetailType = %q, want false and empty", c, c.Failed(), c.DetailType())
			}
			return
		}
		if err != nil {
			t.Fatalf("Build with %v = %v", c, err)
		}
		if cond := stringMember(t, member(t, got.JSON, "requestContext"), "condition"); cond != c.String() {
			t.Fatalf("condition = %q, want %q", cond, c.String())
		}
		if c.Failed() != (c != destination.Success) {
			t.Fatalf("%v.Failed() = %t", c, c.Failed())
		}
		suffix := " - Success"
		if c.Failed() {
			suffix = " - Failure"
		}
		if d := c.DetailType(); !strings.HasPrefix(d, "Lambda Function Invocation Result") || !strings.HasSuffix(d, suffix) {
			t.Fatalf("%v.DetailType() = %q, want the result detail-type ending %q", c, d, suffix)
		}
	})
}
