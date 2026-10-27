package lambda

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/service/lambda/destination"
)

// For any sequence of attempt results, an event that ends says why and how
// the function ran, as its record does (lambda-destinations-layers.md 3.2,
// 3.3): a response is Success counting every attempt with that response; a
// function error with no retry left is RetriesExhausted, even with no
// retries; any other end of a failure or requeue is EventAgeExceeded; a
// discard keeps its own condition. A record that is not Success counts the
// failed attempts and carries the last failure's response, or never ran
// when the function never did (finding 5).
func TestAsyncPlanCarriesTheRecordsConditionAndRun(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		cfg := asyncConfig{
			retries: rapid.IntRange(0, 2).Draw(rt, "retries"),
			maxAge:  time.Duration(rapid.IntRange(60, 21_600).Draw(rt, "maxAgeSeconds")) * time.Second,
		}
		start := clock.FromWire(time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC))
		ev := asyncEvent{Enqueued: start, Due: start, Running: true}
		now := start
		failures := 0
		var last *destination.Response
		for i := range 50 {
			response := destination.Response{ExecutedVersion: "$LATEST", Payload: []byte(`{"attempt":` + strconv.Itoa(i) + `}`)}
			var r attemptResult
			switch rapid.IntRange(0, 9).Draw(rt, "result") {
			case 0:
				r = delivered{response: response}
			case 1:
				r = discarded{reason: "reserved concurrency 0", condition: destination.ZeroReservedConcurrency}
			case 2, 3, 4:
				response.FunctionError = "Unhandled"
				r = functionFailed{response: response}
			default:
				r = requeued{}
			}
			plan := ev.next(r, cfg, now)
			if f, ok := r.(functionFailed); ok {
				failures++
				last = &f.response
			}
			switch p := plan.(type) {
			case finished:
				if want := (destination.Ran{Attempts: failures + 1, Response: response}); !reflect.DeepEqual(p.ran, want) {
					rt.Fatalf("finished with %+v, want %+v", p.ran, want)
				}
				return
			case dropped:
				want := destination.EventAgeExceeded
				switch r := r.(type) {
				case functionFailed:
					if failures > cfg.retries {
						want = destination.RetriesExhausted
					}
				case discarded:
					want = r.condition
				case requeued, delivered, vanished:
				}
				if p.condition != want {
					rt.Fatalf("dropped after %T with %s, want %s", r, p.condition, want)
				}
				var inv destination.Invocation = destination.NeverRan{}
				if last != nil {
					inv = destination.Ran{Attempts: failures, Response: *last}
				}
				if !reflect.DeepEqual(p.invocation, inv) {
					rt.Fatalf("dropped with %+v, want %+v", p.invocation, inv)
				}
				return
			case abandoned:
				rt.Fatalf("a %T attempt abandoned the event", r)
			case retryAt:
				ev, now = p.event, p.event.Due
				ev.Running = true
			}
		}
	})
}

// An event whose function or qualifier is gone ends with no record
// (lambda-destinations-layers.md 11.3.4).
func TestAsyncPlanAbandonsAVanishedFunction(t *testing.T) {
	start := clock.FromWire(time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC))
	ev := asyncEvent{Enqueued: start, Due: start, Running: true, Failures: 1, Last: &asyncResponse{ExecutedVersion: "$LATEST", FunctionError: "Unhandled", Payload: []byte(`{}`)}}
	if p, ok := ev.next(vanished{reason: "gone"}, defaultAsyncConfig(), start).(abandoned); !ok || p.reason != "gone" {
		t.Errorf("next(vanished) = %#v, want abandoned with its reason", ev.next(vanished{reason: "gone"}, defaultAsyncConfig(), start))
	}
}
