package asl_test

import (
	"math"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// The documented delay sequences (aws-doc dg/concepts-error-handling;
// stepfunctions-asl.md 8.3): Interval 3, Backoff 2, MaxDelay 5 waits 3, 5,
// 5 s; Interval 2, Backoff 2 waits 2, 4, 8 s.
func TestRetryDelayDocumentedSequences(t *testing.T) {
	tests := []struct {
		r    asl.Retrier
		want []time.Duration
	}{
		{asl.Retrier{IntervalSeconds: 3, MaxAttempts: 3, BackoffRate: 2, MaxDelaySeconds: 5, Jitter: asl.JitterNone}, []time.Duration{3 * time.Second, 5 * time.Second, 5 * time.Second}},
		{asl.Retrier{IntervalSeconds: 2, MaxAttempts: 3, BackoffRate: 2, Jitter: asl.JitterNone}, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}},
		{asl.Retrier{IntervalSeconds: 1, MaxAttempts: 3, BackoffRate: 2, Jitter: asl.JitterNone}, []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}},
	}
	for _, tt := range tests {
		for i, want := range tt.want {
			if got := asl.RetryDelay(tt.r, i+1, 0.5); got != want {
				t.Errorf("RetryDelay(%+v, %d) = %v, want %v", tt.r, i+1, got, want)
			}
		}
	}
}

// With JitterStrategy FULL the wait is between 0 and the capped delay
// (aws-doc dg/concepts-error-handling: Interval 2, Backoff 2 gives 0-2,
// 0-4, 0-8 s). The distribution is understudy's (ASL-10), so only the
// bounds are asserted.
func TestRetryDelayFullJitterBounds(t *testing.T) {
	r := asl.Retrier{IntervalSeconds: 2, MaxAttempts: 3, BackoffRate: 2, Jitter: asl.JitterFull}
	for k, upper := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second} {
		for _, j := range []float64{0, 0.25, 0.5, 0.999} {
			if got := asl.RetryDelay(r, k+1, j); got < 0 || got > upper {
				t.Errorf("RetryDelay(FULL, %d, %v) = %v, want within [0, %v]", k+1, j, got, upper)
			}
		}
	}
}

// exactDelay is the documented formula in seconds:
// IntervalSeconds * BackoffRate^(k-1), capped at MaxDelaySeconds when set
// (stepfunctions-asl.md 8.3).
func exactDelay(r asl.Retrier, k int) float64 {
	d := float64(r.IntervalSeconds) * math.Pow(r.BackoffRate, float64(k-1))
	if r.MaxDelaySeconds > 0 {
		d = math.Min(d, float64(r.MaxDelaySeconds))
	}
	return d
}

func retrier(t *rapid.T, jitter asl.Jitter) asl.Retrier {
	return asl.Retrier{
		ErrorEquals:     []string{"States.ALL"},
		IntervalSeconds: rapid.IntRange(1, 100).Draw(t, "interval"),
		MaxAttempts:     10,
		BackoffRate:     1 + float64(rapid.IntRange(0, 12).Draw(t, "backoffQuarters"))/4,
		MaxDelaySeconds: rapid.SampledFrom([]int{0, rapid.IntRange(1, 10_000).Draw(t, "maxDelay")}).Draw(t, "cap"),
		Jitter:          jitter,
	}
}

// Delay sequences against the 8.3 formula. Without jitter the
// delay is the formula's value floored to whole seconds (ASL-10, recorded
// 2026-11-03); the comparison allows half a millisecond for float error at
// a whole second.
func TestRetryDelayFormula(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := retrier(t, asl.JitterNone)
		k := rapid.IntRange(1, 8).Draw(t, "attempt")
		want := math.Floor(exactDelay(r, k) + 0.0005)
		got := asl.RetryDelay(r, k, rapid.Float64Range(0, 0.999).Draw(t, "jitter")).Seconds()
		if math.Abs(got-want) > 0.0005 {
			t.Fatalf("RetryDelay(%+v, %d) = %vs, want %vs", r, k, got, want)
		}
	})
}

// With FULL jitter every delay lies in [0, formula].
func TestRetryDelayFullJitterProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := retrier(t, asl.JitterFull)
		k := rapid.IntRange(1, 8).Draw(t, "attempt")
		j := rapid.Float64Range(0, 0.999).Draw(t, "jitter")
		got := asl.RetryDelay(r, k, j).Seconds()
		if upper := exactDelay(r, k); got < 0 || got > upper+0.0005 {
			t.Fatalf("RetryDelay(%+v, %d, %v) = %vs, want within [0, %v]", r, k, j, got, upper)
		}
	})
}
