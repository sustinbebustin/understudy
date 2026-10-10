package clock

import (
	"fmt"
	"time"
)

// BackwardsError reports a request to move emulator time to an earlier
// instant. The clock is unchanged.
type BackwardsError struct {
	From, To Instant
}

func (e *BackwardsError) Error() string {
	return fmt.Sprintf("clock: emulator time is %s and cannot move back to %s; the clock is unchanged (reset or load a snapshot to return to an earlier time)",
		e.From, e.To)
}

// ModeError reports a Mode value that is not a Running, Frozen or Scaled
// value, such as a pointer to one, which satisfies the interface but is not a
// mode. Nothing changed: New created no clock and Restore left the clock as
// it was.
type ModeError struct {
	Mode Mode
}

func (e *ModeError) Error() string {
	return fmt.Sprintf("clock: %#v is not a clock mode; pass a Running, Frozen or Scaled value, not a pointer or nil; nothing changed", e.Mode)
}

// StartConflictError reports Options with both Start and a Mode that carries
// its own starting instant (a non-zero Running.Offset, Frozen.At,
// Scaled.AnchorWall or Scaled.AnchorEmu). No clock was created.
type StartConflictError struct {
	Start Instant
	Mode  Mode
}

func (e *StartConflictError) Error() string {
	return fmt.Sprintf("clock: Options.Start %s and the instant fields of mode %#v both set the starting time; no clock was created (set Start with a mode whose instant fields are zero, or leave Start zero)",
		e.Start, e.Mode)
}

// RangeError reports an emulator time more than MaxSkew from the wall time
// Wall. Nothing changed: New created no clock, and Restore, Advance,
// AdvanceTo and Resume left the clock as it was.
type RangeError struct {
	At   Instant
	Wall time.Time
}

func (e *RangeError) Error() string {
	return fmt.Sprintf("clock: emulator time %s is more than %d years from wall time %s, outside the range the clock supports; nothing changed (choose an instant within %d years of wall time, or reset or load a snapshot to return to one)",
		e.At, maxSkewYears, e.Wall.UTC().Format(time.RFC3339Nano), maxSkewYears)
}

// ScaleError reports a scale factor outside (0, MaxScale]. The clock mode is
// unchanged.
type ScaleError struct {
	Factor float64
}

func (e *ScaleError) Error() string {
	return fmt.Sprintf("clock: scale factor %g is outside (0, %d]; the clock mode is unchanged", e.Factor, MaxScale)
}
