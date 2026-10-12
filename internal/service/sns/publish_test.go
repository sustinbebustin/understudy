package sns_test

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // verifying SignatureVersion 1, which is SHA1withRSA
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

// sqsEnvelope is the SQS body of an enveloped notification.
type sqsEnvelope struct {
	Type              string
	MessageID         string `json:"MessageId"`
	SequenceNumber    string
	TopicArn          string
	Subject           string
	Message           string
	Timestamp         string
	SignatureVersion  string
	Signature         string
	SigningCertURL    string
	UnsubscribeURL    string
	MessageAttributes map[string]struct{ Type, Value string }
}

func attr(dataType, value string) snsapi.MessageAttributeValue {
	return snsapi.MessageAttributeValue{DataType: dataType, StringValue: &value}
}

// TestPublishFansOutInSubscriptionOrder publishes to a topic with three
// subscriptions and checks the outbox entries and what each stub target
// received (ARCHITECTURE.md 16; sns.md 5.1, 5.2, 5.4).
func TestPublishFansOutInSubscriptionOrder(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		h.subscribe(topic, "sqs", queueARN, map[string]string{"RawMessageDelivery": "true"})
		wrapped := h.subscribe(topic, "sqs", "arn:aws:sqs:us-east-1:000000000000:audit", nil)
		fn := h.subscribe(topic, "lambda", functionARN+":live", nil)
		out := must(h, snsapi.OpPublish, &snsapi.PublishInput{
			TopicArn: &topic, Message: "hello", Subject: new("greeting"),
			MessageAttributes: map[string]snsapi.MessageAttributeValue{
				"store": attr("String", "north"),
				"price": attr("Number", "99.12"),
				"blob":  {DataType: "Binary", BinaryValue: []byte{2, 3, 4}},
			},
		})
		if out.MessageId == nil || out.SequenceNumber != nil {
			h.Fatalf("Publish = %+v, want a MessageId and no SequenceNumber", out)
		}

		got := h.sink.all()
		if len(got) != 3 {
			h.Fatalf("outbox took %d deliveries, want 3", len(got))
		}
		wantTargets := []string{queueARN, "arn:aws:sqs:us-east-1:000000000000:audit", functionARN}
		for i, d := range got {
			var target string
			switch tg := d.Request.Target.(type) {
			case delivery.SQSQueue:
				target = tg.ARN.String()
			case delivery.LambdaFunction:
				target = tg.ARN.String()
				if tg.Qualifier != "live" || tg.Invocation != delivery.InvocationEvent {
					h.Errorf("lambda target %+v, want qualifier live and an Event invocation", tg)
				}
			case delivery.SNSTopic:
				target = tg.ARN.String()
			}
			if target != wantTargets[i] || d.Request.Source.String() != topic {
				h.Errorf("delivery %d goes %s -> %s, want %s -> %s", i, d.Request.Source, target, topic, wantTargets[i])
			}
		}

		h.settle()
		sent := h.sent.all()
		if len(sent) != 2 {
			h.Fatalf("SendMessage ran %d times, want 2", len(sent))
		}
		for _, in := range sent {
			if strings.HasSuffix(in.QueueUrl, "/orders") {
				checkRaw(h, in)
			} else {
				checkEnvelope(h, in, topic, wrapped, *out.MessageId)
			}
		}
		invoked := h.invoked.all()
		if len(invoked) != 1 {
			h.Fatalf("Invoke ran %d times, want 1", len(invoked))
		}
		var event struct {
			Records []struct {
				EventSubscriptionArn string
				Sns                  struct {
					MessageID         string `json:"MessageId"`
					Message           string
					Subject           string
					SigningCertURL    string `json:"SigningCertUrl"`
					MessageAttributes map[string]struct{ Type, Value string }
				}
			}
		}
		if err := json.Unmarshal(invoked[0].Payload, &event); err != nil {
			h.Fatal(err)
		}
		rec := event.Records[0]
		if rec.EventSubscriptionArn != fn || rec.Sns.MessageID != *out.MessageId || rec.Sns.Message != "hello" || rec.Sns.Subject != "greeting" ||
			rec.Sns.MessageAttributes["blob"].Value != "AgME" || rec.Sns.SigningCertURL == "" {
			h.Errorf("lambda event = %s", invoked[0].Payload)
		}
	})
}

// checkRaw checks a raw delivery: the body is the message and the
// attributes are SQS message attributes of the same types (sns.md 5.2).
func checkRaw(h *harness, in sqsapi.SendMessageInput) {
	h.Helper()
	if in.MessageBody != "hello" {
		h.Errorf("raw body = %q, want the message", in.MessageBody)
	}
	if a := in.MessageAttributes["price"]; a.DataType != "Number" || a.StringValue == nil || *a.StringValue != "99.12" {
		h.Errorf("raw price attribute = %+v", a)
	}
	if a := in.MessageAttributes["blob"]; a.DataType != "Binary" || string(a.BinaryValue) != "\x02\x03\x04" {
		h.Errorf("raw blob attribute = %+v", a)
	}
}

// checkEnvelope checks an enveloped delivery and that its signature
// verifies against the certificate served at SigningCertURL (sns.md 5.1,
// 5.6).
func checkEnvelope(h *harness, in sqsapi.SendMessageInput, topic, subscription, messageID string) {
	h.Helper()
	if len(in.MessageAttributes) != 0 {
		h.Errorf("enveloped delivery carries SQS attributes %v", in.MessageAttributes)
	}
	var env sqsEnvelope
	if err := json.Unmarshal([]byte(in.MessageBody), &env); err != nil {
		h.Fatalf("envelope %s: %v", in.MessageBody, err)
	}
	if env.Type != "Notification" || env.MessageID != messageID || env.TopicArn != topic || env.Message != "hello" || env.Subject != "greeting" ||
		env.SignatureVersion != "1" || env.MessageAttributes["store"].Value != "north" || env.MessageAttributes["blob"].Value != "AgME" {
		h.Errorf("envelope = %s", in.MessageBody)
	}
	if want := "http://localhost:4566/_understudy/sns/UnsubscribeLink?Action=Unsubscribe&SubscriptionArn=" + subscription; env.UnsubscribeURL != want {
		h.Errorf("UnsubscribeURL = %q, want %q", env.UnsubscribeURL, want)
	}
	cert := servedCertificate(h, env.SigningCertURL)
	ts, err := time.Parse("2006-01-02T15:04:05.000Z", env.Timestamp)
	if err != nil {
		h.Fatal(err)
	}
	signed := envelope.StringToSign(envelope.Notification{MessageID: env.MessageID, TopicARN: env.TopicArn, Subject: env.Subject, Message: env.Message, Timestamp: ts})
	sig, err := base64.StdEncoding.DecodeString(env.Signature)
	if err != nil {
		h.Fatal(err)
	}
	sum := sha1.Sum(signed) //nolint:gosec // SignatureVersion 1
	if err := rsa.VerifyPKCS1v15(cert, crypto.SHA1, sum[:], sig); err != nil {
		h.Errorf("the envelope signature does not verify against the served certificate: %v", err)
	}
}

// servedCertificate fetches SigningCertURL through the SigningCert link
// and returns its RSA key.
func servedCertificate(h *harness, certURL string) *rsa.PublicKey {
	h.Helper()
	u, err := url.Parse(certURL)
	if err != nil || u.Host != "localhost:4566" {
		h.Fatalf("SigningCertURL %q does not point at the emulator", certURL)
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
		h.Fatalf("certificate key is %T", cert.PublicKey)
	}
	return pub
}

func TestSignatureVersion2AndUnknownCertificate(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", map[string]string{"SignatureVersion": "2"})
		if got := h.topicAttributes(topic)["SignatureVersion"]; got != "2" {
			h.Errorf("SignatureVersion = %q, want 2", got)
		}
		h.subscribe(topic, "sqs", queueARN, nil)
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
		h.settle()
		var env sqsEnvelope
		if err := json.Unmarshal([]byte(h.sent.all()[0].MessageBody), &env); err != nil {
			h.Fatal(err)
		}
		ts, err := time.Parse("2006-01-02T15:04:05.000Z", env.Timestamp)
		if err != nil {
			h.Fatal(err)
		}
		sig, err := base64.StdEncoding.DecodeString(env.Signature)
		if err != nil {
			h.Fatal(err)
		}
		sum := sha256.Sum256(envelope.StringToSign(envelope.Notification{MessageID: env.MessageID, TopicARN: env.TopicArn, Message: env.Message, Timestamp: ts}))
		if err := rsa.VerifyPKCS1v15(servedCertificate(h, env.SigningCertURL), crypto.SHA256, sum[:], sig); err != nil || env.SignatureVersion != "2" {
			h.Errorf("version %s signature: %v", env.SignatureVersion, err)
		}
		_, aerr := h.link("SigningCert", "/_understudy/sns/SigningCert/aws/us-east-1/000000000000/SimpleNotificationService-0.pem")
		wantError(h, aerr, "NotFound", "Certificate does not exist; SigningCertURL paths come from notifications this emulator signed")
	})
}

func TestFIFOTopicDeliversWithGroupAndDeduplicates(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events.fifo", map[string]string{"FifoTopic": "true", "ContentBasedDeduplication": "true"})
		h.subscribe(topic, "sqs", fifoQueueARN, nil)
		h.subscribe(topic, "sqs", queueARN, map[string]string{"RawMessageDelivery": "true"})
		first := must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "Test", MessageGroupId: new("g1")})
		if first.SequenceNumber == nil || len(*first.SequenceNumber) != 20 {
			h.Fatalf("SequenceNumber = %v, want 20 digits", first.SequenceNumber)
		}
		// The same body in another group is a duplicate under the default
		// topic-wide scope (sns.md 4.3).
		dup := must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "Test", MessageGroupId: new("g2")})
		if *dup.SequenceNumber <= *first.SequenceNumber {
			h.Errorf("sequence %s after %s", *dup.SequenceNumber, *first.SequenceNumber)
		}
		h.settle()
		sent := h.sent.all()
		if len(sent) != 2 {
			h.Fatalf("SendMessage ran %d times, want 2 (one per subscription, duplicate dropped)", len(sent))
		}
		for _, in := range sent {
			if strings.HasSuffix(in.QueueUrl, ".fifo") {
				wantDedup := sha256.Sum256([]byte("Test"))
				if in.MessageGroupId == nil || *in.MessageGroupId != "g1" || in.MessageDeduplicationId == nil || *in.MessageDeduplicationId != hex.EncodeToString(wantDedup[:]) {
					h.Errorf("FIFO queue send = %+v", in)
				}
				var env sqsEnvelope
				if err := json.Unmarshal([]byte(in.MessageBody), &env); err != nil {
					h.Fatal(err)
				}
				if env.SequenceNumber != *first.SequenceNumber || env.Signature != "" || env.SigningCertURL != "" {
					h.Errorf("FIFO envelope = %s", in.MessageBody)
				}
			} else if in.MessageGroupId != nil || in.MessageBody != "Test" {
				h.Errorf("standard queue under a FIFO topic got %+v, want the raw body and no group", in)
			}
		}
		h.advance(5 * time.Minute)
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "Test", MessageGroupId: new("g1")})
		h.settle()
		if n := len(h.sent.all()); n != 4 {
			h.Errorf("after the window SendMessage ran %d times in all, want 4", n)
		}
	})
}

func TestStandardTopicForwardsGroupToStandardQueue(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		h.subscribe(topic, "sqs", queueARN, map[string]string{"RawMessageDelivery": "true"})
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m", MessageGroupId: new("tenant-1")})
		h.settle()
		if in := h.sent.all()[0]; in.MessageGroupId == nil || *in.MessageGroupId != "tenant-1" {
			h.Errorf("fair-queue send = %+v, want MessageGroupId tenant-1 (sns.md 4.3)", in)
		}
	})
}

func TestMessageStructureJSONPicksTheProtocolEntry(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		h.subscribe(topic, "sqs", queueARN, map[string]string{"RawMessageDelivery": "true"})
		h.subscribe(topic, "lambda", functionARN, nil)
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, MessageStructure: new("json"), Message: `{"default":"d","sqs":"for sqs","lambda":7}`})
		h.settle()
		if body := h.sent.all()[0].MessageBody; body != "for sqs" {
			h.Errorf("sqs body = %q", body)
		}
		if payload := string(h.invoked.all()[0].Payload); !strings.Contains(payload, `"Message":"d"`) {
			h.Errorf("lambda event %s, want the default message: a non-string entry is ignored", payload)
		}
		for body, msg := range map[string]string{
			"not json":            "Invalid parameter: Message Structure - JSON message body failed to parse",
			`{"sqs":"x"}`:         "Invalid parameter: Message Structure - No default entry in JSON message body",
			`{"default":{"a":1}}`: "Invalid parameter: Message Structure - No default entry in JSON message body",
		} {
			_, err := call(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, MessageStructure: new("json"), Message: body})
			wantError(h, err, "InvalidParameter", msg)
		}
	})
}

func TestPendingAndHTTPSubscriptionsReceiveNothing(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		h.subscribe(topic, "https", "https://127.0.0.1:9443/hook", nil)
		h.subscribe(topic, "email", "a@example.com", nil)
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
		if n := len(h.sink.all()); n != 0 {
			h.Errorf("outbox took %d deliveries for pending subscriptions, want 0", n)
		}
	})
}

func TestPublishInputRules(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", map[string]string{"MaximumMessageSize": "1024"})
		fifo := h.topic("events.fifo", map[string]string{"FifoTopic": "true"})
		cases := []struct {
			name          string
			in            snsapi.PublishInput
			code, message string
		}{
			{"empty message", snsapi.PublishInput{TopicArn: &topic}, "InvalidParameter", "Invalid parameter: Empty message"},
			{"empty subject", snsapi.PublishInput{TopicArn: &topic, Message: "m", Subject: new("")}, "InvalidParameter", "Invalid parameter: Subject"},
			{"too long", snsapi.PublishInput{TopicArn: &topic, Message: strings.Repeat("x", 1017), MessageAttributes: map[string]snsapi.MessageAttributeValue{"a": attr("String", "b")}}, "InvalidParameter", "Invalid parameter: Message too long"},
			// The limit counts Message as sent: here only the sqs entry
			// makes it too long, and the default alone would fit.
			{"too long as json", snsapi.PublishInput{TopicArn: &topic, MessageStructure: new("json"), Message: `{"default":"d","sqs":"` + strings.Repeat("x", 1010) + `"}`}, "InvalidParameter", "Invalid parameter: Message too long"},
			{"group required", snsapi.PublishInput{TopicArn: &fifo, Message: "m", MessageDeduplicationId: new("d")}, "InvalidParameter", "Invalid parameter: The MessageGroupId parameter is required for FIFO topics"},
			{"dedup required", snsapi.PublishInput{TopicArn: &fifo, Message: "m", MessageGroupId: new("g")}, "InvalidParameter", "Invalid parameter: The topic should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly"},
			{"dedup on standard", snsapi.PublishInput{TopicArn: &topic, Message: "m", MessageDeduplicationId: new("d")}, "InvalidParameter", "Invalid parameter: MessageDeduplicationId Reason: The request includes MessageDeduplicationId parameter that is not valid for this topic type"},
			{"phone", snsapi.PublishInput{PhoneNumber: new("+15551234567"), Message: "m"}, "NotImplemented", "understudy does not implement publishing to phone numbers; publish to a topic ARN instead. Run 'understudy coverage --service sns' for what is supported."},
			{"missing topic", snsapi.PublishInput{TopicArn: new("arn:aws:sns:us-east-1:000000000000:missing"), Message: "m"}, "NotFound", "Topic does not exist"},
		}
		for _, tc := range cases {
			_, err := call(h, snsapi.OpPublish, &tc.in)
			if err == nil {
				h.Errorf("%s: no error", tc.name)
				continue
			}
			wantError(h, err, tc.code, tc.message)
		}
		// Exactly at the limit is accepted: 1016 + len("a") + len("String") + len("b") = 1024.
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: strings.Repeat("x", 1016), MessageAttributes: map[string]snsapi.MessageAttributeValue{"a": attr("String", "b")}})
		// TargetArn names a topic too.
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TargetArn: &topic, Message: "m"})
	})
}

func TestMessageAttributeErrors(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		for _, tc := range []struct {
			name    string
			value   snsapi.MessageAttributeValue
			message string
		}{
			{"attr1", attr("String", ""), "The message attribute 'attr1' must contain non-empty message attribute value for message attribute type 'String'."},
			{"attr1", snsapi.MessageAttributeValue{DataType: "Binary", BinaryValue: []byte{}}, "The message attribute 'attr1' must contain non-empty message attribute value for message attribute type 'Binary'."},
			{"attr1", snsapi.MessageAttributeValue{DataType: "String", BinaryValue: []byte{1}}, "The message attribute 'attr1' with type 'String' must use field 'String'."},
			{"attr1", snsapi.MessageAttributeValue{DataType: "Number", BinaryValue: []byte{1}}, "The message attribute 'attr1' with type 'Number' must use field 'String'."},
			{"attr1", attr("Binary", "x"), "The message attribute 'attr1' with type 'Binary' must use field 'Binary'."},
			{"attr1", attr("Integer", "1"), "The message attribute 'attr1' has an invalid message attribute type, the set of supported type prefixes is Binary, Number, and String."},
			{strings.Repeat("a", 257), attr("String", "v"), "Length of message attribute name must be less than 256 bytes."},
			{"at^tr", attr("String", "v"), "Invalid non-alphanumeric character '#x5E' was found in the message attribute name. Can only include alphanumeric characters, hyphens, underscores, or dots."},
			{".attr", attr("String", "v"), "Invalid message attribute name starting with character '.' was found."},
			{"attr.", attr("String", "v"), "Invalid message attribute name ending with character '.' was found."},
			{"at..tr", attr("String", "v"), "Message attribute name can not have successive '.' character."},
			{"list", attr("String.Array", "red"), "The message attribute 'list' with type 'String.Array' has a value that is not a JSON array of strings, numbers, true, false or null: red"},
			{"list", attr("String.Array", `{"a":1}`), `The message attribute 'list' with type 'String.Array' has a value that is not a JSON array of strings, numbers, true, false or null: {"a":1}`},
			{"list", attr("String.Array", `["a",["b"]]`), `The message attribute 'list' with type 'String.Array' has a value that is not a JSON array of strings, numbers, true, false or null: ["a",["b"]]`},
		} {
			_, err := call(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m", MessageAttributes: map[string]snsapi.MessageAttributeValue{tc.name: tc.value}})
			wantError(h, err, "ParameterValueInvalid", tc.message)
			_, err = call(h, snsapi.OpPublishBatch, &snsapi.PublishBatchInput{TopicArn: topic, PublishBatchRequestEntries: []snsapi.PublishBatchRequestEntry{
				{Id: "1", Message: "m", MessageAttributes: map[string]snsapi.MessageAttributeValue{tc.name: tc.value}},
			}})
			wantError(h, err, "ParameterValueInvalid", tc.message)
		}
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m", MessageAttributes: map[string]snsapi.MessageAttributeValue{
			"list": attr("String.Array", `["a", 1, -2.5e3, true, false, null]`),
		}})
	})
}

func TestPublishBatch(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events.fifo", map[string]string{"FifoTopic": "true"})
		h.subscribe(topic, "sqs", fifoQueueARN, nil)
		out := must(h, snsapi.OpPublishBatch, &snsapi.PublishBatchInput{TopicArn: topic, PublishBatchRequestEntries: []snsapi.PublishBatchRequestEntry{
			{Id: "a", Message: "one", MessageGroupId: new("g"), MessageDeduplicationId: new("1")},
			{Id: "b", Message: "two", MessageGroupId: new("g"), MessageDeduplicationId: new("2")},
		}})
		if len(out.Successful) != 2 || out.Failed == nil || len(out.Failed) != 0 || *out.Successful[0].Id != "a" || out.Successful[1].SequenceNumber == nil {
			h.Fatalf("PublishBatch = %+v", out)
		}
		if got := h.sink.all(); len(got) != 2 {
			h.Fatalf("outbox took %d deliveries, want 2", len(got))
		}

		entries := func(n int, id func(i int) string) []snsapi.PublishBatchRequestEntry {
			out := make([]snsapi.PublishBatchRequestEntry, n)
			for i := range out {
				out[i] = snsapi.PublishBatchRequestEntry{Id: id(i), Message: "m", MessageGroupId: new("g"), MessageDeduplicationId: new("d")}
			}
			return out
		}
		for _, tc := range []struct {
			entries       []snsapi.PublishBatchRequestEntry
			code, message string
		}{
			{[]snsapi.PublishBatchRequestEntry{}, "EmptyBatchRequest", "The batch request doesn't contain any entries."},
			{entries(11, strconv.Itoa), "TooManyEntriesInBatchRequest", "The batch request contains more entries than permissible."},
			{entries(2, func(int) string { return "same" }), "BatchEntryIdsNotDistinct", "Two or more batch entries in the request have the same Id."},
			{entries(1, func(int) string { return "message.id" }), "InvalidBatchEntryId", "The Id of a batch entry in the batch request contains an impermissible character: message.id"},
			{entries(1, func(int) string { return strings.Repeat("i", 81) }), "InvalidBatchEntryId", "The Id of a batch entry in the batch request is too long: " + strings.Repeat("i", 81)},
			{[]snsapi.PublishBatchRequestEntry{{Id: "a", Message: "m", MessageDeduplicationId: new("d")}}, "InvalidParameter", "Invalid parameter: The MessageGroupId parameter is required for FIFO topics"},
		} {
			_, err := call(h, snsapi.OpPublishBatch, &snsapi.PublishBatchInput{TopicArn: topic, PublishBatchRequestEntries: tc.entries})
			wantError(h, err, tc.code, tc.message)
		}

		small := h.topic("small", map[string]string{"MaximumMessageSize": "1024"})
		_, err := call(h, snsapi.OpPublishBatch, &snsapi.PublishBatchInput{TopicArn: small, PublishBatchRequestEntries: []snsapi.PublishBatchRequestEntry{
			{Id: "a", Message: strings.Repeat("x", 600)}, {Id: "b", Message: strings.Repeat("x", 600)},
		}})
		wantError(h, err, "BatchRequestTooLong", "The length of all the messages put together is more than the limit.")
	})
}

// deliveries returns the delivery events the journal holds whose outcome
// is an O, by target ARN.
func deliveries[O journal.DeliveryOutcome](h *harness) map[string][]journal.Delivery {
	h.Helper()
	out := map[string][]journal.Delivery{}
	events := h.k.Journal().Query(journal.Filter{Kind: journal.KindDelivery, Limit: 1000}).Events
	for i := range events {
		if d, ok := events[i].Detail.(journal.Delivery); ok {
			if _, ok := d.Outcome.(O); ok {
				out[d.To.String()] = append(out[d.To.String()], d)
			}
		}
	}
	return out
}

// filtered returns the Filtered delivery events, by target ARN.
func (h *harness) filtered() map[string][]journal.Delivery { return deliveries[journal.Filtered](h) }

// TestUndecidedFilterDropsTheMessage: a body the filter policy's matcher
// gives up on (its search budget, an understudy bound) is journaled as
// Dropped at attempt 0 with the subscription named, and reaches neither the
// outbox, the queue, nor the subscription's dead-letter queue. The policy
// and body are inside SNS's limits: five keys, a 5 KB body.
func TestUndecidedFilterDropsTheMessage(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "sqs", queueARN, map[string]string{
			"FilterPolicy":      `{"a":{"b":["1"],"c":["1"],"d":["1"],"e":["1"],"z":["2"]}}`,
			"FilterPolicyScope": "MessageBody",
			"RedrivePolicy":     `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:orders-dlq"}`,
		})
		elem := `{"b":"1","c":"1","d":"1","e":"1","z":"1"}`
		array := `[` + strings.Repeat(elem+",", 31) + elem + `]`
		body := `{` + strings.Repeat(`"a":`+array+",", 3) + `"a":` + array + `}`
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: body})
		h.settle()
		if n := len(h.sink.all()); n != 0 {
			h.Fatalf("outbox took %d deliveries of an undecided message, want 0", n)
		}
		if sent := h.sent.all(); len(sent) != 0 {
			h.Errorf("SendMessage ran %d times, want 0", len(sent))
		}
		if got := h.filtered(); len(got) != 0 {
			h.Errorf("Filtered events = %+v, want none: the message was not decided", got)
		}
		dropped := deliveries[journal.Dropped](h)[queueARN]
		if len(dropped) != 1 {
			h.Fatalf("Dropped events to the queue = %+v, want one", dropped)
		}
		d := dropped[0]
		outcome, ok := d.Outcome.(journal.Dropped)
		id := arn[strings.LastIndex(arn, ":")+1:]
		if !ok || d.Attempt != 0 || d.From.String() != topic || d.Mechanism != delivery.SNSToSQS ||
			!strings.HasPrefix(outcome.Reason, "the filter policy of subscription "+id+" could not be evaluated") {
			h.Errorf("Dropped event = %+v, want attempt 0 from %s over SNSToSQS naming subscription %s", d, topic, id)
		}
	})
}

// TestFilterPolicyDecidesDelivery publishes past an attribute-scope policy
// on a queue and a body-scope policy on a function: a matching message
// reaches the stub target, and a non-matching one is journaled as Filtered
// with the policy and never reaches the outbox (sns.md 6.3).
func TestFilterPolicyDecidesDelivery(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		byAttribute := `{"store":["north"],"price":[{"numeric":[">",100]}]}`
		byBody := `{"detail":{"kind":["order"]}}`
		h.subscribe(topic, "sqs", queueARN, map[string]string{"FilterPolicy": byAttribute, "RawMessageDelivery": "true"})
		h.subscribe(topic, "lambda", functionARN, map[string]string{"FilterPolicy": byBody, "FilterPolicyScope": "MessageBody"})
		publish := func(body string, attrs map[string]snsapi.MessageAttributeValue) {
			must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: body, MessageAttributes: attrs})
		}

		publish(`{"detail":{"kind":"refund"}}`, map[string]snsapi.MessageAttributeValue{"store": attr("String", "south"), "price": attr("Number", "150")})
		h.settle()
		if n := len(h.sink.all()); n != 0 {
			h.Fatalf("outbox took %d deliveries of a message neither policy matches, want 0", n)
		}
		got := h.filtered()
		for target, rule := range map[string]string{queueARN: byAttribute, functionARN: byBody} {
			events := got[target]
			if len(events) != 1 {
				h.Fatalf("Filtered events to %s = %+v, want one", target, events)
			}
			if d := events[0]; d.Outcome != (journal.Filtered{Rule: rule}) || d.From.String() != topic || d.Attempt != 0 {
				h.Errorf("Filtered event to %s = %+v, want rule %s from %s at attempt 0", target, d, rule, topic)
			}
		}

		publish(`{"detail":{"kind":["refund","order"]}}`, map[string]snsapi.MessageAttributeValue{"store": attr("String", "north"), "price": attr("Number", "1.5e2")})
		h.settle()
		if sent := h.sent.all(); len(sent) != 1 || sent[0].MessageAttributes["store"].StringValue == nil || *sent[0].MessageAttributes["store"].StringValue != "north" {
			h.Errorf("SendMessage = %+v, want one raw message with its attributes", sent)
		}
		if n := len(h.invoked.all()); n != 1 {
			h.Errorf("Invoke ran %d times, want 1", n)
		}
		if n := len(h.filtered()[queueARN]); n != 1 {
			h.Errorf("%d Filtered events to the queue after a matching publish, want still 1", n)
		}
	})
}

// TestFilteredMessageNeverReachesTheDeadLetterQueue: a subscription with a
// RedrivePolicy hands its queue to the outbox as the delivery's DLQ, and a
// message its policy filters out produces no outbox entry at all, so it
// cannot be redriven (sns.md 6.3, 7.4).
func TestFilteredMessageNeverReachesTheDeadLetterQueue(t *testing.T) {
	run(t, func(h *harness) {
		const dlq = "arn:aws:sqs:us-east-1:000000000000:orders-dlq"
		topic := h.topic("events", nil)
		h.subscribe(topic, "sqs", queueARN, map[string]string{
			"FilterPolicy":  `{"store":["north"]}`,
			"RedrivePolicy": `{"deadLetterTargetArn":"` + dlq + `"}`,
		})
		h.subscribe(topic, "lambda", functionARN, map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"` + dlq + `"}`})
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
		h.settle()
		got := h.sink.all()
		if len(got) != 1 {
			h.Fatalf("outbox took %d deliveries, want only the function's", len(got))
		}
		if r := got[0].Request; r.DLQ == nil || r.DLQ.String() != dlq || r.Mechanism != delivery.SNSToLambda {
			h.Errorf("delivery = %+v, want the function's with DLQ %s", r, dlq)
		}
		if len(h.filtered()[queueARN]) != 1 {
			h.Error("the filtered message was not journaled as Filtered")
		}
		for _, in := range h.sent.all() {
			h.Errorf("SendMessage ran for %s; a filtered message reached a queue", in.QueueUrl)
		}
	})
}

// TestFIFOFilterKeepsGroupAndOrder: on a FIFO topic a matching message
// reaches a FIFO queue with its group and deduplication ids, in publish
// order, while a filtered one in between is left out (sns.md 5.3).
func TestFIFOFilterKeepsGroupAndOrder(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events.fifo", map[string]string{"FifoTopic": "true"})
		h.subscribe(topic, "sqs", fifoQueueARN, map[string]string{"FilterPolicy": `{"kind":[{"anything-but":"skip"}]}`, "RawMessageDelivery": "true"})
		for i, kind := range []string{"a", "skip", "b"} {
			must(h, snsapi.OpPublish, &snsapi.PublishInput{
				TopicArn: &topic, Message: kind, MessageGroupId: new("g1"), MessageDeduplicationId: new(strconv.Itoa(i)),
				MessageAttributes: map[string]snsapi.MessageAttributeValue{"kind": attr("String", kind)},
			})
		}
		h.settle()
		sent := h.sent.all()
		if len(sent) != 2 {
			h.Fatalf("SendMessage ran %d times, want 2", len(sent))
		}
		for i, want := range []struct{ body, dedup string }{{"a", "0"}, {"b", "2"}} {
			in := sent[i]
			if in.MessageBody != want.body || in.MessageGroupId == nil || *in.MessageGroupId != "g1" || in.MessageDeduplicationId == nil || *in.MessageDeduplicationId != want.dedup {
				h.Errorf("send %d = body %q group %v dedup %v, want %q in g1 with %s", i, in.MessageBody, in.MessageGroupId, in.MessageDeduplicationId, want.body, want.dedup)
			}
		}
	})
}
