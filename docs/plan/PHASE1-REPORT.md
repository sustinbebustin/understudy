# Phase 1 report

Status: release review, 2026-10-13, at commit `fa84970` (working tree clean apart from this report and `docs/ROADMAP.md`). Written by the release reviewer against [the phase 1 brief](../brief/phase1-brief.md), from a clean build, the full verification, every conformance suite, the README quickstart followed from scratch, and each differentiator exercised by hand. Environment: Linux (Ubuntu 24.04, 22 CPUs), Go 1.27.2, Docker 29.6.2, Terraform 1.16.0, AWS CLI 2.37.13, bun 1.4.3, uv.

## Verdict

**Ready with caveats.** Every brief item is met or met with a named gap; nothing is faked or silently missing. The gates are green, the external suites pass (the two failures left are defects in a test file, not in the emulator), Terraform applies, re-plans empty, updates and destroys cleanly for all eleven resource services, and Lambda runs real code in the official images with hot reload, function URLs and SQS mappings.

What stands between this and a first public tag is trust, not features: no behavior has been recorded against real AWS, so 115 overlay entries and 52 fidelity switches rest on inference; `--sigv4 verify` is refused; presigned URLs are accepted but never checked (and one guide page says otherwise); the DynamoDB Local licence reading, the macOS topology and the ADR 0010 deviation from the brief are open decisions the architecture assigns to a human. [docs/ROADMAP.md](../ROADMAP.md) puts closing those first.

## What was run

| Check | Result |
|---|---|
| `go build ./cmd/understudy` | 1.8 s; 53 MB unstripped, 35.8 MiB stripped |
| `just verify` (gofmt, vet, golangci-lint, `go test -race`), then `go test -race -count=1 ./...` uncached | green, 89 packages |
| `just generate-check`, `just coverage-check`, `golangci-lint config verify`, `just sealed-test` (23 tests) | green; `git status` clean after regeneration |
| `just budgets` | binary 35.8 MiB (ceiling 48), ready in 14 ms (250), idle 31.8 MiB (128), cold build 17.4 s, lint 1.3 s |
| `just smoke`, `go test -race -count=1 -tags docker ./...` | green, 90 packages |
| `just release-check` | skipped: goreleaser is not installed here; CI runs it |
| `rg TODO\|FIXME` over Go sources | none |
| `conformance/go-sdk/run.sh` (64 tests, 211 subtests; HTTP and TLS listeners) | pass, 62 s |
| `conformance/python-boto3/run.sh` (current boto3 and the last Query-protocol botocore) | 527 passed, 4 skipped (need `--sigv4 verify`); legacy 75 passed |
| `conformance/js-sdk-v3/run.sh` (879 tests) | 877 passed, 2 failed, both test defects (below) |
| `conformance/aws-cli/run.sh` (618 tests) | rerun: 618 passed, 0 failed, 0 skipped. The first run had 271 failures, every one exit 127, because the `aws` binary was being replaced by a brew upgrade at 15:39 as the suite started |
| `conformance/terraform/run.sh` (11 services x apply, empty plan, update, empty plan, destroy, nothing left) | 66 passed, 0 failed, 0 skipped |
| `conformance/e2e-pipeline/run.sh` (29 tests, Go, Python and image workers) | pass, 17 s |
| `examples/check.sh` (python-worker, sns-sqs-lambda-terraform, docker-compose, github-actions) | all 4 pass |
| README quickstart, verbatim through `scripts/sealed` | behaves exactly as written; both messages arrive after `clock advance 15m` |

All SDK suites ran with the DynamoDB Local licence accepted (as the Python and e2e scripts do by default), so DynamoDB is covered by every client and by Terraform. Lambda images for Python 3.13 and `provided.al2023` were already present.

## Scorecard

### What must make it better than the alternatives

| # | Brief item | Status | Evidence |
|---|---|---|---|
| 1 | Fidelity from Smithy models, protocol tests, several SDKs, loud 501s | **Partial** | 252 generated files from models pinned at `1cfdfb4` (`just generate-check` green). Protocol corpus: 1,343 server-runnable cases, 1,261 passed, 67 skipped with reasons, 15 known failures in three places (`docs/coverage.md`). Four clients (five counting the legacy Query-protocol botocore) plus Terraform check behavior. `secretsmanager get-random-password` answers `NotImplemented` naming the operation and `understudy coverage`; `ecs list-clusters` names the twelve services it does emulate. The gap: zero `aws-recorded` evidence entries. The coverage page carries 121 `third-party-recorded`, 115 `inferred`, 64 `aws-doc`, 11 `sdk-source`, and 66 fidelity switches (52 inferred). The README says so. |
| 2 | Controllable clock | **Met** | `clock freeze`, `advance`, `set`, `scale`, `resume` over CLI and `POST /_understudy/v1/clock`. Seen on the clock: a 30 s visibility timeout and a 900 s delay (quickstart), a `rate(5 minutes)` rule delivering 12 messages over `advance 1h --settle`, a 7-day secret recovery window (gone after `advance 192h`), a 1-day log retention, KMS `PendingDeletion`, DynamoDB TTL (e2e suite). Caveat: `docs/guide/clock.md` lists "S3 presigned URL expiry" among clock-driven behaviors, but presigned URLs are never verified (see item 1 under Findings). |
| 3 | Causal event journal | **Met** | `sns.Publish` -> advisory -> `sqs.SendMessage` -> delivery as one tree; `lambda.Invoke` -> the function's own `sqs.GetQueueUrl` and `SendMessage` -> invocation (cold, 748 ms) -> `logs.CreateLogGroup/CreateLogStream/PutLogEventsInternal`. Kinds seen: api-call, delivery (delivered, filtered, dropped), invocation, timer-fired, advisory, fault-injected. `trace --canonical` normalizes ids and times. `/journal/events`, `/journal/stream` (NDJSON, followed live) and `/journal/traces/<id>` work. `SecretString` is recorded as `[redacted]`. |
| 4 | Fault injection | **Met** | Rules by service, operation, resource glob, account, region, origin; actions error, throttle, latency, after-commit, status-ok, drop; triggers always, `--next N`, `--probability P` seeded by `serve --seed` and the rule id. A rule naming an error the operation never sends is refused with the list it does send. An injected `QueueDoesNotExist` arrives as `AWS.SimpleQueueService.NonExistentQueue` on the JSON protocol. Delivery-phase drop of `sns-to-sqs` observed and journaled. Caveat (documented): resource-scoped rules match Lambda's `Invoke` only. |
| 5 | Snapshots and persistence | **Met** | `snapshot save/load/inspect/diff`, gzip by extension, `--include-journal`, `--on-server`, `serve --load-snapshot`, `--persist DIR` with 30 s checkpoints (every suite runs under it). Two saves of one state are byte-identical. Load restores the clock to the archive's instant. Caveats: format version 0; `diff` without `--normalize` reports the manifest's `journalNext`; DynamoDB stream labels change across a load. |
| 6 | Terraform-first | **Met** | 66/66 phases with provider 6.68.0 for sqs, sns, s3, dynamodb, kms, secretsmanager, iam, lambda, logs, events, cloudwatch; the SNS -> SQS -> Go Lambda -> S3 example applies, runs and destroys. Hot reload leaves `CodeSha256` unchanged so a plan stays empty. Documented gaps: `terraform import` of SQS queues; `s3_use_path_style` and `skip_requesting_account_id` are needed where `*.localhost` does not resolve. |
| 7 | Real Lambda execution | **Met, with a deviation to sign off** | Zip (Python 3.13, `provided.al2023`) and image functions run in the official base images through the Engine API on the socket. Cold 748 ms, warm 5 ms (journal). `AWS_ENDPOINT_URL=http://172.17.0.1:4566` inside the function; its SDK calls reach understudy as the execution role. Logs land in `/aws/lambda/<name>` with AWS stream names; `--log-type Tail` returns START/END/REPORT. Hot reload (`understudy lambda hot-reload`) swapped code on the next invoke with a cold start. Function URL answered 403 until both permissions existed, then served payload v2.0 on both the `*.lambda-url.*.localhost` and the path form. SQS mapping with `ReportBatchItemFailures` kept only the failed record in flight. The deviation: understudy serves the Runtime API itself rather than running the RIE ([ADR 0010](../adr/0010-lambda-hosted-runtime-api.md), open decision 25.4), with measured reasons; the brief's wording needs the human sign-off the ADR asks for. Not built: layers (refused), destinations (stored), rootless Docker and Podman, macOS (untested). |
| 8 | Least-privilege policy generation | **Met, with quality caveats** | `iam usage` and `iam policy` per principal, with modes direct, caller and execution-role; resource-policy requirements for deliveries. A role that sent, uploaded and received got exact ARNs. Caveats a user will hit: every Service Reference action of an operation is recorded, so `PutObject` yields `s3:PutObjectAcl`, `PutObjectLegalHold`, `PutObjectRetention`, `PutObjectTagging` and `s3-object-lambda:PutObject` on `*`; `AssumeRole` yields `sts:SetContext`, `SetSourceIdentity`, `TagSession`; S3 object actions are recorded against the bucket ARN; `logs:PutLogEvents` is not recorded for the execution role. The guide says all of this. A starting point, not a drop-in policy. |
| 9 | Instant, tiny, offline | **Met** | 35.8 MiB static binary, 14 ms to ready, 31.8 MiB idle; standard library only; no telemetry; the only network use is image pulls. Stated exception: DynamoDB needs the DynamoDB Local licence (an AWS account) and a Docker daemon, and answers 501 with the licence terms until `--accept-dynamodb-local-license`. |

### Phase 1 workload

| Workload piece | Status | Evidence |
|---|---|---|
| Webhooks publish to SNS; SQS subscriptions with filter policies (attribute and body scope) and raw delivery | Met | Attribute-scope filter kept `kind=order` and filtered `refund` (journal shows both); body scope and the full grammar are in the SNS unit and SDK suites |
| SQS dead-letter queues and redrive | Met | `maxReceiveCount=2`: third receive empty, DLQ holds 1; move tasks and redrive allow policy are bound (`coverage --service sqs`: 21 full, 2 stored-only) |
| Lambda workers in Go, Python and image runtimes fed by SQS mappings with batch size, window and partial batch failure | Met | e2e suite (`goworker`, `pyworker`, `imageworker`); hand-run mapping above |
| S3 virtual-hosted and path-style, managed uploads, ListObjectsV2, SSE-KMS | Met | boto3 with `addressing_style=virtual` at `http://s3.localhost:4566` put, got and listed; a 20 MiB `aws s3 cp` arrived as a 3-part multipart (`ETag ...-3`); SSE-KMS buckets in the e2e and Terraform suites |
| DynamoDB lock table: `attribute_not_exists(LockID) OR ExpiresAt < :now`, TTL | Met | e2e `dynamodb_test.go` and the boto3 suite; TTL expires on the emulator clock |
| Secrets Manager | Met | create, get, delete with a recovery window, restore, describe after the window |
| CloudWatch Logs: group and stream per trace, GetLogEvents, FilterLogEvents | Met | JSON pattern `{ $.level = "error" }` and term `ERROR` both matched; retention on the clock |
| EventBridge scheduled rule invokes a Lambda | Met | `rate(5 minutes)` to SQS by hand; to Lambda in the Terraform `events` suite and the guide |
| One Lambda exposed through a function URL | Met | above |
| IAM roles and policies, a KMS key and alias, CloudWatch alarms exist for Terraform | Met | Terraform iam, kms, cloudwatch suites; `kms encrypt`/`decrypt` round trip; alarm created and listed |
| Go v2 and boto3 first; JS v3 and the CLI first-class | Met | four client suites |

### Services and depth

Counts are from `docs/coverage.md` (operations in the model / full / stored-only / partial / unbound). Across the twelve models: 821 operations, 171 full, 67 stored-only, 109 partial, 474 unbound.

| Service | Brief depth | Status | Notes |
|---|---|---|---|
| SQS | 23 / 21 / 2 / 0 / 0 | **Met** | Everything in the brief; JSON and Query protocols (the legacy-botocore group passes); `AddPermission`/`RemovePermission` stored |
| SNS | 42 / 12 / 2 / 5 / 23 | **Met** | SQS and Lambda delivery, raw and enveloped, FIFO to FIFO, redrive, filter grammar; HTTP/S, email, SMS endpoints subscribe but receive nothing (phase 2 by design); the 10,000 filter-policies-per-account quota is not enforced |
| S3 | 112 / 14 / 47 / 21 / 30 | **Met** | Every subresource Terraform reads, with the "not configured" errors (`NoSuchCORSConfiguration`, `NoSuchLifecycleConfiguration`, `NoSuchBucketPolicy` seen); objects, multipart, ranges, conditional requests, checksums with `aws-chunked` trailers over TLS, notifications to SQS, SNS and Lambda. Gaps: presigned URLs are not verified; chunk signatures are not verified; no SSE-C, Object Lock or MFA delete; EventBridge notification configuration is stored only |
| DynamoDB | 58 / 6 / 2 / 18 / 32 | **Met via DynamoDB Local** | TTL on the clock, tags, PITR describe, per account and region isolation, journal and faults in front of Local. Inherits Local's differences (no throughput limits, case-insensitive names, no transaction conflicts). Not supported under Docker Desktop until `internal/docker` has port bindings (ADR 0014 amendment). Streams to Lambda (stretch) not built |
| Lambda | 88 / 25 / 2 / 14 / 47 | **Met** | See differentiator 7. Versions and aliases basic (weighted routing stored); concurrency stored; async retries on the clock; destinations stored only; layers refused; `PublishTo LATEST_PUBLISHED` refused |
| CloudWatch Logs | 118 / 9 / 0 / 9 / 100 | **Met** | Groups, streams, put/get/filter with the real pattern syntax, retention, tags; Lambda logs land here. Over-length names get the generic validator text rather than AWS's (deferred) |
| Secrets Manager | 23 / 12 / 1 / 2 / 8 | **Met** | Versions and stages with idempotency tokens (SDK suites), recovery window on the clock, restore, resource policies stored, tags; rotation and replication unbound |
| KMS | 54 / 19 / 3 / 5 / 27 | **Met** | Keys, aliases, key policies, rotation status, tags, scheduled deletion; Encrypt, Decrypt, GenerateDataKey with AES-256-GCM; used by S3 SSE-KMS and Secrets Manager |
| IAM, STS | 180 / 42 / 6 / 16 / 116; 11 / 1 / 0 / 2 / 8 | **Met** | Roles, managed and inline policies, attachments, users, instance profiles, access keys; `GetCallerIdentity`, `AssumeRole` minting `ASIA` keys that act as the role; account from the access key. AWS managed policies have no documents (placeholder); root `GetUser` sends empty `Path`/`UserName` |
| EventBridge | 57 / 9 / 0 / 8 / 40 | **Met** | Default bus, rate and cron on the clock, patterns via the Ruler engine, Lambda/SQS/SNS targets with input and input path, retry policy and DLQ, `PutEvents`; `CreateEventBus` unbound |
| CloudWatch | 55 / 1 / 2 / 9 / 43 | **Met (minimal, as specified)** | Alarms CRUD and tags; alarms are stored and never evaluated; `PutMetricData` and `GetMetricData` (MetricStat only; no math, Insights or percentiles); `GetMetricStatistics` unbound |

### Platform features

| Item | Status | Evidence |
|---|---|---|
| `understudy serve` on one port; multi-account and multi-region | Met | 12-digit key selects the account (`111111111111` sees no queues, `GetCallerIdentity` returns it); `--region eu-west-1` is empty |
| Admin API and CLI: health, reset, clock, faults, journal (query and stream), snapshots, resources, coverage, IAM policy | Met | Each exercised over the CLI and `curl` against `/_understudy/v1/`; `settle` and `doctor` as well |
| Coverage report from the registry against the models, also as Markdown | Met | `understudy coverage --service sqs`; `just coverage-check` keeps `docs/coverage.md` current |
| Structured logging (`log/slog`) | Met | `--log-format json` lines with `time`, `level`, `msg`, fields |
| Strict input validation from Smithy constraints | Met | An 81-character queue name and `DelaySeconds=901` get AWS's `InvalidParameterValue` texts; 621/655 restJson1 malformed-request cases pass |
| SigV4 signature validation, opt-in | **Missing** | `serve --sigv4 verify` is refused by name (exit 2); signatures are parsed for identity only; the Python suite skips its 4 signature tests; presigned URLs never expire |
| Docker image (static, multi-arch) and a release configuration; nothing published | Met, partly unverified here | `Dockerfile` from scratch, non-root, healthcheck; `just image` builds `understudy:dev` (48.5 MB uncompressed) and the compose example builds and runs the image; `.goreleaser.yaml` builds linux/darwin x amd64/arm64 and multi-arch images with publishing disabled. Not verified on this machine: the multi-arch build and the README's "about 11 MB compressed" (goreleaser absent; CI's release workflow runs it on tags) |

### Engineering constraints

| Constraint | Status | Evidence |
|---|---|---|
| Go 1.27 | Met | `go.mod` `go 1.27`; built and tested with 1.27.2 |
| Standard library only; `pgregory.net/rapid` test-only; ADR for anything else | Met | `go.mod` has one requirement; ADR 0016 covers third-party inputs; 109 test files use rapid |
| Docker through the Engine HTTP API over the socket, no SDK | Met | `internal/docker` on `net/http`; `doctor` reports the socket and the bridge gateway |
| Generated code checked in, headed, reproducible with one command; models vendored at a pinned commit | Met | `just generate-check` regenerates with no diff; `third_party/api-models-aws/UPSTREAM.json` pins `1cfdfb4` with hashes; `vendor -verify` offline |
| Conformance suites outside the module with their own manifests | Met | Six suites, each a module or project; the Go ones are vetted and compiled but not linted (deferred) |
| Tests never call real AWS; record mode never run by agents | Met | `scripts/sealed` wraps every AWS-facing command, refuses non-loopback endpoints, and has 23 tests; zero `aws-recorded` entries confirm no recording happened |
| Security defaults: loopback bind, path-safe S3 keys, zip-slip-safe extraction, body caps, labeled container cleanup, no secrets in logs | Met | Loopback default and token rules in `serve --help`; content-hash object store, zip checks and caps per `SECURITY.md` and the package tests; every container carries `understudy.dev/*` labels, `cleanup --persist` and the start-up sweep remove leftovers (each suite relies on it); secrets redacted in the journal. Known hole, documented in `SECURITY.md` and ADR 0011: in the in-container and Docker Desktop topologies, function code reaches the whole admin API |
| Quality bar: ADTs, typed errors, parse at boundaries, small packages, no dead code, no TODOs; property tests; synctest or the emulator clock; vet, lint, race green | Met | All green uncached; no TODOs; 2,378 test functions over 141k hand-written non-test lines and 98k test lines; service tests run in `kerneltest` bubbles on the emulator clock; `internal/ref/deps_test.go` enforces import direction |

## Remaining conformance failures

- **Protocol compliance, 15 known failures** (`docs/coverage.md`, "Known failures"), unchanged since wave 6: 6 `ComplexError` cases need a structured error member in `internal/awserr`; 5 `ServerPopulatesDefaults*` cases need the generator to write modeled defaults for unset output members; 4 `SparseSetMap*` cases need null-preserving sparse maps of sets in the generated serde. Each names the package to fix.
- **JS SDK v3, 2 failures in `tests/dynamodb.test.ts`**, both defects in the test, not the emulator: `ConditionExpression: "Owner = :me"` and `UpdateExpression: "SET items = ..."` use the DynamoDB reserved words `Owner` and `items` without `ExpressionAttributeNames`; DynamoDB Local answers `ValidationException: Attribute name is a reserved keyword`, exactly as the service does. This file had never been run by an agent (`DEFERRED.md`, conformance round 1). Fix: alias the attributes.
- **boto3, 4 skipped** signature tests that need `--sigv4 verify`.
- **AWS CLI**: none. The first run's 271 failures were all `exit 127` from the `aws` binary being replaced by a brew upgrade (2.37.12 -> 2.37.13 at 15:39) during the run; the rerun passed 618 of 618.
- Go SDK, Terraform, e2e-pipeline and the examples: no failures.

## Known limitations

User-facing, in the order a first-time user meets them. The README's Limitations section covers most of these; the ones marked (new) do not appear there.

1. No behavior is recorded against real AWS. Every inferred default is a named `fidelity.Switch` or overlay entry with its research reference, and the record backlog exists (`docs/research/phase1/s3-checksum-residuals-and-record-backlog.md`), but the record mode has not been run.
2. Signatures are parsed, not verified; `--sigv4 verify` is refused. Presigned URLs are accepted regardless of signature, expiry or `X-Amz-Expires` (new in the guide: `clock.md` claims the opposite).
3. IAM identity, resource and key policies are stored, not enforced; deliveries AWS would deny go through with a journaled advisory.
4. Generated policies over-approximate (differentiator 8) and must be reviewed before use.
5. SNS HTTP/S, email and SMS endpoints receive nothing; Lambda asynchronous destinations are stored only; Lambda layers are refused; S3 has no SSE-C, Object Lock or MFA delete.
6. DynamoDB needs Docker and the DynamoDB Local licence, inherits Local's documented differences, deletes expired items as soon as the clock passes their TTL, and does not work under Docker Desktop yet (new: README says Docker Desktop is "supported by design" without this exception).
7. Lambda needs a Docker Engine; tested on Linux only; Docker Desktop on macOS untested; rootless Docker and Podman unsupported; Windows through WSL2 only; arm64 functions need a binfmt handler (`doctor` says so). The Runtime Interface Emulator is not used (ADR 0010).
8. Anyone who can reach the port has full access, and in the in-container and Docker Desktop topologies function code can reach the admin API (reset, clock, journal).
9. Snapshot format is version 0; the TLS certificate is new at every start; a `--persist` server checkpoints every 30 s even when idle.
10. CloudWatch alarms never evaluate; `GetMetricStatistics` answers 501; `GetMetricData` has no metric math.
11. `terraform import` of SQS queues fails on the queue URL form; `CreateEventBus` is unbound, so only the default bus exists.
12. 474 of 821 modeled operations are unbound and answer 501. The coverage page is the authority; the README table summarizes it.

## Findings from this review

Ranked by what a user would hit first. None changes the verdict; the first two should be fixed before a tag.

1. **Docs defect, fidelity.** `docs/guide/clock.md` lists "S3 presigned URL expiry" as clock-driven. A URL presigned for 60 s still answered after the emulator moved two minutes past it, and `docs/coverage.md` says "presigned URLs are not verified" on `GetObject` and `HeadObject`. Either remove the claim or close the deferred "Presigned URL verification" item (`DEFERRED.md`, wave 7).
2. **Suite defect.** The two JS DynamoDB tests above. The DynamoDB file of the JS suite should also run in CI with the licence accepted, as the Python and e2e suites do, or it will rot again.
3. **Policy output noise.** `s3-object-lambda:PutObject` on `Resource: "*"` for a plain `PutObject`, and `iam:PassRole` on `*` for a session that never passed a role, make `iam policy` output harder to trust than its ARNs deserve. A per-operation allow-list over the Service Reference table (or at least dropping actions of other service prefixes) would fix the worst of it.
4. **README claim not reproducible locally.** "Docker image of about 11 MB compressed" and "multi-arch" rest on the release workflow; `just release-check` is a no-op without goreleaser. Worth a line saying where the number comes from.
5. **Docker Desktop and DynamoDB.** The README says Docker Desktop is supported by design; ADR 0014's wave 6 amendment says DynamoDB is unsupported there until port bindings land. The README should say so.
6. **Cosmetics.** `understudy env` prints `http://localhost:4566` while the banner and queue URLs use `127.0.0.1` (both work). A delayed message's timer prints as `sqs.visibility` in `clock advance` output. `snapshot diff` without `--normalize` shows the manifest's `journalNext`.
7. **Environment drift.** The brief pins AWS CLI 2.36; this machine moved from 2.37.12 to 2.37.13 during the review and the suites still pass. The CLI suite should refuse to start when `aws` cannot be executed, rather than record hundreds of exit-127 failures.

## Open decisions before a first public tag

From `ARCHITECTURE.md` section 25, all still open and all owned by a human:

- 25.1 Run the Docker suite and the smoke test on macOS (Docker Desktop, Apple Silicon).
- 25.2 Counsel's reading of the DynamoDB Local licence; the fallback (a native lock-table subset) is designed, not built.
- 25.3 Record the core workload and Terraform plan stability against real AWS (R01 to R07) so the coverage page gains `aws-recorded` entries and the README wording can firm up.
- 25.4 Sign off ADR 0010's deviation from "with the Runtime Interface Emulator".

Plus, from this review: wire `--sigv4 verify` or remove the opt-in from the brief's promise, and fix the presign claim.
