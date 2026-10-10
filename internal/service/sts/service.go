package sts

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

// State is the STS state of one account; this build keeps none.
type State struct{}

// Service is AWS STS: one state cell per account. This build binds none of
// its operations, so each answers 501 in STS's own error format and
// coverage lists it as unbound.
type Service struct {
	cells *store.Partitioned[aws.AccountID, State]
}

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// New returns the service. It binds no operation.
func New(d kernel.ServiceDeps, _ Options) *Service {
	codec := jsonx.NewCodec()
	return &Service{cells: store.NewPartitioned(d.Kernel.Gate(), store.Codec{Marshal: codec.Marshal, Unmarshal: codec.Unmarshal},
		func(aws.AccountID) State { return State{} })}
}

// Name returns "sts".
func (*Service) Name() ref.ServiceID { return "sts" }

// SchemaVersion returns the version of State.
func (*Service) SchemaVersion() int { return stateVersion }

// Export serializes every non-empty account to one section.
func (s *Service) Export() ([]store.Section, error) {
	return s.cells.Export(store.AccountKeys(), stateVersion)
}

// Import loads sections written by Export of this schema version.
func (s *Service) Import(ss []store.Section) error {
	return s.cells.Import(store.AccountKeys(), stateVersion, ss)
}

// Reset drops the accounts rs covers; STS state has no regions.
func (s *Service) Reset(rs kernel.ResetScope) {
	s.cells.Reset(func(k aws.AccountID) bool { return !rs.Covers(aws.Scope{Partition: aws.PartitionAWS(), Account: k}) })
}

// Rearm does nothing: the state arms no timers.
func (*Service) Rearm(clock.Instant) {}

// Resources lists nothing: the state holds no resources.
func (*Service) Resources(aws.Scope) []kernel.Resource { return nil }

// Scopes returns the accounts that hold a state cell, each with a zero
// region; STS has no regional state.
func (s *Service) Scopes() []aws.Scope {
	accounts := s.cells.Keys(store.AccountKeys())
	out := make([]aws.Scope, len(accounts))
	for i, a := range accounts {
		out[i] = aws.Scope{Partition: aws.PartitionAWS(), Account: a}
	}
	return out
}

// Switches lists no behaviors: the service implements none yet.
func (*Service) Switches() []fidelity.Entry { return nil }
