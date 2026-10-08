# Review: product, developer experience and delivery risk

Reviewer lens: does the architecture deliver the brief's differentiators in a way users will love, is the testing strong enough to back fidelity claims, and does the build order retire the riskiest work first. Inputs: [ARCHITECTURE.md](../ARCHITECTURE.md), [ADRs 0001-0018](../../adr/), the three proposals, and `docs/research/phase1/`. Date: 2026-10-08.

## Verdict

The core is strong: one pipeline for internal and external calls, state-derived timers, typed journal causes, catalogue-validated faults and a three-number coverage report are the right bets and beat every emulator in `landscape.md`. The risks sit where those features meet real users. Four problems are already in the design:

1. A frozen clock hangs `terraform apply`.
2. The Docker image the brief requires is not designed.
3. The generated least-privilege policy would be missing actions that AWS checks for the brief's own workload.
4. The build order leaves the riskiest integrations (Lambda on macOS, the `aws_s3_bucket` read path, DynamoDB persistence) until waves 3 to 5.

## Critical

### C1. A frozen clock makes Terraform hang (sections 9, 14.7, 14.8, 17; ADR 0006)

**Problem.** Lambda `Pending -> Active` and `LastUpdateStatus` move on the emulator clock after a 10 ms delay. Section 14.7 says "freezing the clock freezes the transition". ESM transients also resolve "on the clock within a tick" (14.8). The clock driver does not run in Frozen mode (section 9).

So the documented inner loop breaks: `serve --clock frozen` followed by `terraform apply` leaves `aws_lambda_function` and `aws_lambda_event_source_mapping` waiting until their provider timeouts, which are wall-clock minutes. The research said the opposite: "Make the first poll final... Do not model `CREATING`, `ENABLING` or `DELETING` intermediates by default; if the clock feature adds them, they must be opt-in because they multiply apply time" (`tf-storage.md` implication 3). platform-dx 12.1 agreed.

The same wording leaves another question open. Deliveries are "a scheduler task at `now`" (section 16), and it is unclear whether a task due at exactly `Now()` fires while the clock is frozen. If it does not, nothing SNS publishes is delivered until someone advances the clock.

**Recommendation.**
- Make control-plane transitions final before the mutating call returns, by default and in every clock mode. Put clock-driven intermediates behind an opt-in fidelity switch (`lambda.stateTransitions: clock`) for people who test SDK waiters.
- State the Frozen contract in section 9 and ADR 0006: tasks due at or before `Now()` fire immediately, and only future tasks wait for `Advance`.
- Add a conformance case that runs `terraform apply` of a Lambda function and an ESM with `--clock frozen` and requires it to finish within the provider floors.

### C2. The Docker image and in-container mode are not designed, and the security defaults block them (sections 2, 14.5, 15, 17, 20, 23)

**Problem.** The brief lists "a Docker image (static, multi-arch) and a release configuration" as phase 1 deliverables. The architecture has no section on either, and no wave builds them. Running in a container (docker-compose, Testcontainers, CI service containers) is how most LocalStack users ran it, so this is likely the most common deployment. As designed it fails in three places:

- **Listener.** Inside a container the main listener must bind `0.0.0.0` to be reachable through `-p`. Sections 17 and 20 make `serve` exit on a non-loopback listener without an admin token, so `docker run -p 4566:4566 understudy` fails on first use.
- **DynamoDB Local.** It is "published on `127.0.0.1:0`" (section 15). From inside the understudy container that address is the container's own loopback, so DynamoDB is unreachable.
- **Testing.** `InContainer` is a variant of the `Topology` type, but "Linux bridge is first-class and tested in CI; the others are best effort" (14.5). Nothing tests it.

**Recommendation.**
- Add a short "Distribution" section: the image base (scratch plus CA certificates), the non-root user, socket-mount requirements, and the release matrix (linux/darwin, amd64/arm64 binaries; multi-arch image).
- When `InContainer` is detected, allow binding `0.0.0.0` without a token (the published port is the exposure control). Log a warning in the banner and document `-p 127.0.0.1:4566:4566`.
- Run DynamoDB Local on understudy's network and address it by container IP when `InContainer`, as Lambda environments already are.
- Make `InContainer` a CI-tested topology alongside Linux bridge.
- Schedule the image in wave 2 so every later conformance suite can also run against it.

### C3. Policy generation would leave out actions AWS checks for the brief's own workload (sections 3.2, 4 step 11, 14.6, 16; ADR 0004)

**Problem.** `Target.As` is defined as "ServicePrincipal, or the function's role for ESM polling". The S3 -> KMS, Secrets Manager -> KMS and Lambda -> Logs calls name no principal. In AWS these calls are authorized against the caller:

- `PutObject` to an SSE-KMS bucket needs `kms:GenerateDataKey` on the caller; `GetObject` needs `kms:Decrypt` (`iam-sts-kms.md` line 453).
- Function logs need `logs:CreateLogStream` and `logs:PutLogEvents` on the execution role.

If these calls run as a service principal, `understudy iam policy --principal <worker role>` produces a policy that fails in AWS on the brief's own workload (SSE-KMS buckets, Lambda logs). The differentiator becomes a generator of AccessDenied errors.

Two further gaps:
- Deliveries that AWS gates by resource policy (SNS -> SQS queue policy, EventBridge -> `lambda:InvokeFunction` permission, S3 -> SQS) are where "works locally, silently fails in AWS" happens most. The design neither records nor reports them.
- No test layer checks the policy output, and no wave owns it beyond "access recorder and policy output" in wave 4.

**Recommendation.**
- Add an "on-behalf-of" rule to section 3.2 and ADR 0004. Internal calls that AWS authorizes against the caller run `As` the caller's principal (S3 and Secrets Manager -> KMS: the request principal; Lambda -> Logs and ESM -> SQS: the execution role). Deliveries that AWS authorizes by resource policy run as `ServicePrincipal`.
- Have the access recorder report "resource-policy requirements" (target ARN, service principal, action, source ARN) next to identity policies. Resource policies are already stored and `policydoc` already parses them, so a journal `Advisory` event when a stored policy would not allow a delivery is cheap. It addresses the most common local-versus-cloud surprise without IAM enforcement.
- Add a golden test: run the phase 1 workload and compare the generated policy for each role against the AWS-documented managed policies (`AWSLambdaSQSQueueExecutionRole`, `AWSLambdaBasicExecutionRole`, plus the SSE-KMS actions).
- Define the output contract in section 17: exact ARNs versus wildcards, how `*Batch` and multi-action operations map, and whether failed (non-injected) calls are recorded. Simplicity's argument that the application's handled failures need permissions too is right.

## Major

### M1. macOS and Apple Silicon are best effort, but they are where many developers work (sections 14.3, 14.5, ADR 0011, ADR 0013)

**Problem.** The design leaves the Docker Desktop topology open:
- It does not say which address the container listener binds, or which IPv4 literal goes into `AWS_ENDPOINT_URL`. `host.docker.internal` is a name, and resolving it from the host is not described.
- `containers.md` 8.2 marks "whether loopback is enough on Desktop" UNVERIFIED.
- `*.localhost` resolution on macOS is UNVERIFIED (`landscape.md` 4.3). That decides Go and JS virtual-hosted S3. It also decides the Terraform S3 Control tag read on `<account>.localhost`, without which "every tagged `aws_s3_bucket` is a perpetual diff or a dial error" (`tf-storage.md` implication 1).
- A function's `Architectures` versus the host CPU (Apple Silicon running the default `x86_64` functions, or an arm64 `bootstrap` declared `x86_64`) is open question 8 in `containers.md` 14.4. The architecture never answers it.
- Hot reload on Desktop and Colima only works inside shared directories (`containers.md` 10).

**Recommendation.**
- Specify the `DockerDesktop` variant fully: bind the main port on loopback (Desktop proxies outbound container connections from the host), and resolve the function endpoint by probing once from a labeled container, then caching the IP.
- Decide the architecture-mismatch rule. Honour `Architectures` (AWS fails a mismatched binary too). Turn `exec format error` or exit 126 into a function error message that names both architectures. Have `doctor` report binfmt availability.
- Have `doctor` and the startup banner check shared paths for hot reload.
- Before wave 3 closes, have a human run the e2e workload and the Terraform suite on macOS (Docker Desktop, Apple Silicon). Agents only have Linux. Record the results in ADR 0011 and decide whether Docker Desktop becomes "tested" rather than "best effort".

### M2. The tracer bullet does not exercise the riskiest parts (section 23)

**Problem.** Wave 1 is SQS over awsJson1_0, which is the best-understood path. The integrations most likely to force redesign come later:
- Docker, the hosted Runtime API and topology are in wave 3. Research proved them on Linux only.
- Terraform beyond bootstrap is in wave 4.
- The `aws_s3_bucket` read path is in wave 5 ("subresources"). Every `aws_s3_bucket` refresh makes 14 calls, and each unset subresource needs its exact not-configured error (`tf-storage.md` 2.2). Until wave 5 no Terraform configuration with a bucket can apply, and almost every real configuration has one.
- DynamoDB Local is in wave 4.

Generated-code cost is measured "on the SQS slice". SQS is one of the smallest models. IAM, S3 and Lambda dominate the 5,487 shapes, so the measurement says little about compile time, binary size or lint time.

Wave 3 also bundles the full SNS filter grammar, all of SQS, the Docker client, Lambda execution, ESM, function URLs and logs. That is a breadth wave, not a thin chain.

**Recommendation.**
- Wave 1: generate types and serde for all 12 models (no handlers) and record build time, binary size, `golangci-lint` time and `go test -race` time against budgets (see M6).
- Wave 2: add exit criteria for `aws_sqs_queue` and `aws_s3_bucket` (with tags) through apply, empty second plan and destroy. That pulls the 14 S3 subresource reads and S3 Control tagging forward from wave 5.
- Wave 2: run a Lambda spike in parallel with protocol breadth. That means Docker client, `runtimeapi` and topology, invoking a `provided.al2023` hello-world function by `kernel.Invoke`, on Linux and in the container image (C2).
- Split wave 3. 3a is a thin chain: raw SNS -> SQS -> ESM with batch size 1 -> Lambda -> Logs, plus the e2e trace. 3b deepens it: the filter grammar, FIFO, partial batch failures, retries and DLQ, function URLs.
- Restore platform-dx's risk register (section 15 of that proposal) as a short section of ARCHITECTURE.md, with the wave that retires each risk.

### M3. DynamoDB persistence contradicts itself and is expensive (sections 13, 15; ADRs 0009, 0014)

**Problem.**
- Section 13 imports DynamoDB "into a fresh `-inMemory` Local". Section 15 and ADR 0014 run Local with `-dbPath` under `--persist`, and `--persist` also loads the archive (which contains the DynamoDB export) at startup. Under `--persist` the data either imports twice or conflicts.
- With persistence on, the archive is rewritten "every 30 s while any cell is dirty". That implies a full paginated `Scan` of every table every 30 s. These scans go through the pipeline, so they would fill the journal and could match fault rules.
- `reset` semantics for DynamoDB are unstated.
- Snapshot load needs a fresh Local container (a JVM start), so "load a fixture per test" costs seconds once DynamoDB is involved.

**Recommendation.**
- Under `--persist`, Local's `-dbPath` owns item data and the periodic archive carries only shim state. Full `Scan` export happens only on explicit `snapshot save`.
- Mark snapshot export and import calls as an `Internal{Via: "snapshot"}` origin that fault rules never match and the journal records as one summary event.
- Define DynamoDB `reset` as `DeleteTable` per scope against the running container, not a restart.
- Measure snapshot load time with DynamoDB tables as a tracked number.

### M4. The DynamoDB licence is a release blocker with no fallback (section 15, section 22; ADR 0014)

**Problem.** DynamoDB is part of the target workload (the lock table). ADR 0014 says the licence reading "needs counsel before the first public release". Nothing records who decides, by when, or what happens if the answer is no. The fallback in the plan, "reimplementing DynamoDB", is listed as roadmap. Separately, the default (licence not accepted) means the brief's own workload fails on first run with a 501. That is defensible, but it must be the most visible message understudy prints.

**Recommendation.**
- Add an open-decision entry with an owner and a deadline before wave 4.
- Name the minimal native fallback now: the lock-table subset of `CreateTable`, `PutItem`, `GetItem`, `DeleteItem`, `UpdateItem` and `Query` with condition expressions, plus TTL. Keep the `RawHandler` seam so either backend fits behind the same generated API.
- Make the 501 text, the banner line and `understudy env` output all show the exact flag and environment variable.

### M5. Fidelity claims depend on recordings with no plan to make them (sections 3.4, 19; brief "Fidelity you can trust")

**Problem.** The evidence model (labels, switches, three numbers) is excellent, but the behavioural ground truth depends on the human-only R01-R18 backlog. Nothing schedules it. Without it, most behaviour in the coverage report will be `inferred` or `third-party-recorded`, which falls short of "fidelity you can trust".

Two smaller gaps:
- platform-dx's weekly canary against the latest SDKs (its risk 2) was dropped. Section 19 keeps only a Terraform-provider canary. Wire-capture fixtures fail only when someone bumps an SDK pin, yet SDK default changes are bug class 1 in `landscape.md` 10.
- The e2e suite runs only on a frozen clock. Most users will run in the default Running mode.

**Recommendation.**
- Add a release gate: R01-R07 (the core workload and Terraform plan stability) recorded by a human before the first public tag. Make the README's fidelity wording derive from the coverage numbers rather than adjectives.
- Restore the scheduled, non-blocking canary for Go v2, boto3, JS v3 and the AWS CLI at latest versions.
- Run the e2e workload in both Frozen and Running modes.

### M6. "Instant, tiny" has no budgets (sections 19, 23; ADR 0001)

**Problem.** The brief promises "one static binary, millisecond startup". The design generates serde, validators, schemas and redaction for every operation of 12 models (ADR 0003 accepts that "binary size scale[s] with the whole model"). It also keeps a 200,000-event, 256 MiB journal ring by default. No gate measures startup time, binary size, image size or idle memory. ADR 0007's per-call benchmark is the only performance gate.

**Recommendation.**
- Add numeric budgets to section 19's gates and check them in CI from wave 1, for example:
  - time to a `/health` ready response
  - stripped binary size
  - image size
  - idle RSS
  - per-call pipeline overhead
- If a budget is breached, the per-service closure pruning ADR 0001 already names is the remedy.
- Size the default journal ring to the memory budget.

### M7. Snapshot migrations are required before anyone has a snapshot (section 13; ADR 0009)

**Problem.** "Changing a struct means bumping it and adding a migration with a golden fixture for every past version." During phase 1, a dozen services are built by parallel agents, so state structs change constantly. Each change would carry migration and fixture work for formats no user holds. That is a large, avoidable drag on delivery velocity.

**Recommendation.**
- Keep `stateVersion` and the newer-version refusal.
- Make migrations mandatory only from the first public release. Before that, the format is `0`, and archives from a different build are refused with a clear message.
- Record this in ADR 0009.

## Minor

### m1. DynamoDB case-sensitive table-name escaping undermines the raw passthrough (section 15; ADR 0014)

Mapping every table name to an escaped internal name means rewriting table names in requests and responses everywhere they appear: `TableName`, `RequestItems` keys in batch and transact calls, ARNs and error text. Almost no request can then be forwarded byte-for-byte. That contradicts the reason ADR 0014 chose raw forwarding, and it multiplies the rewrite surface. Tables whose names differ only by case are rare. Drop the escaping for phase 1 and list it as `aws-differs: dynamodb-local` in coverage.

### m2. SNS outbound HTTP delivery is outside the brief's scope (section 16; ADR 0015)

The brief's SNS row names SQS and Lambda delivery. Keep the cheap parts (`Subscribe` stays pending, the token is readable, `ConfirmSubscription` works). Defer the allow-listed outbound POST engine to phase 2.

### m3. Journal lookups need the keys users actually have (sections 10, 17)

- Users hold an AWS request id (from an SDK error or a log line), not a journal event id. Add `understudy journal --request-id <id>` and `journal trace --request-id`.
- Linking a call to an invocation relies on per-environment minted credentials. Function code written for LocalStack often builds clients with static `test` keys, and then linking and access recording silently fall back to the default account root.
- On Linux bridge the source address of the request identifies the container. Use `External.RemoteAddr` as a second linking signal, and journal an advisory when a call from a function container uses unminted credentials.

### m4. The fault CLI cannot express the brief's match dimensions (section 17)

`faults add` takes only `--service`, `--operation`, the action flags and the trigger flags. The brief names resource, account and region matching, and the design adds origin, phase, `AfterCommit` and `StatusOKError`. Add `--resource`, `--account`, `--region`, `--origin`, `--phase delivery`, `--after-commit` and `--status-ok`, or say plainly that `faults load FILE` is the only way to use them.

### m5. Lambda image pulls: offline failures and CI caching are unspecified (section 14.3)

`CreateFunction` stays `Pending` until the image is present. The design does not say what happens when the pull fails (offline, registry error). Map a failed pull to `State: Failed` with a `StateReasonCode` and message that name the image, so Terraform fails fast instead of waiting for its timeout. Add `understudy images pull [--runtime ...]` so CI can pre-warm and cache images, and so the "offline after first use" promise can be prepared deliberately. Pin runtime image digests per understudy release, as DynamoDB Local's digest is pinned.

### m6. Hot reload and `UpdateFunctionCode` conflict (section 14.4)

The design does not say which code runs when a hot-reload overlay is active and Terraform or the SDK updates the function code. Specify it: either the overlay wins until it is removed and the journal records the shadowed update, or an update clears the overlay.

### m7. The e2e golden output will flake (section 19)

Event ids are global and monotonic, and real containers interleave log delivery, in-function calls and ESM deletes in wall time. "Golden snapshot" and "causal trace tree" comparisons over ids will differ from run to run. Compare a canonical tree: children sorted by kind, operation and resource, with ids replaced by tree positions. Exclude fields that depend on container timing from the golden snapshot.

### m8. The protocol corpus should be a ratchet, not a wave-2 wall (section 6.4, section 23)

"Every corpus suite green" in wave 2 includes 655 expanded restJson1 malformed cases that serve only Lambda. Gate on a checked-in baseline that can only go up, using the skip categories that already exist. That satisfies the brief's "checked against" requirement without blocking the chain.

### m9. The architecture makes no platform statement (section 2, section 20)

The Docker socket on Windows is a named pipe, which the standard library cannot dial. `flock` is Unix-only. State that Windows runs through WSL2 only, so nobody assumes native support.

### m10. Migration from LocalStack is the adoption path (sections 17, 18; ADR 0012)

Port 4566, account `000000000000` and the `hot-reload` bucket already ease migration. Add a short compatibility page as a phase 1 docs deliverable covering:
- accepting `localhost.localstack.cloud` as a configured S3 base domain
- the equivalent of LocalStack's init hooks (`--load-snapshot` and a config file)
- Testcontainers usage of the image (C2)

### m11. Attribution for third-party recorded fixtures (ADR 0016)

Overlays and parity fixtures labelled `third-party-recorded` are derived from LocalStack snapshot files (Apache-2.0). ADR 0016 lists every third-party input except this one. Add it, with a NOTICE entry, before the repository goes public.

## Suggested build order

| Wave | Thin slice | Risk it retires |
|---|---|---|
| 1 | Current spine, plus types and serde for all 12 models with budgets measured (M2, M6); Frozen-mode contract (C1) | generator volume, platform core |
| 2 | Protocol breadth as a corpus ratchet; STS and IAM bootstrap; `aws_sqs_queue` and `aws_s3_bucket` empty-second-plan; container image with `InContainer` tested; Lambda hello-world spike | Terraform read fidelity, distribution, Docker and topology |
| 3a | Raw SNS -> SQS -> ESM -> Lambda -> Logs; e2e in Frozen and Running modes; human macOS run | the causal chain, cross-platform behaviour |
| 3b | Filter grammar, FIFO, partial batch failures, retries and DLQ, function URLs | event semantics depth |
| 4 | KMS, Secrets Manager, EventBridge, CloudWatch; DynamoDB proxy (licence decision due); on-behalf-of access recording with the policy golden test | policy correctness, DynamoDB |
| 5 | S3 checksum and multipart depth, hot reload, persistence, coverage page; R01-R07 recorded before the first public tag | fidelity evidence |
