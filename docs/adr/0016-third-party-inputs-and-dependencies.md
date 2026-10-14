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

## Amendments (dependency policy, 2026-10-14)

The "standard library only" rule is replaced by an allow-list with written criteria. This supersedes the Context's first sentence, the Decision's "`pgregory.net/rapid` ... is the only non-stdlib import" and "written in-repo; no library" bullets, and the first alternative's reason: measured on the release binary, the candidates add little (the Moby client 0.8 MB, about 2 percent; cobra 0.3 MB; bbolt 0.25 MB). The reason the hand-written code is right is fidelity, which rule 1 states. `depguard`'s two allow-lists in `.golangci.yml` mirror the table's two tiers.

1. Behaviour that is AWS's behaviour is implemented in-repo: wire shapes and serde, error identities and texts, check order, signature parsing and verification, pattern, filter and schedule grammars, IAM document rules and evaluation, checksum wire forms, the Lambda Runtime API. A library may serve as a test oracle for such code, never as its implementation. A library cannot be more faithful than its author's goal, and none of these authors' goal was to answer like AWS.
2. A runtime dependency may be admitted when all of these hold; passing admits nothing by itself, the table row must say what the library does that the repo should not own. It does none of the work in rule 1. It is CGO-free. Its licence is Apache-2.0, MIT or BSD. It is at a stable major version with a written compatibility statement, or a v0 with years of unchanged API and a single purpose. The modules it links into the binary, measured with `go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}'` on a probe build, are countable on one hand and come from the library's own organisation or golang.org/x, with no telemetry, logging or TLS framework among them. Its marginal cost on the release binary and on `serve` start is measured by adding a probe file to a copy of the repo and building with the release flags (`CGO_ENABLED=0 -trimpath -ldflags='-s -w'`), and recorded. Third-party assembly, especially runtime-dispatched, is noted in the row as a review point.
3. A test-only dependency needs the licence, stability and linked-module count checks only (rule 2's organisation clause does not apply); MPL-2.0 file-level copyleft is acceptable test-only (rapid). A differential oracle is admitted only when its output is fixed by a frozen specification, so the test cannot drift with the oracle's releases.
4. golang.org/x modules go through rule 2 like any other; they are not admitted by default.
5. `encoding/json/v2` and `encoding/json/jsontext` are standard library in Go 1.27 and are the JSON codec; no third-party JSON library replaces them.
6. Every admitted dependency is one row in the table below: module, pinned version, tier, reason, linked modules, measured cost, trigger if pre-qualified. Candidates refused in the 2026-10-14 review are listed below with their one-line reason so they are not re-litigated. No per-dependency ADR.

Admitted. The runtime tier is empty.

| Module | Version | Tier | Reason | Linked modules | Cost |
|---|---|---|---|---|---|
| pgregory.net/rapid | v1.3.0 | test | property testing | 1 | none (test) |
| github.com/google/go-cmp | v0.7.0 | test | struct diffs in failures | 1 | none (test) |
| github.com/cespare/xxhash/v2 | v2.3.0 | test | XXH64 differential oracle; output frozen | 1 | none (test) |
| github.com/zeebo/xxh3 | v1.1.0 | test | XXH3-64/128 differential oracle; output frozen at xxHash 0.8.0; cpuid-dispatched assembly, test-only | 2 (xxh3, klauspost/cpuid/v2; x/sys is indirect in go.mod, not linked) | none (test) |

Pre-qualified on trigger; the gate is re-run against the then-current version at adoption:

- `github.com/spf13/cobra` + `github.com/spf13/pflag`, when shell completion is wanted. Evaluated at v1.10.2 / v1.0.10: 2 linked modules, +0.29 MB. Needs a pre-scan so `-5s` stays a positional.
- `go.etcd.io/bbolt`, when the persistence write-ahead log is scheduled, beside the snapshot archive, not instead of it. Evaluated at v1.5.0: 2 linked modules, +0.25 MB.

Refused:

- `github.com/moby/moby/client` v0.6.2 for `internal/docker`: v0 API on a breaking-change treadmill, 19 linked modules with OpenTelemetry imported unconditionally, no Podman or dial-failure remediation.
- urfave/cli v3 and kong for `internal/cli`: urfave is +2.81 MB and ships monthly minors; kong is single-maintainer.
- viper and koanf for `internal/config`: viper links 13 modules; both replace the layering and lose source-spelled typo refusal.
- aws-sdk-go-v2 `signer/v4` for `internal/auth`: client-only, never verifies; no SigV2, no signed aws-chunked.
- smithy-go and the SDK serde for `internal/protocol` and `internal/api`: no server-side protocol support; SDK serde is unexported.
- fxamacker/cbor for `internal/protocol/cbor`, as implementation or oracle: a reflection codec whose strictness differs by design; a native fuzz target covers malformed input instead.
- `encoding/xml` and smithy-go `encoding/xml` for `internal/protocol/xmlw`: both replace out-of-range characters with U+FFFD and cannot write S3's `xsi:type` Grantee.
- chi and gorilla/mux for `internal/edge`: wrong layer; they normalise paths S3 must see raw.
- quamina for `internal/ruler`: no numeric, suffix, `$or` or cidr matching and none of AWS's error texts or limits.
- robfig/cron and cronplan/v2 for `internal/schedexpr`: robfig lacks year, `L`, `W` and `#`; cronplan is single-maintainer with two in-major breaks.
- micahhausler/aws-iam-policy for `internal/policydoc`: parse-only v0.x.
- google/uuid for `internal/ids`: the standard library's `uuid` is already used.
- clockwork and benbjohnson/clock for `internal/clock`: test fakes, one archived.
- The Lambda Runtime Interface Emulator for `lambda/runtimeapi`: binary-only, and the defects ADR 0010 records are still in its source.
- testify for tests: brings objx and yaml for assertion sugar and has shipped semantic regressions.
- No standalone aws-chunked decoder or CloudWatch Logs filter library exists to consider.
