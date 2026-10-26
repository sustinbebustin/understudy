package admin_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
)

// A tail with no filters sends tail alone.
func TestJournalTailWithoutFiltersSendsOnlyTail(t *testing.T) {
	c, rec := fakeServer(t, http.StatusOK, `{"events":[]}`)
	events, err := c.JournalTail(t.Context(), admin.JournalQuery{}, 5)
	if err != nil || len(events) != 0 {
		t.Fatalf("JournalTail = %+v, %v; want no events", events, err)
	}
	if got, want := oneGet(t, rec, admin.Prefix+"/journal/events").URL.Query(), (url.Values{admin.ParamTail: {"5"}}); got.Encode() != want.Encode() {
		t.Errorf("JournalTail sent %s, want %s", got.Encode(), want.Encode())
	}
}

// UISession sends the admin token beside the session secret, since a
// token-guarded listener requires it on every route (ADR 0020).
func TestUISessionSendsTheTokenToo(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"valid":true}`)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := admin.New(srv.URL, admin.Options{Token: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if s, err := c.UISession(t.Context(), "ABCDEFGHIJKLMNOPQRSTUVWX27"); err != nil || !s.Valid {
		t.Fatalf("UISession = %+v, %v; want valid", s, err)
	}
	if got.Get("Authorization") != "Bearer s3cret" || got.Get(admin.UISessionHeader) != "ABCDEFGHIJKLMNOPQRSTUVWX27" {
		t.Errorf("UISession sent Authorization %q and %s %q; want the token and the secret", got.Get("Authorization"), admin.UISessionHeader, got.Get(admin.UISessionHeader))
	}
}
