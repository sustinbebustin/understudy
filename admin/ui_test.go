package admin_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
)

// recorded is what a fake server saw of the requests it answered.
type recorded struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (r *recorded) seen() []*http.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*http.Request(nil), r.requests...)
}

// fakeServer answers every request with status and body, records it, and
// returns a client for it.
func fakeServer(t *testing.T, status int, body string) (*admin.Client, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.requests = append(rec.requests, r.Clone(r.Context()))
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	c, err := admin.New(srv.URL, admin.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return c, rec
}

// oneGet checks the fake saw exactly one request, GET path, and returns it.
func oneGet(t *testing.T, rec *recorded, path string) *http.Request {
	t.Helper()
	seen := rec.seen()
	if len(seen) != 1 || seen[0].Method != http.MethodGet || seen[0].URL.Path != path {
		got := make([]string, 0, len(seen))
		for _, r := range seen {
			got = append(got, r.Method+" "+r.URL.String())
		}
		t.Fatalf("requests = %v, want one GET %s", got, path)
	}
	return seen[0]
}

const journalEventJSON = `{"id":%d,"cause":{"trace":%d},"at":"2026-01-01T00:00:00Z","wall":"2026-01-01T00:00:00Z",` +
	`"scope":{"partition":"aws","account":"000000000000","region":"us-east-1"},"kind":"timer-fired","detail":{"key":{"kind":"test.tick","id":"a"}}}`

// JournalTail sends the query's filters and tail=n, never limit or cursor,
// and returns the events oldest first as the server sent them (approved
// addition: the journal tail).
func TestJournalTailSendsTheFiltersAndTail(t *testing.T) {
	body := `{"events":[` + fmt.Sprintf(journalEventJSON, 4, 4) + `,` + fmt.Sprintf(journalEventJSON, 5, 5) + `]}`
	c, rec := fakeServer(t, http.StatusOK, body)
	q := admin.JournalQuery{Kind: "timer-fired", Service: "sqs", FromID: 2, Limit: 7, Cursor: 9}
	events, err := c.JournalTail(t.Context(), q, 3)
	if err != nil || len(events) != 2 || events[0].ID != 4 || events[1].ID != 5 {
		t.Fatalf("JournalTail = %+v, %v; want events 4 and 5", events, err)
	}
	r := oneGet(t, rec, admin.Prefix+"/journal/events")
	want := url.Values{admin.ParamKind: {"timer-fired"}, admin.ParamService: {"sqs"}, admin.ParamFromID: {"2"}, admin.ParamTail: {"3"}}
	if got := r.URL.Query(); got.Encode() != want.Encode() {
		t.Errorf("JournalTail sent %s, want %s", got.Encode(), want.Encode())
	}
}

// A tail of no events is refused before any request, naming the field.
func TestJournalTailRefusesANonPositiveCount(t *testing.T) {
	c, rec := fakeServer(t, http.StatusOK, `{"events":[]}`)
	for _, n := range []int{0, -1} {
		_, err := c.JournalTail(t.Context(), admin.JournalQuery{}, n)
		var e *admin.Error
		if !errors.As(err, &e) || e.Code != admin.InvalidRequest || e.Field != "tail" {
			t.Errorf("JournalTail(%d) = %v, want an *Error with code invalid_request and field tail", n, err)
		}
	}
	if seen := rec.seen(); len(seen) != 0 {
		t.Errorf("refused tails sent %d requests, want none", len(seen))
	}
}

// Snapshots reads GET /snapshots: each archive's path, size and
// modification time; an empty list as an empty, non-nil slice; and the
// server's refusal without persistence as unavailable.
func TestSnapshotsReadsTheList(t *testing.T) {
	c, rec := fakeServer(t, http.StatusOK, `{"snapshots":[{"path":"snaps/before.tar","bytes":10240,"modified":"2026-10-26T12:00:00.123Z"}]}`)
	files, err := c.Snapshots(t.Context())
	want := admin.SnapshotFile{Path: "snaps/before.tar", Bytes: 10240, Modified: time.Date(2026, 10, 26, 12, 0, 0, 123_000_000, time.UTC)}
	if err != nil || len(files) != 1 || files[0].Path != want.Path || files[0].Bytes != want.Bytes || !files[0].Modified.Equal(want.Modified) {
		t.Fatalf("Snapshots = %+v, %v; want [%+v]", files, err, want)
	}
	if r := oneGet(t, rec, admin.Prefix+"/snapshots"); r.URL.RawQuery != "" {
		t.Errorf("Snapshots sent the query %q, want none", r.URL.RawQuery)
	}

	empty, _ := fakeServer(t, http.StatusOK, `{"snapshots":[]}`)
	if files, err := empty.Snapshots(t.Context()); err != nil || files == nil || len(files) != 0 {
		t.Errorf("Snapshots of an empty directory = %#v, %v; want an empty non-nil slice", files, err)
	}

	none, _ := fakeServer(t, http.StatusServiceUnavailable,
		`{"error":{"code":"unavailable","message":"listing snapshots needs persistence, and this server has none","hint":"start serve with --persist DIR"}}`)
	_, err = none.Snapshots(t.Context())
	var e *admin.Error
	if !errors.As(err, &e) || e.Code != admin.Unavailable {
		t.Errorf("Snapshots without persistence = %v, want an *Error with code unavailable", err)
	}
}

// UISession sends the secret in the session header, exactly once, and none
// for an empty secret, and reads whether it is valid (ADR 0020).
func TestUISessionSendsTheSecret(t *testing.T) {
	c, rec := fakeServer(t, http.StatusOK, `{"valid":true}`)
	s, err := c.UISession(t.Context(), "ABCDEFGHIJKLMNOPQRSTUVWX27")
	if err != nil || s == nil || !s.Valid {
		t.Fatalf("UISession = %+v, %v; want valid", s, err)
	}
	r := oneGet(t, rec, admin.Prefix+"/ui/session")
	if got := r.Header.Values(admin.UISessionHeader); len(got) != 1 || got[0] != "ABCDEFGHIJKLMNOPQRSTUVWX27" {
		t.Errorf("UISession sent %s %q, want the secret once", admin.UISessionHeader, got)
	}

	c, rec = fakeServer(t, http.StatusOK, `{"valid":false}`)
	if s, err := c.UISession(t.Context(), ""); err != nil || s == nil || s.Valid {
		t.Fatalf("UISession(\"\") = %+v, %v; want not valid", s, err)
	}
	if got := oneGet(t, rec, admin.Prefix+"/ui/session").Header.Values(admin.UISessionHeader); len(got) != 0 {
		t.Errorf("UISession(\"\") sent %s %q, want no header", admin.UISessionHeader, got)
	}
}
