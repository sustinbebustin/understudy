package outbox

import (
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/delivery"
)

// SNS's AWS-managed policy for SQS and Lambda endpoints (sns.md 7.2): 3
// retries with no delay, 2 one second apart, 10 backing off exponentially
// from 1 s to 20 s, then 100,000 twenty seconds apart.
func TestSNSManagedSchedule(t *testing.T) {
	ms := time.Millisecond
	want := map[int]time.Duration{
		1: 0, 2: 0, 3: 0,
		4: time.Second, 5: time.Second,
		6: 1000 * ms, 7: 1394 * ms, 8: 1945 * ms, 9: 2714 * ms, 10: 3786 * ms,
		11: 5281 * ms, 12: 7368 * ms, 13: 10278 * ms, 14: 14337 * ms, 15: 20000 * ms,
		16: 20 * time.Second, 50_000: 20 * time.Second, 100_015: 20 * time.Second,
	}
	for retry, d := range want {
		got, ok := retryDelay(delivery.SNSManaged{}, retry, 0)
		if !ok || got != d {
			t.Errorf("retry %d: delay = %v, %v; want %v", retry, got, ok, d)
		}
	}
	if d, ok := retryDelay(delivery.SNSManaged{}, 100_016, 0); ok {
		t.Errorf("retry 100,016 is allowed with delay %v; the policy has 100,015 retries", d)
	}
}

// The backoff phase is 1 s * 20^(k/9) for k = 0..9, truncated to the
// millisecond: the exponential from 1 s to 20 s in ten steps. AWS does not
// publish the series; this one meets both documented ends.
func TestSNSManagedBackoffIsExponential(t *testing.T) {
	for k := range 9 {
		lo, _ := retryDelay(delivery.SNSManaged{}, 6+k, 0)
		hi, _ := retryDelay(delivery.SNSManaged{}, 7+k, 0)
		if ratio := float64(hi) / float64(lo); ratio < 1.393 || ratio > 1.397 {
			t.Errorf("step %d: %v -> %v has ratio %.4f, want 20^(1/9) = 1.3950", k, lo, hi, ratio)
		}
	}
}

func TestS3NotificationAndNoneSchedules(t *testing.T) {
	for retry, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second} {
		if got, ok := retryDelay(delivery.S3Notification{}, retry, 0); !ok || got != want {
			t.Errorf("S3Notification retry %d: delay = %v, %v; want %v", retry, got, ok, want)
		}
	}
	if _, ok := retryDelay(delivery.S3Notification{}, 4, 0); ok {
		t.Error("S3Notification allows a fourth retry")
	}
	if _, ok := retryDelay(delivery.None{}, 1, 0); ok {
		t.Error("None allows a retry")
	}
}

// eventBridgeBases is the base delay of each EventBridge retry: doubling
// from 1 s, capped at five minutes from the tenth retry on.
var eventBridgeBases = map[int]time.Duration{
	1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 4: 8 * time.Second, 5: 16 * time.Second,
	6: 32 * time.Second, 7: 64 * time.Second, 8: 128 * time.Second, 9: 256 * time.Second,
	10: 5 * time.Minute, 11: 5 * time.Minute, 100: 5 * time.Minute, 185: 5 * time.Minute,
}

// An EventBridge target retries MaxAttempts times at most, each delay
// between half and all of its base, drawn only from the delivery's jitter
// seed and the retry number.
func TestEventBridgeSchedule(t *testing.T) {
	retries := make([]int, 0, len(eventBridgeBases))
	for r := range eventBridgeBases {
		retries = append(retries, r)
	}
	slices.Sort(retries)
	rapid.Check(t, func(t *rapid.T) {
		p := delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: rapid.IntRange(0, 185).Draw(t, "attempts")}
		seed := rapid.Uint64().Draw(t, "seed")
		retry := rapid.SampledFrom(retries).Draw(t, "retry")
		d, ok := retryDelay(p, retry, seed)
		if retry > p.MaxAttempts {
			if ok {
				t.Fatalf("retry %d of %d allowed", retry, p.MaxAttempts)
			}
			return
		}
		base := eventBridgeBases[retry]
		if !ok || d < base/2 || d > base {
			t.Fatalf("retry %d: delay = %v, %v; want within [%v, %v]", retry, d, ok, base/2, base)
		}
		if again, _ := retryDelay(p, retry, seed); again != d {
			t.Fatalf("retry %d with seed %d: %v then %v", retry, seed, d, again)
		}
	})
}
