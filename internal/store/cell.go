package store

import (
	"sync"
	"sync/atomic"
)

// Cell holds one partition's state behind a mutex. Cells are created by
// Partitioned (For and Import); the zero value is not usable.
type Cell[S any] struct {
	gate *Gate
	mu   sync.Mutex
	s    S
	rev  uint64
	// retired is set, under Partitioned's map lock, in the same step that
	// drops the cell from its Partitioned.
	retired atomic.Bool
}

// Update runs fn under the gate's shared side and the cell mutex. When fn
// reports mutated, the cell's revision advances; a call that changed nothing
// leaves it as it was, so revisions track real changes ("dirty" state).
//
// It is the primitive kernel.Write builds on; handlers never call it directly.
// R must never alias S: return values or explicit clones. A caller that holds
// on to a cell checks Retired inside fn (see Retired).
func Update[S, R any](c *Cell[S], fn func(s *S) (r R, mutated bool)) R {
	c.gate.Enter()
	defer c.gate.Leave()
	c.mu.Lock()
	defer c.mu.Unlock()
	r, mutated := fn(&c.s)
	if mutated {
		c.rev++
	}
	return r
}

// Read runs fn under the gate's shared side and the cell mutex without
// changing the revision. fn must not mutate the state, and R must never alias
// S: return values or explicit clones.
func Read[S, R any](c *Cell[S], fn func(s *S) R) R {
	c.gate.Enter()
	defer c.gate.Leave()
	c.mu.Lock()
	defer c.mu.Unlock()
	return fn(&c.s)
}

// Rev reports how many Updates have mutated the cell since it was created.
func (c *Cell[S]) Rev() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rev
}

// Retired reports whether Partitioned.Delete, Reset or Import has dropped or
// replaced this cell. A retired cell is unreachable through For, Lookup, Keys
// and Export, so anything written to it is lost.
//
// A caller that keeps a cell across calls, such as a long poll, checks Retired
// at the start of its Update or Read fn. A write whose fn saw false is ordered
// before the retirement, exactly as if it had completed just before the
// Delete, Reset or Import. On true, fn changes nothing and the caller
// re-resolves the cell with For and tries again. Retired takes no lock, so it
// is safe to call from inside fn.
func (c *Cell[S]) Retired() bool {
	return c.retired.Load()
}
