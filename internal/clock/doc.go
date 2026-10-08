// Package clock provides the emulator clock: Instant, Clock, the Scheduler
// that dispatches by timer kind, Deadline, Wall and Stopwatch.
//
// All time-based behavior runs on this clock so it can be frozen, advanced or
// scaled. It is the only package allowed to read the wall clock.
package clock
