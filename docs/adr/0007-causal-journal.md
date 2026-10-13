# ADR 0007: The causal journal

Status: accepted, 2026-10-08.

## Context

The brief wants every API call and every cross-service delivery recorded with causal links, queryable and streamable, as a free equivalent of a paid inspector. Causality must survive asynchronous hops (a message enqueued now, invoked later) and calls made from inside function code, where no SDK forwards a trace header reliably (`lambda-runtime-contract.md`, `landscape.md` Implications 5). Proposals differed on the event model (flat strings vs typed detail), on causes across time, and on the stream encoding (SSE vs NDJSON).

## Decision

- `journal.Event{ID, Cause{Parent, Trace}, Links, At, Wall, Scope, Detail}` with `Detail` a sealed sum: `APICall`, `Delivery`, `Invocation`, `TimerFired`, `StateChange`, `FaultInjected`, `ClockChanged`, `Unclaimed`, `Gap`. Event ids are monotonic per kernel and preserved across snapshot load.
- Causes come from the pipeline, never from handler code: internal calls inherit the parent and trace; state carries causes across time (an SQS message stores its enqueue event; the ESM invocation's parent is the earliest message's event, the rest are `Links`); calls from function code link to the current invocation because each execution environment has its own minted credentials and runs one invocation at a time. `X-Amzn-Trace-Id` is stored when present but never relied on.
- Storage is a bounded in-memory ring (default 200,000 events or 256 MiB) with indexes by trace, resource and operation; with persistence on, events also append to an NDJSON file with rotation. The journal is history, not state: snapshots omit it unless asked.
- Query filters: trace, service, operation, resource ARN glob, kind, outcome, principal, emulator-time or event-id range, limit with cursor. Stream: NDJSON over a chunked HTTP response, resumable with `?after=<id>`; a slow consumer gets a `Gap` event, never backpressure on the emulator.
- Redaction is generated from the Smithy `sensitive` trait (47 shapes) plus overlay additions (SigV4 `Authorization`, `X-Amz-Security-Token`); request and response bodies are stored only with `journal.bodies` and are redacted either way.
- The journal is a test oracle: conformance and e2e tests assert causal chains, and Terraform tests assert call sequences per resource.

## Alternatives considered

- Flat string events (`Service, Operation string`, `Summary map[string]string`): easier to serialize, but outcomes and delivery results become string conventions that tests cannot pattern-match safely.
- Explicit tracing calls in handlers: handler authors forget them; the pipeline and the state-carried cause rule cover every hop without cooperation.
- Server-Sent Events: browser-friendly and resumable, but needs an SSE parser on every consumer; NDJSON works with `curl -N`, is the file format, and an SSE variant is one `Accept` branch away for the phase 2 web UI.
- Persisting the journal in snapshots by default: it is history; a snapshot that must reproduce a state should not grow with traffic.

## Consequences

- "Why didn't my Lambda run?" is `understudy journal trace <id>` or `understudy journal trace --request-id <id>`.
- Events hold slices and redacted inputs, so `Append` estimates each event's size and the ring evicts by bytes and by count; a benchmark gates per-call overhead.
- The function-credential linking rule means every environment mints its own session credentials (ADR 0010, ADR 0012).

## Amendments (review, 2026-10-08)

- `journal` owns its record types and imports only `ref`, `aws`, `clock`, `ids` and `delivery`; `EventID` and `Cause` live in the leaf `ref` package and `APICall.Origin` is a `ref.Origin` (engineering C1). The principal is stored rendered, so `journal` never imports `identity`.
- The default ring is 100,000 events or 128 MiB, sized to the RSS budget (product M6); eviction uses the per-event estimate (engineering m19).
- `Detail` gains `Advisory` (a stored resource policy would deny a delivery; unminted credentials from a function container; a code update shadowed by a hot-reload overlay). `StateChange` carries closed `ResourceState` values, `Delivery` a `delivery.Mechanism`, `FaultInjected` a `ref.RuleID` (engineering m3).
- Queries filter by request id and traces are reachable by request id, since that is the key users hold (product m3). On Linux bridge the request's `RemoteAddr` is a second linking signal for in-function calls; init-phase calls link to the cold-start `Invocation` event (fidelity F18).
- Golden fixtures compare canonical trace trees (children sorted by kind, operation and resource; ids replaced by tree positions; `Wall` and `Latency` ignored), never raw event ids (ADR 0018).
- The kernel is the only writer of `APICall` events; the edge writes `Unclaimed` only (engineering M8).

## Amendments (wave 3, 2026-10-10)

- The NDJSON file is written by a background writer, never under the journal's lock, so a slow disk never backpressures the emulator. Its queue holds at most `MaxBytes` estimated bytes; when the disk falls that far behind, the file stops at the last event queued before the overflow and `FileErr` reports a `*journal.QueueFullError`. Every file failure stops the file, so it is always a prefix of recording order. `Close` waits for the queue to drain and has no deadline of its own.

## Amendments (pre-tag review, 2026-10-13)

Decided at the pre-tag review.

- `Detail` gains `Redrive{From, To, Messages, Reason}` (wire kind `redrive`), `Reason` a sealed `MaxReceiveCount{Count}` | `MoveTask{TaskHandle}`, recorded with `Tx.Record` from the SQS receive that exceeds `maxReceiveCount` and from each move-task step, so its parent is that receive or task event; it is the "journal edge source-receive to DLQ-send" of ARCHITECTURE.md 16. `delivery.Mechanism` stays the closed set of outbox paths; an in-service move is never a `Delivery`. Implementation is deferred to the first journal round after the tag (`docs/plan/DEFERRED.md`).
- In-function calls, init phase included, are children of the `Invoke` call (the `Invocation` event's own parent), not of the `Invocation` event, which is written when the invocation ends; calls between invocations root their own trace. This supersedes the review amendment's "init-phase calls link to the cold-start `Invocation` event"; ARCHITECTURE.md 10 and 14.7 are amended to the implemented behavior.
- `Detail` gains `AdminAction{Op: Reset | SnapshotSave | SnapshotLoad; Scope}` (wire kind `admin-action`), written by the admin handler like `ClockChanged`, one per action. It replaces the per-service DynamoDB summary event of ADR 0014; deferred to the post-tag journal round with `Redrive`.
