package admin_test

import (
	"encoding/json"
	"io"
	"maps"
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

// uiSecret has the shape of a secret serve draws: 26 characters of A-Z and
// 2-7, rand.Text's alphabet (ADR 0020: drawn from crypto/rand).
const uiSecret = "ABCDEFGHIJKLMNOPQRSTUVWX27"

// canonical is h with every name in its canonical form, as a server reads
// it off the wire: admin.UISessionHeader is spelled Understudy-UI-Session,
// which http.Header stores as Understudy-Ui-Session.
func canonical(h http.Header) http.Header {
	out := http.Header{}
	for name, values := range h {
		out[http.CanonicalHeaderKey(name)] = append(out[http.CanonicalHeaderKey(name)], values...)
	}
	return out
}

// pageCall sends method target with header as given, each name with every
// value listed, and a JSON body when body is not empty. httptest gives a
// path target the Host example.com, so the page's own origin is
// http://example.com; an https target sets r.TLS.
func pageCall(k *kerneltest.K, h http.Handler, method, target, body string, header http.Header) *httptest.ResponseRecorder {
	k.Helper()
	var r io.Reader = http.NoBody
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(k.Context(), method, target, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	maps.Copy(req.Header, canonical(header))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// sameOrigin is what a fetch from the UI page itself sends (ADR 0020: the
// one pass is Sec-Fetch-Site same-origin plus the secret), with extra on
// top.
func sameOrigin(secret string, extra http.Header) http.Header {
	h := http.Header{"Sec-Fetch-Site": {"same-origin"}, admin.UISessionHeader: {secret}}
	maps.Copy(h, extra)
	return canonical(h)
}

// uiHandler is the admin handler over k with uiSecret as the run's UI
// secret and svc as its one service.
func uiHandler(k *kerneltest.K, svc *lateService, set func(*serveradmin.Options)) *serveradmin.Handler {
	k.Helper()
	return handlerWith(k, func(o *serveradmin.Options) {
		o.Services, o.Late = []serveradmin.Service{svc}, []snapshot.Late{svc}
		o.UISecret = uiSecret
		if set != nil {
			set(o)
		}
	})
}

// wantRefused sends method target with hdr and checks the answer is the
// browser refusal: 403 invalid_request (ADR 0020: every call refused today
// without the secret is still refused) and, when the call carried a session
// value, a hint pointing at the UI URL serve prints.
func wantRefused(k *kerneltest.K, h http.Handler, what, method, target string, hdr http.Header) {
	k.Helper()
	rec := pageCall(k, h, method, target, "", hdr)
	var env admin.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || rec.Code != http.StatusForbidden || env.Error == nil || env.Error.Code != admin.InvalidRequest {
		k.Errorf("%s = %d %s, want 403 invalid_request", what, rec.Code, rec.Body)
		return
	}
	if canonical(hdr).Get(admin.UISessionHeader) != "" && !strings.Contains(env.Error.Hint, "/_understudy/ui/") {
		k.Errorf("%s refused with hint %q, want it to name /_understudy/ui/", what, env.Error.Hint)
	}
}

// The UI page's own calls, same-origin with this run's secret, pass the
// browser check: reads and every mutating call the UI makes (reset, clock,
// fault add and remove) are served (ADR 0020).
func TestUISessionPassServesTheUIsOwnCalls(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := &lateService{}
		h := uiHandler(k, svc, nil)
		for _, extra := range []http.Header{
			nil,
			{"Origin": {"http://example.com"}},
			// Origins compare without regard to ASCII case (ADR 0020: Origin,
			// when sent, the listener's own).
			{"Origin": {"HTTP://Example.COM"}},
		} {
			hdr := sameOrigin(uiSecret, extra)
			if rec := pageCall(k, h, http.MethodGet, admin.Prefix+"/journal/events", "", hdr); rec.Code != http.StatusOK {
				k.Errorf("GET journal/events from the UI with %v = %d %s, want 200", extra, rec.Code, rec.Body)
			}
			before := len(svc.resets)
			if rec := pageCall(k, h, http.MethodPost, admin.Prefix+"/reset", "", hdr); rec.Code != http.StatusOK || len(svc.resets) != before+1 {
				k.Errorf("POST reset from the UI with %v = %d %s, resets %d; want 200 and one more reset", extra, rec.Code, rec.Body, len(svc.resets)-before)
			}
		}

		hdr := sameOrigin(uiSecret, http.Header{"Origin": {"http://example.com"}})
		now := k.Clock.Now()
		if rec := pageCall(k, h, http.MethodPost, admin.Prefix+"/clock", `{"op": "advance", "by": "1h"}`, hdr); rec.Code != http.StatusOK || k.Clock.Now().Sub(now) != time.Hour {
			k.Errorf("POST clock advance from the UI = %d %s, clock moved %v; want 200 and 1h", rec.Code, rec.Body, k.Clock.Now().Sub(now))
		}
		rule, err := json.Marshal(admin.FaultRule{Phase: admin.DeliveryPhase, Action: admin.Latency{Wall: time.Second}, Trigger: admin.Always{}})
		if err != nil {
			k.Fatal(err)
		}
		if rec := pageCall(k, h, http.MethodPut, admin.Prefix+"/faults/ui-rule", string(rule), hdr); rec.Code != http.StatusOK || len(k.Faults.List()) != 1 {
			k.Errorf("PUT fault from the UI = %d %s, rules %v; want 200 and the rule added", rec.Code, rec.Body, k.Faults.List())
		}
		if rec := pageCall(k, h, http.MethodDelete, admin.Prefix+"/faults/ui-rule", "", hdr); rec.Code >= 300 || len(k.Faults.List()) != 0 {
			k.Errorf("DELETE fault from the UI = %d %s, rules %v; want success and no rule", rec.Code, rec.Body, k.Faults.List())
		}
	})
}

// A browser call passes only same-origin, with exactly one session value
// equal to the run's secret and at most one Origin, the request's own;
// anything else is refused before it changes anything, the secret
// notwithstanding (ADR 0020: Sec-Fetch-Site same-origin, Origin when sent
// the listener's own, secret compared in constant time).
func TestUISessionPassRefusesEverythingElse(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := &lateService{}
		h := uiHandler(k, svc, nil)
		for _, tc := range []struct {
			name string
			hdr  http.Header
		}{
			{"cross-site", http.Header{"Sec-Fetch-Site": {"cross-site"}, admin.UISessionHeader: {uiSecret}}},
			{"same-site", http.Header{"Sec-Fetch-Site": {"same-site"}, admin.UISessionHeader: {uiSecret}}},
			{"Origin without Fetch Metadata", http.Header{"Origin": {"http://example.com"}, admin.UISessionHeader: {uiSecret}}},
			{"foreign Origin", sameOrigin(uiSecret, http.Header{"Origin": {"https://attacker.example"}})},
			{"null Origin", sameOrigin(uiSecret, http.Header{"Origin": {"null"}})},
			{"Origin on another port", sameOrigin(uiSecret, http.Header{"Origin": {"http://example.com:4566"}})},
			{"Origin with the TLS scheme on plain HTTP", sameOrigin(uiSecret, http.Header{"Origin": {"https://example.com"}})},
			{"two Origin values", sameOrigin(uiSecret, http.Header{"Origin": {"http://example.com", "http://example.com"}})},
			{"two session values", http.Header{"Sec-Fetch-Site": {"same-origin"}, admin.UISessionHeader: {uiSecret, uiSecret}}},
			{"wrong secret", sameOrigin("ABCDEFGHIJKLMNOPQRSTUVWX22", nil)},
			{"secret prefix", sameOrigin(uiSecret[:25], nil)},
			{"empty secret", sameOrigin("", nil)},
		} {
			wantRefused(k, h, "POST reset, "+tc.name, http.MethodPost, admin.Prefix+"/reset", tc.hdr)
			wantRefused(k, h, "GET journal/events, "+tc.name, http.MethodGet, admin.Prefix+"/journal/events", tc.hdr)
		}
		if len(svc.resets) != 0 {
			k.Errorf("refused calls reset %d times, want none", len(svc.resets))
		}

		// Without a session secret no browser call passes, whatever it sends.
		none := handlerWith(k, func(o *serveradmin.Options) { o.Services = []serveradmin.Service{svc} })
		for _, secret := range []string{"", uiSecret} {
			wantRefused(k, none, "POST reset to a handler without a secret, session "+secret, http.MethodPost, admin.Prefix+"/reset", sameOrigin(secret, nil))
		}
		if len(svc.resets) != 0 {
			k.Errorf("calls to a handler without a secret reset %d times, want none", len(svc.resets))
		}
	})
}

// Over TLS the page's own origin is https://<Host>; the plain-HTTP spelling
// of it is another origin (ADR 0020).
func TestUISessionPassTakesTheSchemeFromTheConnection(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := uiHandler(k, &lateService{}, nil)
		target := "https://127.0.0.1:4567" + admin.Prefix + "/journal/events"
		if rec := pageCall(k, h, http.MethodGet, target, "", sameOrigin(uiSecret, http.Header{"Origin": {"https://127.0.0.1:4567"}})); rec.Code != http.StatusOK {
			k.Errorf("GET over TLS with its own https Origin = %d %s, want 200", rec.Code, rec.Body)
		}
		wantRefused(k, h, "GET over TLS with an http Origin", http.MethodGet, target, sameOrigin(uiSecret, http.Header{"Origin": {"http://127.0.0.1:4567"}}))
	})
}

// A browser call that passes still needs the admin token where one is
// configured (ADR 0020: the admin token, where configured, is still
// required; ADR 0017: Bearer beyond loopback).
func TestUISessionPassStillNeedsTheToken(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := uiHandler(k, &lateService{}, func(o *serveradmin.Options) { o.Token = "s3cret" })
		rec := pageCall(k, h, http.MethodGet, admin.Prefix+"/journal/events", "", sameOrigin(uiSecret, nil))
		var env admin.Envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || rec.Code != http.StatusUnauthorized || env.Error == nil ||
			env.Error.Code != admin.Unauthorized || rec.Header().Get("WWW-Authenticate") != `Bearer realm="understudy"` {
			k.Errorf("UI call without the token = %d %s, WWW-Authenticate %q; want 401 unauthorized with the Bearer challenge", rec.Code, rec.Body, rec.Header().Get("WWW-Authenticate"))
		}
		if rec := pageCall(k, h, http.MethodGet, admin.Prefix+"/journal/events", "", sameOrigin(uiSecret, http.Header{"Authorization": {"Bearer s3cret"}})); rec.Code != http.StatusOK {
			k.Errorf("UI call with the token = %d %s, want 200", rec.Code, rec.Body)
		}
		// The browser check runs first: a wrong secret is refused as a page's
		// call, token or not.
		wantRefused(k, h, "UI call with a wrong secret and the token", http.MethodGet, admin.Prefix+"/journal/events",
			sameOrigin("ABCDEFGHIJKLMNOPQRSTUVWX22", http.Header{"Authorization": {"Bearer s3cret"}}))
	})
}

// The session header changes nothing for a call that carries neither Origin
// nor Fetch Metadata, as the CLI's and curl's do: served or refused as
// without it.
func TestUISessionHeaderDoesNotAffectNonBrowserCalls(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		open := uiHandler(k, &lateService{}, nil)
		guarded := uiHandler(k, &lateService{}, func(o *serveradmin.Options) { o.Token = "s3cret" })
		for _, secret := range []string{uiSecret, "wrong", ""} {
			hdr := http.Header{admin.UISessionHeader: {secret}}
			if rec := pageCall(k, open, http.MethodGet, admin.Prefix+"/health", "", hdr); rec.Code != http.StatusOK {
				k.Errorf("CLI GET health with session %q = %d %s, want 200", secret, rec.Code, rec.Body)
			}
			if rec := pageCall(k, guarded, http.MethodGet, admin.Prefix+"/health", "", hdr); rec.Code != http.StatusUnauthorized {
				k.Errorf("CLI GET health without the token, session %q = %d %s, want 401", secret, rec.Code, rec.Body)
			}
		}
	})
}

// The container listener's handler is unchanged by the session pass: a
// page's call with the secret is answered as one without it.
func TestUISessionPassIsNotOnTheContainerListener(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := uiHandler(k, &lateService{}, nil).Container()
		for _, req := range []struct{ method, path string }{
			{http.MethodGet, admin.Prefix + "/clock"},
			{http.MethodPost, admin.Prefix + "/reset"},
			{http.MethodGet, admin.Prefix + "/journal/events"},
		} {
			page := http.Header{"Sec-Fetch-Site": {"same-origin"}}
			without := pageCall(k, h, req.method, req.path, "", page)
			with := pageCall(k, h, req.method, req.path, "", sameOrigin(uiSecret, nil))
			if with.Code != without.Code {
				k.Errorf("container %s %s with the session = %d %s, without = %d; want the same answer", req.method, req.path, with.Code, with.Body, without.Code)
			}
		}
	})
}

// No preflight is approved, so a cross-origin page can never send the
// session header: OPTIONS carrying Origin is refused as today, with no CORS
// grant (ADR 0020: the admin API approves no preflight).
func TestPreflightForTheSessionHeaderIsRefused(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := uiHandler(k, &lateService{}, nil)
		rec := pageCall(k, h, http.MethodOptions, admin.Prefix+"/reset", "", http.Header{
			"Origin":                         {"https://attacker.example"},
			"Sec-Fetch-Site":                 {"cross-site"},
			"Access-Control-Request-Method":  {"POST"},
			"Access-Control-Request-Headers": {strings.ToLower(admin.UISessionHeader)},
		})
		if rec.Code != http.StatusForbidden {
			k.Errorf("preflight for the session header = %d %s, want 403", rec.Code, rec.Body)
		}
		for name := range rec.Header() {
			if strings.HasPrefix(name, "Access-Control-Allow-") {
				k.Errorf("preflight answered %s: %v, want no CORS grant", name, rec.Header()[name])
			}
		}
	})
}

// uiSession reads GET /ui/session with header and returns the status and
// the decoded body.
func uiSession(k *kerneltest.K, h http.Handler, header http.Header) (status int, body map[string]any) {
	k.Helper()
	rec := pageCall(k, h, http.MethodGet, admin.Prefix+"/ui/session", "", header)
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			k.Fatalf("GET ui/session = %s: %v", rec.Body, err)
		}
	}
	return rec.Code, body
}

// GET /ui/session reports whether the call carried exactly one session
// value equal to the run's secret, valid always present; Fetch Metadata
// does not enter into it (approved addition; ADR 0020).
func TestUISessionEndpointReportsValidity(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := uiHandler(k, &lateService{}, nil)
		for _, tc := range []struct {
			name  string
			hdr   http.Header
			valid bool
		}{
			{"the secret from the CLI", http.Header{admin.UISessionHeader: {uiSecret}}, true},
			{"the secret from the page", sameOrigin(uiSecret, http.Header{"Origin": {"http://example.com"}}), true},
			{"no header", nil, false},
			{"a wrong secret", http.Header{admin.UISessionHeader: {"ABCDEFGHIJKLMNOPQRSTUVWX22"}}, false},
			{"an empty secret", http.Header{admin.UISessionHeader: {""}}, false},
			{"two values", http.Header{admin.UISessionHeader: {uiSecret, uiSecret}}, false},
		} {
			status, body := uiSession(k, h, tc.hdr)
			if v, ok := body["valid"].(bool); status != http.StatusOK || !ok || v != tc.valid || len(body) != 1 {
				k.Errorf("GET ui/session with %s = %d %v, want 200 {\"valid\":%t}", tc.name, status, body, tc.valid)
			}
		}

		none := handlerWith(k, func(*serveradmin.Options) {})
		for _, secret := range []string{"", uiSecret} {
			if status, body := uiSession(k, none, http.Header{admin.UISessionHeader: {secret}}); status != http.StatusOK || body["valid"] != false {
				k.Errorf("GET ui/session without a run secret, session %q = %d %v, want valid false", secret, status, body)
			}
		}

		head := pageCall(k, h, http.MethodHead, admin.Prefix+"/ui/session", "", http.Header{admin.UISessionHeader: {uiSecret}})
		if head.Code != http.StatusOK || head.Body.Len() != 0 {
			k.Errorf("HEAD ui/session = %d with %d body bytes, want 200 and no body", head.Code, head.Body.Len())
		}
		if rec := pageCall(k, h, http.MethodGet, admin.Prefix+"/ui/session?verbose=1", "", nil); rec.Code != http.StatusBadRequest {
			k.Errorf("GET ui/session with a query parameter = %d %s, want 400 as every route refuses one", rec.Code, rec.Body)
		}

		guarded := uiHandler(k, &lateService{}, func(o *serveradmin.Options) { o.Token = "s3cret" })
		if status, _ := uiSession(k, guarded, http.Header{admin.UISessionHeader: {uiSecret}}); status != http.StatusUnauthorized {
			k.Errorf("GET ui/session without the token = %d, want 401", status)
		}
		if status, body := uiSession(k, guarded, http.Header{admin.UISessionHeader: {uiSecret}, "Authorization": {"Bearer s3cret"}}); status != http.StatusOK || body["valid"] != true {
			k.Errorf("GET ui/session with the token = %d %v, want valid true", status, body)
		}
	})
}
