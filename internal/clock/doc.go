// Package clock provides the emulator clock: Instant, Clock, the Scheduler
// that dispatches by timer kind, Deadline, Wall and Stopwatch.
//
// All time-based behavior runs on this clock so it can be frozen, advanced or
// scaled. It is the only package allowed to read the wall clock.
//
// Three time domains have distinct types. Instant is emulator time and
// follows the Mode; Wall is the host's wall time, for values clients compare
// with their own clocks; Stopwatch measures monotonic wall durations that
// emulator modes never touch.
//
// Emulator time never moves backwards, except through Restore, which reset
// and snapshot load use together with Scheduler.Clear. Advance and AdvanceTo
// move it forward stepwise, firing due timers in (due, seq) order with Now
// held at each due instant (or, for a timer already overdue, at the latest
// instant the advance has reached); a handler that arms a timer due before
// the end of the advance sees it fire in the same advance. In every mode a
// timer due at or before Now fires at once; Frozen only stops Now from
// moving. The driver (Run) fires timers as wall time passes. Handlers run
// outside the clock's data lock but are serialized with each other, with
// advances and with mode changes, so a handler must not block, advance the
// clock or change its mode.
//
// Decisions the architecture leaves open:
//
//   - Options.Start, when set, fixes emulator time at construction and the
//     mode supplies only its kind and factor, so configuration says "start
//     at this instant in this kind of mode" without computing offsets or
//     anchors from wall time. A mode that also sets its instant fields is
//     refused rather than silently overridden. Without Start, zero instant
//     fields of the mode default to wall time at construction, so Frozen{}
//     and Scaled{Factor: f} start from now.
//   - Restore(at, mode) ignores the mode's instant fields instead of refusing
//     them: snapshot load passes the recorded mode, whose offset and anchors
//     refer to the wall clock of the process that recorded it.
//   - Mode is a sealed interface, but the value receivers of its marker
//     method let pointers to modes satisfy it, so New and Restore validate
//     the mode at the boundary and refuse anything else with a *ModeError.
//   - Freeze, Resume, Scale and Restore return a ModeChange with the
//     replaced mode read under the same lock as the change, so the journal's
//     ClockChanged{From, To} cannot interleave with a concurrent change.
//   - The driver and Until wake at least once a second while they wait on
//     wall time: their timers are monotonic, Running emulator time follows
//     the wall clock, and a wall step or host suspend must not delay a timer
//     or a long poll by the rest of the old wait.
//   - The OnAdvance hook runs after the advance releases every lock, so it
//     may change the mode or advance the clock again.
//   - A timer handler receives its due instant. During an advance Now equals
//     it; when the driver fires a timer as wall time passes, Now may already
//     be later, and work the handler starts is stamped with the due instant.
//   - Advance(0) and AdvanceTo(Now) are allowed and fire overdue timers.
//   - Emulator time is set only within MaxSkew (100 years) of wall time, so
//     Running's time.Duration offset, about 292 years each way, never
//     saturates; a farther instant is refused with a *RangeError instead of
//     wrapping or sticking. A Scaled clock may still run past the bound, so
//     Resume returns an error, and Scaled time is computed in whole seconds
//     once its scaled elapsed time leaves the range of time.Duration.
//   - Instant implements encoding.TextMarshaler as RFC 3339 with nanoseconds
//     so persisted state can hold Instants.
package clock
