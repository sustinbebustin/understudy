# ADR 0003: Per-operation typed binding, coverage marks and 501 NotImplemented

Status: accepted, 2026-10-08.

## Context

The brief wants build-as-you-go extensibility, unimplemented operations that fail loudly in AWS's own format, and a coverage report against the Smithy models. Three binding designs were proposed: a generated `Handlers` interface that is exactly the set listed in `codegen/services/<svc>.json` (compile-time enforcement, but a generator run per operation and a large interface per service), `api.Handle(r, op, fn)` with per-operation `Op[In,Out]` descriptors (simplicity), and a generated `Binder` with one method per operation taking a handler and a `Coverage` value (platform-dx).

## Decision

- The generator emits types, serde, validators, descriptors and a `Binder` method for every operation in the model. Adding an operation needs no generator run: write `func (s *Service) op(ctx, c *kernel.Call, in *In) (*Out, *awserr.Error)` and call `b.Op(s.op, registry.Full)` in the service constructor.
- Every bind site states coverage: `registry.Full`, `registry.StoredOnly(note)`, `registry.Partial(note)`. An operation never bound is `registry.Unimplemented`.
- `Handler[In, Out]` returns the concrete `*awserr.Error`, so a handler cannot return an un-AWS-shaped failure. Internal bugs are wrapped with `awserr.Internalf` and render as 500 `InternalFailure` with the cause in the journal and log only.
- Unimplemented operations answer HTTP 501 in the protocol's native envelope with code `NotImplemented` (S3's real code, reused everywhere so one string identifies the condition) and the message `understudy does not implement <Service>.<Operation>. Run 'understudy coverage --service <svc>' for what is supported.` They are journaled and access-recorded. 501 is outside the retry sets of Go, botocore and JS, so SDKs fail fast with the message visible (`wire-json-query.md` Implications 15).
- Behaviors chosen without AWS-recorded evidence are `fidelity.Switch[T]` values with a name, provenance, research reference and the record-backlog item that settles them; they are not user-configurable in phase 1.
- `understudy coverage` reports protocol compliance, operation coverage and evidence as three separate numbers.

## Alternatives considered

- Generated `Handlers` interface from an `implemented` list: strongest compile-time guarantee, but every added operation is a config edit plus a generator run plus a checked-in diff, and a service interface with dozens of methods is hostile to incremental work. The guarantee this design keeps is the typed signature: a wrong input or output type is a compile error.
- `error` return type constrained by review: rejected; "every failure is AWS-shaped" must be a property of the types.
- Returning 400 `UnknownOperationException` or 500 for unimplemented operations: retried by some SDKs, indistinguishable from a bug, and not what the brief asks for.

## Consequences

- Compile time and binary size scale with the whole model, not the implemented subset (ADR 0001 records the measurement and the pruning escape hatch).
- The boot-time `registry.New` check catches a binding to a nonexistent operation and a descriptor without a builder.
- Fault rules validate error shapes against the per-operation generated error list, which exists for unbound operations too.
- The exact `NotImplemented` code string is a product choice; AWS documents none.

## Amendments (review, 2026-10-08)

- Generated descriptors are immutable (engineering C3). Bindings live in a per-kernel `kernel.Bindings` table indexed by `Operation.Ordinal`; `Op[In, Out]` stays a static handle and `Invoke` resolves the handler through the kernel. Several kernels coexist in one process without a data race, which parallel tests and `understudytest` require.
- Bind sites state a `registry.BindCoverage` (`Full`, `StoredOnly{Note}`, `Partial{Note}`); the kernel reports a `Status` (`Bound{Coverage}` or `Unbound`). `Unbound` cannot be bound. `kernel.New` fails on a duplicate bind or a bind outside the binder's service; the "operation absent from its model" check was impossible and is gone (engineering m2).
- A bind may carry `kernel.WithValidator(func(*In) []awserr.Violation)`: a stateless, typed validator that runs at pipeline step 7 after the generated validator and before faults, so hand-owned limit texts keep the Coral check order (engineering M7).
- Two kinds of operation exist outside the model (engineering M6, fidelity F13): `dev.understudy#internalOperation` (typed, non-routable, ADR 0002) and `rawOperations` in `codegen/services/<svc>.json` (a `registry.Operation` of kind `Raw` with an IAM action and a `BindRawHTTP` binder method, claimed by a prefix or host rule: Lambda `InvokeFunctionUrl`, SNS `ConfirmSubscriptionLink`, `UnsubscribeLink`, `SigningCert`). Both are journaled, faultable and listed separately in coverage.
- `*awserr.Error` does not implement `error`; `Dispatch` maps `(nil, nil)` and `(out, err)` to `Internal` (engineering m1). `awserr.Identity` composite literals are forbidden outside `internal/api` and `internal/awserr` (engineering m11).
- Services expose `Switches() []fidelity.Entry`; `wire.go` collects them for coverage (engineering m5).
