package clock

import (
	"math"
	"time"
)

// MaxScale is the largest factor a Scaled clock accepts.
const MaxScale = 10000

// MaxSkew is the farthest emulator time may be set from wall time: New,
// Restore, Advance, AdvanceTo and Resume refuse a farther instant with a
// *RangeError. Running keeps emulator time as a time.Duration offset from wall
// time, which spans about 292 years each way; the bound keeps every offset and
// every difference between two settable instants inside that range. A Scaled
// clock may run past it on its own, after which it can still be frozen,
// rescaled or restored.
const MaxSkew = maxSkewYears * 365 * 24 * time.Hour

const maxSkewYears = 100

// Mode is how emulator time relates to wall time.
//
//sumtype:decl
type Mode interface{ isMode() }

// Running keeps emulator time at wall time plus Offset.
type Running struct{ Offset time.Duration }

// Frozen holds emulator time at At. Timers that are already due still fire.
type Frozen struct{ At Instant }

// Scaled runs emulator time Factor times faster than wall time from the
// anchor pair: emulator time is AnchorEmu + (wall - AnchorWall) * Factor,
// with 0 < Factor <= MaxScale.
type Scaled struct {
	Factor     float64
	AnchorWall time.Time
	AnchorEmu  Instant
}

func (Running) isMode() {}
func (Frozen) isMode()  {}
func (Scaled) isMode()  {}

// validScale reports whether f is an accepted Scaled factor; NaN is not.
func validScale(f float64) bool { return f > 0 && f <= MaxScale }

// parseMode accepts a Running, Frozen or Scaled value with a valid factor.
// Pointers to modes satisfy the interface through the value receivers of
// isMode, so they and nil are refused with a *ModeError here rather than
// reaching the fall-through returns of the mode switches below.
func parseMode(m Mode) (Mode, error) {
	switch m := m.(type) {
	case Running, Frozen:
		return m, nil
	case Scaled:
		if !validScale(m.Factor) {
			return nil, &ScaleError{Factor: m.Factor}
		}
		return m, nil
	}
	return nil, &ModeError{Mode: m}
}

// hasInstant reports whether any instant field of m is set.
func hasInstant(m Mode) bool {
	switch m := m.(type) {
	case Running:
		return m.Offset != 0
	case Frozen:
		return !m.At.IsZero()
	case Scaled:
		return !m.AnchorWall.IsZero() || !m.AnchorEmu.IsZero()
	}
	return false
}

// emuAt returns emulator time under m when the wall clock reads wall.
func emuAt(m Mode, wall time.Time) Instant {
	switch m := m.(type) {
	case Running:
		return FromWire(wall.Add(m.Offset))
	case Frozen:
		return m.At
	case Scaled:
		return addScaled(m.AnchorEmu, wall.Sub(m.AnchorWall), m.Factor)
	}
	return Instant{}
}

// checkSkew refuses an at more than MaxSkew from wall with a *RangeError.
// Sub saturates at the bounds of time.Duration, which lie beyond MaxSkew, so
// an instant too far to subtract is refused too.
func checkSkew(at Instant, wall time.Time) error {
	if d := at.Wire().Sub(wall); d > MaxSkew || d < -MaxSkew {
		return &RangeError{At: at, Wall: wall}
	}
	return nil
}

// shifted returns m with emulator time moved d later.
func shifted(m Mode, d time.Duration) Mode {
	switch m := m.(type) {
	case Running:
		return Running{Offset: m.Offset + d}
	case Frozen:
		return Frozen{At: m.At.Add(d)}
	case Scaled:
		return Scaled{Factor: m.Factor, AnchorWall: m.AnchorWall, AnchorEmu: m.AnchorEmu.Add(d)}
	}
	return m
}

// rebased returns a mode of the same kind as m (and, for Scaled, the same
// factor) under which emulator time is start when the wall clock reads wall.
func rebased(m Mode, start Instant, wall time.Time) Mode {
	switch m := m.(type) {
	case Running:
		return Running{Offset: start.Wire().Sub(wall)}
	case Frozen:
		return Frozen{At: start}
	case Scaled:
		return Scaled{Factor: m.Factor, AnchorWall: wall, AnchorEmu: start}
	}
	return m
}

// anchored returns m with its zero instant fields set from wall: a zero
// Frozen.At or Scaled.AnchorWall becomes wall, and a zero Scaled.AnchorEmu
// becomes AnchorWall, so a mode built without instants starts at wall time.
func anchored(m Mode, wall time.Time) Mode {
	switch m := m.(type) {
	case Running:
		return m
	case Frozen:
		if m.At.IsZero() {
			m.At = FromWire(wall)
		}
		return m
	case Scaled:
		if m.AnchorWall.IsZero() {
			m.AnchorWall = wall
		}
		if m.AnchorEmu.IsZero() {
			m.AnchorEmu = FromWire(m.AnchorWall)
		}
		return m
	}
	return m
}

// wallWait returns how much wall time passes under m while emulator time
// moves by emu, rounded up so a waiter never wakes early. It reports false
// under Frozen, where wall time never moves emulator time.
func wallWait(m Mode, emu time.Duration) (time.Duration, bool) {
	switch m := m.(type) {
	case Running:
		return emu, true
	case Frozen:
		return 0, false
	case Scaled:
		return clampDuration(math.Ceil(float64(emu) / m.Factor)), true
	}
	return 0, false
}

// addScaled returns at + d*f. At MaxScale the product leaves the range of
// time.Duration after about 10.7 days of wall time, so a larger product is
// added as whole seconds and a nanosecond remainder instead of saturating.
// Past that range float64 resolves the product to within a few milliseconds;
// Now's high-water mark hides any step back that rounding could cause.
func addScaled(at Instant, d time.Duration, f float64) Instant {
	ns := float64(d) * f
	if math.Abs(ns) < 1<<63 {
		return at.Add(time.Duration(ns))
	}
	sec := math.Trunc(ns / 1e9)
	t := at.Wire()
	return FromWire(time.Unix(t.Unix()+int64(sec), int64(t.Nanosecond())+int64(ns-sec*1e9)))
}

// clampDuration converts x nanoseconds to a time.Duration, saturating at its
// bounds.
func clampDuration(x float64) time.Duration {
	switch {
	case x >= math.MaxInt64:
		return math.MaxInt64
	case x <= math.MinInt64:
		return math.MinInt64
	default:
		return time.Duration(x)
	}
}
