package asl

import (
	"math"
	"slices"
	"time"
)

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

// maxRetryDelay bounds an uncapped delay so it stays a time.Duration: no
// execution outlives maxExecution, so a longer wait never ends anyway.
const maxRetryDelay = maxExecution

// RetryDelay is the wait before retry number attempt (1 for the first) of
// r: IntervalSeconds * BackoffRate^(attempt-1), capped at MaxDelaySeconds
// when set, then, for JitterFull only, multiplied by jitter, a draw in
// [0, 1) (stepfunctions-asl.md 8.3). Fractions of a second are kept, to
// the nanosecond, not rounded to whole seconds. Whether AWS keeps fractions, applies
// the cap before jitter and draws uniformly is unrecorded; these are
// understudy's choices (inferred, ASL-10). A delay past maxRetryDelay is
// maxRetryDelay.
func RetryDelay(r Retrier, attempt int, jitter float64) time.Duration {
	seconds := float64(r.IntervalSeconds) * math.Pow(r.BackoffRate, float64(max(attempt, 1)-1))
	if r.MaxDelaySeconds > 0 {
		seconds = math.Min(seconds, float64(r.MaxDelaySeconds))
	}
	if r.Jitter == JitterFull {
		seconds *= min(max(jitter, 0), 1)
	}
	if math.IsNaN(seconds) || seconds >= maxRetryDelay.Seconds() {
		return maxRetryDelay
	}
	return time.Duration(math.Round(seconds * float64(time.Second)))
}

// errTaskFailed matches every error name but States.Timeout
// (stepfunctions-asl.md 8.1).
const errTaskFailed = "States.TaskFailed"

// errorNamed reports whether an ErrorEquals list names the error name:
// States.ALL matches every error a retrier or catcher sees, States.Timeout
// and States.DataLimitExceeded included (aws-recorded localstack@8b9a79f
// v2/error_handling/test_states_errors.snapshot.json:L210-L238;
// stepfunctions-asl.md finding 5), and States.TaskFailed every name but
// States.Timeout (aws-doc dg/concepts-error-handling).
func errorNamed(errorEquals []string, name string) bool {
	return slices.ContainsFunc(errorEquals, func(n string) bool {
		return n == name || n == errAll || (n == errTaskFailed && name != errTimeout)
	})
}

// jitterDraw is the n-th FULL jitter draw of an execution whose seed is
// seed, in [0, 1): a SplitMix64 step, so a draw depends only on the seed
// and its number, not on what else the service draws (inferred, ASL-10).
func jitterDraw(seed, n uint64) float64 {
	x := seed + n*0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	x ^= x >> 31
	return float64(x>>11) / (1 << 53)
}
