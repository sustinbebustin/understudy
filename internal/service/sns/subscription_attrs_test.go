package sns_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// TestFilterPolicyValidationTexts checks the recorded filter policy texts
// on both calls that carry a policy (sns.md 6.5): SetSubscriptionAttributes
// answers "Invalid parameter: <reason>", Subscribe "Invalid parameter:
// Attributes Reason: <reason>". Jackson's location line, which AWS appends
// to structural errors, is compared away. Each row sets its policy on a
// subscription of its own, so a row runs under its own scope only.
func TestFilterPolicyValidationTexts(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		for i, tc := range []struct {
			policy, scope, reason string
		}{
			{`invalid`, "", "FilterPolicy: failed to parse JSON. Unrecognized token 'invalid': was expecting (JSON String, Number, Array, Object or token 'null', 'true' or 'false')"},
			{`{"key":[]}`, "", "FilterPolicy: Empty arrays are not allowed"},
			{`{"key":[{"numeric":[">",3,"<",1]}]}`, "", "FilterPolicy: Bottom must be less than top"},
			{`{"a":["1"],"b":["1"],"c":["1"],"d":["1"],"e":["1"],"f":["1"]}`, "", "FilterPolicy: Filter policy can not have more than 5 keys"},
			{`{"object":{"key":[{"prefix":"auto-"}]}}`, "", "Filter policy scope MessageAttributes does not support nested filter policy"},
			{`{"key":{"suffix":"v"}}`, "MessageBody", `FilterPolicy: "suffix" must be an object or an array`},
			// Recorded on Subscribe.
			{`{"key":{"not-an-operator":"value"}}`, "", `FilterPolicy: "not-an-operator" must be an object or an array`},
		} {
			arn := h.subscribe(topic, "sqs", queueARN+"-"+strconv.Itoa(i), nil)
			attrs := map[string]string{"FilterPolicy": tc.policy}
			if tc.scope != "" {
				attrs["FilterPolicyScope"] = tc.scope
				must(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "FilterPolicyScope", AttributeValue: new(tc.scope)})
			}
			_, err := call(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "FilterPolicy", AttributeValue: new(tc.policy)})
			wantFirstLine(h, err, "Invalid parameter: "+tc.reason)
			_, err = call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: "sqs", Endpoint: new(queueARN + "-new"), Attributes: attrs})
			wantFirstLine(h, err, "Invalid parameter: Attributes Reason: "+tc.reason)
			if _, ok := h.subscriptionAttributes(arn)["FilterPolicy"]; ok {
				h.Errorf("refused FilterPolicy %s was stored", tc.policy)
			}
		}
	})
}

// TestRedrivePolicyQueueMustBeDeliverable: a deadLetterTargetArn outside
// the aws partition, without a Region, or without a 12-digit account is
// refused with the recorded invalid-arn text on SetSubscriptionAttributes
// and Subscribe, and SetSubscriptionAttributes refuses a queue in another
// account or Region with an inferred text on both calls (sns.md 3.3, 7.4).
// Nothing refused is stored, so a later Publish to the topic is still
// delivered.
func TestRedrivePolicyQueueMustBeDeliverable(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "sqs", queueARN, nil)
		redrive := func(dlq string) string { return `{"deadLetterTargetArn":"` + dlq + `"}` }
		const invalid = "RedrivePolicy: deadLetterTargetArn is an invalid arn"
		for _, dlq := range []string{
			"arn:aws:sqs:::orders-dlq",
			"arn:aws:sqs:us-east-1::orders-dlq",
			"arn:aws:sqs::000000000000:orders-dlq",
			"arn:aws-cn:sqs:cn-north-1:000000000000:orders-dlq",
			"arn:aws:sqs:us-east-1:12345:orders-dlq",
			"arn:aws:sqs:US_EAST_1:000000000000:orders-dlq",
		} {
			_, err := call(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "RedrivePolicy", AttributeValue: new(redrive(dlq))})
			wantError(h, err, "InvalidParameter", "Invalid parameter: "+invalid)
			_, err = call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: "sqs", Endpoint: new(queueARN + "-new"), Attributes: map[string]string{"RedrivePolicy": redrive(dlq)}})
			wantError(h, err, "InvalidParameter", "Invalid parameter: Attributes Reason: "+invalid)
		}
		const elsewhere = "RedrivePolicy: deadLetterTargetArn must be an Amazon SQS queue in the account and Region of the subscription (000000000000, us-east-1)"
		for _, dlq := range []string{"arn:aws:sqs:eu-west-1:000000000000:orders-dlq", "arn:aws:sqs:us-east-1:111111111111:orders-dlq"} {
			_, err := call(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "RedrivePolicy", AttributeValue: new(redrive(dlq))})
			wantError(h, err, "InvalidParameter", "Invalid parameter: "+elsewhere)
			_, err = call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: "sqs", Endpoint: new(queueARN + "-new"), Attributes: map[string]string{"RedrivePolicy": redrive(dlq)}})
			wantError(h, err, "InvalidParameter", "Invalid parameter: Attributes Reason: "+elsewhere)
		}
		// Recorded 2025-10-06 (sns.md 3.3); the location line is compared
		// away.
		_, err := call(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "RedrivePolicy", AttributeValue: new("{invalid}")})
		wantFirstLine(h, err, "Invalid parameter: RedrivePolicy: failed to parse JSON. Unexpected character ('i' (code 105)): was expecting double-quote to start field name")
		if got, ok := h.subscriptionAttributes(arn)["RedrivePolicy"]; ok {
			h.Errorf("refused RedrivePolicy %s was stored", got)
		}
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
		h.settle()
		if sent := h.sent.all(); len(sent) != 1 {
			h.Errorf("SendMessage ran %d times after the refused policies, want 1", len(sent))
		}
	})
}

// TestSubscriptionDeliveryPolicyIsStored: a subscription DeliveryPolicy is
// a JSON object stored and echoed verbatim, "" removes it, and anything
// else is refused with the inferred text on SetSubscriptionAttributes and
// Subscribe, leaving the stored policy unchanged. A repeated Subscribe
// agrees only with the same policy (sns.md 3.1, 3.3).
func TestSubscriptionDeliveryPolicyIsStored(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		const policy = `{"healthyRetryPolicy": {"numRetries": 5}}`
		arn := h.subscribe(topic, "sqs", queueARN, map[string]string{"DeliveryPolicy": policy})
		if got := h.subscriptionAttributes(arn)["DeliveryPolicy"]; got != policy {
			h.Errorf("DeliveryPolicy after Subscribe = %q, want %q", got, policy)
		}
		if again := h.subscribe(topic, "sqs", queueARN, map[string]string{"DeliveryPolicy": policy}); again != arn {
			h.Errorf("a repeated Subscribe with the same DeliveryPolicy = %s, want %s", again, arn)
		}
		_, err := call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: "sqs", Endpoint: new(queueARN), Attributes: map[string]string{"DeliveryPolicy": `{}`}})
		wantError(h, err, "InvalidParameter", "Invalid parameter: Attributes Reason: Subscription already exists with different attributes")

		set := func(value string) *awserr.Error {
			_, err := call(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "DeliveryPolicy", AttributeValue: new(value)})
			return err
		}
		for _, bad := range []string{`{bad`, `[]`, `"text"`} {
			wantError(h, set(bad), "InvalidParameter", "Invalid parameter: DeliveryPolicy: failed to parse JSON.")
			_, err := call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: "sqs", Endpoint: new(queueARN + "-new"), Attributes: map[string]string{"DeliveryPolicy": bad}})
			wantError(h, err, "InvalidParameter", "Invalid parameter: Attributes Reason: DeliveryPolicy: failed to parse JSON.")
		}
		if got := h.subscriptionAttributes(arn)["DeliveryPolicy"]; got != policy {
			h.Errorf("DeliveryPolicy after the refused values = %q, want %q", got, policy)
		}
		const replaced = `{"throttlePolicy":{"maxReceivesPerSecond":1}}`
		if err := set(replaced); err != nil {
			h.Fatalf("SetSubscriptionAttributes DeliveryPolicy = %s %q", err.ID.QueryCode, err.Message)
		}
		if got := h.subscriptionAttributes(arn)["DeliveryPolicy"]; got != replaced {
			h.Errorf("DeliveryPolicy after Set = %q, want %q", got, replaced)
		}
		if err := set(""); err != nil {
			h.Fatalf("clearing DeliveryPolicy = %s %q", err.ID.QueryCode, err.Message)
		}
		if got, ok := h.subscriptionAttributes(arn)["DeliveryPolicy"]; ok {
			h.Errorf("DeliveryPolicy %q is still shown after a clear", got)
		}
	})
}

// wantFirstLine fails unless err is InvalidParameter whose message's first
// line is message.
func wantFirstLine(h *harness, err *awserr.Error, message string) {
	h.Helper()
	if err == nil {
		h.Fatalf("no error, want InvalidParameter %q", message)
	}
	if first, _, _ := strings.Cut(err.Message, "\n"); err.ID.QueryCode != "InvalidParameter" || first != message {
		h.Fatalf("error = %s %q\nwant  InvalidParameter %q", err.ID.QueryCode, err.Message, message)
	}
}

// TestFilterPolicyQuotaPerTopic: a topic holds at most 200 subscriptions
// with a filter policy (sns.md 6.5). The 201st policy is refused with
// FilterPolicyLimitExceeded (403) and not stored, replacing one of the 200
// is allowed, and removing one makes room.
func TestFilterPolicyQuotaPerTopic(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		subs := make([]string, 201)
		for i := range subs {
			subs[i] = h.subscribe(topic, "sqs", queueARN+"-"+strconv.Itoa(i), nil)
		}
		setPolicy := func(arn, policy string) *awserr.Error {
			_, err := call(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "FilterPolicy", AttributeValue: new(policy)})
			return err
		}
		for i, arn := range subs[:200] {
			if err := setPolicy(arn, `{"store":["north"]}`); err != nil {
				h.Fatalf("policy %d of 200 = %s %q", i+1, err.ID.QueryCode, err.Message)
			}
		}
		err := setPolicy(subs[200], `{"store":["north"]}`)
		wantError(h, err, "FilterPolicyLimitExceeded", "Filter policy limit exceeded: the topic already has 200 subscriptions with a filter policy, "+
			"the most it allows. Remove the FilterPolicy of another subscription to the topic, then retry.")
		if err.ID.Status != http.StatusForbidden {
			h.Errorf("status = %d, want 403", err.ID.Status)
		}
		if _, ok := h.subscriptionAttributes(subs[200])["FilterPolicy"]; ok {
			h.Error("the refused 201st policy was stored")
		}
		if err := setPolicy(subs[0], `{"store":["south"]}`); err != nil {
			h.Fatalf("replacing a policy at the quota = %s %q", err.ID.QueryCode, err.Message)
		}
		if err := setPolicy(subs[1], ""); err != nil {
			h.Fatalf("removing a policy = %s %q", err.ID.QueryCode, err.Message)
		}
		if err := setPolicy(subs[200], `{"store":["north"]}`); err != nil {
			h.Fatalf("a policy after one was removed = %s %q", err.ID.QueryCode, err.Message)
		}
	})
}

// TestFilterPolicySwitchesAreReported: the service lists the filter
// policy switches, with their record-backlog items, beside its own, so the
// coverage report shows them (matching-engines.md 10).
func TestFilterPolicySwitchesAreReported(t *testing.T) {
	run(t, func(h *harness) {
		records := map[string]string{}
		for _, e := range h.svc.Switches() {
			records[e.Name] = e.Record
		}
		for name, record := range map[string]string{
			"sns.filter-policy.empty-input":             "R09/6 (matching-engines.md H1)",
			"sns.filter-policy.exists-true-empty-value": "matching-engines.md H2",
			"sns.filter-policy.anything-but-suffix":     "R09/7 (matching-engines.md H7)",
			"sns.fifo.duplicate":                        "",
		} {
			if got, ok := records[name]; !ok || got != record {
				h.Errorf("switch %s: listed %v, record %q; want listed, record %q", name, ok, got, record)
			}
		}
	})
}

// TestFilterPolicyQuotaOnSubscribe:Subscribe counts toward the same
// per-topic quota. The 201st Subscribe with a FilterPolicy answers
// FilterPolicyLimitExceeded (403), yet the subscription is created without
// the policy, as AWS creates it (sns.md 3.1).
func TestFilterPolicyQuotaOnSubscribe(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		policy := map[string]string{"FilterPolicy": `{"store":["north"]}`}
		for i := range 200 {
			h.subscribe(topic, "sqs", queueARN+"-"+strconv.Itoa(i), policy)
		}
		last := queueARN + "-200"
		_, err := call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: "sqs", Endpoint: new(last), Attributes: policy})
		wantError(h, err, "FilterPolicyLimitExceeded", "Filter policy limit exceeded: the topic already has 200 subscriptions with a filter policy, "+
			"the most it allows. Remove the FilterPolicy of another subscription to the topic, then retry.")
		if err.ID.Status != http.StatusForbidden {
			h.Errorf("status = %d, want 403", err.ID.Status)
		}
		// A repeat without attributes finds the subscription the refused
		// call created.
		arn := h.subscribe(topic, "sqs", last, nil)
		if got, ok := h.subscriptionAttributes(arn)["FilterPolicy"]; ok {
			h.Errorf("the refused 201st policy was stored: %s", got)
		}
	})
}

// TestFilterPolicyScopeMustKeepThePolicyValid: a nested policy compiles
// under MessageBody, and moving it back to MessageAttributes is refused
// with the recorded text, leaving the subscription unchanged (sns.md 6.5;
// Terraform orders its scope writes around this, sns.md 3.3).
func TestFilterPolicyScopeMustKeepThePolicyValid(t *testing.T) {
	run(t, func(h *harness) {
		nested := `{"detail":{"store":["north"]}}`
		arn := h.subscribe(h.topic("events", nil), "sqs", queueARN, map[string]string{"FilterPolicy": nested, "FilterPolicyScope": "MessageBody"})
		_, err := call(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "FilterPolicyScope", AttributeValue: new("MessageAttributes")})
		wantError(h, err, "InvalidParameter", "Invalid parameter: Filter policy scope MessageAttributes does not support nested filter policy")
		if got := h.subscriptionAttributes(arn); got["FilterPolicyScope"] != "MessageBody" || got["FilterPolicy"] != nested {
			h.Errorf("after the refused scope change = %v", got)
		}
		// Without a policy any scope is stored.
		plain := h.subscribe(h.topic("other", nil), "sqs", queueARN, nil)
		must(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: plain, AttributeName: "FilterPolicyScope", AttributeValue: new("MessageAttributes")})
		_, err = call(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: plain, AttributeName: "FilterPolicy", AttributeValue: new(nested)})
		wantError(h, err, "InvalidParameter", "Invalid parameter: Filter policy scope MessageAttributes does not support nested filter policy")
		if strings.Contains(h.subscriptionAttributes(plain)["FilterPolicy"], "detail") {
			h.Error("a nested policy was stored under MessageAttributes")
		}
	})
}
