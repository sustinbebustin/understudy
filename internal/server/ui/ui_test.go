package ui_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sustinbebustin/understudy/internal/server/ui"
)

// Every UI answer carries these, whatever its status (ADR 0020: the ui/
// row's headers are item 10's). The CSP directives: default-src, script-src,
// frame-ancestors, base-uri and form-action, https://www.w3.org/TR/CSP3/;
// nosniff, https://fetch.spec.whatwg.org/#x-content-type-options-header;
// same-origin, https://html.spec.whatwg.org/multipage/browsers.html#cross-origin-opener-policies;
// no-referrer, https://www.w3.org/TR/referrer-policy/#referrer-policy-no-referrer.
var securityHeaders = map[string]string{
	"Content-Security-Policy":    "default-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'",
	"X-Content-Type-Options":     "nosniff",
	"Cross-Origin-Opener-Policy": "same-origin",
	"Referrer-Policy":            "no-referrer",
}

const indexHTML = `<!doctype html><html><head><script type="module" src="./main-k2x8f1q9.js"></script></head></html>`

// built is a build as bun writes it: index.html and content-hashed assets.
func built() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte(indexHTML)},
		"main-k2x8f1q9.js":     {Data: []byte("console.log(1)")},
		"main-p3c7a0d2.css":    {Data: []byte("body{}")},
		"main-k2x8f1q9.map":    {Data: []byte("{}")},
		"labels-a1b2c3d4.json": {Data: []byte("[]")},
		"logo-a1b2c3d4.svg":    {Data: []byte("<svg/>")},
		"logo-a1b2c3d4.png":    {Data: []byte("png")},
		"icon-a1b2c3d4.ico":    {Data: []byte("ico")},
		"font-a1b2c3d4.woff2":  {Data: []byte("woff2")},
		"blob-a1b2c3d4.bin":    {Data: []byte("bin")},
		"chunks/part-1.js":     {Data: []byte("x")},
	}
}

// newHandler is ui.New over fsys, failing the test on an error.
func newHandler(t *testing.T, fsys fs.FS) *ui.Handler {
	t.Helper()
	h, err := ui.New(fsys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func get(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, target, http.NoBody))
	return rec
}

// wantSecurityHeaders checks rec carries every security header, once.
func wantSecurityHeaders(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	for name, want := range securityHeaders {
		if got := rec.Header().Values(name); len(got) != 1 || got[0] != want {
			t.Errorf("%s: %s = %q, want exactly %q", what, name, got, want)
		}
	}
}

// The package's CSP is the one every answer carries.
func TestCSPConstant(t *testing.T) {
	if ui.CSP != securityHeaders["Content-Security-Policy"] {
		t.Errorf("ui.CSP = %q, want %q", ui.CSP, securityHeaders["Content-Security-Policy"])
	}
	if ui.Path != "/_understudy/ui/" {
		t.Errorf("ui.Path = %q, want /_understudy/ui/ (ADR 0020: the ui/ sub-prefix)", ui.Path)
	}
}

// The page is index.html, never cached without revalidation since it names
// the current build's assets; an asset is cached for good since its name
// changes with its content (RFC 9111 5.2.2.4 no-cache; RFC 8246
// immutable).
func TestHandlerServesTheBuild(t *testing.T) {
	h := newHandler(t, built())
	for _, target := range []string{"/_understudy/ui/", "/_understudy/ui/index.html", "/_understudy/ui/?s=x"} {
		rec := get(t, h, http.MethodGet, target)
		if rec.Code != http.StatusOK || rec.Body.String() != indexHTML ||
			rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || rec.Header().Get("Cache-Control") != "no-cache" ||
			rec.Header().Get("Content-Length") != strconv.Itoa(len(indexHTML)) {
			t.Errorf("GET %s = %d %q, headers %v; want index.html, text/html; charset=utf-8, no-cache and its length", target, rec.Code, rec.Body, rec.Header())
		}
		wantSecurityHeaders(t, "GET "+target, rec)
	}
	for name, ct := range map[string]string{
		"main-k2x8f1q9.js":     "text/javascript; charset=utf-8",
		"main-p3c7a0d2.css":    "text/css; charset=utf-8",
		"main-k2x8f1q9.map":    "application/json",
		"labels-a1b2c3d4.json": "application/json",
		"logo-a1b2c3d4.svg":    "image/svg+xml",
		"logo-a1b2c3d4.png":    "image/png",
		"icon-a1b2c3d4.ico":    "image/x-icon",
		"font-a1b2c3d4.woff2":  "font/woff2",
		"blob-a1b2c3d4.bin":    "application/octet-stream",
	} {
		want := string(built()[name].Data)
		for _, target := range []string{"/_understudy/ui/" + name, "/_understudy/ui/" + name + "?v=2"} {
			rec := get(t, h, http.MethodGet, target)
			if rec.Code != http.StatusOK || rec.Body.String() != want || rec.Header().Get("Content-Type") != ct ||
				rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || rec.Header().Get("Content-Length") != strconv.Itoa(len(want)) {
				t.Errorf("GET %s = %d %q, headers %v; want its bytes as %s, cached immutable", target, rec.Code, rec.Body, rec.Header(), ct)
			}
			wantSecurityHeaders(t, "GET "+target, rec)
		}
		head := get(t, h, http.MethodHead, "/_understudy/ui/"+name)
		if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Type") != ct || head.Header().Get("Content-Length") != strconv.Itoa(len(want)) {
			t.Errorf("HEAD %s = %d with %d body bytes, headers %v; want GET's headers and no body", name, head.Code, head.Body.Len(), head.Header())
		}
		wantSecurityHeaders(t, "HEAD "+name, head)
	}
}

// The bare prefix redirects to the page, so the browser keeps the
// fragment holding the secret (RFC 9110 15.4.2; RFC 9110 10.2.2: a
// redirect keeps the original fragment when Location has none).
func TestHandlerRedirectsTheBarePrefix(t *testing.T) {
	rec := get(t, newHandler(t, built()), http.MethodGet, "/_understudy/ui")
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/_understudy/ui/" {
		t.Errorf("GET /_understudy/ui = %d, Location %q; want 301 to /_understudy/ui/", rec.Code, rec.Header().Get("Location"))
	}
	wantSecurityHeaders(t, "GET /_understudy/ui", rec)
}

// Anything that is not a file of the build is 404 in plain text: a
// directory, a missing name, and a path with an empty, . or .. segment,
// which never reaches the file system.
func TestHandlerRefusesWhatIsNotAFile(t *testing.T) {
	h := newHandler(t, built())
	for _, target := range []string{
		"/_understudy/ui/missing.js",
		"/_understudy/ui/chunks",
		"/_understudy/ui/chunks/",
		"/_understudy/ui//index.html",
		"/_understudy/ui/./index.html",
		"/_understudy/ui/../ui/index.html",
		"/_understudy/ui/chunks/../index.html",
		"/_understudy/ui/%2e%2e/index.html",
		"/_understudy/ui/index.html/",
	} {
		rec := get(t, h, http.MethodGet, target)
		if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("GET %s = %d %q (%s), want 404 text/plain; charset=utf-8", target, rec.Code, rec.Body, rec.Header().Get("Content-Type"))
		}
		wantSecurityHeaders(t, "GET "+target, rec)
	}
}

// Only GET and HEAD are served (RFC 9110 15.5.6: a 405 lists the allowed
// methods in Allow).
func TestHandlerAllowsOnlyGetAndHead(t *testing.T) {
	h := newHandler(t, built())
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		rec := get(t, h, method, "/_understudy/ui/")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s /_understudy/ui/ = %d, Allow %q; want 405 with Allow: GET, HEAD", method, rec.Code, rec.Header().Get("Allow"))
		}
		wantSecurityHeaders(t, method+" /_understudy/ui/", rec)
	}
}

// A build without index.html is refused when the handler is made, naming
// the command that builds the UI.
func TestNewRefusesABuildWithoutIndex(t *testing.T) {
	for name, fsys := range map[string]fs.FS{
		"nil":           nil,
		"no index.html": fstest.MapFS{"main-k2x8f1q9.js": {Data: []byte("x")}},
	} {
		h, err := ui.New(fsys)
		var missing *ui.MissingIndexError
		if !errors.As(err, &missing) || h != nil || !strings.Contains(err.Error(), "just ui") {
			t.Errorf("New(%s) = %v, %v; want a *MissingIndexError naming 'just ui'", name, h, err)
		}
	}
}

// Mount sends the UI's paths, on the escaped path, to the UI and every
// other request to next, so a percent-encoded spelling never reaches the
// UI.
func TestMountRoutesOnlyTheUIPrefix(t *testing.T) {
	var reached string
	mark := func(name string) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = name })
	}
	h := ui.Mount(mark("ui"), mark("next"))
	for target, want := range map[string]string{
		"/_understudy/ui":              "ui",
		"/_understudy/ui/":             "ui",
		"/_understudy/ui/index.html":   "ui",
		"/_understudy/ui/x/y.js":       "ui",
		"/_understudy/uix":             "next",
		"/_understudy/UI/":             "next",
		"/_understudy/%75i/":           "next",
		"/_understudy/ui%2Findex.html": "next",
		"/_understudy/v1/health":       "next",
		"/_understudy/":                "next",
		"/":                            "next",
		"/bucket/_understudy/ui/":      "next",
	} {
		reached = ""
		get(t, h, http.MethodGet, target)
		if reached != want {
			t.Errorf("Mount routed %s to %q, want %q", target, reached, want)
		}
	}
}

// rand.Text's alphabet and length: 26 characters of A-Z and 2-7
// (https://pkg.go.dev/crypto/rand#Text).
var secretShape = regexp.MustCompile(`^[A-Z2-7]{26}$`)

// Each serve draws its own secret from crypto/rand (ADR 0020).
func TestNewSecretIsRandText(t *testing.T) {
	seen := map[string]bool{}
	for range 64 {
		s := ui.NewSecret()
		if !secretShape.MatchString(s) {
			t.Fatalf("NewSecret() = %q, want 26 characters of A-Z and 2-7", s)
		}
		if seen[s] {
			t.Fatalf("NewSecret() repeated %q", s)
		}
		seen[s] = true
	}
}

// The banner's URL hands the secret over in the fragment only, which a
// browser never sends to a server or in a Referer (ADR 0020; RFC 3986 3.5:
// the fragment is separated before a dereference; RFC 9110 10.1.3: a
// Referer never carries the fragment).
func TestURLPutsTheSecretInTheFragment(t *testing.T) {
	if got := ui.URL("http://127.0.0.1:4566", "ABCDEFGHIJKLMNOPQRSTUVWX27"); got != "http://127.0.0.1:4566/_understudy/ui/#s=ABCDEFGHIJKLMNOPQRSTUVWX27" {
		t.Errorf("URL = %q, want http://127.0.0.1:4566/_understudy/ui/#s=ABCDEFGHIJKLMNOPQRSTUVWX27", got)
	}
}

// The embedded build serves: it holds index.html, references its assets
// by relative URLs only (CSP default-src 'self'), and names every asset by
// its content hash, which the immutable cache header relies on.
func TestEmbeddedBuild(t *testing.T) {
	assets := ui.Assets()
	if assets == nil {
		t.Fatal("Assets() = nil, want the embedded build; run 'just ui'")
	}
	h := newHandler(t, assets)
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, http.MethodGet, "/_understudy/ui/"); rec.Code != http.StatusOK || rec.Body.String() != string(index) {
		t.Errorf("GET /_understudy/ui/ of the embedded build = %d, want its index.html", rec.Code)
	}
	refs := regexp.MustCompile(`(?:src|href)="([^"]*)"`).FindAllStringSubmatch(string(index), -1)
	if len(refs) == 0 {
		t.Errorf("the built index.html references no asset:\n%s", index)
	}
	for _, m := range refs {
		name, ok := strings.CutPrefix(m[1], "./")
		if !ok || strings.Contains(name, "//") || strings.Contains(name, ":") {
			t.Errorf("the built index.html references %q, want ./<file>", m[1])
			continue
		}
		if _, err := fs.Stat(assets, name); err != nil {
			t.Errorf("the built index.html references %q, which the build lacks: %v", m[1], err)
		}
	}
	if strings.Contains(string(index), "<script>") || strings.Contains(string(index), "<style") {
		t.Errorf("the built index.html holds an inline script or style, which its CSP refuses:\n%s", index)
	}
	hashed := regexp.MustCompile(`^[^/]+-[A-Za-z0-9]+\.[A-Za-z0-9]+$`)
	err = fs.WalkDir(assets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path == "index.html" {
			return err
		}
		if !hashed.MatchString(path) {
			t.Errorf("built asset %q is not named <name>-<hash>.<ext>", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The built UI stays under its budget of 300,000 bytes.
func TestBundleBudget(t *testing.T) {
	assets := ui.Assets()
	if assets == nil {
		t.Fatal("Assets() = nil, want the embedded build; run 'just ui'")
	}
	total := 0
	err := fs.WalkDir(assets, ".", func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += int(info.Size())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 || total >= 300_000 {
		t.Errorf("the embedded build is %d bytes, want more than 0 and under 300,000", total)
	}
}

// The UI has no npm dependencies (ADR 0016: no runtime dependency, and the
// allow-list names no JavaScript package).
func TestUIHasNoDependencies(t *testing.T) {
	data, err := os.ReadFile("../../../ui/package.json")
	if err != nil {
		t.Fatal(err)
	}
	var pkg map[string]json.RawMessage
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatalf("ui/package.json: %v", err)
	}
	for _, key := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
		if v, ok := pkg[key]; ok {
			t.Errorf("ui/package.json has %s %s, want none", key, v)
		}
	}
}
