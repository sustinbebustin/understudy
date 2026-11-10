# ADR 0014: DynamoDB Local as a raw proxy behind the pipeline

Status: accepted, 2026-10-08. The licence reading is settled by the first amendment of 2026-10-14, which removes the licence gate.

## Context

understudy proxies DynamoDB to an understudy-managed DynamoDB Local container, pulled at runtime and never bundled, with shims for what Local lacks. Local's licence is proprietary, forbids bundling, modification and redistribution, and requires an AWS account in good standing; telemetry is disabled only by `-disableTelemetry` or `DDB_LOCAL_TELEMETRY=0` (the literal `0`) (`dynamodb-local.md 2`, `dynamodb-local-terms.md 1, 2`). Local partitions by the credential's access key and region, hard-codes `ddblocal`/`000000000000` in ARNs, drops tags, has no PITR, omits `WarmThroughput` (which hangs Terraform for 2.5 minutes), is case-insensitive on table names, sweeps TTL on the wall clock with a malformed stream record, and answers generic errors differently from real DynamoDB (`dynamodb-local.md 4 to 7`, `error-matrix.md` finding 6).

## Decision

- One `amazon/dynamodb-local` container pinned by digest (`sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab`, 3.3.1), ECR Public as a fallback registry with the same digest, never re-tagged, saved or patched; `-inMemory` by default, `-dbPath` under `--persist`; both telemetry opt-outs; published on `127.0.0.1:0` (only where the host cannot route to container addresses, since the second amendment of 2026-10-14); labeled and memory-bounded; a permanent egress test.
- Licence gate (superseded by the first amendment of 2026-10-14, which removes the gate): the pull happens only after `--accept-dynamodb-local-license` or `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1`; otherwise DynamoDB calls answer 501 with the notice text and the flag name while every other service runs. The CLI never prompts. The README states that understudy is Apache-2.0 and that the DynamoDB service uses a proprietary component with account terms.
- Partitioning by credential rewrite: `Credential=<account>/<date>/<region>/dynamodb/aws4_request`, regions only from understudy's validated table.
- The service binds its operations with `RawHandler`s: the pipeline decodes and validates the typed input (journal, faults, access recorder, Coral-grammar validation and the generic-error checks run before forwarding), then the original request bytes are forwarded and Local's response is written verbatim unless rewriting is needed (ARNs, `awsRegion`, injected `WarmThroughput`, `TableClassSummary`, `SSEDescription`, `TableId`, `message` casing), in which case `x-amz-crc32` is recomputed. Internal callers (the TTL sweeper) reach the proxy through typed `Op`s; the kernel encodes and decodes with the awsjson codec.
- Shim-owned state per `(account, region, table)`: tags, TTL spec (never forwarded, so Local's sweeper stays off), PITR, table class, SSE, table id; `UpdateTable` with only shim fields answered locally; `DeleteTable` reports `DELETING`.
- TTL sweep on the emulator clock: `Scan` with `#ttl <= :now` following `LastEvaluatedKey`, `DeleteItem` conditioned on still-expired; admin hook to run it now; TTL deletes tagged for stream `userIdentity`.
- Case-sensitive table identity: a stable escaped internal name per table, rewritten in both directions. (Withdrawn by the amendment of 2026-10-08: names are forwarded verbatim.)
- Known gaps are coverage entries marked `aws-differs: dynamodb-local`.

## Alternatives considered

- Reimplementing DynamoDB: out of scope, and a roadmap option if AWS withdraws the image.
- One Local container per `(account, region)`: unnecessary; credential rewrite gives native isolation.
- Decoding and re-encoding every request and response through generated types: loses byte identity and the pass-through checksum, and any lenient-decode drop changes semantics; typed rewriting is used only where a rewrite is needed.
- `-sharedDb` with table-name mapping: loses Local's native isolation for no gain.
- An interactive licence prompt: a CLI that never prompts is simpler for agents and CI.

## Consequences

- understudy otherwise needs no AWS account; the DynamoDB service is the stated exception, since Local's terms require one.
- A DynamoDB failure (image withdrawn, or the service turned off) leaves every other service working.
- Snapshots of DynamoDB are replay dumps through the proxy (ADR 0009).

## Amendment 2026-10-08: verbatim table names, translated messages, shim timestamps and persistence

- Table names are forwarded verbatim; the escaped-name mapping is withdrawn. The shim rejects a `CreateTable` whose name collides case-insensitively with an existing table in the same scope with `ResourceInUseException`, listed in coverage as `aws-differs: dynamodb-local`. Passthrough stays byte-exact for every operation, PartiQL included.
- Local's message texts that clients match are translated through a small table before `x-amz-crc32` is recomputed, starting with `ResourceNotFoundException` ("Cannot do operations on a non-existent table" becomes AWS's "Requested resource not found: Table: <name> not found"), which Terraform keys on for `DeleteTable`.
- Local's wall-clock timestamps (`CreationDateTime`, `LastUpdateToPayPerRequestDateTime`, stream `ApproximateCreationDateTime`) are replaced from instants the shim stores on the emulator clock. Stream labels are Local's and change across a snapshot load; listed as a deviation. (Superseded by the amendment of 2026-10-26: understudy mints the labels and they survive a load.)
- Persistence: under `--persist` Local runs with `-dbPath` and owns item data; explicit `snapshot save` scans items; `snapshot load` and `reset` delete and recreate tables in the running container (ADR 0009). Export, import and reset run outside the pipeline against Local, never match fault rules and write no journal events of their own. The admin action that triggered them is journaled once by the admin handler (`AdminAction`; ADR 0007, amendment 2026-10-13), which replaced the per-service summary event first planned here.
- When the topology is `InContainer`, Local attaches to understudy's network and is addressed by container IP.
- The config state is `Disabled | AwaitingLicence | Accepted{Digest}`; the 501 text, the startup banner and `understudy env` all print the exact flag and environment variable. (Superseded by the first amendment of 2026-10-14: the state is `Enabled | Disabled`.)
- The licence reading was left open here for counsel before the first release; the first amendment of 2026-10-14 settles it. If AWS objects, the fallback is a native lock-table subset (`CreateTable`, `PutItem`, `GetItem`, `DeleteItem`, `UpdateItem`, `Query` with condition expressions, TTL) behind the same generated API and `RawHandler` seam. It is not built.

## Amendment 2026-10-11: Local at its bridge address until ports can be published; buffered raw bodies

- Until `internal/docker` carries port bindings (`Config.ExposedPorts`, `HostConfig.PortBindings`, `NetworkSettings.Ports`), the Manager reaches DynamoDB Local at its default-bridge address instead of a port published on `127.0.0.1:0`. That address is routable from a Linux host only, so DynamoDB is unsupported under Docker Desktop until then; `ddblocal.NotReadyError` says so. The second amendment of 2026-10-14 closes this.
- The edge buffers a client's body for every operation bound with `kernel.BindRaw` and passes it as the `FromClient` wire, so the passthrough is byte-exact through the front door, as the amendment of 2026-10-08 requires.

## Amendment 2026-10-14: the licence gate is removed

The owner settled the licence questions without counsel, on the precedent and reading below: whether understudy may pull and proxy DynamoDB Local for its users, and whether the project's CI may run it.

- **Proxying DynamoDB Local.** understudy keeps pulling and proxying DynamoDB Local. AWS's own DynamoDB MCP server, testcontainers and AWS Powertools start the same image for their users with no licence gate. No AWS objection to tools that fetch AWS software for a user was found. The account and use conditions bind the user, who is the licensee. understudy is an HTTP client of an unmodified program on the user's own Docker daemon.
- **The licence gate is removed.** This supersedes the "Licence gate" decision and the `Disabled | AwaitingLicence | Accepted{Digest}` state of the 2026-10-08 amendment. DynamoDB runs by default and starts on first use. When DynamoDB Local first starts, understudy logs one line: DynamoDB is served by Amazon DynamoDB Local under Amazon's licence terms, with the licence URL (`ddblocal.Notice`). The configuration state is `Enabled | Disabled`. The existing `dynamodb.enabled: false` file key is the off switch, and its 501 names that setting. `--accept-dynamodb-local-license`, `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE` and the file key `acceptDynamodbLocalLicense` are removed, not deprecated. No release ever shipped them, and understudy refuses any unknown flag, variable or key, so a stale one fails loudly and names itself. Acceptance records written under `<data>/dynamodb-local/licence-accepted/` are no longer read.
- **CI.** The project's CI runs DynamoDB Local in `test-docker` and in every conformance suite, including on fork PRs. The trigger stays `pull_request`, never `pull_request_target`, so fork runs get no secrets and a read-only token. The repository owner, who holds an AWS account in good standing, is the licensee for the workflow. If that account lapses, CI sets `CONFORMANCE_DYNAMODB=0` again until it is restored.
- **Constraints that keep this reading.**
  - Never bundle, mirror, re-tag, `docker save`, push or patch the image, and never touch Local's files or classes. Pull it by the pinned digest only.
  - Never put the image in `actions/cache` or any registry, and never upload Local's files as CI artifacts.
  - Never offer understudy as a hosted multi-user service with Local behind it.
  - Keep both telemetry opt-outs and the egress test.
- **If AWS objects.** On any request from AWS, a patch release turns DynamoDB off by default and the native lock-table fallback gets built.

## Amendment 2026-10-14: Local's port is published where the host cannot route to containers

This closes the deviation of the 2026-10-11 amendment. `internal/docker` carries `Config.ExposedPorts`, `HostConfig.PortBindings` and `NetworkSettings.Ports`, written in-repo because the dependency policy kept the Docker client in-repo (ADR 0016, amendment 2026-10-14).

- Where the host cannot route to container addresses (Docker Desktop, rootless Docker, rootless Podman), the Manager publishes `8000/tcp` on `127.0.0.1` with host port `0` and reaches Local at the host port the daemon reports. DynamoDB works under Docker Desktop: on the macOS Docker Desktop run of 2026-11-06 (Docker Desktop 4.98.0), `serve` logged `DynamoDB Local is ready` at `http://127.0.0.1:<ephemeral port>` and `ListTables` answered from the host.
- On a Linux bridge and rootful Podman the Manager keeps reaching Local at its default-bridge address and publishes no port. In the `InContainer` topology Local still attaches to understudy's network and is addressed by container IP.
- `ddblocal.Network` is `DefaultBridge | Published{HostIP} | Attached`; the server picks one from the detected topology.
- On a remote daemon reached over `tcp://` (topology `Remote`; ADR 0011, amendment 2026-10-14) Local is published on the daemon's own address rather than loopback, which is the daemon host's loopback there, and reached at that address and the read-back host port. The port is open to the daemon's network; for docker:dind that is the CI job's network.

## Amendment 2026-10-26: owned stream log

DynamoDB Streams is not proxied. Local's Streams API refuses ARNs rewritten to the caller's scope, expires iterators and keeps retention on the wall clock, numbers records with one counter shared by every table, and mints UUID event ids (`dynamodb-streams.md` 8.3 rows 1-9). Proxying it would break the one-clock rule and seeded ids (ADR 0006, ADR 0018) and lose stream contents on every snapshot load (`dynamodb-streams.md` 11.1, rejected alternative). This supersedes the 2026-10-08 amendment's sentence "Stream labels are Local's and change across a snapshot load; listed as a deviation", and makes the TTL decision's tagging concrete.

- **Owned log.** `service/dynamodb` keeps a stream log per `(account, region, table)` beside its table shim state, in DynamoDB's snapshot section, and serves the four DynamoDB Streams operations from it as a second registry service on the signing name `dynamodb` (ADR 0019), wired with the built DynamoDB service. Local supplies change data (keys, images, `SizeBytes`, `eventName`) and nothing else.
- **Labels on the emulator clock.** A stream's label is minted at the CreateTable or UpdateTable admission instant on the emulator clock as `yyyy-MM-ddTHH:mm:ss.SSS` UTC, plus 1 ms on a same-table collision. `LatestStreamArn` and `LatestStreamLabel` are rewritten from Local's to understudy's in CreateTable, UpdateTable and DescribeTable, and the label survives snapshot load and reset (`dynamodb-streams.md` 4.1, 11.1 items 6-7).
- **The drain.** After every forwarded write to a stream-enabled table (PutItem, UpdateItem, DeleteItem, BatchWriteItem, TransactWriteItems, the PartiQL writes, the TTL sweeper's DeleteItem) the shim drains Local's shard from its saved position before answering, holding that table's stream lock across forward and drain; multi-table writes lock in sorted table order, and PartiQL writes lock every stream-enabled table of the scope. Each record takes the write's admission instant, a per-shard sequence number and a seeded `eventID`. A failed drain keeps its instant, is retried at the next write or on an emulator-clock timer, and is journaled; nothing is dropped or invented. A snapshot import moves the saved position to Local's tip, so the import's own writes emit nothing (`dynamodb-streams.md` 11.1 items 2-3).
- **TTL `userIdentity`.** A record carries `userIdentity {PrincipalId: dynamodb.amazonaws.com, Type: Service}` only when its write's Origin is `Internal{Via: DynamoDBTTL}`, the sweeper's (`dynamodb-streams.md` 7.5). Local's own TTL stays off.
- **Single shard.** Each stream has one shard, which closes on disable and never splits or rolls over, as in Local; listed in coverage as `aws-differs` (`dynamodb-streams.md` 5.6).
- Iterators, trimming at 24 hours and deletion 24 hours after the table run on the emulator clock (`dynamodb-streams.md` 4.4, 5.4, 5.5).
