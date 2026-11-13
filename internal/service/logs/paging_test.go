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
			if (unpaged.NextToken != nil) != (len(unpaged.Events) > 0) {
				t.Fatalf("an unlimited listing of %d events carries nextToken %s; want one exactly when it has events", len(unpaged.Events), val(unpaged.NextToken))
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

// sevenEvents is the batch R10 step 05 pages: p1 to p7, one millisecond
// apart, the newest a millisecond before now.
func sevenEvents(now int64) []logsapi.InputLogEvent {
	batch := make([]logsapi.InputLogEvent, 7)
	for i := range batch {
		batch[i] = event(now-7+int64(i), fmt.Sprintf("p%d", i+1))
	}
	return batch
}

// FilterLogEvents hands out nextToken on every page with events, the last
// one included, and ends with an empty page that carries none, as AWS paged
// seven events by three (testdata/parity/logs/R10/05-filter-page-*.json).
func TestFilterLogEventsTokenOnEveryPageWithEvents(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		h.stream("g", "s3")
		h.put("g", "s3", sevenEvents(h.now())...)

		in := logsapi.FilterLogEventsInput{LogGroupName: new("g"), LogStreamNames: []string{"s3"}, Limit: new(int32(3))}
		want := [][]string{{"p1", "p2", "p3"}, {"p4", "p5", "p6"}, {"p7"}}
		for i, w := range want {
			page := must(h, logsapi.OpFilterLogEvents, &in)
			if got := filteredMessages(page.Events); !slices.Equal(got, w) || page.NextToken == nil {
				h.Fatalf("page %d = %v, nextToken %s; want %v and a token", i+1, got, val(page.NextToken), w)
			}
			in.NextToken = page.NextToken
		}
		last := must(h, logsapi.OpFilterLogEvents, &in)
		if len(last.Events) != 0 || last.NextToken != nil {
			h.Errorf("page 4 = %v, nextToken %s; want no events and no token", filteredMessages(last.Events), val(last.NextToken))
		}
	})
}

// A FilterLogEvents whose matches all fit on the first page still carries
// nextToken, and the page it reads is empty and carries none
// (testdata/parity/logs/R10/03-filter-*.json).
func TestFilterLogEventsSinglePageCarriesAToken(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		h.stream("g", "s")
		now := h.now()
		h.put("g", "s", event(now-2, "ERROR a"), event(now-1, "INFO b"))

		in := logsapi.FilterLogEventsInput{LogGroupName: new("g"), FilterPattern: new("ERROR")}
		first := must(h, logsapi.OpFilterLogEvents, &in)
		if got := filteredMessages(first.Events); !slices.Equal(got, []string{"ERROR a"}) || first.NextToken == nil {
			h.Fatalf("first page = %v, nextToken %s; want [ERROR a] and a token", got, val(first.NextToken))
		}
		in.NextToken = first.NextToken
		next := must(h, logsapi.OpFilterLogEvents, &in)
		if len(next.Events) != 0 || next.NextToken != nil {
			h.Errorf("next page = %v, nextToken %s; want no events and no token", filteredMessages(next.Events), val(next.NextToken))
		}
	})
}

// GetLogEvents read forward to the end: the first empty page hands out a new
// forward token, and the empty page it reads repeats that token, so the
// token stops changing one call after the last events, as AWS paged seven
// events by three (testdata/parity/logs/R10/05-get-page-*.json).
func TestGetLogEventsForwardTokenSettlesAfterTheFirstEmptyPage(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		h.stream("g", "s3")
		h.put("g", "s3", sevenEvents(h.now())...)

		in := logsapi.GetLogEventsInput{LogGroupName: new("g"), LogStreamName: "s3", Limit: new(int32(3)), StartFromHead: new(true)}
		want := [][]string{{"p1", "p2", "p3"}, {"p4", "p5", "p6"}, {"p7"}}
		for i, w := range want {
			page := h.get(&in)
			if got := messages(page.Events); !slices.Equal(got, w) {
				h.Fatalf("page %d = %v, want %v", i+1, got, w)
			}
			in.NextToken = page.NextForwardToken
		}
		third := val(in.NextToken)
		fourth := h.get(&in)
		if len(fourth.Events) != 0 || fourth.NextForwardToken == nil || *fourth.NextForwardToken == third {
			h.Fatalf("page 4 = %v, forward token %s; want no events and a token other than page 3's %s", messages(fourth.Events), val(fourth.NextForwardToken), third)
		}
		in.NextToken = fourth.NextForwardToken
		fifth := h.get(&in)
		if len(fifth.Events) != 0 || val(fifth.NextForwardToken) != *fourth.NextForwardToken {
			h.Errorf("page 5 = %v, forward token %s; want no events and page 4's token %s", messages(fifth.Events), val(fifth.NextForwardToken), *fourth.NextForwardToken)
		}
	})
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
