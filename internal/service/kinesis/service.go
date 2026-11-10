package kinesis

import (
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/shardlog"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Service is Amazon Kinesis Data Streams slice 1: one state cell per
// account and region.
type Service struct {
	opts  Options
	k     *kernel.Kernel
	ids   *ids.Source
	blobs *blob.Store
	cells *store.Partitioned[aws.Scope, State]
	// retention is TimerRetention as registered with the kernel.
	retention kernel.HandledKind
}

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// stateVersion is the schema version of the service's snapshot section.
const stateVersion = 1

// New binds the 21 operations of slice 1 (kinesis.md 13.1) and the
// retention timer; every other operation stays unbound and answers 501.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{opts: o, k: d.Kernel, ids: d.IDs, blobs: d.Blob, cells: store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newState)}
	s.retention = d.Kernel.Timers().Handle(TimerRetention, s.onRetention)
	b := kinesisapi.NewBinder(d.Binder)
	s.bindStreams(b)
	s.bindShards(b)
	s.bindData(b)
	s.bindMonitoring(b)
	s.bindTags(b)
	declareAccess(d.Binder)
	return s
}

// Name returns "kinesis".
func (*Service) Name() ref.ServiceID { return "kinesis" }

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

// Reset drops the streams of the scopes rs covers.
func (s *Service) Reset(rs kernel.ResetScope) {
	s.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
}

// Resources lists the streams of scope in name order, each ACTIVE.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	return readState(s, scope, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Streams))
		for _, name := range slices.Sorted(maps.Keys(st.Streams)) {
			out = append(out, kernel.Resource{ARN: streamARN(scope, name), Summary: string(kinesisapi.StreamStatusActive)})
		}
		return out
	})
}

// Scopes returns the accounts and regions that hold a stream.
func (s *Service) Scopes() []aws.Scope {
	var out []aws.Scope
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		if readState(s, scope, func(st *State) bool { return !st.IsZero() }) {
			out = append(out, scope)
		}
	}
	return out
}

// Switches lists the five Kinesis switches, each inferred
// and unrecorded, reporting the value the service was built with.
func (s *Service) Switches() []fidelity.Entry {
	sw := func(name, value, ref, record string, ops ...string) fidelity.Entry {
		return fidelity.Switch[string]{
			Name: name, Value: value, Label: evidence.Inferred, Ref: ref, Record: record, Ops: ops,
			Settles: fidelity.NoScenario{Reason: "unrecorded: " + record + " is queued"},
		}.Entry()
	}
	const doc = "docs/research/kinesis.md#"
	return []fidelity.Entry{
		sw("kinesis.hash.even-split", s.opts.EvenSplit.String(), doc+"33-hash-key-space-and-the-even-split", "KIN-01",
			"kinesis.CreateStream", "kinesis.DescribeStream", "kinesis.ListShards"),
		sw("kinesis.hash.md5-byte-order", s.opts.HashByteOrder.String(), doc+"53-shard-selection", "KIN-13",
			"kinesis.PutRecord", "kinesis.PutRecords"),
		sw("kinesis.sequence.layout", s.opts.SequenceLayout.String(), doc+"34-sequence-numbers", "KIN-09, KIN-16",
			"kinesis.PutRecord", "kinesis.PutRecords", "kinesis.GetRecords", "kinesis.DescribeStream", "kinesis.ListShards"),
		sw("kinesis.get-records.millis-behind-latest", s.opts.MillisBehind.String(), doc+"510-approximatearrivaltimestamp-and-millisbehindlatest", "KIN-10",
			"kinesis.GetRecords"),
		sw("kinesis.retention.equal-value", s.opts.RetentionEqual.String(), doc+"47-retention-period", "KIN-06",
			"kinesis.IncreaseStreamRetentionPeriod", "kinesis.DecreaseStreamRetentionPeriod"),
	}
}

// sequencer is the layout's Sequencer: decimal-56 is a 56-digit decimal
// counter, anything else V2Sequencer.
func (l SequenceLayout) sequencer() shardlog.Sequencer {
	if l == LayoutDecimal56 {
		return shardlog.DecimalCounter{Digits: 56}
	}
	return V2Sequencer{}
}
