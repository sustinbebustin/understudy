package sns_test

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // verifying SignatureVersion 1, which is SHA1withRSA
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

// TestTerraformSubscriptionSequenceReadsBackWithoutDiff replays
// aws_sns_topic_subscription for sqs and lambda (tf-messaging.md 3.5,
// tf-remaining.md 9): create, the confirmation waiter, the listing, the
// read, updates, and delete with the NotFound poll.
func TestTerraformSubscriptionSequenceReadsBackWithoutDiff(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		filter := `{"type":["order.created"]}`
		redrive := `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:orders-dlq"}`
		out := must(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{
			TopicArn: topic, Protocol: "sqs", Endpoint: new(queueARN), ReturnSubscriptionArn: true,
			Attributes: map[string]string{"FilterPolicy": filter, "FilterPolicyScope": "MessageBody", "RawMessageDelivery": "true", "RedrivePolicy": redrive},
		})
		arn := *out.SubscriptionArn
		if !strings.HasPrefix(arn, topic+":") {
			h.Fatalf("SubscriptionArn = %q", arn)
		}
		if got := h.subscriptionAttributes(arn)["PendingConfirmation"]; got != "false" {
			h.Fatalf("PendingConfirmation = %q; the confirmation waiter needs false on the first poll", got)
		}
		listed := must(h, snsapi.OpListSubscriptionsByTopic, &snsapi.ListSubscriptionsByTopicInput{TopicArn: topic}).Subscriptions
		if len(listed) != 1 || *listed[0].SubscriptionArn != arn || *listed[0].Endpoint != queueARN || *listed[0].Protocol != "sqs" || *listed[0].Owner != "000000000000" {
			h.Fatalf("ListSubscriptionsByTopic = %+v", listed)
		}
		want := map[string]string{
			"ConfirmationWasAuthenticated": "true",
			"Endpoint":                     queueARN,
			"FilterPolicy":                 filter,
			"FilterPolicyScope":            "MessageBody",
			"Owner":                        "000000000000",
			"PendingConfirmation":          "false",
			"Protocol":                     "sqs",
			"RawMessageDelivery":           "true",
			"RedrivePolicy":                redrive,
			"SubscriptionArn":              arn,
			"SubscriptionPrincipal":        "arn:aws:iam::000000000000:root",
			"TopicArn":                     topic,
		}
		if got := h.subscriptionAttributes(arn); !maps.Equal(got, want) {
			h.Fatalf("GetSubscriptionAttributes =\n%v\nwant\n%v", got, want)
		}

		// Update: raw off, a new policy, scope MessageAttributes last, and
		// RedrivePolicy cleared with AttributeValue omitted.
		set := func(name string, value *string) {
			must(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: name, AttributeValue: value})
		}
		set("RawMessageDelivery", new("false"))
		set("FilterPolicy", new(`{"type":["order.created","order.updated"]}`))
		set("FilterPolicyScope", new("MessageAttributes"))
		set("RedrivePolicy", nil)
		got := h.subscriptionAttributes(arn)
		if got["RawMessageDelivery"] != "false" || got["FilterPolicyScope"] != "MessageAttributes" || got["FilterPolicy"] != `{"type":["order.created","order.updated"]}` {
			h.Errorf("after update = %v", got)
		}
		if _, ok := got["RedrivePolicy"]; ok {
			h.Error("RedrivePolicy is still shown after a clear")
		}
		// Removing the policy hides the scope with it.
		set("FilterPolicy", new("{}"))
		got = h.subscriptionAttributes(arn)
		if _, ok := got["FilterPolicy"]; ok {
			h.Error(`FilterPolicy "{}" is still shown`)
		}
		if _, ok := got["FilterPolicyScope"]; ok {
			h.Error("FilterPolicyScope is shown without a FilterPolicy")
		}

		must(h, snsapi.OpUnsubscribe, &snsapi.UnsubscribeInput{SubscriptionArn: arn})
		_, err := call(h, snsapi.OpGetSubscriptionAttributes, &snsapi.GetSubscriptionAttributesInput{SubscriptionArn: arn})
		wantError(h, err, "NotFound", "Subscription does not exist")
		if deleted := h.topicAttributes(topic)["SubscriptionsDeleted"]; deleted != "1" {
			h.Errorf("SubscriptionsDeleted = %q", deleted)
		}
		// Unsubscribe is idempotent.
		must(h, snsapi.OpUnsubscribe, &snsapi.UnsubscribeInput{SubscriptionArn: arn})

		lambda := h.subscribe(topic, "lambda", functionARN, nil)
		want = map[string]string{
			"ConfirmationWasAuthenticated": "true",
			"Endpoint":                     functionARN,
			"Owner":                        "000000000000",
			"PendingConfirmation":          "false",
			"Protocol":                     "lambda",
			"RawMessageDelivery":           "false",
			"SubscriptionArn":              lambda,
			"SubscriptionPrincipal":        "arn:aws:iam::000000000000:root",
			"TopicArn":                     topic,
		}
		if got := h.subscriptionAttributes(lambda); !maps.Equal(got, want) {
			h.Errorf("lambda subscription attributes =\n%v\nwant\n%v", got, want)
		}
	})
}

func TestFilterPolicyScopeAloneStaysHidden(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.subscribe(h.topic("events", nil), "sqs", queueARN, nil)
		must(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "FilterPolicyScope", AttributeValue: new("MessageBody")})
		if _, ok := h.subscriptionAttributes(arn)["FilterPolicyScope"]; ok {
			h.Fatal("FilterPolicyScope is shown without a FilterPolicy (sns.md 3.3)")
		}
		must(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "FilterPolicy", AttributeValue: new(`{"a":["b"]}`)})
		if got := h.subscriptionAttributes(arn)["FilterPolicyScope"]; got != "MessageBody" {
			h.Fatalf("FilterPolicyScope = %q, want the stored MessageBody", got)
		}
	})
}

func TestSubscribeIsIdempotentPerEndpoint(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		first := h.subscribe(topic, "sqs", queueARN, map[string]string{"RawMessageDelivery": "true"})
		if again := h.subscribe(topic, "sqs", queueARN, nil); again != first {
			h.Errorf("Subscribe again = %q, want %q", again, first)
		}
		if again := h.subscribe(topic, "sqs", queueARN, map[string]string{"RawMessageDelivery": "TRUE"}); again != first {
			h.Errorf("Subscribe with equal attributes = %q, want %q", again, first)
		}
		_, err := call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: "sqs", Endpoint: new(queueARN), Attributes: map[string]string{"RawMessageDelivery": "false"}})
		wantError(h, err, "InvalidParameter", "Invalid parameter: Attributes Reason: Subscription already exists with different attributes")
		if n := len(must(h, snsapi.OpListSubscriptions, &snsapi.ListSubscriptionsInput{}).Subscriptions); n != 1 {
			h.Errorf("ListSubscriptions has %d subscriptions, want 1", n)
		}

		// A subscription that set nothing agrees only with its defaults
		// (test_subscribe_idempotency): a value it never set is a conflict,
		// never silently dropped.
		const plain = "arn:aws:sqs:us-east-1:000000000000:plain"
		bare := h.subscribe(topic, "sqs", plain, nil)
		if again := h.subscribe(topic, "sqs", plain, map[string]string{"FilterPolicyScope": "MessageAttributes", "RawMessageDelivery": "false"}); again != bare {
			h.Errorf("Subscribe with the default attributes = %q, want %q", again, bare)
		}
		for name, value := range map[string]string{
			"RawMessageDelivery": "true",
			"FilterPolicy":       `{"store":["example_corp"]}`,
			"FilterPolicyScope":  "MessageBody",
		} {
			_, err := call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: "sqs", Endpoint: new(plain), Attributes: map[string]string{name: value}})
			wantError(h, err, "InvalidParameter", "Invalid parameter: Attributes Reason: Subscription already exists with different attributes")
		}
		attrs := must(h, snsapi.OpGetSubscriptionAttributes, &snsapi.GetSubscriptionAttributesInput{SubscriptionArn: bare}).Attributes
		if _, ok := attrs["FilterPolicy"]; ok || attrs["RawMessageDelivery"] != "false" {
			h.Errorf("attributes after refused repeats = %v, want none set", attrs)
		}
	})
}

func TestSubscribeErrors(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		fifo := h.topic("events.fifo", map[string]string{"FifoTopic": "true"})
		cases := []struct {
			topic, protocol, endpoint string
			attrs                     map[string]string
			code, message             string
		}{
			{topic, "test-protocol", "x", nil, "InvalidParameter", "Invalid parameter: Amazon SNS does not support this protocol string: test-protocol"},
			{topic, "sqs", "https://sqs.us-east-1.amazonaws.com/000000000000/orders", nil, "InvalidParameter", "Invalid parameter: SQS endpoint ARN"},
			{topic, "sqs", fifoQueueARN, nil, "InvalidParameter", "Invalid parameter: Invalid parameter: Endpoint Reason: FIFO SQS Queues can not be subscribed to standard SNS topics"},
			// Queues and functions the outbox cannot deliver to: another
			// partition, no Region, no 12-digit account.
			{topic, "sqs", "arn:aws-cn:sqs:cn-north-1:000000000000:q", nil, "InvalidParameter", "Invalid parameter: SQS endpoint ARN"},
			{topic, "sqs", "arn:aws:sqs:::q", nil, "InvalidParameter", "Invalid parameter: SQS endpoint ARN"},
			{topic, "sqs", "arn:aws:sqs:us-east-1:12345:q", nil, "InvalidParameter", "Invalid parameter: SQS endpoint ARN"},
			{topic, "lambda", "arn:aws-cn:lambda:cn-north-1:000000000000:function:f", nil, "InvalidParameter", "Invalid parameter: Lambda endpoint ARN"},
			{topic, "lambda", "arn:aws:lambda:::function:f", nil, "InvalidParameter", "Invalid parameter: Lambda endpoint ARN"},
			{topic, "sqs", queueARN, map[string]string{"Bogus": "x"}, "InvalidParameter", "Invalid parameter: Attributes Reason: AttributeName"},
			{topic, "sqs", queueARN, map[string]string{"RawMessageDelivery": "wrongvalue"}, "InvalidParameter", "Invalid parameter: Attributes Reason: RawMessageDelivery: Invalid value [wrongvalue]. Must be true or false."},
			{topic, "sqs", queueARN, map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"not-an-arn"}`}, "InvalidParameter", "Invalid parameter: Attributes Reason: RedrivePolicy: deadLetterTargetArn is an invalid arn"},
			{fifo, "sqs", fifoQueueARN, map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"` + queueARN + `"}`}, "InvalidParameter", "Invalid parameter: Attributes Reason: RedrivePolicy: must use a FIFO queue as DLQ for a FIFO Subscription to a FIFO Topic."},
			// The endpoints AWS recorded refusing (sns.md 3.1), before
			// NotImplemented for a valid number.
			{topic, "sms", "invalid-number", nil, "InvalidParameter", "Invalid SMS endpoint: invalid-number"},
			{topic, "sms", "+15--551234567", nil, "InvalidParameter", "Invalid SMS endpoint: +15--551234567"},
			{topic, "sms", "NAA+15551234567", nil, "InvalidParameter", "Invalid SMS endpoint: NAA+15551234567"},
			{topic, "sms", "+15551234567.", nil, "InvalidParameter", "Invalid SMS endpoint: +15551234567."},
			{topic, "sms", "/+15551234567", nil, "InvalidParameter", "Invalid SMS endpoint: /+15551234567"},
			{topic, "sms", "+15551234567", nil, "NotImplemented", "understudy does not implement SNS delivery to sms endpoints; subscribe an sqs, lambda, http, https, email or email-json endpoint instead. Run 'understudy coverage --service sns' for what is supported."},
			{"arn:aws:sns:us-east-1:000000000000:missing", "sqs", queueARN, nil, "NotFound", "Topic does not exist"},
		}
		for _, tc := range cases {
			_, err := call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: tc.topic, Protocol: tc.protocol, Endpoint: new(tc.endpoint), Attributes: tc.attrs})
			wantError(h, err, tc.code, tc.message)
		}
		if _, err := call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: "firehose", Endpoint: new("arn:aws:firehose:us-east-1:000000000000:deliverystream/s")}); err == nil || err.ID.Status != http.StatusNotImplemented {
			h.Errorf("firehose Subscribe = %v, want 501 NotImplemented", err)
		}
		// A queue that does not exist is accepted (sns.md 3.1).
		h.subscribe(topic, "sqs", "arn:aws:sqs:us-east-1:000000000000:nowhere", nil)
		// Nothing refused was stored, so a publish reaches the one queue.
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
		h.settle()
		if sent := h.sent.all(); len(sent) != 1 {
			h.Errorf("SendMessage ran %d times after the refused subscriptions, want 1", len(sent))
		}
	})
}

func TestSetSubscriptionAttributesErrors(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.subscribe(h.topic("events", nil), "sqs", queueARN, nil)
		for _, tc := range []struct {
			name, value, message string
		}{
			{"RawMessageDelivery", "", "Invalid parameter: RawMessageDelivery: Invalid value []. Must be true or false."},
			{"FilterPolicyScope", "RandomValue", "Invalid parameter: FilterPolicyScope: Invalid value [RandomValue]. Please use either MessageBody or MessageAttributes"},
			{"Endpoint", "x", "Invalid parameter: AttributeName"},
		} {
			_, err := call(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: tc.name, AttributeValue: new(tc.value)})
			wantError(h, err, "InvalidParameter", tc.message)
		}
		missing := strings.TrimSuffix(arn, arn[strings.LastIndex(arn, ":")+1:]) + "00000000-0000-0000-0000-000000000000"
		_, err := call(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: missing, AttributeName: "RawMessageDelivery", AttributeValue: new("true")})
		wantError(h, err, "NotFound", "Subscription does not exist")
	})
}

func TestUnsubscribeErrors(t *testing.T) {
	run(t, func(h *harness) {
		h.topic("events", nil)
		for _, tc := range []struct{ arn, message string }{
			{"", "Invalid parameter: SubscriptionArn Reason: no value for required parameter"},
			{"arn:aws:sns:us-east-1", "Invalid parameter: SubscriptionArn Reason: An ARN must have at least 6 elements, not 4"},
			{"arn:aws:sns:us-east-1:000000000000:events", "Invalid parameter: SubscriptionId"},
			{"arn:aws:sns:eu-west-1:000000000000:events:00000000-0000-0000-0000-000000000000", "Invalid parameter: SubscriptionArn"},
		} {
			_, err := call(h, snsapi.OpUnsubscribe, &snsapi.UnsubscribeInput{SubscriptionArn: tc.arn})
			wantError(h, err, "InvalidParameter", tc.message)
		}
	})
}

// TestPendingHTTPSubscriptionConfirmsAndUnsubscribesByLink covers the
// pending handshake (ADR 0015): the token is readable through the admin
// hook, ConfirmSubscription confirms with it, and the unsubscribe link
// removes the subscription.
func TestPendingHTTPSubscriptionConfirmsAndUnsubscribesByLink(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		out := must(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: "http", Endpoint: new("http://127.0.0.1:9000/hook")})
		if *out.SubscriptionArn != "pending confirmation" {
			h.Fatalf("SubscriptionArn = %q, want pending confirmation", *out.SubscriptionArn)
		}
		arn := h.subscribe(topic, "http", "http://127.0.0.1:9000/hook", nil)
		listed := must(h, snsapi.OpListSubscriptions, &snsapi.ListSubscriptionsInput{}).Subscriptions
		if len(listed) != 1 || *listed[0].SubscriptionArn != "PendingConfirmation" {
			h.Fatalf("ListSubscriptions = %+v, want one PendingConfirmation", listed)
		}
		attrs := h.subscriptionAttributes(arn)
		if attrs["PendingConfirmation"] != "true" || attrs["ConfirmationWasAuthenticated"] != "false" {
			h.Errorf("pending attributes = %v", attrs)
		}
		if got := h.topicAttributes(topic)["SubscriptionsPending"]; got != "1" {
			h.Errorf("SubscriptionsPending = %q", got)
		}
		_, err := call(h, snsapi.OpUnsubscribe, &snsapi.UnsubscribeInput{SubscriptionArn: arn})
		wantError(h, err, "InvalidParameter", "Invalid parameter: Cannot unsubscribe a subscription that is pending confirmation")

		parsed, perr := aws.ParseARN(arn)
		if perr != nil {
			h.Fatal(perr)
		}
		token, aerr := h.svc.SubscriptionToken(parsed)
		if aerr != nil {
			h.Fatalf("SubscriptionToken = %s", aerr.Message)
		}
		if len(token) != 288 {
			h.Errorf("token %q has %d characters, want 288", token, len(token))
		}
		_, err = call(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: "abc"})
		wantError(h, err, "InvalidParameter", "Invalid parameter: Token")
		_, err = call(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: strings.Repeat("0", 288)})
		wantError(h, err, "InvalidParameter", "Invalid token")
		_, err = call(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: "arn:aws:sns:eu-west-1:000000000000:events", Token: token})
		wantError(h, err, "InvalidParameter", "Invalid parameter: Topic")

		confirmed := must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: token})
		if *confirmed.SubscriptionArn != arn {
			h.Errorf("ConfirmSubscription = %q, want %q", *confirmed.SubscriptionArn, arn)
		}
		// Confirming again with the same token is idempotent.
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: token})
		attrs = h.subscriptionAttributes(arn)
		if attrs["PendingConfirmation"] != "false" || attrs["ConfirmationWasAuthenticated"] != "true" {
			h.Errorf("after a ConfirmSubscription call PendingConfirmation = %q, ConfirmationWasAuthenticated = %q, want false and true",
				attrs["PendingConfirmation"], attrs["ConfirmationWasAuthenticated"])
		}
		if _, aerr := h.svc.SubscriptionToken(parsed); aerr == nil {
			h.Error("a confirmed subscription still has a token to read")
		}

		rec, aerr := h.link("UnsubscribeLink", "/_understudy/sns/UnsubscribeLink?Action=Unsubscribe&SubscriptionArn="+url.QueryEscape(arn))
		if aerr != nil {
			h.Fatalf("UnsubscribeLink = %s %q", aerr.ID.QueryCode, aerr.Message)
		}
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), `<UnsubscribeResponse xmlns="http://sns.amazonaws.com/doc/2010-03-31/">`) {
			h.Errorf("UnsubscribeLink answered %d %s", rec.Code, rec.Body)
		}
		_, err = call(h, snsapi.OpGetSubscriptionAttributes, &snsapi.GetSubscriptionAttributesInput{SubscriptionArn: arn})
		wantError(h, err, "NotFound", "Subscription does not exist")
	})
}

func TestConfirmLinkAndTokenExpiry(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "email", "someone@example.com", nil)
		parsed, perr := aws.ParseARN(arn)
		if perr != nil {
			h.Fatal(perr)
		}
		token, aerr := h.svc.SubscriptionToken(parsed)
		if aerr != nil {
			h.Fatal(aerr.Message)
		}
		rec, aerr := h.link("ConfirmSubscriptionLink", "/_understudy/sns/ConfirmSubscriptionLink?Action=ConfirmSubscription&TopicArn="+url.QueryEscape(topic)+"&Token="+token)
		if aerr != nil {
			h.Fatalf("ConfirmSubscriptionLink = %s %q", aerr.ID.QueryCode, aerr.Message)
		}
		if !strings.Contains(rec.Body.String(), "<SubscriptionArn>"+arn+"</SubscriptionArn>") {
			h.Errorf("ConfirmSubscriptionLink answered %s", rec.Body)
		}
		// An unsigned GET confirmed it.
		if got := h.subscriptionAttributes(arn)["ConfirmationWasAuthenticated"]; got != "false" {
			h.Errorf("ConfirmationWasAuthenticated after the link = %q, want false", got)
		}

		other := h.subscribe(topic, "https", "https://127.0.0.1:9443/hook", nil)
		token = h.token(other)
		// Tokens confirm for two days of emulator time (sns.md 3.1).
		h.advance(48 * time.Hour)
		_, err := call(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: token})
		wantError(h, err, "InvalidParameter", "Invalid token")
	})
}

// TestExpiredPendingSubscriptionIsDeleted checks that a pending
// subscription is gone once its token expires, as AWS deletes it after two
// days (sns.md 3.1), and that subscribing the endpoint again starts over.
func TestExpiredPendingSubscriptionIsDeleted(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		const endpoint = "https://127.0.0.1:9443/hook"
		arn := h.subscribe(topic, "https", endpoint, nil)
		stale := h.token(arn)
		h.advance(48 * time.Hour)

		_, err := call(h, snsapi.OpGetSubscriptionAttributes, &snsapi.GetSubscriptionAttributesInput{SubscriptionArn: arn})
		wantError(h, err, "NotFound", "Subscription does not exist")
		if listed := must(h, snsapi.OpListSubscriptions, &snsapi.ListSubscriptionsInput{}).Subscriptions; len(listed) != 0 {
			h.Errorf("ListSubscriptions = %+v, want none", listed)
		}
		if listed := must(h, snsapi.OpListSubscriptionsByTopic, &snsapi.ListSubscriptionsByTopicInput{TopicArn: topic}).Subscriptions; len(listed) != 0 {
			h.Errorf("ListSubscriptionsByTopic = %+v, want none", listed)
		}
		if got := h.topicAttributes(topic)["SubscriptionsPending"]; got != "0" {
			h.Errorf("SubscriptionsPending = %q, want 0", got)
		}
		if got := h.svc.Resources(h.scope)[0].Summary; got != "0 subscriptions" {
			h.Errorf("Resources summary = %q, want 0 subscriptions", got)
		}
		// Unsubscribing it succeeds as for any subscription that is gone.
		must(h, snsapi.OpUnsubscribe, &snsapi.UnsubscribeInput{SubscriptionArn: arn})

		again := h.subscribe(topic, "https", endpoint, nil)
		if again == arn {
			h.Fatalf("Subscribe after expiry returned the expired subscription %s", arn)
		}
		fresh := h.token(again)
		if fresh == stale {
			h.Fatal("Subscribe after expiry kept the expired token")
		}
		confirmed := must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: fresh})
		if *confirmed.SubscriptionArn != again {
			h.Errorf("ConfirmSubscription = %q, want %q", *confirmed.SubscriptionArn, again)
		}
	})
}

// TestAuthenticateOnUnsubscribeRefusesTheLink checks that a subscription
// confirmed with AuthenticateOnUnsubscribe=true cannot be removed by the
// unsigned UnsubscribeURL, only by an Unsubscribe call (sns.md 3.2).
func TestAuthenticateOnUnsubscribeRefusesTheLink(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "http", "http://127.0.0.1:9000/hook", nil)
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn), AuthenticateOnUnsubscribe: new("true")})

		_, aerr := h.link("UnsubscribeLink", "/_understudy/sns/UnsubscribeLink?Action=Unsubscribe&SubscriptionArn="+url.QueryEscape(arn))
		wantError(h, aerr, "InvalidParameter", "Invalid parameter: SubscriptionArn Reason: the subscription was confirmed with AuthenticateOnUnsubscribe=true, "+
			"so only a signed Unsubscribe call can remove it; the subscription is unchanged")
		if got := h.subscriptionAttributes(arn)["PendingConfirmation"]; got != "false" {
			h.Fatalf("the refused link changed the subscription: PendingConfirmation = %q", got)
		}
		must(h, snsapi.OpUnsubscribe, &snsapi.UnsubscribeInput{SubscriptionArn: arn})
		_, err := call(h, snsapi.OpGetSubscriptionAttributes, &snsapi.GetSubscriptionAttributesInput{SubscriptionArn: arn})
		wantError(h, err, "NotFound", "Subscription does not exist")
	})
}

// TestCrossAccountQueueGetsSubscriptionConfirmation subscribes a queue of
// another account, which stays pending, and checks that the queue receives
// a signed SubscriptionConfirmation whose SubscribeURL confirms the
// subscription (sns.md 3.1, 5.5, 5.6).
func TestCrossAccountQueueGetsSubscriptionConfirmation(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		const remote = "arn:aws:sqs:us-east-1:111111111111:remote"
		out := must(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: "sqs", Endpoint: new(remote)})
		if *out.SubscriptionArn != "pending confirmation" {
			h.Fatalf("SubscriptionArn = %q, want pending confirmation", *out.SubscriptionArn)
		}
		arn := h.subscribe(topic, "sqs", remote, nil)
		h.settle()
		sent := h.sent.all()
		if len(sent) != 1 || !strings.HasSuffix(sent[0].QueueUrl, "/111111111111/remote") {
			h.Fatalf("SendMessage = %+v, want one confirmation to the remote queue", sent)
		}
		var msg struct {
			Type, MessageID, Token, TopicArn, Message, SubscribeURL, Timestamp string
			SignatureVersion, Signature, SigningCertURL                        string
		}
		if err := json.Unmarshal([]byte(sent[0].MessageBody), &msg); err != nil {
			h.Fatalf("confirmation %s: %v", sent[0].MessageBody, err)
		}
		token := h.token(arn)
		wantURL := "http://localhost:4566/_understudy/sns/ConfirmSubscriptionLink?Action=ConfirmSubscription&TopicArn=" + topic + "&Token=" + token
		if msg.Type != "SubscriptionConfirmation" || msg.Token != token || msg.TopicArn != topic || msg.SubscribeURL != wantURL || msg.SignatureVersion != "1" {
			h.Errorf("confirmation = %s", sent[0].MessageBody)
		}
		ts, err := time.Parse("2006-01-02T15:04:05.000Z", msg.Timestamp)
		if err != nil {
			h.Fatal(err)
		}
		signed := envelope.ConfirmationStringToSign(envelope.Confirmation{
			MessageID: msg.MessageID, Token: msg.Token, TopicARN: msg.TopicArn, Timestamp: ts, SubscribeURL: msg.SubscribeURL,
		})
		sig, err := base64.StdEncoding.DecodeString(msg.Signature)
		if err != nil {
			h.Fatal(err)
		}
		sum := sha1.Sum(signed) //nolint:gosec // SignatureVersion 1
		if err := rsa.VerifyPKCS1v15(servedCertificate(h, msg.SigningCertURL), crypto.SHA1, sum[:], sig); err != nil {
			h.Errorf("the confirmation signature does not verify against the served certificate: %v", err)
		}

		u, err := url.Parse(msg.SubscribeURL)
		if err != nil {
			h.Fatal(err)
		}
		if _, aerr := h.link("ConfirmSubscriptionLink", u.RequestURI()); aerr != nil {
			h.Fatalf("ConfirmSubscriptionLink = %s %q", aerr.ID.QueryCode, aerr.Message)
		}
		attrs := h.subscriptionAttributes(arn)
		if attrs["PendingConfirmation"] != "false" || attrs["ConfirmationWasAuthenticated"] != "false" {
			h.Errorf("after the SubscribeURL = %v", attrs)
		}
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
		h.settle()
		if n := len(h.sent.all()); n != 2 {
			h.Errorf("SendMessage ran %d times, want the confirmation and one notification", n)
		}
	})
}

// A FIFO queue of another account gets its SubscriptionConfirmation with
// the ids SendMessage requires of a FIFO queue: the subscription id as the
// message group and the confirmation's MessageId as the deduplication id
// (inferred, package doc). Without them the confirmation would never
// arrive.
func TestCrossAccountFIFOQueueGetsSubscriptionConfirmation(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events.fifo", map[string]string{"FifoTopic": "true"})
		const remote = "arn:aws:sqs:us-east-1:111111111111:remote.fifo"
		arn := h.subscribe(topic, "sqs", remote, nil)
		h.settle()
		sent := h.sent.all()
		if len(sent) != 1 || !strings.HasSuffix(sent[0].QueueUrl, "/111111111111/remote.fifo") {
			h.Fatalf("SendMessage = %+v, want one confirmation to the remote FIFO queue", sent)
		}
		var msg struct {
			Type, MessageID, SubscribeURL string
		}
		if err := json.Unmarshal([]byte(sent[0].MessageBody), &msg); err != nil {
			h.Fatalf("confirmation %s: %v", sent[0].MessageBody, err)
		}
		subscriptionID := arn[strings.LastIndex(arn, ":")+1:]
		group, dedup := "", ""
		if sent[0].MessageGroupId != nil {
			group = *sent[0].MessageGroupId
		}
		if sent[0].MessageDeduplicationId != nil {
			dedup = *sent[0].MessageDeduplicationId
		}
		if msg.Type != "SubscriptionConfirmation" || group != subscriptionID || dedup != msg.MessageID || msg.MessageID == "" {
			h.Errorf("confirmation group %q dedup %q, want %q and the MessageId of %s", group, dedup, subscriptionID, sent[0].MessageBody)
		}
		u, err := url.Parse(msg.SubscribeURL)
		if err != nil {
			h.Fatal(err)
		}
		if _, aerr := h.link("ConfirmSubscriptionLink", u.RequestURI()); aerr != nil {
			h.Fatalf("ConfirmSubscriptionLink = %s %q", aerr.ID.QueryCode, aerr.Message)
		}
		if attrs := h.subscriptionAttributes(arn); attrs["PendingConfirmation"] != "false" {
			h.Errorf("after the SubscribeURL = %v", attrs)
		}
	})
}

// TestLargeMessageTopicLimitsSubscriptions checks the rule of a
// MaximumMessageSize above 262144 bytes (sns.md 2.2) from both sides: the
// attribute is refused on a topic with an http subscription, and such a
// topic refuses an http subscription and a 101st one.
func TestLargeMessageTopicLimitsSubscriptions(t *testing.T) {
	run(t, func(h *harness) {
		const (
			large = "262145"
			rule  = "Invalid parameter: MaximumMessageSize Reason: a topic whose MaximumMessageSize is above 262144 bytes "
		)
		withHTTP := h.topic("hooks", nil)
		h.subscribe(withHTTP, "http", "http://127.0.0.1:9000/hook", nil)
		_, err := call(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: withHTTP, AttributeName: "MaximumMessageSize", AttributeValue: new(large)})
		wantError(h, err, "InvalidParameter", rule+"can have only sqs, lambda and firehose subscriptions, not http")
		if _, ok := h.topicAttributes(withHTTP)["MaximumMessageSize"]; ok {
			h.Error("the refused MaximumMessageSize was stored")
		}

		topic := h.topic("events", map[string]string{"MaximumMessageSize": large})
		if got := h.topicAttributes(topic)["MaximumMessageSize"]; got != large {
			h.Errorf("MaximumMessageSize = %q, want %s", got, large)
		}
		_, err = call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: "email", Endpoint: new("a@example.com")})
		wantError(h, err, "InvalidParameter", rule+"can have only sqs, lambda and firehose subscriptions, not email")
		h.subscribe(topic, "lambda", functionARN, nil)
		for i := range 99 {
			h.subscribe(topic, "sqs", queueARN+strconv.Itoa(i), nil)
		}
		_, err = call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: "sqs", Endpoint: new(queueARN + "-last")})
		wantError(h, err, "InvalidParameter", rule+"can have at most 100 subscriptions")
		if n := len(must(h, snsapi.OpListSubscriptionsByTopic, &snsapi.ListSubscriptionsByTopicInput{TopicArn: topic}).Subscriptions); n != 100 {
			h.Errorf("the topic has %d subscriptions, want 100", n)
		}
	})
}

// token reads a pending subscription's token through the admin hook.
func (h *harness) token(subscription string) string {
	h.Helper()
	parsed, err := aws.ParseARN(subscription)
	if err != nil {
		h.Fatal(err)
	}
	token, aerr := h.svc.SubscriptionToken(parsed)
	if aerr != nil {
		h.Fatalf("SubscriptionToken(%s) = %s", subscription, aerr.Message)
	}
	return token
}
