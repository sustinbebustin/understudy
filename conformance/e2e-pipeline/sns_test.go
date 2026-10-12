package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// TestSNSFilterPoliciesRouteEachMessage publishes one message per route
// and checks each reached exactly the workers whose subscription filter
// policy matches it: attribute scope for the Go and image workers, body
// scope for the Python worker, and nothing for a message no policy
// matches (sns.md 6, the filter policy grammar).
func TestSNSFilterPoliciesRouteEachMessage(t *testing.T) {
	pipe(t)
	order, audit, refund, none := newID(t), newID(t), newID(t), newID(t)
	publish(t, message{ID: order, Priority: 1}, map[string]string{"kind": "order"})
	publish(t, message{ID: audit, Priority: 7}, map[string]string{"kind": "audit"})
	publish(t, message{ID: refund, Priority: 2}, map[string]string{"kind": "refund", "amount": "500"})
	publish(t, message{ID: none, Priority: 1}, map[string]string{"kind": "audit"})
	advance(t, pyWindow())

	waitProcessed(t, goWorker, order)
	waitProcessed(t, pyWorker, audit)
	waitProcessed(t, imageWorker, refund)
	settle(t)
	routes := map[string][]string{order: {goWorker}, audit: {pyWorker}, refund: {imageWorker}, none: nil}
	for id, want := range routes {
		for _, fn := range []string{goWorker, pyWorker, imageWorker} {
			if !slices.Contains(want, fn) {
				assertNotProcessed(t, fn, id)
			}
		}
	}
}

// TestSNSEnvelopeDeliveryToSQS reads a message from the tap queue, a
// subscription without raw delivery, and checks the JSON envelope against
// the recorded AWS form: member order, values, attribute map, and no SQS
// message attributes (sns.md 5.1).
func TestSNSEnvelopeDeliveryToSQS(t *testing.T) {
	p := pipe(t)
	id := newID(t)
	pub := publish(t, message{ID: id}, map[string]string{"kind": "order", "amount": "12"})
	settle(t)
	m := receiveEnvelope(t, p.tap.url, pub.MessageID)

	keys := jsonKeys(t, []byte(aws.ToString(m.Body)))
	wantKeys := []string{"Type", "MessageId", "TopicArn", "Subject", "Message", "Timestamp", "SignatureVersion", "Signature", "SigningCertURL", "UnsubscribeURL", "MessageAttributes"}
	if !slices.Equal(keys, wantKeys) {
		t.Errorf("envelope members %v, want in this order %v", keys, wantKeys)
	}
	var e snsEnvelope
	if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &e); err != nil {
		t.Fatalf("the SQS body is not the JSON envelope: %v\n%s", err, aws.ToString(m.Body))
	}
	if e.Type != "Notification" || e.MessageID != pub.MessageID || e.TopicArn != p.topicARN || e.Subject != "e2e "+id || e.Message != pub.Body {
		t.Errorf("envelope Type %q, MessageId %q, TopicArn %q, Subject %q, Message %q; want Notification, %s, %s, %q, %q",
			e.Type, e.MessageID, e.TopicArn, e.Subject, e.Message, pub.MessageID, p.topicARN, "e2e "+id, pub.Body)
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`).MatchString(e.Timestamp) {
		t.Errorf("Timestamp %q, want ISO 8601 UTC with exactly three fractional digits", e.Timestamp)
	}
	if e.SignatureVersion != "1" || e.Signature == "" || e.SigningCertURL == "" {
		t.Errorf("SignatureVersion %q, Signature %q, SigningCertURL %q; want 1 and a signature and certificate URL", e.SignatureVersion, e.Signature, e.SigningCertURL)
	}
	if !strings.Contains(e.UnsubscribeURL, "Action=Unsubscribe&SubscriptionArn="+p.tapSubARN) {
		t.Errorf("UnsubscribeURL %q, want it to carry Action=Unsubscribe&SubscriptionArn=%s", e.UnsubscribeURL, p.tapSubARN)
	}
	wantAttrs := map[string]struct{ Type, Value string }{"kind": {"String", "order"}, "amount": {"Number", "12"}}
	if !maps.Equal(e.MessageAttributes, wantAttrs) {
		t.Errorf("MessageAttributes %v, want %v", e.MessageAttributes, wantAttrs)
	}
	if len(m.MessageAttributes) != 0 {
		t.Errorf("the SQS message carries message attributes %v; without raw delivery they live only in the envelope", m.MessageAttributes)
	}
}

// TestSNSSubscriptionAttributesReadBack reads each worker subscription's
// attributes: filter policy and scope, raw delivery, and the confirmed SQS
// subscription members AWS reports (sns.md 3.3).
func TestSNSSubscriptionAttributesReadBack(t *testing.T) {
	p := pipe(t)
	for _, sp := range specs {
		w := p.workers[sp.name]
		out, err := env.sns.GetSubscriptionAttributes(t.Context(), &sns.GetSubscriptionAttributesInput{SubscriptionArn: aws.String(w.subARN)})
		if err != nil {
			t.Errorf("sns GetSubscriptionAttributes %s: %v", sp.name, err)
			continue
		}
		a := out.Attributes
		scope := "MessageAttributes"
		if sp.bodyScope {
			scope = "MessageBody"
		}
		want := map[string]string{
			"Protocol": "sqs", "Endpoint": w.queue.arn, "TopicArn": p.topicARN, "SubscriptionArn": w.subARN, "Owner": env.account,
			"PendingConfirmation": "false", "ConfirmationWasAuthenticated": "true",
			"RawMessageDelivery": strconv.FormatBool(sp.raw), "FilterPolicyScope": scope,
		}
		for k, v := range want {
			if a[k] != v {
				t.Errorf("%s subscription %s = %q, want %q", sp.name, k, a[k], v)
			}
		}
		if !jsonEqual(a["FilterPolicy"], sp.filterPolicy) {
			t.Errorf("%s subscription FilterPolicy %s, want %s", sp.name, a["FilterPolicy"], sp.filterPolicy)
		}
	}
}

// snsEnvelope is the JSON body SNS sends an SQS subscription without raw
// delivery (sns.md 5.1).
type snsEnvelope struct {
	Type              string                                  `json:"Type"`
	MessageID         string                                  `json:"MessageId"`
	TopicArn          string                                  `json:"TopicArn"`
	Subject           string                                  `json:"Subject"`
	Message           string                                  `json:"Message"`
	Timestamp         string                                  `json:"Timestamp"`
	SignatureVersion  string                                  `json:"SignatureVersion"`
	Signature         string                                  `json:"Signature"`
	SigningCertURL    string                                  `json:"SigningCertURL"`
	UnsubscribeURL    string                                  `json:"UnsubscribeURL"`
	MessageAttributes map[string]struct{ Type, Value string } `json:"MessageAttributes"`
}

// receiveEnvelope receives from the tap queue until it finds the envelope
// of SNS message snsID, deleting what it reads; the tap exists only to be
// read.
func receiveEnvelope(t *testing.T, queueURL, snsID string) sqstypes.Message {
	t.Helper()
	var found *sqstypes.Message
	eventually(t, "the tap queue holds SNS message "+snsID, func() (bool, string) {
		out, err := env.sqs.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10,
			MessageAttributeNames:       []string{"All"},
			MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameAll},
		})
		if err != nil {
			t.Fatalf("sqs ReceiveMessage: %v", err)
		}
		for _, m := range out.Messages {
			var e snsEnvelope
			if json.Unmarshal([]byte(aws.ToString(m.Body)), &e) == nil && e.MessageID == snsID {
				found = &m
			}
			if _, err := env.sqs.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: m.ReceiptHandle}); err != nil {
				t.Fatalf("sqs DeleteMessage: %v", err)
			}
		}
		return found != nil, fmt.Sprintf("received %d other messages", len(out.Messages))
	})
	return *found
}

// jsonKeys lists the top-level member names of a JSON object in order.
func jsonKeys(t *testing.T, data []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not a JSON object: %s", data)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("reading %s: %v", data, err)
		}
		key, ok := tok.(string)
		if !ok {
			t.Fatalf("reading %s: member name %v is not a string", data, tok)
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("reading %s: %v", data, err)
		}
	}
	return keys
}

// jsonEqual reports whether two JSON texts hold the same value.
func jsonEqual(a, b string) bool {
	var va, vb any
	if json.Unmarshal([]byte(a), &va) != nil || json.Unmarshal([]byte(b), &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}
