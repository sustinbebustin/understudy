package kinesis

import (
	"context"
	"errors"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Service is Amazon Kinesis Data Streams slice 1: one state cell per
// account and region.
type Service struct {
	opts Options
}

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// stateVersion is the schema version of the service's snapshot section.
const stateVersion = 1

// New binds the 21 operations of slice 1 (kinesis.md 13.1); the other 24
// stay unbound and answer 501. Not implemented yet: every bound operation
// answers 501 NotImplemented.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{opts: o}
	b := kinesisapi.NewBinder(d.Binder)
	cov := registry.Partial{Note: "not implemented yet"}
	b.CreateStream(unbuilt[kinesisapi.CreateStreamInput, kinesisapi.CreateStreamOutput], cov)
	b.DeleteStream(unbuilt[kinesisapi.DeleteStreamInput, kinesisapi.DeleteStreamOutput], cov)
	b.DescribeStream(unbuilt[kinesisapi.DescribeStreamInput, kinesisapi.DescribeStreamOutput], cov)
	b.DescribeStreamSummary(unbuilt[kinesisapi.DescribeStreamSummaryInput, kinesisapi.DescribeStreamSummaryOutput], cov)
	b.ListStreams(unbuilt[kinesisapi.ListStreamsInput, kinesisapi.ListStreamsOutput], cov)
	b.ListShards(unbuilt[kinesisapi.ListShardsInput, kinesisapi.ListShardsOutput], cov)
	b.IncreaseStreamRetentionPeriod(unbuilt[kinesisapi.IncreaseStreamRetentionPeriodInput, kinesisapi.IncreaseStreamRetentionPeriodOutput], cov)
	b.DecreaseStreamRetentionPeriod(unbuilt[kinesisapi.DecreaseStreamRetentionPeriodInput, kinesisapi.DecreaseStreamRetentionPeriodOutput], cov)
	b.PutRecord(unbuilt[kinesisapi.PutRecordInput, kinesisapi.PutRecordOutput], cov)
	b.PutRecords(unbuilt[kinesisapi.PutRecordsInput, kinesisapi.PutRecordsOutput], cov)
	b.GetShardIterator(unbuilt[kinesisapi.GetShardIteratorInput, kinesisapi.GetShardIteratorOutput], cov)
	b.GetRecords(unbuilt[kinesisapi.GetRecordsInput, kinesisapi.GetRecordsOutput], cov)
	b.AddTagsToStream(unbuilt[kinesisapi.AddTagsToStreamInput, kinesisapi.AddTagsToStreamOutput], cov)
	b.RemoveTagsFromStream(unbuilt[kinesisapi.RemoveTagsFromStreamInput, kinesisapi.RemoveTagsFromStreamOutput], cov)
	b.ListTagsForStream(unbuilt[kinesisapi.ListTagsForStreamInput, kinesisapi.ListTagsForStreamOutput], cov)
	b.TagResource(unbuilt[kinesisapi.TagResourceInput, kinesisapi.TagResourceOutput], cov)
	b.UntagResource(unbuilt[kinesisapi.UntagResourceInput, kinesisapi.UntagResourceOutput], cov)
	b.ListTagsForResource(unbuilt[kinesisapi.ListTagsForResourceInput, kinesisapi.ListTagsForResourceOutput], cov)
	b.DescribeLimits(unbuilt[kinesisapi.DescribeLimitsInput, kinesisapi.DescribeLimitsOutput], cov)
	b.EnableEnhancedMonitoring(unbuilt[kinesisapi.EnableEnhancedMonitoringInput, kinesisapi.EnhancedMonitoringOutput], cov)
	b.DisableEnhancedMonitoring(unbuilt[kinesisapi.DisableEnhancedMonitoringInput, kinesisapi.EnhancedMonitoringOutput], cov)
	return s
}

// unbuilt answers an operation slice 1 binds but no ticket has built yet.
func unbuilt[In, Out any](context.Context, *kernel.Call, *In) (*Out, *awserr.Error) { //nolint:unparam // a kernel.Handler; built handlers return their output
	return nil, kinesisapi.ErrNotImplemented("not implemented yet")
}

// Name returns "kinesis".
func (*Service) Name() ref.ServiceID { return "kinesis" }

// SchemaVersion returns the version of the service's snapshot section.
func (*Service) SchemaVersion() int { return stateVersion }

// Export returns no section: the service holds no state yet.
func (*Service) Export() ([]store.Section, error) { return nil, nil }

// errStateUnbuilt is Import's answer to a section until the state exists.
var errStateUnbuilt = errors.New("kinesis: loading a snapshot section is not implemented yet; the section was not applied")

// Import accepts no section yet.
func (*Service) Import(ss []store.Section) error {
	if len(ss) > 0 {
		return errStateUnbuilt
	}
	return nil
}

// Reset has nothing to drop yet.
func (*Service) Reset(kernel.ResetScope) {}

// Rearm has no timers to arm yet.
func (*Service) Rearm(clock.Instant) {}

// Resources lists nothing yet.
func (*Service) Resources(aws.Scope) []kernel.Resource { return nil }

// Scopes returns no scope yet.
func (*Service) Scopes() []aws.Scope { return nil }

// Switches lists no switch yet.
func (*Service) Switches() []fidelity.Entry { return nil }
