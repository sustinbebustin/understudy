# Engineering review of the phase 1 architecture

Reviewer lens: Go engineering quality. That covers package boundaries and dependency direction, the core interfaces, concurrency and locking, the error model, testability, the generated versus hand-written boundary, the cost of adding an operation or a service, and anything likely to rot. Inputs: [ARCHITECTURE.md](../ARCHITECTURE.md), ADRs 0001 to 0018, [go-toolchain.md](../../research/phase1/go-toolchain.md) and the three proposals the architecture merges.

Severity:
- **critical**: will not compile as written, or breaks a core promise of the brief.
- **major**: will cause bugs, flaky tests or rework in waves 1 to 3 unless decided now.
- **minor**: cheap to fix now and expensive later.

## Verdict

The skeleton is sound and should stay. These choices are right:
- one pipeline for every call, with no HTTP loopback
- typed per-operation binding with no generator run
- timers rebuilt by re-arming instead of being serialized
- a distinct `clock.Instant` type
- `jsonx` as the only importer of json/v2
- explicit `wire.go` and no `init()` registration
- one codec per protocol behind generated serde

The weak spot is where `store`, `kernel`, the scheduler and `delivery` meet. The merge made two edits to platform-dx's design:
1. It replaced the untyped `tx.After(func())` with a typed `Deliver{delivery.Request}` effect.
2. It turned `Cell.Write` into a free function that never sees the `Call`.

Those two edits broke the dependency graph, the lock assertion and cause stamping. The scheduler contract also allows blocking callbacks. Fix C1 to C3 and M1 to M5 before wave 1 builds the spine on them.

## Critical

### C1. The stated dependency graph has import cycles (sections 2, 3.2, 3.3, 3.5, 10)

These are the edges as the types are written:

| Cycle | Edges |
|---|---|
| `awserr` and `registry` | `awserr.Unimplemented{Op registry.OperationID}`, `awserr.NotImplemented(op *registry.Operation)` and `awserr.Validate(op *registry.Operation, ...)` point one way. `registry.Operation.Errors []*awserr.Identity` points back. |
| `awserr` and `fault` | `awserr.Injected{Rule fault.RuleID}` points one way. Fault actions resolve to `*awserr.Error`, and rule validation reads `[]*awserr.Identity`, which points back. |
| `kernel` and `journal` | Section 2 states that `kernel` depends on `journal`. But `journal.APICall.Origin` is a `kernel.Origin`, and `journal` also imports `registry`, `identity`, `fault` and `clock`. That contradicts "journal depends on nothing above it". |
| `kernel` to `store` to `delivery` to `kernel` | `store.Deliver{Request delivery.Request}`, and every delivery attempt calls `kernel.Invoke`. |
| `registry` and `kernel` | `Operation.invoke erasedHandler` must accept a `*kernel.Call`, and `kernel.Call.Op` is a `*registry.Operation`. |

Recommendation:
- **Leaf package for identifiers.** Add one leaf package (for example `internal/ref`) that holds only identifiers and imports only `aws`: `ServiceID`, `OperationID`, `RuleID`, `EventID`, `Cause`, `RequestID` and `Via`.
- **`awserr`:** it holds plain values (`Unimplemented{Op ref.OperationID}`, `Injected{Rule ref.RuleID}`). The helpers that need descriptors (`NotImplemented`, `Validate`) move to `kernel`.
- **`journal`:** it owns its record types. `Origin` either moves to the leaf package or `journal` defines its own copy. `journal` never imports `kernel`.
- **`store`:** it knows no effect variants. Either type `Tx[E]` over a kernel-defined effect sum, or move `Write` into `kernel` (see M1).
- **Import test first.** Land the import-walking test as the first commit of wave 1, with the allowed edges kept as data. Section 2 cannot then drift from the code.

### C2. The scheduler runs callbacks on one goroutine, and the callbacks block (sections 9, 14.8, 16)

Delivery attempts and ESM poll cycles are "scheduler tasks that call `kernel.Invoke`". Several of those calls block:
- The ESM invocation blocks for the function's wall-clock duration, up to 900 s.
- A Request-phase `Latency{Wall}` fault sleeps inside the pipeline.
- A long-poll `ReceiveMessage` parks for up to 20 s.

There is only one scheduler goroutine. One slow function therefore stalls every visibility expiry, schedule and retry in the process. Under `Advance`, the stepwise loop blocks on container work, which contradicts "Advance does not wait for real work".

Moving the work off the scheduler goroutine exposes a second bug. Internal `Invoke` stamps `Admitted = clock.Now()`, so after `advance 1h` every deferred delivery is stamped T+60m. That breaks the stated property that the twelve schedule events are stamped 5, 10 and so on up to 60 minutes.

Recommendation:
- State the task contract: `fire(now)` must not block. It may write cells and record effects.
- Spawn anything that calls `Invoke` as tracked work (`tracker.Go`) that carries the due instant.
- `Invoke` takes the admission instant from its trigger: a `Target.At` field, or the parent's instant. It never calls `Now()` for deferred work.
- Add a synctest test where one scheduled task blocks forever and a later timer still fires on `Advance`.

### C3. `Bind` mutates package-level generated descriptors (sections 3.2, 3.4)

`OpSendMessage = kernel.NewOp(&descSendMessage)` is a package variable, and `Operation.Coverage` and `Operation.invoke` are "set by Bind". Several kernels can exist in one process:
- `understudytest` instances in parallel Go tests
- the in-process e2e run
- every synctest service test

All of them share the same descriptors and overwrite each other's handlers. The result is a data race under `-race`, and test A's SQS handler can end up closing over test B's state. It is also the mutable global state that `gochecknoglobals` exists to forbid.

Recommendation:
- Generated descriptors become immutable.
- Bindings live in the per-kernel `Registry`, indexed by a generated ordinal or by descriptor pointer.
- `Op[In, Out]` stays a static handle, and `Invoke` resolves the handler through `k.registry`.
- Runtime coverage is read from the registry, never from the descriptor.
- Add a `-race` test that builds two kernels concurrently with different handlers.

## Major

### M1. `store.Write` never receives the `Call`, so the lock check and cause stamping cannot be built (sections 3.5, 8 rule 1, ADR 0005)

Section 8 says "the kernel marks the Call as holding inside `store.Write`". But `Write(c *Cell[S], fn)` never receives a `Call`, and neither does the long-poll example. Go has no goroutine-local state, so three things have no way to reach the `Call`:
- the `holding` flag
- "causes come from the pipeline, never from handler code": today `delivery.Request.Cause` has to be filled in by the handler
- work-tracker counting of effects

Nothing enforces "at most one cell lock" either. A `Write` on cell B inside a `Write` on cell A is an ABBA deadlock waiting to happen.

Recommendation:
- `kernel.Write(c *Call, cell, fn)` becomes the only write entry point for handlers. It sets and clears `holding`, refuses a nested `Write` or `Invoke` while holding, stamps the `Cause` on effects from `c`, and counts effects in the tracker.
- Writes that start in the scheduler get a synthetic `Call` with `Origin Internal{Via: timer}`, so there is one write path.
- The check is a bool compare, so keep it always on. It returns an `Internal` error instead of panicking (the lint config bans `panic`). Delete the `lockcheck` build tag: a path gated by a build tag that CI "runs once" will rot.

### M2. Effects run after unlock, out of order with later commits (section 3.5, ADR 0005)

Example: write W1 (a receive) records `Arm(visibility m1)` and unlocks. Write W2 (`DeleteMessage`) commits and runs `Disarm(m1)`. W1's effect runner runs afterwards, and the result is a ghost timer for a deleted message. Out-of-order `Wake` is harmless; out-of-order `Arm` and `Disarm` are not.

Recommendation:
- Apply `Arm` and `Disarm` inside the cell's critical section, in commit order. They are in-memory heap operations under a leaf lock.
- Defer only `Wake` and `Deliver` until after unlock.
- Separately, state the rule that every timer callback re-reads state and does nothing if its condition no longer holds. `Rearm` needs the same rule.
- Cover it with a rapid state machine that interleaves receive, delete and change-visibility.

### M3. "No lock ordering exists to get wrong" is false (section 8, ADR 0005 Consequences)

The design has these locks:
- the gate (`RWMutex`)
- the cell
- the `Partitioned` map
- the clock mode
- the scheduler heap
- the waiter registry
- the journal ring
- the ids streams
- the fault budgets
- the Lambda pool

The long-poll example calls `Clock().Now()` inside `Write`, which takes the clock lock under the cell lock. Scheduler callbacks write cells, which takes the cell lock under the scheduler lock if the callbacks fire while the scheduler holds its own.

Recommendation: document the hierarchy as gate, then cell, then leaf locks (clock, scheduler, waiters, journal, ids, fault).
- Leaf locks never call out and never call back.
- The scheduler and `Advance` release their mutexes before running callbacks.

Put this in the contributor checklist next to rule 1 of section 8.

### M4. Snapshot and load do I/O and pipeline calls under the gate's write side (sections 8 rule 5, 13, ADR 0009)

`Export(w *snapshot.Writer)` writes tar entries and blobs while every cell write in the process is blocked.

DynamoDB is worse. Save runs `DescribeTable` and a paginated `Scan` through the proxy, and load runs `CreateTable` and `BatchWriteItem`, all "under the gate's write side". If those calls go through `kernel.Invoke`, they reach the shim cells, which take the gate's read side, and the process deadlocks on itself. If they bypass the pipeline, the document says the opposite.

Recommendation: split save into two phases.
1. Under the gate, each cell serializes to an in-memory section and pins its blob references. That is the consistent cut.
2. Release the gate, then write the tar.

Then:
- **Export signature:** `Stateful.Export` becomes `Export() (Section, error)`, with no I/O handle.
- **DynamoDB items:** they are not cell state. Export them after the cut and import them before the shim cells are loaded. Quiesce the proxy with an admission flag, not the gate.

### M5. `settle` depends on the work tracker's definition, which is unclear (sections 4, 16 Settling, ADR 0015)

The tracker "counts in-flight deliveries, queued async invokes, running invocations and due-but-unfired timers". The document does not say whether a parked long poll counts. The ESM poller "calls SQS through `kernel.Invoke`":
- If the poller long-polls and in-flight calls count, the tracker never reaches zero.
- If in-flight internal calls do not count, a delivery's `SendMessage` can be missed.

Every conformance and e2e test uses `settle` as its oracle, so this ambiguity turns into hangs or flaky tests.

Recommendation:
- Make `WorkKind` a closed enum: `EffectPending`, `DeliveryAttempt`, `AsyncInvokeQueued`, `InvocationRunning`, `TimerDue`.
- Explicitly exclude parked waiters: client long polls and idle ESM pollers.
- The ESM poller waits on the queue's `WaitKey`, then calls `ReceiveMessage` with `WaitTimeSeconds: 0`, so it never parks inside the pipeline.
- On timeout, `settle` returns what is still pending, grouped by kind.

### M6. Internal-only operations have nowhere to live (sections 9 last paragraph, 14.6, 2)

`sqs.HoldVisibility` is "bound in Go, absent from the model". The ESM poller lives in `service/lambda/esm` and needs an `Op` handle for it. But a service may import only another service's generated `<svc>api` package, and the generator emits `Op` handles only from the model.

Lambda's "internal `PutLogEvents` bypassing the client time windows" has the same problem. Today it can only be a branch on `c.Origin` inside a public handler, which is a hidden mode.

Recommendation: add `dev.understudy#internalOperation` to the closed trait set in section 5.2. With it, the generator emits types, a validator, a descriptor and an `Op` into `<svc>api` marked `Routable: false`.
- The edge never claims it.
- Coverage lists it separately.
- The access recorder skips it.

Use the same trait for the Logs bypass.

### M7. Hand-written validation runs after fault injection (sections 2 service files, 4, ADR 0002, ADR 0008)

Generated `Validate` is step 7, faults are step 8 and the handler is step 9. `limits.go` checks run inside the handler, after faults. Examples are the SQS limit texts, CloudWatch `InvalidParameterCombination` and cross-member rules. So a request AWS would reject can still receive an injected fault, and the check order depends on who wrote the rule. ADR 0008's whole argument for validating first rests on this order.

Recommendation: let the binder accept a typed validator, for example `b.SendMessage(h, cov, registry.WithValidator(s.validateSendMessage))`, where the validator has signature `func(*In) []awserr.Violation`.
- It runs at step 7, after the generated validator and before faults.
- Validators read no state. Checks that depend on state stay in the handler.

### M8. Outcomes decided at the edge bypass the pipeline (sections 4 steps 1 to 5, 4.1 step 9, 6.3)

The edge produces these outcomes before `kernel.Dispatch`, and they must still be journaled and access-recorded:
- unknown operation
- unimplemented 501
- deserialization failures
- `Unclaimed`

That is a second journaling and recording path, and it will drift from the kernel's.

Recommendation: give the kernel one entry point.
- The edge hands it `Decoded = Ok{In} | Rejected{*awserr.Error}`.
- The binding lookup and the unimplemented decision happen in `Dispatch`, which reads the registry anyway.
- Only `kernel` calls `journal.Begin`, `journal.End` and `access.Record`.
- `Unclaimed` stays a journal-only edge event.

### M9. "Structs are the schema" requires migrations from day one and clashes with the ADT rule (section 13, ADR 0009)

Every refactor of state becomes a format change: a `stateVersion` bump, a migration and a golden fixture for every past version. That cost lands in exactly the waves when state changes most, before anything has been released.

The sealed-interface sums that the quality bar requires in state (message state, ESM state, function state) also do not round-trip through `json` tags. Each service will invent its own discriminator.

Recommendation:
- Until the first tagged release, refuse a mismatched `stateVersion` instead of migrating it; the downgrade guard already covers this. Migrations become mandatory from the first release.
- Define one tagged-union JSON convention in `jsonx`, `{"kind": "...", ...}`, with a table per sum type.
- Add a rapid round-trip property for every persisted sum.

### M10. State can escape the critical section (sections 3.5, 8 long-poll example)

`Write[S, R]` returns an `R` computed from `*S`, and the example returns `[]Message` from `st.receive`. If `Message` holds maps or slices (attributes, system attributes), the caller reads them while another goroutine mutates the queue. The API invites this race.

Recommendation: add a rule to section 3.5 that `R` never aliases `S`. Return values or explicit `Clone()` results.
- Prefer immutable message records that are replaced and never mutated in place, so sharing is safe by construction.
- Run service tests under `-race` that read results while concurrent writes run.

### M11. `Arm` effects cannot carry the timer callback (sections 3.5, 9)

`Scheduler.Arm(k, at, fire func(now))` takes a closure, but the `Arm{Key, At}` effect carries none. `Rearm` also has to rebuild callbacks after a snapshot load. `TimerKey{Service, Kind, ID string}` is three strings.

Recommendation: add a typed registry of timer kinds.
- Each service registers handlers at wiring, for example `k.Timers().Handle(sqs.TimerVisibility, s.onVisibility)`.
- `TimerKey` becomes `{Kind TimerKind; ID string}` with declared constants, and the scheduler dispatches by kind.
- Arming an unregistered kind fails at boot. It never silently drops the timer.

### M12. The determinism contract promises more than the concurrency model gives (sections 12, 13, 19 e2e row, ADR 0018)

The contract says "a seeded single-client run reproduces byte-identical journals and snapshots". But one client still fans out into concurrent internal work: one delivery per subscription, ESM pollers, async invokes and Lambda environments. Message ids, receipt handles and journal event ids are drawn in whatever order the goroutines run. As specified, the e2e golden snapshot and trace tree will be flaky.

Recommendation: choose one of these.
- **(a) Narrow the contract** to what is actually deterministic. The e2e then asserts a normalized trace tree, using the normalizers the parity replayer already has, plus a snapshot diff that ignores ids.
- **(b) Add a `--deterministic` mode** in which internal work drains on one worker in `(due, seq)` order.

Take (a) unless (b) is a product requirement. Either way, raw event ids never go into golden fixtures.

### M13. Generated code needs a size budget now, and half of it is not needed (section 5.5, ADR 0001)

The generator covers 5,487 shapes, with both `Serialize` and `Deserialize` on every shape, plus validators and redaction. All of it links into one binary the brief wants "tiny", and every test binary that imports `wire.go` compiles it.

The server only needs `Deserialize` on inputs and `Serialize` on outputs and errors, because internal calls pass Go values. Only services with `rawBinding` (internal DynamoDB `Op`s through the proxy) and the protocol-test services need both directions.

Recommendation:
- Generate each direction by the shape's role in the closure: `Deserialize` and `Validate` for shapes reachable from inputs, `Serialize` for shapes reachable from outputs and errors, and both for shapes used in both roles.
- Generate both directions everywhere only for `rawBinding` services and `protocoltest/gen`.
- Set budgets in ADR 0001 before wave 1 and enforce them in CI, so "measure and record" becomes a gate: binary size, cold `go build`, `go vet`, and golangci-lint memory.

## Minor

### m1. The handler's return pair allows illegal states (section 3.2)

`(*Out, *awserr.Error)` has four combinations, so "exactly two outcomes" is only a convention. There is also a typed-nil trap: if `*awserr.Error` implements `error`, a nil `*Error` stored in an `error` variable is non-nil. `Export`, `Deserialize` and config already return plain `error`, so the two will get mixed.

Recommendation:
- `Dispatch` maps `(nil, nil)` and `(out, err)` to `Internal`, naming the operation. Unit outputs are an empty struct, so nil is never legitimate.
- `*awserr.Error` does not implement `error`. Provide `AsError()` for the rare boundary that needs one.
- `nilnil` does not check custom pointer types, so a test covers this instead.

### m2. A handler can be bound as `Unimplemented` (section 3.4)

`var Unimplemented Coverage` is a bindable value, and `var Full Coverage` is a mutable global. The boot check "a binding names an operation absent from its model" can never fire, because binder methods are generated from the model.

Recommendation:
- Split `BindCoverage` (`Full | StoredOnly | Partial`) from the registry status `Bound{Coverage} | Unbound`.
- Replace the impossible check with a duplicate-bind check.

### m3. Closed sets are typed as strings (sections 3.2, 9, 10, 11, 14.2)

| Field | Section |
|---|---|
| `Internal.Via` and `Target.Via` | 3.2 |
| `TimerKey` fields | 9 |
| `Delivery.Mechanism` | 10 |
| `StateChange.From` and `StateChange.To` | 10 |
| `fault.Match.Operations` | 11 |
| `pool.Draining.Reason` | 14.2 |

Recommendation: use typed enums, or `ref.OperationID` for operations. `exhaustive` and `gochecksumtype` then check every switch over them.

### m4. Five vocabularies for one concept, evidence (sections 3.4, 5.2, 6.4, 15, 19)

`fidelity.Provenance` (`RecordedRaw | RecordedSDK | Doc | SDKSource | ThirdPartyRecording | Inferred`) and the overlay labels (`aws-recorded`, `third-party-recorded`, `aws-doc`, `sdk-source`, `inferred`) name the same thing differently. Parity labels, skip-list categories and `aws-differs` add three more.

Recommendation: use one `evidence.Label` type everywhere: generator output, the fidelity ledger, the parity replayer and coverage. Skip-list categories reference it.

### m5. Fidelity switches have no registration path (section 3.4)

Coverage must list every switch, but `init()` registration and globals are both banned.

Recommendation: each service exposes `Switches() []fidelity.Entry`, a type-erased view of `Switch[T]`. `wire.go` collects them alongside the builders.

### m6. `Partitioned` keys and lazy creation (sections 3.5, 8 rule 2)

IAM and STS keep "one cell per account" by keying on `aws.Scope`. That relies on every caller zeroing the region, and one that forgets creates a separate IAM cell per region. `For(k)` also creates a cell on reads, so snapshots and `Resources` grow with read traffic from arbitrary accounts.

Recommendation:
- Key IAM and STS with `Partitioned[aws.AccountID, S]`.
- Use `Lookup(k) (*Cell, bool)` for reads and keep `For` for writes.
- `Export` skips zero-valued cells.

### m7. `Stateful` repeats the same per-cell code in every service (section 13)

`Export`, `Import`, `Reset` and `SchemaVersion` are the same loop in every service: for each cell, write JSON.

Recommendation: `Partitioned` implements them, given a key codec and a state version. Services implement only `Rearm` and `Resources`. This shortens the add-a-service recipe and removes the "copy STS's skeleton" step from section 5.8.

### m8. Wait subscriptions leak (sections 8 example, 9)

`Clock().Until(d)` and `k.Waiter(key)` return channels and offer no way to cancel. Each iteration of a long-poll loop, and each ESM cycle, registers another one.

Recommendation:
- Change the signatures to `Until(ctx, d)` and `Waiter(ctx, key)`, unregistering when the context ends, or return a `stop func()`.
- Add a test that wakes a long poll 1000 times and then counts the remaining registrations.

### m9. `RawHandler` has two raw sources and no internal-call path (sections 3.2, 15, ADR 0014)

The raw request arrives both as the `raw RawRequest` parameter and as `Call.raw *RawRequest`. Internal `Op`s into DynamoDB have no raw request at all. "The kernel encodes with the awsjson codec" adds a `kernel` to `protocol` edge.

Recommendation:
- Drop `Call.raw`.
- `RawHandler` receives `Wire = FromClient{RawRequest} | Synthesized`.
- The DynamoDB service encodes `Synthesized` itself, so `kernel` never imports a codec.

### m10. `Call` exposes mutable fields to handlers (section 3.2)

Handlers receive `*Call` and can reassign `Scope`, `Principal` or `Admitted`.

Recommendation: make the fields unexported with accessors. `Touch` stays the only mutator.

### m11. "`errors.go` for hand-owned shapes" contradicts ADR 0002 (sections 2, 3.3)

Under ADR 0002, unmodeled errors are overlay shapes, so a "hand-owned shape" has no definition.

Recommendation:
- `errors.go` holds only message builders over the generated constructors.
- Forbid `awserr.Identity{...}` composite literals outside `internal/api` and `awserr` with a small analyzer or `forbidigo`.

### m12. Edge routing reads S3 state (section 4.1 rule 8)

"Lambda only when no bucket of that name exists" makes the edge depend on S3 cell state, against "edge sees services only through registry".

Recommendation: `wire.go` injects a narrow `BucketExists(name) bool` function. Document it as the one routing input that comes from state.

### m13. Public packages must not expose internal types (section 2, ADR 0017)

`understudytest`'s "journal accessors" would return `internal/journal` types that external users cannot name.

Recommendation: `understudytest` returns an `admin.Client` bound to the in-process server and uses only `admin` wire types. Running instances in parallel also depends on C3.

### m14. Admin errors are untyped (section 17)

Errors are `{"error": {"code", "message", "hint"}}`, and the client picks the CLI exit code (1, 3 or 4) itself.

Recommendation: a closed `admin.ErrorCode` enum in the public package, mapped to exit codes in one table. The CLI then never matches on strings.

### m15. Config allows contradictory states (section 18)

- `DynamoDB{Enabled, AcceptLicense}` is two booleans, and acceptance is also stored per image digest in the data directory, a third source of truth.
- `Admin.Token` "required when Listen is not loopback" is a rule that spans two fields.

Recommendation: model the valid states as types.
- `DynamoDB: Disabled | AwaitingLicense | Accepted{Digest}`
- `Listen: Loopback{AddrPort} | Exposed{AddrPort, Token}`

The invalid combinations then cannot be constructed.

### m16. Kernel goroutine lifecycle under synctest (sections 2, 19)

The kernel owns the scheduler driver, effect runners, delivery workers and journal streamers. `synctest.Test` fails if any goroutine outlives the bubble.

Recommendation:
- A kernel method `Run(ctx) error` owns every goroutine through `WaitGroup.Go` and returns once all of them have exited.
- A test helper, `kerneltest.New(t, services...)`, runs the kernel inside the bubble and registers cleanup. It also lets a test bind stub handlers for other services' operations, so SNS tests do not need real SQS or Lambda.

### m17. The delivery payload is not typed per target (section 16)

The payload is "envelope bytes plus target-specific attributes", so an SNS-to-Lambda request can carry FIFO group ids.

Recommendation: move the payload into the target variant, for example `SQSQueue{ARN, Body, Attributes, GroupID, DedupID}` and `LambdaFunction{ARN, Qualifier, Event}`.

### m18. The design uses panics the lint config bans (sections 3.2, 4, go-toolchain.md 13)

`Invoke` "panics" on a holding call, while `forbidigo` bans `panic`. Panic recovery at step 9 also misses every goroutine the kernel spawns.

Recommendation:
- Return `Internal` instead of panicking (see M1).
- Keep the recovery at step 9 as the one framework boundary, and add the same recovery to every tracked goroutine: delivery workers, pollers and stream writers.

### m19. Journal size accounting (section 10, ADR 0007)

ADR 0007 says "journal events are fixed-size structs". They are not: events hold slices, interfaces and redacted inputs. The 256 MiB cap only works with a per-event size estimate.

Recommendation: estimate each event's size in `journal.Append`, evict by bytes, and drop the "fixed-size" claim.

### m20. The gates skip the suites that prove the headline features (section 19 Gates)

Neither `-tags docker` nor the Terraform and conformance suites are in the gate list. The Runtime API, the pool and the empty second plan can therefore regress silently between waves.

Recommendation: add `just test-docker` and a Terraform smoke subset to the wave gate. Docker is available in this environment.
