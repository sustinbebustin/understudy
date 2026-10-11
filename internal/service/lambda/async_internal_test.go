package lambda

import (
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/clock"
)

// The queue's schedule over any sequence of attempt results: a function
// error is retried at most the configured number of times, first after 60
// seconds and then after 120; a requeue backs off from 1 second doubling to
// at most 5 minutes; nothing is attempted past the event's maximum age; a
// delivered event is finished.
func TestAsyncScheduleProperties(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		cfg := asyncConfig{
			retries: rapid.IntRange(0, 2).Draw(rt, "retries"),
			maxAge:  time.Duration(rapid.IntRange(60, 21_600).Draw(rt, "maxAgeSeconds")) * time.Second,
		}
		start := clock.FromWire(time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
		ev := asyncEvent{Enqueued: start, Due: start, Running: true}
		now := start
		failures, requeues := 0, 0
		for range 50 {
			var r attemptResult
			switch rapid.IntRange(0, 9).Draw(rt, "result") {
			case 0:
				r = delivered{}
			case 1, 2, 3:
				r = functionFailed{}
			default:
				r = requeued{}
			}
			plan := ev.next(r, cfg, now)
			switch p := plan.(type) {
			case finished:
				if _, ok := r.(delivered); !ok {
					rt.Fatalf("a %T attempt finished the event", r)
				}
				return
			case dropped:
				switch r.(type) {
				case functionFailed:
					if failures+1 <= cfg.retries && now.Add(retryDelayAfter(failures+1)).Sub(start) <= cfg.maxAge {
						rt.Fatalf("dropped after %d failures with %d retries allowed and the age within %s: %s", failures+1, cfg.retries, cfg.maxAge, p.reason)
					}
				case requeued:
					if now.Add(requeueDelay(requeues+1)).Sub(start) <= cfg.maxAge {
						rt.Fatalf("dropped a requeue within the maximum age: %s", p.reason)
					}
				case delivered, discarded:
					rt.Fatalf("a %T attempt dropped the event", r)
				}
				return
			case retryAt:
				next := p.event
				var want time.Duration
				switch r.(type) {
				case functionFailed:
					failures++
					want = retryDelayAfter(failures)
					if failures > cfg.retries {
						rt.Fatalf("retry %d planned with %d retries allowed", failures, cfg.retries)
					}
				case requeued:
					requeues++
					want = min(time.Second<<(requeues-1), 5*time.Minute)
				case delivered, discarded:
					rt.Fatalf("a %T attempt planned a retry", r)
				}
				if got := next.Due.Sub(now); got != want || next.Running {
					rt.Fatalf("next attempt %s after the last (running %v), want %s", got, next.Running, want)
				}
				if next.Due.Sub(start) > cfg.maxAge {
					rt.Fatalf("attempt planned at age %s past the maximum %s", next.Due.Sub(start), cfg.maxAge)
				}
				ev, now = next, next.Due
				ev.Running = true
			}
		}
	})
}

// retryDelayAfter is the wait after the nth failed attempt.
func retryDelayAfter(n int) time.Duration {
	if n == 1 {
		return time.Minute
	}
	return 2 * time.Minute
}

func TestRequeueDelayDoublesToFiveMinutes(t *testing.T) {
	for n, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 9: 256 * time.Second, 10: 5 * time.Minute, 1000: 5 * time.Minute} {
		if got := requeueDelay(n); got != want {
			t.Errorf("requeueDelay(%d) = %s, want %s", n, got, want)
		}
	}
}
