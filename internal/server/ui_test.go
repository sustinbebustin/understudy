package server_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/server/ui"
)

// e2eUISecret has the shape of a drawn secret: rand.Text's 26 characters of
// A-Z and 2-7 (https://pkg.go.dev/crypto/rand#Text).
const e2eUISecret = "ABCDEFGHIJKLMNOPQRSTUVWX27"

// answer is what browse read back.
type answer struct {
	status int
	header http.Header
	body   string
}

// browse sends method url with header and body (a JSON body when not
// empty) without following redirects, and returns the answer.
func browse(t *testing.T, method, url, body string, header http.Header) answer {
	t.Helper()
	var r io.Reader = http.NoBody
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range header {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	if err := errors.Join(err, resp.Body.Close()); err != nil {
		t.Fatal(err)
	}
	return answer{status: resp.StatusCode, header: resp.Header, body: string(data)}
}

// The main listener serves the UI with its security headers, and the
// page's own admin calls, same-origin with the run's secret, pass the edge
// and the admin API end to end; the same calls without the secret are
// refused as every page's call is (ADR 0020).
func TestUIOnTheMainListener(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{UISecret: e2eUISecret})

	got := browse(t, http.MethodGet, endpoint+"/_understudy/ui/", "", nil)
	if got.status != http.StatusOK || !strings.HasPrefix(got.header.Get("Content-Type"), "text/html") ||
		got.header.Get("Content-Security-Policy") != ui.CSP || got.header.Get("X-Content-Type-Options") != "nosniff" ||
		got.header.Get("Cross-Origin-Opener-Policy") != "same-origin" || got.header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("GET /_understudy/ui/ = %d %v\n%s\nwant the page with the UI's headers", got.status, got.header, got.body)
	}
	if strings.Contains(got.body, e2eUISecret) {
		t.Errorf("the UI page holds the session secret; it travels only in the URL fragment")
	}
	if got := browse(t, http.MethodGet, endpoint+"/_understudy/ui", "", nil); got.status != http.StatusMovedPermanently || got.header.Get("Location") != "/_understudy/ui/" {
		t.Errorf("GET /_understudy/ui = %d, Location %q; want 301 to /_understudy/ui/", got.status, got.header.Get("Location"))
	}

	page := http.Header{"Sec-Fetch-Site": {"same-origin"}, "Origin": {endpoint}}
	withSecret := page.Clone()
	withSecret.Set(admin.UISessionHeader, e2eUISecret)
	got = browse(t, http.MethodGet, endpoint+admin.Prefix+"/ui/session", "", withSecret)
	var session admin.UISession
	if got.status != http.StatusOK || json.Unmarshal([]byte(got.body), &session) != nil || !session.Valid {
		t.Errorf("GET ui/session from the page = %d %s, want valid", got.status, got.body)
	}
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPost, "/reset", ""},
		{http.MethodPost, "/clock", `{"op": "advance", "by": "1m"}`},
		{http.MethodGet, "/journal/events", ""},
	} {
		if got := browse(t, call.method, endpoint+admin.Prefix+call.path, call.body, withSecret); got.status != http.StatusOK {
			t.Errorf("%s %s from the page with the secret = %d %s, want 200", call.method, call.path, got.status, got.body)
		}
		if got := browse(t, call.method, endpoint+admin.Prefix+call.path, call.body, page); got.status != http.StatusForbidden {
			t.Errorf("%s %s from the page without the secret = %d %s, want 403", call.method, call.path, got.status, got.body)
		}
	}

	if s, err := c.UISession(t.Context(), e2eUISecret); err != nil || !s.Valid {
		t.Errorf("Client.UISession with the secret = %+v, %v; want valid", s, err)
	}
	if s, err := c.UISession(t.Context(), ""); err != nil || s.Valid {
		t.Errorf("Client.UISession without a secret = %+v, %v; want not valid", s, err)
	}
}

// With persistence the client lists the archives saved under the
// snapshots directory; without it the list is unavailable.
func TestAdminSnapshotList(t *testing.T) {
	dir := t.TempDir()
	c, endpoint := adminServer(t, server.Config{Persist: server.PersistDir{Path: dir}})
	if files, err := c.Snapshots(t.Context()); err != nil || files == nil || len(files) != 0 {
		t.Fatalf("Snapshots before any save = %#v, %v; want an empty list", files, err)
	}
	queue(t, endpoint, "p", "m")
	for _, path := range []string{"snaps/one.tar", "a.tar"} {
		if _, err := c.SaveSnapshotTo(t.Context(), path, admin.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	files, err := c.Snapshots(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
		info, err := os.Stat(filepath.Join(dir, "snapshots", filepath.FromSlash(f.Path)))
		if err != nil || f.Bytes != info.Size() || !f.Modified.Equal(info.ModTime()) {
			t.Errorf("listed %+v, file %v (%v); want its size and modification time", f, info, err)
		}
	}
	if !slices.Equal(paths, []string{"a.tar", "snaps/one.tar"}) {
		t.Errorf("Snapshots = %v, want [a.tar snaps/one.tar]", paths)
	}

	plain, _ := adminServer(t, server.Config{})
	_, err = plain.Snapshots(t.Context())
	wantCode(t, err, admin.Unavailable)
}

// The client's tail is the newest events of the query, oldest first.
func TestAdminJournalTail(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	queue(t, endpoint, "tailed", "m")
	page, err := c.JournalEvents(t.Context(), admin.JournalQuery{Limit: 1000})
	if err != nil || len(page.Events) < 2 {
		t.Fatalf("journal = %+v, %v; want the queue's calls", page, err)
	}
	ids := func(es []admin.JournalEvent) []uint64 {
		out := make([]uint64, 0, len(es))
		for i := range es {
			out = append(out, es[i].ID)
		}
		return out
	}
	all := ids(page.Events)
	tail, err := c.JournalTail(t.Context(), admin.JournalQuery{Limit: 1}, 2)
	if got := ids(tail); err != nil || !slices.Equal(got, all[len(all)-2:]) {
		t.Errorf("JournalTail(2) = %v, %v; want %v", got, err, all[len(all)-2:])
	}
}
