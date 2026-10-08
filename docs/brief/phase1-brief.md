# understudy: phase 1 brief

Every agent working on this repo reads this first. It is the product brief, the phase 1 scope, the constraints, and the working rules.

## What we are building

**understudy** is an open-source local AWS emulator: a single Go binary (and a small Docker image) that stands in for AWS on a developer machine and in CI. Applications, the AWS SDKs, the AWS CLI and Terraform talk to it through the standard endpoint override (`AWS_ENDPOINT_URL`) with no code changes.

- Module path: `github.com/sustinbebustin/understudy`. Binary and CLI: `understudy`. License: Apache-2.0.
- Repo root: `/home/austin/dev/local-aws-stack`.
- It will be published publicly, so code, tests, docs and commit history must be fit for strangers to read, use and contribute to.

### Why it exists

LocalStack moved commercial use behind a paid plan in March 2026 and archived its open-source repo. moto is a Python mock library with a server mode. AWS ships no multi-service local emulator and tells people to test in the cloud. See `docs/research/localstack-vs-aws-tooling.md` for the cited landscape. understudy's bet is that a small number of services done with **real fidelity**, plus platform features the alternatives charge for or lack, beats broad shallow coverage.

### What must make it better than the alternatives

1. **Fidelity you can trust**:
   - Request and response shapes, error codes and input validation are generated from AWS's own Smithy models (`github.com/aws/api-models-aws`, Apache-2.0).
   - The protocol layer is checked against Smithy's official protocol compliance tests.
   - Behavior is checked against several real SDKs (Go v2, boto3, JS v3, AWS CLI) and Terraform.
   - Unimplemented operations fail loudly with AWS's own error format and say they are unimplemented. Nothing is silently faked.
2. **A controllable clock.** All time-based behavior runs on one emulator clock that can be frozen, advanced or scaled through the admin API and CLI. That covers visibility timeouts, delays, retention, TTL, batching windows, schedules, secret deletion windows and log retention. Tests then run real AWS timing settings in milliseconds without lying about them.
3. **A causal event journal.** Every API call and every cross-service delivery (SNS publish -> SQS message -> Lambda invocation -> downstream calls) is recorded with causal links and can be queried and streamed. This is a free equivalent of LocalStack's paid App Inspector.
4. **Fault injection.** Declarative rules by service, operation, resource, account or region inject AWS-shaped errors, throttling or latency. Rules can apply with a probability or for the next N matching calls, and use a seed so runs are deterministic.
5. **Snapshots and persistence.** The whole emulator state can be saved to and loaded from a versioned, deterministic file. Persistence across restarts is opt-in.
6. **Terraform-first.** `terraform apply` against understudy works for every phase 1 resource type. A second `terraform plan` shows no changes (read fidelity), and `terraform destroy` is clean.
7. **Real Lambda execution.** Functions run in AWS's official Lambda base images, with the Runtime Interface Emulator, through the Docker Engine API. Supported:
   - zip and container-image functions
   - warm container reuse
   - hot reload from a local directory
   - SDK calls inside functions routed back to understudy automatically
   - function logs delivered to CloudWatch Logs
8. **Least-privilege policy generation.** understudy records which IAM actions each principal used, and can print a least-privilege policy from that record. Phase 1 records access but does not enforce IAM.
9. **Instant, tiny, offline.** One static binary, millisecond startup, no account, no token, no telemetry, no network access needed at runtime. The one exception is pulling Docker images for Lambda and DynamoDB Local on first use.

## Phase 1 scope

The target workload is an event-driven pipeline:

- Webhooks publish to **SNS** topics.
- SQS subscriptions use filter policies (attribute and body scope) and raw message delivery.
- **SQS** queues have dead-letter queues and redrive.
- **Lambda** workers run in Go (`provided.al2023`), Python and container-image runtimes. They are fed by SQS event source mappings with batch sizes, batching windows and partial batch failure reporting (`ReportBatchItemFailures`).
- The workers use:
  - **S3**: virtual-hosted and path-style addressing, managed uploads (`upload_fileobj`, multipart), ListObjectsV2, SSE-KMS buckets.
  - A **DynamoDB** lock table: conditional writes such as `attribute_not_exists(LockID) OR ExpiresAt < :now`, plus TTL.
  - **Secrets Manager**.
  - **CloudWatch Logs**: a log group and stream per trace, read back with GetLogEvents and FilterLogEvents.
- An **EventBridge** scheduled rule invokes a Lambda.
- One Lambda is exposed through a **function URL**.
- **IAM** roles and policies, a **KMS** key and alias, and **CloudWatch** metric alarms exist because Terraform declares them.
- The SDKs used are aws-sdk-go-v2 and boto3. JS v3 and the AWS CLI are also first-class.

### Services and depth

The level each service must reach:

| Service | Phase 1 depth |
|---|---|
| SQS | Full data plane and control plane. Standard and FIFO queues. Long polling. Visibility, receipt handles, redrive and DLQ, redrive allow policy, DLQ redrive move tasks. Message and system attributes with exact MD5 checksums. Tags, queue policies (stored). JSON and Query protocols, including `awsQueryCompatible` errors. |
| SNS | Topics, subscriptions, Publish and PublishBatch. The full filter policy grammar for both scopes, with AWS's limits. Raw and enveloped delivery to SQS and Lambda, with AWS's envelope format. FIFO topics delivering to FIFO queues. Subscription attributes including redrive to a DLQ. Topic policies (stored). Tags. |
| S3 | Buckets, plus every bucket subresource Terraform reads (unset ones must return AWS's exact "not configured" errors). Objects: Put, Get, Head, Delete, DeleteObjects, Copy, ListObjects v1 and v2, multipart, ranges, conditional requests and conditional writes, user metadata, object tags. Every flexible checksum algorithm, including `aws-chunked` bodies with trailers. Presigned URLs. Virtual-hosted and path-style addressing. Should-have: event notifications to SQS, SNS and Lambda. |
| DynamoDB | Proxy to an understudy-managed DynamoDB Local container (AWS's own emulator, pulled at runtime and never bundled). Shims cover what Local lacks: TTL expiry on the emulator clock, tags, continuous backups and PITR describe, per account and region isolation. Calls go through the journal and fault injection. Stretch: Streams to Lambda. |
| Lambda | The API surface the SDKs and Terraform use: functions, configuration and code updates, the function state machine, Invoke (RequestResponse, Event, DryRun), permissions and policies, SQS event source mappings with exact semantics, function URLs (payload v2.0), versions and aliases (basic), concurrency settings (stored), tags. Execution as in item 7 above. Async invoke retries; stretch: destinations. |
| CloudWatch Logs | Log groups and streams, PutLogEvents, GetLogEvents, FilterLogEvents with the real filter pattern syntax (terms and JSON patterns), retention on the clock, tags. Lambda logs land here. |
| Secrets Manager | Create, Get, Put, Update, Describe, List, Delete (recovery window on the clock), Restore. Versions and stages with idempotency tokens. Resource policies (stored). Tags. |
| KMS | Keys, aliases, key policies, rotation status, tags, scheduled deletion. Encrypt, Decrypt and GenerateDataKey with real local cryptography. Used by S3 SSE-KMS and Secrets Manager. |
| IAM, STS | IAM: roles, managed and inline policies, attachments, and the minimal users and instance profiles Terraform needs. Stored only, plus the access recorder. STS: GetCallerIdentity and AssumeRole. The account is derived from the access key. |
| EventBridge | Default bus. Rules with schedule expressions (rate and cron, on the clock) and event patterns. Targets: Lambda, SQS and SNS, with input and input path. PutEvents. |
| CloudWatch (metrics) | Metric alarms CRUD and tags (stored), as Terraform needs. Basic PutMetricData and GetMetricData. |

Out of scope for phase 1 (roadmap): ECS, RDS, EC2/VPC, Step Functions, API Gateway, Cognito, Kinesis, IAM enforcement, a web UI.

### Platform features (phase 1)

- `understudy serve` on one port. Multi-account (from the access key) and multi-region.
- An admin API under a reserved path prefix, plus CLI subcommands for:
  - health
  - reset
  - clock
  - faults
  - journal (query and stream)
  - snapshots
  - resources
  - coverage
  - IAM policy generation
- A coverage report generated from the operation registry against the Smithy models (`understudy coverage`, also as Markdown for the docs).
- Structured logging (`log/slog`).
- Strict input validation from Smithy constraints. SigV4 signature validation is opt-in.
- A Docker image (static, multi-arch) and a release configuration. Nothing is published.

## Engineering constraints

- **Go 1.27** (installed: go1.27.1).
- The main module depends on the **standard library only**. The one pre-approved exception is test-only `pgregory.net/rapid`. Any other dependency needs an ADR that justifies it.
- Docker is reached through the Engine HTTP API over its unix socket with `net/http`; no Docker SDK. Podman's compatible socket should work.
- **Generated code is checked in**, carries the `// Code generated ... DO NOT EDIT.` header, and is reproducible with one command. Smithy models for the phase 1 services are vendored at a pinned commit, recorded in the repo.
- Conformance suites (Go SDK, Python, JS, CLI, Terraform, end-to-end) live outside the main module, with their own dependency manifests.
- **Tests never call real AWS.** A record/replay parity harness may exist, but its record mode is never run by agents.
- Security defaults:
  - Bind to 127.0.0.1 unless configured otherwise.
  - Mapping S3 keys to disk must be path-traversal safe.
  - Lambda zip extraction must be zip-slip safe.
  - Cap request body sizes.
  - Clean up Docker containers (all labeled) on shutdown and on crash recovery.
  - Never log secrets or credentials.
- Quality bar:
  - The global CLAUDE.md rules apply in full: ADTs and discriminated unions over flags, errors as values with typed errors, parse at boundaries, small cohesive packages around one domain type, no dead code, no stray TODOs.
  - Tests verify AWS's semantic behavior, not implementation details.
  - Use property tests (rapid) for parsers, filter and pattern engines, expression evaluation, checksums and encoders.
  - Use `testing/synctest` or the emulator clock for time.
  - `go vet`, `golangci-lint` and `go test -race` stay green.

## Environment

- **Toolchain:** Go 1.27.1, golangci-lint 2.13.2, just 1.58, Docker 29.6.2 (22 CPUs, 23 GB RAM), Terraform 1.16.0, AWS CLI 2.36, Python 3.14 with uv, Node 24 with bun and pnpm (no npm), gh 2.99.
- **No Java.** DynamoDB Local runs from the `amazon/dynamodb-local` image.
- **Research tools:** load WebFetch, WebSearch, `mcp__grep_app__searchGitHub` (code search across GitHub), `mcp__opensrc__execute` and context7 through ToolSearch. `gh api` reads GitHub repo contents. `go doc` reads the local toolchain.

## Working rules for agents

- Work only inside `/home/austin/dev/local-aws-stack`. Do not read or write anywhere else except the Go module and build caches, temp dirs you create, and public network sources.
- Never call real AWS with credentials. Never push, publish, or create remote resources.
- Do not commit. The orchestrator's wave gate commits once a wave is green.
- When running in parallel with other agents:
  - Edit only the paths your task owns.
  - Run formatters and linters only on your own packages.
  - Never run `go mod tidy` unless you own `go.mod`.
  - Tests bind ephemeral ports (`127.0.0.1:0`), never fixed ports.
  - Docker containers you start carry the label `understudy.dev/test=1` and a unique name, and you remove them before you finish.
- Read the architecture (`docs/design/ARCHITECTURE.md`) and ADRs (`docs/adr/`) before implementing. Where they are silent, follow the research in `docs/research/phase1/` and record a short decision in your report.
- No emojis anywhere.
