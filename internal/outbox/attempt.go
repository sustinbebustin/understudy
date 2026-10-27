package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/egress"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// onAttempt is TimerAttempt's handler. It must not block, so it claims the
// entry (Entry.Running), evaluates the Delivery-phase fault rules and hands
// the attempt due at due to tracked work, which Settle counts until it
// ends. A timer whose entry is gone, rescheduled or already being attempted
// is stale and does nothing. An attempt its subscription's throttle paces
// is re-armed for the instant the throttle allows instead (pacer).
//
// The clock dispatches timers one at a time in firing order, and the
// outbox keeps that order: the Delivery-phase rules are evaluated here, and
// the attempt joins the sequencer, which makes the target calls one at a
// time in the order onAttempt queued them. Attempts due together then
// spend a NextN budget, draw from a seeded Probability stream and draw
// seeded ids in that order, in both fault phases, not in the order
// goroutines happen to run. An attempt that would hold the sequencer for
// wall time runs on its own goroutine instead (attempt.blocks), or, for a
// throttled subscription, on that subscription's sequencer (pacer).
//
// The claim is a plain cell update, not a kernel.Write: it has no effects,
// and Write would create the cell of a scope a reset just dropped. It
// leaves the revision alone, since Running is never saved. A paced
// attempt's re-arm is a plain update too, like Enqueue's arm.
func (e *engine) onAttempt(c *kernel.Call, key clock.TimerKey, due clock.Instant) {
	scope, id, ok := parseTimerID(key.ID)
	if !ok {
		e.k.Log().LogAttrs(context.Background(), slog.LevelError, "outbox timer names no delivery", slog.String("timer", key.ID))
		return
	}
	cell, ok := e.cells.Lookup(scope)
	if !ok {
		return
	}
	var throttled *sequencer
	entry := store.Update(cell, func(st *State) (*Entry, bool) {
		en, ok := st.Entries[id]
		if cell.Retired() || !ok || en.Running || en.NextAt.Compare(due) != 0 {
			return nil, false
		}
		rearm, paced, ready := e.pace.admit(&en, due)
		if paced {
			// Re-armed under the cell lock, as Enqueue arms, its attempt
			// number unchanged: a paced attempt is not made, so it spends
			// no retry and no fault rule's budget.
			en.NextAt = rearm
			st.Entries[id] = en
			e.k.Timers().Scheduler().Arm(key, rearm)
			return nil, true
		}
		throttled = ready
		en.Running = true
		st.Entries[id] = en
		return &en, false
	})
	if entry == nil {
		return
	}
	a := &attempt{e: e, c: c, cell: cell, scope: scope, entry: *entry, due: due, decision: e.evaluateFaults(scope, entry)}
	switch {
	case throttled != nil:
		e.sequence(throttled, a)
	case a.blocks():
		e.k.Go(kernel.DeliveryAttempt, a.do)
	default:
		e.sequence(&e.ready, a)
	}
}

// evaluateFaults returns the Delivery-phase fault decision for the attempt
// of en into scope, None when the outbox has no fault stage.
func (e *engine) evaluateFaults(scope aws.Scope, en *Entry) fault.Decision {
	if e.faults == nil {
		return fault.None{}
	}
	req := en.Request
	var touched []aws.ARN
	if to := deliveredTo(req); to != (aws.ARN{}) {
		touched = []aws.ARN{to}
	}
	return e.faults.Evaluate(fault.DeliveryAttempt, fault.MatchInput{
		Service:   req.Mechanism.Target(),
		Mechanism: req.Mechanism,
		Touched:   touched,
		Scope:     scope,
		Origin:    ref.Internal{Via: req.Mechanism.Via()},
	})
}

// attempt is one attempt of a delivery: its entry as it was when the
// attempt became due, the cell it was read from, the scope of its target,
// the due instant, which stamps every call and journal event it makes, and
// the fault decision onAttempt made for it.
type attempt struct {
	e *engine
	c *kernel.Call
	// cell is the cell the entry was read from; once a reset or snapshot
	// load retires it, the attempt's outcome is stale.
	cell     *store.Cell[State]
	scope    aws.Scope
	entry    Entry
	due      clock.Instant
	decision fault.Decision
	// took is the wall time an HTTP attempt's exchange took through egress:
	// 0 for an injected answer. exchange is the HTTP attempt's account for
	// its Delivery event, nil for every other attempt and for one a rule
	// dropped.
	took     time.Duration
	exchange *journal.HTTPExchange
}

// blocks reports whether the attempt would hold the sequencer for wall
// time: a Delivery-phase latency rule holds it, it invokes a function
// synchronously, which waits for the function to run, or it posts to an
// HTTP endpoint, which waits on the network. Such an attempt runs on its
// own goroutine, so its target call is not ordered with the others. One a
// rule drops, or answers in place of its HTTP endpoint, makes no target
// call, so it stays on the sequencer, and its dead letter keeps the firing
// order.
func (a *attempt) blocks() bool {
	switch a.decision.(type) {
	case fault.Delay:
		return true
	case fault.Drop, fault.RespondHTTP, fault.TimeOutHTTP:
		return false
	case fault.None, fault.Fail, fault.FailAfterCommit, fault.FailStatusOK, nil:
	}
	switch t := a.entry.Request.Target.(type) {
	case delivery.LambdaFunction:
		return t.Invocation == delivery.InvocationRequestResponse
	case delivery.HTTPEndpoint:
		return true
	case delivery.SQSQueue, delivery.SNSTopic, delivery.EventBus, delivery.S3Object:
	}
	return false
}

// do makes the attempt and records its outcome.
func (a *attempt) do(ctx context.Context) {
	// finish ends the claim with the outcome; this releases it when the
	// kernel stopped first or the attempt panicked, so the next Rearm can
	// attempt the delivery again.
	defer a.release()
	if outcome, ok := a.run(ctx); ok {
		a.finish(ctx, outcome)
	}
}

// release ends the attempt's claim on its entry if it still holds it: the
// cell is live and the entry is pending at the attempt's number. After
// finish it finds the entry gone or moved on and does nothing.
func (a *attempt) release() {
	id := a.entry.ID
	store.Update(a.cell, func(st *State) (struct{}, bool) {
		cur, ok := st.Entries[id]
		if a.cell.Retired() || !ok || !cur.Running || cur.Attempt != a.entry.Attempt {
			return struct{}{}, false
		}
		cur.Running = false
		st.Entries[id] = cur
		return struct{}{}, false
	})
}

// run makes the attempt and returns how it ended; false when the kernel
// stopped first, which leaves the delivery pending for the next Rearm.
//
// An attempt the stop finds still queued neither advises nor calls its
// target: kernel.Invoke does not refuse a done context, so the call would
// commit while its outcome is discarded, and the Rearm would deliver it
// again. It journals only the fault hits onAttempt decided for it, whose
// budgets are spent.
func (a *attempt) run(ctx context.Context) (journal.DeliveryOutcome, bool) {
	if ctx.Err() != nil {
		a.journalSpent(ctx, a.decision)
		return nil, false
	}
	if outcome, decided, ok := a.faults(ctx); !ok || decided {
		return outcome, ok
	}
	a.advise(ctx)
	return a.send(ctx, a.entry.Request.Target)
}

// end is the outcome of an attempt whose target call answered err, nil
// when it delivered; false when the kernel stopped first.
func (a *attempt) end(ctx context.Context, err *awserr.Error) (journal.DeliveryOutcome, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	switch {
	case err == nil:
		return journal.Delivered{}, true
	case unimplemented(err):
		return journal.Dropped{Reason: describe(err)}, true
	case retryable(err.ID):
		return a.retry(ctx, targetError{err: err}, a.due)
	default:
		return a.deadLetter(ctx, targetError{err: err}, refused)
	}
}

// endHTTP is the outcome of an HTTPEndpoint attempt that ended as r; false
// when the kernel stopped first. By the reading sns-http-delivery.md 6.1
// recommends, a 2xx delivers; 429, 5xx and a failed or timed-out exchange
// are retried; any other answer is a permanent failure, dead-lettered when
// the delivery has a queue. 6.1 leaves UNVERIFIED whether a
// connection-level failure is retried (SNSH-24), and the class of 3xx and
// other 4xx and whether a permanent failure goes to the DLQ (SNSH-05). A
// request egress refused, or one with no client to send it, is Refused:
// never retried or dead-lettered (7.3). The retry delay runs from the end
// of the attempt (attemptEnd), waited of wall time after due. The exchange
// is kept for the attempt's Delivery event.
func (a *attempt) endHTTP(ctx context.Context, r httpResult, waited time.Duration) (journal.DeliveryOutcome, bool) {
	a.exchange = exchangeOf(r, a.took)
	if ctx.Err() != nil {
		return nil, false
	}
	switch r := r.(type) {
	case httpAccepted:
		return journal.Delivered{}, true
	case httpRetry:
		ended := a.due
		if waited > 0 {
			ended = attemptEnd(a.e.k.Clock().Mode(), a.due, waited)
		}
		return a.retry(ctx, endpointError{reason: r.reason}, ended)
	case httpPermanent:
		return a.deadLetter(ctx, endpointError{reason: statusText(r.status)}, refused)
	case httpRefused:
		return journal.Refused{Reason: r.reason}, true
	}
	return journal.Dropped{Reason: "the HTTP attempt reported no result"}, true
}

// retry arms the delivery's next attempt when its retry policy allows
// one, its delay running from ended, and otherwise dead-letters it, f
// being how this attempt failed.
func (a *attempt) retry(ctx context.Context, f failure, ended clock.Instant) (journal.DeliveryOutcome, bool) {
	if next, ok := nextAttempt(&a.entry, ended); ok {
		return journal.Retrying{Next: next}, true
	}
	return a.deadLetter(ctx, f, exhausted)
}

// faults applies the attempt's Delivery-phase decision: it journals each
// rule hit, waits out a delay, drops the attempt or answers an HTTP attempt
// without its endpoint, an injected status as a real answer of that status
// and an injected timeout as a timed-out exchange (sns-http-delivery.md 8.2
// item 2). Neither waits or records a duration, but an injected timeout's
// retry is scheduled from the end of the 15-second deadline it stands for
// (3.1), as a real timeout's is under a running or scaled clock; on a
// frozen clock that end is the due instant. decided is true when a rule
// decided the attempt; ok is false when the kernel stopped during an
// injected delay.
func (a *attempt) faults(ctx context.Context) (outcome journal.DeliveryOutcome, decided, ok bool) {
	decision := a.decision
	for {
		switch d := decision.(type) {
		case fault.Drop:
			a.journal(ctx, journal.FaultInjected{Rule: d.Rule, Hit: d.Hit, Of: d.Of})
			return journal.Dropped{Reason: fmt.Sprintf("fault rule %s dropped the attempt", d.Rule)}, true, true
		case fault.RespondHTTP:
			a.journal(ctx, journal.FaultInjected{Rule: d.Rule, Hit: d.Hit, Of: d.Of})
			outcome, ok := a.endHTTP(ctx, statusResult(d.Status), 0)
			return outcome, true, ok
		case fault.TimeOutHTTP:
			a.journal(ctx, journal.FaultInjected{Rule: d.Rule, Hit: d.Hit, Of: d.Of})
			outcome, ok := a.endHTTP(ctx, httpRetry{reason: fmt.Sprintf("fault rule %s timed out the attempt", d.Rule)}, egress.DefaultTimeout)
			return outcome, true, ok
		case fault.Delay:
			a.journal(ctx, journal.FaultInjected{Rule: d.Rule, Hit: d.Hit, Of: d.Of})
			if !a.wait(ctx, d.Wall) {
				a.journalSpent(ctx, d.Then)
				return nil, false, false
			}
			decision = d.Then
		case fault.None, fault.Fail, fault.FailAfterCommit, fault.FailStatusOK, nil:
			return nil, false, true
		}
	}
}

// journalSpent journals the hits Evaluate decided after a delay the
// attempt abandoned: their budgets are spent.
func (a *attempt) journalSpent(ctx context.Context, decision fault.Decision) {
	for {
		switch d := decision.(type) {
		case fault.Drop:
			a.journal(ctx, journal.FaultInjected{Rule: d.Rule, Hit: d.Hit, Of: d.Of})
			return
		case fault.RespondHTTP:
			a.journal(ctx, journal.FaultInjected{Rule: d.Rule, Hit: d.Hit, Of: d.Of})
			return
		case fault.TimeOutHTTP:
			a.journal(ctx, journal.FaultInjected{Rule: d.Rule, Hit: d.Hit, Of: d.Of})
			return
		case fault.Delay:
			a.journal(ctx, journal.FaultInjected{Rule: d.Rule, Hit: d.Hit, Of: d.Of})
			decision = d.Then
		case fault.None, fault.Fail, fault.FailAfterCommit, fault.FailStatusOK, nil:
			return
		}
	}
}

// wait holds the attempt for d of wall time, which a frozen clock cannot
// stretch; false when the kernel stopped first.
func (a *attempt) wait(ctx context.Context, d time.Duration) bool {
	clk := a.e.k.Clock()
	<-clk.Until(ctx, clk.WallDeadline(clk.Wall().Add(d)))
	return ctx.Err() == nil
}

// advise journals an Advisory when the target's stored resource policy
// would not let the delivering service make the call in AWS. understudy
// delivers anyway (ARCHITECTURE.md 16).
//
// An events-to-events delivery gets none: AWS authorizes an event bus
// target through its RoleArn, or the sender account against the receiving
// bus's policy (eventbridge-buses.md 6.2, 6.3), not through a grant to
// events.amazonaws.com, so this model would misreport every same-account
// forward.
func (a *attempt) advise(ctx context.Context) {
	req := a.entry.Request
	if a.e.policies == nil || req.Mechanism == delivery.EventsToEvents {
		return
	}
	target := deliveredTo(req)
	op := operationOf(req.Target)
	if op == nil || len(op.IAM) == 0 {
		return
	}
	doc, ok := a.e.policies.ResourcePolicy(target)
	if !ok {
		return
	}
	principal := access.ServicePrincipal{Name: servicePrincipal(req.Mechanism)}
	if d, ok := access.Advise(doc, principal, op.IAM[0], req.Source, target).(access.WouldDeny); ok {
		a.journal(ctx, journal.Advisory{Kind: journal.AdvisoryPolicyWouldDeny, Resource: target, Text: d.Reason})
	}
}

// as is how every call of the attempt runs: in scope, as the delivering
// service on behalf of the source, admitted at the due instant and caused
// by the source call, like the attempt's Delivery event.
func (a *attempt) as(scope aws.Scope) kernel.Target {
	req := a.entry.Request
	return kernel.Target{
		Scope:  scope,
		As:     kernel.AsService{Name: servicePrincipal(req.Mechanism), Source: req.Source},
		Via:    req.Mechanism.Via(),
		At:     a.due,
		Resume: a.entry.Cause,
	}
}

// deadLetter sends the delivery's dead letter to its dead-letter queue, or
// drops it when it has none or the queue refuses it. f is how the last
// attempt failed and end why it was not retried again.
func (a *attempt) deadLetter(ctx context.Context, f failure, end ending) (journal.DeliveryOutcome, bool) {
	why := end.reason(f)
	dlq := a.entry.Request.DLQ
	if dlq == nil {
		return journal.Dropped{Reason: "no dead-letter queue is set and " + why}, true
	}
	scope, scopeErr := scopeOf(*dlq)
	if scopeErr != nil {
		return journal.Dropped{Reason: fmt.Sprintf("%s; the dead-letter queue %s: %v", why, unservedScope, scopeErr)}, true
	}
	m, msgErr := deadLetterOf(&a.entry, *dlq, f, end)
	if msgErr != nil {
		return journal.Dropped{Reason: fmt.Sprintf("%s; its dead letter for %s could not be written: %v", why, dlq, msgErr)}, true
	}
	sendErr := a.sendSQS(ctx, scope, m)
	if ctx.Err() != nil {
		return nil, false
	}
	if sendErr != nil {
		return journal.Dropped{Reason: fmt.Sprintf("%s; the dead-letter queue %s refused it with %s", why, dlq, describe(sendErr))}, true
	}
	return journal.DeadLettered{DLQ: *dlq}, true
}

// finish records the outcome: the entry is rescheduled when retrying and
// removed otherwise, then the attempt is journaled with the source call as
// its parent. A terminal outcome arms the next delivery of the entry's FIFO
// lane, due no earlier than this attempt, so a later message never carries
// an earlier instant than the one it waited for.
//
// A Delivery event claims the state change it describes, so it is
// journaled only when the write made that change. The write is stale, and
// changes and journals nothing, when the cell the entry came from was
// retired (a reset or snapshot load replaced it, even with an entry of the
// same id and attempt, which Rearm attempts afresh), or when the entry is
// gone or no longer at this attempt's number. The event is appended
// inside the write, before it commits: the write arms the next attempt's
// timer under the cell's lock, and that attempt could otherwise journal
// its own outcome before this one, so the journal would show a retry after
// the delivery it led to. The write can fail only on a zero HandledKind,
// which Bind rules out; if it ever fails, the error is logged. The outcome
// is then recorded but the next attempt's timer is not armed, so the next
// Rearm arms it.
func (a *attempt) finish(ctx context.Context, outcome journal.DeliveryOutcome) {
	id := a.entry.ID
	req := a.entry.Request
	event := journal.Delivery{
		From: req.Source, To: deliveredTo(req), Endpoint: endpointOf(req.Target),
		Mechanism: req.Mechanism, Attempt: a.entry.Attempt + 1, Outcome: outcome, HTTP: a.exchange,
	}
	changed, err := kernel.Write(a.c, a.e.cells, a.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (bool, *awserr.Error) {
		// Import and Reset retire cells under the gate's exclusive side and
		// Write holds its shared side, so the check cannot race them.
		cur, ok := st.Entries[id]
		if a.cell.Retired() || !ok || cur.Attempt != a.entry.Attempt {
			tx.Unchanged()
			return false, nil
		}
		a.journal(ctx, event)
		if r, retry := outcome.(journal.Retrying); retry {
			// The claim ends in the write that arms the retry, so a retry
			// due at once finds the entry free when its timer fires.
			cur.Attempt++
			cur.NextAt = r.Next
			cur.Running = false
			st.Entries[id] = cur
			tx.Arm(a.e.attempt, timerID(a.scope, id), r.Next)
			return true, nil
		}
		delete(st.Entries, id)
		if l, ok := laneOf(&cur); ok {
			if next, ok := st.nextInLane(l); ok {
				if next.NextAt.Compare(a.due) < 0 {
					next.NextAt = a.due
					st.Entries[next.ID] = next
				}
				tx.Arm(a.e.attempt, timerID(a.scope, next.ID), next.NextAt)
			}
		}
		return true, nil
	})
	if err != nil {
		a.e.k.Log().LogAttrs(ctx, slog.LevelError, "outbox could not record a delivery attempt; the delivery stays pending for the next Rearm",
			slog.String("delivery", string(id)), slog.String("error", describe(err)))
		return
	}
	if !changed {
		a.e.k.Log().LogAttrs(ctx, slog.LevelDebug, "outbox discarded the outcome of a delivery attempt: the delivery was reset, reloaded or moved on while the attempt ran",
			slog.String("delivery", string(id)), slog.Int("attempt", a.entry.Attempt+1))
	}
}

// journal records d at the due instant, caused by the source call.
func (a *attempt) journal(ctx context.Context, d journal.Detail) {
	_, err := a.e.k.Journal().Append(journal.Event{Cause: a.entry.Cause, At: a.due, Scope: a.scope, Detail: d})
	if err != nil {
		a.e.k.Log().LogAttrs(ctx, slog.LevelWarn, "journal refused a delivery event",
			slog.String("delivery", string(a.entry.ID)), slog.String("error", err.Error()))
	}
}

// servicePrincipal is the AWS service principal that makes deliveries by m.
func servicePrincipal(m delivery.Mechanism) string {
	switch m {
	case delivery.SNSToSQS, delivery.SNSToLambda, delivery.SNSToHTTP:
		return "sns.amazonaws.com"
	case delivery.S3ToSQS, delivery.S3ToSNS, delivery.S3ToLambda:
		return "s3.amazonaws.com"
	case delivery.EventsToLambda, delivery.EventsToSQS, delivery.EventsToSNS, delivery.EventsToEvents:
		return "events.amazonaws.com"
	}
	return ""
}

// retryable reports whether a target error is server-side, which the
// delivering service retries: a server fault, a 5xx, a 429 or a throttling
// error. Anything else is client-side (the queue or function is gone, the
// message is invalid for it) and is not retried (sns.md 7.1).
func retryable(id awserr.Identity) bool {
	return id.Retryable || id.Fault == awserr.Receiver || id.Status >= 500 || id.Status == 429
}

// unimplemented reports whether err says understudy does not implement the
// target operation; retrying cannot help, so the delivery is dropped with
// that message.
func unimplemented(err *awserr.Error) bool {
	_, ok := err.Origin.(awserr.Unimplemented)
	return ok
}

// describe renders a target error for a journal reason: its shape and
// message.
func describe(err *awserr.Error) string {
	if err.Message == "" {
		return string(err.ID.Shape)
	}
	return string(err.ID.Shape) + ": " + err.Message
}
