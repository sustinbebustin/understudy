# Fidelity review of the phase 1 architecture

Reviewer lens: will the architecture reproduce the AWS behavior the research documents, across Go v2, boto3, JS v3, the AWS CLI and Terraform. Covers protocol edges, checksums and `aws-chunked`, S3 addressing, SQS/SNS and Lambda/ESM semantics, error-shape fidelity, time semantics, account and region isolation, and what the generator cannot express. Inputs: [ARCHITECTURE.md](../ARCHITECTURE.md), ADRs 0001 to 0018, and the research in [../../research/phase1/](../../research/phase1/).

Severity:
- **critical**: a phase 1 behavior the brief names cannot be built on the contract as written.
- **major**: recorded AWS behavior will come out wrong, or a gate will hang or flap, unless decided before the owning wave.
- **minor**: a narrow fidelity gap, cheap to close now.

## Verdict

The protocol layer is the strongest part of the design. Overlays carry evidence, error identities are typed, the Smithy corpus serves as the proof, and the CBOR, awsQueryCompatible and S3 routing details are taken straight from the research. Fidelity is weaker in four places:
- the handler and serde contract has no streaming body, which S3 needs;
- the scheduler and Frozen-mode semantics break the causal chain and Terraform waiters;
- error identity is configured per operation, but AWS varies it per member;
- the architecture never says how a call that names a resource in another account or region is resolved.

## Critical

### F1. S3 bodies cannot stream, so `Expect: 100-continue`, trailers and 5 GiB uploads do not fit the contract (sections 3.2, 4, 6.1, 5.5; ADR 0001)

**Problem.** The contract assumes a fully decoded, fully buffered body:
- Every handler is `Handler[In, Out]` over a fully decoded `*In`.
- The edge decodes the body at step 5, before validation and before the handler.
- `Serializer.WriteBlob` takes `[]byte`.

The research requires the opposite for S3:
- Header-level checks (bucket exists, preconditions, SSE-KMS key state, `x-amz-sdk-checksum-algorithm` consistency) run before the first body read. Go's `net/http` sends `100 Continue` on that first read, and botocore sends `Expect: 100-continue` on every PUT ([sigv4-checksums-endpoints.md](../../research/phase1/sigv4-checksums-endpoints.md) 3.4 and Implication 6, [s3.md](../../research/phase1/s3.md) 6.3, [wire-rest.md](../../research/phase1/wire-rest.md) 3.4).
- PutObject and UploadPart carry up to 5 GiB, and GetObject, Range and `partNumber` responses must stream.
- With `aws-chunked`, the `x-amz-checksum-*` value arrives as a trailer after the body. The model binds it as an `httpHeader`, so `restbind` cannot fill it.
- `BadDigest`, `IncompleteBody` and `XAmzContentSHA256Mismatch` are only knowable at EOF.

As written, the step 7 body cap becomes a multi-gigabyte in-memory read. A `NoSuchBucket` or `412` cannot be returned before the body is consumed. A trailer checksum has nowhere to land.

**Recommendation.**
- Add a streaming payload kind to `smithy.Schema` and the generated types for `@streaming` members (S3 `StreamingBlob`): an input reader and an output body source with a size and a range opener.
- Have `restbind` expose trailer-bound members as a deferred value that the `aws-chunked` reader resolves at EOF.
- Split decoding into a head part (labels, query, headers; validated at step 7) and a body the handler pulls after its pre-checks.
- Enforce caps for streaming members with a counting reader (`EntityTooLarge`) instead of buffering.
- Add tests:
  - an early reject without any body read;
  - a 5 GiB PUT with bounded RSS;
  - a trailer mismatch that returns `BadDigest`;
  - a ranged GET that streams from `blob`.

### F2. Scheduler callbacks perform blocking calls (sections 9, 14.8, 16)

**Problem.** Three statements combine badly:
- Timers fire on one scheduler goroutine.
- The ESM poller "is a scheduler task ... that calls SQS through `kernel.Invoke`".
- Each delivery attempt "calls the target through `kernel.Invoke`" from a scheduler task.

As written, a synchronous ESM `Invoke` (seconds to minutes of container time) or a `ReceiveMessage` long poll runs on the only goroutine that fires visibility expiries, schedules and retries. The consequences:
- A long poll waits on `Clock.Until` for a deadline that only the blocked scheduler can fire. In Running mode it ends only through `WallCap`; it is a deadlock in all but name.
- A stepwise `Advance` would block on container time while emulator time is pinned at the callback's `due`.
- Unrelated SQS visibility timers fire late, so messages stay invisible longer than AWS would keep them.

**Recommendation.**
- State in the contract that scheduler callbacks are non-blocking. They may only commit through `store.Write` and start tracked work.
- Deliveries, ESM cycles and async invokes run on work-tracker goroutines that carry the fire instant as their timestamp, while deadlines read the live clock.
- `Advance` fires callbacks in order and does not wait for spawned work, except under `--settle`.
- Assert in `lockcheck` builds that the scheduler goroutine never enters `kernel.Invoke`.
- Add a synctest case: an ESM poller plus a long poll plus an `Advance` must not deadlock.

## Major

### F3. Frozen mode stalls due-now tasks and Terraform-visible control-plane transitions (sections 9, 14.7, 14.8, 19; ADR 0006, 0017)

**Problem.**
1. The driver "sleeps ... not at all in Frozen". The text does not say that tasks with `due <= Now()` still fire. If they do not, a large part of the phase 1 workload never runs on the frozen clock that the e2e test and `understudytest` use:
   - SNS fan-out, S3 notifications and EventBridge `PutEvents` deliveries;
   - the first Lambda async attempt;
   - window-0 ESM polls.
2. Control-plane transitions that client waiters poll run on the emulator clock: Lambda `Pending -> Active` and `LastUpdateStatus`, ESM `Creating -> Enabled`, and ESM `Deleting -> 404`. Section 14.7 says that freezing the clock freezes the transition. Terraform's function and ESM waiters (10 minute budgets, [lambda-events.md](../../research/phase1/lambda-events.md) 1.5, [lambda-api.md](../../research/phase1/lambda-api.md) finding 10) then never converge. `terraform apply` of the workload onto a frozen clock fails.
3. An SQS `QueueDeletedRecently` tombstone on the emulator clock blocks destroy-then-apply of the same queue name forever. [tf-messaging.md](../../research/phase1/tf-messaging.md) 4.2 row 17 and Implication 19 advise against enforcing it by default.

**Recommendation.**
- Specify that in every mode, tasks with `due <= Now()` fire immediately. Frozen only stops `Now()` from moving.
- In the domain table, split timers into two classes:
  - **AWS semantics**: visibility, delay, retention, schedules, retries, deletion windows, TTL. These follow the emulator clock.
  - **Provisioning settle**: function state, `LastUpdateStatus`, ESM transients, `Deleting -> gone`, name tombstones. These resolve on the next read after a short monotonic minimum, whatever the clock mode.
- Make "hold in Pending" an explicit fidelity switch or fault for waiter tests.
- Add an e2e gate that runs `terraform apply` against a frozen clock.

### F4. Accepted deliveries are not state, so snapshots and restarts drop them (principle 5; sections 3.5, 13, 16; ADR 0009, 0015)

**Problem.** A `Deliver{Request}` effect becomes a scheduler task. The following live only in the scheduler:
- attempt number, next-attempt time and DLQ routing;
- SNS's 23-day managed policy and EventBridge's 24 h policy;
- the Lambda async queue (60 s and 120 s retries, 6 h maximum age).

This contradicts principle 5 ("nothing lives only in the scheduler"). Section 13 admits the loss: save "waits up to 5 s ... deliveries still in flight are named in the response as not captured". In AWS, a returned SNS `MessageId` means the message is saved ([sns.md](../../research/phase1/sns.md) 4.1), and a 202 from `Invoke` with `Event` means the event is accepted. After a snapshot load, `Rearm` has nothing to rebuild them from.

**Recommendation.**
- Make `delivery` a `Stateful` service with an outbox cell of `{id, request, policy, attempt, nextAt, cause}`.
- Insert the outbox entry in the same commit as the source mutation (the effect becomes an insert plus an `Arm`), and remove it on a terminal outcome.
- Keep Lambda async queues in the Lambda cell.
- `Rearm` restores pending attempts. Drop the "not captured" clause.

### F5. Validation error identity is per operation, but AWS varies it per member and constraint (sections 3.3, 5.2, 5.5)

**Problem.** `awserr.Validate(op, v)` and the `validationError` trait (on a service or an operation) choose one code and one text style per operation. Recorded behavior varies inside a single operation:

| Service and operation | Recorded behavior | Source |
|---|---|---|
| Lambda `CreateFunction` | `ValidationException` for most members, `InvalidParameterValueException` for the `runtime` enum and a 64-character `functionName` | [error-matrix.md](../../research/phase1/error-matrix.md) finding 3, [lambda-api.md](../../research/phase1/lambda-api.md) 2.1 |
| Lambda ESM | enum violation gives `ValidationException`; a missing `StartingPosition` gives `InvalidParameterValueException` with a trailing period | [lambda-events.md](../../research/phase1/lambda-events.md) 1.3 |
| SQS | the reason text differs per parameter ("Must be between 1 and 10, if provided." against "Must be >= 0 and <= 20, if provided."); one violation is reported; an unknown `QueueAttributeName` is the business error `InvalidAttributeName`, not an enum failure | [sqs.md](../../research/phase1/sqs.md) 2.4 and 4 |
| SNS | a missing required member gives Coral `ValidationError`; other checks give `Invalid parameter: ...` text | [sns.md](../../research/phase1/sns.md) 1 |
| Lambda `runtime` | only the creatable subset of the model enum is accepted | [error-matrix.md](../../research/phase1/error-matrix.md) section 4 |

With one code per operation, these recorded cases get the wrong identity. The alternative is a hand check that runs after generated validation has already answered with the wrong code.

**Recommendation.**
- Key the overlay by `(operation, member path, constraint kind)`, falling back to the operation, then the service.
- Add `enumPolicy: closed | open | handler` and per-member reason templates for the hand-text styles.
- Define how a violation set that spans two codes is reported.
- Let `handler` defer a member's checks to the service code, so step 7 cannot pre-empt the business error.
- Turn each recorded row into a golden test.

### F6. The closed overlay trait set cannot express several researched corrections (section 5.2; principles 2 and 6)

**Problem.** The generator fails on any unknown `dev.understudy#` trait, but these recorded facts need generator support and have no trait:
- **Success status.**
  - S3 `PutBucketPolicy`, `PutBucketTagging` and the metrics-family `Put*` operations return 204 where the model says 200 ([s3.md](../../research/phase1/s3.md) 14.1).
  - Lambda `DeleteFunction` returns 204 and the model has no code ([lambda-api.md](../../research/phase1/lambda-api.md) 2).
- **Duplicate Query wire keys.** SQS `ReceiveMessage.AttributeNames` and `MessageSystemAttributeNames` both serialize as `AttributeName.N` ([sqs.md](../../research/phase1/sqs.md) 2.3 and Implication 2). The generated form decoder for that operation is ambiguous.
- **Messages that differ by protocol.** Over Query, SQS `NonExistentQueue` says "... does not exist for this wsdl version." ([sqs.md](../../research/phase1/sqs.md) Implication 4). `awserr.Error` carries one `Message`.
- **Empty-collection emission.**
  - AWS omits empty SQS collections even when the model marks them required: batch `Successful`/`Failed`, `{}` for an empty receive, no `Tags` key ([sqs.md](../../research/phase1/sqs.md) 2.1).
  - IAM emits `<Roles/>` ([wire-json-query.md](../../research/phase1/wire-json-query.md) 8.5).
  - The rule "required means a plain value" would emit `[]` for SQS.

**Recommendation.** Add the following to the closed set, each with evidence and a generator golden test:
- `successCode` on an operation;
- a query-key resolution trait for colliding members;
- a per-protocol message on an error identity (or an `awserr.Error` message map keyed by protocol);
- `emptyCollection: omit | emit` on a service or member.

### F7. Requests signed for other services fall through to S3 (section 4.1, steps 4 and 8)

**Problem.** Step 4 makes the credential-scope service the router key. Step 8 then sends "anything else" to S3 path-style. A request signed for `sqs`, `dynamodb` or `sns` that steps 5 to 7 do not claim gets an S3 `NoSuchBucket` XML body. Real AWS answers from the signed service:

| Request | AWS answer | Source |
|---|---|---|
| GET on an SQS queue URL with no `Action` | 404 `UnknownOperationException` (XML) | [sqs.md](../../research/phase1/sqs.md) 2.3 |
| DynamoDB request with no `X-Amz-Target` | 400 `UnknownOperationException` | [error-matrix.md](../../research/phase1/error-matrix.md) finding 6 |
| DynamoDB request with a wrong Content-Type | 404 `<UnknownOperationException/>` | [error-matrix.md](../../research/phase1/error-matrix.md) H7 |

**Recommendation.**
- Allow the S3 fallthrough only for requests that are unsigned, SigV2, or scoped to `s3`.
- For any other service, answer with that service's `unknownOperationError` or `malformedRequestError` identity and journal an `Unclaimed` event.
- Add a wire golden for each row of the table.

### F8. Calls that name resources in another account or region have no resolution rule (sections 3.1, 3.2, 7, 8)

**Problem.** Cells are keyed by the caller's scope, and the reference handler resolves resources through `s.cells.For(c.Scope)`. Phase 1 inputs routinely name resources in another scope, and each service answers differently:

| Case | AWS answer | Source |
|---|---|---|
| SQS `QueueUrl` path carries another account | cross-account lookup by the account in the path | [sqs.md](../../research/phase1/sqs.md) Implication 6 |
| SNS `TopicArn` in another region | `Invalid parameter: TopicArn` | [sns.md](../../research/phase1/sns.md) 1 (recorded) |
| Lambda ARN in another region | `ResourceNotFoundException`: "Functions from 'x' are not reachable in this region" | [lambda-api.md](../../research/phase1/lambda-api.md) 3 |
| Lambda ARN in another account | 403 `AccessDeniedException` | [lambda-api.md](../../research/phase1/lambda-api.md) 2.1 |
| S3 SSE-KMS key ARN in another region | `KMS.NotFoundException`: "Invalid arn <region>" | [s3.md](../../research/phase1/s3.md) 5.7 |
| DynamoDB table ARN in another account or region | Local ignores the account and region in the ARN, so the proxy would serve the caller's own table | [dynamodb-local.md](../../research/phase1/dynamodb-local.md) 4.2 |

Without a rule, each handler will look in the caller's cell and return a generic not-found error.

**Recommendation.**
- Add a kernel-level `ResourceRef` resolver for ARNs, URLs and names. It applies a generated per-member policy: `SameScope{mismatch identity and text}`, `CrossAccount`, or `Global`.
- The handler receives the resolved scope and cell.
- The DynamoDB proxy applies the same resolver before forwarding.
- Test each service with the forms name, same-scope ARN, other-region ARN and other-account ARN.

### F9. Region is undefined for unsigned, SigV2 and anonymous requests, and the S3 cross-region default misfires on them (section 7; ADR 0013)

**Problem.** Region comes only from the SigV4 credential scope, and `localhost` hosts carry no region. Some requests carry no region at all:
- boto3 presigns S3 URLs with SigV2 by default in eight legacy regions, including `us-east-1`, `us-west-1/2` and `eu-west-1` ([sigv4-checksums-endpoints.md](../../research/phase1/sigv4-checksums-endpoints.md) 2.3);
- Go `manager.GetBucketRegion` sends an unsigned `HEAD` ([wire-rest.md](../../research/phase1/wire-rest.md) 3.5);
- anonymous GETs.

ADR 0013 answers unsigned region-mismatched requests with `PermanentRedirect` 301. With no asserted region there is no mismatch to detect. The rule would fail a boto3 SigV2 presigned GET for a bucket outside `us-east-1`, which real S3 serves through the global or virtual-hosted endpoint.

**Recommendation.**
- Model the region source as `CredentialScope | HostRegion | None`.
- With `None`, S3 serves the bucket from its home region and always sets `x-amz-bucket-region`. Apply the cross-region error rules only when the request asserts a region.
- Document the `None` default for unsigned non-S3 calls.
- Add conformance cases: a boto3 SigV2 presign in `us-west-2`, and Go `GetBucketRegion`.

### F10. DynamoDB name escaping contradicts byte passthrough, and Local's message texts break Terraform (section 15; ADR 0014)

**Problem.**
1. Escaped internal table names require a rewrite in every request and response that names a table, not only in ARN-bearing ones:
   - `RequestItems`, `Responses`, `UnprocessedItems` and `UnprocessedKeys` map keys;
   - `TransactItems[].*.TableName` and `ConsumedCapacity.TableName`;
   - index and stream ARNs, shard iterators and error messages;
   - PartiQL statement text such as `SELECT * FROM "Locks"`, which needs a lexer. A mangled name there silently addresses a missing table.

   This contradicts "forward the original bytes" and "everything else passes through byte-identical".
2. On `DeleteTable`, Terraform treats `ResourceNotFoundException` as "already gone" only when the message contains "Requested resource not found: Table: ". Local's text is "Cannot do operations on a non-existent table" ([dynamodb-local.md](../../research/phase1/dynamodb-local.md) 4.2 and 7.1). The architecture normalizes only the casing of `message`.

**Recommendation.**
- Keep table names verbatim. The shim already holds per-table state, so it can detect a case-insensitive collision and reject the second table with a documented `aws-differs: dynamodb-local` coverage entry. Passthrough then stays byte-exact for every operation, PartiQL included.
- Add a translation table for the Local message texts that clients match, starting with `ResourceNotFoundException`, and recompute `x-amz-crc32` after translating.

### F11. DynamoDB persistence has two sources of truth, and Local's timestamps bypass the clock (sections 13, 15, 9, 12)

**Problem.**
1. Sections 15 and 13 conflict:
   - Section 15 runs Local with `-dbPath` under `--persist`.
   - Section 13 imports scanned items "into a fresh `-inMemory` Local".

   On restart with `--persist`, Local's files already hold the tables, so the import collides (`ResourceInUseException`) or writes twice. After a crash, Local holds tables up to 30 s newer than the shim's state, which lacks their tags, TTL specification and `TableId`.
2. Local stamps several values from the host wall clock:
   - `CreationDateTime` and `LastUpdateToPayPerRequestDateTime`;
   - `LatestStreamLabel` and the stream records' `ApproximateCreationDateTime`.

   Those values sit outside the emulator-clock contract and the seed. A re-import through `CreateTable` mints a new stream label, so `LatestStreamArn` and Terraform's `stream_arn` change after a snapshot load.

**Recommendation.**
- Choose one source of truth: always `-inMemory` with archive-only persistence, or `-dbPath` with the shim state committed beside it.
- Rewrite Local-originated timestamps into the emulator domain from instants the shim stores.
- Map stream labels in shim state so a reload keeps them, or list the change as a deviation.

### F12. Internal calls run under the wrong principal, so generated least-privilege policies would fail in AWS (sections 3.2 `Target.As`, 14.6, 16, 11)

**Problem.** AWS charges several internal calls to the original caller or the function role:
- S3 SSE-KMS, Secrets Manager and SQS SSE-KMS call KMS on behalf of the caller (forward access sessions). The caller's policy needs `kms:GenerateDataKey` and `kms:Decrypt`. The Service Reference excludes these actions ([smithy-models.md](../../research/phase1/smithy-models.md) 8).
- Lambda writes function logs with the execution role (`logs:CreateLogGroup`, `CreateLogStream`, `PutLogEvents`).

The architecture runs these calls as a `ServicePrincipal` or leaves the principal unspecified. `understudy iam policy` for a role would then omit actions that real AWS requires.

**Recommendation.**
- Give each internal path an explicit principal mode:
  - `Caller`, recorded as a forward-access call for KMS from S3, Secrets Manager and SQS;
  - `ExecutionRole` for ESM and Lambda logs;
  - `Service(name)` for SNS, S3 and EventBridge deliveries.
- Add policy-generation tests asserting that the generated policy for a function role contains the basic-execution log actions, and that the policy for an SSE-KMS workload contains the KMS actions.

### F13. Paths outside the model get no journal, faults or coverage (sections 3.4, 4.1, 9, 14.7, 17)

**Problem.** `registry.New` fails at boot on any binding absent from the model. These paths are therefore outside the pipeline:
- the function URL data plane: raw HTTP in, a payload v2.0 event out, a 403 `{"Message":null}` body with `x-amzn-ErrorType`, and a plain `Internal Server Error` 502;
- the SNS subscribe, unsubscribe and certificate URLs;
- the internal `sqs.HoldVisibility`.

They get no journal event, no fault rule, no `lambda:InvokeFunctionUrl` access record and no coverage line. That contradicts "one pipeline for every call", and function URL to Lambda is a phase 1 chain.

**Recommendation.**
- Add synthetic operations to the registry: declared in `codegen/services/<svc>.json`, each with an IAM action, a raw HTTP or typed internal handler and a `Routable` flag, and listed separately in coverage.
- Function URLs get raw request and response access, including non-AWS error bodies.

### F14. The determinism contract is overstated, so the e2e golden snapshot will flap (section 12, sections 10, 14.6 and 19; ADR 0018)

**Problem.** A seeded single-client run is not byte-reproducible once Lambda is in the loop:
- journal events carry `Wall` and wall-clock `Latency`;
- the REPORT lines stored in CloudWatch Logs carry wall-clock `Duration` and `Init Duration`;
- function SDK calls interleave with scheduler work on wall time;
- DynamoDB Local timestamps come from the wall clock (F11);
- STS `Expiration` is wall time;
- SSE-KMS ETags are random.

A gate that compares a golden snapshot byte for byte will flap.

**Recommendation.**
- Restate the contract: state sections are byte-identical except a named set of wall-domain fields, and journals are equal per trace tree, ignoring `Wall` and `Latency`.
- Ship `snapshot diff --normalize` with the closed normalizer set the parity replayer uses, and gate the e2e test on it.
- Draw SSE-KMS ETags from the seeded `ids` source.

### F15. The conformance matrix misses the HTTPS wire form and identifier fanout (section 19; ADR 0013)

**Problem.**
- Go v2 and botocore send `STREAMING-UNSIGNED-PAYLOAD-TRAILER` only over HTTPS. Over HTTP they send a checksum header ([sigv4-checksums-endpoints.md](../../research/phase1/sigv4-checksums-endpoints.md) 3.2 and 4.3). With the default plain-HTTP listener, only JS stream bodies exercise the trailer decoder. The research names this exact trap ([landscape.md](../../research/phase1/landscape.md) Traps: "tests must cover both http and https for every S3 write path").
- Two test generators that caught real bugs in other emulators are absent ([landscape.md](../../research/phase1/landscape.md) Implication 3):
  - identifier-form fanout: name, `name:qualifier`, partial ARN, full ARN, URL-encoded ARN;
  - create-then-get round-trip echo.

**Recommendation.**
- Run every S3 write-path case for every SDK over both HTTP and the self-signed HTTPS listener.
- Add both generators to the conformance suites. Fanout also tests F8.
- Report their results in the behavioral coverage number.

## Minor

### F16. Handlers cannot see unmodeled request headers (section 3.2)

**Problem.** `Call` and `External` expose no trace header and no unmodeled headers. Recorded behavior needs them:
- `X-Amzn-Trace-Id` sets the SQS `AWSTraceHeader` system attribute ([sqs.md](../../research/phase1/sqs.md) 6.1, recorded).
- That attribute propagates through SNS to SQS ([sns.md](../../research/phase1/sns.md) 5.1), into ESM records and into `Lambda-Runtime-Trace-Id`.
- S3 events carry `responseElements.x-amz-id-2`.

**Recommendation.** Add `Trace` to `Call`, taken from the header or inherited from the parent call. Expose a read-only set of selected headers.

### F17. `Call.Admitted` is the only "now" (section 3.2)

**Problem.** Some outputs and deadlines need the commit instant, not the admission instant:
- after a long poll, the receive timestamps;
- S3 `eventTime`, which is "when S3 finished processing";
- a request admitted before an `Advance` and committed after it, which arms visibility and retention deadlines in the past.

**Recommendation.** Define two instants:
- `Admitted`, for the journal and request time;
- the commit instant, `Clock.Now()` read under the cell lock, for state deadlines and server-assigned timestamps.

List the instant used per field.

### F18. Lambda execution gaps (sections 14.2, 14.7, 10)

**Problem.**
1. Real Lambda freezes the sandbox after `/response`. understudy containers keep running, so background threads, timers and flushers act between invocations, and their calls belong to no invocation.
2. Section 14.2 checks concurrency before container work and returns the 429 shape. That is right for a synchronous `Invoke`. For an `Event` invoke, AWS requeues throttles and 5xx errors for up to 6 h with 1 s to 5 min backoff ([lambda-events.md](../../research/phase1/lambda-events.md) 8).
3. SDK calls made during the init phase have no invocation to link to.

**Recommendation.**
- Pause environments between invocations through the Engine API pause and unpause calls, behind a fidelity switch.
- Specify the async throttle requeue policy in `delivery`.
- Link init-phase calls to the cold-start `Invocation` event.

### F19. Regex and string semantics differ between dialects (section 5.4)

**Problem.**
- The regex stage fixes only syntax. RE2 `\s` and `\w` are ASCII-only, while ECMA's `\s` is Unicode-aware. ECMA's `.` excludes `\r`, U+2028 and U+2029 ([smithy-models.md](../../research/phase1/smithy-models.md) 6.2). Translated patterns therefore accept and reject different strings without any error.
- The spec does not say that string `length` counts Unicode scalar values.
- json/v2 rejects invalid UTF-8 and lone surrogates by default. SQS `InvalidMessageContents` cases would surface as `SerializationException`.

**Recommendation.**
- Rewrite `\s` and `.` to explicit ECMA classes, and add a rapid differential test against an ECMA semantics table.
- State the rule for `length`.
- Decode `jsonx` input with invalid UTF-8 allowed and let validators classify it.
