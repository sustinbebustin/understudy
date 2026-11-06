package edge_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// The execute-api answer, byte for byte: API Gateway's {"message": ...}
// shape (apigateway-http.md 4.5) as JSON with no trailing newline, and one
// Unclaimed event whose last reason names the claiming rule.
func TestExecuteAPIAnswerAndJournal(t *testing.T) {
	const body = `{"message":"understudy does not implement API Gateway invoke (the execute-api data plane); nothing was changed. To ask for it, see https://github.com/sustinbebustin/understudy/issues?q=execute-api"}`
	cases := []struct {
		name, host, path, reason string
	}{
		{
			"host", "abc123.execute-api.us-east-1.localhost:4566", "/prod/pets",
			`rule 2: host "abc123.execute-api.us-east-1.localhost" is an API Gateway execute-api host, and understudy does not implement API Gateway invoke`,
		},
		{
			"path", "localhost:4566", "/_understudy/execute-api/abc123/prod/pets",
			"rule 1: /_understudy/execute-api/abc123/prod/pets is the API Gateway execute-api path fallback, and understudy does not implement API Gateway invoke",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := s3Front(t)
			r := request(t, http.MethodGet, c.path, nil, "", "")
			r.Host = c.host
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, r)
			if rec.Code != http.StatusNotImplemented || rec.Body.String() != body {
				t.Errorf("answer = %d %q, want 501 %q", rec.Code, rec.Body, body)
			}
			if ct := rec.Header().Values("Content-Type"); len(ct) != 1 || ct[0] != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if events := f.journal.Query(journal.Filter{Kind: journal.KindUnclaimed}).Events; len(events) != 1 {
				t.Errorf("journaled %d Unclaimed events, want 1", len(events))
			}
			if got := f.lastReason(t); got != c.reason {
				t.Errorf("last reason = %q, want %q", got, c.reason)
			}
		})
	}
}

// The host claim ends at a base domain (apigateway-http.md 11.3): a bucket
// named like an execute-api host is S3's on its regional virtual host,
// <bucket>.s3.<region>.<base>, as every such host is S3's on AWS.
func TestExecuteAPIHostClaimEndsAtABaseDomain(t *testing.T) {
	f := s3Front(t)
	r := request(t, http.MethodGet, "/", nil, "", "")
	r.Host = "abc123.execute-api.bar.s3.us-east-1.localhost:4566"
	if got := f.serve(t, r); !strings.Contains(got, "X-Amz-Request-Id: ") {
		t.Fatalf("regional virtual host of bucket abc123.execute-api.bar did not reach S3:\n%s", got)
	}
	if op, want := f.lastCall(t), (ref.OperationID{Service: "s3", Name: "ListObjects"}); op != want {
		t.Errorf("the request reached %s, want %s", op, want)
	}
}

// Both forms are claimed before credentials are read: unreadable ones on
// the host form, and the path form with no admin handler configured.
func TestExecuteAPIIsClaimedWhateverTheCredentials(t *testing.T) {
	f := s3Front(t)
	r := request(t, http.MethodGet, "/prod/pets", http.Header{"Authorization": {"AWS4-HMAC-SHA256 garbage"}}, "", "")
	r.Host = "abc123.execute-api.localhost"
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	wantInvokeNotImplemented(t, f, rec, "unreadable credentials on the host form")

	f = newFrontWith(t, map[ref.ProtocolID]edge.Codec{ref.RESTXML: xmlCodec{}}, func(o *edge.Options) { o.Admin = nil }, s3())
	rec = httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodGet, "/_understudy/execute-api/abc123/prod/pets", nil, "", "s3"))
	wantInvokeNotImplemented(t, f, rec, "the path form without an admin handler")
}
