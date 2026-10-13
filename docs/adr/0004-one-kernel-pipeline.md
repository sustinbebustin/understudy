# ADR 0004: One kernel pipeline for external and internal calls

Status: accepted, 2026-10-08.

## Context

The brief's platform features (journal with causal links, fault injection, access recording, clock) must cover cross-service traffic: SNS to SQS, ESM polling, S3 notifications, EventBridge targets, Lambda logs. LocalStack's ESM poller calls SQS through its own HTTP gateway with private override headers and pins a gateway thread per long poll (`landscape.md 7`, Implications 6). moto fires ESM synchronously inside `SendMessage`. All three proposals converged on an in-process invoker through the same stages.

## Decision

`kernel.Dispatch` is the only path to a handler. External requests enter it after the edge's admit, auth, identity, claim and decode stages; internal calls enter through `kernel.Invoke(ctx, parent, op, target, in)`, which builds a `Call` with `Origin Internal{Via}`, the target scope and principal, and `Cause{Parent: parent.Event, Trace: parent.Cause.Trace}`. Both run journal begin, validation, request-phase faults, the handler, after-phase faults, access recording and journal end. Internal calls skip only the wire codec and signature parsing. Services never import each other; they import the other service's generated `<svc>api` package for `Op` handles and input types. Validation runs for internal calls too. Order inside the pipeline: validate before faults, so a request AWS would reject for shape reasons gets that error and only a valid request can be faulted.

## Alternatives considered

- HTTP loopback for internal calls: fully uniform wire behavior, but needs private headers to exceed SQS's 10-message and 20-second limits, blocks a server goroutine per poll, and adds codec cost to every delivery. Rejected (`landscape.md` Implications 6).
- Direct Go interface calls between services: no journal, no faults, no access record, and wiring interfaces or import cycles between services. Rejected.
- Faults before validation: both orderings have no side effects; validate-first matches the observed Coral check order (unknown operation, auth, deserialization, validation, business rules; `error-matrix.md 4`).

## Consequences

- Every delivery and poll is a journal edge and a fault target for free (`{Service: sqs, Operation: SendMessage, Origin: InternalOnly}` drives the SNS retry and DLQ path).
- The ESM poller receives through the real `ReceiveMessage` handler with the 10-message ceiling, looping to fill a batch; batching windows are literal on the emulator clock.
- A handler that needs another service synchronously (S3 to KMS) calls `Invoke` before taking its cell lock (ADR 0005).
- The DynamoDB proxy needs raw request bytes; `RawHandler` is the one passthrough affordance and still runs every stage (ADR 0014).

## Amendments (review, 2026-10-08)

- The kernel has one entry point (engineering M8): the edge hands `Dispatch` a `Decoded` value (`Ok{In}` or `Rejected{*awserr.Error}`); the binding lookup, the 501 decision and the rejected-decode outcome are journaled and access-recorded inside the kernel. `Unclaimed` stays the one journal event the edge writes.
- `Target.As` is a `PrincipalMode` (fidelity F12, product C3): `AsCaller` for forward-access calls AWS charges to the original principal (S3, Secrets Manager and SQS to KMS), `AsExecutionRole` for ESM polling and Lambda logs, `AsService{name}` for resource-policy-gated deliveries. The access recorder attributes each call accordingly and journals an `Advisory` when a stored resource policy would not allow a delivery.
- `Target.At` carries the admission instant of deferred work (engineering C2): a delivery or poll started by a timer is stamped with the timer's due instant, never with `Now()`.
- `Invoke` returns `Internal` (never panics) when the parent call holds a cell lock (engineering M1, m18).
- `RawHandler` receives `Wire = FromClient{RawRequest} | Synthesized`; `Call` no longer carries a raw request, and the DynamoDB service encodes `Synthesized` inputs itself so the kernel never imports a codec (engineering m9).
- Cross-scope references resolve through `kernel.ResolveScope(c, ref)` with per-service mismatch identities (fidelity F8).

## Amendments (pre-tag review, 2026-10-13)

Decided at the pre-tag review: access recording.

- `kernel.WithActions[In](func(*Call, *In) []registry.IAMAction)` declares, per input, the secondary IAM actions a call used, evaluated like `WithResources`. The kernel records the primary action (the first in the operation's generated IAM list) always, the declared ones (refusing, and logging as a bug, any outside the generated list) and, for other service prefixes, only actions paired with a touched resource of that service, never on `*` (`iam:PassRole` lands on the passed role, which the handler touches).
- S3 object operations touch object ARNs (`arn:aws:s3:::<bucket>/<key>`); the generated policy renders object-level resources as `arn:aws:s3:::<bucket>/*`.
- Internal operations record only declared actions (`logs.PutLogEventsInternal` declares `logs:PutLogEvents`, so the execution role's policy carries it).
