package esm_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// kinesisFailedBatch is the documented Kinesis on-failure record's inputs
// (aws-doc kinesis-on-failure-destination; lambda-stream-sources.md 5.6,
// kinesis.md 8.2).
func kinesisFailedBatch(t *testing.T, cond esm.FailCondition) esm.FailedBatch {
	t.Helper()
	return esm.FailedBatch{
		RequestID:   "c9b8fa9f-5a7f-xmpl-af9c-0c604cde93a5",
		FunctionARN: "arn:aws:lambda:us-east-2:123456789012:function:myfunction",
		Condition:   cond,
		InvokeCount: 1,
		Timestamp:   instant(t, "2019-11-14T00:38:06.021Z"),
		Stream:      arn(t, "arn:aws:kinesis:us-east-2:123456789012:stream/mystream"),
		Shard:       "shardId-000000000001",
		First:       esm.StreamRecord{Seq: "49601189658422359378836298521827638475320189012309704722", Arrival: instant(t, "2019-11-14T00:38:04.835Z")},
		Last:        esm.StreamRecord{Seq: "49601189658422359378836298522902373528957594348623495186", Arrival: instant(t, "2019-11-14T00:38:05.580Z")},
		Size:        500,
	}
}

// The documented Kinesis record reproduces byte for byte from its inputs:
// the DynamoDB record's key order with KinesisBatchInfo, arrival times with
// milliseconds and Z (aws-doc kinesis-on-failure-destination,
// lambda-stream-sources.md 5.6; recorded LS-K:L1343-L1351).
func TestKinesisFailureRecordDocumented(t *testing.T) {
	t.Parallel()
	b := kinesisFailedBatch(t, esm.RetryAttemptsExhausted{StatusCode: 200, ExecutedVersion: "$LATEST", FunctionError: "Unhandled"})
	want := `{"requestContext":{"requestId":"c9b8fa9f-5a7f-xmpl-af9c-0c604cde93a5","functionArn":"arn:aws:lambda:us-east-2:123456789012:function:myfunction","condition":"RetryAttemptsExhausted","approximateInvokeCount":1},` +
		`"responseContext":{"statusCode":200,"executedVersion":"$LATEST","functionError":"Unhandled"},` +
		`"version":"1.0","timestamp":"2019-11-14T00:38:06.021Z",` +
		`"KinesisBatchInfo":{"shardId":"shardId-000000000001","startSequenceNumber":"49601189658422359378836298521827638475320189012309704722",` +
		`"endSequenceNumber":"49601189658422359378836298522902373528957594348623495186",` +
		`"approximateArrivalOfFirstRecord":"2019-11-14T00:38:04.835Z","approximateArrivalOfLastRecord":"2019-11-14T00:38:05.580Z","batchSize":500,` +
		`"streamArn":"arn:aws:kinesis:us-east-2:123456789012:stream/mystream"}}`
	if got := string(esm.FailureRecord(esm.KinesisStream{}, b)); got != want {
		t.Errorf("FailureRecord =\n%s\nwant\n%s", got, want)
	}
}

// RecordAgeExceeded has no responseContext (recorded LS-K:L3212-L3219,
// lambda-stream-sources.md 5.6), and arrival times keep milliseconds even
// when they are zero (the documented form 2006-01-02T15:04:05.000Z).
func TestKinesisFailureRecordAgeExceeded(t *testing.T) {
	t.Parallel()
	b := kinesisFailedBatch(t, esm.RecordAgeExceeded{})
	b.First.Arrival = instant(t, "2019-11-14T00:38:04Z")
	b.Last, b.Size = b.First, 1
	got := string(esm.FailureRecord(esm.KinesisStream{}, b))
	if strings.Contains(got, "responseContext") || !strings.Contains(got, `"condition":"RecordAgeExceeded"`) ||
		!strings.Contains(got, `"approximateArrivalOfFirstRecord":"2019-11-14T00:38:04.000Z","approximateArrivalOfLastRecord":"2019-11-14T00:38:04.000Z","batchSize":1`) {
		t.Errorf("FailureRecord = %s", got)
	}
}

// A Kinesis stream's retry delay is its documented polling rate, "a base
// rate of once per second" (aws-doc, kinesis.md 8.2;
// lambda-stream-sources.md 4.1), and StreamOf reads its start and
// settings.
func TestKinesisStreamKind(t *testing.T) {
	t.Parallel()
	if esm.KinesisRetryDelay != time.Second || esm.PollInterval(esm.KinesisStream{}) != time.Second {
		t.Errorf("KinesisRetryDelay %v, PollInterval %v; want 1s", esm.KinesisRetryDelay, esm.PollInterval(esm.KinesisStream{}))
	}
	k := esm.KinesisStream{Position: esm.Latest{}, Settings: esm.DefaultStreamSettings()}
	pos, set, ok := esm.StreamOf(k)
	if _, latest := pos.(esm.Latest); !ok || !latest || set != esm.DefaultStreamSettings() {
		t.Errorf("StreamOf(%+v) = %v, %+v, %v", k, pos, set, ok)
	}
	if _, _, ok := esm.StreamOf(esm.SQS{}); ok {
		t.Error("StreamOf(SQS) reports a stream")
	}
}
