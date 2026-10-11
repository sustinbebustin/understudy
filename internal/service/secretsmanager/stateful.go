package secretsmanager

import (
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// Name returns "secretsmanager".
func (*Service) Name() ref.ServiceID { return "secretsmanager" }

// SchemaVersion returns the version of State.
func (*Service) SchemaVersion() int { return stateVersion }

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

// Rearm arms the deletion timer of every secret scheduled for deletion;
// timers are never saved.
func (s *Service) Rearm(clock.Instant) {
	sched := s.k.Timers().Scheduler()
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		due := store.Read(cell, func(st *State) []clock.Armed {
			var out []clock.Armed
			for _, name := range slices.Sorted(maps.Keys(st.Secrets)) {
				sec := st.Secrets[name]
				if l, ok := sec.Lifecycle.(scheduled); ok {
					out = append(out, clock.Armed{Key: s.deletion.Key(sec.ARN), Due: l.DeletionDate})
				}
			}
			return out
		})
		for _, a := range due {
			sched.Arm(a.Key, a.Due)
		}
	}
}

// Resources lists the secrets of scope, those scheduled for deletion
// included.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	return readState(s, scope, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Secrets))
		for _, name := range slices.Sorted(maps.Keys(st.Secrets)) {
			sec := st.Secrets[name]
			arn, err := aws.ParseARN(sec.ARN)
			if err != nil {
				continue
			}
			summary := "active"
			if _, ok := sec.Lifecycle.(scheduled); ok {
				summary = "scheduled for deletion"
			}
			out = append(out, kernel.Resource{ARN: arn, Summary: summary})
		}
		return out
	})
}

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }

// readState runs fn on the state of scope without creating its cell; a
// scope with no cell reads as empty.
func readState[R any](s *Service, scope aws.Scope, fn func(st *State) R) R {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		st := newState(scope)
		return fn(&st)
	}
	return store.Read(cell, fn)
}
