package outbox

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/store"
)

// genNewTarget draws an EventBus, S3Object or HTTPEndpoint target.
func genNewTarget() *rapid.Generator[delivery.Target] {
	text := rapid.StringMatching(`[A-Za-z0-9 ."{}:]{0,12}`)
	return rapid.OneOf(
		rapid.Custom(func(t *rapid.T) delivery.Target {
			// The record holds a list; the outbox's checks, not the
			// record, limit it to one entry.
			entries := make([]delivery.EventEntry, rapid.IntRange(1, 3).Draw(t, "entries"))
			for i := range entries {
				entries[i] = delivery.EventEntry{Source: text.Draw(t, "source"), DetailType: text.Draw(t, "detailType"), Detail: text.Draw(t, "detail")}
				if n := rapid.IntRange(0, 2).Draw(t, "resources"); n > 0 {
					entries[i].Resources = rapid.SliceOfN(text, n, n).Draw(t, "resource")
				}
				// A forwarded event keeps its envelope in the record
				// (eventbridge-buses.md 10.4.12).
				if rapid.Bool().Draw(t, "forwarded") {
					entries[i].Forwarded = genForwarded().Draw(t, "envelope")
				}
			}
			bus := genARN("events").Draw(t, "bus")
			bus.Resource = "event-bus/" + bus.Resource
			return delivery.EventBus{ARN: bus, Entries: entries}
		}),
		rapid.Custom(func(t *rapid.T) delivery.Target {
			return delivery.S3Object{
				Bucket: rapid.StringMatching(`[a-z0-9][a-z0-9.-]{2,20}`).Draw(t, "bucket"),
				Key:    rapid.StringMatching(`[A-Za-z0-9/._-]{1,20}`).Draw(t, "key"),
				Body:   rapid.SliceOfN(rapid.Byte(), 1, 16).Draw(t, "body"),
			}
		}),
		rapid.Custom(func(t *rapid.T) delivery.Target {
			raw := rapid.StringMatching(`https?://[a-z]{1,8}\.example\.test(:[1-9][0-9]{0,3})?/[a-z/]{0,8}(\?a=[0-9]{1,3})?`).Draw(t, "url")
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", raw, err)
			}
			// No headers is nil, as a record without them decodes.
			var headers []delivery.HTTPHeader
			if n := rapid.IntRange(0, 3).Draw(t, "headers"); n > 0 {
				headers = make([]delivery.HTTPHeader, n)
				for i := range headers {
					headers[i] = delivery.HTTPHeader{Name: rapid.StringMatching(`[A-Za-z-]{1,12}`).Draw(t, "name"), Value: text.Draw(t, "value")}
				}
			}
			kind := rapid.SampledFrom([]delivery.HTTPKind{delivery.HTTPNotification, delivery.HTTPSubscriptionConfirmation, delivery.HTTPUnsubscribeConfirmation}).Draw(t, "kind")
			// A raw notification's message attributes ride with the dead
			// letter only; nil for every other delivery, as a record
			// without them decodes.
			var deadLetter []delivery.MessageAttribute
			if kind == delivery.HTTPNotification {
				if attrs := genAttributes().Draw(t, "deadLetterAttributes"); len(attrs) > 0 {
					deadLetter = attrs
				}
			}
			return delivery.HTTPEndpoint{URL: *u, Kind: kind, Headers: headers, Body: rapid.SliceOfN(rapid.Byte(), 1, 16).Draw(t, "body"), DeadLetterAttributes: deadLetter}
		}),
	)
}

// The new targets survive a snapshot record: each encodes to its record
// and decodes back to an equal target.
func TestNewTargetRecordsRoundTrip(t *testing.T) {
	c, err := unions()
	if err != nil {
		t.Fatal(err)
	}
	rapid.Check(t, func(t *rapid.T) {
		target := genNewTarget().Draw(t, "target")
		data, err := c.Marshal(&requestRecord{Mechanism: delivery.SNSToSQS, Target: targetRecordOf(target)})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var back requestRecord
		if err := c.Unmarshal(data, &back); err != nil {
			t.Fatalf("Unmarshal(%s): %v", data, err)
		}
		got, rerr := targetOf(back.Target)
		if rerr != nil {
			t.Fatalf("targetOf(%s): %v", data, rerr)
		}
		if !reflect.DeepEqual(got, target) {
			t.Fatalf("round trip of %s\n got %#v\nwant %#v", data, got, target)
		}
	})
}

// The records' wire shape: the jsonx {"kind": ...} convention with keys
// sorted, bodies in base64, and resources and headers omitted when empty.
func TestNewTargetRecordsWireShape(t *testing.T) {
	c, err := unions()
	if err != nil {
		t.Fatal(err)
	}
	hook, err := url.Parse("https://hooks.example.test/orders?attempt=1")
	if err != nil {
		t.Fatal(err)
	}
	bus := aws.ARN{Partition: "aws", Service: "events", Region: "us-east-1", Account: "000000000000", Resource: "event-bus/orders"}
	cases := []struct {
		target delivery.Target
		want   string
	}{
		{
			delivery.EventBus{ARN: bus, Entries: []delivery.EventEntry{{Source: "app", DetailType: "Order", Detail: `{}`}}},
			`{"bus":"arn:aws:events:us-east-1:000000000000:event-bus/orders","entries":[{"detail":"{}","detailType":"Order","source":"app"}],"kind":"event-bus"}`,
		},
		{
			delivery.EventBus{ARN: bus, Entries: []delivery.EventEntry{{Source: "app", DetailType: "Order", Detail: `{}`, Forwarded: &delivery.Forwarded{
				ID: "e-1", Account: "111111111111", Region: "eu-west-2", Time: time.Date(2026, 10, 14, 7, 14, 15, 0, time.UTC),
			}}}},
			`{"bus":"arn:aws:events:us-east-1:000000000000:event-bus/orders","entries":[{"detail":"{}","detailType":"Order",` +
				`"forwarded":{"account":"111111111111","id":"e-1","region":"eu-west-2","time":"2026-10-14T07:14:15Z"},"source":"app"}],"kind":"event-bus"}`,
		},
		{
			delivery.S3Object{Bucket: "audit", Key: "a/b.json", Body: []byte("hello")},
			`{"body":"aGVsbG8=","bucket":"audit","key":"a/b.json","kind":"s3-object"}`,
		},
	}
	for _, tc := range cases {
		data, err := c.Marshal(&requestRecord{Mechanism: delivery.SNSToSQS, Target: targetRecordOf(tc.target)})
		if err != nil {
			t.Fatalf("Marshal(%#v): %v", tc.target, err)
		}
		if !strings.Contains(string(data), `"target":`+tc.want) {
			t.Errorf("record of %#v\n%s\ndoes not hold %s", tc.target, data, tc.want)
		}
	}
	// An HTTP endpoint's record holds its URL, body, headers when there are
	// any, the delivery kind by its name, and the dead letter's attributes
	// only when there are any.
	httpCases := []struct {
		target        delivery.HTTPEndpoint
		holds, lacks  []string
		kindName      string
		deadLetterSet bool
	}{
		{
			target: delivery.HTTPEndpoint{URL: *hook, Kind: delivery.HTTPNotification, Headers: []delivery.HTTPHeader{{Name: "x-amz-sns-message-type", Value: "Notification"}}, Body: []byte("hello")},
			holds:  []string{`"kind":"http-endpoint"`, `"body":"aGVsbG8="`, `"url":"https://hooks.example.test/orders?attempt=1"`, `"headers":[{"name":"x-amz-sns-message-type","value":"Notification"}]`},
			lacks:  []string{`"deadLetterAttributes"`},
		},
		{
			target: delivery.HTTPEndpoint{URL: *hook, Kind: delivery.HTTPSubscriptionConfirmation, Body: []byte("hello")},
			holds:  []string{`"kind":"http-endpoint"`, `"body":"aGVsbG8="`, `"SubscriptionConfirmation"`},
			lacks:  []string{`"headers"`, `"deadLetterAttributes"`},
		},
		{
			target: delivery.HTTPEndpoint{URL: *hook, Kind: delivery.HTTPNotification, Body: []byte("hello"), DeadLetterAttributes: []delivery.MessageAttribute{{Name: "store", Value: delivery.StringAttribute{Value: "north"}}}},
			holds:  []string{`"kind":"http-endpoint"`, `"deadLetterAttributes":[`, `"store"`, `"north"`, `"Notification"`},
		},
	}
	for _, tc := range httpCases {
		data, err := c.Marshal(&requestRecord{Mechanism: delivery.SNSToHTTP, Target: targetRecordOf(tc.target)})
		if err != nil {
			t.Fatalf("Marshal(%#v): %v", tc.target, err)
		}
		for _, want := range tc.holds {
			if !strings.Contains(string(data), want) {
				t.Errorf("record of %#v\n%s\ndoes not hold %s", tc.target, data, want)
			}
		}
		for _, unwanted := range tc.lacks {
			if strings.Contains(string(data), unwanted) {
				t.Errorf("record of %#v\n%s\nholds %s", tc.target, data, unwanted)
			}
		}
	}
}

// The sns-http policy record has every member, the delays in whole
// milliseconds, and reads back as the policy it came from.
func TestSNSHTTPPolicyRecord(t *testing.T) {
	c, err := unions()
	if err != nil {
		t.Fatal(err)
	}
	def := delivery.SNSHTTP{Backoff: 3, MinDelayTarget: 20 * time.Second, MaxDelayTarget: 20 * time.Second, BackoffFunction: delivery.BackoffLinear}
	data, err := c.Marshal(&requestRecord{Mechanism: delivery.SNSToHTTP, Policy: policyRecordOf(def)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	const want = `"policy":{"backoff":3,"backoffFunction":"linear","kind":"sns-http","maxDelay":0,"maxDelayTargetMillis":20000,"minDelay":0,"minDelayTargetMillis":20000,"noDelay":0}`
	if !strings.Contains(string(data), want) {
		t.Errorf("record\n%s\ndoes not hold %s", data, want)
	}
	// Every member is required.
	missing := strings.Replace(string(data), `"noDelay":0`, `"noDelayX":0`, 1)
	var back requestRecord
	if err := c.Unmarshal([]byte(missing), &back); err == nil {
		if _, ok := policyOf(back.Policy); ok {
			t.Errorf("a record without noDelay decoded: %s", missing)
		}
	}
}

func TestSNSHTTPPolicyRecordRoundTrips(t *testing.T) {
	c, err := unions()
	if err != nil {
		t.Fatal(err)
	}
	rapid.Check(t, func(t *rapid.T) {
		minMs := rapid.IntRange(1000, 3_600_000).Draw(t, "minDelayTargetMillis")
		p := delivery.SNSHTTP{
			NoDelay:         rapid.IntRange(0, 25).Draw(t, "noDelay"),
			MinDelay:        rapid.IntRange(0, 25).Draw(t, "minDelay"),
			Backoff:         rapid.IntRange(0, 25).Draw(t, "backoff"),
			MaxDelay:        rapid.IntRange(0, 25).Draw(t, "maxDelay"),
			MinDelayTarget:  time.Duration(minMs) * time.Millisecond,
			MaxDelayTarget:  time.Duration(rapid.IntRange(minMs, 3_600_000).Draw(t, "maxDelayTargetMillis")) * time.Millisecond,
			BackoffFunction: rapid.SampledFrom([]delivery.BackoffFunction{delivery.BackoffLinear, delivery.BackoffArithmetic, delivery.BackoffGeometric, delivery.BackoffExponential}).Draw(t, "function"),
		}
		data, err := c.Marshal(&requestRecord{Mechanism: delivery.SNSToHTTP, Policy: policyRecordOf(p)})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var back requestRecord
		if err := c.Unmarshal(data, &back); err != nil {
			t.Fatalf("Unmarshal(%s): %v", data, err)
		}
		if got, ok := policyOf(back.Policy); !ok || got != p {
			t.Fatalf("round trip of %s = %#v, %t; want %#v", data, got, ok, p)
		}
	})
}

// A loaded record goes through Enqueue's checks, so a new target no
// mechanism delivers to is refused on load as it is on Enqueue.
func TestLoadRefusesNewTargetsNoMechanismDeliversTo(t *testing.T) {
	c := codec()
	hook, err := url.Parse("http://127.0.0.1:8080/hook")
	if err != nil {
		t.Fatal(err)
	}
	source := aws.ARN{Partition: "aws", Service: "sns", Region: "us-east-1", Account: "000000000000", Resource: "events"}
	st := newState(aws.Scope{})
	st.Entries["d-1"] = Entry{
		ID:      "d-1",
		Request: delivery.Request{Source: source, Mechanism: delivery.SNSToSQS, Target: delivery.HTTPEndpoint{URL: *hook, Kind: delivery.HTTPNotification, Body: []byte("b")}, Policy: delivery.None{}},
	}
	data, err := c.Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back := newState(aws.Scope{})
	err = c.Unmarshal(data, &back)
	ee, ok := errors.AsType[*EntryError](err)
	if !ok || ee.ID != "d-1" || ee.Err.Field != "Target" || !strings.Contains(ee.Err.Reason, "HTTPEndpoint") {
		t.Errorf("Unmarshal = %v, want an *EntryError for d-1's Target naming HTTPEndpoint", err)
	}
}

// The checks a new target meets once a mechanism delivers to it.
func TestNewTargetChecks(t *testing.T) {
	bus := aws.ARN{Partition: "aws", Service: "events", Region: "us-east-1", Account: "000000000000", Resource: "event-bus/orders"}
	entry := delivery.EventEntry{Source: "app", DetailType: "Order", Detail: `{}`}
	parse := func(raw string) url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return *u
	}
	cases := []struct {
		name  string
		err   *RequestError
		field string
	}{
		{"a bus", checkEventBus(delivery.EventBus{ARN: bus, Entries: []delivery.EventEntry{entry}}), ""},
		{"no entries", checkEventBus(delivery.EventBus{ARN: bus}), "Target.Entries"},
		{"two entries", checkEventBus(delivery.EventBus{ARN: bus, Entries: []delivery.EventEntry{entry, entry}}), "Target.Entries"},
		{"a rule, not a bus", checkEventBus(delivery.EventBus{ARN: aws.ARN{Partition: "aws", Service: "events", Region: "us-east-1", Account: "000000000000", Resource: "rule/r"}, Entries: []delivery.EventEntry{entry}}), "Target.ARN"},
		{"a bus with no name", checkEventBus(delivery.EventBus{ARN: aws.ARN{Partition: "aws", Service: "events", Region: "us-east-1", Account: "000000000000", Resource: "event-bus/"}, Entries: []delivery.EventEntry{entry}}), "Target.ARN"},
		{"a queue, not a bus", checkEventBus(delivery.EventBus{ARN: aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Account: "000000000000", Resource: "event-bus/orders"}, Entries: []delivery.EventEntry{entry}}), "Target.ARN"},
		{"an object", checkS3Object(delivery.S3Object{Bucket: "b", Key: "k"}), ""},
		{"no bucket", checkS3Object(delivery.S3Object{Key: "k"}), "Target.Bucket"},
		{"no key", checkS3Object(delivery.S3Object{Bucket: "b"}), "Target.Key"},
		{"http", checkHTTPEndpoint(delivery.HTTPEndpoint{URL: parse("http://127.0.0.1:8080/")}), ""},
		{"https", checkHTTPEndpoint(delivery.HTTPEndpoint{URL: parse("https://hooks.example.test/")}), ""},
		{"ftp", checkHTTPEndpoint(delivery.HTTPEndpoint{URL: parse("ftp://hooks.example.test/")}), "Target.URL"},
		{"no host", checkHTTPEndpoint(delivery.HTTPEndpoint{URL: parse("http:///hook")}), "Target.URL"},
		{"a header", checkHTTPEndpoint(delivery.HTTPEndpoint{URL: parse("http://127.0.0.1:8080/"), Headers: []delivery.HTTPHeader{{Name: "x-amz-sns-message-type", Value: "Notification"}}}), ""},
		{"a lower-case content-length", checkHTTPEndpoint(delivery.HTTPEndpoint{URL: parse("http://127.0.0.1:8080/"), Headers: []delivery.HTTPHeader{{Name: "x-amz-sns-message-type", Value: "Notification"}, {Name: "content-length", Value: "0"}}}), "Target.Headers[1]"},
		{"a transfer-encoding", checkHTTPEndpoint(delivery.HTTPEndpoint{URL: parse("http://127.0.0.1:8080/"), Headers: []delivery.HTTPHeader{{Name: "Transfer-Encoding", Value: "chunked"}}}), "Target.Headers[0]"},
		{"a host", checkHTTPEndpoint(delivery.HTTPEndpoint{URL: parse("http://127.0.0.1:8080/"), Headers: []delivery.HTTPHeader{{Name: "HOST", Value: "elsewhere.test"}}}), "Target.Headers[0]"},
		{"a connection", checkHTTPEndpoint(delivery.HTTPEndpoint{URL: parse("http://127.0.0.1:8080/"), Headers: []delivery.HTTPHeader{{Name: "connection", Value: "keep-alive"}}}), "Target.Headers[0]"},
	}
	for _, c := range cases {
		switch {
		case c.field == "" && c.err != nil:
			t.Errorf("%s: refused with %v", c.name, c.err)
		case c.field != "" && (c.err == nil || c.err.Field != c.field):
			t.Errorf("%s: = %v, want a refusal of %s", c.name, c.err, c.field)
		}
	}
}

// httpAttempt is an attempt of an HTTPEndpoint delivery under policy with
// no dead-letter queue, due at the epoch.
func httpAttempt(t *testing.T, policy delivery.RetryPolicy) *attempt {
	t.Helper()
	hook, err := url.Parse("http://127.0.0.1:8080/hook")
	if err != nil {
		t.Fatal(err)
	}
	at := clock.FromWire(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	return &attempt{
		entry: Entry{
			ID: ids.DeliveryID("d-1"),
			Request: delivery.Request{
				Source:    aws.ARN{Partition: "aws", Service: "sns", Region: "us-east-1", Account: "000000000000", Resource: "events"},
				Mechanism: delivery.SNSToSQS,
				Target:    delivery.HTTPEndpoint{URL: *hook, Body: []byte("b")},
				Policy:    policy,
			},
			Accepted: at,
		},
		due: at,
	}
}

// An HTTP attempt's answer becomes its journal outcome: 2xx delivers, a
// retryable answer retries while the policy allows, a permanent one ends
// at once (sns-http-delivery.md 6.1), and a refusal is Refused, never
// retried or dead-lettered (7.3).
func TestEndHTTPOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		policy delivery.RetryPolicy
		result httpResult
		want   func(journal.DeliveryOutcome) bool
	}{
		{"accepted", delivery.SNSManaged{}, httpAccepted{status: 204}, func(o journal.DeliveryOutcome) bool { return o == journal.Delivered{} }},
		{"refused", delivery.SNSManaged{}, httpRefused{reason: "egress refused it"}, func(o journal.DeliveryOutcome) bool {
			return o == journal.Refused{Reason: "egress refused it"}
		}},
		{"retried", delivery.SNSManaged{}, httpRetry{reason: "HTTP 503 Service Unavailable"}, func(o journal.DeliveryOutcome) bool {
			_, ok := o.(journal.Retrying)
			return ok
		}},
		{"retries exhausted", delivery.None{}, httpRetry{reason: "HTTP 503 Service Unavailable"}, func(o journal.DeliveryOutcome) bool {
			return o == journal.Dropped{Reason: "no dead-letter queue is set and its retries are exhausted; the last attempt failed with HTTP 503 Service Unavailable"}
		}},
		{"permanent with retries left", delivery.SNSManaged{}, httpPermanent{status: 404}, func(o journal.DeliveryOutcome) bool {
			return o == journal.Dropped{Reason: "no dead-letter queue is set and the endpoint's answer is a permanent failure, which is not retried: HTTP 404 Not Found"}
		}},
	}
	for _, c := range cases {
		got, ok := httpAttempt(t, c.policy).endHTTP(t.Context(), c.result, 0)
		if !ok || !c.want(got) {
			t.Errorf("%s: endHTTP(%#v) = %#v, %t", c.name, c.result, got, ok)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, ok := httpAttempt(t, delivery.SNSManaged{}).endHTTP(ctx, httpAccepted{status: 200}, 0); ok {
		t.Errorf("endHTTP after the kernel stopped = %#v, want no outcome", got)
	}
}

// A 4xx other than 429 is a permanent failure under the retries page's
// reading, which sns-http-delivery.md 6.1 recommends; the endpoint
// preparation page conflicts, and the class is UNVERIFIED (SNSH-05).
func TestPostHTTPClassifiesA404AsPermanent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := &endpoint{status: http.StatusNotFound}
		got := postHTTP(t.Context(), egressTo(t, e), hook(t, "http://127.0.0.1:8080/hook"))
		synctest.Wait()
		if r, ok := got.(httpPermanent); !ok || r.status != http.StatusNotFound {
			t.Errorf("postHTTP to a 404 = %#v, want permanent 404", got)
		}
	})
}

// A dead letter of an HTTP delivery carries the body the endpoint would
// have received. understudy's choice: the form is UNVERIFIED, and
// sns-http-delivery.md 6.5 gives SQS's as the likely one (SNSH-08).
func TestHTTPDeadLetterCarriesTheBody(t *testing.T) {
	dlq := aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Account: "000000000000", Resource: "dlq"}
	hook, err := url.Parse("http://127.0.0.1:8080/hook")
	if err != nil {
		t.Fatal(err)
	}
	e := &Entry{Request: delivery.Request{Mechanism: delivery.SNSToSQS, Target: delivery.HTTPEndpoint{URL: *hook, Body: []byte("hook body")}}}
	m := deadLetterMessage(e, dlq, endpointError{reason: "HTTP 404 Not Found"}, refused)
	if m.body != "hook body" || m.queue != dlq || len(m.attributes) != 0 {
		t.Errorf("dead letter = %+v, want the body alone in %s", m, dlq)
	}
}

// A dead-letter queue is refused for a target whose dead letter has no
// researched form, an event bus under any mechanism but events-to-events
// or an S3 object, and taken for an HTTP endpoint and for an event bus
// target, which is an EventBridge target like any other (eb-rule-dlq.html,
// iam-enforcement-surfaces.md 8.4).
func TestCheckDLQ(t *testing.T) {
	dlq := aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Account: "000000000000", Resource: "dlq"}
	hook, err := url.Parse("http://127.0.0.1:8080/hook")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		mechanism delivery.Mechanism
		target    delivery.Target
		refused   bool
	}{
		{delivery.SNSToSQS, busCode(""), true},
		{delivery.EventsToSQS, busCode(""), true},
		{delivery.EventsToEvents, busCode(""), false},
		{delivery.SNSToSQS, delivery.S3Object{Bucket: "b", Key: "k", Body: []byte("b")}, true},
		{delivery.SNSToSQS, delivery.HTTPEndpoint{URL: *hook, Body: []byte("b")}, false},
	}
	for _, c := range cases {
		err := checkDLQ(delivery.Request{Mechanism: c.mechanism, Target: c.target, DLQ: &dlq})
		if refused := err != nil && err.Field == "DLQ"; refused != c.refused {
			t.Errorf("checkDLQ(%T under %s with a queue) = %v, want refused %t", c.target, c.mechanism, err, c.refused)
		}
	}
	fn := delivery.LambdaFunction{ARN: aws.ARN{Partition: "aws", Service: "lambda", Region: "us-east-1", Account: "000000000000", Resource: "function:f"}}
	built := &delivery.DeadLetterMessage{Body: "{}"}
	for _, c := range []struct {
		req     delivery.Request
		refused bool
	}{
		{delivery.Request{Mechanism: delivery.SNSToLambda, Target: fn, DLQ: &dlq, DLQMessage: built}, false},
		{delivery.Request{Mechanism: delivery.SNSToLambda, Target: fn, DLQMessage: built}, true},
		{delivery.Request{Mechanism: delivery.EventsToLambda, Target: fn, DLQ: &dlq, DLQMessage: built}, true},
	} {
		err := checkDLQ(c.req)
		if refused := err != nil && err.Field == "DLQMessage"; refused != c.refused {
			t.Errorf("checkDLQ(a built dead letter under %s, DLQ %v) = %v, want refused %t", c.req.Mechanism, c.req.DLQ, err, c.refused)
		}
	}
}

// An object delivery lands in the object, in the source's partition; an
// HTTP delivery in no ARN, its endpoint named instead without path, query
// or userinfo, the port defaulted by scheme.
func TestDeliveredToAndEndpoint(t *testing.T) {
	source := aws.ARN{Partition: "aws-cn", Service: "sns", Region: "cn-north-1", Account: "000000000000", Resource: "events"}
	obj := delivery.Request{Source: source, Target: delivery.S3Object{Bucket: "audit", Key: "a/b.json"}}
	if got, want := deliveredTo(obj), (aws.ARN{Partition: "aws-cn", Service: "s3", Resource: "audit/a/b.json"}); got != want {
		t.Errorf("deliveredTo(S3Object) = %s, want %s", got, want)
	}
	if got := endpointOf(obj.Target); got != "" {
		t.Errorf("endpointOf(S3Object) = %q, want none", got)
	}
	for raw, want := range map[string]string{
		"https://alice:secret@hooks.example.test/x?y=1": "hooks.example.test:443",
		"http://hooks.example.test/x":                   "hooks.example.test:80",
		"http://[::1]:8080/":                            "[::1]:8080",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		req := delivery.Request{Source: source, Target: delivery.HTTPEndpoint{URL: *u}}
		if got := endpointOf(req.Target); got != want {
			t.Errorf("endpointOf(%s) = %q, want %q", raw, got, want)
		}
		if got := deliveredTo(req); got != (aws.ARN{}) {
			t.Errorf("deliveredTo(%s) = %s, want the zero ARN", raw, got)
		}
	}
}

// plant makes req pending and arms its first attempt now, as Enqueue does,
// without Enqueue's mechanism check: no mechanism delivers to the new
// targets until their producers add one, so Enqueue refuses them today.
func plant(k *kerneltest.K, o *Outbox, req delivery.Request) {
	k.Helper()
	e := o.e.Load()
	in, ok := targetARN(req.Target)
	if !ok {
		in = req.Source
	}
	scope, serr := scopeOf(in)
	if serr != nil {
		k.Fatal(serr)
	}
	now := k.Clock.Now()
	entry := Entry{ID: e.k.IDs().DeliveryID(), Request: req, NextAt: now, Accepted: now, Seq: e.seq.Add(1)}
	store.Update(e.cells.For(scope), func(st *State) (struct{}, bool) {
		st.Entries[entry.ID] = entry
		e.k.Timers().Scheduler().Arm(e.attempt.Key(timerID(scope, entry.ID)), now)
		return struct{}{}, true
	})
}

// busCode is an EventBus target of one entry whose DetailType names the
// code the PutEvents stub answers for it: "" puts the entry.
func busCode(code string) delivery.EventBus {
	return delivery.EventBus{ARN: busARN(), Entries: []delivery.EventEntry{{Source: "app", DetailType: code, Detail: `{}`}}}
}

// Each new target runs the whole attempt path: the timer fires, the
// attempt makes its call, an HTTP one on its own goroutine, and finish
// journals a Delivery naming where it landed. A failed EventBus entry ends
// by its code (API_PutEventsResultEntry): a code AWS lists as
// non-retryable is not retried, InternalFailure is, and understudy's own
// NotImplemented is dropped as unimplemented.
func TestNewTargetsRunTheAttemptPath(t *testing.T) {
	source := aws.ARN{Partition: "aws", Service: "sns", Region: "us-east-1", Account: "000000000000", Resource: "events"}
	object := aws.ARN{Partition: "aws", Service: "s3", Resource: "audit/k"}
	cases := []struct {
		name   string
		target delivery.Target
		// calls is how many target calls the attempts made before the
		// clock moved: more than one is a retry.
		calls func(n int) bool
		want  func(d journal.Delivery) bool
	}{
		{"bus entry put", busCode(""), one, func(d journal.Delivery) bool {
			return d.To == busARN() && d.Endpoint == "" && d.Outcome == journal.Delivered{}
		}},
		{"bus entry refused", busCode("MalformedDetail"), one, func(d journal.Delivery) bool {
			r, ok := d.Outcome.(journal.Dropped)
			return ok && d.To == busARN() && strings.Contains(r.Reason, "not retried") && strings.Contains(r.Reason, "MalformedDetail: the stub failed it")
		}},
		{"bus entry unimplemented", busCode("NotImplemented"), one, func(d journal.Delivery) bool {
			r, ok := d.Outcome.(journal.Dropped)
			return ok && strings.HasPrefix(r.Reason, "dev.understudy#NotImplemented: 1 of 1 entries failed; the first with NotImplemented")
		}},
		{"bus entry retried", busCode("InternalFailure"), func(n int) bool { return n > 1 }, func(d journal.Delivery) bool {
			_, ok := d.Outcome.(journal.Retrying)
			return ok && d.To == busARN()
		}},
		{"object", delivery.S3Object{Bucket: "audit", Key: "k", Body: []byte("b")}, one, func(d journal.Delivery) bool {
			return d.To == object && d.Endpoint == "" && d.Outcome == journal.Delivered{}
		}},
		{"endpoint", hook(t, "http://127.0.0.1:8080/hook"), one, func(d journal.Delivery) bool {
			return d.To == (aws.ARN{}) && d.Endpoint == "127.0.0.1:8080" && d.Outcome == journal.Delivered{}
		}},
		{"endpoint egress refuses", hook(t, "http://10.9.9.9:8080/hook"), none, func(d journal.Delivery) bool {
			r, ok := d.Outcome.(journal.Refused)
			return ok && d.To == (aws.ARN{}) && d.Endpoint == "10.9.9.9:8080" && strings.Contains(r.Reason, "10.9.9.9")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kerneltest.Run(t, func(k *kerneltest.K) {
				var made calls[string]
				k.Stub(eventbridgeapi.OpPutEvents, func(_ context.Context, _ *kernel.Call, in *eventbridgeapi.PutEventsInput) (*eventbridgeapi.PutEventsOutput, *awserr.Error) {
					out := &eventbridgeapi.PutEventsOutput{}
					for _, e := range in.Entries {
						made.add(val(e.DetailType))
						if code := val(e.DetailType); code != "" {
							out.FailedEntryCount++
							out.Entries = append(out.Entries, eventbridgeapi.PutEventsResultEntry{ErrorCode: new(code), ErrorMessage: new("the stub failed it")})
							continue
						}
						out.Entries = append(out.Entries, eventbridgeapi.PutEventsResultEntry{EventId: new("event-0")})
					}
					return out, nil
				})
				k.Stub(s3api.OpPutObject, func(_ context.Context, _ *kernel.Call, in *s3api.PutObjectInput) (*s3api.PutObjectOutput, *awserr.Error) {
					made.add(in.Bucket + "/" + in.Key)
					return &s3api.PutObjectOutput{}, nil
				})
				receiver := &endpoint{status: http.StatusOK}
				o := New()
				if err := o.Bind(k.Kernel, Options{Faults: k.Faults, Egress: egressTo(k.T, receiver)}); err != nil {
					k.Fatal(err)
				}
				plant(k, o, delivery.Request{Source: source, Mechanism: delivery.SNSToSQS, Target: c.target, Policy: delivery.SNSManaged{}})
				if p, err := k.Kernel.Settle(k.Context(), time.Minute); err != nil {
					k.Fatalf("Settle = %v, %v", p, err)
				}
				receiver.mu.Lock()
				n := len(made.all()) + len(receiver.reqs)
				receiver.mu.Unlock()
				if !c.calls(n) {
					k.Errorf("the attempts made %d target calls", n)
				}
				events := k.Journal.Query(journal.Filter{Kind: journal.KindDelivery, Limit: 100}).Events
				if len(events) == 0 {
					k.Fatal("no Delivery event was journaled")
				}
				if d, ok := events[0].Detail.(journal.Delivery); !ok || !c.want(d) {
					k.Errorf("first Delivery event = %#v", events[0].Detail)
				}
			}, kerneltest.Services(eventbridgeapi.Service(), s3api.Service()))
		})
	}
}

func one(n int) bool { return n == 1 }

func none(n int) bool { return n == 0 }
