package gosdk_test

import (
	"context"
	"math/big"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodbstreams"
	streamtypes "github.com/aws/aws-sdk-go-v2/service/dynamodbstreams/types"
)

// Research: docs/research/dynamodb-streams.md 3-7 and 11.

// streamLabel is the label form AWS answers and Terraform's acceptance
// tests match against AWS (dynamodb-streams.md 4.1).
var streamLabel = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}$`)

// createStreamTable creates an on-demand table keyed by id with a stream
// of view, waits until it is ACTIVE and deletes it when the test ends.
func createStreamTable(t *testing.T, tableName string, view types.StreamViewType) *types.TableDescription {
	t.Helper()
	ctx := t.Context()
	client := dynamodb.NewFromConfig(awsCfg)
	out, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(tableName),
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
		BillingMode:          types.BillingModePayPerRequest,
		StreamSpecification:  &types.StreamSpecification{StreamEnabled: aws.Bool(true), StreamViewType: view},
	})
	ok(t, err, "CreateTable "+tableName)
	t.Cleanup(func() {
		_, _ = client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(tableName)})
	})
	err = dynamodb.NewTableExistsWaiter(client).Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)}, 3*time.Minute)
	ok(t, err, "waiting for "+tableName+" to be ACTIVE")
	return out.TableDescription
}

// streamRecords reads shard of stream from TRIM_HORIZON until a page is
// empty or the shard ends.
func streamRecords(t *testing.T, streamArn, shard string) []streamtypes.Record {
	t.Helper()
	client := dynamodbstreams.NewFromConfig(awsCfg)
	it, err := client.GetShardIterator(t.Context(), &dynamodbstreams.GetShardIteratorInput{
		StreamArn: aws.String(streamArn), ShardId: aws.String(shard), ShardIteratorType: streamtypes.ShardIteratorTypeTrimHorizon,
	})
	ok(t, err, "GetShardIterator TRIM_HORIZON")
	if !strings.HasPrefix(aws.ToString(it.ShardIterator), streamArn+"|1|") {
		t.Errorf("ShardIterator = %q, want %s|1|<token> (dynamodb-streams.md 4.4)", aws.ToString(it.ShardIterator), streamArn)
	}
	var out []streamtypes.Record
	next := it.ShardIterator
	for next != nil {
		page, err := client.GetRecords(t.Context(), &dynamodbstreams.GetRecordsInput{ShardIterator: next})
		ok(t, err, "GetRecords")
		out = append(out, page.Records...)
		if len(page.Records) == 0 {
			break
		}
		next = page.NextShardIterator
	}
	return out
}

func TestDynamoDBStreams(t *testing.T) {
	requireDynamoDB(t)
	t.Parallel()
	ctx := t.Context()
	ddb := dynamodb.NewFromConfig(awsCfg)
	streams := dynamodbstreams.NewFromConfig(awsCfg)
	tableName := name("streamed")
	desc := createStreamTable(t, tableName, types.StreamViewTypeNewAndOldImages)
	streamArn, label := aws.ToString(desc.LatestStreamArn), aws.ToString(desc.LatestStreamLabel)

	// dynamodb-streams.md 4.1: the ARN form in the caller's account and
	// region, and CreateTable already answers both members.
	if !streamLabel.MatchString(label) || streamArn != "arn:aws:dynamodb:"+region()+":"+account(t)+":table/"+tableName+"/stream/"+label {
		t.Fatalf("CreateTable LatestStreamArn %q, LatestStreamLabel %q", streamArn, label)
	}
	// dynamodb-streams.md 9.3: identical on every read.
	got, err := ddb.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)})
	ok(t, err, "DescribeTable")
	if aws.ToString(got.Table.LatestStreamArn) != streamArn || aws.ToString(got.Table.LatestStreamLabel) != label {
		t.Errorf("DescribeTable LatestStream = %q, %q; want CreateTable's", aws.ToString(got.Table.LatestStreamArn), aws.ToString(got.Table.LatestStreamLabel))
	}

	listed, err := streams.ListStreams(ctx, &dynamodbstreams.ListStreamsInput{TableName: aws.String(tableName)})
	ok(t, err, "ListStreams")
	if len(listed.Streams) != 1 || aws.ToString(listed.Streams[0].StreamArn) != streamArn || aws.ToString(listed.Streams[0].StreamLabel) != label ||
		aws.ToString(listed.Streams[0].TableName) != tableName {
		t.Errorf("ListStreams = %+v, want the table's stream", listed.Streams)
	}

	// DDB-SNAP:L708-L735 (third-party-recorded): one open shard, ENABLED.
	d, err := streams.DescribeStream(ctx, &dynamodbstreams.DescribeStreamInput{StreamArn: aws.String(streamArn)})
	ok(t, err, "DescribeStream")
	sd := d.StreamDescription
	if sd.StreamStatus != streamtypes.StreamStatusEnabled || sd.StreamViewType != streamtypes.StreamViewTypeNewAndOldImages ||
		aws.ToString(sd.TableName) != tableName || len(sd.Shards) != 1 || sd.Shards[0].SequenceNumberRange.EndingSequenceNumber != nil {
		t.Fatalf("DescribeStream = %+v", sd)
	}
	shard := aws.ToString(sd.Shards[0].ShardId)
	start, _ := new(big.Int).SetString(aws.ToString(sd.Shards[0].SequenceNumberRange.StartingSequenceNumber), 10)

	_, err = ddb.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tableName), Item: map[string]types.AttributeValue{"id": ddbS("a"), "v": ddbS("1")}})
	ok(t, err, "PutItem")
	_, err = ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName), Key: map[string]types.AttributeValue{"id": ddbS("a")},
		UpdateExpression: aws.String("SET v = :v"), ExpressionAttributeValues: map[string]types.AttributeValue{":v": ddbS("2")},
	})
	ok(t, err, "UpdateItem")
	_, err = ddb.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(tableName), Key: map[string]types.AttributeValue{"id": ddbS("a")}})
	ok(t, err, "DeleteItem")

	records := streamRecords(t, streamArn, shard)
	if len(records) != 3 {
		t.Fatalf("%d records, want INSERT, MODIFY and REMOVE", len(records))
	}
	// dynamodb-streams.md 7.1, 7.2 (NEW_AND_OLD_IMAGES row), 7.3; first
	// record above StartingSequenceNumber (third-party-recorded,
	// DDB-TEST:L1993-L1994); eventVersion 1.1 (DDB-SNAP:L761).
	prev := start
	for i, r := range records {
		want := []streamtypes.OperationType{streamtypes.OperationTypeInsert, streamtypes.OperationTypeModify, streamtypes.OperationTypeRemove}[i]
		if r.EventName != want || aws.ToString(r.EventSource) != "aws:dynamodb" || aws.ToString(r.EventVersion) != "1.1" || aws.ToString(r.AwsRegion) != region() || r.UserIdentity != nil {
			t.Errorf("record %d = %+v, want %s", i, r, want)
		}
		sr := r.Dynamodb
		seq, isNumber := new(big.Int).SetString(aws.ToString(sr.SequenceNumber), 10)
		if !isNumber || prev == nil || seq.Cmp(prev) != 1 {
			t.Errorf("record %d SequenceNumber %s, want a number above %v", i, aws.ToString(sr.SequenceNumber), prev)
		}
		prev = seq
		if aws.ToInt64(sr.SizeBytes) < 1 || sr.StreamViewType != streamtypes.StreamViewTypeNewAndOldImages || sr.ApproximateCreationDateTime == nil ||
			!sr.ApproximateCreationDateTime.Equal(sr.ApproximateCreationDateTime.Truncate(time.Second)) {
			t.Errorf("record %d dynamodb = %+v", i, sr)
		}
		hasNew, hasOld := sr.NewImage != nil, sr.OldImage != nil
		if wantNew, wantOld := i < 2, i > 0; hasNew != wantNew || hasOld != wantOld {
			t.Errorf("record %d images new %v old %v, want %v %v", i, hasNew, hasOld, wantNew, wantOld)
		}
	}

	// STREAMS-SNAP:L90-L104 (third-party-recorded): the not-found text.
	missing := "arn:aws:dynamodb:" + region() + ":" + account(t) + ":table/" + tableName + "x/stream/2024-11-18T14:36:44.149"
	_, err = streams.DescribeStream(ctx, &dynamodbstreams.DescribeStreamInput{StreamArn: aws.String(missing)})
	errAs[*streamtypes.ResourceNotFoundException](t, err, "DescribeStream of a missing stream")
	wantMessage(t, err, "DescribeStream of a missing stream", "Requested resource not found: Stream: "+missing+" not found")

	// dynamodb-streams.md 5.3: a disable closes the shard and keeps
	// LatestStreamArn; the closed shard's last page has no NextShardIterator.
	_, err = ddb.UpdateTable(ctx, &dynamodb.UpdateTableInput{TableName: aws.String(tableName), StreamSpecification: &types.StreamSpecification{StreamEnabled: aws.Bool(false)}})
	ok(t, err, "UpdateTable disable the stream")
	eventually(t, time.Minute, 500*time.Millisecond, "the stream to be DISABLED", func() (bool, string) {
		d, err := streams.DescribeStream(ctx, &dynamodbstreams.DescribeStreamInput{StreamArn: aws.String(streamArn)})
		if err != nil {
			return false, err.Error()
		}
		return d.StreamDescription.StreamStatus == streamtypes.StreamStatusDisabled && d.StreamDescription.Shards[0].SequenceNumberRange.EndingSequenceNumber != nil,
			"StreamStatus " + string(d.StreamDescription.StreamStatus)
	})
	got, err = ddb.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)})
	ok(t, err, "DescribeTable after the disable")
	if aws.ToString(got.Table.LatestStreamArn) != streamArn || got.Table.StreamSpecification != nil {
		t.Errorf("after the disable DescribeTable LatestStreamArn %q, StreamSpecification %+v", aws.ToString(got.Table.LatestStreamArn), got.Table.StreamSpecification)
	}
	it, err := streams.GetShardIterator(ctx, &dynamodbstreams.GetShardIteratorInput{StreamArn: aws.String(streamArn), ShardId: aws.String(shard), ShardIteratorType: streamtypes.ShardIteratorTypeTrimHorizon})
	ok(t, err, "GetShardIterator on the closed shard")
	page, err := streams.GetRecords(ctx, &dynamodbstreams.GetRecordsInput{ShardIterator: it.ShardIterator})
	ok(t, err, "GetRecords on the closed shard")
	if len(page.Records) != 3 || page.NextShardIterator != nil {
		t.Errorf("closed shard page: %d records, NextShardIterator %v; want 3 and none", len(page.Records), page.NextShardIterator)
	}
}
