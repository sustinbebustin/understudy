package delivery_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/delivery"
)

// The backoff phase steps evenly from minDelayTarget to maxDelayTarget, and
// a single backoff retry waits minDelayTarget: sns.http.backoff-curves
// linear-for-all, inferred while the curves and the single-retry delay are
// UNVERIFIED (sns-http-delivery.md 6.3, 8.4; SNSH-09).
func TestDelayStepsLinearlyThroughTheBackoffPhase(t *testing.T) {
	s := time.Second
	cases := []struct {
		name   string
		policy delivery.SNSHTTP
		want   []time.Duration
	}{
		{
			"four backoff retries from 1s to 10s",
			delivery.SNSHTTP{Backoff: 4, MinDelayTarget: s, MaxDelayTarget: 10 * s, BackoffFunction: delivery.BackoffExponential},
			[]time.Duration{s, 4 * s, 7 * s, 10 * s},
		},
		{
			"one retry in every phase",
			delivery.SNSHTTP{NoDelay: 1, MinDelay: 1, Backoff: 1, MaxDelay: 1, MinDelayTarget: 5 * s, MaxDelayTarget: 60 * s, BackoffFunction: delivery.BackoffGeometric},
			[]time.Duration{0, 5 * s, 5 * s, 60 * s},
		},
		{
			// 1s + 2s*1/3 is 1.666666666s, truncated to the millisecond.
			"a step that is not a whole millisecond",
			delivery.SNSHTTP{Backoff: 4, MinDelayTarget: s, MaxDelayTarget: 3 * s, BackoffFunction: delivery.BackoffLinear},
			[]time.Duration{s, 1666 * time.Millisecond, 2333 * time.Millisecond, 3 * s},
		},
	}
	for _, c := range cases {
		var got []time.Duration
		for retry := 1; retry <= c.policy.Retries(); retry++ {
			d, ok := c.policy.Delay(retry)
			if !ok {
				t.Fatalf("%s: Delay(%d) refused", c.name, retry)
			}
			got = append(got, d)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: delays %v, want %v", c.name, got, c.want)
		}
	}
}

// A policy with no retries makes one attempt and allows no retry.
func TestNoRetriesAllowsNoDelay(t *testing.T) {
	p := delivery.SNSHTTP{MinDelayTarget: time.Second, MaxDelayTarget: time.Second, BackoffFunction: delivery.BackoffLinear}
	if p.Retries() != 0 {
		t.Fatalf("Retries() = %d, want 0", p.Retries())
	}
	if d, ok := p.Delay(1); ok {
		t.Errorf("Delay(1) = %v, true; want no retry", d)
	}
}

// The declared kinds and functions are exactly the named ones.
func TestHTTPEnumsValid(t *testing.T) {
	for k := range delivery.HTTPKind(255) {
		if want := k >= delivery.HTTPNotification && k <= delivery.HTTPUnsubscribeConfirmation; k.Valid() != want {
			t.Errorf("HTTPKind(%d).Valid() = %t, want %t", k, k.Valid(), want)
		}
	}
	for f := range delivery.BackoffFunction(255) {
		if want := f >= delivery.BackoffLinear && f <= delivery.BackoffExponential; f.Valid() != want {
			t.Errorf("BackoffFunction(%d).Valid() = %t, want %t", f, f.Valid(), want)
		}
	}
}
