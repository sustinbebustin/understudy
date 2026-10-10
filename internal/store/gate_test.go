package store

import "testing"

// These tests probe the gate's RWMutex directly: a Try* call answers at once
// whether the other side is held, so no goroutine has to be scheduled before
// the assertion means anything.

func TestExclusiveHoldsOffTheSharedSideUntilReleased(t *testing.T) {
	t.Parallel()
	g := &Gate{}
	release := g.Exclusive()
	if g.TryEnter() {
		g.Leave()
		t.Fatal("the shared side was free while Exclusive was held, so Update and Read could run during a snapshot cut")
	}
	release()
	if !g.TryEnter() {
		t.Fatal("the shared side is still blocked after Exclusive was released")
	}
	g.Leave()
}

func TestUpdateAndReadHoldTheSharedSide(t *testing.T) {
	t.Parallel()
	g := &Gate{}
	c := &Cell[int]{gate: g}
	exclusiveFree := func() bool {
		if g.mu.TryLock() {
			g.mu.Unlock()
			return true
		}
		return false
	}
	if Update(c, func(*int) (bool, bool) { return exclusiveFree(), false }) {
		t.Fatal("Exclusive could start while an Update was in flight")
	}
	if Read(c, func(*int) bool { return exclusiveFree() }) {
		t.Fatal("Exclusive could start while a Read was in flight")
	}
	if !exclusiveFree() {
		t.Fatal("Update or Read left the shared side held after returning")
	}
}
