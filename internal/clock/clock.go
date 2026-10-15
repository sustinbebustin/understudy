package clock

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Options configures New. The zero value is a Running clock at wall time with
// no timers.
type Options struct {
	// Start, when set, is emulator time at construction: Running's Offset,
	// Frozen's At and Scaled's anchors are derived from it, and Scaled keeps
	// its Factor. A Mode that sets any of those instant fields as well is
	// refused with a *StartConflictError.
	Start Instant
	// Mode is the initial mode; nil means Running, and anything but a
	// Running, Frozen or Scaled value is refused with a *ModeError. When
	// Start is zero, the zero instant fields of Mode default to wall time at
	// construction: Frozen{} freezes at wall time now, a zero AnchorWall is
	// wall time now and a zero AnchorEmu is AnchorWall read as an Instant,
	// so Scaled{Factor: 60} runs 60 times faster from wall time now.
	Mode Mode
	// Wall reads wall time; nil means time.Now. The driver and Until wait
	// with the time package's timers, so a replacement must advance with
	// them, as the fake clock of a synctest bubble does.
	Wall func() time.Time
	// Timers is the scheduler the clock fires; nil means one that nothing
	// can arm.
	Timers *Scheduler
	// OnAdvance, when set, is called once after every Advance and AdvanceTo,
	// outside every lock.
	OnAdvance WaitersHook
}

// WaitersHook is called once per advance so blocked waiters can re-check
// conditions that depend on emulator time.
type WaitersHook func(Advanced)

// Advanced reports one advance: the timers it fired, in firing order, and
// the emulator times it moved between.
type Advanced struct {
	Fired    []TimerKey
	From, To Instant
}

// Deadline bounds a wait in two domains: it ends when emulator time reaches
// Emu or wall time reaches WallCap, whichever is first. A zero WallCap means
// no wall bound.
type Deadline struct {
	Emu     Instant
	WallCap time.Time
}

// ModeChange reports a mode change: the mode replaced and the one set, read
// under the same lock as the change so a concurrent change cannot slip
// between them.
type ModeChange[T Mode] struct {
	From Mode
	To   T
}

// Clock is the emulator clock. Emulator time moves forwards with wall time
// (Running, Scaled) or only on Advance (Frozen), and never backwards except
// through Restore.
//
// Lock order: step, then mu, then the scheduler's lock. Dispatch runs with
// only step held, so a timer handler may arm and disarm timers and read Now,
// but must not change the clock's mode or advance it.
type Clock struct {
	wall      func() time.Time
	timers    *Scheduler
	onAdvance WaitersHook

	// step serializes firing timers, advancing and changing mode.
	step sync.Mutex

	mu       sync.Mutex
	mode     Mode
	last     Instant       // the latest emulator time reported; Now never returns less
	stepping bool          // an advance is firing timers and Now is held at last
	changed  chan struct{} // closed and replaced by every advance, mode change and restore

	// untilWaits counts Until goroutines still running, so tests can assert
	// that cancelled waits leave none behind.
	untilWaits atomic.Int64
}

// maxWallWait bounds every wall-time wait of the driver and Until. Their
// timers run on the monotonic clock, which ignores wall clock steps and, on
// Linux, host suspend, while Running emulator time follows the wall clock;
// waking at least this often lets them notice such a jump and recompute.
const maxWallWait = time.Second

// New returns a clock configured by opts. It fails with a *ModeError when
// opts.Mode is not a mode value, a *ScaleError when it is Scaled with a
// factor outside (0, MaxScale], a *StartConflictError when opts.Start and
// the mode's instant fields are both set, and a *RangeError when the starting
// emulator time is more than MaxSkew from wall time.
func New(opts Options) (*Clock, error) {
	wall := opts.Wall
	if wall == nil {
		wall = time.Now
	}
	timers := opts.Timers
	if timers == nil {
		timers = NewScheduler(func(TimerKey, Instant) {})
	}
	mode := opts.Mode
	if mode == nil {
		mode = Running{}
	}
	mode, err := parseMode(mode)
	if err != nil {
		return nil, err
	}
	if !opts.Start.IsZero() && hasInstant(mode) {
		return nil, &StartConflictError{Start: opts.Start, Mode: mode}
	}
	w := wall()
	start := opts.Start
	if start.IsZero() {
		mode = anchored(mode, w)
		start = emuAt(mode, w)
	} else {
		mode = rebased(mode, start, w)
	}
	if err := checkSkew(start, w); err != nil {
		return nil, err
	}
	return &Clock{
		wall:      wall,
		timers:    timers,
		onAdvance: opts.OnAdvance,
		mode:      mode,
		last:      start,
		changed:   make(chan struct{}),
	}, nil
}

// Now returns the current emulator time. While an advance fires timers it
// returns each timer's due instant or, for a timer already overdue when the
// advance reaches it, the latest instant the advance has reached.
func (c *Clock) Now() Instant {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nowLocked()
}

// Mode returns the current mode.
func (c *Clock) Mode() Mode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

// Freeze stops emulator time at its current value, which the returned
// change's To.At holds.
func (c *Clock) Freeze() ModeChange[Frozen] {
	c.step.Lock()
	defer c.step.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	to := Frozen{At: c.nowLocked()}
	return ModeChange[Frozen]{From: c.setModeLocked(to), To: to}
}

// Resume returns to Running from the current emulator time, keeping the
// offset from wall time the clock has accumulated. When emulator time is more
// than MaxSkew from wall time, as a Scaled clock left running can make it, it
// refuses with a *RangeError and leaves the mode unchanged.
func (c *Clock) Resume() (ModeChange[Running], error) {
	c.step.Lock()
	defer c.step.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	now, w := c.nowLocked(), c.wall()
	if err := checkSkew(now, w); err != nil {
		return ModeChange[Running]{}, err
	}
	to := Running{Offset: now.Wire().Sub(w)}
	return ModeChange[Running]{From: c.setModeLocked(to), To: to}, nil
}

// Scale runs emulator time f times faster than wall time from now on. It
// refuses f outside (0, MaxScale] with a *ScaleError and leaves the mode
// unchanged.
func (c *Clock) Scale(f float64) (ModeChange[Scaled], error) {
	if !validScale(f) {
		return ModeChange[Scaled]{}, &ScaleError{Factor: f}
	}
	c.step.Lock()
	defer c.step.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.wall()
	to := Scaled{Factor: f, AnchorWall: w, AnchorEmu: c.nowLocked()}
	return ModeChange[Scaled]{From: c.setModeLocked(to), To: to}, nil
}

// Restore sets emulator time to at, earlier or later than Now, in the kind
// of mode m names (and, for Scaled, its factor). It is reserved for reset and
// snapshot load, the only ways back to an earlier time; every other change
// moves forward. The instant fields of m are ignored: a recorded mode's
// offset and anchors are tied to the wall clock of the process that recorded
// it, and at alone fixes emulator time. Restore fires no timers, so the
// caller clears the scheduler first and re-arms from the restored state; the
// driver and Until waiters wake and recompute. It refuses an m that is not a
// mode value with a *ModeError, a Scaled factor outside (0, MaxScale] with a
// *ScaleError and an at more than MaxSkew from wall time with a *RangeError,
// leaving the clock unchanged. Like the other mode changes it must not be
// called from a timer handler.
func (c *Clock) Restore(at Instant, m Mode) (ModeChange[Mode], error) {
	m, err := parseMode(m)
	if err != nil {
		return ModeChange[Mode]{}, err
	}
	c.step.Lock()
	defer c.step.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.wall()
	if err := checkSkew(at, w); err != nil {
		return ModeChange[Mode]{}, err
	}
	to := rebased(m, at, w)
	c.last = at // the one place emulator time may move backwards
	return ModeChange[Mode]{From: c.setModeLocked(to), To: to}, nil
}

// Advance moves emulator time forward by d, firing every timer due on the
// way in (due, seq) order with Now equal to each timer's due instant or, for a
// timer already overdue when the advance reaches it, the latest instant the
// advance has reached. Timers armed by a handler fire in the same advance
// when they are due by its end. A negative d is refused with a
// *BackwardsError, and a target more than MaxSkew from wall time with a
// *RangeError, leaving the clock unchanged. Advance does not wait for
// work that handlers start. When a handler panics the panic propagates, but
// the clock still ends at the target with its mode intact, the timers not yet
// fired stay armed, and OnAdvance is not called.
func (c *Clock) Advance(d time.Duration) (Advanced, error) {
	return c.advanceNotify(func(from Instant) Instant { return from.Add(d) })
}

// AdvanceTo moves emulator time forward to t, as Advance does. A t earlier
// than Now is refused with a *BackwardsError (only reset and snapshot load
// return to an earlier time, through Restore), and a t more than MaxSkew
// from wall time with a *RangeError.
func (c *Clock) AdvanceTo(t Instant) (Advanced, error) {
	return c.advanceNotify(func(Instant) Instant { return t })
}

// Until returns a channel that is closed when emulator time reaches d.Emu,
// wall time reaches d.WallCap, or ctx ends, whichever is first. The wait
// follows mode changes, advances and restores, and holds no resources once
// the channel is closed.
func (c *Clock) Until(ctx context.Context, d Deadline) <-chan struct{} {
	done := make(chan struct{})
	c.untilWaits.Add(1)
	go func() {
		defer close(done)
		defer c.untilWaits.Add(-1)
		for {
			changed, wait, reached := c.untilWait(d)
			if reached {
				return
			}
			if !waitFor(ctx, wait, changed, nil) {
				return
			}
		}
	}()
	return done
}

// noEmulatorDeadline stands for "no emulator deadline" in a wall-only
// Deadline: a century, which even MaxScale takes days of wall time to cover.
const noEmulatorDeadline = 100 * 365 * 24 * time.Hour

// WallDeadline is a Deadline bounded by wall time alone: a wait on it lasts
// until at, which a frozen clock cannot stretch.
func (c *Clock) WallDeadline(at time.Time) Deadline {
	return Deadline{Emu: c.Now().Add(noEmulatorDeadline), WallCap: at}
}

// Wall returns wall time. Use it for values clients compare with their own
// wall clock: the Date header, SigV4 skew, STS expirations and
// Lambda-Runtime-Deadline-Ms.
func (c *Clock) Wall() time.Time { return c.wall() }

// Stopwatch starts a monotonic stopwatch and returns a function reporting the
// wall time elapsed since. Emulator modes never affect it; use it for
// function timeouts, REPORT durations and injected latency.
func (c *Clock) Stopwatch() func() time.Duration {
	start := c.wall()
	return func() time.Duration { return c.wall().Sub(start) }
}

// Run drives the scheduler until ctx ends: it fires every timer whose due
// instant is not after Now, then sleeps until the next one is due, which
// takes (due - now) / factor of wall time when Running or Scaled, waking at
// least every maxWallWait to notice a wall clock step. When Frozen it sleeps
// until a timer is armed, the clock advances or the mode changes. Start one
// driver per clock.
func (c *Clock) Run(ctx context.Context) {
	for {
		armed := c.timers.armSignal()
		c.fireDue()
		changed, wait := c.driverWait()
		if !waitFor(ctx, wait, changed, armed) {
			return
		}
	}
}

// advanceNotify advances from Now to target(Now), then calls the OnAdvance
// hook with no lock held, so the hook may change the mode or advance again.
func (c *Clock) advanceNotify(target func(from Instant) Instant) (Advanced, error) {
	adv, err := c.advanceLocked(target)
	if err != nil {
		return Advanced{}, err
	}
	if c.onAdvance != nil {
		c.onAdvance(adv)
	}
	return adv, nil
}

// advanceLocked holds step while it advances from Now to target(Now),
// refusing a target before Now with a *BackwardsError and one more than
// MaxSkew from wall time with a *RangeError. The bound keeps the shift that
// advance applies to the mode inside the range of time.Duration.
func (c *Clock) advanceLocked(target func(from Instant) Instant) (Advanced, error) {
	c.step.Lock()
	defer c.step.Unlock()
	c.mu.Lock()
	from := c.nowLocked()
	to := target(from)
	var err error
	if to.Before(from) {
		err = &BackwardsError{From: from, To: to}
	} else {
		err = checkSkew(to, c.wall())
	}
	if err == nil {
		c.stepping = true
	}
	c.mu.Unlock()
	if err != nil {
		return Advanced{}, err
	}
	return c.advance(from, to), nil
}

// advance fires the timers due by to, then moves the mode to to. The caller
// holds step and has set stepping with Now held at from.
func (c *Clock) advance(from, to Instant) Advanced {
	// Deferred so a panicking handler still leaves the clock at to with Now
	// following its mode again; the unfired timers stay armed and fire as
	// overdue on the next driver pass or advance.
	defer func() {
		c.mu.Lock()
		c.mode = shifted(c.mode, to.Sub(from))
		c.last = to
		c.stepping = false
		c.broadcastLocked()
		c.mu.Unlock()
	}()
	var fired []TimerKey
	for {
		k, due, ok := c.timers.popDue(to)
		if !ok {
			break
		}
		c.mu.Lock()
		if c.last.Before(due) {
			c.last = due
		}
		c.mu.Unlock()
		c.timers.fire(k, due)
		fired = append(fired, k)
	}
	return Advanced{Fired: fired, From: from, To: to}
}

// fireDue dispatches every timer due at or before Now, in (due, seq) order.
func (c *Clock) fireDue() {
	c.step.Lock()
	defer c.step.Unlock()
	for {
		k, due, ok := c.timers.popDue(c.Now())
		if !ok {
			return
		}
		c.timers.fire(k, due)
	}
}

// driverWait returns the change signal to wait on and, unless the clock is
// frozen or nothing is armed, the wall time until the next timer is due.
func (c *Clock) driverWait() (<-chan struct{}, wallTimeout) {
	next, armed := c.timers.Next()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !armed {
		return c.changed, wallTimeout{}
	}
	d, ok := wallWait(c.mode, next.Sub(c.nowLocked()))
	return c.changed, wallTimeout{d: d, set: ok}
}

// untilWait reports whether d is reached and, if not, the change signal and
// the wall time after which to check again.
func (c *Clock) untilWait(d Deadline) (<-chan struct{}, wallTimeout, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.nowLocked()
	wall := c.wall()
	if !now.Before(d.Emu) || (!d.WallCap.IsZero() && !wall.Before(d.WallCap)) {
		return nil, wallTimeout{}, true
	}
	emu, ok := wallWait(c.mode, d.Emu.Sub(now))
	wait := wallTimeout{d: emu, set: ok}
	if !d.WallCap.IsZero() {
		wait = wait.min(d.WallCap.Sub(wall))
	}
	return c.changed, wait, false
}

func (c *Clock) nowLocked() Instant {
	if c.stepping {
		return c.last
	}
	if n := emuAt(c.mode, c.wall()); c.last.Before(n) {
		c.last = n
	}
	return c.last
}

// setModeLocked sets the mode, wakes every waiter and returns the mode it
// replaced.
func (c *Clock) setModeLocked(m Mode) Mode {
	from := c.mode
	c.mode = m
	c.broadcastLocked()
	return from
}

func (c *Clock) broadcastLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// wallTimeout is an optional wall-time wait.
type wallTimeout struct {
	d   time.Duration
	set bool
}

func (w wallTimeout) min(d time.Duration) wallTimeout {
	if w.set && w.d <= d {
		return w
	}
	return wallTimeout{d: d, set: true}
}

// waitFor blocks until the timeout, capped at maxWallWait, elapses or either
// signal closes, and reports false when ctx ends first. A nil signal never
// closes.
func waitFor(ctx context.Context, wait wallTimeout, a, b <-chan struct{}) bool {
	var fire <-chan time.Time
	if wait.set {
		t := time.NewTimer(min(max(wait.d, 0), maxWallWait))
		defer t.Stop()
		fire = t.C
	}
	select {
	case <-ctx.Done():
		return false
	case <-fire:
	case <-a:
	case <-b:
	}
	return true
}
