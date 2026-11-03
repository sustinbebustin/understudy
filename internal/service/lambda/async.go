package lambda

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda/destination"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The asynchronous invocation queue (lambda-api.md 8.6, lambda-events.md
// 8): an Event invoke is stored and answered 202, and every attempt runs
// from a timer on the emulator clock. A function error is retried after 60
// and then 120 seconds, up to the configured retry attempts (2 by
// default); a throttle or a failure of the emulator goes back to the queue
// with a backoff from 1 second doubling to 5 minutes. An event that would
// reach its maximum age (6 hours by default) before its next attempt is
// discarded at once (lambda-destinations-layers.md finding 3).
//
// An event that ends sends its invocation record to the invoked
// qualifier's on-success or on-failure destination, and a failed one its
// payload to the function's dead-letter queue or topic, in the write that
// removes it (lambda-destinations-layers.md 3, 5, 11.3).

// TimerAsync fires an asynchronous invocation's next attempt. Its id is
// the function's unqualified ARN, a slash and the invocation's request id.
const TimerAsync kernel.TimerKind = "lambda.async"

// The retry schedule, and the limits a function without an asynchronous
// invocation configuration gets.
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
	// TraceHeader is the Invoke call's X-Amzn-Trace-Id, "" when it had
	// none: an SQS destination's AWSTraceHeader
	// (lambda-destinations-layers.md 4.1, 11.3.5).
	TraceHeader string `json:"traceHeader,omitempty"`
	// Last is the last failed attempt's response, nil while the function
	// never ran, so a later throttle that ages the event out still reports
	// it (lambda-destinations-layers.md 3.3).
	Last *asyncResponse `json:"last,omitempty"`
	// Running is set while an attempt runs, so a timer firing again does
	// not start a second one. It is this process's claim, never saved: a
	// loaded event is attempted again.
	Running bool `json:"-"`
}

// asyncResponse is the persisted form of a destination.Response.
type asyncResponse struct {
	ExecutedVersion string `json:"executedVersion"`
	FunctionError   string `json:"functionError"`
	Payload         []byte `json:"payload"`
}

func (ev asyncEvent) cause() ref.Cause { return ref.Cause{Parent: ev.Parent, Trace: ev.Trace} }

// invocation is how the function has run ev so far, for its record: never,
// or Failures times, the last with Last's response. Only a failure keeps
// an event queued, so Last is set exactly when Failures is at least 1.
func (ev asyncEvent) invocation() destination.Invocation {
	if ev.Last == nil {
		return destination.NeverRan{}
	}
	return destination.Ran{Attempts: ev.Failures, Response: destination.Response{
		ExecutedVersion: ev.Last.ExecutedVersion, FunctionError: ev.Last.FunctionError, Payload: ev.Last.Payload,
	}}
}

// asyncConfig is what the invoked qualifier's asynchronous invocation
// configuration and the function's dead-letter queue set for its queue,
// with the execution role its deliveries run as.
type asyncConfig struct {
	retries int
	maxAge  time.Duration
	// onSuccess and onFailure are the invoked qualifier's destination
	// ARNs, "" for none.
	onSuccess, onFailure string
	// deadLetter is the function's dead-letter target, $LATEST's for every
	// version (lambda-destinations-layers.md 5.1), "" for none.
	deadLetter string
	// role is the invoked version's execution role; zero in the defaults,
	// which have no destination.
	role aws.ARN
}

// defaultAsyncConfig is the queue's behavior without a configuration.
func defaultAsyncConfig() asyncConfig {
	return asyncConfig{retries: defaultAsyncRetries, maxAge: defaultMaxEventAge}
}

// asyncConfigOf reads the configuration of the qualifier invoked, whose
// version is v; $LATEST's serves the unqualified function. An alias or
// version without a configuration of its own gets the defaults, not
// $LATEST's (switch lambda.async.qualifier-config, LDL-01). The
// dead-letter target is the function's, $LATEST's.
func asyncConfigOf(f Function, v Version, qualifier string) asyncConfig {
	cfg := defaultAsyncConfig()
	cfg.deadLetter = f.Latest.Config.DeadLetterTarget
	if role, err := aws.ParseARN(v.Config.Role); err == nil {
		cfg.role = role
	}
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
	cfg.onSuccess, cfg.onFailure = c.OnSuccess, c.OnFailure
	return cfg
}

// attemptResult is how one attempt ended for the queue.
//
//sumtype:decl
type attemptResult interface{ isAttemptResult() }

// delivered: the function returned response.
type delivered struct{ response destination.Response }

// functionFailed: the function ran and failed (error, timeout, crash),
// answering response.
type functionFailed struct{ response destination.Response }

// requeued: the attempt never reached the function (throttled, or the
// emulator failed to run it).
type requeued struct{}

// discarded: the event can never run, for condition, a failed one:
// reserved concurrency 0 or an age already at its maximum.
type discarded struct {
	reason    string
	condition destination.Condition
}

// vanished: the function or qualifier no longer exists, and with it the
// configuration naming its destinations (lambda-destinations-layers.md
// 11.3.4).
type vanished struct{ reason string }

func (delivered) isAttemptResult()      {}
func (functionFailed) isAttemptResult() {}
func (requeued) isAttemptResult()       {}
func (discarded) isAttemptResult()      {}
func (vanished) isAttemptResult()       {}

// asyncPlan is what becomes of an event after an attempt.
//
//sumtype:decl
type asyncPlan interface{ isAsyncPlan() }

// retryAt keeps the event for another attempt at its Due.
type retryAt struct{ event asyncEvent }

// finished removes a delivered event, which ran as ran: its Success record.
type finished struct{ ran destination.Ran }

// dropped removes an event that will not be attempted again, for
// condition, having run as invocation: its failure record and dead letter
// (lambda-destinations-layers.md 3.3).
type dropped struct {
	reason     string
	condition  destination.Condition
	invocation destination.Invocation
}

// abandoned removes an event whose function or qualifier is gone, with no
// record and no dead letter (lambda-destinations-layers.md 11.3.4).
type abandoned struct{ reason string }

func (retryAt) isAsyncPlan()   {}
func (finished) isAsyncPlan()  {}
func (dropped) isAsyncPlan()   {}
func (abandoned) isAsyncPlan() {}

// next plans ev after an attempt ended with r at now (lambda-destinations-layers.md
// 3.3): a response is Success; a function error with no retry left is
// RetriesExhausted, even with no retries at all; a failure or requeue
// whose next attempt would come when the event is at or past its maximum
// age is EventAgeExceeded at once, without waiting (finding 3).
func (ev asyncEvent) next(r attemptResult, cfg asyncConfig, now clock.Instant) asyncPlan {
	ev.Running = false
	var delay time.Duration
	switch r := r.(type) {
	case delivered:
		return finished{ran: destination.Ran{Attempts: ev.Failures + 1, Response: r.response}}
	case vanished:
		return abandoned(r)
	case discarded:
		return dropped{reason: r.reason, condition: r.condition, invocation: ev.invocation()}
	case functionFailed:
		ev.Failures++
		ev.Last = &asyncResponse{ExecutedVersion: r.response.ExecutedVersion, FunctionError: r.response.FunctionError, Payload: r.response.Payload}
		if ev.Failures > cfg.retries {
			return dropped{reason: "the function failed on all " + strconv.Itoa(ev.Failures) + " attempts", condition: destination.RetriesExhausted, invocation: ev.invocation()}
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
	if ev.Due.Sub(ev.Enqueued) >= cfg.maxAge {
		return dropped{
			reason:    "the event would reach its maximum age of " + cfg.maxAge.String() + " by its next attempt",
			condition: destination.EventAgeExceeded, invocation: ev.invocation(),
		}
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
			RequestID:   ids.LambdaRequestID(c.ID()),
			Function:    t.ref.name,
			Qualifier:   t.ref.qualifier,
			Payload:     payload,
			Parent:      c.Event(),
			Trace:       c.Cause().Trace,
			Enqueued:    now,
			Due:         now,
			TraceHeader: c.Trace(),
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
	scope, err := aws.ScopeOf(a)
	return scope, request, err == nil
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
	// The attempt's log lines are delivered on c's behalf, and may still
	// be when settleAsync writes on c (onBehalf).
	writes := &sync.Mutex{}
	x.k.Go(kernel.InvocationRunning, func(ctx context.Context) {
		// An attempt that panics is requeued, so the claimed event is
		// never left running with no timer armed.
		var result attemptResult = requeued{}
		cfg := defaultAsyncConfig()
		defer func() {
			writes.Lock()
			defer writes.Unlock()
			x.settleAsync(ctx, c, scope, got.event.RequestID, result, cfg)
		}()
		result, cfg = x.attempt(ctx, c, writes, scope, &got.event)
	})
}

// attempt runs one attempt of ev on behalf of c, on which writes are made
// holding writes, and classifies it for the queue. A run carries the
// response and the version that ran: the payload of a success, or the
// error payload of any other outcome with the function error Unhandled
// (lambda-destinations-layers.md 3.2).
func (x *Execution) attempt(ctx context.Context, c *kernel.Call, writes *sync.Mutex, scope aws.Scope, ev *asyncEvent) (attemptResult, asyncConfig) {
	r := functionRef{scope: scope, name: ev.Function, qualifier: ev.Qualifier}
	t, err := x.resolve(r)
	switch {
	case err != nil && err.ID.Status == http.StatusNotFound:
		return vanished{reason: "the function or its qualifier no longer exists"}, defaultAsyncConfig()
	case err != nil:
		return requeued{}, x.queueConfig(r)
	case t.reserved != nil && *t.reserved == 0:
		return discarded{reason: "the function's reserved concurrency is 0", condition: destination.ZeroReservedConcurrency}, t.async
	case c.Admitted().Sub(ev.Enqueued) >= t.async.maxAge:
		// Planning a retry checks the age too, but an event loaded from a
		// snapshot or after a restart may be past it already.
		return discarded{reason: "the event reached its maximum age of " + t.async.maxAge.String(), condition: destination.EventAgeExceeded}, t.async
	}
	res, runErr := x.execute(ctx, &run{target: t, request: ev.RequestID, payload: ev.Payload, parent: c, parentWrites: writes, cause: ev.cause()})
	if runErr != nil {
		return requeued{}, t.async
	}
	response := destination.Response{ExecutedVersion: t.version.Number, Payload: res.outcome.Response(ev.RequestID)}
	if _, ok := res.outcome.(runtimeapi.Success); ok {
		return delivered{response: response}, t.async
	}
	response.FunctionError = runtimeapi.FunctionErrorHeader(res.outcome)
	return functionFailed{response: response}, t.async
}

// queueConfig reads the queue's configuration for r when its version
// cannot run now, as while it is still being created: a requeue that then
// ages out still reaches the destinations. The defaults when the function
// or qualifier is gone.
func (x *Execution) queueConfig(r functionRef) asyncConfig {
	cfg, _ := view(x.svc, r.scope, func(st *State) (asyncConfig, *awserr.Error) {
		f, v, err := st.lookup(r)
		if err != nil {
			return defaultAsyncConfig(), nil
		}
		return asyncConfigOf(f, v, r.qualifier), nil
	})
	return cfg
}

// settleAsync applies an attempt's result to the event, unless a reset
// dropped it while the attempt ran. An event that ends sends its record
// and dead letter in the write that removes it (lambda-destinations-layers.md
// 11.3.3); a destination stored in a form understudy cannot deliver to is
// logged and skipped, and the others still go.
func (x *Execution) settleAsync(ctx context.Context, c *kernel.Call, scope aws.Scope, request ids.LambdaRequestID, r attemptResult, cfg asyncConfig) {
	// outcome's plan is nil when a reset dropped the event.
	type outcome struct {
		plan  asyncPlan
		event asyncEvent
		sent  sent
	}
	got, err := kernel.Write(c, x.queues, scope, func(q *asyncQueue, now clock.Instant, tx *kernel.Tx) (outcome, *awserr.Error) {
		ev, ok := q.Events[string(request)]
		if !ok || !ev.Running {
			tx.Unchanged()
			return outcome{}, nil
		}
		// The record, dead letter and advisories continue the Invoke, as the
		// attempt did, not the TimerFired event that started the attempt.
		tx.Resume(ev.cause())
		plan := ev.next(r, cfg, now)
		var s sent
		switch p := plan.(type) {
		case retryAt:
			q.Events[string(request)] = p.event
			tx.Arm(x.async, asyncTimerID(scope, ev.Function, request), p.event.Due)
		case finished:
			delete(q.Events, string(request))
			s = x.sendRecords(tx, scope, ev, cfg, now, destination.Success, p.ran)
		case dropped:
			delete(q.Events, string(request))
			s = x.sendRecords(tx, scope, ev, cfg, now, p.condition, p.invocation)
		case abandoned:
			delete(q.Events, string(request))
			// Dropped with no record and a journal entry: the configuration
			// naming its destinations went with the function
			// (lambda-destinations-layers.md 11.3.4).
			tx.Record(scope, journal.Advisory{
				Kind: journal.AdvisoryAsyncEventAbandoned, Resource: invokedARN(functionRef{scope: scope, name: ev.Function, qualifier: ev.Qualifier}),
				Text: "Lambda dropped the asynchronous invocation " + string(request) + " with no record and no dead letter: " + p.reason +
					", and its destinations' configuration went with it (lambda-destinations-layers.md 11.3.4)",
			})
		}
		return outcome{plan: plan, event: ev, sent: s}, nil
	})
	if err != nil {
		x.k.Log().LogAttrs(ctx, slog.LevelError, "recording an asynchronous Lambda attempt failed; the event stays claimed until the queue is reset",
			slog.String("request_id", string(request)), slog.String("error", err.Message))
		return
	}
	fn := functionARN(scope, got.event.Function)
	for _, problem := range got.sent.problems {
		x.k.Log().LogAttrs(ctx, slog.LevelError, "Lambda could not send an asynchronous invocation's record or dead letter; the other destinations still receive theirs",
			slog.String("function", fn), slog.String("request_id", string(request)), slog.String("error", problem.Error()))
	}
	var reason string
	switch p := got.plan.(type) {
	case dropped:
		if got.sent.deliveries > 0 {
			return
		}
		reason = p.reason
	case abandoned:
		reason = p.reason
	case retryAt, finished, nil:
		return
	}
	x.k.Log().LogAttrs(ctx, slog.LevelWarn, "Lambda discarded an asynchronous invocation; no destination or dead-letter queue receives it",
		slog.String("function", fn), slog.String("request_id", string(request)), slog.String("reason", reason))
}

// errNoSenderRole is why an event's record and dead letter were not sent:
// its version's stored execution role is not an ARN, so there is no role
// to deliver as.
var errNoSenderRole = errors.New("the version's stored execution role is not an ARN, so Lambda has no role to deliver its record or dead letter as; set a valid role with UpdateFunctionConfiguration")

// sent is what sendRecords handed the outbox: how many deliveries, and why
// any it could not build was left out.
type sent struct {
	deliveries int
	problems   []error
}

// sendRecords hands tx the deliveries of ev ending for condition, having run
// as inv, at now: the record to the invoked qualifier's on-success or
// on-failure destination, and for a failure the payload to the function's
// dead-letter target, both when both are set (lambda-destinations-layers.md
// 5.3, 11.3). The record's functionArn is the invoked qualifier's, $LATEST
// for the unqualified function (switch
// lambda.destination.record-function-arn, LDL-01). A response that is not
// JSON leaves responsePayload out and journals why (11.4, LDL-02).
func (x *Execution) sendRecords(tx *kernel.Tx, scope aws.Scope, ev asyncEvent, cfg asyncConfig, now clock.Instant, condition destination.Condition, inv destination.Invocation) sent {
	var s sent
	target := cfg.onFailure
	if condition == destination.Success {
		target = cfg.onSuccess
	}
	if target == "" && (!condition.Failed() || cfg.deadLetter == "") {
		return s
	}
	if cfg.role == (aws.ARN{}) {
		// Only a stored role that is not an ARN leaves it unknown: resolve
		// refuses such a version, so its event requeues until it ages out.
		s.problems = append(s.problems, errNoSenderRole)
		return s
	}
	invoked := functionRef{scope: scope, name: ev.Function, qualifier: ev.Qualifier}
	qualified := invokedARN(functionRef{scope: scope, name: ev.Function, qualifier: invoked.eventInvokeKey()})
	sender := destination.Sender{
		Function:  invokedARN(functionRef{scope: scope, name: ev.Function}),
		Qualified: qualified.String(),
		Role:      cfg.role,
		Trace:     ev.TraceHeader,
	}
	if target != "" {
		req, omitted, err := x.invocationRecord(sender, ev, target, now, condition, inv)
		if err != nil {
			s.problems = append(s.problems, err)
		} else {
			tx.Deliver(req)
			s.deliveries++
			if omitted {
				tx.Record(scope, journal.Advisory{
					Kind: journal.AdvisoryRecordResponseOmitted, Resource: qualified,
					Text: "the function's response for request " + string(ev.RequestID) + " is not JSON, so its " + condition.String() +
						" record leaves out responsePayload: how AWS renders it is unrecorded (lambda-destinations-layers.md 11.4, LDL-02)",
				})
			}
		}
	}
	if condition.Failed() && cfg.deadLetter != "" {
		dlq, err := aws.ParseARN(cfg.deadLetter)
		var req delivery.Request
		if err == nil {
			req, err = destination.DeadLetter(dlq, sender, ev.RequestID, condition, inv, ev.Payload)
		}
		if err != nil {
			s.problems = append(s.problems, err)
		} else {
			tx.Deliver(req)
			s.deliveries++
		}
	}
	return s
}

// invocationRecord builds ev's invocation record for condition and wraps
// it for the destination target; omitted reports a response left out of
// it.
func (x *Execution) invocationRecord(sender destination.Sender, ev asyncEvent, target string, now clock.Instant, condition destination.Condition, inv destination.Invocation) (req delivery.Request, omitted bool, err error) {
	d, err := destination.Parse(target)
	if err != nil {
		return delivery.Request{}, false, err
	}
	built, err := destination.Build(destination.Record{
		At: now.Wire(), RequestID: ev.RequestID, FunctionARN: sender.Qualified, Condition: condition, Invocation: inv, Payload: ev.Payload,
	})
	if err != nil {
		return delivery.Request{}, false, err
	}
	o := destination.Outgoing{Condition: condition, Body: built.JSON}
	if _, ok := d.(destination.Bucket); ok {
		// The key's UUID is drawn only for a bucket, so other destinations
		// leave the seeded id streams as they were.
		o.ObjectKey = destination.ObjectKey(ev.Function, now.Wire(), x.k.IDs().LambdaRecordKey())
	}
	req, err = destination.To(d, sender, o)
	return req, built.ResponseOmitted, err
}

// asyncQueueVersion is the schema version of the queue's sections. 2: an
// event keeps the Invoke's trace header and its last failed response.
const asyncQueueVersion = 2

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
		// Armed under the cell lock, where a write arms, so a concurrent
		// write either commits first and is read here, or commits after
		// and its own arm replaces this one.
		store.Read(cell, func(qu *asyncQueue) struct{} {
			for _, id := range slices.Sorted(maps.Keys(qu.Events)) {
				ev := qu.Events[id]
				due := ev.Due
				if due.Before(now) {
					due = now
				}
				sched.Arm(q.x.async.Key(asyncTimerID(scope, ev.Function, ev.RequestID)), due)
			}
			return struct{}{}
		})
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
