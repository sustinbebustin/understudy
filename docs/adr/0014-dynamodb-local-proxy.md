# ADR 0014: DynamoDB Local as a raw proxy behind the pipeline

Status: accepted, 2026-10-08. The licence reading is settled by the amendment of 2026-10-14, which removes the licence gate.

## Context

The brief proxies DynamoDB to an understudy-managed DynamoDB Local container, pulled at runtime and never bundled, with shims for what Local lacks. Local's licence is proprietary, forbids bundling, modification and redistribution, and requires an AWS account in good standing; telemetry is disabled only by `-disableTelemetry` or `DDB_LOCAL_TELEMETRY=0` (the literal `0`) (`dynamodb-local.md 2`, `gap-G5.md`). Local partitions by the credential's access key and region, hard-codes `ddblocal`/`000000000000` in ARNs, drops tags, has no PITR, omits `WarmThroughput` (which hangs Terraform for 2.5 minutes), is case-insensitive on table names, sweeps TTL on the wall clock with a malformed stream record, and answers generic errors differently from real DynamoDB (`dynamodb-local.md 4 to 7`, `error-matrix.md` finding 6).

## Decision

- One `amazon/dynamodb-local` container pinned by digest (`sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab`, 3.3.1), ECR Public as a fallback registry with the same digest, never re-tagged, saved or patched; `-inMemory` by default, `-dbPath` under `--persist`; both telemetry opt-outs; published on `127.0.0.1:0`; labeled and memory-bounded; a permanent egress test.
- Licence gate: the pull happens only after `--accept-dynamodb-local-license` or `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1`; otherwise DynamoDB calls answer 501 with the notice text and the flag name while every other service runs. The CLI never prompts. The README states that understudy is Apache-2.0 and that the DynamoDB service uses a proprietary component with account terms.
- Partitioning by credential rewrite: `Credential=<account>/<date>/<region>/dynamodb/aws4_request`, regions only from understudy's validated table.
- The service binds its operations with `RawHandler`s: the pipeline decodes and validates the typed input (journal, faults, access recorder, Coral-grammar validation and the generic-error checks run before forwarding), then the original request bytes are forwarded and Local's response is written verbatim unless rewriting is needed (ARNs, `awsRegion`, injected `WarmThroughput`, `TableClassSummary`, `SSEDescription`, `TableId`, `message` casing), in which case `x-amz-crc32` is recomputed. Internal callers (the TTL sweeper) reach the proxy through typed `Op`s; the kernel encodes and decodes with the awsjson codec.
- Shim-owned state per `(account, region, table)`: tags, TTL spec (never forwarded, so Local's sweeper stays off), PITR, table class, SSE, table id; `UpdateTable` with only shim fields answered locally; `DeleteTable` reports `DELETING`.
- TTL sweep on the emulator clock: `Scan` with `#ttl <= :now` following `LastEvaluatedKey`, `DeleteItem` conditioned on still-expired; admin hook to run it now; TTL deletes tagged for stream `userIdentity`.
- Case-sensitive table identity: a stable escaped internal name per table, rewritten in both directions.
- Known gaps are coverage entries marked `aws-differs: dynamodb-local`.

## Alternatives considered

- Reimplementing DynamoDB: out of scope for phase 1 and a roadmap option if AWS withdraws the image.
- One Local container per `(account, region)`: unnecessary; credential rewrite gives native isolation.
- Decoding and re-encoding every request and response through generated types: loses byte identity and the pass-through checksum, and any lenient-decode drop changes semantics; typed rewriting is used only where a rewrite is needed.
- `-sharedDb` with table-name mapping: loses Local's native isolation for no gain.
- An interactive licence prompt: a CLI that never prompts is simpler for agents and CI.

## Consequences

- "No account" in the brief gets a stated exception for the DynamoDB service.
- A DynamoDB failure (image withdrawn, licence declined) leaves every other service working.
- Snapshots of DynamoDB are replay dumps through the proxy (ADR 0009).

## Amendments (review, 2026-10-08)

- Table names are forwarded verbatim; the escaped-name mapping is withdrawn (fidelity F10, product m1). The shim rejects a `CreateTable` whose name collides case-insensitively with an existing table in the same scope with `ResourceInUseException`, listed in coverage as `aws-differs: dynamodb-local`. Passthrough stays byte-exact for every operation, PartiQL included.
- Local's message texts that clients match are translated through a small table before `x-amz-crc32` is recomputed, starting with `ResourceNotFoundException` ("Cannot do operations on a non-existent table" becomes AWS's "Requested resource not found: Table: <name> not found"), which Terraform keys on for `DeleteTable`.
- Local's wall-clock timestamps (`CreationDateTime`, `LastUpdateToPayPerRequestDateTime`, stream `ApproximateCreationDateTime`) are replaced from instants the shim stores on the emulator clock (fidelity F11). Stream labels are Local's and change across a snapshot load; listed as a deviation.
- Persistence: under `--persist` Local runs with `-dbPath` and owns item data; explicit `snapshot save` scans items; `snapshot load` and `reset` delete and recreate tables in the running container (ADR 0009). Export, import and reset run outside the pipeline against Local, never match fault rules and write no journal events of their own; the admin action that triggered them is journaled once by the admin handler (ADR 0007 pre-tag review amendment, `AdminAction`), which supersedes the per-service summary event (pre-tag review, 2026-10-13).
- When the topology is `InContainer`, Local attaches to understudy's network and is addressed by container IP (product C2).
- The config state is `Disabled | AwaitingLicence | Accepted{Digest}`; the 501 text, the startup banner and `understudy env` all print the exact flag and environment variable (engineering m15, product M4).
- Open decision: the licence reading needs counsel before the first public tag (architecture 25.2). If the answer is no, the fallback is a native lock-table subset (`CreateTable`, `PutItem`, `GetItem`, `DeleteItem`, `UpdateItem`, `Query` with condition expressions, TTL) behind the same generated API and `RawHandler` seam; it is not built in phase 1.

## Amendment (wave 6, 2026-10-11)

- Until `internal/docker` carries port bindings (`Config.ExposedPorts`, `HostConfig.PortBindings`, `NetworkSettings.Ports`), the Manager reaches DynamoDB Local at its default-bridge address instead of a port published on `127.0.0.1:0`. That address is routable from a Linux host only, so DynamoDB is unsupported under Docker Desktop until then; `ddblocal.NotReadyError` says so, and `docs/plan/DEFERRED.md` (wave 6) tracks the fix.
- The edge buffers a client's body for every operation bound with `kernel.BindRaw` and passes it as the `FromClient` wire, so the passthrough is byte-exact through the front door as the amendment above requires.

## Amendment (licence, 2026-10-14)

The owner settled the licence questions without counsel on the precedent and reading in [licensing-precedent.md](../research/phase1/licensing-precedent.md), sections 1 and 2. That closes ARCHITECTURE.md 25.2 and 25.6.

- **25.2, closed.** understudy keeps pulling and proxying DynamoDB Local. AWS's own DynamoDB MCP server, testcontainers and AWS Powertools start the same image for their users with no licence gate. No AWS objection to tools that fetch AWS software for a user was found. The account and use conditions bind the user, who is the licensee. understudy is an HTTP client of an unmodified program on the user's own Docker daemon.
- **The licence gate is removed.** This supersedes the "Licence gate" decision and the `Disabled | AwaitingLicence | Accepted{Digest}` state of the review amendment. DynamoDB runs by default and starts on first use. When DynamoDB Local first starts, understudy logs one line: DynamoDB is served by Amazon DynamoDB Local under Amazon's licence terms, with the licence URL (`ddblocal.Notice`). The configuration state is `Enabled | Disabled`. The existing `dynamodb.enabled: false` file key is the off switch, and its 501 names that setting. `--accept-dynamodb-local-license`, `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE` and the file key `acceptDynamodbLocalLicense` are removed, not deprecated. No release ever shipped them, and understudy refuses any unknown flag, variable or key, so a stale one fails loudly and names itself. Acceptance records written under `<data>/dynamodb-local/licence-accepted/` are no longer read.
- **25.6, closed.** The project's CI runs DynamoDB Local in `test-docker` and in every conformance suite, including on fork PRs. The trigger stays `pull_request`, never `pull_request_target`, so fork runs get no secrets and a read-only token. The repository owner, who holds an AWS account in good standing, is the licensee for the workflow. If that account lapses, CI sets `CONFORMANCE_DYNAMODB=0` again until it is restored.
- **Constraints that keep this reading.**
  - Never bundle, mirror, re-tag, `docker save`, push or patch the image, and never touch Local's files or classes. Pull it by the pinned digest only.
  - Never put the image in `actions/cache` or any registry, and never upload Local's files as CI artifacts.
  - Never offer understudy as a hosted multi-user service with Local behind it.
  - Keep both telemetry opt-outs and the egress test.
- **If AWS objects.** On any request from AWS, a patch release turns DynamoDB off by default and the native lock-table fallback gets built.

## Amendment (dependency policy, 2026-10-14)

This closes the wave 6 amendment's deviation. `internal/docker` carries `Config.ExposedPorts`, `HostConfig.PortBindings` and `NetworkSettings.Ports`, written in-repo under the dependency review that kept the Docker client.

- Where the host cannot route to container addresses (Docker Desktop, rootless Docker, rootless Podman), the Manager publishes `8000/tcp` on `127.0.0.1` with host port `0` and reaches Local at the host port the daemon reports. DynamoDB works under Docker Desktop.
- On a Linux bridge and rootful Podman the Manager keeps reaching Local at its default-bridge address and publishes no port. In the `InContainer` topology Local still attaches to understudy's network and is addressed by container IP.
- `ddblocal.Network` is `DefaultBridge | Published{HostIP} | Attached`; the server picks one from the detected topology.
- On a remote daemon reached over `tcp://` (topology `Remote`, ADR 0011 dependency policy amendment) Local is published on the daemon's own address rather than loopback, which is the daemon host's loopback there, and reached at that address and the read-back host port. The port is open to the daemon's network; for docker:dind that is the CI job's network.
