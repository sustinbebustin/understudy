package server_test

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
)

// refusalReasons returns each journaled sns-to-http refusal's reason.
func refusalReasons(t *testing.T, c *admin.Client) []string {
	t.Helper()
	page, err := c.JournalEvents(t.Context(), admin.JournalQuery{Kind: "delivery"})
	if err != nil {
		t.Fatal(err)
	}
	var reasons []string
	for i := range page.Events {
		var d struct {
			Mechanism string `json:"mechanism"`
			Outcome   struct {
				Kind   string `json:"kind"`
				Reason string `json:"reason"`
			} `json:"outcome"`
		}
		if err := json.Unmarshal(page.Events[i].Detail, &d); err != nil {
			t.Fatalf("delivery event %s: %v", page.Events[i].Detail, err)
		}
		if d.Mechanism == "sns-to-http" && d.Outcome.Kind == "refused" {
			reasons = append(reasons, d.Outcome.Reason)
		}
	}
	return reasons
}

// The default list refuses a receiver as not allowed and names the setting
// that lifts it; understudy's own listeners are refused as its own, which
// no setting lifts (sns-http-delivery.md 7.2, 7.3).
func TestSNSHTTPDeliveryRefusalsNameTheirCause(t *testing.T) {
	rcv := newReceiver(t, false)
	t.Run("the default list", func(t *testing.T) {
		c, endpoint, _ := snsHTTPServer(t, server.SNSHTTPDelivery{})
		topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"hooks"}}), "TopicArn")
		mustSNS(t, endpoint, url.Values{"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {"http"}, "Endpoint": {rcv.srv.URL + "/hook"}})
		settleServer(t, c)
		reasons := refusalReasons(t, c)
		if len(reasons) != 1 || !strings.Contains(reasons[0], "(not-allowed)") || !strings.Contains(reasons[0], `to sns.httpDelivery.allow`) {
			t.Errorf("refusals %q, want one not-allowed naming sns.httpDelivery.allow", reasons)
		}
	})
	t.Run("understudy's own listeners", func(t *testing.T) {
		c, endpoint, tlsEndpoint := snsHTTPServer(t, server.SNSHTTPDelivery{Allow: allowing(t, "127.0.0.1:*")})
		topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"hooks"}}), "TopicArn")
		for protocol, own := range map[string]string{"http": endpoint + "/hook", "https": tlsEndpoint + "/hook"} {
			mustSNS(t, endpoint, url.Values{"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {protocol}, "Endpoint": {own}})
		}
		settleServer(t, c)
		reasons := refusalReasons(t, c)
		if len(reasons) != 2 {
			t.Fatalf("refusals %q, want two", reasons)
		}
		for _, r := range reasons {
			if !strings.Contains(r, "(own-listener)") {
				t.Errorf("refusal %q, want own-listener", r)
			}
		}
	})
}
