package sns_test

import (
	"bytes"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/journal"
)

// failingReplay publishes one message to an http subscription whose
// endpoint answers every notification 503, its topic's DeliveryPolicy
// topicPolicy and its own subscriptionPolicy ("" for none). It advances
// the clock by each of steps and settles after each, as
// `understudy clock advance --settle` does: an advance alone does not wait
// for the attempts its timers start (ARCHITECTURE.md 9). It returns each
// attempt's offset from the publish and the attempts.
func failingReplay(t *testing.T, topicPolicy, subscriptionPolicy string, steps ...time.Duration) ([]time.Duration, []journal.Delivery) {
	t.Helper()
	var at []time.Duration
	var ds []journal.Delivery
	s := confirmingThen(func(int) (int, http.Header) { return http.StatusServiceUnavailable, nil })
	onTheWire(t, s, func(h *harness) {
		var topicAttrs, subAttrs map[string]string
		if topicPolicy != "" {
			topicAttrs = map[string]string{"DeliveryPolicy": topicPolicy}
		}
		if subscriptionPolicy != "" {
			subAttrs = map[string]string{"DeliveryPolicy": subscriptionPolicy}
		}
		topic := h.topic("events", topicAttrs)
		t1 := subscribeConfirmed(h, s, topic, hookURL, subAttrs)
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
		h.settle()
		for _, step := range steps {
			h.advance(step)
			h.settle()
		}
		at, ds = attemptsSince(h, t1)
		if n := len(s.ofType("Notification")); n != len(ds) {
			h.Errorf("the endpoint got %d Notifications for %d journaled attempts", n, len(ds))
		}
	})
	return at, ds
}

// The DeliveryPolicy's healthyRetryPolicy, the subscription's or the
// topic's http default, drives the retry schedule on the wire: each retry
// comes its phase's delay after the failed attempt (sns-http-delivery.md
// 6.2, 6.3), the backoff phase stepping linearly (sns.http.backoff-curves)
// without jitter (sns.http.retry-jitter). Advancing with a settle replays
// it the same whether the clock moves in one step or many (8.2 item 7),
// and with no RedrivePolicy the last failure is discarded (6.5, aws-doc).
func TestHTTPRetryScheduleReplaysUnderAdvanceAndSettle(t *testing.T) {
	s := time.Second
	cases := []struct {
		name                    string
		topicPolicy, subscriber string
		want                    []time.Duration
		stepped                 []time.Duration
	}{
		{
			name: "the subscription's backoff phase", subscriber: retryPolicy(1, 10, 4, 0, 0, 0),
			want: []time.Duration{0, s, 5 * s, 12 * s, 22 * s}, stepped: []time.Duration{s, 4 * s, 7 * s, 10 * s, time.Hour},
		},
		{
			name: "the topic's http default", topicPolicy: `{"http":{"defaultHealthyRetryPolicy":{"minDelayTarget":1,"maxDelayTarget":10,"numRetries":4}}}`,
			want: []time.Duration{0, s, 5 * s, 12 * s, 22 * s}, stepped: []time.Duration{3 * s, 3 * s, 15 * s, time.Hour},
		},
		{
			name: "every phase", subscriber: retryPolicy(2, 30, 4, 1, 1, 1),
			want: []time.Duration{0, 0, 2 * s, 4 * s, 34 * s}, stepped: []time.Duration{2 * s, 2 * s, 29 * s, time.Hour},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			at, ds := failingReplay(t, c.topicPolicy, c.subscriber, time.Hour)
			if !slices.Equal(at, c.want) {
				t.Fatalf("attempts at %v, want %v", at, c.want)
			}
			for i, d := range ds {
				if d.Attempt != i+1 || d.HTTP == nil || d.HTTP.Status != http.StatusServiceUnavailable || d.HTTP.Class != journal.HTTPRetryable {
					t.Errorf("attempt %d = %+v, want attempt %d answered 503, classed retryable", i, d, i+1)
				}
				_, retrying := d.Outcome.(journal.Retrying)
				_, dropped := d.Outcome.(journal.Dropped)
				if last := i == len(ds)-1; last && !dropped || !last && !retrying {
					t.Errorf("attempt %d outcome = %#v, want Retrying before the last and Dropped for it", i+1, d.Outcome)
				}
			}
			if again, _ := failingReplay(t, c.topicPolicy, c.subscriber, time.Hour); !slices.Equal(again, at) {
				t.Errorf("a second run attempted at %v, the first at %v", again, at)
			}
			if stepped, _ := failingReplay(t, c.topicPolicy, c.subscriber, c.stepped...); !slices.Equal(stepped, at) {
				t.Errorf("advancing by %v attempted at %v, want %v", c.stepped, stepped, at)
			}
		})
	}
}

// Under sns.http.status-classes 429, 5xx and a refused connection are
// retried under the DeliveryPolicy set after the subscription was
// confirmed, and once the retries are spent the message goes to the
// RedrivePolicy queue; any other status goes there at once
// (sns-http-delivery.md 6.1, 6.5). TestHTTPStatusClassesFollowTheSwitch
// asserts the same without a settle after its advance.
func TestHTTPStatusClassesAfterASettle(t *testing.T) {
	const dlq = "arn:aws:sqs:us-east-1:000000000000:orders-dlq"
	cases := []struct {
		name     string
		endpoint string
		status   int
		retried  bool
	}{
		{"404", hookURL, http.StatusNotFound, false},
		{"301", hookURL, http.StatusMovedPermanently, false},
		{"429", hookURL, http.StatusTooManyRequests, true},
		{"502", hookURL, http.StatusBadGateway, true},
		{"a refused connection", "http://127.0.0.1:8090/hook", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := confirmingThen(func(int) (int, http.Header) { return c.status, nil })
			onTheWire(t, s, func(h *harness) {
				if v := switchValue(h, "sns.http.status-classes"); v != "retries-doc" {
					noExpectation(h, "sns.http.status-classes", v)
				}
				topic := h.topic("events", nil)
				arn := h.subscribe(topic, "http", c.endpoint, map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"` + dlq + `"}`})
				h.settle()
				must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
				must(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "DeliveryPolicy", AttributeValue: new(retryPolicy(1, 1, 1, 0, 0, 0))})
				h.advance(time.Hour)
				t1 := h.clock.Now()
				must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
				h.settle()
				h.advance(time.Minute)
				h.settle()
				at, ds := attemptsSince(h, t1)
				want := []time.Duration{0}
				if c.retried {
					want = []time.Duration{0, time.Second}
				}
				if !slices.Equal(at, want) {
					h.Fatalf("attempts at %v, want %v", at, want)
				}
				last := ds[len(ds)-1]
				if last.Outcome != (journal.DeadLettered{DLQ: mustARN(h, dlq)}) {
					h.Errorf("last attempt outcome = %#v, want dead-lettered to %s", last.Outcome, dlq)
				}
				wantClass := journal.HTTPPermanent
				if c.retried {
					wantClass = journal.HTTPRetryable
				}
				if last.HTTP == nil || last.HTTP.Class != wantClass || last.HTTP.Status != c.status {
					h.Errorf("last exchange = %+v, want status %d classed %v", last.HTTP, c.status, wantClass)
				}
				if sent := h.sent.all(); len(sent) != 1 || !strings.HasSuffix(sent[0].QueueUrl, "/orders-dlq") {
					h.Errorf("SendMessage calls = %+v, want one dead letter", sent)
				}
			})
		})
	}
}

// Under sns.http.retry-body the retry a settle lets run resends the first
// attempt's bytes and headers, MessageId included (sns-http-delivery.md
// 4.6). TestHTTPRetriesResendTheSameBytes asserts the same without a
// settle after its advance.
func TestHTTPRetryAfterASettleResendsTheSameBytes(t *testing.T) {
	s := confirmingThen(func(n int) (int, http.Header) {
		if n == 0 {
			return http.StatusServiceUnavailable, nil
		}
		return http.StatusOK, nil
	})
	onTheWire(t, s, func(h *harness) {
		if v := switchValue(h, "sns.http.retry-body"); v != "identical" {
			noExpectation(h, "sns.http.retry-body", v)
		}
		topic := h.topic("events", nil)
		t1 := subscribeConfirmed(h, s, topic, hookURL, nil)
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
		h.settle()
		h.advance(time.Hour)
		h.settle()
		ns := s.ofType("Notification")
		if len(ns) != 2 {
			h.Fatalf("the endpoint got %d Notifications, want the failed one and its retry", len(ns))
		}
		if !bytes.Equal(ns[0].body, ns[1].body) || !maps.EqualFunc(ns[0].header, ns[1].header, slices.Equal) {
			h.Errorf("the retry sent\n%v\n%s\nwant the first attempt's\n%v\n%s", ns[1].header, ns[1].body, ns[0].header, ns[0].body)
		}
		// The system default policy's first retry waits minDelayTarget, 20 s
		// (sns-http-delivery.md 6.2, 6.3).
		if at, _ := attemptsSince(h, t1); !slices.Equal(at, []time.Duration{0, 20 * time.Second}) {
			h.Errorf("attempts at %v, want [0s 20s]", at)
		}
	})
}

// throttlePolicy.maxReceivesPerSecond, the subscription's or the topic's
// http default, spaces the subscription's notifications 1/n seconds apart
// on the emulator clock, and a paced notification spends no retry
// (sns-http-delivery.md 6.4, aws-doc; 8.2 item 5).
func TestHTTPThrottlePolicyPacesNotifications(t *testing.T) {
	cases := []struct {
		name                    string
		topicPolicy, subscriber string
		want                    []time.Duration
	}{
		{"unthrottled", "", "", []time.Duration{0, 0, 0}},
		{"the subscription's", "", `{"throttlePolicy":{"maxReceivesPerSecond":2}}`, []time.Duration{0, 500 * time.Millisecond, time.Second}},
		{"the topic's http default", `{"http":{"defaultThrottlePolicy":{"maxReceivesPerSecond":4}}}`, "", []time.Duration{0, 250 * time.Millisecond, 500 * time.Millisecond}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := answering(http.StatusOK)
			onTheWire(t, s, func(h *harness) {
				var topicAttrs, subAttrs map[string]string
				if c.topicPolicy != "" {
					topicAttrs = map[string]string{"DeliveryPolicy": c.topicPolicy}
				}
				if c.subscriber != "" {
					subAttrs = map[string]string{"DeliveryPolicy": c.subscriber}
				}
				topic := h.topic("events", topicAttrs)
				t1 := subscribeConfirmed(h, s, topic, hookURL, subAttrs)
				for range 3 {
					must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
				}
				h.settle()
				h.advance(10 * time.Second)
				h.settle()
				at, ds := attemptsSince(h, t1)
				if !slices.Equal(at, c.want) {
					h.Errorf("notifications attempted at %v, want %v", at, c.want)
				}
				for i, d := range ds {
					if d.Attempt != 1 || d.Outcome != (journal.Delivered{}) {
						h.Errorf("notification %d = %+v, want attempt 1 delivered: pacing spends no retry", i, d)
					}
				}
			})
		})
	}
}
