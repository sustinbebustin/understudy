package sns

import (
	"strconv"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// storedSubscriptionPolicy is the DeliveryPolicy a subscription stores for
// policy, "" for none, as SetSubscriptionAttributes stores it.
func storedSubscriptionPolicy(tb testing.TB, policy string) string {
	tb.Helper()
	if policy == "" {
		return ""
	}
	m, ok := parseObject(policy)
	if !ok {
		tb.Fatalf("%s is not a JSON object", policy)
	}
	stored, ok := storedSubscriptionDeliveryPolicy(m)
	if !ok {
		tb.Fatalf("storedSubscriptionDeliveryPolicy(%s) does not encode", policy)
	}
	return stored
}

// storedTopicPolicy is the DeliveryPolicy a topic stores for policy, ""
// for none, as SetTopicAttributes stores it.
func storedTopicPolicy(tb testing.TB, policy string) string {
	tb.Helper()
	if policy == "" {
		return ""
	}
	m, ok := parseObject(policy)
	if !ok {
		tb.Fatalf("%s is not a JSON object", policy)
	}
	stored, ok := storedDeliveryPolicy(m)
	if !ok {
		tb.Fatalf("storedDeliveryPolicy(%s) does not encode", policy)
	}
	return stored
}

// The rules httpRetryPolicy infers where no source speaks
// (sns-http-delivery.md 6.2, 6.3): a member AWS would not accept takes the
// system default's value, the phases take numRetries in order and are
// capped, maxDelayTarget is raised to minDelayTarget, and the members no
// source gives an effect change nothing.
func TestHTTPRetryPolicyInferredRules(t *testing.T) {
	s := time.Second
	system := delivery.SNSHTTP{Backoff: 3, MinDelayTarget: 20 * s, MaxDelayTarget: 20 * s, BackoffFunction: delivery.BackoffLinear}
	cases := []struct {
		name              string
		topic, subscriber string
		want              delivery.SNSHTTP
	}{
		{
			"a member that is not an integer takes the system default's, not the topic's",
			`{"http":{"defaultHealthyRetryPolicy":{"numRetries":6,"minDelayTarget":2,"maxDelayTarget":30}}}`,
			`{"healthyRetryPolicy":{"numRetries":"5","minDelayTarget":2.5}}`,
			delivery.SNSHTTP{Backoff: 3, MinDelayTarget: 20 * s, MaxDelayTarget: 30 * s, BackoffFunction: delivery.BackoffLinear},
		},
		{
			"a backoffFunction in any case", "", `{"healthyRetryPolicy":{"backoffFunction":"Geometric"}}`,
			delivery.SNSHTTP{Backoff: 3, MinDelayTarget: 20 * s, MaxDelayTarget: 20 * s, BackoffFunction: delivery.BackoffGeometric},
		},
		{"a backoffFunction that names none", "", `{"healthyRetryPolicy":{"backoffFunction":3}}`, system},
		{
			"phases capped at the subscription's numRetries in order",
			`{"http":{"defaultHealthyRetryPolicy":{"numRetries":6,"numNoDelayRetries":1,"numMinDelayRetries":2,"numMaxDelayRetries":3}}}`,
			`{"healthyRetryPolicy":{"numRetries":2}}`,
			delivery.SNSHTTP{NoDelay: 1, MinDelay: 1, MinDelayTarget: 20 * s, MaxDelayTarget: 20 * s, BackoffFunction: delivery.BackoffLinear},
		},
		{
			"a maxDelayTarget below minDelayTarget raised to it", "", `{"healthyRetryPolicy":{"minDelayTarget":30}}`,
			delivery.SNSHTTP{Backoff: 3, MinDelayTarget: 30 * s, MaxDelayTarget: 30 * s, BackoffFunction: delivery.BackoffLinear},
		},
		{"a minDelayTarget above 3600 takes the system default's", "", `{"healthyRetryPolicy":{"minDelayTarget":4000}}`, system},
		{
			"a null throttlePolicy takes the topic's", `{"http":{"defaultThrottlePolicy":{"maxReceivesPerSecond":5}}}`, `{"throttlePolicy":null}`,
			delivery.SNSHTTP{Backoff: 3, MinDelayTarget: 20 * s, MaxDelayTarget: 20 * s, BackoffFunction: delivery.BackoffLinear, MaxReceivesPerSecond: 5},
		},
		{
			"the subscription's throttlePolicy over the topic's", `{"http":{"defaultThrottlePolicy":{"maxReceivesPerSecond":5}}}`, `{"throttlePolicy":{"maxReceivesPerSecond":2}}`,
			delivery.SNSHTTP{Backoff: 3, MinDelayTarget: 20 * s, MaxDelayTarget: 20 * s, BackoffFunction: delivery.BackoffLinear, MaxReceivesPerSecond: 2},
		},
		{
			"sicklyRetryPolicy, guaranteed and disableSubscriptionOverrides change nothing",
			`{"http":{"defaultHealthyRetryPolicy":{"numRetries":1},"defaultSicklyRetryPolicy":{"numRetries":9},"disableSubscriptionOverrides":true}}`,
			`{"healthyRetryPolicy":{"numRetries":2},"sicklyRetryPolicy":{"numRetries":7},"guaranteed":true}`,
			delivery.SNSHTTP{Backoff: 2, MinDelayTarget: 20 * s, MaxDelayTarget: 20 * s, BackoffFunction: delivery.BackoffLinear},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := httpRetryPolicy(storedSubscriptionPolicy(t, c.subscriber), storedTopicPolicy(t, c.topic)); got != c.want {
				t.Errorf("httpRetryPolicy = %+v\nwant %+v", got, c.want)
			}
		})
	}
	if got := httpRetryPolicy("not json", "neither"); got != system {
		t.Errorf("httpRetryPolicy of policies that do not parse = %+v, want the system default %+v", got, system)
	}
}

// TestHTTPRetryPolicyStaysInTheOutboxBounds checks over random
// subscription and topic policies the write checks accept that the
// resolved policy keeps every bound the outbox's Enqueue checks (counts at
// least 0, at most 100 retries, 1 s <= minDelayTarget <= maxDelayTarget <=
// 3600 s, a declared function), so no accepted policy makes a delivery
// fail to enqueue, and that its retries are the effective numRetries
// (sns-http-delivery.md 6.3).
func TestHTTPRetryPolicyStaysInTheOutboxBounds(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		retryPolicy := func(level string) document.Map {
			var retry document.Map
			for _, m := range []struct {
				name   string
				lo, hi int
			}{{"minDelayTarget", 1, 4000}, {"maxDelayTarget", 1, 3600}, {"numRetries", 0, 100}, {"numNoDelayRetries", 0, 60}, {"numMinDelayRetries", 0, 60}, {"numMaxDelayRetries", 0, 60}} {
				if rapid.Bool().Draw(t, level+m.name+"Given") {
					retry = append(retry, document.Entry{Key: m.name, Value: document.Number(strconv.Itoa(rapid.IntRange(m.lo, m.hi).Draw(t, level+m.name)))})
				}
			}
			if f := rapid.SampledFrom([]string{"", "linear", "Exponential", "geometric", "ARITHMETIC"}).Draw(t, level+"backoffFunction"); f != "" {
				retry = append(retry, document.Entry{Key: "backoffFunction", Value: document.String(f)})
			}
			return retry
		}
		throttle := func(level string) document.Map {
			if !rapid.Bool().Draw(t, level+"throttled") {
				return nil
			}
			return document.Map{{Key: "maxReceivesPerSecond", Value: document.Number(strconv.Itoa(rapid.IntRange(1, 2000).Draw(t, level+"maxReceivesPerSecond")))}}
		}
		sub := document.Map{{Key: "healthyRetryPolicy", Value: retryPolicy("subscription.")}}
		if th := throttle("subscription."); th != nil {
			sub = append(sub, document.Entry{Key: "throttlePolicy", Value: th})
		}
		http := document.Map{{Key: "defaultHealthyRetryPolicy", Value: retryPolicy("topic.")}}
		if th := throttle("topic."); th != nil {
			http = append(http, document.Entry{Key: "defaultThrottlePolicy", Value: th})
		}
		topic := document.Map{{Key: "http", Value: http}}
		if checkSubscriptionDeliveryPolicy(sub) != "" || checkTopicDeliveryPolicy(topic) != "" {
			t.Skip("a write check refuses the policies")
		}
		storedSub, ok := storedSubscriptionDeliveryPolicy(sub)
		if !ok {
			t.Fatalf("storedSubscriptionDeliveryPolicy(%v) does not encode", sub)
		}
		storedTopic, ok := storedDeliveryPolicy(topic)
		if !ok {
			t.Fatalf("storedDeliveryPolicy(%v) does not encode", topic)
		}
		p := httpRetryPolicy(storedSub, storedTopic)
		switch {
		case p.NoDelay < 0 || p.MinDelay < 0 || p.Backoff < 0 || p.MaxDelay < 0 || p.Retries() > 100:
			t.Fatalf("phase counts out of bounds: %+v", p)
		case p.MinDelayTarget < time.Second || p.MinDelayTarget > p.MaxDelayTarget || p.MaxDelayTarget > time.Hour:
			t.Fatalf("delay targets out of bounds: %+v", p)
		case !p.BackoffFunction.Valid() || p.MaxReceivesPerSecond < 0:
			t.Fatalf("function or throttle out of bounds: %+v", p)
		}
		want := int64(3)
		if n, ok := intMember(mapMember(http, "defaultHealthyRetryPolicy"), "numRetries"); ok {
			want = n
		}
		if n, ok := intMember(mapMember(sub, "healthyRetryPolicy"), "numRetries"); ok {
			want = n
		}
		if int64(p.Retries()) != want {
			t.Fatalf("Retries() = %d, want the effective numRetries %d: %+v", p.Retries(), want, p)
		}
	})
}
