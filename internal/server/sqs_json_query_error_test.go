package server_test

import (
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
)

// SQS's JSON front end sends x-amzn-query-error with its own codes for the
// protocol-level errors of the query-compatible JSON family, and none for a
// target that names another service (testdata/parity/sqs/R11/notes.md 03).
func TestSQSJSONProtocolErrorsCarryTheRecordedQueryError(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceOff)
	queueURL := endpoint + "/000000000000/q"
	tests := []struct {
		name, target, body string
		queryError         string // "" when the header is absent
		wantBody           string
	}{
		{
			// testdata/parity/sqs/R11/03-string-for-integer.json
			"string for an integer", "AmazonSQS.ReceiveMessage", `{"QueueUrl":"` + queueURL + `","MaxNumberOfMessages":"5"}`,
			"MalformedInput;Sender",
			`{"__type":"com.amazon.coral.service#SerializationException","Message":"STRING_VALUE cannot be converted to Integer"}`,
		},
		{
			// testdata/parity/sqs/R11/03-unknown-operation.json
			"unknown operation", "AmazonSQS.NoSuchOperation", `{}`,
			"InvalidAction;Sender",
			`{"__type":"com.amazon.coral.service#UnknownOperationException"}`,
		},
		{
			// testdata/parity/sqs/R11/03-wrong-target-prefix.json
			"another service's target prefix", "AmazonSNS.ListQueues", `{}`,
			"",
			`{"__type":"com.amazon.coral.service#UnknownOperationException"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := jsonCall(t, u, endpoint, "sqs", "1.0", tt.target, tt.body)
			if w.status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.status)
			}
			got, sent := w.header["X-Amzn-Query-Error"]
			switch {
			case tt.queryError == "" && sent:
				t.Errorf("x-amzn-query-error = %q, want none", got)
			case tt.queryError != "" && w.header.Get("X-Amzn-Query-Error") != tt.queryError:
				t.Errorf("x-amzn-query-error = %q, want %q", got, tt.queryError)
			}
			if w.body != tt.wantBody {
				t.Errorf("body = %s\nwant %s", w.body, tt.wantBody)
			}
		})
	}
}

// SQS's codes come from its own overlay: CloudWatch, the other
// query-compatible JSON service, has no recording of these errors, so it
// keeps the shape names as codes (wire-json-query.md 6.2).
func TestCloudWatchJSONProtocolErrorsKeepTheirQueryError(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceOff)
	for _, tt := range []struct{ target, body, queryError string }{
		{"GraniteServiceVersion20100801.ListMetrics", `{"Namespace":5}`, "SerializationException;Sender"},
		{"GraniteServiceVersion20100801.NoSuchOperation", `{}`, "UnknownOperationException;Sender"},
	} {
		w := jsonCall(t, u, endpoint, "monitoring", "1.0", tt.target, tt.body)
		if got := w.header.Get("X-Amzn-Query-Error"); w.status != http.StatusBadRequest || got != tt.queryError {
			t.Errorf("%s: %d x-amzn-query-error %q, want 400 %q (%s)", tt.target, w.status, got, tt.queryError, w.body)
		}
	}
}
