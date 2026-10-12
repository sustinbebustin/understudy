package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// TimerKind names a family of timers; services declare their kinds as
// constants (sqs.TimerVisibility).
type TimerKind = clock.TimerKind

// TimerHandler runs when a timer of its kind is due. It gets a synthetic
// Call with Origin Internal{Via: Timer}, admitted at the due instant, whose
// event is the timer's TimerFired journal event, so it writes through
// kernel.Write like any handler. It must not block: work that waits or
// calls another service runs through Kernel.Go, stamped with due as its
// Target.At. It re-reads state and does nothing when its condition no
// longer holds.
type TimerHandler func(c *Call, key clock.TimerKey, due clock.Instant)

// Timers routes due timers to the handler registered for their kind. Build
// it first, hand Scheduler to clock.New and the table to kernel.New, which
// attaches the kernel the handlers run in.
type Timers struct {
	sched *clock.Scheduler
	k     atomic.Pointer[Kernel]

	mu       sync.RWMutex
	handlers map[TimerKind]TimerHandler
	errs     []error
}

// NewTimers returns an empty table with its scheduler.
func NewTimers() *Timers {
	t := &Timers{handlers: map[TimerKind]TimerHandler{}}
	t.sched = clock.NewScheduler(t.dispatch)
	return t
}

// Scheduler returns the scheduler the clock fires; Tx.Arm and Tx.Disarm
// change it, Stateful.Rearm arms it directly after a load.
func (t *Timers) Scheduler() *clock.Scheduler { return t.sched }

// TimerKindError is a kind registered twice, with an empty name or with a
// nil handler.
type TimerKindError struct {
	Kind TimerKind
}

func (e *TimerKindError) Error() string {
	if e.Kind == "" {
		return "kernel: a timer handler was registered for the empty kind; name kinds <service>.<purpose>"
	}
	return fmt.Sprintf("kernel: timer kind %q has two handlers or a nil one; register each kind once with a handler", e.Kind)
}

// AttachError is a timer table handed to a second kernel.
type AttachError struct{}

func (*AttachError) Error() string {
	return "kernel: this Timers already serves another kernel; build one Timers, scheduler and clock per kernel"
}

// HandledKind is a timer kind registered with Timers.Handle, the only
// constructor outside this package, so a timer armed through Tx.Arm always
// has a handler to fire.
type HandledKind struct{ kind TimerKind }

// Kind returns the kind's name.
func (h HandledKind) Kind() TimerKind { return h.kind }

// Key returns the timer key of id under this kind.
func (h HandledKind) Key(id string) clock.TimerKey {
	return clock.TimerKey{Kind: h.kind, ID: id}
}

// Handle registers fn for kind and returns the handle Tx.Arm and Tx.Disarm
// take. Registering a kind twice, the empty kind or a nil handler is a bind
// error, which stops Run.
func (t *Timers) Handle(kind TimerKind, fn TimerHandler) HandledKind {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, dup := t.handlers[kind]; dup || kind == "" || fn == nil {
		t.errs = append(t.errs, &TimerKindError{Kind: kind})
		return HandledKind{kind: kind}
	}
	t.handlers[kind] = fn
	return HandledKind{kind: kind}
}

// Unhandled returns the armed timers whose kind has no handler, in firing
// order. Tx.Arm cannot arm one, but Stateful.Rearm arms the scheduler
// directly from loaded state; such a timer would only be logged when it
// fires, so the server fails boot when any is armed.
func (t *Timers) Unhandled() []clock.TimerKey {
	armed := t.sched.Snapshot()
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []clock.TimerKey
	for _, a := range armed {
		if _, ok := t.handlers[a.Key.Kind]; !ok {
			out = append(out, a.Key)
		}
	}
	return out
}

func (t *Timers) attach(k *Kernel) error {
	if !t.k.CompareAndSwap(nil, k) {
		return &AttachError{}
	}
	return nil
}

func (t *Timers) err() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return errors.Join(t.errs...)
}

// dispatch runs the handler of a due timer. The clock calls it with its step
// lock held and no other lock, one timer at a time.
func (t *Timers) dispatch(key clock.TimerKey, due clock.Instant) {
	k := t.k.Load()
	if k == nil {
		return
	}
	t.mu.RLock()
	fn := t.handlers[key.Kind]
	t.mu.RUnlock()
	ctx := context.Background()
	event, err := k.journal.Append(journal.Event{At: due, Detail: journal.TimerFired{Key: key}})
	if err != nil {
		k.log.LogAttrs(ctx, slog.LevelWarn, "journal refused a timer",
			slog.String("kind", string(key.Kind)), slog.String("timer", key.ID), slog.String("error", err.Error()))
	}
	if fn == nil {
		k.log.LogAttrs(ctx, slog.LevelError, "a timer fired with no handler for its kind",
			slog.String("kind", string(key.Kind)), slog.String("timer", key.ID))
		return
	}
	c := &Call{
		k:         k,
		id:        k.ids.RequestID(),
		principal: identity.Anonymous{},
		origin:    ref.Internal{Via: ref.Timer},
		label:     "timer " + string(key.Kind),
		event:     event,
		// The TimerFired event roots its own trace (journal.Append), so the
		// call's children and deliveries carry it.
		cause:    ref.Cause{Trace: event},
		admitted: due,
		elapsed:  k.clock.Stopwatch(),
	}
	defer func() {
		if v := recover(); v != nil {
			k.log.LogAttrs(ctx, slog.LevelError, "timer handler panicked",
				slog.String("kind", string(key.Kind)), slog.String("timer", key.ID),
				slog.String("panic", fmt.Sprint(v)), slog.String("stack", string(debug.Stack())))
		}
	}()
	fn(c, key, due)
}
