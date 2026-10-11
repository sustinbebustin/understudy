package logs_test

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// bubbleTB hides *testing.T from rapid, which would otherwise call
// T.Deadline, forbidden inside a synctest bubble.
type bubbleTB struct{ *testing.T }

// stored is one event a property put, with its stream and the order of its
// ingestion, which breaks timestamp ties in every listing.
type stored struct {
	stream  string
	ts      int64
	message string
	order   int
}

// pagingFixture is a group filled with events whose timestamps collide,
// put in several batches so ingestion order differs from timestamp order.
type pagingFixture struct {
	group  string
	events []stored
}

func newPagingFixture(t *rapid.T, h *harness, group string, streams []string) pagingFixture {
	h.group(group)
	for _, s := range streams {
		h.stream(group, s)
	}
	f := pagingFixture{group: group}
	now := h.now()
	batches := rapid.IntRange(0, 6).Draw(t, "batches")
	for range batches {
		stream := rapid.SampledFrom(streams).Draw(t, "stream")
		offsets := rapid.SliceOfN(rapid.Int64Range(0, 15), 1, 12).Draw(t, "offsets")
		slices.Sort(offsets)
		batch := make([]logsapi.InputLogEvent, len(offsets))
		for i, off := range offsets {
			n := len(f.events)
			kind := "odd"
			if n%2 == 0 {
				kind = "even"
			}
			e := stored{stream: stream, ts: now - 15 + off, message: fmt.Sprintf("e%d %s", n, kind), order: n}
			f.events = append(f.events, e)
			batch[i] = event(e.ts, e.message)
		}
		h.put(group, stream, batch...)
	}
	return f
}

// listing is the messages of the events keep selects, in timestamp then
// ingestion order: the unpaginated answer.
func (f *pagingFixture) listing(keep func(stored) bool) []string {
	sel := slices.DeleteFunc(slices.Clone(f.events), func(e stored) bool { return !keep(e) })
	slices.SortStableFunc(sel, func(a, b stored) int {
		if c := cmp.Compare(a.ts, b.ts); c != 0 {
			return c
		}
		return cmp.Compare(a.order, b.order)
	})
	out := make([]string, len(sel))
	for i, e := range sel {
		out[i] = e.message
	}
	return out
}

// maxPages bounds a paging loop: a listing of n events in pages of at
// least one event ends within n+2 calls.
func maxPages(f *pagingFixture) int { return len(f.events) + 2 }

// GetLogEvents pages, followed in either direction until the token comes
// back unchanged, concatenate to the unpaginated listing of the window
// [startTime, endTime).
func TestGetLogEventsPagesConcatenateToTheListing(t *testing.T) {
	run(t, func(h *harness) {
		groups := 0
		rapid.Check(bubbleTB{h.T}, func(t *rapid.T) {
			groups++
			f := newPagingFixture(t, h, fmt.Sprintf("get-%d", groups), []string{"s"})
			now := h.now()
			in := logsapi.GetLogEventsInput{LogGroupName: new(f.group), LogStreamName: "s"}
			lo, hi := int64(0), int64(1<<62)
			if rapid.Bool().Draw(t, "bounded") {
				lo = now - 15 + rapid.Int64Range(0, 16).Draw(t, "start")
				hi = lo + rapid.Int64Range(0, 16).Draw(t, "span")
				in.StartTime, in.EndTime = new(lo), new(hi)
			}
			want := f.listing(func(e stored) bool { return e.ts >= lo && e.ts < hi })
			in.Limit = new(rapid.Int32Range(1, 5).Draw(t, "limit"))
			forward := rapid.Bool().Draw(t, "forward")
			in.StartFromHead = new(forward)

			var got []string
			done := false
			for range maxPages(&f) {
				page := h.get(&in)
				passed, next := in.NextToken, page.NextBackwardToken
				if forward {
					got = append(got, messages(page.Events)...)
					next = page.NextForwardToken
				} else {
					got = append(messages(page.Events), got...)
				}
				if passed != nil && val(next) == *passed {
					done = true
					break
				}
				in.NextToken = next
			}
			if !done {
				t.Fatalf("paging did not terminate within %d calls", maxPages(&f))
			}
			if !slices.Equal(got, want) {
				t.Fatalf("pages (forward %v, limit %d) = %v\nlisting = %v", forward, *in.Limit, got, want)
			}
		})
	})
}

// FilterLogEvents pages, followed until nextToken is absent, concatenate to
// the unpaginated listing of the matching events of the selected streams in
// [startTime, endTime], oldest first, or newest first when startFromHead is
// false.
func TestFilterLogEventsPagesConcatenateToTheListing(t *testing.T) {
	run(t, func(h *harness) {
		groups := 0
		rapid.Check(bubbleTB{h.T}, func(t *rapid.T) {
			groups++
			f := newPagingFixture(t, h, fmt.Sprintf("filter-%d", groups), []string{"a", "b", "c"})
			now := h.now()
			lo := now - 15 + rapid.Int64Range(0, 8).Draw(t, "start")
			hi := lo + rapid.Int64Range(0, 16).Draw(t, "span")
			pattern := rapid.SampledFrom([]string{"", "even", "-even", "?e1 ?e2"}).Draw(t, "pattern")
			// No stream names selects every stream; the model takes at least
			// one name when the member is sent.
			var streams []string
			if rapid.Bool().Draw(t, "named") {
				streams = rapid.SliceOfNDistinct(rapid.SampledFrom([]string{"a", "b", "c"}), 1, 3, rapid.ID).Draw(t, "streams")
			}
			in := logsapi.FilterLogEventsInput{
				LogGroupName: new(f.group), LogStreamNames: streams, FilterPattern: new(pattern),
				StartTime: new(lo), EndTime: new(hi),
			}
			unpaged := must(h, logsapi.OpFilterLogEvents, &in)
			if unpaged.NextToken != nil {
				t.Fatalf("an unlimited listing of %d events carries nextToken %s", len(unpaged.Events), *unpaged.NextToken)
			}
			want := f.listing(func(e stored) bool {
				return e.ts >= lo && e.ts <= hi && (len(streams) == 0 || slices.Contains(streams, e.stream)) && filterMatches(pattern, e.message)
			})
			if got := filteredMessages(unpaged.Events); !slices.Equal(got, want) {
				t.Fatalf("listing = %v, want %v", got, want)
			}

			forward := rapid.Bool().Draw(t, "forward")
			in.StartFromHead = new(forward)
			if !forward {
				slices.Reverse(want)
			}
			in.Limit = new(rapid.Int32Range(1, 5).Draw(t, "limit"))
			var got []string
			done := false
			for range maxPages(&f) {
				page := must(h, logsapi.OpFilterLogEvents, &in)
				got = append(got, filteredMessages(page.Events)...)
				if page.NextToken == nil {
					done = true
					break
				}
				in.NextToken = page.NextToken
			}
			if !done {
				t.Fatalf("paging did not terminate within %d calls", maxPages(&f))
			}
			if !slices.Equal(got, want) {
				t.Fatalf("pages (forward %v, limit %d) = %v\nlisting = %v", forward, *in.Limit, got, want)
			}
		})
	}, kerneltest.Mode(clock.Frozen{At: clock.FromWire(backwardEra)}))
}

// backwardEra is an emulator instant FilterLogEvents can read backward
// from: startFromHead=false takes only a startTime from 2024 on.
var backwardEra = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// filterMatches is the property's own reading of its few patterns over the
// fixture messages "e<n> even" and "e<n> odd".
func filterMatches(pattern, message string) bool {
	switch pattern {
	case "even":
		return strings.Contains(message, "even")
	case "-even":
		return !strings.Contains(message, "even")
	case "?e1 ?e2":
		return strings.Contains(message, "e1") || strings.Contains(message, "e2")
	}
	return true
}

func filteredMessages(events []logsapi.FilteredLogEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = val(e.Message)
	}
	return out
}
