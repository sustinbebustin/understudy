# ADR 0005: State cells with typed post-commit effects and a gate

Status: accepted, 2026-10-08.

## Context

Handlers mutate state, wake waiters, arm timers and start deliveries. `testing/synctest` advances only when every goroutine is durably blocked, and a mutex hand-off is not durable blocking (`go-toolchain.md 1`), so waits must be channel-based. Snapshots need a consistent cut. S3 conditional writes must be evaluated under the same lock as the commit (`s3.md` Implications 8). Proposals offered a global lock, one mutex per service with a `lockcheck` build tag, and per-partition cells with effects run after unlock.

## Decision

`store.Cell[S]` holds one partition's state behind a mutex; `store.Write(cell, fn)` runs `fn(state, tx)` under the lock and returns typed effects recorded on `tx` (`Wake`, `Arm`, `Disarm`, `Deliver`) that the kernel runs after unlock, in order. `store.Partitioned[K, S]` lazily creates one cell per key: `aws.Scope` by default, a bucket name for S3 buckets plus a partition-wide index cell, one key per account for IAM and STS. A kernel-wide `store.Gate` (`sync.RWMutex`) is taken on the read side by every write and on the write side by snapshot, reset and load.

Rules: a handler holds at most one cell lock; it never calls `kernel.Invoke` while holding it (synchronous cross-service calls happen before the lock, asynchronous ones are effects); no I/O under a lock (blobs are written first, the cell publishes the blob id); validate first, mutate last, so a returned error leaves the state unchanged; waiting uses wake channels, scheduler channels and `context`, never a mutex hand-off. The kernel marks a `Call` as holding inside `Write`; `Invoke` on a holding call panics under `-tags lockcheck`, which CI runs once.

## Alternatives considered

- One mutex per service: fewer moving parts, and snapshots take all locks in registry order. Rejected because unrelated accounts and regions contend, per-bucket conditional-write races need a finer lock anyway, and parallel tests isolated by account would serialize.
- Global lock: long polls and Lambda waits would contend for no reason.
- Per-resource actors or goroutines: snapshots and multi-resource invariants (SNS subscription counters, SQS redrive-allow checks) become cross-actor protocols for no fidelity gain.
- Untyped `tx.After(func())` effects: simpler, but typed effects let service tests assert exactly which timers and deliveries a mutation produced.

## Consequences

- Every cross-service hop is a journal edge.
- Effects run after commit, so SNS returns `MessageId` before fan-out; the delivery is state in the outbox the moment the call returns (ADR 0015).
- The S3 bucket-name index cell is the one place two cells are touched in sequence (index, then bucket); it is written as two commits with a tombstone, never nested.

## Amendments (review, 2026-10-08)

- `store` is a leaf that knows no effects: `Cell.Update` and `Read`, `Partitioned` (with `Lookup`, `Delete`, `Export`, `Import`, `Reset`), `Waiters` and `Section`. `kernel.Write(c *Call, cell, fn)` is the only write entry point for handlers and timer callbacks (engineering C1, M1): it marks the `Call` as holding (a nested `Write` or `Invoke` returns `Internal`; the check is always on and the `lockcheck` build tag is gone), passes the commit instant read under the lock (fidelity F17), stamps the call's `Cause` on effects and counts them in the work tracker.
- `Arm` and `Disarm` are applied inside the critical section in commit order (engineering M2); only `Wake` and `Deliver` run after unlock. Timer callbacks and `Rearm` re-read state and do nothing when their condition no longer holds. A rapid state machine interleaves receive, delete and change-visibility against timer state.
- Lock hierarchy (engineering M3): gate, then one cell, then leaf locks (clock mode, scheduler heap, waiters, journal ring, ids streams, fault budgets, outbox). Leaf locks never call out; the scheduler releases its mutex before dispatching a timer.
- `R` never aliases `S` (engineering M10): writes return values or clones; persisted records are immutable values replaced on change.
- IAM and STS cells are `Partitioned[aws.AccountID, S]`; reads use `Lookup`, which never creates a cell; `Export` skips zero-valued cells (engineering m6).
- `Waiters.Wait(ctx, key)` and `Clock.Until(ctx, d)` end with their context, so long-poll loops leak nothing (engineering m8).
- The snapshot cut takes the gate's write side for microseconds and does no I/O (ADR 0009).
