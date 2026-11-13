# ADR 0002: Model overlays as Smithy JSON AST with evidence

Status: accepted, 2026-10-08.

## Context

The vendored models are the authority for wire shapes but not for everything AWS does. SQS has no constraints at all. DynamoDB targets loose shapes. 33 patterns do not compile under RE2. `__type` namespaces are not derivable. The constraint-violation error code is per operation. S3 models 21 of about 80 error codes and its error structures declare no members. KMS statuses disagree with observed behavior (`smithy-models.md 6 to 7`, `error-matrix.md 1`). Vendored models must stay verbatim so their hashes verify against upstream. Something must carry the corrections, and nothing may deviate silently.

## Decision

Corrections live in `codegen/overlays/<svc>.json` and `overlays/common.json` as Smithy JSON AST fragments: `apply` entries adding traits to existing shapes and members, new `structure` shapes for unmodeled errors, and a closed set of traits in the `dev.understudy#` namespace (`evidence`, `errorNamespace`, `jsonType`, `validationError`, `validationPathStyle`, `patternEcho`, `patternValidator`, `alwaysEmit`, `maxRequestBytes`, `throttleError`, `notConfigured`, `malformedRequestError`, `statusOverride`, `unknownOperationError`; the amendment of 2026-10-08 adds five, those of 2026-11-13 `optionalOutput`, `memberOrder` and `requestTooLargeError`). Every overlay entry carries `dev.understudy#evidence` with a label from `aws-recorded`, `third-party-recorded`, `aws-doc`, `sdk-source`, `inferred`, a source and an optional validation date. The generator fails on an unknown `dev.understudy#` trait, on an overlay that references a missing shape, and on a trait that conflicts with the vendored model unless the entry says `"replace": true`. The coverage report lists every overlay entry by label.

## Alternatives considered

- Flat per-service JSON with ad hoc keys (`errorNamespace`, `statusOverrides`, `constraintError`, ...): simpler to read, but every new kind of correction invents a key, unmodeled errors cannot become generated typed shapes, and nothing ties an entry to its evidence.
- Editing the vendored models: breaks hash verification and hides what was changed.
- Hand-written Go tables keyed by shape id: works for S3's error table, but overlays reach the generator, which is where validators, identities and redaction are produced; Go tables would need to be mirrored into generated data by hand.

## Consequences

- Reviewing a fidelity claim is reviewing one JSON entry with its source.
- A model pin bump that fixes a gap surfaces as a failing generate (duplicate trait), so stale overlays are deleted on purpose.
- Behavior that is code, not data (SQS limit texts, S3 router overrides, CloudWatch `InvalidParameterCombination` texts), stays in the service package; the rule is "if the generator needs it, overlay; if a handler needs it, Go".
- Fidelity switches (behavior chosen without recorded evidence) are not overlays; they are the `internal/fidelity` ledger (ADR 0003).

## Amendment 2026-10-08: a wider `validationError` and five more traits

`validationError` widens and the closed trait set grows by five, each with a generator golden test:

- `validationError` applies at member level too, with an optional constraint-kind filter, a reason template and `enumPolicy: closed | open | handler`; `handler` defers a member's checks to the service so a business error (SQS `InvalidAttributeName`, Lambda's creatable runtime subset) is never pre-empted. When a violation set spans two codes the first violation in member order is reported, as AWS reports one.
- `successCode` on an operation (S3 `PutBucketPolicy` 204, Lambda `DeleteFunction` 204).
- `queryKeyAlias` on a member, for colliding Query wire keys (SQS `AttributeName.N`).
- `messageByProtocol` on an error shape (SQS `NonExistentQueue` over Query).
- `emptyCollection: omit | emit` on a service or member (SQS omits, IAM emits `<Roles/>`).
- `internalOperation` on an operation shape the overlay adds: typed, validated, journaled, never claimed by the edge, skipped by the access recorder, listed separately in coverage (`sqs.HoldVisibility`, `logs.PutLogEventsInternal`).

Evidence labels are the one `evidence.Label` type shared with fidelity switches, skip-list entries and parity fixtures. Synthetic raw HTTP operations (function URL data plane, SNS links) are not overlays; they are declared in `codegen/services/<svc>.json` under `rawOperations` (ADR 0003).

## Amendment 2026-11-13: `optionalOutput`

The closed set grows by `optionalOutput`, an annotation on a member the model marks required that AWS leaves out of an answer: SES v1 `GetIdentityNotificationAttributes` sends no `BounceTopic`, `ComplaintTopic` or `DeliveryTopic` for an identity without topics (`testdata/parity/ses/SES-08/03-notif-raw.json`). The member stops being required in the generated code, so its Go field is a pointer, its schema member is not `Required`, and the serializer writes it only when the handler sets it. The trait is protocol-independent: it changes the member, not a codec, so a JSON service uses it as an awsQuery one does. The generator refuses it on a member the model does not require, on one with a modeled default (which the serializer would write in its place), beside `alwaysEmit`, and on a structure that is also input, where it would drop the required check. Like every entry it carries evidence; a generator golden test shows the pointer field and its omitted serialization.

## Amendment 2026-11-13: `memberOrder`

The closed set grows by `memberOrder`, a list of a structure's member names in the order AWS writes them in an answer, where it differs from the model's: SES v1 `GetIdentityNotificationAttributes` lists `HeadersInBounceNotificationsEnabled`, `HeadersInDeliveryNotificationsEnabled`, `HeadersInComplaintNotificationsEnabled` and then `ForwardingEnabled`, where the model lists `ForwardingEnabled` first (`testdata/parity/ses/SES-08/03-notif-raw.json`). Only the generated serializer follows it; the Go type, the schema and the deserializer keep the model's order. The trait is protocol-independent: it orders the serializer's calls, not a codec's output, so every protocol writes the members in that order. The generator refuses an order that does not name every member exactly once, one that repeats the model's order, and the trait on a structure no output uses, where the order reaches no answer. A recording that shows only some members fixes only their relative order; where the others go is then inferred, and the entry's evidence says so. Like every entry it carries evidence; a generator golden test shows the reordered serialization. SES-15 `GetSendQuota`'s order difference stays a listed difference.

## Amendment 2026-11-13: `requestTooLargeError`

The closed set grows by `requestTooLargeError`, a service-level shape reference: the identity the edge answers a request body past its operation's cap with, in Coral's text `Request must be smaller than <cap> bytes for the <operation> operation`. Lambda answers so (`testdata/parity/lambda/LDL-14/03-publish-53-zipped.json`), with the text under `Message`, which the shared `RequestEntityTooLargeException` shape takes from a `messageKey` entry in Lambda's overlay. The 413 shape is shared, and no other service's answer past a cap is recorded, so a service without the trait keeps the generic `RequestEntityTooLargeException` and understudy's own text. The trait is emitted as `registry.Service.RequestTooLargeError`; the shape joins the closure as `throttleError`'s does. Like every entry it carries evidence; a generator golden test shows the descriptor field.
