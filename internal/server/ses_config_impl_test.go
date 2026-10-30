package server_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
)

// ses.verifiedIdentities reaches the service from serve's configuration:
// a listed address starts Success (contract 6).
func TestSESVerifiedIdentitiesAreWired(t *testing.T) {
	_, endpoint := adminServer(t, server.Config{SESVerifiedIdentities: server.SESVerifiedIdentities{AddressDomains: []string{"example.net"}}})
	if status, body := sesCall(t, endpoint, url.Values{"Action": {"VerifyEmailIdentity"}, "EmailAddress": {"a@example.net"}}); status != http.StatusOK {
		t.Fatalf("VerifyEmailIdentity = %d %s", status, body)
	}
	if got := sesStatus(t, endpoint, "a@example.net"); got != "Success" {
		t.Errorf("a configured address = %q, want Success", got)
	}
}
