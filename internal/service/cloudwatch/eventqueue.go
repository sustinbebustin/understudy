package cloudwatch

import (
	"context"
	"sync"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// eventQueue holds the alarm events waiting to be sent, in the order of the
// writes that made them, so they reach the default bus, draw their seeded
// ids and enqueue their rule targets' deliveries in that order however
// goroutines are scheduled (ADR 0018, amendment 2026-10-13). One drainer at a
// time sends them: the handler that finds the queue idle, or tracked work
// started for a timer, which must not block the clock (ADR 0006).
type eventQueue struct {
	mu       sync.Mutex
	pending  []*queuedEvent
	draining bool
}

// queuedEvent is one event, the call it is sent on behalf of and the
// instant it is admitted at (zero: the call's admission). sent is closed
// once it has been sent or its send has failed.
type queuedEvent struct {
	parent *kernel.Call
	at     clock.Instant
	event  alarmEvent
	sent   chan struct{}
}

// pushEvent queues e once the change it reports has committed: inside the
// write that made a configuration change, under its cell's lock, or after
// the history write of a transition (finishTransition). The caller then
// flushes the queue.
func (s *Service) pushEvent(parent *kernel.Call, at clock.Instant, e alarmEvent) *queuedEvent {
	q := &queuedEvent{parent: parent, at: at, event: e, sent: make(chan struct{})}
	s.events.mu.Lock()
	defer s.events.mu.Unlock()
	s.events.pending = append(s.events.pending, q)
	return q
}

// flushEvents sends the queue up to and including last, then returns, so a
// handler answers after its own events are on the bus as before. When
// another drainer holds the queue it waits for last instead; whatever
// follows last is left to tracked work. A nil last does nothing.
func (s *Service) flushEvents(ctx context.Context, last *queuedEvent) {
	if last == nil {
		return
	}
	if !s.claimQueue() {
		select {
		case <-last.sent:
		case <-ctx.Done():
		}
		return
	}
	s.drain(ctx, last)
}

// flushEventsLater has tracked work send the queue, for a timer handler,
// which must not wait on EventBridge.
func (s *Service) flushEventsLater() {
	if !s.claimQueue() {
		return
	}
	s.k.Go(kernel.DeliveryAttempt, func(ctx context.Context) { s.drain(ctx, nil) })
}

// claimQueue makes the caller the drainer, or reports that another is.
func (s *Service) claimQueue() bool {
	s.events.mu.Lock()
	defer s.events.mu.Unlock()
	if s.events.draining {
		return false
	}
	s.events.draining = true
	return true
}

// drain sends queued events in order until the queue is empty or, with
// stop non-nil, until stop has been sent; what remains then goes on to
// tracked work, which keeps the drainer's claim.
func (s *Service) drain(ctx context.Context, stop *queuedEvent) {
	for {
		s.events.mu.Lock()
		if len(s.events.pending) == 0 {
			s.events.draining = false
			s.events.mu.Unlock()
			return
		}
		q := s.events.pending[0]
		s.events.pending = s.events.pending[1:]
		s.events.mu.Unlock()
		s.sendEvent(ctx, q.parent, q.at, &q.event)
		close(q.sent)
		if q != stop {
			continue
		}
		s.events.mu.Lock()
		rest := len(s.events.pending)
		if rest == 0 {
			s.events.draining = false
		}
		s.events.mu.Unlock()
		if rest > 0 {
			s.k.Go(kernel.DeliveryAttempt, func(ctx context.Context) { s.drain(ctx, nil) }) //nolint:contextcheck // the rest is tracked work that outlives the request which drained up to its own event
		}
		return
	}
}
