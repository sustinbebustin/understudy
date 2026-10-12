package sqs

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// timerID is the id of a timer about one thing in queue q, a message or a
// move task: "<queue-arn>/<id>". A queue name holds no slash, so the last
// one splits it.
func timerID(q queueRef, id string) string {
	return q.arn().String() + "/" + id
}

// parseTimerID reads a timerID back: the scheduler keys timers by string,
// so a timer handler parses its key once, here.
func parseTimerID(key string) (queueRef, string, bool) {
	before, after, ok0 := strings.CutLast(key, "/")
	if !ok0 {
		return queueRef{}, "", false
	}
	q, ok := parseQueueARN(before)
	return q, after, ok
}

// armVisibility makes the visibility timer of m agree with it at now: armed
// for when a hidden message becomes receivable, disarmed for a pinned one,
// which only HoldVisibility releases, and for a receivable one, whose
// queue's receivers are woken.
func (s *Service) armVisibility(tx *kernel.Tx, q queueRef, m *Message, now clock.Instant) {
	id := timerID(q, string(m.ID))
	switch {
	case m.pinned():
		tx.Disarm(s.visibility, id)
	case m.visible(now):
		tx.Disarm(s.visibility, id)
		tx.Wake(q.waitKey())
	default:
		tx.Arm(s.visibility, id, m.VisibleAt)
	}
}

// armRetention arms the retention timer of m for the end of queue's
// MessageRetentionPeriod, counted from the message's enqueue (sqs.md 7.3).
func (s *Service) armRetention(tx *kernel.Tx, q queueRef, queue *Queue, m *Message) {
	tx.Arm(s.retention, timerID(q, string(m.ID)), m.Sent.Add(seconds(queue.Attributes.MessageRetentionPeriod)))
}

// disarmMessage disarms both timers of a message leaving the queue.
func (s *Service) disarmMessage(tx *kernel.Tx, q queueRef, id ids.MessageID) {
	tx.Disarm(s.visibility, timerID(q, string(id)))
	tx.Disarm(s.retention, timerID(q, string(id)))
}

// removeMessage takes message i out of queue, as removeMessages does.
func (s *Service) removeMessage(tx *kernel.Tx, q queueRef, queue *Queue, i int) {
	s.removeMessages(tx, q, queue, []int{i})
}

// removeMessages takes the messages at the ascending indices at out of
// queue in one pass, with their timers, and wakes the queue's receivers,
// as a FIFO group a message held may open.
func (s *Service) removeMessages(tx *kernel.Tx, q queueRef, queue *Queue, at []int) {
	if len(at) == 0 {
		return
	}
	for _, i := range at {
		s.disarmMessage(tx, q, queue.Messages[i].ID)
	}
	queue.remove(at)
	tx.Wake(q.waitKey())
}

// onVisibility wakes the queue's receivers when the timer's message is
// still there and receivable; a deleted message's timer does nothing.
func (s *Service) onVisibility(_ *kernel.Call, key clock.TimerKey, due clock.Instant) {
	q, id, ok := parseTimerID(key.ID)
	if !ok {
		return
	}
	visible, err := readQueue(s, q, func(_ *State, queue *Queue) bool {
		i := queue.find(ids.MessageID(id))
		return i >= 0 && queue.Messages[i].visible(due)
	})
	if err == nil && visible {
		s.k.Waiters().Wake(q.waitKey())
	}
}

// onRetention deletes the timer's message once it is past the queue's
// retention, in flight or not (sqs.md 7.3). A message the queue kept
// longer, after a SetQueueAttributes raised the period, has a later timer.
func (s *Service) onRetention(c *kernel.Call, key clock.TimerKey, _ clock.Instant) {
	q, id, ok := parseTimerID(key.ID)
	if !ok {
		return
	}
	s.timerWrite(c, q, "message retention", func(_ *State, queue *Queue, now clock.Instant, tx *kernel.Tx) bool {
		i := queue.find(ids.MessageID(id))
		if i < 0 || !queue.Messages[i].expired(seconds(queue.Attributes.MessageRetentionPeriod), now) {
			return false
		}
		s.removeMessage(tx, q, queue, i)
		return true
	})
}

// timerWrite runs a timer handler's change to a working copy of queue q,
// which reports whether it changed anything, and stores the copy back when
// it did, as writeQueue does. A queue deleted since the timer was armed
// has nothing left to change; any other failure is logged, as a timer has
// no caller to answer.
func (s *Service) timerWrite(c *kernel.Call, q queueRef, what string, fn func(st *State, queue *Queue, now clock.Instant, tx *kernel.Tx) bool) {
	if _, ok := s.cells.Lookup(q.scope); !ok {
		return
	}
	_, err := kernel.Write(c, s.cells, q.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		stored, ok := st.Queues[q.name]
		if !ok {
			tx.Unchanged()
			return struct{}{}, nil
		}
		queue := stored.clone()
		if !fn(st, &queue, now, tx) {
			tx.Unchanged()
			return struct{}{}, nil
		}
		st.Queues[q.name] = queue
		return struct{}{}, nil
	})
	if err != nil {
		s.k.Log().LogAttrs(context.Background(), slog.LevelError, "an sqs timer failed; its work runs again when a snapshot load re-arms the timer",
			slog.String("work", what), slog.String("queue", q.arn().String()), slog.String("error", err.Message))
	}
}

// find returns the index of the message id, or -1.
func (q *Queue) find(id ids.MessageID) int {
	return slices.IndexFunc(q.Messages, func(m Message) bool { return m.ID == id })
}

// byReceipt returns the index of the message whose latest receipt handle is
// handle, or -1.
func (q *Queue) byReceipt(handle string) int {
	return slices.IndexFunc(q.Messages, func(m Message) bool { return m.holds(handle) })
}
