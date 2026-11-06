package admin_test

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
)

const sesMessageJSON = `{"messageId":"0100000000000000-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee-000000","api":"ses","operation":"SendEmail",` +
	`"requestId":"r1","account":"000000000000","region":"us-east-1","accepted":"2026-01-01T00:00:00Z","kind":"send",` +
	`"source":"sender@example.com","from":["sender@example.com"],"to":["success@simulator.amazonses.com"],` +
	`"envelope":["success@simulator.amazonses.com"],"subject":{"data":"Hi","charset":"UTF-8"},"raw":"cmF3",` +
	`"tags":[{"name":"campaign","value":"spring"}],"simulator":{"success@simulator.amazonses.com":"success"}}`

// SESMessages sends GET /ses/messages with every filter, Limit and Cursor
// as the contract 4.1 parameters, and decodes the page as the server sent
// it (Gate B approved surface).
func TestSESMessagesSendsTheFiltersAndDecodesThePage(t *testing.T) {
	c, rec := fakeServer(t, http.StatusOK, `{"messages":[`+sesMessageJSON+`],"next":"c2","evicted":3}`)
	since := time.Date(2026, 1, 1, 0, 0, 0, 5, time.UTC)
	q := admin.SESMessageQuery{
		Account: "000000000000", Region: "us-east-1", MessageID: "m1", From: "sender@example.com",
		To: "success@simulator.amazonses.com", Since: since, Limit: 7, Cursor: "c1",
	}
	page, err := c.SESMessages(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if page.Next != "c2" || page.Evicted != 3 || len(page.Messages) != 1 {
		t.Fatalf("SESMessages = %+v", page)
	}
	m := page.Messages[0]
	if m.API != "ses" || m.Kind != "send" || m.Subject == nil || m.Subject.Data != "Hi" || string(m.Raw) != "raw" ||
		len(m.Tags) != 1 || m.Tags[0].Name != "campaign" || m.Simulator["success@simulator.amazonses.com"] != "success" {
		t.Errorf("decoded message = %+v", m)
	}
	r := oneGet(t, rec, admin.Prefix+"/ses/messages")
	want := url.Values{
		admin.ParamSESAccount: {"000000000000"}, admin.ParamSESRegion: {"us-east-1"}, admin.ParamSESMessageID: {"m1"},
		admin.ParamSESFrom: {"sender@example.com"}, admin.ParamSESTo: {"success@simulator.amazonses.com"},
		admin.ParamSESSince: {since.Format(time.RFC3339Nano)}, admin.ParamLimit: {"7"}, admin.ParamCursor: {"c1"},
	}
	if got := r.URL.Query(); got.Encode() != want.Encode() {
		t.Errorf("SESMessages sent %s, want %s", got.Encode(), want.Encode())
	}
}

// ClearSESMessages sends DELETE /ses/messages with the filters but never
// limit or cursor, and returns the deleted count (contract 4.1).
func TestClearSESMessagesSendsDeleteWithoutPaging(t *testing.T) {
	c, rec := fakeServer(t, http.StatusOK, `{"deleted":2}`)
	res, err := c.ClearSESMessages(t.Context(), admin.SESMessageQuery{To: "a@example.com", Limit: 5, Cursor: "c"})
	if err != nil || res == nil || res.Deleted != 2 {
		t.Fatalf("ClearSESMessages = %+v, %v; want 2 deleted", res, err)
	}
	seen := rec.seen()
	if len(seen) != 1 || seen[0].Method != http.MethodDelete || seen[0].URL.Path != admin.Prefix+"/ses/messages" {
		t.Fatalf("requests = %v, want one DELETE %s/ses/messages", seen, admin.Prefix)
	}
	want := url.Values{admin.ParamSESTo: {"a@example.com"}}
	if got := seen[0].URL.Query(); got.Encode() != want.Encode() {
		t.Errorf("ClearSESMessages sent %s, want %s", got.Encode(), want.Encode())
	}
}

// An ADR 0017 error envelope comes back as *admin.Error naming the field
// (contract 4.1: a bad limit is invalid_request on field limit).
func TestSESMessagesReturnsTheServerError(t *testing.T) {
	c, _ := fakeServer(t, http.StatusBadRequest, `{"error":{"code":"invalid_request","message":"limit must be 1 to 1000","field":"limit"}}`)
	_, err := c.SESMessages(t.Context(), admin.SESMessageQuery{Limit: 1001})
	var e *admin.Error
	if !errors.As(err, &e) || e.Code != admin.InvalidRequest || e.Field != "limit" {
		t.Errorf("SESMessages = %v, want an *Error with code invalid_request and field limit", err)
	}
}
