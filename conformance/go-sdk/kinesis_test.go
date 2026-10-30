package gosdk_test

import (
	"bytes"
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	"github.com/aws/aws-sdk-go-v2/service/kinesis/types"
)

// Research: docs/research/kinesis.md 3-6, 10 and 13.

// createKinesisStream creates a stream (PROVISIONED with shards shards, or
// with only its name when shards is 0), waits until it is ACTIVE and deletes
// it when the test ends.
func createKinesisStream(t *testing.T, streamName string, shards int32) {
	t.Helper()
	ctx := t.Context()
	client := kinesis.NewFromConfig(awsCfg)
	in := &kinesis.CreateStreamInput{StreamName: aws.String(streamName)}
	if shards > 0 {
		in.ShardCount = aws.Int32(shards)
	}
	_, err := client.CreateStream(ctx, in)
	ok(t, err, "CreateStream "+streamName)
	t.Cleanup(func() {
		_, _ = client.DeleteStream(context.Background(), &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	})
	// kinesis.md 4.2: the model's StreamExists waiter.
	err = kinesis.NewStreamExistsWaiter(client).Wait(ctx, &kinesis.DescribeStreamInput{StreamName: aws.String(streamName)}, 2*time.Minute)
	ok(t, err, "waiting for "+streamName+" to be ACTIVE")
}

func TestKinesisStreams(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := kinesis.NewFromConfig(awsCfg)
	streamName := name(t, "kin")
	createKinesisStream(t, streamName, 3)
	streamArn := "arn:aws:kinesis:" + region() + ":" + account(t) + ":stream/" + streamName

	// kinesis.md 3.1 (ARN form), 4.4 (required members), 4.7 (24 hours by
	// default), 10.3 (PROVISIONED for a config with only a shard count).
	sum, err := client.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{StreamName: aws.String(streamName)})
	ok(t, err, "DescribeStreamSummary")
	s := sum.StreamDescriptionSummary
	if aws.ToString(s.StreamARN) != streamArn || s.StreamStatus != types.StreamStatusActive || aws.ToInt32(s.RetentionPeriodHours) != 24 ||
		aws.ToInt32(s.OpenShardCount) != 3 || s.StreamModeDetails == nil || s.StreamModeDetails.StreamMode != types.StreamModeProvisioned ||
		s.StreamCreationTimestamp == nil || len(s.EnhancedMonitoring) == 0 {
		t.Errorf("DescribeStreamSummary = %+v", s)
	}

	// kinesis.md 3.3: the documented 3-shard stream's ranges; 3.2: ids from 0.
	shards, err := client.ListShards(ctx, &kinesis.ListShardsInput{StreamName: aws.String(streamName)})
	ok(t, err, "ListShards")
	want := [][3]string{
		{"shardId-000000000000", "0", "113427455640312821154458202477256070484"},
		{"shardId-000000000001", "113427455640312821154458202477256070485", "226854911280625642308916404954512140969"},
		{"shardId-000000000002", "226854911280625642308916404954512140970", "340282366920938463463374607431768211455"},
	}
	if len(shards.Shards) != len(want) {
		t.Fatalf("ListShards returned %d shards, want 3", len(shards.Shards))
	}
	for i, sh := range shards.Shards {
		got := [3]string{aws.ToString(sh.ShardId), aws.ToString(sh.HashKeyRange.StartingHashKey), aws.ToString(sh.HashKeyRange.EndingHashKey)}
		if got != want[i] || sh.SequenceNumberRange.EndingSequenceNumber != nil {
			t.Errorf("shard %d = %v, ending %v; want %v and open", i, got, sh.SequenceNumberRange.EndingSequenceNumber, want[i])
		}
	}

	// kinesis.md 4.10 (aws-recorded, LS test_kinesis.snapshot.json:L49-L57).
	_, err = client.AddTagsToStream(ctx, &kinesis.AddTagsToStreamInput{StreamName: aws.String(streamName), Tags: map[string]string{"foo": "bar"}})
	ok(t, err, "AddTagsToStream")
	tags, err := client.ListTagsForStream(ctx, &kinesis.ListTagsForStreamInput{StreamName: aws.String(streamName)})
	ok(t, err, "ListTagsForStream")
	if len(tags.Tags) != 1 || aws.ToString(tags.Tags[0].Key) != "foo" || aws.ToString(tags.Tags[0].Value) != "bar" || aws.ToBool(tags.HasMoreTags) {
		t.Errorf("ListTagsForStream = %+v, HasMoreTags %v", tags.Tags, aws.ToBool(tags.HasMoreTags))
	}

	// kinesis.md 5.3: ExplicitHashKey overrides the partition key, so the
	// first key of shard 2 lands there.
	put, err := client.PutRecord(ctx, &kinesis.PutRecordInput{
		StreamName: aws.String(streamName), PartitionKey: aws.String("k1"), Data: []byte("hello"),
		ExplicitHashKey: aws.String(want[2][1]),
	})
	ok(t, err, "PutRecord")
	if aws.ToString(put.ShardId) != "shardId-000000000002" {
		t.Errorf("PutRecord with ExplicitHashKey %s went to %s, want shardId-000000000002", want[2][1], aws.ToString(put.ShardId))
	}
	// kinesis.md 5.2: one result per entry in request order; empty data is
	// legal (aws-recorded, LS test_kinesis.snapshot.json:L58-L67).
	puts, err := client.PutRecords(ctx, &kinesis.PutRecordsInput{StreamName: aws.String(streamName), Records: []types.PutRecordsRequestEntry{
		{PartitionKey: aws.String("k2"), Data: []byte{}, ExplicitHashKey: aws.String(want[2][1])},
		{PartitionKey: aws.String("k3"), Data: []byte("three"), ExplicitHashKey: aws.String(want[2][2])},
	}})
	ok(t, err, "PutRecords")
	if aws.ToInt32(puts.FailedRecordCount) != 0 || len(puts.Records) != 2 {
		t.Fatalf("PutRecords = FailedRecordCount %d, %d results", aws.ToInt32(puts.FailedRecordCount), len(puts.Records))
	}

	it, err := client.GetShardIterator(ctx, &kinesis.GetShardIteratorInput{
		StreamName: aws.String(streamName), ShardId: aws.String("shardId-000000000002"), ShardIteratorType: types.ShardIteratorTypeTrimHorizon,
	})
	ok(t, err, "GetShardIterator TRIM_HORIZON")
	page, err := client.GetRecords(ctx, &kinesis.GetRecordsInput{ShardIterator: it.ShardIterator})
	ok(t, err, "GetRecords")
	if len(page.Records) != 3 {
		t.Fatalf("GetRecords returned %d records, want 3", len(page.Records))
	}
	// kinesis.md 5.5: an open shard always answers a NextShardIterator.
	if page.NextShardIterator == nil {
		t.Error("GetRecords on an open shard returned no NextShardIterator")
	}
	wantData := [][]byte{[]byte("hello"), {}, []byte("three")}
	wantSeq := []string{aws.ToString(put.SequenceNumber), aws.ToString(puts.Records[0].SequenceNumber), aws.ToString(puts.Records[1].SequenceNumber)}
	var prev *big.Int
	for i, r := range page.Records {
		// kinesis.md 4.11 (aws-recorded, LS test_kinesis.snapshot.json:L58-L88):
		// no EncryptionType on an unencrypted stream's records; 5.10: arrival
		// to the millisecond.
		if !bytes.Equal(r.Data, wantData[i]) || aws.ToString(r.SequenceNumber) != wantSeq[i] || r.EncryptionType != "" ||
			r.ApproximateArrivalTimestamp == nil || !r.ApproximateArrivalTimestamp.Equal(r.ApproximateArrivalTimestamp.Truncate(time.Millisecond)) {
			t.Errorf("record %d = %+v", i, r)
		}
		// kinesis.md 3.4: increasing decimal sequence numbers within a shard.
		seq, isNumber := new(big.Int).SetString(aws.ToString(r.SequenceNumber), 10)
		if !isNumber || (prev != nil && seq.Cmp(prev) != 1) {
			t.Errorf("record %d SequenceNumber %s, want a number above %v", i, aws.ToString(r.SequenceNumber), prev)
		}
		prev = seq
	}

	// kinesis.md finding 5 (aws-recorded over CBOR, LS
	// test_kinesis.snapshot.json:L230-L237): the name and account, even when
	// addressed by ARN.
	missing := streamName + "x"
	_, err = client.DescribeStream(ctx, &kinesis.DescribeStreamInput{StreamARN: aws.String(streamArn + "x")})
	errAs[*types.ResourceNotFoundException](t, err, "DescribeStream of a missing stream")
	wantMessage(t, err, "DescribeStream of a missing stream", "Stream "+missing+" under account "+account(t)+" not found.")

	// kinesis.md 4.3: gone after DeleteStream; the model's StreamNotExists waiter.
	_, err = client.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(streamName)})
	ok(t, err, "DeleteStream")
	err = kinesis.NewStreamNotExistsWaiter(client).Wait(ctx, &kinesis.DescribeStreamInput{StreamName: aws.String(streamName)}, 2*time.Minute)
	ok(t, err, "waiting for "+streamName+" to be deleted")
}

// kinesis.md finding 4 (aws-recorded, LS test_kinesis.snapshot.json:L2-L47):
// a stream created with only a name has four shards.
func TestKinesisStreamWithOnlyAName(t *testing.T) {
	t.Parallel()
	client := kinesis.NewFromConfig(awsCfg)
	streamName := name(t, "kin-od")
	createKinesisStream(t, streamName, 0)
	shards, err := client.ListShards(t.Context(), &kinesis.ListShardsInput{StreamName: aws.String(streamName)})
	ok(t, err, "ListShards")
	if len(shards.Shards) != 4 {
		t.Errorf("ListShards returned %d shards, want 4", len(shards.Shards))
	}
}
