// Package kernel is the one pipeline every call runs through, whether it came
// from an SDK or from a cross-service delivery.
//
// It holds Call, Handler, Op, Binder, Bindings, Dispatch, Invoke, Write, Tx,
// the timer kinds, the work tracker and the Stateful view used by snapshots.
// It never imports a codec or the snapshot package.
//
// Construction order: NewTimers, then clock.New with Timers.Scheduler() as
// the clock's scheduler, then journal.New over that clock, then New. The
// scheduler dispatches due timers into the table, which New attaches to the
// kernel; services register their kinds with Timers().Handle and bind their
// operations through Binder. Run drives the clock and owns every goroutine
// started with Go.
//
// Decisions the architecture leaves open:
//
//   - Services are constructed after New, from the ServiceDeps it feeds, so
//     a bind mistake (an operation bound twice or outside its binder's
//     service, a timer kind registered twice) cannot fail New. Bindings
//     record it; BindErr reports every one, wire.go checks it once the
//     services exist, and Run refuses to start while it is non-nil.
//     Bindings are atomic per operation, so a test can bind a stub while
//     the kernel runs.
//   - Write takes the Partitioned and the key instead of a cell: a cell
//     that reset or snapshot load retired is resolved again with For, which
//     needs both. The architecture's Write(c, cell, fn) cannot re-resolve.
//   - Validate is generic over the input type, Validate[In](violations):
//     the policy the generator copies into each api package is reached
//     through the input type's ValidationError method (ARCHITECTURE.md 3.3,
//     interim contract), which a *registry.Operation cannot name.
//   - Result has a third variant, StatusOKFailure, for S3's 200-with-error
//     answer, so the after-commit fault decision FailStatusOK has a result
//     the edge renders with status 200.
//   - AsService carries the Source ARN of the delivery besides the service
//     principal's name, because the access recorder pairs a delivery with
//     the target's resource policy through it.
//   - A call naming an operation its service does not model has a nil Op;
//     NewCall resolves the operation id itself, and Dispatch journals the
//     call and answers the edge's rejection without reaching a handler.
//   - Each due timer is journaled as a TimerFired event, and its handler's
//     synthetic call is that event: the calls it invokes and the deliveries
//     it makes are the event's children in the event's trace, so the work a
//     timer causes traces back to it. The synthetic principal is anonymous;
//     nothing it does is access-recorded.
//   - Settle counts, as TimerDue, timers already due in every clock mode
//     and timers the clock is firing, from the moment it takes one off the
//     scheduler until its handler returns (clock.Scheduler.DueOrFiring);
//     timers not yet due never count, so a frozen clock settles.
//   - An internal call made as an execution role runs as an assumed-role
//     session of that role named "understudy": the access recorder keys on
//     the role, and no environment's session exists for kernel-made calls.
//   - The journal records a call's input as the encoding/json rendering of
//     its generated Redact(); an input that does not encode is logged and
//     journaled without input rather than failing the call.
//   - Strict mode checks outputs codec-independently: the output is
//     serialized through a serializer that writes nothing and notes every
//     required timestamp member left at the zero time.
package kernel
