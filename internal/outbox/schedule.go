package outbox

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
)

// SNS's AWS-managed delivery policy for SQS and Lambda endpoints (sns.md
// 7.2): retry counts per phase and the post-backoff delay.
const (
	snsImmediateRetries   = 3
	snsPreBackoffRetries  = 2
	snsPostBackoffRetries = 100_000
	snsMaxDelay           = 20 * time.Second
)

// snsBackoff is the backoff phase, ten retries growing exponentially from
// 1 s to 20 s: 1 s * 20^(k/9) truncated to the millisecond. AWS publishes
// the ends and the function, not the series.
func snsBackoff() [10]time.Duration {
	ms := time.Millisecond
	return [10]time.Duration{1000 * ms, 1394 * ms, 1945 * ms, 2714 * ms, 3786 * ms, 5281 * ms, 7368 * ms, 10278 * ms, 14337 * ms, 20000 * ms}
}

// The bounds EventBridge puts on a target's RetryPolicy:
// MaximumEventAgeInSeconds is 60 to 86,400 and MaximumRetryAttempts 0 to 185.
const (
	minEventAge     = time.Minute
	maxEventAge     = 24 * time.Hour
	maxEventRetries = 185
)

// maxEventsBackoff caps an EventBridge target's exponential retry delay.
// EventBridge documents 185 attempts over 24 hours with exponential backoff
// and jitter, not the series; doubling from 1 s up to this cap spends the
// attempts within the documented day.
const maxEventsBackoff = 5 * time.Minute

// s3Retries is how many times an S3 notification is retried, 1 s, 2 s and
// 4 s after the previous attempt, before it is dropped. S3 documents
// at-least-once delivery, not its retries.
const s3Retries = 3

// retryDelay returns how long after a failed attempt retry number retry
// (1-based) is due under p, and false when p allows no such retry. seed is
// the delivery's jitter seed; only EventBridge targets use it.
func retryDelay(p delivery.RetryPolicy, retry int, seed uint64) (time.Duration, bool) {
	switch p := p.(type) {
	case delivery.SNSManaged:
		return snsDelay(retry)
	case delivery.EventBridgeTarget:
		if retry > p.MaxAttempts {
			return 0, false
		}
		base := maxEventsBackoff
		if retry <= 9 {
			base = time.Second << (retry - 1)
		}
		// Equal jitter: half the base, plus a seeded fraction of the other
		// half.
		half := base / 2
		return half + time.Duration(jitter(seed, retry)*float64(half)).Truncate(time.Millisecond), true
	case delivery.S3Notification:
		if retry > s3Retries {
			return 0, false
		}
		return time.Second << (retry - 1), true
	case delivery.None:
	}
	return 0, false
}

// snsDelay walks the phases of the AWS-managed policy, counting retry down
// through each.
func snsDelay(retry int) (time.Duration, bool) {
	r := retry - snsImmediateRetries
	if r <= 0 {
		return 0, true
	}
	if r -= snsPreBackoffRetries; r <= 0 {
		return time.Second, true
	}
	backoff := snsBackoff()
	for _, d := range backoff {
		r--
		if r == 0 {
			return d, true
		}
	}
	if r <= snsPostBackoffRetries {
		return snsMaxDelay, true
	}
	return 0, false
}

// nextAttempt returns when e's next attempt is due after attempt number
// e.Attempt+1, due at last, failed; false when its policy allows no more:
// retries are exhausted, or an EventBridge target's next attempt would come
// after its maximum event age (exhaustedCondition says which).
func nextAttempt(e *Entry, last clock.Instant) (clock.Instant, bool) {
	d, ok := retryDelay(e.Request.Policy, e.Attempt+1, e.Jitter)
	if !ok {
		return clock.Instant{}, false
	}
	next := last.Add(d)
	if p, ok := e.Request.Policy.(delivery.EventBridgeTarget); ok && next.Sub(e.Accepted) > p.MaxAge {
		return clock.Instant{}, false
	}
	return next, true
}

// exhaustedCondition names the EventBridge retry limit that stopped e after
// nextAttempt refused its next attempt: MaximumRetryAttempts when the
// retries ran out, MaximumEventAgeInSeconds when the next one would have
// come too late.
func exhaustedCondition(e *Entry, p delivery.EventBridgeTarget) string {
	if e.Attempt+1 > p.MaxAttempts {
		return "MaximumRetryAttempts"
	}
	return "MaximumEventAgeInSeconds"
}

// jitter is a fraction in [0, 1) that depends only on seed and retry: the
// splitmix64 finalizer of their sum.
func jitter(seed uint64, retry int) float64 {
	x := seed + uint64(retry)*0x9e3779b97f4a7c15 //nolint:gosec // retry is a positive count, never negative
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	x ^= x >> 31
	return float64(x>>11) / (1 << 53)
}
