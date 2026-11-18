package lambda_test

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
)

// A completely failing batch runs MaximumRetryAttempts + 1 invocations
// (third-party-recorded: 1 gives 2, LS-D:L427; 0 is one invocation,
// inferred from the same rule; lambda-stream-sources.md 5.2), then is
// discarded: the checkpoint moves past it and the next record is read
// ("Lambda discards the records and continues", aws-doc 5.1).
func TestStreamRetryLimitDiscardsTheBatch(t *testing.T) {
	for _, retries := range []int32{0, 1, 3} {
		t.Run(strconv.Itoa(int(retries)), func(t *testing.T) {
			runStream(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *streamHarness) {
				h.rec.setAnswer(alwaysFail)
				m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{MaximumRetryAttempts: new(retries)})
				h.start()
				h.stream.add(h.Clock.Now(), "a", "b")
				h.quiesce()
				if n := h.rec.count(); n != int(retries)+1 {
					h.Errorf("%d invocations, want MaximumRetryAttempts + 1 = %d", n, retries+1)
				}
				if got := stateChanges(h, *m.EventSourceMappingArn); !slices.Contains(got, "TRIM_HORIZON->AFTER "+seqAt(1)) {
					h.Errorf("mapping state changes %v, want the checkpoint moved past the discarded batch", got)
				}
				h.rec.setAnswer(nil)
				h.stream.add(h.Clock.Now(), "c")
				h.quiesce()
				if ids := h.rec.ids(); !slices.Equal(ids[len(ids)-1], []string{"c"}) {
					h.Errorf("after the discard the reader read %v, want [c]", ids[len(ids)-1])
				}
				if got := derefAny(h.get(*m.UUID).LastProcessingResult); got != "OK" {
					h.Errorf("LastProcessingResult = %v, want OK after c", got)
				}
			})
		})
	}
}

// Under poll-interval each retry waits 250 ms of emulator time
// (lambda.esm-stream.retry-delay, inferred, ESMS-06), so a limit of 2 runs
// its three invocations over 500 ms.
func TestStreamRetryLimitHonorsTheRetryDelay(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.rec.setAnswer(alwaysFail)
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{MaximumRetryAttempts: new(int32(2))})
		h.start()
		h.stream.add(h.Clock.Now(), "a")
		h.quiesce()
		h.advancePolled(2*esm.DynamoDBRetryDelay - time.Nanosecond)
		if n := h.rec.count(); n != 2 {
			h.Fatalf("%d invocations 1ns before the second retry, want 2", n)
		}
		h.advancePolled(time.Nanosecond)
		h.advancePolled(time.Minute)
		if n := h.rec.count(); n != 3 {
			h.Errorf("%d invocations, want 3 and no more after the discard", n)
		}
	})
}

// A reported failure of a batch's first record retries the whole batch
// and counts as an attempt: with MaximumRetryAttempts 2 the batch runs
// three times, then the next record is read (lambda-stream-sources.md
// 5.5: the checkpoint is the lowest reported record).
func TestStreamReportedFirstRecordCountsAsAnAttempt(t *testing.T) {
	runStream(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *streamHarness) {
		first := seqAt(0)
		h.rec.setAnswer(func(ev streamEvent) runtimeapi.Outcome {
			if seqOf(ev.Records[0]) == first {
				return runtimeapi.Success{Payload: []byte(`{"batchItemFailures":[{"itemIdentifier":"` + first + `"}]}`)}
			}
			return runtimeapi.Success{Payload: []byte("null")}
		})
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{
			BatchSize: new(int32(2)), MaximumRetryAttempts: new(int32(2)),
			FunctionResponseTypes: []lambdaapi.FunctionResponseType{"ReportBatchItemFailures"},
		})
		h.start()
		h.stream.add(h.Clock.Now(), "a", "b", "c")
		h.quiesce()
		want := [][]string{{"a", "b"}, {"a", "b"}, {"a", "b"}, {"c"}}
		if got := h.rec.ids(); !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations %v, want %v", got, want)
		}
	})
}

// A record older than MaximumRecordAgeInSeconds when read is discarded
// without invocation and the fresh ones are invoked (third-party-recorded
// LS-K.py:L1259-L1336; lambda-stream-sources.md 5.3; age from the
// record's arrival, inferred, ESMS-09). A record exactly at the limit is
// not older than it and is invoked.
func TestStreamRecordAgeBeforeInvocationWithoutDestination(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.stream.add(h.Clock.Now(), "old")
		h.advancePolled(20 * time.Second)
		h.stream.add(h.Clock.Now(), "edge")
		h.advancePolled(60 * time.Second)
		h.stream.add(h.Clock.Now(), "f1", "f2")
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{BatchSize: new(int32(10)), MaximumRecordAgeInSeconds: new(int32(60))})
		h.start()
		if got, want := h.rec.ids(), [][]string{{"edge", "f1", "f2"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations %v, want %v", got, want)
		}
		changes := stateChanges(h, *m.EventSourceMappingArn)
		if !slices.Contains(changes, "TRIM_HORIZON->AFTER "+seqAt(0)) || !slices.Contains(changes, "AFTER "+seqAt(0)+"->AFTER "+seqAt(3)) {
			h.Errorf("mapping state changes %v, want past the expired record, then past the batch", changes)
		}
	})
}

// A batch retried without a limit is discarded once its records pass
// MaximumRecordAgeInSeconds while being retried (third-party-recorded
// RecordAgeExceeded after several invocations with MaximumRetryAttempts
// -1, LS-K:L3208-L3239; lambda-stream-sources.md 5.3), and the reader
// moves on. Under poll-interval the last retry is the one at exactly 60 s.
func TestStreamRecordAgeEndsUnlimitedRetries(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.rec.setAnswer(alwaysFail)
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{MaximumRecordAgeInSeconds: new(int32(60))})
		h.start()
		h.stream.add(h.Clock.Now(), "a")
		h.quiesce()
		for range time.Minute / esm.DynamoDBRetryDelay {
			h.advancePolled(esm.DynamoDBRetryDelay)
		}
		atLimit := h.rec.count()
		if want := int(time.Minute/esm.DynamoDBRetryDelay) + 1; atLimit != want {
			h.Errorf("%d invocations by the 60 s mark, want %d", atLimit, want)
		}
		h.advancePolled(esm.DynamoDBRetryDelay)
		if n := h.rec.count(); n != atLimit {
			h.Errorf("%d invocations once the record passed its age, want %d", n, atLimit)
		}
		h.rec.setAnswer(nil)
		h.stream.add(h.Clock.Now(), "b")
		h.quiesce()
		if ids := h.rec.ids(); !slices.Equal(ids[len(ids)-1], []string{"b"}) {
			h.Errorf("after the discard the reader read %v, want [b]", ids[len(ids)-1])
		}
	})
}
