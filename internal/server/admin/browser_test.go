package admin_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
	"github.com/sustinbebustin/understudy/internal/snapshot"
)

// call sends method path with body declared as contentType ("" sends no
// Content-Type) and the extra headers.
func call(k *kerneltest.K, h http.Handler, method, path, contentType, body string, header map[string]string) *httptest.ResponseRecorder {
	k.Helper()
	var r io.Reader = http.NoBody
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(k.Context(), method, path, r)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for name, v := range header {
		req.Header.Set(name, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A call a web page makes, cross-site, from a hostname rebound to loopback
// or from a page the emulator itself serves, is refused before it changes
// or reveals anything, token or not; the CLI's and curl's calls, which carry
// neither Origin nor Sec-Fetch-Site, and a URL the user types, are served.
func TestMainListenerRefusesCallsAWebPageMakes(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := &lateService{}
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Services, o.Late = []serveradmin.Service{svc}, []snapshot.Late{svc}
		})
		for _, header := range []map[string]string{
			{"Origin": "https://attacker.example"},
			{"Origin": "http://rebound.attacker.example:4566"},
			{"Origin": "null"},
			{"Origin": "http://localhost:4566"},
			{"Sec-Fetch-Site": "cross-site"},
			{"Sec-Fetch-Site": "same-site"},
			{"Sec-Fetch-Site": "same-origin"},
		} {
			// A bodyless POST is a CORS simple request: no preflight runs.
			rec := call(k, h, http.MethodPost, admin.Prefix+"/reset", "", "", header)
			var env admin.Envelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || rec.Code != http.StatusForbidden ||
				env.Error == nil || env.Error.Code != admin.InvalidRequest || len(svc.resets) != 0 {
				k.Fatalf("POST reset with %v = %d %s, resets %v; want 403 invalid_request and no reset", header, rec.Code, rec.Body, svc.resets)
			}
			if rec := call(k, h, http.MethodGet, admin.Prefix+"/journal/events", "", "", header); rec.Code != http.StatusForbidden {
				k.Fatalf("GET journal/events with %v = %d %s, want 403", header, rec.Code, rec.Body)
			}
		}
		if rec := call(k, handler(k, "s3cret"), http.MethodGet, admin.Prefix+"/health", "", "", map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
			k.Errorf("cross-site GET health with a token configured = %d %s, want 403", rec.Code, rec.Body)
		}
		if rec := call(k, h, http.MethodGet, admin.Prefix+"/health", "", "", map[string]string{"Sec-Fetch-Site": "none"}); rec.Code != http.StatusOK {
			k.Errorf("GET health typed into the address bar = %d %s, want 200", rec.Code, rec.Body)
		}
		if rec := call(k, h, http.MethodPost, admin.Prefix+"/reset", "", "", nil); rec.Code != http.StatusOK || len(svc.resets) != 1 {
			k.Errorf("bodyless POST reset from an HTTP client = %d %s, resets %v; want 200 and one reset", rec.Code, rec.Body, svc.resets)
		}
	})
}

// A body a page can send cross-site without a preflight (text/plain, a
// form) is refused even when it holds valid JSON; the same body declared
// application/json is served.
func TestAdminReadsOnlyJSONBodies(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := &lateService{}
		h := handlerWith(k, func(o *serveradmin.Options) {
			o.Services, o.Late = []serveradmin.Service{svc}, []snapshot.Late{svc}
		})
		before := k.Clock.Now()
		rule := `{"phase": "request", "match": {"operations": ["sqs.SendMessage"]}, "action": {"kind": "throttle"}, "trigger": {"kind": "always"}}`
		for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", ""} {
			for _, req := range []struct{ method, path, body string }{
				{http.MethodPost, admin.Prefix + "/clock", `{"op": "advance", "by": "1h"}`},
				{http.MethodPost, admin.Prefix + "/reset", `{}`},
				{http.MethodPut, admin.Prefix + "/faults/f1", rule},
			} {
				rec := call(k, h, req.method, req.path, ct, req.body, nil)
				var env admin.Envelope
				if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || rec.Code != http.StatusBadRequest ||
					env.Error == nil || !strings.Contains(env.Error.Message, "application/json") {
					k.Fatalf("%s %s as %q = %d %s, want 400 naming application/json", req.method, req.path, ct, rec.Code, rec.Body)
				}
			}
		}
		if k.Clock.Now().Compare(before) != 0 || len(svc.resets) != 0 || len(k.Faults.List()) != 0 {
			k.Fatalf("refused bodies changed state: clock %v (was %v), resets %v, faults %v", k.Clock.Now(), before, svc.resets, k.Faults.List())
		}
		if rec := call(k, h, http.MethodPost, admin.Prefix+"/clock", "application/json; charset=utf-8", `{"op": "advance", "by": "1h"}`, nil); rec.Code != http.StatusOK || k.Clock.Now().Sub(before) != time.Hour {
			k.Fatalf("POST clock as application/json = %d %s; clock %v", rec.Code, rec.Body, k.Clock.Now())
		}
	})
}
