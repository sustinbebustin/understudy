package edge_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// s3Front is the edge with the one-route S3 of rest_test.go, so a request
// that reaches S3 shows it: S3's x-amz-request-id and an s3 API call.
func s3Front(t *testing.T) *front {
	t.Helper()
	return newFront(t, map[ref.ProtocolID]edge.Codec{ref.RESTXML: xmlCodec{}}, s3())
}

// wantInvokeNotImplemented checks the answer understudy gives until the
// execute-api data plane is bound: 501 NotImplemented (ADR 0003), in API
// Gateway's own {"message": ...} shape (apigateway-http.md 4.5), naming
// API Gateway invoke and the issues link, with the edge's Date and request
// id (ARCHITECTURE.md 4.1), and no kernel call: S3 never answers it.
func wantInvokeNotImplemented(t *testing.T, f *front, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("%s: status = %d, want 501", what, rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body) != 1 {
		t.Errorf("%s: body %s is not API Gateway's {\"message\": ...} shape (%v)", what, rec.Body, err)
	}
	msg := body["message"]
	if !strings.HasPrefix(msg, "understudy does not implement API Gateway invoke") ||
		!strings.Contains(msg, "https://github.com/sustinbebustin/understudy/issues") {
		t.Errorf("%s: message = %q, want it to say understudy does not implement API Gateway invoke and give the issues link", what, msg)
	}
	if dates := rec.Header().Values("Date"); len(dates) != 1 {
		t.Errorf("%s: Date headers = %q, want exactly one", what, dates)
	}
	if rec.Header().Get("X-Amzn-Requestid") == "" {
		t.Errorf("%s: no x-amzn-RequestId header", what)
	}
	if id := rec.Header().Get("X-Amz-Request-Id"); id != "" {
		t.Errorf("%s: S3's x-amz-request-id %q is set, so S3 answered", what, id)
	}
	if ops := f.calls(t); len(ops) != 0 {
		t.Errorf("%s: journaled calls = %v, want none", what, ops)
	}
}

// lastReason returns the last reason of the latest Unclaimed event.
func (f *front) lastReason(t *testing.T) string {
	t.Helper()
	reasons := f.unclaimedReasons(t)
	if len(reasons) == 0 {
		t.Fatal("the Unclaimed event lists no reasons")
	}
	return reasons[len(reasons)-1]
}

// <id>.execute-api.<region>.<base> and <id>.execute-api.<base> are API
// Gateway's data plane, never S3 virtual-hosted buckets, whatever the
// request is signed for (apigateway-http.md 4.1 and Implications 3,
// ADR 0013): AWS serves execute-api hosts apart from S3. The claim is
// journaled as an Unclaimed event of rule 2.
func TestExecuteAPIHostsAreNotS3(t *testing.T) {
	hosts := []string{
		"abc123.execute-api.us-east-1.localhost:4566",
		"abc123.execute-api.us-east-1.localhost",
		"abc123.execute-api.localhost:4566",
		"ABC123.Execute-API.US-EAST-1.localhost:4566",
	}
	for _, host := range hosts {
		for _, signedFor := range []string{"", "s3", "apigateway", "execute-api"} {
			for _, target := range []struct{ method, path string }{{http.MethodGet, "/"}, {http.MethodGet, "/prod/pets?limit=2"}, {http.MethodPost, "/orders"}} {
				what := target.method + " " + host + target.path + " signed for " + signedFor
				t.Run(what, func(t *testing.T) {
					f := s3Front(t)
					r := request(t, target.method, target.path, http.Header{"Content-Type": {"application/json"}}, `{}`, signedFor)
					r.Host = host
					rec := httptest.NewRecorder()
					f.handler.ServeHTTP(rec, r)
					wantInvokeNotImplemented(t, f, rec, what)
					name := strings.ToLower(host)
					if h, _, err := net.SplitHostPort(name); err == nil {
						name = h
					}
					if last := f.lastReason(t); !strings.HasPrefix(last, "rule 2: ") || !strings.Contains(last, name) {
						t.Errorf("last journaled reason = %q, want rule 2's naming %s", last, name)
					}
				})
			}
		}
	}
}

// The path fallback /_understudy/execute-api/<id>/... is claimed on the
// main listener as rule 1 claims the function URL fallback
// (apigateway-http.md Implications 3), whatever follows the prefix.
func TestExecuteAPIPathFallbackIsClaimedOnTheMainListener(t *testing.T) {
	for _, path := range []string{"/_understudy/execute-api/abc123/prod/pets", "/_understudy/execute-api/abc123/", "/_understudy/execute-api/"} {
		for _, signedFor := range []string{"", "apigateway", "execute-api"} {
			what := "GET " + path + " signed for " + signedFor
			t.Run(what, func(t *testing.T) {
				f := s3Front(t)
				rec := httptest.NewRecorder()
				f.handler.ServeHTTP(rec, request(t, http.MethodGet, path, nil, "", signedFor))
				wantInvokeNotImplemented(t, f, rec, what)
				if uri, _ := f.admin.seen(); uri != "" {
					t.Errorf("the admin handler saw %q", uri)
				}
				if last := f.lastReason(t); !strings.HasPrefix(last, "rule 1: ") || !strings.Contains(last, path) {
					t.Errorf("last journaled reason = %q, want rule 1's naming %s", last, path)
				}
			})
		}
	}
}

// On the container listener the prefix is the admin part of rule 1, which
// serves only health and clock there (ARCHITECTURE.md 4.1 rule 1).
func TestExecuteAPIPathFallbackIsNotClaimedOnTheContainerListener(t *testing.T) {
	h, err := edge.New(edge.Options{Kernel: kernelOf(t), Registry: mustRegistry(t), Listener: ref.ListenerContainer, Admin: &recorder{}})
	if err != nil {
		t.Fatal(err)
	}
	answer := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, request(t, http.MethodGet, path, nil, "", ""))
		return rec
	}
	other := answer("/_understudy/v1/reset")
	got := answer("/_understudy/execute-api/abc123/prod/pets")
	if got.Code != other.Code || got.Body.String() != other.Body.String() {
		t.Errorf("container listener execute-api path = %d %s, want the admin part's answer to other paths, %d %s", got.Code, got.Body, other.Code, other.Body)
	}
}

// A bucket named like an execute-api host is still an S3 bucket path-style
// (ADR 0013), since only the host form is API Gateway's.
func TestABucketNamedLikeAnExecuteAPIHostIsReachablePathStyle(t *testing.T) {
	for _, signedFor := range []string{"", "s3"} {
		f := s3Front(t)
		got := f.serve(t, request(t, http.MethodGet, "/abc123.execute-api.us-east-1", nil, "", signedFor))
		if !strings.Contains(got, "X-Amz-Request-Id: ") {
			t.Errorf("signed for %q: GET /abc123.execute-api.us-east-1 did not reach S3:\n%s", signedFor, got)
		}
		if op, want := f.lastCall(t), (ref.OperationID{Service: "s3", Name: "ListObjects"}); op != want {
			t.Errorf("signed for %q: the request reached %s, want %s", signedFor, op, want)
		}
	}
}

// execute-api is a public data plane that pages on any site call, as on
// AWS: the browser defences refuse neither form (ADR 0020), as they refuse
// no function URL (TestFunctionURLsAnswerPagesOnEverySite).
func TestExecuteAPIAnswersPagesOnEverySite(t *testing.T) {
	for _, target := range []struct{ host, path string }{
		{"abc123.execute-api.us-east-1.localhost:4566", "/prod/pets"},
		{"localhost:4566", "/_understudy/execute-api/abc123/prod/pets"},
	} {
		f := s3Front(t)
		r := request(t, http.MethodPost, target.path, http.Header{"Content-Type": {"text/plain"}, "Origin": {"https://evil.example"}}, "hi", "")
		r.Host = target.host
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, r)
		wantInvokeNotImplemented(t, f, rec, "a page on evil.example calling "+target.host+target.path)
	}
}

// The listener's host policy still refuses a foreign name that only looks
// like an execute-api host, as for every other host
// (TestLocalHostsRefusesAForeignHost: DNS rebinding).
func TestExecuteAPIHostsKeepTheHostPolicy(t *testing.T) {
	f := s3Front(t)
	r := request(t, http.MethodGet, "/prod/pets", nil, "", "")
	r.Host = "abc123.execute-api.us-east-1.attacker.example"
	if got := f.serve(t, r); !strings.HasPrefix(got, "403\n") || !strings.Contains(got, "DNS rebinding") {
		t.Errorf("foreign execute-api-looking host = %s, want 403 naming DNS rebinding", got)
	}
	if page := f.journal.Query(journal.Filter{Kind: journal.KindAPICall}); len(page.Events) != 0 {
		t.Errorf("the refused request reached the kernel: %+v", page.Events[0].Detail)
	}
}
