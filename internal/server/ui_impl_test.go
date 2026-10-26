package server_test

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/server/ui"
)

// getRecorded sends GET target with header to h and returns the recorded
// answer.
func getRecorded(t *testing.T, h http.Handler, target string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
	for name, values := range header {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A navigation cannot send a bearer token and the page holds no secret, so
// the UI's files are served without the token; the page's admin calls
// still need it beside the session secret (ADR 0020).
func TestUIIsServedWithoutTheAdminToken(t *testing.T) {
	srv := newServer(t, server.Config{AdminToken: "s3cret", UISecret: e2eUISecret, LogOutput: io.Discard})
	h := srv.Handler()
	if rec := getRecorded(t, h, "http://localhost:4566/_understudy/ui/", nil); rec.Code != http.StatusOK || rec.Header().Get("Content-Security-Policy") != ui.CSP {
		t.Errorf("GET /_understudy/ui/ without the token = %d %v, want the page", rec.Code, rec.Header())
	}

	page := http.Header{"Sec-Fetch-Site": {"same-origin"}, "Origin": {"http://localhost:4566"}, admin.UISessionHeader: {e2eUISecret}}
	rec := getRecorded(t, h, "http://localhost:4566"+admin.Prefix+"/ui/session", page)
	var env admin.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || rec.Code != http.StatusUnauthorized || env.Error.Code != admin.Unauthorized ||
		rec.Header().Get("WWW-Authenticate") != `Bearer realm="understudy"` {
		t.Errorf("the page's call with the secret and no token = %d %v %s, want 401 unauthorized with a Bearer challenge", rec.Code, rec.Header(), rec.Body)
	}
	page.Set("Authorization", "Bearer s3cret")
	rec = getRecorded(t, h, "http://localhost:4566"+admin.Prefix+"/ui/session", page)
	var session admin.UISession
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil || rec.Code != http.StatusOK || !session.Valid {
		t.Errorf("the page's call with the secret and the token = %d %s, want valid", rec.Code, rec.Body)
	}
}

// The edge refuses a cross-site request before the UI sees it, so a site
// cannot frame or fetch the page from another origin (ADR 0020).
func TestEdgeRefusesCrossSiteRequestsForTheUI(t *testing.T) {
	srv := newServer(t, server.Config{UISecret: e2eUISecret, LogOutput: io.Discard})
	for name, header := range map[string]http.Header{
		"cross-site":     {"Sec-Fetch-Site": {"cross-site"}},
		"foreign origin": {"Origin": {"https://example.com"}},
		"null origin":    {"Origin": {"null"}},
	} {
		rec := getRecorded(t, srv.Handler(), "http://localhost:4566/_understudy/ui/", header)
		if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("GET /_understudy/ui/ %s = %d %s, want 403 and no page", name, rec.Code, rec.Body)
		}
	}
}

// The TLS listener shares the main listener's front door, so it serves the
// UI too, and the page's calls pass with an https Origin, its own, and not
// with the http spelling of the same host.
func TestTLSListenerServesTheUI(t *testing.T) {
	r := launch(t, server.Config{UISecret: e2eUISecret, TLS: server.TLSSelfSigned{Listen: netip.MustParseAddrPort("127.0.0.1:0")}})
	r.stopOnCleanup(t)
	data, err := os.ReadFile(r.ready)
	if err != nil {
		t.Fatal(err)
	}
	var doc readyDoc
	if err := json.Unmarshal(data, &doc); err != nil || !strings.HasPrefix(doc.TLSEndpoint, "https://127.0.0.1:") {
		t.Fatalf("ready file = %s, %v; want a tlsEndpoint", data, err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // the self-signed certificate is TestTLSListenerServesTheAPI's subject, not this test's
	defer client.CloseIdleConnections()
	call := func(path string, header http.Header) (int, http.Header, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, doc.TLSEndpoint+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = header
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		if err := errors.Join(err, resp.Body.Close()); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header, string(body)
	}

	if status, header, body := call("/_understudy/ui/", http.Header{}); status != http.StatusOK || header.Get("Content-Security-Policy") != ui.CSP {
		t.Errorf("GET /_understudy/ui/ over TLS = %d %v %s, want the page", status, header, body)
	}
	host := strings.TrimPrefix(doc.TLSEndpoint, "https://")
	for origin, want := range map[string]int{
		"https://" + host: http.StatusOK,
		"http://" + host:  http.StatusForbidden,
	} {
		header := http.Header{"Sec-Fetch-Site": {"same-origin"}, "Origin": {origin}, admin.UISessionHeader: {e2eUISecret}}
		if status, _, body := call(admin.Prefix+"/ui/session", header); status != want {
			t.Errorf("the page's call over TLS with Origin %s = %d %s, want %d", origin, status, body, want)
		}
	}
}
