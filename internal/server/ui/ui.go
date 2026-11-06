// Package ui serves the web UI: a static single-page app over the admin API
// at /_understudy/ui/ on the main listener, built from ui/ with bun and
// embedded from dist (ADR 0020, ARCHITECTURE.md 17).
//
// The page holds no secret, so its files are served without the admin
// token and without the admin handler's browser refusal. The per-run
// session secret reaches the page only in the fragment of the URL the
// banner prints; the page sends it back on each admin call in
// admin.UISessionHeader, which the admin handler checks.
package ui

import (
	"crypto/rand"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// Path is the prefix the UI is served under on the main listener.
const Path = "/_understudy/ui/"

// CSP is the Content-Security-Policy of every UI response.
const CSP = "default-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// bare is Path without its final slash, which redirects to Path.
const bare = "/_understudy/ui"

// index is the page: the build's one file without a content hash in its
// name.
const index = "index.html"

// The page names the current build's assets, so a browser revalidates it
// on every load (RFC 9111 5.2.2.4); an asset's name carries a hash of its
// content, so it is cached for good (RFC 8246).
const (
	cachePage  = "no-cache"
	cacheAsset = "public, max-age=31536000, immutable"
)

// htmlType is the page's Content-Type.
const htmlType = "text/html; charset=utf-8"

// contentType is the Content-Type of a file with extension ext. The table
// is fixed, since mime.TypeByExtension reads the host's MIME files.
func contentType(ext string) string {
	switch ext {
	case ".html":
		return htmlType
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}

// secure sets the headers every answer carries, whatever its status (ADR
// 0020).
func secure(h http.Header) {
	h.Set("Content-Security-Policy", CSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Referrer-Policy", "no-referrer")
}

//go:embed dist
var dist embed.FS

// MissingIndexError is New's error for assets without index.html at their
// top.
type MissingIndexError struct{}

func (*MissingIndexError) Error() string {
	return "ui: the web UI's assets hold no index.html; rebuild them with 'just ui'"
}

// Handler serves the UI's static files under Path.
type Handler struct {
	fsys fs.FS
	// page is index.html, read once by New.
	page []byte
}

// Assets returns the embedded build, internal/server/ui/dist, rooted so that
// index.html is at its top.
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// fs.Sub refuses only a directory name that is not a valid path,
		// and "dist" is one.
		return nil
	}
	return sub
}

// New returns the handler serving fsys under Path. An fsys without
// index.html at its top, or nil, is a *MissingIndexError.
func New(fsys fs.FS) (*Handler, error) {
	if fsys == nil {
		return nil, &MissingIndexError{}
	}
	page, err := fs.ReadFile(fsys, index)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &MissingIndexError{}
	}
	if err != nil {
		return nil, fmt.Errorf("ui: reading the web UI's %s: %w", index, err)
	}
	return &Handler{fsys: fsys, page: page}, nil
}

// ServeHTTP serves one UI file, routing on the escaped path as Mount does:
// the bare prefix redirects to Path, Path and Path+"index.html" are the
// page, and any other regular file of the build is an asset. The query is
// ignored.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	secure(w.Header())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		plain(w, r, http.StatusMethodNotAllowed, "understudy: the web UI answers only GET and HEAD")
		return
	}
	p := r.URL.EscapedPath()
	if p == bare {
		// A Location without a fragment keeps the request's, so the secret
		// survives the redirect (RFC 9110 10.2.2).
		w.Header().Set("Location", Path)
		w.WriteHeader(http.StatusMovedPermanently)
		return
	}
	name, ok := strings.CutPrefix(p, Path)
	if !ok {
		notFound(w, r)
		return
	}
	if name == "" || name == index {
		send(w, r, h.page, htmlType, cachePage)
		return
	}
	body, ok := h.asset(name)
	if !ok {
		notFound(w, r)
		return
	}
	send(w, r, body, contentType(path.Ext(name)), cacheAsset)
}

// asset reads name, a regular file of the build. It reports false for
// anything else: a name fs.ValidPath refuses (an empty, "." or ".."
// segment, or a final slash), a directory, a missing file, or one fsys
// cannot read.
func (h *Handler) asset(name string) ([]byte, bool) {
	if !fs.ValidPath(name) {
		return nil, false
	}
	info, err := fs.Stat(h.fsys, name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	body, err := fs.ReadFile(h.fsys, name)
	return body, err == nil
}

// send answers 200 with body, its length and the given Content-Type and
// Cache-Control; HEAD gets the headers alone.
func send(w http.ResponseWriter, r *http.Request, body []byte, contentType, cache string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cache)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	// A failed write means the client went away; nothing else can be sent.
	// The body is a file of the build, never text from the request.
	_, _ = w.Write(body) //nolint:errcheck,gosec // the client is gone when this fails
}

// notFound answers 404 for a path that names no file of the build.
func notFound(w http.ResponseWriter, r *http.Request) {
	plain(w, r, http.StatusNotFound, "understudy: no such file in the web UI; open the UI from the URL serve printed, which starts "+Path)
}

// plain answers status with msg as plain text; HEAD gets the headers alone.
func plain(w http.ResponseWriter, r *http.Request, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	// A failed write means the client went away; nothing else can be sent.
	_, _ = w.Write([]byte(msg + "\n")) //nolint:errcheck // the client is gone when this fails
}

// Mount returns a handler that sends the UI's paths, /_understudy/ui and
// everything under Path, to ui and every other request to next. It routes
// on the escaped path, as the admin handler does, so a percent-encoded
// spelling such as /_understudy/%75i/ goes to next.
func Mount(ui, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.EscapedPath(); p == bare || strings.HasPrefix(p, Path) {
			ui.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// NewSecret draws a session secret with crypto/rand: rand.Text, 26
// characters of A-Z and 2-7 (ADR 0020: each serve draws its own).
func NewSecret() string { return rand.Text() }

// URL is the UI URL the banner prints for the main endpoint, such as
// http://127.0.0.1:4566: endpoint + Path + "#s=" + secret. The secret is in
// the fragment, which a browser never sends to a server or in a Referer.
func URL(endpoint, secret string) string { return endpoint + Path + "#s=" + secret }
