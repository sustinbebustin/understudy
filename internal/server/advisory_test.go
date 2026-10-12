package server_test

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
)

// An SNS delivery to a queue whose stored policy does not allow the topic
// to send journals a policy-would-deny advisory, and is delivered anyway;
// once the queue policy allows sns.amazonaws.com from the topic, the next
// delivery journals none (ARCHITECTURE.md 16, product C3).
func TestDeliveryAdvisesOnTheQueuePolicy(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	const queueARN = "arn:aws:sqs:us-east-1:000000000000:advised"
	queueURL := endpoint + "/000000000000/advised"
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"advised"}`)
	topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"advising"}}), "TopicArn")
	mustSNS(t, endpoint, url.Values{"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {"sqs"}, "Endpoint": {queueARN}})
	publish := func() {
		t.Helper()
		mustSNS(t, endpoint, url.Values{"Action": {"Publish"}, "TopicArn": {topic}, "Message": {"hello"}})
		if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}

	publish()
	if got := advisories(t, c, queueARN); got != 1 {
		t.Fatalf("%d policy-would-deny advisories for a queue without a policy, want 1", got)
	}
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},` +
		`"Action":"sqs:SendMessage","Resource":"` + queueARN + `","Condition":{"ArnEquals":{"aws:SourceArn":"` + topic + `"}}}]}`
	attrs, err := json.Marshal(map[string]any{"QueueUrl": queueURL, "Attributes": map[string]string{"Policy": policy}})
	if err != nil {
		t.Fatal(err)
	}
	mustSQS(t, endpoint, "SetQueueAttributes", string(attrs))
	publish()
	if got := advisories(t, c, queueARN); got != 1 {
		t.Errorf("%d policy-would-deny advisories after the policy allowed the topic, want still 1", got)
	}
	received := mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+queueURL+`","MaxNumberOfMessages":10}`)
	var out struct{ Messages []json.RawMessage }
	if err := json.Unmarshal([]byte(received), &out); err != nil || len(out.Messages) != 2 {
		t.Errorf("ReceiveMessage = %s, want both deliveries made whatever the advice", received)
	}
}

// advisories counts the policy-would-deny advisories about resource.
func advisories(t *testing.T, c *admin.Client, resource string) int {
	t.Helper()
	page, err := c.JournalEvents(t.Context(), admin.JournalQuery{Kind: "advisory", Resource: resource})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for i := range page.Events {
		var d struct{ Advisory string }
		if json.Unmarshal(page.Events[i].Detail, &d) == nil && d.Advisory == "policy-would-deny" {
			n++
		}
	}
	return n
}
