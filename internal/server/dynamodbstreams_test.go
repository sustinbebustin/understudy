package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/coverage"
	"github.com/sustinbebustin/understudy/internal/server"
)

// With DynamoDB turned off, DynamoDB Streams, served from DynamoDB's stream
// log, answers 501 naming the same setting.
func TestDynamoDBStreamsDisabledNamesTheSetting(t *testing.T) {
	srv, _ := build(t, server.Config{Docker: server.DockerDisabled{}, DynamoDB: server.DynamoDBDisabled{}})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost:4566/", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "DynamoDBStreams_20120810.ListStreams")
	req.Header.Set("X-Amz-Date", "20260101T000000Z")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260101/us-east-1/dynamodb/aws4_request, SignedHeaders=host;x-amz-date, Signature=0")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "dynamodb.enabled is false") {
		t.Fatalf("ListStreams with DynamoDB disabled = %d %s, want 501 naming dynamodb.enabled", rec.Code, rec.Body)
	}
}

// The coverage report carries the Streams notes as the DDBS recordings of
// 2026-11-03 and 2026-11-04 left them (dynamodb-streams.md 5.5, 5.6, 6.5,
// 11.2): ListStreams full, since its constraint texts leave the value out
// as DDBS-02 recorded AWS's; the other three partial with the differences
// recorded (the shard rollover and removal DDBS-14 and DDBS-15 recorded);
// and the stream clauses of Lambda's mapping operations.
func TestDynamoDBStreamsCoverageNotes(t *testing.T) {
	// GetShardIterator and GetRecords both answer the trim, so both carry
	// it (DDBS-15: try-5-at-old-sequence.json, 01-at-old-sequence.json).
	trim := "a record is trimmed from the stream's one shard 24 hours after it arrives, and GetShardIterator or GetRecords at it answers TrimmedDataAccessException with the model's text, " +
		"where AWS was never seen to trim a record from a shard: it kept records for more than 29 hours and then removed their closed shard whole, so GetShardIterator at the record answered ResourceNotFoundException \"Requested resource not found: Shard does not exist\""
	want := map[string]string{
		"dynamodbstreams.DescribeStream": "aws-differs: understudy: a stream has one shard, which never splits or rolls over; " +
			"AWS opened a new on-demand table's stream with 4 shards, replaced each with a child every 3.5 to 4.5 hours and removed a closed shard 24.5 to 26.3 hours after it closed; " +
			"StreamStatus is ENABLED or DISABLED at once, where AWS reports ENABLING with no shards for about 7 seconds and DISABLING for about 8 (dynamodb-streams.md 5.2, 5.6)",
		"dynamodbstreams.GetShardIterator": "aws-differs: understudy: " + trim + "; " +
			"AT_TIMESTAMP is refused: the pinned model (api-models-aws 1cfdfb4) predates it, so the enum text lists the four other types where AWS's lists five (dynamodb-streams.md 3.5, 5.5, 6.5)",
		"dynamodbstreams.GetRecords": "aws-differs: understudy: more than two concurrent readers of a shard are never throttled; inject LimitExceededException with a fault rule; " +
			"ApproximateCreationDateTime is written as a plain epoch-seconds number, where AWS writes the exponent form (1.793723928E9); " +
			"ExpiredIteratorException carries the __type namespace com.amazonaws.dynamodb.v20120810, where AWS's is com.amazonaws.dynamodb.streams.v20120810; " +
			trim + " (dynamodb-streams.md 3.3, 5.5, 7.3)",
	}
	// The note covers both kinds of stream: the Kinesis kind in the
	// first clause, the 501 list naming consumers and AT_TIMESTAMP on a
	// Kinesis stream, and the re-enable clause (lambda-stream-sources.md 7.1)
	// last in the UNVERIFIED list.
	esm := "SQS queues, DynamoDB streams and Kinesis data streams; serve polls the source and invokes the function with each batch; Kinesis stream consumers, AT_TIMESTAMP on a Kinesis stream, ParallelizationFactor above 1, BisectBatchOnFunctionError, TumblingWindowInSeconds above 0, a valid FilterCriteria on a stream and an S3 on-failure destination answer 501; a stream is read as soon as it receives a record rather than at AWS's polling rate (4 per second for DynamoDB, once per second for Kinesis), which is used as the delay before a retry; UNVERIFIED (lambda-stream-sources.md 3.3, 3.4, 3.7, 4.2, 5.1, 5.3, 5.5, 5.6, 7.1): the text refusing MaximumRecordAgeInSeconds 0 to 59, an explicit BatchSize above 10 without a window being accepted on a Kinesis stream, members of the SQS type being ignored on a stream mapping, an Update refusing the stream type's members on an SQS mapping as Create does, destination existence checked only at delivery, a retried batch keeping its records, the 6 MB limit counted over the serialized event, record age measured from the record's arrival in the stream, RetryAttemptsExhausted reported when the last retry fails after the records have also passed their age, one on-failure record per expired record, an itemIdentifier outside the batch counting as complete failure, batchItemFailures keys matched case-sensitively, records lost to stream retention dropped without an on-failure record, an on-failure record the execution role may not send dropped with a journal entry, LastProcessingResult unchanged by a failure, on-failure messages without attributes, and records written while a Kinesis mapping is disabled being delivered after re-enable; MetricsConfig is stored, no metrics are emitted"
	// The iam.enforce clause: the SQS sentence, then the DynamoDB stream
	// reads and the Kinesis reads (iam-enforcement-surfaces.md 8.6).
	esm += "; under iam.enforce=enforce an SQS mapping's execution role must be allowed sqs:GetQueueAttributes on the queue, where AWS is said to check sqs:ReceiveMessage, " +
		"and a stream mapping's dynamodb:DescribeStream on the stream, and at LATEST dynamodb:GetShardIterator and dynamodb:GetRecords (lambda.esm.create-denied-text); " +
		"a role lacking only dynamodb:ListStreams, or at TRIM_HORIZON only dynamodb:GetShardIterator or dynamodb:GetRecords, is accepted, where whether AWS refuses it is UNVERIFIED (iam-enforcement-surfaces.md 8.6); " +
		"a Kinesis mapping's role must be allowed kinesis:DescribeStreamSummary on the stream, and at LATEST kinesis:ListShards, kinesis:GetShardIterator and kinesis:GetRecords; " +
		"a role lacking only those reads at TRIM_HORIZON is accepted, where whether AWS refuses it is UNVERIFIED (iam-enforcement-surfaces.md 8.6); " +
		"a role refused its polls or stream reads leaves the mapping Enabled and invoking nothing (lambda.esm.permission-loss)"
	want["lambda.CreateEventSourceMapping"] = esm
	want["lambda.UpdateEventSourceMapping"] = esm

	got := map[string]coverage.Status{}
	for _, s := range wiredReport(t).Services {
		for _, op := range s.Operations {
			got[string(s.Service)+"."+op.Name] = op.Status
		}
	}
	if _, ok := got["dynamodbstreams.ListStreams"].(coverage.Full); !ok {
		t.Errorf("dynamodbstreams.ListStreams: status %#v, want full", got["dynamodbstreams.ListStreams"])
	}
	for op, note := range want {
		p, ok := got[op].(coverage.Partial)
		if !ok {
			t.Errorf("%s: status %#v, want partial", op, got[op])
			continue
		}
		if p.Note != note {
			t.Errorf("%s note:\n%s\nwant\n%s", op, p.Note, note)
		}
	}
}

// The five switches of DynamoDB Streams and the stream poller are reported
// by the wired emulator. The shape checks every switch must pass are
// TestEverySwitchStatesItsOpsAndSettles'.
func TestDynamoDBStreamsSwitchesAreReported(t *testing.T) {
	names := map[string]bool{}
	for _, sw := range wiredReport(t).Switches {
		names[sw.Name] = true
	}
	for _, n := range []string{
		"dynamodb.streams.sequence-number",
		"dynamodb.streams.iterator-reuse",
		"lambda.esm-stream.latest-anchor",
		"lambda.esm-stream.retry-delay",
		"lambda.esm-stream.partial-resend-budget",
	} {
		if !names[n] {
			t.Errorf("switch %s is not reported", n)
		}
	}
}
