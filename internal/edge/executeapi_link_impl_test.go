package edge_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// invokeAPIService is API Gateway's HTTP APIs with only the raw data-plane
// operation, InvokeApi.
func invokeAPIService() *registry.Service {
	svc := &registry.Service{ID: "apigatewayv2", SigningName: "apigateway", IAMPrefix: "apigateway", ARNNamespace: "apigateway", Protocols: []ref.ProtocolID{ref.RESTJSON1}}
	svc.Operations = []*registry.Operation{{Service: svc, ID: ref.OperationID{Service: "apigatewayv2", Name: "InvokeApi"}, Kind: registry.Raw}}
	return svc
}

// invokeAPIFront is the edge with InvokeApi bound to a handler that records
// the path it saw and answers 200 "served" with a policy of its own.
func invokeAPIFront(t *testing.T, seen *string) *front {
	t.Helper()
	svc := invokeAPIService()
	return newFrontWith(t, nil, func(o *edge.Options) {
		kernel.BindRawHTTP(o.Kernel.Binder(svc), svc.Operations[0], func(_ context.Context, _ *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
			*seen = r.URL.Path
			w.Header().Set("Content-Security-Policy", "default-src 'self'")
			if _, err := io.WriteString(w, "served"); err != nil {
				return awserr.Internalf(err)
			}
			return nil
		}, registry.Full{})
		if err := o.Kernel.BindErr(); err != nil {
			t.Fatal(err)
		}
	}, svc)
}

// With the raw apigatewayv2.InvokeApi registered, the path fallback is that
// operation's, sandboxed over the integration's own policy (ADR 0020), and
// journaled as its call, never as Unclaimed; the host form still answers
// the 501 until the data plane serves it.
func TestExecuteAPIPathFallbackReachesInvokeAPI(t *testing.T) {
	var seen string
	f := invokeAPIFront(t, &seen)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodGet, "/_understudy/execute-api/abc123/prod/pets", nil, "", ""))
	if rec.Code != http.StatusOK || rec.Body.String() != "served" || seen != "/_understudy/execute-api/abc123/prod/pets" {
		t.Errorf("path fallback = %d %q, handler saw %q; want InvokeApi's 200 for the whole path", rec.Code, rec.Body, seen)
	}
	if got := policies(rec); !slices.Equal(got, []string{sandbox}) {
		t.Errorf("Content-Security-Policy = %q, want exactly %q", got, sandbox)
	}
	if ops := f.calls(t); !slices.Equal(ops, []ref.OperationID{{Service: "apigatewayv2", Name: "InvokeApi"}}) {
		t.Errorf("journaled calls = %v, want one InvokeApi", ops)
	}
	if page := f.journal.Query(journal.Filter{Kind: journal.KindUnclaimed}); len(page.Events) != 0 {
		t.Errorf("journaled %d Unclaimed events, want none", len(page.Events))
	}

	seen = ""
	f = invokeAPIFront(t, &seen)
	r := request(t, http.MethodGet, "/prod/pets", nil, "", "")
	r.Host = "abc123.execute-api.us-east-1.localhost:4566"
	rec = httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	wantInvokeNotImplemented(t, f, rec, "the host form")
	if seen != "" {
		t.Errorf("the host form reached InvokeApi with %q", seen)
	}
}

// A page on another site may call the path fallback, as any page may call
// execute-api on AWS: the browser defences do not refuse it.
func TestExecuteAPIPathFallbackAnswersPagesOnEverySite(t *testing.T) {
	var seen string
	f := invokeAPIFront(t, &seen)
	r := request(t, http.MethodPost, "/_understudy/execute-api/abc123/orders", http.Header{"Content-Type": {"text/plain"}, "Origin": {"https://evil.example"}}, "hi", "")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || seen == "" {
		t.Errorf("a page on evil.example calling the path fallback = %d %s, want InvokeApi's 200", rec.Code, rec.Body)
	}
}
