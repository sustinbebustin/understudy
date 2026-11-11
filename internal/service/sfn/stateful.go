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

// Import loads sections written by Export of this schema version. Every
// running invocation is forgotten, so Rearm invokes each task in flight in
// the loaded state again (the StartExecution coverage note).
func (s *Service) Import(ss []store.Section) error {
	s.running.drop(func(aws.Scope) bool { return true })
	return s.cells.Import(store.ScopeKeys(), stateVersion, ss)
}

// Reset drops the scopes rs covers, and the running invocations of their
// executions.
func (s *Service) Reset(rs kernel.ResetScope) {
	s.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
	s.running.drop(rs.Covers)
}

// Rearm arms the engine timers of every running execution, an invocation
// now of each Lambda task it has in flight (the StartExecution coverage
// note: the task is invoked again), and the removal of every closed one;
// timers are never saved (ADR 0009). kernel.RearmEach has the lock
// argument.
func (s *Service) Rearm(now clock.Instant) {
	kernel.RearmEach(s.k.Timers().Scheduler(), s.cells, func(scope aws.Scope, st *State) []clock.Armed {
		var out []clock.Armed
		for _, key := range slices.Sorted(maps.Keys(st.Executions)) {
			rec := st.Executions[key]
			exec := executionRef{machine: machineRef{scope: scope, name: rec.Machine}, name: rec.Name}
			if _, done := rec.Engine.Outcome(); done {
				out = append(out, clock.Armed{Key: s.expireKind.Key(exec.arn().String()), Due: rec.Stopped.Add(retention)})
				continue
			}
			for _, t := range rec.Engine.Timers() {
				out = append(out, clock.Armed{Key: s.stepKind.Key(timerID(exec, t.Timer)), Due: clock.FromWire(t.At)})
			}
			for _, inv := range rec.Engine.InFlight() {
				out = append(out, clock.Armed{Key: s.invokeKind.Key(taskKey(exec, inv.Task)), Due: now})
			}
		}
		return out
	})
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
