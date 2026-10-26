package ui_test

import (
	"io/fs"
	"net/http"
	"strconv"
	"testing"
	"testing/fstest"
)

// A regular file below a directory of the build is an asset like any other.
func TestHandlerServesANestedAsset(t *testing.T) {
	rec := get(t, newHandler(t, built()), http.MethodGet, "/_understudy/ui/chunks/part-1.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "x" || rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" ||
		rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("GET chunks/part-1.js = %d %q, headers %v; want its bytes, cached immutable", rec.Code, rec.Body, rec.Header())
	}
}

// HEAD answers with GET's status and headers and no body, for the page, the
// redirect, a missing file and a refused method alike (RFC 9110 9.3.2).
func TestHandlerHeadHasNoBody(t *testing.T) {
	h := newHandler(t, built())
	page := get(t, h, http.MethodHead, "/_understudy/ui/")
	if page.Code != http.StatusOK || page.Body.Len() != 0 || page.Header().Get("Cache-Control") != "no-cache" ||
		page.Header().Get("Content-Length") != strconv.Itoa(len(indexHTML)) {
		t.Errorf("HEAD /_understudy/ui/ = %d with %d body bytes, headers %v; want the page's headers and no body", page.Code, page.Body.Len(), page.Header())
	}
	for target, want := range map[string]int{
		"/_understudy/ui":            http.StatusMovedPermanently,
		"/_understudy/ui/missing.js": http.StatusNotFound,
	} {
		rec := get(t, h, http.MethodHead, target)
		if rec.Code != want || rec.Body.Len() != 0 {
			t.Errorf("HEAD %s = %d with %d body bytes, want %d and no body", target, rec.Code, rec.Body.Len(), want)
		}
		wantSecurityHeaders(t, "HEAD "+target, rec)
	}
}

// The handler answers only its own prefix; a request outside it, which
// Mount never sends, is a 404 with the UI's headers.
func TestHandlerRefusesPathsOutsideItsPrefix(t *testing.T) {
	for _, target := range []string{"/_understudy/v1/health", "/_understudy/uix", "/"} {
		rec := get(t, newHandler(t, built()), http.MethodGet, target)
		if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("GET %s = %d (%s), want 404 text/plain; charset=utf-8", target, rec.Code, rec.Header().Get("Content-Type"))
		}
		wantSecurityHeaders(t, "GET "+target, rec)
	}
}

// refusing is a build whose file system refuses one name. It offers only
// Open, so fs.Stat and fs.ReadFile cannot reach the files around it.
type refusing struct {
	files fstest.MapFS
	name  string
}

func (r refusing) Open(name string) (fs.File, error) {
	if name == r.name {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}
	return r.files.Open(name)
}

// A name the build's file system refuses is answered as missing.
func TestHandlerAnswersARefusedNameAsMissing(t *testing.T) {
	h := newHandler(t, refusing{files: built(), name: "main-k2x8f1q9.js"})
	if rec := get(t, h, http.MethodGet, "/_understudy/ui/main-k2x8f1q9.js"); rec.Code != http.StatusNotFound {
		t.Errorf("GET of a refused name = %d %q, want 404", rec.Code, rec.Body)
	}
	if rec := get(t, h, http.MethodGet, "/_understudy/ui/main-p3c7a0d2.css"); rec.Code != http.StatusOK {
		t.Errorf("GET of another name = %d %q, want 200", rec.Code, rec.Body)
	}
}
