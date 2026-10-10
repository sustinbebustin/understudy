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

// Rearm arms the visibility timer of every message that is not yet
// receivable at now; timers are never saved.
func (s *Service) Rearm(now clock.Instant) {
	sched := s.k.Timers().Scheduler()
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		pending := store.Read(cell, func(st *State) []clock.Armed {
			var out []clock.Armed
			for _, name := range slices.Sorted(maps.Keys(st.Queues)) {
				queue := st.Queues[name]
				q := queueRef{scope: scope, name: name}
				for i := range queue.Messages {
					if m := &queue.Messages[i]; !m.visible(now) {
						out = append(out, clock.Armed{Key: s.visibility.Key(visibilityID(q, m.ID)), Due: m.VisibleAt})
					}
				}
			}
			return out
		})
		for _, a := range pending {
			sched.Arm(a.Key, a.Due)
		}
	}
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
