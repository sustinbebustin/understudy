package logs

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

// Name returns "logs".
func (*Service) Name() ref.ServiceID { return "logs" }

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

// Rearm arms the retention timer of every group whose events expire;
// timers are never saved. It arms under the cell lock, where kernel.Write
// arms, so a concurrent write either commits first and its expiry is read
// here, or commits after and its own arm replaces this one.
func (s *Service) Rearm(clock.Instant) {
	sched := s.k.Timers().Scheduler()
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		store.Read(cell, func(st *State) struct{} {
			for _, name := range slices.Sorted(maps.Keys(st.Groups)) {
				g := st.Groups[name]
				if due, ok := nextExpiry(&g); ok {
					sched.Arm(s.retention.Key(groupRef{scope: scope, name: name}.arn().String()), due)
				}
			}
			return struct{}{}
		})
	}
}

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }

// Resources lists the log groups of scope with their stream counts.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Groups))
		for _, name := range slices.Sorted(maps.Keys(st.Groups)) {
			out = append(out, kernel.Resource{
				ARN:     groupRef{scope: scope, name: name}.arn(),
				Summary: strconv.Itoa(len(st.Groups[name].Streams)) + " streams",
			})
		}
		return out
	})
}
