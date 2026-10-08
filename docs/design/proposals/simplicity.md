# Architecture proposal: simplicity and velocity

Status: proposal for phase 1. Angle: the fewest moving parts that still meet [the brief](../../brief/phase1-brief.md). Research citations point at `docs/research/phase1/`.

## 0. The bet in one page

A contributor should be able to open `internal/service/sqs/`, read it top to bottom, and add an operation to any service by the end of the afternoon. Everything below serves that.

The rules that follow from it:

1. **One handler contract.** Every operation of every service is a plain typed Go function: `func(ctx, *api.Call, *In) (*Out, *api.Error)`. It knows nothing about JSON, XML, CBOR, HTTP, journals, faults or IAM.
2. **One dispatch path.** External HTTP requests and internal cross-service deliveries both run through one function, `api.Dispatch`. It journals, injects faults, records IAM access, validates and calls the handler. SNS delivers to SQS by calling the SQS `SendMessage` handler through that path, not over HTTP and not through a bespoke interface.
3. **Generate types, interpret wire.** The generator emits typed structs (wire traits in struct tags), operation descriptors, error constructors and validators. It emits no per-shape serializers. Each wire protocol is one hand-written codec that walks those structs with cached reflection. That gives six codecs instead of six code emitters, and the Smithy compliance suite tests each codec directly.
4. **State is plain structs behind one mutex per service.** There is no storage engine, actor system or event sourcing. The snapshot format is the state structs, marshalled as canonical JSON, with a version number per service.
5. **Time is lazy where it can be.** Behavior that is a pure function of "now" (visibility, delay, retention, deletion windows, key deletion) is computed when it is read, against the emulator clock. Goroutines exist only for side effects: long-poll wakeups, pollers, schedules, retries, the TTL sweep.
6. **No speculative layers.** No plugin system, no DI container, no generic resource framework, no config file, no DNS server, no HTTP loopback to ourselves, no RIE. Every missing layer is listed in section 17 with the reason.

## 1. Package layout

```
cmd/understudy/                 main: flag parsing, subcommand dispatch
internal/
  api/                          the handler contract: Call, Principal, Error, Op[In,Out], Registry, Dispatch, Invoke
  clock/                        the emulator clock (freeze, set, advance, rate, Wait)
  journal/                      causal event log: append, query, subscribe
  fault/                        fault rules: parse, match, seeded triggers
  access/                       IAM access recorder and least-privilege policy output
  snapshot/                     archive format: manifest, per-service state, blobs
  blob/                         content-addressed byte store under os.Root (S3 bodies, Lambda zips)
  ids/                          seedable ID source (request ids, message ids, receipt handles)
  config/                       typed Config parsed once from flags and UNDERSTUDY_* env
  server/                       listeners, front-door router, body caps, admin API mount
  admin/                        admin API handlers (served by server under /_understudy/)
  cli/                          admin client subcommands (thin HTTP client of admin/)
  sigv4/                        auth parsing (SigV4 header/query, SigV2), opt-in verification
  wire/                         struct-tag plan cache shared by codecs
  wire/awsjson/                 awsJson1_0 and awsJson1_1, plus awsQueryCompatible errors
  wire/awsquery/                awsQuery form decode and XML encode
  wire/restxml/                 restXml (S3 only); owns the XML writer
  wire/restjson/                restJson1 (Lambda only)
  wire/rpcv2/                   Smithy RPC v2 CBOR (CloudWatch only), including the CBOR subset
  checksum/                     CRC32/32C/64NVME, SHA*, MD5, XXH64/XXH3/XXH128, CRC combine
  awschunked/                   aws-chunked decoder (four modes, trailers)
  match/                        Event Ruler matcher core with per-service allow-lists and limits
  logpattern/                   CloudWatch Logs filter-pattern compiler and matcher
  docker/                       Docker Engine API client over the unix socket
  lambdart/                     Lambda execution: environments, pool, per-environment Runtime API
  service/<svc>/                hand-written service: state.go, one file per resource family, tests
  service/<svc>/<svc>api/       GENERATED: types.go, ops.go, errors.go, validate.go
  cmd/smithygen/                the code generator
  cmd/vendormodels/             fetch/verify vendored models (run by hand, never in tests)
third_party/
  api-models-aws/               12 vendored models + UPSTREAM.json (commit 1cfdfb4, blob SHAs)
  smithy-protocol-tests/        one flattened JSON AST (Smithy 1.74.0) + PIN.json
  aws-service-reference/        derived operation -> IAM action table + provenance
gen/overlays/<svc>.json         hand-owned generator inputs (see 4.3)
conformance/{go,python,js,cli,terraform,e2e}/   separate modules, run through scripts/sealed
testdata/parity/                human-recorded AWS fixtures with provenance (replay only)
```

The 12 services are `sqs sns s3 dynamodb lambda logs secretsmanager kms iam sts events cloudwatch`.

Dependency direction is strict and shallow:

```
cmd -> server, cli, config
server -> api, wire/*, sigv4, admin, service/* (wiring only)
service/<svc> -> api, clock, ids, blob, <svc>api, <other>api (types only), match/logpattern/checksum as needed
<svc>api -> api (descriptors only)
api -> clock, journal, fault, access (dispatch steps)
journal, fault, access, clock -> stdlib only (they take plain strings and IDs, never api types)
```

A service package never imports another service package. It imports another service's generated `<svc>api` package and calls that service's operations with `api.Invoke`. Generated packages are leaves, so there are no import cycles, and services need no wiring interfaces. SNS imports `sqsapi` and `lambdaapi`. S3 imports `kmsapi`, `sqsapi`, `snsapi` and `lambdaapi`.

Why `api` is one package with several types: its single concern is "the contract between a handler and the emulator". Splitting `Call`, `Error` and `Registry` into three packages would make every service import three packages to do one thing. The other packages each center on one type, as the repo rules require (`clock.Clock`, `journal.Journal`, `fault.Rule`, `snapshot.Archive`).

## 2. Core abstractions

### 2.1 Request context

```go
package api

// Call is the per-request context every handler receives. It is created by the
// front door (external) or by Invoke (internal) and is immutable to handlers
// except for SetResource.
type Call struct {
	RequestID string
	Account   Account   // validated 12-digit account id
	Region    Region    // validated against the known region list
	Principal Principal
	Origin    Origin
	Parent    journal.ID // causal parent; zero for a root call
	Entry     journal.ID // this call's journal entry

	resource string // ARN set by the handler for the access recorder and journal
}

func (c *Call) SetResource(arn string) { c.resource = arn }

// Principal is a closed sum type.
type Principal interface{ principal() }

type Root struct{ Account Account }                            // unknown static key, or the account itself
type User struct{ ARN string }                                 // IAM user, from a key minted by CreateAccessKey
type AssumedRole struct{ RoleARN, Session string }             // keys minted by AssumeRole or for a Lambda role
type ServicePrincipal struct{ Name string }                    // "sns.amazonaws.com", for internal deliveries

// Origin is a closed sum type.
type Origin interface{ origin() }

type External struct {
	Protocol   Protocol // AWSJSON10, AWSJSON11, AWSQuery, RESTXML, RESTJSON, RPCv2CBOR
	RemoteAddr netip.AddrPort
}
type Internal struct{ Caller string } // service that issued the call, for example "sns"
```

The front door derives `Account` and `Principal` from the access key (section 8.3) and `Region` from the SigV4 credential scope. The request Host is never used for that. A handler that needs the clock, IDs or blobs gets them as fields of its service struct, set at construction. They do not travel on `Call`.

### 2.2 Operation descriptors and handler signature

```go
package api

// Op is a typed operation descriptor. The generator emits one var per operation,
// for example sqsapi.SendMessage of type api.Op[sqsapi.SendMessageInput, sqsapi.SendMessageOutput].
type Op[In, Out any] struct{ Desc *OpDesc }

type OpDesc struct {
	Service  *ServiceDesc
	Name     string        // "SendMessage"
	HTTP     HTTPBinding   // method, URI template, status; zero for RPC protocols
	Errors   []*ErrorShape // modeled errors, in model order
	Actions  []string      // IAM actions, primary first, from the Service Reference table
	ConstraintError *ErrorShape // which shape a validation failure uses (overlay H2)
}

type ServiceDesc struct {
	Name       string     // "sqs"
	SigV4Name  string     // "sqs"; "monitoring" for CloudWatch
	TargetName string     // X-Amz-Target prefix, "AmazonSQS"
	Version    string     // awsQuery Version, "2012-11-05"
	Protocols  []Protocol // model protocols plus overlay additions (SQS gains AWSQuery)
	XMLNS      string
	BodyLimit  int64      // per-service request body cap (overlay)
	Ops        []*OpDesc  // every model operation, implemented or not
}

type Handler[In, Out any] func(ctx context.Context, c *Call, in *In) (*Out, *Error)

// Handle registers h for op. Called from each service's Register method.
func Handle[In, Out any](r *Registry, op Op[In, Out], h Handler[In, Out])

// Invoke runs op through the full dispatch path (journal, faults, access,
// validation) on behalf of an internal caller.
func Invoke[In, Out any](ctx context.Context, from InternalCall, op Op[In, Out], in *In) (*Out, *Error)

type InternalCall struct {
	Parent  *Call     // the call that caused this one, for the causal link
	As      Principal // ServicePrincipal, or the function's role for ESM polling
	Account Account   // target scope, taken from the target resource ARN
	Region  Region
}
```

`Invoke` is how cross-service work happens (section 11). It skips only the wire codec. The handler a test calls, the handler HTTP reaches and the handler SNS reaches are the same function, going through the same steps.

Generated inputs implement two small methods that the generator writes as plain code:

```go
// Validate appends Smithy constraint violations (required, length, range, pattern, enum).
func (in *SendMessageInput) Validate(v *api.Violations)
// Identifiers returns identifier member values (QueueUrl here) for fault and journal matching.
func (in *SendMessageInput) Identifiers() []string
```

### 2.3 Error model

```go
package api

// ErrorShape is the identity of an AWS error. Generated shapes come from the
// model; hand-owned shapes (S3's not-configured table, SQS InvalidParameterValue,
// common errors) are declared as Go vars in the service or in api/common.go.
type ErrorShape struct {
	Name          string // Smithy shape name, "QueueDoesNotExist"
	Namespace     string // emitted __type prefix (overlay H1); "" for bare names
	QueryCode     string // awsQueryError code, "AWS.SimpleQueueService.NonExistentQueue"
	Status        int
	Fault         Fault  // Sender | Receiver
	MessageMember string // "message" or "Message" exactly as modeled; "" if none
}

// Error is the only error type a handler returns. It is always AWS-shaped.
type Error struct {
	Shape   *ErrorShape
	Message string
	Fields  []Field       // extra members in order: S3 BucketName/Key, Lambda Type/Reason
	Headers []HeaderField // Retry-After, x-amz-bucket-region, ...
	cause   error         // internal detail for slog; never serialized
}

func NotImplemented(op *OpDesc) *Error             // 501, see 2.3.1
func Validation(op *OpDesc, v Violations) *Error   // Coral grammar text, see 2.3.2
func Internal(cause error) *Error                  // 500 InternalFailure, cause logged
```

Handlers return `*api.Error`, not `error`. That makes "every failure is AWS-shaped" a property of the types, not of code review. A disk or Docker failure is wrapped by `api.Internal(err)`. The cause goes to the log, and the client sees an AWS-shaped 500. The codecs render an `*Error` per protocol:

| Protocol | Rendering |
|---|---|
| awsJson | body `{"__type": Namespace#Name or Name, <MessageMember>: msg}`; status from shape; `x-amzn-query-error: QueryCode;Fault` when the service is awsQueryCompatible and the shape has a QueryCode |
| awsQuery | `ErrorResponse/Error{Type,Code=QueryCode or Name,Message}` + `RequestId`; status from shape |
| restXml (S3) | `<Error>` root, no wrapper, `Code`, `Message`, Fields, `RequestId`, `HostId`; HEAD gets status only |
| restJson (Lambda) | `X-Amzn-Errortype: Name`, body with the modeled member names (`Type`, `Message`/`message`) |
| rpcv2 CBOR | CBOR map `{"__type": absolute shape id, <MessageMember>: msg}`, never an empty body; query-error header as for JSON |

#### 2.3.1 Unimplemented operations

The registry is built from `ServiceDesc.Ops`, which lists every operation in the model. An operation with no `Handle` call gets a generated stub that returns `NotImplemented`: HTTP 501, code `NotImplemented`, message `understudy does not implement <sdkId>.<Operation> yet; run "understudy coverage" to see what is supported.`. S3 already uses `NotImplemented`/501 for unsupported features, so the same code reads naturally across all services. 501 is outside the Go, botocore and JS retry sets, so SDKs fail fast instead of retrying (wire-json-query.md item 15). The exact code is an ADR item because AWS documents none.

#### 2.3.2 Validation errors

Generated validators collect `Violation{Path, Value, Constraint}` and never format text. `api.Validation` renders AWS's Coral grammar once: `N validation error(s) detected: Value 'v' at 'lowerCamelPath' failed to satisfy constraint: Member must ...`, joined by `; ` (error-matrix.md section 4). The error shape is `OpDesc.ConstraintError`, set per operation from the overlay, because the code depends on the operation (error-matrix.md finding 3). Hand-written semantic checks in handlers (SQS limits, Lambda runtime list) return their own shapes and texts. They are not squeezed into the generated validator.

### 2.4 Registration

Each service is one struct that implements one interface. Optional interfaces are not probed at runtime.

```go
package api

type Service interface {
	Desc() *ServiceDesc
	Register(r *Registry)
	Start(ctx context.Context) error // starts background goroutines; returns once they are running
	Close() error                    // stops them and waits
	Reset()                          // drop all state; goroutines restart from empty state
	Locker() sync.Locker             // the service mutex; only the snapshot coordinator uses it
	Save(w *snapshot.Writer) error   // called with Locker() held
	Load(r *snapshot.Reader) error   // called before Start, with no goroutines running
	Resources(scope Scope) []Resource // for "understudy resources"
}
```

```go
// internal/service/sqs/service.go
func (s *Service) Register(r *api.Registry) {
	api.Handle(r, sqsapi.CreateQueue, s.createQueue)
	api.Handle(r, sqsapi.SendMessage, s.sendMessage)
	api.Handle(r, sqsapi.ReceiveMessage, s.receiveMessage)
	// ... one line per implemented operation; the rest stay NotImplemented
}
```

`internal/server/services.go` builds the list in one explicit function, with no reflection and no init-time registration:

```go
func newServices(d api.Deps) []api.Service {
	return []api.Service{
		kms.New(d), iam.New(d), sts.New(d), sqs.New(d), sns.New(d), s3.New(d),
		logs.New(d), lambda.New(d), events.New(d), secretsmanager.New(d),
		cloudwatch.New(d), dynamodb.New(d),
	}
}
```

The order of this slice is the lock order for snapshots (2.5) and the shutdown order (reversed).

### 2.5 State and concurrency

Each service owns its state as plain Go structs behind one `sync.Mutex`, partitioned by scope:

```go
type Service struct {
	clock *clock.Clock
	ids   *ids.Source
	mu    sync.Mutex
	scopes map[api.Scope]*scopeState // Scope = {Account, Region}; IAM/STS use Region ""
	// background goroutines
	wg     sync.WaitGroup
	cancel context.CancelFunc
}
```

There are three rules. A test enforces the third, and it is the only rule with a deadlock consequence.

1. **Short critical sections.** Take the lock, read or mutate maps, release. Never block on I/O, the clock or a channel while holding it. Large bodies (S3 PUT, Lambda zip) stream into the blob store before the lock is taken. The commit under the lock only records the blob id.
2. **Waits are channels.** Long polls and pollers wait on a per-resource `changed` channel (closed and replaced on every mutation), on `clock.Wait`, and on `ctx.Done()`. They never wait on a mutex hand-off, which keeps them testable inside `testing/synctest` bubbles (go-toolchain.md section 1).
3. **Never hold your lock across `api.Invoke`.** Synchronous cross-service calls (S3 -> KMS `GenerateDataKey`, Secrets Manager -> KMS `Decrypt`) happen before the lock is taken or after it is released. Asynchronous deliveries are started after the mutation commits. Because no code path ever holds two service locks, the snapshot path can take all locks in registry order without risk of deadlock. A debug build tag (`-tags lockcheck`) records which service lock each goroutine holds and panics if `Invoke` is called while one is held. CI runs the test suite once with it.

Why not one global lock: S3 bodies and KMS crypto would serialize behind unrelated SQS traffic, and re-entrant internal calls would need a "lock already held" variant of every handler. Why not finer locks (per queue, per bucket): they complicate snapshots and buy nothing at local scale. One mutex per service is the smallest unit that keeps internal calls simple.

### 2.6 The clock

```go
package clock

// Clock is the single emulator clock. There is exactly one per process, and it
// is the only package allowed to call time.Now (forbidigo enforces it).
type Clock struct{ /* mu, wall anchor, emu anchor, rate, changed chan */ }

func (c *Clock) Now() time.Time                      // emulator time
func (c *Clock) Wait(ctx context.Context, until time.Time) error // blocks until Now() >= until
func (c *Clock) Freeze()
func (c *Clock) Resume()
func (c *Clock) Set(t time.Time)
func (c *Clock) Advance(d time.Duration)
func (c *Clock) SetRate(r float64)                   // 1 = real time, 100 = 100x, must be > 0
func (c *Clock) State() State                        // for the admin API and snapshot manifest

func Wall() time.Time                     // host wall clock, for the cases listed below; explicit at every call site
func Stopwatch() func() time.Duration     // mono elapsed time since the call
```

Implementation: `Now() = emuAnchor + (wallNow - wallAnchor) * rate`, or `emuAnchor` when frozen. Every state change re-anchors and closes and replaces `changed`. `Wait` computes the wall duration to `until` at the current rate and selects on a wall timer, `changed` and `ctx.Done()`. On wake it re-checks `Now()`. There is no central timer heap. Each waiter is a goroutine in `Wait`, which is simple, cancels cleanly, and handles freeze, advance and scale with no special cases. Under `synctest`, wall time is the bubble's fake time, so the real `Clock` runs unmodified in tests.

The clock-domain contract follows lambda-runtime-contract.md section 3 exactly:

| Domain | Used for |
|---|---|
| **emu** (`clock.Now`) | SQS visibility/delay/retention/dedup windows, long-poll caps, ESM batching windows, async invoke retries, SNS delivery retries, EventBridge schedules and event `time`, DynamoDB TTL sweep, secret and KMS deletion windows, log retention, S3 `Last-Modified`, presigned URL expiry, function state transitions, every `LastModified`/`CreatedDate` the API returns |
| **wall** (`clock.Wall`) | `Lambda-Runtime-Deadline-Ms`, STS `Credentials.Expiration`, SigV4 skew checks, the HTTP `Date` header |
| **mono** (`clock.Stopwatch`) | function timeout enforcement, REPORT durations, init limit, idle container reaping, injected fault latency |
| **wall OR emu** | the PutLogEvents 2 h / 14 day acceptance window for client-stamped events |

A long poll must also end when its wall-time cap is reached, even with a frozen clock: an HTTP client cannot wait forever (sqs.md item 11). `ReceiveMessage` waits for `min(emu deadline, wall deadline = WaitTimeSeconds of real time)`. Advancing the clock ends it early.

### 2.7 The journal

```go
package journal

type ID uint64 // monotonic per process; 0 = none

type Entry struct {
	ID      ID
	Parent  ID        // causal parent
	Root    ID        // first entry of the causal chain (trace id)
	Kind    Kind      // APICall | Delivery | Invocation | Internal
	Time    time.Time // emu
	Wall    time.Time
	Service, Operation string
	Account, Region    string
	Principal          string // ARN form
	Resource           string // ARN set by the handler, or the first identifier
	Origin             string // "external:awsJson1_0" or "internal:sns"
	Status             int
	ErrorCode          string
	Fault              string // id of the fault rule that fired, if any
	Duration           time.Duration // mono
	Summary            map[string]string // small redacted excerpt (identifiers only)
}

type Journal struct{ /* mu, ring buffer, subscribers */ }

func (j *Journal) Begin(e Entry) ID
func (j *Journal) End(id ID, status int, code string, d time.Duration)
func (j *Journal) Query(f Filter) []Entry // service, op, resource glob, root, since, error-only, limit
func (j *Journal) Subscribe(f Filter) (<-chan Entry, func()) // drops on slow readers, counts drops
```

The journal is an in-memory ring buffer (default 100,000 entries, configurable). It is optional to tee it as NDJSON to `--journal-file` for CI artifacts. It is history, not state, so snapshots do not include it.

Causality comes from the dispatch path. There are no explicit tracing calls in handlers.

- `api.Invoke` sets the new call's `Parent` to the invoking call's `Entry`.
- Asynchronous deliveries capture `*Call` before spawning, so the delivery's entry is the child of the publish.
- Calls made *from inside a Lambda function* are linked through the execution environment: each environment gets its own minted access key (section 10.4), and an environment runs one invocation at a time, so a request signed with that key while the environment is `Busy` is a child of that invocation. This is exact and needs no help from the SDK. If the request also carries `X-Amzn-Trace-Id`, the journal stores it, but correctness does not depend on it.

`Summary` holds identifier members only (queue URL, bucket, key, function name). It never holds payloads. Members the model marks `sensitive` are never copied, and the generator emits that list.

### 2.8 Fault injection

```go
package fault

type Rule struct {
	ID       string
	Match    Match   // all fields optional; empty matches everything. Evaluated against a plain Subject
	                 // (service, operation, identifiers, account, region, internal bool), so fault imports no api types
	Action   Action  // closed sum type
	Trigger  Trigger // closed sum type
}

type Match struct {
	Service, Operation string // exact
	Resource           string // glob over Identifiers() and the resolved ARN
	Account, Region    string
	Origin             OriginMatch // Any | External | Internal (to target deliveries only)
}

type Action interface{ action() }
type ReturnError struct{ Code string; Message string } // resolved to an *api.ErrorShape at rule creation
type Throttle struct{}                                 // the service's own throttle shape (table per service)
type Latency struct{ D time.Duration }                 // mono, applied before the handler
type ErrorAfterHeaders struct{ Code string }           // S3 Complete/Copy/UploadPartCopy 200-with-error

type Trigger interface{ trigger() }
type Always struct{}
type NextN struct{ N int }
type Probability struct{ P float64; Seed uint64 } // per-rule PCG stream; deterministic for a sequential client
```

Rules are parsed at the admin API boundary. A `ReturnError` code that is neither a modeled error of the operation nor a known common error is rejected when the rule is created, not when it fires. Matching happens in `Dispatch` before validation and before the handler, so an injected error never leaves state half-mutated. `ErrorAfterHeaders` is the one exception: the handler runs, and the S3 codec writes `200` followed by an `<Error>` body (wire-rest.md section 3.7). Internal deliveries go through `Dispatch`, so a rule like `{Service: sqs, Operation: SendMessage, Origin: Internal}` makes SNS-to-SQS delivery fail and drives the SNS retry and DLQ path. That falls out of the design with no extra code.

Per-rule PRNGs make a run deterministic for a given call sequence. With concurrent clients, the order in which calls reach a rule depends on scheduling. The docs state this rather than hide it.

### 2.9 Dispatch

There is one explicit function, not a middleware chain:

```go
func (d *Dispatcher) dispatch(ctx context.Context, c *Call, e *entry, in any) (any, *Error) {
	elapsed := clock.Stopwatch()
	c.Entry = d.journal.Begin(entryFor(c, e.desc, in))
	out, err := d.run(ctx, c, e, in)
	d.access.Record(principalARN(c), e.desc.Actions, c.resourceOr(e.identifiers(in)))
	d.journal.End(c.Entry, statusOf(e.desc, err), codeOf(err), elapsed())
	return out, err
}

func (d *Dispatcher) run(ctx context.Context, c *Call, e *entry, in any) (any, *Error) {
	if act, ok := d.faults.Match(subjectOf(c, e.desc, e.identifiers(in))); ok {
		if err := d.applyPre(ctx, e.desc, act); err != nil { // resolves ReturnError codes to shapes
			return nil, err
		}
	}
	if v := e.validate(in); !v.Empty() {
		return nil, Validation(e.desc, v)
	}
	return e.handle(ctx, c, in)
}
```

The access recorder records every attempted call, including failed ones, because a least-privilege policy has to cover calls that the application makes and handles.

### 2.10 Snapshot format

A snapshot is a deterministic, uncompressed tar archive (`archive/tar`, entries in sorted order, zero mtimes, uid/gid 0, mode 0644):

```
manifest.json            {"format":1,"understudy":"0.1.0","models":"1cfdfb4...","clock":{...},
                          "services":{"sqs":1,"s3":1,...},"blobs":"sha256"}
state/<service>.json     canonical JSON (encoding/json/v2 with Deterministic(true))
blobs/sha256/<hex>       S3 object bodies, Lambda zips, extracted only on demand
```

- **The state structs are the schema.** Every field of a service's persisted state has an explicit `json` tag. Each service declares `const stateVersion = N`. Changing a persisted struct means bumping the version and adding a `migrateVN(json) json` function, or refusing old files with a message that names the version. A newer format or a newer service version is always refused ("written by a newer understudy"). A round-trip test per service guards this: random state, save, load, compare. The alternative, a separate DTO layer, doubles every type for no benefit while the format is young.
- **Timers are never saved.** Visibility deadlines, schedule `nextFireAt` and deletion dates are state. Goroutines are re-derived from state on `Load`, by calling `Start` again after `Load`, which closes MiniStack's "restored ESMs never re-armed" bug class (landscape.md section 8).
- **Consistent cut.** `Save` waits up to 5 s (wall) for in-flight async deliveries to settle (the delivery tracker in section 11 is a `WaitGroup`). It then takes every service lock in registry order, serializes, and releases. Deliveries still in flight after the wait are named in the response as "not captured".
- **DynamoDB** has no in-process state to dump. It is saved by replay: `DescribeTable` and a paginated `Scan` per table, plus shim state (tags, TTL, PITR), into `state/dynamodb.json`. It is restored with `CreateTable` and `BatchWriteItem` (dynamodb-local.md item 11).
- **Persistence across restarts** (`--persist=<dir>`) is the same archive written to `<dir>/state.snapshot` on graceful shutdown and every 30 s when dirty (atomic rename), and loaded at start. In that mode blobs live directly in `<dir>/blobs`, and the archive references them instead of copying them.

## 3. Request flow end to end

```
TCP -> server.Listener (127.0.0.1:4566, plus the gateway listener for containers)
  -> body cap (per service, http.MaxBytesReader)
  -> server.route: classify (admin | function URL | S3 Control | S3 vhost | protocol) -> (ServiceDesc, Protocol)
  -> sigv4.Parse(auth) -> access key, scope region/service; optional Verify
  -> identity: account + principal from access key (iam store), region from scope
  -> codec.Resolve(req) -> *OpDesc;  codec.Decode(req, opInputType) -> *In
  -> api.Dispatch(ctx, call, op, in)          (journal, faults, validation, handler, access)
  -> codec.Encode(w, op, out) or codec.EncodeError(w, op, err)
```

Every response carries exactly one `Date` header (wall clock), `x-amzn-RequestId` or the protocol's equivalent, and S3's `x-amz-request-id`/`x-amz-id-2` (s3.md finding 7).

## 4. The code generator

### 4.1 Inputs

| Input | Location | Owner |
|---|---|---|
| 12 AWS models, verbatim, commit `1cfdfb4bb9ea858e65623e66d73af04bec353201` | `third_party/api-models-aws/` + `UPSTREAM.json` (git blob SHA-1 and sha256 per file) | `cmd/vendormodels` (run by hand; `-verify` offline in CI) |
| Smithy protocol tests, flattened JSON AST (Smithy 1.74.0, 2,400,318 bytes, deterministic) | `third_party/smithy-protocol-tests/` + `PIN.json` (jar and CLI sha256) | regenerated by hand with the Smithy CLI (smithy-protocol-tests.md section 5); no Java on the test path |
| Operation -> IAM action table | `third_party/aws-service-reference/actions.json` (derived, with provenance) | hand-run fetch; license question goes to an ADR (smithy-models.md section 8) |
| Overlays | `gen/overlays/<svc>.json` | hand-written, reviewed like code |

### 4.2 Pipeline

`just generate` runs `go run ./internal/cmd/smithygen` with these steps:

1. Load each AST with `UseNumber`. Fail on unknown shape types, `mixins`, `apply` and `set` (smithy-models.md finding 1).
2. Walk the service closure through `operations` **and** `resources`. Lambda has 72 of its 88 operations only under resources (smithy-models.md finding 5).
3. Compute effective member traits (member over target).
4. Classify every trait ID as honor, generation-only or ignore. **Fail on an unclassified trait**, so a model bump that adds a trait breaks `go generate` instead of silently losing fidelity.
5. Apply the overlay (4.3).
6. Emit `internal/service/<svc>/<svc>api/`:
   - `types.go`: one struct per structure, union, enum and error shape. Members are pointers where absence matters (`NullableInteger` and similar) and values where the model has a `default`. Every wire trait goes in one `aws` tag: `aws:"QueueUrl,required" ` or `aws:"Metadata,header=x-amz-meta-,map"` or `aws:"Attribute,xmlName=Attribute,flattened"` or `aws:"Expires,ts=http-date"`. Enums become `type QueueAttributeName string` plus constants and a `Valid()` method.
   - `ops.go`: the `ServiceDesc` and one `api.Op[In, Out]` var per operation, with HTTP binding, error list, IAM actions and the constraint-error shape.
   - `errors.go`: one `*api.ErrorShape` var per modeled error and a constructor `NewQueueDoesNotExist(msg string) *api.Error`.
   - `validate.go`: `Validate` and `Identifiers` per input. Patterns are compiled once as package vars after translation (`\uXXXX` to `\x{XXXX}`, surrogate removal, `\p{Print}` replacement). The 7 lookaround patterns that RE2 cannot express become calls to hand-written funcs that the overlay names (smithy-models.md section 6.2).
   - `routes.go` (S3 and Lambda only): the REST dispatch table, ordered by specificity. For S3 that means `(method, scope, query keys present, required header)` rows with `x-id` stripped (wire-rest.md section 3.2). For Lambda it is the Smithy specificity order.
7. Emit `internal/wire/compliance/<protocol>/` test-only packages from the protocol-test AST, using the same emitters, so the synthetic test services get the same kind of structs the real services get.
8. Run `go/format`, sort everything, keep timestamps out, and put the pinned commit in the header. Each file starts with `// Code generated by smithygen from api-models-aws@1cfdfb4; DO NOT EDIT.`. Write to a temp file and rename it.

CI runs `just generate && git diff --exit-code` (go-toolchain.md section 15).

### 4.3 Generated versus hand-written

| Generated (checked in) | Hand-written |
|---|---|
| Input/output/error structs with wire tags | Every handler and all service state |
| Operation and service descriptors, IAM action lists | The six codecs and the XML writer |
| Error shapes with namespace/status/query code | S3 error table (about 80 codes, the not-configured map), SQS `InvalidParameterValue`/`MissingParameter`, common and throttle errors (error-matrix.md H5-H8) |
| Required/length/range/pattern/enum validators | Semantic validation: all SQS limits (the model has none), Lambda runtime list and documented limits, DynamoDB name rules |
| REST route tables for S3 and Lambda | The front-door classifier; S3 vhost/Control handling |
| Compliance-test structs | Event Ruler matcher, log patterns, checksums, aws-chunked, SigV4, CBOR, envelopes |
| Coverage data (ops per service) | The coverage report renderer |

`gen/overlays/<svc>.json` holds only what the generator needs, as data:

```json
{
  "protocolsAdd": ["awsQuery"],
  "xmlNamespace": "http://queue.amazonaws.com/doc/2012-11-05/",
  "errorNamespace": "com.amazonaws.sqs",
  "statusOverrides": {"KMSInvalidStateException": 400},
  "constraintError": {"default": "InvalidParameterValue", "SendMessage": "MissingParameter"},
  "patternFuncs": {"com.amazonaws.lambda#Alias": "validAliasName"},
  "bodyLimit": 8388608
}
```

The rule for contributors is short: **if the generator needs it, it goes in the overlay; if a handler needs it, it is Go code in the service.**

### 4.4 Why reflection codecs instead of generated serializers

Generated per-shape serde, as aws-sdk-go-v2 does it, means six code emitters, roughly 10x the generated volume, and a generator change for every codec fix. A tag-driven reflective codec means one codec per protocol (each a few hundred lines), with type plans cached per `reflect.Type` on first use. A codec bug is then fixed in one place and proven by the compliance suite. Handlers stay fully typed. Reflection is confined to `internal/wire`, never touches handler code, and is covered by rapid round-trip properties per codec. Speed is not a phase 1 constraint: a local emulator handles at most hundreds of requests per second, and reflection-plan walks cost microseconds. If profiling ever says otherwise, the plan cache is the seam where specialized encoders can go, with no handler change.

## 5. Protocols

All codecs implement one interface, so the router does not care which protocol it is driving:

```go
package wire

type Codec interface {
	Resolve(r *http.Request, svc *api.ServiceDesc) (*api.OpDesc, *api.Error)
	Decode(r *http.Request, op *api.OpDesc, in any) *api.Error
	Encode(w http.ResponseWriter, op *api.OpDesc, out any)
	EncodeError(w http.ResponseWriter, svc *api.ServiceDesc, err *api.Error)
}
```

| Codec | Services | Specifics to get right (source) |
|---|---|---|
| `awsjson` | SQS, DynamoDB, CloudWatch (1.0); Logs, Secrets Manager, KMS, EventBridge (1.1) | Accept empty body, `{}`, unknown members, `charset` params, gzip (`PutMetricData`). Emit integers without `.0`, epoch-second number timestamps, padded base64 blobs, `{}` for any modeled output. `__type` namespaced for 1.0 (overlay), bare for 1.1. Message member spelled as modeled. `x-amzn-query-error` for SQS and CloudWatch (wire-json-query.md items 6-12). Built on `encoding/json/jsontext`, with no reliance on v2 defaults for time or nil |
| `awsquery` | SNS, IAM, STS, SQS (legacy), CloudWatch (legacy) | Indexed keys sorted numerically (`member.10` after `member.2`), `+`/`%XX`, empty list keys, `GET` form. Response `<OpResponse xmlns><OpResult>...</OpResult><ResponseMetadata>` always, even for no-output ops. Flattened lists contiguous, lowercase booleans, ms timestamps, **percent-encoded IAM policy documents** (wire-json-query.md items 8-9, iam-sts-kms.md item 11). SQS on a queue path takes the queue from the path |
| `restxml` | S3 | Owned streaming XML writer (stdlib `encoding/xml` silently replaces control characters and cannot place `xsi:` prefixes), plus NEL/LS escapes. `encoding/xml` only for request bodies, with namespace-free tags. Unwrapped `GetBucketLocation`. `encoding-type=url` on every key field. Error root `<Error>` (go-toolchain.md section 10, wire-rest.md implications 6-13) |
| `restjson` | Lambda | Labels decoded before matching (`:` arrives as `%3A`). `httpResponseCode` members. Invoke body opaque with any Content-Type. `X-Amzn-Errortype`. 201/202/204 per op (wire-rest.md section 4) |
| `rpcv2` | CloudWatch | Route `POST /service/GraniteServiceVersion20100801/operation/<Op>` + `Smithy-Protocol: rpc-v2-cbor`. Reject `X-Amz-Target`. **Empty body for `Unit` output** (any member panics the Go client). Errors never empty. Tag-1 timestamps. Decoder accepts indefinite lengths and half floats, caps depth and declared lengths. About 340 lines (wire-cbor.md sections 3, 5, 7) |

CloudWatch is served by three codecs and SQS by two. The handlers are written once, because codecs are chosen per request and handlers never see them.

## 6. Front-door routing

`internal/server/route.go` is a single function with the order taken from wire-json-query.md item 1, wire-rest.md section 5 and landscape.md's traps. It never uses `http.ServeMux` for AWS traffic, because ServeMux redirects `//`, `.` and `..` and loses `%2F` (go-toolchain.md section 5). It routes over `r.URL.EscapedPath()` and `RequestURI`:

1. Path prefix `/_understudy/` on the main listener only -> admin API (a `ServeMux` is fine here). No S3 bucket can collide, because bucket names cannot contain `_`.
2. Host `<id>.lambda-url.<region>.<base>` -> function URL data plane.
3. Path `/v20180820/` with Host `<account>.<base>` -> S3 Control tagging (`ListTagsForResource`, `TagResource`, `UntagResource`), which Terraform needs for every bucket tag read (s3.md finding 1, tf-storage.md item 1).
4. Host `<bucket>.<base>` where `<base>` is a configured S3 base domain and the label is DNS-valid -> S3 virtual-hosted.
5. Parse auth. `Smithy-Protocol: rpc-v2-cbor` -> rpcv2.
6. `X-Amz-Target` present -> awsjson; the service comes from the target prefix (`AmazonSQS`, `DynamoDB_20120810`, ...).
7. `application/x-www-form-urlencoded` POST, or GET with `Action` -> awsquery; the service comes from the SigV4 credential scope, checked against `Version` (which also decides the service for unsigned requests).
8. Otherwise REST. SigV4 service `lambda` -> Lambda routes. Anything else, including unsigned and presigned requests -> S3 path-style.

Signing is a sum type: `SigV4Header | SigV4Query | SigV2Header | SigV2Query | Anonymous`. It is always parsed, because account and region routing need it, and verified only with `--sigv4=verify` (sigv4-checksums-endpoints.md items 1-3).

## 7. S3 addressing and the DNS story

- **No DNS server in phase 1.** The binary must run offline and as a static file. A wildcard DNS responder is a second network service, and it can be added later behind an ADR.
- **Base domains are configuration, not heuristics.** The default list is `localhost`, `s3.localhost`, `s3.<region>.localhost` and the AWS names. A request is virtual-hosted iff its Host minus port ends in `.` + a base domain, with a non-empty DNS-valid prefix. Anything else, including IP literals, is path-style (wire-rest.md item 3).
- **What users see.** boto3 and the AWS CLI use path-style against a custom endpoint. Go v2 and JS v3 use `bucket.localhost:PORT`, which resolves where systemd-resolved or `nss-myhostname` is present (this dev host) and fails elsewhere (sigv4-checksums-endpoints.md section 6). `understudy env --sdk go|python|js|terraform` prints the exact variables, including `UsePathStyle`/`forcePathStyle`. `understudy doctor` reports whether `*.localhost` resolves on this host.
- **Terraform** must use `http://localhost:PORT`, not `127.0.0.1`, because the S3 Control tag read prefixes the account id to the authority (s3.md finding 1). `understudy env --sdk terraform` says so.
- **Inside Lambda containers** the injected `AWS_ENDPOINT_URL` is an **IPv4 literal** (the gateway listener's address), which forces path-style in every SDK and needs no DNS (lambda-runtime-contract.md 6.2).
- **Cross-region buckets** are lenient: a bucket created in region A is reachable from a request signed for region B, and `x-amz-bucket-region` reports A. Mimicking AWS's 301 would break Go and JS for no local benefit (wire-rest.md item 11). Recorded as a known deviation.
- **Key-to-disk safety**: object bodies live in `blob/` under content-addressed names (`sha256/<hex>`) inside an `os.Root`. An S3 key never becomes a path component (go-toolchain.md section 8.2).

## 8. Services: shape of a service package

Every service has the same files, so reading one teaches all of them:

```
internal/service/sqs/
  service.go     // Service struct, New, Desc, Register, Start/Close/Reset, Save/Load, Resources
  state.go       // persisted types with json tags, stateVersion, migrations
  queues.go      // CreateQueue, DeleteQueue, GetQueueUrl, attributes, tags, permissions
  messages.go    // Send/Receive/Delete/ChangeVisibility (+Batch), long poll
  fifo.go        // group ordering, dedup
  redrive.go     // DLQ moves, move tasks
  limits.go      // hand-owned validation (the model has no constraints)
  errors.go      // hand-owned shapes: InvalidParameterValue, MissingParameter
  md5.go         // body and attribute MD5 (golden vectors from sqs.md 6.3)
  *_test.go      // behavior tests through api.Invoke inside synctest bubbles
  sqsapi/        // generated
```

### 8.1 Adding an operation (the afternoon task)

1. The operation already exists in `sqsapi`, generated, and currently answers `NotImplemented`.
2. Write `func (s *Service) listQueueTags(ctx context.Context, c *api.Call, in *sqsapi.ListQueueTagsInput) (*sqsapi.ListQueueTagsOutput, *api.Error)`. Lock, read, unlock, return.
3. Add `api.Handle(r, sqsapi.ListQueueTags, s.listQueueTags)` to `Register`.
4. Write a test that calls it with `api.Invoke` and asserts AWS-observable behavior.
5. `understudy coverage` picks it up, and the conformance suites exercise it over every protocol the service speaks, with no codec work.

### 8.2 Adding a service

1. Vendor the model (`cmd/vendormodels -add <dir>`), add `gen/overlays/<svc>.json`, and run `just generate`.
2. Copy the skeleton of the smallest existing service (STS), add it to `newServices`, and implement handlers one at a time.
3. Add a conformance module case per SDK.

### 8.3 Identity: accounts and principals

`sigv4` returns the access key. The IAM service owns a key table (static keys from `CreateAccessKey`, session keys from `AssumeRole` and Lambda environments), and the front door resolves keys in this order:

1. A key in the table -> its principal and account.
2. A 12-digit numeric key -> `Root{that account}` (the LocalStack convention).
3. Anything else -> `Root{000000000000}`.

Real-looking `AKIA`/`ASIA` keys are **not** decoded with the bit-mask formula. Decoding would quietly map a developer's production key to a foreign account number (iam-sts-kms.md item 6). Keys that understudy mints still encode their account with that formula, so they look right in tools that decode them. This policy is ADR material. Recording the access for policy generation uses the resolved principal.

## 9. Lambda execution

### 9.1 Decision: understudy hosts the Runtime API; RIE is not used

The RIE drops `X-Amz-Function-Error`, returns timeouts as 200 text, hard-codes the invoked ARN and crashes on a second concurrent invoke (containers.md finding 1, lambda-api.md findings 1-3). The official base images skip the RIE when `AWS_LAMBDA_RUNTIME_API` is set and `exec` the runtime directly (observed). understudy therefore runs **AWS's official base images and their official runtime interface clients** and serves the Runtime API itself.

This deviates from the brief's wording "with the Runtime Interface Emulator" only in who serves `2018-06-01`. It needs an ADR and a human sign-off. The benefits are correct error shapes, correct ARNs and deadlines, non-AWS base images working without modification, and about 600 lines of HTTP handling that we own instead of an upstream binary whose bugs we would have to work around.

### 9.2 Moving parts

```
internal/docker      Client{Ping, ImageInspect, Pull, ContainerCreate/Start/Wait/Remove/Logs, Events, Networks}
internal/lambdart    Executor: Invoke(ctx, Invocation) (Result, error); pool of Environments
internal/service/lambda   control plane, Invoke API, async queue, ESM pollers, function URLs
```

`lambdart.Executor` is an interface, the one test seam in this area. Unit tests use an in-process fake whose "functions" are Go funcs. A `-tags docker` integration suite uses real containers (labelled `understudy.dev/test=1`).

An environment is a state machine with illegal states unrepresentable: `Starting -> Idle <-> Busy -> Draining -> Gone`, and `Starting|Busy -> Faulted -> Gone`. Each environment has:

- **One Runtime API listener on an ephemeral port**, bound to the gateway address (9.3). A path prefix is unsafe for the Python RIC and source-IP demultiplexing fails on Docker Desktop (containers.md 4.5). `/next` always sends `Content-Type: application/json` (the Python RIC segfaults without it). It decodes chunked bodies and trailers (aws-lambda-go). Every `/error` maps to `X-Amz-Function-Error: Unhandled`.
- **One container**, created with `Mounts` (never `Binds`, which silently creates host dirs), code read-only at `/var/task` from a content-addressed extraction directory, labels `understudy.dev/{managed,instance,owner,function,account,region,generation}`, `Memory=MemorySwap`, `PidsLimit`, `ExtraHosts localhost:host-gateway` on Linux and Podman, and the environment checklist of lambda-api.md item 7.
- **One log follower**: a `logs?follow=1` stream, demultiplexed and forwarded to CloudWatch Logs via `api.Invoke(logsapi.PutLogEvents)` with `Internal{Caller: "lambda"}` origin (bypassing the client time windows). START/END/REPORT are synthesized by understudy, with a short settle barrier after `/response` so the last lines are attributed to the right invocation (containers.md finding 9).

Pool rules (containers.md 6.3): an exclusive reservation per invocation. The key hashes everything in the container spec. Updates bump a generation, and idle old-generation environments drain at once. A start semaphore (default 16) bounds container creation. Idle reaping uses mono time. A timeout, crash or OOM kills the container (`DELETE ?force=1&v=1`) and the next invoke is cold. Crash detection comes from one Docker `events` subscription filtered by the instance label.

Zip handling: extract once per `CodeSha256` into `blob`'s extraction area. Reject symlink entries and non-`IsLocal` names with a typed error before writing, cap total declared size at 262,144,000 bytes, apply `io.LimitReader` per entry, and write through `os.Root`. `//go:debug zipinsecurepath=0` in `main` is a second layer (go-toolchain.md item 5).

Hot reload: `CreateFunction` with a code location under a reserved bucket name (the LocalStack convention `hot-reload`, pointing at a host directory) mounts that directory read-only instead of extracting a zip. Each invoke checks the directory's mtime, and a change bumps the generation. The exact user-facing form is an implementation detail for the Lambda owner.

Images: pulled on `CreateFunction` (state `Pending`), never on first invoke. Pulls are single-flight, use anonymous registries only, and record the digest.

### 9.3 Container networking

The main listener stays on `127.0.0.1` (brief default). Containers cannot reach loopback (containers.md finding 4), so `serve` opens a **gateway listener** when Lambda is first used:

- **Linux, Docker or Podman rootful:** bound to the default bridge gateway IP (read from `GET /networks/bridge`) **at the same port** as the main listener. It serves the AWS API (not the admin API) and the per-environment Runtime API ports. Functions get `AWS_ENDPOINT_URL=http://<gateway-ip>:<port>`. Together with `ExtraHosts localhost:host-gateway`, a `localhost:PORT` URL minted outside the container (a Terraform output, a QueueUrl in a message) also reaches understudy from inside (lambda-runtime-contract.md 6.2 option D).
- **Docker Desktop and Podman machine:** containers reach the host via `host.docker.internal`, which Desktop resolves itself. The listener binds to the host's non-loopback interface as Desktop requires (UNVERIFIED; `doctor` checks it).
- **Emulator itself in a container:** function containers join the emulator's network and use its IP.
- **Rootless Docker:** unsupported in phase 1, with a specific error from `doctor` and on the first cold-start timeout.

A missing first `GET /next` within the 10 s init limit is the precise signal for an unreachable listener. The error names the address handed to the container, the detected topology and the three likely causes (containers.md 8.4). SQS `QueueUrl` echoes the request Host, so a function calling `GetQueueUrl` gets a URL it can reach.

### 9.4 Invoke and the clock

`RequestResponse` (200 + `X-Amz-Executed-Version`), `Event` (202, async queue with 60 s and 120 s retries and a 6 h max age on the **emu** clock), `DryRun` (204). Deadline, timeout and durations follow 2.6: `Lambda-Runtime-Deadline-Ms = wall dispatch + Timeout`, timeout enforced on mono. A frozen or scaled emulator clock never shortens a function's real execution budget, which is the failure the G4 research observed with aws-lambda-go.

The function state machine runs on the emu clock with a short configurable delay (default 10 ms): `Pending -> Active`, and `LastUpdateStatus InProgress -> Successful`, with exact `ResourceConflictException` texts while transient (lambda-api.md item 3). Terraform's own 5 s waiter delay is the floor regardless.

### 9.5 SQS event source mappings

One poller goroutine per `Enabled` mapping. It is plain code in `service/lambda/esm.go` and calls SQS **through `api.Invoke` as the function's execution role**, which is what real Lambda does. The access recorder therefore attributes `sqs:ReceiveMessage`, `DeleteMessage` and `GetQueueAttributes` to the role, and the generated policy for that role is correct.

Per cycle (lambda-events.md items 11-17):

1. Call `ReceiveMessage` with `Max = min(BatchSize remaining, 10)` until the batch is full, the batching window closes on the emu clock, or the next record would exceed 6 MB.
2. Apply filters with `match` (Lambda allow-list). Non-matching messages are deleted without invoking.
3. Invoke synchronously through the executor.
4. Decide the outcome with the partial-failure matrix. Delete through `DeleteMessageBatch`. Failed ids stay invisible until the queue's own visibility timeout elapses. Redrive is SQS's job, triggered at the receive attempt that exceeds `maxReceiveCount`.
5. FIFO: at most one in-flight batch per `MessageGroupId`.

Scaled-clock hazard (lambda-runtime-contract.md open issue 2): at clock rate k, a held batch's visibility timeout elapses k times faster than the function runs. The poller therefore asks SQS to **pin** the messages it holds until it releases them. Pinning is a hand-declared internal operation (`sqs.PinMessages`, an `api.Op` registered with `api.Handle` like any other but absent from the model, so no codec can route to it). It still goes through dispatch and the journal. It is the only internal-only SQS behavior, and it is documented as an emulator artifact.

## 10. DynamoDB Local integration

`internal/service/dynamodb` is a **proxy with shims**, not a reimplementation.

- **Lazy start.** The `amazon/dynamodb-local` container (pinned by digest, `-inMemory -disableTelemetry`, `DDB_LOCAL_TELEMETRY=0`, published on `127.0.0.1:0`, labelled) starts on the first DynamoDB call, single-flight. Users who never touch DynamoDB never pull it.
- **License gate.** The first pull requires `--accept-dynamodb-local-license` (or the env var) and prints the notice in dynamodb-local-terms.md 5.1. Without it, DynamoDB calls return a clear AWS-shaped `ServiceUnavailable` that names the flag, and every other service keeps working. The README states that the DynamoDB service needs an AWS account under the Local license.
- **Partitioning** by rewriting `Credential=` to `<account>/<date>/<region>/dynamodb/aws4_request`, with the region taken from understudy's validated list and never from the client string (it becomes a filename under `-dbPath`).
- **Pass-through by default.** Operations Local supports are forwarded as raw bytes, without decoding and re-encoding, which keeps Local's exact item and expression semantics. This needs the one escape hatch in `api`: `api.HandleRaw(r, op, h)` for awsJson services. Dispatch still journals, injects faults and records access for raw handlers.
- **Before forwarding**, understudy itself answers malformed JSON, missing or wrong target, wrong Content-Type and missing auth, because Local answers these differently from real DynamoDB (error-matrix.md finding 6). It forwards `{}` for an empty body and strips `Accept-Encoding`.
- **Shim-owned operations**, using generated types: `TagResource`, `UntagResource`, `ListTagsOfResource`, `UpdateTimeToLive`, `DescribeTimeToLive`, `UpdateContinuousBackups`, `DescribeContinuousBackups`. Their state lives in the dynamodb service struct and is saved in snapshots.
- **Response rewriting** for `Create/Describe/Update/DeleteTable` and stream calls: `ddblocal`/`000000000000` ARNs become real ones, and `WarmThroughput`, `TableId`, `TableClassSummary` and `SSEDescription` are injected (without `WarmThroughput`, Terraform hangs for about 2.5 minutes). These responses are parsed into a `jsontext` value tree. `x-amz-crc32` is recomputed over every rewritten or synthesized 200 body.
- **TTL** runs entirely in the shim on the emu clock. It is never forwarded to Local, which keeps Local's wall-clock sweeper off. A sweeper goroutine wakes every emu-second with `clock.Wait`, scans enabled tables with a `FilterExpression` on the TTL attribute, and deletes with a condition that the item is still expired. It also exposes an admin "sweep now" hook.
- **Case-sensitive table names** (Local is case-insensitive): understudy maps each table name to a stable internal Local name and maps it back on the way out. ADR item.

## 11. Cross-service delivery

All delivery is `api.Invoke` of a public operation of the target, started asynchronously by a tiny helper that the source service owns:

```go
// delivery.Go runs f in a goroutine tracked for graceful shutdown and snapshot settling.
func (t *Tracker) Go(parent *api.Call, f func(ctx context.Context))
```

| Path | Mechanism |
|---|---|
| SNS -> SQS | `sqsapi.SendMessage` as `ServicePrincipal{"sns.amazonaws.com"}` with the AWS envelope (or the raw body plus attributes), in the target queue's scope from its ARN. FIFO topics deliver serially per (subscription, group). Standard topics use one goroutine per (message, subscription) |
| SNS -> Lambda | `lambdaapi.Invoke` with `InvocationType=Event` and the exact `Records[0].Sns` shape |
| SNS retries, DLQ | per-delivery `{attempt, nextAt}` on the emu clock, using the AWS-managed policy. Client-side failures (queue or function missing) go straight to the subscription DLQ through `sqsapi.SendMessage` |
| EventBridge schedules | one goroutine per scheduled rule: `for { clock.Wait(nextFire); fire }`, cancelled and re-armed when the rule changes |
| EventBridge -> Lambda/SQS/SNS | `lambdaapi.Invoke` (Event), `sqsapi.SendMessage`, `snsapi.Publish`, after `Input`/`InputPath`/`InputTransformer` |
| S3 notifications | after the object commit, `sqsapi.SendMessage`, `snsapi.Publish` or `lambdaapi.Invoke` (Event) with the `Records` shape. `s3:TestEvent` is sent when an SQS or SNS configuration is stored |
| S3/Secrets Manager -> KMS | synchronous `kmsapi.GenerateDataKey`/`Decrypt`, before the caller takes its lock |
| Lambda -> Logs | `logsapi.CreateLogGroup/CreateLogStream/PutLogEvents` from the log follower |
| Lambda ESM -> SQS | section 9.5 |

The SNS filter policy, EventBridge patterns and Lambda ESM filters share one matcher, `match`. It implements Event Ruler v2.2.0 semantics as a compiled ADT plus a pure `Match` function. Per-service allow-lists and limits are data, and the four named Ruler-versus-docs switches sit in one file (matching-engines.md section 10).

This gives one delivery mechanism, journaled hop by hop with causal links, fault-injectable at each hop, attributed in the IAM recorder to the right principal, and testable with no HTTP at all.

## 12. Admin API and CLI

The admin API lives under `/_understudy/v1/` on the main listener only. It speaks JSON in both directions, and errors are `{"error": {"code", "message", "hint"}}`.

| Endpoint | Purpose |
|---|---|
| `GET health` | version, uptime, model pin, Docker status, DynamoDB Local status |
| `POST reset` (optional `?service=`) | `Reset()` on services; drains Lambda containers |
| `GET/POST clock` | state; `{"freeze"}`, `{"resume"}`, `{"set":"RFC3339"}`, `{"advance":"1h30m"}`, `{"rate":100}` |
| `GET/POST/DELETE faults[/{id}]` | list, add (validated), remove |
| `GET journal?service=&op=&resource=&root=&since=&errors=&limit=` | query |
| `GET journal/stream?...` | NDJSON stream (works with `curl -N`, no SSE client needed) |
| `POST snapshots/save`, `POST snapshots/load` | archive bytes in the body, or a server-side path when `--persist` is set |
| `GET resources?service=&account=&region=` | `Resources()` of each service |
| `GET coverage` | ops implemented per service, compliance results, behavior switches |
| `GET iam/policy?principal=<arn>` | least-privilege policy JSON from the access record |
| `POST dynamodb/ttl-sweep` | run the TTL sweep now |

The CLI is `understudy <noun> <verb>`. Every command takes `--endpoint` (default `http://127.0.0.1:4566`) and `--json`, never prompts (the DynamoDB license is a flag), and exits non-zero with the admin error's message and hint:

```
understudy serve [flags]
understudy health | reset [--service s]
understudy clock [get|freeze|resume|set T|advance D|rate R]
understudy faults [list|add --service --op --error CODE --probability P --seed N|--next N|rm ID|clear]
understudy journal [query FILTERS|tail FILTERS]
understudy snapshot [save FILE|load FILE]
understudy resources [--service s]
understudy coverage [--markdown]        # runs offline from the registry when no server is up
understudy iam policy --principal ARN
understudy env [--sdk go|python|js|cli|terraform]
understudy doctor                        # Docker reachability, gateway listener, *.localhost resolution
understudy cleanup                       # remove labelled containers of dead instances
```

## 13. Configuration

Configuration comes from flags with `UNDERSTUDY_<FLAG>` environment equivalents only. There is no config file in phase 1. Parsing happens once into a typed `config.Config` with sum types where a flag has modes:

| Flag | Default | Notes |
|---|---|---|
| `--listen` | `127.0.0.1:4566` | 4566 is LocalStack's port, so existing configs migrate unchanged. A non-loopback address requires `--admin=off` or `--admin-token` (the admin API is otherwise unauthenticated) |
| `--persist` | unset | unset = ephemeral (blobs in a temp dir, removed on exit); `--persist=DIR` = load at start, autosave |
| `--load-snapshot` | unset | load an archive at start (mutually exclusive with `--persist`; parse-time error) |
| `--seed` | random | seeds `ids` and the default for fault rules; logged at start |
| `--clock` | `running` | `running`, `frozen`, or `frozen@RFC3339`; `--clock-rate` default 1 |
| `--sigv4` | `parse` | `parse` or `verify` |
| `--s3-base-domain` | `localhost` | repeatable |
| `--lambda` | `docker` | `docker` or `off` (Lambda control plane only, Invoke returns an AWS-shaped error that says why) |
| `--lambda-gateway` | `auto` | `auto`, `off`, or an explicit IP |
| `--dynamodb` | `local` | `local` or `off`; plus `--accept-dynamodb-local-license` |
| `--faults` | unset | JSON file of rules loaded at start |
| `--journal-capacity`, `--journal-file` | 100000, unset | |
| `--log-level`, `--log-format` | `info`, `json` | `slog` to stderr |

## 14. Testing strategy

| Layer | What it proves | Where | Tools |
|---|---|---|---|
| Pure units and properties | parsers, matchers, encoders, checksums | `match`, `logpattern`, `checksum`, `awschunked`, `sigv4`, `wire/*`, `rpcv2` CBOR, SQS MD5, schedule parser, input transformer | `rapid`, golden vectors from the research files, committed failfiles |
| Protocol compliance | each codec against the Smithy corpus: awsJson1_0 26+23, awsJson1_1 58+45, awsQuery 35+31, restJson1 137+92, restXml 101+77, rpcv2Cbor 68 | `internal/wire/compliance/` (generated structs + one table-driven runner per protocol) | checked-in skip list with a reason per id. Message text in Smithy validation cases is asserted as expected-to-differ (error-matrix.md finding 2) |
| Service behavior | AWS semantics (visibility, FIFO prefix blocking, redrive, filter policies, state machines, TTL) | `internal/service/<svc>/*_test.go` | `api.Invoke` inside `synctest.Test` bubbles. No sockets, so a 20 s long poll or a 1 h visibility timeout runs in microseconds. Clock-API tests call `Freeze`/`Advance` |
| Wire goldens | exact bytes real SDKs send and expect | `internal/server/testdata/` | request/response vectors captured in research (wire-cbor.md section 8, sigv4 section 3.2, S3 snapshots). HTTP tests use `httptest.NewTestServer` inside bubbles |
| Conformance | real SDKs and tools agree | `conformance/{go,python,js,cli,terraform}` modules | each runs against `understudy serve` on an ephemeral port via `scripts/sealed` |
| Terraform | apply, `plan -detailed-exitcode == 0`, update, plan, destroy for every phase 1 resource, against provider v6.68.0, plus a canary against latest | `conformance/terraform` | per-test account isolation (fakecloud's lesson) |
| End to end | the target pipeline: SNS -> SQS (filter, raw) -> ESM -> Lambda (Go, Python, image) -> S3/DynamoDB lock/Secrets/Logs; schedule; function URL | `conformance/e2e` | asserts on the journal's causal chain as well as outputs |
| Parity replay | understudy versus human-recorded AWS responses | `testdata/parity/` with provenance per fixture | replay only. The record tool exists; agents never run it (backlog: s3-checksum-residuals-and-record-backlog.md section 2) |
| Docker integration | containers, pool, crash recovery, cleanup | `-tags docker` | label `understudy.dev/test=1`, unique names, removed by the test |

Behavior tests do **not** go over HTTP with a hand-rolled client. That would encode requests with our own codecs and hide symmetric bugs. Wire correctness is the job of the compliance suite, the goldens and the real-SDK conformance modules. `go vet`, `golangci-lint` (strict config from go-toolchain.md section 13, `depguard` stdlib-only, `forbidigo` on `time.Now`/`time.Sleep` outside `clock`) and `go test -race` gate every change.

## 15. Security

- The main listener binds `127.0.0.1` by default. A non-loopback `--listen` requires an explicit admin decision (section 13).
- The gateway listener binds to the bridge gateway IP, never `0.0.0.0`, and serves no admin API. Per-environment Runtime API ports only accept the environment's own invocation ids, and late replies after a timeout get `400 InvalidInvocationId`.
- Body caps are per service (`ServiceDesc.BodyLimit` from the overlay). S3 streams to disk with the 5 GiB single-PUT cap. Lambda `CreateFunction` allows about 70 MB. DynamoDB returns 413 above 16 MiB. `MaxHeaderBytes` and `MaxHeaderValueCount` are set on the server.
- S3 keys never touch the filesystem. Blobs are content-addressed under `os.Root`.
- Zip-slip is handled as in 9.2.
- Containers: every one is labelled, removed on graceful shutdown, and swept at startup by an instance lock plus label, with `understudy cleanup` as the manual path. The README states that Docker socket access is root-equivalent.
- No secrets in logs: generated `sensitive` member lists drive redaction in the journal and in `slog` (`LogValuer` on every type that can hold a secret or credential). `SecretString`, `SecretBinary`, `Plaintext`, `SessionToken` and `SecretAccessKey` are never logged.
- No telemetry. The only network access is image pulls, and DynamoDB Local runs with both telemetry opt-outs and an egress test.
- KMS key material always comes from `crypto/rand`, even when `--seed` is set. IDs are seedable; keys are not.

## 16. Top risks and mitigations

| # | Risk | Mitigation |
|---|---|---|
| 1 | Reflection codecs mishandle an edge case (sparse lists, nested flattened XML, defaults) | Compliance suite per protocol, rapid round-trip per codec, and the real-SDK conformance modules. The plan cache is the single seam if a hand-specialized path is ever needed |
| 2 | Error-text and read-back fidelity has a long tail (Terraform perpetual diffs, crash-prone missing dates in tf-remaining.md finding 1) | Error and default tables are data with tests. A Terraform second-plan-empty gate covers every resource from the first slice. UNVERIFIED cells stay tagged and show in `coverage` |
| 3 | Lambda networking varies by topology (Desktop, rootless, Podman, firewalls) | Linux plus Docker is the tested path in phase 1. `doctor`, precise cold-start diagnostics, and `--lambda-gateway` override. Rootless is declared unsupported with a clear error |
| 4 | DynamoDB Local license or availability changes | Lazy start, explicit acceptance, digest pin with an ECR Public fallback, and the rest of the emulator keeps working when DynamoDB is off. Counsel review before release |
| 5 | Deadlock or contention from internal calls | The "never hold your lock across Invoke" rule, the `lockcheck` build tag in CI, and one mutex per service. Snapshot is the only multi-lock path, with a fixed order |
| 6 | Scaled or frozen clock produces impossible interleavings (ESM visibility, long polls, PutLogEvents windows) | The clock-domain contract (2.6), wall caps on long polls, ESM message pinning, and the wall-OR-emu log window. A synctest suite runs every timing behavior at rates 0, 1 and 1000 |
| 7 | Scope: 12 services, 6 protocols, real containers | Tracer-bullet order (section 18). Every unimplemented operation is honest (`NotImplemented`), and coverage is visible from day one |
| 8 | `*.localhost` is not universal, which breaks Go/JS virtual-hosted S3 | `env` and `doctor` tell users the exact knob. Lambda uses IP literals. A DNS responder stays an ADR-gated follow-up |
| 9 | Terraform provider floors (lifecycle about 55 s, SQS waits about 25-42 s, Lambda 5 s) make CI slow | Parallel per-account Terraform cases, documented floors, and tests that never wait on the emulator clock for these |
| 10 | Snapshot schema drift breaks old files silently | Per-service versions, migrations or explicit refusal, round-trip tests, and a refusal of newer formats |
| 11 | Upstream model churn | Pinned commit, `-verify` in CI, and a generator that fails on unclassified traits. Pin bumps are deliberate and review the coverage diff |

## 17. What this proposal deliberately does not build

| Not built | Why |
|---|---|
| Per-shape generated serializers | six emitters and roughly 10x the generated code for no observable benefit at local scale (4.4) |
| A storage engine, event sourcing or a database | plain structs plus a mutex plus a JSON snapshot are enough for a local emulator, and they keep state readable |
| Actors or a goroutine per resource for state | locks are simpler to read, and goroutines exist only for side effects |
| HTTP loopback for internal calls | LocalStack's poller pins gateway threads and bends the protocol (landscape.md item 6). `api.Invoke` gives the same journal, fault and access behavior in-process |
| Wiring interfaces between services | importing the generated `<svc>api` package plus `api.Invoke` avoids cycles with no glue code |
| The RIE | see 9.1 |
| A DNS server | offline and static-binary requirement; ADR-gated follow-up |
| A config file | flags plus env cover phase 1, and a file format is a compatibility promise |
| A plugin or extension system | contributors add code to the repo |
| A central timer heap | `clock.Wait` per waiter is simpler and correct under freeze, advance and scale |
| Journal persistence in snapshots | the journal is history, and `--journal-file` covers CI artifacts |

## 18. Build order (tracer bullets)

1. **SQS over awsJson1_0, end to end**: vendored model -> generator (types, ops, errors, validators) -> `awsjson` codec + compliance runner -> front door -> dispatch with journal -> clock -> `CreateQueue/SendMessage/ReceiveMessage/DeleteMessage` with long poll -> boto3 and Go conformance smoke. Every core abstraction exists after this step.
2. SQS complete, plus awsQuery for SQS, plus faults and admin/CLI for clock, faults, journal, reset and coverage.
3. STS + IAM (awsQuery codec reused, key table, access recorder, policy output) -> Terraform provider bootstrap works.
4. SNS (filter engine `match`, envelopes, delivery via `api.Invoke`, retries, DLQ) -> first causal chain in the journal.
5. KMS, Secrets Manager, Logs (`logpattern`), EventBridge (schedules, targets), CloudWatch with all three codecs including rpcv2 CBOR.
6. S3 (restXml, router, checksums, aws-chunked, multipart, not-configured table, S3 Control tags, SSE-KMS, notifications).
7. Lambda control plane + `lambdart` + gateway listener + ESM + function URLs.
8. DynamoDB proxy with shims and the license gate.
9. Snapshots and persistence across all services, then the e2e pipeline suite.

## 19. Decisions that need ADRs

1. Reflection-driven codecs over generated serializers (4.4).
2. Hosting the Runtime API instead of the RIE (9.1), a deviation from the brief's wording.
3. The gateway listener beyond 127.0.0.1 for container traffic (9.3).
4. Account-from-access-key policy (8.3).
5. `NotImplemented`/501 for unimplemented operations (2.3.1).
6. In-repo xxHash port (s3-checksum-residuals-and-record-backlog.md 1.7), so all ten S3 checksum algorithms are implemented.
7. `pgregory.net/rapid` (MPL-2.0, test-only) and the Service Reference action table's license.
8. The DynamoDB Local license gate, the README caveat on "no account", and case-sensitive table-name mapping.
9. Snapshot format v1 (2.10) with state structs as the schema.
10. No DNS server in phase 1, with base-domain configuration and `env`/`doctor` guidance (7).
11. Lenient cross-region S3 bucket access (7).
12. ESM message pinning as an internal-only SQS extension (9.5).
