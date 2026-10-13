// Package pool tracks warm execution environments and reuses them across
// invocations of a function version.
//
// An Env is one container and its Runtime API listener. Acquire leases one
// exclusively: the most recently used idle environment with the same Key,
// or a new one started under a semaphore (default 16 at once). One
// invocation runs per environment; concurrency comes from more
// environments. Release returns it with the invocation's outcome: success
// and function errors keep it warm, any failing outcome (timeout, crash,
// OOM, init failure, architecture mismatch) removes its container so the
// next invocation starts cold. Idle environments are capped per function
// version (default max(4, CPUs), least recently used evicted), all
// environments by a global cap (default 64, evicting the least recently
// used idle one), and reaped after the idle timeout (default 10 minutes)
// measured on monotonic wall time, never on the emulator clock. A newer
// generation of a function drains its older idle environments at once and
// its busy ones on release. Removal closes the listener before deleting
// the container (ARCHITECTURE.md 14.2). Run consumes the launcher's exit
// stream, so a crashed container fails its invocation and leaves the pool.
//
// Decisions the architecture leaves open:
//
//   - The states add Reserved between a lease and its dispatch, so Busy
//     always names the invocation it runs. Busy carries no deadline: the
//     runtime API computes it at dispatch, after a cold start.
//   - Fault causes are named InitFailed, Crashed, OutOfMemory, TimedOut and
//     WrongArchitecture: values, not error types, so none takes the *Error
//     suffix error types carry. Drain reasons add Abandoned, for an
//     environment whose leaseholder gave up mid-invocation.
//   - Acquire takes the Spec and derives the Key itself (NewKey), so a key
//     can never disagree with the spec it starts. The key hashes every
//     Spec field, credentials included, so the Lambda service must keep a
//     generation's credentials stable for warm reuse. Release takes a
//     context for the container removal it may do and returns removal
//     errors. Acquire never returns a lease together with an error:
//     failing to remove environments drained on the way (an older
//     generation, an evicted idle one) is logged. A removal ignores the
//     context's cancellation, so an abandoned lease still frees its
//     container, but keeps its deadline and gives up after RemoveTimeout
//     at most, so a wedged Docker daemon cannot hold shutdown forever.
//   - IdleTimeout is at least one second (MinIdleTimeout); reaping runs
//     every quarter of it, at most every 30 seconds.
//   - A Starting environment waits only for its container to start; the
//     runtime's init happens inside the first Invoke.
//   - The global cap refuses with *CapacityError when every environment is
//     leased rather than waiting; the service answers throttled.
//   - Environment IDs are 32 random hex characters from crypto/rand: they
//     name containers and log streams but no identifier of the ids package
//     covers them yet.
//   - Concurrency limits (account 1000, reserved concurrency) belong to the
//     Lambda service, checked before Acquire.
package pool
