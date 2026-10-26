package awsquery_test

import (
	"cmp"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/awsquery"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// queryError is the part of an ErrorResponse a denial is compared on.
type queryError struct {
	XMLName xml.Name `xml:"ErrorResponse"`
	Error   struct {
		Type    string `xml:"Type"`
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
	RequestID string `xml:"RequestId"`
}

func parseQueryError(t *testing.T, body []byte) queryError {
	t.Helper()
	var e queryError
	if err := xml.Unmarshal(body, &e); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return e
}

// recordedError is the status, Content-Type and body of the single response
// a parity fixture records.
func recordedError(t *testing.T, fixture string) (status int, contentType string, body []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "parity", fixture))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Exchanges []struct {
			Response struct {
				Status  int        `json:"status"`
				Headers [][]string `json:"headers"`
				Body    string     `json:"body"`
			} `json:"response"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("%s: %v", fixture, err)
	}
	if len(rec.Exchanges) != 1 {
		t.Fatalf("%s records %d exchanges, want 1", fixture, len(rec.Exchanges))
	}
	r := rec.Exchanges[0].Response
	for _, h := range r.Headers {
		if len(h) == 2 && http.CanonicalHeaderKey(h[0]) == "Content-Type" {
			contentType = h[1]
		}
	}
	return r.Status, contentType, []byte(r.Body)
}

// STS's denial identity renders the recorded AssumeRole denial: status,
// Content-Type, the namespaced ErrorResponse, Type, Code and the message
// (testdata/parity/sts/R15/06-assume-denied.json, aws-recorded;
// iam-enforcement-surfaces.md 12.3, last row). The error's origin is
// awserr.Denial, which a codec does not read.
func TestSTSDenialRendersTheRecordedAnswer(t *testing.T) {
	t.Parallel()
	status, contentType, body := recordedError(t, "sts/R15/06-assume-denied.json")
	want := parseQueryError(t, body)
	denial := &awserr.Error{ID: stsapi.Service().Denial.Identity, Message: want.Error.Message, Origin: awserr.Denial{}}
	got := awsquery.Codec{}.EncodeError(stsapi.Service(), want.RequestID, denial)
	if got.Status != status || got.Header.Get("Content-Type") != contentType {
		t.Errorf("status %d Content-Type %q, recorded %d %q", got.Status, got.Header.Get("Content-Type"), status, contentType)
	}
	if g := parseQueryError(t, got.Body); g != want {
		t.Errorf("rendered %+v, recorded %+v", g, want)
	}
}

// SNS's denial is the model's AuthorizationError, 403, Type Sender
// (iam-enforcement-surfaces.md 3.1, 3.2, aws-doc). The text is the
// Authorizer's (sns.denial.text, IAM-ENF-03), so it is only carried.
func TestSNSDenialRendersAuthorizationError(t *testing.T) {
	t.Parallel()
	const message = "User: arn:aws:iam::000000000000:user/u is not authorized to perform: SNS:Publish"
	denial := &awserr.Error{ID: snsapi.Service().Denial.Identity, Message: message, Origin: awserr.Denial{}}
	got := awsquery.Codec{}.EncodeError(snsapi.Service(), requestID, denial)
	e := parseQueryError(t, got.Body)
	if got.Status != http.StatusForbidden || e.Error.Type != "Sender" || e.Error.Code != "AuthorizationError" || e.Error.Message != message {
		t.Errorf("rendered %d %+v, want 403 Sender AuthorizationError with the message", got.Status, e.Error)
	}
}

// SQS over Query answers code AccessDenied with the text "Access to the
// resource <url> is denied." (iam-enforcement-surfaces.md 3.2, 4.3,
// aws-doc), which the denial carries as its Query message. The status is
// inferred (sqs.denial.query-text, IAM-ENF-02), so it is read from the
// identity.
func TestSQSQueryDenialRendersAccessDenied(t *testing.T) {
	t.Parallel()
	const text = "Access to the resource http://localhost:4566/000000000000/q is denied."
	id := sqsapi.Service().Denial.Identity
	denial := &awserr.Error{
		ID: id, Message: "User: u is not authorized to perform: sqs:SendMessage",
		Messages: []awserr.ProtocolMessage{{Protocol: ref.AWSQuery, Message: text}}, Origin: awserr.Denial{},
	}
	got := awsquery.Codec{}.EncodeError(sqsapi.Service(), requestID, denial)
	e := parseQueryError(t, got.Body)
	status := cmp.Or(id.QueryModeStatus, id.Status)
	if e.Error.Code != "AccessDenied" || e.Error.Message != text || status == 0 || got.Status != status {
		t.Errorf("rendered %d %+v, want AccessDenied with the Query text and the identity's status %d", got.Status, e.Error, status)
	}
}
