package gosdk_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
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
		if p.header.Get("x-amz-sns-message-type") == typ {
			out = append(out, p)
		}
	}
	return out
}

// linkClient follows SNS links: an https link is on understudy's TLS
// listener, whose certificate run.sh puts in AWS_CA_BUNDLE.
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
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	t.Cleanup(c.CloseIdleConnections)
	return c
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
	topic := createTopic(t, name("hooks"), nil)
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
	if confirmation.body["Type"] != "SubscriptionConfirmation" || confirmation.body["TopicArn"] != topic || confirmation.header.Get("x-amz-sns-topic-arn") != topic {
		t.Errorf("SubscriptionConfirmation %s with headers %v", confirmation.raw, confirmation.header)
	}
	resp, err := linkClient(t).Get(confirmation.body["SubscribeURL"])
	if err != nil {
		t.Fatalf("GET SubscribeURL %s: %v", confirmation.body["SubscribeURL"], err)
	}
	answer := string(readAll(t, resp.Body, "the SubscribeURL answer"))
	if resp.StatusCode != http.StatusOK || !strings.Contains(answer, "<SubscriptionArn>"+arn+"</SubscriptionArn>") {
		t.Fatalf("GET SubscribeURL = %d %s, want 200 naming %s", resp.StatusCode, answer, arn)
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
	if n.body["Message"] != "hello" || n.body["MessageId"] != aws.ToString(out.MessageId) || n.header.Get("x-amz-sns-subscription-arn") != arn ||
		n.header.Get("x-amz-sns-message-id") != aws.ToString(out.MessageId) || n.header.Get("User-Agent") != "Amazon Simple Notification Service Agent" {
		t.Errorf("Notification %s with headers %v", n.raw, n.header)
	}
	for _, p := range append(rcv.ofType("SubscriptionConfirmation"), rcv.ofType("Notification")...) {
		if m := awsHost.FindString(p.raw); m != "" {
			t.Errorf("a %s names the AWS host %q: %s", p.header.Get("x-amz-sns-message-type"), m, p.raw)
		}
	}
	_, err = client.Unsubscribe(ctx, &sns.UnsubscribeInput{SubscriptionArn: aws.String(arn)})
	ok(t, err, "Unsubscribe")
}
