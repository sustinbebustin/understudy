package awsquery_test

import (
	"encoding/json"
	"encoding/xml"
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
