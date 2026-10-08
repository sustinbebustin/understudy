# ADR 0009: Snapshot format v1 and opt-in persistence

Status: accepted, 2026-10-08.

## Context

The brief requires a versioned, deterministic file for the whole emulator state and opt-in persistence across restarts. LocalStack pickled object graphs and broke compatibility across releases; MiniStack restored event source mappings that were listed enabled but never polled, and added a downgrade guard that refuses files from newer binaries (`landscape.md 8`). DynamoDB Local's SQLite files cannot be copied safely while running (`dynamodb-local.md` Implications 11).

## Decision

- One uncompressed POSIX tar (`archive/tar`), entries sorted, mtime 0, uid and gid 0, mode 0644, optional gzip with an empty name and zero mtime. Same state and understudy version give the same bytes.
- Layout: `manifest.json` (format version, writer version, model pin, clock mode and time, seed and per-kind stream positions, next journal id, section list with schema versions and sha256), `faults.json`, one canonical JSON document per cell under `services/<svc>/<account>/<region>.json` (S3: `index.json` plus `buckets/<bucket>.json`), DynamoDB shim state plus per-table NDJSON item exports, content-addressed `blobs/sha256/<hex>`, optional `journal.ndjson`.
- The state structs are the schema: every persisted field carries a `json` tag, each service declares `const stateVersion = N`, a change bumps it and adds a migration with a golden fixture for every past version. A newer format or section schema is refused with a message naming both versions.
- Timers are never stored. Every service implements `Rearm(now)` that rebuilds visibility expiries, pollers, `nextFireAt`, TTL sweeps, deletion windows and pending deliveries from state. A restart test asserts every re-armable thing fires after load.
- Load is reset-then-import under the gate's write side; save waits up to 5 s (wall) for in-flight deliveries and names what was not captured.
- DynamoDB is exported through the proxy (`DescribeTable` plus paginated `Scan`) and imported into a fresh `-inMemory` Local with `CreateTable` and `BatchWriteItem`.
- Persistence: `--persist <dir>` loads at start, saves atomically (temp file and rename) on graceful shutdown and every 30 s of wall time while dirty; blobs live in `<dir>/blobs` and the archive references them; the directory is `flock`ed. No write-ahead log in phase 1; a crash loses at most 30 s.
- `understudy snapshot diff a b` compares archives section by section.

## Alternatives considered

- A separate DTO layer per service: doubles every type while the format is young; per-struct versions with migrations give the same guarantee.
- Serializing scheduler tasks: duplicates state that already implies them and reintroduces the MiniStack bug class when state and timers disagree.
- Copying DynamoDB Local's SQLite files or using `-dbPath` snapshots: copy-while-running safety is unverified and the files are Local's, not ours.
- A write-ahead log: correct for crash safety, but a 30 s loss is honest for a dev tool and the format is versioned so a WAL can be added later.

## Consequences

- Snapshots are committable test fixtures and a regression tool.
- Every service pays a round-trip test and a `Rearm` implementation; `Partitioned` supplies `Export`, `Import` and `Reset`.
- Seed positions and the journal counter in the manifest make a seeded run reproducible after a load.

## Amendments (review, 2026-10-08)

- Save is two-phase (engineering M4): under the gate's write side every `kernel.Stateful` exports to in-memory `store.Section` values and pins its blobs (the consistent cut); the gate is released; `kernel.Late` exporters (DynamoDB items, through the proxy with `Origin Internal{Via: Snapshot}`) run; then the tar is written. Load mirrors it: `Late` imports first, then reset-then-import under the gate, then `Rearm`. Nothing does I/O or runs the pipeline under the gate. `Stateful.Export` takes no writer.
- Until the first tagged release the archive `format` is `0` and a section whose version differs from the running binary's is refused with a message naming both versions and the writing binary; migrations with golden fixtures become mandatory from the first release (engineering M9, product M7). The newer-format refusal stays.
- Sealed sums in state serialize through one tagged-union convention in `jsonx` (`{"kind": "<variant>", ...}`) with a per-sum table and a rapid round-trip property per sum.
- Accepted deliveries are outbox state (ADR 0015), so a save never names deliveries as not captured; the 5 s settle wait is gone.
- DynamoDB persistence has one source of truth per mode (fidelity F11, product M3): under `--persist` Local runs with `-dbPath <dir>/dynamodb` and owns item data while the periodic archive carries shim state only; an explicit `snapshot save` exports items as `Late` sections; `snapshot load` and `reset` delete the tables in the running container and recreate them from the sections. A fresh `-inMemory` container is never started for a load. Local-minted stream labels change across a load and are listed as `aws-differs: dynamodb-local`.
- `snapshot diff --normalize` applies the closed normalizer set of ADR 0018.
