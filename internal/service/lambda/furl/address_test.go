package furl_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/lambda/furl"
)

func TestResolveReadsTheHostForm(t *testing.T) {
	a, ok := furl.Resolve("AbC123.lambda-url.us-west-2.localhost:4566", "/my/pa%2Fth")
	want := furl.Address{ID: "abc123", Host: "abc123.lambda-url.us-west-2.localhost", Region: "us-west-2", RawPath: "/my/pa%2Fth"}
	if !ok || a != want {
		t.Fatalf("Resolve = %+v, %v; want %+v", a, ok, want)
	}
	if d := a.Domain("eu-west-1"); d != want.Host {
		t.Errorf("Domain = %q, want the host the request named", d)
	}
}

func TestResolveReadsThePathFallback(t *testing.T) {
	a, ok := furl.Resolve("127.0.0.1:4566", "/_understudy/lambda-url/abc123/my/path")
	want := furl.Address{ID: "abc123", RawPath: "/my/path"}
	if !ok || a != want {
		t.Fatalf("Resolve = %+v, %v; want %+v", a, ok, want)
	}
	if d := a.Domain("us-east-1"); d != "abc123.lambda-url.us-east-1.localhost" {
		t.Errorf("Domain = %q, want the host form under localhost", d)
	}
	if a, ok := furl.Resolve("localhost", "/_understudy/lambda-url/abc123"); !ok || a.RawPath != "/" {
		t.Errorf("Resolve without a path = %+v, %v; want the root path", a, ok)
	}
}

func TestResolveRefusesWhatNamesNoFunctionURL(t *testing.T) {
	for _, c := range []struct{ host, path string }{
		{"localhost:4566", "/2015-03-31/functions"},
		{"bucket.s3.localhost", "/key"},
		{"abc.lambda-url.localhost", "/"},
		{"ab-c.lambda-url.us-east-1.localhost", "/"},
		{"localhost", "/_understudy/lambda-url//x"},
		{"localhost", "/_understudy/lambda-url/a%2Fb/x"},
	} {
		if a, ok := furl.Resolve(c.host, c.path); ok {
			t.Errorf("Resolve(%q, %q) = %+v, want no function URL", c.host, c.path, a)
		}
	}
}

// The host form and the path fallback name the same URL and path.
func TestResolveHostAndPathFormsAgree(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		id := rapid.StringMatching(`[a-z0-9]{1,32}`).Draw(t, "id")
		region := rapid.StringMatching(`[a-z]{2}-[a-z]{4,9}-[1-9]`).Draw(t, "region")
		path := rapid.StringMatching(`(/[A-Za-z0-9._~%-]{0,8}){1,4}`).Draw(t, "path")
		host, okHost := furl.Resolve(id+".lambda-url."+region+".localhost:4566", path)
		fallback, okPath := furl.Resolve("127.0.0.1:4566", furl.PathPrefix+id+path)
		if !okHost || !okPath {
			t.Fatalf("Resolve: host form %v, path fallback %v; want both", okHost, okPath)
		}
		if host.ID != fallback.ID || host.RawPath != fallback.RawPath || host.RawPath != path {
			t.Fatalf("host form %+v, path fallback %+v; want id %q and path %q from both", host, fallback, id, path)
		}
		if host.Domain(region) != fallback.Domain(region) {
			t.Fatalf("domains %q and %q differ", host.Domain(region), fallback.Domain(region))
		}
	})
}
