package server_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// ses.verifiedIdentities reaches the service from serve's configuration:
// a listed address starts Success.
func TestSESVerifiedIdentitiesAreWired(t *testing.T) {
	_, endpoint := adminServer(t, server.Config{SESVerifiedIdentities: server.SESVerifiedIdentities{AddressDomains: []string{"example.net"}}})
	if status, body := sesCall(t, endpoint, url.Values{"Action": {"VerifyEmailIdentity"}, "EmailAddress": {"a@example.net"}}); status != http.StatusOK {
		t.Fatalf("VerifyEmailIdentity = %d %s", status, body)
	}
	if got := sesStatus(t, endpoint, "a@example.net"); got != "Success" {
		t.Errorf("a configured address = %q, want Success", got)
	}
}

// ses.productionAccess reaches the service: GetSendQuota answers the
// production MaxSendRate instead of the sandbox's 1 (ses.md 6.3).
func TestSESProductionAccessIsWired(t *testing.T) {
	_, endpoint := adminServer(t, server.Config{SESProductionAccess: true})
	status, body := sesCall(t, endpoint, url.Values{"Action": {"GetSendQuota"}})
	want := "<MaxSendRate>" + strconv.FormatFloat(ses.ProductionMaxSendRate, 'f', -1, 64)
	if status != http.StatusOK || !strings.Contains(body, want) {
		t.Errorf("GetSendQuota = %d %s, want it to hold %s", status, body, want)
	}
}
