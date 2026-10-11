package lambda

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The asynchronous invocation queue (lambda-api.md 8.6, lambda-events.md
// 8): an Event invoke is stored and answered 202, and every attempt runs
// from a timer on the emulator clock. A function error is retried after 60
// and then 120 seconds, up to the configured retry attempts (2 by
// default); a throttle or a failure of the emulator goes back to the queue
// with a backoff from 1 second doubling to 5 minutes. An event older than
// its maximum age (6 hours by default) is discarded.

// TimerAsync fires an asynchronous invocation's next attempt. Its id is
// the function's unqualified ARN, a slash and the invocation's request id.
const TimerAsync kernel.TimerKind = "lambda.async"

// The retry schedule.
const (
	firstRetryDelay  = time.Minute
	secondRetryDelay = 2 * time.Minute
	minRequeueDelay  = time.Second
	maxRequeueDelay  = 5 * time.Minute

	defaultAsyncRetries = 2
	defaultMaxEventAge  = 6 * time.Hour
)

// asyncQueue is one scope's queued asynchronous invocations, by request id.
type asyncQueue struct {
	Events map[string]asyncEvent `json:"events,omitempty"`
}

func newAsyncQueue(aws.Scope) asyncQueue { return asyncQueue{Events: map[string]asyncEvent{}} }

// asyncCodec serializes the queue as JSON.
func asyncCodec() store.Codec {
	return store.Codec{Marshal: json.Marshal, Unmarshal: json.Unmarshal}
}

// asyncEvent is one queued invocation.
type asyncEvent struct {
	// RequestID is the Invoke call's request id, which every attempt runs
	// under.
	RequestID ids.LambdaRequestID `json:"requestId"`
	Function  string              `json:"function"`
	Qualifier string              `json:"qualifier,omitempty"`
	Payload   []byte              `json:"payload"`
	// Parent and Trace are the Invoke call's cause, the cause of every
	// attempt.
	Parent   ref.EventID   `json:"parent"`
	Trace    ref.EventID   `json:"trace"`
	Enqueued clock.Instant `json:"enqueued"`
	// Failures counts the attempts the function failed; Requeues the
	// attempts that never reached it, for the backoff.
	Failures int           `json:"failures,omitzero"`
	Requeues int           `json:"requeues,omitzero"`
	Due      clock.Instant `json:"due"`
	// Running is set while an attempt runs, so a timer firing again does
	// not start a second one. It is this process's claim, never saved: a
	// loaded event is attempted again.
	Running bool `json:"-"`
}

func (ev asyncEvent) cause() ref.Cause { return ref.Cause{Parent: ev.Parent, Trace: ev.Trace} }

// asyncConfig is what a function's asynchronous invocation configuration
// sets for its queue.
type asyncConfig struct {
	retries int
	maxAge  time.Duration
}

// defaultAsyncConfig is the queue's behavior without a configuration.
func defaultAsyncConfig() asyncConfig {
	return asyncConfig{retries: defaultAsyncRetries, maxAge: defaultMaxEventAge}
}

// asyncConfigOf reads the configuration of the qualifier invoked; $LATEST's
// serves the unqualified function.
func asyncConfigOf(f Function, qualifier string) asyncConfig {
	cfg := defaultAsyncConfig()
	if qualifier == "" {
		qualifier = latest
	}
	c, ok := f.EventInvoke[qualifier]
	if !ok {
		return cfg
	}
	if c.MaximumRetryAttempts != nil {
		cfg.retries = int(*c.MaximumRetryAttempts)
	}
	if c.MaximumEventAgeInSeconds != nil {
		cfg.maxAge = time.Duration(*c.MaximumEventAgeInSeconds) * time.Second
	}
	return cfg
}

// attemptResult is how one attempt ended for the queue.
//
//sumtype:decl
type attemptResult interface{ isAttemptResult() }

// delivered: the function returned a response.
type delivered struct{}

// functionFailed: the function ran and failed (error, timeout, crash).
type functionFailed struct{}

// requeued: the attempt never reached the function (throttled, or the
// emulator failed to run it).
type requeued struct{}

// discarded: the event can never run.
type discarded struct{ reason string }

func (delivered) isAttemptResult()      {}
func (functionFailed) isAttemptResult() {}
func (requeued) isAttemptResult()       {}
func (discarded) isAttemptResult()      {}

// asyncPlan is what becomes of an event after an attempt.
//
//sumtype:decl
type asyncPlan interface{ isAsyncPlan() }

// retryAt keeps the event for another attempt at its Due.
type retryAt struct{ event asyncEvent }

// finished removes a delivered event.
type finished struct{}

// dropped removes an event that will not be attempted again.
type dropped struct{ reason string }

func (retryAt) isAsyncPlan()  {}
func (finished) isAsyncPlan() {}
func (dropped) isAsyncPlan()  {}

// next plans ev after an attempt ended with r at now.
func (ev asyncEvent) next(r attemptResult, cfg asyncConfig, now clock.Instant) asyncPlan {
	ev.Running = false
	var delay time.Duration
	switch r := r.(type) {
	case delivered:
		return finished{}
	case discarded:
		return dropped(r)
	case functionFailed:
		ev.Failures++
		if ev.Failures > cfg.retries {
			return dropped{reason: "the function failed on all " + strconv.Itoa(ev.Failures) + " attempts"}
		}
		delay = firstRetryDelay
		if ev.Failures > 1 {
			delay = secondRetryDelay
		}
	case requeued:
		ev.Requeues++
		delay = requeueDelay(ev.Requeues)
	}
	ev.Due = now.Add(delay)
	if ev.Due.Sub(ev.Enqueued) > cfg.maxAge {
		return dropped{reason: "the event would be older than its maximum age of " + cfg.maxAge.String() + " at its next attempt"}
	}
	return retryAt{event: ev}
}

// requeueDelay is the backoff after the nth requeue: 1 second doubling to
// at most 5 minutes.
func requeueDelay(n int) time.Duration {
	d := minRequeueDelay
	for range n - 1 {
		if d *= 2; d >= maxRequeueDelay {
			return maxRequeueDelay
		}
	}
	return d
}

// enqueue stores an Event invocation of t and arms its first attempt at
// once.
func (x *Execution) enqueue(c *kernel.Call, t *target, payload []byte) *awserr.Error {
	scope := t.ref.scope
	_, err := kernel.Write(c, x.queues, scope, func(q *asyncQueue, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		ev := asyncEvent{
			RequestID: ids.LambdaRequestID(c.ID()),
			Function:  t.ref.name,
			Qualifier: t.ref.qualifier,
			Payload:   payload,
			Parent:    c.Event(),
			Trace:     c.Cause().Trace,
			Enqueued:  now,
			Due:       now,
		}
		q.Events[string(ev.RequestID)] = ev
		tx.Arm(x.async, asyncTimerID(scope, ev.Function, ev.RequestID), now)
		return struct{}{}, nil
	})
	return err
}

// asyncTimerID is an event's timer id.
func asyncTimerID(scope aws.Scope, function string, request ids.LambdaRequestID) string {
	return functionARN(scope, function) + "/" + string(request)
}

// parseAsyncTimerID reads an event's scope and request id from its timer id.
func parseAsyncTimerID(id string) (aws.Scope, string, bool) {
	arn, request, ok := strings.Cut(id, "/")
	if !ok {
		return aws.Scope{}, "", false
	}
	a, err := aws.ParseARN(arn)
	if err != nil {
		return aws.Scope{}, "", false
	}
	scope, ok := scopeOf(a)
	return scope, request, ok
}

// onAsync claims a due event and starts its attempt as tracked work; an
// event gone, already running or not yet due is left alone.
func (x *Execution) onAsync(c *kernel.Call, key clock.TimerKey, due clock.Instant) {
	scope, request, ok := parseAsyncTimerID(key.ID)
	if !ok {
		return
	}
	type claim struct {
		event asyncEvent
		ok    bool
	}
	got, err := kernel.Write(c, x.queues, scope, func(q *asyncQueue, _ clock.Instant, tx *kernel.Tx) (claim, *awserr.Error) {
		ev, ok := q.Events[request]
		if !ok || ev.Running || due.Before(ev.Due) {
			tx.Unchanged()
			return claim{}, nil
		}
		ev.Running = true
		q.Events[request] = ev
		return claim{event: ev, ok: true}, nil
	})
	if err != nil {
		x.k.Log().LogAttrs(x.ctx, slog.LevelError, "claiming an asynchronous Lambda invocation failed; it stays queued",
			slog.String("timer", key.ID), slog.String("error", err.Message))
		return
	}
	if !got.ok {
		return
	}
	x.k.Go(kernel.InvocationRunning, func(ctx context.Context) {
		// An attempt that panics is requeued, so the claimed event is
		// never left running with no timer armed.
		var result attemptResult = requeued{}
		cfg := defaultAsyncConfig()
		defer func() { x.settleAsync(ctx, c, scope, got.event.RequestID, result, cfg) }()
		result, cfg = x.attempt(ctx, c, scope, &got.event)
	})
}

// attempt runs one attempt of ev and classifies it for the queue.
func (x *Execution) attempt(ctx context.Context, c *kernel.Call, scope aws.Scope, ev *asyncEvent) (attemptResult, asyncConfig) {
	t, err := x.resolve(functionRef{scope: scope, name: ev.Function, qualifier: ev.Qualifier})
	switch {
	case err != nil && err.ID.Status == http.StatusNotFound:
		return discarded{reason: "the function or its qualifier no longer exists"}, defaultAsyncConfig()
	case err != nil:
		return requeued{}, defaultAsyncConfig()
	case t.reserved != nil && *t.reserved == 0:
		return discarded{reason: "the function's reserved concurrency is 0"}, t.async
	case c.Admitted().Sub(ev.Enqueued) > t.async.maxAge:
		// Planning a retry checks the age too, but an event loaded from a
		// snapshot or after a restart may be past it already.
		return discarded{reason: "the event is older than its maximum age of " + t.async.maxAge.String()}, t.async
	}
	res, runErr := x.execute(ctx, &run{target: t, request: ev.RequestID, payload: ev.Payload, parent: c, cause: ev.cause()})
	if runErr != nil {
		return requeued{}, t.async
	}
	if _, ok := res.outcome.(runtimeapi.Success); ok {
		return delivered{}, t.async
	}
	return functionFailed{}, t.async
}

// settleAsync applies an attempt's result to the event, unless a reset
// dropped it while the attempt ran.
func (x *Execution) settleAsync(ctx context.Context, c *kernel.Call, scope aws.Scope, request ids.LambdaRequestID, r attemptResult, cfg asyncConfig) {
	type outcome struct {
		plan  asyncPlan
		event asyncEvent
	}
	got, err := kernel.Write(c, x.queues, scope, func(q *asyncQueue, now clock.Instant, tx *kernel.Tx) (outcome, *awserr.Error) {
		ev, ok := q.Events[string(request)]
		if !ok || !ev.Running {
			tx.Unchanged()
			return outcome{plan: finished{}}, nil
		}
		plan := ev.next(r, cfg, now)
		switch p := plan.(type) {
		case retryAt:
			q.Events[string(request)] = p.event
			tx.Arm(x.async, asyncTimerID(scope, ev.Function, request), p.event.Due)
		case finished, dropped:
			delete(q.Events, string(request))
		}
		return outcome{plan: plan, event: ev}, nil
	})
	if err != nil {
		x.k.Log().LogAttrs(ctx, slog.LevelError, "recording an asynchronous Lambda attempt failed; the event stays claimed until the queue is reset",
			slog.String("request_id", string(request)), slog.String("error", err.Message))
		return
	}
	if d, ok := got.plan.(dropped); ok {
		x.k.Log().LogAttrs(ctx, slog.LevelWarn, "Lambda discarded an asynchronous invocation; no destination or dead-letter queue receives it",
			slog.String("function", functionARN(scope, got.event.Function)), slog.String("request_id", string(request)), slog.String("reason", d.reason))
	}
}

// asyncQueueVersion is the schema version of the queue's sections.
const asyncQueueVersion = 1

// AsyncQueue is the asynchronous invocation queue as snapshots,
// persistence and resets see it: a Stateful of its own, named
// lambda-async, because the queue lives in the Execution's cells rather
// than the Lambda service's. The server lists it with the services'
// Stateful views.
type AsyncQueue struct{ x *Execution }

var _ kernel.Stateful = (*AsyncQueue)(nil)

// AsyncQueue returns the queue's Stateful view.
func (x *Execution) AsyncQueue() *AsyncQueue { return &AsyncQueue{x: x} }

// Name returns "lambda-async".
func (*AsyncQueue) Name() ref.ServiceID { return "lambda-async" }

// SchemaVersion returns the version of the queue's sections.
func (*AsyncQueue) SchemaVersion() int { return asyncQueueVersion }

// Export serializes every scope's queued events to one section.
func (q *AsyncQueue) Export() ([]store.Section, error) {
	return q.x.queues.Export(store.ScopeKeys(), asyncQueueVersion)
}

// Import loads sections written by Export of this schema version. No
// loaded event is running: Rearm arms an attempt for each.
func (q *AsyncQueue) Import(ss []store.Section) error {
	return q.x.queues.Import(store.ScopeKeys(), asyncQueueVersion, ss)
}

// Reset drops the queued events of the scopes rs covers.
func (q *AsyncQueue) Reset(rs kernel.ResetScope) {
	q.x.queues.Reset(func(s aws.Scope) bool { return !rs.Covers(s) })
}

// Rearm arms the next attempt of every queued event, at its due time or
// now if that has passed. An event an attempt of this process still runs
// is skipped by its timer and rearmed when the attempt settles.
func (q *AsyncQueue) Rearm(now clock.Instant) {
	sched := q.x.k.Timers().Scheduler()
	for _, scope := range q.Scopes() {
		cell, ok := q.x.queues.Lookup(scope)
		if !ok {
			continue
		}
		armed := store.Read(cell, func(qu *asyncQueue) []clock.Armed {
			out := make([]clock.Armed, 0, len(qu.Events))
			for _, id := range slices.Sorted(maps.Keys(qu.Events)) {
				ev := qu.Events[id]
				due := ev.Due
				if due.Before(now) {
					due = now
				}
				out = append(out, clock.Armed{Key: q.x.async.Key(asyncTimerID(scope, ev.Function, ev.RequestID)), Due: due})
			}
			return out
		})
		for _, a := range armed {
			sched.Arm(a.Key, a.Due)
		}
	}
}

// Resources lists the functions of scope with queued events.
func (q *AsyncQueue) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := q.x.queues.Lookup(scope)
	if !ok {
		return nil
	}
	queued := store.Read(cell, func(qu *asyncQueue) map[string]int {
		n := map[string]int{}
		for id := range qu.Events {
			n[qu.Events[id].Function]++
		}
		return n
	})
	out := make([]kernel.Resource, 0, len(queued))
	for _, name := range slices.Sorted(maps.Keys(queued)) {
		summary := strconv.Itoa(queued[name]) + " queued asynchronous invocations"
		if queued[name] == 1 {
			summary = "1 queued asynchronous invocation"
		}
		out = append(out, kernel.Resource{ARN: invokedARN(functionRef{scope: scope, name: name}), Summary: summary})
	}
	return out
}

// Scopes returns the accounts and regions that hold a queue.
func (q *AsyncQueue) Scopes() []aws.Scope { return q.x.queues.Keys(store.ScopeKeys()) }
