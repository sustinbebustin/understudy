package esm_test

import (
	"crypto/md5" //nolint:gosec // SQS's MD5OfBody is an MD5 by definition
	"encoding/hex"
	"encoding/json"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

func queueARN(tb testing.TB, s string) aws.ARN {
	tb.Helper()
	a, err := aws.ParseARN(s)
	if err != nil {
		tb.Fatal(err)
	}
	return a
}

// The standard-queue record of lambda-events.md 3, verbatim from the
// with-sqs guide, with the md5OfMessageAttributes AWS delivers.
func TestNewRecordStandard(t *testing.T) {
	m := &sqsapi.Message{
		MessageId:     new("059f36b4-87a3-44ab-83d2-661975830a7d"),
		ReceiptHandle: new("AQEBwJnKyrHigUMZj6rYigCgxlaS3SLy0a"),
		Body:          new("Test message."),
		MD5OfBody:     new("e4e68fb7bd0e697a0ae8f1bb342846b3"),
		Attributes: map[sqsapi.MessageSystemAttributeName]string{
			sqsapi.MessageSystemAttributeNameApproximateReceiveCount:          "1",
			sqsapi.MessageSystemAttributeNameSentTimestamp:                    "1545082649183",
			sqsapi.MessageSystemAttributeNameSenderId:                         "AIDAIENQZJOLO23YVJ4VO",
			sqsapi.MessageSystemAttributeNameApproximateFirstReceiveTimestamp: "1545082649185",
		},
		MessageAttributes:      map[string]sqsapi.MessageAttributeValue{"myAttribute": {StringValue: new("myValue"), DataType: "String"}},
		MD5OfMessageAttributes: new("6df6bcdb1fe13a91f3b47a8d1fd7f26d"),
	}
	got, err := json.Marshal(esm.NewRecord(m, queueARN(t, "arn:aws:sqs:us-east-2:123456789012:my-queue")))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"messageId":"059f36b4-87a3-44ab-83d2-661975830a7d","receiptHandle":"AQEBwJnKyrHigUMZj6rYigCgxlaS3SLy0a","body":"Test message.",` +
		`"attributes":{"ApproximateFirstReceiveTimestamp":"1545082649185","ApproximateReceiveCount":"1","SenderId":"AIDAIENQZJOLO23YVJ4VO","SentTimestamp":"1545082649183"},` +
		`"messageAttributes":{"myAttribute":{"stringValue":"myValue","stringListValues":[],"binaryListValues":[],"dataType":"String"}},` +
		`"md5OfBody":"e4e68fb7bd0e697a0ae8f1bb342846b3","md5OfMessageAttributes":"6df6bcdb1fe13a91f3b47a8d1fd7f26d",` +
		`"eventSource":"aws:sqs","eventSourceARN":"arn:aws:sqs:us-east-2:123456789012:my-queue","awsRegion":"us-east-2"}`
	if string(got) != want {
		t.Fatalf("record =\n%s\nwant\n%s", got, want)
	}
}

// A message without attributes has {} for both maps and a null
// md5OfMessageAttributes, as AWS delivered it (LocalStack's
// test_message_body_and_attributes_passed_correctly snapshot); a binary
// attribute carries binaryValue in base64 and no stringValue.
func TestNewRecordEmptyAndBinary(t *testing.T) {
	queue := queueARN(t, "arn:aws:sqs:us-east-1:000000000000:q.fifo")
	bare, err := json.Marshal(esm.NewRecord(&sqsapi.Message{MessageId: new("m"), ReceiptHandle: new("h"), Body: new(""), MD5OfBody: new("d41d8cd98f00b204e9800998ecf8427e")}, queue))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"messageId":"m","receiptHandle":"h","body":"","attributes":{},"messageAttributes":{},"md5OfBody":"d41d8cd98f00b204e9800998ecf8427e",` +
		`"md5OfMessageAttributes":null,"eventSource":"aws:sqs","eventSourceARN":"arn:aws:sqs:us-east-1:000000000000:q.fifo","awsRegion":"us-east-1"}`
	if string(bare) != want {
		t.Fatalf("record =\n%s\nwant\n%s", bare, want)
	}
	r := esm.NewRecord(&sqsapi.Message{MessageAttributes: map[string]sqsapi.MessageAttributeValue{"b": {BinaryValue: []byte("1100"), DataType: "Binary"}}}, queue)
	attr, err := json.Marshal(r.MessageAttributes["b"])
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"binaryValue":"MTEwMA==","stringListValues":[],"binaryListValues":[],"dataType":"Binary"}`; string(attr) != want {
		t.Fatalf("binary attribute = %s, want %s", attr, want)
	}
}

// Every received message becomes a record with the same id, handle, body,
// body digest and system attributes, from its queue and region.
func TestNewRecordProperties(t *testing.T) {
	queue := queueARN(t, "arn:aws:sqs:eu-west-1:111122223333:orders")
	rapid.Check(t, func(t *rapid.T) {
		body := rapid.String().Draw(t, "body")
		sum := md5.Sum([]byte(body)) //nolint:gosec // SQS's digest
		attrs := rapid.MapOf(rapid.SampledFrom([]sqsapi.MessageSystemAttributeName{
			sqsapi.MessageSystemAttributeNameSenderId, sqsapi.MessageSystemAttributeNameSentTimestamp,
			sqsapi.MessageSystemAttributeNameMessageGroupId, sqsapi.MessageSystemAttributeNameSequenceNumber,
		}), rapid.StringMatching(`[0-9A-Z]{1,20}`)).Draw(t, "attributes")
		m := &sqsapi.Message{
			MessageId: new(rapid.StringMatching(`[0-9a-f-]{36}`).Draw(t, "id")), ReceiptHandle: new(rapid.String().Draw(t, "handle")),
			Body: new(body), MD5OfBody: new(hex.EncodeToString(sum[:])), Attributes: attrs,
		}
		var decoded map[string]any
		enc, err := json.Marshal(esm.NewRecord(m, queue))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(enc, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded["body"] != body || decoded["messageId"] != *m.MessageId || decoded["receiptHandle"] != *m.ReceiptHandle ||
			decoded["md5OfBody"] != hex.EncodeToString(sum[:]) || decoded["eventSourceARN"] != queue.String() || decoded["awsRegion"] != "eu-west-1" {
			t.Fatalf("record %s does not carry the message %+v", enc, m)
		}
		gotAttrs, ok := decoded["attributes"].(map[string]any)
		if !ok || len(gotAttrs) != len(attrs) {
			t.Fatalf("attributes = %v, want %v", decoded["attributes"], attrs)
		}
		for name, v := range attrs {
			if gotAttrs[string(name)] != v {
				t.Fatalf("attribute %s = %v, want %q", name, gotAttrs[string(name)], v)
			}
		}
	})
}
