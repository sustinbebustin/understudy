package sqs

import (
	"maps"
	"slices"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// Name returns "sqs".
func (s *Service) Name() ref.ServiceID { return "sqs" }

// SchemaVersion returns the version of State.
func (s *Service) SchemaVersion() int { return stateVersion }

// Export serializes every non-empty scope to one section.
func (s *Service) Export() ([]store.Section, error) {
	return s.cells.Export(store.ScopeKeys(), stateVersion)
}

// Import loads sections written by Export of this schema version.
func (s *Service) Import(ss []store.Section) error {
	return s.cells.Import(store.ScopeKeys(), stateVersion, ss)
}

// Reset drops the scopes rs covers.
func (s *Service) Reset(rs kernel.ResetScope) {
	s.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
}

// Rearm arms the timers the state implies at now (timersOf), since timers
// are never saved. It arms under the cell lock, where kernel.Write arms,
// so a concurrent write either commits first and its timers are read
// here, or commits after and its own arms replace these. It then wakes
// the long polls parked on every queue that holds a visible message,
// since no send or timer will wake them for messages the load brought.
func (s *Service) Rearm(now clock.Instant) {
	sched := s.k.Timers().Scheduler()
	var ready []queueRef
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		ready = append(ready, store.Read(cell, func(st *State) []queueRef {
			for _, a := range s.timersOf(scope, st, now) {
				sched.Arm(a.Key, a.Due)
			}
			return readyQueues(scope, st, now)
		})...)
	}
	for _, q := range ready {
		s.k.Waiters().Wake(q.waitKey())
	}
}

// readyQueues lists the queues of scope holding a message visible at now.
func readyQueues(scope aws.Scope, st *State, now clock.Instant) []queueRef {
	var out []queueRef
	for _, name := range slices.Sorted(maps.Keys(st.Queues)) {
		queue := st.Queues[name]
		for i := range queue.Messages {
			if queue.Messages[i].visible(now) {
				out = append(out, queueRef{scope: scope, name: name})
				break
			}
		}
	}
	return out
}

// Scopes lists the scopes the service holds a cell for, whether or not
// any queue lives there.
func (s *Service) Scopes() []aws.Scope {
	return s.cells.Keys(store.ScopeKeys())
}

// Resources lists the queues of scope, by name, with their message counts.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Queues))
		for _, name := range slices.Sorted(maps.Keys(st.Queues)) {
			out = append(out, kernel.Resource{
				ARN:     queueRef{scope: scope, name: name}.arn(),
				Summary: strconv.Itoa(len(st.Queues[name].Messages)) + " messages",
			})
		}
		return out
	})
}

// timersOf lists the timers the state of scope implies at now: the
// visibility timer of every message hidden and not pinned, the retention
// timer of every message and the timer of every running move task.
func (s *Service) timersOf(scope aws.Scope, st *State, now clock.Instant) []clock.Armed {
	var out []clock.Armed
	for _, name := range slices.Sorted(maps.Keys(st.Queues)) {
		queue := st.Queues[name]
		q := queueRef{scope: scope, name: name}
		for i := range queue.Messages {
			m := &queue.Messages[i]
			id := timerID(q, string(m.ID))
			if !m.pinned() && !m.visible(now) {
				out = append(out, clock.Armed{Key: s.visibility.Key(id), Due: m.VisibleAt})
			}
			out = append(out, clock.Armed{Key: s.retention.Key(id), Due: m.Sent.Add(seconds(queue.Attributes.MessageRetentionPeriod))})
		}
	}
	for i := range st.Tasks {
		if run, ok := st.Tasks[i].State.(TaskRunning); ok {
			out = append(out, clock.Armed{Key: s.moveTask.Key(st.Tasks[i].timerID()), Due: run.Next})
		}
	}
	return out
}
