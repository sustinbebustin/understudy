package launcher

import (
	"context"
	"sync"
)

// flights runs at most one call per key at a time: a caller arriving while
// its key is in flight waits for that call's result instead of starting
// another. The call runs under a context of its own, so one caller giving
// up does not fail the others; it is cancelled once every caller waiting
// for it has gone, and a later caller then starts afresh.
type flights[K comparable, V any] struct {
	mu    sync.Mutex
	calls map[K]*flight[V]
}

// flight is one call in progress. val and err are written before done
// closes.
type flight[V any] struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	val     V
	err     error
}

// do returns fn's result for key, sharing a call already in flight. It
// returns ctx's error as soon as ctx ends, whatever the call is doing.
func (g *flights[K, V]) do(ctx context.Context, key K, fn func(context.Context) (V, error)) (V, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = map[K]*flight[V]{}
	}
	f, ok := g.calls[key]
	if !ok {
		callCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		f = &flight[V]{done: make(chan struct{}), cancel: cancel}
		g.calls[key] = f
		go func() {
			defer close(f.done)
			defer cancel()
			f.val, f.err = fn(callCtx)
			g.mu.Lock()
			g.forgetLocked(key, f)
			g.mu.Unlock()
		}()
	}
	f.waiters++
	g.mu.Unlock()
	select {
	case <-f.done:
		return f.val, f.err
	case <-ctx.Done():
		g.mu.Lock()
		f.waiters--
		if f.waiters == 0 {
			f.cancel()
			g.forgetLocked(key, f)
		}
		g.mu.Unlock()
		var zero V
		return zero, ctx.Err()
	}
}

// doReporting is do for a call that reports progress. The call reports to
// the progress callback of the caller that started it, and only while that
// caller waits: a shared call outlives its starter, whose callback may
// write to a response whose handler has returned. Once doReporting
// returns, its progress callback is never called again.
func doReporting[K comparable, V, P any](ctx context.Context, g *flights[K, V], key K, progress func(P), fn func(ctx context.Context, report func(P)) (V, error)) (V, error) {
	r := &relay[P]{to: progress}
	defer r.detach()
	return g.do(ctx, key, func(ctx context.Context) (V, error) { return fn(ctx, r.report) })
}

// relay passes reports to a callback until it is detached.
type relay[P any] struct {
	mu sync.Mutex
	to func(P)
}

// report calls the callback unless the relay is detached. It holds the
// lock through the call, so detach waits for a report in progress.
func (r *relay[P]) report(p P) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.to != nil {
		r.to(p)
	}
}

func (r *relay[P]) detach() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.to = nil
}

// forgetLocked removes f if it is still key's call. Callers hold g.mu.
func (g *flights[K, V]) forgetLocked(key K, f *flight[V]) {
	if g.calls[key] == f {
		delete(g.calls, key)
	}
}
