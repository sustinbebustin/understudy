package asl

import "time"

// Jitter is a retrier's JitterStrategy.
type Jitter int

// Jitter strategies; JitterNone is the default.
const (
	JitterNone Jitter = iota + 1
	JitterFull
)

// Retrier is one Retry entry with its defaults applied
// (stepfunctions-asl.md 3.7).
type Retrier struct {
	ErrorEquals     []string
	IntervalSeconds int     // default 1
	MaxAttempts     int     // default 3
	BackoffRate     float64 // default 2.0
	MaxDelaySeconds int     // 0: no cap
	Jitter          Jitter
}

// RetryDelay is the wait before retry number attempt (1 for the first) of
// r: IntervalSeconds * BackoffRate^(attempt-1), capped at MaxDelaySeconds
// when set, then, for JitterFull only, multiplied by jitter, a draw in
// [0, 1) (stepfunctions-asl.md 8.3). Milliseconds are kept: the result is
// rounded to the nearest millisecond.
//
// Not implemented yet: it returns 0.
func RetryDelay(r Retrier, attempt int, jitter float64) time.Duration {
	_, _, _ = r, attempt, jitter
	return 0
}
