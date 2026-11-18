package awsquery_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/protocol/awsquery"
)

// What precedes the root element is what AWS sent before it: SQS opens a
// Query success body, unit output included, with <?xml version="1.0"?> and
// no newline.
func TestBodyPrologMatchesRecording(t *testing.T) {
	t.Parallel()
	const fixture = "sqs/R11/04-deletemessage-query.json"
	resp, aerr := awsquery.Codec{}.Encode(operation(t, sqsapi.Service(), "DeleteMessage"), requestID, nil, http.StatusOK)
	if aerr != nil {
		t.Fatalf("Encode = %s %q", aerr.ID.Shape, aerr.Message)
	}
	want := prolog(t, recordedBody(t, fixture))
	if got := prolog(t, resp.Body); got != want {
		t.Errorf("prolog = %q, recorded %q", got, want)
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
