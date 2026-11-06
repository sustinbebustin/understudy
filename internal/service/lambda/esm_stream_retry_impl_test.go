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

// The ReportBatchItemFailures matrix of lambda-stream-sources.md 5.5
// (third-party-recorded LS-D:L2715-L4308, MaximumRetryAttempts 3): the
// success responses invoke once, an empty JSON list among them; the
// malformed ones and an exception run four invocations.
func TestStreamReportBatchItemFailuresMatrixWithoutDestination(t *testing.T) {
	for _, c := range []struct {
		name     string
		response string // "" for an exception
		want     int
	}{
		{"empty list", `[]`, 1},
		{"null", `null`, 1},
		{"empty object", `{}`, 1},
		{"empty batchItemFailures", `{"batchItemFailures":[]}`, 1},
		{"null batchItemFailures", `{"batchItemFailures":null}`, 1},
		{"empty itemIdentifier", `{"batchItemFailures":[{"itemIdentifier":""}]}`, 4},
		{"null itemIdentifier", `{"batchItemFailures":[{"itemIdentifier":null}]}`, 4},
		{"bad key", `{"batchItemFailures":[{"foo":123}]}`, 4},
		{"bad key null", `{"batchItemFailures":[{"foo":null}]}`, 4},
		{"unknown identifier", `{"batchItemFailures":[{"itemIdentifier":123}]}`, 4},
		{"exception", "", 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			runStream(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *streamHarness) {
				h.rec.setAnswer(func(streamEvent) runtimeapi.Outcome {
					if c.response == "" {
						return alwaysFail(streamEvent{})
					}
					return runtimeapi.Success{Payload: []byte(c.response)}
				})
				h.mapStream(lambdaapi.CreateEventSourceMappingInput{
					BatchSize: new(int32(1)), MaximumRetryAttempts: new(int32(3)),
					FunctionResponseTypes: []lambdaapi.FunctionResponseType{"ReportBatchItemFailures"},
				})
				h.start()
				h.stream.add(h.Clock.Now(), "a")
				h.quiesce()
				if n := h.rec.count(); n != c.want {
					h.Errorf("%d invocations, want %d", n, c.want)
				}
			})
		})
	}
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

// The partial-resend budget without a destination: with BatchSize 3,
// MaximumRetryAttempts 3 and the sixth of six records always reported,
// records 1 to 5 are delivered once and the sixth five times under restart
// (as the recording's logs show, LS-D:L2443-L2551) or four under shared
// (lambda.esm-stream.partial-resend-budget, inferred, ESMS-08;
// lambda-stream-sources.md 5.5). Either way the reader then moves on.
func TestStreamPartialResendBudgetWithoutDestination(t *testing.T) {
	for _, budget := range []esm.ResendBudget{esm.ResendRestart, esm.ResendShared} {
		t.Run(budget.String(), func(t *testing.T) {
			runStream(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately, ResendBudget: budget}, func(h *streamHarness) {
				sixth := seqAt(5)
				h.rec.setAnswer(func(ev streamEvent) runtimeapi.Outcome {
					for _, r := range ev.Records {
						if seqOf(r) == sixth {
							return runtimeapi.Success{Payload: []byte(`{"batchItemFailures":[{"itemIdentifier":"` + sixth + `"}]}`)}
						}
					}
					return runtimeapi.Success{Payload: []byte("null")}
				})
				h.stream.add(h.Clock.Now(), "1", "2", "3", "4", "5", "6")
				h.mapStream(lambdaapi.CreateEventSourceMappingInput{
					BatchSize: new(int32(3)), MaximumRetryAttempts: new(int32(3)),
					FunctionResponseTypes: []lambdaapi.FunctionResponseType{"ReportBatchItemFailures"},
				})
				h.start()
				counts := map[string]int{}
				for _, ids := range h.rec.ids() {
					for _, id := range ids {
						counts[id]++
					}
				}
				want := map[string]int{"1": 1, "2": 1, "3": 1, "4": 1, "5": 1, "6": 5}
				if budget == esm.ResendShared {
					want["6"] = 4
				}
				for id, n := range want {
					if counts[id] != n {
						h.Errorf("record %s delivered %d times, want %d", id, counts[id], n)
					}
				}
				h.stream.add(h.Clock.Now(), "7")
				h.quiesce()
				if ids := h.rec.ids(); !slices.Equal(ids[len(ids)-1], []string{"7"}) {
					h.Errorf("after the discard the reader read %v, want [7]", ids[len(ids)-1])
				}
			})
		})
	}
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
