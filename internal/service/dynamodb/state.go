package dynamodb

import (
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshots, and of the
// item sections of ExportLate.
const stateVersion = 3

// stateCodec is the snapshot codec of State, with the Billing sum in the
// tagged-union form. A table jsonx refuses is a bug in this file; the codec
// then fails every Export and Import with that error.
func stateCodec() store.Codec {
	u, err := jsonx.NewUnion("dynamodb.Billing",
		jsonx.Variant[Billing, Provisioned]("provisioned"),
		jsonx.Variant[Billing, OnDemand]("on-demand"),
	)
	if err != nil {
		return store.Codec{
			Marshal:   func(any) ([]byte, error) { return nil, err },
			Unmarshal: func([]byte, any) error { return err },
		}
	}
	c := jsonx.NewCodec(u)
	return store.Codec{Marshal: c.Marshal, Unmarshal: c.Unmarshal}
}

// State is the shim's view of one account and region: the tables Local
// holds there, which Local itself cannot tell apart by letter case, and
// what Local drops or answers wrongly about each (Table).
type State struct {
	// Tables is keyed by table name as created.
	Tables map[string]Table `json:"tables"`
}

func newState(aws.Scope) State { return State{Tables: map[string]Table{}} }

// caseTwins returns the tables whose names equal name in every letter but
// case, which Local would take for the same table (dynamodb-local.md 4.1).
// The name itself is no twin: Local answers its own ResourceInUseException.
func (s *State) caseTwins(name string) []string {
	var twins []string
	for have := range s.Tables {
		if have != name && strings.EqualFold(have, name) {
			twins = append(twins, have)
		}
	}
	slices.Sort(twins)
	return twins
}

// onlyTwinOf reports whether the scope lacks name but holds a case twin of
// it, which Local would act on in its place.
func (s *State) onlyTwinOf(name string) bool {
	if _, ok := s.Tables[name]; ok {
		return false
	}
	return len(s.caseTwins(name)) > 0
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

// Reset drops the scopes rs covers. The tables stay in Local; deleting
// them there is ResetLate's work, which a reset runs first (ARCHITECTURE.md
// 13).
func (s *Service) Reset(rs kernel.ResetScope) {
	s.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
}

// Rearm arms a TTL sweep due at now for every table with TTL enabled; each
// sweep arms the next for the earliest expiry it finds.
func (s *Service) Rearm(now clock.Instant) { s.sweeps.rearm(s.enabledTTL(), now) }

// Resources lists the tables of scope by name.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Tables))
		for _, name := range slices.Sorted(maps.Keys(st.Tables)) {
			out = append(out, kernel.Resource{ARN: tableARN(scope, name), Summary: "held by DynamoDB Local"})
		}
		return out
	})
}

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }

// Switches lists no behaviors: every deviation is Local's, listed in
// coverage.
func (*Service) Switches() []fidelity.Entry { return nil }
