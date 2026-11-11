package sns_test

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // verifying SignatureVersion 1, which is SHA1withRSA
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/sns"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

const (
	hookURL = "http://127.0.0.1:8080/hook"
	// base is where links point when no TLS listener serves: the calling
	// client's endpoint (sns-http-delivery.md 8.3 item 3).
	base = "http://localhost:4566/_understudy/sns/"
)

// switchValue returns the value of the named switch the service lists,
// failing the test when it lists none.
func switchValue(h *harness, name string) string {
	h.Helper()
	for _, e := range h.svc.Switches() {
		if e.Name == name {
			return e.Value
		}
	}
	h.Fatalf("the service lists no switch %s", name)
	return ""
}

// noExpectation fails a test reading a switch value it has no expectation
// for: a changed switch needs its test changed with it.
func noExpectation(h *harness, name, value string) {
	h.Helper()
	h.Fatalf("switch %s is %q, which this test has no expectation for", name, value)
}

// httpRequests returns the HTTP deliveries the outbox accepted, in order,
// with their targets.
func httpRequests(h *harness) ([]kernel.Delivery, []delivery.HTTPEndpoint) {
	h.Helper()
	var ds []kernel.Delivery
	var ts []delivery.HTTPEndpoint
	all := h.sink.all()
	for i := range all {
		if tg, ok := all[i].Request.Target.(delivery.HTTPEndpoint); ok {
			ds = append(ds, all[i])
			ts = append(ts, tg)
		}
	}
	return ds, ts
}

// ofKind returns the HTTP deliveries of one kind.
func ofKind(h *harness, kind delivery.HTTPKind) ([]kernel.Delivery, []delivery.HTTPEndpoint) {
	h.Helper()
	ds, ts := httpRequests(h)
	var outD []kernel.Delivery
	var outT []delivery.HTTPEndpoint
	for i := range ts {
		if ts[i].Kind == kind {
			outD = append(outD, ds[i])
			outT = append(outT, ts[i])
		}
	}
	return outD, outT
}

// httpBody is an HTTP body's members.
type httpBody struct {
	Type              string
	MessageID         string `json:"MessageId"`
	Token             string
	TopicArn          string
	Subject           string
	Message           string
	SubscribeURL      string
	UnsubscribeURL    string
	Timestamp         string
	SignatureVersion  string
	Signature         string
	SigningCertURL    string
	MessageAttributes map[string]struct{ Type, Value string }
}

func parseBody(h *harness, body []byte) httpBody {
	h.Helper()
	var b httpBody
	if err := json.Unmarshal(body, &b); err != nil {
		h.Fatalf("body %s: %v", body, err)
	}
	return b
}

// memberNames returns a JSON object's member names in order.
func memberNames(h *harness, body []byte) []string {
	h.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	if _, err := dec.Token(); err != nil {
		h.Fatalf("%s: %v", body, err)
	}
	var names []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			h.Fatalf("%s: %v", body, err)
		}
		name, ok := tok.(string)
		if !ok {
			h.Fatalf("%s: member name %v", body, tok)
		}
		names = append(names, name)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			h.Fatalf("%s: %v", body, err)
		}
	}
	return names
}

// headerMap returns headers by name, failing on a name given twice.
func headerMap(h *harness, hs []delivery.HTTPHeader) map[string]string {
	h.Helper()
	out := map[string]string{}
	for _, x := range hs {
		if _, dup := out[x.Name]; dup {
			h.Errorf("header %s given twice", x.Name)
		}
		out[x.Name] = x.Value
	}
	return out
}

// uuidPattern is a UUID's text form: MessageId is "a Universally Unique
// Identifier" (sns-http-delivery.md 4.6, aws-doc).
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// awsHosts are the AWS DNS suffixes no emitted URL, header or body may
// name (sns-http-delivery.md 7.3, 8.8).
var awsHosts = regexp.MustCompile(`(?i)amazonaws\.com|amazonaws\.eu|amazonwebservices|api\.aws|c2s\.ic\.gov|sc2s\.sgov\.gov|aws\.scloud|adc-e\.uk|hci\.ic\.gov|\.aws(?:[/:?"\\\s]|$)`)

func noAWSHost(h *harness, what string, texts ...string) {
	h.Helper()
	for _, s := range texts {
		if m := awsHosts.FindString(s); m != "" {
			h.Errorf("%s names the AWS host %q:\n%s", what, m, s)
		}
	}
}

// certAt fetches the certificate SigningCertURL names through the
// SigningCert link and returns its key.
func certAt(h *harness, certURL, wantBase string) *rsa.PublicKey {
	h.Helper()
	if !strings.HasPrefix(certURL, wantBase+"SigningCert/aws/us-east-1/000000000000/SimpleNotificationService-") || !strings.HasSuffix(certURL, ".pem") {
		h.Fatalf("SigningCertURL %q is not %sSigningCert/aws/us-east-1/000000000000/SimpleNotificationService-<hex>.pem", certURL, wantBase)
	}
	name := certURL[strings.LastIndex(certURL, "-")+1 : len(certURL)-len(".pem")]
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(name) {
		h.Errorf("SigningCertURL %q does not end in 32 hex digits", certURL)
	}
	u, err := url.Parse(certURL)
	if err != nil {
		h.Fatal(err)
	}
	rec, aerr := h.link("SigningCert", u.Path)
	if aerr != nil {
		h.Fatalf("SigningCert = %s %q", aerr.ID.QueryCode, aerr.Message)
	}
	block, _ := pem.Decode(rec.Body.Bytes())
	if block == nil {
		h.Fatalf("SigningCert served %q, not PEM", rec.Body)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		h.Fatal(err)
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		h.Fatalf("certificate key is %T, want RSA", cert.PublicKey)
	}
	return pub
}

// verifySignature checks a body's signature over signed: SignatureVersion
// 1 is SHA1withRSA, 2 is SHA256withRSA (sns-http-delivery.md 5.2, aws-doc).
func verifySignature(h *harness, b httpBody, signed []byte, wantBase string) {
	h.Helper()
	sig, err := base64.StdEncoding.DecodeString(b.Signature)
	if err != nil {
		h.Fatalf("Signature %q: %v", b.Signature, err)
	}
	pub := certAt(h, b.SigningCertURL, wantBase)
	switch b.SignatureVersion {
	case "1":
		sum := sha1.Sum(signed) //nolint:gosec // SignatureVersion 1
		err = rsa.VerifyPKCS1v15(pub, crypto.SHA1, sum[:], sig)
	case "2":
		sum := sha256.Sum256(signed)
		err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig)
	default:
		h.Fatalf("SignatureVersion %q", b.SignatureVersion)
	}
	if err != nil {
		h.Errorf("the %s signature does not verify against the served certificate: %v", b.Type, err)
	}
}

func timestampOf(h *harness, b httpBody) time.Time {
	h.Helper()
	ts, err := time.Parse("2006-01-02T15:04:05.000Z", b.Timestamp)
	if err != nil {
		h.Fatalf("Timestamp %q: %v", b.Timestamp, err)
	}
	return ts
}

// systemHTTPPolicy is the system default healthyRetryPolicy, three backoff
// retries from 20 s to 20 s, linear (sns-http-delivery.md 6.2,
// third-party-recorded), with no throttling (6.4).
func systemHTTPPolicy() delivery.SNSHTTP {
	return delivery.SNSHTTP{Backoff: 3, MinDelayTarget: 20 * time.Second, MaxDelayTarget: 20 * time.Second, BackoffFunction: delivery.BackoffLinear}
}

// Every switch of sns-http-delivery.md 8.4 is listed with its value, its
// label, its section and the recording that settles it: aws-recorded where
// SNSH (2026-11-03) recorded the value, inferred where AWS contradicted it
// or the recording left it open.
func TestHTTPSwitchesAreListed(t *testing.T) {
	A := []string{"sns.Subscribe", "sns.Publish", "sns.PublishBatch", "sns.UnsubscribeLink"}
	P := []string{"sns.Publish", "sns.PublishBatch"}
	C := []string{"sns.Subscribe", "sns.UnsubscribeLink"}
	const rec, inf = evidence.AWSRecorded, evidence.Inferred
	want := []struct {
		name, value    string
		label          evidence.Label
		anchor, record string
		ops            []string
	}{
		{"sns.http.redirects", "not-followed", rec, "31-method-url-and-connection", "SNSH-06", A},
		{"sns.http.retry-body", "identical", rec, "46-field-values", "SNSH-07", A},
		{"sns.http.dlq-body", "as-sqs", rec, "65-dead-letter-queue", "SNSH-08", P},
		{"sns.http.backoff-curves", "linear-for-all", inf, "63-retry-schedule", "SNSH-09", A},
		{"sns.http.retry-jitter", "none", rec, "63-retry-schedule", "SNSH-10", A},
		{"sns.http.pending-messages", "dropped", rec, "22-the-confirmation-post-and-the-pending-period", "SNSH-11", P},
		{"sns.http.unsubscribe-confirmation", "unauthenticated-only", rec, "24-unsubscribing-and-the-unsubscribeconfirmation", "SNSH-15", []string{"sns.Unsubscribe", "sns.UnsubscribeLink"}},
		{"sns.http.control-dlq", "never", rec, "65-dead-letter-queue", "SNSH-17", C},
		{"sns.http.control-content-type", "as-request-policy", rec, "33-content-type", "SNSH-03", C},
		{"sns.http.basic-auth", "preemptive", inf, "31-method-url-and-connection", "SNSH-22", A},
		// A refused or dropped connection is retried; SNSH-24 was n/a
		// (sns-http-delivery.md 6.1).
		{"sns.http.connection-failure", "retried", inf, "61-response-classes", "SNSH-24", A},
	}
	run(t, func(h *harness) {
		listed := map[string]fidelity.Entry{}
		for _, e := range h.svc.Switches() {
			listed[e.Name] = e
		}
		// SNSH-12 settled the repeated Subscribe of a pending endpoint as
		// AWS does it (2.2), so it is no longer a switch.
		if _, ok := listed["sns.http.resubscribe-pending"]; ok {
			h.Error("switch sns.http.resubscribe-pending is listed, want it gone")
		}
		for _, w := range want {
			e, ok := listed[w.name]
			if !ok {
				h.Errorf("switch %s is not listed", w.name)
				continue
			}
			wantRef := "docs/research/sns-http-delivery.md#" + w.anchor
			if e.Value != w.value || e.Label != w.label || e.Ref != wantRef || e.Record != w.record || !slices.Equal(e.Ops, w.ops) {
				h.Errorf("switch %s = value %q label %s ref %s record %q ops %v; want %q %s %s %q %v",
					w.name, e.Value, e.Label, e.Ref, e.Record, e.Ops, w.value, w.label, wantRef, w.record, w.ops)
			}
			reason, unsettled := e.Settles.(fidelity.NoScenario)
			if !unsettled || !strings.Contains(reason.Reason, strings.SplitN(w.record, ",", 2)[0]) {
				h.Errorf("switch %s settles %#v, want NoScenario naming %s", w.name, e.Settles, w.record)
			}
		}
	})
}

// The coverage marks of sns-http-delivery.md 8.1, with the allow-list
// named by its setting, sns.httpDelivery.allow, and each stored policy's
// and delivery's behavior under iam.enforce (iam-enforcement-surfaces.md
// 8.2, 12.7).
func TestHTTPCoverageNotes(t *testing.T) {
	publishNote := "fan-out to sqs and lambda subscriptions, and to http and https subscriptions whose host and port are in sns.httpDelivery.allow, after their filter policies, " +
		"with raw or enveloped delivery, the DeliveryPolicy retry schedule and the RedrivePolicy dead-letter queue; every backoffFunction steps linearly from minDelayTarget to maxDelayTarget, " +
		"without jitter; under iam.enforce=enforce an sqs or lambda delivery the endpoint's policy refuses is not retried and goes to the RedrivePolicy queue, else is dropped; " +
		"other http and https endpoints, email and email-json receive nothing"
	marks := map[string]registry.BindCoverage{
		"Subscribe": registry.Partial{Note: "sqs and lambda endpoints are delivered to, and their failed deliveries go to the RedrivePolicy queue; " +
			"the endpoint's policy is not checked at Subscribe (sns.subscribe.policy-check), so under iam.enforce=enforce a policy that refuses SNS refuses each delivery; " +
			"http and https endpoints get a signed SubscriptionConfirmation when their host and port are in sns.httpDelivery.allow, " +
			"and otherwise stay subscribed with a readable token but receive nothing; loopback and private endpoints are accepted, where AWS refuses them; email and email-json stay subscribed with a readable token but receive nothing; " +
			"sms, application and firehose answer NotImplemented; the quota of 10,000 filter policies per account is not enforced"},
		"Publish":      registry.Partial{Note: publishNote},
		"PublishBatch": registry.Partial{Note: publishNote},
		"SetSubscriptionAttributes": registry.Partial{Note: "DeliveryPolicy healthyRetryPolicy and requestPolicy apply to http and https deliveries; " +
			"throttlePolicy, sicklyRetryPolicy and guaranteed are stored and echoed, never applied, so deliveries are not paced (AWS's pacing above a burst of 10 is unrecorded, SNSH-20); the quota of 10,000 filter policies per account is not enforced"},
		"SetTopicAttributes": registry.Partial{Note: "Policy is stored and echoed, evaluated under iam.enforce=advise, which journals each denial, and enforced under iam.enforce=enforce; " +
			"KmsMasterKeyId, TracingConfig, ArchivePolicy and the delivery-status attributes are stored and echoed, never applied; " +
			"the http defaults in DeliveryPolicy apply to http and https subscriptions"},
		"UnsubscribeLink": registry.Partial{Note: "the UnsubscribeConfirmation reaches the endpoint only when its host and port are in sns.httpDelivery.allow; " +
			"its Token and SubscribeURL restore the subscription for two days of emulator time, an inferred lifetime"},
		"ConfirmSubscription":       registry.Full{},
		"Unsubscribe":               registry.Full{},
		"GetSubscriptionAttributes": registry.Full{},
		"SigningCert":               registry.Full{},
		"ConfirmSubscriptionLink":   registry.Full{},
	}
	run(t, func(h *harness) {
		for name, want := range marks {
			op, ok := h.k.Registry().Operation(ref.OperationID{Service: "sns", Name: name})
			if !ok {
				h.Errorf("no operation sns.%s", name)
				continue
			}
			bound, isBound := h.k.Bindings().Status(op).(kernel.Bound)
			if !isBound {
				h.Errorf("sns.%s is unbound, want %#v", name, want)
				continue
			}
			if bound.Coverage != want {
				h.Errorf("sns.%s coverage = %#v\nwant %#v", name, bound.Coverage, want)
			}
		}
	})
}

// Subscribe sends an http or https endpoint a SubscriptionConfirmation
// "when you create the subscription" (sns-http-delivery.md 2.2, aws-doc),
// signed under the topic's SignatureVersion (5.2), with the headers of 3.2
// and the members of 4.3, its links on understudy, never on an AWS host
// (8.3 item 3, 8.8). It is due at once, so it goes out on a frozen clock
// (8.6 item 2).
func TestSubscribeSendsASignedSubscriptionConfirmation(t *testing.T) {
	for _, version := range []string{"1", "2"} {
		t.Run("SignatureVersion "+version, func(t *testing.T) {
			run(t, func(h *harness) {
				topic := h.topic("events", map[string]string{"SignatureVersion": version})
				h.advance(1500 * time.Millisecond)
				arn := h.subscribe(topic, "http", hookURL, nil)
				ds, ts := httpRequests(h)
				if len(ds) != 1 {
					h.Fatalf("the outbox took %d HTTP deliveries, want the SubscriptionConfirmation", len(ds))
				}
				d, tg := ds[0], ts[0]
				if d.Request.Source.String() != topic || d.Request.Mechanism != delivery.SNSToHTTP || d.Request.DLQ != nil || d.Accepted != h.clock.Now() {
					h.Errorf("request from %s by %v with DLQ %v accepted %v; want from the topic by sns-to-http, no DLQ, accepted now", d.Request.Source, d.Request.Mechanism, d.Request.DLQ, d.Accepted)
				}
				if p, ok := d.Request.Policy.(delivery.SNSHTTP); !ok || p != systemHTTPPolicy() {
					h.Errorf("policy = %#v, want the system default %#v", d.Request.Policy, systemHTTPPolicy())
				}
				if tg.URL.String() != hookURL || tg.Kind != delivery.HTTPSubscriptionConfirmation || tg.DeadLetterAttributes != nil {
					h.Errorf("target = %s %v with dead-letter attributes %v, want %s, a SubscriptionConfirmation, none", tg.URL.String(), tg.Kind, tg.DeadLetterAttributes, hookURL)
				}

				b := parseBody(h, tg.Body)
				token := h.token(arn)
				if want := []string{"Type", "MessageId", "Token", "TopicArn", "Message", "SubscribeURL", "Timestamp", "SignatureVersion", "Signature", "SigningCertURL"}; !slices.Equal(memberNames(h, tg.Body), want) {
					h.Errorf("members %v, want %v (4.3)", memberNames(h, tg.Body), want)
				}
				wantMessage := "You have chosen to subscribe to the topic " + topic + ".\nTo confirm the subscription, visit the SubscribeURL included in this message."
				wantSubscribe := base + "ConfirmSubscriptionLink?Action=ConfirmSubscription&TopicArn=" + topic + "&Token=" + token
				if b.Type != "SubscriptionConfirmation" || b.Token != token || b.TopicArn != topic || b.Message != wantMessage || b.SubscribeURL != wantSubscribe || b.SignatureVersion != version {
					h.Errorf("confirmation = %s", tg.Body)
				}
				if !uuidPattern.MatchString(b.MessageID) {
					h.Errorf("MessageId %q is not a UUID", b.MessageID)
				}
				if ts := timestampOf(h, b); !ts.Equal(h.clock.Now().Wire()) {
					h.Errorf("Timestamp %s, want the commit instant %s", b.Timestamp, envelope.Timestamp(h.clock.Now().Wire()))
				}
				signed := envelope.ConfirmationStringToSign(envelope.Confirmation{MessageID: b.MessageID, Token: b.Token, TopicARN: b.TopicArn, Timestamp: timestampOf(h, b), SubscribeURL: b.SubscribeURL})
				verifySignature(h, b, signed, base)

				// 3.2: the SubscriptionConfirmation's headers.
				hs := headerMap(h, tg.Headers)
				want := map[string]string{
					"x-amz-sns-message-type": "SubscriptionConfirmation",
					"x-amz-sns-message-id":   b.MessageID,
					"x-amz-sns-topic-arn":    topic,
					"Content-Type":           "text/plain; charset=UTF-8",
					"User-Agent":             "Amazon Simple Notification Service Agent",
					"Accept-Encoding":        "gzip,deflate",
				}
				if len(hs) != len(want) {
					h.Errorf("headers %v, want exactly %v", hs, want)
				}
				for name, value := range want {
					if hs[name] != value {
						h.Errorf("header %s = %q, want %q", name, hs[name], value)
					}
				}
				var headerText strings.Builder
				for _, x := range tg.Headers {
					headerText.WriteString(x.Name + ": " + x.Value + "\n")
				}
				noAWSHost(h, "the SubscriptionConfirmation", string(tg.Body), headerText.String(), tg.URL.String())
			})
		})
	}
}

// With a TLS listener the links of an HTTP delivery are on it, as
// https://localhost:<port>, so the validators' https and .pem checks can
// pass (sns-http-delivery.md 8.3 item 3, 5.6); SQS deliveries keep the
// calling client's endpoint.
func TestHTTPLinksUseTheTLSListener(t *testing.T) {
	const tlsBase = "https://localhost:4567/_understudy/sns/"
	opts := harnessOptions{sns: sns.Options{TLSPort: func() (uint16, bool) { return 4567, true }}}
	runWith(t, opts, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "http", hookURL, nil)
		_, ts := ofKind(h, delivery.HTTPSubscriptionConfirmation)
		if len(ts) != 1 {
			h.Fatalf("%d SubscriptionConfirmations, want 1", len(ts))
		}
		b := parseBody(h, ts[0].Body)
		if want := tlsBase + "ConfirmSubscriptionLink?Action=ConfirmSubscription&TopicArn=" + topic + "&Token=" + h.token(arn); b.SubscribeURL != want {
			h.Errorf("SubscribeURL = %q, want %q", b.SubscribeURL, want)
		}
		verifySignature(h, b, envelope.ConfirmationStringToSign(envelope.Confirmation{MessageID: b.MessageID, Token: b.Token, TopicARN: b.TopicArn, Timestamp: timestampOf(h, b), SubscribeURL: b.SubscribeURL}), tlsBase)

		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		h.subscribe(topic, "sqs", queueARN, nil)
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "hello"})
		_, ns := ofKind(h, delivery.HTTPNotification)
		if len(ns) != 1 {
			h.Fatalf("%d Notifications, want 1", len(ns))
		}
		n := parseBody(h, ns[0].Body)
		if want := tlsBase + "UnsubscribeLink?Action=Unsubscribe&SubscriptionArn=" + arn; n.UnsubscribeURL != want {
			h.Errorf("UnsubscribeURL = %q, want %q", n.UnsubscribeURL, want)
		}
		if !strings.HasPrefix(n.SigningCertURL, tlsBase+"SigningCert/") {
			h.Errorf("SigningCertURL = %q, want it on %s", n.SigningCertURL, tlsBase)
		}
		h.settle()
		for _, in := range h.sent.all() {
			var env sqsEnvelope
			if err := json.Unmarshal([]byte(in.MessageBody), &env); err != nil {
				h.Fatal(err)
			}
			if !strings.HasPrefix(env.SigningCertURL, base) || !strings.HasPrefix(env.UnsubscribeURL, base) {
				h.Errorf("the SQS envelope's links %q and %q, want them on the client's endpoint %s", env.SigningCertURL, env.UnsubscribeURL, base)
			}
		}
	})
}

// A confirmed http subscription receives each message its filter admits as
// a signed Notification with the members of 4.2 and the headers of 3.2,
// its subscription's dead-letter queue and delivery policy, and links on
// understudy (8.3, 8.8).
func TestPublishSendsASignedNotification(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		const dlq = "arn:aws:sqs:us-east-1:000000000000:orders-dlq"
		arn := h.subscribe(topic, "http", hookURL, map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"` + dlq + `"}`})
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		h.advance(time.Second)
		out := must(h, snsapi.OpPublish, &snsapi.PublishInput{
			TopicArn: &topic, Message: "hello", Subject: new("greeting"),
			MessageAttributes: map[string]snsapi.MessageAttributeValue{"store": attr("String", "north")},
		})
		ds, ts := ofKind(h, delivery.HTTPNotification)
		if len(ds) != 1 {
			h.Fatalf("%d Notifications, want 1", len(ds))
		}
		d, tg := ds[0], ts[0]
		if d.Request.DLQ == nil || d.Request.DLQ.String() != dlq || d.Request.Mechanism != delivery.SNSToHTTP {
			h.Errorf("request DLQ %v mechanism %v, want %s and sns-to-http", d.Request.DLQ, d.Request.Mechanism, dlq)
		}
		if p, ok := d.Request.Policy.(delivery.SNSHTTP); !ok || p != systemHTTPPolicy() {
			h.Errorf("policy = %#v, want the system default", d.Request.Policy)
		}
		if tg.DeadLetterAttributes != nil {
			h.Errorf("an enveloped notification carries dead-letter attributes %v; its envelope holds them", tg.DeadLetterAttributes)
		}
		b := parseBody(h, tg.Body)
		if want := []string{"Type", "MessageId", "TopicArn", "Subject", "Message", "Timestamp", "SignatureVersion", "Signature", "SigningCertURL", "UnsubscribeURL", "MessageAttributes"}; !slices.Equal(memberNames(h, tg.Body), want) {
			h.Errorf("members %v, want %v (4.2)", memberNames(h, tg.Body), want)
		}
		if b.Type != "Notification" || b.MessageID != *out.MessageId || b.TopicArn != topic || b.Subject != "greeting" || b.Message != "hello" ||
			b.SignatureVersion != "1" || b.MessageAttributes["store"] != (struct{ Type, Value string }{"String", "north"}) {
			h.Errorf("notification = %s", tg.Body)
		}
		if want := base + "UnsubscribeLink?Action=Unsubscribe&SubscriptionArn=" + arn; b.UnsubscribeURL != want {
			h.Errorf("UnsubscribeURL = %q, want %q", b.UnsubscribeURL, want)
		}
		if ts := timestampOf(h, b); !ts.Equal(h.clock.Now().Wire()) {
			h.Errorf("Timestamp %s, want the publish instant", b.Timestamp)
		}
		verifySignature(h, b, envelope.StringToSign(envelope.Notification{MessageID: b.MessageID, TopicARN: b.TopicArn, Subject: b.Subject, Message: b.Message, Timestamp: timestampOf(h, b)}), base)
		hs := headerMap(h, tg.Headers)
		want := map[string]string{
			"x-amz-sns-message-type":     "Notification",
			"x-amz-sns-message-id":       *out.MessageId,
			"x-amz-sns-topic-arn":        topic,
			"x-amz-sns-subscription-arn": arn,
			"Content-Type":               "text/plain; charset=UTF-8",
			"User-Agent":                 "Amazon Simple Notification Service Agent",
			"Accept-Encoding":            "gzip,deflate",
		}
		if len(hs) != len(want) {
			h.Errorf("headers %v, want exactly %v", hs, want)
		}
		for name, value := range want {
			if hs[name] != value {
				h.Errorf("header %s = %q, want %q", name, hs[name], value)
			}
		}
		noAWSHost(h, "the Notification", string(tg.Body), tg.URL.String())
	})
}

// Raw delivery sends the message itself with x-amz-sns-rawdelivery: true
// and no attributes (sns-http-delivery.md 4.5, aws-doc); under
// sns.http.dlq-body the message attributes ride with the dead letter only.
func TestRawHTTPNotification(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "http", hookURL, map[string]string{"RawMessageDelivery": "true"})
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		must(h, snsapi.OpPublish, &snsapi.PublishInput{
			TopicArn: &topic, Message: `{"order":7}`,
			MessageAttributes: map[string]snsapi.MessageAttributeValue{"store": attr("String", "north"), "price": attr("Number", "9.5")},
		})
		_, ts := ofKind(h, delivery.HTTPNotification)
		if len(ts) != 1 {
			h.Fatalf("%d Notifications, want 1", len(ts))
		}
		tg := ts[0]
		if string(tg.Body) != `{"order":7}` {
			h.Errorf("raw body = %q, want the message", tg.Body)
		}
		hs := headerMap(h, tg.Headers)
		if hs["x-amz-sns-rawdelivery"] != "true" || hs["x-amz-sns-message-type"] != "Notification" || hs["x-amz-sns-subscription-arn"] != arn {
			h.Errorf("raw headers = %v", hs)
		}
		switch v := switchValue(h, "sns.http.dlq-body"); v {
		case "as-sqs":
			got := map[string]delivery.AttributeValue{}
			for _, a := range tg.DeadLetterAttributes {
				got[a.Name] = a.Value
			}
			if len(got) != 2 || got["store"] != (delivery.StringAttribute{Value: "north"}) || got["price"] != (delivery.NumberAttribute{Value: "9.5"}) {
				h.Errorf("dead-letter attributes = %v, want store and price as published", tg.DeadLetterAttributes)
			}
		default:
			noExpectation(h, "sns.http.dlq-body", v)
		}
	})
}

// While a subscription is pending it is skipped at fan-out, and under
// sns.http.pending-messages what was published meanwhile is not held for
// it (sns-http-delivery.md 2.2, 8.2 item 6).
func TestPendingHTTPSubscriptionMissesWhatIsPublished(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "https", "https://127.0.0.1:9443/hook", nil)
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "while pending"})
		if _, ns := ofKind(h, delivery.HTTPNotification); len(ns) != 0 {
			h.Fatalf("a pending subscription got %d Notifications, want none", len(ns))
		}
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "once confirmed"})
		_, ns := ofKind(h, delivery.HTTPNotification)
		switch v := switchValue(h, "sns.http.pending-messages"); v {
		case "dropped":
			if len(ns) != 1 || parseBody(h, ns[0].Body).Message != "once confirmed" {
				h.Errorf("after confirmation the subscription got %d Notifications, want only the one published since", len(ns))
			}
		default:
			noExpectation(h, "sns.http.pending-messages", v)
		}
	})
}

// A repeated Subscribe of a pending endpoint returns the same ARN and sends
// a second confirmation with a new token, a new MessageId and the current
// Timestamp; either token confirms, and the other then answers 403
// AuthorizationError "Subscription already confirmed"; once confirmed, a
// repeat sends nothing (sns-http-delivery.md 2.2, SNSH-12).
func TestResubscribingAPendingEndpoint(t *testing.T) {
	for _, confirmWith := range []string{"first", "second"} {
		t.Run("confirmed with the "+confirmWith+" token", func(t *testing.T) {
			run(t, func(h *harness) {
				topic := h.topic("events", nil)
				arn := h.subscribe(topic, "http", hookURL, nil)
				h.advance(time.Minute)
				again := h.subscribe(topic, "http", hookURL, nil)
				if again != arn {
					h.Fatalf("the repeat Subscribe returned %s, want the pending subscription %s", again, arn)
				}
				_, cs := ofKind(h, delivery.HTTPSubscriptionConfirmation)
				if len(cs) != 2 {
					h.Fatalf("%d SubscriptionConfirmations, want one per Subscribe", len(cs))
				}
				first, second := parseBody(h, cs[0].Body), parseBody(h, cs[1].Body)
				if second.Token == first.Token || second.SubscribeURL == first.SubscribeURL {
					h.Errorf("the second confirmation repeats the first's token %q and link %q, want new ones", first.Token, first.SubscribeURL)
				}
				if want := base + "ConfirmSubscriptionLink?Action=ConfirmSubscription&TopicArn=" + topic + "&Token=" + second.Token; second.SubscribeURL != want {
					h.Errorf("the second SubscribeURL = %q, want %q", second.SubscribeURL, want)
				}
				if second.MessageID == first.MessageID {
					h.Errorf("both confirmations have MessageId %s", first.MessageID)
				}
				if ts := timestampOf(h, second); !ts.Equal(h.clock.Now().Wire()) {
					h.Errorf("the second confirmation's Timestamp %s, want now", second.Timestamp)
				}
				used, other := first.Token, second.Token
				if confirmWith == "second" {
					used, other = other, used
				}
				confirmed := must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: used})
				if *confirmed.SubscriptionArn != arn {
					h.Errorf("ConfirmSubscription = %s, want %s", *confirmed.SubscriptionArn, arn)
				}
				_, err := call(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: other})
				wantError(h, err, "AuthorizationError", "Subscription already confirmed")
				if err.ID.Status != 403 {
					h.Errorf("the other token answers HTTP %d, want 403", err.ID.Status)
				}
				before := len(h.sink.all())
				h.subscribe(topic, "http", hookURL, nil)
				if n := len(h.sink.all()); n != before {
					h.Errorf("a Subscribe of the confirmed endpoint took %d deliveries, want none", n-before)
				}
			})
		})
	}
}

// An unsigned GET of a confirmed subscription's UnsubscribeURL sends the
// endpoint an UnsubscribeConfirmation (sns-http-delivery.md 2.4,
// third-party-recorded), with the headers of 3.2 and the members and
// Message of 4.4, a new token (2.4), signed over the confirmation field
// list with Token (5.1); a signed Unsubscribe sends nothing under
// sns.http.unsubscribe-confirmation.
func TestUnsubscribeLinkSendsAnUnsubscribeConfirmation(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", map[string]string{"SignatureVersion": "2"})
		arn := h.subscribe(topic, "http", hookURL, map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:orders-dlq"}`})
		original := h.token(arn)
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: original})
		h.advance(time.Minute)
		if _, aerr := h.link("UnsubscribeLink", "/_understudy/sns/UnsubscribeLink?Action=Unsubscribe&SubscriptionArn="+url.QueryEscape(arn)); aerr != nil {
			h.Fatalf("UnsubscribeLink = %s %q", aerr.ID.QueryCode, aerr.Message)
		}
		ds, ts := ofKind(h, delivery.HTTPUnsubscribeConfirmation)
		if len(ds) != 1 {
			h.Fatalf("%d UnsubscribeConfirmations, want 1", len(ds))
		}
		d, tg := ds[0], ts[0]
		if p, ok := d.Request.Policy.(delivery.SNSHTTP); !ok || p != systemHTTPPolicy() || d.Request.Mechanism != delivery.SNSToHTTP {
			h.Errorf("request = %+v, want sns-to-http under the system default policy", d.Request)
		}
		switch v := switchValue(h, "sns.http.control-dlq"); v {
		case "never":
			if d.Request.DLQ != nil {
				h.Errorf("the UnsubscribeConfirmation has DLQ %s, want none", d.Request.DLQ)
			}
		default:
			noExpectation(h, "sns.http.control-dlq", v)
		}
		b := parseBody(h, tg.Body)
		if want := []string{"Type", "MessageId", "Token", "TopicArn", "Message", "SubscribeURL", "Timestamp", "SignatureVersion", "Signature", "SigningCertURL"}; !slices.Equal(memberNames(h, tg.Body), want) {
			h.Errorf("members %v, want %v (4.4)", memberNames(h, tg.Body), want)
		}
		wantMessage := "You have chosen to deactivate subscription " + arn + ".\nTo cancel this operation and restore the subscription, visit the SubscribeURL included in this message."
		if b.Type != "UnsubscribeConfirmation" || b.TopicArn != topic || b.Message != wantMessage || b.SignatureVersion != "2" {
			h.Errorf("UnsubscribeConfirmation = %s", tg.Body)
		}
		if !uuidPattern.MatchString(b.MessageID) {
			h.Errorf("MessageId %q is not a UUID", b.MessageID)
		}
		if b.Token == original || !regexp.MustCompile(`^[0-9a-f]{288}$`).MatchString(b.Token) {
			h.Errorf("Token %q, want a new 288-hex token (2.3, 2.4)", b.Token)
		}
		if want := base + "ConfirmSubscriptionLink?Action=ConfirmSubscription&TopicArn=" + topic + "&Token=" + b.Token; b.SubscribeURL != want {
			h.Errorf("SubscribeURL = %q, want %q", b.SubscribeURL, want)
		}
		if ts := timestampOf(h, b); !ts.Equal(h.clock.Now().Wire()) {
			h.Errorf("Timestamp %s, want the unsubscribe's instant", b.Timestamp)
		}
		signed := envelope.UnsubscribeConfirmationStringToSign(envelope.UnsubscribeConfirmation{
			MessageID: b.MessageID, Token: b.Token, TopicARN: b.TopicArn, SubscriptionARN: arn, Timestamp: timestampOf(h, b), SubscribeURL: b.SubscribeURL,
		})
		verifySignature(h, b, signed, base)
		hs := headerMap(h, tg.Headers)
		if hs["x-amz-sns-message-type"] != "UnsubscribeConfirmation" || hs["x-amz-sns-message-id"] != b.MessageID || hs["x-amz-sns-topic-arn"] != topic ||
			hs["x-amz-sns-subscription-arn"] != arn || hs["Content-Type"] != "text/plain; charset=UTF-8" || hs["x-amz-sns-rawdelivery"] != "" {
			h.Errorf("UnsubscribeConfirmation headers = %v", hs)
		}
		noAWSHost(h, "the UnsubscribeConfirmation", string(tg.Body))

		other := h.subscribe(topic, "http", "http://127.0.0.1:8080/other", nil)
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(other)})
		must(h, snsapi.OpUnsubscribe, &snsapi.UnsubscribeInput{SubscriptionArn: other})
		switch v := switchValue(h, "sns.http.unsubscribe-confirmation"); v {
		case "unauthenticated-only":
			if _, us := ofKind(h, delivery.HTTPUnsubscribeConfirmation); len(us) != 1 {
				h.Errorf("%d UnsubscribeConfirmations after a signed Unsubscribe, want still the one", len(us))
			}
		default:
			noExpectation(h, "sns.http.unsubscribe-confirmation", v)
		}
	})
}

// A link the subscription refuses (AuthenticateOnUnsubscribe) sends
// nothing.
func TestRefusedUnsubscribeLinkSendsNothing(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "http", hookURL, nil)
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn), AuthenticateOnUnsubscribe: new("true")})
		if _, aerr := h.link("UnsubscribeLink", "/_understudy/sns/UnsubscribeLink?Action=Unsubscribe&SubscriptionArn="+url.QueryEscape(arn)); aerr == nil {
			h.Fatal("the link removed a subscription confirmed with AuthenticateOnUnsubscribe=true")
		}
		if _, us := ofKind(h, delivery.HTTPUnsubscribeConfirmation); len(us) != 0 {
			h.Errorf("%d UnsubscribeConfirmations after a refused link, want none", len(us))
		}
	})
}

// unsubscribeByLink GETs arn's UnsubscribeURL and returns the
// UnsubscribeConfirmation it sent the endpoint.
func unsubscribeByLink(h *harness, arn string) httpBody {
	h.Helper()
	before, _ := ofKind(h, delivery.HTTPUnsubscribeConfirmation)
	if _, aerr := h.link("UnsubscribeLink", "/_understudy/sns/UnsubscribeLink?Action=Unsubscribe&SubscriptionArn="+url.QueryEscape(arn)); aerr != nil {
		h.Fatalf("UnsubscribeLink = %s %q", aerr.ID.QueryCode, aerr.Message)
	}
	_, ts := ofKind(h, delivery.HTTPUnsubscribeConfirmation)
	if len(ts) != len(before)+1 {
		h.Fatalf("%d UnsubscribeConfirmations, want %d", len(ts), len(before)+1)
	}
	return parseBody(h, ts[len(ts)-1].Body)
}

// followSubscribeURL GETs subscribeURL and returns the subscription ARN
// its ConfirmSubscriptionResponse carries.
func followSubscribeURL(h *harness, subscribeURL string) (string, *awserr.Error) {
	h.Helper()
	u, err := url.Parse(subscribeURL)
	if err != nil {
		h.Fatal(err)
	}
	rec, aerr := h.link("ConfirmSubscriptionLink", u.RequestURI())
	if aerr != nil {
		return "", aerr
	}
	var doc struct {
		XMLName xml.Name `xml:"ConfirmSubscriptionResponse"`
		ARN     string   `xml:"ConfirmSubscriptionResult>SubscriptionArn"`
	}
	if rec.Code != 200 {
		h.Fatalf("ConfirmSubscriptionLink answered HTTP %d", rec.Code)
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		h.Fatalf("ConfirmSubscriptionLink answered %s: %v", rec.Body, err)
	}
	return doc.ARN, nil
}

// listedARNs returns the subscription ARNs ListSubscriptionsByTopic shows
// for topic.
func listedARNs(h *harness, topic string) []string {
	h.Helper()
	listed := must(h, snsapi.OpListSubscriptionsByTopic, &snsapi.ListSubscriptionsByTopicInput{TopicArn: topic}).Subscriptions
	out := make([]string, 0, len(listed))
	for _, s := range listed {
		out = append(out, *s.SubscriptionArn)
	}
	return out
}

// Visiting an UnsubscribeConfirmation's SubscribeURL restores the
// subscription under its old ARN: 200 ConfirmSubscriptionResponse with
// that ARN, PendingConfirmation and ConfirmationWasAuthenticated false,
// listed again (sns-http-delivery.md 2.4, SNSH-16, AWS-recorded). A
// second visit answers the same ARN, as confirming is idempotent (2.3).
// Until then the subscription is gone and receives nothing, which is
// inferred: AWS did not record what the old ARN answers in between.
// Keeping its attributes across the restore is inferred too, as the
// recorded subscription had only defaults; delivery after the restore is
// inferred from it being confirmed again.
func TestUnsubscribeConfirmationLinkRestoresTheSubscription(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "http", hookURL, map[string]string{"RawMessageDelivery": "true"})
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		u := unsubscribeByLink(h, arn)

		_, err := call(h, snsapi.OpGetSubscriptionAttributes, &snsapi.GetSubscriptionAttributesInput{SubscriptionArn: arn})
		wantError(h, err, "NotFound", "Subscription does not exist")
		if listed := listedARNs(h, topic); len(listed) != 0 {
			h.Errorf("listed while unsubscribed: %v", listed)
		}
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "while-gone"})
		if ns, _ := ofKind(h, delivery.HTTPNotification); len(ns) != 0 {
			h.Errorf("%d Notifications while unsubscribed, want none", len(ns))
		}

		restored, aerr := followSubscribeURL(h, u.SubscribeURL)
		if aerr != nil {
			h.Fatalf("SubscribeURL = %s %q", aerr.ID.QueryCode, aerr.Message)
		}
		if restored != arn {
			h.Errorf("SubscribeURL answered ARN %s, want %s", restored, arn)
		}
		attrs := h.subscriptionAttributes(arn)
		want := map[string]string{
			"SubscriptionArn": arn, "TopicArn": topic, "Protocol": "http", "Endpoint": hookURL,
			"PendingConfirmation": "false", "ConfirmationWasAuthenticated": "false", "RawMessageDelivery": "true",
		}
		for k, v := range want {
			if attrs[k] != v {
				h.Errorf("%s after the restore = %q, want %q", k, attrs[k], v)
			}
		}
		if listed := listedARNs(h, topic); !slices.Equal(listed, []string{arn}) {
			h.Errorf("listed after the restore: %v, want [%s]", listed, arn)
		}
		if again, aerr := followSubscribeURL(h, u.SubscribeURL); aerr != nil || again != arn {
			h.Errorf("a second visit = %s, %v; want %s", again, aerr, arn)
		}

		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "after-restore"})
		_, ns := ofKind(h, delivery.HTTPNotification)
		if len(ns) != 1 || ns[0].URL.String() != hookURL || string(ns[0].Body) != "after-restore" {
			h.Errorf("Notifications after the restore = %+v, want the raw message at %s", ns, hookURL)
		}
	})
}

// Two days after the unsubscribe the UnsubscribeConfirmation's SubscribeURL
// answers as an unknown token does (sns-http-delivery.md 2.3); the two
// days are the confirmation token's lifetime (2.2), as AWS did not record
// this token's, so the lifetime is inferred.
func TestUnsubscribeConfirmationLinkLastsTwoDays(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "http", hookURL, nil)
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		early := h.subscribe(topic, "http", "http://127.0.0.1:8080/early", nil)
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(early)})
		u, earlyU := unsubscribeByLink(h, arn), unsubscribeByLink(h, early)
		h.advance(48*time.Hour - time.Second)
		if restored, aerr := followSubscribeURL(h, earlyU.SubscribeURL); aerr != nil || restored != early {
			h.Fatalf("a second before two days the link = %s, %v; want %s", restored, aerr, early)
		}
		h.advance(time.Second)
		_, err := followSubscribeURL(h, u.SubscribeURL)
		wantError(h, err, "InvalidParameter", "Invalid parameter: Token")
		if listed := listedARNs(h, topic); !slices.Equal(listed, []string{early}) {
			h.Errorf("listed after the link expired: %v, want [%s]", listed, early)
		}
	})
}

// ConfirmSubscription with the UnsubscribeConfirmation's Token restores
// the subscription as its SubscribeURL does, the link being that call
// without credentials (sns-http-delivery.md 2.3); with credentials it
// reads ConfirmationWasAuthenticated true. AWS recorded only the link, so
// this is inferred.
func TestUnsubscribeConfirmationTokenRestoresThroughTheCall(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "http", hookURL, nil)
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		u := unsubscribeByLink(h, arn)
		restored := must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: u.Token})
		if *restored.SubscriptionArn != arn {
			h.Errorf("ConfirmSubscription = %s, want %s", *restored.SubscriptionArn, arn)
		}
		if got := h.subscriptionAttributes(arn)["ConfirmationWasAuthenticated"]; got != "true" {
			h.Errorf("ConfirmationWasAuthenticated after the call = %q, want true", got)
		}
	})
}

// A topic that subscribed the endpoint again before the restore holds the
// new subscription only: the old token answers as an unknown one, which is
// inferred, since AWS did not record it.
func TestUnsubscribeConfirmationLinkAfterResubscribing(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "http", hookURL, nil)
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		u := unsubscribeByLink(h, arn)
		again := h.subscribe(topic, "http", hookURL, nil)
		if again == arn {
			h.Fatalf("the new Subscribe reused the unsubscribed ARN %s", arn)
		}
		_, err := followSubscribeURL(h, u.SubscribeURL)
		wantError(h, err, "InvalidParameter", "Invalid parameter: Token")
		if listed := listedARNs(h, topic); !slices.Equal(listed, []string{"PendingConfirmation"}) {
			h.Errorf("listed = %v, want the new pending subscription only", listed)
		}
	})
}

// Under sns.http.control-dlq a SubscriptionConfirmation never goes to the
// RedrivePolicy queue, while the subscription's notifications do
// (sns-http-delivery.md 6.5, 8.2 item 4).
func TestConfirmationsHaveNoDeadLetterQueue(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		const dlq = "arn:aws:sqs:us-east-1:000000000000:orders-dlq"
		arn := h.subscribe(topic, "http", hookURL, map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"` + dlq + `"}`})
		cs, _ := ofKind(h, delivery.HTTPSubscriptionConfirmation)
		switch v := switchValue(h, "sns.http.control-dlq"); v {
		case "never":
			if len(cs) != 1 || cs[0].Request.DLQ != nil {
				h.Errorf("SubscriptionConfirmations %+v, want one with no DLQ", cs)
			}
		default:
			noExpectation(h, "sns.http.control-dlq", v)
		}
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
		if ns, _ := ofKind(h, delivery.HTTPNotification); len(ns) != 1 || ns[0].Request.DLQ == nil || ns[0].Request.DLQ.String() != dlq {
			h.Errorf("Notifications %+v, want one with DLQ %s", ns, dlq)
		}
	})
}

// requestPolicy.headerContentType sets a notification's Content-Type,
// verbatim (sns-http-delivery.md 3.3, third-party-recorded for a topic's
// application/json and a raw subscription's text/csv); under
// sns.http.control-content-type the confirmations carry it too (SNSH-03,
// recorded 2026-11-03), and one with none keeps text/plain.
func TestRequestPolicyContentType(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", map[string]string{"DeliveryPolicy": `{"http":{"defaultRequestPolicy":{"headerContentType":"application/json"}}}`})
		enveloped := h.subscribe(topic, "http", hookURL, nil)
		raw := h.subscribe(topic, "http", "http://127.0.0.1:8080/raw", map[string]string{
			"RawMessageDelivery": "true",
			"DeliveryPolicy":     `{"requestPolicy":{"headerContentType":"text/csv"}}`,
		})
		plainTopic := h.topic("plain", nil)
		h.subscribe(plainTopic, "http", "http://127.0.0.1:8080/plain", nil)
		_, cs := ofKind(h, delivery.HTTPSubscriptionConfirmation)
		switch v := switchValue(h, "sns.http.control-content-type"); v {
		case "as-request-policy":
			want := map[string]string{hookURL: "application/json", "http://127.0.0.1:8080/raw": "text/csv", "http://127.0.0.1:8080/plain": "text/plain; charset=UTF-8"}
			if len(cs) != len(want) {
				h.Fatalf("%d SubscriptionConfirmations, want %d", len(cs), len(want))
			}
			for _, c := range cs {
				if got := headerMap(h, c.Headers)["Content-Type"]; got != want[c.URL.String()] {
					h.Errorf("a SubscriptionConfirmation to %s has Content-Type %q, want %q", c.URL.String(), got, want[c.URL.String()])
				}
			}
		default:
			noExpectation(h, "sns.http.control-content-type", v)
		}
		for _, arn := range []string{enveloped, raw} {
			must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		}
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "a,b"})
		_, ns := ofKind(h, delivery.HTTPNotification)
		if len(ns) != 2 {
			h.Fatalf("%d Notifications, want 2", len(ns))
		}
		want := map[string]string{hookURL: "application/json", "http://127.0.0.1:8080/raw": "text/csv"}
		for _, n := range ns {
			if got := headerMap(h, n.Headers)["Content-Type"]; got != want[n.URL.String()] {
				h.Errorf("the Notification to %s has Content-Type %q, want %q", n.URL.String(), got, want[n.URL.String()])
			}
		}
	})
}

// Credentials in the endpoint URL select Basic authentication
// (sns-http-delivery.md 2.1, 3.1, aws-doc); under sns.http.basic-auth they
// are sent on the first request.
func TestEndpointCredentialsSendBasicAuthorization(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		h.subscribe(topic, "http", "http://alice:s3cret@127.0.0.1:8080/hook", nil)
		h.subscribe(topic, "http", "http://bob@127.0.0.1:8080/hook", nil)
		h.subscribe(topic, "http", hookURL, nil)
		_, cs := ofKind(h, delivery.HTTPSubscriptionConfirmation)
		if len(cs) != 3 {
			h.Fatalf("%d SubscriptionConfirmations, want 3", len(cs))
		}
		v := switchValue(h, "sns.http.basic-auth")
		if v != "preemptive" {
			noExpectation(h, "sns.http.basic-auth", v)
		}
		want := map[string]string{
			"http://alice:s3cret@127.0.0.1:8080/hook": "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret")),
			"http://bob@127.0.0.1:8080/hook":          "Basic " + base64.StdEncoding.EncodeToString([]byte("bob:")),
			hookURL:                                   "",
		}
		for _, c := range cs {
			got, ok := headerMap(h, c.Headers)["Authorization"]
			if w := want[c.URL.String()]; got != w || ok != (w != "") {
				h.Errorf("Authorization of the confirmation to %s = %q (set %t), want %q", c.URL.String(), got, ok, w)
			}
		}
	})
}

// The delivery policy of an http subscription is its effective one: each
// top-level member is the subscription's when set, else the topic's http
// default, else for healthyRetryPolicy the system default; the phases are
// numNoDelayRetries, numMinDelayRetries, the backoff retries that remain of
// numRetries, and numMaxDelayRetries (sns-http-delivery.md 6.2,
// third-party-recorded; 6.3, aws-doc); a throttlePolicy is stored and not
// applied (6.4, SNSH-20).
func TestHTTPRetryPolicyResolution(t *testing.T) {
	const full = `{"minDelayTarget":1,"maxDelayTarget":4,"numRetries":5,"numNoDelayRetries":2,"numMinDelayRetries":0,"numMaxDelayRetries":0,"backoffFunction":"exponential"}`
	const topicDefault = `{"minDelayTarget":2,"maxDelayTarget":30,"numRetries":6,"numNoDelayRetries":1,"numMinDelayRetries":1,"numMaxDelayRetries":1,"backoffFunction":"arithmetic"}`
	cases := []struct {
		name              string
		topic, subscriber string
		want              delivery.SNSHTTP
	}{
		{"neither", "", "", systemHTTPPolicy()},
		{
			"the subscription's healthyRetryPolicy", "", `{"healthyRetryPolicy":` + full + `}`,
			delivery.SNSHTTP{NoDelay: 2, Backoff: 3, MinDelayTarget: time.Second, MaxDelayTarget: 4 * time.Second, BackoffFunction: delivery.BackoffExponential},
		},
		{
			"the topic's default", `{"http":{"defaultHealthyRetryPolicy":` + topicDefault + `}}`, "",
			delivery.SNSHTTP{NoDelay: 1, MinDelay: 1, Backoff: 3, MaxDelay: 1, MinDelayTarget: 2 * time.Second, MaxDelayTarget: 30 * time.Second, BackoffFunction: delivery.BackoffArithmetic},
		},
		{
			"the subscription's over the topic's", `{"http":{"defaultHealthyRetryPolicy":` + topicDefault + `}}`, `{"healthyRetryPolicy":` + full + `}`,
			delivery.SNSHTTP{NoDelay: 2, Backoff: 3, MinDelayTarget: time.Second, MaxDelayTarget: 4 * time.Second, BackoffFunction: delivery.BackoffExponential},
		},
		{
			"the subscription's throttlePolicy is not applied (SNSH-20)", "", `{"throttlePolicy":{"maxReceivesPerSecond":2}}`,
			systemHTTPPolicy(),
		},
		{
			"the topic's throttlePolicy is not applied (SNSH-20)", `{"http":{"defaultThrottlePolicy":{"maxReceivesPerSecond":3}}}`, "",
			systemHTTPPolicy(),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run(t, func(h *harness) {
				var topicAttrs, subAttrs map[string]string
				if c.topic != "" {
					topicAttrs = map[string]string{"DeliveryPolicy": c.topic}
				}
				if c.subscriber != "" {
					subAttrs = map[string]string{"DeliveryPolicy": c.subscriber}
				}
				topic := h.topic("events", topicAttrs)
				arn := h.subscribe(topic, "http", hookURL, subAttrs)
				must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
				must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
				ds, _ := httpRequests(h)
				if len(ds) != 2 {
					h.Fatalf("%d HTTP deliveries, want the confirmation and the notification", len(ds))
				}
				for _, d := range ds {
					if got, ok := d.Request.Policy.(delivery.SNSHTTP); !ok || got != c.want {
						h.Errorf("policy = %#v, want %#v", d.Request.Policy, c.want)
					}
				}
			})
		})
	}
}

// A message an http subscription's filter rejects is journaled for it by
// mechanism and endpoint, before any attempt.
func TestFilteredHTTPDeliveryIsJournaled(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "http", hookURL, map[string]string{"FilterPolicy": `{"store":["north"]}`})
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m", MessageAttributes: map[string]snsapi.MessageAttributeValue{"store": attr("String", "south")}})
		if _, ns := ofKind(h, delivery.HTTPNotification); len(ns) != 0 {
			h.Errorf("a filtered message was delivered: %d Notifications", len(ns))
		}
		var found bool
		events := h.k.Journal().Query(journal.Filter{Kind: journal.KindDelivery, Limit: 1000}).Events
		for i := range events {
			d, ok := events[i].Detail.(journal.Delivery)
			if !ok {
				continue
			}
			if _, filtered := d.Outcome.(journal.Filtered); filtered {
				found = true
				if d.Mechanism != delivery.SNSToHTTP || d.To != (aws.ARN{}) || d.Endpoint != "127.0.0.1:8080" || d.Attempt != 0 || d.HTTP != nil {
					h.Errorf("filtered delivery = %+v, want sns-to-http to 127.0.0.1:8080 at attempt 0, no ARN and no exchange", d)
				}
			}
		}
		if !found {
			h.Error("no Filtered delivery was journaled")
		}
	})
}

// A pending https subscription and an email one receive no notification:
// the https endpoint's only delivery is its SubscriptionConfirmation
// (sns-http-delivery.md 2.2).
func TestPendingSubscriptionsReceiveNoNotification(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		h.subscribe(topic, "https", "https://127.0.0.1:9443/hook", nil)
		h.subscribe(topic, "email", "a@example.com", nil)
		before := len(h.sink.all())
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
		if n := len(h.sink.all()) - before; n != 0 {
			h.Errorf("the publish gave pending subscriptions %d deliveries, want none", n)
		}
		if _, cs := ofKind(h, delivery.HTTPSubscriptionConfirmation); len(cs) != 1 {
			h.Errorf("%d SubscriptionConfirmations, want the https endpoint's one", len(cs))
		}
	})
}
