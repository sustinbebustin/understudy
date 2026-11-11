package ssm

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// Name returns "ssm".
func (*Service) Name() ref.ServiceID { return "ssm" }

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

// Rearm does nothing: Parameter Store arms no timers.
func (*Service) Rearm(clock.Instant) {}

// Resources lists the parameters of scope in byte order of ARN, each
// summarized as "<Type>, version <n>" of its latest
// version.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	return store.View(s.cells, scope, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Parameters))
		for canonical, p := range st.Parameters {
			v := p.latest()
			out = append(out, kernel.Resource{
				ARN:     parameterARN(scope, canonical),
				Summary: fmt.Sprintf("%s, version %d", v.Type, v.Number),
			})
		}
		slices.SortFunc(out, func(a, b kernel.Resource) int { return strings.Compare(a.ARN.String(), b.ARN.String()) })
		return out
	})
}

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }
