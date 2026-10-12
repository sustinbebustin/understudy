# understudy phase 1 implementation plan

Status: definitive, 2026-10-08. Derived from [ARCHITECTURE.md](../design/ARCHITECTURE.md) as amended by the review decisions (section 26) and the ADRs. Every task below implements that document; where a task spec and the architecture disagree, the architecture wins and the task reports the discrepancy.

## Shape of the plan

Nine waves, 45 tasks. Leaves first; then the tracer bullet in two halves (the generator and the awsJson codec, then the kernel, edge and five SQS operations through the real binary); then the generator over all twelve models with the protocol corpus as a ratchet, the remaining codecs and the platform (outbox, admin API); then services in three waves; then the CLI, final wiring, smoke test and image.

| Wave | Goal | Tasks |
|---|---|---|
| 1 | scaffold | T01 |
| 2 | leaf packages with no repo dependencies | T02 to T09 |
| 3 | tracer bullet, first half: generator over SQS, awsJson codec, and the leaf-plus-one packages (identity, fault, access, journal) | T10 to T15 |
| 4 | tracer bullet, second half: kernel, edge, server, SQS over raw HTTP; snapshot format; Lambda runtime spike | T16 to T18 |
| 5 | broadening: all twelve models, remaining codecs, outbox, admin API and service stubs | T19 to T24 |
| 6 | protocol corpus and coverage; first service wave (nine services or service halves) | T25 to T34 |
| 7 | second service wave | T35 to T38 |
| 8 | third service wave; Docker image | T39 to T43 |
| 9 | CLI and config; final wiring and smoke test | T44, T45 |

Stretch items (DynamoDB Streams to Lambda, Lambda destinations) are not planned; the plan is at its 45-task ceiling. Multi-SDK conformance suites, Terraform acceptance, the end-to-end example, user guides and the parity replayer belong to later orchestrator phases; the seams they need (readiness endpoint, `--listen 127.0.0.1:0`, `--ready-file`, `--persist`, labeled containers, `understudytest`, canonical trace trees, normalized snapshot diffs) are built here.

## Rules every task follows

- Read `docs/brief/phase1-brief.md`, `docs/design/ARCHITECTURE.md` (the sections the task cites) and the ADRs it cites before writing code. Research files are cited as `file.md section`.
- Edit only the paths in `owns`. Shared files have exactly one owner per wave: `go.mod` and `go.sum` (T01 only), `justfile` (T01 creates every recipe the plan names, including ones that cannot run yet; nobody else edits it), `.golangci.yml` (T01), `.github/workflows/ci.yml` (T01, then T19 in wave 5, T25 in wave 6, T45 in wave 9), `internal/server/wire.go` (T16 in wave 4, T24 in wave 5, T45 in wave 9), `codegen/overlays/common.json` (T19 in wave 5, T25 in wave 6).
- Generated files (`internal/api/*`, `internal/protocoltest/gen/*`, `docs/coverage.md`) do not count toward a task's line budget. A task that owns `codegen/overlays/<svc>.json` regenerates its own `internal/api/<svc>api` with `go run ./internal/cmd/gen -service <svc>` and never edits generated files by hand.
- A service package exists as a stub from wave 5 (`New(kernel.ServiceDeps, Options) *Service` binding nothing). Service tasks replace the stub; they never edit `wire.go`. `New` calls one `bind<Family>(b)` per resource family; a family a later task owns is a stub function in its own file so parallel tasks in a later wave touch disjoint files. Service-specific settings live in the package's `Options` struct with usable zero values; T45 maps `config.Config` onto every `Options`.
- Tests: behavior through `kernel.Invoke` inside `kerneltest` bubbles (`testing/synctest`), never over HTTP except in `edge`, `server`, `protocoltest` and `smoke`. Property tests use `pgregory.net/rapid`. Time uses the emulator clock. Docker tests carry `-tags docker`, the label `understudy.dev/test=1`, a unique name, and remove what they start. Tests bind `127.0.0.1:0`. Cross-service imports are allowed in `_test.go` files only (the dependency test exempts them).
- Verification commands are scoped to the task's own packages; the wave gate runs `just verify`, `just generate-check` (from wave 5), the protocol-test baseline (from wave 6), `just budgets` (from wave 5), `just test-docker` (from wave 4) and `just smoke` (wave 9).
- No AWS calls. Every AWS-facing command runs through `scripts/sealed`. No commits.
- No emojis; no stray TODOs; doc comments on every exported identifier; errors as values; sealed sums annotated `//sumtype:decl`.

## Wave 1: scaffold

### T01 Repository scaffold (sonnet)

Owns: `go.mod`, `go.sum`, `justfile`, `.golangci.yml`, `.gitignore`, `.editorconfig`, `LICENSE`, `NOTICE`, `README.md`, `.github/workflows/ci.yml`, `cmd/understudy/main.go`, `admin/doc.go`, `understudytest/doc.go`, `internal/**/doc.go`, `codegen/README.md`, `third_party/README.md`, `testdata/README.md`, `conformance/README.md`.

Spec:
- `go.mod`: `module github.com/sustinbebustin/understudy`, `go 1.27`, `require pgregory.net/rapid v1.3.0` (test-only; `go.sum` committed). No other dependency, ever, without an ADR.
- `.golangci.yml`: the strict configuration in `go-toolchain.md 13` verbatim (version 2, `linters.default: none`, the listed linters and settings, `depguard` main and tests rules, `forbidigo` patterns, exclusions for `_test.go`, `internal/clock/`, `cmd/`, formatters `gofumpt` and `goimports` with the local prefix, `issues.uniq-by-line: false`). `golangci-lint config verify` passes.
- `justfile`: `verify` (gofmt -l over non-generated files must print nothing, `go vet ./...`, `golangci-lint run ./...`, `go test -race ./...`), `fmt`, `lint`, `test`, `test-docker` (`go test -race -tags docker ./...`), `generate` (`go run ./internal/cmd/vendor -verify && go generate ./...`), `generate-check` (generate, then `git diff --exit-code` and `git status --porcelain` empty), `protocoltest-baseline` (`go test ./internal/protocoltest -run TestCorpus -update-baseline`), `coverage-check` (`go run ./cmd/understudy coverage --format markdown > docs/coverage.md` then diff), `budgets` (`go run ./internal/cmd/budgets`), `smoke` (`go test -race -tags smoke ./internal/smoke`), `image` (`docker build -t understudy:dev .`), `release-check` (`goreleaser check` when installed), `aws *ARGS` (`scripts/sealed aws {{ARGS}}`), `tf *ARGS` (`scripts/sealed terraform {{ARGS}}`), `sealed-test` (`uv run --with pytest pytest scripts/tests`). Recipes that reference packages that do not exist yet may fail when run; `just verify` must pass.
- `.github/workflows/ci.yml`: ubuntu-latest, Go 1.27.1, golangci-lint 2.13.2, just; one job running `just verify` and `just sealed-test`. Later waves add jobs.
- `LICENSE`: Apache-2.0 text. `NOTICE`: project notice plus a third-party section for `aws/api-models-aws` (Apache-2.0, pinned commit `1cfdfb4bb9ea858e65623e66d73af04bec353201`), Smithy protocol tests (Apache-2.0, 1.74.0), LocalStack snapshot-derived fixtures (Apache-2.0, archived repository; the commit is cited per overlay entry), `pgregory.net/rapid` (MPL-2.0, tests only, not shipped), and the in-repo xxHash port written from the public specification (ADR 0016).
- `.gitignore`: `/bin/`, `/dist/`, `*.snapshot`, `/.data/`, editor files. `.editorconfig`: tabs for Go, two spaces for YAML and JSON, LF, final newline.
- Skeleton: a `doc.go` with a package comment in every package named in ARCHITECTURE.md section 2 (`internal/cli` through `internal/parity`, `internal/protocol/*`, `internal/service/<svc>` for the twelve services and their listed subpackages, `internal/codegen`, `internal/cmd/gen`, `internal/cmd/vendor`, `internal/cmd/budgets`, `internal/protocoltest`, `internal/smoke`, `admin`, `understudytest`). `cmd/understudy/main.go` prints `understudy <version>` where `var version = "dev"` is set through `-ldflags "-X main.version=..."`, and exits 0.
- `README.md`: three paragraphs: what understudy is, that it is pre-release and nothing is published, and how to run `just verify`.

Acceptance:
- `just verify` passes on the skeleton; `golangci-lint config verify` passes; `go build ./...` succeeds; `just sealed-test` passes.
- Every package directory named in ARCHITECTURE.md section 2 exists with a `doc.go`.
- `grep -R "TODO" --include=*.go` finds nothing.

Verify: `just verify`, `golangci-lint config verify`, `just sealed-test`.

## Wave 2: leaf packages

### T02 Vocabulary: identifiers, errors, schema descriptors, registry, delivery types (opus)

Owns: `internal/aws`, `internal/ref`, `internal/evidence`, `internal/fidelity`, `internal/awserr`, `internal/protocol/smithy`, `internal/registry`, `internal/delivery`.

Spec (ARCHITECTURE.md 3.1 to 3.4, 6.1, 16; ADR 0003, 0016 amendments):
- `aws`: `AccountID` (`ParseAccountID`, exactly 12 digits), `Region` with the table of real AWS region names plus `WithExtra`, `Partition`, `Scope`, `ARN` (`ParseARN`, `String`, S3 and IAM forms with empty region or account, `MustMatch` helpers), `RegionSource` sum (`CredentialScope{Region} | HostRegion{Region} | None`).
- `ref`: `ServiceID`, `OperationID`, `RuleID`, `EventID`, `Cause`, `ProtocolID` constants (`awsjson1_0`, `awsjson1_1`, `awsquery`, `restjson1`, `restxml`, `rpcv2cbor`, `raw`), `ListenerKind`, `Via` enum (closed), `Origin` sum (`External`, `Internal`). The import-direction test `ref/deps_test.go` walks every package under `internal/`, `admin/`, `understudytest/` and `cmd/` with `go/packages`-free parsing (`go/parser` on import specs) and checks edges against a table of allowed importers kept as data in the test, mirroring ARCHITECTURE.md section 2; `_test.go` files are exempt. Packages not yet present are skipped.
- `evidence`: `Label` string type with the five values and `ParseLabel`.
- `fidelity`: `Switch[T comparable]{Name, Value, Label, Ref, Record}`, `Entry`, `(Switch[T]).Entry()`.
- `awserr`: `Error`, `Identity`, `ProtocolMessage`, `Field`, `Header`, `Fault`, `MessageKey`, `Origin` sum (`Modeled`, `Generic`, `Validation`, `Unimplemented{Op ref.OperationID}`, `Injected{Rule ref.RuleID}`, `Internal{Cause}`), `Violation`, `Path`, `Rendered`, `Constraint` sum, `Style`, `Internalf`, `AsError`, `RequestAborted`, and the generic catalogue identities per protocol family from `error-matrix.md 3` (`SerializationException`, `UnknownOperationException`, `InvalidAction`; the auth identities per family, JSON `MissingAuthenticationTokenException`, `IncompleteSignatureException`, `InvalidSignatureException` and Query `MissingAuthenticationToken`, `IncompleteSignature`, `SignatureDoesNotMatch`, chosen with `awserr.FamilyOf`; `AccessDeniedException`, `InternalFailure`, `NotImplemented`, `RequestEntityTooLarge`, `ThrottlingException`). `*Error` does not implement `error`. A test `awserr/literals_test.go` parses every non-test Go file under `internal/` and fails on an `awserr.Identity{` composite literal outside `internal/api` and `internal/awserr`.
- `protocol/smithy`: `ShapeID` (`Parse`, `Namespace`, `Name`), `ShapeType` enum covering every Smithy 2.0 kind the corpus uses (`smithy-models.md 2.2`, `smithy-protocol-tests.md 3`: structure, union, list, map, string, enum, intEnum, blob, boolean, byte, short, integer, long, float, double, bigInteger, bigDecimal, timestamp, document, unit), `Schema` and `Member` descriptors carrying every wire trait a codec needs (`required`, `default`, `jsonName`, `xmlName`, `xmlFlattened`, `xmlAttribute`, `xmlNamespace{URI, Prefix}`, `timestampFormat`, `sensitive`, `streaming`, `sparse`, `httpLabel`, `httpQuery`, `httpQueryParams`, `httpHeader`, `httpPrefixHeaders`, `httpPayload`, `httpResponseCode`, trailer binding, `error` fault, `httpError`, `mediaType`, `hostLabel` ignored), `Serializer` and `Deserializer` interfaces exactly as ARCHITECTURE.md 6.1 (every `Write*`/`Read*` including `WriteBigInt`, `WriteBigDecimal`, `WriteFloat32`, `WriteInt8/16/32`, unions and documents), `Body`, `BodySource`, `document.Value` ADT (null, bool, number as `jsontext`-free decimal string, string, list, map), `Serializable` and `Deserializable` interfaces.
- `registry`: `Service`, `Operation`, `OperationKind`, `IAMAction`, `HTTPBinding`, `Route`, `RouteTable` (rows with method, path pattern segments `Literal | Label | Greedy`, required query keys with optional values, required headers, operation; `Match(method, path, query, headers) (*Route, bool)` by Smithy specificity, `smithy-models.md 9` item 7 and `wire-rest.md 1.1`), `BindCoverage` sum (`Full`, `StoredOnly{Note}`, `Partial{Note}`), `Registry` with `New(services...)` and lookups by `ServiceID`, by JSON target prefix, by `(Action, Version)`, by signing name.
- `delivery`: `Target` sum with payloads, `Mechanism` enum, `Request`, `RetryPolicy` sum (`SNSManaged`, `EventBridgeTarget{MaxAge, MaxAttempts}`, `S3Notification`, `None`), `InvocationType`, `MessageAttribute` with the sealed `AttributeValue` sum (`StringAttribute`, `NumberAttribute` keeping the sender's decimal text, `BinaryAttribute`).

Acceptance:
- rapid: `ARN` parse and format round trip for standard, S3 and IAM forms; `AccountID` rejects anything but 12 ASCII digits; `RouteTable.Match` prefers literal over label over greedy, path over query, more segments over fewer, on generated random tables.
- The dependency test passes and fails on a synthetic violation (tested with a temp package).
- The literal test fails on a synthetic `awserr.Identity{}` literal in a temp file.
- `registry.New` rejects duplicate service ids and non-contiguous ordinals.

Verify: `go test -race ./internal/aws/... ./internal/ref/... ./internal/evidence/... ./internal/fidelity/... ./internal/awserr/... ./internal/protocol/smithy/... ./internal/registry/... ./internal/delivery/...`, `golangci-lint run ./internal/aws/... ./internal/ref/... ./internal/evidence/... ./internal/fidelity/... ./internal/awserr/... ./internal/protocol/smithy/... ./internal/registry/... ./internal/delivery/...`.

### T03 Clock and identifier source (opus)

Owns: `internal/clock`, `internal/ids`.

Spec (ARCHITECTURE.md 9, 12; ADR 0006, 0018 and their amendments; `lambda-runtime-contract.md 3`; `iam-sts-kms.md 2.1, 2.2`; `go-toolchain.md 1, 9.1`):
- `clock`: `Instant` (`Add`, `Sub`, `Before`, `Wire`, `Compare`, `IsZero`), `Mode` sum (`Running{Offset}`, `Frozen{At}`, `Scaled{Factor, AnchorWall, AnchorEmu}`), `Clock` (`New(Options{Start, Mode, Wall func() time.Time})`, `Now`, `Mode`, `Freeze`, `Resume`, `Scale` with `0 < f <= 10000`, `Advance`, `AdvanceTo` refusing backwards moves with a typed error, `Until(ctx, Deadline)`, `Wall`, `Stopwatch`, `Run(ctx)` driver), `Deadline{Emu, WallCap}`, `Advanced{Fired []TimerKey, From, To}`, `TimerKind`, `TimerKey`, `Scheduler` (`NewScheduler(dispatch)`, `Arm`, `Disarm`, `Next() (Instant, bool)`, `Len`, `Snapshot() []Armed` for tests), `WaitersHook` (a callback invoked once per advance). The driver sleeps `(nextDue - now) / factor` of wall time in Running and Scaled; in Frozen it waits for an arm, advance or mode change; due timers fire in `(due, seq)` order with `Now() == due` inside the dispatch; timers armed during an advance fire in the same advance if due; the mutex is released before `dispatch` is called. `time.Now` and timers are used only here.
- `ids`: typed string ids (`RequestID`, `MessageID`, `ReceiptHandle`, `SNSMessageID`, `LambdaRequestID`, `ESMID`, `UploadID`, `VersionID`, `ETag`, `KMSKeyID`, `IAMID`, `AccessKeyID`, `SecretAccessKey`, `SessionToken`, `DeliveryID`), `Kind` enum (one per id type plus `FaultStream`, `Jitter`), `Source` (`NewRandom()`, `NewSeeded(seed uint64)`, `Draw(kind, n) []byte`, `Uint64(kind)`, `UUID(kind)`, `Positions() map[Kind]uint64`, `Seek(positions)`), constructors per kind with AWS formats: UUID v4 text for message and request ids, 36-char base64-like receipt handles, `AROA`/`ANPA`/`AIDA` plus 17 base32 characters, `AKIA`/`ASIA` plus 16 base32 characters with the account in the 48-bit window (top bit set, `account<<7`, random remainder) and `DecodeAccount(key) (string, bool)` returning the 12 digits (`ids` is a leaf, ARCHITECTURE.md 2; callers convert with `aws.ParseAccountID`), 40-character secrets, opaque session tokens. KMS key material and SNS signing keys are not drawn here.

Acceptance:
- synctest: stepwise advance across a `rate(5 minutes)`-style key fires twelve times with `Now()` equal to each due; an arm inside dispatch fires in the same advance; `Frozen` fires a timer armed at or before `Now()` immediately; `Scaled{Factor: 60}` fires a 60 s timer after 1 s of wall; `Until` with `WallCap` ends under a frozen clock; a cancelled `Until` leaves no registration (count via a test hook); `AdvanceTo` backwards returns the typed error; the driver exits when its context ends.
- `ids`: two sources with the same seed draw identical sequences per kind and different kinds are independent; `Positions` then `Seek` reproduces the next draw; minted keys decode to their account; UUIDs match the RFC 4122 v4 pattern; the random source never repeats within 10,000 draws.

Verify: `go test -race ./internal/clock/... ./internal/ids/...`, `golangci-lint run ./internal/clock/... ./internal/ids/...`.

### T04 State store and blob store (opus)

Owns: `internal/store`, `internal/blob`.

Spec (ARCHITECTURE.md 3.5, 8; ADR 0005 amendments; `go-toolchain.md 8.2`):
- `store`: `Gate` (`Enter`/`Leave` read side, `Exclusive() (release func())`), `Cell[S]` with `Update` and `Read`, `rev` and `Rev()`, `Partitioned[K, S]` (`NewPartitioned(gate, codec, init)`, `For`, `Lookup`, `Keys(kc)` sorted by encoded key, `Delete`, `Export(kc, version) ([]Section, error)` skipping zero-valued cells (a cell is zero when `S` has an `IsZero() bool` method returning true, else never skipped), `Import(kc, version, sections)` refusing mismatched versions with a typed error naming both, `Reset(keep)`), `KeyCodec[K]{Encode(K) string; Decode(string) (K, error)}` with codecs `ScopeKeys()`, `AccountKeys()` and `StringKeys()` (an empty encoding means the key has none, and `Export` refuses it), `Cell.Retired()`, `Section{Path, Version, Data, Blobs}`, `WaitKey`, `Waiters` (`Wait(ctx, key) <-chan struct{}`, `Wake(key)`, `Len(key)` for tests). JSON for `Export` uses `encoding/json` with sorted map keys through a `Marshal` hook the caller injects (`jsonx` arrives in wave 3; `Partitioned` takes a `Codec{Marshal, Unmarshal}` so the choice is the caller's).
- `blob`: `Store` (`NewMemory(spillDir, spillAbove int64)`, `NewDisk(root *os.Root)`), `Ref` (sha256 hex), `Put(ctx, r io.Reader, sizeHint int64) (Ref, Size, error)` computing the hash while writing to a temp file under the root and renaming into `sha256/<hex>`, `Open(ref) (ReadSeekCloser, error)`, `Range(ref, off, n) (io.ReadCloser, error)`, `Size(ref)`, `Retain(ref)`, `Release(ref)` deleting at zero, `Pin(refs)` for snapshot cuts, `Refs() []Ref`, `Import(ctx, fs.FS)` (a source with no `sha256/` directory imports nothing), `Export(ref) (io.ReadCloser, error)` and `Prune()` deleting every blob at zero references once a load's retain pass ends. `NewMemory` refuses a spill threshold above `MaxSpillAbove` (1 GiB) with `*SpillAboveError`. Names are hex, so no user string ever becomes a path component. Objects above the spill threshold (default 8 MiB) go to disk even in memory mode; the temp dir is removed on `Close`.

Acceptance:
- rapid: `Partitioned` export then import then export is byte-identical over random states and keys; `Update` returning `mutated=false` leaves `Rev` unchanged; concurrent `Update` and `Read` under `-race` with results that are copies.
- `Waiters`: 1,000 waits each cancelled by context leave `Len(key) == 0`; `Wake` with no waiters is a no-op.
- `blob`: streaming `Put` of 64 MiB uses bounded memory (asserted by reading from an `io.LimitReader` over a zero reader and checking `runtime.MemStats` growth under 16 MiB); `Range` equals the slice of the whole; `Release` to zero removes the file; a `Ref` that is not hex is rejected.

Verify: `go test -race ./internal/store/... ./internal/blob/...`, `golangci-lint run ./internal/store/... ./internal/blob/...`.

### T05 Checksums and aws-chunked framing (opus)

Owns: `internal/checksum`, `internal/awschunked`.

Spec (`go-toolchain.md 7`; `s3-checksum-residuals-and-record-backlog.md` part 1; `gap-G8.md`; `sigv4-checksums-endpoints.md 3, 4`; `s3.md 6`; ADR 0016):
- `checksum`: `Algorithm` enum (CRC32, CRC32C, CRC64NVME, SHA1, SHA256, SHA512, MD5, XXHASH64, XXHASH3, XXHASH128) with `HeaderName()` (`x-amz-checksum-crc32` and so on), `ParseAlgorithm`, `ParseHeaderName`, `New(alg) (*Hash, error)` (a `hash.Hash` with `Checksum() Sum`), a `Sum` value pairing algorithm and big-endian digest whose `String()` is the wire form (base64; XXH3-128 as `high64 || low64`), `Decode(alg, value) (Sum, error)`, `Combine(alg, parts []Part) (Sum, error)` for CRC32, CRC32C and CRC64NVME (`go-toolchain.md 7.4`), `Composite(alg, parts []Sum) (string, error)` (`-N` suffix form), `Verify(trailerValue, computed Sum) error` returning `InvalidValueError` or `MismatchError`, xxHash64, XXH3-64 and XXH3-128 ported from the specification (seed 0, default secret, every length class), `crc64nvme` table.
- `awschunked`: `Mode` enum (`UnsignedTrailer`, `Signed`, `SignedTrailer`, `PlainChunked` with `Content-Encoding: aws-chunked` and `x-amz-decoded-content-length`), `NewReader(r, Mode, Options{DecodedLength Length, Trailer, Seed, OnChunkSignature SignatureFunc, MaxChunk}) *Reader` implementing `io.Reader` with `Trailers() (http.Header, bool)` valid after EOF, typed errors `IncompleteBodyError{Expected, Got}`, `DecodedLengthExceededError`, `MalformedChunkError{Offset, Defect}`, `ChunkTooSmallError{Chunk, Size}` (every mode: chunks but the last hold at least 8,192 bytes), `MissingTrailerError`, `TrailerMismatchError`, `UndeclaredTrailerError`; `NewWriter` (encoder) for tests. Trailer checksum verification is `checksum.Verify`, because `awschunked` imports nothing in the repo.

Acceptance:
- Every published vector in `go-toolchain.md 7.3` and `s3-checksum-residuals-and-record-backlog.md 1.4` passes, including the xxHash length-class vectors.
- rapid: streaming equals one-shot for every algorithm over random chunkings; `Combine` of random part splits equals the whole-body checksum; `Composite` is stable; encode then decode over random chunk sizes with and without trailers reproduces the body and the trailers; a truncated body yields `IncompleteBody` with the right counts.

Verify: `go test -race ./internal/checksum/... ./internal/awschunked/...`, `golangci-lint run ./internal/checksum/... ./internal/awschunked/...`.

### T06 Streaming XML writer (opus)

Owns: `internal/protocol/xmlw`.

Spec (ARCHITECTURE.md 6.2 awsquery and restxml rows; `wire-rest.md 2.1 to 2.3`; `wire-json-query.md 8.1, 8.3`; `go-toolchain.md 10`):
- `Writer` over `io.Writer`: `Start(name)`, `StartNS(name, xmlns)`, `Attr(name, value)`, `AttrNS(prefix, name, value)`, `Text(string)`, `End()`, `Empty(name)`, `Declaration()`; text and attribute escaping with the SDK escape table (`&`, `<`, `>`, `"`, `'`, `\r`, `\n`, `\t` in attributes, U+0085 and U+2028 as numeric references, other control characters as numeric references, invalid UTF-8 replaced); `xsi:type` helper for grantees; deterministic byte output; no reflection.
- `Reader` helpers over `encoding/xml` for requests: `Decode(r, maxBytes) (*Node, error)` into a local-name tree ignoring namespaces, with size and depth limits and typed `MalformedXML`.

Acceptance:
- Goldens: the awsQuery envelope of `wire-json-query.md 8.1`, an S3 `ListBucketResult` with `xmlns`, an S3 `<Error>` document, an IAM `<Roles/>` empty collection, a grantee with `xsi:type`.
- rapid: for random Unicode strings, `Text` then `Decode` returns the original (or the replacement-character form for invalid UTF-8); output never contains an unescaped `<`, `&` or raw control character.

Verify: `go test -race ./internal/protocol/xmlw/...`, `golangci-lint run ./internal/protocol/xmlw/...`.

### T07 Event Ruler matching engine (opus)

Owns: `internal/ruler`.

Spec (`matching-engines.md` all sections; `sns.md 6`; `lambda-events.md 2.4`; `secrets-logs-events-alarms.md 4.4`; ARCHITECTURE.md 16):
- `Profile` sum: `EventBridge`, `SNS{Scope: MessageAttributes | MessageBody}`, `LambdaESM`; each with its operator allow-list and limits from `matching-engines.md 5.1 to 5.2` and `sns.md 6.5`.
- `Compile(pattern []byte, p Profile) (*Pattern, *CompileError)`: typed pattern ADT (exact, prefix, suffix, equals-ignore-case, wildcard, anything-but with its variants, numeric with ranges, exists, null, cidr, `$or`, nested keys), limits (keys, combinations, depth, value counts), error texts per service (`matching-engines.md 5.3`, `sns.md 6.5` texts); strict compile errors for malformed `$or`.
- `Match(p *Pattern, event []byte) (bool, error)` with event flattening (`4.2`), array versus scalar semantics (`4.3`), null, empty string and boolean handling (`4.4`), numeric comparison on decimal values (`4.5`), `anything-but` (`4.6`), string operators (`4.7`), `$or` and combinations (`4.8`). The four Ruler-versus-doc conflicts (`matching-engines.md 10`) are `Options` fields whose defaults follow Ruler; `Switches()` returns `ruler.Switch` metadata (Name, Value, Ref, Record), because `ruler` imports nothing in the repo, and the owning service wraps each in a `fidelity.Switch`.
- `MatchAttributes(p, attrs map[string]Attribute)` for the SNS attribute scope (string, number, string array types).

Acceptance:
- All 85 seed vectors in `matching-engines.md 9` pass as table tests.
- rapid: a pattern matches an event iff it matches after the event's arrays are permuted (array and scalar intersection); matching is independent of key order in the pattern; numeric patterns never match string values; every compile error names the operator or limit that failed.

Verify: `go test -race ./internal/ruler/...`, `golangci-lint run ./internal/ruler/...`.

### T08 Logs filter patterns and schedule expressions (opus)

Owns: `internal/logsfilter`, `internal/schedexpr`.

Spec:
- `logsfilter` (`logs-filter-patterns.md 3 to 8, 10`; `secrets-logs-events-alarms.md 3.6`; `gap-G3.md`): `Compile(pattern string) (*Filter, *Error)` for terms, quoted phrases, exclusion, OR groups, JSON patterns with `=`, `!=`, `<`, `<=`, `>`, `>=`, `IS NULL`, `NOT EXISTS`, wildcards and compound `&&`/`||`; `Match(f, message string) bool`; the token-boundary rule of section 5 and the delimited field-count rule as `Options` fields whose `Switches()` metadata (`logs.filter.term-boundary`, `logs.filter.delimited-field-count`) the logs service wraps in `fidelity.Switch` values, since `logsfilter` imports nothing in the repo; errors with the exact `InvalidParameterException` texts of section 7.
- `schedexpr` (`secrets-logs-events-alarms.md 4.2`): `Parse(expr string) (Schedule, *Error)` for `rate(N unit)` (singular and plural rules, N >= 1) and `cron(min hour dom month dow year)` with ranges, lists, steps, `*`, `?`, `L`, `W`, `#`, month and day names, the day-of-month and day-of-week exclusivity rule; `Next(s, after time.Time) (time.Time, bool)` in UTC; error text `Parameter ScheduleExpression is not valid.`.

Acceptance:
- Every vector in `logs-filter-patterns.md 8` passes; the residual cases in section 9 are table-tested with their documented default and marked by switch name.
- rapid (`logsfilter`): `Match(A) || Match(B)` equals `Match(A OR B)`; `Match(-A)` equals `!Match(A)` for term patterns; compile never panics on random input.
- rapid (`schedexpr`): `Next` is strictly increasing when iterated; `rate(N minutes)` fires exactly every N minutes from the anchor; `cron` results satisfy every field constraint of the parsed expression; parse never panics.

Verify: `go test -race ./internal/logsfilter/... ./internal/schedexpr/...`, `golangci-lint run ./internal/logsfilter/... ./internal/schedexpr/...`.

### T09 Docker Engine client and topology detection (opus)

Owns: `internal/docker`, `internal/docker/dockertest`, `internal/topology`.

Spec (`containers.md 2, 3, 8, 9, 10`; ARCHITECTURE.md 14.5, 20; ADR 0011 and amendments):
- `docker.Client` over the unix socket with `net/http` (`New(Options{Socket, APIVersionMin})`, negotiated version `2.2`), typed `Error{Status, Message}` with `IsNotFound`, `IsConflict`, `IsUnavailable`; `Ping`, `Info`, `ImageInspect`, `ImagePull(ctx, ref, platform, progress func(Progress))` streaming the JSON progress frames, `ContainerCreate(name, Config, HostConfig, NetworkingConfig)`, `ContainerStart`, `ContainerStop(timeout)`, `ContainerKill`, `ContainerRemove(force, volumes)`, `ContainerWait`, `ContainerInspect`, `ContainerLogs(follow, timestamps, since) (Stream, error)` with the `stdcopy` frame demultiplexer, `ContainerArchiveUpload(path, tar)` (tar-copy fallback), `Events(ctx, filters) (<-chan Event, error)`, `NetworkInspect`, `NetworkCreate(name, internal)`, `NetworkConnect`, `ContainerList(labels)`. Labels helpers for `understudy.dev/managed`, `instance`, `owner`, `function`, `account`, `region`, `generation`, `test`. `Sweep(ctx, ownerLabel)` removes this owner's leftovers (`containers.md 9.3`). Podman differences (`10.1`) are handled where the research names them.
- `dockertest.Daemon`: an in-process fake Engine API on a unix socket in a temp dir implementing the above endpoints with scripted containers, image presence, events and logs, for use by this and later tasks.
- `topology`: `Topology` sum (`LinuxBridge{Gateway}`, `DockerDesktop{Host DesktopHost}` with `DesktopHost` = `ProbedHost{Addr, Cleanup}` | `FallbackHost{Cause}`, `RootlessDocker{HostAddr}`, `Podman{Rootless, Gateway}`, `InContainer{Network, Self}`), `Detector.Detect(ctx, *docker.Client, Options{ProbeImage string; ProbeLabels docker.Labels; Root fs.FS}) (Topology, error)` following `containers.md 8.1 to 8.3` (in-container via `/.dockerenv` or cgroup plus self inspect; Desktop via `Info.OperatingSystem`; rootless via `Info.SecurityOptions`; Podman via the `Libpod-API-Version` header; Linux bridge via the `bridge` network gateway), the Docker Desktop probe (run `ProbeImage` with Entrypoint `getent ahostsv4 host.docker.internal`, cached per daemon on success, fallback `192.168.65.254`), `Addresses() {ListenerBind netip.Addr; FunctionEndpointHost netip.Addr; ExtraHosts []string; NeedsContainerListener bool; FunctionNetwork string}`, `UnsupportedError` for rootless Docker and rootless Podman with the documented text.

Acceptance:
- Every client method is tested against `dockertest.Daemon`, including error mapping, the `stdcopy` demultiplexer on interleaved frames, pull progress and events.
- `topology.Detect` is table-tested against scripted `Info` responses for all five variants.
- `-tags docker`: `Ping`, `Info`, pull `public.ecr.aws/lambda/provided:al2023` if absent, create and run a labeled container (`understudy.dev/test=1`, unique name) with `["/bin/sh", "-c", "echo hello"]`, read `hello` from logs, wait, remove; `Detect` on this host returns `LinuxBridge` or `InContainer` with a non-zero address; `Sweep` removes a leftover labeled container.

Verify: `go test -race ./internal/docker/... ./internal/topology/...`, `go test -race -tags docker ./internal/docker/... ./internal/topology/...`, `golangci-lint run ./internal/docker/... ./internal/topology/...`.

## Wave 3: tracer bullet, first half

### T10 Generator slice: vendoring, loader, SQS code generation (opus)

Owns: `internal/codegen`, `internal/cmd/gen`, `internal/cmd/vendor`, `third_party/api-models-aws`, `codegen/services/sqs.json`, `codegen/overlays/sqs.json`, `codegen/overlays/common.json`, `internal/api/generate.go`, `internal/api/sqsapi`.

Spec (ARCHITECTURE.md 5.1 to 5.5, 5.7, 3.2 to 3.4; ADR 0001, 0002, 0003 and amendments; `smithy-models.md 2, 4, 6, 10`; `sqs.md 2.1, 2.4, 4, 6.1`; `error-matrix.md 3, 5.1, 7`; `wire-json-query.md 4, 5`; `go-toolchain.md 15`):
- `cmd/vendor`: `-fetch` downloads the twelve model files of `smithy-models.md 3.1` at commit `1cfdfb4bb9ea858e65623e66d73af04bec353201` through the GitHub tree and raw endpoints, verifies the git blob SHA-1 against the tree entry, writes them verbatim plus `LICENSE`, `NOTICE` and `UPSTREAM.json` (repo, commit, retrieval date, licence, per-file `dir`, `path`, `bytes`, `gitBlobSha1`, `sha256`); `-verify` recomputes both hashes offline and is what `just generate` runs. Run `-fetch` once in this task; all twelve files are checked in.
- `codegen` library: loader (`encoding/json` with `UseNumber`; reject unknown shape types, `mixins`, `apply` and `set` in the vendored model, `smithy-models.md 2`), overlay merge (Smithy AST fragments; `dev.understudy#evidence` mandatory with an `evidence.Label`; the full closed trait table of ARCHITECTURE.md 5.2 is parsed and validated, conflicts with the model fail unless `"replace": true`), closure (operations, resources, nested), classifier (every trait id is `honor`, `generation-only` or `ignore` from the table in `smithy-models.md 4.3`; unclassified fails), effective member traits, regex stage (syntax translation and the ECMA semantic rewrites of 5.4; `patternValidator` for lookarounds; every pattern must compile), role analysis, typed IR, emitters, `go/format`, sorted iteration, temp file and rename, the header `// Code generated by understudy-gen from api-models-aws@1cfdfb4 <file>; DO NOT EDIT.`.
- Emitters in this task cover what SQS over awsJson1_0 needs: `service_gen.go`, `types_gen.go` (required as values, optional as pointers, enums with `Known()`, unions, error structs), `schema_gen.go`, `serde_gen.go` (JSON-relevant members; the XML, Query, HTTP and CBOR trait emission is wave 5), `validate_gen.go` (required, length in scalar values, range, pattern, enum with `enumPolicy`, AWS per-member order), `errors_gen.go` (`Identity` constants and constructors, per-operation error lists, `messageByProtocol`), `ops_gen.go` (immutable descriptors with `Ordinal`, `Kind`, `SuccessCode`, `MaxRequestBytes`, IAM list containing the same-name action until the Service Reference table lands in T19), `bind_gen.go`, `redact_gen.go`, `examples_gen_test.go` when the model has examples. `internalOperation` shapes are emitted with `Kind: Internal`.
- `codegen/services/sqs.json` (`extraProtocols: ["aws.protocols#awsQuery"]`), `codegen/overlays/sqs.json` with the SQS constraints of `sqs.md 4, 6.1` and the unmodeled errors of `sqs.md 2.4` (`InvalidParameterValue`, `MissingParameter`, `__type` values), `validationError` hand-text style, `errorNamespace`, `throttleError`, `unknownOperationError`, `HoldVisibility` as an `internalOperation`; `codegen/overlays/common.json` with the generic vocabulary.
- `//go:generate go run ../cmd/gen -service sqs` in `internal/api/generate.go` (one directive per service; later tasks add theirs), `cmd/gen` with `-service`, `-all`, `-check`.

Acceptance:
- `go run ./internal/cmd/vendor -verify` passes offline; `UPSTREAM.json` lists twelve files with both hashes.
- `go generate ./internal/api/...` is idempotent (second run produces no diff) and `internal/api/sqsapi` compiles and lints clean under `generated: strict`.
- Generator golden tests feed small model fragments through every stage (a structure with every member kind, an enum, a union, an error with `awsQueryError`, an overlay conflict, an unknown `dev.understudy#` trait, a lookaround pattern without a validator) and compare to expected output; an unclassified trait fails.
- A recording-serializer test drives `SendMessageOutput.Serialize` through a fake `smithy.Serializer` and asserts the member sequence, and replays a recorded client sequence into `SendMessageInput.Deserialize`. Role pruning (ARCHITECTURE.md 5.4, ADR 0001 amendment) gives an input-only shape no `Serialize`; a shape used both ways gets a round-trip property test.
- `validate_gen` rejects a 257-character queue name and a `DelaySeconds` of 901 with violations in AWS order.

Verify: `go test -race ./internal/codegen/... ./internal/cmd/... ./internal/api/...`, `golangci-lint run ./internal/codegen/... ./internal/cmd/... ./internal/api/...`, `go run ./internal/cmd/vendor -verify`.

### T11 JSON runtime and the awsJson codec (opus)

Owns: `internal/protocol/jsonx`, `internal/protocol/awsjson`.

Spec (ARCHITECTURE.md 6.1, 6.2 awsjson row, 6.3, 13 tagged unions; `wire-json-query.md 2 to 6, 10, 11`; `go-toolchain.md 2`; `error-matrix.md 3, 7`; `sqs.md 2.1, 2.2`):
- `jsonx`: the only importer of `encoding/json/v2` and `encoding/json/jsontext`. `Encoder` (deterministic: sorted object keys for snapshots, raw order for wire), `Decoder` (lenient: unknown members, `null`, duplicate keys last-wins, invalid UTF-8 and lone surrogates preserved as bytes for validators, numbers as decimal strings), `Value` tree for rewriting (DynamoDB proxy), `Union[T]` helpers implementing the `{"kind": ...}` convention with a per-sum registry, `Marshal`/`Unmarshal` for `store.Codec`.
- `awsjson`: `Codec{Version: V10 | V11}` implementing the edge codec interface (claim from `X-Amz-Target`, decode the body into a `smithy.Deserializer`, encode outputs through a `smithy.Serializer`, write errors): accept an empty body, `{}`, unknown and `null` members, `; charset=`, gzip for `PutMetricData`; emit plain-digit integers, epoch-second timestamps with millisecond precision, padded base64 blobs, `{}` for any modeled output; `__type` always (namespaced for 1.0 from `Identity.JSONType`, bare for 1.1), the message under `Identity.MessageKey`, `x-amzn-query-error: <code>;<Sender|Receiver>` on every error of a `QueryCompatible` service regardless of `x-amzn-query-mode`, status from `Identity.Status`, `x-amzn-RequestId`, `Content-Type: application/x-amz-json-1.0|1.1`. `MalformedRequest` returns the service's `malformedRequestError` identity from the descriptor or `SerializationException`.

Acceptance:
- rapid: encode then decode round trip over random values of a hand-built `smithy.Schema` covering every shape kind the service models use; decoding random bytes never panics.
- Goldens from `wire-json-query.md 4.1 to 4.4, 5.3, 6.2` and `sqs.md 2.2` byte for byte (SQS `QueueDoesNotExist` 1.0 body and headers, a Logs 1.1 error, an empty output).
- An input with invalid UTF-8 decodes with the bytes preserved and a flag the validator can read.

Verify: `go test -race ./internal/protocol/jsonx/... ./internal/protocol/awsjson/...`, `golangci-lint run ./internal/protocol/jsonx/... ./internal/protocol/awsjson/...`.

### T12 Identity store and request authentication (opus)

Owns: `internal/identity`, `internal/auth`.

Spec (ARCHITECTURE.md 7, 20; ADR 0012 and amendments; `sigv4-checksums-endpoints.md 1, 2, 3.1 to 3.3, 7`; `iam-sts-kms.md 2.2, 2.3, 4.1`; `lambda-runtime-contract.md 3` rows 9 and 10):
- `identity`: `Principal` sum (`Root`, `User`, `AssumedRole{Role RoleRef, Session, Source}`, `ServicePrincipal`, `Anonymous`), `SessionSource` sum, `RoleRef`, `Render(Principal) string` (ARN forms of `iam-sts-kms.md 4.1`), `Store` (`New(ids, defaultAccount)`, `Mint(principal, Options{Session bool, Expiry}) Credentials`, `Resolve(key) (Principal, Resolution)` with the order minted key, 12-digit numeric key as that account's root, anything else as the default account's root with a one-per-key warning hook; `Revoke`; `Export`/`Import` for snapshots), `Credentials` implementing `slog.LogValuer` with redaction.
- `auth`: `Parse(r *http.Request) (Auth, *awserr.Error)` returning the sum `SigV4Header{Credential{Key, Date, Region, Service}, SignedHeaders, Signature, PayloadHash}`, `SigV4Query{...Expires}`, `SigV2Header`, `SigV2Query`, `Anonymous`; `PayloadHash` sum (`Hex`, `Unsigned`, `StreamingUnsignedTrailer`, `StreamingSigned`, `StreamingSignedTrailer`, `NotSent`); `RegionSource()` derived from the auth; `Verify(a Auth, req Normalized, secret, wallNow time.Time, opts) *awserr.Error` as a pure function (canonical request per `sigv4-checksums-endpoints.md 1.2`, skew 15 minutes on wall time, the error identities of `error-matrix.md 3` auth family); `VerifyPresigned(a, req, secret, emuNow clock.Instant)`; `VerifySigV2Presigned`; `ChunkSignatureVerifier(a, secret) func(prev, chunk, sig) error` for `awschunked`; `Presign(req, creds, expires, now)` for tests and for the S3 presign tests of later tasks.

Acceptance:
- The seven DOC vectors of `sigv4-checksums-endpoints.md 1.2` pass as table tests; rapid: sign then verify succeeds for random requests and fails after any single-byte mutation of the signature or signed headers.
- `Resolve`: minted keys map to their principal; `AKIAIOSFODNN7EXAMPLE` resolves to the default account root and is not decoded; a 12-digit key resolves to that account.
- Credentials never appear in `slog` output (asserted with a `slog.TextHandler` into a buffer).

Verify: `go test -race ./internal/identity/... ./internal/auth/...`, `golangci-lint run ./internal/identity/... ./internal/auth/...`.

### T13 Fault injection rules and evaluation (opus)

Owns: `internal/fault`.

Spec (ARCHITECTURE.md 11; ADR 0008 and amendments; `error-matrix.md 8.4`; `sqs.md 13.18`; `tf-compute.md 1.3`):
- Types exactly as ARCHITECTURE.md 11: `Rule`, `Match` (with `[]ref.OperationID` and `[]delivery.Mechanism`), `ARNGlob` (glob over ARN strings with `*` and `?`), `Phase`, `Action` sum, `Trigger` sum, `OriginMatch`, `Catalogue` interface.
- `Store`: `New(seed *uint64, ids *ids.Source, cat Catalogue)`, `Add(Rule) (RuleID, *RuleError)` validating the shape against the catalogue (the error lists the codes the service really sends), the throttle identity resolution, probability bounds and `NextN >= 1`; `Remove`, `List`, `Clear`; `Evaluate(phase, MatchInput) Decision` where `MatchInput{Service, Op, Mechanism, Touched []aws.ARN, Scope, Origin}` and `Decision` is the sum `None | Fail{*awserr.Error, Rule, Hit, Of} | Latency{Wall, then} | AfterCommit{...} | StatusOKError{...} | Drop{...}`; NextN budgets decrement per hit; `Probability` draws from the `FaultStream` kind keyed by `hash(seed, rule id)`; `Export()`/`Import()` of rules, remaining budgets and stream positions for snapshots and config, as JSON through the tagged-union convention (`Export` refuses with `*ExportError{Rule, Position}` when the stream positions together pass `ids.MaxPosition`, which Import could not replay; callers show it and write no partial archive); `RetryableWarning(rule) (string, bool)` for the CLI.

Acceptance:
- Determinism: two stores with the same seed and rule set produce the same hit sequence for the same inputs; different rule ids produce independent streams.
- `NextN{3}` hits exactly three times then disappears from `List`; `Always` never expires.
- Validation rejects an unknown shape with a message listing the catalogue; accepts an overlay-added shape.
- rapid: `ARNGlob` agrees with a reference matcher on random ARNs and globs.

Verify: `go test -race ./internal/fault/...`, `golangci-lint run ./internal/fault/...`.

### T14 IAM policy documents and the access recorder (opus)

Owns: `internal/policydoc`, `internal/access`.

Spec (ARCHITECTURE.md 16 advisories, 17 `/iam` contract, 19 policy generation; ADR 0004 and 0015 amendments; `iam-sts-kms.md 3.3, 3.4, 5.3, 5.8`; `smithy-models.md 8`; `lambda-api.md 7.3`):
- `policydoc`: `Parse([]byte) (Document, *ParseError)` for `Version`, `Id`, `Statement` (single or list), `Sid`, `Effect`, `Action`/`NotAction`, `Resource`/`NotResource`, `Principal`/`NotPrincipal` (string `*`, `AWS`, `Service`, `Federated` forms), `Condition` (stored as a typed tree, keys and operators validated loosely); `ValidateIdentity` (identity policies, which must not name a principal) and `ValidateTrust` (trust policies) with the `MalformedPolicyDocument` texts of `iam-sts-kms.md 3.3`; canonical `Encode` (stable key order); `Allows(doc, Query{Principal, Action, Resource, Context}) Decision` (`Allowed{Assumed, Unsettled} | ImplicitDeny | ExplicitDeny{Statement}`) with wildcard globbing on actions and resources (single-valued string and ARN conditions on supplied keys are evaluated; other conditions are assumed to hold for an Allow and reported); `Render(statements)` for least-privilege output.
- `access`: `Recorder` (`Record(Entry{Principal, Mode, Actions []registry.IAMAction, Resources []aws.ARN, Outcome, At}) error`, where `Principal` is access's own sum `Root | User | Role | ServicePrincipal` because access may not import identity, and a malformed entry is refused; `Usage(Filter) []Use` (aggregated per principal, mode, action and resource, with call and failure counts), `Policy(Identity) (policydoc.Document, bool)` per the contract in ARCHITECTURE.md 17 (one statement per `(service, resource)`, exact ARNs, every action of a multi-action operation, non-injected failures included), `ResourceRequirements(Filter) []Requirement`, `ParsePrincipal` for the admin `?principal=` parameter, `Reset`), and the plain function `Advise(stored *policydoc.Document, ServicePrincipal, action, source, target) Advice` (`Allowed | WouldDeny{Reason}`; nil means no stored policy).

Acceptance:
- Goldens: a recorded ESM worker session (`sqs:ReceiveMessage`, `DeleteMessage`, `GetQueueAttributes` as the execution role, `logs:CreateLogGroup`, `CreateLogStream`, `PutLogEvents`) renders a policy that is a superset of `AWSLambdaSQSQueueExecutionRole` and `AWSLambdaBasicExecutionRole` actions; a recorded SSE-KMS `PutObject` and `GetObject` as the caller render `kms:GenerateDataKey` and `kms:Decrypt`.
- rapid: `Parse` then `Encode` then `Parse` is stable; `Allows` agrees with a brute-force evaluator over random single-statement policies.
- `Advise` returns `WouldDeny` for an SQS queue policy without a `sqs:SendMessage` statement for the topic's ARN and `Allowed` with one.

Verify: `go test -race ./internal/policydoc/... ./internal/access/...`, `golangci-lint run ./internal/policydoc/... ./internal/access/...`.

### T15 Causal journal (opus)

Owns: `internal/journal`.

Spec (ARCHITECTURE.md 10, 12; ADR 0007 and amendments):
- Types exactly as ARCHITECTURE.md 10 (`Event`, `Detail` sum with every variant including `Advisory`, `Outcome`, `DeliveryOutcome`, `InvocationOutcome`, `ResourceState`, `Redacted`).
- `Journal`: `New(Options{Capacity, MaxBytes, File *FileOptions{Path, RotateBytes}, Clock})`, `Begin(partial Event) (ref.EventID, error)`, `End(id, outcome, latency)`, `Append(Event) (ref.EventID, error)` for non-call events (both refuse an event no consumer could read with `*RecordError`), `FileErr() error` reporting a stopped journal file, `Query(Filter{Trace, RequestID, Service, Operation, ResourceGlob, Kind, Outcome, Principal, EmuRange, IDRange, Limit, Cursor}) Page`, `Trace(id) (*Tree, bool)`, `TraceByRequestID`, `(*Tree).Canonical() CanonicalTree` (children sorted by kind, operation and resource; ids replaced by positions; `Wall` and `Latency` dropped), `Stream(ctx, Filter, after ref.EventID) <-chan Event` with `Gap` on overflow of a bounded per-consumer buffer, `NextID()` and `Seek(next)` for the snapshot manifest, `EncodeNDJSON`/`DecodeNDJSON`, `Estimate(Event) int` bytes; eviction by bytes and count; indexes by trace, resource ARN, operation, request id.

Acceptance:
- rapid: `Query` results equal a brute-force scan over random event sets and filters; eviction keeps the newest events and the byte total under `MaxBytes`.
- `Stream` resumes exactly after `after`; a consumer that stops reading receives one `Gap{Missed: n}` and the emulator-side `Begin` never blocks (asserted with a timeout under synctest).
- `Canonical()` of two trees that differ only in ids, `Wall` and `Latency` is equal.
- NDJSON encode then decode of every `Detail` variant is identical.

Verify: `go test -race ./internal/journal/...`, `golangci-lint run ./internal/journal/...`.

## Wave 4: tracer bullet, second half

### T16 Kernel, edge, server and the SQS tracer operations (opus)

Owns: `internal/kernel`, `internal/kernel/kerneltest`, `internal/edge`, `internal/server`, `cmd/understudy`, `internal/service/sqs`.

Spec (ARCHITECTURE.md 3.2, 3.4, 3.5, 4, 4.1, 8, 9, 13 `Stateful`, 16 `WorkKind`, 17 health, 20; ADR 0003, 0004, 0005, 0006 and amendments; `sqs.md 5.1, 5.2, 6.1, 7.1, 7.2, 13`; `wire-json-query.md 2`; `go-toolchain.md 5`):
- `kernel`: `Kernel`, `Options{Registry, Clock, IDs, Journal, Faults FaultStage, Access AccessRecorder, Identity, Gate, Deliver DeliverSink, Linker, Log, Strict bool}` (interfaces defined here so `outbox` and `lambda` plug in later; a nil `DeliverSink` makes `Deliver` an `Internal` error), `New`, `Run(ctx) error` owning the scheduler driver, the timer dispatch, tracked goroutines and stream writers through `sync.WaitGroup.Go`, `Gate`, `Clock`, `IDs`, `Waiters`, `Timers().Handle(kind, fn)`, `Go(kind WorkKind, fn func(ctx))` with panic recovery, `Settle(ctx, timeout) (Pending, error)`, `Bindings` with `Status(op)`, `Binder`, `Bind`, `BindRaw`, `BindRawHTTP`, `WithValidator`, `Op`, `NewOp`, `Call` with accessors and `Touch`, `NewCall(External...)` for the edge, `Decoded`, `Result`, `Dispatch` (steps 6 to 12 of ARCHITECTURE.md 4 including the unbound 501, rejected decode, `(nil, nil)` and `(out, err)` mapping, panic recovery, `Strict` zero-timestamp check hook for encoders), `Invoke` (returns `Internal` while holding; `Target.At` stamping; `PrincipalMode` resolution through `Identity`), `Target`, `PrincipalMode` sum, `Write`, `Tx`, `TimerKind`, `WorkKind`, `Stateful`, `Late`, `ResetScope`, `Resource`, `ResolveScope`, `ScopeRef` sum, `NotImplemented`, `Validate` (per-member identity selection from the generated descriptors), `Catalogue` implementation for `fault`, `ServiceDeps`. Timer callbacks get a synthetic `Call` with `Origin Internal{Via: Timer}`. `Arm`/`Disarm` apply inside the critical section; `Wake` and `Deliver` after unlock; effects counted by the tracker.
- `kerneltest`: `Run(t, func(t *testing.T, k *K))` inside `synctest.Test` with a frozen clock by default, `K{Kernel, Clock, Journal, Faults, Stub(op, handler), Invoke(...)}`, cleanup that stops `Run`.
- `edge`: `Handler` implementing steps 1 to 5 and 13 with `Options{Kernel, Registry, Identity, Codecs map[ref.ProtocolID]Codec, BucketExists func(string) bool, S3BaseDomains, Listener ref.ListenerKind, MaxHeaderBytes}`; the `Codec` interface (`Claim`, `DecodeHead`, `DecodeBody`, `Encode`, `WriteError`); the claim order of 4.1 in full (rules 1 to 9 including the F7 fallthrough rule; rules whose codec or route table is absent decline with a reason); streaming head and body split; counting-reader caps; `Unclaimed` journal event; response headers (`Date` wall, request ids, S3 `x-amz-id-2`); the `RequestURI` and `EscapedPath` discipline of `go-toolchain.md 5`.
- `server`: `Config{Listen netip.AddrPort, ClockStart, Seed, LogFormat, LogLevel, ReadyFile}` (the full `config` package is T44; this is the serve subset), `New(Config) (*Server, error)`, `Serve(ctx) error` (listener on `127.0.0.1:0` allowed, `--ready-file` JSON `{"endpoint", "pid"}`, startup banner through `slog`, graceful shutdown), `wire.go` constructing the registry, kernel and the SQS service only, `health.go` serving `GET /_understudy/v1/health` with the readiness contract of ARCHITECTURE.md 17 (503 with `waiting` until ready, then 200 with version, uptime, services, model pin).
- `cmd/understudy`: `serve` with flags `--listen`, `--clock running|frozen|frozen@RFC3339`, `--seed`, `--ready-file`, `--log-format`, `--log-level`, and `version`.
- `service/sqs`: `State` (queues by name with attributes, messages as immutable records with enqueue event ids, visibility and receipt handles), `CreateQueue` (idempotency and attribute conflict per `sqs.md 5.1`, name validation in `validate.go`), `GetQueueUrl` (URL echoes the request `Host` per ADR 0013; cross-account by path), `SendMessage` (`MD5OfMessageBody`, delay, size limit 256 KiB, message id), `ReceiveMessage` (the long-poll reference of ARCHITECTURE.md 8, visibility timer kind, `MaxNumberOfMessages`, `WaitTimeSeconds`, `VisibilityTimeout`), `DeleteMessage` (receipt handle validity and the `InvalidParameterValue` texts of `sqs.md 7.2`), `Stateful` with `Rearm`, `Resources`, `Switches`, `Options` stub. Every other SQS operation stays unbound.

Acceptance:
- `internal/server` test: start on `127.0.0.1:0`, send `CreateQueue`, `SendMessage`, `ReceiveMessage` and `DeleteMessage` as raw awsJson1_0 HTTP requests with a SigV4 header built by `auth.Presign`-style helpers, assert bodies and headers byte for byte against goldens from `sqs.md 2.1`, assert the journal trace through `Journal.Trace`, assert `GET /_understudy/v1/health` is 503 before `Serve` accepts and 200 after.
- `cmd/understudy` test builds the binary, runs `serve --listen 127.0.0.1:0 --ready-file`, reads the file, hits health within 250 ms of process start, sends SIGINT and sees a clean exit.
- synctest: a long poll on a frozen clock returns at `WallCap`; a `SendMessage` wakes a parked `ReceiveMessage`; visibility expiry re-delivers under `Advance`; a `DeleteMessage` after a receive produces no ghost timer (effects order test); a timer whose work blocks forever does not stall a later timer; a nested `Write` and an `Invoke` while holding return `Internal` naming both operations; two kernels bound with different `SendMessage` handlers run concurrently under `-race`; `Settle` returns pending work grouped by kind.
- Wire goldens for the claim rules: every row of the F7 table, `X-Amz-Target` with and without charset, `Smithy-Protocol` plus `X-Amz-Target` rejected, a path with `%2F` and `..` reaches the handler unchanged.

Verify: `go test -race ./internal/kernel/... ./internal/edge/... ./internal/server/... ./cmd/... ./internal/service/sqs/...`, `golangci-lint run ./internal/kernel/... ./internal/edge/... ./internal/server/... ./cmd/... ./internal/service/sqs/...`.

### T17 Snapshot archive format and diff (opus)

Owns: `internal/snapshot`.

Spec (ARCHITECTURE.md 13, 12; ADR 0009 and 0018 amendments):
- `Manifest{Format int, Writer string, ModelPin string, Clock {Mode, At}, Seed *uint64, Positions map[ids.Kind]uint64, JournalNext ref.EventID, Sections []SectionMeta{Path, Version, SHA256}}`, `Archive{Manifest, Sections []store.Section, Faults []byte, Journal io.Reader (optional), Blobs BlobSource}`, `Write(w, Archive, Options{Gzip bool}) error` producing the deterministic tar of ARCHITECTURE.md 13 (sorted entries, mtime 0, uid and gid 0, mode 0644, gzip with empty name and zero mtime), `Read(r, Options{MaxBytes}) (*Archive, error)` validating every entry name with `filepath.IsLocal` and refusing a newer `Format` or an unknown section version with messages naming both versions and the writer, `Diff(a, b *Archive, Normalize bool) []Difference` section by section with the closed normalizer set (request ids, receipt handles, wall-domain timestamps, hosts, account ids, event ids), `Source` interfaces (`Exporter`, `LateExporter`) and a `Coordinator{Cut(gate, exporters) (Archive, error); WriteAfterCut; Load(...)}` implementing the two-phase save and the mirrored load against the `store.Gate`.

Acceptance:
- Writing the same `Archive` twice yields identical bytes, with and without gzip; rapid: write then read then write is byte-identical over random sections and blobs.
- `Read` rejects `../x`, absolute names and a `Format` of `1` when the reader is `0`, each with the documented message.
- `Diff` goldens: a changed queue attribute, an added bucket, and two archives equal under `Normalize` but not without it.
- `Coordinator.Cut` holds the gate for the duration of `Export` calls only (asserted with a blocking writer that proceeds after the cut).

Verify: `go test -race ./internal/snapshot/...`, `golangci-lint run ./internal/snapshot/...`.

### T18 Lambda execution runtime: Runtime API, pool, launcher, code store (opus)

Owns: `internal/service/lambda/runtimeapi`, `internal/service/lambda/pool`, `internal/service/lambda/pool/pooltest`, `internal/service/lambda/launcher`, `internal/service/lambda/codestore`.

Spec (ARCHITECTURE.md 14.1 to 14.3, 20; ADR 0010, 0011 and amendments; `containers.md 1, 3, 4.5, 4.6, 5, 6, 7, 9, 11`; `lambda-api.md 9`; `lambda-runtime-contract.md 3, 4`; `gap-G4.md`):
- `runtimeapi.Server`: one `net/http` listener per environment on the topology's bind address and an ephemeral port; `GET /2018-06-01/runtime/invocation/next` blocks on the environment's invocation channel and always sends `Content-Type: application/json`, `Lambda-Runtime-Aws-Request-Id`, `Lambda-Runtime-Deadline-Ms` (wall epoch ms), `Lambda-Runtime-Invoked-Function-Arn`, `Lambda-Runtime-Trace-Id`, `Lambda-Runtime-Client-Context`, `Lambda-Runtime-Cognito-Identity`; `POST .../{id}/response` and `/error` answer 202, read chunked bodies and `Lambda-Runtime-Function-Error-Type` from `r.Trailer`; `/init/error`; late replies get `400 InvalidInvocationId`; `Invoke(ctx, env, Invocation{Payload, Deadline, ...}) (Outcome, error)` where `Outcome` is the sum `Success{Payload}`, `FunctionError{Type, Payload}`, `Timeout`, `ExitError{Code}`, `OOM`, `InitError{...}` with the AWS payload texts of `containers.md 4.6` and `lambda-api.md 8.3`.
- `launcher.Launcher`: `Start(ctx, Spec) (*Container, error)` building the container spec of ARCHITECTURE.md 14.3 (`Env`, `Mounts` never `Binds`, tar-copy fallback, memory and swap, `PidsLimit`, `nofile`, `LogConfig`, labels, `CapDrop ALL`, `no-new-privileges`, read-only code mount, `/tmp` sized from `EphemeralStorage`, `ExtraHosts` from topology, `platform` from `Architectures`), `Stop`, `Remove`, `Logs(ctx) (<-chan Line, error)` demultiplexed with timestamps, `Events` subscription with `wait` fallback, `EnsureImage(ctx, ref, platform, progress)` single-flight with digest recording, `images.go` with the pinned runtime image table (tag and digest per supported runtime: `provided.al2023`, `python3.13`, `python3.12`, `nodejs22.x`, `nodejs20.x`, `java21`, `dotnet8`, `ruby3.3`; `lambda-api.md 4.2`), the arch-mismatch mapping (`exec format error` or exit 126 to `ArchMismatch{Declared, Host}`), `Sweep(owner)`.
- `pool.Pool`: `EnvState` sum and `Key` as ARCHITECTURE.md 14.2, `Acquire(ctx, key, spec) (*Env, error)` with exclusive reservation, start semaphore (default 16), per-function idle cap `max(4, CPUs)`, global cap with LRU eviction, idle timeout on monotonic time (default 10 minutes), generation bumps draining old environments, `Release(env, outcome)`, `Drain(key | all)`, `Len`, crash detection from events; `pooltest.FakeLauncher` and `pooltest.FakeRuntime` for later service tests.
- `codestore.Store`: `PutZip(ctx, r, Options{MaxUnzipped: 262144000, MaxEntry}) (Code{Sha256, Dir, Size}, error)` under an `os.Root`, names `filepath.IsLocal`, symlinks rejected, modes normalized (`bootstrap` 0755), content-addressed by `CodeSha256`, `Release(sha)`, `Dir(sha)`.

Acceptance:
- Unit tests against `dockertest.Daemon` and an in-process runtime client simulator for every `Outcome` variant, late replies, the trailer read, the `Content-Type` invariant, pool state transitions as a rapid state machine against `pooltest.FakeLauncher` (reservation exclusivity, idle reaping, generation drain, caps), zip-slip rejection (`../`, absolute, symlink, oversized).
- `-tags docker`, labeled `understudy.dev/test=1` with unique names and removal: a `provided.al2023` function whose `bootstrap` is a shell script using the Runtime API returns a payload; a `python3.13` function with a handler module runs; warm reuse (second invoke has no `INIT_START`); `Unhandled` function error with `X-Amz-Function-Error` semantics in the `Outcome`; `Runtime.ExitError`; timeout kills the container and the next invoke is cold; OOM with a 128 MB limit; two concurrent invokes use two environments; a `/next` without `Content-Type` is impossible (regression test on the handler); crash recovery: `kill -9` the test process's child container and `Sweep` removes it; the G4 deadline test (`get_remaining_time_in_millis()` within 100 ms of the timeout after the clock is frozen then advanced, since the deadline is wall time).

Verify: `go test -race ./internal/service/lambda/runtimeapi/... ./internal/service/lambda/pool/... ./internal/service/lambda/launcher/... ./internal/service/lambda/codestore/...`, `go test -race -tags docker ./internal/service/lambda/...`, `golangci-lint run ./internal/service/lambda/...`.

## Wave 5: broadening and platform

### T19 Generator over all twelve models, protocol-test corpus, IAM table, budgets (opus)

Owns: `internal/codegen`, `internal/cmd/gen`, `internal/cmd/vendor`, `internal/cmd/budgets`, `third_party/smithy-protocol-tests`, `third_party/aws-service-reference`, `codegen/services/*.json`, `codegen/overlays/common.json`, `codegen/overlays/*.json` (initial per-service files), `internal/api/generate.go`, `internal/api/*`, `internal/protocoltest/gen`, `internal/service/sqs` (call-site fixes for renamed generated symbols only), `.github/workflows/ci.yml` (add `generate-check` and `budgets` jobs).

Spec (ARCHITECTURE.md 5 in full; ADR 0001, 0002, 0016 and amendments; `smithy-models.md 2.2, 4, 6.2, 8, 9`; `smithy-protocol-tests.md 2, 3, 5, 7`; `wire-rest.md 1, 2.2, 3.2`; `wire-cbor.md 6, 7`; `error-matrix.md 5.1 to 5.13, 7`; `lambda-api.md 2, 4.1`):
- Emit every shape kind the corpus uses (documents, `byte`, `short`, `float`, `bigInteger`, `bigDecimal`, `intEnum`, sparse collections, unions with every member kind), every HTTP binding into `schema_gen.go` and `routes_gen.go` (Lambda routes by specificity; S3 rows from the model plus `routeOverrides` in `codegen/services/s3.json` per `wire-rest.md 3.2` and ARCHITECTURE.md 5.5), XML and Query traits, `httpResponseCode`, `successCode`, `queryKeyAlias`, `emptyCollection`, `messageByProtocol`, `rawOperations` (Lambda `InvokeFunctionUrl`; SNS `ConfirmSubscriptionLink`, `UnsubscribeLink`, `SigningCert`) as `Kind: Raw` descriptors with `BindRawHTTP` binder methods, role-based serde direction, streaming members as `smithy.Body`, the `Strict` zero-timestamp hook, `redact_gen.go` from `sensitive` plus overlay additions, `examples_gen_test.go` for the 260 examples across the seven services that have them.
- `third_party/smithy-protocol-tests/model.json`: produce once with the self-contained Smithy CLI 1.74.0 release (bundled runtime; no system Java) in a fresh temp directory per `smithy-protocol-tests.md 5`, record `PIN.json` (Smithy version, the six jar sha256 values, the CLI archive sha256), check in the flattened AST. `cmd/vendor -verify` covers it.
- `third_party/aws-service-reference/actions.json`: derived through `cmd/vendor -service-reference` from the Service Reference JSON for the twelve services (`Operations[].AuthorizedActions`, `Actions[].Annotations`) with provenance per service (`Version`, `modified`, body sha256); the raw JSON is not vendored. The generator fills `Operation.IAM` from it with the same-name or mapped action first and `*Batch` operations mapped to their single-item action; CloudWatch uses the `cloudwatch` prefix.
- `codegen/services/<svc>.json` for all twelve (`rawBinding: true` for DynamoDB; `extraProtocols` for SQS awsQuery and CloudWatch's three protocols are in the model), `codegen/overlays/common.json` complete (the generic vocabulary per protocol family, `unknownOperationError` and `malformedRequestError` defaults per family from `error-matrix.md 3, 7`), and an initial `codegen/overlays/<svc>.json` per service holding only what generation needs to succeed and the service-wide traits (`errorNamespace`, `throttleError`, `validationError` default, `validationPathStyle`, `unknownOperationError`, `malformedRequestError`, KMS `statusOverride`, Lambda `maxRequestBytes` for `CreateFunction`, the twenty S3 unmodeled error shapes of `wire-rest.md 2.4`, the synthetic `RestJson` overlay for `SerializationException`). Service tasks extend their own overlay file from wave 6.
- `internal/protocoltest/gen/<testsvc>/`: types, schema, serde and binders for the synthetic services listed in ARCHITECTURE.md 5.6 plus `cases_gen_test.go` tables (request, response and expanded malformed cases as typed literals; `appliesTo: client` cases excluded).
- `internal/cmd/budgets`: builds the stripped binary, measures its size, starts it with `--listen 127.0.0.1:0 --ready-file`, times the first 200 from `/health`, reads RSS from `/proc/<pid>/status`, measures `go build ./...` with a cold cache in a temp `GOCACHE`, and fails on any ceiling of ARCHITECTURE.md 5.7; prints a table.

Acceptance:
- `just generate-check` is clean; every `internal/api/<svc>api` and `internal/protocoltest/gen/*` compiles; `golangci-lint` reports nothing (generated files excluded, hand-written helpers clean).
- `examples_gen_test.go` passes for all 260 examples; every emitted pattern compiles; the regex differential test (random strings against an ECMA semantics table for `\s`, `\w`, `\d`, `.`) passes.
- `cmd/vendor -verify` validates models, corpus and the actions table offline.
- `just budgets` passes every ceiling; the numbers are printed in the task report.
- `internal/service/sqs` tests still pass.

Verify: `just generate-check`, `go test -race ./internal/codegen/... ./internal/cmd/... ./internal/api/... ./internal/protocoltest/gen/... ./internal/service/sqs/...`, `just budgets`, `golangci-lint run ./internal/codegen/... ./internal/cmd/... ./internal/api/... ./internal/protocoltest/...`.

### T20 awsQuery codec and form encoding (opus)

Owns: `internal/protocol/form`, `internal/protocol/awsquery`.

Spec (ARCHITECTURE.md 6.2 awsquery row, 6.3; `wire-json-query.md 7, 8, 9, 10, 11`; `iam-sts-kms.md 1.2`; `sns.md 1`; `sqs.md 2.3`; `smithy-models.md 3.1` namespaces):
- `form`: `Parse(body or query) (Values, error)` with `+` and `%XX` decoding, duplicate keys, numeric index ordering (`member.10` after `member.2`), empty list keys; `Encode` for tests.
- `awsquery.Codec`: claim on form media type (parameters ignored) or GET with `Action`, service from `(Action, Version)` confirmed by signing name; decode through a `smithy.Deserializer` over `form.Values` honouring `xmlName`, `xmlFlattened`, `queryKeyAlias`, nested member paths and maps (`entry.N.key`); encode `<OpResponse xmlns><OpResult/><ResponseMetadata><RequestId/></ResponseMetadata></OpResponse>` through `xmlw` (Result whenever the op has output; envelope even for `Unit`; lowercase booleans; millisecond timestamps; percent-encoded IAM policy documents with `+` as `%2B`; `emptyCollection` per service; SQS legacy namespace `http://queue.amazonaws.com/doc/2012-11-05/` from the retained traits); errors `ErrorResponse/Error{Type, Code, Message}` plus `RequestId`, `Code` from `QueryCode`, status from `Identity.Status`, no `xmlns` for SNS, `xmlns` for CloudWatch, `messageByProtocol`; SQS legacy Query also on `/<account>/<queue>` paths.

Acceptance:
- rapid: form index permutations decode to the same value; encode then decode round trip over hand-built schemas covering lists, flattened lists, maps, nested structures, timestamps and booleans; decoding random bytes never panics.
- Goldens byte for byte from `wire-json-query.md 8.1, 8.4, 8.5, 9.1`, `sns.md 1` (an SNS error without `xmlns`), `iam-sts-kms.md 1.2` (a `GetRole` response with an encoded policy document), `sqs.md 2.3` (a legacy `SendMessage` request and response).

Verify: `go test -race ./internal/protocol/form/... ./internal/protocol/awsquery/...`, `golangci-lint run ./internal/protocol/form/... ./internal/protocol/awsquery/...`.

### T21 REST binding, restJson1 and restXml codecs (opus)

Owns: `internal/protocol/restbind`, `internal/protocol/restjson`, `internal/protocol/restxml`.

Spec (ARCHITECTURE.md 6.1, 6.2 restjson and restxml rows; `wire-rest.md 1, 2, 3.3, 3.4, 3.6 to 3.8, 4.1, 4.2, 4.4, 4.5`; `s3.md 5.1, 9.1`; `lambda-api.md 2.1`; `sigv4-checksums-endpoints.md 6.3`):
- `restbind`: split a request into label, query, query-params map, header, prefix-header, payload and trailer members per `HTTPBinding`; labels percent-decoded once with `/` preserved in greedy labels and dot segments verbatim; query lists and maps; header lists, timestamps (HTTP-date default, `epoch-seconds`, `date-time`), booleans, base64 for `mediaType` strings; trailer-bound members as deferred values resolved by the body `smithy.Body` at EOF; response side: status from `httpResponseCode` members or `Output.Status`, headers, prefix headers, payload blobs and structures, streaming `BodySource`.
- `restjson.Codec`: Lambda: body for unbound members, `X-Amzn-Errortype: <Shape>` and a body with the model's exact keys (`Type`, `Message` or `message`), `Invoke` accepting any `Content-Type` and an empty body, malformed bodies to the service's `malformedRequestError`, 415 and 406 handling per the corpus.
- `restxml.Codec`: S3 and S3 Control: request bodies parsed on local names through `xmlw.Decode`, responses through `xmlw` (`xmlns` on success roots, `xsi:type` grantees, flattened lists, `encoding-type=url` applied to every key field when requested, HEAD status-only); `<Error>` root with `Code`, `Message`, context elements from `Fields`, `RequestId`, `HostId`; unwrapped `GetBucketLocation`; the 200-with-error path for `CompleteMultipartUpload`, `CopyObject`, `UploadPartCopy` (`Output.Status` 200 with an error body and the `x-amz-request-id` pair).

Acceptance:
- rapid: bind then unbind round trip over hand-built bindings with labels (including greedy with `/` and `%2F`), query lists, header timestamps and prefix headers; random bytes never panic either decoder.
- Goldens byte for byte: an S3 `<Error>` for `NoSuchBucket` with `BucketName`, a `ListBucketResult`, a `GetBucketLocation` body, a Lambda `ResourceNotFoundException` body and headers, an `Invoke` response with `X-Amz-Function-Error` and `X-Amz-Executed-Version`, a `CompleteMultipartUpload` 200-with-error body.
- A trailer-bound member read before EOF returns "not yet available"; after EOF it returns the trailer value.

Verify: `go test -race ./internal/protocol/restbind/... ./internal/protocol/restjson/... ./internal/protocol/restxml/...`, `golangci-lint run ./internal/protocol/restbind/... ./internal/protocol/restjson/... ./internal/protocol/restxml/...`.

### T22 CBOR subset and the rpcv2Cbor codec (opus)

Owns: `internal/protocol/cbor`, `internal/protocol/rpcv2cbor`.

Spec (ARCHITECTURE.md 6.2 rpcv2cbor row; `wire-cbor.md 3, 5, 6, 7, 8`; `gap-G1.md`; `smithy-models.md 3.2`):
- `cbor`: decoder for major types 0 to 7, tag 1 timestamps, indefinite lengths, half, single and double floats, non-minimal widths, depth cap 128, declared-length caps, typed `Malformed` errors, never panics; encoder with definite minimal lengths, canonical widths, tag 1 for timestamps, `nil` for null; a `Value` ADT and streaming `Reader`/`Writer` suited to `smithy.Serializer`.
- `rpcv2cbor.Codec`: claim on `Smithy-Protocol: rpc-v2-cbor` with `/service/{Name or dotted form}/operation/{Op}`, reject `X-Amz-Target`, `Content-Type` and `Accept` `application/cbor`; empty body for `Unit` outputs; errors never empty with `__type` as the absolute shape id; `Smithy-Protocol` on every response; `x-amzn-query-error` for CloudWatch; the malformed and unclaimed behaviors of `wire-cbor.md 3.9`.

Acceptance:
- Vectors G01 to G24 (G22 to G24 negative) and S01 to S13 pass byte for byte.
- rapid: encode then decode round trip over random `Value`s; the decoder never panics on mutated input; all 65,536 half-float bit patterns decode to the IEEE value or a typed error.
- A request with both `Smithy-Protocol` and `X-Amz-Target` is rejected with the documented identity.

Verify: `go test -race ./internal/protocol/cbor/... ./internal/protocol/rpcv2cbor/...`, `golangci-lint run ./internal/protocol/cbor/... ./internal/protocol/rpcv2cbor/...`.

### T23 Outbox delivery engine (opus)

Owns: `internal/outbox`.

Spec (ARCHITECTURE.md 16, 13; ADR 0015 and amendments; `sns.md 7.1 to 7.4`; `secrets-logs-events-alarms.md 4.5`; `lambda-events.md 4, 5.5, 6`; `s3.md 13.2`):
- `Outbox` implementing `kernel.DeliverSink` (`Enqueue(req delivery.Request, cause ref.Cause, accepted clock.Instant) ids.DeliveryID`) and `kernel.Stateful`: `Partitioned[aws.Scope, State]` of `Entry` records as ARCHITECTURE.md 16; one `TimerKind` (`outbox.attempt`); a due attempt is `Kernel.Go(DeliveryAttempt)` work stamped `Target.At = due` that calls `sqsapi.OpSendMessage`, `lambdaapi.OpInvoke` (`InvocationType` from the target) or `snsapi.OpPublish` through `kernel.Invoke` as `AsService{name}` with the mechanism's `Via`; `Phase: Delivery` fault evaluation before each attempt (`Drop` is terminal); `journal.Delivery` events with the source as parent; retry schedules: `SNSManaged` (3 immediate, 2 at 1 s, 10 exponential 1 s to 20 s, then 100,000 at 20 s; `sns.md 7.2`), `EventBridgeTarget{MaxAge 24h, MaxAttempts 185}` exponential with jitter from the `Jitter` ids kind, `S3Notification` (a few quick retries then drop), `None`; client-side failures (`QueueDoesNotExist`, `ResourceNotFoundException`) go straight to the DLQ when one is set (SNS DLQ envelope per `sns.md 7.4` through `sqsapi.OpSendMessage`), else `Dropped`; `Rearm` re-arms every pending entry; `Resources` lists pending deliveries; `Advise` hook through `access.Recorder` with the target's stored policy supplied by a `PolicySource` interface (`wire.go` plugs the SQS, Lambda and SNS services in during wave 9; a nil source skips advisories).

Acceptance (all through `kerneltest` with stub target handlers):
- A stub `SendMessage` that fails `n` times then succeeds is retried on the `SNSManaged` schedule exactly (timestamps asserted under `Advance`), and the retry storm replays identically with the same seed.
- A missing queue goes to the DLQ at once with the SNS DLQ envelope; a `Delivery`-phase `Drop` rule produces a `Dropped` event and no attempt.
- Snapshot round trip with pending entries then `Rearm` fires them at the right instants; `Settle` counts a pending attempt and returns once it completes.
- An `Advise` `WouldDeny` produces an `Advisory` event and the delivery is still attempted.

Verify: `go test -race ./internal/outbox/...`, `golangci-lint run ./internal/outbox/...`.

### T24 Admin API, public admin package, service stubs and wiring (opus)

Owns: `admin`, `internal/server/admin`, `internal/server` (all files including `wire.go`), `internal/service/{sns,s3,dynamodb,lambda,logs,secretsmanager,kms,iam,sts,events,cloudwatch}/service.go` and `options.go` (stubs only).

Spec (ARCHITECTURE.md 17, 13, 16 settle, 20; ADR 0017 and amendments):
- `admin`: wire types for every endpoint of ARCHITECTURE.md 17 (`Health`, `Reset`, `ClockState`, `ClockOp`, `FaultRule` mirroring `fault.Rule` as JSON, `JournalEvent`, `Trace`, `SnapshotInfo`, `Resource`, `Usage`, `Policy`, `Settle`), `ErrorCode` enum with `ExitCode()` table, `Error` type, `Client` (`New(endpoint, Options{Token, HTTPClient})`, one method per endpoint, NDJSON stream as an iterator, typed errors from the envelope).
- `internal/server/admin`: handlers on `/_understudy/v1/` for `health` (moved from T16's `health.go`, same contract), `reset` (all or by services, accounts, regions; drains through a `Drainer` interface the Lambda service implements later; clock and faults kept unless asked), `clock` (freeze, resume, advance with the fired timer list, set forward-only, scale), `faults` (list, put with validation errors as `invalid_request` carrying the catalogue, delete, clear), `journal/events`, `journal/stream` (chunked NDJSON, `after`), `journal/traces/{id}` with `request_id` and `canonical`, `snapshots` (POST returns the archive through the two-phase coordinator; PUT loads; `{"path"}` under the data dir when persistence is on), `resources`, `coverage` (left for T25; returns `unavailable` until then), `iam/usage`, `iam/policy`, `settle`, `lambda/functions/{name}/hot-reload`, `lambda/images/pull`, `dynamodb/ttl-sweep`, `sns/subscriptions/{arn}/token` (the last four call interfaces the services implement later; stubs answer `unavailable` with a hint); bearer-token middleware for `Exposed{Token}`; the container listener serves only `health` and `clock`.
- `internal/server`: `wire.go` constructs the registry over all twelve generated service descriptors, the kernel with `outbox` as the `DeliverSink`, every codec registered by `ref.ProtocolID`, every service through `New(kernel.ServiceDeps, Options{})` (SQS real, the eleven others as stubs), the `Stateful` and `Late` lists, `Switches`, and the admin mux; `Server.Config` gains `Persist`, `Admin Token`, `TLS` placeholders that T44 and T45 complete.
- Stubs: each `service.go` has `type Service struct`, `type Options struct` with documented zero values, `New(d kernel.ServiceDeps, o Options) *Service` binding nothing, `Stateful` methods over an empty state, `Switches() []fidelity.Entry` returning nil.

Acceptance:
- `httptest` per endpoint with the SQS service: clock freeze, advance and the fired list; a fault rule put with an invalid shape returns `invalid_request` listing SQS codes; journal query and stream resumption; snapshot save then reset then load restores a queue and its messages; `resources` lists the queue ARN; `settle` returns 200 with no pending work; health readiness.
- `admin.Client` round-trips every endpoint against the test server; an `ErrorCode` maps to its exit code through the single table.
- A non-loopback listener without a token refuses to start with the documented error; with a token every admin call without `Authorization` returns 401 with code `unauthorized`.

Verify: `go test -race ./admin/... ./internal/server/... ./internal/service/...`, `golangci-lint run ./admin/... ./internal/server/... ./internal/service/...`.

## Wave 6: protocol corpus, coverage and the first service wave

### T25 Protocol compliance harness, baseline ratchet and coverage report (opus)

Owns: `internal/protocoltest` (except `gen`), `internal/protocol/*` (fixes), `codegen/overlays/common.json`, `internal/coverage`, `docs/coverage.md`, `internal/server/admin/coverage.go`, `.github/workflows/ci.yml` (add `protocoltest` baseline and `coverage-check` jobs).

Spec (ARCHITECTURE.md 5.6, 6.4, 3.4 coverage; `smithy-protocol-tests.md 2, 3, 4.2, 6, 7, 8`; `error-matrix.md` finding 2):
- Harness: for every generated case table, build a kernel with the synthetic service bound to echo handlers (request cases assert the decoded input equals the case's `params`; response cases bind a handler returning the case output and compare the wire response with comparators: JSON equivalence with number normalization, XML equivalence on names resolved to namespace URI and local name (prefixes ignored) and ordering rules, CBOR structural equality, form key sets, header subsets, status, body absence); malformed cases assert status and `x-amzn-errortype`; the edge runs with every codec registered; `skips.json` entries carry `id`, `reason`, `category` (`corpus-bug`, `client-only`, `aws-differs` with an `evidence.Label` and source); `baseline.json` lists passing case ids per protocol; the test fails when a baseline case fails or when a passing case is missing from the baseline; `-update-baseline` rewrites it (`just protocoltest-baseline`).
- Codec fixes under `internal/protocol/*` as the corpus reveals them.
- `coverage`: `Report(Input{Bindings, Registry, Baseline, Skips, Switches, Overlays}) Report` with the three numbers of ARCHITECTURE.md 3.4 (never summed), `JSON()`, `Markdown()`; the admin endpoint `GET /coverage?format=`; `docs/coverage.md` generated by `just coverage-check` through the binary's offline mode (T44 adds the CLI subcommand; until then the recipe runs `go run ./internal/cmd/coverage`, which this task adds under `internal/cmd/coverage` as a thin main that builds the wiring in-process).

Acceptance:
- Baseline at or above: awsJson1_0 39 of 49, awsJson1_1 98 of 102 (one more case is not generated), awsQuery 61 of 66, rpcv2Cbor 64 of 68, restXml 164 of 178, restJson1 215 of 225 (four more cases are not generated) plus 621 of 655 malformed cases; every excluded case is in `skips.json` with a reason and category; the ratchet fails on a synthetic regression. (Wave 6 amendment: the floors first set, 49 of 49 and the like, counted cases the `corpus-bug`, `client-only` and `aws-differs` skips of ARCHITECTURE.md 6.4 exclude, so no implementation could reach them. The gap to every generated case is those skips plus 15 known failures in `internal/awserr` and `internal/codegen`, tracked in `docs/plan/DEFERRED.md`.)
- `docs/coverage.md` drift check passes; the report lists SQS operations by status, every overlay entry by label, every switch.

Verify: `go test -race ./internal/protocoltest/... ./internal/protocol/... ./internal/coverage/... ./internal/cmd/coverage/...`, `just coverage-check`, `golangci-lint run ./internal/protocoltest/... ./internal/protocol/... ./internal/coverage/... ./internal/cmd/coverage/...`.

### T26 SQS complete (opus)

Owns: `internal/service/sqs`, `codegen/overlays/sqs.json`, `codegen/services/sqs.json`, `internal/api/sqsapi`.

Spec (brief SQS row; ARCHITECTURE.md 8, 9 ESM pin, 14.8 contract; `sqs.md` all sections, especially 2.4, 3, 4, 5, 6.2, 6.3, 7, 8, 9, 10, 12, 13; `tf-messaging.md 3.1, 3.2, 4.2, 6`; `error-matrix.md 5.1, 5.2`):
- Every operation in the model bound (`Full`, or `StoredOnly` for `AddPermission`, `RemovePermission`, policy attributes): queue attributes with validation texts and limits (`sqs.md 4`, `validate.go`), `SetQueueAttributes` semantics, `ListQueues` paging, `DeleteQueue` with the `QueueDeletedRecently` switch, `PurgeQueue` with the 60 s cooldown, tags, `ListDeadLetterSourceQueues`, batch operations with per-entry results and `BatchRequestTooLong`, `EmptyBatchRequest`, `BatchEntryIdsNotDistinct`, message attributes and system attributes with the exact MD5s of `sqs.md 6.3` (vectors), `AWSTraceHeader` from `Call.Trace()`, retention timer, `ChangeMessageVisibility` and batch, receipt-handle lifecycle (`sqs.md 7.2`), FIFO queues (`MessageGroupId`, `MessageDeduplicationId`, content-based dedup, 5-minute dedup window on the clock, group in-flight blocking, `ReceiveRequestAttemptId`, `SequenceNumber`), redrive policy and `maxReceiveCount` move to the DLQ with the journal edge, redrive allow policy, `StartMessageMoveTask`, `ListMessageMoveTasks`, `CancelMessageMoveTask` on the clock with `MaxNumberOfMessagesPerSecond`, `HoldVisibility` internal operation, cross-account queue URLs through `ResolveScope`, legacy Query claims on `/<account>/<queue>` with the queue taken from the path, `Stateful` with `Rearm`, `Resources`, `Switches`.

Acceptance:
- rapid state machine over receive, delete, change-visibility, send with delay and purge against a reference model with timers (no ghost deliveries, no lost messages).
- FIFO: strict order within a group; a group with an in-flight batch yields no further messages; duplicates within the window are suppressed and return the original id.
- MD5 vectors of `sqs.md 6.3` pass; batch limits and per-entry errors match `sqs.md 6.2`.
- Redrive moves a message after `maxReceiveCount` receives with the journal edge from the receive to the DLQ send; a move task moves messages at the configured rate under `Advance`.
- Identifier forms (URL with own account, URL with another account, wrong region host) behave as `sqs.md` Implications 6.
- Snapshot round trip and `Rearm` re-fire visibility and retention timers.

Verify: `go generate ./internal/api/ && go test -race ./internal/service/sqs/... ./internal/api/sqsapi/...`, `golangci-lint run ./internal/service/sqs/...`.

### T27 SNS core: topics, subscriptions, publish, envelopes, links (opus)

Owns: `internal/service/sns` (except `filterpolicy`), `internal/service/sns/envelope`, `codegen/overlays/sns.json`, `codegen/services/sns.json`, `internal/api/snsapi`.

Spec (brief SNS row; ARCHITECTURE.md 16; ADR 0015 amendments; `sns.md 1 to 5, 7, 8, 9`; `tf-messaging.md 3.3 to 3.5`; `tf-remaining.md 9`; `lambda-events.md 4`):
- Topics (`CreateTopic` idempotency and attribute conflicts, FIFO topics, attributes of `sns.md 2.2`, tags, permissions stored), subscriptions (`Subscribe` for `sqs`, `lambda` confirmed at once; `http`, `https`, `email`, `email-json` pending with a readable token; `sms`, `application`, `firehose` answered with the unimplemented AWS-shaped error of ADR 0015; `ConfirmSubscription`; `Unsubscribe`; subscription attributes of `sns.md 3.3` including `RawMessageDelivery`, `RedrivePolicy`, `FilterPolicy` and `FilterPolicyScope` stored as strings in this wave; listing), `Publish` and `PublishBatch` with the input rules of `sns.md 4` and message attributes, fan-out as `Deliver` effects in subscription-creation order (SQS raw or enveloped, Lambda `Records[0].Sns`, FIFO topic to FIFO queue with group and dedup ids), the envelope builders of `envelope` (SQS envelope key order of `sns.md 5.1`, FIFO variant without signature fields, Lambda event casing, signature version 1 with one RSA key per account-region from `crypto/rand` persisted in state), the raw operations `SigningCert`, `ConfirmSubscriptionLink`, `UnsubscribeLink` under `/_understudy/`, `Invalid parameter: TopicArn` for another region through `ResolveScope`, `Stateful`, `Resources`, the admin token endpoint through the interface T24 defined.

Acceptance:
- Envelope goldens byte for byte for `sns.md 5.1 to 5.4`; the signature verifies against the served certificate.
- Through `kerneltest` with a stub `SendMessage`: a publish to a topic with three subscriptions produces three outbox entries in creation order; raw delivery carries attributes as SQS message attributes; a FIFO publish carries the group id.
- Pending `http` subscription: token readable, `ConfirmSubscription` confirms, the unsubscribe link works through the edge.
- Terraform sequences of `tf-messaging.md 3.3 to 3.5` replay through `Invoke` with empty-diff reads.

Verify: `go generate ./internal/api/ && go test -race ./internal/service/sns/... ./internal/api/snsapi/...`, `golangci-lint run ./internal/service/sns/...`.

### T28 S3 buckets, subresources, addressing and S3 Control tags (opus)

Owns: `internal/service/s3` (`service.go`, `state.go`, `options.go`, `bucket.go`, `subresources.go`, `tags.go`, `control.go`, `resolve.go`, `validate.go`, `errors.go`, stub files `objects.go`, `multipart.go`, `notification.go` with `bind*` stubs and the `commitObject` and `notify` seams), `codegen/overlays/s3.json`, `codegen/services/s3.json`, `internal/api/s3api`.

Spec (brief S3 row; ARCHITECTURE.md 4.1, 7, 8 rule 3; ADR 0013 and amendments; `s3.md 1, 2, 3, 4, 11.2, 14`; `tf-storage.md 1, 2.1 to 2.6, 7`; `wire-rest.md 3.1, 3.2, 3.5, 3.6, 3.8`; `error-matrix.md 5.4`):
- Index cell (bucket name to home region and account) plus one cell per bucket; `CreateBucket` (naming rules, `BucketAlreadyOwnedByYou`, `BucketAlreadyExists`, `IllegalLocationConstraintException`, `LocationConstraint` rules), `HeadBucket`, `GetBucketLocation`, `ListBuckets`, `DeleteBucket` (`BucketNotEmpty`); every subresource Terraform reads with its exact not-configured error or default document (`tf-storage.md 2.2, 2.4`, `s3.md 4.1, 4.2`): versioning, acl, cors, website, policy, logging, lifecycle, replication, encryption (the default SSE-S3 document), object lock, request payment, accelerate, public access block, ownership controls, tagging (`NoSuchTagSet`), notification (empty document in this wave), metrics, analytics, inventory, intelligent tiering; `Put*` and `Delete*` of each with `successCode` where AWS uses 204; S3 Control `ListTagsForResource`, `TagResource`, `UntagResource` on `<account>.<base>` hosts sharing the bucket tag store; cross-region behavior and `RegionSource None` per ADR 0013 with the `s3.crossRegion` switch; `BucketExists(name) bool` exported for the edge; `validate.go` for names and headers; every object-level operation stays unbound in this wave through the stubbed families.

Acceptance:
- For a fresh bucket, every `Get*` subresource returns the exact code, status and body of `tf-storage.md 2.4` (table test over the 14 reads); after a `Put*`, the `Get*` echoes it and a `Delete*` restores the not-configured answer.
- Cross-region matrix: SigV4 for the wrong region gives `AuthorizationHeaderMalformed` with `Region`; an unsigned request with a host region gives `PermanentRedirect`; `RegionSource None` serves the bucket with `x-amz-bucket-region`; `lenient` serves everything with a journal warning.
- S3 Control tag round trip on the account host; bucket tags visible through both APIs.
- `CreateBucket` in a second account for an existing name returns `BucketAlreadyExists`.

Verify: `go generate ./internal/api/ && go test -race ./internal/service/s3/... ./internal/api/s3api/...`, `golangci-lint run ./internal/service/s3/...`.

### T29 KMS and Secrets Manager (opus)

Owns: `internal/service/kms`, `internal/service/secretsmanager`, `codegen/overlays/kms.json`, `codegen/overlays/secretsmanager.json`, `codegen/services/kms.json`, `codegen/services/secretsmanager.json`, `internal/api/kmsapi`, `internal/api/secretsmanagerapi`.

Spec (brief KMS and Secrets Manager rows; ARCHITECTURE.md 16 KMS forward access, 20; `iam-sts-kms.md 1.3, 5`; `secrets-logs-events-alarms.md 2`; `tf-storage.md 4, 5`; `tf-remaining.md 5, 6`; `error-matrix.md 5.8, 5.9`):
- KMS: `CreateKey` with `KeyMetadata` defaults (`iam-sts-kms.md 5.2`), key identifier resolution (`5.1`: id, ARN, alias name, alias ARN; other-region ARN error), aliases, key policies (`policydoc`), rotation status (`EnableKeyRotation`, `GetKeyRotationStatus`, rotation period stored), tags, `ScheduleKeyDeletion` and `CancelKeyDeletion` on the emulator clock with the 7 to 30 day window, `DisableKey`/`EnableKey`, the key-state matrix of `5.6` (every operation versus every state with the documented error), `Encrypt`, `Decrypt`, `GenerateDataKey`, `GenerateDataKeyWithoutPlaintext`, `ReEncrypt` with real AES-256-GCM (key material from `crypto/rand`, encryption context as AAD, ciphertext blob format with the key id embedded so `Decrypt` can resolve it), `statusOverride` per the overlay, `DescribeKey`, `ListKeys`, `ListAliases`, `GetKeyPolicy`, `PutKeyPolicy`, `ListResourceTags`.
- Secrets Manager: identifiers (`2.1`), version and stage model (`2.2`: `AWSCURRENT`, `AWSPREVIOUS`, `AWSPENDING`, version ids as UUIDs), `ClientRequestToken` idempotency (`2.3`), `CreateSecret`, `GetSecretValue` (by stage or version), `PutSecretValue`, `UpdateSecret`, `DescribeSecret`, `ListSecrets` with filters, `DeleteSecret` with the recovery window on the clock and `ForceDeleteWithoutRecovery`, `RestoreSecret`, `TagResource`, `UntagResource`, `PutResourcePolicy`, `GetResourcePolicy`, `DeleteResourcePolicy`, `UpdateSecretVersionStage`, limits (`2.6`), encryption of secret values through `kernel.Invoke(kmsapi.OpEncrypt/OpDecrypt, AsCaller, Via KMSForward)` with the account's default key when none is given, the exact Terraform sequences (`2.7`).

Acceptance:
- Key-state matrix table test; `GenerateDataKey` then `Decrypt` round trips with the context; a wrong context fails with `InvalidCiphertextException`; scheduled deletion completes under `Advance` and the key is `PendingDeletion` then gone.
- Secrets: rapid over put, stage moves and gets (current and previous always consistent); deletion then restore within the window; the recovery window elapses under `Advance`; idempotent `PutSecretValue` with the same token returns the same version.
- The access recorder, after `GetSecretValue`, holds `kms:Decrypt` for the caller.

Verify: `go generate ./internal/api/ && go test -race ./internal/service/kms/... ./internal/service/secretsmanager/... ./internal/api/kmsapi/... ./internal/api/secretsmanagerapi/...`, `golangci-lint run ./internal/service/kms/... ./internal/service/secretsmanager/...`.

### T30 IAM and STS (opus)

Owns: `internal/service/iam`, `internal/service/sts`, `codegen/overlays/iam.json`, `codegen/overlays/sts.json`, `codegen/services/iam.json`, `codegen/services/sts.json`, `internal/api/iamapi`, `internal/api/stsapi`.

Spec (brief IAM and STS row; ARCHITECTURE.md 7; ADR 0012 and amendments; `iam-sts-kms.md 2, 3, 4`; `tf-compute.md 3`; `tf-remaining.md 3`; `error-matrix.md 5.10, 5.11`):
- IAM over `Partitioned[aws.AccountID, State]`: roles (`CreateRole` with trust policy validation through `policydoc`, `GetRole`, `UpdateRole`, `UpdateAssumeRolePolicy`, `DeleteRole` with the dependency errors, `ListRoles`, tags), managed policies (`CreatePolicy`, versions, `GetPolicy`, `GetPolicyVersion`, `ListPolicyVersions`, `DeletePolicy`), inline policies (`PutRolePolicy`, `GetRolePolicy`, `DeleteRolePolicy`, `ListRolePolicies`), attachments (`AttachRolePolicy`, `DetachRolePolicy`, `ListAttachedRolePolicies`, `ListEntitiesForPolicy`), users (`CreateUser`, `GetUser`, `DeleteUser`, `ListUsers`, tags, user policies and attachments), access keys (`CreateAccessKey` minting through `identity.Store`, `ListAccessKeys`, `DeleteAccessKey`, `UpdateAccessKey`), instance profiles (`CreateInstanceProfile`, `AddRoleToInstanceProfile`, `RemoveRoleFromInstanceProfile`, `GetInstanceProfile`, `DeleteInstanceProfile`), AWS managed policy ARNs (`3.5`), the error texts of `3.4`, `iam:` ids with the documented prefixes.
- STS: `GetCallerIdentity` for every principal shape (`4.1`), `AssumeRole` (`4.2`: role must exist, session name rules, `DurationSeconds` bounds, minted `ASIA` credentials with `Expiration` on wall time, `AssumedRoleUser`), `iam.assumeRole: lenient` switch, `GetSessionToken` minimal.

Acceptance:
- The Terraform sequences of `tf-compute.md 3` and `tf-remaining.md 3` replay through `Invoke` with empty-diff reads (role, role policy, attachment, policy, user, access key, instance profile).
- `AssumeRole` then `GetCallerIdentity` with the minted key renders the assumed-role ARN; a nonexistent role returns `AccessDenied` unless lenient.
- Policy documents round trip byte-stable through `GetRolePolicy`.

Verify: `go generate ./internal/api/ && go test -race ./internal/service/iam/... ./internal/service/sts/... ./internal/api/iamapi/... ./internal/api/stsapi/...`, `golangci-lint run ./internal/service/iam/... ./internal/service/sts/...`.

### T31 CloudWatch Logs and CloudWatch metrics (opus)

Owns: `internal/service/logs`, `internal/service/cloudwatch`, `codegen/overlays/logs.json`, `codegen/overlays/cloudwatch.json`, `codegen/services/logs.json`, `codegen/services/cloudwatch.json`, `internal/api/logsapi`, `internal/api/cloudwatchapi`.

Spec (brief Logs and CloudWatch rows; ARCHITECTURE.md 6.2, 9 domain table, 14.6; `secrets-logs-events-alarms.md 1.1, 3, 5`; `logs-filter-patterns.md 6, 7`; `tf-compute.md 5, 6`; `tf-remaining.md 7`; `wire-cbor.md 9`; `gap-G1.md 4`; `error-matrix.md 5.7, 5.13`):
- Logs: groups and streams with ARNs (`3.1`), `CreateLogGroup`, `DeleteLogGroup`, `DescribeLogGroups`, `PutRetentionPolicy` and `DeleteRetentionPolicy` with expiry on the emulator clock as a timer kind, `CreateLogStream`, `DeleteLogStream`, `DescribeLogStreams`, `PutLogEvents` (`3.3`: ordering, batch limits, the 2 h future and 14 day past windows accepted against wall or emulator time, `rejectedLogEventsInfo`, sequence tokens accepted and ignored), `GetLogEvents` (`3.4`: forward and backward tokens, `startFromHead`, paging), `FilterLogEvents` (`3.5` with `logsfilter`, interleaved, `nextToken`, time range), tags (`3.7`), limits (`3.8`), the overlay internal operation `PutLogEventsInternal` bypassing the windows.
- CloudWatch: `PutMetricAlarm`, `DescribeAlarms` (by name, prefix, state), `DeleteAlarms` succeeding for missing names (`gap-G1.md 4`), `EnableAlarmActions`, `DisableAlarmActions`, `SetAlarmState`, `TagResource`, `UntagResource`, `ListTagsForResource` (`5.1`, stored), `PutMetricData` (gzip body, limits, stored series), `GetMetricData` (basic aggregation over stored points on the emulator clock), `ListMetrics`; `ValidationError` and `InvalidParameterCombination` texts in `validate.go`; served through awsJson1_0, awsQuery and rpcv2Cbor by the registry's protocol list.

Acceptance:
- Every vector of `logs-filter-patterns.md 8` matches the same way through `FilterLogEvents`; paging properties (pages concatenate to the unpaginated listing) for both get operations; retention expiry under `Advance` removes events; the window check accepts an event valid against the emulator clock but not wall.
- Alarm Terraform sequence of `tf-compute.md 6` replays with empty-diff reads; `DeleteAlarms` of a missing alarm succeeds; `PutMetricData` then `GetMetricData` aggregates `Sum` and `Average` over a period.

Verify: `go generate ./internal/api/ && go test -race ./internal/service/logs/... ./internal/service/cloudwatch/... ./internal/api/logsapi/... ./internal/api/cloudwatchapi/...`, `golangci-lint run ./internal/service/logs/... ./internal/service/cloudwatch/...`.

### T32 EventBridge (opus)

Owns: `internal/service/events`, `internal/service/events/envelope`, `codegen/overlays/eventbridge.json`, `codegen/services/eventbridge.json`, `internal/api/eventbridgeapi`.

Spec (brief EventBridge row; ARCHITECTURE.md 16; `secrets-logs-events-alarms.md 4`; `lambda-events.md 5`; `tf-compute.md 4`; `error-matrix.md 5.12`):
- Default bus only (`DescribeEventBus`, `ListEventBuses`), rules (`PutRule` with `ScheduleExpression` through `schedexpr` as a timer kind on the emulator clock, `EventPattern` through `ruler` with the EventBridge profile, `State`, `DescribeRule`, `ListRules`, `DeleteRule` with the targets-present error, `EnableRule`, `DisableRule`, tags), targets (`PutTargets` with `Input`, `InputPath`, `InputTransformer` and `RetryPolicy`/`DeadLetterConfig` stored, `ListTargetsByRule`, `RemoveTargets`, Lambda, SQS and SNS target ARNs; unknown bus drops with 200 and a journal entry), `PutEvents` (`4.7`: entries, limits, `FailedEntryCount`, the event envelope of `envelope` with version `0`, second-precision `time` unless the caller sent `Time`), scheduled events with the shape of `lambda-events.md 5.2`, matching every enabled rule's pattern against each event, fan-out to targets as `Deliver` effects with the `EventBridgeTarget` policy, `InputTransformer` semantics of `4.6`, `ListRuleNamesByTarget`, `TestEventPattern`.

Acceptance:
- Advancing 1 h across a `rate(5 minutes)` rule produces twelve outbox entries whose `Accepted` instants are 5, 10, ... 60 minutes after the rule's creation; a `cron` rule fires at the computed instants.
- `PutEvents` with three rules and patterns delivers to the matching targets only, in target creation order; `InputPath` and `InputTransformer` goldens from `4.6`; rapid: transformer output always parses as JSON when the template does.
- Terraform sequence of `tf-compute.md 4` replays with empty-diff reads.

Verify: `go generate ./internal/api/ && go test -race ./internal/service/events/... ./internal/api/eventbridgeapi/...`, `golangci-lint run ./internal/service/events/...`.

### T33 DynamoDB proxy and DynamoDB Local lifecycle (opus)

Owns: `internal/service/dynamodb` (`service.go`, `options.go`, `state.go`, `proxy.go`, `forward.go`, `rewrite.go`, `licence.go`, `validate.go`, `errors.go`, stub `shims.go`, `ttl.go`, `snapshot.go`), `internal/service/dynamodb/ddblocal`, `codegen/overlays/dynamodb.json`, `codegen/services/dynamodb.json`, `internal/api/dynamodbapi`.

Spec (brief DynamoDB row; ARCHITECTURE.md 15, 3.2 `RawHandler` and `Wire`; ADR 0014 and amendments; `dynamodb-local.md 1, 3, 4, 9, 10`; `dynamodb-local-terms.md 5`; `error-matrix.md 5.5` and finding 6):
- `ddblocal.Manager`: pinned image by digest with the ECR Public fallback, `Start(ctx)` (`-inMemory` or `-dbPath`, both telemetry opt-outs, published on `127.0.0.1:0` or attached to understudy's network when `InContainer`, labels, memory bound), readiness probe, `Stop`, `Endpoint()`, the licence state machine (`Disabled | AwaitingLicence | Accepted{Digest}`) with acceptance persisted per digest under the data dir, the notice text of `dynamodb-local-terms.md 5.1` for the 501 and the banner line, the egress test harness (`--internal` network).
- Service: every model operation bound through `BindRaw` with `Full` or `Partial`; pre-forward checks (malformed JSON `com.amazon.coral.service#SerializationException`, missing or bad target `UnknownOperationException`, wrong Content-Type 404 XML, missing auth, 413 above 16 MiB, generated validation, region validated against the table); credential rewrite to `<account>/<date>/<region>/dynamodb/aws4_request`; `FromClient` forwards the original bytes with `Accept-Encoding: identity` and `{}` for empty bodies; `Synthesized` encodes the typed input through `awsjson` (the service imports the codec); response rewrite through `document.Value` (read with `jsonx.Decoder.ReadValue`, written with `jsonx.Encoder.Value`) for ARNs and `awsRegion`, `message` casing, the message translation table (`ResourceNotFoundException` text), `x-amz-crc32` recomputed; unsupported operations `NotImplemented` before Local sees them; `ProvisionedThroughputExceededException`, `ThrottlingException`, `TransactionConflictException` available to faults through the overlay; case-insensitive name collision rejected with `ResourceInUseException`; `Stateful` for the shim table registry; shims, TTL and snapshot export are stubs for T37.

Acceptance:
- Unit tests against a fake Local (`httptest`): every pre-forward check row of `error-matrix.md` finding 6 and H7; credential rewrite; ARN rewrite; crc32 recomputation; the translation table; a `Synthesized` call encodes the same bytes a client would send.
- `-tags docker`: start the pinned image, `CreateTable`, `PutItem`, `GetItem` through `Invoke` with `FromClient` wires for two accounts and two regions and assert isolation; the licence gate returns 501 with the exact flag text when not accepted; the egress test observes zero DNS lookups and connections; the container is labeled and removed.

Verify: `go generate ./internal/api/ && go test -race ./internal/service/dynamodb/... ./internal/api/dynamodbapi/...`, `go test -race -tags docker ./internal/service/dynamodb/...`, `golangci-lint run ./internal/service/dynamodb/...`.

### T34 Lambda API and state machine (opus)

Owns: `internal/service/lambda` root files (`service.go`, `options.go`, `state.go`, `functions.go`, `config.go`, `code.go`, `versions.go`, `aliases.go`, `permissions.go`, `concurrency.go`, `tags.go`, `urls_config.go`, `esm_config.go`, `resolve.go`, `validate.go`, `errors.go`, stub files `invoke.go`, `esm_runtime.go`, `urls_runtime.go`, `logs_delivery.go`), `codegen/overlays/lambda.json`, `codegen/services/lambda.json`, `internal/api/lambdaapi`.

Spec (brief Lambda row control plane; ARCHITECTURE.md 14.3, 14.7, 9 timer classes; ADR 0010 amendments; `lambda-api.md 1 to 7, 11, 12`; `lambda-events.md 1, 7.1, 7.2`; `tf-compute.md 2`; `tf-remaining.md 4`; `error-matrix.md 5.6` and finding 3):
- `CreateFunction` (validation rows of `lambda-api.md 4.1` with the per-member identities of the overlay, runtime subset `4.2`, zip limits and `codestore`, container images through `launcher.EnsureImage` with `Pending` then `Active` or `Failed{StateReasonCode}`, defaults `4.5`), `GetFunction`, `GetFunctionConfiguration`, `ListFunctions`, `UpdateFunctionConfiguration`, `UpdateFunctionCode` (generation bump; the hot-reload advisory hook is a stub), `DeleteFunction` 204, `PublishVersion`, `ListVersionsByFunction`, aliases (`7.2`), `AddPermission`, `GetPolicy`, `RemovePermission` (`7.3` with `policydoc`), `PutFunctionConcurrency`, `GetFunctionConcurrency`, `DeleteFunctionConcurrency` (stored), tags (`7.5`), function URL config (`7.6`: `CreateFunctionUrlConfig` minting the url id, `GetFunctionUrlConfig`, `UpdateFunctionUrlConfig`, `DeleteFunctionUrlConfig`, `ListFunctionUrlConfigs`, AuthType and CORS stored), `PutFunctionEventInvokeConfig` family (stored, `tf-remaining.md 4.2`), event source mapping control plane (`lambda-events.md 1.1 to 1.5`: create 202 with the SQS defaults and the exact error texts, get, list, update, delete; `State` final before return unless `lambda.stateTransitions: clock`), `ResourceConflictException` texts (`5.2`), identifier forms (`3`) and other-region or other-account errors through `ResolveScope`, `Drainer` and `HotReloader` interfaces for the admin API (stubs), `Stateful` with `Rearm`, `Resources`, `Switches`. Invoke, ESM polling, function URL serving and log delivery stay unbound stubs.

Acceptance:
- Every validation row of `lambda-api.md 4.1` and `lambda-events.md 1.3` returns the recorded identity and text (golden table).
- The Terraform sequences of `tf-compute.md 2.1 to 2.4` and `tf-remaining.md 4.1` replay through `Invoke` with empty-diff reads on a frozen clock without any advance; with the switch on, `Pending` is observed and conflicts return `ResourceConflictException`.
- A container-image function whose image pull fails (fake daemon) becomes `Failed` with the message naming the image.
- Identifier fanout (name, `name:alias`, partial ARN, full ARN, URL-encoded ARN, other region, other account) over `GetFunction`.

Verify: `go generate ./internal/api/ && go test -race ./internal/service/lambda/ ./internal/api/lambdaapi/...`, `golangci-lint run ./internal/service/lambda/`.

## Wave 7: second service wave

### T35 SNS filter policies and delivery semantics (opus)

Owns: `internal/service/sns/filterpolicy`, `internal/service/sns/publish.go`, `internal/service/sns/subscription_attrs.go`, `codegen/overlays/sns.json` (additions).

Spec (brief SNS row; `sns.md 6, 7, 9`; `matching-engines.md 3, 5, 8, 10`; `gap-G2.md`):
- `filterpolicy.Compile(policy, scope) (*Policy, *awserr.Error)` over `ruler` with the SNS profile: the grammar and operators of `sns.md 6.1 to 6.4`, the limits and validation texts of `6.5` returned at `SetSubscriptionAttributes` and `Subscribe` time, `MessageAttributes` scope over typed attributes and `MessageBody` scope over the JSON body; `Match`; the recorded conflicts of `matching-engines.md 3` as fidelity switches with the R09 backlog reference.
- Publish path: evaluate every subscription's policy before creating an outbox entry; filtered-out deliveries produce a `journal.Delivery{Filtered}` event and never a DLQ entry; raw delivery with attributes; FIFO topics to FIFO queues preserving group and dedup ids and the ordering of `5.3`; subscription `RedrivePolicy` to the outbox request's `DLQ`; `DeliveryPolicy` stored.

Acceptance:
- Every recorded validation text of `sns.md 6.5` and `matching-engines.md 3` is a golden; rapid over the policy grammar: compile never panics, and a policy matches an attribute set iff the reference evaluator does.
- A publish with a non-matching policy records `Filtered` and the stub target is never invoked; a matching one invokes it; a FIFO publish delivers with `MessageGroupId`.

Verify: `go generate ./internal/api/ && go test -race ./internal/service/sns/...`, `golangci-lint run ./internal/service/sns/...`.

### T36 S3 objects (opus)

Owns: `internal/service/s3` object files (`objects.go`, `put.go`, `get.go`, `head.go`, `delete.go`, `copy.go`, `list.go`, `conditional.go`, `metadata.go`, `objecttags.go`, `presign.go`, `sse.go`, `checksum_single.go`), `codegen/overlays/s3.json` (object error additions).

Spec (brief S3 row; ARCHITECTURE.md 6.1 streaming, 8 rule 3 and 4, 16 KMS forward access; `s3.md 5, 7, 8, 9, 11.1, 12`; `wire-rest.md 3.3, 3.4, 3.7`; `sigv4-checksums-endpoints.md 2, 3.4, 4.3`; `s3-checksum-residuals-and-record-backlog.md 1.7`; `tf-remaining.md 8`):
- `PutObject` over `smithy.Body` (pre-checks before the first body read: bucket, preconditions, SSE-KMS key state through `Invoke(kmsapi.OpGenerateDataKey, AsCaller)`, checksum algorithm consistency; body streamed into `blob` while hashing; single-part checksum validation from the `x-amz-checksum-*` header or the `aws-chunked` trailer through `awschunked` with `BadDigest`, `IncompleteBody`, `XAmzContentSHA256Mismatch` after EOF; `EntityTooLarge` by counting reader; `Content-MD5`; ETag rules of `5.3`; SSE-S3 and SSE-KMS with the seeded ETag; user metadata limits; storage class; conditional writes `If-None-Match` and `If-Match` under the bucket lock with `PreconditionFailed` and `ConditionalRequestConflict`), `GetObject` and `HeadObject` (`5.8`: headers, ranges `5.9` streamed from `blob` through `BodySource`, conditional reads `5.10` matrix, `partNumber` for later multipart objects, response header overrides, SSE-KMS decrypt through `Invoke(kmsapi.OpDecrypt, AsCaller)`), `DeleteObject`, `DeleteObjects` (quiet mode, per-key results, `8.2`), the minimum version model (`8.3`: versioning on means version ids and delete markers), `CopyObject` (`7`: metadata directive, conditional copy source, same-object rules, 200-with-error), `ListObjects`, `ListObjectsV2` (`9.1`: prefix, delimiter, common prefixes, continuation tokens, `encoding-type=url`, key ordering), `ListObjectVersions` basic, object tags (`11.1`), presigned URL verification (SigV4 query and SigV2 through `auth`, expiry on the emulator clock, the signed host from the request), `commitObject` emitting the `notify` seam call, every key stored as an opaque string (never a path).

Acceptance:
- Early reject: a `PutObject` to a missing bucket returns `NoSuchBucket` before the body is read (asserted with a body reader that fails the test when read); `Expect: 100-continue` through `httptest` in the edge path.
- A 256 MiB `PutObject` from a zero reader keeps the process RSS growth under 32 MiB; a ranged `GetObject` streams the exact bytes.
- Trailer mismatch returns `BadDigest`; a short body returns `IncompleteBody`.
- Conditional matrix of `s3.md 5.10` as a table test; concurrent `If-None-Match: *` puts under `-race` yield exactly one success.
- Listing properties: pages concatenate to the unpaginated listing; `encoding-type=url` round trips every key; delimiter grouping matches a reference implementation.
- The access recorder holds `kms:GenerateDataKey` for the caller after an SSE-KMS put and `kms:Decrypt` after the get.

Verify: `go generate ./internal/api/ && go test -race ./internal/service/s3/...`, `golangci-lint run ./internal/service/s3/...`.

### T37 DynamoDB shims, TTL and snapshot export (opus)

Owns: `internal/service/dynamodb` shim files (`shims.go`, `tags.go`, `ttl.go`, `pitr.go`, `describe.go`, `snapshot.go`, `reset.go`).

Spec (ARCHITECTURE.md 15, 13; ADR 0014 and 0009 amendments; `dynamodb-local.md 4.2, 5, 6, 7`; `tf-storage.md 3`):
- Shim state per `(account, region, table)`: tags seeded from `CreateTable.Tags` (`TagResource`, `UntagResource`, `ListTagsOfResource` answered locally), TTL specification (`UpdateTimeToLive`, `DescribeTimeToLive`; never forwarded), continuous backups and PITR (`DescribeContinuousBackups`, `UpdateContinuousBackups` with `TableNotFoundException` for missing tables), table class, SSE description, `TableId`, `WarmThroughput {12000, 4000, ACTIVE}` injected into every table description, `DeleteTable` reporting `DELETING`, `UpdateTable` with only shim-owned fields answered locally, timestamps (`CreationDateTime` and friends) replaced from stored emulator instants, the TTL sweep as a timer kind whose callback starts tracked work (`Scan` with `#ttl <= :now` following `LastEvaluatedKey`, `DeleteItem` conditioned on still-expired, `Synthesized` wires, `Via DynamoDBTTL`), the admin `ttl-sweep` interface, `Late` export (`DescribeTable` plus paginated `Scan` per table as NDJSON sections sorted by key) and import (`CreateTable` plus `BatchWriteItem`), `reset` as `DeleteTable` per scope against the running container, all with `Origin Internal{Via: Snapshot}`.

Acceptance:
- The Terraform sequence of `tf-storage.md 3` replays against a fake Local with empty-diff reads (every shim field present, `WarmThroughput` injected, `DELETING` on delete).
- rapid state machine for the TTL shim against a fake Local: items expire exactly when `#ttl <= now` under `Advance`; a refreshed item survives the sweep.
- `-tags docker`: `Late` export then `reset` then import on the real image reproduces every item and the shim state; stream label change is reported as a deviation in the task report.

Verify: `go test -race ./internal/service/dynamodb/...`, `go test -race -tags docker ./internal/service/dynamodb/...`, `golangci-lint run ./internal/service/dynamodb/...`.

### T38 Lambda invoke, async queue, logs delivery and hot reload (opus)

Owns: `internal/service/lambda/invoke.go`, `internal/service/lambda/async.go`, `internal/service/lambda/envs.go`, `internal/service/lambda/logs_delivery.go`, `internal/service/lambda/hotreload`, `internal/service/lambda/hotreload_wiring.go`.

Spec (brief item 7 and Lambda row execution; ARCHITECTURE.md 14.1 to 14.4, 14.6, 14.7, 10 linking; ADR 0010 amendments; `lambda-api.md 8, 9.4 to 9.8`; `lambda-events.md 8`; `containers.md 5.4, 7`; `lambda-runtime-contract.md 5, 6`):
- `Invoke` (`RequestResponse` 200 with `X-Amz-Executed-Version`, `Event` 202 into the per-function async queue in the cell, `DryRun` 204, payload limits, `LogType: Tail` with `X-Amz-Log-Result`, function error mapping of `8.3`, 429 `TooManyRequestsException` for reserved and account concurrency before container work, `ServiceException` naming the Docker endpoint and `understudy doctor` when Docker is unavailable), execution through `pool.Acquire` with a spec built from the function record (`Env` of `9.4`, credentials minted per environment through `identity.Store` as `AssumedRole{Source: FromExecutionEnvironment}`, `AWS_ENDPOINT_URL` from the topology, `AWS_EC2_METADATA_DISABLED`), the `kernel.Linker` implementation (access key to the current invocation event; init-phase calls to the cold-start event), `journal.Invocation` events with `Cold`, `Init`, `Duration`, the async queue as a timer kind (retries after 60 s and 120 s for function errors; throttles and 5xx requeued with 1 s to 5 min backoff for up to 6 h), log delivery (one `logs` stream per environment from `launcher.Logs`, `INIT_START`, `START`, `END`, `REPORT` synthesized with the settle barrier, `Invoke(logsapi.OpPutLogEventsInternal, AsExecutionRole, Via LambdaLogs)` with `CreateLogGroup` and `CreateLogStream` on first use), hot reload (`hotreload.Watcher` polling mtimes and sizes every 300 ms with a 300 ms quiet period, overlay generation bumps, the magic bucket convention, the config mapping, the admin interface, precedence over `UpdateFunctionCode` with the advisory), `Drainer` for reset and shutdown, `images pull` through `launcher.EnsureImage`.

Acceptance:
- Through `kerneltest` with `pooltest.FakeRuntime`: sync, async and dry-run outcomes; async retries on the schedule under `Advance`; throttle requeue; `Tail` log result; 429 before any pool call; the journal links an in-function call (made with the environment's minted key) to the invocation.
- `-tags docker`: `provided.al2023` and `python3.13` functions invoked through `Invoke`; warm reuse; `Unhandled` with `X-Amz-Function-Error`; timeout then cold start; logs arrive in a stub `PutLogEventsInternal` handler with the four synthesized lines in order; hot reload: editing the directory bumps the generation and the next invoke is cold with the new code; the G4 deadline test through the full path.

Verify: `go test -race ./internal/service/lambda/...`, `go test -race -tags docker ./internal/service/lambda/...`, `golangci-lint run ./internal/service/lambda/...`.

## Wave 8: third service wave and the image

### T39 S3 multipart uploads and checksum semantics (opus)

Owns: `internal/service/s3/multipart.go`, `internal/service/s3/parts.go`, `internal/service/s3/checksums.go`, `internal/service/s3/uploadpartcopy.go`.

Spec (brief S3 row; `s3.md 6, 7, 9.2, 10`; `wire-rest.md 3.7`; `sigv4-checksums-endpoints.md 4.4`; `s3-checksum-residuals-and-record-backlog.md 1.6, 1.7`; `go-toolchain.md 7.4`):
- `CreateMultipartUpload` (upload id from `ids`, checksum algorithm and type recorded), `UploadPart` (streaming through `smithy.Body` into `blob`, part number and size rules, per-part checksum header or trailer validation, ETag per part), `UploadPartCopy` (ranges, conditional source, 200-with-error), `ListParts` and `ListMultipartUploads` with paging, `AbortMultipartUpload`, `CompleteMultipartUpload` (part order and ETag validation with `InvalidPart`, `InvalidPartOrder`, `EntityTooSmall`; the completed object's ETag as the MD5-of-MD5s with `-N`; composite checksums (`-N`) and full-object checksums through `checksum.Combine` for CRC algorithms and the documented rejection for the others; `x-amz-checksum-type`; 200-with-error path; the `StatusOKError` fault action), `GetObject` with `partNumber` and `x-amz-mp-parts-count`, every single-part checksum algorithm of `6.1` accepted on `PutObject` through the shared `checksum_single.go` rules.

Acceptance:
- rapid: random part splits of a random body complete to an object whose bytes, ETag and full-object checksum equal the single-part equivalent for every CRC algorithm; composite checksums match the per-part computation for every algorithm.
- Part validation errors match `s3.md 10`; `partNumber` reads return the exact part bytes; a `StatusOKError` rule produces the 200 body with an error document.

Verify: `go test -race ./internal/service/s3/...`, `golangci-lint run ./internal/service/s3/...`.

### T40 S3 event notifications (opus)

Owns: `internal/service/s3/notify`, `internal/service/s3/notification.go`.

Spec (brief S3 should-have; ARCHITECTURE.md 16; `s3.md 13`; `lambda-events.md 6`; `tf-storage.md 2.5` notification resource):
- `PutBucketNotificationConfiguration` (queue, topic and Lambda configurations with event types and prefix and suffix filters, validation texts, `s3:TestEvent` sent to SQS and SNS targets on configuration), `GetBucketNotificationConfiguration`, the `notify` builder (`eventVersion 2.1`, URL-encoded key, unquoted `eTag`, per-key monotonic `sequencer`, `responseElements` with `x-amz-request-id` and `x-amz-id-2` from the call, `userIdentity`), the `notify` seam implementation producing `Deliver` effects with mechanisms `S3ToSQS`, `S3ToSNS`, `S3ToLambda` and the `S3Notification` policy for every matching configuration on object commit and delete.

Acceptance:
- Record goldens byte for byte for `lambda-events.md 6.2` (`ObjectCreated:Put`, `ObjectRemoved:Delete`); `TestEvent` golden.
- rapid: the `sequencer` is strictly increasing per key across random put and delete sequences; filters select exactly the configurations whose prefix and suffix match.
- A put to a bucket with three configurations produces the matching outbox entries with the right mechanisms.

Verify: `go test -race ./internal/service/s3/...`, `golangci-lint run ./internal/service/s3/...`.

### T41 Lambda SQS event source mappings (opus)

Owns: `internal/service/lambda/esm`, `internal/service/lambda/esm_runtime.go`.

Spec (brief Lambda row ESM; ARCHITECTURE.md 14.8, 9 ESM pin, 16 settle; `lambda-events.md 2, 3, 8`; `sqs.md 7.1`):
- One poller per enabled mapping as `Kernel.Go(InvocationRunning)` work that waits on the queue's `WaitKey` and the batching-window timer kind, then `Invoke(sqsapi.OpReceiveMessage, AsExecutionRole, WaitTimeSeconds 0, up to min(BatchSize, 10))` looping to `BatchSize`, the window (emulator time) or 6 MB; filters through `ruler` with the Lambda profile deleting non-matching messages; the SQS record shape of `lambda-events.md 3` (attributes, `md5OfBody`, `eventSourceARN`, `awsRegion`); synchronous `Invoke` through the in-package invoke path with the invocation's parent set to the earliest message's enqueue event and the rest as `Links`; partial batch responses (`ReportBatchItemFailures`: empty, `null`, `{}` or empty list is full success; malformed or unknown `itemIdentifier` is full failure); `DeleteMessageBatch` for successes; failures left to visibility and `maxReceiveCount` redrive; FIFO one in-flight batch per group; `HoldVisibility` pinning under Frozen and Scaled; mappings that outlive their function stop with a journal event; `ScalingConfig` ignored; `Rearm` restarts pollers after a load.

Acceptance (with the real `internal/service/sqs` in `_test.go` and `pooltest.FakeRuntime`):
- Batch assembly: window 0 invokes on the first message; window 5 s collects messages until the timer fires under `Advance`; `BatchSize` 25 loops three receives.
- Partial failure deletes only the successes; a failed message returns after visibility and reaches the DLQ after `maxReceiveCount`.
- Frozen clock: held messages stay invisible for the whole invocation; after resume AWS semantics apply.
- Filter criteria delete non-matching messages without invoking; the journal trace reads `SendMessage -> ReceiveMessage -> Invoke -> DeleteMessageBatch`.

Verify: `go test -race ./internal/service/lambda/...`, `golangci-lint run ./internal/service/lambda/...`.

### T42 Lambda function URLs (opus)

Owns: `internal/service/lambda/furl`, `internal/service/lambda/urls_runtime.go`.

Spec (brief Lambda row function URLs; ARCHITECTURE.md 4.1 rules 1 and 2, 14.7; `lambda-events.md 7.2 to 7.4`; `lambda-api.md 10`):
- The raw operation `InvokeFunctionUrl` bound with `BindRawHTTP`: resolve the url id from the host label or the `/_understudy/lambda-url/<id>/` path fallback; `AuthType NONE` evaluates the function's resource policy for `lambda:InvokeFunctionUrl` with the public principal and answers the 403 `{"Message":null}` body with `x-amzn-ErrorType` when absent; `AWS_IAM` verifies the SigV4 signature when configured; the payload v2.0 request event (`7.3`: `rawPath`, `rawQueryString`, lowercase headers, cookies, multi-value query joined, base64 body, `requestContext` with `http`, `timeEpoch` on the emulator clock, `domainName`), invocation through the in-package invoke path as `RequestResponse`, response inference (`7.4`: object with `statusCode`, string body, headers, cookies, `isBase64Encoded`, default 200 `application/json`, 502 plain text `Internal Server Error` on malformed responses or function errors), `BUFFERED` only, CORS headers from the stored config, journal and access through the raw operation.

Acceptance:
- Request event goldens from `lambda-events.md 7.3` through the edge with `httptest`; the response inference matrix of `7.4` as a table test; the 403 shape for `NONE` without a policy and success with one; the path fallback and the host form reach the same function.

Verify: `go test -race ./internal/service/lambda/...`, `golangci-lint run ./internal/service/lambda/...`.

### T43 Docker image, release configuration and in-container check (sonnet)

Owns: `Dockerfile`, `.dockerignore`, `.goreleaser.yaml`, `.github/workflows/release.yml`, `internal/distribution`.

Spec (ARCHITECTURE.md 24, 14.5 `InContainer`, 18 `ContainerPublished`; ADR 0011 and 0017 amendments; brief platform features):
- `Dockerfile`: multi-stage; builder `golang:1.27.1` with `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" ./cmd/understudy`; final `FROM scratch`, `ca-certificates` copied from the builder, user `65532:65532`, `ENTRYPOINT ["/understudy"]`, `CMD ["serve"]`, `EXPOSE 4566`, `HEALTHCHECK --interval=5s --timeout=2s --start-period=3s CMD ["/understudy", "health", "--endpoint", "http://127.0.0.1:4566"]` (the `health` subcommand lands with T44 in the next wave; the instruction only runs inside a started container, and this task's image test asserts readiness over HTTP from the host instead), OCI labels; `VOLUME /data`.
- `.goreleaser.yaml` (version 2): builds for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, archives, checksums, `dockers` and `docker_manifests` for the multi-arch image with `skip_push: true`, `release: disable: true`; `just release-check` runs `goreleaser check` and `goreleaser build --snapshot --clean` when the tool is present.
- `.github/workflows/release.yml`: on tags, `goreleaser release --snapshot --clean` with no publish step, artifacts uploaded to the workflow run only.
- `internal/distribution`: a `-tags docker` test that builds the image, runs it with `-p 127.0.0.1:0:4566`, polls `/_understudy/v1/health` until ready, asserts the health body reports topology `in-container` and listener `0.0.0.0:4566` without a token, runs the same image with `-v /var/run/docker.sock` and asserts health reports Docker reachable, removes the container and image (labels `understudy.dev/test=1`).

Acceptance:
- `docker build` succeeds and the image is under 60 MiB; the in-container test passes; `goreleaser check` passes when installed; the release workflow is syntactically valid (`actionlint` when installed, else a YAML parse).

Verify: `just image`, `go test -race -tags docker ./internal/distribution/...`, `just release-check`.

## Wave 9: CLI, wiring and smoke

### T44 CLI, configuration and understudytest (opus)

Owns: `internal/cli`, `internal/config`, `understudytest`, `cmd/understudy`.

Spec (ARCHITECTURE.md 17, 18, 13 persistence flags, 24; ADR 0017 and amendments; ADR 0013 `env`; `dynamodb-local-terms.md 5.1`):
- `config`: `Config` with the sum types of ARCHITECTURE.md 18, `Load(args, env, fileLookup) (Config, *Error)` with precedence flags over `UNDERSTUDY_*` over the JSON file (`--config` or `understudy.json`) over defaults, strict unknown keys with the closest valid key named, durations as Go strings, times RFC 3339, the `Listen` and `DynamoDB` validation rules, `Faults []fault.Rule` preloaded, `Lambda.HotReload` mappings, `S3.BaseDomains` with `localhost.localstack.cloud` allowed.
- `cli`: every subcommand of ARCHITECTURE.md 17 as a thin `admin.Client` user: `serve` (maps `config.Config` to `server.Config`), `health [--wait]`, `settle`, `reset`, `clock now|freeze|resume|advance [--settle]|set|scale`, `faults list|add|remove|clear|load` with every flag, `journal` with every filter and `--follow` over the NDJSON stream, `journal trace`, `snapshot save|load|inspect|diff [--normalize]`, `resources`, `coverage` (offline through the in-process wiring when no server answers), `iam usage|policy`, `lambda hot-reload`, `images pull`, `env` per SDK and format (the exact variables of ADR 0013 and `sigv4-checksums-endpoints.md 5`), `doctor` (Docker reachable and socket permissions, topology, `*.localhost` resolution, binfmt, hot-reload shared paths, DynamoDB licence state with the exact flag), `cleanup` (label sweep through `docker`), `version`; `--output text|json` everywhere; `--endpoint` and `UNDERSTUDY_ENDPOINT`; exit codes from `admin.ErrorCode.ExitCode()` plus 2 for usage and 3 for unreachable; every error names what failed and the next command; every `--help` carries examples; never prompts; stdout only from this package.
- `understudytest`: `Start(t, Options{Seed, Clock, Docker, DynamoDB, Regions}) *Instance` (the package's own sum types, never `internal/config`, which code outside the module cannot import; ADR 0017 m13; fault rules go through `Admin().PutFault`) running the server in-process on `127.0.0.1:0` with cleanup, `Endpoint()`, `Admin() *admin.Client`, `Settle(ctx)`, `Env() map[string]string`; uses only `admin` wire types.
- `cmd/understudy`: builds the CLI tree, nothing else.

Acceptance:
- Config precedence table test (flag beats env beats file beats default for three settings); unknown key error names the closest key; a non-loopback listener without a token fails validation; `ContainerPublished` outside a container fails at boot (fake topology).
- CLI tests against `understudytest`: every subcommand runs in both output modes; exit codes 0, 1, 2, 3, 4 each produced by a scripted case; `journal --follow` streams two events then stops on context cancel; `coverage` offline equals the server's report.
- `understudytest` starts two instances in parallel tests without interference.

Verify: `go test -race ./internal/cli/... ./internal/config/... ./understudytest/... ./cmd/...`, `golangci-lint run ./internal/cli/... ./internal/config/... ./understudytest/... ./cmd/...`.

### T45 Final wiring and the smoke test (opus)

Owns: `internal/server` (all files), `internal/smoke`, `.github/workflows/ci.yml` (add `smoke`, `test-docker` and the image check as jobs).

Spec (ARCHITECTURE.md 2 wiring, 4.1, 13, 14.5, 16, 17, 18, 19 smoke row, 20, 24):
- `wire.go`: `config.Config` mapped onto every service's `Options`; `BucketExists` from the S3 service into the edge; `outbox` with the `PolicySource` backed by SQS, Lambda and SNS; `Stateful` and `Late` lists including `outbox`, `fault` and the journal counter; `Switches` collected; every timer kind registered before `Run` (boot fails on an unregistered kind armed by a `Rearm`); TLS listener (`SelfSigned` with SANs `localhost`, `*.localhost`, `127.0.0.1`); the container listener created on demand from the topology, serving only health and clock from the admin prefix; DynamoDB Local lifecycle hooks (start in the background when accepted, stop on shutdown); startup sweep of this owner's containers under the data-dir `flock`; persistence loop (`--persist`: load at start, save every 30 s while dirty, save on shutdown, blobs under `<dir>/blobs`); graceful drain of Lambda environments on SIGINT and SIGTERM; the banner with endpoint, `env` block, admin URL, enabled services and warnings (`*.localhost`, Docker, DynamoDB licence line).
- `internal/smoke` (`-tags smoke`): builds the binary into a temp dir, starts it with `--listen 127.0.0.1:0 --ready-file --clock frozen --seed 1` (and `--tls`), waits for readiness, then with `net/http` only: SQS `CreateQueue` and `SendMessage` (awsJson1_0), SNS `CreateTopic`, `Subscribe` to the queue and `Publish` (awsQuery) then `settle` and a journal trace assertion `Publish -> SendMessage`, S3 `CreateBucket`, `PutObject` over HTTP with a checksum header and over the TLS listener with a `STREAMING-UNSIGNED-PAYLOAD-TRAILER` body built with `awschunked.NewWriter`, `GetObject`, `GetBucketVersioning` (restXml), DynamoDB `ListTables` asserting either the result (when `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1` and Docker are present) or the 501 text naming the flag, Lambda `CreateFunction` with a tiny zip and `GetFunction` (restJson1), Logs `CreateLogGroup` and `PutLogEvents` (awsJson1_1), Secrets Manager `CreateSecret` and `GetSecretValue`, KMS `CreateKey` and `Encrypt`, IAM `CreateRole`, STS `GetCallerIdentity`, EventBridge `PutRule` and `PutEvents`, CloudWatch `PutMetricAlarm` over awsJson1_0 and over rpcv2Cbor; `understudy iam policy` for the smoke principal contains `kms:GenerateDataKey` after an SSE-KMS put; `/coverage` answers with three numbers; under `-tags docker` the same test runs the Lambda invoke of the zip function and runs the whole suite inside the image (`InContainer`).

Acceptance:
- `just smoke` passes on HTTP and TLS paths; `just smoke` with `-tags docker` passes the invoke and in-container runs; `just verify`, `just generate-check`, `just coverage-check`, `just budgets` and `just test-docker` are green at the gate.
- `serve --persist` round trip: start, create a queue, stop, start, the queue is listed.
- Boot fails with a named error when a service arms an unregistered timer kind (test with a fake service).

Verify: `go test -race ./internal/server/...`, `just smoke`, `go test -race -tags "smoke docker" ./internal/smoke/...`, `golangci-lint run ./internal/server/... ./internal/smoke/...`.

## Scope ledger

| Brief item | Tasks |
|---|---|
| SQS full | T16, T26, T41 (pin) |
| SNS full (filter grammar, raw and enveloped delivery, FIFO, DLQ, policies, tags) | T27, T35, T23 |
| S3 buckets and subresources, objects, multipart and checksums, presigned URLs, addressing, notifications | T28, T36, T39, T40, T05, T21 |
| DynamoDB proxy, shims, TTL, isolation, journal and faults | T33, T37 |
| Lambda API, execution, warm reuse, hot reload, SDK routing, logs, Invoke modes, permissions, ESM, URLs, versions, aliases, concurrency, tags, async retries | T34, T18, T38, T41, T42 |
| CloudWatch Logs with filter patterns and retention | T08, T31 |
| Secrets Manager | T29 |
| KMS with real cryptography | T29 |
| IAM stored plus access recorder, STS | T30, T14, T12 |
| EventBridge rules, schedules, patterns, targets, PutEvents | T08, T07, T32 |
| CloudWatch alarms and basic metrics | T31, T22 |
| serve on one port, multi-account, multi-region | T16, T12, T44, T45 |
| admin API and CLI (health, reset, clock, faults, journal, snapshots, resources, coverage, IAM policy) | T24, T25, T44 |
| coverage report (CLI and Markdown) | T25, T44 |
| structured logging | T01, T16 |
| strict validation from Smithy constraints; opt-in SigV4 | T10, T19, T12 |
| Docker image and release configuration | T43 |
| controllable clock | T03, T16 |
| causal journal | T15, T16, T23 |
| fault injection | T13, T16, T24 |
| snapshots and persistence | T17, T24, T45 |
| least-privilege policy generation | T14, T24, T44 |
| Smithy protocol tests | T19, T25 |
