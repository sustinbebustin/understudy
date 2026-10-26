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
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The sandbox wins however a function sends its header (ADR 0020): a key
// in another case, which net/http sends as written beside the canonical
// one, an implicit 200 from a bare Write, and a flush before any status.
// Served over a real listener, so the headers checked are the ones on the
// wire, where a client folds every case of a name into one.
func TestSandboxWinsOnTheWire(t *testing.T) {
	cases := []struct {
		name string
		h    kernel.RawHTTPHandler
	}{
		{"a policy keyed in lowercase", func(_ context.Context, _ *kernel.Call, _ *http.Request, w http.ResponseWriter) *awserr.Error {
			w.Header()["content-security-policy"] = []string{"default-src *"}
			w.WriteHeader(http.StatusOK)
			return writeBody(w)
		}},
		{"a bare Write", func(_ context.Context, _ *kernel.Call, _ *http.Request, w http.ResponseWriter) *awserr.Error {
			w.Header().Set("Content-Security-Policy", "sandbox allow-same-origin allow-scripts")
			return writeBody(w)
		}},
		{"a flush before the status", func(_ context.Context, _ *kernel.Call, _ *http.Request, w http.ResponseWriter) *awserr.Error {
			w.Header().Set("Content-Security-Policy", "default-src *")
			if err := http.NewResponseController(w).Flush(); err != nil {
				return awserr.Internalf(err)
			}
			return writeBody(w)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			port := serveOn(t, functionURLFront(t, c.h).handler)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:"+port+"/_understudy/lambda-url/abc123/", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != http.StatusOK || string(body) != "ok" {
				t.Errorf("answer = %d %q, want 200 %q", res.StatusCode, body, "ok")
			}
			if got := res.Header.Values("Content-Security-Policy"); !slices.Equal(got, []string{sandbox}) {
				t.Errorf("Content-Security-Policy on the wire = %q, want exactly %q", got, sandbox)
			}
		})
	}
}

func writeBody(w http.ResponseWriter) *awserr.Error {
	if _, err := io.WriteString(w, "ok"); err != nil {
		return awserr.Internalf(err)
	}
	return nil
}

// Off the main listener neither path fallback is claimed: the admin part
// of rule 1 answers them as understudy's own, with no policy added.
func TestPathFallbacksOffTheMainListenerAreNotSandboxed(t *testing.T) {
	h, err := edge.New(edge.Options{Kernel: kernelOf(t), Registry: mustRegistry(t), Listener: ref.ListenerContainer, Admin: &recorder{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/_understudy/lambda-url/abc123/", "/_understudy/execute-api/abc123/prod/pets"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, request(t, http.MethodGet, path, nil, "", ""))
		if got := policies(rec); len(got) != 0 {
			t.Errorf("container listener %s: Content-Security-Policy = %q, want none", path, got)
		}
	}
}

// SNS links are understudy's own pages under /_understudy/, so the edge
// adds no policy to them.
func TestSNSLinksAreNotSandboxed(t *testing.T) {
	svc := &registry.Service{ID: "sns", SigningName: "sns", IAMPrefix: "sns", ARNNamespace: "sns", Protocols: []ref.ProtocolID{ref.AWSQuery}, QueryVersion: "2010-03-31"}
	svc.Operations = []*registry.Operation{{Service: svc, ID: ref.OperationID{Service: "sns", Name: "SigningCert"}, Kind: registry.Raw}}
	f := newFrontWith(t, nil, func(o *edge.Options) {
		kernel.BindRawHTTP(o.Kernel.Binder(svc), svc.Operations[0], page(http.StatusOK, ""), registry.Full{})
		if err := o.Kernel.BindErr(); err != nil {
			t.Fatal(err)
		}
	}, svc)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodGet, "/_understudy/sns/SigningCert", nil, "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /_understudy/sns/SigningCert = %d %s, want the link's 200", rec.Code, rec.Body)
	}
	if got := policies(rec); len(got) != 0 {
		t.Errorf("SNS link Content-Security-Policy = %q, want none", got)
	}
}
