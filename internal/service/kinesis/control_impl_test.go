package kinesis_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/kinesis"
)

// CreateStream refuses a stream that would take the scope's shards above
// the 20,000 shard quota with LimitExceededException (aws-doc kinesis.md
// 4.1; understudy's text, UNVERIFIED, KIN-23), counting ON_DEMAND streams'
// four shards; a stream that reaches the quota exactly is accepted.
func TestCreateStreamShardLimit(t *testing.T) {
	limit := func(open, adding int) string {
		return fmt.Sprintf("This request would exceed the shard limit for the account %s in us-east-1. Current shard count for the account: %d. Limit: 20000. Number of additional shards that would have resulted from this request: %d.", account, open, adding)
	}
	run(t, kinesis.Options{}, func(h *harness) {
		wantErr(h, "one request above the quota", fail(h, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{StreamName: "huge", ShardCount: new(int32(2_000_000_000))}),
			"LimitExceededException", 400, limit(0, 2_000_000_000))
		h.create("s", 19_997)
		wantErr(h, "ON_DEMAND past the quota", fail(h, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{StreamName: "od"}),
			"LimitExceededException", 400, limit(19_997, 4))
		h.create("t", 3)
		if got := call(h, kinesisapi.OpDescribeLimits, &kinesisapi.DescribeLimitsInput{}).OpenShardCount; got != 20_000 {
			h.Errorf("OpenShardCount = %d, want 20000", got)
		}
		wantErr(h, "one more shard", fail(h, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{StreamName: "u", ShardCount: new(int32(1))}),
			"LimitExceededException", 400, limit(20_000, 1))
	})
}

// CreateStream tags are listed by ListTagsForStream (kinesis.md 10.3: the
// tags Terraform reads include CreateStream's) and checked as
// AddTagsToStream checks them: more than 50 is refused with the tag text
// (contract 3.5; understudy's text, UNVERIFIED, KIN-07) and nothing is
// created.
func TestCreateStreamTags(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		call(h, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{StreamName: "s", ShardCount: new(int32(1)), Tags: map[string]string{"b": "2", "a": "1"}})
		out := call(h, kinesisapi.OpListTagsForStream, &kinesisapi.ListTagsForStreamInput{StreamName: new("s")})
		if got := tagPairs(out.Tags); !slices.Equal(got, []string{"a=1", "b=2"}) || out.HasMoreTags {
			h.Errorf("ListTagsForStream after CreateStream with tags = %v (more %v)", got, out.HasMoreTags)
		}
		many := map[string]string{}
		for i := range 51 {
			many[fmt.Sprintf("k%02d", i)] = "v"
		}
		invalidArg(h, "51 tags", fail(h, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{StreamName: "t", ShardCount: new(int32(1)), Tags: many}),
			"Failed to add tags to stream t under account "+account+" because a given stream cannot have more than 50 tags.")
		wantErr(h, "refused stream", fail(h, kinesisapi.OpDescribeStreamSummary, &kinesisapi.DescribeStreamSummaryInput{StreamName: new("t")}),
			"ResourceNotFoundException", 400, notFound("t", account))
	})
}

// DescribeStream starts from the first shard when ExclusiveStartShardId is
// not one of the stream's (contract 3.5).
func TestDescribeStreamUnknownStartShard(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 3)
		page := call(h, kinesisapi.OpDescribeStream, &kinesisapi.DescribeStreamInput{StreamName: new("s"), ExclusiveStartShardId: new(shardID(9))}).StreamDescription
		if got := shardIDs(page.Shards); !slices.Equal(got, []string{shardID(0), shardID(1), shardID(2)}) || page.HasMoreShards {
			h.Errorf("DescribeStream after an unknown shard = %v (more %v)", got, page.HasMoreShards)
		}
	})
}

// Writes addressed by an ARN of another account are not found, named by
// the ARN's account, and change nothing (contract 3.3; kinesis.md finding
// 5, KIN-05).
func TestForeignARNWritesAreNotFound(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		other := "arn:aws:kinesis:us-east-1:111111111111:stream/s"
		wantErr(h, "increase", fail(h, kinesisapi.OpIncreaseStreamRetentionPeriod, &kinesisapi.IncreaseStreamRetentionPeriodInput{StreamARN: new(other), RetentionPeriodHours: 48}),
			"ResourceNotFoundException", 400, notFound("s", "111111111111"))
		wantErr(h, "delete", fail(h, kinesisapi.OpDeleteStream, &kinesisapi.DeleteStreamInput{StreamARN: new(other)}),
			"ResourceNotFoundException", 400, notFound("s", "111111111111"))
		wantErr(h, "tag", fail(h, kinesisapi.OpTagResource, &kinesisapi.TagResourceInput{ResourceARN: other, Tags: map[string]string{"a": "b"}}),
			"ResourceNotFoundException", 400, notFound("s", "111111111111"))
		if s := h.summary("s"); s.RetentionPeriodHours != 24 {
			h.Errorf("retention after the refused increase = %d, want 24", s.RetentionPeriodHours)
		}
	})
}

// An ARN the model's pattern admits but that names no stream is refused by
// the hand checks (contract 3.3; understudy's texts, KIN-08, KIN-07); one
// the pattern refuses gets the generated ValidationException (contract 0).
func TestARNsThatNameNoStream(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		nested := arnOf("s") + "/x"
		invalidArg(h, "nested stream ARN", fail(h, kinesisapi.OpDescribeStreamSummary, &kinesisapi.DescribeStreamSummaryInput{StreamARN: new(nested)}), "Invalid StreamARN "+nested+".")
		other := "arn:aws:kinesis:us-east-1:" + account + ":xstream/x"
		invalidArg(h, "other resource", fail(h, kinesisapi.OpTagResource, &kinesisapi.TagResourceInput{ResourceARN: other, Tags: map[string]string{"a": "b"}}), "Invalid ResourceARN "+other+".")
		table := "arn:aws:kinesis:us-east-1:" + account + ":table/s"
		if err := fail(h, kinesisapi.OpDescribeStreamSummary, &kinesisapi.DescribeStreamSummaryInput{StreamARN: new(table)}); err.ID.JSONType != "ValidationException" || err.ID.Status != 400 {
			h.Errorf("pattern-refused StreamARN = %s %d %q, want ValidationException 400", err.ID.JSONType, err.ID.Status, err.Message)
		}
	})
}

// Export and Import carry the streams; Reset drops them, and a reset scope
// lists no stream (kernel.Stateful; contract 3.9 for the stream fields).
func TestStreamsSurviveExportAndReset(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		call(h, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{StreamName: "s", ShardCount: new(int32(3)), Tags: map[string]string{"a": "1"}})
		call(h, kinesisapi.OpIncreaseStreamRetentionPeriod, &kinesisapi.IncreaseStreamRetentionPeriodInput{StreamName: new("s"), RetentionPeriodHours: 48})
		before := h.describe("s")
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if got := h.svc.Scopes(); len(got) != 0 {
			h.Fatalf("Scopes after Reset = %v", got)
		}
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		after := h.describe("s")
		if after.RetentionPeriodHours != 48 || !slices.Equal(shardIDs(after.Shards), shardIDs(before.Shards)) ||
			after.Shards[2].HashKeyRange != before.Shards[2].HashKeyRange || !after.StreamCreationTimestamp.Equal(before.StreamCreationTimestamp) {
			h.Errorf("after Import = %+v\nbefore %+v", after, before)
		}
		if got := tagPairs(call(h, kinesisapi.OpListTagsForStream, &kinesisapi.ListTagsForStreamInput{StreamName: new("s")}).Tags); !slices.Equal(got, []string{"a=1"}) {
			h.Errorf("tags after Import = %v", got)
		}
	})
}
