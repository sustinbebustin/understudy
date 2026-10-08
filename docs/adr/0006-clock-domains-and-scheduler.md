# ADR 0006: Three time domains, a forward-only stepwise clock and a heap scheduler

Status: accepted, 2026-10-08.

## Context

The brief promises one controllable clock for every time-based behavior. The G4 research showed that containers, SDK signers and runtime interface clients read the host wall clock and cannot be virtualized; a stale `Lambda-Runtime-Deadline-Ms` hands aws-lambda-go a dead context and Python a remaining time of 0; SDKs correct skew from the response `Date` header; STS expirations are compared against the client's wall clock (`lambda-runtime-contract.md 3 to 4`). Proposals differed on the time type (`time.Time` vs a distinct type), on whether `Set` may move backwards, and on per-waiter goroutines vs a central scheduler.

## Decision

- Three domains with distinct types: `clock.Instant` (emulator time; `Wire()` is the only conversion to `time.Time`), `Clock.Wall()` (wall time), `Clock.Stopwatch()` (monotonic durations). `forbidigo` bans `time.Now`, `time.Sleep`, timers and tickers outside `internal/clock`.
- Which behavior reads which domain follows `lambda-runtime-contract.md 3` in full: emulator time for everything only the emulator observes (visibility, delays, retention, dedup, ESM windows, async retries, schedules, TTL, deletion windows, server timestamps, presign expiry); wall time for `Lambda-Runtime-Deadline-Ms`, SigV4 skew, the `Date` header, STS `Expiration`, `slog`; monotonic for function timeouts, REPORT durations, init limits, idle reaping, injected latency; `PutLogEvents` windows accept if valid against wall or emulator.
- Modes: `Running{Offset}`, `Frozen{At}`, `Scaled{Factor, anchors}` with `0 < Factor <= 10000`. Emulator time never moves backwards: `Set` to an earlier instant is refused (`reset` and `snapshot load` are the only ways back).
- A heap scheduler keyed by `TimerKey{Service, Kind, ID}`: `Arm` replaces, `Disarm` is idempotent, tasks fire in `(due, seq)` order on one goroutine. `Advance` is stepwise: each callback observes `Now() == due`, tasks armed by callbacks fire in the same advance if due, waiters are woken once. `Advance` does not wait for real work; `--settle` does.
- Blocking requests use `Clock.Until(Deadline{Emu, WallCap})`: a frozen clock never holds an HTTP request longer than AWS would (`sqs.md 13.11`).
- ESM visibility under `Frozen` and `Scaled`: the poller pins held messages invisible until the invocation returns through an internal, non-routable SQS operation; under `Running` AWS semantics apply unchanged.

## Alternatives considered

- `time.Time` everywhere: a wall deadline compared with an emulator instant is a silent bug class; the distinct type makes every conversion greppable.
- Per-waiter goroutines instead of a heap: simpler, but after `Advance` the firing order of due timers depends on goroutine scheduling, and "fire twelve schedule events stamped 5, 10, ... 60 minutes" is not expressible.
- Allowing backward `Set`: already-committed deadlines would run in reverse and monotonic sequence numbers, receipt handles and log tokens would break.
- Rational scale factor: exact, but `Now()` under scaling is anchored to wall time anyway; float with a bounded range is enough.

## Consequences

- Deterministic timer order under advance, which snapshots and the journal rely on.
- Users must be told that user code calling `time.time()` is not steered and that a frozen clock never fires a function timeout; functions read emulator time from the clock endpoint on the container listener.
- The real `Clock` runs unmodified inside `synctest` bubbles because wall reads go through `time.Now`.

## Amendments (review, 2026-10-08)

- Timer callbacks never block (engineering C2, fidelity F2). A callback may `kernel.Write` and start tracked work; anything that calls `kernel.Invoke`, waits on a container or long-polls runs as tracked work (`Kernel.Go`) stamped with the due instant (`Target.At`). `Advance` therefore never waits on container work, and the twelve `rate(5 minutes)` events are stamped 5, 10, ... 60 minutes because the work they start carries the due instant. A synctest test schedules a task that blocks forever and asserts a later timer still fires on `Advance`.
- In every mode a timer with `due <= Now()` fires at once; Frozen only stops `Now()` from moving (fidelity F3, product C1).
- Two timer classes: AWS semantics (visibility, delay, retention, schedules, retries, deletion windows, TTL) follow the emulator clock; provisioning settle (Lambda `State` and `LastUpdateStatus`, ESM transients, SQS `QueueDeletedRecently`) is final before the mutating call returns, in every mode, by default. The fidelity switches `lambda.stateTransitions: clock` and `sqs.queueDeletedRecently: enforce` restore clock-driven intermediates for waiter tests. A frozen clock therefore never hangs `terraform apply`.
- Timers carry no closure (engineering M11): `TimerKey{Kind TimerKind; ID string}`, the scheduler dispatches by `Kind` to the handler the service registered with `Kernel.Timers().Handle`, and arming an unregistered kind fails at bind time.
- `Until(ctx, d)` and `Waiters.Wait(ctx, key)` end with their context (engineering m8).
- The ESM pin is the overlay-declared internal operation `sqs.HoldVisibility` (ADR 0002).
