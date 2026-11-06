package kinesis_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/service/kinesis"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// ---- CreateStream (kinesis.md 4.1, 13.6; contract 3.5) ----.

// The three modes: ShardCount without a mode is PROVISIONED (kinesis.md
// 13.6 item 1, finding 4); neither mode nor count is ON_DEMAND with four
// shards (third-party-recorded, golden 1: localstack@8b9a79f
// tests/aws/services/kinesis/test_kinesis.snapshot.json:L2-L47, validated
// 2022-08-26; kinesis.md finding 4); ON_DEMAND alone is four shards. The
// stream is ACTIVE when CreateStream returns (contract 0: status
// transitions are final).
func TestCreateStreamModes(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("prov", 2)
		call(h, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{StreamName: "bare"})
		call(h, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{StreamName: "od", StreamModeDetails: &kinesisapi.StreamModeDetails{StreamMode: kinesisapi.StreamModeOnDemand}})
		for _, tc := range []struct {
			name   string
			mode   kinesisapi.StreamMode
			shards int32
		}{
			{"prov", kinesisapi.StreamModeProvisioned, 2},
			{"bare", kinesisapi.StreamModeOnDemand, 4},
			{"od", kinesisapi.StreamModeOnDemand, 4},
		} {
			s := h.summary(tc.name)
			if s.StreamStatus != "ACTIVE" || s.StreamModeDetails == nil || s.StreamModeDetails.StreamMode != tc.mode || s.OpenShardCount != tc.shards {
				h.Errorf("%s: status %s mode %v shards %d; want ACTIVE %s %d", tc.name, s.StreamStatus, s.StreamModeDetails, s.OpenShardCount, tc.mode, tc.shards)
			}
			if got := len(h.describe(tc.name).Shards); got != int(tc.shards) {
				h.Errorf("%s: DescribeStream lists %d shards, want %d", tc.name, got, tc.shards)
			}
		}
	})
}

// CreateStream's refusals in the contract's order (contract 3.5; texts (u)
// or (k), UNVERIFIED: KIN-05, KIN-07, kinesis.md 13.3), the 501s for
// settings without a test-time effect (kinesis.md 13.9: never accepted and
// ignored; contract 3.7), and a missing name answering 400 (third-party-
// recorded status only, golden 6: localstack@8b9a79f
// tests/aws/services/kinesis/test_kinesis.py:L115-L122, validated
// 2024-06-10).
func TestCreateStreamRefusals(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		create := func(in kinesisapi.CreateStreamInput) *awserr.Error {
			h.Helper()
			if in.StreamName == "" {
				in.StreamName = "s"
			}
			return fail(h, kinesisapi.OpCreateStream, &in)
		}
		if err := fail(h, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{}); err.ID.Status != 400 {
			h.Errorf("CreateStream without a name: status %d, want 400", err.ID.Status)
		}
		wantNotImplemented(h, "WarmThroughputMiBps", create(kinesisapi.CreateStreamInput{WarmThroughputMiBps: new(int32(0))}), "WarmThroughputMiBps", "CreateStream")
		wantNotImplemented(h, "AUTO", create(kinesisapi.CreateStreamInput{RecordDistributionStrategy: new(kinesisapi.RecordDistributionStrategyAuto)}), "RecordDistributionStrategy AUTO", "CreateStream")
		invalidArg(h, "ON_DEMAND with ShardCount", create(kinesisapi.CreateStreamInput{ShardCount: new(int32(2)), StreamModeDetails: &kinesisapi.StreamModeDetails{StreamMode: kinesisapi.StreamModeOnDemand}}),
			"ShardCount must not be specified for a stream in ON_DEMAND mode.")
		invalidArg(h, "PROVISIONED without ShardCount", create(kinesisapi.CreateStreamInput{StreamModeDetails: &kinesisapi.StreamModeDetails{StreamMode: kinesisapi.StreamModeProvisioned}}),
			"ShardCount must be specified for a stream in PROVISIONED mode.")
		h.create("s", 1)
		wantErr(h, "duplicate", create(kinesisapi.CreateStreamInput{ShardCount: new(int32(1))}), "ResourceInUseException", 400, "Stream s under account "+account+" already exists.")
		// Nothing refused was created.
		out := call(h, kinesisapi.OpListStreams, &kinesisapi.ListStreamsInput{})
		if !slices.Equal(out.StreamNames, []string{"s"}) {
			h.Errorf("streams after the refusals = %v, want [s]", out.StreamNames)
		}
	})
}

// DescribeStreamSummary of a fresh stream: the fields Terraform reads
// (kinesis.md 13.6 item 2, 10.3), EnhancedMonitoring one empty entry
// (kinesis.md 4.4, kinesalite's form [E]), MaxRecordSizeInKiB 1024 by
// default (aws-doc, kinesis.md 4.1) or as created, the creation instant on
// the emulator clock (kinesis.md 13.5 item 1), and none of the members
// contract 3.5 omits (KeyId, WarmThroughput, ConsumerCount, StreamId,
// RecordDistributionStrategy, ChannelCount; KIN-03, the DescribeStream
// note).
func TestDescribeStreamSummaryOfAFreshStream(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		created := h.Clock.Now()
		h.create("orders", 3)
		call(h, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{StreamName: "big", ShardCount: new(int32(1)), MaxRecordSizeInKiB: new(int32(2048))})
		h.advance(time.Minute)
		s := h.summary("orders")
		if s.StreamName != "orders" || s.StreamARN != arnOf("orders") || s.StreamStatus != "ACTIVE" || s.RetentionPeriodHours != 24 || s.OpenShardCount != 3 {
			h.Errorf("summary = %+v", s)
		}
		if s.StreamCreationTimestamp.UnixMilli() != created.Wire().UnixMilli() {
			h.Errorf("StreamCreationTimestamp %v, want the CreateStream instant %v", s.StreamCreationTimestamp, created)
		}
		if s.StreamModeDetails == nil || s.StreamModeDetails.StreamMode != kinesisapi.StreamModeProvisioned {
			h.Errorf("StreamModeDetails = %+v, want PROVISIONED", s.StreamModeDetails)
		}
		if deref(s.EncryptionType) != kinesisapi.EncryptionTypeNone {
			h.Errorf("EncryptionType = %v, want NONE", deref(s.EncryptionType))
		}
		if len(s.EnhancedMonitoring) != 1 || len(s.EnhancedMonitoring[0].ShardLevelMetrics) != 0 {
			h.Errorf("EnhancedMonitoring = %+v, want [{ShardLevelMetrics: []}]", s.EnhancedMonitoring)
		}
		if deref(s.MaxRecordSizeInKiB) != int32(1024) {
			h.Errorf("MaxRecordSizeInKiB = %v, want 1024", deref(s.MaxRecordSizeInKiB))
		}
		if s.KeyId != nil || s.WarmThroughput != nil || s.ConsumerCount != nil || s.StreamId != nil || s.RecordDistributionStrategy != nil || s.ChannelCount != nil {
			h.Errorf("summary carries a member contract 3.5 omits: %+v", s)
		}
		if got := deref(h.summary("big").MaxRecordSizeInKiB); got != int32(2048) {
			h.Errorf("MaxRecordSizeInKiB as created = %v, want 2048", got)
		}
	})
}

// ---- DescribeStream and ListShards (kinesis.md 4.4, 4.6) ----.

// DescribeStream lists shards in index order, each with the switch's even
// split (kinesis.hash.even-split, read from kinesis.Split with the
// harness's option) and the layout switch's StartingSequenceNumber, no end
// and no parent; a page holds min(Limit or 100, 100) shards after
// ExclusiveStartShardId (kinesis.md 4.4, aws-doc). The stream fields are
// the summary's, without KeyId (contract 3.5).
func TestDescribeStreamShards(t *testing.T) {
	for _, o := range []kinesis.Options{{}, {EvenSplit: kinesis.SplitFloorI, SequenceLayout: kinesis.LayoutDecimal56}} {
		run(t, o, func(h *harness) {
			opened := h.Clock.Now()
			h.create("ten", 10)
			d := h.describe("ten")
			if d.StreamName != "ten" || d.StreamARN != arnOf("ten") || d.StreamStatus != "ACTIVE" || d.RetentionPeriodHours != 24 || d.HasMoreShards ||
				deref(d.EncryptionType) != kinesisapi.EncryptionTypeNone || d.KeyId != nil || d.StreamModeDetails == nil {
				h.Errorf("DescribeStream = %+v", d)
			}
			ranges := kinesis.Split(10, o.EvenSplit)
			if len(d.Shards) != 10 || len(ranges) != 10 {
				h.Fatalf("%d shards, %d ranges; want 10", len(d.Shards), len(ranges))
			}
			for i, sh := range d.Shards {
				start := h.sequencer().Start(shardlog.ShardInfo{ID: shardID(i), Index: i, Opened: opened})
				if sh.ShardId != shardID(i) || sh.HashKeyRange.StartingHashKey != ranges[i].Start.String() || sh.HashKeyRange.EndingHashKey != ranges[i].End.String() ||
					sh.SequenceNumberRange.StartingSequenceNumber != string(start) || sh.SequenceNumberRange.EndingSequenceNumber != nil ||
					sh.ParentShardId != nil || sh.AdjacentParentShardId != nil {
					h.Errorf("shard %d = %+v, want %s %s..%s from %s", i, sh, shardID(i), ranges[i].Start, ranges[i].End, start)
				}
			}
			page := call(h, kinesisapi.OpDescribeStream, &kinesisapi.DescribeStreamInput{StreamName: new("ten"), Limit: new(int32(3)), ExclusiveStartShardId: new(shardID(2))}).StreamDescription
			if got := shardIDs(page.Shards); !slices.Equal(got, []string{shardID(3), shardID(4), shardID(5)}) || !page.HasMoreShards {
				h.Errorf("page after shard 2, Limit 3 = %v (more %v)", got, page.HasMoreShards)
			}
			last := call(h, kinesisapi.OpDescribeStream, &kinesisapi.DescribeStreamInput{StreamName: new("ten"), Limit: new(int32(3)), ExclusiveStartShardId: new(shardID(7))}).StreamDescription
			if got := shardIDs(last.Shards); !slices.Equal(got, []string{shardID(8), shardID(9)}) || last.HasMoreShards {
				h.Errorf("last page = %v (more %v)", got, last.HasMoreShards)
			}
		})
	}
	// More than 100 shards: a page holds at most 100 (kinesis.md 4.4,
	// aws-doc "at most 100 results are returned").
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("wide", 101)
		page := call(h, kinesisapi.OpDescribeStream, &kinesisapi.DescribeStreamInput{StreamName: new("wide"), Limit: new(int32(500))}).StreamDescription
		if len(page.Shards) != 100 || !page.HasMoreShards {
			h.Errorf("Limit 500 of 101 shards gives %d (more %v), want 100 and more", len(page.Shards), page.HasMoreShards)
		}
	})
}

func shardIDs(ss []kinesisapi.Shard) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.ShardId)
	}
	return out
}

// ListShards: the shard form of DescribeStream, MaxResults paging with an
// opaque NextToken, ExclusiveStartShardId and the filters (kinesis.md 4.6,
// aws-doc), by name or ARN.
func TestListShardsPagingAndFilters(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 5)
		all := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{StreamARN: new(arnOf("s"))})
		if got := shardIDs(all.Shards); len(got) != 5 || got[0] != shardID(0) || all.NextToken != nil {
			h.Errorf("ListShards = %v, token %v", got, deref(all.NextToken))
		}
		if !slices.EqualFunc(all.Shards, h.describe("s").Shards, func(a, b kinesisapi.Shard) bool {
			return a.ShardId == b.ShardId && a.HashKeyRange == b.HashKeyRange && a.SequenceNumberRange.StartingSequenceNumber == b.SequenceNumberRange.StartingSequenceNumber
		}) {
			h.Error("ListShards and DescribeStream disagree on the shards")
		}
		first := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{StreamName: new("s"), MaxResults: new(int32(2))})
		if got := shardIDs(first.Shards); !slices.Equal(got, []string{shardID(0), shardID(1)}) || first.NextToken == nil {
			h.Fatalf("first page = %v, token %v", got, deref(first.NextToken))
		}
		var got []string
		for tok := first.NextToken; tok != nil; {
			page := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{NextToken: tok, MaxResults: new(int32(2))})
			got = append(got, shardIDs(page.Shards)...)
			tok = page.NextToken
		}
		if !slices.Equal(got, []string{shardID(2), shardID(3), shardID(4)}) {
			h.Errorf("pages after the first = %v", got)
		}
		after := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{StreamName: new("s"), ExclusiveStartShardId: new(shardID(2))})
		if got := shardIDs(after.Shards); !slices.Equal(got, []string{shardID(3), shardID(4)}) {
			h.Errorf("ExclusiveStartShardId shard 2 = %v", got)
		}
		filtered := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{StreamName: new("s"), ShardFilter: &kinesisapi.ShardFilter{Type: kinesisapi.ShardFilterTypeAfterShardId, ShardId: new(shardID(3))}})
		if got := shardIDs(filtered.Shards); !slices.Equal(got, []string{shardID(4)}) {
			h.Errorf("AFTER_SHARD_ID shard 3 = %v", got)
		}
		for _, typ := range []kinesisapi.ShardFilterType{kinesisapi.ShardFilterTypeAtTrimHorizon, kinesisapi.ShardFilterTypeFromTrimHorizon, kinesisapi.ShardFilterTypeAtLatest} {
			out := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{StreamName: new("s"), ShardFilter: &kinesisapi.ShardFilter{Type: typ}})
			if len(out.Shards) != 5 {
				h.Errorf("%s lists %d shards, want all 5", typ, len(out.Shards))
			}
		}
		now := h.Clock.Now().Wire()
		out := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{StreamName: new("s"), ShardFilter: &kinesisapi.ShardFilter{Type: kinesisapi.ShardFilterTypeAtTimestamp, Timestamp: &now}})
		if len(out.Shards) != 5 {
			h.Errorf("AT_TIMESTAMP now lists %d shards, want the 5 open at it", len(out.Shards))
		}
		before := now.Add(-time.Hour)
		out = call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{StreamName: new("s"), ShardFilter: &kinesisapi.ShardFilter{Type: kinesisapi.ShardFilterTypeAtTimestamp, Timestamp: &before}})
		if len(out.Shards) != 0 {
			h.Errorf("AT_TIMESTAMP before the stream lists %d shards, want none open then", len(out.Shards))
		}
		out = call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{StreamName: new("s"), ShardFilter: &kinesisapi.ShardFilter{Type: kinesisapi.ShardFilterTypeFromTimestamp, Timestamp: &before}})
		if len(out.Shards) != 5 {
			h.Errorf("FROM_TIMESTAMP lists %d shards, want every open shard", len(out.Shards))
		}
	})
}

// ListShards' refusals (contract 3.5; (k) texts kinesalite's, (u)
// understudy's, all UNVERIFIED, KIN-15, ListShards note) and its token
// lifetime: "Tokens expire after 300 seconds" (aws-doc, kinesis.md 4.6),
// on the emulator clock (KIN-11).
func TestListShardsRefusalsAndTokenExpiry(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 3)
		created := h.summary("s").StreamCreationTimestamp
		page := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{StreamName: new("s"), MaxResults: new(int32(1))})
		if page.NextToken == nil {
			h.Fatal("MaxResults 1 of 3 shards gives no NextToken")
		}
		tok := page.NextToken
		list := func(in kinesisapi.ListShardsInput) *awserr.Error {
			h.Helper()
			return fail(h, kinesisapi.OpListShards, &in)
		}
		invalidArg(h, "token and name", list(kinesisapi.ListShardsInput{NextToken: tok, StreamName: new("s")}), "NextToken and StreamName cannot be provided together.")
		invalidArg(h, "token and ARN", list(kinesisapi.ListShardsInput{NextToken: tok, StreamARN: new(arnOf("s"))}), "NextToken and StreamName cannot be provided together.")
		invalidArg(h, "token and timestamp", list(kinesisapi.ListShardsInput{NextToken: tok, StreamCreationTimestamp: &created}), "NextToken and StreamCreationTimestamp cannot be provided together.")
		invalidArg(h, "token and start", list(kinesisapi.ListShardsInput{NextToken: tok, ExclusiveStartShardId: new(shardID(0))}), "NextToken and ExclusiveStartShardId cannot be provided together.")
		invalidArg(h, "token and filter", list(kinesisapi.ListShardsInput{NextToken: tok, ShardFilter: &kinesisapi.ShardFilter{Type: kinesisapi.ShardFilterTypeAtLatest}}), "NextToken and ShardFilter cannot be provided together.")
		invalidArg(h, "neither", list(kinesisapi.ListShardsInput{}), "Either NextToken or StreamName should be provided.")
		invalidArg(h, "AFTER_SHARD_ID without ShardId", list(kinesisapi.ListShardsInput{StreamName: new("s"), ShardFilter: &kinesisapi.ShardFilter{Type: kinesisapi.ShardFilterTypeAfterShardId}}),
			"ShardId must be specified for ShardFilter type AFTER_SHARD_ID.")
		for _, typ := range []kinesisapi.ShardFilterType{kinesisapi.ShardFilterTypeAtTimestamp, kinesisapi.ShardFilterTypeFromTimestamp} {
			invalidArg(h, string(typ)+" without Timestamp", list(kinesisapi.ListShardsInput{StreamName: new("s"), ShardFilter: &kinesisapi.ShardFilter{Type: typ}}),
				"Timestamp must be specified for ShardFilter type "+string(typ)+".")
		}
		invalidArg(h, "malformed token", list(kinesisapi.ListShardsInput{NextToken: new("bm90IGEgdG9rZW4=")}), "Invalid NextToken.")
		other := created.Add(time.Millisecond)
		wantErr(h, "another creation timestamp", list(kinesisapi.ListShardsInput{StreamName: new("s"), StreamCreationTimestamp: &other}), "ResourceNotFoundException", 400, notFound("s", account))
		if out := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{StreamName: new("s"), StreamCreationTimestamp: &created}); len(out.Shards) != 3 {
			h.Errorf("the stream's own creation timestamp lists %d shards", len(out.Shards))
		}

		h.advance(300*time.Second - time.Millisecond)
		call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{NextToken: tok})
		h.advance(time.Millisecond)
		wantErr(h, "token at 300 s", list(kinesisapi.ListShardsInput{NextToken: tok}), "ExpiredNextTokenException", 400,
			"The NextToken has expired: ListShards tokens are valid for 300 seconds.")
	})
}

// ---- ListStreams (kinesis.md 4.5) ----.

// The scope's streams in lexical order (UNVERIFIED, ListStreams note,
// KIN-15), pages of min(Limit or 100, 100) (aws-doc, 4.5) with summaries,
// ExclusiveStartStreamName, and a NextToken only when more follow.
func TestListStreams(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		for _, n := range []string{"c", "a", "d", "b"} {
			h.create(n, 1)
		}
		out := call(h, kinesisapi.OpListStreams, &kinesisapi.ListStreamsInput{})
		if !slices.Equal(out.StreamNames, []string{"a", "b", "c", "d"}) || out.HasMoreStreams || out.NextToken != nil || len(out.StreamSummaries) != 4 {
			h.Errorf("ListStreams = %+v", out)
		}
		s := out.StreamSummaries[0]
		if s.StreamName != "a" || s.StreamARN != arnOf("a") || s.StreamStatus != "ACTIVE" || s.StreamModeDetails == nil || s.StreamModeDetails.StreamMode != kinesisapi.StreamModeProvisioned || s.StreamCreationTimestamp == nil {
			h.Errorf("summary = %+v", s)
		}
		first := call(h, kinesisapi.OpListStreams, &kinesisapi.ListStreamsInput{Limit: new(int32(3))})
		if !slices.Equal(first.StreamNames, []string{"a", "b", "c"}) || !first.HasMoreStreams || first.NextToken == nil {
			h.Fatalf("Limit 3 = %+v", first)
		}
		rest := call(h, kinesisapi.OpListStreams, &kinesisapi.ListStreamsInput{NextToken: first.NextToken})
		if !slices.Equal(rest.StreamNames, []string{"d"}) || rest.HasMoreStreams || rest.NextToken != nil {
			h.Errorf("after the token = %+v", rest)
		}
		after := call(h, kinesisapi.OpListStreams, &kinesisapi.ListStreamsInput{ExclusiveStartStreamName: new("b")})
		if !slices.Equal(after.StreamNames, []string{"c", "d"}) {
			h.Errorf("after b = %v", after.StreamNames)
		}
		// Contract 3.5: the token never expires.
		h.advance(24 * time.Hour)
		if again := call(h, kinesisapi.OpListStreams, &kinesisapi.ListStreamsInput{NextToken: first.NextToken}); !slices.Equal(again.StreamNames, []string{"d"}) {
			h.Errorf("a day-old token = %v", again.StreamNames)
		}
		invalidArg(h, "token and start", fail(h, kinesisapi.OpListStreams, &kinesisapi.ListStreamsInput{NextToken: first.NextToken, ExclusiveStartStreamName: new("a")}),
			"NextToken and ExclusiveStartStreamName cannot be provided together.")
		invalidArg(h, "bad token", fail(h, kinesisapi.OpListStreams, &kinesisapi.ListStreamsInput{NextToken: new("!!")}), "Invalid NextToken.")
	})
	run(t, kinesis.Options{}, func(h *harness) {
		for i := range 101 {
			h.create(fmt.Sprintf("s%03d", i), 1)
		}
		out := call(h, kinesisapi.OpListStreams, &kinesisapi.ListStreamsInput{Limit: new(int32(1000))})
		if len(out.StreamNames) != 100 || !out.HasMoreStreams {
			h.Errorf("Limit 1000 of 101 streams gives %d (more %v), want 100", len(out.StreamNames), out.HasMoreStreams)
		}
	})
}

// ---- Retention (kinesis.md 4.7) ----.

// Increase and Decrease within 24..8760 (aws-doc 4.7); the refusals'
// texts are kinesalite's (k) or their mirror (u), UNVERIFIED (KIN-06,
// retention note); an equal value per kinesis.retention.equal-value.
func TestRetentionPeriod(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		inc := func(n int32) *kinesisapi.IncreaseStreamRetentionPeriodInput {
			return &kinesisapi.IncreaseStreamRetentionPeriodInput{StreamName: new("s"), RetentionPeriodHours: n}
		}
		dec := func(n int32) *kinesisapi.DecreaseStreamRetentionPeriodInput {
			return &kinesisapi.DecreaseStreamRetentionPeriodInput{StreamName: new("s"), RetentionPeriodHours: n}
		}
		// The default switch value accepts the current value, which
		// Terraform sends on every create (kinesis.md finding 3).
		if h.opts.RetentionEqual != kinesis.RetentionEqualAccept {
			h.Fatal("harness default is not accept")
		}
		call(h, kinesisapi.OpIncreaseStreamRetentionPeriod, inc(24))
		call(h, kinesisapi.OpIncreaseStreamRetentionPeriod, inc(48))
		if got := h.summary("s").RetentionPeriodHours; got != 48 {
			h.Errorf("after Increase 48: %d", got)
		}
		call(h, kinesisapi.OpDecreaseStreamRetentionPeriod, dec(48))
		invalidArg(h, "increase below 24", fail(h, kinesisapi.OpIncreaseStreamRetentionPeriod, inc(23)),
			"Minimum allowed retention period is 24 hours. Requested retention period (23 hours) is too short.")
		invalidArg(h, "decrease below 24", fail(h, kinesisapi.OpDecreaseStreamRetentionPeriod, dec(23)),
			"Minimum allowed retention period is 24 hours. Requested retention period (23 hours) is too short.")
		invalidArg(h, "increase above 8760", fail(h, kinesisapi.OpIncreaseStreamRetentionPeriod, inc(8761)),
			"Maximum allowed retention period is 8760 hours. Requested retention period (8761 hours) is too long.")
		invalidArg(h, "increase below current", fail(h, kinesisapi.OpIncreaseStreamRetentionPeriod, inc(25)),
			"Requested retention period (25 hours) for stream s can not be shorter than existing retention period (48 hours). Use DecreaseRetentionPeriod API.")
		invalidArg(h, "decrease above current", fail(h, kinesisapi.OpDecreaseStreamRetentionPeriod, dec(72)),
			"Requested retention period (72 hours) for stream s can not be longer than existing retention period (48 hours). Use IncreaseRetentionPeriod API.")
		call(h, kinesisapi.OpDecreaseStreamRetentionPeriod, dec(24))
		if got := h.summary("s").RetentionPeriodHours; got != 24 {
			h.Errorf("after Decrease 24: %d", got)
		}
		call(h, kinesisapi.OpIncreaseStreamRetentionPeriod, inc(8760))
		wantErr(h, "missing stream", fail(h, kinesisapi.OpIncreaseStreamRetentionPeriod, &kinesisapi.IncreaseStreamRetentionPeriodInput{StreamName: new("nope"), RetentionPeriodHours: 48}),
			"ResourceNotFoundException", 400, notFound("nope", account))
	})
	// Flipped: reject refuses an equal value both ways (contract 3.6 texts).
	run(t, kinesis.Options{RetentionEqual: kinesis.RetentionEqualReject}, func(h *harness) {
		h.create("s", 1)
		invalidArg(h, "equal increase", fail(h, kinesisapi.OpIncreaseStreamRetentionPeriod, &kinesisapi.IncreaseStreamRetentionPeriodInput{StreamName: new("s"), RetentionPeriodHours: 24}),
			"Requested retention period (24 hours) for stream s must be more than the existing retention period (24 hours).")
		invalidArg(h, "equal decrease", fail(h, kinesisapi.OpDecreaseStreamRetentionPeriod, &kinesisapi.DecreaseStreamRetentionPeriodInput{StreamName: new("s"), RetentionPeriodHours: 24}),
			"Requested retention period (24 hours) for stream s must be less than the existing retention period (24 hours).")
	})
}

// ---- Tags (kinesis.md 4.10) ----.

// AddTagsToStream then ListTagsForStream: the recorded {"foo": "bar"} with
// HasMoreTags false (third-party-recorded, golden 2: localstack@8b9a79f
// tests/aws/services/kinesis/test_kinesis.snapshot.json:L49-L57, validated
// 2022-08-25); keys in order, overwritten on merge, paged by Limit after
// ExclusiveStartTagKey with HasMoreTags (kinesis.md 13.6 item 4); absent
// keys ignored on removal (aws-doc 4.10); the resource API sees the same
// tags.
func TestStreamTags(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		call(h, kinesisapi.OpAddTagsToStream, &kinesisapi.AddTagsToStreamInput{StreamName: new("s"), Tags: map[string]string{"foo": "bar"}})
		out := call(h, kinesisapi.OpListTagsForStream, &kinesisapi.ListTagsForStreamInput{StreamName: new("s")})
		if len(out.Tags) != 1 || out.Tags[0].Key != "foo" || deref(out.Tags[0].Value) != "bar" || out.HasMoreTags {
			h.Errorf("ListTagsForStream = %+v", out)
		}
		call(h, kinesisapi.OpAddTagsToStream, &kinesisapi.AddTagsToStreamInput{StreamARN: new(arnOf("s")), Tags: map[string]string{"foo": "baz", "a": "1", "m": "2"}})
		page := call(h, kinesisapi.OpListTagsForStream, &kinesisapi.ListTagsForStreamInput{StreamName: new("s"), Limit: new(int32(2))})
		if got := tagPairs(page.Tags); !slices.Equal(got, []string{"a=1", "foo=baz"}) || !page.HasMoreTags {
			h.Errorf("Limit 2 = %v (more %v)", got, page.HasMoreTags)
		}
		rest := call(h, kinesisapi.OpListTagsForStream, &kinesisapi.ListTagsForStreamInput{StreamName: new("s"), ExclusiveStartTagKey: new("foo")})
		if got := tagPairs(rest.Tags); !slices.Equal(got, []string{"m=2"}) || rest.HasMoreTags {
			h.Errorf("after foo = %v (more %v)", got, rest.HasMoreTags)
		}
		call(h, kinesisapi.OpRemoveTagsFromStream, &kinesisapi.RemoveTagsFromStreamInput{StreamName: new("s"), TagKeys: []string{"m", "absent"}})
		res := call(h, kinesisapi.OpListTagsForResource, &kinesisapi.ListTagsForResourceInput{ResourceARN: arnOf("s")})
		if got := tagPairs(res.Tags); !slices.Equal(got, []string{"a=1", "foo=baz"}) {
			h.Errorf("ListTagsForResource = %v", got)
		}
		call(h, kinesisapi.OpTagResource, &kinesisapi.TagResourceInput{ResourceARN: arnOf("s"), Tags: map[string]string{"z": "9"}})
		call(h, kinesisapi.OpUntagResource, &kinesisapi.UntagResourceInput{ResourceARN: arnOf("s"), TagKeys: []string{"a"}})
		if got := tagPairs(call(h, kinesisapi.OpListTagsForStream, &kinesisapi.ListTagsForStreamInput{StreamName: new("s")}).Tags); !slices.Equal(got, []string{"foo=baz", "z=9"}) {
			h.Errorf("after TagResource and UntagResource = %v", got)
		}
	})
}

func tagPairs(ts []kinesisapi.Tag) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Key+"="+fmt.Sprint(deref(t.Value)))
	}
	return out
}

// 50 tags per stream and no aws: key (aws-doc, kinesis.md 4.10); the
// refusal is InvalidArgumentException with understudy's text (inferred,
// KIN-07; tag note), nothing changes on refusal, and CreateStream checks
// its Tags the same way (contract 3.5). A consumer or channel ARN answers
// 501 (contract 3.7); another ARN is refused.
func TestStreamTagLimits(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		fifty := map[string]string{}
		for i := range 50 {
			fifty[fmt.Sprintf("k%02d", i)] = "v"
		}
		call(h, kinesisapi.OpAddTagsToStream, &kinesisapi.AddTagsToStreamInput{StreamName: new("s"), Tags: fifty})
		call(h, kinesisapi.OpAddTagsToStream, &kinesisapi.AddTagsToStreamInput{StreamName: new("s"), Tags: map[string]string{"k00": "overwrite"}})
		tooMany := "Failed to add tags to stream s under account " + account + " because a given stream cannot have more than 50 tags."
		invalidArg(h, "51st tag", fail(h, kinesisapi.OpAddTagsToStream, &kinesisapi.AddTagsToStreamInput{StreamName: new("s"), Tags: map[string]string{"k50": "v"}}), tooMany)
		invalidArg(h, "51st tag by TagResource", fail(h, kinesisapi.OpTagResource, &kinesisapi.TagResourceInput{ResourceARN: arnOf("s"), Tags: map[string]string{"k50": "v"}}), tooMany)
		for _, key := range []string{"aws:x", "AWS:x", "Aws:Name"} {
			invalidArg(h, key, fail(h, kinesisapi.OpAddTagsToStream, &kinesisapi.AddTagsToStreamInput{StreamName: new("s"), Tags: map[string]string{key: "v"}}),
				"Tag key "+key+" uses the reserved prefix aws:.")
		}
		res := call(h, kinesisapi.OpListTagsForResource, &kinesisapi.ListTagsForResourceInput{ResourceARN: arnOf("s")})
		if len(res.Tags) != 50 {
			h.Errorf("%d tags after the refusals, want 50", len(res.Tags))
		}
		invalidArg(h, "CreateStream aws: tag", fail(h, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{StreamName: "t", ShardCount: new(int32(1)), Tags: map[string]string{"aws:x": "v"}}),
			"Tag key aws:x uses the reserved prefix aws:.")
		consumer := arnOf("s") + "/consumer/c:1540591608"
		wantNotImplemented(h, "consumer ARN", fail(h, kinesisapi.OpTagResource, &kinesisapi.TagResourceInput{ResourceARN: consumer, Tags: map[string]string{"a": "b"}}), "tags on Kinesis consumers and channels", "TagResource")
		// The value fails the model's ResourceARN pattern, so the generated
		// validator refuses it with ValidationException before any hand
		// check (kinesis.md 13.3; identity UNVERIFIED, KIN-07).
		other := "arn:aws:kinesis:us-east-1:" + account + ":thing/x"
		wantErr(h, "other ARN", fail(h, kinesisapi.OpTagResource, &kinesisapi.TagResourceInput{ResourceARN: other, Tags: map[string]string{"a": "b"}}), "ValidationException", 400,
			"1 validation error detected: Value '"+other+"' at 'resourceARN' failed to satisfy constraint: Member must satisfy regular expression pattern: arn:aws.*:kinesis:.*:\\d{12}:.*(stream|channel)/\\S+")
	})
}

// ---- DescribeLimits, enhanced monitoring, delete ----.

// DescribeLimits reports the fixed quotas and the scope's counts (contract
// 3.5; DescribeLimits note: "ShardLimit is 20000 and
// OnDemandStreamCountLimit 50 in every region", the documented us-east-1
// defaults, aws-doc kinesis.md 4.1), and no channel members.
func TestDescribeLimits(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("p", 3)
		call(h, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{StreamName: "od"})
		out := call(h, kinesisapi.OpDescribeLimits, &kinesisapi.DescribeLimitsInput{})
		if out.ShardLimit != 20000 || out.OnDemandStreamCountLimit != 50 || out.OpenShardCount != 7 || out.OnDemandStreamCount != 1 || out.ChannelCount != nil || out.ChannelCountLimit != nil {
			h.Errorf("DescribeLimits = %+v", out)
		}
	})
}

// Enhanced monitoring is stored and reported, nothing published
// (registry.StoredOnly; contract 3.5): ALL stands for the seven names, the
// output has the metrics before and after in enum order (model MetricsName,
// kinesis.md 4.4).
func TestEnhancedMonitoringStored(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		on := call(h, kinesisapi.OpEnableEnhancedMonitoring, &kinesisapi.EnableEnhancedMonitoringInput{
			StreamName:        new("s"),
			ShardLevelMetrics: []kinesisapi.MetricsName{kinesisapi.MetricsNameOutgoingBytes, kinesisapi.MetricsNameIncomingBytes},
		})
		if deref(on.StreamName) != "s" || deref(on.StreamARN) != arnOf("s") || len(on.CurrentShardLevelMetrics) != 0 ||
			!slices.Equal(on.DesiredShardLevelMetrics, []kinesisapi.MetricsName{kinesisapi.MetricsNameIncomingBytes, kinesisapi.MetricsNameOutgoingBytes}) {
			h.Errorf("Enable = %+v", on)
		}
		if got := h.summary("s").EnhancedMonitoring; len(got) != 1 || !slices.Equal(got[0].ShardLevelMetrics, on.DesiredShardLevelMetrics) {
			h.Errorf("summary EnhancedMonitoring = %+v", got)
		}
		all := call(h, kinesisapi.OpEnableEnhancedMonitoring, &kinesisapi.EnableEnhancedMonitoringInput{StreamName: new("s"), ShardLevelMetrics: []kinesisapi.MetricsName{kinesisapi.MetricsNameAll}})
		seven := []kinesisapi.MetricsName{
			kinesisapi.MetricsNameIncomingBytes, kinesisapi.MetricsNameIncomingRecords, kinesisapi.MetricsNameOutgoingBytes, kinesisapi.MetricsNameOutgoingRecords,
			kinesisapi.MetricsNameWriteProvisionedThroughputExceeded, kinesisapi.MetricsNameReadProvisionedThroughputExceeded, kinesisapi.MetricsNameIteratorAgeMilliseconds,
		}
		if !slices.Equal(all.DesiredShardLevelMetrics, seven) {
			h.Errorf("Enable ALL desired = %v, want the seven names", all.DesiredShardLevelMetrics)
		}
		off := call(h, kinesisapi.OpDisableEnhancedMonitoring, &kinesisapi.DisableEnhancedMonitoringInput{StreamName: new("s"), ShardLevelMetrics: []kinesisapi.MetricsName{kinesisapi.MetricsNameAll}})
		if !slices.Equal(off.CurrentShardLevelMetrics, seven) || len(off.DesiredShardLevelMetrics) != 0 {
			h.Errorf("Disable ALL = %+v", off)
		}
		if got := h.describe("s").EnhancedMonitoring; len(got) != 1 || len(got[0].ShardLevelMetrics) != 0 {
			h.Errorf("DescribeStream EnhancedMonitoring = %+v", got)
		}
	})
}

// DeleteStream removes the stream at once (contract 0; DeleteStream note):
// DescribeStreamSummary then answers the not-found text (kinesis.md 13.6
// item 6), and the name can be used again.
func TestDeleteStream(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		call(h, kinesisapi.OpAddTagsToStream, &kinesisapi.AddTagsToStreamInput{StreamName: new("s"), Tags: map[string]string{"a": "b"}})
		call(h, kinesisapi.OpDeleteStream, &kinesisapi.DeleteStreamInput{StreamName: new("s")})
		wantErr(h, "summary after delete", fail(h, kinesisapi.OpDescribeStreamSummary, &kinesisapi.DescribeStreamSummaryInput{StreamName: new("s")}),
			"ResourceNotFoundException", 400, notFound("s", account))
		wantErr(h, "delete again", fail(h, kinesisapi.OpDeleteStream, &kinesisapi.DeleteStreamInput{StreamARN: new(arnOf("s"))}),
			"ResourceNotFoundException", 400, notFound("s", account))
		h.create("s", 1)
		if out := call(h, kinesisapi.OpListTagsForStream, &kinesisapi.ListTagsForStreamInput{StreamName: new("s")}); len(out.Tags) != 0 {
			h.Errorf("a re-created stream has the old tags %v", tagPairs(out.Tags))
		}
	})
}

// ---- Stream identification (contract 3.3) ----.

// Every stream-scoped input names its stream by StreamName, StreamARN or
// both ("you must use either the StreamARN or the StreamName parameter, or
// both", aws-doc kinesis.md 3.1); the refusals are understudy's texts
// (UNVERIFIED, KIN-08); an ARN of another account or region is not found,
// named by the ARN's name and account (kinesis.md finding 5, KIN-05).
// StreamId is ignored (kinesis.md 3.1, "Not Implemented. Reserved").
func TestStreamIdentification(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		h.create("t", 1)
		sum := func(in kinesisapi.DescribeStreamSummaryInput) *awserr.Error {
			h.Helper()
			return fail(h, kinesisapi.OpDescribeStreamSummary, &in)
		}
		invalidArg(h, "neither", sum(kinesisapi.DescribeStreamSummaryInput{}), "Either StreamName or StreamARN must be provided.")
		// The value fails the model's StreamARN pattern, so the generated
		// validator refuses it with ValidationException before any hand
		// check (kinesis.md 13.3; identity UNVERIFIED, KIN-07).
		bad := "arn:aws:kinesis:us-east-1:" + account + ":table/s"
		wantErr(h, "not a stream ARN", sum(kinesisapi.DescribeStreamSummaryInput{StreamARN: new(bad)}), "ValidationException", 400,
			"1 validation error detected: Value '"+bad+"' at 'streamARN' failed to satisfy constraint: Member must satisfy regular expression pattern: arn:aws.*:kinesis:.*:\\d{12}:stream/\\S+")
		invalidArg(h, "disagree", sum(kinesisapi.DescribeStreamSummaryInput{StreamName: new("s"), StreamARN: new(arnOf("t"))}),
			"StreamName s and StreamARN "+arnOf("t")+" do not name the same stream.")
		otherAccount := "arn:aws:kinesis:us-east-1:111111111111:stream/s"
		wantErr(h, "another account", sum(kinesisapi.DescribeStreamSummaryInput{StreamARN: new(otherAccount)}), "ResourceNotFoundException", 400, notFound("s", "111111111111"))
		otherRegion := "arn:aws:kinesis:eu-west-1:" + account + ":stream/s"
		wantErr(h, "another region", sum(kinesisapi.DescribeStreamSummaryInput{StreamARN: new(otherRegion)}), "ResourceNotFoundException", 400, notFound("s", account))
		wantErr(h, "absent by ARN", sum(kinesisapi.DescribeStreamSummaryInput{StreamARN: new(arnOf("nope"))}), "ResourceNotFoundException", 400, notFound("nope", account))
		for _, in := range []kinesisapi.DescribeStreamSummaryInput{
			{StreamARN: new(arnOf("s"))},
			{StreamName: new("s"), StreamARN: new(arnOf("s"))},
			{StreamName: new("s"), StreamId: new("abcdefghij0123456789-abc")},
		} {
			if got := call(h, kinesisapi.OpDescribeStreamSummary, &in).StreamDescriptionSummary; got.StreamName != "s" || got.StreamId != nil {
				h.Errorf("%+v resolves to %s (StreamId %v)", in, got.StreamName, deref(got.StreamId))
			}
		}
	})
}

// The 24 operations outside slice 1 stay unbound and answer 501 (spec
// Scope 8b; invariant 3).
func TestUnboundOperationsAnswer501(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 2)
		errs := map[string]*awserr.Error{
			"SplitShard":          fail(h, kinesisapi.OpSplitShard, &kinesisapi.SplitShardInput{StreamName: new("s"), ShardToSplit: shardID(0), NewStartingHashKey: "1"}),
			"UpdateShardCount":    fail(h, kinesisapi.OpUpdateShardCount, &kinesisapi.UpdateShardCountInput{StreamName: new("s"), TargetShardCount: 4, ScalingType: "UNIFORM_SCALING"}),
			"ListStreamConsumers": fail(h, kinesisapi.OpListStreamConsumers, &kinesisapi.ListStreamConsumersInput{StreamARN: arnOf("s")}),
			"GetResourcePolicy":   fail(h, kinesisapi.OpGetResourcePolicy, &kinesisapi.GetResourcePolicyInput{ResourceARN: arnOf("s")}),
		}
		for op, err := range errs {
			if err.ID.JSONType != "NotImplemented" || err.ID.Status != 501 {
				h.Errorf("%s = %s %d, want 501 NotImplemented", op, err.ID.JSONType, err.ID.Status)
			}
		}
	})
}

// ---- Switches, resources (contract 3.1, 3.8) ----.

// Switches lists the five switches in contract order, each inferred with
// Ref, Record, Ops and NoScenario, reporting the value the service was
// built with (spec Fidelity switches; contract 3.8).
func TestSwitches(t *testing.T) {
	flipped := kinesis.Options{
		EvenSplit: kinesis.SplitFloorI, HashByteOrder: kinesis.HashLittleEndianUTF8, SequenceLayout: kinesis.LayoutDecimal56,
		MillisBehind: kinesis.BehindTipLastReturned, RetentionEqual: kinesis.RetentionEqualReject,
	}
	for _, tc := range []struct {
		o      kinesis.Options
		values []string
	}{
		{kinesis.Options{}, []string{"i*floor(2^128/N)", "big-endian-utf8", "v2-56-digit", "now-minus-next-unread-arrival", "accept"}},
		{flipped, []string{"floor(i*2^128/N)", "little-endian-utf8", "decimal-56", "tip-minus-last-returned-arrival", "reject"}},
	} {
		run(t, tc.o, func(h *harness) {
			want := []struct {
				name, ref, record string
				ops               []string
			}{
				{"kinesis.hash.even-split", "docs/research/kinesis.md#33-hash-key-space-and-the-even-split", "KIN-01", []string{"kinesis.CreateStream", "kinesis.DescribeStream", "kinesis.ListShards"}},
				{"kinesis.hash.md5-byte-order", "docs/research/kinesis.md#53-shard-selection", "KIN-13", []string{"kinesis.PutRecord", "kinesis.PutRecords"}},
				{"kinesis.sequence.layout", "docs/research/kinesis.md#34-sequence-numbers", "KIN-09, KIN-16", []string{"kinesis.PutRecord", "kinesis.PutRecords", "kinesis.GetRecords", "kinesis.DescribeStream", "kinesis.ListShards"}},
				{"kinesis.get-records.millis-behind-latest", "docs/research/kinesis.md#510-approximatearrivaltimestamp-and-millisbehindlatest", "KIN-10", []string{"kinesis.GetRecords"}},
				{"kinesis.retention.equal-value", "docs/research/kinesis.md#47-retention-period", "KIN-06", []string{"kinesis.IncreaseStreamRetentionPeriod", "kinesis.DecreaseStreamRetentionPeriod"}},
			}
			got := h.svc.Switches()
			if len(got) != len(want) {
				h.Fatalf("Switches lists %d, want %d: %+v", len(got), len(want), got)
			}
			for i, w := range want {
				e := got[i]
				ns, isNS := e.Settles.(fidelity.NoScenario)
				if e.Name != w.name || e.Value != tc.values[i] || e.Label != evidence.Inferred || e.Ref != w.ref || e.Record != w.record || !slices.Equal(e.Ops, w.ops) ||
					!isNS || ns.Reason != "unrecorded: "+w.record+" is queued" {
					h.Errorf("switch %d = %+v, want %s = %s %+v", i, e, w.name, tc.values[i], w)
				}
			}
		})
	}
}

// Resources lists one entry per stream in name order, Summary ACTIVE, and
// Scopes names the scopes holding streams (contract 3.1, 3.9: `understudy
// resources` lists streams).
func TestResourcesAndScopes(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		if len(h.svc.Scopes()) != 0 {
			h.Errorf("Scopes before any stream = %v", h.svc.Scopes())
		}
		h.create("b", 1)
		h.create("a", 1)
		rs := h.svc.Resources(h.Scope)
		if len(rs) != 2 || rs[0].ARN.String() != arnOf("a") || rs[1].ARN.String() != arnOf("b") || rs[0].Summary != "ACTIVE" {
			h.Errorf("Resources = %+v", rs)
		}
		if got := h.svc.Scopes(); len(got) != 1 || got[0] != h.Scope {
			h.Errorf("Scopes = %v, want [%v]", got, h.Scope)
		}
		var euWest aws.Region
		if err := euWest.UnmarshalText([]byte("eu-west-1")); err != nil {
			h.Fatal(err)
		}
		other := aws.Scope{Partition: h.Scope.Partition, Account: h.Scope.Account, Region: euWest}
		if got := h.svc.Resources(other); len(got) != 0 {
			h.Errorf("Resources(%v) = %+v", other, got)
		}
		if h.svc.Name() != "kinesis" || h.svc.SchemaVersion() != 1 {
			h.Errorf("Name, SchemaVersion = %s, %d", h.svc.Name(), h.svc.SchemaVersion())
		}
	})
}
