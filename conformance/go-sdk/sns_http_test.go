package gosdk_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
)

// Research: docs/research/sns-http-delivery.md. run.sh lists 127.0.0.1:* in
// sns.httpDelivery.allow, so understudy delivers to this test's receiver.

// hookPost is one POST the receiver got.
type hookPost struct {
	header http.Header
	body   map[string]string
	raw    string
}

// hookReceiver is an httptest server that records each POST and answers
// 200.
type hookReceiver struct {
	*httptest.Server
	mu    sync.Mutex
	posts []hookPost
}

func newHookReceiver(t *testing.T) *hookReceiver {
	t.Helper()
	r := &hookReceiver{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p := hookPost{header: req.Header.Clone(), raw: string(raw)}
		// Every SNS HTTP body but a raw notification is a JSON object of
		// strings (sns-http-delivery.md 4.1).
		_ = json.Unmarshal(raw, &p.body)
		r.mu.Lock()
		r.posts = append(r.posts, p)
		r.mu.Unlock()
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *hookReceiver) ofType(typ string) []hookPost {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []hookPost
	for _, p := range r.posts {
		if p.header.Get("X-Amz-Sns-Message-Type") == typ {
			out = append(out, p)
		}
	}
	return out
}

// loopbackOnly refuses any request whose host is not loopback before it is
// sent, so a wrong link can never leave the machine, an AWS host least of
// all (8.8). Redirects pass through it too.
type loopbackOnly struct{ next http.RoundTripper }

func (l loopbackOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if !isLoopbackHost(req.URL.Hostname()) {
		return nil, fmt.Errorf("link [%s] is not on loopback; not following it", req.URL)
	}
	return l.next.RoundTrip(req)
}

// isLoopbackHost reports whether host is localhost, a *.localhost name or a
// loopback IP.
func isLoopbackHost(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// linkClient follows SNS links: an https link is on understudy's TLS
// listener, whose certificate run.sh puts in AWS_CA_BUNDLE. It refuses a
// link off loopback.
func linkClient(t *testing.T) *http.Client {
	t.Helper()
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if path := os.Getenv("AWS_CA_BUNDLE"); path != "" {
		pem, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading AWS_CA_BUNDLE: %v", err)
		}
		roots.AppendCertsFromPEM(pem)
	}
	c := &http.Client{Timeout: 10 * time.Second, Transport: loopbackOnly{&http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

// linkGet sends a GET of link through the link client; an unparsable link
// fails as the client's refusals do, with an error that names it.
func linkGet(t *testing.T, link string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, link, http.NoBody)
	if err != nil {
		return nil, err
	}
	return linkClient(t).Do(req)
}

// awsHost matches AWS's DNS suffixes (sns-http-delivery.md 7.3); no link,
// header or body understudy sends names one (8.8).
var awsHost = regexp.MustCompile(`(?i)amazonaws\.com|amazonaws\.eu|amazonwebservices|api\.aws|\.aws(?:[/:?"\\\s]|$)`)

// An http subscription gets a SubscriptionConfirmation at once (2.2);
// a GET of its SubscribeURL confirms it (2.3); a publish then reaches the
// endpoint as a Notification with the headers of 3.2.
func TestSNSHTTPSubscription(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sns.NewFromConfig(awsCfg)
	rcv := newHookReceiver(t)
	topic := createTopic(t, name(t, "hooks"), nil)
	sub, err := client.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: aws.String(topic), Protocol: aws.String("http"), Endpoint: aws.String(rcv.URL + "/hook"), ReturnSubscriptionArn: true,
	})
	ok(t, err, "Subscribe http")
	arn := aws.ToString(sub.SubscriptionArn)

	eventually(t, 10*time.Second, 50*time.Millisecond, "the SubscriptionConfirmation", func() (bool, string) {
		n := len(rcv.ofType("SubscriptionConfirmation"))
		return n == 1, "the receiver has no SubscriptionConfirmation"
	})
	confirmation := rcv.ofType("SubscriptionConfirmation")[0]
	if confirmation.body["Type"] != "SubscriptionConfirmation" || confirmation.body["TopicArn"] != topic || confirmation.header.Get("X-Amz-Sns-Topic-Arn") != topic {
		t.Errorf("SubscriptionConfirmation %s with headers %v", confirmation.raw, confirmation.header)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, confirmation.body["SubscribeURL"], http.NoBody)
	ok(t, err, "building the SubscribeURL request")
	answer := send(t, linkClient(t), req, "GET SubscribeURL "+confirmation.body["SubscribeURL"])
	if answer.status != http.StatusOK || !strings.Contains(string(answer.body), "<SubscriptionArn>"+arn+"</SubscriptionArn>") {
		t.Fatalf("GET SubscribeURL = %d %s, want 200 naming %s", answer.status, answer.body, arn)
	}
	attrs, err := client.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{SubscriptionArn: aws.String(arn)})
	ok(t, err, "GetSubscriptionAttributes")
	if attrs.Attributes["PendingConfirmation"] != "false" {
		t.Errorf("PendingConfirmation = %q after the SubscribeURL, want false", attrs.Attributes["PendingConfirmation"])
	}

	out, err := client.Publish(ctx, &sns.PublishInput{TopicArn: aws.String(topic), Message: aws.String("hello")})
	ok(t, err, "Publish")
	eventually(t, 10*time.Second, 50*time.Millisecond, "the Notification", func() (bool, string) {
		return len(rcv.ofType("Notification")) == 1, "the receiver has no Notification"
	})
	n := rcv.ofType("Notification")[0]
	if n.body["Message"] != "hello" || n.body["MessageId"] != aws.ToString(out.MessageId) || n.header.Get("X-Amz-Sns-Subscription-Arn") != arn ||
		n.header.Get("X-Amz-Sns-Message-Id") != aws.ToString(out.MessageId) || n.header.Get("User-Agent") != "Amazon Simple Notification Service Agent" {
		t.Errorf("Notification %s with headers %v", n.raw, n.header)
	}
	for _, p := range append(rcv.ofType("SubscriptionConfirmation"), rcv.ofType("Notification")...) {
		if m := awsHost.FindString(p.raw); m != "" {
			t.Errorf("a %s names the AWS host %q: %s", p.header.Get("X-Amz-Sns-Message-Type"), m, p.raw)
		}
	}
	_, err = client.Unsubscribe(ctx, &sns.UnsubscribeInput{SubscriptionArn: aws.String(arn)})
	ok(t, err, "Unsubscribe")
}

// The link client refuses a link off loopback before any request goes out,
// so a wrong SubscribeURL can never reach another host (8.8).
func TestLinkClientRefusesNonLoopback(t *testing.T) {
	t.Parallel()
	for _, link := range []string{
		"https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription",
		"http://example.com:8080/x",
		"http://10.0.0.1:8080/x",
		"http://notlocalhost:8080/x",
		"http://localhost.example.com/x",
		"::not a url",
	} {
		resp, err := linkGet(t, link)
		if err == nil {
			_ = resp.Body.Close()
			t.Errorf("GET %s succeeded, want a refusal", link)
			continue
		}
		if !strings.Contains(err.Error(), link) {
			t.Errorf("GET %s refused with %q, want the error to name the link", link, err)
		}
	}
}

func TestLinkClientAllowsLoopback(t *testing.T) {
	t.Parallel()
	rcv := newHookReceiver(t)
	for _, link := range []string{rcv.URL + "/x", strings.Replace(rcv.URL, "127.0.0.1", "localhost", 1) + "/x"} {
		resp, err := linkGet(t, link)
		if err != nil {
			t.Errorf("GET %s: %v", link, err)
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", link, resp.StatusCode)
		}
	}
}
