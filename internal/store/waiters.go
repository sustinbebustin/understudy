package store

import (
	"context"
	"sync"
)

// WaitKey names something a blocked operation waits for, such as "messages
// are available on this queue".
type WaitKey string

// Waiters lets blocking operations (long polls) wait for a key to be woken.
// Waiting is channel-based so testing/synctest bubbles can advance, and every
// registration ends when its context does, so abandoned waits leak nothing.
//
// The zero value is ready to use. Its mutex is a leaf lock.
type Waiters struct {
	mu   sync.Mutex
	regs map[WaitKey]map[*registration]struct{}
}

type registration struct {
	ch   chan struct{}
	stop func() bool
}

// Wait registers interest in k and returns a channel closed by the next Wake
// of k. The registration is removed when ctx ends. Callers subscribe before
// checking state, so a Wake between the check and the select is not lost.
func (w *Waiters) Wait(ctx context.Context, k WaitKey) <-chan struct{} {
	r := &registration{ch: make(chan struct{})}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.regs == nil {
		w.regs = map[WaitKey]map[*registration]struct{}{}
	}
	set, ok := w.regs[k]
	if !ok {
		set = map[*registration]struct{}{}
		w.regs[k] = set
	}
	set[r] = struct{}{}
	r.stop = context.AfterFunc(ctx, func() { w.remove(k, r) })
	return r.ch
}

// Wake closes the channel of every current registration for k and removes
// them. Waking a key nobody waits for does nothing.
func (w *Waiters) Wake(k WaitKey) {
	w.mu.Lock()
	set := w.regs[k]
	delete(w.regs, k)
	w.mu.Unlock()
	for r := range set {
		r.stop()
		close(r.ch)
	}
}

// Len reports how many registrations for k are live.
func (w *Waiters) Len(k WaitKey) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.regs[k])
}

func (w *Waiters) remove(k WaitKey, r *registration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	set := w.regs[k]
	delete(set, r)
	if len(set) == 0 {
		delete(w.regs, k)
	}
}
