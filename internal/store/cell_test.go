package store_test

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/store"
)

type counter struct {
	N   int   `json:"n"`
	Log []int `json:"log,omitempty"`
}

func jsonCodec() store.Codec {
	return store.Codec{Marshal: json.Marshal, Unmarshal: json.Unmarshal}
}

func newCounterCell(g *store.Gate) *store.Cell[counter] {
	p := store.NewPartitioned(g, jsonCodec(), func(string) counter { return counter{} })
	return p.For("k")
}

func TestUpdateAdvancesRevOnlyWhenMutated(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		c := newCounterCell(&store.Gate{})
		ops := rapid.SliceOf(rapid.Bool()).Draw(t, "mutations")
		var want uint64
		for _, mutate := range ops {
			before := c.Rev()
			got := store.Update(c, func(s *counter) (int, bool) {
				if mutate {
					s.N++
				}
				return s.N, mutate
			})
			if mutate {
				want++
			}
			if c.Rev() != want {
				t.Fatalf("Rev() = %d after mutated=%v from %d, want %d", c.Rev(), mutate, before, want)
			}
			if got != int(want) {
				t.Fatalf("Update result = %d, want %d", got, want)
			}
		}
	})
}

func TestReadDoesNotAdvanceRev(t *testing.T) {
	t.Parallel()
	c := newCounterCell(&store.Gate{})
	store.Update(c, func(s *counter) (struct{}, bool) { s.N = 7; return struct{}{}, true })
	got := store.Read(c, func(s *counter) int { return s.N })
	if got != 7 || c.Rev() != 1 {
		t.Fatalf("Read = %d, Rev = %d; want 7 and 1", got, c.Rev())
	}
}

// Results are copies: a reader walks a cloned slice outside the cell while
// writers rewrite every element in place, so returning the state's own slice
// would be a data race the race detector reports.
func TestConcurrentUpdateAndReadReturnCopies(t *testing.T) {
	t.Parallel()
	const writers, readers, rounds = 4, 4, 200
	c := newCounterCell(&store.Gate{})
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			for range rounds {
				store.Update(c, func(s *counter) (struct{}, bool) {
					s.N++
					s.Log = append(s.Log, 0)
					for i := range s.Log {
						s.Log[i] = s.N
					}
					return struct{}{}, true
				})
			}
		})
	}
	for range readers {
		wg.Go(func() {
			for range rounds {
				log := store.Read(c, func(s *counter) []int { return slices.Clone(s.Log) })
				for i, v := range log {
					if v != len(log) {
						t.Errorf("log[%d] = %d, want every entry equal to the length %d", i, v, len(log))
						return
					}
				}
			}
		})
	}
	wg.Wait()
	if got := c.Rev(); got != writers*rounds {
		t.Fatalf("Rev() = %d, want %d", got, writers*rounds)
	}
}

func TestExclusiveReleaseTwiceIsANoOp(t *testing.T) {
	t.Parallel()
	g := &store.Gate{}
	release := g.Exclusive()
	release()
	release()
	again := g.Exclusive()
	again()
}
