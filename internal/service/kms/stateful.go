package kms

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

// Name returns "kms".
func (*Service) Name() ref.ServiceID { return "kms" }

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

// Rearm arms the deletion timer of every key pending deletion; timers are
// never saved. kernel.RearmEach has the lock argument.
func (s *Service) Rearm(clock.Instant) {
	kernel.RearmEach(s.k.Timers().Scheduler(), s.cells, s.timersOf)
}

// timersOf lists the deletion timer of every key of st, the state of scope,
// that is pending deletion.
func (s *Service) timersOf(scope aws.Scope, st *State) []clock.Armed {
	var out []clock.Armed
	for _, id := range slices.Sorted(maps.Keys(st.Keys)) {
		if p, ok := st.Keys[id].Lifecycle.(pendingDeletion); ok {
			out = append(out, clock.Armed{Key: s.deletion.Key(keyARN(scope, id).String()), Due: p.DeletionDate})
		}
	}
	return out
}

// Resources lists the keys of scope with their key state.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Keys))
		for _, id := range slices.Sorted(maps.Keys(st.Keys)) {
			out = append(out, kernel.Resource{ARN: keyARN(scope, id), Summary: string(st.Keys[id].Lifecycle.keyState())})
		}
		return out
	})
}

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }
