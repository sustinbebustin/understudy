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
// it) and the commit instant it was accepted at.
type Delivery struct {
	Request  delivery.Request
	Cause    ref.Cause
	Accepted clock.Instant
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
	// records are journal events applied in the critical section, in
	// Record order, before the timers.
	records []recordEffect
	err     error
	// unchanged is set by Unchanged: the cell's revision stays.
	unchanged bool
}

// Unchanged tells the write that fn left the state exactly as it was (an
// empty receive, an idempotent create), so the cell's revision does not
// advance and persistence has nothing new to save. Its timers, wakes and
// deliveries still apply. Calling it after changing the state loses that
// change from the next persisted archive.
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
// cell's lock, with now read under the lock (the commit instant, F17). A
// cell that reset or snapshot load retired is resolved again, so a long poll
// survives both. When fn returns an error the state must be unchanged
// (validate first, mutate last) and no effect runs.
//
// The call is marked as holding while fn runs: a nested Write, or an Invoke
// made from fn, returns Internal naming both operations. Timer arms and
// disarms apply inside the critical section in commit order; wakes and
// deliveries run after unlock, stamped with the call's cause and counted by
// the work tracker.
func Write[K comparable, S, R any](c *Call, p *store.Partitioned[K, S], key K, fn func(s *S, now clock.Instant, tx *Tx) (R, *awserr.Error)) (R, *awserr.Error) {
	var zero R
	if !c.holding.CompareAndSwap(false, true) {
		return zero, awserr.Internalf(&HoldingError{Attempted: "kernel.Write", Holder: c.label})
	}
	defer c.holding.Store(false)
	for {
		res := commit(c, p.For(key), fn)
		if res.retired {
			continue
		}
		if res.err != nil {
			return zero, res.err
		}
		if err := c.k.afterUnlock(c, res.tx); err != nil {
			return zero, err
		}
		if res.tx.err != nil {
			return zero, awserr.Internalf(res.tx.err)
		}
		return res.r, nil
	}
}

// committed is the outcome of one attempt at a Write.
type committed[R any] struct {
	r   R
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
func commit[S, R any](c *Call, cell *store.Cell[S], fn func(s *S, now clock.Instant, tx *Tx) (R, *awserr.Error)) committed[R] {
	k := c.k
	return store.Update(cell, func(s *S) (committed[R], bool) {
		if cell.Retired() {
			return committed[R]{retired: true}, false
		}
		tx := &Tx{k: k, now: k.clock.Now()}
		r, err := fn(s, tx.now, tx)
		if err != nil {
			return committed[R]{err: err}, false
		}
		for _, rec := range tx.records {
			if err := k.record(c, tx.now, rec.scope, rec.detail); err != nil {
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
		if n := len(tx.wakes) + len(tx.sends); n > 0 {
			k.work.add(EffectPending, n)
		}
		return committed[R]{r: r, tx: tx}, !tx.unchanged
	})
}

// afterUnlock runs a committed write's wakes and deliveries. A refused
// delivery is the call's Internal error, logged and journaled by Dispatch.
func (k *Kernel) afterUnlock(c *Call, tx *Tx) *awserr.Error {
	defer k.work.done(EffectPending, len(tx.wakes)+len(tx.sends))
	for _, key := range tx.wakes {
		k.waiters.Wake(key)
	}
	cause := ref.Cause{Parent: c.event, Trace: c.cause.Trace}
	for i := range tx.sends {
		if err := k.deliver.Deliver(Delivery{Request: tx.sends[i], Cause: cause, Accepted: tx.now}); err != nil {
			return awserr.Internalf(&DeliverError{Err: err})
		}
	}
	return nil
}
