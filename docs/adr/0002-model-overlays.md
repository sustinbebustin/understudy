# ADR 0002: Model overlays as Smithy JSON AST with evidence

Status: accepted, 2026-10-08.

## Context

The vendored models are the authority for wire shapes but not for everything AWS does: SQS has no constraints at all, DynamoDB targets loose shapes, 33 patterns do not compile under RE2, `__type` namespaces are not derivable, the constraint-violation error code is per operation, S3 models 21 of about 80 error codes and its error structures declare no members, KMS statuses disagree with observed behavior (`smithy-models.md 6 to 7`, `error-matrix.md 1`). Vendored models must stay verbatim so their hashes verify against upstream. Something must carry the corrections, and nothing may deviate silently.

## Decision

Corrections live in `codegen/overlays/<svc>.json` and `overlays/common.json` as Smithy JSON AST fragments: `apply` entries adding traits to existing shapes and members, new `structure` shapes for unmodeled errors, and a closed set of traits in the `dev.understudy#` namespace (`evidence`, `errorNamespace`, `jsonType`, `validationError`, `validationPathStyle`, `patternEcho`, `patternValidator`, `alwaysEmit`, `maxRequestBytes`, `throttleError`, `notConfigured`, `malformedRequestError`, `statusOverride`, `unknownOperationError`). Every overlay entry carries `dev.understudy#evidence` with a label from `aws-recorded`, `third-party-recorded`, `aws-doc`, `sdk-source`, `inferred`, a source and an optional validation date. The generator fails on an unknown `dev.understudy#` trait, on an overlay that references a missing shape, and on a trait that conflicts with the vendored model unless the entry says `"replace": true`. The coverage report lists every overlay entry by label.

## Alternatives considered

- Flat per-service JSON with ad hoc keys (`errorNamespace`, `statusOverrides`, `constraintError`, ...): simpler to read, but every new kind of correction invents a key, unmodeled errors cannot become generated typed shapes, and nothing ties an entry to its evidence.
- Editing the vendored models: breaks hash verification and hides what was changed.
- Hand-written Go tables keyed by shape id: works for S3's error table, but overlays reach the generator, which is where validators, identities and redaction are produced; Go tables would need to be mirrored into generated data by hand.

## Consequences

- Reviewing a fidelity claim is reviewing one JSON entry with its source.
- A model pin bump that fixes a gap surfaces as a failing generate (duplicate trait), so stale overlays are deleted on purpose.
- Behavior that is code, not data (SQS limit texts, S3 router overrides, CloudWatch `InvalidParameterCombination` texts), stays in the service package; the rule is "if the generator needs it, overlay; if a handler needs it, Go".
- Fidelity switches (behavior chosen without recorded evidence) are not overlays; they are the `internal/fidelity` ledger (ADR 0003).

## Amendments (review, 2026-10-08)

The closed trait set grows by five, each with a generator golden test:

- `validationError` applies at member level too, with an optional constraint-kind filter, a reason template and `enumPolicy: closed | open | handler`; `handler` defers a member's checks to the service so a business error (SQS `InvalidAttributeName`, Lambda's creatable runtime subset) is never pre-empted. When a violation set spans two codes the first violation in member order is reported, as AWS reports one.
- `successCode` on an operation (S3 `PutBucketPolicy` 204, Lambda `DeleteFunction` 204).
- `queryKeyAlias` on a member, for colliding Query wire keys (SQS `AttributeName.N`).
- `messageByProtocol` on an error shape (SQS `NonExistentQueue` over Query).
- `emptyCollection: omit | emit` on a service or member (SQS omits, IAM emits `<Roles/>`).
- `internalOperation` on an operation shape the overlay adds: typed, validated, journaled, never claimed by the edge, skipped by the access recorder, listed separately in coverage (`sqs.HoldVisibility`, `logs.PutLogEventsInternal`).

Evidence labels are the one `evidence.Label` type shared with fidelity switches, skip-list entries and parity fixtures. Synthetic raw HTTP operations (function URL data plane, SNS links) are not overlays; they are declared in `codegen/services/<svc>.json` under `rawOperations` (ADR 0003).
