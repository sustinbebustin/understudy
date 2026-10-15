# understudy architecture

The design contracts of understudy: package layout, core types, the request lifecycle, code generation, the protocol layer, identity and auth, state and concurrency, the clock, the journal, fault injection, IDs and determinism, snapshots, the Lambda runtime, the DynamoDB Local integration, cross-service delivery, the admin API and CLI, configuration, the testing strategy, the security model and distribution. Decisions with alternatives are recorded as ADRs in [adr/](adr/) (index in section 27). AWS behavior is researched in [research/](research/) and cited as `file.md section`.

## 1. Principles that decide ties

1. Wire fidelity beats convenience. The default is what AWS does; leniency is a named configuration switch.
2. Every deviation from AWS is data the coverage report lists: an overlay entry with evidence, a fidelity switch with provenance, a skip-list entry with a category, or a coverage mark at the bind site. Nothing deviates silently.
3. One pipeline for every call. An SDK request and an SNS-to-SQS delivery run the same stages; only the wire codec differs.
4. Time has three domains and the type system keeps them apart. Time a container, SDK signer or user code observes is wall time; time only the emulator observes is emulator time; durations of real work are monotonic.
5. Timers are a function of state. Nothing lives only in the scheduler; snapshots store state and `Rearm` rebuilds timers.
6. If the answer is a function of the model plus overlay, it is generated. If it depends on state or time, it is hand-written.
7. A contributor adds an operation by writing one typed function and binding it. No generator run, no codec work.

## 2. Package layout

One module, `github.com/sustinbebustin/understudy`, Go 1.27. Dependencies are the allow-list in ADR 0016 (amendment 2026-10-14): no runtime dependency, and the test tier `pgregory.net/rapid`, `github.com/google/go-cmp`, `github.com/cespare/xxhash/v2` and `github.com/zeebo/xxh3`. `depguard` and `forbidigo` enforce it with the configuration in `go-toolchain.md 13`.

```
cmd/understudy/                 main: builds the CLI tree, nothing else
admin/                          PUBLIC (experimental): admin API wire types, ErrorCode, and a typed client
understudytest/                 PUBLIC (experimental): start an in-process emulator from a Go test; exposes an admin.Client

internal/
  cli/                          subcommands: admin.Client calls plus formatting; no service imports
  config/                       Config type with sum-typed modes; parse flags > env > JSON file; validate; defaults
  server/                       serve: listeners, wire.go (explicit construction of every service), startup, shutdown, container sweep
  server/admin/                 admin API handlers over kernel, journal, snapshot, fault, clock, access
  edge/                         front door: body caps, request id, auth parse, identity resolve, claim, codec dispatch, Unclaimed diagnostics
  kernel/                       Call, Handler, Op, Binder, Bindings, Dispatch, Invoke, Write, Tx, timer kinds, work tracker, Stateful, Run
  kernel/kerneltest/            run a kernel inside a synctest bubble; bind stub handlers for other services
  registry/                     immutable Service, Operation, Route descriptors; BindCoverage; Registry of descriptors
  ref/                          LEAF identifiers: ServiceID, OperationID, RuleID, EventID, Cause, Via, Origin, ProtocolID, ListenerKind
  evidence/                     LEAF: the one Label type for overlays, switches, skips and parity fixtures
  awserr/                       Error, Identity, Origin, Violation; renderers; generic catalogue (no descriptors, no registry)
  aws/                          AccountID, Region, Partition, Scope, ARN, RegionSource
  identity/                     Principal, credential store, key minting, account-from-key
  auth/                         SigV4 and SigV2 parse (sum type), opt-in verify, presign, payload hash
  clock/                        Instant, Clock, Scheduler (dispatch by TimerKind), Deadline, Wall, Stopwatch
  ids/                          id types and the seeded source: request, message, receipt, upload, version, key, UUID
  journal/                      Event ADT over ref types, ring store with byte accounting, indexes, query, NDJSON stream
  fault/                        Rule, Match, Phase, Action, Trigger, seeded evaluation against an error catalogue interface
  access/                       IAM access recorder (principal modes), resource-policy advisories, least-privilege rendering
  fidelity/                     named behavior switches with evidence.Label provenance; Entry for coverage
  store/                        Gate, Cell[S], Partitioned[K,S], Waiters, WaitKey, Section (no effect types)
  paging/                       LEAF: keyset pager with listing-scoped, versioned markers; MarkerError
  paging/pagingtest/            SurvivesChanges: page a service listing while items come and go between pages
  blob/                         content-addressed bytes under os.Root; memory and disk; refcounts
  snapshot/                     archive format, manifest, section readers and writers, normalizers, diff
  coverage/                     report (JSON, Markdown) from kernel bindings + protocol-test baseline + fidelity ledger
  delivery/                     LEAF types: Target variants with their payloads, Request, RetryPolicy, Mechanism
  outbox/                       the delivery engine: a Stateful outbox cell, attempts on the clock, DLQ routing, kernel.Invoke per attempt
  protocol/
    smithy/                     runtime schema descriptors incl. streaming kind, Serializer and Deserializer interfaces, ShapeID
    jsonx/                      the only importer of encoding/json/v2 and jsontext; tagged-union convention for persisted sums
    xmlw/                       streaming XML writer (namespaces, xsi:type, SDK escape table)
    cbor/                       stdlib CBOR subset (wire-cbor.md 7)
    form/                       x-www-form-urlencoded with numeric index ordering
    restbind/                   label, query, header, prefix-header, payload and deferred trailer binding
    awsjson/ awsquery/ restjson/ restxml/ rpcv2cbor/   one codec per protocol
  checksum/                     ten S3 algorithms incl. xxHash ports, CRC combine
  awschunked/                   aws-chunked decoder, four modes, streaming, trailer resolution at EOF
  ruler/                        Event Ruler core: compiled pattern ADT, Match; per-service allow-lists and limits
  logsfilter/                   CloudWatch Logs filter pattern compiler and matcher
  schedexpr/                    EventBridge rate and cron parser, next-fire
  policydoc/                    IAM policy document parse and validate; least-privilege rendering
  docker/                       Engine API client over the unix socket: typed errors, stdcopy, progress, events
  topology/                     detect Linux bridge, Docker Desktop, rootless, Podman, in-container; derive addresses
  api/<svc>api/                 GENERATED per service: types, schema, serde, validators, errors, ops, binder, routes, iam, redact
  service/<svc>/                hand-written, one package per service:
    sqs/ sns/ s3/ dynamodb/ lambda/ logs/ secretsmanager/ kms/ iam/ sts/ events/ cloudwatch/
    sns/envelope/ sns/filterpolicy/ s3/notify/ events/envelope/
    lambda/runtimeapi/ lambda/pool/ lambda/launcher/ lambda/codestore/ lambda/esm/ lambda/furl/ lambda/hotreload/
    dynamodb/ddblocal/
  codegen/                      generator library: loader, overlay merge, closure, trait classifier, role analysis, IR, emitters
  cmd/gen/                      go run ./internal/cmd/gen
  cmd/vendor/                   vendoring tool (network only when a human runs it; -verify offline)
  protocoltest/                 compliance harness, comparators, skip list, checked-in baseline (ratchet)
  protocoltest/gen/             GENERATED synthetic test services and case tables
  smoke/                        in-repo smoke test: builds and starts the binary, one raw HTTP request per service
  parity/                       parity recording replayer (built in the conformance phase; the normalizer set is shared with snapshot)

codegen/
  services/<svc>.json           per service: package, extra protocols, raw binding, raw operations, route overrides
  overlays/<svc>.json           Smithy JSON AST fragments with dev.understudy traits and evidence
  overlays/common.json          framework errors, generic vocabulary
third_party/
  api-models-aws/               12 model files verbatim at 1cfdfb4bb9ea858e65623e66d73af04bec353201, UPSTREAM.json, LICENSE, NOTICE
  smithy-protocol-tests/        flattened AST from Smithy 1.74.0 jars, PIN.json
  aws-service-reference/        derived operation -> IAM action table with provenance
testdata/parity/<svc>/          parity recordings with evidence labels
conformance/                    separate modules, own manifests, each with a run.sh that goes through scripts/sealed:
  go-sdk/ python-boto3/ js-sdk-v3/ aws-cli/ terraform/ e2e-pipeline/
Dockerfile, .goreleaser.yaml    distribution (section 24)
docs/coverage.md                generated by `understudy coverage --format markdown`, drift-checked
```

Dependency direction, checked by a test in `internal/ref` that walks imports with the allowed edges kept as data (`ref/deps_test.go`, landed with the first package):

- Leaves, in order: `ref` imports only `aws`; `store` imports only `aws` (the shared `aws.Scope` and `aws.AccountID` key codecs); `evidence`, `aws`, `clock`, `ids`, `blob`, `checksum`, `awschunked`, `xmlw`, `cbor`, `form`, `ruler`, `logsfilter`, `schedexpr`, `policydoc`, `docker`, `paging` import nothing in the repo. `ruler` and `logsfilter` therefore return switch metadata that the owning service wraps in `fidelity.Switch` values. `topology` and `docker/dockertest` import `docker`. `delivery` imports `aws` and `ref`. They all run under `testing/synctest`.
- `awserr` imports `ref`, `evidence` and `protocol/smithy`; it holds plain values (`Unimplemented{Op ref.OperationID}`, `Injected{Rule ref.RuleID}`) and never a descriptor. `registry` imports `awserr`, `smithy`, `ref`, `aws`. `journal` imports `ref`, `aws`, `clock`, `ids` and owns its record types; it never imports `kernel`. `fault` imports `ref`, `aws`, `awserr`, `smithy`, `ids`, `delivery` and `jsonx` (rules export through the tagged-union convention). `access` imports `ref`, `aws`, `policydoc` and `registry` (recorder entries carry `registry.IAMAction`).
- `kernel` imports `registry`, `journal`, `fault`, `access`, `identity`, `clock`, `ids`, `store`, `delivery`, `awserr`, `ref`, `auth` (the verified credentials on a `Call`, section 7), and `blob` and `docker` for `ServiceDeps` (3.2). Services that need the container topology get it from the container host through the Lambda launcher and Runtime API options, not from `ServiceDeps`. It never imports a codec or `snapshot`.
- `api/*` (generated) imports only `protocol/smithy`, `awserr`, `registry`, `kernel`, `aws`, `ref`.
- `service/*` imports `api/*`, `kernel`, `store`, `clock`, `ids`, `blob`, `delivery`, `fidelity`, `paging` and the engines it needs. A service never imports another service; it calls it through `kernel.Invoke` with the other service's generated `Op`. Three exceptions: a service imports `journal` for `Detail` values only, which it records through `kernel.Call.Record` or `kernel.Tx.Record` (section 10); DynamoDB imports `protocol/awsjson` to encode a `Synthesized` call as a client would; S3 imports `protocol/xmlw` and `protocol/restxml` for the bodies and `x-amz-id-2` of its raw S3 Control tag operations.
- `protocol/*` imports `protocol/smithy`, `awserr`, `ref` and the other protocol helpers it builds on; the codecs and `restbind` also import `registry` and `aws`, because a codec claims requests through the service descriptor and reads its identities. It never sees a service or a generated package.
- `outbox` imports `kernel`, `delivery`, `journal`, `store`, `clock`, and the generated `api/*` packages whose `SendMessage`, `Invoke` and `Publish` ops its attempts call. `edge` imports `kernel`, `registry`, `auth`, `identity` and the codec interface, plus the public `admin` package (standard library only) for the JSON error envelope it sends to LocalStack paths (4.1 rule 1); `server` is the only package that names every service, every codec and `outbox`.
- `snapshot` imports `store`, `blob`, `jsonx`, `clock`, `ids`; `kernel.Stateful` exports `store.Section` values and `snapshot` archives them, so neither imports the other.
- `cli` depends on `admin`, `config` and `server`, plus the read-only non-service packages its offline subcommands need: `serve` maps `config.Config` onto `server.Config`; `coverage` runs offline in process and filters, renders and adds overlay evidence through `coverage`; `snapshot inspect` and `diff` read local archives through `snapshot`; `doctor` and `cleanup` reach the daemon through `docker` and detect the topology through `topology`; `env` checks `--region` against the region names in the `aws` leaf. It never imports a service.

Every service package has the same files, so reading one teaches all: `service.go` (struct, `New(kernel.ServiceDeps, Options)`, lifecycle, one `bind<Family>` call per resource family), `state.go` (persisted types with `json` tags and `stateVersion`), one file per resource family, `validate.go` for typed per-operation validators bound with `kernel.WithValidator`, `errors.go` for message builders over the generated constructors (never an `awserr.Identity` literal), `*_test.go` through `kernel.Invoke` inside `kerneltest` bubbles.

Shared helpers have homes, never a utils package: `smithy.Deref` for optional generated members; `fidelity.Inferred` over the structural `fidelity.SwitchMeta` for engine switch metadata (`ruler` stays a leaf and satisfies it with a method); `paging` for keyset listings; `kernel.CellState` for the Stateful boilerplate over `store.Partitioned`; `store.View` for reads that create no cell. Only `paging` is built before the first tag (KMS, Lambda and IAM listings first); the others are not built yet.

## 3. Core types

### 3.1 Scope and identity

```go
package aws

type AccountID struct{ digits string } // exactly 12 ASCII digits; ParseAccountID is the only constructor
type Region struct{ name string }      // member of the region table (real AWS names; config adds more)
type Partition struct{ name string }   // "aws" in phase 1

type Scope struct {
	Partition Partition
	Account   AccountID
	Region    Region // IAM and STS cells ignore Region; S3 buckets have a home region
}

type ARN struct{ Partition, Service, Region, Account, Resource string }
func ParseARN(s string) (ARN, error)
```

```go
package identity

type Principal interface{ isPrincipal() }

type Root struct{ Account aws.AccountID }
type User struct{ Account aws.AccountID; Path, Name, ID string }          // ID is AIDA...
type AssumedRole struct {
	Account aws.AccountID
	Role    RoleRef // name, path, AROA id
	Session string
	Source  SessionSource
}
type ServicePrincipal struct{ Name string } // "sns.amazonaws.com" on internal deliveries
type Anonymous struct{}                     // unsigned request; function URL with AuthType NONE

type SessionSource interface{ isSessionSource() }
type FromAssumeRole struct{}
type FromExecutionEnvironment struct{ Function aws.ARN; Env EnvID }

// Resolve maps an access key to who is calling. Order: minted key in the store;
// 12-digit numeric key = that account's root; anything else = default account root.
func (s *Store) Resolve(key string) Principal
```

### 3.2 The call and the handler contract

```go
package ref // leaf: identifiers only

type ServiceID string
type OperationID struct{ Service ServiceID; Name string }
type RuleID string
type EventID uint64
type Cause struct{ Parent, Trace EventID } // Parent 0 for roots; Trace is the root of the causal tree
type ProtocolID string                      // awsjson1_0 | awsjson1_1 | awsquery | restjson1 | restxml | rpcv2cbor | raw
type ListenerKind uint8                     // Main | Container

type Via uint8 // closed: SNSDelivery | LambdaESM | EventsTarget | S3Notification | LambdaLogs | DynamoDBTTL | Timer | Snapshot | Admin | KMSForward | STSAssumeRole

// Origin says where a call came from.
//
//sumtype:decl
type Origin interface{ isOrigin() }
type External struct{ Protocol ProtocolID; Listener ListenerKind; RemoteAddr netip.AddrPort; UserAgent string }
type Internal struct{ Via Via }
```

```go
package kernel

// Call is the context of one operation invocation, external or internal. The edge or Invoke
// builds it; handlers read it through accessors and may Touch resources. Fields are unexported.
type Call struct{ /* id, scope, principal, origin, op, event, cause, admitted, endpoint, trace, headers, touched, holding */ }

func (c *Call) ID() ids.RequestID
func (c *Call) Scope() aws.Scope
func (c *Call) Principal() identity.Principal
func (c *Call) Origin() ref.Origin
func (c *Call) Op() *registry.Operation
func (c *Call) Event() ref.EventID            // this call's journal event
func (c *Call) Cause() ref.Cause              // parent event and trace; zero parent for roots
func (c *Call) Admitted() clock.Instant       // admission instant: journal time and request time (commit time comes from Write)
func (c *Call) Endpoint() Endpoint            // scheme and host as the client addressed us; canonical for internal calls
func (c *Call) Trace() string                 // X-Amzn-Trace-Id, or inherited from the parent call
func (c *Call) Header(name string) (string, bool) // read-only view of a closed set of unmodeled request headers
func (c *Call) Touch(arns ...aws.ARN)        // the only mutator; feeds the journal and the access recorder

// Handler implements one AWS operation. Dispatch maps (nil, nil) and (out, err) to Internal,
// naming the operation; unit outputs are the empty struct, so nil is never legitimate.
type Handler[In, Out any] func(ctx context.Context, c *Call, in *In) (*Out, *awserr.Error)

// RawHandler is the passthrough form (DynamoDB proxy only, ADR 0014): the typed input is decoded
// and validated; Wire says whether a client request exists to forward.
type RawHandler[In any] func(ctx context.Context, c *Call, in *In, w Wire) (RawResponse, *awserr.Error)

// Wire says whether a client request exists to forward.
//
//sumtype:decl
type Wire interface{ isWire() }
type FromClient struct{ Request RawRequest } // original bytes and headers
type Synthesized struct{}                    // internal caller; the service encodes the typed input itself

// RawHTTPHandler serves a raw operation (codegen/services/<svc>.json "rawOperations"): the
// function URL data plane and the SNS confirm, unsubscribe and certificate links. It runs the
// full pipeline (journal, faults, access record) and owns the HTTP body in both directions.
type RawHTTPHandler func(ctx context.Context, c *Call, r *http.Request, w http.ResponseWriter) *awserr.Error

// Op is a typed, immutable handle on an operation, one generated var per operation. Bindings
// never live on the descriptor: each Kernel holds its own Bindings table indexed by Operation.Ordinal,
// so several kernels (parallel tests, understudytest) coexist without a data race.
type Op[In, Out any] struct{ desc *registry.Operation }
func NewOp[In, Out any](d *registry.Operation) Op[In, Out]

type Binder struct{ k *Kernel; svc *registry.Service }
func Bind[In, Out any](b *Binder, op Op[In, Out], h Handler[In, Out], cov registry.BindCoverage, opts ...BindOption)
func BindRaw[In any](b *Binder, op Op[In, RawOutput], h RawHandler[In], cov registry.BindCoverage, opts ...BindOption)
func BindRawHTTP(b *Binder, op *registry.Operation, h RawHTTPHandler, cov registry.BindCoverage)
func WithValidator[In any](v func(*In) []awserr.Violation) BindOption // stateless; runs at step 7 after the generated validator, before faults
// Binding the same operation twice fails New; binding an operation outside the binder's service fails New.

// Decoded is what the edge hands the kernel: the kernel is the only caller of journal.Begin,
// journal.End and access.Record, so a rejected decode and a 501 are journaled by the same code.
//
//sumtype:decl
type Decoded interface{ isDecoded() }
type Ok struct{ In any }
type Rejected struct{ Err *awserr.Error }

// Result is what a handler returns to the pipeline.
//
//sumtype:decl
type Result interface{ isResult() }
type Output struct{ Out any; Status int }
type Failure struct{ Err *awserr.Error }

func (k *Kernel) Dispatch(ctx context.Context, c *Call, d Decoded) Result

// Invoke runs an internal call through the full pipeline minus wire codec and auth. It returns
// Internal (never panics) when parent is holding a cell lock.
func Invoke[In, Out any](ctx context.Context, k *Kernel, parent *Call, op Op[In, Out], t Target, in *In) (*Out, *awserr.Error)

type Target struct {
	Scope aws.Scope      // from the target resource ARN
	As    PrincipalMode  // who AWS would authorize this call as
	Via   ref.Via
	At    clock.Instant  // admission instant for deferred work (the timer's due instant); zero means the parent's Admitted
}

// PrincipalMode: AsCaller is a forward-access call charged to the original principal (S3, Secrets
// Manager and SQS to KMS); AsExecutionRole is the function role (ESM polling, Lambda logs);
// AsService is a resource-policy-gated delivery (SNS, S3, EventBridge to their targets).
//
//sumtype:decl
type PrincipalMode interface{ isPrincipalMode() }
type AsCaller struct{}
type AsExecutionRole struct{ Role aws.ARN }
type AsService struct{ Name string } // "sns.amazonaws.com"

// ServiceDeps is what wire.go hands every service constructor; service-specific settings are a
// per-service Options struct with usable zero values, filled from config by wire.go.
type ServiceDeps struct{ Kernel *Kernel; Binder *Binder; Clock *clock.Clock; IDs *ids.Source; Blob *blob.Store; Identity *identity.Store; Docker *docker.Client; Log *slog.Logger }
```

Generated binder and a hand-written service:

```go
// Code generated by understudy-gen from api-models-aws@1cfdfb4 sqs-2012-11-05.json; DO NOT EDIT.
package sqsapi

var OpSendMessage = kernel.NewOp[SendMessageInput, SendMessageOutput](&descSendMessage) // descSendMessage is immutable

type Binder struct{ b *kernel.Binder }
func NewBinder(b *kernel.Binder) Binder

func (b Binder) SendMessage(h kernel.Handler[SendMessageInput, SendMessageOutput], cov registry.BindCoverage, opts ...kernel.BindOption)
// one method per model operation and per dev.understudy#internalOperation; a Raw variant per operation only when codegen/services/<svc>.json sets "rawBinding": true
```

```go
package sqs

type Service struct {
	k     *kernel.Kernel
	cells *store.Partitioned[aws.Scope, State]
}

func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{k: d.Kernel, cells: store.NewPartitioned[aws.Scope, State](d.Kernel.Gate(), newState)}
	b := sqsapi.NewBinder(d.Binder)
	b.CreateQueue(s.createQueue, registry.Full, kernel.WithValidator(validateCreateQueue))
	b.SendMessage(s.sendMessage, registry.Full)
	b.AddPermission(s.addPermission, registry.StoredOnly("statements stored in Policy; not enforced"))
	// unbound operations are Unbound: 501 in the native envelope, listed in coverage
	d.Kernel.Timers().Handle(TimerVisibility, s.onVisibility)
	return s
}
```

Generated input types: `required` members are plain values; optional members are pointers (`new(expr)` makes them cheap); `NullableInteger` style members are pointers so absent differs from zero (`sqs.md 6.1`). Output types follow the same rule, and the overlay trait `dev.understudy#alwaysEmit` promotes the output members whose absence crashes the Terraform provider (`tf-remaining.md 11`) to plain values. `@streaming` blob members (S3 bodies) are generated as `smithy.Body` (input: a bounded reader whose trailer-bound members resolve at EOF; output: a `smithy.BodySource` with size and a range opener), never `[]byte`. The pipeline's encode step treats a zero-valued required timestamp as an `Internal` error in strict mode (tests and conformance), so a forgotten field is a failing test, not a provider crash.

`*awserr.Error` does not implement `error`; `AsError()` exists for the rare boundary that needs one. This keeps the typed-nil trap out of handler code, and the plain-`error` returns of `Export`, `Deserialize` and config never mix with it.

### 3.3 Errors

```go
package awserr

// Error is the only failure a handler returns. It is always AWS-shaped. It does not implement error.
type Error struct {
	ID       Identity
	Message  string            // empty only where AWS sends none
	Messages []ProtocolMessage // per-protocol override of Message (SQS NonExistentQueue over Query says "... for this wsdl version.")
	Fields   []Field           // extra wire members: S3 BucketName, Key, UploadId, ArgumentName; Lambda Type, Reason
	Headers  []Header          // Retry-After, x-amz-bucket-region
	Origin   Origin
}

// Identity is everything a codec needs to render an error on any protocol.
// For modeled and overlay-added shapes it is a generated constant. Composite literals outside
// internal/api and internal/awserr are forbidden by lint (forbidigo on awserr.Identity{).
type Identity struct {
	Shape           smithy.ShapeID // com.amazonaws.sqs#QueueDoesNotExist, or a synthetic id for generic errors
	JSONType        string         // __type exactly as AWS sends it: overlay namespace (awsJson1_0), bare name (1.1)
	QueryCode       string         // awsQueryError.code, else shape name
	Status          int            // httpError, else 400 client / 500 server (statusOverride from overlays)
	QueryModeStatus int            // awsQueryError.httpResponseCode when x-amzn-query-mode: true (CloudWatch 404)
	Fault           Fault          // Sender | Receiver
	MessageKey      MessageKey     // MessageLower | MessageUpper | NoMessage, from the shape
	Retryable       bool           // retryable trait or the throttling catalogue
}

// Origin says why an error exists, which decides how it renders.
//
//sumtype:decl
type Origin interface{ isOrigin() }
type Modeled struct{}
type Generic struct{}                          // SerializationException, UnknownOperationException, auth family
type Validation struct{ Violations []Violation }
type Unimplemented struct{ Op ref.OperationID }
type Injected struct{ Rule ref.RuleID }
type Internal struct{ Cause error }            // a bug: 500 InternalFailure; Cause is journaled and logged, never sent

type Violation struct {
	Path       Path       // lowerCamel segments; indexed (targets.1.member.id) or rolled up (Lambda), per overlay style
	Value      Rendered   // Java toString rendering; blobs omitted
	Constraint Constraint // Required | LengthMin | LengthMax | RangeMin | RangeMax | Pattern{Echo} | Enum{Values} | Encoding{InvalidUTF8}
}

type Style uint8 // Coral (AWS text) | SmithyFramework (fieldList envelope; synthetic test services only)

func Internalf(cause error) *Error
func (e *Error) AsError() error
```

```go
package kernel // the helpers that need descriptors live here, not in awserr

func NotImplemented(op *registry.Operation) *awserr.Error
func Validate(op *registry.Operation, v []awserr.Violation) *awserr.Error // picks code and style from the overlay per (operation, member path, constraint kind), then operation, then service; reports the first violation in member order when a set spans two codes
```

The current contract: `registry.Operation` has no validation-policy field, and generated packages may import only the vocabulary packages (section 2), so the policy runtime is written once in `internal/codegen/validationpolicy` and copied into each `validate_gen.go`. Every generated input type has the method `ValidationError([]awserr.Violation) *awserr.Error`, and `kernel.Validate` reaches the policy by type-asserting the decoded `*In` on that method inside `kernel.NewOp[In, Out]`. Moving the policy types into `awserr` and adding the descriptor field (so the generator emits data only) is not done yet.

Generated constructors read like the SDK's: `sqsapi.ErrQueueDoesNotExist(msg)`, `s3api.ErrNoSuchBucketPolicy(bucket)`. No string codes in handler code. Rules the codecs share, taken from `error-matrix.md 7` (D1 to D10, H1 to H10):

- Status comes from `httpError`; SQS `PurgeQueueInProgress` is 403 because the SQS model sets `httpError` equal to `awsQueryError.httpResponseCode` (`sqs.md` finding 4). CloudWatch's 404 under `x-amzn-query-mode: true` is `QueryModeStatus`.
- Message member spelling is the shape's own (`Message` for Secrets Manager, `message` for KMS); Go v2 drops a differently cased message on modeled errors.
- Unmodeled codes services really send are overlay shapes, so they are generated, typed and listed in coverage: the 20 S3 codes and their context members (`wire-rest.md 2.4`), SQS `InvalidParameterValue` and `MissingParameter` (`__type` `InvalidParameterValueException` and `MissingRequiredParameterException`), CloudWatch `ValidationError`, Lambda `RequestEntityTooLargeException`, every service's generic vocabulary (`overlays/common.json`). The vocabulary is per protocol family: `common.json` declares one service shape per family (`dev.understudy.defaults#JSONFamily`, `#QueryFamily`) whose standard Smithy `errors` list is that family's generic errors, so no new trait is needed; each generated package emits them as `GenericErrors(f)`. `awserr.Catalogue` stays the runtime copy for the edge and the kernel, which act before a service is known, and a test in `internal/api` keeps the two in step for every service.
- The constraint-violation code comes from `dev.understudy#validationError`, which applies at service, operation or member level with an optional constraint-kind filter: `ValidationException`, `ValidationError`, `InvalidParameterException`, `InvalidParameterValueException`, or a hand-text style (SQS `Value X for parameter P is invalid. Reason: ...`, SNS `Invalid parameter: P Reason: ...`) with per-member reason templates. The same trait carries `enumPolicy: closed | open | handler`; `handler` defers a member's checks to the service so step 7 cannot pre-empt a business error (SQS `InvalidAttributeName`, Lambda's creatable runtime subset). Each recorded row in `error-matrix.md`, `lambda-api.md 2.1`, `lambda-events.md 1.3`, `sqs.md 2.4` and `sns.md 1` is a golden test.
- Check order follows the Coral front end: unknown operation, auth, deserialization, constraint validation (generated, then the typed per-operation validator), faults, business rules.

### 3.4 Registry and coverage

```go
package registry // immutable descriptors; nothing here changes after generation

type Service struct {
	ID               ref.ServiceID
	Shape            smithy.ShapeID
	SigningName      string          // "monitoring" for CloudWatch
	IAMPrefix        string          // "cloudwatch" for CloudWatch (smithy-models.md 3.1)
	ARNNamespace     string
	Protocols        []ref.ProtocolID // model protocols plus extras from codegen/services (SQS awsQuery)
	JSONTargetPrefix string           // AmazonSQS, TrentService, GraniteServiceVersion20100801
	QueryVersion     string
	XMLNamespace     string
	QueryCompatible  bool
	MalformedRequestError awserr.Identity // overlay malformedRequestError; zero means the codec's protocol default
	Operations       []*Operation    // every operation, sorted; Ordinal is the index
	Routes           *RouteTable     // REST services: generated routes plus codegen/services route overrides, ordered by Smithy specificity
}

type OperationKind uint8 // Modeled | Internal (dev.understudy#internalOperation: typed, never claimed by the edge) | Raw (rawOperations: RawHTTPHandler, claimed by a prefix or host rule)

type Operation struct {
	Service         *Service
	ID              ref.OperationID
	Ordinal         int
	Kind            OperationKind
	Shape           smithy.ShapeID
	Input, Output   *smithy.Schema
	Errors          []awserr.Identity // modeled plus overlay errors, for fault-rule validation
	IAM             []IAMAction       // primary (same-name or mapped) first, from the Service Reference table
	HTTP            *HTTPBinding      // REST services only
	SuccessCode     int               // dev.understudy#successCode, else the model's http code, else 200
	MaxRequestBytes int64             // overlay cap; 0 means none, and the edge and codec apply the protocol default
	Readonly, Idempotent bool
}

// BindCoverage is what a bind site states. Status is what the kernel reports: Bound{Coverage} | Unbound.
//
//sumtype:decl
type BindCoverage interface{ isBindCoverage() }
type Full struct{}
type StoredOnly struct{ Note string } // accepted, stored, echoed; no runtime effect
type Partial struct{ Note string }    // implemented with named gaps

type Registry struct{ /* services by id, operations by (service, ordinal), JSON target and Query (Action, Version) tables */ }
func New(services ...*Service) (*Registry, error) // fails on duplicate ids or ordinals
```

Bindings are per kernel (`kernel.Bindings`, indexed by `Operation.Ordinal`); `kernel.New` fails when the same operation is bound twice. Runtime coverage (`Bound{Full}`, `Bound{StoredOnly}`, `Bound{Partial}`, `Unbound`) is read from the kernel, never from the descriptor. A `-race` test builds two kernels concurrently with different handlers for the same operation.

Wiring is explicit in `internal/server/wire.go`: construct each service with `kernel.ServiceDeps` and its `Options`, collect every service's `Switches()` and `Stateful` view. No `init()` registration. A service is wired even while its `New` binds nothing, so binding its operations never edits the wiring file.

```go
package evidence // leaf

type Label string // aws-recorded | third-party-recorded | aws-doc | sdk-source | inferred; the one vocabulary for overlays, switches, skip-list entries and parity fixtures
```

```go
package fidelity

// Switch is a behavior chosen without AWS-recorded evidence. Not user-configurable in phase 1.
type Switch[T comparable] struct {
	Name   string         // "logs.filter.term-boundary"
	Value  T
	Label  evidence.Label
	Ref    string         // "docs/research/logs-filter-patterns.md#5"
	Record string         // backlog item that settles it, e.g. "R10/C1"
}

type Entry struct{ Name, Value, Ref, Record string; Label evidence.Label } // type-erased view for coverage
func (s Switch[T]) Entry() Entry
// Every service exposes Switches() []fidelity.Entry; wire.go collects them.
```

`understudy coverage` reports three separate numbers, never summed: protocol compliance (Smithy cases passed, skipped with reason, failed per protocol, from the checked-in baseline), operations (Full, StoredOnly, Partial, Unbound per service against the model; internal and raw operations listed separately), evidence (overlay entries and fidelity switches by label, parity fixtures by label).

### 3.5 State store

```go
package store // no effects, no kernel, no clock; imports only aws, for the shared key codecs

// Gate is shared by every cell in a kernel. Writes take the read side for the microseconds
// they hold a cell; snapshot cut, reset and load take the write side and see a consistent cut.
type Gate struct{ mu sync.RWMutex }

type Cell[S any] struct{ gate *Gate; mu sync.Mutex; s S; rev uint64 } // rev bumps per mutating Update; drives "dirty"

// Update runs fn under the gate's read side and the cell mutex. It is the primitive kernel.Write
// builds on; handlers never call it directly (lint: Update is allowed only in kernel and tests).
func Update[S, R any](c *Cell[S], fn func(s *S) (r R, mutated bool)) R
func Read[S, R any](c *Cell[S], fn func(s *S) R) R
// Rule: R never aliases S. Return values or explicit clones; persisted records are immutable
// values replaced on change, never mutated in place. Service tests read results while writes run under -race.

// Partitioned lazily creates one cell per key on For; Lookup never creates. Keys: aws.Scope for most
// services, aws.AccountID for IAM and STS, a bucket name for S3 buckets, a single key for indexes.
type Partitioned[K comparable, S any] struct{ /* gate, mu, codec, cells map[K]*Cell[S], init func(K) S */ }
func NewPartitioned[K comparable, S any](g *Gate, codec Codec, init func(K) S) *Partitioned[K, S] // Codec{Marshal, Unmarshal} for sections
func (p *Partitioned[K, S]) For(k K) *Cell[S]
func (p *Partitioned[K, S]) Lookup(k K) (*Cell[S], bool)
func (p *Partitioned[K, S]) Keys(kc KeyCodec[K]) []K // sorted by encoded key, for snapshots and resource listing
func (p *Partitioned[K, S]) Delete(k K)              // retires the cell: Cell.Retired() reports true from then on. Call it inside the kernel.Write fn of the cell being deleted, after Retired() returned false, so the delete is atomic against cuts, load and reset
func (c *Cell[S]) Retired() bool                      // kernel.Write checks it inside fn and re-resolves with For, so a long poll holding a cell survives reset and load
func (p *Partitioned[K, S]) Export(kc KeyCodec[K], version int) ([]Section, error) // one Section per non-zero cell; the per-service Export loop lives here
func (p *Partitioned[K, S]) Import(kc KeyCodec[K], version int, ss []Section) error // refuses a mismatched version
func (p *Partitioned[K, S]) Reset(keep func(K) bool)

type Section struct{ Path string; Version int; Data []byte; Blobs []string } // Path "000000000000/us-east-1.json", relative to the service; snapshot adds and strips "services/sqs/". Blobs are sha256 hex refs the section pins

// KeyCodec maps a partition key to its section path. An empty encoding means the key has none
// (a zero or non-aws scope), and Export refuses it. ScopeKeys() ("<account>/<region>"),
// AccountKeys() (12 digits) and StringKeys() are the shared codecs.
type KeyCodec[K any] struct{ Encode func(K) string; Decode func(string) (K, error) }

type WaitKey string
type Waiters struct{ /* channel-based, ctx-aware; a registration ends when ctx does */ }
func (w *Waiters) Wait(ctx context.Context, k WaitKey) <-chan struct{}
func (w *Waiters) Wake(k WaitKey)
```

```go
package kernel

// Write is the only write entry point for handlers and timer callbacks. It marks the Call as
// holding (Invoke and a nested Write return Internal while holding), reads now under the lock
// (the commit instant for state deadlines and server-assigned timestamps), applies Arm and
// Disarm to the scheduler inside the critical section in commit order, and runs Wake and
// Deliver after unlock, stamped with the Call's Cause and counted by the work tracker. It takes
// the Partitioned and the key rather than a resolved cell, so a cell that reset or snapshot load
// retired is resolved again with For.
func Write[K comparable, S, R any](c *Call, p *store.Partitioned[K, S], key K, fn func(s *S, now clock.Instant, tx *Tx) (R, *awserr.Error)) (R, *awserr.Error)

type Tx struct{ /* effects */ }
func (tx *Tx) Wake(k store.WaitKey)
func (tx *Tx) Arm(k clock.TimerKey, at clock.Instant) // replaces the same key; the kind must be registered or New fails
func (tx *Tx) Disarm(k clock.TimerKey)
func (tx *Tx) Deliver(r delivery.Request)              // inserted into the outbox cell before the call returns
```

The convention, enforced by per-service tests, is validate first, mutate last: if `fn` returns an error the state is unchanged. Timer callbacks and `Rearm` re-read state and do nothing when their condition no longer holds (a visibility timer for a deleted message is a no-op). Writes that start in a timer get a synthetic `Call` with `Origin Internal{Via: Timer}`, so there is one write path.

## 4. Request lifecycle

```
External request
  edge:  1 admit      body cap (op's MaxRequestBytes or protocol default; a counting reader for streaming members), request id, Admitted = clock.Now()
         2 auth       auth.Parse -> SigV4Header | SigV4Query | SigV2Header | SigV2Query | Anonymous; verify in step 4a when configured (after the claim, before decode)
         3 identity   principal from the access key; Scope = account from key, region from RegionSource (7)
         4 claim      Route{service, protocol, operation | unknown}  (order in 4.1)
        4a verify     only with sigv4: verify: every parsed credential form, in the claimed service's dialect; a failure is dispatched as Rejected (section 7)
         5 decode     codec + generated Deserialize -> Decoded = Ok{*In} | Rejected{Generic error}; streaming members bind the head only
  kernel.Dispatch(ctx, call, decoded)                 <-- kernel.Invoke enters here for internal calls
         6 journal.Begin(APICall with redacted input); an unknown operation, an Unbound operation (501) or a Rejected decode ends here, journaled and access-recorded by the same code
         7 validate   generated Validate, then the bound typed validator -> Violations -> kernel.Validate(op, v)
         8 fault.Before   first matching Request-phase rule may short-circuit (Error, Throttle, Latency)
         9 handler    (pulls a streaming body only after its own pre-checks, so 100-continue and early rejects work)
        10 fault.After    AfterCommit, StatusOKError (S3 200-with-error)
        11 access.Record(principal mode, primary action + actions the binding declared used (WithActions, Declare), or the whole generated list when it declared none, touched)   (never for injected faults; an internal operation only when its bind site declares actions)
        12 journal.End(outcome, latency)
  edge: 13 encode     generated Serialize + codec, or the protocol's error writer; Date header is wall time; streaming outputs stream from blob
```

Internal calls skip 1 to 5: `Invoke` builds a `Call` with `Origin Internal{Via}`, the target scope, the principal the `PrincipalMode` resolves to, `Cause{Parent: parent.Event, Trace: parent.Cause.Trace}`, `Admitted = Target.At` when set (deferred work is stamped with the instant that triggered it, never `Now()`), and runs 6 to 12. Validation runs for internal calls too; it is cheap and catches a delivery that builds an input AWS would reject. `Deliver` effects are inserted into the outbox before step 12 returns; `Wake` effects run after unlock; the work tracker counts both.

A panic in steps 7 to 10 (validators, checks, resource resolvers, the handler and the after-commit work) is recovered by one deferred recover over those steps and becomes `Internal` (500 `InternalFailure`) with the stack in the journal and the log, never in the response. Every goroutine the kernel starts (`Kernel.Go`: delivery attempts, pollers, stream writers, timer dispatch) has the same recovery.

### 4.1 Front-door claiming

Order, first match wins (`wire-json-query.md` Implications 1, `wire-rest.md 5`). AWS dispatch uses `r.RequestURI` and `EscapedPath()`, never `http.ServeMux`, which redirects `.`, `..` and `//` paths and loses `%2F` (`go-toolchain.md 5`).

1. Path prefix `/_understudy/` on the main listener: admin API, SNS `SigningCertURL` and unsubscribe or confirm links, function URL path fallback `/_understudy/lambda-url/<url-id>/...`. Bucket names cannot start with `_`, so this never shadows S3. The container listener serves only `GET /_understudy/v1/health` and `GET /_understudy/v1/clock` from this prefix. Paths whose first segment is `_localstack` (a migrated `/_localstack/health` check) are claimed here too, before S3: they get a 404 in the admin API's JSON error envelope that names `GET /_understudy/v1/health`, and are journaled as `Unclaimed`. A bare `/health` is not claimed, since `health` is a valid bucket name.
2. Host `<url-id>.lambda-url.<region>.<base>`: function URL data plane.
3. Host `<account>.<base>` with path `/v20180820/`: S3 Control tagging (`tf-storage.md` 1).
4. Parse auth. The credential-scope service is the router key from here on.
5. `Smithy-Protocol: rpc-v2-cbor` and a `/service/<Name>/operation/<Op>` path: rpcv2cbor. Reject if `X-Amz-Target` is also present.
6. `X-Amz-Target`: awsjson; service from the target prefix table, cross-checked against the signing name.
7. Form media type (parameters ignored) or a GET with `Action`: awsquery; service from `(Action, Version)`, confirmed by the signing name. SQS legacy Query is also accepted on `/<account>/<queue>`, taking the queue from the path.
8. REST: signing name `lambda` is the Lambda route table; `s3`, a presigned query credential or SigV2 is S3; unsigned: a Host matching a configured S3 base domain with a DNS-valid bucket label is S3 virtual-hosted; a `^/\d{4}-\d{2}-\d{2}/` path that matches a Lambda route is Lambda only when no bucket of that name exists (`wire.go` injects `BucketExists(name) bool`, the one routing input that reads state); anything else that is unsigned, SigV2 or scoped to `s3` is S3 path-style. A request signed for any other service that rules 5 to 7 did not claim is answered by that service's `unknownOperationError` or `malformedRequestError` identity (SQS GET without `Action`: 404 `UnknownOperationException` XML; DynamoDB without `X-Amz-Target`: 400; wrong Content-Type: 404 `<UnknownOperationException/>`; `sqs.md 2.3`, `error-matrix.md` finding 6 and H7), never by S3.
9. Nothing claimed: AWS-shaped 400 and a journal `Unclaimed` event listing why each rule declined. `Unclaimed` is the one journal event the edge writes itself.

Media types match by essence (ignore `; charset=`); header names case-insensitively. Every response carries exactly one `Date` header (wall), `x-amzn-RequestId` or the protocol's equivalent, and S3's `x-amz-request-id` and `x-amz-id-2` (`s3.md` finding 7). The edge is codec-agnostic: codecs register per `ref.ProtocolID` in `wire.go`, and the REST matcher works over `registry.RouteTable` (Smithy specificity: literal beats label beats greedy; path beats query literals; more segments beat fewer; S3 rows add subresource-key presence and required headers).

## 5. Code generation

### 5.1 Inputs

| Input | Location | Pin and integrity |
|---|---|---|
| 12 service models, Smithy 2.0 JSON AST, verbatim | `third_party/api-models-aws/` | commit `1cfdfb4bb9ea858e65623e66d73af04bec353201`; `UPSTREAM.json` with git blob SHA-1 and sha256 per file; `cmd/vendor -verify` runs offline in CI (`smithy-models.md 10`) |
| Protocol compliance corpus, flattened AST | `third_party/smithy-protocol-tests/model.json` | Smithy 1.74.0; `PIN.json` with the six jar sha256 values and the CLI sha256; regenerated by a human with `smithy ast --no-config --flatten` (`smithy-protocol-tests.md 5`) |
| Operation to IAM action table, derived | `third_party/aws-service-reference/actions.json` | provenance per service (`Version`, `modified`, body sha256); raw JSON not vendored until its licence is settled (ADR 0016) |
| Overlays | `codegen/overlays/*.json` | hand-owned, reviewed; every entry carries `dev.understudy#evidence` |
| Service generation config | `codegen/services/*.json` | hand-owned |

Network is used only by `cmd/vendor` when a human bumps a pin. `go generate` and every test are offline.

### 5.2 Overlay format (ADR 0002)

Smithy JSON AST fragments: `apply` entries for traits on existing shapes and members, new `structure` shapes for unmodeled errors, and traits in the `dev.understudy#` namespace. Example:

```json
{
  "smithy": "2.0",
  "shapes": {
    "com.amazonaws.sqs#SendMessageRequest$DelaySeconds": {
      "type": "apply",
      "traits": {
        "smithy.api#range": {"min": 0, "max": 900},
        "dev.understudy#evidence": {"label": "aws-doc", "source": "https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_SendMessage.html"}
      }
    },
    "com.amazonaws.sqs#InvalidParameterValue": {
      "type": "structure",
      "members": {"message": {"target": "com.amazonaws.sqs#ExceptionMessage"}},
      "traits": {
        "smithy.api#error": "client",
        "smithy.api#httpError": 400,
        "aws.protocols#awsQueryError": {"code": "InvalidParameterValue", "httpResponseCode": 400},
        "dev.understudy#jsonType": "InvalidParameterValueException",
        "dev.understudy#evidence": {"label": "third-party-recorded", "source": "https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L3759", "validated": "2025-09-22"}
      }
    }
  }
}
```

The closed set of `dev.understudy#` traits; the generator fails on anything else in the namespace:

| Trait | On | Meaning |
|---|---|---|
| `evidence` | any overlay entry | `{label, source, validated?}`; required; labels are `evidence.Label` values: `aws-recorded`, `third-party-recorded`, `aws-doc`, `sdk-source`, `inferred` |
| `errorNamespace` | service | awsJson1_0 `__type` namespace (`com.amazonaws.sqs`, `com.amazonaws.dynamodb.v20120810`, `com.amazonaws.cloudwatch.v2010_08_01`) |
| `jsonType` | error shape | `__type` override for unmodeled names |
| `messageByProtocol` | error shape | per-protocol message text (`{"awsquery": "... for this wsdl version."}`) |
| `validationError` | service, operation or member | constraint-failure code and text style; on a member: `{code, style?, constraints?: [...], reason?: "template", enumPolicy?: closed/open/handler}` |
| `validationPathStyle` | service | `indexed` or `rolledUp` (Lambda) |
| `successCode` | operation | HTTP status of a success where the model is wrong or silent (S3 `PutBucketPolicy` 204, Lambda `DeleteFunction` 204; `s3.md 14.1`, `lambda-api.md 2`) |
| `queryKeyAlias` | member | the Query wire key when two members collide (`ReceiveMessage.AttributeNames` and `MessageSystemAttributeNames` both `AttributeName.N`; `sqs.md 2.3`) and how to resolve the ambiguity on decode: `{key, decode: receive | ignore}`, emitted as `smithy.Member.QueryKeyAlias`; the awsQuery decoder leaves an `ignore` member unset |
| `emptyCollection` | service or member | `omit` or `emit` for empty required collections (SQS omits `Successful`, `Failed`, `Tags`; IAM emits `<Roles/>`; `sqs.md 2.1`, `wire-json-query.md 8.5`) |
| `internalOperation` | operation shape added by the overlay | typed, validated, journaled operation that the edge never claims and the access recorder skips (`sqs.HoldVisibility`, `logs.PutLogEventsInternal`); coverage lists it separately |
| `patternEcho` | string shape | echo text when it is not "pattern minus anchors" |
| `patternValidator` | string shape | hand validator for the 7 lookaround patterns RE2 cannot compile |
| `alwaysEmit` | output member | promote to a plain value in the generated output type |
| `maxRequestBytes` | operation | body cap (Lambda `CreateFunction` 70,167,211; SQS above the encoded 1 MiB batch) |
| `throttleError` | service | the identity `fault.Throttle` resolves to |
| `notConfigured` | S3 Get* operation | error shape or default document when the subresource is unset |
| `malformedRequestError` | service | identity for undecodable bodies (Lambda `InvalidRequestContentException`, DynamoDB `SerializationException`) |
| `statusOverride` | error shape | KMS documented 400/400/500 where the model says 404/409/503 (`iam-sts-kms.md` Implications 3) |
| `unknownOperationError` | service | identity for an operation absent from the model, per protocol family |
| `queryErrorEnvelope` | awsQuery service | `{namespace?, detail?}`: how `ErrorResponse` differs from Smithy's example, emitted as `registry.Service.QueryErrorEnvelope` (CloudWatch and IAM declare the service's `xmlns`, SQS sends an empty `<Detail/>`; `error-matrix.md 3`) |

Overlays are applied after loading and before validation of the merged model. Applying a trait the vendored model already has with a different value is an error unless the entry says `"replace": true`, so a model bump that fixes a gap surfaces as a failing generate.

### 5.3 Service generation config

```json
{
  "service": "com.amazonaws.sqs#AmazonSQS",
  "package": "sqsapi",
  "model": "sqs",
  "iamPrefix": "sqs",
  "iamPrimary": [],
  "extraProtocols": ["aws.protocols#awsQuery"],
  "rawBinding": false,
  "rawOperations": [],
  "routeOverrides": []
}
```

`model` names the service's directory in `third_party/api-models-aws/UPSTREAM.json`. `iamPrefix` is the IAM action prefix (`cloudwatch` for CloudWatch, `events` for EventBridge, `logs` for CloudWatch Logs; `smithy-models.md 8`), required because the model's `arnNamespace` differs from it for some services. It is kept beside the Service Reference table: generation fails when `actions.json` has no entry under it, and `TestReferencePrefixesMatchTheServiceConfigs` checks it against `cmd/vendor`'s `referencePrefixes()`. An operation's IAM actions come from the Service Reference table; the primary one is the action of the same name, else the single form of a `*Batch` action, else the sole action under the service's own prefix. `iamPrimary` (`[{operation, action, reason}]`) names the primary only where those rules leave it ambiguous (S3 `CopyObject` is `s3:PutObject`, addressed to the destination); a redundant entry fails generation, so the list stays minimal and a Service Reference change that settles the ambiguity is noticed.

Types, serde, validators and a binder method are generated for every operation in the model, so adding an operation needs no generator run. `rawBinding: true` (DynamoDB only) adds a `<Op>Raw` binder method per operation. `rawOperations` declares synthetic operations outside the model that still need a descriptor, an IAM action, journal, faults and coverage: Lambda `InvokeFunctionUrl` (`lambda:InvokeFunctionUrl`, claimed by the function URL host and path rules), SNS `ConfirmSubscriptionLink`, `UnsubscribeLink` and `SigningCert` (claimed under `/_understudy/`); each gets a `registry.Operation` of kind `Raw` and a `BindRawHTTP` method. `routeOverrides` carries the hand S3 dispatch rows (`wire-rest.md` Implications 2) so the generated `RouteTable` is complete and the edge needs no S3-specific code.

### 5.4 Stages

```
verify      cmd/vendor -verify: UPSTREAM.json and PIN.json hashes, offline
load        encoding/json with UseNumber; reject unknown shape types, mixins, apply, set (smithy-models.md 2)
overlay     merge codegen/overlays; reject conflicts and unknown dev.understudy traits
closure     service -> operations -> resources (lifecycle, operations, collectionOperations, nested; Lambda's 72 resource-bound ops)
classify    every trait ID is honor | generation-only | ignore (smithy-models.md 4.3); an unclassified trait fails the build
effective   member traits merged over target traits
regex       ECMA-262 -> RE2: \uXXXX -> \x{XXXX}, \p{Print} -> [[:print:]], drop lone surrogates;
            semantics too: \s, \S, \w, \W, \d, \b and `.` are rewritten to explicit ECMA classes
            (Unicode-aware \s, `.` excludes \r, U+2028, U+2029; smithy-models.md 6.2), with a rapid
            differential test against an ECMA semantics table; lookarounds require
            dev.understudy#patternValidator; every emitted pattern must compile
roles       each shape is Input-only, Output-only or Both by reachability; Deserialize and Validate are
            emitted for Input shapes, Serialize for Output and error shapes, both for Both, for
            rawBinding services and for protocoltest/gen
IR          typed IR: ServiceIR, OperationIR, ShapeIR, MemberIR, ErrorIR, HTTPBindingIR, RouteIR
emit        per service package; go/format; sorted iteration; temp file + rename
```

String `length` counts Unicode scalar values (`utf8.RuneCountInString`). `jsonx` decodes input with invalid UTF-8 and lone surrogates allowed so validators classify them (SQS `InvalidMessageContents`), never as a `SerializationException`.

### 5.5 Outputs per service (`internal/api/<svc>api/`)

| File | Content |
|---|---|
| `service_gen.go` | `registry.Service` descriptor with every operation |
| `types_gen.go` | structures, enums (string type, constants, `Known()`), unions (sealed interfaces), error structs |
| `schema_gen.go` | static `smithy.Schema` values: wire names, `xmlName`, `xmlFlattened`, `xmlNamespace`, `timestampFormat`, HTTP bindings, defaults, `sensitive` |
| `serde_gen.go` | `Serialize(smithy.Serializer)` and `Deserialize(smithy.Deserializer) error` per the shape's role (5.4); streaming members as `smithy.Body` |
| `validate_gen.go` | constraint checks emitting `awserr.Violation` in AWS's per-member order (pattern, then max length; `error-matrix.md 4`) |
| `errors_gen.go` | `Identity` constants and constructors per error shape; per-operation error lists |
| `ops_gen.go` | immutable `registry.Operation` descriptors, IAM actions, `MaxRequestBytes`, `SuccessCode`, kinds (modeled, internal, raw) |
| `bind_gen.go` | `kernel.Op` handles and the typed `Binder` over `kernel.Binder` |
| `routes_gen.go` | REST only: `registry.RouteTable` rows: Lambda routes by Smithy specificity; S3 rows `(method, scope, query-key presence, required header)` plus `routeOverrides`, ordered by specificity, `x-id` stripped (`wire-rest.md` Implications 2) |
| `redact_gen.go` | `Redact()` per input and output type from `sensitive` plus overlay additions |
| `examples_gen_test.go` | each model `examples` input decoded and validated (260 across 7 services); an over-strict overlay or a bad regex translation fails here |

Header on line 1: `// Code generated by understudy-gen from api-models-aws@1cfdfb4 <file>; DO NOT EDIT.`; no timestamps or paths.

### 5.6 Protocol-test generation

The same generator runs over the corpus AST and emits `internal/protocoltest/gen/<testsvc>/`: types, schema, serde and binders for the synthetic services (`JsonRpc10`, `QueryCompatibleJsonRpc10`, `JsonProtocol`, `AwsQuery`, `RestJson`, `RestJsonValidation`, `RestXml`, `RestXmlWithNamespace`, the S3 slice, `RpcV2Protocol`) plus `cases_gen.go` with every server-runnable case as a typed Go literal (not a test file, since the harness in `internal/protocoltest` imports the tables and Go never lets one package read another's test files). The corpus uses shape kinds the service models do not (documents, `byte`, `short`, `float`, `bigInteger`, `intEnum`, sparse collections); the generator supports them because the corpus needs them.

### 5.7 Determinism and drift

`just generate` runs `cmd/vendor -verify` then `go generate ./...`; CI runs it with `git diff --exit-code` and `git status --porcelain`. `docs/coverage.md` is regenerated by the same recipe and drift-checked. A generator golden test feeds small model fragments through every stage. Generated code has its own schema and codec tests because linters skip it.

Budgets, measured by `just budgets` and gated in CI; a breach is fixed by the role pruning in 5.4 or by tightening the closure, never by waiving the gate:

| Measure | Ceiling |
|---|---|
| stripped `linux/amd64` binary | 48 MiB |
| time from `serve` start to a ready `/health` | 250 ms |
| idle RSS after start, no Docker | 128 MiB |
| cold `go build ./...` on the CI runner | 180 s |
| `golangci-lint run ./...` wall time | 600 s |
| journal ring default | sized to the RSS budget: 100,000 events or 128 MiB, whichever first |

### 5.8 Recipes

Adding an operation: write `func (s *Service) listQueueTags(ctx, c, in *sqsapi.ListQueueTagsInput) (*sqsapi.ListQueueTagsOutput, *awserr.Error)`; add `b.ListQueueTags(s.listQueueTags, registry.Full)` in `New`; add overlay entries if research shows AWS enforces more than the model; write a test through `kernel.Invoke` in a synctest bubble; `understudy coverage` and the conformance suites pick it up over every protocol the service speaks.

Adding a service: `cmd/vendor -add <dir>`; `codegen/services/<svc>.json` and `codegen/overlays/<svc>.json`; `just generate`; copy STS's skeleton; add it to `wire.go`; bind handlers one at a time; add a conformance case per SDK.

## 6. Protocol layer

### 6.1 Shared serde

```go
package smithy

type Serializer interface {
	WriteStruct(s *Schema); CloseStruct()
	WriteList(m *Member, n int); CloseList()
	WriteMap(m *Member, n int); CloseMap()
	WriteString(m *Member, v string); WriteBool(m *Member, v bool)
	WriteInt64(m *Member, v int64); WriteFloat64(m *Member, v float64)
	WriteTimestamp(m *Member, v time.Time) // format from the member's effective trait and protocol default
	WriteBlob(m *Member, v []byte); WriteDocument(m *Member, v document.Value); WriteNull(m *Member)
}

type Deserializer interface { /* mirror; ReadStructMember returns the next member present on the wire */ }

// Streaming payloads. A @streaming blob member is a Body, never []byte.
type Body interface {
	Open() (io.ReadCloser, error) // the handler pulls it after its pre-checks; net/http sends 100 Continue on the first read
	Len() (int64, bool)           // declared length when known
	Trailer(name string) (string, bool) // a trailer-bound member; valid only after EOF (aws-chunked x-amz-checksum-*)
}
type BodySource interface { Size() int64; Range(off, n int64) (io.ReadCloser, error) } // outputs stream from blob
```

REST protocols put `restbind` in front: label, query, header, prefix-header and payload members are split per `HTTPBinding`, and the body codec sees only the unbound members. Members bound to trailers are exposed as deferred values that the `aws-chunked` reader resolves at EOF; `BadDigest`, `IncompleteBody` and `XAmzContentSHA256Mismatch` are therefore handler outcomes after the body is consumed. Caps on streaming members are enforced by a counting reader (`EntityTooLarge`), never by buffering. Codecs are lenient on input and exact on output (`wire-rest.md` Implications 9).

### 6.2 Per-protocol contracts

| Codec | Services | Must get right |
|---|---|---|
| `awsjson` 1.0 and 1.1 | SQS, DynamoDB, CloudWatch (1.0); Logs, Secrets Manager, KMS, EventBridge (1.1) | accept empty body, `{}`, unknown and `null` members, `charset`, gzip for `PutMetricData`; emit plain-digit integers, epoch-second timestamps with ms precision, padded base64, `{}` for any modeled output; `__type` always (namespaced for 1.0 from the overlay, bare for 1.1); message under the shape's own key; `x-amzn-query-error: <code>;<Sender or Receiver>` on every error from SQS and CloudWatch, regardless of `x-amzn-query-mode`, with the `awsQueryError` code when the shape has the trait, else the shape name, and the catalogue's Query spelling for generic errors (`wire-json-query.md` 11 to 12, `error-matrix.md` 3). Built on `jsonx` only |
| `awsquery` | SNS, IAM, STS, CloudWatch legacy, SQS legacy | indexed keys sorted numerically (`member.10` after `member.2`), `+` and `%XX`, empty list keys, GET form; always `<OpResponse xmlns><OpResult/><ResponseMetadata><RequestId/></ResponseMetadata></OpResponse>`, Result whenever the op has output, envelope even for `Unit` (boto3 raises on an empty body); lowercase booleans, ms timestamps, percent-encoded IAM policy documents (`+` as `%2B`); SQS legacy from retained `xmlName`/`xmlFlattened` with namespace `http://queue.amazonaws.com/doc/2012-11-05/`; errors `ErrorResponse/Error{Type,Code,Message}` + `RequestId`, `Code` from `awsQueryError`, status from `httpResponseCode`; SNS has no `xmlns` on `ErrorResponse`, CloudWatch has one (overlay facts) |
| `restjson` | Lambda | labels decoded before matching (`:` arrives as `%3A`); `X-Amzn-Errortype: <Shape>` and a body with the model's exact keys (`Type`, `Message` or `message`); `httpResponseCode` members from the handler (`Invoke` 200/202/204); ESM create, update, delete 202; `Invoke` accepts any `Content-Type` and an empty body; malformed bodies `InvalidRequestContentException` (overlay); the Smithy malformed corpus runs against the synthetic `RestJson` whose overlay says `SerializationException` |
| `restxml` | S3, S3 Control tags | generated dispatch table; subresource keys tested by presence (`?uploads` and `?uploads=` are the same); `{Key+}` percent-decoded once, `/` preserved, dot segments verbatim; `xmlw` for responses (`xmlns` on success roots, `xsi:type` grantees, flattened lists, SDK escape table incl. NEL and LS); `encoding/xml` only to parse requests on local names; `<Error>` root with context elements, `RequestId`, `HostId`, HEAD status-only; unwrapped `GetBucketLocation`; `encoding-type=url` on every key field; 200-with-error path for `CompleteMultipartUpload`, `CopyObject`, `UploadPartCopy` |
| `rpcv2cbor` | CloudWatch (Go v2, so Terraform) | claim on `Smithy-Protocol: rpc-v2-cbor` + `/service/{GraniteServiceVersion20100801 or dotted form}/operation/{Op}`; reject `X-Amz-Target`; `protocol/cbor` subset of `wire-cbor.md 7` (decoder accepts indefinite lengths, half floats, non-minimal widths; caps depth 128 and declared lengths; encoder definite minimal; timestamps tag 1); empty body for `Unit` output (any member panics the Go client); errors never empty, `__type` absolute shape id; `Smithy-Protocol` on every response; `DeleteAlarms` of a missing alarm succeeds (`wire-cbor.md 3.5`) |

CloudWatch is served by three codecs and SQS by two; handlers are written once.

### 6.3 Unknown and unimplemented operations

| Situation | Answer |
|---|---|
| Operation not in the model | per service from the overlay `unknownOperationError`: JSON `UnknownOperationException` 400; Query `InvalidAction` 400 `The action X is not valid for this endpoint.`; S3 `NotImplemented` 501; Lambda `UnknownOperationException` 404 (UNVERIFIED entries carry that label) |
| In the model, not bound | 501 in the native envelope; code `NotImplemented`; message `understudy does not implement <Service>.<Operation>. Run 'understudy coverage --service <svc>' for what is supported; to ask for it, see https://github.com/sustinbebustin/understudy/issues?q=<Service>.<Operation>` (no trailing period; ADR 0003 pre-tag review amendment) |
| Signed for a service that no rule claimed | that service's `unknownOperationError` or `malformedRequestError` (4.1 rule 8); a wire golden per service and claim rule |

501 is outside the retry set of Go, botocore and JS, so SDKs fail fast with the message visible (`wire-json-query.md` Implications 15).

### 6.4 Proof targets

| Protocol | Server-runnable cases (`smithy-protocol-tests.md 3`) |
|---|---|
| awsJson1_0 | 26 request + 23 response |
| awsJson1_1 | 58 + 45 |
| awsQuery | 35 + 31 (no reference server runner exists; the `both` cases plus four-client conformance are the proof) |
| restJson1 | 137 + 92, plus 655 expanded malformed cases |
| restXml | 101 + 77 (13 S3 cases) |
| rpcv2Cbor | 39 + 29, plus the G01 to G24 Go vectors |

The skip list (`internal/protocoltest/skips.json`) is seeded from smithy-rs's `ExpectFail`, `BrokenTests` and `DisabledTests` plus `host` and `resolvedHost` cases; every entry has a reason and a category (`corpus-bug`, `client-only`, `aws-differs` with an `evidence.Label`). The same file records known failures under the category `gap`: cases understudy itself fails because a part it builds on cannot yet express what they assert, each reason naming the owning package. A known failure is not a skip: the coverage report counts it as failed and lists it apart, and the ratchet accepts it only until the fix lands and the baseline is rewritten. Validation message text in the Smithy cases is asserted as expected-to-differ (`error-matrix.md` finding 2). The corpus is a ratchet, not a wall: `internal/protocoltest/baseline.json` records the passing case ids per protocol; a run fails when any baseline case fails or when the baseline would shrink, and `just protocoltest-baseline` rewrites it when cases are fixed. The 655 expanded restJson1 malformed cases join the baseline as Lambda needs them.

## 7. Identity, auth, accounts and regions

- Account from access key (ADR 0012): a key minted by understudy (`CreateAccessKey`, `AssumeRole`, Lambda execution environment) maps to its principal; a 12-digit numeric key is that account's root; anything else, including real-looking `AKIA`/`ASIA` keys and `AKIAIOSFODNN7EXAMPLE`, is the default account `000000000000`. Real-looking keys are never decoded into foreign account numbers; the server logs one warning per such key. Minted keys embed the account with the documented bit layout so they decode correctly in other tools (`iam-sts-kms.md 2.2`).
- Region: `aws.RegionSource` is `CredentialScope{Region}` from SigV4, `HostRegion{Region}` from a `<svc>.<region>.<base>` host, or `None` for SigV2, presigned-SigV2, unsigned and anonymous requests. Regions are validated against the table of real AWS names (Terraform's `aws_region` data source rejects others, `tf-remaining.md 10`); `regions.extra` adds names. The edge passes the source on the call (`kernel.Call.RegionSource`). With `None`, S3 serves the bucket from its home region and sets `x-amz-bucket-region` (on every error about an existing bucket and on the successes whose output models the header, HeadBucket; a handler cannot add a header to another modeled success yet, see [differences](guide/differences.md)); the cross-region error rules of ADR 0013 apply only when the request asserts a region; non-S3 calls with `None` use the default region `us-east-1`. IAM and STS accept any region in the scope; their cells are keyed by account.
- Auth is always parsed, verified only with `sigv4: verify`. Verification runs in the edge after the claim (the family vocabulary and S3's path-style form are known then) and before decode; a failure is dispatched as a rejected call so it is journaled. The verified credentials travel on the call (`kernel.Call.VerifiedAuth`), which S3 uses to verify aws-chunked chunk and trailer signatures; under `parse` the call carries none. A presigned SigV4 request's `x-amz-*` query parameters reach the call's headers as if sent in the header (`auth.HoistedHeader`, a header sent wins), so S3 reads hoisted aws-chunked framing in both modes. An operation may refuse credentials in its own vocabulary (`edge.Options.OperationAuth`): Lambda's `InvokeFunctionUrl` answers the JSON family's identities with 403, as the function URL front end refuses (the status for a refused signature is inferred, not recorded). Secrets resolve as ADR 0012's pre-tag review amendment says. The verifier is a pure function over a normalized request with the payload hash as a tagged value (`Hex | Unsigned | StreamingUnsignedTrailer | StreamingSigned | StreamingSignedTrailer | NotSent`); the seven DOC vectors are table tests and a rapid sign-then-verify property covers the rest. Skew checks and the `Date` header use wall time; presign expiry uses the emulator clock (`lambda-runtime-contract.md` rows 9 and 10).
- SigV2 presigned S3 URLs are supported because boto3 emits them by default in the legacy regions (`sigv4-checksums-endpoints.md 2.3`).
- STS mints `ASIA` credentials with `Expiration = wall now + DurationSeconds`; `AssumeRole` succeeds only if the role exists (`AccessDenied` otherwise; `iam.assumeRole: lenient` relaxes it). The same path mints execution-role credentials for Lambda environments.
- `GetCallerIdentity` works for every principal shape so the Terraform provider's bootstrap is one call with no `skip_*` flags.

## 8. State and concurrency

Rules, enforced by tests and an always-on check (a bool compare; no `lockcheck` build tag):

1. A handler holds at most one cell lock and never calls `kernel.Invoke` while holding it. Cross-service work is an effect that runs after unlock, or a synchronous `Invoke` made before the lock (S3 and Secrets Manager to KMS). `kernel.Write` marks the `Call` as holding; `Invoke` or a nested `Write` on a holding call returns `Internal` naming both operations.
2. Lock hierarchy, documented here and in the contributor checklist: the clock's step lock, then the gate, then one cell, then leaf locks (clock mode, scheduler heap, waiters, journal ring, ids streams, fault budgets, outbox cell when the kernel inserts a delivery). Leaf locks never call out and never call back. The scheduler and `Advance` release their data mutex before dispatching a timer, but dispatch runs under the step lock that serializes advances and mode changes; a timer callback takes a cell through `kernel.Write` like any handler. So `Clock.Advance`, `AdvanceTo`, `Freeze`, `Resume` and `Scale` are never called while holding the gate or a cell, nor from inside a timer callback.
3. Granularity: one cell per `(account, region)` by default. S3 uses a partition-wide bucket-name index cell (bucket names are global) plus one cell per bucket; conditional writes and multipart completion take the bucket cell, so `ConditionalRequestConflict` races resolve under one lock (`s3.md` Implications 8). IAM and STS use one cell per account (`Partitioned[aws.AccountID, S]`).
4. No I/O under a lock. Blob bytes are written to `blob` before the cell write that publishes them; DynamoDB proxying and container work happen outside cells.
5. Waiting is channel-based and context-aware (`Waiters.Wait(ctx, key)`, `Clock.Until(ctx, d)`, `context`), never a mutex hand-off, so `testing/synctest` bubbles advance (`go-toolchain.md 1`). A registration ends with its context; a test wakes a long poll 1,000 times and asserts no registrations remain.
6. The snapshot cut, reset and load take the gate's write side; nothing else does, and nothing does I/O or `Invoke` under it (13).
7. Cross-scope references: a handler resolves the scope a resource reference names with `kernel.ResolveScope(c, ref)` where `ref` is `SameScope{Mismatch *awserr.Error}` (SNS `Invalid parameter: TopicArn`, Lambda `ResourceNotFoundException` "Functions from 'x' are not reachable in this region", S3 SSE-KMS `KMS.NotFoundException` "Invalid arn <region>"), `CrossAccount` (SQS queue URLs: the account in the path wins), or `Global` (S3 bucket names). Each service tests the forms name, same-scope ARN, other-region ARN and other-account ARN. The DynamoDB proxy applies the same resolution before forwarding.

Long poll, as the reference for every blocking operation:

```go
func (s *Service) receiveMessage(ctx context.Context, c *kernel.Call, in *sqsapi.ReceiveMessageInput) (*sqsapi.ReceiveMessageOutput, *awserr.Error) {
	q, err := s.lookup(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	wait := clock.Deadline{
		Emu:     c.Admitted().Add(waitTime(in, q)),           // the AWS semantics
		WallCap: s.k.Clock().Wall().Add(waitTime(in, q)),    // a frozen clock still ends the HTTP request (sqs.md 13.11)
	}
	for {
		woke := s.k.Waiters().Wait(ctx, waitKey(q))           // subscribe before checking: no lost wakeups; ends with ctx
		msgs, err := kernel.Write(c, s.state, c.Scope(), func(st *State, now clock.Instant, tx *kernel.Tx) ([]Message, *awserr.Error) {
			return st.receive(q, in, now, tx)                 // now is the commit instant; arms visibility timers via tx; returns copies
		})                                                    // a cell retired by reset or load is re-resolved inside kernel.Write
		if err != nil || len(msgs) > 0 {
			return render(msgs), err
		}
		select {
		case <-woke:
		case <-s.k.Clock().Until(ctx, wait):
			return &sqsapi.ReceiveMessageOutput{}, nil
		case <-ctx.Done():
			return nil, awserr.RequestAborted()
		}
	}
}
```

Blob store: object bodies and Lambda code live in `blob` under content-addressed names inside an `os.Root`; an S3 key never becomes a path component (`go-toolchain.md 8.2`). Objects above 8 MiB spill to disk even in ephemeral mode (temp dir, removed at exit); refcounts span versions and snapshots. Snapshot load and `--persist` startup import blobs with zero references, retain each `Section.Blobs` entry, then call `Prune` to delete what no state retained.

## 9. The clock

```go
package clock

// Instant is emulator time. Not time.Time, so an emulator deadline cannot be compared with a
// wall timestamp by accident. Wire() is the one sanctioned conversion, used by codecs.
type Instant struct{ t time.Time }
func (i Instant) Add(d time.Duration) Instant
func (i Instant) Sub(j Instant) time.Duration
func (i Instant) Before(j Instant) bool
func (i Instant) Wire() time.Time

type Mode interface{ isMode() }
type Running struct{ Offset time.Duration }                               // emu = wall + Offset
type Frozen struct{ At Instant }
type Scaled struct{ Factor float64; AnchorWall time.Time; AnchorEmu Instant } // 0 < Factor <= 10000

type Clock struct{ /* mu, step, mode, scheduler, wall source, waiters */ }

// New refuses a mode that is not a Running, Frozen or Scaled value (*ModeError), an invalid
// Scaled factor (*ScaleError), a Start that conflicts with the Mode's own instants
// (*StartConflictError) and a start more than MaxSkew (100 years) from wall time (*RangeError).
func New(opts Options) (*Clock, error)

// ModeChange reports a mode change for the journal's ClockChanged; To keeps its concrete type.
type ModeChange[T Mode] struct{ From Mode; To T }

func (c *Clock) Now() Instant
func (c *Clock) Mode() Mode
func (c *Clock) Freeze() ModeChange[Frozen]
func (c *Clock) Resume() (ModeChange[Running], error)        // back to Running; *RangeError past MaxSkew
func (c *Clock) Scale(f float64) (ModeChange[Scaled], error) // *ScaleError outside 0 < f <= MaxScale
// Restore sets the instant and mode for reset and snapshot load (the instants inside m are
// ignored); the caller runs Scheduler.Clear first and Stateful.Rearm after.
func (c *Clock) Restore(at Instant, m Mode) (ModeChange[Mode], error)
func (c *Clock) Advance(d time.Duration) (Advanced, error) // d >= 0; fires due tasks stepwise
func (c *Clock) AdvanceTo(t Instant) (Advanced, error)     // t >= Now(); *BackwardsError otherwise
func (c *Clock) Until(ctx context.Context, d Deadline) <-chan struct{} // the wait ends with ctx; no leaked registrations
func (c *Clock) Wall() time.Time                           // the only other sanctioned time.Now
func (c *Clock) Stopwatch() func() time.Duration           // monotonic elapsed

type Deadline struct{ Emu Instant; WallCap time.Time }     // zero WallCap means none

type TimerKind string                                      // declared constants per service: sqs.TimerVisibility = "sqs.visibility"
type TimerKey struct{ Kind TimerKind; ID string }          // {sqs.visibility, "<queue-arn>/<msg-id>"}

// Scheduler is a heap ordered by (due, seq) with an index by TimerKey. Arm replaces any timer
// with the same key; Disarm is idempotent. Timers carry no closure: the scheduler calls the one
// dispatch function it was built with, and the kernel routes by Kind to the handler the service
// registered at wiring (Kernel.Timers().Handle(kind, fn)); arming an unregistered kind is an error
// at bind time, never a silently dropped timer.
func NewScheduler(dispatch func(k TimerKey, now Instant)) *Scheduler
func (s *Scheduler) Arm(k TimerKey, at Instant)
func (s *Scheduler) Disarm(k TimerKey)
```

Semantics:

- Emulator time never moves backwards. `serve --clock-start <RFC3339>` starts anywhere; afterwards only forward moves. `reset` and `snapshot load` are the only ways to an earlier time.
- In every mode a timer with `due <= Now()` fires at once. Frozen only stops `Now()` from moving; SNS fan-out, S3 notifications, EventBridge `PutEvents` deliveries, the first Lambda async attempt and window-0 ESM polls all run on a frozen clock.
- Timer callbacks never block. A callback may `kernel.Write` and start tracked work (`Kernel.Go`); anything that calls `kernel.Invoke`, waits on a container or long-polls runs as tracked work that carries the due instant as its `Target.At`. A synctest test schedules one task that blocks forever and asserts a later timer still fires on `Advance`.
- `Advance` is stepwise: due timers fire in `(due, seq)` order and each callback observes `Now() == due`; timers armed by callbacks fire in the same advance if due. Advancing an hour across a `rate(5 minutes)` rule fires twelve events stamped 5, 10, ... 60 minutes later, because the work they start is stamped with the due instant. Waiters are woken once per advance.
- `Advance` does not wait for real work. `understudy clock advance 1h --settle` advances, then waits until the work tracker is idle.
- A driver goroutine sleeps `(nextDue - now) / factor` of wall time in Running and Scaled, and in Frozen only until the next arm, advance or mode change. The scheduler releases its mutex before dispatching.
- `forbidigo` bans `time.Now`, `time.Sleep`, timers and tickers outside `internal/clock`. Under `synctest` wall time is the bubble's fake time, so the real `Clock` runs unmodified in tests.

Two timer classes:

| Class | Examples | Clock |
|---|---|---|
| AWS semantics | visibility, delay, retention, schedules, delivery retries, deletion windows, TTL, log retention | emulator clock, as in the domain table |
| Provisioning settle | Lambda `State`, `LastUpdateStatus`, ESM `Creating`/`Enabled`/`Deleting`, SQS `QueueDeletedRecently` | final before the mutating call returns, in every mode, by default; the fidelity switches `lambda.stateTransitions: clock` and `sqs.queueDeletedRecently: enforce` (both default off) restore clock-driven intermediates for waiter tests (`tf-storage.md` implication 3, `tf-messaging.md 4.2` row 17) |

Domain contract (`lambda-runtime-contract.md 3`, adopted in full):

| Emulator clock | Wall clock (monotonic where it is a duration) |
|---|---|
| SQS visibility, delay, retention, dedup window, purge cooldown, move-task rate | `Lambda-Runtime-Deadline-Ms` = dispatch wall ms + Timeout |
| SNS delivery retries, FIFO dedup, confirmation token expiry | function timeout enforcement, init limit, REPORT durations, idle reaping |
| ESM batching windows, async invoke retries (60 s, 120 s), max event age | SigV4 skew check and the `Date` response header |
| EventBridge schedules and event `time` (unless the caller sent `Time`) | STS `Credentials.Expiration` |
| DynamoDB TTL sweep, secret deletion windows, KMS deletion, log retention | `slog` timestamps |
| server-assigned timestamps (`Last-Modified`, `CreatedTimestamp`, `LastModified`) | injected fault latency |
| S3 presigned URL expiry (`emu now >= X-Amz-Date + X-Amz-Expires`) | `PutLogEvents` 2 h / 14 day windows accept if valid against wall OR emulator |

Two consequences the docs state: user code that calls `time.time()` cannot be steered (functions read emulator time from `GET /_understudy/v1/clock`, served on the container listener too), and a frozen clock never fires, shortens or lengthens a function timeout.

ESM in-flight visibility: in Running mode messages held by an in-flight batch follow AWS (visibility keeps counting). In Frozen and Scaled modes the poller pins its held messages invisible until the invocation returns through the internal SQS operation `HoldVisibility` (an overlay `dev.understudy#internalOperation`: generated types and `Op`, validated, journaled, never claimed by the edge), because a scaled clock would otherwise produce duplicates AWS would not. The journal marks pinned messages.

## 10. The journal

```go
package journal // owns its record types; imports ref, aws, clock, ids only

type Event struct {
	ID     ref.EventID // monotonic per kernel; preserved across snapshot load
	Cause  ref.Cause
	Links  []ref.EventID // related, not parent: the other messages in an ESM batch
	At     clock.Instant
	Wall   time.Time
	Scope  aws.Scope
	Detail Detail
}

// Detail is the kind-specific payload of a journal event.
//
//sumtype:decl
type Detail interface{ isDetail() }
type APICall struct {
	Op        ref.OperationID
	RequestID ids.RequestID
	Origin    ref.Origin
	Principal string          // rendered principal ARN (journal does not import identity)
	Mode      string          // caller | execution-role | service:<name>, for internal calls
	Resources []aws.ARN       // from Call.Touch
	Outcome   Outcome         // OK | Failed{Identity, Status, Message} | Injected{Rule, Identity}
	Latency   time.Duration   // wall
	Input     Redacted        // generated Redact(); sensitive members never stored; bodies only with journal.bodies
}
type Delivery struct{ From, To aws.ARN; Mechanism delivery.Mechanism; Attempt int; Outcome DeliveryOutcome } // Delivered | Filtered{rule} | Retrying{next} | DeadLettered{dlq} | Dropped{reason}
type Invocation struct{ Function aws.ARN; Version, Env string; Cold bool; Init, Duration time.Duration; Outcome InvocationOutcome } // OK | FunctionError{Type} | Timeout | ExitError{Code} | OOM | Throttled
type TimerFired struct{ Key clock.TimerKey }
type StateChange struct{ Resource aws.ARN; From, To ResourceState } // ResourceState is a closed string type per service, declared next to the state machine
type FaultInjected struct{ Rule ref.RuleID; Hit, Of int }
type ClockChanged struct{ From, To clock.Mode }
type Redrive struct{ From, To aws.ARN; Messages int; Reason RedriveReason } // MaxReceiveCount{Count} | MoveTask{TaskHandle}; post-tag (ADR 0007 pre-tag review amendment)
type AdminAction struct{ Op AdminOp; Scope aws.Scope }                         // Reset | SnapshotSave | SnapshotLoad; post-tag (ADR 0007 pre-tag review amendment)
type Advisory struct{ Kind AdvisoryKind; Resource aws.ARN; Text string } // resource policy would deny a delivery; unminted credentials from a function container; shadowed code update under hot reload; a cross-region request s3.crossRegion lenient served
type Unclaimed struct{ Method, Host, Path string; Reasons []string }
type Gap struct{ Missed uint64 } // slow stream consumer
```

How causality survives hops:

- Internal calls inherit `Cause{Parent: caller.Event, Trace: caller.Cause.Trace}`; causes are stamped by the pipeline and by `kernel.Write` on effects, never by handler code. A handler that has more to say than its call's event (a dropped EventBridge delivery, S3's cross-region advisory) describes the event and hands it to the kernel: `Call.Record(ctx, scope, detail)` journals it at once, `Tx.Record(scope, detail)` when its write commits, under the cell's lock and before any timer the write arms can fire; either way the kernel stamps the cause.
- State carries causes across time: an SQS message stores the event id of the call that enqueued it; the ESM invocation's parent is the earliest message's enqueue event and the rest are `Links`. The same holds for outbox entries, EventBridge target delivery, S3 notifications and async invoke queues.
- Calls made by function code link to the `Invoke` call that the invocation serves (the parent of the `Invocation` event too), init phase included: each execution environment has its own minted credentials and runs one invocation at a time, so the access key on a request identifies the current invocation (`kernel.Linker`). A call made between invocations roots its own trace; its principal, the execution-role session named after the function, ties it to the function. On Linux bridge the request's `RemoteAddr` is a second linking signal, and a call from a function container that carries unminted credentials produces an `Advisory`. No SDK cooperation is needed; `X-Amzn-Trace-Id` is stored when present but correctness does not depend on it.

Storage and query: a bounded ring with per-event byte estimates made in `Append` (events hold slices and redacted inputs, so eviction is by estimated bytes and by count: default 100,000 events or 128 MiB, whichever first) with indexes by trace, resource ARN, operation and request id; with persistence on, events append to `<data-dir>/journal.ndjson` with size-based rotation. A background writer appends to the file, never under the journal's lock; if the disk falls `MaxBytes` estimated bytes behind, the file stops there (a prefix of recording order) and `FileErr` reports the queue-full error (ADR 0007). Query filters: trace, request id (the key users hold from an SDK error or a log line), service, operation, resource ARN glob, kind, outcome, principal, emulator-time range or event-id range, limit with cursor. Stream: NDJSON over a chunked response with the same filters, resumable with `?after=<event id>`; slow consumers get a `Gap` event, never backpressure on the emulator. Redaction is generated from `sensitive` (47 shapes) plus overlay additions (SigV4 `Authorization`, `X-Amz-Security-Token`), so the journal cannot leak what `slog` cannot. The journal is history, not state: snapshots omit it unless `--include-journal`.

The journal is also a test oracle: conformance and e2e tests assert causal chains (`PutObject -> S3 notification -> SQS SendMessage -> ESM ReceiveMessage -> Invoke -> DeleteMessageBatch`) instead of sleeping, and Terraform tests assert call sequences per resource.

## 11. Fault injection

```go
package fault

type Rule struct {
	ID      RuleID
	Match   Match
	Phase   Phase   // Request | Delivery
	Action  Action
	Trigger Trigger
}

type Match struct {
	Services   []ref.ServiceID        // empty = any
	Operations []ref.OperationID      // Request phase
	Mechanisms []delivery.Mechanism   // Delivery phase: SNSToSQS, SNSToLambda, S3ToSQS, ...
	Resource   ARNGlob                // over the ARNs the call Touches: "arn:aws:sqs:*:*:orders-*"
	Accounts   []aws.AccountID
	Regions    []aws.Region
	Origin     OriginMatch            // Any | ExternalOnly | InternalOnly
}

// Catalogue is what rule validation reads; the kernel implements it over the registry so fault never imports registry.
type Catalogue interface{ Errors(op ref.OperationID) ([]awserr.Identity, bool); Throttle(svc ref.ServiceID) awserr.Identity }

type Action interface{ isAction() }
type Error struct{ Shape smithy.ShapeID; Message string } // validated against the operation's generated error list plus the generic catalogue
type Throttle struct{}                                   // the service's own throttling identity (overlay throttleError)
type Latency struct{ Wall time.Duration }                // before the handler; wall, so a frozen clock cannot hang it
type AfterCommit struct{ Shape smithy.ShapeID }          // the side effect happens, the client sees the error
type StatusOKError struct{ Shape smithy.ShapeID }        // S3 200-with-error (CompleteMultipartUpload, CopyObject, UploadPartCopy)
type DropDelivery struct{}                               // Delivery phase: the target never receives it

type Trigger interface{ isTrigger() }
type Always struct{}
type NextN struct{ N int }           // removed after N hits; every SDK retry counts as a hit
type Probability struct{ P float64 } // ChaCha8 stream keyed by hash(global seed, rule id)
```

- Rules are validated at creation: an error shape outside the operation's modeled plus overlay errors is rejected with the list of codes the service really sends; `Throttle{}` resolves to `RequestThrottled` 403 (SQS), `ProvisionedThroughputExceededException` (DynamoDB), `TooManyRequestsException` 429 (Lambda), `SlowDown` 503 (S3) and so on.
- Hook points: pipeline steps 8 and 10 for every call, internal ones included (`{Service: sqs, Operation: SendMessage, Origin: InternalOnly}` makes SNS-to-SQS delivery fail and drives the SNS retry and DLQ path); the delivery engine for `Phase: Delivery`; the DynamoDB proxy before forwarding, so Local never sees the request. The DynamoDB export, import and reset run outside the pipeline, so they never match a rule (ADR 0014).
- `Store.Evaluate(Hook, MatchInput) Decision` names the hook, not the phase: `BeforeHandler` (step 8 and the DynamoDB proxy: `Error`, `Throttle`, `Latency`), `AfterHandler` (step 10, only after the handler succeeded: `AfterCommit`, `StatusOKError`) and `DeliveryAttempt`. `Decision` is `None | Fail | Delay{Wall, Then} | FailAfterCommit | FailStatusOK | Drop`, each hit carrying `Injection{Rule, Hit, Of}`. `Delay.Then` is evaluated eagerly, so the caller journals its hits even when the client leaves during the hold. Shapes in the `dev.understudy` namespace are never injectable (ADR 0008).
- Typed validators (3.2 `WithValidator`) run before faults, so the SQS limit texts and CloudWatch `InvalidParameterCombination` precede an injected error exactly as generated constraints do.
- Determinism: same seed, same rule set, same call order gives the same hits. Concurrent clients make call order nondeterministic; `FaultInjected{Hit, Of}` makes every hit visible either way.
- SDK retries are surfaced: the CLI warns when a rule targets a retryable code (Terraform's SDK retries up to 25 attempts, `tf-compute.md` 14), and the docs give the fail-fast codes.
- Rules come from the admin API, the CLI, or the config file (preloaded at startup).

## 12. IDs and determinism

`internal/ids` owns every generated identifier: request ids, SQS message ids and receipt handles, SNS message ids, Lambda request ids, ESM UUIDs, upload ids, version ids, SSE-KMS ETags, KMS key ids, IAM `AROA`/`ANPA`/`AIDA` ids, minted access keys. Default source is `crypto/rand`. With `--seed N` each generator kind draws from its own ChaCha8 stream derived from `(seed, kind)`; stream positions are recorded in snapshots. The stdlib `uuid` package cannot be seeded, so UUIDs are formatted from the stream. KMS key material always comes from `crypto/rand`. Map iteration for output always sorts. Drawing from an undeclared kind panics on either source (ADR 0018, pre-tag review amendment).

The determinism contract is narrower than "byte-identical", because one client still fans out into concurrent internal work and Lambda runs on wall time. What holds: with the same seed, rule set and call order, fault hits are identical; snapshot state sections are byte-identical except a named set of wall-domain fields (STS `Expiration`, persisted as `expiry` by identity's export; Lambda REPORT durations; DynamoDB Local timestamps) and, between runs under different accounts, account ids (ARN and URL accounts, `Account`/`AccountId` members, and minted access key ids, whose whole value is masked when `ids.DecodeAccount` finds an account in it); journals are equal as canonical trace trees (children sorted by kind, operation and resource; ids replaced by tree positions; `Wall` and `Latency` ignored). `snapshot diff --normalize` and the journal's `Canonical()` view apply the closed normalizer set the parity replayer will share; golden fixtures never contain raw event ids. A `--deterministic` single-worker mode is not built in phase 1.

## 13. Snapshots and persistence

One deterministic archive: uncompressed POSIX tar (`archive/tar`), entries sorted, mtime 0, uid and gid 0, mode 0644; optional gzip with an empty name and zero mtime. Same state and same understudy version give the same bytes, so snapshots are committable fixtures and diffable.

```
manifest.json                                   format, writer, model pin, clock mode and time, seed and stream positions,
                                                journal.next, section list with schema versions and sha256
faults.json                                     rules with seeds and remaining budgets
services/sqs/000000000000/us-east-1.json        one canonical JSON document per cell (jsonx, Deterministic)
services/s3/index.json                          bucket-name index cell
services/s3/buckets/<bucket>.json               per-bucket metadata, versions, multipart state
services/dynamodb/<account>/<region>.json            shim state: tags, TTL, PITR, table class, SSE, table ids
services/dynamodb/<account>/<region>/<table>.ndjson   items exported by Scan through the proxy, sorted by key
blobs/sha256/<hex>                              object bodies, multipart parts, Lambda code zips
journal.ndjson                                  optional (--include-journal)
```

```go
package kernel

// Stateful is implemented by every service with state (and by outbox). Export does no I/O: it
// serializes to in-memory sections and pins blob references. A test asserts Export -> Import ->
// Export is byte-identical for every registered service, and that Rearm after Import reproduces
// the scheduler. Partitioned implements Export, Import and Reset; services add Rearm and Resources.
type Stateful interface {
	Name() ref.ServiceID
	SchemaVersion() int
	Export() ([]store.Section, error)
	Import(ss []store.Section) error           // refuses a section whose version differs (13, versions)
	Reset(scope ResetScope)
	Rearm(now clock.Instant)                   // timers are derived from state, never stored
	Resources(scope aws.Scope) []Resource
}

// Late is implemented by services whose data is not cell state (DynamoDB items). It runs after the
// cut, outside the gate and outside the pipeline: no fault rules, no journal events of its own.
type Late interface { ExportLate(ctx context.Context) ([]store.Section, error); ImportLate(ctx context.Context, ss []store.Section) error }
```

- Save is two-phase: under the gate's write side every `Stateful` exports to memory and pins its blobs (the consistent cut, microseconds); the gate is released; then `Late` exports run and the tar is written. Load is the mirror: `Late` imports first, then reset-then-import under the gate, then `Rearm`. Nothing does I/O or runs the pipeline under the gate.
- The state structs are the schema: every persisted field has a `json` tag; each service declares `const stateVersion = N`. Until the first tagged release the archive `format` is `0` and a section whose version differs from the running binary's is refused with a message naming both versions and the binary that wrote it; from the first release on, migrations with a golden fixture per past version become mandatory. A newer `format` is always refused, as MiniStack's downgrade guard does.
- Persisted member names follow the normalizer's naming convention: a `ref.EventID` member's JSON name ends in `EventId`, and a member whose value is an account ends in `Account` or `AccountId`. `snapshot diff --normalize` masks values by these names, so a member that breaks the rule shows up as a difference between every two runs; the only listed exception is SQS's `senderId`, an account only for root senders. A member ending in `AccessKeyId` is masked whole when its value encodes an account (`ids.DecodeAccount`), and STS expiry is masked under both `Expiration` and identity's persisted `expiry`. State that sorts by a masked value sorts by what the mask keeps (identity's export orders keys with `ids.CompareAccessKeys`, which ignores the account). Member-name rules do not apply inside user-keyed maps of strings persisted as `Tags`, `Variables` or `Metadata` (any case), whose keys are the user's; a service that persists another user-keyed map of strings uses one of those names, or the snapshot owner extends `scopeOf` in `internal/snapshot/normalize.go` (`internal/snapshot` package doc).
- Sealed sums in state (message state, ESM state, function state, pool state) serialize through one tagged-union convention in `jsonx`: `{"kind": "<variant>", ...fields}` with a per-sum table; every persisted sum has a rapid round-trip property.
- Timers are never saved. Visibility deadlines, `nextFireAt`, deletion dates, ESM state and outbox attempts are state; `Rearm` rebuilds every timer and poller after load. A restart test asserts every re-armable thing fires (MiniStack#889 is the bug this prevents).
- Load drains Lambda environments, stops pollers and clears the scheduler before the cut.
- Accepted deliveries are state in the outbox, so a save never names deliveries as not captured.
- DynamoDB: under `--persist` Local runs with `-dbPath <dir>/dynamodb` and owns item data; the periodic archive carries shim state only. An explicit `snapshot save` exports items through the proxy (`DescribeTable` plus paginated `Scan`) as `Late` sections; `snapshot load` and `reset` issue `DeleteTable` per scope against the running container, then `CreateTable` plus `BatchWriteItem` from the sections. Local's SQLite files are never copied. These calls never match fault rules and are not journaled; the admin action is (ADR 0007, `AdminAction`, post-tag).
- Persistence across restarts is opt-in (`--persist <dir>`): the archive is written atomically to `<dir>/state.snapshot` on graceful shutdown and every 30 s of wall time while any cell is dirty, loaded at startup; blobs live in `<dir>/blobs` and the archive references them, so the blobs the archive on disk names stay pinned until a newer archive replaces it. No write-ahead log in phase 1: a crash loses at most 30 s. The data dir is `flock`ed so two `serve` processes cannot share it. Archives saved through the admin path form live in `<dir>/snapshots`, which cannot reach the lock, the state file, the blobs or DynamoDB Local's files.
- Archive size: one setting, `snapshotMaxBytes` (`--snapshot-max-bytes`, `UNDERSTUDY_SNAPSHOT_MAX_BYTES`), caps every archive, counted as the uncompressed tar: `snapshot save` and `PUT /snapshots` uploads, `--load-snapshot`, and the `--persist` load and checkpoint. The default is 1 GiB, accepted from 1 MiB to 4 GiB. 4 GiB is also the bound `snapshot inspect` and `diff` read with, so every archive a server writes can be inspected. Writes enforce the cap through `snapshot.WriteOptions.MaxBytes`, so no path writes an archive its own load would refuse. `snapshot.Read` holds an archive in memory, so the setting also bounds a load's memory.
- `understudy snapshot diff a b [--normalize]` compares archives section by section and prints resource-level differences; `--normalize` applies the closed normalizer set of section 12.

## 14. Lambda runtime

### 14.1 Execution: understudy hosts the Runtime API (ADR 0010)

Each execution environment is one container from AWS's official base image (or the user's image) with `AWS_LAMBDA_RUNTIME_API=<gateway-ip>:<env-port>`; `/lambda-entrypoint.sh` then execs the runtime directly instead of starting the RIE (`containers.md` findings 1 to 3). The RIE never sets `X-Amz-Function-Error`, crashes on concurrent invokes, hard-codes the invoked ARN and fakes memory; images built from non-AWS bases have no RIE at all. Functions still run the official runtime interface clients in the official base images; only the server of `2018-06-01` differs.

`service/lambda/runtimeapi` (about 600 lines of `net/http`): `GET /2018-06-01/runtime/invocation/next` blocks and always sends `Content-Type: application/json` (the Python RIC segfaults without it), `Lambda-Runtime-Aws-Request-Id`, `Lambda-Runtime-Deadline-Ms` (wall epoch ms), `Lambda-Runtime-Invoked-Function-Arn` (real account, region, qualifier), `Lambda-Runtime-Trace-Id`, client context and `Lambda-Runtime-Invocation-Id`; `/response` and `/error` answer 202, read chunked bodies and the `Lambda-Runtime-Function-Error-*` trailers from `r.Trailer`; any `/error` becomes `X-Amz-Function-Error: Unhandled`; `/init/error`, exit before the first `/next`, `die` and `oom` events map to AWS-shaped payloads (`Runtime.ExitError` wording, `Sandbox.Timedout` text); a late `/response` after a timeout gets `400 InvalidInvocationId`.

### 14.2 Pool

```go
package pool

type EnvState interface{ isEnvState() }
type Starting struct{ Elapsed func() time.Duration } // from Clock.Stopwatch: monotonic wall time, never emulator time
type Idle struct{ Elapsed func() time.Duration }
type Busy struct{ Invocation InvocationID; Deadline time.Time } // wall deadline as sent to the RIC
type Draining struct{ Reason DrainReason }  // closed: Idle | Generation | Eviction | Reset | Shutdown
type Faulted struct{ Cause FaultCause }     // InitError | ExitError{code} | OOM | Timeout | ArchMismatch{Declared, Host}
type Gone struct{}

// Key is a hash of everything in the container spec. Any config or code update bumps the
// function generation; idle environments of old generations drain at once.
type Key struct{ Function aws.ARN; Version string; Generation uint64; SpecHash [32]byte }
```

`Starting -> Idle <-> Busy -> Draining -> Gone`, `Starting|Busy -> Faulted -> Gone`. Exclusive reservation; one in-flight invocation per environment; concurrency by more environments. Start semaphore (default 16), per-function idle cap `max(4, CPUs)`, global cap with LRU eviction, idle timeout on wall time (default 10 minutes). Concurrency limits (account 1000, reserved concurrency) are checked before container work and return the 429 shape. Removal is `DELETE ?force=1&v=1`; the Runtime API listener is removed from the lookup table before the container is deleted. Crash detection comes from one Docker `events` subscription filtered by instance label, with `wait` as a fallback.

### 14.3 Code and images

Zip functions extract once per `CodeSha256` into a content-addressed directory under `os.Root`: names must be `filepath.IsLocal`, symlink entries rejected, total and per-entry sizes capped (262,144,000 unzipped), modes normalized (`bootstrap` 0755), `//go:debug zipinsecurepath=0` in `main` as a second layer. Mounted read-only at `/var/task` via `HostConfig.Mounts` (never `Binds`, which silently creates host directories as root). When understudy runs in a container or the path is not daemon-visible, it falls back to tar copy without `ReadonlyRootfs`. Images are pulled at `CreateFunction` (state `Pending` until present), never on first invoke, single-flight, with `platform`, digest recorded; local tags are resolved first and ECR URIs that are not present locally fail with `ECR is not emulated in phase 1; tag your local image as <ImageUri>`. A failed pull (offline, registry error) moves the function to `State: Failed` with `StateReasonCode: ImageAccessDenied` or `InvalidImage` and a message naming the image and the Docker error, so Terraform fails fast instead of waiting for its timeout. Runtime base images are pinned per understudy release in `internal/service/lambda/launcher/images.go` (tag plus digest); `understudy images pull [--runtime R]` pre-warms them for CI. `Architectures` is honoured: the container is created for the declared platform; an `exec format error` or exit 126 becomes a function error that names the declared and host architectures, and `doctor` reports binfmt availability.

Container spec: `Env` from the reserved set (`lambda-api.md 12.7`) plus minted execution-role credentials, `AWS_ENDPOINT_URL` as an IPv4 literal, `AWS_EC2_METADATA_DISABLED=true`, `TZ=:UTC`; `Memory = MemorySwap`, `PidsLimit`, `nofile` ulimit, `LogConfig` json-file with rotation, labels (section 20), `Tty=false`, no `AutoRemove`, `CapDrop: ["ALL"]`, `no-new-privileges`, read-only code mount, writable `/tmp` as an unsized anonymous volume (removed with the container). A tmpfs would count its pages against the container's memory limit, and the local volume driver sizes only tmpfs volumes, so `EphemeralStorage` is validated against Lambda's range but not enforced (`launcher/doc.go`).

### 14.4 Hot reload

An execution-layer overlay invisible to the control plane: the API keeps reporting the deployed `CodeSha256` and `LastModified`, so a second `terraform plan` stays empty. Enabled by `understudy lambda hot-reload <function> <dir>`, or by the config file (`lambda.hotReload`). The LocalStack-compatible magic bucket (`s3_bucket = "hot-reload"`) is not implemented: CreateFunction and UpdateFunctionCode fetch it as any other bucket and fail. The watcher polls mtimes and sizes every 300 ms with a 300 ms quiet period; a change bumps the overlay generation and the next invoke cold-starts. With understudy in a container, the directory is the path as understudy's container sees it; host paths are not translated through the container's mounts. `doctor` and the banner check that the directory is inside a shared path on Docker Desktop and Colima. An active overlay wins over `UpdateFunctionCode` until it is removed; the update is stored and reported by the API as usual and the journal records an `Advisory` that the overlay shadows it.

### 14.5 Networking (ADR 0011)

```go
package topology

// Topology is the detected container networking environment.
//
//sumtype:decl
type Topology interface{ isTopology() }
type LinuxBridge struct{ Gateway netip.Addr }                  // listener on the gateway IP; ExtraHosts localhost:host-gateway
type DockerDesktop struct{ Host DesktopHost }                   // ProbedHost{Addr, Cleanup} | FallbackHost{Cause}: where host.docker.internal resolves inside Desktop's VM
type RootlessDocker struct{ HostAddr netip.Addr }               // host loopback blocked by default; unsupported in phase 1 with a specific error
type Podman struct{ Rootless bool; Gateway netip.Addr }         // rootless is unsupported in phase 1 (ADR 0011 amendment)
type InContainer struct{ Network string; Self netip.Addr }      // function containers and DynamoDB Local join understudy's network
type Remote struct{ Daemon, Self netip.Addr }                    // tcp:// daemon on another host; listeners on Self, code copied in (ADR 0011 dependency policy amendment)
```

Detected once at first Lambda use. The container listener (the topology's address, main port, created on demand) serves the AWS API but not the admin API; per-environment Runtime API listeners bind the same address on ephemeral ports. Neither binds `0.0.0.0`. On Linux and Podman, `ExtraHosts: ["localhost:host-gateway"]` makes `localhost:<port>` URLs minted on the host (Terraform outputs, queue URLs in message bodies) reachable from functions (`lambda-runtime-contract.md 6.2` option D). Docker Desktop: no container listener is created; Desktop forwards a container's connection to `host.docker.internal:<port>` to the host's loopback, so the main listener and the per-environment Runtime API listeners bind `127.0.0.1`. The address handed to functions must still be an IPv4 literal (path-style S3 in every SDK), so understudy resolves `host.docker.internal` once per daemon by running the function's own image with `Entrypoint: ["getent", "ahostsv4", "host.docker.internal"]` (the base images' entrypoint rejects more than one argument, and `getent hosts` prints only the IPv6 address of a dual-stack name), caches a successful answer, and otherwise falls back to the documented `192.168.65.254` with the reason kept for `doctor`. `InContainer`: the main listener binds `0.0.0.0` inside the container (the published port is the exposure control, see 18), function containers and DynamoDB Local attach to understudy's own network and are addressed by container IP, and this topology is CI-tested alongside Linux bridge by running the smoke test inside the image. Rootful Podman is best effort with `doctor` checks; rootless Docker and rootless Podman are unsupported in phase 1 with a specific error. A missing first `GET /next` within the 10 s init limit is the diagnostic for an unreachable listener: the error names the address handed to the container, the detected topology and the three likely causes. Only the Linux topologies are tested; Docker Desktop is untested until the Docker suite runs on macOS (Docker Desktop, Apple Silicon), [ROADMAP item 4](ROADMAP.md).

### 14.6 Logs

One `logs?follow=1&timestamps=1` stream per environment, opened right after start, demultiplexed and written to `/aws/lambda/<function>` stream `YYYY/MM/DD/[<version>]<env-id>` through the overlay-declared internal operation `logs.PutLogEventsInternal` (`Internal{Via: LambdaLogs}`, `AsExecutionRole`, bypassing the client time windows), stamped with emulator time. `INIT_START`, `START`, `END`, `REPORT` are synthesized with wall-measured durations and a settle barrier after `/response` so the last lines land in the right invocation and in `LogType: Tail` (`containers.md` finding 9). The log group is created on first invocation, never on `CreateFunction`; `CreateLogGroup`, `CreateLogStream` and `PutLogEvents` are recorded against the execution role so the generated policy contains the basic-execution actions.

### 14.7 Invoke, async, state machine, function URLs

- RequestResponse 200 with `X-Amz-Executed-Version`; Event 202 into a per-function async queue on the emulator clock, kept in the Lambda execution layer's own per-scope cells rather than the Lambda cell: it is a `Stateful` of its own with `ServiceID` `lambda-async`, listed beside `lambda` in snapshots, resources and reset, and a reset of `lambda` empties it too through the execution layer's `Drain` (retries after 60 s and 120 s on function errors; throttles and 5xx requeue with 1 s to 5 min backoff for up to 6 h, `lambda-events.md 8`; destinations are stretch and plug into `delivery`); DryRun 204. Limits: 6 MB sync, 1 MB async (`lambda-api.md` finding 4; not 256 KB). `X-Amzn-RequestId` always.
- Function state and `LastUpdateStatus` are final before the mutating call returns (`Active`, `Successful`) in every clock mode by default; `lambda.stateTransitions: clock` restores `Pending -> Active` and `InProgress -> Successful` on the emulator clock with a 10 ms delay and the exact `ResourceConflictException` for updates inside the window, for SDK-waiter tests.
- Docker unavailable: the control plane keeps working (Terraform applies cleanly); Invoke answers 501 NotImplemented, which no SDK retries, naming the Docker endpoint tried and `understudy doctor`.
- Containers keep running between invocations (AWS freezes the sandbox); background work between invocations belongs to no invocation and roots its own trace under the function's execution-role session. Pausing environments through the Engine API is deferred.
- Function URLs: `http://<url-id>.lambda-url.<region>.localhost:<port>/` (host-header routed; the provider derives `url_id` from the first label) with the path fallback; served by the raw operation `lambda.InvokeFunctionUrl` (IAM `lambda:InvokeFunctionUrl`) so it is journaled, faultable and covered; payload v2.0 builder and response inference per `lambda-events.md 7`; `NONE` auth evaluates the resource-policy statements because the 403 `{"Message":null}` with `x-amzn-ErrorType` is observable; `BUFFERED` only.

### 14.8 SQS event source mappings

One poller per enabled mapping, tracked work (`Kernel.Go(InvocationRunning)`) that never parks inside the pipeline: it waits on the queue's `WaitKey` and the batching-window timer, then calls `ReceiveMessage` through `kernel.Invoke` with `WaitTimeSeconds: 0` as `AsExecutionRole` (so the access recorder attributes `sqs:ReceiveMessage`, `DeleteMessage` and `GetQueueAttributes` to the role). Per cycle: receive up to `min(BatchSize, 10)` per call and loop until `BatchSize`, the batching window (emulator time) or 6 MB; window 0 invokes as soon as a message is visible (the 20 s long-poll floor is not modeled). Filters run before invoke with `ruler` under the Lambda allow-list; non-matching messages are deleted without invoking. Partial batch response per `lambda-events.md` Implications 12 (empty, `null`, `{}` or empty list is full success; any malformed or unknown `itemIdentifier` is full failure); deletes go through `DeleteMessageBatch` so the journal links receive, invoke and delete; redrive comes from SQS `maxReceiveCount`. FIFO: one in-flight batch per group; the batching window is accepted and ignored. Control plane returns 202 with `State: Enabled` final before the call returns by default (`lambda.stateTransitions: clock` restores `Creating`, `Enabling`, `Deleting`); responses carry `FunctionResponseTypes: []` and `MaximumBatchingWindowInSeconds: 0` and omit unset `ScalingConfig` and `FilterCriteria`. Mappings survive function deletion but stop invoking, with a journal event saying why. The `EventSourceKind` type keeps a `DynamoDBStream` variant open for the stretch goal.

## 15. DynamoDB Local integration (ADR 0014)

DynamoDB is the one service that proxies rather than implements. It still uses the generated `dynamodbapi` package: every request is decoded and validated so the journal, faults, access recorder and Coral-grammar validation run, then the original bytes are forwarded through a `RawHandler`.

- Container: one `amazon/dynamodb-local` pinned by digest (`sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab`, 3.3.1; ECR Public mirror as fallback, same digest), pulled at first use, never bundled, re-tagged or patched. Args `-jar DynamoDBLocal.jar -port 8000 -disableTelemetry` plus `-inMemory` (default) or `-dbPath /data` (persistence); env `DDB_LOCAL_TELEMETRY=0` (only the literal `0` works). Published on `127.0.0.1:0` where the host cannot route to container addresses (Docker Desktop, rootless engines), addressed at its default-bridge IP on a Linux bridge or rootful Podman, or attached to understudy's network and addressed by container IP when the topology is `InContainer` (ADR 0014 dependency-policy amendment); labeled, memory-bounded. Started in the background at `serve` when the image is present, otherwise on first call with a bounded wait.
- Licence (ADR 0014 amendment of 2026-10-14): no acceptance gate. DynamoDB runs by default; the first start of DynamoDB Local logs one line saying DynamoDB is served by Amazon DynamoDB Local under Amazon's licence terms, with the licence URL. The config state is one sum type, `Enabled | Disabled`; `dynamodb.enabled: false` turns the service off, and its calls then answer 501 naming that setting, as they do while Docker is disabled. The CLI never prompts. The image is never bundled, mirrored, re-tagged, saved, pushed or patched, and understudy is never offered as a hosted multi-user service with Local behind it.
- Isolation: the forwarded `Credential=` is rewritten to `<account>/<date>/<region>/dynamodb/aws4_request` so Local partitions by `(account, region)` natively; only regions from understudy's validated table are forwarded (they become file names under `-dbPath`).
- Before forwarding, understudy answers what Local gets wrong (`error-matrix.md` finding 6): malformed JSON (`com.amazon.coral.service#SerializationException` 400), missing or bad target (`UnknownOperationException` 400), wrong Content-Type (404 XML), missing auth, request too large, generated validation. Unsupported operations are `NotImplemented` before Local sees them. Empty bodies are forwarded as `{}`; `Accept-Encoding` is forced to `identity`.
- Shims (typed state per `(account, region, table)` in `service/dynamodb`): tags (seeded from `CreateTable.Tags`), TTL spec (never forwarded, so Local's wall-clock sweeper stays off), PITR describe and update (`TableNotFoundException` for missing tables), `WarmThroughput {12000, 4000, ACTIVE}`, `TableClassSummary`, `SSEDescription`, `TableId` injected on table descriptions (without `WarmThroughput` Terraform hangs about 2.5 minutes), `DeleteTable` reporting `DELETING`, `UpdateTable` with only shim-owned fields answered locally.
- Responses: operations whose outputs carry ARNs or `awsRegion` are rewritten through a `jsontext` value tree (`arn:aws:dynamodb:ddblocal:000000000000:` to the caller's scope), `message` casing normalized, Local's wall-clock timestamps (`CreationDateTime`, `LastUpdateToPayPerRequestDateTime`, `LatestStreamLabel`, stream `ApproximateCreationDateTime`) replaced from instants the shim stores on the emulator clock, `x-amz-crc32` recomputed over the final bytes; everything else passes through byte-identical with Local's checksum. Local's message texts that clients match are translated through a small table before the checksum is recomputed, starting with `ResourceNotFoundException` ("Cannot do operations on a non-existent table" becomes "Requested resource not found: Table: <name> not found"), which Terraform keys on for `DeleteTable` (`dynamodb-local.md 4.2, 7.1`).
- TTL sweep is a timer kind on the emulator clock whose callback starts tracked work: `Scan` with `#ttl <= :now` following `LastEvaluatedKey`, then `DeleteItem` conditioned on the attribute still being expired, so a concurrent refresh wins. Admin hook `POST /_understudy/v1/dynamodb/ttl-sweep` runs it now. With streams enabled, TTL deletes carry the service `userIdentity`.
- Table names are forwarded verbatim: Local is case-insensitive, so the shim rejects a `CreateTable` whose name collides case-insensitively with an existing table in the same scope with `ResourceInUseException`, listed in coverage as `aws-differs: dynamodb-local`. Passthrough stays byte-exact for every operation, PartiQL included.
- Stream labels are minted by Local; after `snapshot load` or `reset` the re-created table carries a new `LatestStreamLabel`, listed as `aws-differs: dynamodb-local`.
- Faults inject `ProvisionedThroughputExceededException`, `ThrottlingException`, `TransactionConflictException` (which Local never raises).
- Known gaps (no throughput enforcement, sequential `Scan` segments, single-shard streams) are coverage entries marked `aws-differs: dynamodb-local`. A permanent integration test runs the pinned image with understudy's exact arguments on an `--internal` network and asserts zero DNS lookups and connections.

## 16. Cross-service delivery

```go
package delivery // leaf types; the payload lives in the target variant

// Target is where a delivery goes, with its payload.
//
//sumtype:decl
type Target interface{ isTarget() }
type SQSQueue struct{ ARN aws.ARN; Body string; Attributes []MessageAttribute; GroupID, DedupID string }
type LambdaFunction struct{ ARN aws.ARN; Qualifier string; Event []byte; Invocation InvocationType } // Event | RequestResponse (ESM uses Invoke directly, not the outbox)
type SNSTopic struct{ ARN aws.ARN; Message string; Attributes []MessageAttribute; Subject string }

type Mechanism uint8 // closed: SNSToSQS | SNSToLambda | S3ToSQS | S3ToSNS | S3ToLambda | EventsToLambda | EventsToSQS | EventsToSNS

type Request struct {
	Source    aws.ARN
	Mechanism Mechanism
	Target    Target
	Policy    RetryPolicy // SNSManaged | EventBridgeTarget{MaxAge, MaxAttempts} | S3Notification | None
	DLQ       *aws.ARN
}
```

```go
package outbox // the engine: a Stateful service with one cell per scope

type Entry struct {
	ID       ids.DeliveryID
	Request  delivery.Request
	Attempt  int
	NextAt   clock.Instant
	Cause    ref.Cause
	Accepted clock.Instant
	Seq      uint64 // the order the outbox took deliveries in, drawn under the cell lock
	Jitter   uint64 // seeds an EventBridge target's retry jitter; 0 for other policies
}
```

Deliveries to one SQS FIFO queue with one `GroupID` form a lane, attempted one at a time in `Seq` order: only the first pending one is armed, a retry keeps the lane blocked, and a terminal outcome arms the next no earlier than itself (`sns.md 10`). A group on a standard queue is a fair-queue tenant label and forms no lane (`sqs.md 8.5`); deliveries outside a lane run concurrently.

A `Deliver` effect is an outbox insert the kernel performs before the source call returns, so an accepted delivery is state the moment SNS returns a `MessageId`; the insert arms the attempt timer. Attempts, backoff and DLQ routing run on the emulator clock with jitter drawn from the seeded source, so `clock advance` replays a retry storm deterministically. A due attempt is tracked work (`DeliveryAttempt`) stamped `At = due` that calls the target through `kernel.Invoke` as `AsService{name}` with `Origin Internal{Via}`, is subject to `Phase: Delivery` rules, emits a `journal.Delivery` event whose parent is the source event, and removes the entry on a terminal outcome. `Rearm` restores every pending attempt after a snapshot load. Client-side failures (queue or function missing) go straight to the DLQ; filtered messages never reach a DLQ. Fan-out order is deterministic (subscription or target creation order). Before each attempt the access recorder checks the target's stored resource policy with `policydoc` and journals an `Advisory` when it would not allow the delivery in AWS (SNS to a queue without a `sqs:SendMessage` statement for the topic, EventBridge to a function without `lambda:InvokeFunction` permission); the delivery is still attempted.

| Path | Trigger | Target operation | Retry and failure |
|---|---|---|---|
| SNS -> SQS | Publish (after response) | `sqs:SendMessage`, envelope or raw; FIFO keeps group and dedup ids | AWS-managed policy (3 immediate, 2 at 1 s, 10 exponential 1 to 20 s, 100,000 at 20 s); missing queue to subscription DLQ at once |
| SNS -> Lambda | Publish | async `Invoke` with `Records[0].Sns` | Lambda async retries after acceptance |
| SNS -> HTTP/S | Publish | none in phase 1: `Subscribe` stores a pending subscription with a readable token and `ConfirmSubscription` works; the outbound POST engine and its allow-list are phase 2 | n/a |
| SQS -> Lambda | ESM poller cycle | sync `Invoke`, then internal `DeleteMessageBatch`, both `AsExecutionRole` | visibility timeout; DLQ by `maxReceiveCount` |
| SQS -> SQS (DLQ) | the receive attempt that exceeds `maxReceiveCount` | inside SQS | a `Redrive` journal event whose parent is the receive (or the move task's step) |
| EventBridge -> Lambda, SQS, SNS | schedule timer or `PutEvents` | async `Invoke`, `SendMessage`, `Publish` with `Input`, `InputPath`, `InputTransformer` | stored target retry policy; unknown bus drops with 200 and a journal entry |
| S3 -> SQS, SNS, Lambda | object commit (`Deliver` effect) | `SendMessage`, `Publish`, async `Invoke` with `Records`; `s3:TestEvent` to SQS and SNS on configuration | async; per-key monotonic `sequencer` |
| S3, Secrets Manager, SQS (SSE) -> KMS | synchronous, before the caller's lock | `GenerateDataKey`, `Decrypt`, `Encrypt` as `AsCaller` (forward access, `iam-sts-kms.md 5.8`), `Via: KMSForward` | journaled as KMS calls, faultable; recorded against the caller so the generated policy carries `kms:GenerateDataKey` and `kms:Decrypt` |
| Lambda -> Logs | container log stream | internal `PutLogEventsInternal` as `AsExecutionRole` | none |

Envelope and event builders are pure functions with golden fixtures and rapid properties, owned by the producing service: `service/sns/envelope` (SQS envelope with the recorded key order, FIFO variant without signature fields, Lambda `Records[0].Sns` casing; one RSA key per account-region, persisted, cert at `SigningCertURL`), `service/s3/notify` (`eventVersion 2.1`, URL-encoded key, unquoted `eTag`, `sequencer`), `service/events/envelope` (version `0`, second-precision `time`, `InputPath` and `InputTransformer`), `service/sqs` (shared attribute MD5).

The shared `ruler` engine serves EventBridge patterns, SNS filter policies (behind SNS's operator allow-list and limits) and ESM filters, compiled once to a typed pattern ADT at configuration time. The four Ruler-versus-doc conflicts (`matching-engines.md 10`) are fidelity switches, default Ruler, strict compile errors for malformed `$or` shapes.

Settling: the kernel's work tracker counts a closed `WorkKind` set: `EffectPending`, `DeliveryAttempt`, `AsyncInvokeQueued`, `InvocationRunning`, `TimerDue`. Parked waiters never count: a client long poll, an idle ESM poller waiting on its `WaitKey`, a journal stream. `GET /_understudy/v1/settle?timeout=30s` returns when all are zero or names what is still pending grouped by kind; in Running mode it ignores timers not yet due.

## 17. Admin API and CLI (ADR 0017)

Mounted at `/_understudy/v1/` on the main listener; JSON both ways; errors `{"error": {"code", "message", "hint"}}` where `code` is a closed `admin.ErrorCode` (`invalid_request`, `not_found`, `conflict`, `condition_not_met`, `unavailable`, `internal`) mapped to CLI exit codes in one table in `admin`. A non-loopback main listener requires `Authorization: Bearer <admin token>` on every admin endpoint unless the listener is `Exposed{ContainerPublished}` (18). The container listener exposes only `GET /health` and `GET /clock`. `GET /health` answers `503` with `{"ready": false, "waiting": [...]}` until every service is constructed and the listener accepts, then `200`; readiness is what `--ready-file`, Testcontainers wait strategies and `understudy health --wait` key on.

| Endpoint | Purpose |
|---|---|
| `GET /health` | version, uptime, per-service status (enabled, disabled with reason, degraded), Docker and DynamoDB Local status, model pin, the container topology (`admin.Topology`), the bound main listener and the clock as `GET /clock` reports it |
| `POST /reset` | all state, or `{"services":[], "accounts":[], "regions":[]}`; drains Lambda environments; clock and faults kept unless asked |
| `GET /clock`, `POST /clock` | read; `{"op":"freeze"|"resume"|"advance"|"set"|"scale", "by":"90s", "to":"...", "factor":60}`; returns timers fired |
| `GET /faults`, `PUT /faults/{id}`, `DELETE /faults/{id}`, `DELETE /faults` | rules; PUT validates against the operation's catalogue |
| `GET /journal/events` | query with cursor pagination; `request_id=` is a filter |
| `GET /journal/stream` | NDJSON stream, same filters, `?after=<id>` |
| `GET /journal/traces/{id}` | causal tree by trace id or by `request_id=`; `?canonical=1` returns the normalized tree of section 12 |
| `POST /snapshots` (archive out), `PUT /snapshots` (archive in) | save and load; with `--persist` also `{"path": ...}` under `<data dir>/snapshots`, apart from the lock, state file, blobs and DynamoDB files |
| `GET /resources` | every resource ARN by service, account, region with a short state summary |
| `GET /coverage?format=json|markdown` | registry coverage, protocol compliance results, fidelity switches; overlay evidence only in the offline report (`docs/coverage.md`, `understudy coverage`), since the binary does not carry the overlay files |
| `GET /iam/usage`, `GET /iam/policy?principal=` | access record; least-privilege policy. Contract: one statement per `(service, resource ARN)` with exact ARNs (no wildcards unless the operation has no resource); policy rule (ADR 0004 amendment): the primary action always; same-prefix secondaries when the binding declares them used for the input (`kernel.WithActions`, or none with `kernel.WithoutSecondaryActions`, at the bind site or later through `kernel.Declare`), and the whole generated list for a binding that declares neither (an over-grant, until the service's actions are audited); other-prefix actions only on a touched resource of their service; S3 object operations touch object ARNs, which the access record keeps, and the usage and policy render, as `<bucket>/*`, so the record grows with buckets rather than keys; calls the edge refused at authentication (sigv4 verify) are not recorded, since on AWS they never reach authorization; failed calls that were not injected are recorded, because the application's handled failures need permissions too; a separate `resourcePolicies` section lists the resource-policy requirements deliveries would need (target ARN, service principal, action, source ARN) |
| `GET /settle?timeout=` | wait for quiescence; `pending` grouped by `WorkKind` on timeout |
| `POST /lambda/functions/{name}/hot-reload` | bind a host directory |
| `POST /lambda/images/pull` | pre-warm pinned runtime images |
| `POST /dynamodb/ttl-sweep` | run the TTL sweep now |
| `GET /sns/subscriptions/{arn}/token` | read a pending confirmation token |

Unversioned AWS-visible URLs under `/_understudy/` (outside `/v1`): SNS `SigningCertURL`, unsubscribe and confirm links, the function URL path fallback.

The CLI is a thin client of `admin.Client`. Non-interactive always; `--output text|json` on every command; `--endpoint` or `UNDERSTUDY_ENDPOINT` (default `http://127.0.0.1:4566`); exit codes 0 success, 1 operation failed, 2 usage, 3 emulator unreachable, 4 condition not met (settle timeout, health not ready), 5 admin token missing or rejected; every error names what failed and the next command to try; every `--help` has examples.

```
understudy serve [--config FILE] [--listen 127.0.0.1:4566] [--persist DIR] [--load-snapshot FILE] [--seed N]
                 [--clock running|frozen|frozen@RFC3339] [--clock-scale F] [--sigv4 parse|verify]
                 [--ready-file PATH] [--log-format json|text] [--log-level L]
understudy health [--wait 30s]
understudy settle [--timeout 30s]
understudy reset [--service S] [--account A] [--region R]
understudy clock now | freeze | resume | advance 15m [--settle] | set 2026-12-31T23:59:00Z | scale 60
understudy faults list | add [--service S] [--operation O] [--resource GLOB] [--account A] [--region R] [--origin any|external|internal]
                       [--phase request|delivery] (--error CODE | --throttle | --latency D | --after-commit CODE | --status-ok CODE | --drop)
                       [--next N | --probability P] | remove ID | clear | load FILE
understudy journal [--trace ID] [--request-id ID] [--service S] [--operation O] [--resource ARN] [--errors] [--since 10m] [--follow]
understudy journal trace (ID | --request-id ID) [--canonical]
understudy snapshot save FILE | load FILE | inspect FILE | diff A B [--normalize]
understudy resources [--service S] [--account A] [--region R]
understudy coverage [--service S] [--format text|markdown|json] [--switches]     # offline from the registry when no server is up
understudy iam usage [--principal ARN] | iam policy --principal ARN
understudy lambda hot-reload FUNCTION DIR
understudy images pull [--runtime R]
understudy env [--sdk go|python|js|cli|terraform] [--format sh|fish|dotenv|github]
understudy doctor
understudy cleanup
understudy version
```

`--ready-file PATH` writes `{"endpoint", "pid"}` once listening; with `--listen 127.0.0.1:0` parallel CI jobs are trivial. The startup banner prints the endpoint, the `env` block, the admin URL, enabled services and warnings (`*.localhost` does not resolve here; Docker not reachable).

## 18. Configuration

Precedence: flags > `UNDERSTUDY_*` environment > JSON config file (`--config`, or `understudy.json` in the working directory) > defaults. JSON because it needs no dependency; parsed strictly (unknown keys are errors naming the key and the closest valid one) into a typed `config.Config` at the boundary. Durations are Go duration strings, times RFC 3339.

```go
package config

type Config struct {
	Listen      Listen         // Loopback{AddrPort} | Exposed{AddrPort, Auth: Token{Secret} | ContainerPublished}; default Loopback 127.0.0.1:4566
	TLS         TLS            // Off | SelfSigned{Listen}
	Persistence Persistence    // Ephemeral | Dir{Path, SaveEvery}
	Snapshot    Snapshot       // MaxBytes: every archive, saved, uploaded, loaded or checkpointed
	SigV4       SigV4Mode      // ParseOnly | Verify
	Seed        *uint64
	Clock       ClockStart     // Running{At?} | Frozen{At}; Scale
	Accounts    Accounts       // default account, static keys
	Regions     []aws.Region   // additions to the real-name table
	S3          S3             // BaseDomains (localhost.localstack.cloud may be added); CrossRegion: AWS | Lenient; MaxObjectBytes
	Lambda      Lambda         // Network: Auto | Bridge{Gateway} | Disabled; StartConcurrency; IdleTimeout; MaxEnvironments; HotReload []Mapping
	DynamoDB    DynamoDB       // Mode: Enabled | Disabled; Registry: DockerHub | ECRPublic; MemoryLimit
	Journal     Journal        // Capacity; Bodies; File
	Faults      []fault.Rule   // preloaded
	Log         Log            // slog level; JSON or text
}
```

The invalid combinations are unrepresentable: a non-loopback listener without a token cannot be constructed, and `ContainerPublished` is accepted by the boot validation only in a container with its own network namespace, outside Kubernetes (the published port is then the exposure control; the banner warns and documents `-p 127.0.0.1:4566:4566`). The token guards only the admin API and the AWS API verifies no signature by default (`sigv4: parse`), so `Token` is built only with `--allow-unauthenticated-aws-api`, which accepts the AWS API as open to every peer that reaches the listener; the server repeats the check (`server.AWSAPIUnauthenticated`). Defaults worth defending: port 4566 and account `000000000000` so existing `AWS_ENDPOINT_URL=http://localhost:4566` setups work unchanged; `Lambda.Network: Auto` binds the container listener only once an environment is needed and only on the topology's address; body caps per operation, not global.

## 19. Testing strategy

Shape conformance, behavioral tests and recorded parity are reported separately, never summed.

| Layer | Where | Runs in | Proves |
|---|---|---|---|
| Generator golden tests | `internal/codegen` | `go test` | model fragments to expected Go; unknown traits fail; generate twice, compare |
| Model example tests | generated `examples_gen_test.go` | `go test` | the 260 model examples decode and validate |
| Protocol compliance | `internal/protocoltest` | `go test` | codecs, bindings and error writers against the official corpus (6.4); per-protocol pass, skip, fail counts feed coverage |
| Codec and engine properties | per package, rapid | `go test` | round trips for every codec; CBOR never panics on mutated input (all 65536 half-float patterns); form index permutations; `aws-chunked` encode and decode over random chunkings with and without trailers; checksums streaming equals one-shot and combine equals whole; Ruler invariants (array and scalar intersection, rule-order independence, numeric never matches strings); log pattern algebra (complement, commutativity); schedule expressions; input transformer output parses; SigV4 sign-then-verify; listing pages concatenate to the unpaginated listing; `encoding-type=url` round trip; SQS MD5 vectors |
| Service semantics | `internal/service/*` | `go test`, `synctest.Test` via `kerneltest` | AWS-observable behavior on the emulator clock through `kernel.Invoke`: visibility, redrive, FIFO group blocking, dedup, long polls, TTL, retention, schedules, deletion windows, key states; rapid `StateMachineActions` for the SQS queue (interleaving receive, delete and change-visibility against timer state), the DynamoDB TTL shim and the Lambda pool against a fake launcher; golden vectors from research (SQS MD5, CBOR G01 to G24, checksum vectors, Coral texts, Ruler seeds, Logs vectors); identifier-form fanout (name, `name:qualifier`, partial ARN, full ARN, other-region and other-account ARN) and create-then-get echo per resource type |
| Policy generation | `internal/access` and the smoke test | `go test` | the generated policy for a function role contains `AWSLambdaSQSQueueExecutionRole` and `AWSLambdaBasicExecutionRole` actions after an ESM-driven invoke; the policy for an SSE-KMS workload contains `kms:GenerateDataKey` and `kms:Decrypt` |
| Budgets | `just budgets` | CI | the ceilings in 5.7 |
| Wire goldens | `internal/edge/testdata` | `go test` | exact request lines real SDKs send (Go snapshots, botocore forms, `?uploads` vs `?uploads=`, `x-id`) route to the same operation; `httptest.NewTestServer` inside bubbles |
| Parity replay | `internal/parity` over `testdata/parity` | `go test` | not built (conformance phase): the recordings exist, the replayer does not. Planned: recorded exchanges replayed in-process; normalizers are a closed, named set (request ids, receipt handles, timestamps, hosts, account ids) |
| Snapshot round trip | per service | `go test` | random state, save, load, save, byte-identical; `Rearm` reproduces the scheduler |
| Real-client conformance | `conformance/*` | each suite's `run.sh` via `scripts/sealed`; CI `conformance` job | Go v2, boto3, JS v3, AWS CLI; per-SDK wire-capture fixtures that fail when an SDK release changes its wire form; DynamoDB runs unless opted out explicitly (`CONFORMANCE_DYNAMODB=0`), and every skip is listed in the run's summary |
| Terraform | `conformance/terraform` | `scripts/sealed` | per resource group at provider 6.68.0: apply, `plan -detailed-exitcode` is 0, mutate, apply, plan, destroy; journal call-sequence assertions; per-test account isolation; a non-blocking canary against the latest provider |
| End to end | `conformance/e2e` (later phase) | `scripts/sealed` | the phase 1 workload with a fixed seed in both Frozen and Running modes; asserts the canonical trace tree and a normalized snapshot diff (section 12) |
| Docker integration | `-tags docker` | `just test-docker` | Python 3.13 and `provided.al2023`: warm reuse, `Unhandled`, `Runtime.ExitError`, timeout then cold start, OOM, concurrent invokes get separate environments, chunked-trailer errors, the missing `Content-Type` regression, hot reload, crash recovery after `kill -9`, the deadline test (frozen then advanced clock leaves `get_remaining_time_in_millis()` within 100 ms of the timeout; `lambda-runtime-contract.md 1`), DynamoDB Local egress; part of the gate |
| Smoke | `internal/smoke` | `just smoke` | builds the binary, starts it on `127.0.0.1:0` with `--ready-file`, waits for `/health` ready, drives one raw HTTP request per service (both HTTP and the self-signed TLS listener for the S3 write path with an `aws-chunked` trailer body), asserts the generated policy for the smoke workload, runs inside the Docker image as the `InContainer` check |

Behavior tests do not go over HTTP with a hand-rolled client; wire correctness is the job of the compliance suite, the goldens and the real-SDK modules. Time-dependent tests use `synctest` with the emulator clock; no test sleeps on wall time except the Docker and Terraform suites, whose provider floors (55 s per lifecycle configuration, 5 s per Lambda waiter, 25 s and 42 s SQS waits) are documented and sized into their timeouts. Every test gets its own account or region namespace, or its own server on `127.0.0.1:0`. Docker tests carry `understudy.dev/test=1` plus a per-run label, unique names, and delete by name.

Parity recordings carry evidence labels (`aws-recorded`, `third-party-recorded`, `aws-doc`, `sdk-source`, `inferred`); the replayer reports pass counts per label. Record mode lives only in `conformance/parity-record`, refuses to run without an explicit human-only flag and real credentials it reads itself, and is never run by agents. Its backlog is R01 to R18 in `s3-checksum-residuals-and-record-backlog.md`.

Gates: `go vet`, `golangci-lint` (the strict configuration in `go-toolchain.md 13`), `go test -race ./...`, `just generate-check`, the protocol-test baseline ratchet, the coverage page drift check, the snapshot round trip across every registered service, `just budgets`, and `just test-docker` once the Lambda runtime exists. The real-SDK conformance suites, Terraform suites, the e2e workload and the scheduled SDK and provider canaries belong to the conformance phase. The human-run R01 to R18 recordings are filed under `testdata/parity/`, captured with the [recording kit](recording-kit.md). CI also runs `examples/check.sh` (the `examples` job) and every conformance suite's run.sh (the `conformance` job, one matrix entry per suite) on pushes to main and pull requests, DynamoDB Local included (ADR 0014 amendment of 2026-10-14).

## 20. Security model

- Listeners: main on `127.0.0.1` by default. The container listener and per-environment Runtime API listeners bind only the topology's address, never `0.0.0.0`, exist only while Lambda environments do, and serve no admin API. A non-loopback main listener requires an admin token together with `--allow-unauthenticated-aws-api` (the token guards only `/_understudy/`), except inside a container with its own network namespace outside Kubernetes (`Exposed{ContainerPublished}` validated against the detected placement), where Docker's port publishing is the exposure control; without one of the two, `serve` exits with an error that explains both options. A loopback listener answers only Hosts no outside DNS server can point at it (DNS rebinding); a listener beyond loopback answers every Host from a client that is not a browser, since its clients name it by whatever their network calls it, and holds a browser's request to the loopback names. The Runtime API has no authentication in AWS's protocol; isolation is the per-environment port on the bridge address, documented as within the local Docker trust domain.
- Docker socket access is root-equivalent on the host; the README and `doctor` say so. Function containers: memory limit, `PidsLimit`, `nofile`, `CapDrop: ["ALL"]`, `no-new-privileges`, no privileged mode, no host network, no socket mount, read-only code mounts, labels for sweeping.
- Filesystem: S3 keys never become paths; zip extraction rejects non-local names and symlinks and caps sizes; extracted trees are content-addressed and read-only once written; the snapshot loader validates every tar entry name with `filepath.IsLocal` before writing; no symlink is ever created from user input.
- Input limits: body caps per operation, `MaxHeaderBytes`, `MaxHeaderValueCount`, CBOR depth and length checks, JSON and XML decoders with size limits; region strings validated before they reach DynamoDB Local; user metadata 2 KB; S3 single PUT 5 GiB; DynamoDB 413 above 16 MiB.
- Secrets: `sensitive` members are redacted in the journal and logs from the schema; credential and secret types implement `slog.LogValuer`; secret values and KMS plaintext never appear in errors; minted credentials and container `Env` arrays are never logged. KMS uses real AES-256-GCM with the encryption context as AAD; key material comes from `crypto/rand` even when seeded.
- Identity: real-looking access keys are never decoded into foreign account numbers; SigV4 verification is opt-in, parsing always runs.
- Outbound network: none at runtime except image pulls and allow-listed SNS HTTP delivery; no telemetry; DynamoDB Local telemetry disabled and egress-tested.
- Containers: every object labeled with `understudy.dev/managed`, `instance`, `owner`, `function`, `account`, `region`, `generation`; graceful drain on SIGINT and SIGTERM (`DELETE ?force=1&v=1`, Python RIC as PID 1 ignores SIGTERM); startup sweep of this owner's leftovers guarded by the data-dir `flock`; every `serve` also removes, in the background, the containers, networks and temporary directories of runs without persistence whose process is gone, judged by boot id, PID namespace, PID and process start time (`internal/server/runs.go`), and never a run of another boot or PID namespace, which may be live elsewhere; `understudy cleanup` on demand; `understudy.dev/test=1` is set only by test harnesses.
- `scripts/sealed` is the only way tooling in this repo talks to understudy; conformance suites run through it.

## 21. Phase 2 extension points

| Extension | Seam that exists in phase 1 |
|---|---|
| New service | vendor the model, add overlay and service config, `just generate`, copy a skeleton, bind handlers, add a conformance case (5.8) |
| New protocol | implement `smithy.Serializer` and `Deserializer`, add a claim rule and an error writer; the corpus already carries its cases |
| IAM enforcement | the access recorder sees `(principal, actions, resources)` for every call; an evaluator over `policydoc` slots in at pipeline step 8 as a `fault`-like stage |
| DynamoDB Streams to Lambda | `EventSourceKind.DynamoDBStream` variant; stream reads through the proxy with rewritten ARNs; the TTL sweeper already tags its deletes |
| Lambda destinations, API Gateway, Step Functions, Kinesis | new `delivery.Target` variants and envelope builders; the retry engine and journal edges are shared |
| Web UI | the journal NDJSON stream, `/resources` and `/coverage` are the data plane; an SSE variant is one `Accept` branch |
| Wildcard DNS for containers | `topology` yields addresses; a stdlib DNS responder handed to containers via `HostConfig.Dns` is a new listener behind an ADR |
| Named-volume code delivery, Ryuk-style reaper, private registries | `lambda/launcher` and `docker` own those decisions; nothing else knows about mounts or auth |
| SigV4A, signed streaming for non-Go SDKs | `auth` payload-hash sum type already names the variants |
| Scaled-clock ramp semantics for ESM, provisioned concurrency | state-derived scheduler tasks per mapping |
| Write-ahead log for persistence | `Cell.rev` and the section writers; the archive format is versioned; bbolt is pre-qualified for the log, beside the archive (ADR 0016, dependency policy amendment) |

## 24. Distribution

- Binary: `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w -X main.version=..."`, for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`. `.goreleaser.yaml` builds the matrix, archives, checksums and the multi-arch image; nothing is published in phase 1 (`just release-check` runs `goreleaser check` and a snapshot build when the tool is installed; the CI workflow runs the same on tags without a publish step).
- Image: `FROM scratch` plus `ca-certificates` (image pulls over TLS), the static binary, a non-root user (`65532`), `ENTRYPOINT ["/understudy"]`, `CMD ["serve"]`, `EXPOSE 4566`, `HEALTHCHECK` running `understudy health` (which follows `UNDERSTUDY_ENDPOINT`, else the port of `UNDERSTUDY_LISTEN`), labels `org.opencontainers.image.*`. Inside the image `serve` binds `Exposed{0.0.0.0:4566, ContainerPublished}` without a token only when the container has its own network namespace outside Kubernetes; with host networking or in a pod it defaults to loopback with a warning, and listens beyond it only with an admin token. It warns in the banner, and reaches Docker only when `/var/run/docker.sock` is mounted (otherwise Docker and DynamoDB are "disabled: no Docker socket" in `/health`, and Lambda is degraded to its control plane: functions can be created and read, and only Invoke fails). Persistence mounts `/data`. The compose and Testcontainers recipes in the later docs phase build on `--ready-file` and the health endpoint.
- Platforms: Linux and macOS natively; Windows through WSL2 only. The Docker socket on Windows is a named pipe the standard library cannot dial, and the data-dir lock uses `flock`.

## 27. ADR index

| ADR | Decision |
|---|---|
| [0001](adr/0001-generated-schema-serde.md) | Generated schema serde against codec interfaces, not reflection |
| [0002](adr/0002-model-overlays.md) | Overlays as Smithy JSON AST with `dev.understudy#` traits and mandatory evidence |
| [0003](adr/0003-operation-binding-and-coverage.md) | Per-operation typed binding with coverage marks; 501 `NotImplemented`; fidelity ledger |
| [0004](adr/0004-one-kernel-pipeline.md) | One kernel pipeline for external and internal calls; no HTTP loopback |
| [0005](adr/0005-state-cells-and-effects.md) | State cells with typed post-commit effects and a gate |
| [0006](adr/0006-clock-domains-and-scheduler.md) | Three time domains, `clock.Instant`, forward-only stepwise clock, heap scheduler, ESM pinning |
| [0007](adr/0007-causal-journal.md) | Causal journal: typed events, state-carried causes, NDJSON stream, schema-driven redaction |
| [0008](adr/0008-fault-injection.md) | Fault rules validated against the error catalogue, seeded triggers, request and delivery phases |
| [0009](adr/0009-snapshot-format.md) | Snapshot format v1, re-arm instead of serialized timers, opt-in persistence without a WAL |
| [0010](adr/0010-lambda-hosted-runtime-api.md) | Lambda runs on an understudy-hosted Runtime API, not the RIE; hot reload as an overlay |
| [0011](adr/0011-container-networking.md) | Container listener on the bridge gateway, per-environment listeners, IPv4-literal endpoints |
| [0012](adr/0012-identity-and-defaults.md) | Account from access key policy, default account, port, region table |
| [0013](adr/0013-s3-addressing-and-urls.md) | No DNS server, base-domain addressing, AWS cross-region behavior by default, queue URLs echo the Host |
| [0014](adr/0014-dynamodb-local-proxy.md) | DynamoDB Local as a raw proxy behind the pipeline, on by default under Amazon's licence terms, TTL in the shim |
| [0015](adr/0015-cross-service-delivery.md) | Delivery engine on the clock; SNS HTTP delivery behind an allow-list |
| [0016](adr/0016-third-party-inputs-and-dependencies.md) | Pinned models and corpus, derived IAM table, rapid, in-repo xxHash |
| [0017](adr/0017-admin-api-cli-config.md) | Admin API, non-interactive CLI, configuration precedence, public Go packages |
| [0018](adr/0018-ids-and-determinism.md) | Seeded id streams and the determinism contract |
