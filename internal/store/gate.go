package store

import "sync"

// Gate is shared by every cell in a kernel. Writes and reads take its shared
// side for the microseconds they hold a cell; the snapshot cut, reset and load
// take its exclusive side and so see a consistent cut of every cell at once.
//
// The zero value is ready to use. A Gate must not be copied after first use.
type Gate struct {
	mu sync.RWMutex
}

// Enter takes the shared side of the gate. Every Enter is paired with one
// Leave.
func (g *Gate) Enter() {
	g.mu.RLock()
}

// TryEnter takes the shared side of the gate without waiting, and reports
// whether it did: it fails while the exclusive side is held or awaited. A
// true result is paired with one Leave. It lets a caller observe a held gate
// without blocking on it, which a test cannot do with Enter.
func (g *Gate) TryEnter() bool {
	return g.mu.TryRLock()
}

// Leave releases the shared side taken by Enter.
func (g *Gate) Leave() {
	g.mu.RUnlock()
}

// Exclusive takes the exclusive side of the gate and returns the function that
// releases it. It waits for every in-flight Update and Read to finish, and no
// new one starts until release runs. Nothing does I/O while holding it.
// Calling release again does nothing.
func (g *Gate) Exclusive() (release func()) {
	g.mu.Lock()
	return sync.OnceFunc(g.mu.Unlock)
}
