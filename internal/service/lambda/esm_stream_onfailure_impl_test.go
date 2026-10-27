package lambda_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// Records that pass their age while their batch is retried are each
// discarded with their own RecordAgeExceeded record, counting the
// invocations made and naming the last invocation's request id, with no
// responseContext (recorded approximateInvokeCount above 1,
// lambda-stream-sources.md 5.3, 5.6; one record each, inferred, ESMS-09;
// contract 7 FailedBatch.RequestID).
func TestStreamRecordAgeWhileRetryingNamesTheLastInvocation(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		url, dlq := h.queue("dlq", nil)
		h.rec.setAnswer(alwaysFail)
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{
			MaximumRecordAgeInSeconds: new(int32(60)),
			DestinationConfig:         &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(dlq)}},
		})
		// Both records are 100 ms short of their age when first invoked;
		// the retry 250 ms later finds them past it.
		h.stream.add(h.Clock.Now().Add(-60*time.Second+100*time.Millisecond), "a", "b")
		h.start()
		h.advancePolled(esm.DynamoDBRetryDelay)
		if n := h.rec.count(); n != 1 {
			h.Errorf("%d invocations, want the one before the records aged", n)
		}
		got := h.failures(url)
		if len(got) != 2 {
			h.Fatalf("%d on-failure records, want one per record", len(got))
		}
		for i, f := range got {
			if f.RequestContext.Condition != "RecordAgeExceeded" || f.RequestContext.ApproximateInvokeCount != 1 || f.ResponseContext != nil ||
				f.Info.BatchSize != 1 || f.Info.Start != seqAt(i) || f.Info.End != seqAt(i) {
				h.Errorf("record %d: %+v", i, f)
			}
		}
		if got[0].RequestContext.RequestID == "" || got[0].RequestContext.RequestID != got[1].RequestContext.RequestID {
			h.Errorf("request ids %q and %q, want the one invocation's in both", got[0].RequestContext.RequestID, got[1].RequestContext.RequestID)
		}
	})
}

// Records already past their age when read were never invoked, so each
// record names a request id of its own (contract 7 FailedBatch.RequestID).
func TestStreamRecordAgeBeforeInvocationHasNewRequestIDs(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		url, dlq := h.queue("dlq", nil)
		h.stream.add(h.Clock.Now().Add(-2*time.Minute), "a", "b")
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{
			MaximumRecordAgeInSeconds: new(int32(60)),
			DestinationConfig:         &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(dlq)}},
		})
		h.start()
		if n := h.rec.count(); n != 0 {
			h.Errorf("%d invocations, want none", n)
		}
		got := h.failures(url)
		if len(got) != 2 {
			h.Fatalf("%d on-failure records, want 2", len(got))
		}
		if a, b := got[0].RequestContext.RequestID, got[1].RequestContext.RequestID; a == "" || b == "" || a == b {
			h.Errorf("request ids %q and %q, want two distinct ids", a, b)
		}
	})
}

// An on-failure destination that does not exist does not hold the shard:
// the batch is discarded, the failed send is journaled as an esm-to-sqs
// delivery attempted once, and the reader goes on to the next record
// (destination existence checked only at delivery, inferred,
// lambda-stream-sources.md 3.7; spec enforce row 4: the batch counts as
// discarded).
func TestStreamMissingDestinationStillDiscards(t *testing.T) {
	runStream(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *streamHarness) {
		h.rec.setAnswer(alwaysFail)
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{
			MaximumRetryAttempts: new(int32(0)),
			DestinationConfig:    &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new("arn:aws:sqs:us-east-1:000000000000:missing")}},
		})
		h.start()
		h.stream.add(h.Clock.Now(), "a")
		h.quiesce()
		h.rec.setAnswer(nil)
		h.stream.add(h.Clock.Now(), "b")
		h.quiesce()
		if got, want := h.rec.ids(), [][]string{{"a"}, {"b"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations %v, want %v", got, want)
		}
		var sends []journal.Delivery
		for _, ev := range h.Journal.Query(journal.Filter{Kind: journal.KindDelivery, Limit: 100}).Events {
			if d, ok := ev.Detail.(journal.Delivery); ok && d.Mechanism == delivery.ESMToSQS {
				sends = append(sends, d)
			}
		}
		if len(sends) != 1 {
			h.Fatalf("%d esm-to-sqs deliveries, want 1", len(sends))
		}
		if _, dropped := sends[0].Outcome.(journal.Dropped); !dropped || sends[0].Attempt != 1 {
			h.Errorf("delivery %+v, want one dropped attempt", sends[0])
		}
	})
}
