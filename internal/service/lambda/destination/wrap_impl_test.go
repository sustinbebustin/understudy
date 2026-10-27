package destination_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/service/lambda/destination"
)

// A destination's ARN names its scope, and a bucket's names none
// (lambda-destinations-layers.md 2.2): a queue, topic, function or bus ARN
// without a region or account, a bucket ARN with one, a function ARN with
// an empty qualifier and a topic subscription name no destination.
func TestParseRefusesDestinationsOutOfShape(t *testing.T) {
	for _, arn := range []string{
		"arn:aws:sqs::111111111111:orders",
		"arn:aws:sns:us-east-1::alerts",
		"arn:aws:lambda:us-east-1::function:worker",
		"arn:aws:events::111111111111:event-bus/default",
		"arn:aws:s3:us-east-1::audit-records",
		"arn:aws:s3::111111111111:audit-records",
		"arn:aws:lambda:us-east-1:111111111111:function:worker:",
		"arn:aws:lambda:us-east-1:111111111111:function:worker:live:extra",
		"arn:aws:sns:us-east-1:111111111111:alerts:0f0e0d0c-0b0a-4908-8706-050403020100",
	} {
		if _, err := destination.Parse(arn); !isParseError(err) {
			t.Errorf("Parse(%q) = %v, want a *ParseError", arn, err)
		}
	}
}

func isParseError(err error) bool {
	pe, ok := errors.AsType[*destination.ParseError](err)
	return ok && pe.Reason != ""
}

// A response that is no error object, or whose errorMessage is not a
// string, is its own message, cut like any other (lambda-destinations-layers.md
// 5.2, 11.3.7): the guide's "first 1 KB of the error message" names no
// member for such a response.
func TestErrorMessageOfAResponseWithoutAStringErrorMessage(t *testing.T) {
	cases := []struct{ response, want string }{
		{"plain text", "plain text"},
		{`["a","b"]`, `["a","b"]`},
		{`{"errorMessage":null,"errorType":"Error"}`, `{"errorMessage":null,"errorType":"Error"}`},
		{`{"errorMessage":42}`, `{"errorMessage":42}`},
		{`{"ErrorMessage":"cased"}`, `{"ErrorMessage":"cased"}`},
		{strings.Repeat("x", 2000), strings.Repeat("x", 1024)},
		{"", ""},
	}
	for _, c := range cases {
		if got := destination.ErrorMessage([]byte(c.response)); got != c.want {
			t.Errorf("ErrorMessage(%q) = %q, want %q", c.response, got, c.want)
		}
	}
}

// A function error whose message is empty sends no ErrorMessage attribute,
// rather than an empty String attribute, which SQS refuses (sqs.md 2.4).
func TestDeadLetterLeavesOutAnEmptyErrorMessage(t *testing.T) {
	inv := destination.Ran{Attempts: 1, Response: destination.Response{ExecutedVersion: "$LATEST", FunctionError: "Unhandled", Payload: []byte(`{"errorMessage":""}`)}}
	req, err := destination.DeadLetter(mustARN(t, "arn:aws:sqs:us-east-1:111111111111:dlq"), sender(t), goldenRequestID, inv, []byte(pythonEvent))
	if err != nil {
		t.Fatal(err)
	}
	q, ok := req.Target.(delivery.SQSQueue)
	if !ok {
		t.Fatalf("target = %#v, want an SQSQueue", req.Target)
	}
	names := make([]string, len(q.Attributes))
	for i, a := range q.Attributes {
		names[i] = a.Name
	}
	if strings.Join(names, ",") != "ErrorCode,RequestID" {
		t.Errorf("attributes = %v, want ErrorCode then RequestID", names)
	}
}

// A dead letter to a queue carries the Invoke's trace header as an SQS
// destination's record does (lambda-destinations-layers.md 4.1, 11.3.5),
// and its attributes in the order ErrorCode, ErrorMessage, RequestID.
func TestDeadLetterToAQueueCarriesTheTraceHeader(t *testing.T) {
	s := sender(t)
	req := dead(t, "arn:aws:sqs:us-east-1:111111111111:destination-fn-dlq", ranWithPythonError())
	q, ok := req.Target.(delivery.SQSQueue)
	if !ok || q.TraceHeader != s.Trace {
		t.Fatalf("target = %#v, want an SQSQueue with the trace header %s", req.Target, s.Trace)
	}
	names := make([]string, len(q.Attributes))
	for i, a := range q.Attributes {
		names[i] = a.Name
	}
	if strings.Join(names, ",") != "ErrorCode,ErrorMessage,RequestID" {
		t.Errorf("attribute order = %v, want ErrorCode, ErrorMessage, RequestID", names)
	}
}

// A target whose account or region understudy cannot hold is refused when
// the delivery is built: the outbox would refuse it only after the write
// that ended the event committed, losing it and any later delivery of
// that write without a trace. DeadLetterConfig.TargetArn's pattern lets
// such an ARN through (lambda-destinations-layers.md 5.1).
func TestDeliveriesToAnUnservedScopeAreRefused(t *testing.T) {
	for _, arn := range []string{
		"arn:aws:sqs:us-east-1:12345:dlq",
		"arn:aws-cn:sns:cn-north-1:111111111111:alerts",
	} {
		if _, err := destination.DeadLetter(mustARN(t, arn), sender(t), goldenRequestID, ranWithPythonError(), []byte(pythonEvent)); !isWrapError(err) {
			t.Errorf("DeadLetter(%s) = %v, want a *WrapError", arn, err)
		}
	}
	for _, arn := range []string{
		"arn:aws:sqs:us-east-1:12345:orders",
		"arn:aws-cn:sns:cn-north-1:111111111111:alerts",
		"arn:aws:lambda:us-east-1:12345:function:worker",
		"arn:aws:events:us-east-1:12345:event-bus/default",
	} {
		d, err := destination.Parse(arn)
		if err != nil {
			t.Fatalf("Parse(%s) = %v", arn, err)
		}
		if _, err := destination.To(d, sender(t), destination.Outgoing{Condition: destination.Success, Body: []byte("{}")}); !isWrapError(err) {
			t.Errorf("To(%s) = %v, want a *WrapError", arn, err)
		}
	}
}

// To with no destination is refused rather than sending nowhere.
func TestToRefusesANilDestination(t *testing.T) {
	if _, err := destination.To(nil, sender(t), destination.Outgoing{Condition: destination.Success, Body: []byte("{}")}); !isWrapError(err) {
		t.Errorf("To(nil) = %v, want a *WrapError", err)
	}
}
