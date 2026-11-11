// Package serviceevent sends the events an AWS service puts on
// EventBridge's default bus about its own resources, such as a CloudWatch
// alarm's state change or a Parameter Store change, through EventBridge's
// internal PutServiceEventInternal operation after the write that made the
// change commits (ADR 0015).
//
// A Queue sends them in the order of the writes that made them, so they
// reach the bus, draw their seeded ids and enqueue their rule targets'
// deliveries in that order however goroutines are scheduled (ADR 0018). One
// drainer at a time sends them: the handler that finds the queue idle, or
// tracked work started for a timer, which must not block the clock (ADR
// 0006).
package serviceevent

import (
	"context"
	"sync"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Event is one service event and whom it is put as.
type Event struct {
	// Scope is the account and Region whose default bus receives it.
	Scope aws.Scope
	// Principal is the service principal the event is put as, such as
	// "cloudwatch.amazonaws.com".
	Principal string
	// Via is the mechanism the internal call is journaled under.
	Via        ref.Via
	Source     string
	DetailType string
	// Detail is the event's detail, a JSON object.
	Detail    string
	Resources []string
}

// LostFunc reports an event that did not reach the default bus and is not
// sent again; why says what failed. Each service words the warning for
// its own resources.
type LostFunc func(ctx context.Context, e *Event, why string)

// Queue holds a service's events waiting to be sent, in commit order.
type Queue struct {
	k    *kernel.Kernel
	lost LostFunc

	mu       sync.Mutex
	pending  []*Queued
	draining bool
}

// Queued is one event, the call it is sent on behalf of and the instant
// it is admitted at (zero: the call's admission). sent is closed once it
// has been sent or its send has failed.
type Queued struct {
	parent *kernel.Call
	at     clock.Instant
	event  Event
	sent   chan struct{}
}

// New is an empty queue sending through k, which reports a lost event to
// lost.
func New(k *kernel.Kernel, lost LostFunc) *Queue {
	return &Queue{k: k, lost: lost}
}

// Push queues e once the change it reports has committed: inside the write
// that made it, under its cell's lock, so events queue in commit order. The
// caller then flushes the queue.
func (q *Queue) Push(parent *kernel.Call, at clock.Instant, e Event) *Queued {
	item := &Queued{parent: parent, at: at, event: e, sent: make(chan struct{})}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = append(q.pending, item)
	return item
}

// Flush sends the queue up to and including last, then returns, so a
// handler answers after its own events are on the bus. When another
// drainer holds the queue it waits for last instead; whatever follows last
// is left to tracked work. A nil last does nothing.
func (q *Queue) Flush(ctx context.Context, last *Queued) {
	if last == nil {
		return
	}
	if !q.claim() {
		select {
		case <-last.sent:
		case <-ctx.Done():
		}
		return
	}
	q.drain(ctx, last)
}

// FlushLater has tracked work send the queue, for a timer handler, which
// must not wait on EventBridge.
func (q *Queue) FlushLater() {
	if !q.claim() {
		return
	}
	q.k.Go(kernel.DeliveryAttempt, func(ctx context.Context) { q.drain(ctx, nil) })
}

// claim makes the caller the drainer, or reports that another is.
func (q *Queue) claim() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.draining {
		return false
	}
	q.draining = true
	return true
}

// drain sends queued events in order until the queue is empty or, with
// stop non-nil, until stop has been sent; what remains then goes on to
// tracked work, which keeps the drainer's claim.
func (q *Queue) drain(ctx context.Context, stop *Queued) {
	for {
		q.mu.Lock()
		if len(q.pending) == 0 {
			q.draining = false
			q.mu.Unlock()
			return
		}
		item := q.pending[0]
		q.pending = q.pending[1:]
		q.mu.Unlock()
		q.send(ctx, item)
		close(item.sent)
		if item != stop {
			continue
		}
		q.mu.Lock()
		rest := len(q.pending)
		if rest == 0 {
			q.draining = false
		}
		q.mu.Unlock()
		if rest > 0 {
			q.k.Go(kernel.DeliveryAttempt, func(ctx context.Context) { q.drain(ctx, nil) }) //nolint:contextcheck // the rest is tracked work that outlives the request which drained up to its own event
		}
		return
	}
}

// send puts item's event on the default bus of its scope as its service
// principal, on behalf of its call. It runs after the write that made the
// change, which holds no lock then. A kernel without EventBridge, or a
// failed send, loses the event; it is not retried.
func (q *Queue) send(ctx context.Context, item *Queued) {
	e := &item.event
	if _, bound := q.k.Bindings().Status(eventbridgeapi.OpPutServiceEventInternal.Descriptor()).(kernel.Bound); !bound {
		q.lost(ctx, e, "this kernel has no EventBridge PutServiceEventInternal")
		return
	}
	t := kernel.Target{Scope: e.Scope, As: kernel.AsPlatform{Name: e.Principal}, Via: e.Via, At: item.at}
	in := &eventbridgeapi.PutServiceEventInternalInput{Source: e.Source, DetailType: e.DetailType, Detail: e.Detail, Resources: e.Resources}
	if _, err := kernel.Invoke(ctx, q.k, item.parent, eventbridgeapi.OpPutServiceEventInternal, t, in); err != nil {
		q.lost(ctx, e, err.Message)
	}
}
