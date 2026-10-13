package dynamodb_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestPreForwardChecksAnswerBeforeLocal: every generic error real DynamoDB
// sends and Local gets wrong is answered by understudy, in DynamoDB's
// words, before Local sees the request (error-matrix.md finding 6, 5.5 T5
// to T9 and H7; dynalite connection.js, recorded against real DynamoDB).
func TestPreForwardChecksAnswerBeforeLocal(t *testing.T) {
	jsonHeader := func(target, contentType string) http.Header {
		h := http.Header{"Content-Type": {contentType}}
		if target != "" {
			h.Set("X-Amz-Target", target)
		}
		return h
	}
	tests := []struct {
		name   string
		header http.Header
		body   string
		from   *caller
		status int
		want   string
	}{
		{
			name: "T6 malformed JSON", header: target("ListTables"), body: `{"Limit":`, from: defaultCaller(),
			status: 400, want: `{"__type":"com.amazon.coral.service#SerializationException"}`,
		},
		{
			name: "T2 wrong member type", header: target("ListTables"), body: `{"Limit":"five"}`, from: defaultCaller(),
			status: 400, want: `{"__type":"com.amazon.coral.service#SerializationException","Message":"STRING_VALUE cannot be converted to Integer"}`,
		},
		{
			name: "T5 missing target", header: jsonHeader("", "application/x-amz-json-1.0"), body: `{}`, from: defaultCaller(),
			status: 400, want: `{"__type":"com.amazon.coral.service#UnknownOperationException"}`,
		},
		{
			name: "T5 bad target", header: target("ListTablez"), body: `{}`, from: defaultCaller(),
			status: 400, want: `{"__type":"com.amazon.coral.service#UnknownOperationException"}`,
		},
		{
			name: "T5 other service's target", header: jsonHeader("DynamoDB_20111205.ListTables", "application/x-amz-json-1.0"), body: `{}`, from: defaultCaller(),
			status: 400, want: `{"__type":"com.amazon.coral.service#UnknownOperationException"}`,
		},
		{
			name: "H7 wrong Content-Type", header: jsonHeader("DynamoDB_20120810.ListTables", "application/x-amz-json-1.1"), body: `{}`, from: defaultCaller(),
			status: 404, want: "<UnknownOperationException/>\n",
		},
		{
			name: "T8 no credentials", header: target("ListTables"), body: `{}`,
			status: 400, want: `{"__type":"com.amazon.coral.service#MissingAuthenticationTokenException","message":"Request is missing Authentication Token"}`,
		},
		{
			name: "T8 region outside the table", body: `{}`,
			header: http.Header{
				"Content-Type": {"application/x-amz-json-1.0"}, "X-Amz-Target": {"DynamoDB_20120810.ListTables"}, "X-Amz-Date": {"20261010T120000Z"},
				"Authorization": {"AWS4-HMAC-SHA256 Credential=111111111111/20261010/mars-north-1/dynamodb/aws4_request, SignedHeaders=host;x-amz-date, Signature=00"},
			},
			status: 400, want: `{"__type":"com.amazon.coral.service#InvalidSignatureException","message":"Credential should be scoped to a valid region, not 'mars-north-1'. Add the name to the regions array in the understudy configuration to use it."}`,
		},
		{
			name: "T3 generated validation", header: target("ListTables"), body: `{"Limit":101}`, from: defaultCaller(),
			status: 400, want: `{"__type":"com.amazon.coral.validate#ValidationException","message":"1 validation error detected: Value '101' at 'limit' failed to satisfy constraint: Member must have value less than or equal to 100"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run(t, func(h *harness) {
				got := h.post(tt.header, tt.body, tt.from)
				if got.Status != tt.status || got.Body != tt.want {
					h.Errorf("answer = %d %s\nwant %d %s", got.Status, got.Body, tt.status, tt.want)
				}
				if seen := h.local.requests(); len(seen) != 0 {
					h.Errorf("Local received %d requests, want none: %+v", len(seen), seen)
				}
			})
		})
	}
}

// TestBodyOver16MiBIsRefusedWith413: DynamoDB answers 413 above 16 MiB
// (error-matrix.md 5.5 T9); Local never reads the body.
func TestBodyOver16MiBIsRefusedWith413(t *testing.T) {
	run(t, func(h *harness) {
		body := `{"TableName":"Locks","Item":{"k":{"S":"` + strings.Repeat("x", 16<<20) + `"}}}`
		got := h.call("PutItem", body)
		if got.Status != http.StatusRequestEntityTooLarge || !strings.Contains(got.Body, "RequestEntityTooLargeException") {
			h.Errorf("answer = %d %.200s, want 413 RequestEntityTooLargeException", got.Status, got.Body)
		}
		if seen := h.local.requests(); len(seen) != 0 {
			h.Errorf("Local received %d requests, want none", len(seen))
		}
	})
}
