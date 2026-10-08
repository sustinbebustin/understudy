# ADR 0008: Fault injection rules

Status: accepted, 2026-10-08.

## Context

The brief asks for declarative rules by service, operation, resource, account or region that inject AWS-shaped errors, throttling or latency, with probability or next-N budgets and a seed for determinism. Research shows that a throttle is retried by SDKs (Terraform's up to 25 attempts, `tf-compute.md 14`), that each service has its own throttling identity (`sqs.md 13.18`, `error-matrix.md 8.4`), that S3 fails some operations after a 200 status (`wire-rest.md 3.7`), and that DynamoDB Local never raises `TransactionConflictException` (`dynamodb-local.md 4.1`).

## Decision

`fault.Rule{ID, Match, Phase, Action, Trigger}`:

- `Match`: services, operations (or delivery mechanisms in the `Delivery` phase), resource ARN glob over the ARNs the call touches, accounts, regions, origin (`Any | ExternalOnly | InternalOnly`).
- `Phase`: `Request` (pipeline steps 8 and 10 for every call, internal included; the DynamoDB proxy before forwarding) or `Delivery` (the delivery engine per attempt).
- `Action`: `Error{Shape, Message}`, `Throttle{}` (resolves to the service's identity from the overlay `throttleError`), `Latency{Wall}` (wall, so a frozen clock cannot hang it), `AfterCommit{Shape}` (the side effect happens, the client sees the error), `StatusOKError{Shape}` (S3 200-with-error), `DropDelivery{}`.
- `Trigger`: `Always`, `NextN{N}` (every SDK retry counts as a hit), `Probability{P}` with a ChaCha8 stream keyed by `hash(global seed, rule id)`.
- Rules are validated at creation: an error shape not in the operation's generated error list or the generic catalogue is rejected with the list of codes the service really sends. The CLI warns when a rule targets a retryable code.
- Request-phase faults run after validation and before the handler, so an injected error has no side effects unless the action is `AfterCommit`.
- Every hit is a `FaultInjected{Rule, Hit, Of}` journal event. Rules come from the admin API, the CLI or the config file.

## Alternatives considered

- Faults before validation: no observable difference in side effects; validate-first matches the observed check order and keeps "AWS rejects this shape" authoritative.
- Free-text error codes: a rule could inject an error AWS never sends; validation at creation keeps the test honest.
- Latency on the emulator clock: a frozen clock would hang the request forever.

## Consequences

- Determinism holds for a given call order; concurrent clients make order nondeterministic, and the journal shows every hit either way (documented, not hidden).
- Internal deliveries are faultable without special code paths, which exercises SNS retries, DLQs and ESM partial failures.
- Rule state (remaining budgets, stream positions) is part of snapshots.

## Amendments (review, 2026-10-08)

- `Match.Operations` is `[]ref.OperationID` and delivery mechanisms are a closed `delivery.Mechanism` enum (engineering m3). Rule validation reads a `fault.Catalogue` interface the kernel implements over the registry, so `fault` is a leaf that imports no descriptors (engineering C1).
- Typed per-operation validators (`kernel.WithValidator`) run before faults, so the validate-first argument holds for hand-owned checks too (engineering M7).
- Calls with `Origin Internal{Via: Snapshot}` (the DynamoDB export and import) never match a rule (product M3).
- The CLI exposes every match dimension and action: `--resource`, `--account`, `--region`, `--origin`, `--phase delivery`, `--after-commit`, `--status-ok`, `--drop` (product m4).
