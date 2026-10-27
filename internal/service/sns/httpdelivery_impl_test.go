package sns_test

import (
	"bytes"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/service/sns"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

// An http or https endpoint must be an absolute URL of its protocol with a
// host. Without the scheme AWS answers "Endpoint must match the specified
// protocol" (testdata/parity/sns/R16/05-http-endpoint-no-scheme.json,
// sns-http-delivery.md 2.1); the same text for a URL with the scheme and
// no host, or the other protocol's scheme, is inferred. Nothing is stored
// or sent.
func TestSubscribeRefusesAnHTTPEndpointThatIsNotAURLWithAHost(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		for _, c := range []struct{ protocol, endpoint string }{
			{"http", "example.com"},
			{"http", "http://"},
			{"https", "https://:8443/hook"},
			{"https", "http://127.0.0.1:8080/hook"},
			{"http", "https://127.0.0.1:8443/hook"},
		} {
			_, err := call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{TopicArn: topic, Protocol: c.protocol, Endpoint: new(c.endpoint)})
			wantError(h, err, "InvalidParameter", "Invalid parameter: Endpoint must match the specified protocol")
		}
		if listed := must(h, snsapi.OpListSubscriptionsByTopic, &snsapi.ListSubscriptionsByTopicInput{TopicArn: topic}).Subscriptions; len(listed) != 0 {
			h.Errorf("refused endpoints were stored: %+v", listed)
		}
		if n := len(h.sink.all()); n != 0 {
			h.Errorf("refused endpoints took %d deliveries", n)
		}
	})
}

// A repeated Subscribe of a pending endpoint whose attributes disagree is
// refused (sns.md 3.1) and resends nothing.
func TestResubscribingWithOtherAttributesSendsNothing(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		h.subscribe(topic, "http", hookURL, nil)
		_, err := call(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{
			TopicArn: topic, Protocol: "http", Endpoint: new(hookURL), Attributes: map[string]string{"RawMessageDelivery": "true"},
		})
		wantError(h, err, "InvalidParameter", "Invalid parameter: Attributes Reason: Subscription already exists with different attributes")
		if _, cs := ofKind(h, delivery.HTTPSubscriptionConfirmation); len(cs) != 1 {
			h.Errorf("%d SubscriptionConfirmations, want the first Subscribe's alone", len(cs))
		}
	})
}

// A subscription DeleteTopic detached is removed by its UnsubscribeURL
// without an UnsubscribeConfirmation, since its topic is gone (inferred:
// no source covers it), and a GET of a pending one's link is refused
// (sns-http-delivery.md 2.4) with nothing sent.
func TestDetachedAndPendingUnsubscribeLinksSendNothing(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		pending := h.subscribe(topic, "http", "http://127.0.0.1:8080/pending", nil)
		if _, aerr := h.link("UnsubscribeLink", "/_understudy/sns/UnsubscribeLink?Action=Unsubscribe&SubscriptionArn="+url.QueryEscape(pending)); aerr == nil {
			h.Error("the link removed a pending subscription")
		}
		arn := h.subscribe(topic, "http", hookURL, nil)
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		must(h, snsapi.OpDeleteTopic, &snsapi.DeleteTopicInput{TopicArn: topic})
		if _, aerr := h.link("UnsubscribeLink", "/_understudy/sns/UnsubscribeLink?Action=Unsubscribe&SubscriptionArn="+url.QueryEscape(arn)); aerr != nil {
			h.Fatalf("UnsubscribeLink of a detached subscription = %s %q", aerr.ID.QueryCode, aerr.Message)
		}
		_, err := call(h, snsapi.OpGetSubscriptionAttributes, &snsapi.GetSubscriptionAttributesInput{SubscriptionArn: arn})
		wantError(h, err, "NotFound", "Subscription does not exist")
		if _, us := ofKind(h, delivery.HTTPUnsubscribeConfirmation); len(us) != 0 {
			h.Errorf("%d UnsubscribeConfirmations, want none", len(us))
		}
	})
}

// With a TLS listener the UnsubscribeConfirmation's SubscribeURL and
// SigningCertURL are on it too (sns-http-delivery.md 8.3 item 3).
func TestUnsubscribeConfirmationLinksUseTheTLSListener(t *testing.T) {
	const tlsBase = "https://localhost:4567/_understudy/sns/"
	opts := harnessOptions{sns: sns.Options{TLSPort: func() (uint16, bool) { return 4567, true }}}
	runWith(t, opts, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "https", "https://127.0.0.1:9443/hook", nil)
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		if _, aerr := h.link("UnsubscribeLink", "/_understudy/sns/UnsubscribeLink?Action=Unsubscribe&SubscriptionArn="+url.QueryEscape(arn)); aerr != nil {
			h.Fatalf("UnsubscribeLink = %s %q", aerr.ID.QueryCode, aerr.Message)
		}
		_, us := ofKind(h, delivery.HTTPUnsubscribeConfirmation)
		if len(us) != 1 {
			h.Fatalf("%d UnsubscribeConfirmations, want 1", len(us))
		}
		b := parseBody(h, us[0].Body)
		if want := tlsBase + "ConfirmSubscriptionLink?Action=ConfirmSubscription&TopicArn=" + topic + "&Token=" + b.Token; b.SubscribeURL != want {
			h.Errorf("SubscribeURL = %q, want %q", b.SubscribeURL, want)
		}
		verifySignature(h, b, envelope.UnsubscribeConfirmationStringToSign(envelope.UnsubscribeConfirmation{
			MessageID: b.MessageID, Token: b.Token, TopicARN: b.TopicArn, SubscriptionARN: arn, Timestamp: timestampOf(h, b), SubscribeURL: b.SubscribeURL,
		}), tlsBase)
	})
}

// PublishBatch gives a confirmed http subscription one Notification per
// entry, each with the entry's MessageId (sns.md 4.4).
func TestPublishBatchSendsEachEntryToAnHTTPSubscription(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "http", hookURL, nil)
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		out := must(h, snsapi.OpPublishBatch, &snsapi.PublishBatchInput{TopicArn: topic, PublishBatchRequestEntries: []snsapi.PublishBatchRequestEntry{
			{Id: "a", Message: "first"}, {Id: "b", Message: "second"},
		}})
		_, ns := ofKind(h, delivery.HTTPNotification)
		if len(ns) != 2 || len(out.Successful) != 2 {
			h.Fatalf("%d Notifications for %d entries, want 2 of each", len(ns), len(out.Successful))
		}
		for i, n := range ns {
			b := parseBody(h, n.Body)
			if b.MessageID != *out.Successful[i].MessageId || b.Message != []string{"first", "second"}[i] {
				h.Errorf("Notification %d = %s, want entry %s", i, n.Body, *out.Successful[i].Id)
			}
			if got := headerMap(h, n.Headers)["x-amz-sns-message-id"]; got != b.MessageID {
				h.Errorf("Notification %d x-amz-sns-message-id = %q, want %q", i, got, b.MessageID)
			}
		}
	})
}

// A confirmed http subscription whose stored endpoint is not a URL with a
// host, which a snapshot from a build that checked only the scheme can
// hold, breaks nothing else: a publish still reaches the topic's sqs
// subscription and journals the message to the http one as Dropped before
// any attempt, and its UnsubscribeURL still removes it, journaling the
// UnsubscribeConfirmation as Dropped.
func TestAStoredEndpointThatIsNotAURLIsDropped(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		h.subscribe(topic, "sqs", "arn:aws:sqs:us-east-1:000000000000:inbox", nil)
		arn := h.subscribe(topic, "http", hookURL, nil)
		must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		for i := range sections {
			sections[i].Data = bytes.ReplaceAll(sections[i].Data, []byte(strconv.Quote(hookURL)), []byte(`"http://"`))
		}
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		if got := h.subscriptionAttributes(arn)["Endpoint"]; got != "http://" {
			h.Fatalf("Endpoint after the import = %q, want %q", got, "http://")
		}
		before := len(h.sink.all())
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
		got := h.sink.all()[before:]
		if len(got) != 1 || got[0].Request.Mechanism != delivery.SNSToSQS {
			h.Errorf("publish deliveries = %+v, want the sqs subscription's alone", got)
		}
		if _, aerr := h.link("UnsubscribeLink", "/_understudy/sns/UnsubscribeLink?Action=Unsubscribe&SubscriptionArn="+url.QueryEscape(arn)); aerr != nil {
			h.Fatalf("UnsubscribeLink = %s %q", aerr.ID.QueryCode, aerr.Message)
		}
		_, gerr := call(h, snsapi.OpGetSubscriptionAttributes, &snsapi.GetSubscriptionAttributesInput{SubscriptionArn: arn})
		wantError(h, gerr, "NotFound", "Subscription does not exist")
		var dropped int
		events := h.k.Journal().Query(journal.Filter{Kind: journal.KindDelivery, Limit: 1000}).Events
		for i := range events {
			d, ok := events[i].Detail.(journal.Delivery)
			if !ok {
				continue
			}
			if out, isDropped := d.Outcome.(journal.Dropped); isDropped {
				dropped++
				if d.Mechanism != delivery.SNSToHTTP || d.Attempt != 0 || !strings.Contains(out.Reason, `"http://"`) {
					h.Errorf("dropped = %+v, want sns-to-http at attempt 0 naming the endpoint", d)
				}
			}
		}
		if dropped != 2 {
			h.Errorf("%d Dropped deliveries, want the Notification and the UnsubscribeConfirmation", dropped)
		}
	})
}

// The UnsubscribeURL of a subscription that gets no UnsubscribeConfirmation
// makes no signing key in a scope that has none, whether the link removes
// it (a lambda subscription) or refuses (the queue owner's sqs
// subscription): only a confirmation that will be sent needs one.
func TestUnsubscribeLinkMakesAKeyOnlyForAConfirmation(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		function := h.subscribe(topic, "lambda", "arn:aws:lambda:us-east-1:000000000000:function:f", nil)
		queue := h.subscribe(topic, "sqs", "arn:aws:sqs:us-east-1:000000000000:inbox", nil)
		hasKey := func() bool {
			sections, err := h.svc.Export()
			if err != nil {
				h.Fatal(err)
			}
			for _, s := range sections {
				if bytes.Contains(s.Data, []byte(`"signing"`)) {
					return true
				}
			}
			return false
		}
		if hasKey() {
			h.Fatal("the scope has a signing key before anything was signed")
		}
		if _, aerr := h.link("UnsubscribeLink", "/_understudy/sns/UnsubscribeLink?Action=Unsubscribe&SubscriptionArn="+url.QueryEscape(function)); aerr != nil {
			h.Fatalf("UnsubscribeLink of the lambda subscription = %s %q", aerr.ID.QueryCode, aerr.Message)
		}
		if _, aerr := h.link("UnsubscribeLink", "/_understudy/sns/UnsubscribeLink?Action=Unsubscribe&SubscriptionArn="+url.QueryEscape(queue)); aerr == nil {
			h.Fatal("UnsubscribeLink removed the queue owner's sqs subscription")
		}
		if hasKey() {
			h.Error("an UnsubscribeURL that sends no UnsubscribeConfirmation made a signing key")
		}
	})
}

// Under MessageStructure=json an http or https subscription receives its
// protocol's entry, else the default, raw or enveloped
// (sns-http-delivery.md 4.5, 8.3 item 4).
func TestHTTPDeliveryTakesTheProtocolEntry(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.topic("events", nil)
		subs := []struct{ protocol, endpoint, raw, want string }{
			{"https", "https://127.0.0.1:9443/raw", "true", "for https"},
			{"http", "http://127.0.0.1:8080/enveloped", "false", "for http"},
		}
		for _, s := range subs {
			arn := h.subscribe(topic, s.protocol, s.endpoint, map[string]string{"RawMessageDelivery": s.raw})
			must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
		}
		must(h, snsapi.OpPublish, &snsapi.PublishInput{
			TopicArn: &topic, MessageStructure: new("json"),
			Message: `{"default":"for everyone","http":"for http","https":"for https"}`,
		})
		_, ns := ofKind(h, delivery.HTTPNotification)
		if len(ns) != len(subs) {
			h.Fatalf("%d Notifications, want %d", len(ns), len(subs))
		}
		for i, s := range subs {
			got := string(ns[i].Body)
			if s.raw == "false" {
				got = parseBody(h, ns[i].Body).Message
			}
			if got != s.want || !strings.HasPrefix(ns[i].URL.String(), s.protocol+"://") {
				h.Errorf("%s got %q, want %q", ns[i].URL.String(), got, s.want)
			}
		}
	})
}
