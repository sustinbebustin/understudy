package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/sustinbebustin/understudy/internal/kernel"
)

// sequencer runs delivery attempts one at a time, in the order onAttempt
// hands them over, which is the clock's firing order. The target call of
// each attempt then draws its request-phase fault decision, its request id
// and the ids the target mints in that order too, rather than in the order
// concurrent goroutines happen to run (ADR 0018).
//
// At most one drain goroutine runs at a time, as tracked work that Settle
// counts until the queue is empty.
type sequencer struct {
	mu       sync.Mutex
	queue    []*attempt
	draining bool
}

// push queues a and reports whether the caller must start a drain: none is
// running.
func (s *sequencer) push(a *attempt) (start bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, a)
	start = !s.draining
	s.draining = true
	return start
}

// pop takes the next attempt; false when the queue is empty, which also
// ends the running drain.
func (s *sequencer) pop() (*attempt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		s.draining = false
		return nil, false
	}
	a := s.queue[0]
	s.queue[0] = nil
	s.queue = s.queue[1:]
	return a, true
}

// sequence runs a after every attempt handed over before it.
func (e *engine) sequence(a *attempt) {
	if e.ready.push(a) {
		e.k.Go(kernel.DeliveryAttempt, e.drain)
	}
}

// drain runs queued attempts until the queue is empty. Once the kernel
// stopped, ctx is done and each remaining attempt records no outcome and
// releases its claim, leaving its delivery pending for the next Rearm.
func (e *engine) drain(ctx context.Context) {
	for {
		a, ok := e.ready.pop()
		if !ok {
			return
		}
		e.runSequenced(ctx, a)
	}
}

// runSequenced runs one attempt of the drain. A panic ends that attempt
// alone, logged with its stack as Kernel.Go would log it, so the attempts
// queued behind it still run.
func (e *engine) runSequenced(ctx context.Context, a *attempt) {
	defer func() {
		if v := recover(); v != nil {
			e.k.Log().LogAttrs(ctx, slog.LevelError, "outbox delivery attempt panicked; the delivery stays pending for the next Rearm",
				slog.String("delivery", string(a.entry.ID)), slog.String("panic", fmt.Sprint(v)), slog.String("stack", string(debug.Stack())))
		}
	}()
	a.do(ctx)
}
