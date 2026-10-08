# Proposal: fidelity-first, codegen-maximalist architecture for understudy phase 1

Status: proposal (architect role), 2026-10-08. Input: [phase 1 brief](../../brief/phase1-brief.md) and every file in [docs/research/phase1/](../../research/phase1/). Research files are cited by name and section, for example `sqs.md 13.7`.

## Thesis

understudy wins on "it behaves like AWS". This proposal treats that as an engineering property with three layers of proof, and makes the code shape follow from it:

1. **Everything the Smithy models can say is generated, never hand-typed.** Wire types, serde, validators, error types and identities, the operation registry, REST routing tables, IAM action tables, redaction, coverage data, and the protocol test cases themselves. Where AWS differs from its own model, the difference is written once as a reviewable **overlay**, in Smithy's own JSON AST `apply` syntax plus a small `dev.understudy#` trait namespace, each entry carrying evidence. Vendored models are never edited.
2. **Protocol behavior is proven by the official Smithy compliance corpus**, run against code emitted by the same generator for the corpus's synthetic services. The codecs under test are the ones the real services use.
3. **Service behavior is pinned by parity recordings**: request/response exchanges with provenance labels (`aws-recorded`, `third-party-recorded`, `aws-doc`, `sdk-source`, `inferred`) replayed in plain `go test`. Agents only ever replay; a human records.

Adding an operation is: list it in `codegen/services/<svc>.json`, run `just generate`, implement one method on a generated interface. The compiler refuses to build until the method exists, and the coverage report updates itself.

Principles that decide ties:

- Wire fidelity beats convenience. Where the default is a judgment call, the default is what AWS does, and leniency is a named config switch.
- Every deviation from AWS is data (an overlay entry, a skip-list entry, a named semantic switch) that the coverage report lists. Nothing deviates silently.
- Internal cross-service traffic goes through the same middleware as external traffic (journal, faults, access recorder) but not through HTTP (`landscape.md` Implications 6).
- Time has three domains (emulator, wall, monotonic) and the type system keeps them apart (`lambda-runtime-contract.md` section 3).

---

## 1. Package layout

One module, `github.com/sustinbebustin/understudy`, standard library only (test-only `pgregory.net/rapid`). `depguard` and `forbidigo` enforce it, using the config in `go-toolchain.md` 13.

```
cmd/understudy/                 main: flag parsing and subcommand dispatch only
internal/
  cli/                          subcommands; an admin-API client; no service imports
  config/                       Config type, parsing (flags > env > file), validation

  server/                       listeners (main, gateway, optional TLS), graceful shutdown
  frontdoor/                    request claiming: host rules, auth parse, protocol detection
  pipeline/                     admission -> decode -> validate -> journal -> faults -> handler -> encode
  op/                           Call (request context), Origin, Endpoint
  registry/                     Service and Operation descriptors, Binding, Impl
  inproc/                       in-process invoker for cross-service calls (same pipeline, no HTTP)

  schema/                       runtime shape descriptors (hand-written types; values are generated)
  serde/                        Encoder/Decoder interfaces every protocol implements
  opt/                          Option[T]
  validate/                     Violation, Path, constraint checks, message styles (Coral, Smithy framework)
  awserr/                       error ADT and per-protocol identity rules
  protocol/
    awsjson/                    awsJson1_0 and awsJson1_1 (+ awsQueryCompatible header)
    awsquery/                   awsQuery (SNS, IAM, STS, CloudWatch, SQS legacy)
    restbind/                   HTTP label/query/header/payload binding interpreter
    restjson/                   restJson1 (Lambda)
    restxml/                    restXml (S3, S3 Control tags)
    rpcv2cbor/                  Smithy RPC v2 CBOR (CloudWatch from Go v2)
  codec/
    jsonx/                      the only importer of encoding/json/v2 and jsontext
    xmlw/                       streaming XML writer (namespaces, xsi:type, escape table)
    form/                       x-www-form-urlencoded with numeric index ordering
    cbor/                       ~340-line stdlib CBOR subset (wire-cbor.md 7)
  auth/
    sigv4/                      parse (always), verify (opt-in), presign, aws-chunked
    sigv2/                      boto3's SigV2 presigned S3 URLs (sigv4-checksums-endpoints.md 2.3)
    identity/                   access key -> account and principal; key minting
  aws/                          AccountID, Region, Partition, Scope, ARN parsing

  clock/                        emulator clock, scheduler, wall and monotonic sources
  ids/                          request ids, UUIDs, receipt handles; seedable source
  journal/                      events, causal spans, ring buffer, query, SSE stream
  faults/                       rules, matchers, actions, seeded budgets
  snapshot/                     deterministic state archive, migrations
  state/                        Partitioned[S], Effects outbox
  blob/                         content-addressed blob store under os.Root
  accessrec/                    IAM action recorder and least-privilege policy generator
  coverage/                     coverage report (JSON and Markdown)
  admin/                        /_understudy/v1 HTTP API (net/http.ServeMux is fine here)
  docker/                       Docker Engine API client over the unix socket
  delivery/                     cross-service delivery engine, targets, retry policies

  match/ruler/                  Event Ruler semantics core (EventBridge, SNS, Lambda ESM)
  match/logpattern/             CloudWatch Logs filter pattern compiler and matcher

  api/<svc>api/                 GENERATED per service: types, schema, serde, validators,
                                errors, Handlers interface, Bind, routes, IAM, redaction
  service/<svc>/                hand-written domain state and handlers, one package per service:
    sqs/ sns/ s3/ s3/checksum/ dynamodb/ lambda/ lambda/runtimeapi/ lambda/pool/
    lambda/launcher/ lambda/esm/ lambda/furl/ lambda/codestore/ logs/ secretsmanager/
    kms/ iam/ sts/ events/ cloudwatch/

  codegen/                      generator internals: smithy loader, overlay merge, IR, emitters
  cmd/gen/                      `go run ./internal/cmd/gen` (generator entry point)
  cmd/vendor/                   model and corpus vendoring tool (network only when run by hand)
  protocoltest/                 compliance harness, skip list, comparators
  protocoltest/gen/             GENERATED: synthetic test services and their case tables
  parity/                       parity recording replayer (stdlib only)

codegen/
  services/<svc>.json           per service: implemented operations, extra protocols, styles
  overlays/<svc>.json           Smithy JSON AST apply/shape fragments + dev.understudy traits
  overlays/common.json          framework errors, generic vocabulary (error-matrix.md 3)
third_party/
  api-models-aws/               12 model files verbatim at 1cfdfb4, UPSTREAM.json, LICENSE, NOTICE
  smithy-protocol-tests/        flattened AST from Smithy 1.74.0 jars, PIN.json (jar + CLI sha256)
  aws-service-reference/        derived operation -> IAM action table with provenance (no raw JSON)
testdata/parity/<svc>/          parity recordings
conformance/                    separate modules, own manifests, run through scripts/sealed:
  gosdk/ python/ js/ cli/ terraform/ e2e/ parity-record/
docs/
  coverage.md                   generated by `understudy coverage --format markdown`, drift-checked
```

Dependency direction is strict and checked by a test that walks imports:

- `api/*` (generated) imports only `schema`, `serde`, `opt`, `validate`, `awserr`, `registry`, `op`.
- `service/*` imports `api/*`, `state`, `clock`, `journal` (span API only), `delivery`, `inproc`, `blob`, `ids`. A service never imports another service; it reaches it through `inproc` and the other service's generated `api` types.
- `protocol/*` never imports `service/*` or `api/*`. It sees shapes only through `schema` and `serde`.
- `frontdoor` and `pipeline` see services only through `registry`.

## 2. Request lifecycle

```
TCP -> server -> frontdoor.Claim(req) -> Route{service, protocol, operation | unknown}
    -> pipeline:
       1 admission      request id, emulator time, body cap (per-operation, generated)
       2 auth           parse SigV4/SigV2/anonymous (always); verify signature (opt-in)
       3 identity       account from access key, region from credential scope, principal
       4 resolve        registry lookup; Unimplemented -> 501 now (step 9 skipped)
       5 decode         protocol codec + generated Deserialize -> typed input | Framework error
       6 validate       generated validators -> typed input | awserr.Validation
       7 journal open   span with redacted input (generated Redact)
       8 faults ingress first matching rule may short-circuit with an AWS-shaped error
       9 handler        typed method on the service; returns (Output, error) and Effects
      10 faults egress  latency, 200-with-error (S3), response replacement
      11 encode         generated Serialize + protocol codec; error -> protocol error writer
      12 journal close  status, error identity, resources touched; access recorder
      13 effects        outbox drained after the state lock is released (section 3.5)
```

The check order 2 to 6 follows the order observed on AWS frontends: unknown operation, auth, deserialization, constraint validation, business rules (`error-matrix.md` 4, last paragraph). Internal calls through `inproc` enter at step 3 with a service principal and an `op.Internal` origin, skip 1, 2 and the wire parts of 5 and 11, and run everything else.

## 3. Core abstractions

### 3.1 Request context

```go
package aws

// AccountID is always 12 decimal digits; construct with ParseAccountID.
type AccountID struct{ digits [12]byte }

// Region is a member of the region table (real AWS names by default; Terraform's
// aws_region data source rejects others, gap-G6.md finding 6).
type Region struct{ name string }

// Scope is where a request acts.
type Scope struct {
	Account AccountID
	Region  Region
}

// StateKey is how a service partitions its state.
type StateKey interface{ stateKey() }

type Regional struct{ Account AccountID; Region Region } // most services
type Global struct{ Account AccountID }                 // IAM, STS, S3 bucket namespace (section 7)
```

```go
package identity

// Principal is who made the call; recorded by the access recorder.
type Principal interface{ principal() }

type Root struct{ Account aws.AccountID }
type User struct {
	Account    aws.AccountID
	Path, Name string
	UserID     string // AIDA...
}
type AssumedRole struct {
	Account                aws.AccountID
	RoleName, SessionName string
	RoleID                string // AROA...
}
type ServicePrincipal struct{ Name string } // "sns.amazonaws.com" on internal deliveries
type Anonymous struct{}                     // function URL with AuthType NONE, unsigned public reads
```

```go
package op

// Call is the per-request context every handler receives. Only the pipeline builds it.
type Call struct {
	RequestID ids.RequestID
	Scope     aws.Scope
	Principal identity.Principal
	Origin    Origin
	Operation registry.OperationID
	Protocol  protocol.ID   // protocol.InProcess for internal calls
	Endpoint  Endpoint      // scheme, host, port exactly as the client addressed us
	Received  clock.Instant // emulator time at admission
	Span      journal.SpanID
	touched   []aws.ARN
}

// Touch records a resource the call read or wrote; the access recorder and the
// journal use it. Handlers know the exact ARN, so nothing is derived heuristically.
func (c *Call) Touch(arn aws.ARN)

type Origin interface{ origin() }
type External struct {
	Remote    netip.AddrPort
	UserAgent string
}
type Internal struct{ Cause journal.SpanID } // delivery, poller, scheduled task

// Endpoint is used to mint client-facing URLs: QueueUrl echoes the request Host
// (lambda-runtime-contract.md 6.2 option A), function URLs, SNS SigningCertURL.
type Endpoint struct {
	Scheme string
	Host   string // as received, port included
}
```

### 3.2 Operation handler signature

Generated per service from `codegen/services/<svc>.json`, which lists the implemented operations:

```go
// Code generated by understudy-gen from api-models-aws@1cfdfb4 sqs-2012-11-05.json; DO NOT EDIT.
package sqsapi

// Handlers is exactly the set of implemented operations. Listing an operation in
// codegen/services/sqs.json without implementing it, or implementing one that is
// not listed, is a compile error.
type Handlers interface {
	CreateQueue(ctx context.Context, c *op.Call, in CreateQueueInput) (CreateQueueOutput, error)
	SendMessage(ctx context.Context, c *op.Call, in SendMessageInput) (SendMessageOutput, error)
	// ...
}

// Bind adapts Handlers to the registry: decode, validate, call, encode.
func Bind(h Handlers) registry.Binding
```

Generated input and output types make absence explicit and required members unconditional:

```go
type SendMessageInput struct {
	QueueUrl               string            // @required: always present once decoded
	MessageBody            string            // @required
	DelaySeconds           opt.Option[int32] // NullableInteger: absent differs from 0 (sqs.md 6.1)
	MessageAttributes      map[string]MessageAttributeValue
	MessageDeduplicationId opt.Option[string]
	MessageGroupId         opt.Option[string]
}

func (v *SendMessageInput) Deserialize(d serde.Decoder) error   // presence bitset, defaults applied
func (v SendMessageInput) Validate(c *validate.Collector)        // length/range/pattern/enum + overlay
func (v SendMessageInput) Redact() journal.Fields                // `sensitive` members replaced
```

The generated adapter runs `Deserialize` then `Validate`; a missing required member becomes a `Violation` before the handler exists, so a handler can never see an input that AWS would have rejected for shape reasons. Output types use the same rule in reverse: `required` output members are plain values, and the overlay trait `dev.understudy#alwaysEmit` promotes optional output members that real clients cannot live without. `gap-G6.md` finding 1 lists four: `CreateDate` on `GetRole` and `GetInstanceProfile`, `LastChangedDate` on `DescribeSecret`, `CreatedDate` on `GetSecretValue`; when they are absent the Terraform provider panics. The compiler then forces every handler to supply them.

Effects (section 3.5) are returned through the `op.Call`-scoped outbox, not the signature, so the handler signature stays the AWS operation's signature.

### 3.3 Error model

```go
package awserr

type Fault uint8

const (
	Client Fault = iota // Query Type "Sender"
	Server              // Query Type "Receiver"
)

// Error is every failure that reaches the wire. Any other error escaping a
// handler is a bug: it is logged with its cause and rendered as InternalFailure 500.
type Error interface {
	error
	Identity() Identity
	awsError()
}

// Identity is everything a protocol writer needs. For modeled errors it is a
// generated constant: no protocol code computes it at runtime.
type Identity struct {
	Shape           smithy.ShapeID // com.amazonaws.sqs#QueueDoesNotExist
	JSONType        string         // __type: overlay namespace (awsJson1_0) or bare name (awsJson1_1)
	QueryCode       string         // awsQueryError.code, else shape name
	Status          int            // httpError, else 400 client / 500 server
	QueryModeStatus int            // awsQueryError.httpResponseCode, used when x-amzn-query-mode: true
	Fault           Fault
	MessageKey      MessageKey     // MessageLower | MessageUpper | NoMessage, from the shape
}

// The variants.
type Modeled struct { // generated error structs, including overlay-added shapes
	Value Shape
}
type Validation struct {
	Operation  registry.OperationID
	Violations []validate.Violation // rendered by the service's validation style and code overlay
}
type Framework struct { // protocol-level failures with per-service identity from overlays/common.json
	Kind    FrameworkKind // UnknownOperation, Serialization, UnsupportedMediaType, NotAcceptable,
	Message string        // MissingAuthenticationToken, SignatureDoesNotMatch, RequestEntityTooLarge, ...
}
type NotImplemented struct { // 501; message names the operation and points at `understudy coverage`
	Operation registry.OperationID
}
type Internal struct{ Cause error }
```

Generated error types read like the SDK's:

```go
// Code generated ...; DO NOT EDIT.
type QueueDoesNotExist struct{ Message opt.Option[string] }

func (QueueDoesNotExist) Identity() awserr.Identity { return identityQueueDoesNotExist } // constant
```

Rules encoded here rather than in each codec (`error-matrix.md` 7, D1 to D10 and H1 to H10):

- `__type` namespace is an overlay fact: SQS `com.amazonaws.sqs`, DynamoDB `com.amazonaws.dynamodb.v20120810`, CloudWatch `com.amazonaws.cloudwatch.v2010_08_01`; awsJson1_1 services use the bare shape name; framework errors use `com.amazon.coral.validate#` and `com.amazon.coral.service#`.
- Message member spelling is the shape's own (`Message` for Secrets Manager, `message` for KMS); Go v2 drops a differently cased message on modeled errors (`wire-json-query.md` Implications 11).
- Status comes from `httpError`. That satisfies both the Smithy case `QueryCompatibleAwsJson10CustomCodeError` (no `httpError`, so 400) and recorded SQS behavior (`PurgeQueueInProgress` 403, `sqs.md` finding 4), because the SQS model sets `httpError` equal to `awsQueryError.httpResponseCode`. CloudWatch's recorded 404 under `x-amzn-query-mode: true` (`error-matrix.md` 3) is the `QueryModeStatus` field.
- Unmodeled codes the services really send are added as overlay shapes, so they become generated, typed and listed in coverage: the 20 S3 codes absent from the model (with their context members, `BucketName`, `Key`, `UploadId`, `Condition`, `ArgumentName`, and so on: `wire-rest.md` 2.4), SQS `InvalidParameterValue` and `MissingParameter` (with `__type` `InvalidParameterValueException` and `MissingRequiredParameterException`), CloudWatch `ValidationError`, Lambda `RequestEntityTooLargeException`, and every service's generic vocabulary.
- Constraint-failure code is per operation (`error-matrix.md` finding 3): `dev.understudy#validationError` on the operation or service picks `ValidationException`, `ValidationError`, `InvalidParameterException`, `InvalidParameterValueException` or a hand-text style (SQS `Value X for parameter P is invalid. Reason: ...`, SNS `Invalid parameter: P Reason: ...`).

Validation violations are structured; text is rendered by a style:

```go
package validate

type Violation struct {
	Path       Path       // lowerCamel segments; list index 1-based; style decides indexed vs rolled-up
	Value      Rendered   // Java toString rendering (error-matrix.md 4, "Value rendering")
	Constraint Constraint
}

type Constraint interface{ constraint() }
type Required struct{}
type LengthMin struct{ N int64 }
type LengthMax struct{ N int64 }
type RangeMin struct{ N string } // decimal text as the model writes it
type RangeMax struct{ N string }
type Pattern struct{ Echo string } // model pattern minus one ^ and one $, overlay exceptions
type Enum struct{ Values []string }

type Style uint8

const (
	Coral           Style = iota // "1 validation error detected: Value 'x' at 'path' failed to satisfy constraint: ..."
	SmithyFramework              // fieldList envelope; used only by the synthetic protocol-test services
)
```

Having both styles is what lets one validator pass the Smithy `RestJsonValidation` corpus and match AWS's recorded Coral text.

### 3.4 Service registration

```go
package registry

type ServiceID string // "sqs"

type OperationID struct {
	Service ServiceID
	Name    string
}

// Service is a generated descriptor of the whole model, implemented or not.
type Service struct {
	ID               ServiceID
	Shape            smithy.ShapeID
	SigningName      string        // monitoring for CloudWatch
	IAMPrefix        string        // cloudwatch for CloudWatch (smithy-models.md 3.1)
	ARNNamespace     string
	Protocols        []protocol.ID // model protocols plus codegen/services extras (SQS awsQuery)
	JSONTargetPrefix string        // AmazonSQS, TrentService, GraniteServiceVersion20100801
	QueryVersion     string
	XMLNamespace     string        // overlay supplies SQS http://queue.amazonaws.com/doc/2012-11-05/
	QueryCompatible  bool
	Operations       []Operation   // all of them; sorted
}

type Operation struct {
	ID              OperationID
	Schema          *schema.Operation
	IAMActions      []iam.Action          // primary first (Service Reference, smithy-models.md 8)
	HTTP            opt.Option[schema.HTTP]
	MaxRequestBytes int64                 // overlay; defaults per protocol
	Readonly        bool
	Idempotent      bool
}

// Impl is either a bound handler or the loud default.
type Impl interface{ impl() }
type Implemented struct{ Invoke Invoke }
type Unimplemented struct{}

type Invoke func(ctx context.Context, c *op.Call, d serde.Decoder) (serde.Serializable, error)

type Binding struct {
	Service *Service
	Impls   map[string]Implemented
}

// New fails at boot if a binding names an operation absent from its model, or if
// a generated descriptor has no binding at all.
func New(bindings ...Binding) (*Registry, error)
```

Wiring is explicit in one place (`internal/server/wire.go`): construct each service with its dependencies, `Bind` it, pass all bindings to `registry.New`. No `init()` registration.

### 3.5 State store and concurrency model

Each service owns its state as hand-written domain types (not Smithy shapes), partitioned by `aws.StateKey`:

```go
package state

// Partitioned holds one S per partition. Each partition has its own mutex; a
// handler mutates exactly one partition per call. No lock is held across services.
type Partitioned[K comparable, S any] struct {
	mu    sync.Mutex
	parts map[K]*cell[S]
	zero  func() *S
}

type cell[S any] struct {
	mu sync.Mutex
	s  *S
}

// Update runs fn under the partition lock. Effects returned by fn are executed
// by the caller after the lock is released.
func (p *Partitioned[K, S]) Update(k K, fn func(*S) (Effects, error)) (Effects, error)
func (p *Partitioned[K, S]) Read(k K, fn func(*S))
func (p *Partitioned[K, S]) Keys() []K // sorted, for snapshots and resource listing

// Effects is the outbox: work that must happen after commit.
type Effects struct{ items []Effect }

type Effect interface{ effect() }
type Deliver struct{ Delivery delivery.Request }    // SNS fan-out, S3 notification, EventBridge target
type Schedule struct{ Task clock.Task }             // visibility expiry, secret purge, alarm tick
type Wake struct{ Key clock.WaitKey }              // long pollers, ESM pollers
```

Rules:

1. One partition lock per handler call; never call another service while holding it. Cross-service work is an `Effect`, executed after commit through `inproc` or `delivery`. This removes lock ordering as a concern and makes every cross-service hop a natural journal edge.
2. Blocking waits (SQS long poll, `Invoke` RequestResponse) never hold a lock. They use `clock.Wait` with a wake key (section 3.6), so `testing/synctest` bubbles can advance through them (`go-toolchain.md` 17.6).
3. Large bytes live outside the lock: S3 object bodies and Lambda code go to `blob` (content-addressed, under `os.Root`, keys never become paths). The partition commits only metadata pointing at a finished blob, so a PUT is atomic per key and conditional writes are evaluated under the same lock as the commit (`s3.md` Implications 8).
4. Determinism: every id that matters to behavior (message ids, receipt handles, version ids, request ids) comes from `ids.Source`, which is crypto-random by default and seedable for tests and snapshot restores. Iteration over maps for output always sorts.

Global serialization is rejected (it makes long polls and Lambda waits contend for no reason); per-resource actors are rejected (they make snapshots and multi-resource invariants, such as SNS subscription counters or SQS redrive-allow checks, harder for no fidelity gain).

### 3.6 The clock

Three time domains, each its own type, with the rule from `lambda-runtime-contract.md` 6.1: time a container, SDK signer or user code observes or supplies is wall time; time only the emulator observes is emulator time.

```go
package clock

// Instant is emulator time. It is deliberately not time.Time, so an emulator
// deadline cannot be compared with a wall timestamp by accident.
type Instant struct{ t time.Time }

func (i Instant) Add(d time.Duration) Instant
func (i Instant) Before(j Instant) bool
func (i Instant) Format(layout string) string // the one sanctioned way out, for wire fields

// Mode is the clock's state.
type Mode interface{ mode() }
type Running struct {
	Anchor Instant   // emulator time at WallAnchor
	WallAnchor time.Time
	Scale  Scale     // rational, > 0; 1/1 is real time
}
type Frozen struct{ At Instant }

type Clock struct{ /* mode, scheduler heap, waiters, wall source */ }

func (c *Clock) Now() Instant
func (c *Clock) Mode() Mode
func (c *Clock) Freeze()
func (c *Clock) Resume()
func (c *Clock) Advance(d time.Duration) (fired int, err error) // d >= 0
func (c *Clock) Set(to Instant) error                         // ErrClockRegression if to < Now()
func (c *Clock) SetScale(s Scale) error

// Schedule runs fn when emulator time reaches at. Tasks fire in (at, seq) order
// on a single scheduler goroutine, so Advance is deterministic.
func (c *Clock) Schedule(at Instant, key TaskKey, fn func(now Instant)) TaskHandle

// Wait blocks until emulator time reaches until, the key is woken, ctx ends, or
// the wall safety cap elapses. The cap equals the requested wait in wall time,
// so a frozen clock never holds an HTTP request longer than AWS would
// (sqs.md 13.11).
func (c *Clock) Wait(ctx context.Context, until Instant, key WaitKey, wallCap time.Duration) WaitResult
func (c *Clock) Wake(key WaitKey)

// Wall and Mono are the other two domains, injected so tests can control them.
type Wall interface{ Now() time.Time }
type Mono interface{ Since(start MonoTime) time.Duration; Start() MonoTime }
```

Backward `Set` is refused: deadlines already committed (visibility, retention, deletion windows) would otherwise run in reverse. `Advance` fires every due task in order, including tasks scheduled by tasks, before returning, and wakes all waiters once.

Which behavior uses which domain (condensed from `lambda-runtime-contract.md` section 3):

| Domain | Behaviors |
|---|---|
| Emulator | SQS visibility, delay, retention, dedup window, purge cooldown; ESM batching window; Lambda async retry delays and max event age; Lambda state transitions; EventBridge schedules; DynamoDB TTL sweep; secret deletion windows; KMS deletion; log retention sweep; S3 `Last-Modified`; presigned URL expiry comparison; SNS delivery retries; `LastModified` fields |
| Wall | SigV4 skew checks; the `Date` response header (always real, or Go retries with a bogus offset); `Lambda-Runtime-Deadline-Ms`; STS `Credentials.Expiration`; structured logs |
| Monotonic | Lambda timeout enforcement, `Duration`/`Billed Duration`/`Init Duration`, init limit, container idle reaping |
| Wall OR emulator | `PutLogEvents` 2-hour/14-day acceptance window (accept if inside either) |

`forbidigo` bans `time.Now`, `time.Sleep`, timers and tickers outside `internal/clock`.

### 3.7 The journal

```go
package journal

type EventID struct{ /* 128-bit, from ids.Source */ }
type TraceID struct{ /* root cause id; a client call starts a trace */ }

type Event struct {
	ID        EventID
	Trace     TraceID
	Parent    opt.Option[EventID] // single causal parent
	Causes    []EventID           // fan-in: an ESM invocation caused by N SQS messages
	At        clock.Instant
	Wall      time.Time
	Scope     aws.Scope
	Principal identity.Principal
	Kind      Kind
}

type Kind interface{ kind() }

type APICall struct {
	Operation registry.OperationID
	RequestID ids.RequestID
	Protocol  protocol.ID
	Origin    op.Origin
	Status    int
	Error     opt.Option[awserr.Identity]
	Input     Fields     // generated Redact(); sensitive members never stored
	Resources []aws.ARN
	Elapsed   time.Duration
}
type Delivery struct {
	Source  aws.ARN
	Target  delivery.Target
	Attempt int
	Outcome DeliveryOutcome // Delivered | Filtered{rule} | Retrying{next clock.Instant} | DeadLettered{dlq} | Dropped{reason}
}
type Invocation struct {
	Function aws.ARN
	Version  string
	Type     InvocationType // RequestResponse | Event | DryRun
	Outcome  InvocationOutcome // Succeeded | FunctionError{kind} | Timeout | Throttled
	Duration time.Duration  // monotonic
	ColdStart bool
}
type FaultApplied struct{ Rule faults.RuleID; Action faults.Action }
type StateTransition struct{ Resource aws.ARN; From, To string }
type ClockChanged struct{ From, To clock.Mode }
```

Spans travel in `context.Context`; `op.Call.Span` is the parent of everything a handler causes, and `Effects` carry it into deliveries and scheduled tasks. Storage is a bounded ring (default 100,000 events) plus an optional JSONL sink; query filters are service, operation, resource ARN prefix, trace, principal, error code, kind and emulator-time range. The stream is Server-Sent Events. Redaction is generated from the `sensitive` trait plus overlay additions (SigV4 `Authorization`, `X-Amz-Security-Token`, SQS bodies are kept, secret values are not), so the journal cannot leak what slog cannot.

The journal is also a test oracle: conformance and e2e tests assert causal chains (`PutObject -> S3 notification -> SQS SendMessage -> ESM ReceiveMessage -> Invoke -> DeleteMessageBatch`) instead of sleeping.

### 3.8 Fault injection hooks

```go
package faults

type Rule struct {
	ID     RuleID
	Match  Matcher
	Action Action
	Budget Budget
	Seed   uint64 // per-rule PCG stream; same seed, same decisions
}

type Matcher struct {
	Services   []registry.ServiceID   // empty = any
	Operations []registry.OperationID
	Resource   opt.Option[ARNPattern] // glob over the ARNs the call Touches
	Accounts   []aws.AccountID
	Regions    []aws.Region
	Origin     OriginFilter           // Any | ExternalOnly | InternalOnly
}

type Action interface{ action() }
type Error struct{ Shape smithy.ShapeID; Message opt.Option[string] } // validated against the op's modeled + overlay errors
type Throttle struct{}                                               // service's own throttle identity (overlay)
type Latency struct{ Wall time.Duration }                            // wall, so a frozen clock cannot hang it
type PostHeaderError struct{ Shape smithy.ShapeID }                  // S3 200-with-error (wire-rest.md 3.7)

type Budget interface{ budget() }
type Always struct{}
type NextN struct{ N int }
type Probability struct{ P float64 }
```

Hook points: pipeline step 8 (ingress, after decode so operation and resource are known), step 10 (egress), `inproc` (so deliveries and pollers are faultable, for example "the third SQS SendMessage from SNS fails with KMS.ThrottlingException"), and the DynamoDB proxy before forwarding.

Fidelity rules: a rule is rejected at creation if its error shape is not in the operation's generated error list or the overlay's common errors; `Throttle` resolves to the service's real throttle identity from the overlay (SQS `RequestThrottled` 403, DynamoDB `ProvisionedThroughputExceededException`, Lambda `TooManyRequestsException` 429, S3 `SlowDown` 503). The admin API warns when a rule's error is in the SDK retry set, because Terraform will retry it up to 25 times before seeing it (`tf-compute.md` 1.3).

### 3.9 State snapshot format

A snapshot is one deterministic file: same state and same understudy version give the same bytes.

```
<name>.usnap  (ustar; entries sorted; mtime 0; uid/gid 0; no wall timestamps anywhere)
  manifest.json
  clock.json                   mode, emulator time, scale; pending scheduler tasks are NOT stored
  ids.json                     id source state (seeded mode only)
  faults.json                  rules with seeds and remaining budgets
  services/<svc>.json          one section per service, own formatVersion
  dynamodb/<account>/<region>/<table>.json   DescribeTable + paged Scan replay dump
  blobs/<sha256>               S3 object bytes, Lambda code zips, image references
```

```json
{
  "format": "understudy-snapshot",
  "formatVersion": 1,
  "understudyVersion": "0.1.0",
  "models": {"api-models-aws": "1cfdfb4bb9ea858e65623e66d73af04bec353201"},
  "emulatorTime": "2026-01-01T00:00:00.000Z",
  "sections": [
    {"name": "sqs", "formatVersion": 1, "path": "services/sqs.json", "sha256": "..."}
  ]
}
```

Rules:

- JSON via `jsonx` with deterministic output (`go-toolchain.md` 17.2). Each service section has its own version and a migration chain; a section from a newer binary is refused with an error that names both versions (MiniStack's downgrade guard, `landscape.md` 8).
- Scheduler tasks are not serialized. Each service implements `Rearm(now clock.Instant)` that rebuilds tasks from state on load: visibility expiries, ESM pollers, EventBridge `nextFireAt`, TTL sweeps, secret purges, pending deliveries (`landscape.md` Implications 9; MiniStack #889 is the bug this prevents). A restart test asserts every re-armable thing fires after load.
- DynamoDB Local's SQLite files are never copied while running; the snapshot is a replay dump through the proxy (`dynamodb-local.md` Implications 11).
- Persistence across restarts (opt-in, `--state-dir`) is the same format: load at start, save on graceful shutdown and on a configurable wall interval, written to a temp file and renamed. S3 blobs live in the state directory's blob store directly, so periodic saves do not copy bytes.

---

## 4. The code generator pipeline

### 4.1 Inputs

| Input | Location | Pin and integrity |
|---|---|---|
| 12 AWS service models, Smithy 2.0 JSON AST, verbatim | `third_party/api-models-aws/` | commit `1cfdfb4`, `UPSTREAM.json` with git blob SHA-1 and sha256 per file; `cmd/vendor -verify` offline (`smithy-models.md` 10) |
| Smithy protocol compliance corpus, flattened AST | `third_party/smithy-protocol-tests/model.json` | Smithy 1.74.0; `PIN.json` with six jar sha256 values and the Smithy CLI sha256; regenerated by hand with `smithy ast --no-config --flatten` (`smithy-protocol-tests.md` 5) |
| Operation to IAM action table, derived | `third_party/aws-service-reference/actions.json` | provenance per service (`Version`, `modified`, body sha256); raw Service Reference JSON is not vendored until its licence is settled (`smithy-models.md` 8) |
| Overlays | `codegen/overlays/*.json` | hand-owned, reviewed; every entry carries `dev.understudy#evidence` |
| Service generation config | `codegen/services/*.json` | hand-owned |

Network is used only by `cmd/vendor` when a human bumps a pin. `go generate` and every test are offline.

### 4.2 Overlay format

Overlays are Smithy JSON AST fragments: `apply` entries for traits on existing shapes and members, new `structure` shapes for unmodeled errors, plus traits in `dev.understudy#`. Example:

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

`dev.understudy#` traits (the complete phase 1 set; the generator fails on anything else in that namespace):

| Trait | On | Meaning |
|---|---|---|
| `evidence` | any overlay entry | `{label, source, validated?}`; required |
| `errorNamespace` | service | awsJson1_0 `__type` namespace |
| `jsonType` | error shape | `__type` override for unmodeled names |
| `validationError` | service or operation | constraint-failure code and style |
| `validationPathStyle` | service | `indexed` (`targets.1.member.id`) or `rolledUp` (Lambda) |
| `patternEcho` | string shape | echo text when it is not "pattern minus anchors" |
| `patternValidator` | string shape | name of a hand validator for patterns RE2 cannot compile (7 lookarounds) |
| `alwaysEmit` | output member | promote to required in the generated output type |
| `maxRequestBytes` | operation | body cap (Lambda `CreateFunction` 70,167,211; SQS above the encoded 1 MiB batch) |
| `throttleError` | service | the identity `faults.Throttle` uses |
| `notConfigured` | S3 Get* operation | error shape or default document when the subresource is unset |
| `malformedRequestError` | service | identity for undecodable bodies (Lambda `InvalidRequestContentException`, DynamoDB `SerializationException`, test services `SerializationException`) |
| `statusOverride` | error shape | KMS documented 400/400/500 where the model says 404/409/503 (`iam-sts-kms.md` Implications 3) |
| `semanticSwitch` | service | names a documented behavior toggle (section 10.5) |

Overlays are applied after loading and before validation of the merged model. Applying a trait that the vendored model already has with a different value is an error unless the overlay entry says `"replace": true`, so a model bump that fixes a gap surfaces as a failing generate, not as a silent double definition.

### 4.3 Service generation config

```json
{
  "service": "com.amazonaws.sqs#AmazonSQS",
  "package": "sqsapi",
  "extraProtocols": ["aws.protocols#awsQuery"],
  "implemented": ["CreateQueue", "DeleteQueue", "GetQueueUrl", "SendMessage", "ReceiveMessage", "..."],
  "storedOnly": ["AddPermission", "RemovePermission"]
}
```

`implemented` decides which operations get types, serde and a `Handlers` method. Every model operation gets a registry descriptor and routing entry regardless, so unimplemented ones are recognized and answered with `NotImplemented` 501 rather than "unknown operation". `storedOnly` is a coverage annotation for operations whose state is kept and echoed but not acted on (queue policies, topic policies, Lambda concurrency settings); the brief requires that distinction to be visible.

### 4.4 Stages

```
load        encoding/json with UseNumber; reject unknown shape types, mixins, apply, set
            (smithy-models.md 10 checklist); the corpus AST loads through the same loader
overlay     merge codegen/overlays; reject conflicts and unknown dev.understudy traits
closure     walk service -> operations -> resources (lifecycle, operations,
            collectionOperations, nested resources: Lambda's 72 resource-bound ops)
classify    every trait ID is honor | generation-only | ignore (smithy-models.md 4.3);
            an unclassified trait fails the build
effective   member traits merged over target traits
regex       ECMA-262 -> RE2: \uXXXX -> \x{XXXX}, \p{Print} -> [[:print:]], drop lone
            surrogates; lookarounds require a dev.understudy#patternValidator; every
            emitted pattern must compile
IR          typed IR: ServiceIR, OperationIR, ShapeIR, MemberIR, ErrorIR, HTTPBindingIR
emit        per service package (below); go/format; sorted iteration; temp file + rename
```

### 4.5 Outputs per service (`internal/api/<svc>api/`)

| File | Content |
|---|---|
| `service_gen.go` | `registry.Service` descriptor for every operation in the model |
| `types_gen.go` | structs, enums (string type + constants + `Known()`), unions (sealed interfaces), error structs, for the implemented closure |
| `schema_gen.go` | static `schema.Shape`/`schema.Member` values: wire names, xmlName/xmlFlattened/xmlNamespace, timestampFormat, HTTP bindings, defaults |
| `serde_gen.go` | `Serialize(serde.Encoder)` and `Deserialize(serde.Decoder)` for every type in the closure, both directions on inputs, outputs and errors |
| `validate_gen.go` | constraint checks emitting structured `Violation`s, in AWS's per-member order (pattern, then max length, `error-matrix.md` 4) |
| `errors_gen.go` | `Identity()` constants per error shape, per-operation error lists (for fault validation and coverage) |
| `handlers_gen.go` | `Handlers` interface and `Bind` adapter |
| `routes_gen.go` | REST only: Lambda route trie ordered by Smithy specificity routing; S3 dispatch table `(method, scope, query-key presence, required header)` ordered by specificity, `x-id` stripped (`wire-rest.md` Implications 2) |
| `iam_gen.go` | operation -> authorized actions, primary action first |
| `redact_gen.go` | `Redact()` per input type from `sensitive` |
| `meta_gen.go` | `idempotencyToken`, `paginated`, `httpChecksum`, `requestCompression`, `readonly`, `idempotent` facts as data for the shared helpers |
| `examples_gen_test.go` | each model `examples` input decoded and validated: an over-strict overlay or a bad regex translation fails here (260 examples across 7 services) |

Hand-written runtime that the generated code targets: `schema`, `serde`, `validate`, `awserr`, `opt`, the codecs, `restbind`, pagination tokens, idempotency, checksums. Hand-written service logic: everything in `internal/service/*`. The split, stated as a rule: **if the answer is a function of the model plus overlay, it is generated; if it depends on state or time, it is hand-written.**

### 4.6 Protocol-test generation

The same generator runs over the corpus AST and emits `internal/protocoltest/gen/<testsvc>/`: types, schema, serde and handlers for the synthetic services (`JsonRpc10`, `JsonProtocol`, `AwsQuery`, `RestJson`, `RestJsonValidation`, `RestXml`, `RestXmlWithNamespace`, the S3 slice, `RpcV2Protocol`), plus `cases_gen_test.go` with every server-runnable case as a Go literal whose expected `params` is a typed value, the way aws-sdk-go-v2 generates its client tests (`smithy-protocol-tests.md` 4.1). The corpus uses shape kinds the 12 service models do not (documents, `byte`, `short`, `float`, `bigInteger`, `intEnum`, sparse collections); the generator supports them because the corpus needs them, which also means a future model using them is already handled.

Generated test handlers record the input they receive and return the case's response `params`; the harness (hand-written, `internal/protocoltest`) follows smithy-rs's semantics (`smithy-protocol-tests.md` 4.2): run `appliesTo` absent or `server`; request tests assert the decoded input equals `params`; response tests assert status, `headers`, `forbidHeaders`, `requireHeaders` and media-type-aware body equality; malformed tests assert the handler is not entered and the response matches. Parameterized malformed cases are expanded with `_case<i>` ids and `$`-substitution exactly as specified, including the `$$` rule.

### 4.7 Determinism and drift

- Header on line 1: `// Code generated by understudy-gen from <input@pin>; DO NOT EDIT.`; no timestamps or paths.
- `just generate` runs `go run ./internal/cmd/vendor -verify` then `go generate ./...`. CI runs it and `git diff --exit-code` plus `git status --porcelain`.
- `docs/coverage.md` is regenerated by the same recipe and drift-checked.
- A generator golden test feeds small hand-written model fragments through every stage and compares output, so generator changes are reviewed as diffs of small files.

### 4.8 Recipe: adding an operation

1. Add the name to `codegen/services/<svc>.json` `implemented`.
2. Add overlay entries if research shows AWS enforces more than the model (with evidence).
3. `just generate`. The build now fails until `Handlers` has the method.
4. Implement the method; add semantic tests and, where evidence exists, a parity recording.
5. `understudy coverage` shows the operation as implemented with its test and recording counts.

---

## 5. Protocol implementations

### 5.1 Shared serde

```go
package serde

// Encoder is implemented once per wire format; generated Serialize methods drive it.
type Encoder interface {
	BeginStruct(s *schema.Shape)
	EndStruct()
	BeginList(m *schema.Member, n int)
	EndList()
	BeginMap(m *schema.Member, n int)
	EndMap()
	String(m *schema.Member, v string)
	Bool(m *schema.Member, v bool)
	Int64(m *schema.Member, v int64)
	Float64(m *schema.Member, v float64)
	Timestamp(m *schema.Member, v time.Time) // format from the member's effective trait and protocol default
	Blob(m *schema.Member, v []byte)
	Document(m *schema.Member, v document.Value)
	Null(m *schema.Member)
}

type Decoder interface {
	// mirror of Encoder; ReadStructMember returns the next member present on the wire
}

type Serializable interface{ Serialize(Encoder) }
```

REST protocols put a binding layer in front: `restbind` splits a request into label, query, header, prefix-header and payload sources according to `schema.HTTP` and feeds the body codec only the unbound members. Codecs are lenient on input and exact on output (`wire-rest.md` Implications 9).

### 5.2 Front door claiming

Order, first match wins (`wire-json-query.md` Implications 1, `wire-rest.md` 5, `landscape.md` 3.1 and Traps):

1. Path prefix `/_understudy/` goes to the admin API and emulator-owned AWS-visible URLs (SNS certificates and unsubscribe links, the function URL path fallback). Bucket names cannot start with `_`, so this cannot shadow S3.
2. Host `<url-id>.lambda-url.<region>.<base>` goes to the function URL data plane.
3. Parse auth from `Authorization` or the presigned query (`X-Amz-Credential`, SigV2 `AWSAccessKeyId`). The credential-scope service is the router key from here on.
4. `Smithy-Protocol: rpc-v2-cbor` and a `/service/<Name>/operation/<Op>` path: CBOR. Reject if `X-Amz-Target` is also present.
5. `X-Amz-Target`: awsJson; service from the target prefix table, cross-checked against the signing name.
6. Form media type (parameters ignored) or a GET with `Action`: awsQuery; service from `(Action, Version)`, unique across SQS, SNS, IAM, STS and CloudWatch. SQS legacy Query is also accepted on `/<account>/<queue>`.
7. REST: signing name `s3` with path `/v20180820/` is S3 Control tagging; `s3` otherwise is S3; `lambda` is Lambda. Unsigned: a Host matching a configured S3 base domain with a bucket label is S3 virtual-hosted; a `^/\d{4}-\d{2}-\d{2}/` path is Lambda only when no bucket of that name exists; anything else is S3 path-style.
8. Nothing claimed: 400 with a body that says which signals were missing.

AWS dispatch is done on the raw request target, never through `http.ServeMux`, which redirects `.`, `..` and `//` paths and would corrupt S3 keys (`smithy-protocol-tests.md` 7.1, `go-toolchain.md` 5).

### 5.3 awsJson 1.0 and 1.1 (SQS, DynamoDB, CloudWatch JSON; Logs, Secrets Manager, KMS, EventBridge)

- Accept `application/x-amz-json-1.x` with parameters, empty body or `{}`, unknown members, `null` members, gzip for `PutMetricData` (`requestCompression`).
- Emit integers as plain digits, timestamps as epoch-second numbers with millisecond precision, padded base64 blobs, `{}` for any modeled output, omit unset members (`wire-json-query.md` Implications 6, 7).
- Errors: body `__type` always (boto3 reads nothing else), message under the shape's own key, status per section 3.3; `x-amzn-query-error: <code>;<Sender|Receiver>` for every error with `awsQueryError` on query-compatible services (SQS, CloudWatch), regardless of whether the request sent `x-amzn-query-mode`.
- Built on `encoding/json/v2` through `codec/jsonx` only.

### 5.4 awsQuery (SNS, IAM, STS, CloudWatch Query, SQS legacy)

- Decoder sorts indexed keys numerically (Go sends `member.10` before `member.2`), handles `.member.N`, `.entry.N.key|value`, flattened `Name.N`, empty-value-means-empty-list, both `date-time` forms.
- Encoder always writes `<OpResponse xmlns=...><OpResult>...</OpResult><ResponseMetadata><RequestId/></ResponseMetadata></OpResponse>`, the `Result` element whenever the operation has an output shape, the envelope even for `Unit` output (boto3 raises on an empty body), lowercase booleans, millisecond timestamps, and percent-encoded IAM policy documents (RFC 3986 unreserved only; `+` as `%2B`).
- SQS legacy Query is the same codec over the SQS shapes, driven by the retained `xmlName`/`xmlFlattened` traits and the overlay's namespace and `AttributeName.<n>` collision rule (`sqs.md` 13.2). Its `NonExistentQueue` message keeps the `...for this wsdl version.` text.
- Errors: `ErrorResponse/Error/{Type,Code,Message}` + `RequestId`; `Code` from `awsQueryError`; status from `awsQueryError.httpResponseCode`. SNS has no `xmlns` on `ErrorResponse`, CloudWatch has one (`error-matrix.md` 3); that is an overlay fact.
- No reference server runner exists for awsQuery (`smithy-protocol-tests.md` 4.2), so the 35 + 31 `both` cases plus SDK conformance suites are the proof.

### 5.5 restJson1 (Lambda)

- Route trie generated from 88 `http` traits across 17 date prefixes, ordered by Smithy specificity routing.
- `X-Amzn-Errortype: <Shape>` and a body with the model's exact keys (`Type`, `Message` or `message`); `Type: "User"` on 4xx shapes that have it; `retryAfterSeconds` to `Retry-After`.
- `httpResponseCode` members computed by the handler (`Invoke` 200/202/204). Event source mapping create/update/delete return 202; most other creates 201.
- `Invoke` accepts any `Content-Type` and an empty body; the payload is opaque bytes.
- Malformed bodies answer `InvalidRequestContentException` (overlay `malformedRequestError`); the Smithy malformed corpus runs against the synthetic `RestJson` service, whose overlay says `SerializationException`, so one codec passes both.

### 5.6 restXml (S3 and S3 Control tags)

- Dispatch from the generated table; subresource keys tested by presence (`?uploads` and `?uploads=` are the same); `x-id` ignored, never required.
- Greedy `{Key+}` percent-decoded once with `/` preserved and dot segments kept verbatim (`S3PreservesLeadingDotSegmentInUriLabel`).
- Own XML writer (`codec/xmlw`): `xmlns` on success roots, `xsi:type` grantees, flattened lists, exact declaration, SDK escape table; `encoding/xml` only for parsing requests, matching on local names.
- Errors: `<Error><Code/><Message/>{context elements}<RequestId/><HostId/></Error>` with no wrapper; `HEAD` errors status-only; `x-amz-request-id`, `x-amz-id-2`, exactly one `Date` header (the JS SDK breaks on two, `s3.md` finding 7).
- `GetBucketLocation` unwrapped output (`s3UnwrappedXmlOutput`).
- The 200-with-error path for `CompleteMultipartUpload`, `CopyObject`, `UploadPartCopy` exists in the encoder so faults and real failures can use it.
- S3 Control `GET|POST|DELETE /v20180820/tags/{arn}` on any `<account>.<host>` Host, generated from the S3 Control model slice (three operations), sharing the bucket tag store (`tf-storage.md` Implications 1).

### 5.7 rpcv2Cbor (CloudWatch from aws-sdk-go-v2, so Terraform)

- Claim only with `Smithy-Protocol: rpc-v2-cbor`, `POST`, `/service/{GraniteServiceVersion20100801 | com.amazonaws.cloudwatch.GraniteServiceVersion20100801}/operation/{Op}` (any prefix); reject `X-Amz-Target`.
- `codec/cbor`: the ~340-line subset of `wire-cbor.md` 7; decoder accepts indefinite lengths, half floats, non-minimal widths; encoder emits definite minimal forms; timestamps as tag 1.
- `Unit` output gets an empty body (any member panics the Go client); errors always carry `__type` (absolute shape id) and the modeled message key; `x-amzn-query-error` when the shape has `awsQueryError`; `Smithy-Protocol` on every response.
- `DeleteAlarms` of a missing alarm succeeds (`wire-cbor.md` finding 4: Terraform's destroy accepts only `ResourceNotFoundException`).
- Gate: the 68 server-runnable CBOR cases plus the G01 to G24 golden vectors.

### 5.8 Unknown and unimplemented operations

| Situation | Answer |
|---|---|
| Operation not in the model | JSON `UnknownOperationException` 400 (recorded for DynamoDB by dynalite; other JSON services UNVERIFIED, R11); Query `InvalidAction` 400 `The action X is not valid for this endpoint.` (recorded for SQS; other Query services reuse it until R11); REST no route: S3 `NotImplemented` 501, Lambda `UnknownOperationException` 404 (UNVERIFIED, `wire-rest.md` Implications 15). Each answer is an overlay entry with its evidence label |
| In the model, not implemented | 501 in the protocol's native envelope; code `NotImplemented` (S3's real code; reused everywhere so one string identifies the condition); message `understudy does not implement <Service>.<Operation>; run 'understudy coverage' to see what is supported` |

501 is outside the retry set of Go, botocore and JS, so SDKs fail fast with the message visible (`wire-json-query.md` Implications 15). The exact code string is an ADR item; this proposal picks `NotImplemented`.

### 5.9 Proof targets

| Protocol | Server-runnable cases (computed, `smithy-protocol-tests.md` 3) |
|---|---|
| awsJson1_0 | 26 request + 23 response |
| awsJson1_1 | 58 + 45 |
| awsQuery | 35 + 31 |
| restJson1 | 137 + 92, plus 655 expanded malformed cases |
| restXml | 101 + 77 (13 S3 cases; addressing cases are client-only) |
| rpcv2Cbor | 39 + 29 |

The skip list (`internal/protocoltest/skips.json`) is seeded from smithy-rs's `ExpectFail`, `BrokenTests` and `DisabledTests` plus host/`resolvedHost` cases; every entry has a reason and a category (`corpus-bug`, `client-only`, `aws-differs` with evidence). Pass, skip and fail counts per protocol go into the coverage report.

---

## 6. Identity, accounts, regions and auth

- **Account from access key** (`iam-sts-kms.md` Implications 6, `sigv4-checksums-endpoints.md` Implications 14): a 12-digit numeric key is the account (LocalStack convention, works with DynamoDB Local's alphanumeric rule); a key minted by understudy (present in the IAM key table) maps to its account and principal; anything else, including `test` and documentation keys, maps to the default account `000000000000`. Real-looking `AKIA`/`ASIA` keys that understudy did not mint are not decoded into foreign account numbers; the server logs one warning per key that a real-looking credential was presented.
- **Minting**: `AKIA`/`ASIA` + 16 base32 characters with the account in the 48-bit window, so the bit-mask decode round-trips; secrets 40 characters; session tokens opaque.
- **Region** from the credential scope; validated against the region table (configurable additions). IAM and STS accept any region in the scope; their state is `aws.Global`.
- **SigV4**: always parsed (routing and identity need it); verification opt-in. The verifier is a pure function over a normalized request with the payload hash as a tagged value (`Hex | Unsigned | StreamingUnsignedTrailer | StreamingSigned | StreamingSignedTrailer | NotSent`); `aws-chunked` decoding has one streaming decoder with four modes and is used whether or not verification is on. Skew checks use wall time; presign expiry uses the emulator clock.
- **SigV2 presigned S3 URLs** are supported because boto3 emits them by default in several regions.

## 7. S3 addressing and the DNS story

**Decision: no DNS server in phase 1.** Offline, rootless and Docker-Desktop-safe matter more than covering every hostname. The story has four parts:

1. **Host side.** Recommend `AWS_ENDPOINT_URL=http://localhost:4566`. `*.localhost` resolves to loopback on glibc/systemd-resolved hosts (measured, `landscape.md` 4.3), which covers Go v2 and JS v3 virtual-hosted requests (`bucket.localhost:4566`) and the Terraform S3 Control tag read (`<account>.localhost:4566`). `127.0.0.1` forces path style for every SDK but breaks the S3 Control tag read (`tf-storage.md` 1.1), so it is documented as the fallback with `skip_requesting_account_id`. `understudy env` prints the exact variables per SDK and Terraform; `understudy doctor` tests resolution of `x.localhost` and says what to do when it fails (macOS and Windows behavior is UNVERIFIED).
2. **Virtual-host detection is configuration.** A request is virtual-hosted iff the Host (port stripped) ends in `.` + a configured base domain (default `localhost`, `s3.localhost`, `s3.<region>.localhost`, and the AWS names `s3.<region>.amazonaws.com`) with a DNS-valid bucket label in front, and the credential scope is `s3` or absent. Requests signed for other services on such hosts are never captured (Floci's guard, `landscape.md` 3.3).
3. **Inside Lambda containers.** `AWS_ENDPOINT_URL` is an IPv4 literal of the container-reachable address, which forces path style in Go and JS and needs no DNS; on Linux and Podman, `ExtraHosts: ["localhost:host-gateway"]` plus a listener on the gateway address at the main port makes `localhost:4566` URLs that leaked in from the host (Terraform outputs, message bodies) work too (`lambda-runtime-contract.md` 6.2, options A, B and D).
4. **Function URLs** are returned as `http://<url-id>.lambda-url.<region>.localhost:4566/` (host-header routed; the provider derives `url_id` from the first label), with a path fallback `/_understudy/lambda-url/<url-id>/...` for clients that cannot resolve it.

An opt-in stdlib DNS responder for containers (`HostConfig.Dns`) is a phase 2 candidate behind an ADR.

**Bucket namespace.** Bucket names are global across accounts, as in AWS (`409 BucketAlreadyExists` across accounts). Each bucket has a home region. **Default: AWS behavior for cross-region access** (the redirect or `AuthorizationHeaderMalformed` with the region that AWS returns; `x-amz-bucket-region` on `HeadBucket` and bucket-addressed errors), because a test that passes locally and fails in a real account on a region mismatch is the failure this project exists to prevent. `s3.crossRegion: lenient` serves the bucket regardless. `IllegalLocationConstraintException` is only ever returned from `CreateBucket` (it triggers boto3's redirect logic elsewhere, `wire-rest.md` Implications 11).

**HTTPS.** An opt-in TLS listener with a generated self-signed certificate (SANs `localhost`, `*.localhost`, `127.0.0.1`) exists because aws-sdk-go-v2 refuses unseekable `PutObject` bodies over plain HTTP; `understudy env --tls` prints the CA-bundle variables per SDK.

## 8. Lambda execution and event source mappings

### 8.1 Execution: understudy hosts the Runtime API

Deviation from the brief's wording, for an ADR: functions run in AWS's official base images, but understudy serves the Runtime API itself and sets `AWS_LAMBDA_RUNTIME_API`, so `/lambda-entrypoint.sh` execs the runtime directly instead of starting the RIE. Evidence: the RIE never sets `X-Amz-Function-Error`, returns timeouts as a 200 text body, hard-codes the invoked ARN to `us-east-1:012345678912`, fakes memory, and panics on a second concurrent invoke (`containers.md` finding 1, `lambda-api.md` findings 1 to 3). LocalStack and Floci converged on the same design. Images built `FROM` non-AWS bases (no RIE at all) also work this way.

Packages:

| Package | Owns |
|---|---|
| `docker` | Engine API over the unix socket: version negotiation (min 1.41, target 1.44), image pull with progress, container create/start/wait/remove, logs with the multiplexed frame decoder, events, networks; typed `Error` ADT from statuses; endpoint discovery `DOCKER_HOST`, context, well-known sockets (Podman included) |
| `lambda/launcher` | builds the create body from function config: `Mounts` (never `Binds`), read-only `/var/task`, writable `/tmp` sized from `EphemeralStorage`, env (section 9.4 of `lambda-api.md`), `Memory`=`MemorySwap`, `PidsLimit`, labels |
| `lambda/runtimeapi` | one `net/http` server per environment on its own ephemeral port bound to the gateway address; `2018-06-01` endpoints; always `Content-Type` on `/next` (Python RIC segfaults without it); chunked bodies and trailers (aws-lambda-go errors); `Lambda-Runtime-Deadline-Ms` = wall dispatch + timeout |
| `lambda/pool` | environment state machine and reservation |
| `lambda/codestore` | zip extraction once per `CodeSha256` into a content-addressed dir under `os.Root`: reject symlinks and non-`IsLocal` names, cap declared and actual sizes, keep mode bits; `//go:debug zipinsecurepath=0` as a second layer |
| `lambda/furl` | function URL payload v2.0 request builder and response inference |
| `lambda/esm` | event source mapping control plane and the SQS poller |

Environment states (illegal combinations unrepresentable):

```go
package pool

type EnvState interface{ envState() }

type Starting struct{ Since clock.MonoTime }
type Idle struct{ Since clock.MonoTime }
type Busy struct{ Invocation InvocationID; Deadline time.Time } // wall deadline as sent to the RIC
type Draining struct{}
type Faulted struct{ Reason FaultReason } // InitError | ExitError{code} | OOM | Timeout
type Gone struct{}

// Key is a hash of every field that appears in the container spec; any config or
// code update bumps the function generation, draining idle environments at once.
type Key struct {
	Function   aws.ARN
	Version    string
	Generation uint64
	SpecHash   [32]byte
}
```

Invoke path: concurrency check (reserved concurrency stored and enforced; 429 `TooManyRequestsException` when exhausted) -> reserve an `Idle` environment or cold-start one (start semaphore default 16) -> deliver on `/next` -> wait for `/response`, `/error`, process exit or monotonic timeout -> synthesize the AWS response: `X-Amz-Function-Error: Unhandled`, `X-Amz-Executed-Version`, `X-Amz-Log-Result` (last 4 KB, base64), `Sandbox.Timedout` text, `Runtime.ExitError` -> release or condemn. One in-flight invocation per environment; concurrency scales by environments. Timeouts and durations never read the emulator clock.

Logs: one `logs?follow=1` stream per environment, demultiplexed, split into lines, written through `inproc` to CloudWatch Logs (`/aws/lambda/<name>` created on first invocation, stream `YYYY/MM/DD/[$LATEST]<32 hex>`, a format known only from third-party fixtures, `lambda-api.md` 9.7), with START/END/REPORT synthesized and a settle barrier before END so the last line lands in `LogType: Tail` (`containers.md` finding 9).

Credentials inside functions: STS mints execution-role credentials for the function's role (`AssumedRole` principal, session name = function name), injected as env vars, so calls from inside the function are attributed to the role by the access recorder. IMDS is disabled.

Networking: a gateway listener (bridge gateway IP, main port, serving the AWS API) plus per-environment Runtime API listeners on the same IP. Neither binds `0.0.0.0`. On Docker Desktop no `ExtraHosts` override is added and the address is the one `host.docker.internal` resolves to inside a probe container (UNVERIFIED; first-class risk below). The startup self-check logs the chosen topology; the first-invoke timeout error names the address and the likely causes.

Function state machine on the emulator clock: `CreateFunction` returns `Pending`/`Creating`; a scheduled task flips it to `Active` after a configurable delay (default 0, so the next read is `Active`; Terraform's own 5 s waiter delay is the floor anyway). Updates go `InProgress` -> `Successful`; conflicting calls in between get the exact `ResourceConflictException`. Image functions stay `Pending` until the image is present locally; pulls happen on create, never on first invoke.

Hot reload: `understudy lambda hot-reload <function> <dir>` (admin API) binds a host directory as `/var/task` for that function without changing its AWS-visible configuration, so `terraform plan` stays clean. Changes are detected by a wall-clock mtime scan before each invoke; a change bumps the generation.

Async invoke: `Event` invocations go to a per-function queue on the emulator clock with AWS's retry schedule (60 s, then 120 s) and 6-hour max event age; payload cap 1 MB (`lambda-api.md` finding 4; the brief's 256 KB is outdated). Destinations are the stretch goal and plug into `delivery`.

Cleanup: labels `understudy.dev/managed`, `instance`, `owner`, `function`, `account`, `region`, `generation`; graceful shutdown deletes with `force=1` (Python RIC as PID 1 ignores SIGTERM); startup sweep guarded by an `flock` on the state directory; `understudy cleanup` runs the sweep on demand (`containers.md` 9).

### 8.2 SQS event source mappings

- Control plane per `lambda-events.md` 1: 202 on create/update/delete with the full object; transient states resolve on the emulator clock within one tick; `FunctionResponseTypes: []` and `MaximumBatchingWindowInSeconds: 0` always present, `ScalingConfig`/`FilterCriteria` omitted when unset (second-plan fidelity).
- Configuration is a sum type over sources (`Sqs{Standard|Fifo}`, `DynamoDBStream` reserved for the stretch), with parsed, range-checked parameters.
- One poller task per enabled mapping, scheduled on the emulator clock. It calls SQS through `inproc` (journaled, faultable), receiving up to `min(BatchSize, 10)` per call and looping until `BatchSize`, the batching window (emulator time) or 6 MB. No HTTP, no long-poll threads, no private override headers (`landscape.md` 7).
- Filter criteria evaluated before invoke with `match/ruler`; non-matching messages are deleted without invoking.
- Synchronous invoke; partial batch response handled per `lambda-events.md` Implications 12 (empty, `null`, `{}` or empty list is full success; any malformed or unknown `itemIdentifier` is full failure); listed messages are left to their visibility timeout; deletes go through the `DeleteMessageBatch` path so the journal links receive, invoke and delete. DLQ moves come from SQS `maxReceiveCount`, never from the poller.
- FIFO: one in-flight batch per message group; batching window accepted and ignored (decision; R03 records the real behavior).
- Mappings survive function deletion but stop invoking, with a journal event saying why.

## 9. DynamoDB Local integration

DynamoDB is the one service where understudy proxies rather than implements. The proxy still uses the generated `dynamodbapi` package: requests are decoded with generated types for validation, journaling and faults, and forwarded as the original bytes when unchanged.

- **Container**: one `amazon/dynamodb-local` pinned by digest (`sha256:ff89bd48...`, 3.3.1; ECR Public mirror as fallback, same digest), pulled at first use, never bundled, patched or re-tagged. Args `-jar DynamoDBLocal.jar -port 8000 -disableTelemetry` plus `-inMemory` (default) or `-dbPath /data` (persistence); env `DDB_LOCAL_TELEMETRY=0` (only the literal `0` works). Published on `127.0.0.1:0`, port read back from the API, labeled, memory-bounded. On first pull understudy prints the licence notice from `dynamodb-local-terms.md` 5.1, including that the licence references an AWS account in good standing; the brief's "no account" statement needs that caveat.
- **Isolation**: rewrite the forwarded `Credential=` to `<account>/<date>/<region>/dynamodb/aws4_request` so Local partitions by (account, region) natively; only regions from understudy's validated table are ever forwarded (they become file names under `-dbPath`).
- **Before forwarding**, understudy answers what Local gets wrong: malformed JSON (`com.amazon.coral.service#SerializationException` 400), missing or bad target (`UnknownOperationException` 400), wrong Content-Type (404 XML), missing auth, request too large, plus generated validation with the Coral grammar. Operations outside the supported list are answered `NotImplemented` before Local sees them.
- **Shims** (typed state per (account, region, table) in `service/dynamodb`): tags (`TagResource`, `UntagResource`, `ListTagsOfResource`, seeded from `CreateTable.Tags`), TTL (spec never forwarded to Local, so its wall-clock sweeper stays off), PITR describe/update (`TableNotFoundException` for missing tables), `WarmThroughput`, `TableClassSummary`, `SSEDescription`, `TableId` injected on table descriptions (without `WarmThroughput` Terraform hangs about 2.5 minutes), `DeleteTable` reporting `DELETING`, `UpdateTable` with only shim-owned fields answered locally.
- **Responses**: operations whose outputs carry ARNs are decoded with generated types, `arn:aws:dynamodb:ddblocal:000000000000:` rewritten to the caller's scope, re-encoded, and `x-amz-crc32` recomputed; all others pass through byte-identical with Local's checksum. Error `Message` is re-keyed to the model's `message`.
- **TTL sweep** is a clock task: `Scan` with `#ttl <= :now` and a `DeleteItem` conditioned on the attribute still being expired, so a concurrent refresh wins. Admin hook `POST /_understudy/v1/dynamodb/ttl-sweep` runs it now.
- **Case-sensitive table names**: Local is case-insensitive, AWS is not. understudy maps each table name to a stable escaped internal name and rewrites it in both directions (ADR; `dynamodb-local.md` Implications 12).
- **Faults** inject `ProvisionedThroughputExceededException`, `ThrottlingException`, `TransactionConflictException` (which Local never raises).
- **Snapshots** use the replay dump of section 3.9.
- Known gaps (no throughput enforcement, sequential `Scan` segments, single-shard streams) are listed in coverage as `aws-differs: dynamodb-local`.
- Egress test: a permanent integration test runs the pinned image with understudy's exact arguments on an `--internal` network and asserts zero DNS lookups and connections.

## 10. Cross-service delivery

### 10.1 The delivery engine

```go
package delivery

type Target interface{ target() }
type SQSQueue struct{ ARN aws.ARN }
type LambdaFunction struct{ ARN aws.ARN; Qualifier opt.Option[string] }
type SNSTopic struct{ ARN aws.ARN }

type Request struct {
	Source  aws.ARN
	Target  Target
	Payload Payload        // already-built envelope bytes plus target-specific attributes
	Policy  RetryPolicy    // per source kind
	DLQ     opt.Option[aws.ARN]
	Cause   journal.SpanID
}

type RetryPolicy interface{ retryPolicy() }
type SNSManaged struct{}                          // SQS/Lambda: 3 immediate, 2 at 1 s, 10 exponential 1-20 s, 100,000 at 20 s (sns.md 7.2)
type EventBridgeTarget struct{ MaxAge time.Duration; MaxAttempts int }
type S3Notification struct{}
type None struct{}
```

Each request becomes a clock task at `now`; attempts, backoff and DLQ routing run on the emulator clock with jitter drawn from a seeded source, so `clock advance` replays a retry storm deterministically. Each attempt calls the target through `inproc` with a `ServicePrincipal` (`sns.amazonaws.com`, `events.amazonaws.com`, `s3.amazonaws.com`) and an `op.Internal` origin, and emits a `journal.Delivery` event whose parent is the source span. Client-side failures (queue or function missing) are not retried and go straight to the DLQ; filtered messages never reach a DLQ. Fan-out order is deterministic (subscription or target creation order).

Envelope and event builders are pure functions with golden fixtures and rapid properties, owned by the producing service: `service/sns/envelope` (SQS envelope with recorded key order, FIFO variant without signature fields, Lambda `Records[0].Sns` with `SigningCertUrl`/`UnsubscribeUrl` casing), `service/s3/notify` (`eventVersion 2.1`, URL-encoded key, unquoted `eTag`, per-key monotonic `sequencer`, `s3:TestEvent` to SQS/SNS on configuration), `service/events/envelope` (version `0`, second-precision `time`, `InputPath`/`InputTransformer`), `service/sqs/lambdaevent` (shared attribute MD5 function).

### 10.2 Paths in phase 1

| Path | Mechanism | Notes |
|---|---|---|
| SNS -> SQS | `SendMessage` via `inproc`; envelope or raw; FIFO topic to FIFO queue keeps group and dedup ids | filter policy evaluated per subscription first (`match/ruler` with SNS allow-list and limits); a `journal.Delivery{Filtered}` records the deciding rule |
| SNS -> Lambda | async `Invoke` (`Event`) via `inproc` | Lambda's own async retries apply after acceptance |
| SNS -> HTTP/S | **off by default**: subscription stays pending and the journal records "delivery disabled"; opt-in allow-list of loopback hosts | outbound HTTP from an emulator is an SSRF and offline-guarantee risk |
| EventBridge -> Lambda, SQS, SNS | rule match (`match/ruler`, EventBridge limits) or schedule fire (clock heap), then target input shaping | default bus exists implicitly; unknown bus `PutEvents` drops with 200 and a journal entry |
| S3 -> SQS, SNS, Lambda | after commit, an `Effect`; one message per matching configuration | should-have; configuration validation order per `s3.md` Implications 15 |
| SQS -> Lambda | ESM poller (section 8.2) | |
| SQS -> SQS (DLQ) | inside SQS at the receive attempt that exceeds `maxReceiveCount` | journal edge source-receive -> DLQ-send |

### 10.3 Semantic switches

Where the research could not settle AWS behavior, the choice is a named switch with one implementation point and a coverage-report entry (`sns.md` Implications 4, `matching-engines.md` 10, `logs-filter-patterns.md` 10): Ruler defaults for `exists:true` on `null`/`""`, `exists:false` on empty input, Lambda numeric text equality, `$or` malformed shapes (strict compile errors); SNS leaf-key counting; Logs term matching as case-sensitive substring (N1) and last-field-absorbs (N6); FilterLogEvents `endTime` inclusive. Each switch names the record-backlog item (R09, R10, ...) that will settle it.

## 11. Admin API and CLI

### 11.1 Admin API (`/_understudy/v1`, JSON, `net/http.ServeMux`)

| Method and path | Purpose |
|---|---|
| `GET /health` | per-service readiness, Docker and DynamoDB Local status, models pin, version |
| `POST /reset` | body `{"services":[], "scopes":[]}`; empty means everything; clock and faults kept unless asked |
| `GET /clock`, `POST /clock` | read; `{"action":"freeze"|"resume"|"advance","by":"90s"}`, `{"action":"set","to":"..."}`, `{"action":"scale","factor":"60/1"}` |
| `GET /faults`, `POST /faults`, `DELETE /faults/{id}`, `DELETE /faults` | rules; creation validates error identities against the operation |
| `GET /journal` | query (filters of section 3.7, cursor pagination) |
| `GET /journal/stream` | Server-Sent Events, same filters |
| `GET /journal/traces/{trace}` | causal tree |
| `POST /snapshots/save`, `POST /snapshots/load` | to or from a path under the configured snapshot directory; `GET /snapshots/export` streams one |
| `GET /resources` | resources by service, account, region, with ARNs |
| `GET /coverage?format=json|markdown` | coverage report |
| `GET /iam/policy?principal=<arn>` | least-privilege policy from recorded access |
| `POST /lambda/functions/{name}/hot-reload` | bind a host directory |
| `POST /dynamodb/ttl-sweep` | run the TTL sweep now |
| `GET /sns/subscriptions/{arn}/token` | read a pending confirmation token for tests |

Unversioned AWS-visible URLs under `/_understudy/` (outside `/v1`): SNS `SigningCertURL` certificate, SNS unsubscribe and confirm links, function URL path fallback.

### 11.2 CLI

Non-interactive, `--json` on every read command, `--endpoint` (default from `UNDERSTUDY_ENDPOINT`, then `http://127.0.0.1:4566`), exit codes `0` ok, `1` operation failed, `2` usage, `3` emulator unreachable; every error names what failed and the next command to try.

```
understudy serve [--config file] [--listen 127.0.0.1:4566] [--state-dir dir] [--sigv4=verify]
understudy health
understudy reset [--service sqs] [--account 000000000000 --region us-east-1]
understudy clock [show | freeze | resume | advance 90s | set 2026-01-01T00:00:00Z | scale 60]
understudy faults [list | add --service sqs --op SendMessage --error KmsThrottled --next 3 --seed 7 | rm ID | clear]
understudy journal [--service s --op o --trace t --errors --since 5m] [--follow]
understudy journal trace TRACE
understudy snapshot [save NAME | load NAME | export NAME > file]
understudy resources [--service s]
understudy coverage [--format markdown] [--service s]
understudy iam policy --principal ARN
understudy lambda hot-reload FUNCTION DIR
understudy env [--sdk go|python|js|cli|terraform] [--tls]
understudy doctor
understudy cleanup
understudy version
```

## 12. Configuration

Precedence: flags > `UNDERSTUDY_*` environment > JSON config file (`--config`; JSON keeps the stdlib-only rule). Parsed once at the boundary into typed values; invalid combinations are parse errors that name the field.

```go
package config

type Config struct {
	Listen      netip.AddrPort      // default 127.0.0.1:4566
	TLS         TLS                 // Off | SelfSigned{Listen netip.AddrPort}
	Persistence Persistence         // Ephemeral | Dir{Path; SaveEvery time.Duration}
	SigV4       SigV4Mode           // ParseOnly | Verify
	Accounts    Accounts            // default account, static keys
	Regions     []aws.Region        // additions to the real-name table
	S3          S3                  // BaseDomains; CrossRegion: AWS | Lenient; MaxObjectBytes
	Lambda      Lambda              // Network: Auto | Bridge{Gateway} | Disabled; StartConcurrency; IdleTTL; MaxEnvironments
	DynamoDB    DynamoDB            // Image digest; Registry: DockerHub | ECRPublic; MemoryLimit
	Clock       ClockStart          // Running | Frozen{At}; Scale
	Journal     Journal             // RingSize; File opt
	Faults      []faults.Rule       // preloaded rules
	Admin       Admin               // Token opt (required when Listen is not loopback)
	Log         Log                 // slog level, JSON or text
}
```

Binding to a non-loopback address requires an admin token; without one `serve` exits with an error that explains both options.

## 13. Testing strategy

Seven layers, each with its own job and its own number in the coverage report. Shape conformance, behavioral tests and recorded parity are reported separately, never summed (`landscape.md` Traps).

| Layer | Where | Runs in | Proves |
|---|---|---|---|
| Generator golden tests | `internal/codegen` | `go test` | model fragments -> expected Go; unknown traits fail; determinism (generate twice, compare) |
| Model example tests | generated `examples_gen_test.go` | `go test` | the 260 model examples decode and validate |
| Protocol compliance | `internal/protocoltest` | `go test` | codecs, bindings, error writers against the official corpus (section 5.9) |
| Codec and engine properties | per package, rapid | `go test` | round trips for every codec; CBOR never panics on mutated input; form index permutations; `aws-chunked` encode/decode; checksum streaming = one-shot and CRC combine; Ruler invariants; log pattern algebra; SigV4 sign-then-verify; S3 listing pages concatenate to the unpaginated listing |
| Service semantics | `internal/service/*` | `go test`, `testing/synctest` | AWS-observable behavior on the emulator clock: visibility, redrive, FIFO group blocking, dedup, long polls, TTL, retention, schedules, deletion windows; rapid `StateMachineActions` for SQS and the DynamoDB TTL shim; golden vectors from research (SQS MD5 vectors, CBOR G01-G24, checksum vectors, Coral texts) |
| Parity replay | `internal/parity` over `testdata/parity` | `go test` | recorded exchanges replayed in-process through `httptest` in a synctest bubble; normalized diff |
| Real-client conformance | `conformance/*` modules | `just conformance` via `scripts/sealed` | Go v2, boto3, JS v3, AWS CLI, Terraform (apply, `plan -detailed-exitcode` = 0, update, plan, destroy for every phase 1 resource at provider 6.68.0), e2e pipeline with journal assertions |
| Docker integration | `-tags docker` | `just test-docker` | Python 3.13 and `provided.al2023` functions: warm reuse, `Unhandled`, `Runtime.ExitError`, timeout, OOM, concurrent invokes get separate environments, crash recovery sweep, missing-`Content-Type` regression, DynamoDB Local egress |

### 13.1 Parity recordings

```json
{
  "id": "sqs/stale-receipt-handle-after-rereceive",
  "evidence": {
    "label": "aws-recorded",
    "recorded": "2026-11-02",
    "region": "us-east-1",
    "client": "aws-cli/2.36.38",
    "backlog": "R01"
  },
  "variables": {"queueUrl": {"from": "step:create", "jsonPath": "$.QueueUrl"}},
  "steps": [
    {
      "name": "create",
      "request": {"method": "POST", "path": "/", "headers": {"X-Amz-Target": "AmazonSQS.CreateQueue", "Content-Type": "application/x-amz-json-1.0"}, "body": "{\"QueueName\":\"rec-q\"}"},
      "response": {"status": 200, "headers": {"Content-Type": "application/x-amz-json-1.0"}, "body": "{\"QueueUrl\":\"...\"}"},
      "compare": {"body": "json", "normalize": ["queueUrlHost", "requestId"]}
    }
  ]
}
```

- Evidence labels: `aws-recorded` (human-run record mode), `third-party-recorded` (for example a LocalStack snapshot with `last_validated_date`, source URL kept), `aws-doc`, `sdk-source`, `inferred`. The replayer reports pass counts per label.
- Normalizers are a closed, named set (request ids, receipt handles, timestamps, hosts, ARN account ids) so a recording cannot hide a real diff behind a regex.
- Record mode lives only in `conformance/parity-record`, a separate module. It refuses to run unless invoked with an explicit human-only flag and real credentials it reads itself; agents never run it. The research backlog R01 to R18 (`s3-checksum-residuals-and-record-backlog.md` part 2) is its first work list. Until it runs, the third-party recordings cited throughout the research are imported with their labels.

### 13.2 Isolation and time

Every test gets its own account or region namespace, or its own server instance on `127.0.0.1:0`. Time-dependent tests use `testing/synctest` with the emulator clock; no test sleeps on wall time except the Docker and Terraform suites, whose provider-side floors (55 s per lifecycle configuration, 5 s per Lambda waiter, 25 s and 42 s SQS waits) are documented and sized into their timeouts.

## 14. Security

- Bind `127.0.0.1` by default. The Lambda gateway listener binds the bridge gateway IP only, never `0.0.0.0`, and can be disabled; per-environment Runtime API listeners use ephemeral ports on the same IP and are removed from the lookup table before their container is deleted, so a late `/response` cannot reach the next invocation.
- The admin API has no auth on loopback; a non-loopback bind requires a token (section 12).
- Request bodies are capped per operation (generated from overlays, protocol defaults otherwise); S3 object and part sizes are capped by configuration; JSON and CBOR decoders bound nesting depth and check declared lengths before allocating.
- S3 keys never touch the filesystem: blobs are content-addressed under an `os.Root`. Lambda zips are extracted through `os.Root` with symlink and `IsLocal` checks and size caps. No symlink is ever created from user input.
- Docker: every object labeled; graceful drain; startup sweep under an `flock`; test containers labeled `understudy.dev/test=1`.
- Secrets never logged: slog values for credential- and secret-bearing types implement `LogValuer`; journal redaction is generated from `sensitive` plus overlays; SigV4 headers are always redacted.
- No outbound network except the Docker socket and image pulls. SNS HTTP/S delivery is off by default. DynamoDB Local runs with telemetry disabled and an egress test guards it.
- Real-looking access keys are never decoded into foreign account numbers (section 6).
- `scripts/sealed` stays the only way tooling talks to understudy in this repo; conformance suites run through it.

## 15. Top risks and mitigations

| # | Risk | Mitigation |
|---|---|---|
| 1 | The generator becomes a compiler nobody can change; first useful service lands late | Tracer bullet (section 17): SQS `CreateQueue`/`SendMessage` over awsJson1_0 end to end before any other protocol; generator golden tests on tiny fragments; IR kept small and typed; emitters one file each |
| 2 | Generated code volume and compile time (5,487 shapes in the models) | Types and serde only for the implemented closure (`codegen/services`); descriptors for the rest are small; measure build time in wave 1 and record it in the ADR |
| 3 | Overlays rot or encode guesses as facts | Mandatory `evidence` with label; coverage lists every overlay entry by label; a model bump that conflicts fails generation; record backlog R01-R18 promotes `third-party`/`inferred` entries to `aws-recorded` |
| 4 | Smithy corpus and AWS disagree (validation text, malformed bodies) | Style and identity are overlay parameters, so the synthetic services and real services each pass their own oracle; skip entries require a category and evidence |
| 5 | awsQuery has no reference server runner | `both` cases, golden SDK captures in `wire-json-query.md`, and four-client conformance suites; Query is the second protocol built, so it gets the longest soak |
| 6 | Lambda networking differs across Linux Docker, Docker Desktop, rootless Docker and Podman | Topology detection with a startup self-check and actionable diagnostics; Linux Docker and Podman rootful are the supported phase 1 targets; Desktop and rootless are documented as best-effort until verified; IP-literal endpoints avoid DNS entirely |
| 7 | DynamoDB Local licence, availability or behavior change | Digest pin and mirror; licence notice on first pull; legal review before the first public release (`dynamodb-local-terms.md` 5.4); egress test; DynamoDB failure leaves other services working |
| 8 | Clock-domain bugs (dead Go contexts, logs silently dropped) | Distinct types per domain; `forbidigo` on `time.*`; the conformance tests from `lambda-runtime-contract.md` 6.1 (frozen and advanced clock, remaining time within 100 ms of timeout) |
| 9 | Terraform wall-clock floors make the Terraform suite slow | Run per resource group in parallel; nightly full run; make the first poll final everywhere (`tf-storage.md` Implications 3) so only provider floors remain |
| 10 | `encoding/json/v2` surface changes in a later Go release | Single importer `codec/jsonx`; rapid round trips catch behavior drift on toolchain bumps |
| 11 | XXH3/XXH128 port correctness | Vectors from `s3-checksum-residuals-and-record-backlog.md` 1.4 for every length class; streaming = one-shot property |
| 12 | Determinism leaks (map order, uuid v7 following the wrong clock, wall time in snapshots) | `ids.Source` injection; sorted iteration lint rule in review; snapshot byte-equality test (save, load, save, compare) |
| 13 | SDK releases change wire behavior (CloudWatch moved to CBOR in Go v2 v1.53.0) | SDK and provider versions pinned in conformance manifests; a scheduled canary job runs latest; `wire-cbor.md` 2.1 capture repeated at each pin bump |

## 16. Decisions this proposal asks ADRs for

1. Generated typed serde against `serde.Encoder`/`Decoder` (SDK schema style), not a runtime schema interpreter (`go-toolchain.md` 17.10).
2. Overlay format: Smithy JSON AST `apply` plus `dev.understudy#` traits with mandatory evidence.
3. `codegen/services/*.json` `implemented` list generates a `Handlers` interface that is exactly the implemented set.
4. Unimplemented operations: 501 `NotImplemented` in the native envelope.
5. Lambda: understudy-hosted Runtime API instead of the RIE (deviation from the brief's wording).
6. Lambda networking: gateway listener plus per-environment Runtime API listeners on the bridge gateway IP; `localhost:host-gateway` alias on Linux and Podman.
7. Clock domains and the `clock.Instant` type; backward `Set` refused.
8. S3: global bucket namespace with AWS cross-region behavior by default; no DNS server in phase 1.
9. Access keys: decode only understudy-minted keys; numeric keys are accounts; default account `000000000000`.
10. DynamoDB: single Local container with credential-rewrite partitioning; case-sensitive name mapping; licence notice text.
11. Snapshot format and versioning; re-arm instead of serializing scheduler tasks.
12. xxHash: in-repo port (no dependency).
13. Parity recording format, evidence labels and the human-only record module.
14. SNS HTTP/S delivery disabled by default.
15. Default port 4566 (matches `scripts/sealed` and existing user configuration).

Brief statements to caveat: "no account, no telemetry" needs the DynamoDB Local exception (`gap-G5.md`); the async invoke limit is 1 MB, not 256 KB; "with the Runtime Interface Emulator" becomes "with the official runtime images and runtime interface clients".

## 17. Build order (tracer bullet first)

1. **Wave 1, the spine.** Vendoring tool and pins; generator with SQS only (`CreateQueue`, `GetQueueUrl`, `SendMessage`, `ReceiveMessage`, `DeleteMessage`); `schema`, `serde`, `jsonx`, `awsjson`; front door, pipeline, registry, `op.Call`; clock, journal, ids; `awsjson` protocol tests (JsonRpc10 corpus) green; Go v2 and boto3 conformance for those five operations; `understudy serve`, `health`, `clock`, `journal`, `coverage`.
2. **Wave 2, breadth of protocols.** `awsquery` (SNS, SQS legacy, IAM, STS), `restxml` (S3 core), `restjson` (Lambda control plane), `rpcv2cbor` (CloudWatch alarms); every corpus suite green; faults; snapshots.
3. **Wave 3, behavior.** Full SQS and SNS with filter policies and delivery engine; S3 checksums, multipart, `aws-chunked`, conditional requests, subresources; KMS crypto; Secrets Manager; Logs with filter patterns; EventBridge rules and schedules; IAM/STS stores and access recorder.
4. **Wave 4, execution.** Docker client, Lambda execution, ESM, function URLs, logs to CloudWatch Logs; DynamoDB Local proxy and shims; Terraform suite to second-plan-empty for every phase 1 resource; e2e pipeline with journal assertions.
