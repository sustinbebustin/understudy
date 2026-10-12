package furl_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sustinbebustin/understudy/internal/service/lambda/furl"
)

func cors() furl.CORS {
	return furl.CORS{
		AllowCredentials: true,
		AllowHeaders:     []string{"content-type", "x-custom"},
		AllowMethods:     []string{"GET", "POST"},
		AllowOrigins:     []string{"https://example.com"},
		ExposeHeaders:    []string{"x-request-id"},
		MaxAge:           new(int32(300)),
	}
}

func preflight(origin string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "http://abc.lambda-url.us-east-1.localhost/", http.NoBody)
	r.Header.Set("Origin", origin)
	r.Header.Set("Access-Control-Request-Method", "POST")
	return r
}

func TestPreflightAnswersWithTheConfiguredHeaders(t *testing.T) {
	resp, ok := cors().Preflight(preflight("https://example.com"))
	if !ok {
		t.Fatal("Preflight did not answer a preflight request")
	}
	want := map[string]string{
		"Access-Control-Allow-Origin":      "https://example.com",
		"Access-Control-Allow-Credentials": "true",
		"Access-Control-Allow-Methods":     "GET,POST",
		"Access-Control-Allow-Headers":     "content-type,x-custom",
		"Access-Control-Max-Age":           "300",
		"Vary":                             "Origin",
	}
	for name, value := range want {
		if got := resp.Header.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	if resp.Status != http.StatusOK || resp.Header.Get("Access-Control-Expose-Headers") != "" {
		t.Errorf("preflight = %d %v, want 200 without Expose-Headers", resp.Status, resp.Header)
	}
}

func TestPreflightOfADisallowedOriginCarriesNoCORSHeaders(t *testing.T) {
	resp, ok := cors().Preflight(preflight("https://evil.example"))
	if !ok || len(resp.Header) != 0 {
		t.Errorf("Preflight = %v %v, want an answer with no CORS headers", ok, resp.Header)
	}
}

func TestRequestsThatAreNotPreflightsReachTheFunction(t *testing.T) {
	plain := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "http://abc.lambda-url.us-east-1.localhost/", http.NoBody)
	get := preflight("https://example.com")
	get.Method = http.MethodGet
	for _, r := range []*http.Request{plain, get} {
		if _, ok := cors().Preflight(r); ok {
			t.Errorf("Preflight answered %s with headers %v", r.Method, r.Header)
		}
	}
}

func TestDecorateAddsTheConfiguredHeadersOverTheFunctions(t *testing.T) {
	resp := furl.Response{Status: 200, Header: http.Header{"Access-Control-Allow-Origin": {"https://other.example"}}}
	c := cors()
	c.AllowOrigins = []string{"*"}
	c.Decorate(&resp, "https://example.com")
	if resp.Header.Values("Access-Control-Allow-Origin")[0] != "*" || len(resp.Header.Values("Access-Control-Allow-Origin")) != 1 ||
		resp.Header.Get("Access-Control-Expose-Headers") != "x-request-id" || resp.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("headers = %v, want the configured CORS headers once each", resp.Header)
	}
	plain := furl.Response{Status: 200, Header: http.Header{}}
	c.Decorate(&plain, "")
	if len(plain.Header) != 0 {
		t.Errorf("a request without Origin got CORS headers %v", plain.Header)
	}
}
