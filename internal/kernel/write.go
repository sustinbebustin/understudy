package kernel

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Delivery is one accepted cross-service delivery, as the DeliverSink
// receives it: the request, the cause it carries (the call that produced
// it), the commit instant it was accepted at and its order number.
type Delivery struct {
	Request  delivery.Request
	Cause    ref.Cause
	Accepted clock.Instant
	// Order is drawn from a kernel counter inside the commit, under the
	// source cell's lock, so the deliveries of two writes to one cell are
	// numbered in commit order even when the later write hands its
	// deliveries over first (Kernel.HandedOver).
	Order uint64
}

// Tx collects the effects of one Write: journal records and timer arms and
// disarms, applied inside the critical section in commit order, and wakes
// and deliveries, run after unlock.
type Tx struct {
	k *Kernel
	// now is the commit instant; deliveries are accepted at it.
	now    clock.Instant
	timers []timerEffect
	wakes  []store.WaitKey
	sends  []delivery.Request
	// orders are the sends' order numbers, drawn once fn committed.
	orders []uint64
	// records are journal events applied in the critical section, in
	// Record order, before the timers.
	records []recordEffect
	err     error
	// unchanged is set by Unchanged: the cell's revision stays.
	unchanged bool
	// resume is the stored cause Resume set, zero for the call's own.
	resume ref.Cause
}

// Resume stamps the write's journal records and deliveries with cause, a
// stored cause the write continues, instead of the writing call's event
// and trace, as Target.Resume does for an internal call (ARCHITECTURE.md
// 10: state carries causes across time). A timer call that finishes work
// another call began, such as an asynchronous invocation the Invoke
// queued, uses it so the work's effects stay in that call's trace rather
// than in the TimerFired event's. The zero cause leaves the call's own.
func (tx *Tx) Resume(cause ref.Cause) { tx.resume = cause }

// Unchanged tells the write that fn left the state exactly as it was (an
// empty receive, an idempotent create), so the cell's revision
// (store.Cell.Rev) does not advance. Its timers, wakes and deliveries still
// apply. The --persist checkpoint does not read the revision: it compares
// each cut's digest with the last saved one, so a change is saved whether
// or not Unchanged was called. Calling it after changing the state only
// leaves the revision behind.
func (tx *Tx) Unchanged() { tx.unchanged = true }

type timerEffect struct {
	key clock.TimerKey
	at  clock.Instant
	arm bool
}

// Wake wakes every waiter on key once the write commits.
func (tx *Tx) Wake(key store.WaitKey) { tx.wakes = append(tx.wakes, key) }

// Arm schedules the timer id of kind at when the write commits, replacing
// a timer with the same key. Only Timers.Handle makes a HandledKind, so the
// timer has a handler; the zero HandledKind is a bug that makes the write an
// Internal error after it commits.
func (tx *Tx) Arm(kind HandledKind, id string, at clock.Instant) {
	if kind.kind == "" {
		tx.fail(&UnregisteredKindError{ID: id})
		return
	}
	tx.timers = append(tx.timers, timerEffect{key: kind.Key(id), at: at, arm: true})
}

// Disarm removes the timer id of kind when the write commits; disarming a
// timer that is not armed does nothing.
func (tx *Tx) Disarm(kind HandledKind, id string) {
	tx.timers = append(tx.timers, timerEffect{key: kind.Key(id)})
}

// Deliver hands r to the delivery sink once the write commits, before the
// call returns. Without a sink the write still commits with its other
// effects, and the call answers Internal.
func (tx *Tx) Deliver(r delivery.Request) {
	if tx.k.deliver == nil {
		tx.fail(&NoDeliverSinkError{})
		return
	}
	tx.sends = append(tx.sends, r)
}

// Record journals d, in scope, as an event the writing call caused, once
// the write commits: at the commit instant, under the cell's lock and
// before any timer the write arms can fire, so the journal never shows an
// effect of the change before the change. A write that fails journals
// nothing. As with Call.Record, the kernel stamps the cause. The journal
// refuses only a malformed detail, a bug that, like the zero HandledKind,
// makes the write an Internal error after it commits.
func (tx *Tx) Record(scope aws.Scope, d journal.Detail) {
	tx.records = append(tx.records, recordEffect{scope: scope, detail: d})
}

type recordEffect struct {
	scope  aws.Scope
	detail journal.Detail
}

func (tx *Tx) fail(err error) {
	if tx.err == nil {
		tx.err = err
	}
}

// cause is what the write's effects are stamped with: the resumed cause,
// or c's.
func (tx *Tx) cause(c *Call) ref.Cause {
	if tx.resume != (ref.Cause{}) {
		return tx.resume
	}
	return c.effectCause()
}

// UnregisteredKindError is a timer armed with the zero HandledKind, which
// no handler stands behind; the timer was not armed.
type UnregisteredKindError struct {
	ID string
}

func (e *UnregisteredKindError) Error() string {
	return fmt.Sprintf("timer %q was armed with the zero HandledKind and was not scheduled; arm with the value Kernel.Timers().Handle returned in the service constructor", e.ID)
}

// RecordError is a Record effect the journal refused: its detail is
// malformed. The write committed without the event.
type RecordError struct {
	Err error
}

func (e *RecordError) Error() string {
	return "a write's journal record was refused and the write committed without it: " + e.Err.Error()
}

func (e *RecordError) Unwrap() error { return e.Err }

// NoDeliverSinkError is a Deliver effect in a kernel built without a
// DeliverSink.
type NoDeliverSinkError struct{}

func (*NoDeliverSinkError) Error() string {
	return "a write produced a cross-service delivery, but the kernel has no DeliverSink; wire the outbox into kernel.Options.Deliver"
}

// DeliverError is a delivery the sink refused.
type DeliverError struct {
	Err error
}

func (e *DeliverError) Error() string {
	return "the delivery sink refused a delivery: " + e.Err.Error()
}

func (e *DeliverError) Unwrap() error { return e.Err }

// Write is the only write entry point for handlers and timer callbacks. It
// runs fn on the state of key's cell under the gate's shared side and the
// cell's lock, with now read under the lock (the commit instant). A
// cell that reset or snapshot load retired is resolved again, so a long poll
// survives both. When fn returns an error the state must be unchanged
// (validate first, mutate last) and no effect runs.
//
// The call is marked as holding while fn runs: a nested Write, or an Invoke
// made from fn, returns Internal naming the attempt and the holding call
// (HoldingError). Journal records and timer arms and disarms apply inside
// the critical section in commit order; wakes and deliveries run after
// unlock, stamped with the call's cause (or the one Tx.Resume set) and
// counted by the work tracker.
func Write[K comparable, S, R any](c *Call, p *store.Partitioned[K, S], key K, fn func(s *S, now clock.Instant, tx *Tx) (R, *awserr.Error)) (R, *awserr.Error) {
	var r R
	err := write(c, func() *store.Cell[S] { return p.For(key) }, func(s *S, now clock.Instant, tx *Tx) *awserr.Error {
		var err *awserr.Error
		r, err = fn(s, now, tx)
		return err
	})
	if err != nil {
		var zero R
		return zero, err
	}
	return r, nil
}

// write is Write over the state type alone, its key and result captured by
// the caller, so its body is compiled once per state type rather than per
// key, state and result (ADR 0001's binary budget). cell resolves the cell
// again for each attempt.
func write[S any](c *Call, cell func() *store.Cell[S], fn func(s *S, now clock.Instant, tx *Tx) *awserr.Error) *awserr.Error {
	if err := c.beginWrite(); err != nil {
		return err
	}
	defer c.holding.Store(false)
	for {
		res := commit(c, cell(), fn)
		if res.retired {
			continue
		}
		if res.err != nil {
			return res.err
		}
		if err := c.k.afterUnlock(c, res.tx); err != nil {
			return err
		}
		if res.tx.err != nil {
			return awserr.Internalf(res.tx.err)
		}
		return nil
	}
}

// beginWrite marks c as holding for a Write, or returns the error the Write
// ends with. A nested Write is refused first, as Invoke refuses a call made
// under a lock, so an AuthorizedAfterResolve call's authorization never runs
// under a cell lock (ADR 0005); that call is asked next, at its first Write,
// before the lock: a denial is what the Write returns.
func (c *Call) beginWrite() *awserr.Error {
	if c.holding.Load() {
		return awserr.Internalf(&HoldingError{Attempted: "kernel.Write", Holder: c.label})
	}
	if err := c.authorizeDeferred(); err != nil {
		return err
	}
	if !c.holding.CompareAndSwap(false, true) {
		return awserr.Internalf(&HoldingError{Attempted: "kernel.Write", Holder: c.label})
	}
	return nil
}

// committed is the outcome of one attempt at a Write.
type committed struct {
	err *awserr.Error
	tx  *Tx
	// retired reports that the cell was retired before fn ran; nothing
	// happened and the write resolves the cell again.
	retired bool
}

// commit runs fn under the cell's lock and applies the timer effects there.
// An effect Tx refused (tx.err) does not undo the others: fn has changed
// the state in place, so its valid timers, wakes and deliveries still run
// and the scheduler agrees with the state; Write then answers Internal.
func commit[S any](c *Call, cell *store.Cell[S], fn func(s *S, now clock.Instant, tx *Tx) *awserr.Error) committed {
	k := c.k
	return store.Update(cell, func(s *S) (committed, bool) {
		if cell.Retired() {
			return committed{retired: true}, false
		}
		tx := &Tx{k: k, now: k.clock.Now()}
		if err := fn(s, tx.now, tx); err != nil {
			return committed{err: err}, false
		}
		for _, rec := range tx.records {
			if err := k.record(tx.cause(c), tx.now, rec.scope, rec.detail); err != nil {
				tx.fail(&RecordError{Err: err})
			}
		}
		for _, t := range tx.timers {
			if t.arm {
				k.timers.sched.Arm(t.key, t.at)
			} else {
				k.timers.sched.Disarm(t.key)
			}
		}
		tx.orders = k.orders.draw(len(tx.sends))
		if n := len(tx.wakes) + len(tx.sends); n > 0 {
			k.work.add(EffectPending, n)
		}
		return committed{tx: tx}, !tx.unchanged
	})
}

// afterUnlock runs a committed write's wakes and deliveries. A refused
// delivery is the call's Internal error, logged and journaled by Dispatch;
// the deliveries after it are not handed over, and every order number the
// write drew stops counting as pending either way.
func (k *Kernel) afterUnlock(c *Call, tx *Tx) *awserr.Error {
	defer k.work.done(EffectPending, len(tx.wakes)+len(tx.sends))
	defer func() {
		for _, n := range tx.orders {
			k.orders.done(n)
		}
	}()
	for _, key := range tx.wakes {
		k.waiters.Wake(key)
	}
	cause := tx.cause(c)
	for i := range tx.sends {
		d := Delivery{Request: tx.sends[i], Cause: cause, Accepted: tx.now, Order: tx.orders[i]}
		err := k.deliver.Deliver(d)
		k.orders.done(d.Order)
		if err != nil {
			return awserr.Internalf(&DeliverError{Err: err})
		}
	}
	return nil
}
