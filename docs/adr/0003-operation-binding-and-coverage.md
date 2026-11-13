# ADR 0003: Per-operation typed binding, coverage marks and 501 NotImplemented

Status: accepted, 2026-10-08.

## Context

understudy needs build-as-you-go extensibility, unimplemented operations that fail loudly in AWS's own format, and a coverage report against the Smithy models. Three binding designs were considered: a generated `Handlers` interface that is exactly the set listed in `codegen/services/<svc>.json` (compile-time enforcement, but a generator run per operation and a large interface per service), `api.Handle(r, op, fn)` with per-operation `Op[In,Out]` descriptors, and a generated `Binder` with one method per operation taking a handler and a `Coverage` value.

## Decision

- The generator emits types, serde, validators, descriptors and a `Binder` method for every operation in the model. Adding an operation needs no generator run: write `func (s *Service) op(ctx, c *kernel.Call, in *In) (*Out, *awserr.Error)` and call `b.Op(s.op, registry.Full)` in the service constructor. The exception (ADR 0001, amendment 2026-11-02): when `codegen/services/<svc>.json` lists `boundOperations`, also add the operation's name there, keeping the list sorted, and run `just generate`.
- Every bind site states coverage: `registry.Full`, `registry.StoredOnly(note)`, `registry.Partial(note)`. An operation never bound is `registry.Unimplemented`. (The amendment of 2026-10-08 renames these: `registry.BindCoverage` values at the bind site, `Bound` or `Unbound` as the kernel's status.)
- `Handler[In, Out]` returns the concrete `*awserr.Error`, so a handler cannot return an un-AWS-shaped failure. Internal bugs are wrapped with `awserr.Internalf` and render as 500 `InternalFailure` with the cause in the journal and log only.
- Unimplemented operations answer HTTP 501 in the protocol's native envelope with code `NotImplemented` (S3's real code, reused everywhere so one string identifies the condition) and the message `understudy does not implement <Service>.<Operation>. Run 'understudy coverage --service <svc>' for what is supported.` (The amendment of 2026-10-13 extends the message.) They are journaled and access-recorded. 501 is outside the retry sets of Go, botocore and JS, so SDKs fail fast with the message visible (`wire-json-query.md` Implications 15).
- Behaviors chosen without AWS-recorded evidence are `fidelity.Switch[T]` values with a name, provenance, research reference and the record-backlog item that settles them. They are not user-configurable, except those another decision exposes as a setting (`s3.crossRegion`, ADR 0013; `lambda.stateTransitions` and `sqs.queueDeletedRecently`, ADR 0006). (The amendment of 2026-11-13 makes a switch a named choice point that a recording relabels rather than replaces.)
- `understudy coverage` reports protocol compliance, operation coverage and evidence as three separate numbers.

## Alternatives considered

- Generated `Handlers` interface from an `implemented` list: strongest compile-time guarantee, but every added operation is a config edit plus a generator run plus a checked-in diff, and a service interface with dozens of methods is hostile to incremental work. The guarantee this design keeps is the typed signature: a wrong input or output type is a compile error.
- `error` return type constrained by review: rejected; "every failure is AWS-shaped" must be a property of the types.
- Returning 400 `UnknownOperationException` or 500 for unimplemented operations: retried by some SDKs, indistinguishable from a bug, and not a loud failure.

## Consequences

- Compile time and binary size scale with the whole model, not the implemented subset (ADR 0001 records the budgets and the pruning escape hatch).
- The boot-time `registry.New` check catches a binding to a nonexistent operation and a descriptor without a builder.
- Fault rules validate error shapes against the per-operation generated error list, which exists for unbound operations too.
- The exact `NotImplemented` code string is a product choice; AWS documents none.

## Amendment 2026-10-08: per-kernel bindings, bind coverage and operations outside the model

- Generated descriptors are immutable. Bindings live in a per-kernel `kernel.Bindings` table indexed by `Operation.Ordinal`; `Op[In, Out]` stays a static handle and `Invoke` resolves the handler through the kernel. Several kernels coexist in one process without a data race, which parallel tests and `understudytest` require.
- Bind sites state a `registry.BindCoverage` (`Full`, `StoredOnly{Note}`, `Partial{Note}`); the kernel reports a `Status` (`Bound{Coverage}` or `Unbound`). `Unbound` cannot be bound. `kernel.New` fails on a duplicate bind or a bind outside the binder's service; the "operation absent from its model" check was impossible and is gone.
- A bind may carry `kernel.WithValidator(func(*In) []awserr.Violation)`: a stateless, typed validator that runs at pipeline step 7 after the generated validator and before faults, so hand-owned limit texts keep the Coral check order.
- Two kinds of operation exist outside the model: `dev.understudy#internalOperation` (typed, non-routable, ADR 0002) and `rawOperations` in `codegen/services/<svc>.json` (a `registry.Operation` of kind `Raw` with an IAM action and a `BindRawHTTP` binder method, claimed by a prefix or host rule: Lambda `InvokeFunctionUrl`, SNS `ConfirmSubscriptionLink`, `UnsubscribeLink`, `SigningCert`). Both are journaled, faultable and listed separately in coverage.
- `*awserr.Error` does not implement `error`; `Dispatch` maps `(nil, nil)` and `(out, err)` to `Internal`. `awserr.Identity` composite literals are forbidden outside `internal/api` and `internal/awserr`.
- Services expose `Switches() []fidelity.Entry`; `wire.go` collects them for coverage.

## Amendment 2026-10-13: the NotImplemented message says where to ask

- The unimplemented-operation message also names where to request the operation: `understudy does not implement <Service>.<Operation>. Run 'understudy coverage --service <svc>' for what is supported; to ask for it, see https://github.com/sustinbebustin/understudy/issues?q=<Service>.<Operation>`, with no trailing period so terminals link the whole URL. The code (`NotImplemented`) and status (501) are unchanged. Service-specific refusals of an unsupported feature inside an implemented operation keep their own workaround text.

## Amendment 2026-11-13: a switch is a choice point, and a recording relabels it

This amends the Decision bullet that makes "behaviors chosen without AWS-recorded evidence" `fidelity.Switch[T]` values. That bullet implied a recorded answer replaces its switch with fixed behavior; the code instead kept recorded switches labelled `aws-recorded`, and this amendment adopts that.

- A fidelity switch is a named behavior choice point: one behavior understudy implements where more than one was possible, named so coverage lists it. Its `Label` says what backs its `Value`, whether or not a recording settled it.
- A recording that matches the switch's value relabels it `aws-recorded`; the switch stays. It keeps its `Settles` reference (`SettledBy` a replayed step, or `NoScenario` saying why none replays), so a regression replays as `contradicted (<fixture>)` on the coverage page. The coverage page's "Settled by recording, pending retirement" list is the switches whose recording settles them while the code still labels them lower: what retires is the lower label.
- A switch is deleted only when its choice point leaves the code. Deleting a settled switch would weaken the evidence coverage computes for the operations it governs (ARCHITECTURE.md 3.4, 19), and the Ruler and logsfilter switches front per-profile engine options that stay regardless.
- A recorded deviation, where a recording shows AWS doing otherwise and understudy keeps its value on purpose or for now, keeps its switch, or the operation's coverage note, with the recording cited. A switch for a recorded deviation follows one convention:
  - Its `Label` is the evidence for understudy's own value, usually `inferred`, never `aws-recorded`: the recording backs what AWS does instead, and an `aws-recorded` label would make the operations it governs read as recorded where they differ.
  - Where a parity scenario replays the deviation, `Settles` names that step, the baseline records it as `differs`, and the coverage page shows it `contradicted (<fixture>)`. Otherwise `Settles` is `NoScenario` whose reason opens `AWS contradicts it:`, then names the recording item, its date and fixtures, what AWS does, what understudy does and why, and why no scenario replays it. `iam.enforce.order`, `iam.policy.principal-existence` and `logs.authz.outdated-header` follow it.
  - The convention reuses the reason form two recorded deviations already had (`iam.policy.principal-existence`, `logs.authz.outdated-header`) instead of a helper: `authz.RecordedSwitch` labels its switch `aws-recorded`, which a deviation must not be, and the Lambda `differs` helper, which kept the label and wrote `recorded in ..., which differs:`, left the code once every switch it built was recorded. `TestRecordedDeviationsAreNotLabelledRecorded` (`internal/server`) refuses an `aws-recorded` switch whose reason opens `AWS contradicts it:`.
- When understudy changes to do what a recording shows, a recorded deviation becomes a recorded switch at its new value, labelled `aws-recorded`, unless the change removes the choice point.
