package dynamodb

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshots.
const stateVersion = 1

// State is the DynamoDB shim state of one account and region; this build
// keeps none.
type State struct{}

// Service is Amazon DynamoDB: one shim state cell per account and region.
// This build binds none of its operations, so each answers 501 in
// DynamoDB's own error format and coverage lists it as unbound.
type Service struct {
	cells *store.Partitioned[aws.Scope, State]
}

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// New returns the service. It binds no operation.
func New(d kernel.ServiceDeps, _ Options) *Service {
	codec := jsonx.NewCodec()
	return &Service{cells: store.NewPartitioned(d.Kernel.Gate(), store.Codec{Marshal: codec.Marshal, Unmarshal: codec.Unmarshal},
		func(aws.Scope) State { return State{} })}
}

// Name returns "dynamodb".
func (*Service) Name() ref.ServiceID { return "dynamodb" }

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

// Rearm does nothing: the state arms no timers.
func (*Service) Rearm(clock.Instant) {}

// Resources lists nothing: the state holds no resources.
func (*Service) Resources(aws.Scope) []kernel.Resource { return nil }

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }

// Switches lists no behaviors: the service implements none yet.
func (*Service) Switches() []fidelity.Entry { return nil }
