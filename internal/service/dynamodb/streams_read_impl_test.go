package dynamodb_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
)

// GetRecords floors ApproximateCreationDateTime to the second
// (dynamodb-streams.md 7.3), but answers an event source mapping's
// in-process read with the stored arrival, from which the poller measures
// record age and retention (spec 8a poller, inferred, ESMS-09).
func TestRecordsKeepTheArrivalForAMappingRead(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.advance(1500 * time.Millisecond)
		at := h.Clock.Now()
		h.put("Orders", "a")
		stream, err := aws.ParseARN(arn)
		if err != nil {
			h.Fatal(err)
		}
		scope, err := aws.ScopeOf(stream)
		if err != nil {
			h.Fatal(err)
		}
		parent := h.Call(dynamodbapi.OpDescribeTableRaw.Descriptor())
		read := func(via ref.Via) time.Time {
			h.Helper()
			as := kernel.Target{Scope: scope, As: kernel.AsPlatform{Name: "lambda.amazonaws.com"}, Via: via}
			it, aerr := kernel.Invoke(h.Context(), h.Kernel, parent, dynamodbstreamsapi.OpGetShardIterator, as, &dynamodbstreamsapi.GetShardIteratorInput{
				StreamArn: arn, ShardId: h.describe(arn).Shards[0].ShardID, ShardIteratorType: dynamodbstreamsapi.ShardIteratorTypeTrimHorizon,
			})
			if aerr != nil {
				h.Fatalf("GetShardIterator: %v", aerr)
			}
			out, aerr := kernel.Invoke(h.Context(), h.Kernel, parent, dynamodbstreamsapi.OpGetRecords, as, &dynamodbstreamsapi.GetRecordsInput{ShardIterator: *it.ShardIterator})
			if aerr != nil {
				h.Fatalf("GetRecords: %v", aerr)
			}
			if len(out.Records) != 1 || out.Records[0].Dynamodb == nil || out.Records[0].Dynamodb.ApproximateCreationDateTime == nil {
				h.Fatalf("GetRecords = %+v, want one record with a creation time", out.Records)
			}
			return *out.Records[0].Dynamodb.ApproximateCreationDateTime
		}
		if got := read(ref.LambdaESM); !got.Equal(at.Wire()) {
			h.Errorf("a mapping's read: ApproximateCreationDateTime = %v, want the arrival %v", got, at.Wire())
		}
		if got, want := read(ref.ViaAPIGateway), at.Wire().Truncate(time.Second); !got.Equal(want) {
			h.Errorf("another internal read: ApproximateCreationDateTime = %v, want %v (floored)", got, want)
		}
	})
}

// A TRIM_HORIZON iterator taken before a trim whose next record is then
// trimmed is refused at GetRecords with TrimmedDataAccessException ("one
// whose next record was trimmed before it was used", dynamodb-streams.md
// 5.5), as AFTER_SEQUENCE_NUMBER and AT_SEQUENCE_NUMBER iterators are; one
// taken on an empty shard never trimmed reads what arrives later.
func TestTrimHorizonIteratorIsRefusedAfterItsRecordIsTrimmed(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		shard := h.describe(arn).Shards[0].ShardID
		empty := h.iterator(arn, shard, "TRIM_HORIZON", "")
		h.put("Orders", "a")
		if got := h.records(empty, 0).Records; len(got) != 1 {
			h.Errorf("TRIM_HORIZON taken on the empty shard read %d records, want 1", len(got))
		}
		h.advance(24*time.Hour - time.Millisecond)
		h.settle()
		pending := h.iterator(arn, shard, "TRIM_HORIZON", "")
		h.advance(time.Millisecond)
		h.settle()
		e := h.errorOf(h.streamsCall("GetRecords", `{"ShardIterator":"`+pending+`"}`, defaultCaller()), http.StatusBadRequest)
		if e.Type != "com.amazonaws.dynamodb.v20120810#TrimmedDataAccessException" || e.text() != trimmedText {
			h.Errorf("GetRecords from a TRIM_HORIZON iterator past the trim: %+v", e)
		}
	})
}

// Trimming drops only the records 24 hours old (dynamodb-streams.md 5.5,
// aws-doc): a record an hour younger is still read from TRIM_HORIZON and
// from AFTER_SEQUENCE_NUMBER of the trimmed record, and is trimmed in its
// own turn exactly 24 hours after its arrival.
func TestTrimKeepsYoungerRecords(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		shard := h.describe(arn).Shards[0].ShardID
		h.put("Orders", "old")
		h.advance(time.Hour)
		h.put("Orders", "young")
		all := h.allRecords(arn)
		if len(all) != 2 {
			h.Fatalf("%d records, want 2", len(all))
		}
		h.advance(23 * time.Hour)
		if got := h.allRecords(arn); len(got) != 1 || got[0].Dynamodb.SequenceNumber != all[1].Dynamodb.SequenceNumber {
			h.Errorf("at 24 h after the first: %v, want only the younger record", got)
		}
		after := h.records(h.iterator(arn, shard, "AFTER_SEQUENCE_NUMBER", all[0].Dynamodb.SequenceNumber), 0)
		if len(after.Records) != 1 || after.Records[0].Dynamodb.SequenceNumber != all[1].Dynamodb.SequenceNumber {
			h.Errorf("AFTER_SEQUENCE_NUMBER of the trimmed record: %v, want the younger record", after.Records)
		}
		h.advance(time.Hour)
		if got := h.allRecords(arn); len(got) != 0 {
			h.Errorf("at 24 h after the second: %d records, want none", len(got))
		}
	})
}

// Once a deleted table's stream is gone at 24 hours, GetShardIterator
// answers AWS's not-found text (aws-recorded, DDBS-01:
// 02-iterator-missing-stream.json) and an iterator taken before reads the
// same (dynamodb-streams.md 5.4, 6.5; the 24 hours are aws-doc, DDBS-17
// recorded the stream readable just after the delete).
func TestGoneStreamRefusesIterators(t *testing.T) {
	const missing = "Requested resource not found: Stream does not exist"
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		shard := h.describe(arn).Shards[0].ShardID
		h.put("Orders", "a")
		h.mustCall("DeleteTable", `{"TableName":"Orders"}`)
		h.advance(24*time.Hour - time.Second)
		it := h.iterator(arn, shard, "TRIM_HORIZON", "")
		h.advance(time.Second)
		body := `{"StreamArn":"` + arn + `","ShardId":"` + shard + `","ShardIteratorType":"TRIM_HORIZON"}`
		if e := h.errorOf(h.streamsCall("GetShardIterator", body, defaultCaller()), http.StatusBadRequest); !strings.HasSuffix(e.Type, "#ResourceNotFoundException") || e.text() != missing {
			h.Errorf("GetShardIterator at 24 h: %+v", e)
		}
		if e := h.errorOf(h.streamsCall("GetRecords", `{"ShardIterator":"`+it+`"}`, defaultCaller()), http.StatusBadRequest); !strings.HasSuffix(e.Type, "#ResourceNotFoundException") || e.text() != missing {
			h.Errorf("GetRecords at 24 h: %+v", e)
		}
	})
}

// A table re-created under a deleted table's name keeps its own stream
// when the old one is deleted 24 hours later (dynamodb-streams.md 5.4).
func TestGoneStreamLeavesTheNewTablesStream(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		old, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.mustCall("DeleteTable", `{"TableName":"Orders"}`)
		h.advance(time.Hour)
		renewed, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.advance(23 * time.Hour)
		if got := h.listedStreams(`{}`); !slices.Equal(got, []string{renewed}) {
			h.Errorf("ListStreams = %v, want only %s (not %s)", got, renewed, old)
		}
		if d := h.describe(renewed); d.StreamStatus != "ENABLED" {
			h.Errorf("the new table's stream = %+v, want ENABLED", d)
		}
	})
}

// Under dynamodb.streams.iterator-reuse single-use (inferred, DDBS-04) a
// NextShardIterator also reads once, and each iterator GetShardIterator
// issues reads on its own.
func TestSingleUseCoversNextShardIterators(t *testing.T) {
	runStreams(t, dynamodb.Options{IteratorReuse: dynamodb.IteratorSingleUse}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		shard := h.describe(arn).Shards[0].ShardID
		h.put("Orders", "a")
		first := h.records(h.iterator(arn, shard, "TRIM_HORIZON", ""), 0)
		if first.NextShardIterator == nil {
			h.Fatal("no NextShardIterator on an open shard")
		}
		next := *first.NextShardIterator
		h.records(next, 0)
		if e := h.errorOf(h.streamsCall("GetRecords", `{"ShardIterator":"`+next+`"}`, defaultCaller()), http.StatusBadRequest); e.text() != expiredText {
			h.Errorf("the NextShardIterator read twice: %+v, want %q", e, expiredText)
		}
		h.advance(time.Millisecond)
		if n := len(h.records(h.iterator(arn, shard, "TRIM_HORIZON", ""), 0).Records); n != 1 {
			h.Errorf("a new iterator read %d records, want 1", n)
		}
	})
}

// LocalStack goldens 6 and 7 (spec A5), parsed-level:
//   - 6: localstack@8b9a79f tests/aws/services/dynamodb/test_dynamodb.snapshot.json:L662-L665
//     test_dynamodb_streams_describe_with_exclusive_start_shard_id, validated 2025-10-08: an
//     ExclusiveStartShardId naming a shard leaves that shard out of Shards
//     (dynamodb-streams.md 5.6).
//   - 7: localstack@8b9a79f tests/aws/services/dynamodbstreams/test_dynamodb_streams.snapshot.json:L90-L104
//     test_non_existent_stream, validated 2024-11-20: DescribeStream of a missing stream answers 400
//     ResourceNotFoundException "Requested resource not found: Stream: <arn> not found", the ARN in the
//     caller's account and region (dynamodb-streams.md 3.3).
func TestStreamsLocalStackGoldens6And7(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		shard := h.describe(arn).Shards[0].ShardID
		var out struct{ StreamDescription streamDescription }
		h.ok200(h.streamsCall("DescribeStream", `{"StreamArn":"`+arn+`","ExclusiveStartShardId":"`+shard+`"}`, defaultCaller()), &out)
		for _, s := range out.StreamDescription.Shards {
			if s.ShardID == shard {
				h.Errorf("golden 6: Shards %+v lists the ExclusiveStartShardId", out.StreamDescription.Shards)
			}
		}
		other := &caller{Account: "222222222222", Region: "eu-west-1"}
		missing := streamARNOf(other, "Nope", "2024-11-18T14:36:44.149")
		e := h.errorOf(h.streamsCall("DescribeStream", `{"StreamArn":"`+missing+`"}`, other), http.StatusBadRequest)
		if !strings.HasSuffix(e.Type, "#ResourceNotFoundException") || e.text() != "Requested resource not found: Stream: "+missing+" not found" {
			h.Errorf("golden 7: %+v", e)
		}
	})
}
