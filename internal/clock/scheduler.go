package clock

import (
	"cmp"
	"slices"
	"sync"
)

// TimerKind names a family of timers. Services declare their kinds as
// constants (sqs.TimerVisibility = "sqs.visibility") and register one handler
// per kind with the kernel.
type TimerKind string

// TimerKey identifies one timer: its kind and an ID unique within the kind,
// such as {sqs.visibility, "<queue-arn>/<message-id>"}.
type TimerKey struct {
	Kind TimerKind
	ID   string
}

// Armed is one pending timer as Snapshot reports it.
type Armed struct {
	Key TimerKey
	Due Instant
}

// Scheduler is a heap of timers ordered by (due, seq) with an index by key.
// Timers carry no closure: when one is due the clock calls the dispatch
// function the scheduler was built with, passing the timer's due instant.
// Dispatch runs outside the scheduler's lock and the clock's data lock but
// serialized with advances and mode changes, so a handler may arm, disarm and
// read Now but must not advance the clock or change its mode (see Clock).
type Scheduler struct {
	dispatch func(k TimerKey, due Instant)

	mu    sync.Mutex
	heap  []*timer
	byKey map[TimerKey]*timer
	seq   uint64
	armed chan struct{} // closed and replaced by an Arm that makes a new earliest timer, to wake the driver
	// firing counts timers popDue has taken off the heap whose dispatch has
	// not returned, so DueOrFiring never loses a timer between the two.
	firing int
	// dropped is closed and cleared by any change that can lower
	// DueOrFiring; nil until DueOrFiring hands one out.
	dropped chan struct{}
}

type timer struct {
	key   TimerKey
	due   Instant
	seq   uint64
	index int
}

// NewScheduler returns an empty scheduler that hands due timers to dispatch.
func NewScheduler(dispatch func(k TimerKey, due Instant)) *Scheduler {
	return &Scheduler{
		dispatch: dispatch,
		byKey:    make(map[TimerKey]*timer),
		armed:    make(chan struct{}),
	}
}

// Arm schedules k at due, replacing any timer with the same key. A replaced
// timer orders after every timer armed before it with the same due instant.
// A timer whose due instant is not after Now fires as soon as the driver or
// an advance next runs, in every clock mode.
func (s *Scheduler) Arm(k TimerKey, due Instant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	t, ok := s.byKey[k]
	if ok {
		t.due, t.seq = due, s.seq
		s.fix(t.index)
		// Moving a due timer later lowers DueOrFiring.
		s.signalDroppedLocked()
	} else {
		t = &timer{key: k, due: due, seq: s.seq, index: len(s.heap)}
		s.heap = append(s.heap, t)
		s.byKey[k] = t
		s.up(t.index)
	}
	// The driver sleeps until the earliest timer is due, so only a new
	// earliest timer can make it wake sooner. A timer moved off the front
	// leaves the driver waking early, which fires nothing and recomputes.
	if t.index == 0 {
		close(s.armed)
		s.armed = make(chan struct{})
	}
}

// Disarm removes the timer with key k. Disarming a key that is not armed does
// nothing.
func (s *Scheduler) Disarm(k TimerKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.byKey[k]; ok {
		s.remove(t.index)
		s.signalDroppedLocked()
	}
}

// Clear disarms every timer at once, so reset and snapshot load can empty
// the scheduler before Rearm rebuilds the timers from state, with one lock
// and no window in which a concurrent Arm survives half a clear.
func (s *Scheduler) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.heap)
	s.heap = s.heap[:0]
	clear(s.byKey)
	s.signalDroppedLocked()
	// The driver may be sleeping until a timer that no longer exists; wake
	// it so it recomputes from the empty heap.
	close(s.armed)
	s.armed = make(chan struct{})
}

// Next reports the earliest due instant, and false when nothing is armed.
func (s *Scheduler) Next() (Instant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.heap) == 0 {
		return Instant{}, false
	}
	return s.heap[0].due, true
}

// Len reports how many timers are armed.
func (s *Scheduler) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.heap)
}

// Snapshot lists the armed timers in firing order, for tests and diagnostics.
func (s *Scheduler) Snapshot() []Armed {
	// Arm rewrites due and seq in place, so the values are copied under the
	// lock and no *timer escapes it.
	s.mu.Lock()
	timers := make([]timer, len(s.heap))
	for i, t := range s.heap {
		timers[i] = *t
	}
	s.mu.Unlock()
	slices.SortFunc(timers, func(a, b timer) int { return compareTimers(&a, &b) })
	out := make([]Armed, len(timers))
	for i, t := range timers {
		out[i] = Armed{Key: t.key, Due: t.due}
	}
	return out
}

// DueOrFiring counts the timers due at or before now plus the timers being
// fired: taken off the heap, their dispatch not yet returned. A timer moves
// from one count to the other under one lock, so a caller that reads zero
// has missed none. dropped is closed by the next change that can lower the
// count: a fired timer's dispatch returning, a disarm, a re-arm or a clear.
// Kernel settling waits on it; time passing only raises the count.
func (s *Scheduler) DueOrFiring(now Instant) (n int, dropped <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n = s.firing
	for _, t := range s.heap {
		if !now.Before(t.due) {
			n++
		}
	}
	if s.dropped == nil {
		s.dropped = make(chan struct{})
	}
	return n, s.dropped
}

// popDue removes and returns the first timer in (due, seq) order when it is
// due at or before limit, and counts it as firing until fire returns. Every
// timer it returns must be passed to fire.
func (s *Scheduler) popDue(limit Instant) (TimerKey, Instant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.heap) == 0 || limit.Before(s.heap[0].due) {
		return TimerKey{}, Instant{}, false
	}
	t := s.heap[0]
	s.remove(0)
	s.firing++
	return t.key, t.due, true
}

// fire dispatches a timer popDue returned and stops counting it as firing
// once dispatch returns or panics.
func (s *Scheduler) fire(k TimerKey, due Instant) {
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.firing--
		s.signalDroppedLocked()
	}()
	s.dispatch(k, due)
}

// signalDroppedLocked wakes DueOrFiring's waiters. The caller holds s.mu.
func (s *Scheduler) signalDroppedLocked() {
	if s.dropped != nil {
		close(s.dropped)
		s.dropped = nil
	}
}

// armSignal returns a channel that the next Arm closes.
func (s *Scheduler) armSignal() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.armed
}

// remove deletes the timer at heap index i. The caller holds s.mu.
func (s *Scheduler) remove(i int) {
	t := s.heap[i]
	last := len(s.heap) - 1
	s.swap(i, last)
	s.heap[last] = nil
	s.heap = s.heap[:last]
	delete(s.byKey, t.key)
	if i < last {
		s.fix(i)
	}
}

// fix restores heap order after the timer at index i changed.
func (s *Scheduler) fix(i int) {
	if !s.down(i) {
		s.up(i)
	}
}

func (s *Scheduler) up(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if compareTimers(s.heap[i], s.heap[parent]) >= 0 {
			return
		}
		s.swap(i, parent)
		i = parent
	}
}

// down sifts the timer at index i toward the leaves and reports whether it
// moved.
func (s *Scheduler) down(i int) bool {
	start := i
	n := len(s.heap)
	for {
		least := i
		for _, child := range [2]int{2*i + 1, 2*i + 2} {
			if child < n && compareTimers(s.heap[child], s.heap[least]) < 0 {
				least = child
			}
		}
		if least == i {
			return i > start
		}
		s.swap(i, least)
		i = least
	}
}

func (s *Scheduler) swap(i, j int) {
	s.heap[i], s.heap[j] = s.heap[j], s.heap[i]
	s.heap[i].index = i
	s.heap[j].index = j
}

func compareTimers(a, b *timer) int {
	if c := a.due.Compare(b.due); c != 0 {
		return c
	}
	return cmp.Compare(a.seq, b.seq)
}
