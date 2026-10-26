package envelope_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

// HTTPHeaders writes the headers in the order of the 3.2 table, the
// x-amz-sns-* names first, and Authorization last when the endpoint URL
// carries credentials (sns-http-delivery.md 3.2, 2.1, sns.http.basic-auth).
func TestHTTPHeadersOrderAndBasicCredentials(t *testing.T) {
	got := envelope.HTTPHeaders(
		envelope.NotificationPost{MessageID: messageID, TopicARN: exampleTopic, SubscriptionARN: subscription, Raw: true, ContentType: "text/csv"},
		url.UserPassword("alice", "s3cret"),
	)
	want := []delivery.HTTPHeader{
		{Name: "x-amz-sns-message-type", Value: "Notification"},
		{Name: "x-amz-sns-message-id", Value: messageID},
		{Name: "x-amz-sns-topic-arn", Value: exampleTopic},
		{Name: "x-amz-sns-subscription-arn", Value: subscription},
		{Name: "x-amz-sns-rawdelivery", Value: "true"},
		{Name: "Content-Type", Value: "text/csv"},
		{Name: "User-Agent", Value: "Amazon Simple Notification Service Agent"},
		{Name: "Accept-Encoding", Value: "gzip,deflate"},
		{Name: "Authorization", Value: "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))},
	}
	if !slices.Equal(got, want) {
		t.Errorf("headers\n%v\nwant\n%v", got, want)
	}
}

// The credentials are the URL's user information decoded, as an HTTP
// client sends Basic credentials from a URL: a user without a password
// sends "user:", and percent-escapes are decoded (sns-http-delivery.md 2.1).
func TestHTTPHeadersBasicCredentialsFromTheURL(t *testing.T) {
	cases := []struct{ endpoint, credentials string }{
		{"http://alice:s3cret@127.0.0.1:8080/hook", "alice:s3cret"},
		{"http://bob@127.0.0.1:8080/hook", "bob:"},
		{"https://al%40ice:p%3Ass@example.test/hook", "al@ice:p:ss"},
		{"https://alice:@example.test/hook", "alice:"},
	}
	for _, c := range cases {
		u, err := url.Parse(c.endpoint)
		if err != nil {
			t.Fatal(err)
		}
		var auth []string
		for _, h := range envelope.HTTPHeaders(envelope.SubscriptionConfirmationPost{MessageID: messageID, TopicARN: exampleTopic}, u.User) {
			if h.Name == "Authorization" {
				auth = append(auth, h.Value)
			}
		}
		if want := []string{"Basic " + base64.StdEncoding.EncodeToString([]byte(c.credentials))}; !slices.Equal(auth, want) {
			t.Errorf("%s: Authorization %q, want %q", c.endpoint, auth, want)
		}
	}
	for _, p := range []envelope.HTTPPost{
		envelope.NotificationPost{MessageID: messageID, TopicARN: exampleTopic, SubscriptionARN: subscription},
		envelope.SubscriptionConfirmationPost{MessageID: messageID, TopicARN: exampleTopic},
		envelope.UnsubscribeConfirmationPost{MessageID: messageID, TopicARN: exampleTopic, SubscriptionARN: subscription},
	} {
		for _, h := range envelope.HTTPHeaders(p, nil) {
			if h.Name == "Authorization" {
				t.Errorf("%T without credentials sends Authorization %q", p, h.Value)
			}
		}
	}
	if hs := envelope.HTTPHeaders(nil, nil); hs != nil {
		t.Errorf("HTTPHeaders(nil) = %v, want none", hs)
	}
}

// A verifier unescapes the body's values before it builds the string to
// sign (sns-http-delivery.md 4.1), so over random values the
// UnsubscribeConfirmation parses as JSON with the members of 4.3 in order,
// and the 5.1 field list rebuilt from the parsed values is what was signed.
func TestHTTPUnsubscribeConfirmationRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		u := envelope.UnsubscribeConfirmation{
			MessageID:       rapid.StringMatching(`[0-9a-f-]{1,36}`).Draw(t, "id"),
			Token:           rapid.StringMatching(`[0-9a-f]{1,288}`).Draw(t, "token"),
			TopicARN:        rapid.String().Draw(t, "topic"),
			SubscriptionARN: rapid.String().Draw(t, "subscription"),
			Timestamp:       time.UnixMilli(rapid.Int64Range(0, 1<<42).Draw(t, "ms")),
			SubscribeURL:    rapid.String().Draw(t, "subscribe"),
		}
		version := rapid.SampledFrom([]envelope.Version{envelope.V1, envelope.V2}).Draw(t, "version")
		signer := &stubSigner{}
		body, err := envelope.HTTPUnsubscribeConfirmation(u, envelope.Signing{Version: version, Signer: signer, CertURL: rapid.String().Draw(t, "cert")})
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(body))
		if _, err := dec.Token(); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		var names []string
		values := map[string]string{}
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				t.Fatalf("%s: %v", body, err)
			}
			name, ok := tok.(string)
			if !ok {
				t.Fatalf("%s: member name %v", body, tok)
			}
			var value string
			if err := dec.Decode(&value); err != nil {
				t.Fatalf("%s: member %s: %v", body, name, err)
			}
			names = append(names, name)
			values[name] = value
		}
		want := []string{"Type", "MessageId", "Token", "TopicArn", "Message", "SubscribeURL", "Timestamp", "SignatureVersion", "Signature", "SigningCertURL"}
		if !slices.Equal(names, want) {
			t.Fatalf("members %v, want %v", names, want)
		}
		if values["Type"] != "UnsubscribeConfirmation" || values["MessageId"] != u.MessageID || values["Token"] != u.Token ||
			values["TopicArn"] != u.TopicARN || values["SubscribeURL"] != u.SubscribeURL || values["SignatureVersion"] != version.String() {
			t.Errorf("body %s does not carry the confirmation's values", body)
		}
		var rebuilt []byte
		for _, name := range []string{"Message", "MessageId", "SubscribeURL", "Timestamp", "Token", "TopicArn", "Type"} {
			rebuilt = append(rebuilt, name+"\n"+values[name]+"\n"...)
		}
		if !bytes.Equal(rebuilt, signer.signed) || !bytes.Equal(signer.signed, envelope.UnsubscribeConfirmationStringToSign(u)) || signer.version != version {
			t.Errorf("signed %q with version %s; the parsed body gives %q", signer.signed, signer.version, rebuilt)
		}
	})
}

// failingSigner fails every signature.
type failingSigner struct{ err error }

func (s failingSigner) Sign(envelope.Version, []byte) ([]byte, error) { return nil, s.err }

// A signer's failure is the builder's: no body goes out unsigned.
func TestHTTPBuildersReturnTheSignerError(t *testing.T) {
	cause := errors.New("signer failed")
	s := envelope.Signing{Version: envelope.V1, Signer: failingSigner{err: cause}, CertURL: exampleCertURL}
	if body, err := envelope.HTTPNotification(example(), s); !errors.Is(err, cause) || body != nil {
		t.Errorf("HTTPNotification = %q, %v; want no body and the signer's error", body, err)
	}
	if body, err := envelope.HTTPConfirmation(envelope.Confirmation{MessageID: messageID, Token: "t", TopicARN: exampleTopic}, s); !errors.Is(err, cause) || body != nil {
		t.Errorf("HTTPConfirmation = %q, %v; want no body and the signer's error", body, err)
	}
	if body, err := envelope.HTTPUnsubscribeConfirmation(exampleUnsubscribe(), s); !errors.Is(err, cause) || body != nil {
		t.Errorf("HTTPUnsubscribeConfirmation = %q, %v; want no body and the signer's error", body, err)
	}
}
