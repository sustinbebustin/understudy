package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
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

// A client that addresses understudy by an AWS name, here under an S3 base
// domain the loopback listener answers, gets an HTTP delivery's links on
// localhost at the port it used, with no TLS listener serving: no AWS host
// in any link, header or body sent, and the links still work
// (sns-http-delivery.md 8.3.3, 8.8).
func TestSNSHTTPLinksNeverNameTheClientsAWSHost(t *testing.T) {
	rcv := newReceiver(t, false)
	c, endpoint := adminServer(t, server.Config{
		S3BaseDomains:   []string{"localhost", "amazonaws.com"},
		SNSHTTPDelivery: server.SNSHTTPDelivery{Allow: allowing(t, rcv.hostPort(t))},
	})
	port := endpoint[strings.LastIndex(endpoint, ":")+1:]
	awsHost := "sns.us-east-1.amazonaws.com:" + port
	call := func(form url.Values) string {
		t.Helper()
		form.Set("Version", "2010-03-31")
		encoded := form.Encode()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/", strings.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = awsHost
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		region, err := aws.KnownRegions().Parse("us-east-1")
		if err != nil {
			t.Fatal(err)
		}
		auth.Sign(req, []byte(encoded), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: region, Service: "sns"}, time.Now())
		status, body := send(t, req)
		if status != http.StatusOK {
			t.Fatalf("%s through Host %s = %d %s", form.Get("Action"), awsHost, status, body)
		}
		return body
	}
	linkBase := "http://localhost:" + port + "/_understudy/sns/"
	noAWSHost := func(p receivedPost) {
		t.Helper()
		for name, values := range p.header {
			for _, v := range values {
				if strings.Contains(strings.ToLower(v), "amazonaws.com") {
					t.Errorf("header %s: %q names an AWS host", name, v)
				}
			}
		}
		if strings.Contains(strings.ToLower(string(p.body)), "amazonaws.com") {
			t.Errorf("body names an AWS host: %s", p.body)
		}
	}

	topic := element(t, call(url.Values{"Action": {"CreateTopic"}, "Name": {"hooks"}}), "TopicArn")
	call(url.Values{"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {"http"}, "Endpoint": {rcv.srv.URL + "/hook"}})
	settleServer(t, c)
	cs := rcv.ofType("SubscriptionConfirmation")
	if len(cs) != 1 {
		t.Fatalf("SubscriptionConfirmations = %d, want 1", len(cs))
	}
	noAWSHost(cs[0])
	confirmation := decodeMessage(t, cs[0])
	for _, link := range []string{confirmation.SubscribeURL, confirmation.SigningCertURL} {
		if !strings.HasPrefix(link, linkBase) {
			// Fatal, so the GET below never follows a link to an AWS host.
			t.Fatalf("link %q, want it under %s", link, linkBase)
		}
	}
	if status, body := get(t, confirmation.SubscribeURL); status != http.StatusOK {
		t.Fatalf("SubscribeURL = %d %s, want 200", status, body)
	}

	call(url.Values{"Action": {"Publish"}, "TopicArn": {topic}, "Message": {"hello"}})
	settleServer(t, c)
	ns := rcv.ofType("Notification")
	if len(ns) != 1 {
		t.Fatalf("Notifications = %d, want 1", len(ns))
	}
	noAWSHost(ns[0])
	n := decodeMessage(t, ns[0])
	for _, link := range []string{n.UnsubscribeURL, n.SigningCertURL} {
		if !strings.HasPrefix(link, linkBase) {
			t.Errorf("link %q, want it under %s", link, linkBase)
		}
	}
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
