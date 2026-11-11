package awsquery_test

import (
	"bytes"
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
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The ErrorResponse root, namespace included, is the one AWS sent in the
// recorded fixture, for the error the fixture records.
func TestErrorResponseRootMatchesRecording(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fixture string
		svc     *registry.Service
		err     *awserr.Error
	}{
		{"sqs/R11/05-sqs-query-nonexistent-queue.json", sqsapi.Service(), sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")},
		{"sts/R15/06-assume-denied.json", stsapi.Service(), stsapi.ErrAccessDenied("not authorized")},
		{"sns/R16/02-invalid-id.json", snsapi.Service(), snsapi.ErrInvalidBatchEntryIdException("impermissible character")},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			t.Parallel()
			want := rootOf(t, recordedBody(t, c.fixture))
			got := rootOf(t, awsquery.Codec{}.EncodeError(c.svc, requestID, c.err).Body)
			if got != want {
				t.Errorf("ErrorResponse root = %+v, recorded %+v", got, want)
			}
		})
	}
}

// What precedes the root element is what AWS sent before it: SQS opens
// every Query body, success and error, with <?xml version="1.0"?> and no
// newline; SNS sends no declaration.
func TestBodyPrologMatchesRecording(t *testing.T) {
	t.Parallel()
	deleteMessage := func(t *testing.T) []byte {
		t.Helper()
		resp, aerr := awsquery.Codec{}.Encode(operation(t, sqsapi.Service(), "DeleteMessage"), requestID, nil, http.StatusOK)
		if aerr != nil {
			t.Fatalf("Encode = %s %q", aerr.ID.Shape, aerr.Message)
		}
		return resp.Body
	}
	encodeError := func(svc *registry.Service, e *awserr.Error) func(*testing.T) []byte {
		return func(*testing.T) []byte { return awsquery.Codec{}.EncodeError(svc, requestID, e).Body }
	}
	cases := []struct {
		fixture string
		encode  func(*testing.T) []byte
	}{
		{"sqs/R11/04-deletemessage-query.json", deleteMessage},
		{"sqs/R11/05-sqs-query-nonexistent-queue.json", encodeError(sqsapi.Service(), sqsapi.ErrQueueDoesNotExist("The specified queue does not exist."))},
		{"sqs/R11/05-sqs-unknown-action.json", encodeError(sqsapi.Service(), sqsapi.ErrInvalidAction("The action NoSuchAction is not valid for this endpoint."))},
		{"sns/R11/05-sns-unknown-action.json", encodeError(snsapi.Service(), snsapi.ErrInvalidParameterException("Could not find operation NoSuchAction for version 2010-03-31"))},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			t.Parallel()
			want := prolog(t, recordedBody(t, c.fixture))
			if got := prolog(t, c.encode(t)); got != want {
				t.Errorf("prolog = %q, recorded %q", got, want)
			}
		})
	}
}

// prolog is the text of body before its root element's start tag.
func prolog(t *testing.T, body []byte) string {
	t.Helper()
	start := 0
	if bytes.HasPrefix(body, []byte("<?")) {
		end := bytes.Index(body, []byte("?>"))
		if end < 0 {
			t.Fatalf("unterminated declaration in %s", body)
		}
		start = end + len("?>")
	}
	root := bytes.IndexByte(body[start:], '<')
	if root < 0 {
		t.Fatalf("no root element in %s", body)
	}
	return string(body[:start+root])
}

// recordedBody is the body of the single response a parity fixture records.
func recordedBody(t *testing.T, fixture string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "parity", fixture))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Exchanges []struct {
			Response struct {
				Body string `json:"body"`
			} `json:"response"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("%s: %v", fixture, err)
	}
	if len(rec.Exchanges) != 1 {
		t.Fatalf("%s records %d exchanges, want 1", fixture, len(rec.Exchanges))
	}
	return []byte(rec.Exchanges[0].Response.Body)
}

// rootOf is the name, namespace included, of body's root element.
func rootOf(t *testing.T, body []byte) xml.Name {
	t.Helper()
	var root struct{ XMLName xml.Name }
	if err := xml.Unmarshal(body, &root); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return root.XMLName
}
