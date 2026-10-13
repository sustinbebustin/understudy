package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
)

// With the licence not accepted and Docker disabled, a DynamoDB call
// answers the licence notice and also says Docker is needed, so accepting
// the licence is not followed by a second refusal.
func TestDynamoDBNoticeNamesMissingDocker(t *testing.T) {
	srv, _ := build(t, server.Config{Docker: server.DockerDisabled{}})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost:4566/", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "DynamoDB_20120810.ListTables")
	req.Header.Set("X-Amz-Date", "20260101T000000Z")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260101/us-east-1/dynamodb/aws4_request, SignedHeaders=host;x-amz-date, Signature=0")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusNotImplemented || !strings.Contains(body, "DynamoDB Local License Agreement") || !strings.Contains(body, "also needs Docker") {
		t.Fatalf("ListTables with Docker disabled and no licence = %d %s, want 501 with the notice and the Docker reason", rec.Code, body)
	}
}
