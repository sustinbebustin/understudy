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

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
)

// genNewTarget draws an EventBus, S3Object or HTTPEndpoint target.
func genNewTarget() *rapid.Generator[delivery.Target] {
	text := rapid.StringMatching(`[A-Za-z0-9 ."{}:]{0,12}`)
	return rapid.OneOf(
		rapid.Custom(func(t *rapid.T) delivery.Target {
			entries := make([]delivery.EventEntry, rapid.IntRange(1, maxBusEntries).Draw(t, "entries"))
			for i := range entries {
				entries[i] = delivery.EventEntry{Source: text.Draw(t, "source"), DetailType: text.Draw(t, "detailType"), Detail: text.Draw(t, "detail")}
				if n := rapid.IntRange(0, 2).Draw(t, "resources"); n > 0 {
					entries[i].Resources = rapid.SliceOfN(text, n, n).Draw(t, "resource")
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
			return delivery.HTTPEndpoint{URL: *u, Headers: headers, Body: rapid.SliceOfN(rapid.Byte(), 1, 16).Draw(t, "body")}
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
			delivery.S3Object{Bucket: "audit", Key: "a/b.json", Body: []byte("hello")},
			`{"body":"aGVsbG8=","bucket":"audit","key":"a/b.json","kind":"s3-object"}`,
		},
		{
			delivery.HTTPEndpoint{URL: *hook, Headers: []delivery.HTTPHeader{{Name: "x-amz-sns-message-type", Value: "Notification"}}, Body: []byte("hello")},
			`{"body":"aGVsbG8=","headers":[{"name":"x-amz-sns-message-type","value":"Notification"}],"kind":"http-endpoint","url":"https://hooks.example.test/orders?attempt=1"}`,
		},
		{
			delivery.HTTPEndpoint{URL: *hook, Body: []byte("hello")},
			`{"body":"aGVsbG8=","kind":"http-endpoint","url":"https://hooks.example.test/orders?attempt=1"}`,
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
		Request: delivery.Request{Source: source, Mechanism: delivery.SNSToSQS, Target: delivery.HTTPEndpoint{URL: *hook, Body: []byte("b")}, Policy: delivery.None{}},
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
		{"ten entries", checkEventBus(delivery.EventBus{ARN: bus, Entries: make([]delivery.EventEntry, maxBusEntries)}), ""},
		{"no entries", checkEventBus(delivery.EventBus{ARN: bus}), "Target.Entries"},
		{"eleven entries", checkEventBus(delivery.EventBus{ARN: bus, Entries: make([]delivery.EventEntry, maxBusEntries+1)}), "Target.Entries"},
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
		got, ok := httpAttempt(t, c.policy).endHTTP(t.Context(), c.result)
		if !ok || !c.want(got) {
			t.Errorf("%s: endHTTP(%#v) = %#v, %t", c.name, c.result, got, ok)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, ok := httpAttempt(t, delivery.SNSManaged{}).endHTTP(ctx, httpAccepted{status: 200}); ok {
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

// A dead letter of an object or an HTTP delivery carries the body the
// target would have received.
func TestDeadLettersCarryTheBody(t *testing.T) {
	dlq := aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Account: "000000000000", Resource: "dlq"}
	hook, err := url.Parse("http://127.0.0.1:8080/hook")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []delivery.Target{
		delivery.S3Object{Bucket: "b", Key: "k", Body: []byte("object body")},
		delivery.HTTPEndpoint{URL: *hook, Body: []byte("object body")},
	} {
		e := &Entry{Request: delivery.Request{Mechanism: delivery.SNSToSQS, Target: target}}
		m := deadLetterMessage(e, dlq, endpointError{reason: "HTTP 404 Not Found"}, refused)
		if m.body != "object body" || m.queue != dlq || len(m.attributes) != 0 {
			t.Errorf("dead letter of %T = %+v, want the body alone in %s", target, m, dlq)
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
