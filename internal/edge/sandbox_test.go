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
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// sandbox is the policy of the user-controlled /_understudy/ sub-prefixes
// (ADR 0020): their pages run in an opaque origin, with no
// allow-same-origin, so they cannot read admin or UI answers.
const sandbox = "sandbox"

// functionURLFront is the edge with Lambda's raw function URL operation
// bound to h.
func functionURLFront(t *testing.T, h kernel.RawHTTPHandler) *front {
	t.Helper()
	svc := lambdaLinks()
	return newFrontWith(t, nil, func(o *edge.Options) {
		kernel.BindRawHTTP(o.Kernel.Binder(svc), svc.Operations[0], h, registry.Full{})
		if err := o.Kernel.BindErr(); err != nil {
			t.Fatal(err)
		}
	}, svc)
}

// page answers as a function whose response sets its own policy would.
func page(status int, policy string) kernel.RawHTTPHandler {
	return func(_ context.Context, _ *kernel.Call, _ *http.Request, w http.ResponseWriter) *awserr.Error {
		if policy != "" {
			w.Header().Set("Content-Security-Policy", policy)
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(status)
		if _, err := io.WriteString(w, "<script>fetch('/_understudy/v1/resources')</script>"); err != nil {
			return awserr.Internalf(err)
		}
		return nil
	}
}

func policies(rec *httptest.ResponseRecorder) []string {
	return rec.Header().Values("Content-Security-Policy")
}

// Every answer on the function URL path fallback carries exactly one
// Content-Security-Policy: sandbox, whatever the function or the edge set
// (ADR 0020): the fallback shares the admin API's origin, unlike a function
// URL host, which is its own origin as on AWS.
func TestFunctionURLPathFallbackIsSandboxed(t *testing.T) {
	cases := []struct {
		name string
		h    kernel.RawHTTPHandler
		want int
	}{
		{"a page with no policy", page(http.StatusOK, ""), http.StatusOK},
		{"a page with its own policy", page(http.StatusOK, "default-src 'self'"), http.StatusOK},
		{"a page that allows same-origin", page(http.StatusOK, "sandbox allow-scripts allow-same-origin"), http.StatusOK},
		{"a 403", page(http.StatusForbidden, ""), http.StatusForbidden},
		{"a function error", func(context.Context, *kernel.Call, *http.Request, http.ResponseWriter) *awserr.Error {
			return awserr.Internalf(io.ErrUnexpectedEOF)
		}, http.StatusInternalServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := functionURLFront(t, c.h)
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, request(t, http.MethodGet, "/_understudy/lambda-url/abc123/index.html", nil, "", ""))
			if rec.Code != c.want {
				t.Errorf("status = %d %s, want %d", rec.Code, rec.Body, c.want)
			}
			if got := policies(rec); !slices.Equal(got, []string{sandbox}) {
				t.Errorf("Content-Security-Policy = %q, want exactly %q", got, sandbox)
			}
		})
	}
	t.Run("an unbound function URL operation", func(t *testing.T) {
		f := newFront(t, nil, lambdaLinks())
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/_understudy/lambda-url/abc123/", nil, "hi", ""))
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("status = %d, want the unbound operation's 501", rec.Code)
		}
		if got := policies(rec); !slices.Equal(got, []string{sandbox}) {
			t.Errorf("Content-Security-Policy = %q, want exactly %q", got, sandbox)
		}
	})
}

// A function URL host is its own origin, as on AWS, so the edge adds no
// policy there; the function's response is passed on as it wrote it.
func TestFunctionURLHostIsNotSandboxed(t *testing.T) {
	for _, policy := range []string{"", "default-src 'self'"} {
		f := functionURLFront(t, page(http.StatusOK, policy))
		r := request(t, http.MethodGet, "/index.html", nil, "", "")
		r.Host = "abc123.lambda-url.us-east-1.localhost:4566"
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, r)
		var want []string
		if policy != "" {
			want = []string{policy}
		}
		if got := policies(rec); !slices.Equal(got, want) {
			t.Errorf("function policy %q: Content-Security-Policy = %q, want %q", policy, got, want)
		}
	}
}

// The execute-api path fallback is integration-controlled, so its answer,
// today the 501, is sandboxed too; the host form is its own origin (ADR
// 0020).
func TestExecuteAPIPathFallbackIsSandboxed(t *testing.T) {
	f := s3Front(t)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodGet, "/_understudy/execute-api/abc123/prod/pets", nil, "", ""))
	if got := policies(rec); !slices.Equal(got, []string{sandbox}) {
		t.Errorf("path fallback Content-Security-Policy = %q, want exactly %q", got, sandbox)
	}

	f = s3Front(t)
	r := request(t, http.MethodGet, "/prod/pets", nil, "", "")
	r.Host = "abc123.execute-api.us-east-1.localhost:4566"
	rec = httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	if got := policies(rec); len(got) != 0 {
		t.Errorf("host form Content-Security-Policy = %q, want none", got)
	}
}

// The understudy-controlled sub-prefixes gain no policy.
func TestAdminAPIIsNotSandboxed(t *testing.T) {
	f := newFront(t, nil)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodGet, "/_understudy/v1/health", nil, "", ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("GET /_understudy/v1/health = %d, want the admin handler's 204", rec.Code)
	}
	if got := policies(rec); len(got) != 0 {
		t.Errorf("admin Content-Security-Policy = %q, want none", got)
	}
}
