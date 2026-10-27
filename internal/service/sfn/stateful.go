package sfn

import (
	"maps"
	"slices"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// Name returns "sfn".
func (*Service) Name() ref.ServiceID { return sfnapi.Service().ID }

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

// Rearm arms the engine timers of every running execution, an invocation
// now of each Lambda task it has in flight (the StartExecution coverage
// note: the task is invoked again), and the removal of every closed one;
// timers are never saved (ADR 0009). It arms under the cell lock, where
// kernel.Write arms, so a concurrent step either commits first and its
// timers are read here, or commits after and its own arms replace these.
func (s *Service) Rearm(now clock.Instant) {
	sched := s.k.Timers().Scheduler()
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		store.Read(cell, func(st *State) struct{} {
			for _, key := range slices.Sorted(maps.Keys(st.Executions)) {
				rec := st.Executions[key]
				exec := executionRef{machine: machineRef{scope: scope, name: rec.Machine}, name: rec.Name}
				if _, done := rec.Engine.Outcome(); done {
					sched.Arm(s.expireKind.Key(exec.arn().String()), rec.Stopped.Add(retention))
					continue
				}
				for _, t := range rec.Engine.Timers() {
					sched.Arm(s.stepKind.Key(timerID(exec, t.Timer)), clock.FromWire(t.At))
				}
				for _, inv := range rec.Engine.InFlight() {
					sched.Arm(s.invokeKind.Key(taskKey(exec, inv.Task)), now)
				}
			}
			return struct{}{}
		})
	}
}

// Resources lists the state machines of scope with their execution counts.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []kernel.Resource {
		counts := map[string]int{}
		for key := range st.Executions {
			counts[st.Executions[key].Machine]++
		}
		out := make([]kernel.Resource, 0, len(st.Machines))
		for _, name := range slices.Sorted(maps.Keys(st.Machines)) {
			out = append(out, kernel.Resource{
				ARN:     machineRef{scope: scope, name: name}.arn(),
				Summary: strconv.Itoa(counts[name]) + " executions",
			})
		}
		return out
	})
}

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }
