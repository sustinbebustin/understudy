package events

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

// Name returns "eventbridge", the service's registry id; its ARNs use the
// "events" namespace.
func (*Service) Name() ref.ServiceID { return "eventbridge" }

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

// Rearm arms the schedule timer of every rule with a next fire; timers are
// never saved.
func (s *Service) Rearm(clock.Instant) {
	sched := s.k.Timers().Scheduler()
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		pending := store.Read(cell, func(st *State) []clock.Armed {
			var out []clock.Armed
			for _, name := range slices.Sorted(maps.Keys(st.Rules)) {
				if r := st.Rules[name]; !r.NextFire.IsZero() {
					out = append(out, clock.Armed{Key: s.schedule.Key(ruleRef{scope: scope, name: name}.arn().String()), Due: r.NextFire})
				}
			}
			return out
		})
		for _, a := range pending {
			sched.Arm(a.Key, a.Due)
		}
	}
}

// Resources lists the rules of scope with their state and target count.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Rules))
		for _, name := range slices.Sorted(maps.Keys(st.Rules)) {
			r := st.Rules[name]
			out = append(out, kernel.Resource{
				ARN:     ruleRef{scope: scope, name: name}.arn(),
				Summary: string(r.State) + ", " + strconv.Itoa(len(r.Targets)) + " targets",
			})
		}
		return out
	})
}

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }
