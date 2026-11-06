package edge_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// A Streams call refused for its streams.dynamodb scope never reaches the
// service, so the Unclaimed journal event says why: rule 6 names the
// refused scope and the one to sign for.
func TestRefusedScopeIsJournaled(t *testing.T) {
	f := newFront(t, nil, dynamoDBStreams())
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/", jsonHeader("DynamoDBStreams_20120810.ListStreams", "application/x-amz-json-1.0"), `{}`, "streams.dynamodb"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d %s, want 400", rec.Code, rec.Body)
	}
	reasons := f.unclaimedReasons(t)
	if !slices.ContainsFunc(reasons, func(r string) bool {
		return strings.HasPrefix(r, "rule 6: ") && strings.Contains(r, `"streams.dynamodb"`) && strings.Contains(r, `sign for "dynamodb"`)
	}) {
		t.Errorf("journaled reasons = %q, want rule 6's naming the refused scope", reasons)
	}
}

// A target naming no Streams operation is Coral's unknown operation, which
// AWS answers before it reads the credentials (ARCHITECTURE.md 3.3), so a
// streams.dynamodb scope does not turn it into the scope refusal.
func TestRefusedScopeNeedsAnOperation(t *testing.T) {
	f := newFront(t, nil, dynamoDBStreams())
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/", jsonHeader("DynamoDBStreams_20120810.ListTables", "application/x-amz-json-1.0"), `{}`, "streams.dynamodb"))
	if strings.Contains(rec.Body.String(), "InvalidSignatureException") {
		t.Errorf("unknown operation scoped to streams.dynamodb = %d %s, want no scope refusal", rec.Code, rec.Body)
	}
}
