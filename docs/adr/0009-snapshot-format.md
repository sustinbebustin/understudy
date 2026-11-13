# ADR 0009: Snapshot format and opt-in persistence

Status: accepted, 2026-10-08.

## Context

understudy needs a versioned, deterministic file for the whole emulator state and opt-in persistence across restarts. LocalStack pickled object graphs and broke compatibility across releases; MiniStack restored event source mappings that were listed enabled but never polled, and added a downgrade guard that refuses files from newer binaries. DynamoDB Local's SQLite files cannot be copied safely while running (`dynamodb-local.md` Implications 11).

## Decision

- One uncompressed POSIX tar (`archive/tar`), entries sorted, mtime 0, uid and gid 0, mode 0644, optional gzip with an empty name and zero mtime. Same state and understudy version give the same bytes.
- Layout: `manifest.json` (format version, writer version, model pin, clock mode and time, seed and per-kind stream positions, next journal id, section list with schema versions and sha256), `faults.json`, one canonical JSON document per cell under `services/<svc>/<account>/<region>.json` (S3: `index.json` plus `buckets/<bucket>.json`), DynamoDB shim state plus per-table NDJSON item exports, content-addressed `blobs/sha256/<hex>`, optional `journal.ndjson`.
- The state structs are the schema: every persisted field carries a `json` tag, each service declares `const stateVersion = N`, a change bumps it and adds a migration with a golden fixture for every past version. A newer format or section schema is refused with a message naming both versions. (The amendment of 2026-10-08 keeps the format at `0`, with no migrations, until the first release.)
- Timers are never stored. Every service implements `Rearm(now)` that rebuilds visibility expiries, pollers, `nextFireAt`, TTL sweeps, deletion windows and pending deliveries from state. A restart test asserts every re-armable thing fires after load.
- Load is reset-then-import under the gate's write side; save waits up to 5 s (wall) for in-flight deliveries and names what was not captured. (Reworked by the amendments of 2026-10-08 and 2026-10-13: save is two-phase, and the wait is for pending effects.)
- DynamoDB is exported through the proxy (`DescribeTable` plus paginated `Scan`) and imported into a fresh `-inMemory` Local with `CreateTable` and `BatchWriteItem`. (Superseded by the amendment of 2026-10-08: tables are recreated in the running container.)
- Persistence: `--persist <dir>` loads at start, saves atomically (temp file and rename) on graceful shutdown and every 30 s of wall time while dirty; blobs live in `<dir>/blobs` and the archive references them; the directory is `flock`ed. There is no write-ahead log; a crash loses at most 30 s.
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

## Amendment 2026-10-08: two-phase save, format 0 before the first release, DynamoDB persistence

- Save is two-phase: under the gate's write side every `kernel.Stateful` exports to in-memory `store.Section` values and pins its blobs (the consistent cut); the gate is released; `kernel.Late` exporters (DynamoDB items, sent to Local directly, outside the pipeline) run; then the tar is written. Load mirrors it: `Late` imports first, then reset-then-import under the gate, then `Rearm`. Nothing does I/O or runs the pipeline under the gate. `Stateful.Export` takes no writer.
- Until the first tagged release the archive `format` is `0` and a section whose version differs from the running binary's is refused with a message naming both versions and the writing binary; migrations with golden fixtures become mandatory from the first release (superseded by the amendment of 2026-11-10: from 1.0). The newer-format refusal stays.
- Sealed sums in state serialize through one tagged-union convention in `jsonx` (`{"kind": "<variant>", ...}`) with a per-sum table and a rapid round-trip property per sum.
- Accepted deliveries are outbox state (ADR 0015), so a save never names deliveries as not captured; the 5 s settle wait is gone. (The second amendment of 2026-10-13 restores a wait, for pending effects.)
- DynamoDB persistence has one source of truth per mode: under `--persist` Local runs with `-dbPath <dir>/dynamodb` and owns item data while the periodic archive carries shim state only; an explicit `snapshot save` exports items as `Late` sections; `snapshot load` and `reset` delete the tables in the running container and recreate them from the sections. A fresh `-inMemory` container is never started for a load. Local-minted stream labels change across a load and are listed as `aws-differs: dynamodb-local`. (Superseded by the amendment of 2026-10-27: labels survive a load.)
- `snapshot diff --normalize` applies the closed normalizer set of ADR 0018.

## Amendment 2026-10-13: the clock on a restart under `--persist`

- A restart under `--persist` resumes the clock from the checkpoint by mode. A Running clock resumes at wall time, or at its saved instant when that is later, so emulator time never trails wall time and never moves back. The manifest records no offset from wall time, so an advance ahead of wall time that is shorter than the downtime is absorbed. A Frozen clock holds its instant. A Scaled clock resumes from its saved instant, since its time across the downtime is undefined. An explicit `snapshot load` keeps the archive's instant in every mode. Keeping an advanced offset exactly would need the manifest to record the wall time of the cut, a format change. (Superseded for a Running clock by the amendment of 2026-11-11: a checkpoint records the wall time of its cut, and the advance survives.)

## Amendment 2026-10-13: a cut waits for pending effects; load order

- The 2026-10-08 amendment's "the 5 s settle wait is gone" no longer holds. A committed write hands its deliveries to the outbox after the gate is released, so a cut taken in between would hold the write's state change without its delivery. Before taking the gate, a cut waits up to 5 s of wall time (`snapshot.EffectsWait`) for the kernel's count of committed writes' effects (wakes and deliveries) to reach zero, then cuts. A cut taken at the timeout reports the count (`Cut.PendingEffects`): `snapshot save` returns it as `pendingEffects` and logs it at Warn, and a `--persist` checkpoint logs it at Warn. The manifest does not carry it, so the archive format is unchanged.
- Load restores the clock first, then, under the gate, the rest of the runtime head (identifier positions, the journal counter, fault rules), after the export that collects the blobs being replaced and before the reset. If that export fails, nothing under the gate has changed.

## Amendment 2026-10-27: DynamoDB Streams and Lambda stream mappings in state

- DynamoDB's state version 4 carries each scope's streams (label, view type, status, the deleted table's delete instant, the shard with its untrimmed records, the feed's place in Local's stream) and the scope's iterator key. A load maps each re-created table's new Local stream to the restored stream at the new stream's tip, so labels, records and iterators issued before the save survive and the load's own writes emit nothing. The 2026-10-08 amendment's "Local-minted stream labels change across a load" no longer holds.
- Exception to "refused, not migrated": DynamoDB reads a version 3 section, which is a version 4 section without streams, so snapshots saved before streams were kept still load. A stream-enabled table in one gets a fresh stream labeled at the import instant.
- Lambda's state version 4 saves each stream mapping's checkpoints (one resume position per shard), so a loaded mapping resumes after the last record it processed, and moves an SQS mapping's scaling into its source kind, so a stream mapping cannot hold one. A second exception to "refused, not migrated": Lambda reads a version 3 section (the layer store) by moving each SQS mapping's scaling into its kind; any stream mappings in it, saved without checkpoints, read again from their StartingPosition. Version 1 and 2 sections are still refused.
## Amendment 2026-11-10: format 0 until 1.0

- The amendment of 2026-10-08 made migrations mandatory "from the first release". The first releases (v0.1.0 to v0.1.2) shipped with `snapshot.Format` still `0`, so that rule moves to 1.0: while understudy is pre-1.0 the archive `format` stays `0`, and a section whose version differs from the running binary's is refused with a message naming both versions and the writing binary.
- The exceptions that already migrate (the amendment of 2026-10-27) keep migrating.
- From 1.0 the format becomes `1`, and every state version change adds a migration with a golden fixture for every past version.

## Amendment 2026-11-11: a running clock keeps its advance across a restart

- Supersedes the first amendment of 2026-10-13 for a Running clock, whose advance shorter than the downtime was absorbed. A `--persist` checkpoint records in the manifest the wall instant at which its cut was taken (`cutWall`, optional). A restart resumes a Running clock with it at `wall now + (saved instant - cutWall)`, never earlier than the saved instant, so an advance ahead of wall time survives any downtime. The instant is read just after the cut, so the kept offset errs short by the cut's export time, never ahead.
- An archive without `cutWall` resumes a Running clock as the 2026-10-13 amendment says. Frozen and Scaled clocks resume as before, and an explicit `snapshot load` keeps the archive's instant in every mode.
- An explicit `snapshot save` never records `cutWall`, so its bytes still depend only on state and version (ARCHITECTURE.md 13). The checkpoint digest leaves it out, so an idle server still writes nothing.
- The format stays `0` (the amendment of 2026-11-10); the field is optional, so an archive without it still reads.

## Amendment 2026-11-13: Lambda keeps deleted functions' version numbers

- Lambda's state version 5 adds each scope's retired version numbers: the highest version number published under the name of each deleted function, which a function created again under the name publishes after, as AWS does.
- A third exception to "refused, not migrated": Lambda reads a version 4 section as it is, with no numbers kept for functions deleted before the save, and still migrates a version 3 section. A function in either keeps its own numbering.

## Amendment 2026-11-13: Lambda state version 6 records layer policy history and sizes

- Lambda's state version 6 adds whether a layer version ever had a policy and each layer statement's size as AWS's policy limit measures it. Both fields are optional, so Lambda's `Import` reads a version 5 section, and a version 4 section as the amendment above allows, as version 6, with every layer version as never having had a policy and each statement measured at its rendered length. A version 3 section is still migrated. Like the version 3 migration, this runs only once a section reaches `Import`: the coordinator's schema check still refuses a section whose version differs from the running one.

## Amendment 2026-11-13: Lambda records the kind of an image's digest

- Lambda's state version 7 records with each image function's digest whether it is a repository digest or a local build's image ID, so invoke resolves only that reference and never sends a local build's ID to the registry as `repository@<image ID>` (ARCHITECTURE.md 14.3).
- The field is optional, so Lambda's `Import` reads a version 6 section as it is. Its image functions' kinds are unrecorded, and invoke tries both references for them, the repository digest first, as before. Version 5, 4 and 3 sections read as the amendments above say, their image functions' kinds unrecorded too.

## Amendment 2026-11-13: Lambda state version 8 records a stream mapping's self-disable

- Lambda's state version 8 adds whether a stream mapping disabled itself because its stream closed, which sets its `StateTransitionReason` and `LastProcessingResult` (`lambda-stream-sources.md` 7.3). The field is optional, so Lambda's `Import` reads a version 7 section, and a version 6, 5 or 4 section as the amendments above allow, as version 8, with no mapping disabled that way. A version 3 section is still migrated, under the same coordinator limit as above.
