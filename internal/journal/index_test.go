package journal

import (
	"maps"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/ref"
)

func TestIDListBehavesAsSortedSet(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var l idList
		model := map[ref.EventID]struct{}{}
		id := rapid.Custom(func(t *rapid.T) ref.EventID {
			return ref.EventID(rapid.Uint64Range(1, 64).Draw(t, "id"))
		})
		for range rapid.IntRange(0, 200).Draw(t, "steps") {
			v := id.Draw(t, "v")
			if rapid.Bool().Draw(t, "insert") {
				l = l.insert(v)
				model[v] = struct{}{}
			} else {
				l = l.remove(v)
				delete(model, v)
			}
			if want := slices.Sorted(maps.Keys(model)); !slices.Equal(l, want) {
				t.Fatalf("list = %v, want %v", l, want)
			}
			from := id.Draw(t, "from")
			want := slices.DeleteFunc(slices.Sorted(maps.Keys(model)), func(x ref.EventID) bool { return x < from })
			if got := l.from(from); !slices.Equal(got, want) {
				t.Fatalf("from(%d) = %v, want %v", from, got, want)
			}
		}
	})
}
