package admin_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// tailIDs reads GET /journal/events with query and returns the event ids
// and whether the page carried a next member.
func tailIDs(k *kerneltest.K, h http.Handler, query string) (ids []uint64, hasNext bool) {
	k.Helper()
	rec := serve(k, h, http.MethodGet, admin.Prefix+"/journal/events?"+query)
	if rec.Code != http.StatusOK {
		k.Fatalf("GET journal/events?%s = %d %s, want 200", query, rec.Code, rec.Body)
	}
	var page admin.JournalPage
	var members map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		k.Fatal(err)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &members); err != nil {
		k.Fatal(err)
	}
	for i := range page.Events {
		ids = append(ids, page.Events[i].ID)
	}
	_, hasNext = members["next"]
	return ids, hasNext
}

// held returns the ids of every held event f selects, in id order.
func held(k *kerneltest.K, f journal.Filter) []uint64 {
	k.Helper()
	f.Limit = 1000
	events := k.Journal.Query(f).Events
	out := make([]uint64, 0, len(events))
	for i := range events {
		out = append(out, uint64(events[i].ID))
	}
	return out
}

// lastN is the last n of ids.
func lastN(ids []uint64, n int) []uint64 { return ids[max(0, len(ids)-n):] }

// tail=N answers the newest N events the filters select, oldest first, in
// one page with no next (approved addition: the journal tail the UI loads
// before following the stream).
func TestJournalTailAnswersTheNewestEvents(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		for i := range 7 {
			var d journal.Detail = journal.TimerFired{Key: clock.TimerKey{Kind: "test.tick", ID: string(rune('a' + i))}}
			if i%3 == 2 {
				d = clockEvent().Detail
			}
			if _, err := k.Journal.Append(journal.Event{Detail: d}); err != nil {
				k.Fatal(err)
			}
		}
		all := held(k, journal.Filter{})
		timers := held(k, journal.Filter{Kind: journal.KindTimerFired})
		for _, tc := range []struct {
			query string
			want  []uint64
		}{
			{"tail=3", lastN(all, 3)},
			{"tail=1", lastN(all, 1)},
			{"tail=100", all},
			{"tail=2&kind=timer-fired", lastN(timers, 2)},
			{"kind=timer-fired&tail=100", timers},
		} {
			got, hasNext := tailIDs(k, h, tc.query)
			if !slices.Equal(got, tc.want) || hasNext {
				k.Errorf("GET journal/events?%s = ids %v (next %t), want %v and no next", tc.query, got, hasNext, tc.want)
			}
		}
	})
}

// tail takes a positive decimal integer, as limit does, and combines with
// neither limit nor cursor; each refusal names the field. The stream takes
// no tail.
func TestJournalTailRefusals(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		for _, query := range []string{"tail=0", "tail=-1", "tail=abc", "tail=1.5", "tail=2&limit=1", "limit=1&tail=2", "tail=2&cursor=1"} {
			rec := serve(k, h, http.MethodGet, admin.Prefix+"/journal/events?"+query)
			if e := envelope(k, rec); rec.Code != http.StatusBadRequest || e.Code != admin.InvalidRequest || e.Field != "tail" {
				k.Errorf("GET journal/events?%s = %d %+v, want 400 invalid_request with field tail", query, rec.Code, e)
			}
		}
		rec := serve(k, h, http.MethodGet, admin.Prefix+"/journal/stream?tail=1")
		if e := envelope(k, rec); rec.Code != http.StatusBadRequest || e.Code != admin.InvalidRequest {
			k.Errorf("GET journal/stream?tail=1 = %d %+v, want 400 invalid_request", rec.Code, e)
		}
	})
}
