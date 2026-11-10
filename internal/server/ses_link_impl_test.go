package server_test

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// sesStatus is the v1 VerificationStatus GetIdentityVerificationAttributes
// answers for identity, as the awsQuery body spells it.
func sesStatus(t *testing.T, endpoint, identity string) string {
	t.Helper()
	status, body := sesCall(t, endpoint, url.Values{"Action": {"GetIdentityVerificationAttributes"}, "Identities.member.1": {identity}})
	if status != http.StatusOK {
		t.Fatalf("GetIdentityVerificationAttributes(%s) = %d %s", identity, status, body)
	}
	_, rest, ok := strings.Cut(body, "<VerificationStatus>")
	value, _, closed := strings.Cut(rest, "</VerificationStatus>")
	if !ok || !closed {
		return ""
	}
	return value
}

// The verification email's link, opened without credentials on the main
// listener, verifies the address (ses.md 11.3 item 1). Its answer is
// understudy's text/plain under exactly one Content-Security-Policy,
// sandbox, and nosniff, since it echoes the identity (ADR 0020); any
// other method is 405 and a path naming no link operation is
// declined.
func TestSESVerificationLinkThroughTheEdge(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	const addr = "owner@example.com"
	if status, body := sesCall(t, endpoint, url.Values{"Action": {"VerifyEmailIdentity"}, "EmailAddress": {addr}}); status != http.StatusOK {
		t.Fatalf("VerifyEmailIdentity = %d %s", status, body)
	}
	page, err := c.SESMessages(t.Context(), admin.SESMessageQuery{To: addr})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text == nil {
		t.Fatalf("SESMessages = %+v, %v", page, err)
	}
	prefix := endpoint + ses.VerificationLinkPath + "?token="
	at := strings.Index(page.Messages[0].Text.Data, prefix)
	if at < 0 {
		t.Fatalf("the verification email %q carries no link on %s", page.Messages[0].Text.Data, endpoint)
	}
	link, _, _ := strings.Cut(page.Messages[0].Text.Data[at:], "\n")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, link, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/plain; charset=utf-8" ||
		!slices.Equal(res.Header.Values("Content-Security-Policy"), []string{"sandbox"}) || res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("GET link = %d %v", res.StatusCode, res.Header)
	}
	if got := sesStatus(t, endpoint, addr); got != "Success" {
		t.Errorf("%s = %q after its link, want Success", addr, got)
	}

	post, err := http.NewRequestWithContext(t.Context(), http.MethodPost, link, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := send(t, post); status != http.StatusMethodNotAllowed {
		t.Errorf("POST link = %d, want 405", status)
	}
	if status, body := get(t, endpoint+"/_understudy/ses/Nope"); status == http.StatusOK || !strings.Contains(body, "VerificationLink") {
		t.Errorf("GET /_understudy/ses/Nope = %d %s, want a decline naming VerificationLink", status, body)
	}
}
