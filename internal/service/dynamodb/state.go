package dynamodb

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshots, and of the
// item sections of ExportLate. Version 4 adds the scope's streams and its
// iterator key (ADR 0009 amendment of 2026-10-27). A version 3 section is
// migrated on import:
// it is a version 4 section without streams, whose item sections are
// written the same way (Service.Import).
const stateVersion = 4

// stateVersionNoStreams is the version before streams were saved, which
// Import and ImportLate still read.
const stateVersionNoStreams = 3

// stateCodec is the snapshot codec of State, with the Billing sum in the
// tagged-union form. A table jsonx refuses is a bug in this file; the codec
// then fails every Export and Import with that error. Unmarshal refuses a
// table without billing (*MissingBillingError), so Table.Billing is never
// nil after an Import either.
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
	return store.Codec{Marshal: c.Marshal, Unmarshal: func(b []byte, v any) error {
		st, ok := v.(*State)
		if !ok {
			return fmt.Errorf("dynamodb state codec: asked to decode into %T; it decodes only *State", v)
		}
		if err := c.Unmarshal(b, st); err != nil {
			return err
		}
		return st.checkBilling()
	}}
}

// MissingBillingError is a snapshot table that carries no billing, which
// every recorded table has; the snapshot was not written by Export.
type MissingBillingError struct {
	Table string
}

func (e *MissingBillingError) Error() string {
	return fmt.Sprintf("dynamodb snapshot: table %s has no billing mode; every exported table records one, so the snapshot was edited or written by another tool", e.Table)
}

// State is the shim's view of one account and region: the tables Local
// holds there, which Local itself cannot tell apart by letter case, and
// what Local drops or answers wrongly about each (Table).
type State struct {
	// Tables is keyed by table name as created.
	Tables map[string]Table `json:"tables"`
	// Streams are the streams minted in the scope, oldest first, with their
	// records. A load keeps their labels and records and maps each enabled
	// one to its table's new stream in Local (Service.Import).
	Streams streamList `json:"streams,omitempty"`
	// IteratorKey signs the scope's shard iterators; drawn on the first
	// GetShardIterator. Saved, so an iterator issued before a save reads
	// after a load until it expires (ADR 0009 amendment of 2026-10-27).
	IteratorKey []byte `json:"iteratorKey,omitempty"`
	// usedIterators are the shard iterators GetRecords has read with, each
	// with its issue instant, kept until it expires; filled only under
	// IteratorSingleUse. Not saved: after a load, an iterator read before
	// the save reads once more.
	usedIterators map[string]clock.Instant
}

func newState(aws.Scope) State { return State{Tables: map[string]Table{}} }

// checkBilling refuses the first table, by name, that has no billing.
func (s *State) checkBilling() error {
	for _, name := range slices.Sorted(maps.Keys(s.Tables)) {
		if s.Tables[name].Billing == nil {
			return &MissingBillingError{Table: name}
		}
	}
	return nil
}

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

// Import loads sections written by Export of this schema version or of
// version 3, and maps the streams of the tables ImportLate re-created in
// Local (relink). A section that does not decode is left for the cells'
// import to refuse.
func (s *Service) Import(ss []store.Section) error {
	relinks := s.takeRelinks()
	now := s.kernel.Clock().Now()
	out := make([]store.Section, 0, len(ss))
	for _, sec := range ss {
		if sec.Version == stateVersionNoStreams {
			sec.Version = stateVersion
		}
		if sec.Version == stateVersion {
			sec = s.relinked(sec, relinks, now)
		}
		out = append(out, sec)
	}
	return s.cells.Import(store.ScopeKeys(), stateVersion, out)
}

// relinked is sec with relinks applied to the tables of its scope; sec
// unchanged when it names no scope relinks has, or does not decode.
func (s *Service) relinked(sec store.Section, relinks map[tableKey]relink, now clock.Instant) store.Section {
	enc, ok := strings.CutSuffix(sec.Path, ".json")
	if !ok {
		return sec
	}
	scope, err := store.ScopeKeys().Decode(enc)
	if err != nil {
		return sec
	}
	var tables []string
	for k := range relinks {
		if k.scope == scope {
			tables = append(tables, k.table)
		}
	}
	if len(tables) == 0 {
		return sec
	}
	codec := stateCodec()
	st := newState(scope)
	if err := codec.Unmarshal(sec.Data, &st); err != nil {
		return sec
	}
	slices.Sort(tables)
	for _, t := range tables {
		st.relink(scope, t, relinks[tableKey{scope: scope, table: t}], now, s.sequencer())
	}
	data, err := codec.Marshal(&st)
	if err != nil {
		return sec
	}
	sec.Data = data
	return sec
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

// Switches lists the identity of a denial, which understudy answers before
// forwarding (iam-enforcement-surfaces.md 12.1, 12.8); every other
// deviation is Local's, listed in coverage.
func (s *Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{
		authz.DenialSwitch("dynamodb.denial.type", "__type com.amazon.coral.service#AccessDeniedException, 400", evidence.Inferred, "3.2", "IAM-ENF-04",
			s.kernel.Bindings().AuthorizedOps(dynamodbapi.Service())),
	}
}
