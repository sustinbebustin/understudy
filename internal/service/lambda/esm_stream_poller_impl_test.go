package lambda_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// sizes returns how many records each invocation held.
func (r *streamRecorder) sizes() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int, len(r.events))
	for i, ev := range r.events {
		out[i] = len(ev.Records)
	}
	return out
}

// The batching window opens when the reader starts and again when each
// invocation ends ("the next window begins as soon as the previous
// function invocation completes", aws-doc, lambda-stream-sources.md 4.2):
// a batch short of BatchSize waits for it to close.
func TestStreamBatchingWindowOpensAtStartAndAfterEachInvocation(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{BatchSize: new(int32(10)), MaximumBatchingWindowInSeconds: new(int32(5))})
		if got := derefAny(m.MaximumBatchingWindowInSeconds); got != int32(5) {
			h.Fatalf("MaximumBatchingWindowInSeconds = %v, want 5", got)
		}
		h.start()
		h.advancePolled(time.Second)
		h.stream.add(h.Clock.Now(), "a")
		h.quiesce()
		h.advancePolled(4*time.Second - time.Nanosecond)
		if n := h.rec.count(); n != 0 {
			h.Fatalf("%d invocations 1ns before the first window closes, want 0", n)
		}
		h.advancePolled(time.Nanosecond)
		if got := h.rec.ids(); !slices.EqualFunc(got, [][]string{{"a"}}, slices.Equal) {
			h.Fatalf("invocations %v when the first window closes, want [[a]]", got)
		}
		h.stream.add(h.Clock.Now(), "b")
		h.advancePolled(5*time.Second - time.Nanosecond)
		if n := h.rec.count(); n != 1 {
			h.Fatalf("%d invocations 1ns before the window after the invocation closes, want 1", n)
		}
		h.advancePolled(time.Nanosecond)
		if got, want := h.rec.ids(), [][]string{{"a"}, {"b"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations %v, want %v", got, want)
		}
	})
}

// A full batch does not wait for the window (aws-doc 4.2: "the batch size
// is met"); the rest waits for the window the invocation opened.
func TestStreamFullBatchSkipsTheWindow(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{BatchSize: new(int32(2)), MaximumBatchingWindowInSeconds: new(int32(60))})
		h.start()
		h.stream.add(h.Clock.Now(), "a", "b", "c")
		h.quiesce()
		if got := h.rec.ids(); !slices.EqualFunc(got, [][]string{{"a", "b"}}, slices.Equal) {
			h.Fatalf("invocations %v, want [[a b]] at once", got)
		}
		h.advancePolled(time.Minute)
		if got, want := h.rec.ids(), [][]string{{"a", "b"}, {"c"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations %v, want %v", got, want)
		}
	})
}

// A BatchSize above GetRecords' 1000 is filled from several reads
// (lambda-stream-sources.md 4.2; dynamodb-streams.md 6.4).
func TestStreamBatchSpansSeveralReads(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{BatchSize: new(int32(1500))})
		h.start()
		ids := make([]string, 1200)
		for i := range ids {
			ids[i] = "r"
		}
		h.stream.add(h.Clock.Now(), ids...)
		h.quiesce()
		if got := h.rec.sizes(); !slices.Equal(got, []int{1200}) {
			h.Errorf("invocation sizes %v, want one of 1200", got)
		}
	})
}

// A record that would take the serialized event past 6 MB starts the next
// batch, which is invoked at once (aws-doc 4.2: "The payload size reaches
// 6 MB"; counted over the serialized event, inferred, ESMS-18).
func TestStreamSixMegabyteCut(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{BatchSize: new(int32(10))})
		h.start()
		// Each record carries its id twice (Keys and NewImage): about 2.2 MB.
		big := strings.Repeat("x", 1_100_000)
		h.stream.add(h.Clock.Now(), big, big, big)
		h.quiesce()
		if got := h.rec.sizes(); !slices.Equal(got, []int{2, 1}) {
			h.Errorf("invocation sizes %v, want [2 1]", got)
		}
	})
}

// A stream found gone at the reader's first read stops the reader: one
// StreamGone entry, and no further reads when the stream is woken or time
// passes (lambda-stream-sources.md 7.3).
func TestStreamGoneReaderReadsNothingMore(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.stream.setGone()
		h.start()
		reads := func() int {
			return len(h.stream.callers("DescribeStream")) + len(h.stream.callers("GetShardIterator")) + len(h.stream.callers("GetRecords"))
		}
		before := reads()
		h.stream.add(h.Clock.Now(), "a")
		h.advancePolled(10 * time.Minute)
		if after := reads(); after != before {
			h.Errorf("%d stream calls after the reader stopped, want none", after-before)
		}
		gone := 0
		for _, c := range stateChanges(h, *m.EventSourceMappingArn) {
			if c == "Polling->StreamGone" {
				gone++
			}
		}
		if gone != 1 {
			h.Errorf("%d Polling->StreamGone entries, want 1", gone)
		}
		if got := derefAny(h.get(*m.UUID).State); got != "Enabled" {
			h.Errorf("State = %v, want Enabled", got)
		}
	})
}

// A batch being retried whose oldest record reaches the stream's 24 hours
// (dynamodb-streams.md 5.5) is dropped without an on-failure record and
// reading resumes at TRIM_HORIZON (aws-doc lambda-stream-sources.md 5.1,
// 5.3). The record's age is read from its
// ApproximateCreationDateTime, whole seconds.
func TestStreamRetriedBatchPastRetentionIsDropped(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		h.rec.setAnswer(alwaysFail)
		url, dlq := h.queue("dlq", nil)
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{
			DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(dlq)}},
		})
		h.start()
		written := h.Clock.Now()
		h.stream.add(written, "a")
		h.quiesce()
		arrival := clock.FromWire(written.Wire().Truncate(time.Second))
		if _, err := h.Clock.AdvanceTo(arrival.Add(esm.DynamoDBRetention - time.Nanosecond)); err != nil {
			h.Fatal(err)
		}
		h.quiesce()
		if n := h.rec.count(); n != 2 {
			h.Fatalf("%d invocations 1ns before retention, want the failure and its retry", n)
		}
		h.stream.trim(written)
		h.advancePolled(esm.DynamoDBRetryDelay)
		if n := h.rec.count(); n != 2 {
			h.Errorf("%d invocations, want the trimmed batch dropped, not retried", n)
		}
		changes := stateChanges(h, *m.EventSourceMappingArn)
		if !slices.Contains(changes, "Polling->RecordsTrimmed") || !slices.Contains(changes, "RecordsTrimmed->Polling") {
			h.Errorf("mapping state changes %v, want Polling->RecordsTrimmed and back", changes)
		}
		if got := h.failures(url); len(got) != 0 {
			h.Errorf("%d on-failure records for a trimmed batch, want none", len(got))
		}
		h.rec.setAnswer(nil)
		h.stream.add(h.Clock.Now(), "b")
		h.quiesce()
		if got := h.rec.ids(); !slices.Equal(got[len(got)-1], []string{"b"}) {
			h.Errorf("invocations %v, want the reader to go on with b", got)
		}
	})
}

// A mapping whose function is gone reads nothing and journals
// FunctionMissing, then delivers once the function exists again, as the
// SQS poller does (esm_runtime.go).
func TestStreamFunctionMissingThenBack(t *testing.T) {
	runStream(t, esm.StreamFidelity{}, func(h *streamHarness) {
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.start()
		call(h.harness, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "worker"})
		h.stream.add(h.Clock.Now(), "a")
		h.quiesce()
		if n := h.rec.count(); n != 0 {
			h.Fatalf("%d invocations without a function, want 0", n)
		}
		h.createFunction("worker")
		h.advancePolled(time.Second)
		if got := h.rec.ids(); !slices.EqualFunc(got, [][]string{{"a"}}, slices.Equal) {
			h.Errorf("invocations %v, want [[a]] once the function is back", got)
		}
		changes := stateChanges(h, *m.EventSourceMappingArn)
		if !slices.Contains(changes, "Polling->FunctionMissing") || !slices.Contains(changes, "FunctionMissing->Polling") {
			h.Errorf("mapping state changes %v, want Polling->FunctionMissing and back", changes)
		}
	})
}

// A LATEST mapping's checkpoint moves are journaled from LATEST.
func TestStreamCheckpointJournaledFromLatest(t *testing.T) {
	runStream(t, esm.StreamFidelity{LatestAnchor: esm.AnchorFirstRead}, func(h *streamHarness) {
		h.stream.add(h.Clock.Now(), "old")
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("LATEST"))})
		h.start()
		h.stream.add(h.Clock.Now(), "new")
		h.quiesce()
		changes := stateChanges(h, *m.EventSourceMappingArn)
		want := []string{"LATEST->AFTER " + seqAt(0), "AFTER " + seqAt(0) + "->AFTER " + seqAt(1)}
		for _, w := range want {
			if !slices.Contains(changes, w) {
				h.Errorf("mapping state changes %v, want %s", changes, w)
			}
		}
	})
}
