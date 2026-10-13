package server_test

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // verifying SignatureVersion 1, which is SHA1withRSA
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

// snsCall sends one signed awsQuery SNS request as the default account's
// root and returns the status and body.
func snsCall(t *testing.T, endpoint string, form url.Values) (status int, body string) {
	t.Helper()
	form.Set("Version", "2010-03-31")
	encoded := form.Encode()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/", strings.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	region, err := aws.KnownRegions().Parse("us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	auth.Sign(req, []byte(encoded), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: region, Service: "sns"}, time.Now())
	return send(t, req)
}

// get fetches a link as a client that follows it does: no credentials.
func get(t *testing.T, link string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, link, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	return send(t, req)
}

// send makes req and returns the status and body.
func send(t *testing.T, req *http.Request) (status int, body string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

// element returns the text of the first <name> element of an XML answer.
func element(t *testing.T, body, name string) string {
	t.Helper()
	m := regexp.MustCompile("<" + name + ">([^<]*)</" + name + ">").FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no <%s> in %s", name, body)
	}
	return m[1]
}

func mustSNS(t *testing.T, endpoint string, form url.Values) string {
	t.Helper()
	status, body := snsCall(t, endpoint, form)
	if status != http.StatusOK {
		t.Fatalf("%s = %d %s", form.Get("Action"), status, body)
	}
	return body
}

// The links SNS writes into what it sends reach SNS through the edge on
// the address the client used, with no credentials: the SubscribeURL
// confirms a pending subscription, the SigningCertURL serves the
// certificate the envelope's signature verifies against, and the
// UnsubscribeURL removes the subscription. The admin API reads a pending
// subscription's token.
func TestSNSLinksWorkThroughTheEdge(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"links"}}), "TopicArn")

	pending := element(t, mustSNS(t, endpoint, url.Values{
		"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {"http"}, "Endpoint": {"http://127.0.0.1:9/hook"}, "ReturnSubscriptionArn": {"true"},
	}), "SubscriptionArn")
	_, err := c.SubscriptionToken(t.Context(), topic+":00000000-0000-0000-0000-000000000000")
	wantCode(t, err, admin.NotFound)
	token, err := c.SubscriptionToken(t.Context(), pending)
	if err != nil {
		t.Fatal(err)
	}
	if len(token.Token) != 288 {
		t.Fatalf("token = %q, want 288 characters", token.Token)
	}
	confirm := endpoint + "/_understudy/sns/ConfirmSubscriptionLink?" + url.Values{"Action": {"ConfirmSubscription"}, "TopicArn": {topic}, "Token": {token.Token}}.Encode()
	if status, body := get(t, confirm); status != http.StatusOK || element(t, body, "SubscriptionArn") != pending {
		t.Fatalf("SubscribeURL = %d %s, want 200 naming %s", status, body, pending)
	}
	attrs := mustSNS(t, endpoint, url.Values{"Action": {"GetSubscriptionAttributes"}, "SubscriptionArn": {pending}})
	if !strings.Contains(attrs, "<key>PendingConfirmation</key><value>false</value>") {
		t.Fatalf("attributes after confirming = %s, want PendingConfirmation false", attrs)
	}

	queueURL := endpoint + "/000000000000/inbox"
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"inbox"}`)
	sub := element(t, mustSNS(t, endpoint, url.Values{
		"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:us-east-1:000000000000:inbox"},
	}), "SubscriptionArn")
	mustSNS(t, endpoint, url.Values{"Action": {"Publish"}, "TopicArn": {topic}, "Message": {"hello"}})
	if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	var received struct {
		Messages []struct{ Body string }
	}
	if err := json.Unmarshal([]byte(mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+queueURL+`"}`)), &received); err != nil || len(received.Messages) != 1 {
		t.Fatalf("ReceiveMessage = %+v, %v; want the notification", received, err)
	}
	var n struct {
		MessageID        string `json:"MessageId"`
		TopicArn         string
		Message          string
		Timestamp        time.Time
		SignatureVersion string
		Signature        string
		SigningCertURL   string
		UnsubscribeURL   string
	}
	if err := json.Unmarshal([]byte(received.Messages[0].Body), &n); err != nil {
		t.Fatal(err)
	}

	status, certPEM := get(t, n.SigningCertURL)
	block, _ := pem.Decode([]byte(certPEM))
	if status != http.StatusOK || block == nil {
		t.Fatalf("SigningCertURL %s = %d %q, want a PEM certificate", n.SigningCertURL, status, certPEM)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	key, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("certificate key is %T, want RSA", cert.PublicKey)
	}
	sig, err := base64.StdEncoding.DecodeString(n.Signature)
	if err != nil {
		t.Fatal(err)
	}
	signed := envelope.StringToSign(envelope.Notification{MessageID: n.MessageID, TopicARN: n.TopicArn, Message: n.Message, Timestamp: n.Timestamp})
	hash, digest := crypto.SHA1, sha1.Sum(signed) //nolint:gosec // SignatureVersion 1
	sum := digest[:]
	if n.SignatureVersion == "2" {
		d := sha256.Sum256(signed)
		hash, sum = crypto.SHA256, d[:]
	}
	if err := rsa.VerifyPKCS1v15(key, hash, sum, sig); err != nil {
		t.Fatalf("the envelope's signature does not verify against the served certificate: %v", err)
	}

	// The queue owner subscribed its own queue, which sets
	// AuthenticateOnUnsubscribe, so the unsigned link is refused and the
	// subscription stays.
	if status, body := get(t, n.UnsubscribeURL); status != http.StatusBadRequest || !strings.Contains(body, "AuthenticateOnUnsubscribe") {
		t.Fatalf("UnsubscribeURL %s = %d %s, want 400 naming AuthenticateOnUnsubscribe", n.UnsubscribeURL, status, body)
	}
	mustSNS(t, endpoint, url.Values{"Action": {"GetSubscriptionAttributes"}, "SubscriptionArn": {sub}})

	// The http subscription was confirmed without AuthenticateOnUnsubscribe,
	// so the same link unsubscribes it.
	unsubscribe := endpoint + "/_understudy/sns/UnsubscribeLink?" + url.Values{"Action": {"Unsubscribe"}, "SubscriptionArn": {pending}}.Encode()
	if status, body := get(t, unsubscribe); status != http.StatusOK {
		t.Fatalf("UnsubscribeURL %s = %d %s, want 200", unsubscribe, status, body)
	}
	if status, body := snsCall(t, endpoint, url.Values{"Action": {"GetSubscriptionAttributes"}, "SubscriptionArn": {pending}}); status != http.StatusNotFound || element(t, body, "Code") != "NotFound" {
		t.Fatalf("GetSubscriptionAttributes after unsubscribing = %d %s, want 404 NotFound", status, body)
	}
}
