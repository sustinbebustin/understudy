package lambda_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// A completely failing batch runs exactly MaximumRetryAttempts + 1
// invocations and then emits one RetryAttemptsExhausted record with that
// count (third-party-recorded: settings 1, 2 and 3 give 2, 3 and 4,
// lambda-stream-sources.md 5.2; property 12.10 item 4).
func TestStreamFailingBatchRunsRetriesPlusOneProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		retries32 := rapid.Int32Range(0, 4).Draw(rt, "retries")
		retries := int(retries32)
		size := rapid.IntRange(1, 5).Draw(rt, "records")
		var invocations int
		var counts []int
		runStream(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *streamHarness) {
			url, dlq := h.queue("dlq", nil)
			h.rec.setAnswer(alwaysFail)
			h.mapStream(lambdaapi.CreateEventSourceMappingInput{
				BatchSize: new(int32(10)), MaximumRetryAttempts: new(retries32),
				DestinationConfig: &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(dlq)}},
			})
			ids := make([]string, size)
			for i := range ids {
				ids[i] = strconv.Itoa(i)
			}
			h.stream.add(h.Clock.Now(), ids...)
			h.start()
			invocations = h.rec.count()
			for _, f := range h.failures(url) {
				if f.RequestContext.Condition == "RetryAttemptsExhausted" && f.Info.BatchSize == size {
					counts = append(counts, f.RequestContext.ApproximateInvokeCount)
				}
			}
		})
		if invocations != retries+1 {
			rt.Fatalf("%d invocations with MaximumRetryAttempts %d, want %d", invocations, retries, retries+1)
		}
		if !slices.Equal(counts, []int{retries + 1}) {
			rt.Fatalf("RetryAttemptsExhausted counts %v, want [%d]", counts, retries+1)
		}
	})
}

// After a partial batch response the reader resends from just before the
// lowest reported sequence number: the next invocation starts at that
// record and holds every later record of the batch ("Lambda uses the
// record with the lowest sequence number as the checkpoint. Lambda then
// retries all records starting from that checkpoint", aws-doc ddb-partial,
// lambda-stream-sources.md 5.5; property 12.10 item 3).
func TestStreamPartialResponseCheckpointsBeforeLowestProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n32 := rapid.Int32Range(1, 8).Draw(rt, "records")
		n := int(n32)
		reported := rapid.SliceOfNDistinct(rapid.IntRange(0, n-1), 1, n, rapid.ID[int]).Draw(rt, "reported")
		var got [][]string
		runStream(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *streamHarness) {
			first := true
			h.rec.setAnswer(func(streamEvent) runtimeapi.Outcome {
				if !first {
					return runtimeapi.Success{Payload: []byte("null")}
				}
				first = false
				items := make([]string, len(reported))
				for i, r := range reported {
					items[i] = `{"itemIdentifier":"` + seqAt(r) + `"}`
				}
				return runtimeapi.Success{Payload: []byte(`{"batchItemFailures":[` + strings.Join(items, ",") + `]}`)}
			})
			ids := make([]string, n)
			for i := range ids {
				ids[i] = strconv.Itoa(i)
			}
			h.stream.add(h.Clock.Now(), ids...)
			h.mapStream(lambdaapi.CreateEventSourceMappingInput{
				BatchSize: new(n32), FunctionResponseTypes: []lambdaapi.FunctionResponseType{"ReportBatchItemFailures"},
			})
			h.start()
			got = h.rec.ids()
		})
		lowest := slices.Min(reported)
		var want []string
		for i := lowest; i < n; i++ {
			want = append(want, strconv.Itoa(i))
		}
		if len(got) != 2 || !slices.Equal(got[1], want) {
			rt.Fatalf("invocations %v after reporting %v, want the resend %v", got, reported, want)
		}
	})
}

// Records sharing an item key reach the function in sequence order and a
// record is never invoked again after a batch holding it succeeded, under
// any pattern of failing invocations; the checkpoint the journal shows
// never moves backwards ("reads that shard's records in sequence-number
// order", aws-doc, lambda-stream-sources.md 4.3; spec A8).
func TestStreamOrderAndCheckpointProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(rt, "records")
		batch := rapid.Int32Range(1, 4).Draw(rt, "batchSize")
		fails := rapid.SliceOfN(rapid.Bool(), 0, 20).Draw(rt, "fails")
		var events [][]string
		var checkpoints []string
		runStream(t, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *streamHarness) {
			calls := 0
			h.rec.setAnswer(func(streamEvent) runtimeapi.Outcome {
				fail := calls < len(fails) && fails[calls]
				calls++
				if fail {
					return alwaysFail(streamEvent{})
				}
				return runtimeapi.Success{Payload: []byte("null")}
			})
			m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{BatchSize: new(batch)})
			h.start()
			for i := range n {
				h.stream.add(h.Clock.Now(), strconv.Itoa(i))
				h.quiesce()
			}
			h.rec.mu.Lock()
			for _, ev := range h.rec.events {
				seqs := make([]string, len(ev.Records))
				for j, r := range ev.Records {
					seqs[j] = seqOf(r)
				}
				events = append(events, seqs)
			}
			h.rec.mu.Unlock()
			for _, c := range stateChanges(h, *m.EventSourceMappingArn) {
				if _, to, found := strings.Cut(c, "->AFTER "); found {
					checkpoints = append(checkpoints, to)
				}
			}
		})
		committed := shardlog.Seq("0")
		delivered := map[string]bool{}
		for i, seqs := range events {
			for j, s := range seqs {
				if j > 0 && shardlog.Seq(s).Compare(shardlog.Seq(seqs[j-1])) != 1 {
					rt.Fatalf("invocation %d holds %v out of order", i, seqs)
				}
				if shardlog.Seq(s).Compare(committed) != 1 {
					rt.Fatalf("invocation %d holds %s at or below the checkpoint %s", i, s, committed)
				}
				delivered[s] = true
			}
			if i+1 < len(events) && events[i+1][0] != seqs[0] {
				committed = shardlog.Seq(seqs[len(seqs)-1])
			}
		}
		if len(delivered) != n {
			rt.Fatalf("%d records delivered, want all %d", len(delivered), n)
		}
		for i := 1; i < len(checkpoints); i++ {
			if shardlog.Seq(checkpoints[i]).Compare(shardlog.Seq(checkpoints[i-1])) < 0 {
				rt.Fatalf("checkpoint moved back: %v", checkpoints)
			}
		}
	})
}
