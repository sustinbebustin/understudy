package admin

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/sustinbebustin/understudy/admin"
)

// uiPath is where the main listener serves the web UI: ui.Path, which this
// package does not import.
const uiPath = "/_understudy/ui/"

// uiSessionState answers GET /ui/session: whether the call carried this
// run's UI session secret (ADR 0020). Fetch Metadata does not enter into
// it; a page's call with a wrong secret never gets here, refused by
// refuseBrowser.
func (h *Handler) uiSessionState(w http.ResponseWriter, r *http.Request, _ []string) {
	h.ok(w, r, admin.UISession{Valid: h.uiSession(r)})
}

// uiSession reports whether r carries exactly one admin.UISessionHeader
// value and it is this run's UI secret, compared in constant time; false
// when the run has none.
func (h *Handler) uiSession(r *http.Request) bool {
	got := r.Header.Values(admin.UISessionHeader)
	return h.o.UISecret != "" && len(got) == 1 && subtle.ConstantTimeCompare([]byte(got[0]), []byte(h.o.UISecret)) == 1
}

// ownOrigin reports whether origin, the request's Origin values, is absent
// or the one origin r was sent to: its scheme, https over TLS and http
// otherwise, and its Host, compared without regard to ASCII case.
func ownOrigin(r *http.Request, origin []string) bool {
	switch len(origin) {
	case 0:
		return true
	case 1:
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		return strings.EqualFold(origin[0], scheme+"://"+r.Host)
	default:
		return false
	}
}
