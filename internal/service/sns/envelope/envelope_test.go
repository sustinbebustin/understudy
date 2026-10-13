package envelope_test

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
	"io"
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

// stubSigner returns a fixed signature, so goldens do not depend on a key,
// and records what it was asked to sign.
type stubSigner struct {
	version envelope.Version
	signed  []byte
}

func (s *stubSigner) Sign(v envelope.Version, stringToSign []byte) ([]byte, error) {
	s.version, s.signed = v, stringToSign
	return []byte("EXAMPLE"), nil
}

const (
	exampleTopic   = "arn:aws:sns:us-west-2:123456789012:MyTopic"
	exampleCertURL = "https://sns.us-west-2.amazonaws.com/SimpleNotificationService-f3ecfb7224c7233fe7bb5f59f96de52f.pem"
	exampleUnsub   = "https://sns.us-west-2.amazonaws.com/?Action=Unsubscribe&SubscriptionArn=arn:aws:sns:us-west-2:123456789012:MyTopic:c7fe3a54-ab0e-4ec2-88e0-db410a0f2bee"
)

// example is the notification of sns.md 5.1.
func example() envelope.Notification {
	return envelope.Notification{
		MessageID:      "63a3f6b6-d533-4a47-aef9-fcf5cf758c76",
		TopicARN:       exampleTopic,
		Subject:        "Testing publish to subscribed queues",
		Message:        "Hello world!",
		Timestamp:      time.Date(2012, time.March, 29, 5, 12, 16, 901_000_000, time.UTC),
		Attributes:     []envelope.Attribute{{Name: "attr1", Type: "Number", Value: "99.12"}},
		UnsubscribeURL: exampleUnsub,
	}
}

func TestSQSEnvelopeMatchesTheRecordedLayout(t *testing.T) {
	const golden = `{
  "Type" : "Notification",
  "MessageId" : "63a3f6b6-d533-4a47-aef9-fcf5cf758c76",
  "TopicArn" : "arn:aws:sns:us-west-2:123456789012:MyTopic",
  "Subject" : "Testing publish to subscribed queues",
  "Message" : "Hello world!",
  "Timestamp" : "2012-03-29T05:12:16.901Z",
  "SignatureVersion" : "1",
  "Signature" : "RVhBTVBMRQ==",
  "SigningCertURL" : "https://sns.us-west-2.amazonaws.com/SimpleNotificationService-f3ecfb7224c7233fe7bb5f59f96de52f.pem",
  "UnsubscribeURL" : "https://sns.us-west-2.amazonaws.com/?Action=Unsubscribe&SubscriptionArn=arn:aws:sns:us-west-2:123456789012:MyTopic:c7fe3a54-ab0e-4ec2-88e0-db410a0f2bee",
  "MessageAttributes" : {
    "attr1" : {"Type":"Number","Value":"99.12"}
  }
}`
	signer := &stubSigner{}
	got, err := envelope.SQS(example(), envelope.Signing{Version: envelope.V1, Signer: signer, CertURL: exampleCertURL})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != golden {
		t.Errorf("SQS envelope:\n%s\nwant:\n%s", got, golden)
	}
	wantSigned := "Message\nHello world!\nMessageId\n63a3f6b6-d533-4a47-aef9-fcf5cf758c76\nSubject\nTesting publish to subscribed queues\n" +
		"Timestamp\n2012-03-29T05:12:16.901Z\nTopicArn\narn:aws:sns:us-west-2:123456789012:MyTopic\nType\nNotification\n"
	if string(signer.signed) != wantSigned || signer.version != envelope.V1 {
		t.Errorf("signed %q with version %s, want %q with version 1", signer.signed, signer.version, wantSigned)
	}
}

func TestSQSEnvelopeOmitsAnAbsentSubjectAndAttributes(t *testing.T) {
	const golden = `{
  "Type" : "Notification",
  "MessageId" : "63a3f6b6-d533-4a47-aef9-fcf5cf758c76",
  "TopicArn" : "arn:aws:sns:us-west-2:123456789012:MyTopic",
  "Message" : "Hello world!",
  "Timestamp" : "2012-03-29T05:12:16.901Z",
  "SignatureVersion" : "2",
  "Signature" : "RVhBTVBMRQ==",
  "SigningCertURL" : "https://sns.us-west-2.amazonaws.com/SimpleNotificationService-f3ecfb7224c7233fe7bb5f59f96de52f.pem",
  "UnsubscribeURL" : "https://sns.us-west-2.amazonaws.com/?Action=Unsubscribe&SubscriptionArn=arn:aws:sns:us-west-2:123456789012:MyTopic:c7fe3a54-ab0e-4ec2-88e0-db410a0f2bee"
}`
	n := example()
	n.Subject, n.Attributes = "", nil
	signer := &stubSigner{}
	got, err := envelope.SQS(n, envelope.Signing{Version: envelope.V2, Signer: signer, CertURL: exampleCertURL})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != golden {
		t.Errorf("SQS envelope:\n%s\nwant:\n%s", got, golden)
	}
	if bytes.Contains(signer.signed, []byte("Subject")) || signer.version != envelope.V2 {
		t.Errorf("signed %q with version %s, want no Subject line and version 2", signer.signed, signer.version)
	}
}

func TestFIFOEnvelopeHasASequenceNumberAndNoSignature(t *testing.T) {
	const golden = `{
  "Type" : "Notification",
  "MessageId" : "63a3f6b6-d533-4a47-aef9-fcf5cf758c76",
  "SequenceNumber" : "10000000000000003000",
  "TopicArn" : "arn:aws:sns:us-west-2:123456789012:MyTopic",
  "Subject" : "Testing publish to subscribed queues",
  "Message" : "Hello world!",
  "Timestamp" : "2012-03-29T05:12:16.901Z",
  "UnsubscribeURL" : "https://sns.us-west-2.amazonaws.com/?Action=Unsubscribe&SubscriptionArn=arn:aws:sns:us-west-2:123456789012:MyTopic:c7fe3a54-ab0e-4ec2-88e0-db410a0f2bee",
  "MessageAttributes" : {
    "attr1" : {"Type":"Number","Value":"99.12"}
  }
}`
	if got := envelope.SQSFIFO(example(), "10000000000000003000"); string(got) != golden {
		t.Errorf("FIFO envelope:\n%s\nwant:\n%s", got, golden)
	}
}

func TestSQSConfirmationHasTheDocumentedMembers(t *testing.T) {
	const golden = `{
  "Type" : "SubscriptionConfirmation",
  "MessageId" : "165545c9-2a5c-472c-8df2-7ff2be2b3b1b",
  "Token" : "2336412f37",
  "TopicArn" : "arn:aws:sns:us-west-2:123456789012:MyTopic",
  "Message" : "You have chosen to subscribe to the topic arn:aws:sns:us-west-2:123456789012:MyTopic.\nTo confirm the subscription, visit the SubscribeURL included in this message.",
  "SubscribeURL" : "https://sns.us-west-2.amazonaws.com/?Action=ConfirmSubscription&TopicArn=arn:aws:sns:us-west-2:123456789012:MyTopic&Token=2336412f37",
  "Timestamp" : "2012-04-26T20:45:04.751Z",
  "SignatureVersion" : "1",
  "Signature" : "RVhBTVBMRQ==",
  "SigningCertURL" : "https://sns.us-west-2.amazonaws.com/SimpleNotificationService-f3ecfb7224c7233fe7bb5f59f96de52f.pem"
}`
	c := envelope.Confirmation{ //nolint:gosec // G101: an example confirmation token, not a credential
		MessageID:    "165545c9-2a5c-472c-8df2-7ff2be2b3b1b",
		Token:        "2336412f37",
		TopicARN:     exampleTopic,
		Timestamp:    time.Date(2012, time.April, 26, 20, 45, 4, 751_000_000, time.UTC),
		SubscribeURL: "https://sns.us-west-2.amazonaws.com/?Action=ConfirmSubscription&TopicArn=" + exampleTopic + "&Token=2336412f37",
	}
	signer := &stubSigner{}
	got, err := envelope.SQSConfirmation(c, envelope.Signing{Version: envelope.V1, Signer: signer, CertURL: exampleCertURL})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != golden {
		t.Errorf("SQS confirmation:\n%s\nwant:\n%s", got, golden)
	}
	wantSigned := "Message\nYou have chosen to subscribe to the topic " + exampleTopic +
		".\nTo confirm the subscription, visit the SubscribeURL included in this message.\n" +
		"MessageId\n165545c9-2a5c-472c-8df2-7ff2be2b3b1b\nSubscribeURL\n" + c.SubscribeURL + "\n" +
		"Timestamp\n2012-04-26T20:45:04.751Z\nToken\n2336412f37\nTopicArn\n" + exampleTopic + "\nType\nSubscriptionConfirmation\n"
	if string(signer.signed) != wantSigned {
		t.Errorf("signed %q, want %q", signer.signed, wantSigned)
	}
}

// TestVersionTextRoundTrips checks the persisted form of every Version and
// that any other text is refused.
func TestVersionTextRoundTrips(t *testing.T) {
	for _, v := range []envelope.Version{envelope.V1, envelope.V2} {
		text, err := v.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		var back envelope.Version
		if err := back.UnmarshalText(text); err != nil || back != v {
			t.Errorf("%s round-trips to %s, %v", v, back, err)
		}
	}
	var v envelope.Version
	if err := v.UnmarshalText([]byte("3")); err == nil {
		t.Error(`UnmarshalText accepted "3"`)
	}
	if _, err := envelope.Version(0).MarshalText(); err == nil {
		t.Error("MarshalText accepted the zero Version")
	}
}

func TestLambdaEventMatchesTheRecordedCasing(t *testing.T) {
	const sub = "arn:aws:sns:us-east-1:123456789012:sns-lambda:21be56ed-a058-49f5-8c98-aedd2564c486"
	n := envelope.Notification{
		MessageID:      "95df01b4-ee98-5cb9-9903-4c221d41eb5e",
		TopicARN:       "arn:aws:sns:us-east-1:123456789012:sns-lambda",
		Subject:        "TestInvoke",
		Message:        "Hello from SNS!",
		Timestamp:      time.Date(2019, time.January, 2, 12, 45, 7, 0, time.UTC),
		Attributes:     []envelope.Attribute{{Name: "Test", Type: "String", Value: "TestString"}, {Name: "TestBinary", Type: "Binary", Value: "VGVzdEJpbmFyeQ=="}},
		UnsubscribeURL: "https://sns.us-east-1.amazonaws.com/?Action=Unsubscribe&SubscriptionArn=" + sub,
	}
	const golden = `{"Records":[{"EventSource":"aws:sns","EventVersion":"1.0","EventSubscriptionArn":"arn:aws:sns:us-east-1:123456789012:sns-lambda:21be56ed-a058-49f5-8c98-aedd2564c486",` +
		`"Sns":{"Type":"Notification","MessageId":"95df01b4-ee98-5cb9-9903-4c221d41eb5e","TopicArn":"arn:aws:sns:us-east-1:123456789012:sns-lambda","Subject":"TestInvoke",` +
		`"Message":"Hello from SNS!","Timestamp":"2019-01-02T12:45:07.000Z","SignatureVersion":"1","Signature":"RVhBTVBMRQ==",` +
		`"SigningCertUrl":"https://sns.us-east-1.amazonaws.com/SimpleNotificationService-ac565b8b1a6c5d002d285f9598aa1d9b.pem",` +
		`"UnsubscribeUrl":"https://sns.us-east-1.amazonaws.com/?Action=Unsubscribe&SubscriptionArn=arn:aws:sns:us-east-1:123456789012:sns-lambda:21be56ed-a058-49f5-8c98-aedd2564c486",` +
		`"MessageAttributes":{"Test":{"Type":"String","Value":"TestString"},"TestBinary":{"Type":"Binary","Value":"VGVzdEJpbmFyeQ=="}}}}]}`
	signer := &stubSigner{}
	got, err := envelope.Lambda(n, sub, envelope.Signing{
		Version: envelope.V1,
		Signer:  signer,
		CertURL: "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-ac565b8b1a6c5d002d285f9598aa1d9b.pem",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != golden {
		t.Errorf("Lambda event:\n%s\nwant:\n%s", got, golden)
	}
	if signer.version != envelope.V1 {
		t.Errorf("Lambda signature version = %s, want 1", signer.version)
	}
}

// TestLambdaEventOfAVersion2TopicIsLabelledOneAndSignedWithSHA256 checks
// AWS's behavior for a topic with SignatureVersion 2: the event says "1"
// but its signature verifies only as SHA256withRSA (sns.md 5.4).
func TestLambdaEventOfAVersion2TopicIsLabelledOneAndSignedWithSHA256(t *testing.T) {
	pub, signer := newKey(t)
	n := example()
	body, err := envelope.Lambda(n, exampleTopic+":sub", envelope.Signing{Version: envelope.V2, Signer: signer, CertURL: exampleCertURL})
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Records []struct {
			Sns struct{ SignatureVersion, Signature string }
		}
	}
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatal(err)
	}
	if len(event.Records) != 1 {
		t.Fatalf("Lambda event %s: want one record", body)
	}
	got := event.Records[0].Sns
	if got.SignatureVersion != "1" {
		t.Errorf("SignatureVersion = %q, want \"1\"", got.SignatureVersion)
	}
	sig, err := base64.StdEncoding.DecodeString(got.Signature)
	if err != nil {
		t.Fatal(err)
	}
	if err := verify(pub, envelope.V2, envelope.StringToSign(n), sig); err != nil {
		t.Errorf("signature does not verify as SHA256withRSA: %v", err)
	}
}

func TestLambdaEventWithoutSubjectOrAttributes(t *testing.T) {
	n := example()
	n.Subject, n.Attributes = "", nil
	got, err := envelope.Lambda(n, exampleTopic+":sub", envelope.Signing{Version: envelope.V1, Signer: &stubSigner{}, CertURL: exampleCertURL})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"Subject":null,`)) || !bytes.HasSuffix(got, []byte(`"MessageAttributes":{}}}]}`)) {
		t.Errorf("Lambda event %s: want Subject null and MessageAttributes {}", got)
	}
}

// newKey makes a signing key and returns the public key of its certificate
// and its Signer.
func newKey(t *testing.T) (*rsa.PublicKey, envelope.Signer) {
	t.Helper()
	key, err := envelope.NewKey("understudy SNS 000000000000 us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(key.CertificatePEM())
	if block == nil {
		t.Fatal("CertificatePEM is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("certificate key is %T, want RSA", cert.PublicKey)
	}
	signer, err := key.Signer()
	if err != nil {
		t.Fatal(err)
	}
	return pub, signer
}

func TestSignaturesVerifyAgainstTheCertificate(t *testing.T) {
	pub, signer := newKey(t)
	n := example()
	for _, v := range []envelope.Version{envelope.V1, envelope.V2} {
		body, err := envelope.SQS(n, envelope.Signing{Version: v, Signer: signer, CertURL: exampleCertURL})
		if err != nil {
			t.Fatal(err)
		}
		var parsed struct{ Signature string }
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatal(err)
		}
		sig, err := base64.StdEncoding.DecodeString(parsed.Signature)
		if err != nil {
			t.Fatal(err)
		}
		if err := verify(pub, v, envelope.StringToSign(n), sig); err != nil {
			t.Errorf("version %s signature does not verify: %v", v, err)
		}
	}
}

func verify(pub *rsa.PublicKey, v envelope.Version, signed, sig []byte) error {
	if v == envelope.V1 {
		sum := sha1.Sum(signed) //nolint:gosec // SignatureVersion 1
		return rsa.VerifyPKCS1v15(pub, crypto.SHA1, sum[:], sig)
	}
	sum := sha256.Sum256(signed)
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig)
}

// TestSQSEnvelopeRoundTrips checks over random notifications that the SQS
// and FIFO bodies parse as JSON with the documented members in order and
// the notification's values.
func TestSQSEnvelopeRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := envelope.Notification{
			MessageID:      rapid.StringMatching(`[0-9a-f-]{1,36}`).Draw(t, "id"),
			TopicARN:       rapid.String().Draw(t, "topic"),
			Subject:        rapid.String().Draw(t, "subject"),
			Message:        rapid.StringN(1, 64, -1).Draw(t, "message"),
			Timestamp:      time.UnixMilli(rapid.Int64Range(0, 1<<42).Draw(t, "ms")),
			UnsubscribeURL: rapid.String().Draw(t, "unsubscribe"),
		}
		for i := range rapid.IntRange(0, 3).Draw(t, "attrs") {
			n.Attributes = append(n.Attributes, envelope.Attribute{
				Name:  "a" + string(rune('0'+i)),
				Type:  rapid.SampledFrom([]string{"String", "Number", "Binary", "String.Array"}).Draw(t, "type"),
				Value: rapid.String().Draw(t, "value"),
			})
		}
		body, err := envelope.SQS(n, envelope.Signing{Version: envelope.V1, Signer: &stubSigner{}, CertURL: "c"})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"Type", "MessageId", "TopicArn", "Subject", "Message", "Timestamp", "SignatureVersion", "Signature", "SigningCertURL", "UnsubscribeURL", "MessageAttributes"}
		checkMembers(t, body, n, want)
		fifo := envelope.SQSFIFO(n, "1")
		checkMembers(t, fifo, n, []string{"Type", "MessageId", "SequenceNumber", "TopicArn", "Subject", "Message", "Timestamp", "UnsubscribeURL", "MessageAttributes"})
	})
}

func checkMembers(t *rapid.T, body []byte, n envelope.Notification, want []string) {
	var names []string
	dec := json.NewDecoder(bytes.NewReader(body))
	if _, err := dec.Token(); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		name, ok := tok.(string)
		if !ok {
			t.Fatalf("%s: member name %v", body, tok)
		}
		names = append(names, name)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
	}
	if _, err := dec.Token(); err != nil && err != io.EOF {
		t.Fatalf("%s: %v", body, err)
	}
	var expected []string
	for _, name := range want {
		if (name == "Subject" && n.Subject == "") || (name == "MessageAttributes" && len(n.Attributes) == 0) {
			continue
		}
		expected = append(expected, name)
	}
	if !slices.Equal(names, expected) {
		t.Fatalf("members %v, want %v", names, expected)
	}
	var parsed struct {
		MessageID         string `json:"MessageId"`
		TopicArn          string
		Subject           string
		Message           string
		Timestamp         string
		UnsubscribeURL    string
		MessageAttributes map[string]struct{ Type, Value string }
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.MessageID != n.MessageID || parsed.TopicArn != n.TopicARN || parsed.Subject != n.Subject ||
		parsed.Message != n.Message || parsed.UnsubscribeURL != n.UnsubscribeURL || parsed.Timestamp != envelope.Timestamp(n.Timestamp) {
		t.Fatalf("parsed %+v from %s, want the values of %+v", parsed, body, n)
	}
	for _, a := range n.Attributes {
		if got := parsed.MessageAttributes[a.Name]; got.Type != a.Type || got.Value != a.Value {
			t.Fatalf("attribute %s = %+v, want %+v", a.Name, got, a)
		}
	}
}
