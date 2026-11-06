package journal

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/ref"
)

// Tail is the last n events of every Query(f) page put together: the newest
// n retained and open events f matches, pending calls included as Query
// includes them (doc.go), in ascending id order, whatever f.Limit and
// f.Cursor say; nil for n <= 0.
func TestTailIsTheNewestOfQuery(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		j := newTestJournal(t, Options{Capacity: rapid.IntRange(1, 40).Draw(t, "capacity")})
		next := ref.EventID(1)
		var open []ref.EventID
		header := func(t *rapid.T) Event {
			return Event{
				Cause: ref.Cause{Parent: genEventID(next-1).Draw(t, "parent")},
				At:    genAt().Draw(t, "at"),
				Scope: scope(),
			}
		}
		steps := rapid.IntRange(0, 50).Draw(t, "steps")
		for range steps {
			switch rapid.IntRange(0, 2).Draw(t, "op") {
			case 0:
				e := header(t)
				e.Detail = genAppendable().Draw(t, "detail")
				mustAppend(t, j, e)
				next++
			case 1:
				e := header(t)
				call := genAPICall(genText(), genOutcome()).Draw(t, "call")
				call.Outcome, call.Latency = Pending{}, 0
				e.Detail = call
				open = append(open, mustBegin(t, j, e))
				next++
			case 2:
				if len(open) == 0 {
					continue
				}
				i := rapid.IntRange(0, len(open)-1).Draw(t, "end")
				if err := j.End(open[i], genEnded().Draw(t, "outcome"), 0); err != nil {
					t.Fatalf("End(%d): %v", open[i], err)
				}
				open = slices.Delete(open, i, i+1)
			}
		}
		for i := range 6 {
			f := genFilter(next).Draw(t, fmt.Sprintf("filter%d", i))
			f.Cursor = genEventID(next).Draw(t, fmt.Sprintf("cursor%d", i))
			n := rapid.IntRange(-2, 60).Draw(t, fmt.Sprintf("n%d", i))
			got := j.Tail(f, n)
			if n <= 0 {
				if got != nil {
					t.Fatalf("Tail(%+v, %d) = ids %v, want nil", f, n, eventIDs(got))
				}
				continue
			}
			every := f
			every.Limit, every.Cursor = 0, 0
			all := queryAll(t, j, every)
			want := all[max(0, len(all)-n):]
			if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
				t.Fatalf("Tail(%+v, %d)\n got ids %v\nwant ids %v", f, n, eventIDs(got), eventIDs(want))
			}
		}
	})
}
