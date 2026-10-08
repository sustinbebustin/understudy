# ADR 0018: Seeded identifier streams and the determinism contract

Status: accepted, 2026-10-08.

## Context

The brief wants fault injection runs to be deterministic under a seed and snapshots to be deterministic files. Message ids, receipt handles, request ids, upload ids, version ids, key ids and minted access keys all appear in journals, snapshots and client-visible responses. The stdlib `uuid` package draws from `crypto/rand` and cannot be seeded at runtime, and `uuid.NewV7` follows the `synctest` bubble clock, not the emulator clock (`go-toolchain.md 9.1`). KMS key material must never be predictable.

## Decision

- `internal/ids` owns every generated identifier. The default source is `crypto/rand`. With `--seed N`, each generator kind (request id, SQS message id, receipt handle, SNS message id, Lambda request id, ESM UUID, upload id, version id, KMS key id, IAM `AROA`/`ANPA`/`AIDA` ids, minted access keys, fault streams, delivery jitter) draws from its own ChaCha8 stream derived from `(seed, kind)`. UUIDs are formatted from the stream. Stream positions are recorded in the snapshot manifest and restored on load.
- KMS key material and SNS signing keys always come from `crypto/rand`, even when seeded.
- Map iteration for any output, wire or file, always sorts; snapshots use deterministic JSON.
- Timer firing order is `(due, seq)` (ADR 0006); fan-out order is creation order (ADR 0015).
- The contract, stated in the docs: same seed, same rule set and same call order give the same journal, the same fault hits and the same snapshot bytes. Concurrent clients make call order nondeterministic; the journal shows what happened either way.

## Alternatives considered

- One global seeded stream: a new call site anywhere would shift every id after it; per-kind streams localize the effect.
- `testing/cryptotest.SetGlobalRandom`: test-only and not usable in parallel tests; it cannot seed a running emulator.
- Seeding everything including key material: predictable ciphertexts would make a seeded emulator a worse place to test crypto paths than a real one.

## Consequences

- The seed is logged at startup and recorded in snapshots.
- Every generator is a thin typed wrapper over `ids.Source`, so no package calls `crypto/rand` or `uuid` directly except `ids`, `kms` and `sns` (lint-enforced).

## Amendments (review, 2026-10-08)

The contract is narrowed (engineering M12, fidelity F14, product m7). A single client still fans out into concurrent internal work, Lambda runs on wall time and DynamoDB Local stamps its own timestamps, so "byte-identical journals and snapshots" was overstated. What holds, and what the e2e suite and `understudytest` assert:

- Same seed, same rule set and same call order give the same fault hits.
- Snapshot state sections are byte-identical except a named, closed set of wall-domain fields (STS `Expiration`, Lambda REPORT durations, DynamoDB Local timestamps and stream labels); `snapshot diff --normalize` masks them.
- Journals are equal as canonical trace trees: children sorted by kind, operation and resource, ids replaced by tree positions, `Wall` and `Latency` ignored. Golden fixtures never contain raw event ids.
- SSE-KMS ETags and every other emulator-minted identifier come from the seeded source; only KMS key material and SNS signing keys do not.
- A `--deterministic` single-worker mode is not built in phase 1; option (a) of the engineering review was taken.
