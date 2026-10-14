package clock

import (
	"cmp"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"pgregory.net/rapid"
)

// TestSchedulerFiresInDueThenArmOrder checks the scheduler against a model:
// after any sequence of Arm and Disarm, an advance past every due instant
// fires exactly the armed keys, ordered by due instant and then by when each
// was last armed.
func TestSchedulerFiresInDueThenArmOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		type armed struct {
			due time.Duration
			seq int
		}
		model := map[TimerKey]armed{}
		timers := NewScheduler(func(TimerKey, Instant) {})
		c, err := New(Options{Start: epoch, Mode: Frozen{}, Timers: timers})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		keys := rapid.SampledFrom([]string{"a", "b", "c", "d", "e", "f"})
		for seq := range rapid.IntRange(0, 60).Draw(t, "ops") {
			k := TimerKey{Kind: "k", ID: keys.Draw(t, "key")}
			if rapid.Bool().Draw(t, "disarm") {
				timers.Disarm(k)
				delete(model, k)
				continue
			}
			due := time.Duration(rapid.IntRange(0, 10).Draw(t, "due")) * time.Second
			timers.Arm(k, epoch.Add(due))
			model[k] = armed{due: due, seq: seq}
		}

		if timers.Len() != len(model) {
			t.Fatalf("Len = %d, model has %d", timers.Len(), len(model))
		}
		want := make([]TimerKey, 0, len(model))
		for k := range model {
			want = append(want, k)
		}
		slices.SortFunc(want, func(a, b TimerKey) int {
			return cmp.Or(cmp.Compare(model[a].due, model[b].due), cmp.Compare(model[a].seq, model[b].seq))
		})

		adv, err := c.Advance(time.Minute)
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if !slices.Equal(adv.Fired, want) {
			t.Fatalf("fired %v, want %v", adv.Fired, want)
		}
		if timers.Len() != 0 {
			t.Fatalf("%d timers remain armed after firing", timers.Len())
		}
	})
}

// TestClearThenRearm empties the scheduler as reset and snapshot load do,
// then re-arms from state; only the re-armed timers fire.
func TestClearThenRearm(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{Start: epoch, Mode: Frozen{}})
		h.runDriver(t)
		for _, id := range []string{"a", "b", "c"} {
			h.timers.Arm(TimerKey{Kind: "old", ID: id}, epoch.Add(time.Minute))
		}

		h.timers.Clear()
		if n, snap := h.timers.Len(), h.timers.Snapshot(); n != 0 || len(snap) != 0 {
			t.Fatalf("after Clear: Len %d, Snapshot %v; want empty", n, snap)
		}
		if _, ok := h.timers.Next(); ok {
			t.Fatal("Next reports a timer after Clear")
		}
		rearmed := []Armed{
			{Key: TimerKey{Kind: "new", ID: "a"}, Due: epoch.Add(time.Second)},
			{Key: TimerKey{Kind: "old", ID: "b"}, Due: epoch.Add(2 * time.Minute)},
		}
		for _, a := range rearmed {
			h.timers.Arm(a.Key, a.Due)
		}
		if snap := h.timers.Snapshot(); !slices.Equal(snap, rearmed) {
			t.Fatalf("Snapshot after re-arming = %v, want %v", snap, rearmed)
		}

		adv, err := h.clock.Advance(time.Hour)
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if want := []TimerKey{rearmed[0].Key, rearmed[1].Key}; !slices.Equal(adv.Fired, want) {
			t.Errorf("fired %v, want only the re-armed timers %v", adv.Fired, want)
		}
	})
}

// TestDueOrFiringCountsATimerUntilItsDispatchReturns parks a dispatch the
// driver has started: the timer has left the heap, and DueOrFiring still
// counts it until the dispatch returns, then signals the drop.
func TestDueOrFiringCountsATimerUntilItsDispatchReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{Start: epoch, Mode: Frozen{}})
		release := make(chan struct{})
		h.onFire = func(TimerKey, Instant) { <-release }
		k := TimerKey{Kind: "k", ID: "a"}
		h.timers.Arm(k, epoch)
		h.timers.Arm(TimerKey{Kind: "k", ID: "later"}, epoch.Add(time.Minute))
		if n, _ := h.timers.DueOrFiring(epoch); n != 1 {
			t.Fatalf("DueOrFiring before the driver runs = %d, want the 1 due timer", n)
		}

		h.runDriver(t)
		synctest.Wait()
		if f := h.drain(); len(f) != 1 || f[0].key != k {
			t.Fatalf("dispatched %v, want only %v", f, k)
		}
		if snap := h.timers.Snapshot(); len(snap) != 1 {
			t.Fatalf("Snapshot while dispatching = %v, want only the later timer", snap)
		}
		n, dropped := h.timers.DueOrFiring(epoch)
		if n != 1 {
			t.Fatalf("DueOrFiring while dispatching = %d, want 1", n)
		}
		select {
		case <-dropped:
			t.Fatal("dropped closed while the dispatch is still running")
		default:
		}

		close(release)
		synctest.Wait()
		select {
		case <-dropped:
		default:
			t.Fatal("dropped still open after the dispatch returned")
		}
		if n, _ := h.timers.DueOrFiring(epoch); n != 0 {
			t.Fatalf("DueOrFiring after the dispatch = %d, want 0", n)
		}
	})
}

// TestDueOrFiringSignalsDisarmAndClear checks that Disarm, Clear and a re-Arm
// that moves a due timer later each close dropped; a waiter would otherwise
// keep waiting on a count that has already fallen.
func TestDueOrFiringSignalsDisarmAndClear(t *testing.T) {
	timers := NewScheduler(func(TimerKey, Instant) {})
	k := TimerKey{Kind: "k", ID: "a"}
	for name, drop := range map[string]func(){
		"Disarm": func() { timers.Disarm(k) },
		"Clear":  timers.Clear,
		"re-Arm": func() { timers.Arm(k, epoch.Add(time.Hour)) },
	} {
		timers.Arm(k, epoch)
		n, dropped := timers.DueOrFiring(epoch)
		if n != 1 {
			t.Fatalf("%s: DueOrFiring = %d, want 1", name, n)
		}
		drop()
		select {
		case <-dropped:
		default:
			t.Errorf("%s did not close dropped", name)
		}
		if n, _ := timers.DueOrFiring(epoch); n != 0 {
			t.Errorf("after %s: DueOrFiring = %d, want 0", name, n)
		}
		timers.Clear()
	}
}

// TestSnapshotDuringRearm runs Snapshot while another goroutine re-arms an
// existing key. Under -race it fails if Snapshot reads a timer outside the
// scheduler's lock; without -race it checks every snapshot is a consistent
// view of the one armed key.
func TestSnapshotDuringRearm(t *testing.T) {
	timers := NewScheduler(func(TimerKey, Instant) {})
	k := TimerKey{Kind: "k", ID: "a"}
	timers.Arm(k, epoch)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 1000 {
			timers.Arm(k, epoch.Add(time.Duration(i)*time.Second))
		}
	}()
	for range 1000 {
		got := timers.Snapshot()
		if len(got) != 1 || got[0].Key != k {
			t.Fatalf("Snapshot = %v, want one timer with key %v", got, k)
		}
	}
	<-done
}
