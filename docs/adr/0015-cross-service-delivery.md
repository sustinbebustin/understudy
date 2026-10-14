# ADR 0015: The delivery engine and SNS HTTP delivery

Status: accepted, 2026-10-08.

## Context

SNS fan-out, S3 notifications, EventBridge targets and Lambda async retries all deliver to SQS, Lambda or SNS with retry policies on the emulator clock and DLQ routing (`sns.md 7.2`, `lambda-events.md`, `secrets-logs-events-alarms.md 4`). SNS also supports `http`/`https`, `email`, `sms`, `application` and `firehose` endpoints. An emulator that makes outbound HTTP requests to arbitrary hosts is an SSRF vector and breaks the rule of no network access at runtime. The phase 1 SNS depth row names SQS and Lambda delivery only.

## Decision

- `internal/delivery` owns `Target` (`SQSQueue`, `LambdaFunction`, `SNSTopic`), `Request{Source, Target, Payload, Policy, DLQ, Cause}` and `RetryPolicy` (`SNSManaged`, `EventBridgeTarget`, `S3Notification`, `None`). A request is a scheduler task at `now`; attempts, backoff and DLQ routing run on the emulator clock with seeded jitter. Each attempt is a `kernel.Invoke` of the target's real operation as a `ServicePrincipal` with `Origin Internal{Via}`, subject to `Delivery`-phase fault rules, emitting a `journal.Delivery` event whose parent is the source event, and counted by the work tracker. Client-side failures (queue or function missing) go straight to the DLQ; filtered messages never reach a DLQ; fan-out order is deterministic.
- Envelope and event builders are pure functions owned by the producing service with golden fixtures and rapid properties.
- One `ruler` engine serves EventBridge patterns, SNS filter policies (behind SNS's allow-list and limits) and ESM filters, compiled at configuration time; the four Ruler-versus-doc conflicts are fidelity switches, default Ruler.
- SNS `http`/`https` subscriptions: `Subscribe` stores the subscription as pending with a confirmation token readable through the admin API, `ConfirmSubscription` works, and outbound delivery runs only to hosts in `sns.httpDelivery.allow` (empty by default, so disabled). `email` and `email-json` stay pending forever with the token readable. `sms`, `application`, `firehose` and `PublishBatch` to phone numbers or endpoint ARNs return an AWS-shaped error that names the protocol as unimplemented in understudy.
- Settling: `GET /_understudy/v1/settle` waits for in-flight deliveries, queued async invokes, running invocations and due timers.

## Alternatives considered

- Per-service delivery code with `tracker.Go` goroutines: fewer abstractions, but every source would reimplement retry schedules, DLQ routing and journal edges, and deterministic replay under `clock advance` needs the scheduler.
- SNS HTTP delivery fully enabled: required by the SNS research for completeness, but it is outbound network from an emulator; the allow-list keeps loopback test receivers working.
- Delivery over HTTP to understudy's own endpoint: rejected by ADR 0004.

## Consequences

- `clock advance` replays a retry storm deterministically; `understudy journal trace` shows every attempt and filter decision.
- Destinations (Lambda async), DynamoDB Streams and future services are new `Target` variants.
- The SNS envelope signature needs one RSA key per account-region, persisted in snapshots, with the certificate served under `/_understudy/`.

## Amendments (review, 2026-10-08)

- `internal/delivery` is a leaf of types and the engine is `internal/outbox`, a `kernel.Stateful` service with an outbox cell of `{id, request, attempt, nextAt, cause, accepted}`. A `Deliver` effect is an outbox insert the kernel performs before the source call returns, so an accepted delivery is state the moment SNS returns a `MessageId`; the insert arms the attempt timer and `Rearm` restores pending attempts after a load. The insert is a separate cell write after the source cell unlocks, never a nested lock.
- Payloads live in the target variant: `SQSQueue{ARN, Body, Attributes, GroupID, DedupID}`, `LambdaFunction{ARN, Qualifier, Event, Invocation}`, `SNSTopic{ARN, Message, Attributes, Subject}`. `Mechanism` is a closed enum.
- A due attempt is tracked work stamped with the due instant (ADR 0006) that invokes the target as `AsService{name}`; ESM polling and Lambda logs run `AsExecutionRole`; forward-access KMS calls run `AsCaller` (ADR 0004). Before an attempt the access recorder checks the target's stored resource policy and journals an `Advisory` when AWS would deny the delivery; the delivery is still attempted.
- SNS outbound HTTP/S delivery and its allow-list are phase 2. Phase 1 keeps `Subscribe` pending with a readable token, `ConfirmSubscription`, and the unsubscribe and certificate links as raw operations.
- Lambda async throttles and 5xx requeue with 1 s to 5 min backoff for up to 6 h.
- The work tracker counts a closed `WorkKind` set (`EffectPending`, `DeliveryAttempt`, `AsyncInvokeQueued`, `InvocationRunning`, `TimerDue`); parked waiters never count; on timeout `settle` names what is pending by kind.
