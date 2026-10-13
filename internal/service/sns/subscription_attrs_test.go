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
// a JSON object, stored with every member AWS shows (absent ones null,
// guaranteed false), "" removes it, and anything else is refused with the
// inferred text on SetSubscriptionAttributes and Subscribe, leaving the
// stored policy unchanged. A repeated Subscribe agrees only with the same
// policy (sns.md 3.1, 3.3).
func TestSubscriptionDeliveryPolicyIsStored(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		const policy = `{"healthyRetryPolicy": {"numRetries": 5}}`
		const stored = `{"healthyRetryPolicy":{"numRetries":5},"sicklyRetryPolicy":null,"throttlePolicy":null,"requestPolicy":null,"guaranteed":false}`
		arn := h.subscribe(topic, "sqs", queueARN, map[string]string{"DeliveryPolicy": policy})
		if got := h.subscriptionAttributes(arn)["DeliveryPolicy"]; got != stored {
			h.Errorf("DeliveryPolicy after Subscribe = %q, want %q", got, stored)
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
		if got := h.subscriptionAttributes(arn)["DeliveryPolicy"]; got != stored {
			h.Errorf("DeliveryPolicy after the refused values = %q, want %q", got, stored)
		}
		if err := set(`{"throttlePolicy":{"maxReceivesPerSecond":1}}`); err != nil {
			h.Fatalf("SetSubscriptionAttributes DeliveryPolicy = %s %q", err.ID.QueryCode, err.Message)
		}
		const replaced = `{"healthyRetryPolicy":null,"sicklyRetryPolicy":null,"throttlePolicy":{"maxReceivesPerSecond":1},"requestPolicy":null,"guaranteed":false}`
		if got := h.subscriptionAttributes(arn)["DeliveryPolicy"]; got != replaced {
			h.Errorf("DeliveryPolicy after Set = %q, want %q", got, replaced)
		}
		if got, ok := h.subscriptionAttributes(arn)["EffectiveDeliveryPolicy"]; ok {
			h.Errorf("an sqs subscription shows EffectiveDeliveryPolicy %q", got)
		}
		if err := set(""); err != nil {
			h.Fatalf("clearing DeliveryPolicy = %s %q", err.ID.QueryCode, err.Message)
		}
		if got, ok := h.subscriptionAttributes(arn)["DeliveryPolicy"]; ok {
			h.Errorf("DeliveryPolicy %q is still shown after a clear", got)
		}
	})
}

// TestSubscriptionDeliveryPolicyKeepsRetryConstraints: a subscription
// DeliveryPolicy is held to the topic's retry and throttle constraints
// (sns.md 7.3), its paths without the http. and default prefixes, on both
// SetSubscriptionAttributes and Subscribe; the stored policy is unchanged.
func TestSubscriptionDeliveryPolicyKeepsRetryConstraints(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "http", "http://127.0.0.1:9000/hook", map[string]string{"DeliveryPolicy": `{"healthyRetryPolicy":{"numRetries":100}}`})
		for policy, detail := range map[string]string{
			`{"healthyRetryPolicy":{"numRetries":101}}`:                      "healthyRetryPolicy.numRetries must be less than or equal to 100",
			`{"healthyRetryPolicy":{"minDelayTarget":5,"maxDelayTarget":4}}`: "healthyRetryPolicy.minDelayTarget must be less than or equal to 4",
			`{"throttlePolicy":{"maxReceivesPerSecond":0}}`:                  "throttlePolicy.maxReceivesPerSecond must be greater than or equal to 1",
		} {
			_, err := call(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "DeliveryPolicy", AttributeValue: new(policy)})
			wantError(h, err, "InvalidParameter", "Invalid parameter: DeliveryPolicy: "+detail)
			_, err = call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: "http", Endpoint: new("http://127.0.0.1:9000/other"), Attributes: map[string]string{"DeliveryPolicy": policy}})
			wantError(h, err, "InvalidParameter", "Invalid parameter: Attributes Reason: DeliveryPolicy: "+detail)
		}
		if got := h.subscriptionAttributes(arn)["DeliveryPolicy"]; !strings.Contains(got, `"numRetries":100`) {
			h.Errorf("DeliveryPolicy after the refused values = %s", got)
		}
	})
}

// TestHTTPSubscriptionDeliveryPolicyAsRecorded replays both AWS recordings
// of test_subscribe_external_http_endpoint_content_type (LocalStack,
// snapshot key sub-attrs): an http subscription's DeliveryPolicy shows
// every member in AWS's order, its healthyRetryPolicy reordered, and its
// EffectiveDeliveryPolicy fills the rest from the topic's effective http
// policy.
func TestHTTPSubscriptionDeliveryPolicyAsRecorded(t *testing.T) {
	const defaultRetry = `{"minDelayTarget":20,"maxDelayTarget":20,"numRetries":3,"numMaxDelayRetries":0,"numNoDelayRetries":0,"numMinDelayRetries":0,"backoffFunction":"linear"}`
	const fixtureRetry = `{"minDelayTarget":1,"maxDelayTarget":1,"numRetries":0,"numMaxDelayRetries":0,"numNoDelayRetries":0,"numMinDelayRetries":0,"backoffFunction":"linear"}`
	// fixturePolicy is the policy LocalStack's sns_create_http_endpoint
	// sets, in its member order.
	const fixturePolicy = `{"healthyRetryPolicy": {"minDelayTarget": 1, "maxDelayTarget": 1, "numRetries": 0, "numNoDelayRetries": 0, "numMinDelayRetries": 0, ` +
		`"numMaxDelayRetries": 0, "backoffFunction": "linear"}, "sicklyRetryPolicy": null, "throttlePolicy": {"maxReceivesPerSecond": 1000}, "guaranteed": false}`
	run(t, func(h *harness) {
		setSub := func(arn, policy string) {
			must(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "DeliveryPolicy", AttributeValue: new(policy)})
		}
		want := func(arn, attr, value string) {
			h.Helper()
			if got := h.subscriptionAttributes(arn)[attr]; got != value {
				h.Errorf("%s =\n%s\nwant\n%s", attr, got, value)
			}
		}

		// [True]: the subscription's policy replaces the fixture's.
		topic := h.topic("csv", nil)
		arn := h.subscribe(topic, "http", "http://127.0.0.1:9000/sns-endpoint", nil)
		setSub(arn, fixturePolicy)
		setSub(arn, `{"requestPolicy": {"headerContentType": "text/csv"}}`)
		want(arn, "DeliveryPolicy", `{"healthyRetryPolicy":null,"sicklyRetryPolicy":null,"throttlePolicy":null,"requestPolicy":{"headerContentType":"text/csv"},"guaranteed":false}`)
		want(arn, "EffectiveDeliveryPolicy", `{"healthyRetryPolicy":`+defaultRetry+`,"sicklyRetryPolicy":null,"throttlePolicy":null,"requestPolicy":{"headerContentType":"text/csv"},"guaranteed":false}`)

		// [False]: the topic's defaultRequestPolicy fills the null one.
		topic = h.topic("json", nil)
		arn = h.subscribe(topic, "http", "http://127.0.0.1:9000/sns-endpoint", nil)
		setSub(arn, fixturePolicy)
		must(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{
			TopicArn: topic, AttributeName: "DeliveryPolicy", AttributeValue: new(`{"http": {"defaultRequestPolicy": {"headerContentType": "application/json"}}}`),
		})
		want(arn, "DeliveryPolicy", `{"healthyRetryPolicy":`+fixtureRetry+`,"sicklyRetryPolicy":null,"throttlePolicy":{"maxReceivesPerSecond":1000},"requestPolicy":null,"guaranteed":false}`)
		want(arn, "EffectiveDeliveryPolicy", `{"healthyRetryPolicy":`+fixtureRetry+`,"sicklyRetryPolicy":null,"throttlePolicy":{"maxReceivesPerSecond":1000},`+
			`"requestPolicy":{"headerContentType":"application/json"},"guaranteed":false}`)
	})
}

// TestRawMessageDeliveryOnlyWhereSupported: RawMessageDelivery=true is
// refused for a protocol without raw delivery on both calls, with the
// texts recorded against AWS for email and sms; false is accepted.
func TestRawMessageDeliveryOnlyWhereSupported(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		for _, tc := range []struct{ protocol, endpoint string }{
			{"email", "someone@example.com"},
			{"email-json", "someone@example.com"},
			{"lambda", functionARN},
		} {
			_, err := call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{
				TopicArn: topic, Protocol: tc.protocol, Endpoint: new(tc.endpoint), Attributes: map[string]string{"RawMessageDelivery": "true"},
			})
			wantError(h, err, "InvalidParameter", "Invalid parameter: Attributes Reason: Delivery protocol ["+tc.protocol+"] does not support raw message delivery.")

			arn := h.subscribe(topic, tc.protocol, tc.endpoint, map[string]string{"RawMessageDelivery": "false"})
			_, err = call(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "RawMessageDelivery", AttributeValue: new("true")})
			wantError(h, err, "InvalidParameter", "Invalid parameter: Delivery protocol ["+tc.protocol+"] does not support raw message delivery.")
			if got := h.subscriptionAttributes(arn)["RawMessageDelivery"]; got != "false" {
				h.Errorf("%s RawMessageDelivery after the refusal = %q, want false", tc.protocol, got)
			}
		}
		for _, tc := range []struct{ protocol, endpoint string }{
			{"sqs", queueARN},
			{"http", "http://127.0.0.1:9000/hook"},
			{"https", "https://127.0.0.1:9443/hook"},
		} {
			arn := h.subscribe(topic, tc.protocol, tc.endpoint, map[string]string{"RawMessageDelivery": "true"})
			if got := h.subscriptionAttributes(arn)["RawMessageDelivery"]; got != "true" {
				h.Errorf("%s RawMessageDelivery = %q, want true", tc.protocol, got)
			}
		}
	})
}

// TestReplayPolicyOnlyOnFIFOTopics: message replay exists only for FIFO
// topics (sns.md 3.3), so a ReplayPolicy on a standard topic's subscription
// is refused at Subscribe and SetSubscriptionAttributes with an inferred
// text and not stored, while a FIFO topic's subscription keeps it.
func TestReplayPolicyOnlyOnFIFOTopics(t *testing.T) {
	const replay = `{"PointType":"Timestamp","StartingPoint":"2025-01-01T00:00:00.000Z"}`
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		_, err := call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{
			TopicArn: topic, Protocol: "sqs", Endpoint: new(queueARN), Attributes: map[string]string{"ReplayPolicy": replay},
		})
		wantError(h, err, "InvalidParameter", "Invalid parameter: Attributes Reason: ReplayPolicy")
		arn := h.subscribe(topic, "sqs", queueARN, nil)
		_, err = call(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "ReplayPolicy", AttributeValue: new(replay)})
		wantError(h, err, "InvalidParameter", "Invalid parameter: ReplayPolicy")
		if got, ok := h.subscriptionAttributes(arn)["ReplayPolicy"]; ok {
			h.Errorf("refused ReplayPolicy was stored: %q", got)
		}

		fifo := h.topic("events.fifo", map[string]string{"FifoTopic": "true"})
		fifoArn := h.subscribe(fifo, "sqs", fifoQueueARN, nil)
		must(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: fifoArn, AttributeName: "ReplayPolicy", AttributeValue: new(replay)})
		if got := h.subscriptionAttributes(fifoArn)["ReplayPolicy"]; got != replay {
			h.Errorf("FIFO ReplayPolicy = %q, want %q", got, replay)
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
