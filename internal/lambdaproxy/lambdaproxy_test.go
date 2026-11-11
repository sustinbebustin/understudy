package lambdaproxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/lambdaproxy"
)

func TestReadBody(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		limit    int
		want     string
		tooLarge bool
	}{
		{name: "under the limit", body: "abc", limit: 4, want: "abc"},
		{name: "at the limit", body: "abcd", limit: 4, want: "abcd"},
		{name: "over the limit", body: "abcde", limit: 4, tooLarge: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(tc.body))
			body, tooLarge, err := lambdaproxy.ReadBody(r, tc.limit)
			if err != nil {
				t.Fatalf("ReadBody: %v", err)
			}
			if tooLarge != tc.tooLarge || string(body) != tc.want {
				t.Errorf("ReadBody = %q, tooLarge %v; want %q, tooLarge %v", body, tooLarge, tc.want, tc.tooLarge)
			}
		})
	}
}

func TestReadBodyWithoutBody(t *testing.T) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	r.Body = nil
	body, tooLarge, err := lambdaproxy.ReadBody(r, 4)
	if body != nil || tooLarge || err != nil {
		t.Errorf("ReadBody of a request without a body = %q, %v, %v; want nil, false, nil", body, tooLarge, err)
	}
}

func TestEscapedPathKeepsTheClientsEscapes(t *testing.T) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/a%2Fb/c%20d?x=1", http.NoBody)
	if got, want := lambdaproxy.EscapedPath(r), "/a%2Fb/c%20d"; got != want {
		t.Errorf("EscapedPath = %q, want %q", got, want)
	}
}

func TestHostName(t *testing.T) {
	for host, want := range map[string]string{
		"Abc.Execute-Api.localhost:4566": "abc.execute-api.localhost",
		"abc.execute-api.localhost":      "abc.execute-api.localhost",
		"[::1]:4566":                     "::1",
	} {
		if got := lambdaproxy.HostName(host); got != want {
			t.Errorf("HostName(%q) = %q, want %q", host, got, want)
		}
	}
}
