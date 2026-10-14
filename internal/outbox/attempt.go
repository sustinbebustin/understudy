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
// is stale and does nothing.
//
// The clock dispatches timers one at a time in firing order, and the
// outbox keeps that order: the Delivery-phase rules are evaluated here, and
// the attempt joins the sequencer, which makes the target calls one at a
// time in the order onAttempt queued them. Attempts due together then
// spend a NextN budget, draw from a seeded Probability stream and draw
// seeded ids in that order, in both fault phases, not in the order
// goroutines happen to run. An attempt that would hold the sequencer for
// wall time runs on its own goroutine instead (attempt.blocks).
//
// The claim is a plain cell update, not a kernel.Write: it has no effects,
// and Write would create the cell of a scope a reset just dropped. It
// leaves the revision alone, since Running is never saved.
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
	entry := store.Update(cell, func(st *State) (*Entry, bool) {
		en, ok := st.Entries[id]
		if cell.Retired() || !ok || en.Running || en.NextAt.Compare(due) != 0 {
			return nil, false
		}
		en.Running = true
		st.Entries[id] = en
		return &en, false
	})
	if entry == nil {
		return
	}
	a := &attempt{e: e, c: c, cell: cell, scope: scope, entry: *entry, due: due, decision: e.evaluateFaults(scope, entry)}
	if a.blocks() {
		e.k.Go(kernel.DeliveryAttempt, a.do)
		return
	}
	e.sequence(a)
}

// evaluateFaults returns the Delivery-phase fault decision for the attempt
// of en into scope, None when the outbox has no fault stage.
func (e *engine) evaluateFaults(scope aws.Scope, en *Entry) fault.Decision {
	if e.faults == nil {
		return fault.None{}
	}
	req := en.Request
	target, _ := targetARN(req.Target)
	return e.faults.Evaluate(fault.DeliveryAttempt, fault.MatchInput{
		Service:   req.Mechanism.Target(),
		Mechanism: req.Mechanism,
		Touched:   []aws.ARN{target},
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
}

// blocks reports whether the attempt would hold the sequencer for wall
// time: a Delivery-phase latency rule holds it, or it invokes a function
// synchronously, which waits for the function to run. Such an attempt runs
// on its own goroutine, so its target call is not ordered with the others.
func (a *attempt) blocks() bool {
	if _, delayed := a.decision.(fault.Delay); delayed {
		return true
	}
	f, ok := a.entry.Request.Target.(delivery.LambdaFunction)
	return ok && f.Invocation == delivery.InvocationRequestResponse
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
	err := a.send(ctx, a.entry.Request.Target)
	if ctx.Err() != nil {
		return nil, false
	}
	switch {
	case err == nil:
		return journal.Delivered{}, true
	case unimplemented(err):
		return journal.Dropped{Reason: describe(err)}, true
	case retryable(err.ID):
		if next, ok := nextAttempt(&a.entry, a.due); ok {
			return journal.Retrying{Next: next}, true
		}
		return a.deadLetter(ctx, err, exhausted)
	default:
		return a.deadLetter(ctx, err, refused)
	}
}

// faults applies the attempt's Delivery-phase decision: it journals each
// rule hit, waits out a delay and drops the attempt. decided is true when a
// rule dropped the attempt; ok is false when the kernel stopped during an
// injected delay.
func (a *attempt) faults(ctx context.Context) (outcome journal.DeliveryOutcome, decided, ok bool) {
	decision := a.decision
	for {
		switch d := decision.(type) {
		case fault.Drop:
			a.journal(ctx, journal.FaultInjected{Rule: d.Rule, Hit: d.Hit, Of: d.Of})
			return journal.Dropped{Reason: fmt.Sprintf("fault rule %s dropped the attempt", d.Rule)}, true, true
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
		case fault.Delay:
			a.journal(ctx, journal.FaultInjected{Rule: d.Rule, Hit: d.Hit, Of: d.Of})
			decision = d.Then
		case fault.None, fault.Fail, fault.FailAfterCommit, fault.FailStatusOK, nil:
			return
		}
	}
}

// noEmulatorDeadline stands for "no emulator deadline" in a wall-time wait.
const noEmulatorDeadline = 100 * 365 * 24 * time.Hour

// wait holds the attempt for d of wall time, which a frozen clock cannot
// stretch; false when the kernel stopped first.
func (a *attempt) wait(ctx context.Context, d time.Duration) bool {
	clk := a.e.k.Clock()
	<-clk.Until(ctx, clock.Deadline{Emu: clk.Now().Add(noEmulatorDeadline), WallCap: clk.Wall().Add(d)})
	return ctx.Err() == nil
}

// advise journals an Advisory when the target's stored resource policy
// would not let the delivering service make the call in AWS. understudy
// delivers anyway (ARCHITECTURE.md 16).
func (a *attempt) advise(ctx context.Context) {
	if a.e.policies == nil {
		return
	}
	req := a.entry.Request
	target, _ := targetARN(req.Target)
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
// drops it when it has none or the queue refuses it. err is the target's
// last error and end why it was not retried again.
func (a *attempt) deadLetter(ctx context.Context, err *awserr.Error, end ending) (journal.DeliveryOutcome, bool) {
	why := end.reason(err)
	dlq := a.entry.Request.DLQ
	if dlq == nil {
		return journal.Dropped{Reason: "no dead-letter queue is set and " + why}, true
	}
	scope, scopeErr := scopeOf(*dlq)
	if scopeErr != nil {
		return journal.Dropped{Reason: fmt.Sprintf("%s; the dead-letter queue %s: %v", why, unservedScope, scopeErr)}, true
	}
	sendErr := a.sendSQS(ctx, scope, deadLetterMessage(&a.entry, *dlq, err, end))
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
	target, _ := targetARN(req.Target)
	event := journal.Delivery{From: req.Source, To: target, Mechanism: req.Mechanism, Attempt: a.entry.Attempt + 1, Outcome: outcome}
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
	case delivery.SNSToSQS, delivery.SNSToLambda:
		return "sns.amazonaws.com"
	case delivery.S3ToSQS, delivery.S3ToSNS, delivery.S3ToLambda:
		return "s3.amazonaws.com"
	case delivery.EventsToLambda, delivery.EventsToSQS, delivery.EventsToSNS:
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
