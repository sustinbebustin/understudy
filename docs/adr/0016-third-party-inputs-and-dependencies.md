# ADR 0016: Third-party inputs and dependencies

Status: accepted, 2026-10-08.

## Context

The main module depends on the standard library only, with `pgregory.net/rapid` pre-approved for tests; any other dependency needs an ADR. The generator needs the AWS Smithy models, the protocol compliance corpus (published only as IDL inside jars, convertible with the Smithy CLI, `smithy-protocol-tests.md 5`), and an operation-to-IAM-action mapping (the AWS Service Reference JSON, whose licence is unstated, `smithy-models.md 8`). S3's ten checksum algorithms include XXHASH64, XXHASH3 and XXHASH128, which the standard library lacks; their wire form is settled (`gap-G8.md`).

## Decision

- `third_party/api-models-aws/`: the 12 phase 1 model files verbatim at commit `1cfdfb4bb9ea858e65623e66d73af04bec353201` with `UPSTREAM.json` (git blob SHA-1 and sha256 per file), LICENSE and NOTICE. `internal/cmd/vendor` fetches by hand and verifies offline in CI. Pins bump deliberately with a coverage-diff review.
- `third_party/smithy-protocol-tests/model.json`: one flattened JSON AST produced by a human with Smithy CLI 1.74.0 (`smithy ast --no-config --flatten` over the six jars), with `PIN.json` recording the Smithy version and the jar and CLI sha256 values. No Java on the test path; no IDL parser.
- `third_party/aws-service-reference/actions.json`: a derived table of `Operations[].AuthorizedActions` and `Actions[].Annotations` for the 12 services with provenance (service `Version`, `modified`, body sha256). The raw JSON is not vendored until its licence is confirmed.
- `pgregory.net/rapid` v1.3.0 (MPL-2.0, file-level copyleft, test-only) is the only non-stdlib import; `depguard` enforces it. Committed `testdata/rapid/**` failfiles are regression seeds.
- XXHASH64, XXHASH3 and XXHASH128 are ported in `internal/checksum` from the xxHash specification (seed 0, default 192-byte secret, big-endian output, XXH3-128 as `high64 || low64`), validated against the published vectors for every length class, with a streaming-equals-one-shot property. CRC combine for full-object multipart is implemented in-repo (`go-toolchain.md 7.4`).
- The stdlib CBOR subset, XML writer, form codec, SigV4 verifier and Docker Engine client are written in-repo; no library.
- Conformance suites are separate modules with their own manifests and pins (aws-sdk-go-v2, boto3, JS v3, AWS CLI, hashicorp/aws 6.68.0) and run only through `scripts/sealed`.

## Alternatives considered

- A CBOR, XML or Docker SDK dependency: each would need this ADR and would pull transitive code into a static binary the brief wants tiny; the needed subsets are small.
- Returning "unsupported" for the xxHash algorithms: botocore, Java and C++ clients that ask for them would break; the digest form is settled, so the port is the right cost.
- Vendoring the raw Service Reference JSON: licence unverified; the derived facts are what the generator needs.
- A Smithy IDL parser in Go: no maintained 2.0 parser exists; the CLI conversion is deterministic and byte-identical across runs.

## Consequences

- Generation and every test run offline.
- MPL-2.0 is recorded here because the project is Apache-2.0; the dependency never ships in the binary.
- The xxHash port is a maintained artifact with its own vector corpus.

## Amendments (review, 2026-10-08)

- Overlay entries and parity fixtures labelled `third-party-recorded` are derived from LocalStack's snapshot files (Apache-2.0, archived repository). They are a third-party input like the others: `NOTICE` carries the attribution and the pinned commit, and every such overlay entry cites the file and line in its `evidence.source` (product m11).
- `evidence.Label` is one Go type shared by the generator, the fidelity ledger, the protocol-test skip list and the parity replayer (engineering m4).
