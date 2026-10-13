package kernel

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WorkKind is the closed set of work the tracker counts for settling.
// Parked waiters never count: a client long poll, an idle poller waiting on
// its key, a journal stream.
type WorkKind uint8

// WorkKind values.
const (
	// EffectPending is a committed write's wake or delivery not yet run.
	EffectPending WorkKind = iota + 1
	// DeliveryAttempt is a cross-service delivery attempt in progress.
	DeliveryAttempt
	// AsyncInvokeQueued is an accepted asynchronous Lambda invocation.
	AsyncInvokeQueued
	// InvocationRunning is a function invocation or a poller cycle running.
	InvocationRunning
	// TimerDue is a timer whose due instant has passed and whose handler has
	// not finished.
	TimerDue
)

// String returns the kind's name, such as "timer-due".
func (w WorkKind) String() string {
	switch w {
	case EffectPending:
		return "effect-pending"
	case DeliveryAttempt:
		return "delivery-attempt"
	case AsyncInvokeQueued:
		return "async-invoke-queued"
	case InvocationRunning:
		return "invocation-running"
	case TimerDue:
		return "timer-due"
	default:
		return "WorkKind(" + strconv.Itoa(int(w)) + ")"
	}
}

// Pending is outstanding work grouped by kind; an empty Pending is settled.
type Pending map[WorkKind]int

// String lists the kinds in their declared order: "effect-pending=1,
// timer-due=2".
func (p Pending) String() string {
	parts := make([]string, 0, len(p))
	for _, k := range slices.Sorted(maps.Keys(p)) {
		parts = append(parts, k.String()+"="+strconv.Itoa(p[k]))
	}
	return strings.Join(parts, ", ")
}

// tracker counts work in flight by kind and signals every change.
type tracker struct {
	mu      sync.Mutex
	counts  map[WorkKind]int
	changed chan struct{}
}

func newTracker() *tracker {
	return &tracker{counts: map[WorkKind]int{}, changed: make(chan struct{})}
}

func (t *tracker) add(kind WorkKind, n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts[kind] += n
	t.signalLocked()
}

func (t *tracker) done(kind WorkKind, n int) {
	if n == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts[kind] -= n
	if t.counts[kind] <= 0 {
		delete(t.counts, kind)
	}
	t.signalLocked()
}

func (t *tracker) signalLocked() {
	close(t.changed)
	t.changed = make(chan struct{})
}

// snapshot returns the counts and the channel the next change closes.
func (t *tracker) snapshot() (p Pending, changed <-chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return maps.Clone(t.counts), t.changed
}

// PendingEffects is the count of committed writes' wakes and deliveries not
// yet run, and a channel closed by the next change to the tracked work. A
// write adds its effects inside store.Update, which holds the gate's shared
// side, so a reader holding the gate exclusively sees every committed one.
func (k *Kernel) PendingEffects() (n int, changed <-chan struct{}) {
	p, changed := k.work.snapshot()
	return p[EffectPending], changed
}

// Go runs fn on a goroutine the kernel owns, counted as kind until it
// returns. fn's context ends when Run stops; a panic is logged with its
// stack and ends only fn. After Run has stopped, Go does not start fn.
func (k *Kernel) Go(kind WorkKind, fn func(ctx context.Context)) {
	ctx, cancel := context.WithCancel(context.Background())
	// The stopped check and the WaitGroup's Add happen under k.mu, so every
	// Add precedes the Wait Run starts after setting stopped.
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stopped {
		cancel()
		return
	}
	k.cancels[&cancel] = struct{}{}
	k.work.add(kind, 1)
	k.wg.Go(func() {
		defer k.work.done(kind, 1)
		defer func() {
			k.mu.Lock()
			delete(k.cancels, &cancel)
			k.mu.Unlock()
			cancel()
		}()
		defer func() {
			if v := recover(); v != nil {
				k.log.LogAttrs(ctx, slog.LevelError, "tracked work panicked",
					slog.String("kind", kind.String()), slog.String("panic", fmt.Sprint(v)), slog.String("stack", string(debug.Stack())))
			}
		}()
		fn(ctx)
	})
}

// pending is the tracked work plus, as TimerDue, the timers due or being
// fired, in every clock mode. changed is closed by the next change to the
// tracked work, and dropped when the timer count can have fallen.
//
// The tracker is read first: work that arms a due timer arms it before it
// is done, so the scheduler read then sees the timer. A timer handler that
// starts tracked work after the tracker read closes changed, which Settle
// checks before it reports settled.
func (k *Kernel) pending() (p Pending, changed, dropped <-chan struct{}) {
	p, changed = k.work.snapshot()
	n, dropped := k.timers.sched.DueOrFiring(k.clock.Now())
	if n > 0 {
		p[TimerDue] = n
	}
	return p, changed, dropped
}

// SettleError reports work still pending when Settle gave up.
type SettleError struct {
	Pending Pending
	Timeout time.Duration
	// Err is why the wait ended: the timeout, or the caller's context.
	Err error
}

func (e *SettleError) Error() string {
	return fmt.Sprintf("kernel: not settled after %s: %s still pending (%v); raise the timeout, or advance the clock if the work waits on emulator time",
		e.Timeout, e.Pending, e.Err)
}

func (e *SettleError) Unwrap() error { return e.Err }

// Settle waits until no tracked work is pending and no timer is due, or
// until timeout of wall time passes. On timeout it returns what is still
// pending, grouped by kind, with a *SettleError. Timers not yet due never
// count, so a frozen clock settles.
func (k *Kernel) Settle(ctx context.Context, timeout time.Duration) (Pending, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		p, changed, dropped := k.pending()
		if len(p) == 0 {
			select {
			case <-changed:
				// Work started between the two reads; read again.
				continue
			default:
				return p, nil
			}
		}
		select {
		case <-changed:
		case <-dropped:
		case <-ctx.Done():
			return p, &SettleError{Pending: p, Timeout: timeout, Err: context.Cause(ctx)}
		}
	}
}
