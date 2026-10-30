package kinesis

import (
	"context"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/paging"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// Stream defaults and limits (aws-doc kinesis.md 4.1, 4.7, finding 4).
const (
	defaultRetentionHours   = 24
	maxRetentionHours       = 8760
	defaultMaxRecordSizeKiB = 1024
	// onDemandShards is the shard count of an ON_DEMAND stream, which
	// never scales in slice 1 (third-party-recorded, kinesis.md finding 4).
	onDemandShards = 4
	// streamPage is the most streams or shards one ListStreams or
	// DescribeStream page holds (aws-doc kinesis.md 4.4, 4.5).
	streamPage = 100
	// shardLimit and onDemandStreamLimit are the documented us-east-1
	// quotas DescribeLimits reports (aws-doc kinesis.md 4.1). CreateStream
	// enforces shardLimit per scope, which also bounds the memory one
	// request can take; onDemandStreamLimit is not enforced.
	shardLimit          = 20000
	onDemandStreamLimit = 50
)

const describeNote = "the optional fields of a fresh stream (EncryptionType NONE, StreamModeDetails, MaxRecordSizeInKiB 1024, no KeyId, WarmThroughput or ConsumerCount) and the not-found text over JSON are inferred; AWS's are UNVERIFIED (kinesis.md 4.4, finding 5)"

const retentionNote = "error texts follow kinesalite and are UNVERIFIED (kinesis.md 4.7)"

// bindStreams binds the stream lifecycle, description, listing, retention
// and limit operations.
func (s *Service) bindStreams(b kinesisapi.Binder) {
	b.CreateStream(s.createStream, registry.Partial{Note: createStreamNote})
	b.DeleteStream(s.deleteStream, registry.Partial{Note: "the stream is gone when DeleteStream returns, where AWS reports DELETING first"})
	b.DescribeStreamSummary(s.describeStreamSummary, registry.Partial{Note: describeNote})
	b.DescribeStream(s.describeStream, registry.Partial{Note: describeNote})
	b.ListStreams(s.listStreams, registry.Partial{Note: "names are listed in lexical order; AWS's order and token lifetime are UNVERIFIED (kinesis.md 4.5)"})
	b.IncreaseStreamRetentionPeriod(s.increaseRetention, registry.Partial{Note: retentionNote})
	b.DecreaseStreamRetentionPeriod(s.decreaseRetention, registry.Partial{Note: retentionNote})
	b.DescribeLimits(s.describeLimits, registry.Partial{Note: "ShardLimit is 20000 and OnDemandStreamCountLimit 50 in every region; CreateStream enforces the shard limit, not the on-demand stream limit"})
}

const createStreamNote = "ON_DEMAND streams start with four shards and never scale; WarmThroughputMiBps and RecordDistributionStrategy AUTO are answered with 501 NotImplemented; a stream that would take the region's shards above 20000 is refused with LimitExceededException, whose text is understudy's and UNVERIFIED (kinesis.md 4.1), and the on-demand stream quota is not enforced; the stream is ACTIVE when CreateStream returns, where AWS reports CREATING first"

func (s *Service) createStream(_ context.Context, c *kernel.Call, in *kinesisapi.CreateStreamInput) (*kinesisapi.CreateStreamOutput, *awserr.Error) {
	// Settings with no test-time effect are refused, never stored without
	// one (kinesis.md 13.9; contract 3.7).
	if in.WarmThroughputMiBps != nil {
		return nil, errNotImplemented("WarmThroughputMiBps", "CreateStream")
	}
	if in.RecordDistributionStrategy != nil && *in.RecordDistributionStrategy == kinesisapi.RecordDistributionStrategyAuto {
		return nil, errNotImplemented("RecordDistributionStrategy AUTO", "CreateStream")
	}
	// ShardCount without a mode is PROVISIONED (kinesis.md 13.6 item 1);
	// neither is ON_DEMAND with four shards (finding 4).
	mode := kinesisapi.StreamModeOnDemand
	switch {
	case in.StreamModeDetails != nil:
		mode = in.StreamModeDetails.StreamMode
	case in.ShardCount != nil:
		mode = kinesisapi.StreamModeProvisioned
	}
	shards := onDemandShards
	switch {
	case mode == kinesisapi.StreamModeOnDemand && in.ShardCount != nil:
		return nil, errShardCountOnDemand()
	case mode == kinesisapi.StreamModeProvisioned && in.ShardCount == nil:
		return nil, errShardCountProvisioned()
	case in.ShardCount != nil:
		shards = int(*in.ShardCount)
	}
	account := c.Scope().Account.String()
	// Refused before Split, which allocates per shard (aws-doc kinesis.md 4.1).
	if shards > shardLimit {
		return nil, errShardLimit(c.Scope(), 0, shards)
	}
	if err := checkTags(in.StreamName, account, nil, in.Tags); err != nil {
		return nil, err
	}
	maxRecord := int32(defaultMaxRecordSizeKiB)
	if in.MaxRecordSizeInKiB != nil {
		maxRecord = *in.MaxRecordSizeInKiB
	}
	ranges := Split(shards, s.opts.EvenSplit)
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *State, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if _, taken := st.Streams[in.StreamName]; taken {
			return struct{}{}, errStreamInUse(in.StreamName, account)
		}
		if open := st.shardCount(); open+len(ranges) > shardLimit {
			return struct{}{}, errShardLimit(c.Scope(), open, len(ranges))
		}
		if st.TokenKey == nil {
			st.TokenKey = s.ids.StreamIteratorKey()
		}
		str := Stream{
			Name: in.StreamName, Created: now, Mode: mode,
			RetentionHours: defaultRetentionHours, MaxRecordSizeKiB: maxRecord,
			Tags: maps.Clone(in.Tags), Shards: make([]Shard, len(ranges)),
		}
		for i, r := range ranges {
			str.Shards[i] = newShard(i, r, now, s.opts.SequenceLayout)
		}
		st.Streams[in.StreamName] = str
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(streamARN(c.Scope(), in.StreamName))
	return &kinesisapi.CreateStreamOutput{}, nil
}

// deleteStream removes the stream with its tags and records at once
// (contract 0) and wakes its readers, which find it gone.
// EnforceConsumerDeletion has nothing to act on: no consumer exists.
func (s *Service) deleteStream(ctx context.Context, c *kernel.Call, in *kinesisapi.DeleteStreamInput) (*kinesisapi.DeleteStreamOutput, *awserr.Error) {
	r, err := streamOf(c, in.StreamName, in.StreamARN)
	if err != nil {
		return nil, err
	}
	if r.foreign {
		return nil, r.notFound()
	}
	scope := c.Scope()
	arn := streamARN(scope, r.name)
	released, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, tx *kernel.Tx) ([]blob.Ref, *awserr.Error) {
		str, ok := st.Streams[r.name]
		if !ok {
			return nil, r.notFound()
		}
		delete(st.Streams, r.name)
		tx.Disarm(s.retention, arn.String())
		tx.Wake(shardlog.WaitKey(arn))
		return str.blobRefs(), nil
	})
	if err != nil {
		return nil, err
	}
	s.release(ctx, released)
	c.Touch(arn)
	return &kinesisapi.DeleteStreamOutput{}, nil
}

// enhancedMonitoring is the stream's one EnhancedMetrics entry
// (kinesalite's form, kinesis.md 4.4): the stored metrics, empty when none
// is enabled.
func enhancedMonitoring(str Stream) []kinesisapi.EnhancedMetrics {
	return []kinesisapi.EnhancedMetrics{{ShardLevelMetrics: append([]kinesisapi.MetricsName{}, str.Metrics...)}}
}

// describeStreamSummary renders the fields of kinesis.md 13.6 item 2 and
// none of KeyId, WarmThroughput, ConsumerCount, StreamId,
// RecordDistributionStrategy or ChannelCount (KIN-03, the DescribeStream
// note).
func (s *Service) describeStreamSummary(_ context.Context, c *kernel.Call, in *kinesisapi.DescribeStreamSummaryInput) (*kinesisapi.DescribeStreamSummaryOutput, *awserr.Error) {
	r, err := streamOf(c, in.StreamName, in.StreamARN)
	if err != nil {
		return nil, err
	}
	str, err := s.readStream(c, r)
	if err != nil {
		return nil, err
	}
	return &kinesisapi.DescribeStreamSummaryOutput{StreamDescriptionSummary: kinesisapi.StreamDescriptionSummary{
		StreamName:              str.Name,
		StreamARN:               streamARN(c.Scope(), str.Name).String(),
		StreamStatus:            kinesisapi.StreamStatusActive,
		StreamModeDetails:       &kinesisapi.StreamModeDetails{StreamMode: str.Mode},
		RetentionPeriodHours:    str.RetentionHours,
		StreamCreationTimestamp: str.Created.Wire(),
		EnhancedMonitoring:      enhancedMonitoring(str),
		EncryptionType:          new(kinesisapi.EncryptionTypeNone),
		OpenShardCount:          int32(len(str.Shards)), //nolint:gosec // CreateStream's ShardCount is an int32
		MaxRecordSizeInKiB:      new(str.MaxRecordSizeKiB),
	}}, nil
}

// describeStream pages the shards in index order after
// ExclusiveStartShardId (from the first when the id is not the stream's),
// min(Limit or 100, 100) to a page (aws-doc kinesis.md 4.4).
func (s *Service) describeStream(_ context.Context, c *kernel.Call, in *kinesisapi.DescribeStreamInput) (*kinesisapi.DescribeStreamOutput, *awserr.Error) {
	r, err := streamOf(c, in.StreamName, in.StreamARN)
	if err != nil {
		return nil, err
	}
	str, err := s.readStream(c, r)
	if err != nil {
		return nil, err
	}
	from := 0
	if in.ExclusiveStartShardId != nil {
		for i := range str.Shards {
			if shardID(i) == *in.ExclusiveStartShardId {
				from = i + 1
				break
			}
		}
	}
	n := streamPage
	if in.Limit != nil {
		n = min(int(*in.Limit), streamPage)
	}
	to := min(from+n, len(str.Shards))
	shards := make([]kinesisapi.Shard, 0, to-from)
	for i := from; i < to; i++ {
		shards = append(shards, s.shardOf(str, i))
	}
	return &kinesisapi.DescribeStreamOutput{StreamDescription: kinesisapi.StreamDescription{
		StreamName:              str.Name,
		StreamARN:               streamARN(c.Scope(), str.Name).String(),
		StreamStatus:            kinesisapi.StreamStatusActive,
		StreamModeDetails:       &kinesisapi.StreamModeDetails{StreamMode: str.Mode},
		Shards:                  shards,
		HasMoreShards:           to < len(str.Shards),
		RetentionPeriodHours:    str.RetentionHours,
		StreamCreationTimestamp: str.Created.Wire(),
		EnhancedMonitoring:      enhancedMonitoring(str),
		EncryptionType:          new(kinesisapi.EncryptionTypeNone),
	}}, nil
}

// shardOf renders shard i of str: open, so without an ending sequence
// number, and never a child, so without parents.
func (s *Service) shardOf(str Stream, i int) kinesisapi.Shard {
	r := str.Shards[i].Range
	start := str.Shards[i].Layout.sequencer().Start(shardlog.ShardInfo{ID: shardID(i), Index: i, Opened: str.Created})
	return kinesisapi.Shard{
		ShardId:             shardID(i),
		HashKeyRange:        kinesisapi.HashKeyRange{StartingHashKey: r.Start.String(), EndingHashKey: r.End.String()},
		SequenceNumberRange: kinesisapi.SequenceNumberRange{StartingSequenceNumber: string(start)},
	}
}

// listingStreams names ListStreams in the tokens it issues.
const listingStreams paging.Listing = "kinesis-streams"

// listStreams lists the scope's streams in lexical name order (UNVERIFIED,
// ListStreams note), min(Limit or 100, 100) to a page (aws-doc kinesis.md
// 4.5). A NextToken never expires and resumes at the next name.
func (s *Service) listStreams(_ context.Context, c *kernel.Call, in *kinesisapi.ListStreamsInput) (*kinesisapi.ListStreamsOutput, *awserr.Error) {
	if in.NextToken != nil && in.ExclusiveStartStreamName != nil {
		return nil, errListStreamsTokenAndStart()
	}
	scope := c.Scope()
	streams := readState(s, scope, func(st *State) []Stream {
		out := make([]Stream, 0, len(st.Streams))
		for _, name := range slices.Sorted(maps.Keys(st.Streams)) {
			if in.ExclusiveStartStreamName == nil || name > *in.ExclusiveStartStreamName {
				out = append(out, st.Streams[name])
			}
		}
		return out
	})
	n := streamPage
	if in.Limit != nil {
		n = min(int(*in.Limit), streamPage)
	}
	p, perr := paging.Keyset(listingStreams, streams, func(str Stream) string { return str.Name }, n, in.NextToken)
	if perr != nil {
		return nil, errInvalidNextToken()
	}
	out := &kinesisapi.ListStreamsOutput{
		StreamNames:     make([]string, 0, len(p.Items)),
		StreamSummaries: make([]kinesisapi.StreamSummary, 0, len(p.Items)),
		HasMoreStreams:  p.Truncated,
		NextToken:       p.Next,
	}
	for _, str := range p.Items {
		out.StreamNames = append(out.StreamNames, str.Name)
		out.StreamSummaries = append(out.StreamSummaries, kinesisapi.StreamSummary{
			StreamName:              str.Name,
			StreamARN:               streamARN(scope, str.Name).String(),
			StreamStatus:            kinesisapi.StreamStatusActive,
			StreamModeDetails:       &kinesisapi.StreamModeDetails{StreamMode: str.Mode},
			StreamCreationTimestamp: new(str.Created.Wire()),
		})
	}
	return out, nil
}

// retentionChange is the direction of a retention call.
type retentionChange uint8

const (
	increase retentionChange = iota
	decrease
)

// setRetention applies Increase or DecreaseStreamRetentionPeriod: 24 to
// 8760 hours (aws-doc kinesis.md 4.7), not against the direction, and an
// equal value per kinesis.retention.equal-value (KIN-06). The new period
// applies at once: records already trimmed stay gone, and a Decrease trims
// what it now expires (aws-doc kinesis.md 4.7).
func (s *Service) setRetention(ctx context.Context, c *kernel.Call, name, arn *string, n int32, dir retentionChange) *awserr.Error {
	r, err := streamOf(c, name, arn)
	if err != nil {
		return err
	}
	if err := s.updateStream(c, r, func(str *Stream, _ clock.Instant) *awserr.Error {
		cur := str.RetentionHours
		switch {
		case n < defaultRetentionHours:
			return errRetentionTooShort(n)
		case n > maxRetentionHours:
			return errRetentionTooLong(n)
		case dir == increase && n < cur:
			return errRetentionShorter(n, str.Name, cur)
		case dir == decrease && n > cur:
			return errRetentionLonger(n, str.Name, cur)
		case n == cur && s.opts.RetentionEqual == RetentionEqualReject && dir == increase:
			return errRetentionEqual(n, str.Name, "more", cur)
		case n == cur && s.opts.RetentionEqual == RetentionEqualReject:
			return errRetentionEqual(n, str.Name, "less", cur)
		}
		str.RetentionHours = n
		return nil
	}); err != nil {
		return err
	}
	return s.trimStream(ctx, c, c.Scope(), r.name)
}

func (s *Service) increaseRetention(ctx context.Context, c *kernel.Call, in *kinesisapi.IncreaseStreamRetentionPeriodInput) (*kinesisapi.IncreaseStreamRetentionPeriodOutput, *awserr.Error) {
	if err := s.setRetention(ctx, c, in.StreamName, in.StreamARN, in.RetentionPeriodHours, increase); err != nil {
		return nil, err
	}
	return &kinesisapi.IncreaseStreamRetentionPeriodOutput{}, nil
}

func (s *Service) decreaseRetention(ctx context.Context, c *kernel.Call, in *kinesisapi.DecreaseStreamRetentionPeriodInput) (*kinesisapi.DecreaseStreamRetentionPeriodOutput, *awserr.Error) {
	if err := s.setRetention(ctx, c, in.StreamName, in.StreamARN, in.RetentionPeriodHours, decrease); err != nil {
		return nil, err
	}
	return &kinesisapi.DecreaseStreamRetentionPeriodOutput{}, nil
}

// describeLimits reports the fixed quotas with the scope's open shards and
// ON_DEMAND streams, and no channel members (contract 3.5).
func (s *Service) describeLimits(_ context.Context, c *kernel.Call, _ *kinesisapi.DescribeLimitsInput) (*kinesisapi.DescribeLimitsOutput, *awserr.Error) {
	out := readState(s, c.Scope(), func(st *State) *kinesisapi.DescribeLimitsOutput {
		out := &kinesisapi.DescribeLimitsOutput{ShardLimit: shardLimit, OnDemandStreamCountLimit: onDemandStreamLimit}
		out.OpenShardCount = int32(st.shardCount()) //nolint:gosec // at most shardLimit, enforced by CreateStream
		for _, str := range st.Streams {
			if str.Mode == kinesisapi.StreamModeOnDemand {
				out.OnDemandStreamCount++
			}
		}
		return out
	})
	return out, nil
}
