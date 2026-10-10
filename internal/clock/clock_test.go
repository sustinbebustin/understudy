package clock

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

var epoch = FromWire(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

// fired records what a dispatch saw: the key, the due instant it was handed
// and the clock's Now during the call.
type fired struct {
	key      TimerKey
	due, now Instant
}

// harness builds a clock whose scheduler records every dispatch and lets a
// test react to it.
type harness struct {
	clock  *Clock
	timers *Scheduler
	fired  chan fired
	onFire func(k TimerKey, due Instant)
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	h := &harness{fired: make(chan fired, 1024)}
	h.timers = NewScheduler(func(k TimerKey, due Instant) {
		h.fired <- fired{key: k, due: due, now: h.clock.Now()}
		if h.onFire != nil {
			h.onFire(k, due)
		}
	})
	opts.Timers = h.timers
	c, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.clock = c
	return h
}

func (h *harness) drain() []fired {
	var out []fired
	for {
		select {
		case f := <-h.fired:
			out = append(out, f)
		default:
			return out
		}
	}
}

// runDriver starts the driver and stops it when the test ends.
func (h *harness) runDriver(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.clock.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestAdvanceFiresRateScheduleStepwise(t *testing.T) {
	// An EventBridge rate(5 minutes) rule is one key re-armed by its own
	// handler. Advancing an hour fires twelve events stamped 5, 10, ... 60
	// minutes after the start, each observing Now equal to its due instant.
	synctest.Test(t, func(t *testing.T) {
		rule := TimerKey{Kind: "events.schedule", ID: "rule/every-5"}
		h := newHarness(t, Options{Start: epoch})
		h.onFire = func(k TimerKey, due Instant) { h.timers.Arm(k, due.Add(5*time.Minute)) }
		h.timers.Arm(rule, epoch.Add(5*time.Minute))

		adv, err := h.clock.Advance(time.Hour)
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}

		got := h.drain()
		if len(got) != 12 || len(adv.Fired) != 12 {
			t.Fatalf("fired %d times (Advanced reports %d), want 12", len(got), len(adv.Fired))
		}
		for i, f := range got {
			want := epoch.Add(time.Duration(i+1) * 5 * time.Minute)
			if f.due != want || f.now != want {
				t.Errorf("event %d: due %s, Now %s; want both %s", i+1, f.due, f.now, want)
			}
		}
		if adv.From != epoch || adv.To != epoch.Add(time.Hour) {
			t.Errorf("Advanced spans %s to %s, want %s to %s", adv.From, adv.To, epoch, epoch.Add(time.Hour))
		}
		if now := h.clock.Now(); now != epoch.Add(time.Hour) {
			t.Errorf("Now after advance = %s, want %s", now, epoch.Add(time.Hour))
		}
		if next, ok := h.timers.Next(); !ok || next != epoch.Add(65*time.Minute) {
			t.Errorf("Next = %s, %v; want the 65th minute still armed", next, ok)
		}
	})
}

func TestTimerArmedDuringAdvanceFiresInSameAdvance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		send := TimerKey{Kind: "sqs.delay", ID: "q/m1"}
		visible := TimerKey{Kind: "sqs.visibility", ID: "q/m1"}
		h := newHarness(t, Options{Start: epoch, Mode: Frozen{}})
		h.onFire = func(k TimerKey, due Instant) {
			if k == send {
				h.timers.Arm(visible, due.Add(30*time.Second))
			}
		}
		h.timers.Arm(send, epoch.Add(time.Minute))

		adv, err := h.clock.Advance(2 * time.Minute)
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}

		if !slices.Equal(adv.Fired, []TimerKey{send, visible}) {
			t.Fatalf("fired %v, want [%v %v]", adv.Fired, send, visible)
		}
		got := h.drain()
		if want := epoch.Add(90 * time.Second); got[1].now != want {
			t.Errorf("the timer armed by a handler saw Now %s, want %s", got[1].now, want)
		}
	})
}

func TestTimersFireInDueThenArmOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{Start: epoch, Mode: Frozen{}})
		a := TimerKey{Kind: "k", ID: "a"}
		b := TimerKey{Kind: "k", ID: "b"}
		c := TimerKey{Kind: "k", ID: "c"}
		h.timers.Arm(a, epoch.Add(2*time.Second))
		h.timers.Arm(b, epoch.Add(time.Second))
		h.timers.Arm(c, epoch.Add(2*time.Second))

		adv, err := h.clock.Advance(5 * time.Second)
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if want := []TimerKey{b, a, c}; !slices.Equal(adv.Fired, want) {
			t.Errorf("fired %v, want %v", adv.Fired, want)
		}
	})
}

func TestArmReplacesAndDisarmRemoves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{Start: epoch, Mode: Frozen{}})
		moved := TimerKey{Kind: "sqs.visibility", ID: "extended"}
		gone := TimerKey{Kind: "sqs.visibility", ID: "deleted"}
		h.timers.Arm(moved, epoch.Add(time.Second))
		h.timers.Arm(moved, epoch.Add(time.Hour))
		h.timers.Arm(gone, epoch.Add(time.Second))
		h.timers.Disarm(gone)
		h.timers.Disarm(gone)

		if n := h.timers.Len(); n != 1 {
			t.Fatalf("Len = %d, want 1", n)
		}
		adv, err := h.clock.Advance(time.Minute)
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if len(adv.Fired) != 0 {
			t.Errorf("fired %v, want nothing before the extended deadline", adv.Fired)
		}
		if want := []Armed{{Key: moved, Due: epoch.Add(time.Hour)}}; !slices.Equal(h.timers.Snapshot(), want) {
			t.Errorf("Snapshot = %v, want %v", h.timers.Snapshot(), want)
		}
	})
}

func TestFrozenFiresDueTimersAtOnce(t *testing.T) {
	// Frozen only stops Now from moving: SNS fan-out and window-0 ESM polls
	// arm timers at Now and expect them to run.
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{Start: epoch, Mode: Frozen{}})
		h.runDriver(t)
		atNow := TimerKey{Kind: "sns.deliver", ID: "now"}
		overdue := TimerKey{Kind: "sns.deliver", ID: "overdue"}
		later := TimerKey{Kind: "sns.deliver", ID: "later"}

		// Each timer is armed and settled on its own: the driver runs on its
		// own goroutine, so firing order across separate Arm calls is a
		// scheduling race. (due, seq) order is covered elsewhere.
		firesAlone := func(k TimerKey, due Instant) {
			t.Helper()
			h.timers.Arm(k, due)
			synctest.Wait()
			got := h.drain()
			keys := make([]TimerKey, 0, len(got))
			for _, f := range got {
				keys = append(keys, f.key)
			}
			if want := []TimerKey{k}; !slices.Equal(keys, want) {
				t.Fatalf("arming %v due %s fired %v, want %v", k, due, keys, want)
			}
		}

		h.timers.Arm(later, epoch.Add(time.Second))
		synctest.Wait()
		firesAlone(overdue, epoch.Add(-time.Second))
		firesAlone(atNow, epoch)

		time.Sleep(24 * time.Hour)
		synctest.Wait()
		if got := h.drain(); len(got) != 0 {
			t.Errorf("a frozen clock fired %v after a day of wall time", got)
		}
		if now := h.clock.Now(); now != epoch {
			t.Errorf("frozen Now moved to %s", now)
		}
	})
}

func TestRunningDriverFiresOnWallTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{Start: epoch})
		h.runDriver(t)
		k := TimerKey{Kind: "sqs.visibility", ID: "m"}
		h.timers.Arm(k, epoch.Add(30*time.Second))

		time.Sleep(30*time.Second - time.Nanosecond)
		synctest.Wait()
		if got := h.drain(); len(got) != 0 {
			t.Fatalf("fired %v before the due instant", got)
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if got := h.drain(); len(got) != 1 || got[0].due != epoch.Add(30*time.Second) {
			t.Fatalf("fired %v, want one dispatch due at 30s", got)
		}
	})
}

func TestScaledDriverFiresAfterScaledWallTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{Start: epoch, Mode: Scaled{Factor: 60}})
		h.runDriver(t)
		k := TimerKey{Kind: "lambda.retry", ID: "fn/1"}
		h.timers.Arm(k, epoch.Add(60*time.Second))

		time.Sleep(time.Second - time.Millisecond)
		synctest.Wait()
		if got := h.drain(); len(got) != 0 {
			t.Fatalf("fired %v after %v of wall time", got, time.Second-time.Millisecond)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		got := h.drain()
		if len(got) != 1 || got[0].due != epoch.Add(time.Minute) {
			t.Fatalf("fired %v, want one dispatch due at 60s", got)
		}
		if now := h.clock.Now(); now != epoch.Add(time.Minute) {
			t.Errorf("Now after 1s of wall at 60x = %s, want %s", now, epoch.Add(time.Minute))
		}
	})
}

func TestDriverWakesOnModeChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{Start: epoch, Mode: Frozen{}})
		h.runDriver(t)
		h.timers.Arm(TimerKey{Kind: "k", ID: "x"}, epoch.Add(time.Minute))
		time.Sleep(time.Hour)
		synctest.Wait()
		if got := h.drain(); len(got) != 0 {
			t.Fatalf("fired %v while frozen", got)
		}

		if _, err := h.clock.Resume(); err != nil {
			t.Fatalf("Resume: %v", err)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := h.drain(); len(got) != 1 {
			t.Fatalf("fired %v after resuming for a minute, want one", got)
		}
	})
}

func TestDriverExitsWhenContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{Start: epoch})
		h.timers.Arm(TimerKey{Kind: "k", ID: "far"}, epoch.Add(24*time.Hour))
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.clock.Run(ctx)
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("Run did not return after its context was cancelled")
		}
	})
}

func TestAdvanceToRefusesBackwards(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{Start: epoch, Mode: Frozen{}})
		_, err := h.clock.AdvanceTo(epoch.Add(-time.Second))
		var back *BackwardsError
		if !errors.As(err, &back) {
			t.Fatalf("AdvanceTo an earlier instant returned %v, want *BackwardsError", err)
		}
		if back.From != epoch || back.To != epoch.Add(-time.Second) {
			t.Errorf("BackwardsError{From: %s, To: %s}, want {%s, %s}", back.From, back.To, epoch, epoch.Add(-time.Second))
		}
		if _, err := h.clock.Advance(-time.Nanosecond); !errors.As(err, &back) {
			t.Errorf("Advance(-1ns) returned %v, want *BackwardsError", err)
		}
		if now := h.clock.Now(); now != epoch {
			t.Errorf("a refused move changed Now to %s", now)
		}

		adv, err := h.clock.AdvanceTo(epoch.Add(time.Hour))
		if err != nil || adv.To != epoch.Add(time.Hour) || h.clock.Now() != epoch.Add(time.Hour) {
			t.Errorf("AdvanceTo forward = %+v, %v; Now %s", adv, err, h.clock.Now())
		}
	})
}

func TestPanickingHandlerLeavesClockRunning(t *testing.T) {
	// net/http recovers a panicking admin request, so a timer handler that
	// panics during `clock advance` must leave the clock moving and its
	// remaining timers armed rather than holding emulator time forever.
	synctest.Test(t, func(t *testing.T) {
		bad := TimerKey{Kind: "test.panic", ID: "bad"}
		pending := TimerKey{Kind: "test.ok", ID: "pending"}
		later := TimerKey{Kind: "test.ok", ID: "later"}
		h := newHarness(t, Options{Start: epoch})
		h.onFire = func(k TimerKey, _ Instant) {
			if k == bad {
				panic("handler failed")
			}
		}
		h.timers.Arm(bad, epoch.Add(time.Minute))
		h.timers.Arm(pending, epoch.Add(2*time.Minute))

		recovered := func() (r any) {
			defer func() { r = recover() }()
			if _, err := h.clock.Advance(time.Hour); err != nil {
				t.Errorf("Advance: %v", err)
			}
			return nil
		}()
		if recovered == nil {
			t.Fatal("Advance swallowed the handler's panic")
		}
		h.drain()
		if _, ok := h.clock.Mode().(Running); !ok {
			t.Fatalf("Mode after a panicking advance = %#v, want Running", h.clock.Mode())
		}
		if now := h.clock.Now(); now != epoch.Add(time.Hour) {
			t.Errorf("Now after a panicking advance = %s, want the advance's end %s", now, epoch.Add(time.Hour))
		}
		time.Sleep(time.Minute)
		if now := h.clock.Now(); now != epoch.Add(61*time.Minute) {
			t.Errorf("Now a wall minute after a panicking advance = %s, want %s", now, epoch.Add(61*time.Minute))
		}

		h.runDriver(t)
		synctest.Wait()
		if got := h.drain(); len(got) != 1 || got[0].key != pending {
			t.Fatalf("driver fired %+v, want the timer the panic skipped", got)
		}
		h.timers.Arm(later, h.clock.Now().Add(time.Minute))
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := h.drain(); len(got) != 1 || got[0].key != later {
			t.Errorf("driver fired %+v, want the timer armed after the panic", got)
		}
	})
}

func TestOnAdvanceCalledOncePerAdvance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls []Advanced
		h := newHarness(t, Options{Start: epoch, Mode: Frozen{}, OnAdvance: func(a Advanced) { calls = append(calls, a) }})
		h.onFire = func(k TimerKey, due Instant) {
			if due.Before(epoch.Add(time.Hour)) {
				h.timers.Arm(k, due.Add(time.Minute))
			}
		}
		h.timers.Arm(TimerKey{Kind: "k", ID: "tick"}, epoch.Add(time.Minute))
		if _, err := h.clock.Advance(time.Hour); err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if _, err := h.clock.AdvanceTo(epoch.Add(2 * time.Hour)); err != nil {
			t.Fatalf("AdvanceTo: %v", err)
		}
		if len(calls) != 2 {
			t.Fatalf("hook called %d times for two advances, want 2", len(calls))
		}
		if len(calls[0].Fired) != 60 {
			t.Errorf("first advance reported %d fired timers, want 60", len(calls[0].Fired))
		}
	})
}

func TestOnAdvanceMayChangeTheClock(t *testing.T) {
	// The hook runs with no clock lock held, so it can freeze the clock and
	// advance it again. Mutex waits are invisible to synctest, so a wall-time
	// guard catches a deadlock.
	var c *Clock
	calls := 0
	c, err := New(Options{Start: epoch, Mode: Frozen{}, OnAdvance: func(Advanced) {
		calls++
		c.Freeze()
		if calls == 1 {
			if _, err := c.Advance(time.Minute); err != nil {
				t.Errorf("Advance from the hook: %v", err)
			}
		}
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Advance(time.Hour)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Advance did not return within 5s of wall time; the OnAdvance hook deadlocked on a clock lock")
	}
	if calls != 2 {
		t.Errorf("hook called %d times, want 2 (the outer advance and the one it made)", calls)
	}
	if now, want := c.Now(), epoch.Add(61*time.Minute); now != want {
		t.Errorf("Now = %s, want %s", now, want)
	}
}

func TestDriverFiresTimersArmedOutOfOrder(t *testing.T) {
	// Timers armed behind the earliest one, and an earliest timer moved
	// later, still fire at their own due instants.
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{Start: epoch})
		h.runDriver(t)
		near := TimerKey{Kind: "k", ID: "near"}
		middle := TimerKey{Kind: "k", ID: "middle"}
		moved := TimerKey{Kind: "k", ID: "moved"}
		h.timers.Arm(moved, epoch.Add(5*time.Second))
		h.timers.Arm(near, epoch.Add(10*time.Second))
		h.timers.Arm(middle, epoch.Add(30*time.Second))
		h.timers.Arm(moved, epoch.Add(time.Minute))

		keys := make([]TimerKey, 0, 3)
		for range 3 {
			f := <-h.fired
			if f.now != f.due {
				t.Errorf("%v fired at %s, due %s", f.key, f.now, f.due)
			}
			keys = append(keys, f.key)
		}
		if want := []TimerKey{near, middle, moved}; !slices.Equal(keys, want) {
			t.Errorf("fired %v, want %v", keys, want)
		}
	})
}

func TestNewWithoutStartBeginsAtWallTime(t *testing.T) {
	// A mode built without instants, as a --clock-scale or --clock-frozen
	// flag would build it, starts from wall time instead of the zero time.
	t.Run("frozen", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c, err := New(Options{Mode: Frozen{}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			start := FromWire(time.Now())
			time.Sleep(time.Minute)
			if now := c.Now(); now != start {
				t.Errorf("Now = %s, want the wall time at construction %s", now, start)
			}
		})
	})
	t.Run("scaled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c, err := New(Options{Mode: Scaled{Factor: 60}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			start := FromWire(time.Now())
			time.Sleep(time.Second)
			if now, want := c.Now(), start.Add(time.Minute); now != want {
				t.Errorf("Now after 1s of wall at 60x = %s, want %s", now, want)
			}
		})
	})
	t.Run("scaled from an emulator anchor", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			c, err := New(Options{Mode: Scaled{Factor: 60, AnchorEmu: epoch}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			time.Sleep(time.Second)
			if now, want := c.Now(), epoch.Add(time.Minute); now != want {
				t.Errorf("Now after 1s of wall at 60x = %s, want %s", now, want)
			}
		})
	})
}

func TestUntil(t *testing.T) {
	t.Run("ends at the emulator deadline under Running", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t, Options{Start: epoch})
			start := time.Now()
			<-h.clock.Until(t.Context(), Deadline{Emu: epoch.Add(20 * time.Second)})
			if waited := time.Since(start); waited != 20*time.Second {
				t.Errorf("waited %v of wall time, want 20s", waited)
			}
		})
	})
	t.Run("ends at the wall cap under a frozen clock", func(t *testing.T) {
		// A ReceiveMessage long poll never holds the HTTP request longer than
		// its WaitTimeSeconds, even when emulator time stands still.
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t, Options{Start: epoch, Mode: Frozen{}})
			start := time.Now()
			<-h.clock.Until(t.Context(), Deadline{Emu: epoch.Add(time.Hour), WallCap: h.clock.Wall().Add(20 * time.Second)})
			if waited := time.Since(start); waited != 20*time.Second {
				t.Errorf("waited %v of wall time, want the 20s cap", waited)
			}
		})
	})
	t.Run("ends when an advance reaches the deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t, Options{Start: epoch, Mode: Frozen{}})
			done := h.clock.Until(t.Context(), Deadline{Emu: epoch.Add(time.Hour)})
			if _, err := h.clock.Advance(59 * time.Minute); err != nil {
				t.Fatalf("Advance: %v", err)
			}
			synctest.Wait()
			select {
			case <-done:
				t.Fatal("Until ended before its deadline")
			default:
			}
			if _, err := h.clock.Advance(time.Minute); err != nil {
				t.Fatalf("Advance: %v", err)
			}
			<-done
		})
	})
	t.Run("ends with scaled time", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t, Options{Start: epoch})
			done := h.clock.Until(t.Context(), Deadline{Emu: epoch.Add(time.Hour)})
			if _, err := h.clock.Scale(3600); err != nil {
				t.Fatalf("Scale: %v", err)
			}
			start := time.Now()
			<-done
			if waited := time.Since(start); waited != time.Second {
				t.Errorf("waited %v of wall time at 3600x for an hour, want 1s", waited)
			}
		})
	})
	t.Run("cancelled waits leave no goroutine", func(t *testing.T) {
		// The waits are never drained: synctest.Test waits for every goroutine
		// in the bubble to exit and fails the test when they deadlock, so a
		// wait that outlives its context on this frozen clock fails it.
		synctest.Test(t, func(t *testing.T) {
			h := newHarness(t, Options{Start: epoch, Mode: Frozen{}})
			for range 1000 {
				ctx, cancel := context.WithCancel(t.Context())
				h.clock.Until(ctx, Deadline{Emu: epoch.Add(time.Hour)})
				cancel()
			}
			synctest.Wait()
			if n := h.clock.untilWaits.Load(); n != 0 {
				t.Errorf("%d of 1000 cancelled waits are still registered", n)
			}
		})
	})
}

func TestModes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{Start: epoch})
		initial := h.clock.Mode()
		time.Sleep(time.Minute)
		if now := h.clock.Now(); now != epoch.Add(time.Minute) {
			t.Fatalf("Running Now after a minute = %s", now)
		}

		frozen := h.clock.Freeze()
		if frozen.From != initial || frozen.To != (Frozen{At: epoch.Add(time.Minute)}) || h.clock.Mode() != frozen.To {
			t.Fatalf("Freeze reported %#v, Mode %#v", frozen, h.clock.Mode())
		}
		time.Sleep(time.Minute)

		resumed, err := h.clock.Resume()
		if err != nil {
			t.Fatalf("Resume: %v", err)
		}
		wantRunning := Running{Offset: time.Until(epoch.Add(time.Minute).Wire())}
		if resumed.From != frozen.To || resumed.To != wantRunning || h.clock.Mode() != resumed.To {
			t.Fatalf("Resume reported %#v, Mode %#v", resumed, h.clock.Mode())
		}
		time.Sleep(time.Minute)
		if now := h.clock.Now(); now != epoch.Add(2*time.Minute) {
			t.Errorf("Resume did not continue from the frozen instant: Now %s", now)
		}

		for _, f := range []float64{0, -1, MaxScale + 1, nan()} {
			var scaleErr *ScaleError
			if _, err := h.clock.Scale(f); !errors.As(err, &scaleErr) {
				t.Errorf("Scale(%g) = %v, want *ScaleError", f, err)
			}
		}
		if h.clock.Mode() != resumed.To {
			t.Errorf("a refused Scale changed the mode to %#v", h.clock.Mode())
		}
		scaled, err := h.clock.Scale(MaxScale)
		if err != nil {
			t.Fatalf("Scale(MaxScale) = %v", err)
		}
		want := Scaled{Factor: MaxScale, AnchorWall: time.Now(), AnchorEmu: epoch.Add(2 * time.Minute)}
		if scaled.From != resumed.To || !scaled.To.AnchorWall.Equal(want.AnchorWall) ||
			scaled.To.Factor != want.Factor || scaled.To.AnchorEmu != want.AnchorEmu {
			t.Errorf("Scale reported %#v, want From %#v and To %#v", scaled, resumed.To, want)
		}
	})
}

func TestNewValidatesOptions(t *testing.T) {
	var (
		modeErr     *ModeError
		scaleErr    *ScaleError
		conflictErr *StartConflictError
		rangeErr    *RangeError
	)
	wall := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	year1600 := FromWire(time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC))
	tests := []struct {
		name string
		opts Options
		want any // a pointer to the error type New must return, or nil
	}{
		{"pointer to Running", Options{Mode: &Running{}}, &modeErr},
		{"pointer to Frozen", Options{Mode: &Frozen{At: epoch}}, &modeErr},
		{"pointer to Scaled with a negative factor", Options{Mode: &Scaled{Factor: -1}}, &modeErr},
		{"zero factor", Options{Mode: Scaled{Factor: 0}}, &scaleErr},
		{"Start with Running.Offset", Options{Start: epoch, Mode: Running{Offset: time.Hour}}, &conflictErr},
		{"Start with Frozen.At", Options{Start: epoch, Mode: Frozen{At: epoch.Add(time.Hour)}}, &conflictErr},
		{"Start with Scaled.AnchorWall", Options{Start: epoch, Mode: Scaled{Factor: 2, AnchorWall: wall}}, &conflictErr},
		{"Start with Scaled.AnchorEmu", Options{Start: epoch, Mode: Scaled{Factor: 2, AnchorEmu: epoch}}, &conflictErr},
		{"Start beyond MaxSkew", Options{Start: year1600}, &rangeErr},
		{"Frozen.At beyond MaxSkew", Options{Mode: Frozen{At: year1600}}, &rangeErr},
		{"Running.Offset beyond MaxSkew", Options{Mode: Running{Offset: MaxSkew + time.Hour}}, &rangeErr},
		{"Start with a mode without instants", Options{Start: epoch, Mode: Frozen{}}, nil},
		{"mode instants without Start", Options{Mode: Frozen{At: epoch}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(tt.opts)
			if tt.want == nil {
				if err != nil || c.Now() != epoch {
					t.Fatalf("New = %v; want a clock at %s", err, epoch)
				}
				return
			}
			if !errors.As(err, tt.want) || c != nil {
				t.Errorf("New = %v, %v; want no clock and an error of type %T", c, err, tt.want)
			}
		})
	}
}

func TestRestore(t *testing.T) {
	// Reset and snapshot load are the only ways back to an earlier time.
	for _, mode := range []Mode{Frozen{}, Running{}, Scaled{Factor: 60}} {
		t.Run(fmt.Sprintf("%T", mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				later := epoch.Add(24 * time.Hour)
				h := newHarness(t, Options{Start: later, Mode: Frozen{}})
				h.runDriver(t)
				h.timers.Arm(TimerKey{Kind: "k", ID: "stale"}, later.Add(time.Hour))
				synctest.Wait()

				h.timers.Clear()
				change, err := h.clock.Restore(epoch, mode)
				if err != nil {
					t.Fatalf("Restore: %v", err)
				}
				if change.From != (Frozen{At: later}) || change.To != h.clock.Mode() {
					t.Errorf("Restore reported %#v, Mode %#v", change, h.clock.Mode())
				}
				if now := h.clock.Now(); now != epoch {
					t.Fatalf("Now after Restore = %s, want %s", now, epoch)
				}
				if _, err := h.clock.AdvanceTo(epoch.Add(-time.Second)); !errors.As(err, new(*BackwardsError)) {
					t.Errorf("AdvanceTo before the restored instant = %v, want *BackwardsError", err)
				}

				// Clear dropped the stale timer, and a timer armed against the
				// restored timeline fires when the restored clock reaches it.
				h.timers.Arm(TimerKey{Kind: "k", ID: "fresh"}, epoch.Add(time.Second))
				if _, frozen := mode.(Frozen); frozen {
					if _, err := h.clock.Advance(time.Second); err != nil {
						t.Fatalf("Advance: %v", err)
					}
				} else {
					time.Sleep(time.Second)
				}
				synctest.Wait()
				if got := h.drain(); len(got) != 1 || got[0].key.ID != "fresh" || got[0].due != epoch.Add(time.Second) {
					t.Errorf("fired %+v, want only the fresh timer at %s", got, epoch.Add(time.Second))
				}
			})
		})
	}
}

func TestRestoreWakesDriverAndWaiters(t *testing.T) {
	// Before the restore the clock is frozen short of both the armed timer
	// and the Until deadline, so the driver and the waiter wait with no wall
	// timeout and nothing is armed or advanced afterwards: only Restore can
	// wake them. The timer is kept rather than cleared, as a forward restore
	// may keep a timer that is still valid on the restored timeline.
	for _, mode := range []Mode{Frozen{}, Running{}, Scaled{Factor: 60}} {
		t.Run(fmt.Sprintf("%T", mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t, Options{Start: epoch, Mode: Frozen{}})
				h.runDriver(t)
				kept := TimerKey{Kind: "k", ID: "kept"}
				due := epoch.Add(time.Hour)
				h.timers.Arm(kept, due)
				done := h.clock.Until(t.Context(), Deadline{Emu: due})
				synctest.Wait()
				if got := h.drain(); len(got) != 0 {
					t.Fatalf("fired %+v before the clock reached the timer", got)
				}
				select {
				case <-done:
					t.Fatal("Until ended before the clock reached its deadline")
				default:
				}

				if _, err := h.clock.Restore(epoch.Add(2*time.Hour), mode); err != nil {
					t.Fatalf("Restore: %v", err)
				}
				synctest.Wait()
				select {
				case <-done:
				default:
					t.Error("Until still waits after Restore moved the clock past its deadline")
				}
				if got := h.drain(); len(got) != 1 || got[0].key != kept || got[0].due != due {
					t.Errorf("driver fired %+v after Restore, want %v due %s", got, kept, due)
				}
			})
		})
	}
}

func TestRestoreRefuses(t *testing.T) {
	var (
		modeErr  *ModeError
		scaleErr *ScaleError
		rangeErr *RangeError
	)
	tests := []struct {
		name string
		at   Instant
		mode Mode
		want any // a pointer to the error type Restore must return
	}{
		{"nil mode", epoch.Add(-time.Hour), nil, &modeErr},
		{"pointer to a mode", epoch.Add(-time.Hour), &Frozen{}, &modeErr},
		{"factor above MaxScale", epoch.Add(-time.Hour), Scaled{Factor: MaxScale + 1}, &scaleErr},
		{"instant beyond MaxSkew", FromWire(time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC)), Running{}, &rangeErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(Options{Start: epoch, Mode: Frozen{}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := c.Restore(tt.at, tt.mode); !errors.As(err, tt.want) {
				t.Errorf("Restore(%s, %#v) = %v, want an error of type %T", tt.at, tt.mode, err, tt.want)
			}
			if c.Now() != epoch || c.Mode() != (Frozen{At: epoch}) {
				t.Errorf("a refused Restore changed the clock to %s, %#v", c.Now(), c.Mode())
			}
		})
	}
}

func TestEmulatorTimeStaysWithinMaxSkew(t *testing.T) {
	// Running holds emulator time as a time.Duration offset from wall time,
	// so a farther instant is refused rather than wrapping the offset or
	// leaving the clock stuck.
	t.Run("advance past the bound is refused and the clock keeps running", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := FromWire(time.Now())
			c, err := New(Options{Start: start})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := c.Advance(MaxSkew); err != nil {
				t.Fatalf("Advance(MaxSkew): %v", err)
			}
			edge := start.Add(MaxSkew)
			var rangeErr *RangeError
			if _, err := c.Advance(time.Nanosecond); !errors.As(err, &rangeErr) || rangeErr.At != edge.Add(time.Nanosecond) {
				t.Errorf("Advance past MaxSkew = %v, want a *RangeError at %s", err, edge.Add(time.Nanosecond))
			}
			if _, err := c.AdvanceTo(FromWire(time.Date(2400, 1, 1, 0, 0, 0, 0, time.UTC))); !errors.As(err, &rangeErr) {
				t.Errorf("AdvanceTo(2400) = %v, want *RangeError", err)
			}
			if now, mode := c.Now(), c.Mode(); now != edge || mode != (Running{Offset: MaxSkew}) {
				t.Errorf("after refused advances Now = %s, Mode = %#v; want %s, Running at MaxSkew", now, mode, edge)
			}
			time.Sleep(time.Minute)
			if now := c.Now(); now != edge.Add(time.Minute) {
				t.Errorf("Now a wall minute later = %s, want %s", now, edge.Add(time.Minute))
			}
		})
	})
	t.Run("a frozen clock resumes from the bound", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			start := FromWire(time.Now())
			c, err := New(Options{Start: start, Mode: Frozen{}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := c.AdvanceTo(FromWire(time.Date(2400, 1, 1, 0, 0, 0, 0, time.UTC))); !errors.As(err, new(*RangeError)) {
				t.Errorf("AdvanceTo(2400) = %v, want *RangeError", err)
			}
			edge := start.Add(MaxSkew)
			if _, err := c.AdvanceTo(edge); err != nil {
				t.Fatalf("AdvanceTo(wall + MaxSkew): %v", err)
			}
			if _, err := c.Resume(); err != nil {
				t.Fatalf("Resume: %v", err)
			}
			time.Sleep(time.Hour)
			if now := c.Now(); now != edge.Add(time.Hour) {
				t.Errorf("Now an hour after Resume = %s, want %s", now, edge.Add(time.Hour))
			}
		})
	})
	t.Run("a scaled clock runs past the duration range", func(t *testing.T) {
		// At MaxScale the scaled elapsed time leaves the range of
		// time.Duration after about 10.7 days of wall time; emulator time
		// must keep moving, and Resume, which needs a Running offset, is
		// refused while the clock can still be frozen or restored.
		synctest.Test(t, func(t *testing.T) {
			start := FromWire(time.Now())
			c, err := New(Options{Start: start, Mode: Scaled{Factor: MaxScale}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			time.Sleep(20 * 24 * time.Hour)
			want := FromWire(start.Wire().AddDate(0, 0, 20*MaxScale))
			if now := c.Now(); now != want {
				t.Fatalf("Now after 20 days of wall at MaxScale = %s, want %s", now, want)
			}
			before := c.Mode()
			if _, err := c.Resume(); !errors.As(err, new(*RangeError)) || c.Mode() != before {
				t.Errorf("Resume = %v with Mode %#v, want *RangeError and Mode unchanged", err, c.Mode())
			}
			if frozen := c.Freeze(); frozen.To.At != want {
				t.Errorf("Freeze held %s, want %s", frozen.To.At, want)
			}
			if _, err := c.Restore(start, Running{}); err != nil || c.Now() != start {
				t.Errorf("Restore to the start = %v with Now %s, want Now %s", err, c.Now(), start)
			}
		})
	})
}

func TestWallStepIsNoticed(t *testing.T) {
	// Running emulator time follows the wall clock, but the waits run on the
	// monotonic clock, which a wall step (or host suspend) does not move.
	// The waits must notice the jump within maxWallWait instead of sleeping
	// out the rest of the old wait.
	synctest.Test(t, func(t *testing.T) {
		var jump atomic.Int64
		wall := func() time.Time { return time.Now().Add(time.Duration(jump.Load())) }
		h := newHarness(t, Options{Start: epoch, Wall: wall})
		h.runDriver(t)
		h.timers.Arm(TimerKey{Kind: "events.schedule", ID: "hourly"}, epoch.Add(30*time.Minute))
		waiting := h.clock.Until(t.Context(), Deadline{Emu: epoch.Add(30 * time.Minute)})
		synctest.Wait()

		jump.Store(int64(time.Hour))
		start := time.Now()
		<-waiting
		if waited := time.Since(start); waited > maxWallWait {
			t.Errorf("Until noticed the wall step after %v, want at most %v", waited, maxWallWait)
		}
		synctest.Wait()
		if got := h.drain(); len(got) != 1 {
			t.Fatalf("fired %+v within %v of the wall step, want the timer due before it", got, time.Since(start))
		}
		if waited := time.Since(start); waited > maxWallWait {
			t.Errorf("the driver noticed the wall step after %v, want at most %v", waited, maxWallWait)
		}
	})
}

func TestStopwatchIgnoresEmulatorTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, Options{Start: epoch, Mode: Frozen{}})
		elapsed := h.clock.Stopwatch()
		if _, err := h.clock.Advance(time.Hour); err != nil {
			t.Fatalf("Advance: %v", err)
		}
		time.Sleep(3 * time.Second)
		if got := elapsed(); got != 3*time.Second {
			t.Errorf("Stopwatch = %v, want 3s of wall time", got)
		}
	})
}

func TestInstantText(t *testing.T) {
	in := epoch.Add(1500 * time.Millisecond)
	b, err := in.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	var out Instant
	if err := out.UnmarshalText(b); err != nil || out != in {
		t.Errorf("round trip of %s gave %s, %v", in, out, err)
	}
	if err := out.UnmarshalText([]byte("yesterday")); err == nil {
		t.Error("UnmarshalText accepted a non-timestamp")
	}
	// A year RFC 3339 cannot hold must fail on save, not on load.
	if b, err := FromWire(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)).MarshalText(); err == nil {
		t.Errorf("MarshalText wrote %q for year 10000, which UnmarshalText rejects", b)
	}
}

func nan() float64 {
	var zero float64
	return zero / zero
}

func ExampleClock_Advance() {
	timers := NewScheduler(func(k TimerKey, due Instant) {
		fmt.Println(k.ID, "fired at", due.Wire().Format(time.TimeOnly))
	})
	c, err := New(Options{Start: epoch, Mode: Frozen{}, Timers: timers})
	if err != nil {
		fmt.Println(err)
		return
	}
	timers.Arm(TimerKey{Kind: "sqs.visibility", ID: "msg-1"}, epoch.Add(30*time.Second))
	timers.Arm(TimerKey{Kind: "sqs.delay", ID: "msg-2"}, epoch.Add(10*time.Second))
	if _, err := c.Advance(time.Minute); err != nil {
		fmt.Println(err)
	}
	// Output:
	// msg-2 fired at 00:00:10
	// msg-1 fired at 00:00:30
}
