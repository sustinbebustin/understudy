package cloudwatch

import (
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// Name returns "cloudwatch".
func (*Service) Name() ref.ServiceID { return "cloudwatch" }

// SchemaVersion returns the version of State.
func (*Service) SchemaVersion() int { return stateVersion }

// Export serializes every non-empty scope to one section, and its alarm
// history to another under history/.
func (s *Service) Export() ([]store.Section, error) {
	state, err := s.cells.Export(store.ScopeKeys(), stateVersion)
	if err != nil {
		return nil, err
	}
	history, err := s.history.Export(historyKeys(), stateVersion)
	if err != nil {
		return nil, err
	}
	return append(state, history...), nil
}

// Import loads sections written by Export of this schema version. A
// snapshot written before alarm history existed has no history sections
// and loads with none.
func (s *Service) Import(ss []store.Section) error {
	state, history := splitHistory(ss)
	if err := s.cells.Import(store.ScopeKeys(), stateVersion, state); err != nil {
		return err
	}
	return s.history.Import(historyKeys(), stateVersion, history)
}

// Reset drops the scopes rs covers, with their alarm history. A stale
// evaluation timer of a dropped alarm finds nothing and dies.
func (s *Service) Reset(rs kernel.ResetScope) {
	keep := func(k aws.Scope) bool { return !rs.Covers(k) }
	s.cells.Reset(keep)
	s.history.Reset(keep)
}

// Rearm arms the next evaluation after now of every evaluable alarm;
// timers are never saved (cloudwatch-alarms.md 12.3). kernel.RearmEach has
// the lock argument.
func (s *Service) Rearm(now clock.Instant) {
	kernel.RearmEach(s.k.Timers().Scheduler(), s.cells, func(scope aws.Scope, st *State) []clock.Armed {
		var out []clock.Armed
		for _, name := range slices.Sorted(maps.Keys(st.Alarms)) {
			a := st.Alarms[name]
			if cfg, err := alarm.ConfigOf(&a.Config); err == nil {
				out = append(out, clock.Armed{Key: s.evaluation.Key(alarmRef{scope: scope, name: name}.arn().String()), Due: clock.FromWire(cfg.Next(now.Wire()))})
			}
		}
		return out
	})
}

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }

// Resources lists the alarms of scope with their states.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Alarms))
		for _, name := range slices.Sorted(maps.Keys(st.Alarms)) {
			out = append(out, kernel.Resource{
				ARN:     alarmRef{scope: scope, name: name}.arn(),
				Summary: string(st.Alarms[name].State.Value),
			})
		}
		return out
	})
}
