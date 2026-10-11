package sns_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// TestSnapshotRoundTripKeepsTopicsSubscriptionsAndKey exports the state,
// resets it, imports it and checks the second export is byte-identical and
// the signing key still signs with the certificate it served before.
func TestSnapshotRoundTripKeepsTopicsSubscriptionsAndKey(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", map[string]string{"DisplayName": "Events"})
		sub := h.subscribe(topic, "sqs", queueARN, map[string]string{"RawMessageDelivery": "true"})
		h.subscribe(topic, "http", "http://127.0.0.1:9000/hook", nil)
		h.subscribe(h.topic("orders.fifo", map[string]string{"FifoTopic": "true", "ContentBasedDeduplication": "true"}), "sqs", fifoQueueARN, nil)
		envelopeSub := h.subscribe(topic, "sqs", "arn:aws:sqs:us-east-1:000000000000:audit", nil)
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "before"})
		h.settle()
		certBefore := certURLOf(h, envelopeSub)

		before, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if _, aerr := call(h, snsapi.OpGetTopicAttributes, &snsapi.GetTopicAttributesInput{TopicArn: topic}); aerr == nil {
			h.Fatal("the topic survived a reset")
		}
		if err := h.svc.Import(before); err != nil {
			h.Fatal(err)
		}
		after, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		if len(before) != len(after) {
			h.Fatalf("export has %d sections after the round trip, want %d", len(after), len(before))
		}
		for i := range before {
			if before[i].Path != after[i].Path || !bytes.Equal(before[i].Data, after[i].Data) {
				h.Errorf("section %s differs after the round trip:\n%s\n%s", before[i].Path, before[i].Data, after[i].Data)
			}
		}
		if got := h.subscriptionAttributes(sub)["RawMessageDelivery"]; got != "true" {
			h.Errorf("RawMessageDelivery after load = %q", got)
		}
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "after"})
		h.settle()
		if certAfter := certURLOf(h, envelopeSub); certAfter != certBefore {
			h.Errorf("SigningCertURL changed across the load: %s then %s", certBefore, certAfter)
		}
		resources := h.svc.Resources(h.scope)
		if len(resources) != 2 || resources[0].ARN.String() != topic || resources[0].Summary != "3 subscriptions" || resources[1].Summary != "1 subscription" {
			h.Errorf("Resources = %+v", resources)
		}
	})
}

// certURLOf returns the SigningCertURL of the last envelope sent to the
// queue a subscription delivers to.
func certURLOf(h *harness, subscription string) string {
	h.Helper()
	endpoint := h.subscriptionAttributes(subscription)["Endpoint"]
	sent := h.sent.all()
	for _, s := range slices.Backward(sent) {
		if !bytes.HasSuffix([]byte(s.QueueUrl), []byte(endpoint[len("arn:aws:sqs:us-east-1:000000000000:"):])) {
			continue
		}
		var env sqsEnvelope
		if err := json.Unmarshal([]byte(s.MessageBody), &env); err != nil {
			h.Fatal(err)
		}
		servedCertificate(h, env.SigningCertURL)
		return env.SigningCertURL
	}
	h.Fatalf("no envelope reached %s", endpoint)
	return ""
}
