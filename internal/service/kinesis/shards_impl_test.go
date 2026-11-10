package kinesis_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/service/kinesis"
)

// A ListShards NextToken resumes the listing it came from: by MaxResults
// on the stream named by ARN, and the later of ExclusiveStartShardId and
// AFTER_SHARD_ID's ShardId starts it.
func TestListShardsTokenResumesItsListing(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 6)
		first := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{
			StreamARN:             new(arnOf("s")),
			ExclusiveStartShardId: new(shardID(3)),
			ShardFilter:           &kinesisapi.ShardFilter{Type: kinesisapi.ShardFilterTypeAfterShardId, ShardId: new(shardID(0))},
			MaxResults:            new(int32(1)),
		})
		if got := shardIDs(first.Shards); !slices.Equal(got, []string{shardID(4)}) || first.NextToken == nil {
			h.Fatalf("first page = %v, token %v", got, deref(first.NextToken))
		}
		rest := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{NextToken: first.NextToken})
		if got := shardIDs(rest.Shards); !slices.Equal(got, []string{shardID(5)}) || rest.NextToken != nil {
			h.Errorf("resumed page = %v, token %v", got, deref(rest.NextToken))
		}
		exact := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{StreamName: new("s"), MaxResults: new(int32(6))})
		if len(exact.Shards) != 6 || exact.NextToken != nil {
			h.Errorf("MaxResults equal to the shard count = %d shards, token %v; want all and no token", len(exact.Shards), deref(exact.NextToken))
		}
	})
}

// A token names its stream by creation instant: once the stream is deleted,
// or deleted and re-created under the same name, the token's stream is not
// found (a token carries stream and creation instant).
func TestListShardsTokenOfAGoneStream(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 2)
		page := call(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{StreamName: new("s"), MaxResults: new(int32(1))})
		if page.NextToken == nil {
			h.Fatal("MaxResults 1 of 2 shards gives no NextToken")
		}
		call(h, kinesisapi.OpDeleteStream, &kinesisapi.DeleteStreamInput{StreamName: new("s")})
		list := func() {
			h.Helper()
			wantErr(h, "token of a gone stream", fail(h, kinesisapi.OpListShards, &kinesisapi.ListShardsInput{NextToken: page.NextToken}),
				"ResourceNotFoundException", 400, notFound("s", account))
		}
		list()
		h.advance(time.Millisecond)
		h.create("s", 2)
		list()
	})
}

// Enable adds to the stored metrics and Disable removes only the named
// ones; both report before and after in enum order.
func TestEnhancedMonitoringMergesAndRemoves(t *testing.T) {
	run(t, kinesis.Options{}, func(h *harness) {
		h.create("s", 1)
		call(h, kinesisapi.OpEnableEnhancedMonitoring, &kinesisapi.EnableEnhancedMonitoringInput{StreamName: new("s"), ShardLevelMetrics: []kinesisapi.MetricsName{kinesisapi.MetricsNameIteratorAgeMilliseconds}})
		on := call(h, kinesisapi.OpEnableEnhancedMonitoring, &kinesisapi.EnableEnhancedMonitoringInput{StreamARN: new(arnOf("s")), ShardLevelMetrics: []kinesisapi.MetricsName{kinesisapi.MetricsNameIncomingRecords}})
		both := []kinesisapi.MetricsName{kinesisapi.MetricsNameIncomingRecords, kinesisapi.MetricsNameIteratorAgeMilliseconds}
		if !slices.Equal(on.CurrentShardLevelMetrics, []kinesisapi.MetricsName{kinesisapi.MetricsNameIteratorAgeMilliseconds}) || !slices.Equal(on.DesiredShardLevelMetrics, both) {
			h.Errorf("second Enable = %+v", on)
		}
		off := call(h, kinesisapi.OpDisableEnhancedMonitoring, &kinesisapi.DisableEnhancedMonitoringInput{StreamName: new("s"), ShardLevelMetrics: []kinesisapi.MetricsName{kinesisapi.MetricsNameIteratorAgeMilliseconds, kinesisapi.MetricsNameOutgoingBytes}})
		if !slices.Equal(off.CurrentShardLevelMetrics, both) || !slices.Equal(off.DesiredShardLevelMetrics, []kinesisapi.MetricsName{kinesisapi.MetricsNameIncomingRecords}) {
			h.Errorf("Disable = %+v", off)
		}
		if got := h.describe("s").EnhancedMonitoring; len(got) != 1 || !slices.Equal(got[0].ShardLevelMetrics, off.DesiredShardLevelMetrics) {
			h.Errorf("DescribeStream EnhancedMonitoring = %+v", got)
		}
		wantErr(h, "absent stream", fail(h, kinesisapi.OpEnableEnhancedMonitoring, &kinesisapi.EnableEnhancedMonitoringInput{StreamName: new("x"), ShardLevelMetrics: []kinesisapi.MetricsName{kinesisapi.MetricsNameAll}}),
			"ResourceNotFoundException", 400, notFound("x", account))
	})
}
