package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
)

// listTables answers a signed DynamoDB ListTables through srv's front door.
func listTables(t *testing.T, srv *server.Server) (status int, body string) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost:4566/", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "DynamoDB_20120810.ListTables")
	req.Header.Set("X-Amz-Date", "20260101T000000Z")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260101/us-east-1/dynamodb/aws4_request, SignedHeaders=host;x-amz-date, Signature=0")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// With Docker disabled by configuration, a DynamoDB call answers 501 saying
// DynamoDB Local needs Docker, which no retry changes.
func TestDynamoDBWithoutDockerSaysDockerIsNeeded(t *testing.T) {
	srv, _ := build(t, server.Config{Docker: server.DockerDisabled{}})
	if code, body := listTables(t, srv); code != http.StatusNotImplemented || !strings.Contains(body, "which needs Docker") {
		t.Fatalf("ListTables with Docker disabled = %d %s, want 501 naming Docker", code, body)
	}
}

// With DynamoDB turned off, a DynamoDB call answers 501 naming the setting
// that turned it off.
func TestDynamoDBDisabledNamesTheSetting(t *testing.T) {
	srv, _ := build(t, server.Config{Docker: server.DockerDisabled{}, DynamoDB: server.DynamoDBDisabled{}})
	if code, body := listTables(t, srv); code != http.StatusNotImplemented || !strings.Contains(body, "dynamodb.enabled is false") {
		t.Fatalf("ListTables with DynamoDB disabled = %d %s, want 501 naming dynamodb.enabled", code, body)
	}
}
