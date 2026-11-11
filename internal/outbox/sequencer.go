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
//
// A sequencer that releases lets the attempts behind an attempt run as
// soon as a request-phase latency rule holds one of its calls: the kernel
// signals the hold (kernel.Target.OnLatency) after the call drew its fault
// hits and before it waits, the waiting attempt finishes alone on its
// goroutine, and a new drain takes over the queue. The call's fault draws
// keep the sequencer's order; the ids its target draws after the wait fall
// in wall-time order, as after a Delivery-phase delay (ADR 0018, amendment
// 2026-11-11). The engine's sequencer releases; a throttled subscription's
// does not, so it keeps journaling its attempts in the order they start,
// as it does through a Delivery-phase delay.
type sequencer struct {
	releases bool

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

// handOff ends the running drain's hold on s while its attempt carries on,
// and reports whether the caller must start a drain for the attempts
// queued behind it. With none queued the drain is over, and the next push
// starts one.
func (s *sequencer) handOff() (start bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		s.draining = false
		return false
	}
	return true
}

// idle reports whether s has no attempt queued and no drain running.
func (s *sequencer) idle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.draining && len(s.queue) == 0
}

// sequence runs a on s after every attempt handed to s before it: the
// engine's sequencer, or a throttled subscription's (pacer).
func (e *engine) sequence(s *sequencer, a *attempt) {
	if s.push(a) {
		e.startDrain(s)
	}
}

// startDrain runs s's queue on a new drain goroutine.
func (e *engine) startDrain(s *sequencer) {
	e.k.Go(kernel.DeliveryAttempt, func(ctx context.Context) { e.drain(ctx, s) })
}

// drain runs the attempts queued on s until its queue is empty, or until
// an attempt hands the queue to a new drain. Once the kernel stopped, ctx
// is done and each remaining attempt neither advises nor calls its target:
// it records no outcome and releases its claim, leaving its delivery
// pending for the next Rearm.
func (e *engine) drain(ctx context.Context, s *sequencer) {
	for {
		a, ok := s.pop()
		if !ok {
			return
		}
		if e.runSequenced(ctx, s, a) {
			return
		}
	}
}

// runSequenced runs one attempt of the drain and reports whether the
// attempt handed s's queue to a new drain, which a request-phase latency
// on one of its calls does when s releases. A panic ends that attempt
// alone, logged with its stack as Kernel.Go would log it, so the attempts
// queued behind it still run.
func (e *engine) runSequenced(ctx context.Context, s *sequencer, a *attempt) (handedOff bool) {
	defer func() {
		if v := recover(); v != nil {
			e.k.Log().LogAttrs(ctx, slog.LevelError, "outbox delivery attempt panicked; the delivery stays pending for the next Rearm",
				slog.String("delivery", string(a.entry.ID)), slog.String("panic", fmt.Sprint(v)), slog.String("stack", string(debug.Stack())))
		}
	}()
	if s.releases {
		// The kernel calls it on this goroutine, inside the attempt's call.
		a.onLatency = func() { //nolint:contextcheck // the new drain is kernel work with the kernel's context, not this attempt's
			if handedOff {
				return
			}
			handedOff = true
			if s.handOff() {
				e.startDrain(s)
			}
		}
	}
	a.do(ctx)
	return handedOff
}
