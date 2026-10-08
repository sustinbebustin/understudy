# Proposal: understudy phase 1 architecture, platform and developer experience angle

Status: proposal (one of several competing architecture proposals). Date: 2026-10-08.
Angle: the clock, causal journal, fault injection, snapshots, hot reload, Terraform-first workflow, Lambda container orchestration, CLI and admin API are designed into the core, not attached to it. The test of every decision below is: does a developer trust understudy's answer in CI, and can they find out why when it surprises them?

Inputs: [phase1-brief.md](../../brief/phase1-brief.md) and every file in [docs/research/phase1/](../../research/phase1/). Research files are cited by name and section; claims marked UNVERIFIED there stay UNVERIFIED here.

---

## 0. The bet in ten lines

1. **One pipeline for every call.** An HTTP request from boto3 and an SNS-to-SQS delivery inside the process run through the same `Kernel.Invoke` stages: identity, validation, fault rules, handler, access recorder, journal. Only the wire codec differs. Every platform feature works for internal traffic for free.
2. **Time is an injected dependency with three typed domains:** emulator time (`clock.EmuTime`), wall time (signers, containers, deadlines) and monotonic durations. The compiler and `forbidigo` keep them apart.
3. **Timers are a function of state.** No closure lives only in a timer heap. Every scheduled thing (visibility expiry, rule fire, TTL sweep, secret purge, ESM poll) is re-derivable from service state, so snapshots are plain data and restore re-arms everything.
4. **The journal is the debugger.** Every call, delivery, invocation, timer firing, injected fault and clock change is an event with a parent and a trace id. "Why didn't my Lambda run?" is one CLI command.
5. **Faults are a pipeline stage, not handler code.** Rules resolve error codes against each service's real error catalogue at creation time, so a rule cannot inject an error AWS would never send.
6. **Determinism where it is cheap:** seeded ID generation, ordered timer firing, deterministic snapshot bytes. Same seed plus same call order gives the same journal and the same snapshot file.
7. **Honest coverage.** Every operation is `Full`, `StoredOnly`, `Partial` or `Unimplemented` at its registration site; every behavior chosen without AWS-recorded evidence is a named switch with provenance. `understudy coverage` prints both.
8. **Lambda runs on understudy's own Runtime API** inside AWS's official base images, one listener per execution environment. The RIE is not on the execution path (ADR required; this deviates from the brief's wording).
9. **Zero-config happy path, `understudy doctor` for the rest.** `AWS_ENDPOINT_URL=http://localhost:4566` plus any credentials works for SDKs, the CLI and Terraform on Linux. Every topology-dependent failure has a doctor check and an error message that names the fix.
10. **Terraform read fidelity is an API-layer contract,** asserted per resource through the journal (call sequences) as well as by `plan -detailed-exitcode`.

---

## 1. Package layout

One module, `github.com/sustinbebustin/understudy`, standard library only (test-only `pgregory.net/rapid`). Packages are small and centered on one type; generated code sits in `api` subpackages next to the hand-written service.

```
cmd/understudy/                  main: builds the CLI tree, nothing else
admin/                           PUBLIC: admin API wire types + typed client (used by the CLI and by users)
understudytest/                  PUBLIC: start an in-process emulator from a Go test (section 12.4)

internal/
  cli/                           subcommands; each is an admin.Client call plus formatting
  config/                        Config type: parse JSON/env/flags at the boundary, validate, defaults
  server/                        `serve`: listeners, topology wiring, startup, graceful shutdown
  edge/                          HTTP front door: body caps, request ids, request claiming, unclaimed diagnostics
  auth/                          SigV4/SigV2 header and query parsing (sum type), opt-in verification, presign expiry
  identity/                      access key -> principal, credential minting, account policy
  scope/                         Scope{Partition, Account, Region}, AccountID, Region (parsed types)
  arn/                           ARN parse/format
  ids/                           seeded generators: request ids, message ids, receipt handles, UUIDs
  kernel/                        Call, Invoke pipeline, Service lifecycle, work tracker (settle)
  registry/                      service/operation descriptors, binding, coverage
  awserr/                        Error, Identity, Origin, Coral violation formatter, generic error catalogue
  fidelity/                      named behavior switches with provenance (the honesty ledger)
  clock/                         EmuTime, Clock modes, Scheduler
  wallclock/                     the only other sanctioned time.Now: Now, Stopwatch
  journal/                       Event model, ring store, indexes, query, stream fan-out
  fault/                         Rule, Match, Action, Trigger, seeded evaluation
  access/                        access recorder and least-privilege policy generation
  store/                         Gate, Cell[S], Partitioned[K,S], Tx effects, WaitKey wakeups
  blob/                          object bytes: memory and disk (os.Root) stores, content addressing
  snapshot/                      archive format, manifest, section readers/writers, migrations
  protocol/
    smithy/                      runtime schema descriptors, Serializer/Deserializer interfaces, ShapeID
    jsonx/                       the one wrapper around encoding/json/v2 + jsontext
    xmlw/                        streaming XML writer (requests are parsed with encoding/xml)
    cbor/                        stdlib CBOR subset (wire-cbor.md section 7)
    awsjson/ awsquery/ restjson/ restxml/ rpcv2cbor/   one codec per protocol
  checksum/                      all ten S3 algorithms incl. xxHash ports, CRC combine
  awschunked/                    aws-chunked decoder (four modes) as a streaming reader
  ruler/                         Event Ruler core: compile pattern ADT, Match; per-service allow-lists and limits
  logsfilter/                    CloudWatch Logs filter pattern compiler and matcher
  schedexpr/                     EventBridge rate/cron parser and next-fire computation
  docker/                        Docker Engine API client (unix socket, typed errors, stdcopy, events)
  topology/                      detect Linux bridge / Desktop / rootless / Podman / in-container; derive addresses
  aws/
    sqs/ sns/ s3/ dynamodb/ lambda/ logs/ secretsmanager/ kms/ iam/ sts/ events/ cloudwatch/
      api/                       generated: types, schemas, operations, validators, errors, binder
    lambda/runtimeapi/           per-environment Runtime API server (2018-06-01)
    lambda/pool/                 execution environments, reservation, generations, reaping
    lambda/launcher/             builds container create bodies (only place that knows mounts, env, labels)
    lambda/esm/                  event source mapping pollers
    lambda/hotreload/            directory watcher and code overlays
    dynamodb/ddblocal/           DynamoDB Local container lifecycle and licence gate
  admin/                         admin API server handlers (mounted under /_understudy/)
  cmd/gen/                       code generator (go run ./internal/cmd/gen)
  cmd/vendormodels/              vendor tool for api-models-aws (-verify offline; -update by a human)

third_party/api-models-aws/      12 verbatim models at 1cfdfb4 + UPSTREAM.json + LICENSE/NOTICE
third_party/smithy-protocol-tests/  flattened JSON AST of the protocol tests + pin record
codegen/overlays/<svc>.json      hand-owned generator inputs (error namespaces, codes, regex translations, tightened limits)
conformance/{go,python,js,cli,terraform,e2e}/   separate modules with their own manifests
parity/                          recorded fixtures with provenance + replay harness (separate module)
```

Dependency direction: `aws/*` depend on `kernel`, `store`, `clock`, `awserr`, `registry`, engines (`ruler`, `checksum`, ...). `kernel` depends on `registry`, `journal`, `fault`, `access`, `identity`, `clock`. Nothing depends on `edge` or `server`. `store` and `clock` depend on nothing in the repo (effects are injected), which keeps them testable under `testing/synctest`.

---

## 2. Core abstractions

### 2.1 Scope, identity and the per-call context

```go
package scope

// AccountID is exactly 12 ASCII digits. The zero value is invalid and never
// reaches a handler; the edge resolves one for every request.
type AccountID struct{ digits string }

func ParseAccountID(s string) (AccountID, error)

// Region is a member of understudy's region table. A client-supplied region
// string is never used raw: it becomes part of ARNs, URLs and (for DynamoDB
// Local) file names (dynamodb-local.md section 3).
type Region struct{ name string }

func ParseRegion(s string) (Region, error)

type Scope struct {
	Partition string // "aws" in phase 1
	Account   AccountID
	Region    Region // IAM and STS data are account-scoped; their cells ignore Region
}
```

```go
package identity

type Principal interface{ isPrincipal() }

type Root struct{ Account scope.AccountID }
type User struct {
	Account          scope.AccountID
	Path, Name, ID   string // ID is AIDA...
}
type AssumedRole struct {
	Account scope.AccountID
	Role    RoleRef // name, path, AROA id
	Session string
	Source  SessionSource
}
type Anonymous struct{}                        // unsigned; presigned requests are signed
type ServicePrincipal struct{ Name string }    // "sns.amazonaws.com" for internal deliveries

type SessionSource interface{ isSessionSource() }
type FromAssumeRole struct{}                   // sts:AssumeRole
type FromExecutionEnvironment struct {         // Lambda role credentials, see section 6.6
	Function arn.ARN
	Env      EnvID
}
```

Account resolution from an access key, in order (iam-sts-kms.md implication 6, sigv4-checksums-endpoints.md implication 14):

1. The key is in the credential store (minted by `CreateAccessKey`, `AssumeRole` or a Lambda execution environment): use its principal.
2. The key is 12 digits: that account's root (the LocalStack convention, so `AWS_ACCESS_KEY_ID=111111111111` selects an account with no setup).
3. Anything else, including real-looking `AKIA`/`ASIA` keys and `AKIAIOSFODNN7EXAMPLE`: the configured default account (`000000000000`).

Decoding the account bits of arbitrary real keys is refused on purpose: a developer's production key must never silently select a foreign account number. Keys understudy mints embed the account with the documented bit layout anyway, so they also decode correctly in other tools.

```go
package kernel

// Call is the context of one operation invocation, external or internal.
// Built by the edge or by Kernel.Invoke; read-only for the handler except for
// Touch.
type Call struct {
	ctx       context.Context
	ID        ids.RequestID       // x-amzn-RequestId / x-amz-request-id
	Scope     scope.Scope
	Principal identity.Principal
	Origin    Origin
	Op        *registry.Operation
	Event     journal.EventID     // this call's journal event
	Cause     journal.Cause       // parent event + trace; zero parent for roots
	Admitted  clock.EmuTime       // emulator time at admission; one timestamp per call
	k         *Kernel
	touched   []arn.ARN
}

func (c *Call) Context() context.Context
func (c *Call) Clock() *clock.Clock     // fresh reads for long polls
func (c *Call) Touch(resources ...arn.ARN) // feeds the journal and the access recorder

type Origin interface{ isOrigin() }
type External struct {
	Protocol   protocol.ID   // awsJson1_0, awsQuery, restXml, ...
	Listener   ListenerKind  // Main | Container
	RemoteAddr netip.AddrPort
	UserAgent  string        // parsed later into SDK name/version for the journal
}
type Internal struct {
	Via string // "sns.delivery", "lambda.esm", "events.target", "s3.notification", ...
}
```

`Admitted` is the single emulator timestamp a handler uses for `CreatedTimestamp`, `LastModified`, `SentTimestamp` and friends, so one call never writes two different "now" values.

### 2.2 Operation handler signature and binding

```go
package kernel

// Handler implements one AWS operation. Exactly two outcomes: output, or an
// AWS-shaped error. A panic is recovered at the pipeline boundary and becomes
// InternalFailure with the stack in the journal (never in the response).
type Handler[In, Out any] func(c *Call, in *In) (*Out, *awserr.Error)

// Op is a typed handle on a registered operation, generated per operation, used
// for internal calls (SNS delivering to SQS, the ESM deleting messages).
type Op[In, Out any] struct{ desc *registry.Operation }

// Invoke runs an internal call through the full pipeline (minus wire codec and
// SigV4). Go 1.27 generic method.
func (k *Kernel) Invoke[In, Out any](parent *Call, op Op[In, Out], target scope.Scope,
	as identity.Principal, via string, in *In) (*Out, *awserr.Error)
```

The returned error type is the concrete `*awserr.Error`, not `error`: a handler cannot return an un-AWS-shaped failure by accident. Internal bugs are wrapped explicitly with `awserr.Internal(err)`.

Generated binder (excerpt) and a hand-written service:

```go
// Code generated by understudy-gen from aws/api-models-aws@1cfdfb4bb9ea; DO NOT EDIT.
package sqsapi

var OpSendMessage = kernel.NewOp[SendMessageInput, SendMessageOutput](&descSendMessage)

type Binder struct{ s *registry.ServiceBuilder }

func (b Binder) SendMessage(h kernel.Handler[SendMessageInput, SendMessageOutput], cov registry.Coverage) {
	b.s.Bind(&descSendMessage, kernel.Erase(h), cov)
}
```

```go
package sqs

type Service struct {
	k      *kernel.Kernel
	queues *store.Partitioned[scope.Scope, State]
}

func Register(k *kernel.Kernel, b sqsapi.Binder) *Service {
	s := &Service{k: k, queues: store.NewPartitioned[scope.Scope, State](k.Store(), newState)}
	b.CreateQueue(s.createQueue, registry.Full)
	b.SendMessage(s.sendMessage, registry.Full)
	b.AddPermission(s.addPermission, registry.StoredOnly("statements stored in Policy; not enforced"))
	// Operations not bound here are registered as Unimplemented by the builder:
	// they answer in the protocol's own error format and appear in coverage.
	return s
}
```

### 2.3 Error model

```go
package awserr

type Error struct {
	ID      Identity
	Message string  // empty only where AWS sends no message (CloudWatch ResourceNotFound)
	Fields  []Field // extra wire members: S3 BucketName/Key/UploadId/ArgumentName,
	                // Lambda Type/Reason/retryAfterSeconds, KMS key ARN, ...
	Origin  Origin
}

// Identity is everything a codec needs to render the error on any protocol.
// Generated from the model, corrected by codegen/overlays (error-matrix.md
// section 7 rules D7-D10 and H1-H10).
type Identity struct {
	Shape      smithy.ShapeID // com.amazonaws.sqs#QueueDoesNotExist or a synthetic id for unmodeled codes
	JSONType   string         // __type exactly as AWS sends it (namespace rules H1)
	QueryCode  string         // awsQueryError code, else shape name
	Status     int
	Fault      Fault          // Sender | Receiver
	MessageKey string         // "message" or "Message", exactly as modeled
	Retryable  bool           // from the model's retryable trait or the throttling catalogue
}

type Origin interface{ isOrigin() }
type Modeled struct{}
type Generic struct{}                        // SerializationException, UnknownOperationException, auth family
type Validation struct{ Violations []Violation }
type Unimplemented struct{ Service, Operation string }
type Injected struct{ Rule string }          // fault rule id
type Internal struct{ Cause error }          // a bug: 500 InternalFailure; Cause is journaled, never sent

// Violation renders in the Coral grammar (error-matrix.md section 4):
// "Value 'x' at 'lowerCamel.path' failed to satisfy constraint: Member must ..."
type Violation struct {
	Path       Path
	Value      Rendered // Java toString-style rendering, blobs omitted
	Constraint Constraint
}
type Constraint interface{ isConstraint() } // NotNull | MinLen | MaxLen | MinValue | MaxValue | Pattern{Echo} | Enum{Values}
```

Rules that make errors trustworthy:

- **Every error a handler can produce is a generated or catalogued constructor**: `sqsapi.ErrQueueDoesNotExist(msg)`, `s3err.NoSuchBucketPolicy(bucket)`, `awserr.Throttling(svc)`. No string codes in handler code.
- **Validation code is per operation**, from overlay H2 (Lambda `runtime` gives `InvalidParameterValueException`, SQS single send without `MessageGroupId` gives `MissingParameter`). The formatter owns the Coral grammar and the per-service path style (indexed vs Lambda's rolled-up composite).
- **Unimplemented** answers 501 in the protocol's own envelope with code `NotImplemented` and the message `understudy does not implement <Service>.<Operation>. Run 'understudy coverage --service <svc>' for what is supported.` 501 sits outside every SDK's retry set (wire-json-query.md implication 15), so clients fail fast. The exact code string is an ADR item.
- **S3 owns its whole error table** in hand-written Go (error-matrix.md H8; wire-rest.md implication 6), including context elements and HEAD's bodyless form.
- **Check order** follows the observed Coral front end: unknown operation, auth, deserialization, constraint validation, business rules (error-matrix.md section 4, closing paragraph).

### 2.4 Service registration and coverage

```go
package registry

type Operation struct {
	Service  *Service
	Name     string               // "SendMessage"
	Shape    smithy.ShapeID
	IAM      []IAMAction          // primary (same-name or mapped) first; from the Service Reference table
	Traits   OpTraits             // ReadOnly, Idempotent, Paginated (informational)
	Input    *smithy.Schema
	Output   *smithy.Schema
	Errors   []*awserr.Identity   // modeled errors, for fault-rule validation
	HTTP     *HTTPBinding         // REST services only
	Coverage Coverage
	invoke   erasedHandler        // set by Bind; Unimplemented when never bound
}

type Coverage interface{ isCoverage() }
type full struct{}
var Full Coverage = full{}
type storedOnly struct{ Note string }   // accepted, stored, echoed; no runtime effect
type partial struct{ Note string }      // implemented with named gaps
type unimplemented struct{}

func StoredOnly(note string) Coverage
func Partial(note string) Coverage
```

`understudy coverage` is computed from this registry plus three checked-in result files, and reports **three separate numbers** (landscape.md "Traps": never sell shape conformance as parity):

1. Protocol compliance: Smithy cases passed / skipped-with-reason / failed per protocol.
2. Operations: Full / StoredOnly / Partial / Unimplemented per service, against the model's operation list.
3. Evidence: count of behavior switches by provenance (section 2.12) and parity fixtures by label.

`--format md` regenerates the docs page; CI fails if the committed page differs.

### 2.5 The pipeline

```
External request
  edge: body cap (per service) -> request id -> auth.Parse (sum type) -> identity.Resolve
        -> claim (section 4.1) -> codec.Decode
                      \
Internal call (Kernel.Invoke) --> admission -------------------------------+
                                                                             |
  admission:  journal.Begin(event)                                           |
              fault.Before(rule match)   -> injected error: skip handler     |
              validate (generated + overlays)                                |
  handler(c, in)                                                             |
              fault.After(rule match)    -> AfterCommit / 200-with-error     |
              access.Record(principal, op.IAM, touched)                      |
              journal.End(outcome, latency)                                  |
  edge: codec.Encode(output | error)                                         |
```

- Validation also runs for internal calls: it is cheap and catches bugs where a delivery builds an input AWS would reject.
- `fault.Before` runs before the handler, so an injected error has no side effects (what a client expects from a throttle). `AfterCommit` is the separate action for "the side effect happened but the client saw an error", the best idempotency test there is.
- The access recorder runs for successes and modeled failures, never for injected or internal errors.

### 2.6 State store and concurrency model

```go
package store

// Gate is shared by every cell in a kernel. Writes take the read side for the
// few microseconds they hold a cell; snapshot, reset and clock jumps take the
// write side and therefore see a consistent point in time.
type Gate struct{ mu sync.RWMutex }

type Cell[S any] struct {
	gate  *Gate
	run   EffectRunner // injected by the kernel; runs effects after unlock
	mu    sync.Mutex
	state S
	rev   uint64       // bumped per committed write; drives "dirty" for persistence
}

// Write gives exclusive access to the state. If fn returns an error the cell
// is unchanged: fn mutates a working value only when it must, and the
// convention (enforced by tests per service) is validate first, mutate last.
// Effects recorded on tx run after the lock is released, in order.
func (c *Cell[S]) Write[R any](fn func(s *S, tx *Tx) (R, *awserr.Error)) (R, *awserr.Error)
func (c *Cell[S]) Read[R any](fn func(s *S) R) R

type Tx struct{ effects []Effect }

func (tx *Tx) Wake(k WaitKey)                           // long-poll waiters
func (tx *Tx) Arm(k clock.TimerKey, at clock.EmuTime)   // (re)arm a state-derived timer
func (tx *Tx) Disarm(k clock.TimerKey)
func (tx *Tx) After(f func())                           // internal deliveries via Kernel.Invoke

// Partitioned lazily creates one cell per key: Scope for most services,
// a bucket name for S3 buckets, a single key for partition-wide indexes.
type Partitioned[K comparable, S any] struct{ /* gate, mu, cells map[K]*Cell[S], init func(K) S */ }

func (p *Partitioned[K, S]) For(k K) *Cell[S]
```

Rules:

- **A handler holds at most one cell lock, and never calls `Kernel.Invoke` while holding it.** Cross-service work is an effect (`tx.After`) that runs after unlock. With no nested locks there is no lock order to get wrong. Debug and race builds assert it (the kernel marks a `Call` as holding while inside `Write`; `Invoke` on a holding call panics).
- **Granularity:** one cell per (service, account, region) by default. S3 uses a partition-wide bucket-name index cell (bucket names are global) plus one cell per bucket; conditional writes take the bucket cell, so `ConditionalRequestConflict` races resolve under one lock (s3.md implication 8).
- **No I/O under a lock.** Blob bytes are written to the blob store before the cell write that publishes them; DynamoDB proxying happens outside any cell; container work happens in the Lambda pool, not in cells.
- **Waiting is channel-based** (wake channels, scheduler channels, `context`), never mutex hand-off, so `testing/synctest` bubbles advance (go-toolchain.md implication 6).

Long poll, as the reference for every blocking operation:

```go
func (s *Service) receiveMessage(c *kernel.Call, in *sqsapi.ReceiveMessageInput) (*sqsapi.ReceiveMessageOutput, *awserr.Error) {
	q, err := s.lookup(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	wait := clock.Deadline{
		Emu:     c.Admitted.Add(waitTime(in, q)),          // the AWS semantics
		WallCap: wallclock.Now().Add(waitTime(in, q)),      // a frozen clock still ends the HTTP request
	}
	for {
		woke := s.k.Waiter(waitKey(q))                      // subscribe before checking: no lost wakeups
		msgs, err := s.queues.For(c.Scope).Write(func(st *State, tx *store.Tx) ([]Message, *awserr.Error) {
			return st.receive(q, in, c.Clock().Now(), tx)   // arms visibility timers via tx
		})
		if err != nil || len(msgs) > 0 {
			return render(msgs), err
		}
		select {
		case <-woke:
		case <-c.Clock().Until(wait):
			return &sqsapi.ReceiveMessageOutput{}, nil
		case <-c.Context().Done():
			return nil, awserr.RequestAborted()
		}
	}
}
```

### 2.7 The clock

```go
package clock

// EmuTime is a point on the emulator clock. Not time.Time on purpose: mixing
// emulator and wall time needs an explicit, greppable conversion.
type EmuTime struct{ t time.Time }

func (e EmuTime) Add(d time.Duration) EmuTime
func (e EmuTime) Sub(o EmuTime) time.Duration
func (e EmuTime) Before(o EmuTime) bool
func (e EmuTime) Wire() time.Time // the one conversion, for codecs

type Mode interface{ isMode() }
type Live struct{ Offset time.Duration }                                  // emu = wall + Offset
type Frozen struct{ At EmuTime }
type Scaled struct{ Factor float64; AnchorWall time.Time; AnchorEmu EmuTime } // 0 < Factor <= 10000

type Clock struct{ /* mu, mode, sched, wall source */ }

func (c *Clock) Now() EmuTime
func (c *Clock) Mode() Mode
func (c *Clock) Freeze() EmuTime
func (c *Clock) Resume()                                       // back to Live, keeping the current offset
func (c *Clock) Scale(f float64) error
func (c *Clock) Advance(d time.Duration) (Advanced, error)     // d > 0
func (c *Clock) AdvanceTo(t EmuTime) (Advanced, error)         // t > Now(); never backwards
func (c *Clock) Until(d Deadline) <-chan struct{}

type Deadline struct {
	Emu     EmuTime
	WallCap time.Time // zero means none
}

type TimerKey struct{ Service, Kind, ID string } // ("sqs","visibility","<queue-arn>/<msg-id>")

type Scheduler struct{ /* heap ordered by (due, seq), index by TimerKey */ }

func (s *Scheduler) Arm(k TimerKey, at EmuTime, fire func(now EmuTime)) // replaces any timer with the same key
func (s *Scheduler) Disarm(k TimerKey)
```

Semantics, chosen for test authors:

- **Emulator time never moves backwards.** `serve --clock-start 2024-02-29T23:59:00Z` starts anywhere; afterwards only forward moves. Going back would re-fire timers and break monotonic sequence numbers, receipt handles and log tokens. `reset` and `snapshot load` are the only ways to an earlier time.
- **`Advance` is stepwise.** Due timers fire in `(due, seq)` order and each callback observes `Now() == due`. Advancing an hour across a `rate(5 minutes)` rule fires twelve scheduled events stamped 5, 10, ... 60 minutes later, not twelve events stamped "now".
- **Advance does not wait for real work** (Lambda invocations take wall time). `understudy clock advance 1h --settle` advances, then waits until the kernel's work tracker is idle (section 9.1).
- **A driver goroutine** sleeps for `(nextDue - now) / factor` of wall time in `Live` and `Scaled`, and not at all in `Frozen`. Mode changes and arms wake it.
- **Only `internal/clock` and `internal/wallclock` may call `time.Now`, `time.Sleep`, `time.After`, `time.NewTimer`, `time.Tick`** (`forbidigo`, go-toolchain.md implication 6).

The domain contract is lambda-runtime-contract.md section 3, adopted in full. In one sentence: time that a container, an SDK signer or user code observes or supplies is wall time; time that only the emulator observes is emulator time. The table that matters for users:

| Emulator clock | Wall clock (monotonic where it is a duration) |
|---|---|
| SQS visibility, delay, retention, dedup window, purge cooldown, move-task rate | `Lambda-Runtime-Deadline-Ms` = dispatch wall ms + Timeout |
| SNS delivery retries, FIFO dedup, confirmation token expiry | Function timeout enforcement, Init limit, REPORT durations |
| ESM batching windows, async invoke retries (60 s, 120 s), max event age | Warm-pool idle reaping |
| EventBridge schedules and event `time` (unless the caller sent `Time`) | SigV4 skew check and the `Date` response header |
| DynamoDB TTL sweep, secret deletion windows, KMS deletion, log retention | STS `Credentials.Expiration` |
| Server-assigned timestamps (`Last-Modified`, `CreatedTimestamp`, ...) | `slog` timestamps |
| S3 presigned URL expiry (`emu_now >= X-Amz-Date + X-Amz-Expires`) | PutLogEvents windows accept if valid against wall **or** emu |

Two consequences the docs must state plainly: user code that calls `time.time()` cannot be steered (functions can read emulator time from `GET /_understudy/v1/clock`, served on the container listener too), and a frozen clock never fires, shortens or lengthens a function timeout.

**ESM in-flight visibility** (lambda-runtime-contract.md open issue 2): in `Live` mode messages held by an in-flight ESM batch follow AWS exactly (visibility keeps counting; a function slower than the visibility timeout causes duplicates, as on AWS). In `Frozen` and `Scaled` modes the poller pins its held messages invisible until the invocation returns, because a scaled clock would otherwise produce duplicates AWS would not. The journal marks pinned messages.

### 2.8 The causal journal

```go
package journal

type EventID uint64 // monotonic per kernel; preserved across snapshot load

type Cause struct {
	Parent EventID // what triggered this event; 0 for roots
	Trace  EventID // the root of the causal tree
}

type Event struct {
	ID     EventID
	Cause  Cause
	Links  []EventID      // related events that are not the parent (other messages in an ESM batch)
	Emu    clock.EmuTime
	Wall   time.Time
	Scope  scope.Scope
	Detail Detail
}

type Detail interface{ isDetail() }

type APICall struct {
	Service, Operation string
	Origin             string        // "external:boto3/1.43.109" or "internal:sns.delivery"
	Principal          string        // ARN form
	Resources          []arn.ARN     // from Call.Touch
	Outcome            Outcome
	Latency            time.Duration // wall
	Request, Response  Redacted      // summaries; bodies only with --journal-bodies, always redacted
}
type Delivery struct {
	From, To  arn.ARN
	Mechanism string // "sns->sqs", "events->lambda", "s3->sqs", "esm->lambda", ...
	Attempt   int
	Outcome   Outcome
}
type Invocation struct {
	Function arn.ARN
	Version  string
	Env      string // execution environment id
	Cold     bool
	Init, Duration time.Duration
	Outcome  Outcome // OK | FunctionError{Type} | Timeout | ExitError{Code} | OOM
}
type TimerFired struct{ Key clock.TimerKey }
type StateChange struct{ Resource arn.ARN; From, To string } // Lambda Pending->Active, ESM Creating->Enabled
type FaultInjected struct{ Rule string; Hit, Of int }       // "3rd of next 3"
type ClockChanged struct{ From, To clock.Mode }
type Unclaimed struct{ Method, Host, Path string; Reasons []string } // routing diagnostics
type Gap struct{ Missed uint64 }                             // slow stream consumer

type Outcome interface{ isOutcome() } // OK{} | Failed{Code, Status, Message} | Injected{Rule, Code}
```

How causality survives hops:

- Internal calls inherit `Cause{Parent: caller.Event, Trace: caller.Cause.Trace}`.
- **State carries causes across time.** An SQS message stores the event id of the call that enqueued it. When the ESM invokes a batch, the invocation's parent is the earliest message's enqueue event and the rest are `Links`. The same holds for SNS delivery state, EventBridge target delivery, S3 notifications and async invoke queues.
- **Calls made by function code link to the invocation.** Each execution environment gets its own minted role credentials (section 6.6), and one environment runs one invocation at a time, so the access key on a request identifies the environment and therefore the current invocation. No SDK cooperation is needed. (Whether SDKs also forward `_X_AMZN_TRACE_ID` as `X-Amzn-Trace-Id` is UNVERIFIED; if they do, the edge uses it as a cross-check only.)

Storage and query:

- A bounded in-memory ring (default 200,000 events or 256 MiB, whichever first) with indexes by trace, resource ARN and service/operation. With persistence on, events are also appended to `<data-dir>/journal.ndjson` with size-based rotation.
- Query: `GET /_understudy/v1/journal/events?trace=&service=&operation=&resource=&kind=&outcome=error&since=&until=&limit=`. `since`/`until` accept an event id or an emulator timestamp.
- Trace tree: `GET /_understudy/v1/journal/traces/{id}` returns the tree; the CLI renders it (section 9.3).
- Stream: `GET /_understudy/v1/journal/stream` as Server-Sent Events with the same filters, resumable with `Last-Event-ID`. Slow consumers get a `Gap` event, never backpressure on the emulator.
- Redaction: members with the Smithy `sensitive` trait (47 shapes: KMS plaintext, `SecretString`, `SecretBinary`, rotation tokens, ...; smithy-models.md 4.1) are replaced by `[redacted]` in the journal and in logs, decided from the schema, not by hand.

### 2.9 Fault injection

```go
package fault

type Rule struct {
	ID      string
	Match   Match
	Phase   Phase   // Request | Delivery
	Action  Action
	Trigger Trigger
}

type Match struct {
	Services   []string   // empty = any
	Operations []string   // "SendMessage"; for Delivery phase: mechanisms ("sns->sqs")
	Resource   ARNGlob    // "arn:aws:sqs:*:*:orders-*"
	Accounts   []scope.AccountID
	Regions    []scope.Region
	Origin     OriginMatch // Any | ExternalOnly | InternalOnly
}

type Action interface{ isAction() }
type Error struct{ Code string; Message string } // resolved and validated against the service catalogue
type Throttle struct{}                           // the service's own throttling identity
type Latency struct{ Wall time.Duration }       // delay before the handler runs
type AfterCommit struct{ Code string }           // side effect happens, client gets the error
type StatusOKError struct{ Code string }         // S3 200-with-error (CompleteMultipartUpload, CopyObject, UploadPartCopy)
type DropDelivery struct{}                       // Delivery phase: the target never receives it

type Trigger interface{ isTrigger() }
type Always struct{}
type Probability struct{ P float64 } // seeded: ChaCha8 keyed by hash(global seed, rule id)
type NextN struct{ N int }           // removed after N hits; every SDK retry counts as a hit
```

Design points:

- **Rules are validated at creation against the target service's error catalogue** (modeled errors plus the generic and throttling catalogue). `faults add --service sqs --error ThrottlingException` fails with the list of codes SQS actually sends (`RequestThrottled` 403, `OverLimit` 403, ...; sqs.md implication 18). `Throttle{}` picks the right code per service so users do not need to know them; per-service throttling codes not yet recorded (error-matrix.md open question 4) are behavior switches with provenance.
- **One engine for everything that goes through the pipeline**, including the DynamoDB proxy (before forwarding, so Local never sees the request; `TransactionConflictException`, `ProvisionedThroughputExceededException` are injectable although Local never raises them) and internal deliveries (`Phase: Delivery`, or a `Request` rule with `Origin: InternalOnly`).
- **Determinism**: same seed, same rule set, same call order gives the same hits. Concurrent clients make call order nondeterministic; the journal's `FaultInjected{Hit, Of}` events make every hit visible either way.
- **SDK retries are surfaced, not hidden.** A throttle is retried by the SDK (Terraform's Go SDK up to 25 attempts; tf-compute.md implication 14), so `NextN{3}` is consumed by retries of one logical call. The CLI prints this when a rule targets a retryable code, and the docs give the fail-fast codes (`NonExistentQueue`, `NotFound`) for Terraform tests (tf-messaging.md implication 20).
- Rules come from the admin API, the CLI, or the config file (preloaded at startup, so a CI job can declare its chaos up front).

### 2.10 IDs and determinism

`internal/ids` owns every generated identifier: request ids, SQS message ids and receipt handles, SNS message ids, Lambda request ids, ESM UUIDs, upload ids, version ids, KMS key ids, IAM `AROA`/`ANPA`/`AIDA` ids, minted access keys. In default mode they come from `crypto/rand`. With `--seed N` they come from ChaCha8 streams derived per (seed, generator kind), so a seeded single-client run reproduces byte-identical journals and snapshots. The stdlib `uuid` package cannot be seeded (go-toolchain.md 12a), so UUIDs are formatted from the seeded stream.

### 2.11 Snapshots and persistence

A snapshot is one deterministic, versioned archive: an uncompressed POSIX tar (`archive/tar`) with entries in sorted order, zero mtimes, uid/gid 0, mode 0644, and an optional gzip wrapper with an empty header name and zero mtime. The same state always produces the same bytes, so snapshots can be committed as test fixtures and diffed.

```
manifest.json                               format version, writer version, model pin, seed + generator positions,
                                            clock mode and time, journal counter, section list with sha256
services/sqs/000000000000/us-east-1.json    one canonical JSON document per cell (json v2, Deterministic(true))
services/s3/buckets/<bucket>.json           S3: per-bucket metadata, versions, multipart state
services/dynamodb/<account>/<region>/<table>.ndjson   items exported by Scan through the proxy, sorted by key
services/dynamodb/<account>/<region>/tables.json      shim state: tags, TTL, PITR, table class, SSE, table ids
blobs/sha256/<hex>                          object bodies, multipart parts, Lambda code zips (content-addressed)
journal.ndjson                              optional (--include-journal)
```

```json
{
  "format": 1,
  "writer": "understudy 0.1.0",
  "models": "aws/api-models-aws@1cfdfb4bb9ea858e65623e66d73af04bec353201",
  "clock": {"mode": "frozen", "at": "2026-10-08T12:00:00Z"},
  "seed": {"value": 42, "positions": {"request-id": 1812, "sqs-message-id": 77}},
  "journal": {"next": 9135},
  "sections": [{"path": "services/sqs/000000000000/us-east-1.json", "service": "sqs", "schema": 1, "sha256": "..."}]
}
```

```go
package kernel

// Stateful is implemented by every service with state. A conformance test
// asserts Export -> Import -> Export is byte-identical for every registered
// service, and that Rearm after Import reproduces the scheduler contents.
type Stateful interface {
	Name() string
	SchemaVersion() int
	Export(w *snapshot.Writer) error
	Import(r *snapshot.Reader, from int) error // migrates older section schemas
	Reset(scope ResetScope)
	Rearm(s *clock.Scheduler)                  // timers are derived from state, never stored
}
```

- **Loading refuses a newer `format` or section schema** with a message naming both versions (MiniStack's downgrade guard, landscape.md section 8). Older schemas migrate through per-service functions with golden fixtures for every past version.
- **Load is reset-then-import under the gate's write side:** warm Lambda environments drain, ESM pollers stop, the scheduler clears; after import every service `Rearm`s (ESM pollers, schedules, visibility timers, TTL sweeps, deletion windows) from state. A restart test covers it (MiniStack#889 is the failure to avoid).
- **DynamoDB is exported through the proxy** (DescribeTable plus paginated Scan) and imported with CreateTable plus BatchWriteItem into a fresh `-inMemory` Local, rather than copying Local's SQLite files (copy-while-running safety is UNVERIFIED; dynamodb-local.md implication 11).
- **Persistence across restarts is opt-in** (`--persist`): the same snapshot written atomically (temp file plus rename) to `<data-dir>/state.snapshot` on graceful shutdown and every 30 s of wall time while any cell is dirty, and loaded at startup. No write-ahead log in phase 1: a crash loses at most 30 s, which is honest for a dev tool. The data dir is locked with `flock` so two `serve` processes cannot share it.
- `understudy snapshot diff a b` compares two archives section by section and prints resource-level differences, which turns snapshots into a regression tool ("this test run left an extra queue behind").

### 2.12 Behavior switches: the fidelity ledger

Where research found no AWS-recorded evidence, the behavior lives behind one named switch, not scattered branches (sns.md implication 4, matching-engines.md implication 2, logs-filter-patterns.md section 10):

```go
package fidelity

type Provenance int // RecordedRaw | RecordedSDK | Doc | SDKSource | ThirdPartyRecording | Inferred

type Switch[T comparable] struct {
	Name       string     // "logs.filter.term-boundary"
	Value      T          // the chosen behavior
	Provenance Provenance
	Ref        string     // "docs/research/phase1/logs-filter-patterns.md#5"
	Record     string     // backlog item that would settle it, e.g. "R10/C1"
}
```

Switches are not user-configurable in phase 1 (no config sprawl); they are listed by `understudy coverage --switches`. When a human-run recording settles one, the change is one value plus the tests that pin it.

---

## 3. Code generation from the vendored Smithy models

### 3.1 Inputs

| Input | Location | Notes |
|---|---|---|
| 12 service models, verbatim | `third_party/api-models-aws/models/...` | pinned at `1cfdfb4bb9ea858e65623e66d73af04bec353201`; `UPSTREAM.json` holds per-file git blob SHA-1 and sha256 (smithy-models.md section 10) |
| Smithy protocol tests, flattened JSON AST | `third_party/smithy-protocol-tests/model.json` | produced once by a human with the Smithy CLI 1.74.0 (`smithy ast --no-config --flatten <six jars>`, deterministic, about 2.4 MB); pin record holds jar and CLI sha256s (smithy-protocol-tests.md section 5) |
| IAM action mapping | `third_party/aws-service-reference/actions.json` | derived table of `Operations[].AuthorizedActions` for the 12 services; raw JSON licence is UNVERIFIED, so vendor the derived facts until an ADR decides |
| Overlays | `codegen/overlays/<svc>.json` | hand-owned corrections the model cannot express (below) |

Overlay content, each entry referencing a shape id that the generator verifies exists:

- error identity: `__type` namespaces (H1), per-operation constraint-violation codes (H2), message key casing corrections, unmodeled codes per service (H6)
- regex translations for the 33 patterns RE2 rejects, and pattern-echo exceptions (H3; smithy-models.md 6.2)
- tightened limits the model widens (Lambda `Timeout` 1..900, `MemorySize` 128..10240; lambda-api.md finding 5) and missing ones (DynamoDB `TableName` rules)
- error status overrides (KMS generated 404/409/503 vs observed 400/400/500; iam-sts-kms.md implication 3)

Behavior that is code, not data, stays hand-written in the service package: SQS limits (the model has none), the S3 error table and router overrides, CloudWatch's `InvalidParameterCombination` texts.

### 3.2 Pipeline

`go run ./internal/cmd/gen` (wrapped as `just generate`):

1. Verify `UPSTREAM.json` hashes offline. No network, ever, during generation or tests.
2. Load each model with `UseNumber`; reject unknown shape types, `mixins`, `apply`, `set`.
3. Walk the service closure through operations **and resources** (Lambda reaches 72 of 88 operations only through resources; smithy-models.md finding 5); fail on a missing shape.
4. Classify every trait id as honor / generation-only / ignore; **fail on an unclassified id** so a model bump with a new trait breaks `go generate` instead of fidelity (smithy-models.md 4.3).
5. Compute effective member traits (member over target).
6. Merge overlays; fail if an overlay references a shape that does not exist or is now redundant.
7. Build a typed IR; emit Go through `go/format`; sorted iteration everywhere; temp file plus rename.
8. Run the same pipeline over the Smithy protocol-test services into test-only packages, so codecs are tested against the official cases with real generated types.

### 3.3 Outputs

Per service, `internal/aws/<svc>/api/` (all with `// Code generated by understudy-gen from aws/api-models-aws@1cfdfb4; DO NOT EDIT.` on line 1):

| File | Content |
|---|---|
| `types_gen.go` | Go types for every shape in the closure: structures, unions as sealed interfaces, enums as string types with known values, optionality preserved (`*int32` where absent differs from 0) |
| `schema_gen.go` | runtime schema descriptors: member wire names, `xmlName`, `xmlFlattened`, `xmlNamespace`, `timestampFormat`, HTTP bindings, `sensitive`, `default` |
| `serde_gen.go` | `SerializeShape(smithy.Serializer)` / `DeserializeShape(smithy.Deserializer) error` per structure, no reflection (the aws-sdk-go-v2 schema-serde design, go-toolchain.md implication 10) |
| `ops_gen.go` | operation descriptors, `kernel.Op` handles, IAM actions, HTTP routes, modeled error lists |
| `validate_gen.go` | `required`, `length` (runes for strings), `range`, `pattern`, enum checks emitting `awserr.Violation` in AWS's order |
| `errors_gen.go` | error constructors with full `Identity` |
| `bind_gen.go` | the typed `Binder` |

Plus `internal/aws/s3/api/router_gen.go`: the S3 dispatch table `(method, path shape, query keys present, required header) -> operation`, ordered by specificity, `x-id` stripped (wire-rest.md implication 2), with a hand-written override list beside it.

`just generate-check` (CI) runs the generator and `git diff --exit-code`. Generated code has its own unit tests at the schema and codec level because linters skip it.

---

## 4. Protocols

### 4.1 The front door

All protocols share one port. Claiming is explicit and ordered; nothing falls through to S3 by default (the LocalStack `legacy_s3_rules` trap, landscape.md 3.1):

1. Path prefix `/_understudy/` -> admin, SNS cert/unsubscribe handlers, function URL path fallback. S3 bucket names cannot start with `_`, so this never shadows a bucket.
2. Host claims: `<id>.lambda-url.<region>.<base>` -> function URL data plane; `<account-id>.<base>` with path `/v20180820/` -> S3 Control tagging (s3.md finding 1).
3. Parse auth into `SigV4Header | SigV4Query | SigV2Header | SigV2Query | Anonymous`; take the credential-scope service.
4. `Smithy-Protocol: rpc-v2-cbor` and a `/service/<Name>/operation/<Op>` path -> CBOR (CloudWatch).
5. `X-Amz-Target` present -> awsJson; service from the target prefix table (`AmazonSQS`, `DynamoDB_20120810`, `DynamoDBStreams_20120810`, `Logs_20140328`, `secretsmanager`, `TrentService`, `AWSEvents`, `GraniteServiceVersion20100801`).
6. Form-urlencoded POST, or GET with `Action` -> awsQuery; service from signing name, confirmed by `Version`.
7. REST: signing service `lambda` -> Lambda router; `s3` (header or presigned query) -> S3 router; unsigned with a Lambda date-prefix path that matches a Lambda route and no bucket of that name -> Lambda; virtual-hosted bucket host -> S3.
8. Nothing claimed: an AWS-shaped 400 plus an `Unclaimed` journal event listing why each rule declined. This turns "my client sends something odd" into a readable diagnosis.

Media types match by essence (ignore `; charset=...`); header names case-insensitively. `http.ServeMux` serves only the admin prefix; AWS dispatch uses `r.RequestURI`/`EscapedPath()` because ServeMux redirects `//`, `.` and `..` (go-toolchain.md section 5).

### 4.2 awsJson1_0 / awsJson1_1 (SQS, DynamoDB, CloudWatch-JSON; Logs, Secrets Manager, KMS, EventBridge)

- Built on `encoding/json/v2` + `jsontext` behind `protocol/jsonx` only.
- Decode: accept empty body, `{}`, null members, unknown members, exponent numbers, `charset`, gzip for `PutMetricData`.
- Encode: integers as plain digits, timestamps as epoch seconds (ms precision), blobs as padded base64, non-finite floats as strings, omit nulls, `{}` for any modeled output.
- Errors: `__type` mandatory, `namespace#Shape` on 1.0 services with the overlay namespaces (`com.amazonaws.sqs`, `com.amazonaws.dynamodb.v20120810`, `com.amazonaws.cloudwatch.v2010_08_01`), bare shape name on 1.1; message under the shape's own member name; status from `httpError`.

### 4.3 awsQueryCompatible (SQS and CloudWatch over JSON and CBOR)

On every error whose shape carries `awsQueryError`, emit `x-amzn-query-error: <code>;Sender|Receiver`, generated from the model's traits, regardless of `x-amzn-query-mode` (wire-json-query.md implication 12). Status follows the `awsQueryError.httpResponseCode` where AWS-recorded (`PurgeQueueInProgress` 403; sqs.md finding 4). Unmodeled SQS codes use `MissingRequiredParameterException` / `InvalidParameterValueException` in `__type` with the query code in the header.

### 4.4 awsQuery (SNS, IAM, STS, CloudWatch, legacy SQS)

- Decode: indexed keys sorted numerically (Go sends `member.10` before `member.2`), `+` and `%XX`, empty list value, GET, both date-time forms.
- Encode with `protocol/xmlw`: `<OpResponse xmlns><OpResult/><ResponseMetadata><RequestId/></ResponseMetadata></OpResponse>`; the Result element whenever the operation has output and the envelope even when it does not (boto3 raises otherwise); IAM policy documents percent-encoded with RFC 3986 unreserved characters (`+` as `%2B`).
- SQS legacy Query: hand-built from the JSON model's `xmlName`/`xmlFlattened` traits, namespace `http://queue.amazonaws.com/doc/2012-11-05/`, `Action` on `/` or on `/<account>/<queue>` (sqs.md implication 2).
- Errors: `ErrorResponse/Error{Type,Code,Message}` plus `RequestId`, `Code` from `awsQueryError`; no `xmlns` for SNS, `xmlns` for CloudWatch (error-matrix.md section 3).

### 4.5 restJson1 (Lambda)

Router from the generated `http` traits with Smithy specificity ordering (literal beats label beats greedy). Errors: `X-Amzn-Errortype: <Shape>` plus a body with the model's exact member names (`Type`, `message`/`Message`). `Invoke` treats the body as opaque bytes, accepts any `Content-Type`, and computes 200/202/204 from the invocation type; ESM create/update/delete return 202.

### 4.6 restXml (S3)

Hand-routed over the generated dispatch table (section 3.3). Own XML writer for `xmlns` on roots, `xsi:type` grantees, flattened lists and exact declarations (`encoding/xml` silently replaces control characters; go-toolchain.md implication 9). Unwrapped `GetBucketLocation`. One error writer: `<Error>` without namespace, `Code`, `Message`, context elements, `RequestId`, `HostId`; HEAD errors bodyless. Exactly one `Date` header (two break the JS SDK; s3.md finding 7).

### 4.7 rpcv2Cbor (CloudWatch, Go SDK and therefore Terraform)

The stdlib CBOR subset from wire-cbor.md section 7 (decoder about 275 lines, encoder about 65): definite and indefinite inputs, tag 1 timestamps, half floats, depth 128, length checks before allocation. Contract: reject requests carrying `X-Amz-Target`; empty body for `Unit` outputs (any member panics the Go client); errors always carry `__type` and the modeled message member; `x-amzn-query-error` per 4.3.

### 4.8 Codec testing

Every codec implements the same `smithy.Serializer`/`Deserializer` interfaces, so one rapid round-trip property covers every generated shape per protocol, and one fuzz target per decoder asserts "never panics, bounded allocation". The Smithy server-applicable cases are the gate per protocol (section 13.2).

---

## 5. S3 addressing and the DNS story

### 5.1 Host parsing is configuration

`s3.baseDomains` (default `["localhost"]`, plus built-in AWS forms `s3.<region>.amazonaws.com`, `s3.amazonaws.com`, dualstack and fips variants). A request is virtual-hosted iff its Host (port stripped) ends in `.` + a base domain or `.s3.` / `.s3.<region>.` + a base domain and the remaining prefix is a DNS-valid bucket; Host equal to a base domain, or an IP literal, is path-style. Dotted buckets are handled by matching the longest base suffix, never by splitting at the first dot (the Floci `HEAD /` -> 200 -> "bucket exists" trap, landscape.md 4.1). The virtual-host rewrite never captures requests whose credential scope names another service.

### 5.2 What each client does, and what understudy says about it

| Client against `http://localhost:4566` | Addressing | Needs |
|---|---|---|
| Go v2, JS v3 (incl. Terraform) | virtual-hosted `bucket.localhost:4566` | `*.localhost` resolution, or `UsePathStyle` / `forcePathStyle` / `s3_use_path_style` |
| boto3, AWS CLI | path-style | nothing |
| any SDK against `http://127.0.0.1:4566` | path-style | nothing, but Terraform's S3 Control tag read builds `<account>.127.0.0.1` and fails (s3.md finding 1) |

Measured facts: `*.localhost` resolves through systemd-resolved on the dev host; Go does not synthesize it itself; macOS, Windows and musl behavior is UNVERIFIED (sigv4-checksums-endpoints.md 6.2).

### 5.3 The decision

- **Host side, no DNS server.** The documented default is `AWS_ENDPOINT_URL=http://localhost:4566`. `understudy doctor` resolves `probe.localhost` and `000000000000.localhost` with both the Go resolver and the system resolver and prints, per SDK, whether virtual-hosted S3 will work and the exact knob if it will not. On a platform without `*.localhost`, the Terraform recipe adds `s3_use_path_style = true` and `skip_requesting_account_id = true` (with no account id the provider uses `GetBucketTagging` instead of S3 Control; tf-storage.md 1.1).
- **Container side, no DNS at all.** Functions get an IPv4-literal `AWS_ENDPOINT_URL` (path-style in every SDK; lambda-runtime-contract.md 6.2), plus `localhost:host-gateway` and a gateway listener on the main port on Linux and Podman so `localhost:4566` URLs minted on the host still work inside functions (strategy D).
- **Names that must carry a host** (function URLs, virtual-hosted presigned URLs) have path fallbacks: `/_understudy/lambda-url/<url-id>/<path>` for function URLs.
- **An opt-in stdlib DNS responder** (wildcard A records for the base domain, handed to containers via `HostConfig.Dns`) is deferred to an ADR with evidence from users on macOS and Docker Desktop. A public wildcard domain resolving to 127.0.0.1 is a product decision for humans (it adds a runtime network dependency the brief forbids by default).

### 5.4 Other S3 platform points

- Keys never touch filesystem paths: bytes live in `internal/blob` under opaque content-addressed ids, written through `os.Root`, refcounted across versions and snapshots. Objects above 8 MiB spill to disk even in ephemeral mode (temp dir, removed at exit).
- Checksums: all ten algorithms including hand-ported XXHASH64/XXH3/XXH128 (s3-checksum-residuals-and-record-backlog.md 1.7), CRC combine for full-object multipart, `aws-chunked` decoding in four modes as a streaming `io.Reader` that verifies as it goes; headers validated before the first body read so `Expect: 100-continue` works.
- Cross-region bucket access: buckets live in one partition-wide namespace with a home region; a request signed for another region is served (lenient) with `x-amz-bucket-region` and a journal warning. Mimicking AWS's redirects helps only boto3 (wire-rest.md implication 11). ADR item.
- S3 Control `ListTagsForResource`/`TagResource`/`UntagResource` on any `<account>.<base>` host share the bucket tag store.

---

## 6. Lambda execution

### 6.1 Execution model: understudy hosts the Runtime API

Each execution environment is one container from AWS's official base image (or the user's image) with `AWS_LAMBDA_RUNTIME_API=<gateway-ip>:<env-port>`; the official `/lambda-entrypoint.sh` then execs the runtime directly instead of starting the RIE (containers.md findings 1-3). Reasons: the RIE never sets `X-Amz-Function-Error`, crashes on concurrent invokes, hard-codes the invoked ARN and fakes memory in REPORT; non-AWS-base images have no RIE at all; and the hosted design needs no inbound path to containers (which matters on Docker Desktop). This deviates from the brief's "with the Runtime Interface Emulator" only in who serves the Runtime API and needs an ADR.

`internal/aws/lambda/runtimeapi` (about 600 lines, `net/http`):

- `GET /2018-06-01/runtime/invocation/next` blocks; always sends `Content-Type: application/json` (the Python RIC segfaults without it), `Lambda-Runtime-Aws-Request-Id`, `Lambda-Runtime-Deadline-Ms` (wall epoch ms), `Lambda-Runtime-Invoked-Function-Arn` (real account, region, qualifier), `Lambda-Runtime-Trace-Id`, client context and the new `Lambda-Runtime-Invocation-Id`.
- `/response` and `/error` answer 202; chunked bodies and the `Lambda-Runtime-Function-Error-*` trailers are read from `r.Trailer`; any `/error` becomes `X-Amz-Function-Error: Unhandled`.
- `/init/error`, exit before the first `/next`, `die`/`oom` events map to AWS-shaped payloads (`Runtime.ExitError` wording; containers.md 6.5).
- The first `GET /next` marks init done. No `/next` within the init limit produces a diagnostic error naming the address handed to the container, the detected topology and the three likely causes (containers.md 8.4).

### 6.2 Pool

`internal/aws/lambda/pool` with the state machine `Starting -> Idle <-> Busy -> Draining -> Gone`, `Starting|Busy -> Faulted -> Gone`, as a sealed type so an environment cannot be both Busy and Idle:

```go
type EnvState interface{ isEnvState() }
type Starting struct{ Since time.Time }
type Idle struct{ Since time.Time }
type Busy struct{ Invocation InvocationID; Deadline time.Time }
type Draining struct{ Reason string }
type Faulted struct{ Cause FaultCause }
```

- Exclusive reservation; one in-flight invocation per environment; concurrency by more environments.
- Pool key = hash of everything in the container spec (code SHA or image digest, runtime, architecture, memory, timeout, handler, image config, layers, env vars, role, logging config, hot-reload overlay generation). Any configuration or code update bumps a generation: idle environments of old generations drain at once, busy ones finish and are discarded (the MiniStack stale-container bug, containers.md 6.2).
- Start semaphore (default 16, configurable), per-function idle cap `max(4, CPUs)`, global cap with LRU eviction, idle timeout on wall time (default 10 minutes).
- Concurrency limits (account 1000, reserved concurrency) are checked before any container work and return the AWS 429 shape.
- Removal is `DELETE ?force=1&v=1`; Python's RIC as PID 1 ignores SIGTERM (containers.md finding 7).

### 6.3 Code delivery

Zip functions extract once per `CodeSha256` into a content-addressed directory: names must be `filepath.IsLocal`, symlink entries rejected, total and per-entry sizes capped, modes normalized (`bootstrap` 0755), written through `os.Root`, binary built with `//go:debug zipinsecurepath=0` as a second layer. Mounted read-only at `/var/task` via `HostConfig.Mounts` (not `Binds`, which silently creates missing host paths as root). When understudy runs in a container, or the path is not daemon-visible, it falls back to tar copy (`PUT /containers/{id}/archive`) without `ReadonlyRootfs`. Image functions pull at `CreateFunction` (state `Pending` until present), resolving local tags first; ECR URIs that are not present locally fail with "ECR is not emulated in phase 1; tag your local image as `<ImageUri>`" (containers.md 5.3).

### 6.4 Hot reload

Hot reload is an **execution-layer overlay, invisible to the control plane**. The API keeps reporting the deployed `CodeSha256` and `LastModified`, so Terraform's second plan stays empty while the developer iterates.

Two ways to enable it, both leaving IaC untouched for production:

```json
{"lambda": {"hotReload": [{"function": "worker", "path": "./build/worker"}]}}
```

or the LocalStack-compatible magic bucket (`s3_bucket = "hot-reload"`, `s3_key = <absolute host path>`) for teams migrating.

The watcher (`internal/aws/lambda/hotreload`) polls mtimes and sizes (stdlib has no inotify) every 300 ms with a 300 ms quiet period; a change bumps the overlay generation, so the next invoke cold-starts on the new code (about 590 ms for Python; containers.md 5.4). The directory is bind-mounted read-only at `/var/task`. Journal events record overlay activation and every reload. With understudy itself in a container, host paths are translated through `GET /containers/<self>/json` mounts.

### 6.5 Networking

`internal/topology` detects the environment once at first Lambda use and returns a sealed type:

```go
type Topology interface{ isTopology() }
type LinuxBridge struct{ Gateway netip.Addr }       // listener on the gateway IP; ExtraHosts host-gateway
type DockerDesktop struct{}                          // host.docker.internal provided by Desktop; no ExtraHosts
type RootlessDocker struct{ HostAddr netip.Addr }    // host loopback blocked by default
type Podman struct{ Rootless bool }
type InContainer struct{ Network string; Self netip.Addr } // function containers join understudy's network
```

Each variant yields: bind addresses for the container listener and Runtime API listeners, the IPv4-literal `AWS_ENDPOINT_URL` for functions, and `ExtraHosts`. Linux bridge is first-class and tested in CI; the other variants are best effort with doctor checks until verified (containers.md 8.1 marks several UNVERIFIED).

### 6.6 Environment and credentials

Each environment gets the full AWS env set (`AWS_LAMBDA_FUNCTION_NAME`, `_VERSION`, `_MEMORY_SIZE`, `AWS_LAMBDA_LOG_GROUP_NAME`, `_LOG_STREAM_NAME`, `AWS_REGION`, `_HANDLER`, `LAMBDA_TASK_ROOT`, `TZ=:UTC`, ...; lambda-api.md 12.7) and **its own minted `ASIA` credentials for the function's execution role** via the STS code path. That gives three things at once: `GetCallerIdentity` inside the function returns the assumed role; the access recorder attributes calls to the role; the journal links function calls to the current invocation (section 2.8). Credentials are never logged.

### 6.7 Logs

One `logs?follow=1&timestamps=1` stream per environment, opened right after start; frames demultiplexed; lines written to `/aws/lambda/<function>` stream `YYYY/MM/DD/[<version>]<env-id>` through internal `PutLogEvents`, stamped with emulator time. START/END/REPORT (and `INIT_START`) are synthesized with wall-measured durations and a settle barrier after `/response` before cutting the invocation's window (log lines and responses race by under a millisecond; containers.md finding 9). `LogType=Tail` returns the last 4 KB. The journal records one log-delivery event per invocation, not per line.

### 6.8 Invoke, async and state machine

- RequestResponse 200 with `X-Amz-Executed-Version`; Event 202 into a per-function async queue on the emulator clock (retries after 60 s and 120 s, 6 h max age, destinations as stretch); DryRun 204. Limits: 6 MB sync, 1 MB async (lambda-api.md finding 4).
- Function state `Pending -> Active` and `LastUpdateStatus InProgress -> Successful` transition on the emulator clock with a short default delay; conflicting updates during the window return the exact `ResourceConflictException`. Freezing the clock freezes the transition, which lets tests exercise SDK waiters deterministically.
- Docker unavailable: the control plane keeps working (Terraform applies cleanly); Invoke returns `ServiceException` with a message naming the Docker endpoint tried and `understudy doctor`.

### 6.9 Event source mappings

`internal/aws/lambda/esm`: one poller per enabled mapping, a kernel task on the emulator clock that calls the SQS service through internal ops (no HTTP, no private headers, no 10-message ceiling; landscape.md implication 6):

- Gather up to `BatchSize` while the batching window (emulator time) is open or the 6 MB payload limit would be exceeded; window 0 invokes as soon as messages are visible. The 20-second long-poll floor is not modeled: on the controllable clock the window is literal, which is the "real AWS timing in milliseconds" promise.
- Filter before invoke; non-matching messages are deleted without invoking.
- Partial batch failure: delete exactly the messages not listed in `batchItemFailures`; any malformed identifier or function error fails the whole batch; redrive comes from SQS `maxReceiveCount`, not from the ESM (lambda-events.md implications 12-13).
- FIFO: one in-flight batch per message group; concurrency `min(groups, MaximumConcurrency)`.
- Pollers are derived from state: `Rearm` after snapshot load or restart restarts every `Enabled` mapping.
- Control plane: 202 with `Creating`/`Updating`/`Deleting` resolved on the clock within a short tick; response fields shaped for an empty second plan (`FunctionResponseTypes: []`, `MaximumBatchingWindowInSeconds: 0`).

---

## 7. DynamoDB Local integration

`internal/aws/dynamodb` is a shim proxy in front of one understudy-managed `amazon/dynamodb-local` container (dynamodb-local.md implications):

- **Licence gate.** The container is pulled only after explicit acceptance: an interactive prompt with the notice from dynamodb-local-terms.md 5.1, or `--accept-dynamodb-local-license` / `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1` in CI. Without it, DynamoDB calls answer 501 with a message naming the flag; every other service runs. Acceptance is stored per image digest in the data dir (or per process in ephemeral mode).
- **Sealed dependency.** Pulled by digest (`sha256:ff89bd48...` is 3.3.1), ECR Public fallback with the same digest, never re-tagged, saved or patched. Started with `-disableTelemetry` and `DDB_LOCAL_TELEMETRY=0` (only the literal `0` works), `-inMemory`, `127.0.0.1:0:8000` published, a memory bound, understudy labels. A permanent egress test asserts zero outbound connections.
- **Partitioning by credential rewrite.** Every forwarded request's `Credential=` becomes `<account>/<date>/<region>/dynamodb/aws4_request` with a region from understudy's own table; Local then isolates (account, region) natively.
- **Before forwarding:** the pipeline's journal, fault rules and access recorder run; understudy answers generic errors itself (malformed JSON, missing target, wrong Content-Type, auth) because Local's answers differ from real DynamoDB (error-matrix.md finding 6); unsupported operations fail as Unimplemented; empty bodies are forwarded as `{}`; `Accept-Encoding` is forced to `identity`.
- **After forwarding:** typed JSON rewriting of every ARN and `awsRegion`; `WarmThroughput {12000, 4000, ACTIVE}`, `TableClassSummary`, `SSEDescription`, `TableId` injected (without `WarmThroughput` Terraform hangs about 2.5 minutes and fails); `message` casing normalized; `x-amz-crc32` recomputed over the final bytes on every 200.
- **Shim-owned control plane:** tags, PITR and continuous backups, TTL spec, table class, SSE, case-sensitive table identity, `DELETING` on delete.
- **TTL entirely in understudy, on the emulator clock.** `UpdateTimeToLive` never reaches Local (which disables Local's wall-clock sweeper for free). A state-derived sweep timer per enabled table scans `#ttl <= :now` and deletes with a condition that the item is still expired, so a concurrent refresh wins. `clock advance 1h --settle` therefore expires a lock-table entry deterministically; with streams enabled, TTL deletes carry the service `userIdentity`.
- **Streams** (stretch) read through the proxy with rewritten ARNs; the ESM type keeps a `DynamoDBStream` variant open now.
- Lifecycle: started in the background at `serve` when accepted and the image is present (cold start about 1.2 s), otherwise on first call with a bounded wait; readiness is any HTTP answer on `/`.

---

## 8. Cross-service delivery

Every delivery is an internal `Kernel.Invoke` of the target's real operation (or the Lambda invoker), journaled as `Delivery` with the source event as parent, subject to `Phase: Delivery` fault rules, and tracked by the work tracker:

| Source | Trigger | Target operation | Retry and failure (emulator clock) |
|---|---|---|---|
| SNS -> SQS | Publish (after response) | `sqs:SendMessage` with envelope or raw body; FIFO keeps group and dedup | AWS-managed policy; missing queue is a client error -> subscription DLQ at once |
| SNS -> Lambda | Publish | async Invoke with the `Records[0].Sns` event | Lambda async retries; throttles retried |
| SQS -> Lambda (ESM) | poller tick | sync Invoke; then internal `DeleteMessageBatch` | visibility timeout; DLQ by `maxReceiveCount` |
| EventBridge rule -> Lambda / SQS / SNS | schedule timer or PutEvents | async Invoke / `SendMessage` / `Publish` with `Input`, `InputPath`, `InputTransformer` applied | target retry policy (stored); journaled per attempt |
| S3 -> SQS / SNS / Lambda | object write/delete commit (`tx.After`) | `SendMessage` / `Publish` / async Invoke with `Records` JSON; `s3:TestEvent` to SQS/SNS on configuration | async; per-key monotonic `sequencer` |
| Lambda -> CloudWatch Logs | container log stream | internal `PutLogEvents` | none (local) |
| Lambda async -> destinations (stretch) | invocation outcome | `SendMessage` / `Publish` / Invoke / PutEvents | per destination |

Principles:

- Deliveries are effects scheduled after the source's commit, so a source call never waits on its fan-out (SNS returns `MessageId` before delivery) and never holds two locks.
- The principal of a delivery is the AWS service principal (`sns.amazonaws.com`, `events.amazonaws.com`); the access recorder files these separately from user principals.
- The shared Ruler engine (`internal/ruler`) serves EventBridge patterns, SNS filter policies (behind SNS's operator allow-list and limits) and ESM filters, compiled once to a typed pattern ADT at configuration time (matching-engines.md implication 1).

### 8.1 Settling

The kernel's work tracker counts in-flight deliveries, queued async invokes, running invocations and due-but-unfired timers. `GET /_understudy/v1/settle?timeout=30s` returns when all are zero (or reports what is still pending on timeout). Tests no longer need sleep loops: publish, `understudy settle`, assert. In `Live` mode settle ignores timers that are not yet due, so a 5-minute schedule does not block it.

---

## 9. Admin API and CLI

### 9.1 Admin API

Mounted at `/_understudy/v1/` on the main listener; JSON in and out; errors as `{"error": {"code", "message", "hint"}}`. When the main listener binds a non-loopback address, every admin endpoint requires `Authorization: Bearer <UNDERSTUDY_ADMIN_TOKEN>`. The container listener exposes only `GET /clock` and `GET /health`.

| Endpoint | Purpose |
|---|---|
| `GET /health` | version, uptime, per-service status (enabled, disabled with reason, degraded), Docker and DynamoDB Local status |
| `POST /reset` | all state, or `{"service","account","region"}`; drains Lambda environments |
| `GET /clock`, `POST /clock` | read; `{"op":"freeze"|"resume"|"advance"|"set"|"scale", ...}`; returns timers fired |
| `GET/PUT/DELETE /faults/rules[/{id}]` | manage rules; PUT validates against the service catalogue |
| `GET /journal/events`, `GET /journal/stream`, `GET /journal/traces/{id}` | query, SSE stream, causal tree |
| `POST /snapshots` (body: archive out), `PUT /snapshots` (archive in) | save and load |
| `GET /resources` | every resource ARN by service/account/region, with a short state summary |
| `GET /coverage` | registry coverage, protocol compliance results, behavior switches |
| `GET /iam/usage`, `GET /iam/policy?principal=` | access record; generated least-privilege policy |
| `GET /settle` | wait for quiescence (section 8.1) |
| `GET /sns/subscriptions/{arn}/token` | read a pending confirmation token (http and email endpoints) |

### 9.2 CLI shape

The CLI is a thin client of the admin API (`admin.Client`), so everything scriptable through the CLI is scriptable over HTTP. Rules: non-interactive by default; `--output text|json` everywhere (JSON is stable and documented); `--endpoint` or `UNDERSTUDY_ENDPOINT`, default `http://localhost:4566`; exit codes 0 success, 1 operation failed, 2 usage error, 3 emulator unreachable, 4 condition not met (settle timeout, health not ready). Every `--help` has examples.

```
understudy serve [--config understudy.json] [--port 4566] [--bind 127.0.0.1] [--data-dir DIR] [--persist]
                 [--seed N] [--clock-start TIME] [--clock frozen] [--services sqs,sns,...]
                 [--accept-dynamodb-local-license] [--sigv4 off|verify] [--ready-file PATH] [--log-format json]
understudy health [--wait 30s]
understudy settle [--timeout 30s]
understudy reset [--service sqs] [--account 000000000000] [--region us-east-1]
understudy clock now | freeze | resume | advance 15m [--settle] | set 2026-12-31T23:59:00Z | scale 60
understudy faults list | add --service sqs --operation SendMessage --throttle --next 3 | remove ID | clear | load rules.json
understudy journal [--trace ID] [--service S] [--operation O] [--resource ARN] [--errors] [--since 10m] [--follow]
understudy journal trace ID            # causal tree
understudy snapshot save FILE | load FILE | inspect FILE | diff A B
understudy resources [--service S] [--account A]
understudy coverage [--service S] [--format text|md|json] [--switches]
understudy iam usage [--principal ARN] | iam policy --principal ARN [--collapse]
understudy env [--format sh|fish|dotenv|github|terraform] [--sdk go|python|js|cli]
understudy doctor [--json]
understudy cleanup                     # remove labeled containers left by dead instances
```

A trace, as developers will see it:

```
$ understudy journal trace 4127
e4127  12:00:00.120  sns:Publish            orders-topic                     OK     external boto3/1.43.109
 |- e4128  12:00:00.121  delivery sns->sqs  orders-topic -> orders-queue      OK     filter matched: {"type":["order"]}
 |   `- e4129  12:00:00.121  sqs:SendMessage    orders-queue                  OK     internal sns.delivery
 |       `- e4140  12:00:00.130  lambda:Invoke   worker (esm, batch 1/10)     OK     cold, init 412ms, 38ms
 |           |- e4141  dynamodb:PutItem   Locks                             FAILED ConditionalCheckFailedException
 |           `- e4142  s3:PutObject       artifacts/order-17.json           OK
 `- e4130  12:00:00.121  delivery sns->sqs  orders-topic -> audit-queue       FILTERED {"type":["audit"]} did not match
```

---

## 10. Configuration

Precedence: flags > environment (`UNDERSTUDY_*`) > config file (`understudy.json`, or `--config`) > defaults. JSON because it needs no dependency; parsed strictly (unknown keys are errors naming the key and the closest valid one) into a validated `config.Config` at the boundary. Durations are Go duration strings, times RFC 3339.

```json
{
  "listen": {"address": "127.0.0.1:4566"},
  "containerListener": "auto",
  "dataDir": ".understudy",
  "persist": false,
  "seed": 42,
  "clock": {"start": "2026-01-01T00:00:00Z", "mode": "live"},
  "accounts": {"default": "000000000000"},
  "services": {
    "dynamodb": {"acceptLicense": true},
    "cloudwatch": {"enabled": true}
  },
  "s3": {"baseDomains": ["localhost"]},
  "lambda": {
    "startConcurrency": 16,
    "idleTimeout": "10m",
    "enforceMemory": true,
    "hotReload": [{"function": "worker", "path": "./build/worker"}]
  },
  "sigv4": "off",
  "journal": {"capacity": 200000, "bodies": false},
  "faults": [
    {"id": "flaky-locks", "match": {"services": ["dynamodb"], "operations": ["PutItem"]},
     "action": {"throttle": {}}, "trigger": {"probability": 0.1}}
  ],
  "limits": {"maxBodyBytes": {"default": "8MiB", "lambda": "72MiB", "s3": "5GiB"}}
}
```

Defaults worth defending:

- **Port 4566**, so existing `AWS_ENDPOINT_URL=http://localhost:4566` setups from LocalStack work unchanged.
- **Default account `000000000000`**, same reason.
- **`containerListener: auto`** binds the extra listener only once a Lambda environment is needed, and only on the topology's gateway address, never `0.0.0.0`.
- **Body caps per service, not global** (go-toolchain.md section 5): SQS batches JSON-escape up to about 6x, Lambda `CreateFunction` carries a base64 zip up to about 70 MB, S3 streams to the blob store.

---

## 11. Startup and shutdown

- `serve` is instant: generated registries are static tables, no model parsing, no Docker contact. Docker is contacted when DynamoDB is enabled and accepted (background) or at the first Lambda need.
- The startup banner prints the endpoint, the env block from `understudy env`, the admin URL, enabled services and any warnings (`*.localhost` does not resolve here; Docker not reachable, Lambda execution disabled; DynamoDB disabled pending licence acceptance).
- `--ready-file PATH` writes `{"endpoint": "...", "pid": ...}` once listening, which together with `--port 0` makes parallel CI jobs trivial.
- Shutdown on SIGINT/SIGTERM: stop accepting, fail new invokes with a retriable 5xx, write the persistence snapshot if enabled, `DELETE ?force=1&v=1` every environment and the DynamoDB Local container in parallel with a hard bound of a few seconds, remove empty networks.
- Crash recovery: at startup, sweep containers labeled `understudy.dev/managed=true` whose `understudy.dev/owner` matches this data dir (or port in ephemeral mode) and whose instance is gone (the `flock` on the data dir decides), never another owner's (containers.md 9.3). `understudy.dev/test=1` is set by test harnesses only.

---

## 12. Developer workflows

### 12.1 Terraform-first

```hcl
provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  # AWS_ENDPOINT_URL=http://localhost:4566 in the environment; no endpoints block needed.
}
```

- No `skip_*` flags are required: STS `GetCallerIdentity` derives the account from the key, so the provider's one bootstrap call succeeds (tf-messaging.md implication 15).
- "Make the first poll final": every mutating call publishes its final state before returning (table `ACTIVE`, TTL `ENABLED`, secret version labeled); only the provider's own floors remain (SQS queue about 26 s create / 42 s destroy, Lambda 5 s, lifecycle about 55 s; tf-remaining.md section 2). The docs list them so nobody files a bug against understudy for provider waits.
- Every resource type has a conformance module run as apply, `plan -detailed-exitcode` (expect 0), mutate, apply, plan (expect 0), destroy. The journal adds **call-sequence assertions**: the test asserts the exact operations the provider made (tag calls before attribute calls, no calls on tags-only changes), so read-fidelity regressions show up as a precise diff instead of a mysterious exit code (tf-messaging.md implication 21).
- Pins: the suite pins hashicorp/aws 6.68.0 and runs a non-blocking canary against the latest provider (the `WarmThroughput` break arrived in a minor release; landscape.md section 10).

### 12.2 CI

```yaml
- run: understudy serve --port 0 --ready-file /tmp/us.json --seed 1 --accept-dynamodb-local-license &
- run: understudy health --wait 30s --endpoint "$(jq -r .endpoint /tmp/us.json)"
- run: eval "$(understudy env --format sh)" && terraform apply -auto-approve && go test ./...
- if: failure()
  run: understudy journal --errors --output json > journal-errors.json && understudy snapshot save state.snapshot
```

On failure the job keeps the error journal and a snapshot that reproduces the exact state locally with `understudy snapshot load`.

### 12.3 The inner test loop

```
understudy clock freeze
aws sqs send-message ...                 # message with a 15 minute delay
understudy clock advance 15m --settle    # delay elapses, ESM polls, Lambda runs, all finished
understudy journal --resource arn:aws:lambda:...:function:worker --since 1m
understudy faults add --service s3 --operation PutObject --error SlowDown --next 1
```

### 12.4 Go tests in-process

The public `understudytest` package starts the emulator inside the test binary (still standard library only):

```go
func TestPipeline(t *testing.T) {
	emu := understudytest.Start(t, understudytest.WithSeed(1), understudytest.WithFrozenClock(start))
	cfg := emu.AWSConfig()                     // endpoint + static credentials, as plain values
	// ... create resources, publish ...
	emu.Clock().Advance(t, 5*time.Minute)
	emu.Settle(t)
	got := emu.Journal().Trace(t, publishEventID)
	// assert on the causal tree
}
```

`t.Cleanup` stops it and removes any containers it started. The public surface is deliberately small (`Start`, options, and accessors returning `admin.Client` wrappers) and marked experimental until 1.0.

---

## 13. Testing strategy

### 13.1 Unit and property tests (main module)

- Pure engines are rapid property-tested: Ruler patterns (array/scalar intersection, rule-order independence, numeric never matches strings), SNS complexity counting, Logs filter patterns (complement and commutativity properties), schedule expressions, input transformers, SigV4 sign-then-verify and canonicalization invariance, `aws-chunked` encode/decode over random chunkings with and without trailers, every checksum (streaming equals one-shot; combine of parts equals whole), SQS MD5 of attributes, S3 `encoding-type=url` round trip, listing pagination (concatenated pages equal the unpaginated listing), CBOR (round trip, never panics, all 65536 half-float patterns), Query key ordering.
- Stateful models use `rapid.StateMachine`: SQS queue (visibility, FIFO group blocking, DLQ move) against a tiny reference model; the DynamoDB TTL shim; the Lambda pool state machine against a fake launcher.
- Time is tested inside `synctest.Test` bubbles with `httptest.NewTestServer`; a 20-second long poll runs in about zero seconds (go-toolchain.md implication 6). The emulator clock in `Live` mode reads wall time through `internal/wallclock`, which inside a bubble is the bubble's fake time, so the whole kernel is synctest-compatible.
- Golden vectors from research are tests: the 85 Ruler seeds, 167 Logs filter vectors, CRC/xxHash vectors, the 11 SQS MD5 vectors, the CBOR G01-G24 vectors.

### 13.2 Protocol compliance

The vendored Smithy cases run per protocol against generated test-service types: request cases assert the decoded input equals `params`; response cases assert status, headers and body; malformed cases assert the handler was not entered. Validation message bodies are expected to differ (AWS uses Coral text, not the Smithy framework's; error-matrix.md section 6) and are skipped with that reason. Target set: awsJson1_0 49, awsJson1_1 103, awsQuery 66, restJson1 229, restXml 178, rpcv2Cbor 68 server-runnable cases. The skip list is checked in with a reason per entry; results feed `understudy coverage`.

### 13.3 Wire-capture regression fixtures

The capture approach from the research becomes a test asset: per-SDK scripts in the conformance modules point each client at a recording listener (loopback only) and store request fixtures per SDK version. Golden routing tests replay them through the front door. When an SDK release changes its wire form (checksums, protocol switches), a fixture diff names it before users do.

### 13.4 Conformance suites (separate modules, run through `scripts/sealed`)

| Suite | Content |
|---|---|
| `conformance/go` | aws-sdk-go-v2 per service; error identity assertions (`ErrorCode()`, typed errors); CBOR CloudWatch |
| `conformance/python` | boto3 via uv: `client.exceptions.*`, `Error.Code`, `upload_fileobj` multipart, SigV2 presign |
| `conformance/js` | JS v3 via bun: `aws-chunked` streamed PUT, `QueueUrl` routing, `Date` header handling |
| `conformance/cli` | AWS CLI shell cases (`s3 cp`, `crc64nvme`) |
| `conformance/terraform` | per resource group: apply, empty plan, mutate, empty plan, destroy, plus journal call-sequence assertions; the DynamoDB plain/full/SSE tables |
| `conformance/e2e` | the phase 1 workload end to end on a frozen clock with a fixed seed; asserts the causal trace tree and a golden snapshot |

Docker-dependent tests carry a build tag, the `understudy.dev/test=1` label plus a per-run label, unique names and ephemeral ports, and delete by name. Function-runtime cases from containers.md 14.3 are mandatory: warm reuse, `Unhandled` errors, `Runtime.ExitError`, timeout then cold start, OOM, concurrent invokes get separate environments, chunked-trailer errors, the `Content-Type` regression, hot reload, crash recovery after `kill -9`, and the G4 deadline test (frozen then advanced clock leaves `get_remaining_time_in_millis()` within 100 ms of the timeout).

### 13.5 Parity fixtures

`parity/fixtures/<service>/<case>.json` hold AWS responses with provenance (`REC-RAW`, `REC-SDK`, `DOC`, `SDK-SOURCE`, `THIRD-PARTY`, `INFERRED`), a recorded date and normalizers for ids and timestamps. The replay harness runs the same request against understudy and diffs after normalization. The record tool exists in `parity/` but refuses to run unless a human sets an explicit acknowledgement variable and real credentials; agents never run it. Its backlog is R01-R18 from the research.

### 13.6 Gates

`go vet`, `golangci-lint` (strict config with `depguard` for stdlib-only, `forbidigo` for time), `go test -race ./...`, `just generate-check`, the coverage page drift check, and the snapshot round-trip test across every registered service.

---

## 14. Security

- **Listeners.** Main listener on `127.0.0.1` by default. The container listener and per-environment Runtime API listeners bind only the topology's gateway address, never `0.0.0.0`, and exist only while Lambda environments do. Binding the main listener elsewhere requires an admin token for `/_understudy/` endpoints. The Runtime API has no authentication in AWS's protocol (a path token is unsafe for the Python RIC; containers.md 4.5), so isolation is the per-environment port on the bridge address; this is documented as within the local Docker trust domain.
- **Docker socket.** Access is root-equivalent on the host; README and `doctor` say so. Function containers: memory limit (`Memory = MemorySwap`), `PidsLimit`, nofile ulimit, no privileged mode, no host network, `CapDrop: ["ALL"]` (to verify per runtime in conformance; UNVERIFIED that every runtime tolerates it), read-only code mounts, labels for sweeping.
- **Filesystem.** S3 keys never become paths (opaque blob ids under `os.Root`); zip extraction rejects non-local names and symlinks and caps sizes; extracted trees are content-addressed and read-only once written; the snapshot loader validates every tar entry name with `filepath.IsLocal` before writing anything.
- **Input limits.** Per-service body caps, `MaxHeaderBytes`, `MaxHeaderValueCount`, CBOR depth and length checks, JSON and XML decoders with size limits; region strings validated before they reach DynamoDB Local.
- **Secrets.** `sensitive` members are redacted in the journal and logs from the schema; credential types implement `slog.LogValuer`; secret values and KMS plaintext never appear in errors; minted credentials never logged. KMS uses real AES-256-GCM with the encryption context as AAD.
- **Identity.** Real-looking access keys are never decoded into foreign account numbers (section 2.1). SigV4 verification is opt-in (`--sigv4 verify`), but parsing always runs for routing.
- **Outbound network.** None at runtime except image pulls; no telemetry; DynamoDB Local telemetry disabled and egress-tested; the DynamoDB Local licence is surfaced before the pull.

---

## 15. Top risks and mitigations

| # | Risk | Mitigation |
|---|---|---|
| 1 | **Six wire protocols plus CBOR is the critical path**, and awsQuery has no reference server runner | One serializer/deserializer interface pair, generated serde, Smithy cases per protocol from day one, a tracer bullet that takes one operation per protocol end to end before breadth |
| 2 | **SDK releases change the wire under us** (checksums by default, SQS JSON switch, CloudWatch CBOR) | Wire-capture fixtures per SDK version (13.3), a weekly non-blocking canary against the latest SDKs and provider (no AWS needed), every service served on every protocol its model or clients use |
| 3 | **Terraform read-fidelity drift** (perpetual diffs, waiter hangs, provider crashes on missing dates; tf-remaining.md findings 1-3) | Empty-second-plan and journal call-sequence assertions per resource, the crash-prone fields in a "must always be present" response test, provider pin plus latest canary |
| 4 | **Container networking differs per topology** and fails as silent timeouts | Topology ADT, `understudy doctor` with a labeled probe container, first-`/next` timeout diagnostics naming address and causes, Linux bridge as the tested baseline and the others marked best effort |
| 5 | **Three time domains confuse users and implementers** | Distinct `EmuTime` type, `forbidigo`, the domain table in user docs, the G4 conformance tests, settle and stepwise advance so tests never sleep |
| 6 | **Deadlocks or lost wakeups** between cells, waiters and the scheduler | Single-cell rule with debug assertions, effects after unlock, subscribe-before-check waiters, `-race` plus synctest plus rapid state machines |
| 7 | **Snapshot compatibility breaks user fixtures** across versions | Format and per-section schema versions, migrations with golden fixtures for every past schema, newer-version refusal, byte-identical round-trip test per service |
| 8 | **DynamoDB depends on a proprietary image** with account-bound licence terms, and AWS can withdraw it | Explicit acceptance gate, digest pin with mirror, isolated failure (other services unaffected), counsel review before release (dynamodb-local-terms.md 5.4); a native DynamoDB stays a roadmap option |
| 9 | **Fidelity claims without AWS recordings** | Provenance-labeled fixtures, behavior switches in one ledger, coverage output that separates shape conformance, behavior tests and recorded evidence; the R01-R18 backlog for a human |
| 10 | **Fault injection misleads users** because SDKs retry injected errors | Journal shows each attempt and `Hit/Of`; rule validation rejects codes the service never sends; the CLI warns when a rule targets a retryable code; docs give fail-fast recipes |
| 11 | **Resource exhaustion in CI** (big uploads, many environments, journal growth) | Disk spill for large objects, pool caps and start semaphore, bounded journal ring with gap events, per-service body caps |
| 12 | **Platform features cost latency on every call** | Journal events are fixed-size structs appended to a ring (bodies off by default), fault matching is indexed by (service, operation), access recording is a map increment; a benchmark gate on the pipeline overhead per call |

---

## 16. Decisions that need ADRs

1. Hosted Runtime API instead of the RIE (deviates from the brief's wording).
2. Default port 4566 and default account `000000000000`.
3. Account-from-key policy (minted keys and 12-digit keys only; no decoding of arbitrary real keys).
4. `encoding/json/v2` behind `protocol/jsonx`; generated schema serde rather than runtime interpretation.
5. Unimplemented error code string and status (501 `NotImplemented` proposed).
6. Hand-ported xxHash (stdlib-only rule; no dependency needed, but the port is a maintained artifact).
7. IAM action mapping: vendor a derived table from the Service Reference until its licence is known.
8. Snapshot format v1 (tar layout, canonical JSON, DynamoDB via Scan export, no WAL).
9. Clock semantics: forward-only emulator time, stepwise advance, ESM visibility pinning outside `Live` mode.
10. DNS: no responder in phase 1; IP-literal endpoints in containers; doctor-guided host configuration.
11. S3 cross-region bucket access is lenient; queue URLs echo the request host (Terraform import of queue URLs unsupported).
12. Container listener on the gateway address only, created on demand.
13. DynamoDB Local licence gate behavior and notice text (needs counsel).
14. Public packages `admin` and `understudytest` and their stability promise.

---

## 17. Sequencing (tracer bullet first)

1. **Kernel slice**: `scope`, `identity`, `clock`, `journal`, `fault`, `store`, `registry`, `awserr`, `edge`, admin `health`/`clock`/`journal`, the generator for one service. SQS over awsJson1_0 with `CreateQueue`, `SendMessage`, `ReceiveMessage` (long poll on the clock), `DeleteMessage`. The journal, faults, settle and snapshot round trip work on this slice before any second service exists.
2. **Protocol breadth**: awsQuery (SNS), restXml (S3 bucket plus PutObject/GetObject), restJson1 (Lambda control plane), awsJson1_1 (Logs), rpcv2Cbor (CloudWatch `PutMetricAlarm`/`DescribeAlarms`), each with its Smithy cases green.
3. **First causal chain**: SNS -> SQS -> ESM -> Lambda (Python and `provided.al2023`) -> CloudWatch Logs, with the e2e trace tree and the G4 deadline test.
4. **Terraform loop**: STS, IAM, KMS, Secrets Manager, EventBridge, the DynamoDB shim; the Terraform conformance modules with empty-second-plan and call-sequence assertions.
5. **Depth**: S3 checksums and multipart, SQS FIFO and move tasks, SNS filter grammar, Logs filter patterns, hot reload, function URLs, IAM policy generation, coverage page.
