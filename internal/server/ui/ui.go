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
	"errors"
	"io/fs"
	"net/http"
)

// Path is the prefix the UI is served under on the main listener.
const Path = "/_understudy/ui/"

// CSP is the Content-Security-Policy of every UI response.
const CSP = "default-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// errNotImplemented is what the parts of this package not implemented yet
// answer.
var errNotImplemented = errors.New("ui: the web UI is not implemented yet")

// MissingIndexError is New's error for assets without index.html at their
// top.
type MissingIndexError struct{}

func (*MissingIndexError) Error() string {
	return "ui: the web UI's assets hold no index.html; rebuild them with 'just ui'"
}

// Handler serves the UI's static files under Path.
type Handler struct{}

// Assets returns the embedded build, internal/server/ui/dist, rooted so that
// index.html is at its top. Not implemented yet: it returns nil.
func Assets() fs.FS { return nil }

// New returns the handler serving fsys under Path. Not implemented yet: it
// returns an error.
func New(fs.FS) (*Handler, error) { return nil, errNotImplemented }

// ServeHTTP serves one UI file. Not implemented yet: it answers 501.
func (*Handler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, errNotImplemented.Error(), http.StatusNotImplemented)
}

// Mount returns a handler that sends the UI's paths, /_understudy/ui and
// everything under Path, to ui and every other request to next. Not
// implemented yet: every request goes to next.
func Mount(_, next http.Handler) http.Handler { return next }

// NewSecret draws a session secret with crypto/rand. Not implemented yet:
// it returns "".
func NewSecret() string { return "" }

// URL is the UI URL the banner prints for the main endpoint, such as
// http://127.0.0.1:4566: endpoint + Path + "#s=" + secret. Not implemented
// yet: it returns "".
func URL(_, _ string) string { return "" }
