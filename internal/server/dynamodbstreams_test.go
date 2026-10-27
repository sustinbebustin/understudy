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
// log, answers 501 naming the same setting (spec 8a scope: "While DynamoDB
// is disabled, Streams answers 501 naming the same setting").
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

// The coverage report carries the 8a notes exactly as the spec words them
// ("Coverage marks"): the four Streams operations, all bound and partial,
// and the stream clauses of Lambda's mapping operations.
func TestDynamoDBStreamsCoverageNotes(t *testing.T) {
	want := map[string]string{
		"dynamodbstreams.ListStreams":      "a TableName naming no table returns an empty list, a disabled stream stays listed until it is deleted 24 hours after its table, and LastEvaluatedStreamArn is set only when more streams follow; AWS's answers to all three, and the Limit text (DynamoDB Local's), are UNVERIFIED (dynamodb-streams.md 6.1, 6.5)",
		"dynamodbstreams.DescribeStream":   "aws-differs: understudy: a stream has one shard, which never splits or rolls over; AWS opens a shard per table partition and closes shards as it replaces them; StreamStatus is ENABLED or DISABLED at once, never ENABLING or DISABLING; the Limit text is DynamoDB Local's and UNVERIFIED (dynamodb-streams.md 6.5)",
		"dynamodbstreams.GetShardIterator": "AT_TIMESTAMP is refused: the pinned model (api-models-aws 1cfdfb4) predates it; the validation and not-found texts are DynamoDB Local's, and a deleted table's stream stays readable until it is deleted 24 hours later; AWS's forms of both are UNVERIFIED (dynamodb-streams.md 5.4, 6.5)",
		"dynamodbstreams.GetRecords":       "aws-differs: understudy: more than two concurrent readers of a shard are never throttled; inject LimitExceededException with a fault rule; the last page of a closed shard omits NextShardIterator, a Limit above 1000 answers ValidationException as DynamoDB Local does, ApproximateCreationDateTime is written as a plain epoch-seconds number, a NextShardIterator expires 15 minutes after its own issue, SizeBytes is DynamoDB Local's (a byte above the documented item-size rule for some fractional numbers), a DeleteItem of an absent key emits no record, and the malformed, expired and trimmed iterator texts are DynamoDB Local's or understudy's; AWS's forms are UNVERIFIED (dynamodb-streams.md 4.4, 5.3, 5.5, 6.4, 7.3, 7.4, 7.6)",
	}
	esm := "SQS queues and DynamoDB streams; serve polls the source and invokes the function with each batch; Kinesis streams, ParallelizationFactor above 1, BisectBatchOnFunctionError, TumblingWindowInSeconds above 0, a valid FilterCriteria on a stream and an S3 on-failure destination answer 501; a stream is read as soon as it receives a record rather than at AWS's polling rate (4 per second for DynamoDB, once per second for Kinesis), which is used as the delay before a retry; UNVERIFIED (lambda-stream-sources.md 3.3, 3.4, 3.7, 4.2, 5.1, 5.3, 5.5, 5.6): the text refusing MaximumRecordAgeInSeconds 0 to 59, an explicit BatchSize above 10 without a window being accepted on a stream, members of the other source type being ignored, destination existence checked only at delivery, a retried batch keeping its records, the 6 MB limit counted over the serialized event, record age measured from the record's arrival in the stream, RetryAttemptsExhausted reported when the last retry fails after the records have also passed their age, one on-failure record per expired record, an itemIdentifier outside the batch counting as complete failure, batchItemFailures keys matched case-sensitively, records lost to stream retention dropped without an on-failure record, an on-failure record the execution role may not send dropped with a journal entry, LastProcessingResult unchanged by a failure, and on-failure messages without attributes; MetricsConfig is stored, no metrics are emitted"
	want["lambda.CreateEventSourceMapping"] = esm
	want["lambda.UpdateEventSourceMapping"] = esm

	got := map[string]coverage.Status{}
	for _, s := range wiredReport(t).Services {
		for _, op := range s.Operations {
			got[string(s.Service)+"."+op.Name] = op.Status
		}
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

// The five 8a switches are reported by the wired emulator (spec "Fidelity
// switches"; contract 9). The shape checks every switch must pass are
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
