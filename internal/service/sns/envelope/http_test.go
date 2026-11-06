package envelope_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

// recordedSigner returns a recorded signature, so a body rebuilt from a
// recording carries AWS's own bytes, and records what it was asked to sign.
type recordedSigner struct {
	sig    []byte
	signed []byte
}

func (s *recordedSigner) Sign(_ envelope.Version, stringToSign []byte) ([]byte, error) {
	s.signed = stringToSign
	return s.sig, nil
}

// recordedEnvelopes returns the enveloped notification bodies an SQS queue
// received from SNS in a recording (aws-recorded,
// testdata/parity/sqs/R16/*.json).
func recordedEnvelopes(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "testdata", "parity", "sqs", "R16", name))
	if err != nil {
		t.Fatal(err)
	}
	var step struct {
		Exchanges []struct {
			Response struct{ Body string }
		}
	}
	if err := json.Unmarshal(data, &step); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, x := range step.Exchanges {
		var received struct{ Messages []struct{ Body string } }
		if err := json.Unmarshal([]byte(x.Response.Body), &received); err != nil {
			continue
		}
		for _, m := range received.Messages {
			bodies = append(bodies, m.Body)
		}
	}
	return bodies
}

// The HTTP Notification body has the member order of the Notification
// format page and the "Key" : value layout of its sample
// (sns-http-delivery.md 4.1, 4.2, aws-doc), which is the layout AWS gives an
// SQS subscription byte for byte (aws-recorded, 4.1). Each recorded envelope
// without attributes, rebuilt from its own values and signature, comes out
// identical; the attribute block's order is a recorded difference
// (testdata/parity/sns/R16/notes.md) and is not pinned here.
func TestHTTPNotificationMatchesTheRecordedLayout(t *testing.T) {
	var checked int
	for _, file := range []string{"01-sqs-enveloped.json", "01-sqs-enveloped-2.json"} {
		for _, body := range recordedEnvelopes(t, file) {
			var rec struct {
				Type, MessageID, TopicArn, Subject, Message, Timestamp      string
				SignatureVersion, Signature, SigningCertURL, UnsubscribeURL string
				MessageAttributes                                           map[string]json.RawMessage
			}
			if err := json.Unmarshal([]byte(body), &rec); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			if rec.Type != "Notification" || len(rec.MessageAttributes) > 0 {
				continue
			}
			ts, err := time.Parse("2006-01-02T15:04:05.000Z", rec.Timestamp)
			if err != nil {
				t.Fatal(err)
			}
			sig, err := base64.StdEncoding.DecodeString(rec.Signature)
			if err != nil {
				t.Fatal(err)
			}
			n := envelope.Notification{
				MessageID: rec.MessageID, TopicARN: rec.TopicArn, Subject: rec.Subject, Message: rec.Message,
				Timestamp: ts, UnsubscribeURL: rec.UnsubscribeURL,
			}
			signer := &recordedSigner{sig: sig}
			got, err := envelope.HTTPNotification(n, envelope.Signing{Version: envelope.V1, Signer: signer, CertURL: rec.SigningCertURL})
			if err != nil {
				t.Fatalf("HTTPNotification: %v", err)
			}
			if string(got) != body {
				t.Errorf("%s: HTTP notification\n%s\nwant the recorded\n%s", file, got, body)
			}
			// A Notification signs Message, MessageId, Subject when present,
			// Timestamp, TopicArn and Type (5.1).
			want := "Message\n" + rec.Message + "\nMessageId\n" + rec.MessageID + "\n"
			if rec.Subject != "" {
				want += "Subject\n" + rec.Subject + "\n"
			}
			want += "Timestamp\n" + rec.Timestamp + "\nTopicArn\n" + rec.TopicArn + "\nType\nNotification\n"
			if string(signer.signed) != want {
				t.Errorf("%s: signed %q, want %q", file, signer.signed, want)
			}
			checked++
		}
	}
	if checked < 2 {
		t.Fatalf("checked %d recorded envelopes, want the two without attributes", checked)
	}
}

// Over random notifications and confirmations the HTTP bodies are the SQS
// bodies: the HTTP Notification and SubscriptionConfirmation have the
// members and layout of the SQS ones (4.1, 4.2, 4.3).
func TestHTTPBodiesAreTheSQSBodies(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		version := rapid.SampledFrom([]envelope.Version{envelope.V1, envelope.V2}).Draw(t, "version")
		n := envelope.Notification{
			MessageID:      rapid.StringMatching(`[0-9a-f-]{1,36}`).Draw(t, "id"),
			TopicARN:       rapid.String().Draw(t, "topic"),
			Subject:        rapid.String().Draw(t, "subject"),
			Message:        rapid.StringN(1, 64, -1).Draw(t, "message"),
			Timestamp:      time.UnixMilli(rapid.Int64Range(0, 1<<42).Draw(t, "ms")),
			UnsubscribeURL: rapid.String().Draw(t, "unsubscribe"),
		}
		for i := range rapid.IntRange(0, 3).Draw(t, "attrs") {
			n.Attributes = append(n.Attributes, envelope.Attribute{Name: "a" + string(rune('0'+i)), Type: "String", Value: rapid.String().Draw(t, "value")})
		}
		s := envelope.Signing{Version: version, Signer: &stubSigner{}, CertURL: rapid.String().Draw(t, "cert")}
		want, err := envelope.SQS(n, s)
		if err != nil {
			t.Fatal(err)
		}
		got, err := envelope.HTTPNotification(n, s)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("HTTPNotification = %s, %v\nwant the SQS body %s", got, err, want)
		}
		c := envelope.Confirmation{
			MessageID: n.MessageID, Token: rapid.StringMatching(`[0-9a-f]{1,288}`).Draw(t, "token"), TopicARN: n.TopicARN,
			Timestamp: n.Timestamp, SubscribeURL: rapid.String().Draw(t, "subscribe"),
		}
		want, err = envelope.SQSConfirmation(c, s)
		if err != nil {
			t.Fatal(err)
		}
		got, err = envelope.HTTPConfirmation(c, s)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("HTTPConfirmation = %s, %v\nwant the SQS body %s", got, err, want)
		}
	})
}

// exampleUnsubscribe is the UnsubscribeConfirmation of the format page's
// sample, with its elided Token, SubscribeURL and Signature cut where the
// page cuts them (https://docs.aws.amazon.com/sns/latest/dg/http-unsubscribe-confirmation-json.html).
func exampleUnsubscribe() envelope.UnsubscribeConfirmation {
	return envelope.UnsubscribeConfirmation{ //nolint:gosec // G101: the documented example token, not a credential
		MessageID:       "47138184-6831-46b8-8f7c-afc488602d7d",
		Token:           "2336412f37",
		TopicARN:        exampleTopic,
		SubscriptionARN: exampleTopic + ":2bcfbf39-05c3-41de-beaa-fcfcc21c8f55",
		Timestamp:       time.Date(2012, time.April, 26, 20, 6, 41, 581_000_000, time.UTC),
		SubscribeURL:    "https://sns.us-west-2.amazonaws.com/?Action=ConfirmSubscription&TopicArn=" + exampleTopic + "&Token=2336412f37",
	}
}

// The UnsubscribeConfirmation has the members of the SubscriptionConfirmation
// in the same order and layout, and the documented Message
// (sns-http-delivery.md 4.4, aws-doc and third-party-recorded; the format
// page's sample).
func TestHTTPUnsubscribeConfirmationHasTheDocumentedMembers(t *testing.T) {
	const golden = `{
  "Type" : "UnsubscribeConfirmation",
  "MessageId" : "47138184-6831-46b8-8f7c-afc488602d7d",
  "Token" : "2336412f37",
  "TopicArn" : "arn:aws:sns:us-west-2:123456789012:MyTopic",
  "Message" : "You have chosen to deactivate subscription arn:aws:sns:us-west-2:123456789012:MyTopic:2bcfbf39-05c3-41de-beaa-fcfcc21c8f55.\nTo cancel this operation and restore the subscription, visit the SubscribeURL included in this message.",
  "SubscribeURL" : "https://sns.us-west-2.amazonaws.com/?Action=ConfirmSubscription&TopicArn=arn:aws:sns:us-west-2:123456789012:MyTopic&Token=2336412f37",
  "Timestamp" : "2012-04-26T20:06:41.581Z",
  "SignatureVersion" : "1",
  "Signature" : "RVhBTVBMRQ==",
  "SigningCertURL" : "https://sns.us-west-2.amazonaws.com/SimpleNotificationService-f3ecfb7224c7233fe7bb5f59f96de52f.pem"
}`
	u := exampleUnsubscribe()
	signer := &stubSigner{}
	got, err := envelope.HTTPUnsubscribeConfirmation(u, envelope.Signing{Version: envelope.V1, Signer: signer, CertURL: exampleCertURL})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != golden {
		t.Errorf("UnsubscribeConfirmation:\n%s\nwant:\n%s", got, golden)
	}
	if !bytes.Equal(signer.signed, envelope.UnsubscribeConfirmationStringToSign(u)) || signer.version != envelope.V1 {
		t.Errorf("signed %q with version %s, want UnsubscribeConfirmationStringToSign with version 1", signer.signed, signer.version)
	}
}

// AWS signs an UnsubscribeConfirmation over the confirmation field list,
// Token included: Message, MessageId, SubscribeURL, Timestamp, Token,
// TopicArn, Type, each "name\nvalue\n" (sns-http-delivery.md 5.1, aws-doc
// verification steps; third-party-recorded against real AWS signatures).
// sns-validator 0.3.5 checks it without Token and so rejects genuine ones
// (5.3); the list is never bent to suit it (8.8).
func TestUnsubscribeConfirmationStringToSignIncludesTheToken(t *testing.T) {
	u := exampleUnsubscribe()
	want := "Message\nYou have chosen to deactivate subscription " + u.SubscriptionARN +
		".\nTo cancel this operation and restore the subscription, visit the SubscribeURL included in this message.\n" +
		"MessageId\n47138184-6831-46b8-8f7c-afc488602d7d\n" +
		"SubscribeURL\n" + u.SubscribeURL + "\n" +
		"Timestamp\n2012-04-26T20:06:41.581Z\n" +
		"Token\n2336412f37\n" +
		"TopicArn\n" + exampleTopic + "\n" +
		"Type\nUnsubscribeConfirmation\n"
	if got := string(envelope.UnsubscribeConfirmationStringToSign(u)); got != want {
		t.Errorf("string to sign\n%q\nwant\n%q", got, want)
	}
}

// Under both versions the signature verifies against the key's
// certificate over AWS's field list (5.1, 5.2: version 1 is SHA1withRSA,
// version 2 SHA256withRSA, both PKCS #1 v1.5).
func TestHTTPUnsubscribeConfirmationSignaturesVerify(t *testing.T) {
	pub, signer := newKey(t)
	u := exampleUnsubscribe()
	for _, v := range []envelope.Version{envelope.V1, envelope.V2} {
		body, err := envelope.HTTPUnsubscribeConfirmation(u, envelope.Signing{Version: v, Signer: signer, CertURL: exampleCertURL})
		if err != nil {
			t.Fatal(err)
		}
		var parsed struct{ SignatureVersion, Signature string }
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.SignatureVersion != v.String() {
			t.Errorf("SignatureVersion = %q, want %q", parsed.SignatureVersion, v)
		}
		sig, err := base64.StdEncoding.DecodeString(parsed.Signature)
		if err != nil {
			t.Fatal(err)
		}
		if err := verify(pub, v, envelope.UnsubscribeConfirmationStringToSign(u), sig); err != nil {
			t.Errorf("version %s signature does not verify over the confirmation field list: %v", v, err)
		}
	}
}

// header is one header as HTTPHeaders writes it, for comparison.
type header struct{ name, value string }

func headersOf(hs []delivery.HTTPHeader) []header {
	out := make([]header, 0, len(hs))
	for _, h := range hs {
		out = append(out, header{h.Name, h.Value})
	}
	slices.SortFunc(out, func(a, b header) int { return strings.Compare(a.name, b.name) })
	return out
}

func sorted(hs ...header) []header {
	slices.SortFunc(hs, func(a, b header) int { return strings.Compare(a.name, b.name) })
	return hs
}

const (
	messageID    = "22b80b92-fdea-4c2c-8f9d-bdfb0c7bf324"
	subscription = exampleTopic + ":c9135db0-26c4-47ec-8998-413945fb5a96"
)

// The headers of each message type (sns-http-delivery.md 3.2): the
// x-amz-sns-* names in lower case as the docs print them, the message type,
// the body's MessageId, the topic ARN, the subscription ARN except on a
// SubscriptionConfirmation, x-amz-sns-rawdelivery only on a raw
// notification, Content-Type text/plain; charset=UTF-8 unless a request
// policy sets another (3.3, sent verbatim), User-Agent Amazon Simple
// Notification Service Agent (aws-doc), and Accept-Encoding gzip,deflate
// (third-party-recorded in every request).
func TestHTTPHeadersOfEachMessageType(t *testing.T) {
	common := func(contentType string) []header {
		return []header{
			{"Content-Type", contentType},
			{"User-Agent", "Amazon Simple Notification Service Agent"},
			{"Accept-Encoding", "gzip,deflate"},
		}
	}
	cases := []struct {
		name string
		post envelope.HTTPPost
		want []header
	}{
		{
			"a notification",
			envelope.NotificationPost{MessageID: messageID, TopicARN: exampleTopic, SubscriptionARN: subscription},
			append([]header{
				{"x-amz-sns-message-type", "Notification"},
				{"x-amz-sns-message-id", messageID},
				{"x-amz-sns-topic-arn", exampleTopic},
				{"x-amz-sns-subscription-arn", subscription},
			}, common("text/plain; charset=UTF-8")...),
		},
		{
			"a raw notification with a request policy content type",
			envelope.NotificationPost{MessageID: messageID, TopicARN: exampleTopic, SubscriptionARN: subscription, Raw: true, ContentType: "text/csv"},
			append([]header{
				{"x-amz-sns-message-type", "Notification"},
				{"x-amz-sns-message-id", messageID},
				{"x-amz-sns-topic-arn", exampleTopic},
				{"x-amz-sns-subscription-arn", subscription},
				{"x-amz-sns-rawdelivery", "true"},
			}, common("text/csv")...),
		},
		{
			"an enveloped notification with a topic's application/json",
			envelope.NotificationPost{MessageID: messageID, TopicARN: exampleTopic, SubscriptionARN: subscription, ContentType: "application/json"},
			append([]header{
				{"x-amz-sns-message-type", "Notification"},
				{"x-amz-sns-message-id", messageID},
				{"x-amz-sns-topic-arn", exampleTopic},
				{"x-amz-sns-subscription-arn", subscription},
			}, common("application/json")...),
		},
		{
			"a subscription confirmation",
			envelope.SubscriptionConfirmationPost{MessageID: messageID, TopicARN: exampleTopic},
			append([]header{
				{"x-amz-sns-message-type", "SubscriptionConfirmation"},
				{"x-amz-sns-message-id", messageID},
				{"x-amz-sns-topic-arn", exampleTopic},
			}, common("text/plain; charset=UTF-8")...),
		},
		{
			"an unsubscribe confirmation",
			envelope.UnsubscribeConfirmationPost{MessageID: messageID, TopicARN: exampleTopic, SubscriptionARN: subscription},
			append([]header{
				{"x-amz-sns-message-type", "UnsubscribeConfirmation"},
				{"x-amz-sns-message-id", messageID},
				{"x-amz-sns-topic-arn", exampleTopic},
				{"x-amz-sns-subscription-arn", subscription},
			}, common("text/plain; charset=UTF-8")...),
		},
		// SNSH-03 (recorded 2026-11-03): requestPolicy.headerContentType
		// applies to both confirmations, verbatim.
		{
			"a subscription confirmation with a topic's application/json",
			envelope.SubscriptionConfirmationPost{MessageID: messageID, TopicARN: exampleTopic, ContentType: "application/json"},
			append([]header{
				{"x-amz-sns-message-type", "SubscriptionConfirmation"},
				{"x-amz-sns-message-id", messageID},
				{"x-amz-sns-topic-arn", exampleTopic},
			}, common("application/json")...),
		},
		{
			"an unsubscribe confirmation with a subscription's text/plain",
			envelope.UnsubscribeConfirmationPost{MessageID: messageID, TopicARN: exampleTopic, SubscriptionARN: subscription, ContentType: "text/plain"},
			append([]header{
				{"x-amz-sns-message-type", "UnsubscribeConfirmation"},
				{"x-amz-sns-message-id", messageID},
				{"x-amz-sns-topic-arn", exampleTopic},
				{"x-amz-sns-subscription-arn", subscription},
			}, common("text/plain")...),
		},
	}
	for _, c := range cases {
		got := headersOf(envelope.HTTPHeaders(c.post, nil))
		if want := sorted(c.want...); !slices.Equal(got, want) {
			t.Errorf("%s: headers\n%v\nwant\n%v", c.name, got, want)
		}
	}
	if envelope.DefaultContentType != "text/plain; charset=UTF-8" || envelope.UserAgent != "Amazon Simple Notification Service Agent" {
		t.Errorf("DefaultContentType %q, UserAgent %q", envelope.DefaultContentType, envelope.UserAgent)
	}
}

// No header carries a request's framing (Content-Length, Host, Connection):
// the HTTP client writes those (3.2), and the outbox refuses a delivery
// that sets them.
func TestHTTPHeadersLeaveFramingToTheClient(t *testing.T) {
	for _, p := range []envelope.HTTPPost{
		envelope.NotificationPost{MessageID: messageID, TopicARN: exampleTopic, SubscriptionARN: subscription, Raw: true},
		envelope.SubscriptionConfirmationPost{MessageID: messageID, TopicARN: exampleTopic},
		envelope.UnsubscribeConfirmationPost{MessageID: messageID, TopicARN: exampleTopic, SubscriptionARN: subscription},
	} {
		for _, h := range envelope.HTTPHeaders(p, url.UserPassword("user", "secret")) {
			switch strings.ToLower(h.Name) {
			case "content-length", "host", "connection", "transfer-encoding":
				t.Errorf("%T sets the framing header %s", p, h.Name)
			}
		}
	}
}
