package journal

import (
	"context"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ref"
)

func timerEvent() Event {
	return Event{Scope: scope(), Detail: TimerFired{Key: clock.TimerKey{Kind: "sqs.visibility", ID: "orders/m-1"}}}
}

func callEvent(parent ref.EventID) Event {
	return Event{
		Cause: ref.Cause{Parent: parent},
		Scope: scope(),
		Detail: APICall{
			Op:     ref.OperationID{Service: "sqs", Name: "SendMessage"},
			Origin: ref.Internal{Via: ref.SNSDelivery},
		},
	}
}

// receive reads n events, failing if they do not arrive.
func receive(t *testing.T, ch <-chan Event, n int) []Event {
	t.Helper()
	out := make([]Event, 0, n)
	for range n {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed after %d of %d events", len(out), n)
			}
			out = append(out, e)
		case <-time.After(time.Minute):
			t.Fatalf("stream delivered %d of %d events", len(out), n)
		}
	}
	return out
}

// assertIdle fails if the stream has anything more to deliver.
func assertIdle(t *testing.T, ch <-chan Event) {
	t.Helper()
	synctest.Wait()
	select {
	case e := <-ch:
		t.Fatalf("stream delivered an unexpected %s event %d", KindOf(e.Detail), e.ID)
	default:
	}
}

func TestStreamResumesExactlyAfterTheGivenEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		j := newTestJournal(t, Options{})
		for range 10 {
			mustAppend(t, j, timerEvent())
		}
		ch := j.Stream(t.Context(), Filter{}, 4)
		if got := eventIDs(receive(t, ch, 6)); !slices.Equal(got, []ref.EventID{5, 6, 7, 8, 9, 10}) {
			t.Fatalf("replay after 4 = %v, want 5 through 10", got)
		}
		mustAppend(t, j, timerEvent())
		if got := receive(t, ch, 1)[0].ID; got != 11 {
			t.Fatalf("live event = %d, want 11", got)
		}
		assertIdle(t, ch)
	})
}

func TestStreamFollowsRecordingOrderAcrossNestedCalls(t *testing.T) {
	// A call's internal call ends first, so it is recorded first; a consumer
	// that saw the child resumes and still receives the parent.
	synctest.Test(t, func(t *testing.T) {
		j := newTestJournal(t, Options{})
		live := j.Stream(t.Context(), Filter{}, 0)
		parent := mustBegin(t, j, callEvent(0))
		child := mustBegin(t, j, callEvent(parent))
		if err := j.End(child, OK{}, time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if err := j.End(parent, OK{}, 2*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if got := eventIDs(receive(t, live, 2)); !slices.Equal(got, []ref.EventID{child, parent}) {
			t.Fatalf("live stream = %v, want child %d then parent %d", got, child, parent)
		}
		resumed := j.Stream(t.Context(), Filter{}, child)
		if got := receive(t, resumed, 1)[0]; got.ID != parent {
			t.Fatalf("resumed after the child = %d, want the parent %d", got.ID, parent)
		}
		assertIdle(t, resumed)
		assertIdle(t, j.Stream(t.Context(), Filter{}, parent))
	})
}

func TestStreamGivesSlowConsumerOneGapAndNeverBlocksRecording(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		j := newTestJournal(t, Options{StreamBuffer: 4})
		ch := j.Stream(t.Context(), Filter{}, 0)
		const recorded = 60
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range recorded / 2 {
				id, err := j.Begin(callEvent(0))
				if err != nil {
					t.Error(err)
					return
				}
				if err := j.End(id, OK{}, time.Millisecond); err != nil {
					t.Error(err)
					return
				}
				if _, err := j.Append(timerEvent()); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Begin, End or Append blocked on a stream consumer that is not reading")
		}

		var before []Event
		var gap Gap
		for gap.Missed == 0 {
			e := receive(t, ch, 1)[0]
			if d, ok := e.Detail.(Gap); ok {
				gap = d
			} else {
				before = append(before, e)
			}
		}
		if len(before) == 0 || uint64(len(before))+gap.Missed != recorded {
			t.Fatalf("received %d events then Gap{Missed: %d}; want some events and a gap accounting for all %d", len(before), gap.Missed, recorded)
		}
		for i, e := range eventIDs(before) {
			if e != ref.EventID(i+1) {
				t.Fatalf("events before the gap = %v, want the first %d recorded", eventIDs(before), len(before))
			}
		}
		assertIdle(t, ch)
		id := mustAppend(t, j, timerEvent())
		if got := receive(t, ch, 1)[0].ID; got != id {
			t.Fatalf("after the gap the stream delivered %d, want the new event %d", got, id)
		}
	})
}

// bigCall is a finished call whose input is a JSON document of about n bytes,
// as journal.bodies records an S3 or Lambda payload.
func bigCall(t *testing.T, n int) Event {
	t.Helper()
	input, err := NewRedacted([]byte(`{"Body":"` + strings.Repeat("a", n) + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	e := callEvent(0)
	call, ok := e.Detail.(APICall)
	if !ok {
		t.Fatal("callEvent is not an APICall")
	}
	call.Input, call.Outcome = input, OK{}
	e.Detail = call
	return e
}

func estimates(es []Event) int {
	n := 0
	for i := range es {
		n += Estimate(es[i])
	}
	return n
}

func TestStreamBoundsWhatAStalledConsumerHoldsInBytes(t *testing.T) {
	// Inputs just under the MaxBytes/16 cap: the event-count buffer alone
	// would let a stalled consumer hold StreamBuffer of them.
	synctest.Test(t, func(t *testing.T) {
		const maxBytes = 1 << 20
		j := newTestJournal(t, Options{MaxBytes: maxBytes})
		ch := j.Stream(t.Context(), Filter{}, 0)
		const recorded = 100
		mustRecord(t, j, bigCall(t, 60<<10))
		synctest.Wait() // the stream takes the first event and waits on the consumer
		for range recorded - 1 {
			mustRecord(t, j, bigCall(t, 60<<10))
		}

		var before []Event
		var gap Gap
		for gap.Missed == 0 {
			e := receive(t, ch, 1)[0]
			if d, ok := e.Detail.(Gap); ok {
				gap = d
			} else {
				before = append(before, e)
			}
		}
		if uint64(len(before))+gap.Missed != recorded {
			t.Fatalf("received %d events then Gap{Missed: %d}; want a gap accounting for all %d", len(before), gap.Missed, recorded)
		}
		if call, ok := before[0].Detail.(APICall); !ok || len(call.Input.JSON()) < 60<<10 {
			t.Fatalf("first event = %s; want the call with its whole input", KindOf(before[0].Detail))
		}
		// The first event was already handed over; the rest were buffered.
		if held := estimates(before[1:]); held > maxBytes/8 {
			t.Fatalf("the stream buffered %d events of %d bytes for a stalled consumer; want at most MaxBytes/8 = %d", len(before)-1, held, maxBytes/8)
		}
		assertIdle(t, ch)
		id := mustAppend(t, j, timerEvent())
		if got := receive(t, ch, 1)[0].ID; got != id {
			t.Fatalf("after the gap the stream delivered %d, want the new event %d", got, id)
		}
	})
}

func TestStalledConsumerOfMixedSizesGetsOneGap(t *testing.T) {
	// Large calls interleaved with small timer events: a small event that
	// still fits the byte bound after a large one was dropped must not reopen
	// the queue, or the consumer would see many Gaps.
	synctest.Test(t, func(t *testing.T) {
		j := newTestJournal(t, Options{MaxBytes: 1 << 20})
		ch := j.Stream(t.Context(), Filter{}, 0)
		mustRecord(t, j, bigCall(t, 60<<10))
		synctest.Wait() // the stream takes the first event and waits on the consumer
		recorded := uint64(1)
		for range 20 {
			mustRecord(t, j, bigCall(t, 60<<10))
			mustAppend(t, j, timerEvent())
			recorded += 2
		}

		var delivered, gaps, missed uint64
		for delivered+missed < recorded {
			e := receive(t, ch, 1)[0]
			if d, ok := e.Detail.(Gap); ok {
				gaps++
				missed += d.Missed
			} else {
				delivered++
			}
		}
		if gaps != 1 || delivered+missed != recorded {
			t.Fatalf("received %d events and %d Gaps missing %d; want exactly one Gap, accounting for all %d", delivered, gaps, missed, recorded)
		}
		assertIdle(t, ch)
	})
}

func TestStreamReplayBatchesStayUnderTheByteBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const maxBytes = 1 << 20
		j := newTestJournal(t, Options{MaxBytes: maxBytes})
		first := mustAppend(t, j, timerEvent())
		want := make([]ref.EventID, 0, 10)
		for range 10 {
			want = append(want, mustRecord(t, j, bigCall(t, 60<<10)))
		}
		j.mu.Lock()
		r := j.replayFrom(first)
		j.mu.Unlock()
		var got []ref.EventID
		for {
			batch := j.nextBatch(&r)
			if len(batch) == 0 {
				break
			}
			if held := estimates(batch); held > maxBytes/8 {
				t.Fatalf("a replay batch held %d events of %d bytes; want at most MaxBytes/8 = %d", len(batch), held, maxBytes/8)
			}
			got = append(got, eventIDs(batch)...)
		}
		if !slices.Equal(got, want) || r.missed != 0 {
			t.Fatalf("replayed %v with %d missed; want %v with none", got, r.missed, want)
		}
	})
}

func TestStreamDeliversAnEventLargerThanItsByteBound(t *testing.T) {
	// A principal far longer than MaxBytes/8 makes one event exceed what a
	// stream buffers; a consumer that reads still receives it, live and on
	// replay.
	synctest.Test(t, func(t *testing.T) {
		const maxBytes = 16 << 10
		j := newTestJournal(t, Options{MaxBytes: maxBytes})
		before := mustAppend(t, j, timerEvent())
		live := j.Stream(t.Context(), Filter{}, before)
		e := bigCall(t, 0)
		call, ok := e.Detail.(APICall)
		if !ok {
			t.Fatal("bigCall is not an APICall")
		}
		call.Principal = "arn:aws:iam::000000000000:role/" + strings.Repeat("r", 4<<10)
		e.Detail = call
		if size := Estimate(e); size <= maxBytes/8 {
			t.Fatalf("test event is %d bytes, want more than MaxBytes/8 = %d", size, maxBytes/8)
		}
		id := mustRecord(t, j, e)
		if got := receive(t, live, 1)[0].ID; got != id {
			t.Fatalf("live stream delivered %d, want the large event %d", got, id)
		}
		if got := receive(t, j.Stream(t.Context(), Filter{}, before), 1)[0].ID; got != id {
			t.Fatalf("replay delivered %d, want the large event %d", got, id)
		}
		assertIdle(t, live)
	})
}

func TestStreamResumingAfterAnEvictedEventStartsWithAGap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		j := newTestJournal(t, Options{Capacity: 3})
		for range 10 {
			mustAppend(t, j, timerEvent())
		}
		got := receive(t, j.Stream(t.Context(), Filter{}, 2), 4)
		if gap, ok := got[0].Detail.(Gap); !ok || gap.Missed != 5 {
			t.Fatalf("first event = %s, want Gap{Missed: 5} for evicted ids 3 through 7", detailName(got[0].Detail))
		}
		if ids := eventIDs(got[1:]); !slices.Equal(ids, []ref.EventID{8, 9, 10}) {
			t.Fatalf("replay = %v, want the retained 8, 9, 10", ids)
		}
	})
}

func TestStreamResumingAfterAnEvictedEventCountsInRecordingOrder(t *testing.T) {
	// A call is recorded when it ends, after its internal calls, so ids do
	// not follow recording order: recorded C, P, X with ids 2, 1, 3.
	synctest.Test(t, func(t *testing.T) {
		j := newTestJournal(t, Options{Capacity: 1})
		parent := mustBegin(t, j, callEvent(0))
		child := mustBegin(t, j, callEvent(parent))
		if err := j.End(child, OK{}, time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if err := j.End(parent, OK{}, 2*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		last := mustAppend(t, j, timerEvent())

		afterChild := j.Stream(t.Context(), Filter{}, child)
		got := receive(t, afterChild, 2)
		if gap, ok := got[0].Detail.(Gap); !ok || gap.Missed != 1 {
			t.Fatalf("resumed after the child: first event %s, want Gap{Missed: 1} for the evicted parent %d", detailName(got[0].Detail), parent)
		}
		if got[1].ID != last {
			t.Fatalf("resumed after the child: replayed %d, want %d", got[1].ID, last)
		}
		assertIdle(t, afterChild)

		afterParent := j.Stream(t.Context(), Filter{}, parent)
		if got := receive(t, afterParent, 1)[0]; got.ID != last {
			t.Fatalf("resumed after the parent: first event %s %d, want %d with no Gap, since the child was recorded before it", KindOf(got.Detail), got.ID, last)
		}
		assertIdle(t, afterParent)
	})
}

func TestStreamReplayGivesAGapForEventsEvictedBeforeItsTurn(t *testing.T) {
	// A consumer that resumes far behind and reads slowly does not hold the
	// ring's events alive: those evicted before the replay reaches them
	// arrive as one Gap.
	synctest.Test(t, func(t *testing.T) {
		j := newTestJournal(t, Options{Capacity: 200})
		for range 200 {
			mustAppend(t, j, timerEvent())
		}
		ch := j.Stream(t.Context(), Filter{}, 1)
		if got := receive(t, ch, 1)[0].ID; got != 2 {
			t.Fatalf("first replayed event = %d, want 2", got)
		}
		for range 150 { // evicts ids 1 through 150
			mustAppend(t, j, timerEvent())
		}
		// The first batch, ids 2 to replayBatch+1, was taken before the
		// evictions; the rest of the replay, up to 150, was evicted.
		var want []ref.EventID
		for id := ref.EventID(3); id <= 350; id++ {
			if id <= replayBatch+1 || id > 150 {
				want = append(want, id)
			}
		}
		var got []ref.EventID
		var gaps []uint64
		for len(got) < len(want) {
			e := receive(t, ch, 1)[0]
			if g, ok := e.Detail.(Gap); ok {
				gaps = append(gaps, g.Missed)
				continue
			}
			got = append(got, e.ID)
		}
		if !slices.Equal(got, want) || !slices.Equal(gaps, []uint64{150 - replayBatch - 1}) {
			t.Fatalf("after the evictions received %v with gaps %v; want %v with one gap of %d", got, gaps, want, 150-replayBatch-1)
		}
		assertIdle(t, ch)
	})
}

func TestStreamResumingAfterAForgottenEventCountsEveryEviction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		j := newTestJournal(t, Options{Capacity: 2})
		const recorded = minRememberedEvictions + 4
		for range recorded {
			mustAppend(t, j, timerEvent())
		}
		// Ids 1 to recorded-2 were evicted, more than the journal remembers,
		// so it no longer knows where id 1 was recorded.
		got := receive(t, j.Stream(t.Context(), Filter{}, 1), 3)
		if gap, ok := got[0].Detail.(Gap); !ok || gap.Missed != recorded-2 {
			t.Fatalf("first event = %s, want Gap{Missed: %d}, every evicted event", detailName(got[0].Detail), recorded-2)
		}
		if ids := eventIDs(got[1:]); !slices.Equal(ids, []ref.EventID{recorded - 1, recorded}) {
			t.Fatalf("replay = %v, want the retained %d, %d", ids, recorded-1, recorded)
		}
	})
}

func TestStreamDeliversOnlyMatchingEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		j := newTestJournal(t, Options{})
		mustAppend(t, j, timerEvent())
		ch := j.Stream(t.Context(), Filter{Kind: KindAPICall}, 1)
		id := mustBegin(t, j, callEvent(0))
		mustAppend(t, j, timerEvent())
		if err := j.End(id, OK{}, 0); err != nil {
			t.Fatal(err)
		}
		if got := receive(t, ch, 1)[0]; got.ID != id {
			t.Fatalf("filtered stream delivered event %d, want the call %d", got.ID, id)
		}
		assertIdle(t, ch)
	})
}

func TestStreamClosesAndUnsubscribesWhenContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		j := newTestJournal(t, Options{})
		ctx, cancel := context.WithCancel(t.Context())
		ch := j.Stream(ctx, Filter{}, 0)
		cancel()
		synctest.Wait()
		if _, ok := <-ch; ok {
			t.Fatal("stream still open after its context ended")
		}
		j.mu.Lock()
		subs := len(j.subs)
		j.mu.Unlock()
		if subs != 0 {
			t.Fatalf("%d subscribers remain after the stream ended", subs)
		}
	})
}
