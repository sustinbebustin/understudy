package server_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/egress"
	"github.com/sustinbebustin/understudy/internal/server"
)

// receiver is an HTTP endpoint a test subscribes; it records each POST and
// answers 200.
type receiver struct {
	srv *httptest.Server

	mu    sync.Mutex
	posts []receivedPost
}

type receivedPost struct {
	header http.Header
	body   []byte
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.posts = append(r.posts, receivedPost{header: req.Header.Clone(), body: body})
	r.mu.Unlock()
}

func (r *receiver) ofType(typ string) []receivedPost {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []receivedPost
	for _, p := range r.posts {
		if p.header.Get("X-Amz-Sns-Message-Type") == typ {
			out = append(out, p)
		}
	}
	return out
}

func newReceiver(t *testing.T, tlsServer bool) *receiver {
	t.Helper()
	r := &receiver{}
	if tlsServer {
		r.srv = httptest.NewTLSServer(r)
	} else {
		r.srv = httptest.NewServer(r)
	}
	t.Cleanup(r.srv.Close)
	return r
}

// hostPort is the receiver's 127.0.0.1:<port>.
func (r *receiver) hostPort(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(r.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func allowing(t *testing.T, entries ...string) egress.AllowList {
	t.Helper()
	a, err := egress.ParseAllowList(egress.Setting{Service: "sns", Feature: "httpDelivery"}, entries)
	if err != nil {
		t.Fatalf("ParseAllowList(%q): %v", entries, err)
	}
	return a
}

// snsHTTPServer runs a seeded, frozen server with a TLS listener and cfg's
// SNS HTTP delivery settings, and returns an admin client, the main
// endpoint and the TLS endpoint.
func snsHTTPServer(t *testing.T, delivery server.SNSHTTPDelivery) (c *admin.Client, endpoint, tlsEndpoint string) {
	t.Helper()
	seed := uint64(11)
	r := launch(t, server.Config{
		Seed: &seed, ClockStart: server.ClockFrozen{At: frozenAt}, Version: "test",
		TLS:             server.TLSSelfSigned{Listen: netip.MustParseAddrPort("127.0.0.1:0")},
		SNSHTTPDelivery: delivery,
	})
	r.stopOnCleanup(t)
	data, err := os.ReadFile(r.ready)
	if err != nil {
		t.Fatal(err)
	}
	var doc readyDoc
	if err := json.Unmarshal(data, &doc); err != nil || doc.TLSEndpoint == "" {
		t.Fatalf("ready file = %s, %v; want a tlsEndpoint", data, err)
	}
	c, err = admin.New(r.endpoint, admin.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return c, r.endpoint, doc.TLSEndpoint
}

// trustingClient returns a client that trusts the TLS listener's
// self-signed certificate, as a receiver given understudy env --tls's
// bundle does.
func trustingClient(t *testing.T, tlsEndpoint string) *http.Client {
	t.Helper()
	dialer := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // reads the certificate to trust it below
	conn, err := dialer.DialContext(t.Context(), "tcp", strings.TrimPrefix(tlsEndpoint, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	tc, ok := conn.(*tls.Conn)
	if !ok {
		t.Fatalf("tls.Dialer returned %T", conn)
	}
	roots := x509.NewCertPool()
	roots.AddCert(tc.ConnectionState().PeerCertificates[0])
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func getWith(t *testing.T, client *http.Client, link string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, link, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", link, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

func settleServer(t *testing.T, c *admin.Client) {
	t.Helper()
	if _, err := c.Settle(t.Context(), waitTimeout); err != nil {
		t.Fatalf("Settle: %v", err)
	}
}

type snsMessage struct {
	Type, MessageID, Token, TopicArn, Message, SubscribeURL, UnsubscribeURL, SigningCertURL string
}

func decodeMessage(t *testing.T, p receivedPost) snsMessage {
	t.Helper()
	var m snsMessage
	if err := json.Unmarshal(p.body, &m); err != nil {
		t.Fatalf("POST body %s: %v", p.body, err)
	}
	return m
}

// With sns.httpDelivery.allow listing a receiver, the server sends it the
// signed SNS messages through its egress client, with links on the TLS
// listener at https://localhost:<port> (sns-http-delivery.md 8.3 item 3),
// and the TLS listener serves the three link routes: the SigningCertURL,
// the SubscribeURL and the UnsubscribeURL work over it with no
// credentials.
func TestSNSHTTPDeliveryThroughTheServer(t *testing.T) {
	rcv := newReceiver(t, false)
	c, endpoint, tlsEndpoint := snsHTTPServer(t, server.SNSHTTPDelivery{Allow: allowing(t, rcv.hostPort(t))})
	tlsPort := tlsEndpoint[strings.LastIndex(tlsEndpoint, ":")+1:]
	linkBase := "https://localhost:" + tlsPort + "/_understudy/sns/"
	client := trustingClient(t, tlsEndpoint)

	topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"hooks"}}), "TopicArn")
	sub := element(t, mustSNS(t, endpoint, url.Values{
		"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {"http"}, "Endpoint": {rcv.srv.URL + "/hook"}, "ReturnSubscriptionArn": {"true"},
	}), "SubscriptionArn")
	settleServer(t, c)
	cs := rcv.ofType("SubscriptionConfirmation")
	if len(cs) != 1 {
		t.Fatalf("the receiver got %d SubscriptionConfirmations on a frozen clock, want 1", len(cs))
	}
	confirmation := decodeMessage(t, cs[0])
	for _, link := range []string{confirmation.SubscribeURL, confirmation.SigningCertURL} {
		if !strings.HasPrefix(link, linkBase) {
			t.Errorf("link %q, want it under %s", link, linkBase)
		}
	}
	if status, body := getWith(t, client, confirmation.SigningCertURL); status != http.StatusOK || !strings.HasPrefix(body, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("SigningCertURL over TLS = %d %.40q, want the PEM certificate", status, body)
	}
	if status, body := getWith(t, client, confirmation.SubscribeURL); status != http.StatusOK || element(t, body, "SubscriptionArn") != sub {
		t.Fatalf("SubscribeURL over TLS = %d %s, want 200 naming %s", status, body, sub)
	}

	mustSNS(t, endpoint, url.Values{"Action": {"Publish"}, "TopicArn": {topic}, "Message": {"hello"}})
	settleServer(t, c)
	ns := rcv.ofType("Notification")
	if len(ns) != 1 || decodeMessage(t, ns[0]).Message != "hello" {
		t.Fatalf("Notifications = %d, want the one published", len(ns))
	}
	n := decodeMessage(t, ns[0])
	if !strings.HasPrefix(n.UnsubscribeURL, linkBase) {
		t.Errorf("UnsubscribeURL %q, want it under %s", n.UnsubscribeURL, linkBase)
	}
	if status, body := getWith(t, client, n.UnsubscribeURL); status != http.StatusOK || !strings.Contains(body, "UnsubscribeResponse") {
		t.Fatalf("UnsubscribeURL over TLS = %d %s, want 200 UnsubscribeResponse", status, body)
	}
	settleServer(t, c)
	if us := rcv.ofType("UnsubscribeConfirmation"); len(us) != 1 {
		t.Errorf("UnsubscribeConfirmations = %d, want 1", len(us))
	}
}

// deliveryOutcomes returns each journaled delivery's outcome kind and HTTP
// class.
func deliveryOutcomes(t *testing.T, c *admin.Client) (outcomes, classes []string) {
	t.Helper()
	page, err := c.JournalEvents(t.Context(), admin.JournalQuery{Kind: "delivery"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range page.Events {
		e := &page.Events[i]
		var d struct {
			Mechanism string `json:"mechanism"`
			Outcome   struct {
				Kind string `json:"kind"`
			} `json:"outcome"`
			HTTP *struct {
				Class string `json:"class"`
			} `json:"http"`
		}
		if err := json.Unmarshal(e.Detail, &d); err != nil {
			t.Fatalf("delivery event %s: %v", e.Detail, err)
		}
		if d.Mechanism != "sns-to-http" {
			continue
		}
		outcomes = append(outcomes, d.Outcome.Kind)
		if d.HTTP != nil {
			classes = append(classes, d.HTTP.Class)
		}
	}
	return outcomes, classes
}

// An endpoint the allow-list does not name, and understudy's own
// listeners whatever the list says, are refused and journaled as such
// (sns-http-delivery.md 7.2, 7.3); the default allows nothing.
func TestSNSHTTPDeliveryRefusals(t *testing.T) {
	rcv := newReceiver(t, false)
	t.Run("the default list", func(t *testing.T) {
		c, endpoint, _ := snsHTTPServer(t, server.SNSHTTPDelivery{})
		topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"hooks"}}), "TopicArn")
		mustSNS(t, endpoint, url.Values{"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {"http"}, "Endpoint": {rcv.srv.URL + "/hook"}})
		settleServer(t, c)
		if n := len(rcv.ofType("SubscriptionConfirmation")); n != 0 {
			t.Errorf("an unlisted receiver got %d confirmations", n)
		}
		outcomes, classes := deliveryOutcomes(t, c)
		if len(outcomes) != 1 || outcomes[0] != "refused" || len(classes) != 1 || classes[0] != "refused" {
			t.Errorf("deliveries %v classed %v, want one refused", outcomes, classes)
		}
	})
	t.Run("understudy's own listeners", func(t *testing.T) {
		c, endpoint, tlsEndpoint := snsHTTPServer(t, server.SNSHTTPDelivery{Allow: allowing(t, "127.0.0.1:*", "localhost:*")})
		topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"hooks"}}), "TopicArn")
		for protocol, own := range map[string]string{"http": endpoint + "/hook", "https": tlsEndpoint + "/hook"} {
			mustSNS(t, endpoint, url.Values{"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {protocol}, "Endpoint": {own}})
		}
		settleServer(t, c)
		outcomes, _ := deliveryOutcomes(t, c)
		if len(outcomes) != 2 || outcomes[0] != "refused" || outcomes[1] != "refused" {
			t.Errorf("deliveries to understudy's own listeners %v, want both refused", outcomes)
		}
	})
}

// sns.httpDelivery.caBundle adds the CAs an https endpoint may chain to
// beside the system roots (sns-http-delivery.md 7.4).
func TestSNSHTTPDeliveryTrustsTheCABundle(t *testing.T) {
	rcv := newReceiver(t, true)
	c, endpoint, _ := snsHTTPServer(t, server.SNSHTTPDelivery{
		Allow:    allowing(t, rcv.hostPort(t)),
		CABundle: []*x509.Certificate{rcv.srv.Certificate()},
	})
	topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"hooks"}}), "TopicArn")
	mustSNS(t, endpoint, url.Values{"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {"https"}, "Endpoint": {rcv.srv.URL + "/hook"}})
	settleServer(t, c)
	if n := len(rcv.ofType("SubscriptionConfirmation")); n != 1 {
		t.Errorf("the https receiver got %d confirmations, want 1", n)
	}
}
