package lambda

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
	"github.com/sustinbebustin/understudy/internal/service/lambda/hotreload"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Invoke request limits (lambda-api.md 8.2, lambda-events.md 8). The
// synchronous one is 6 MiB, the figure Lambda's front end names when it
// refuses a request (AWS's own Lambda test tool, aws/aws-lambda-dotnet
// LambdaRuntimeAPI.cs); the 6,291,556 bytes of runtimeapi.MaxResponseBytes
// limit the response only.
const (
	maxSyncRequest  = 6_291_456
	maxAsyncRequest = 1_048_576
	// maxClientContext is X-Amz-Client-Context's limit, base64 included.
	maxClientContext = 3583
	// maxTail is how much of an invocation's log X-Amz-Log-Result carries.
	maxTail = 4096
)

// defaultLogSettle is the settle barrier: how long after a runtime's
// response its last output lines may still be in flight (containers.md 7
// measured them at most 0.3 ms behind).
const defaultLogSettle = 20 * time.Millisecond

const invokeNote = "an alias with weighted routing always runs its primary version; asynchronous invocations retry but do not deliver to destinations or dead-letter queues; " +
	"LoggingConfig reaches the function as AWS_LAMBDA_LOG_FORMAT and AWS_LAMBDA_LOG_LEVEL, but Lambda's own INIT_START, START, END and REPORT lines stay text and SystemLogLevel filters nothing"

// The data plane's decisions the architecture leaves open:
//
//   - The data plane is an Execution built beside the Service (NewExecution)
//     rather than part of it: it holds the pool, the environments'
//     credentials, the asynchronous queue's cells and the hot-reload
//     overlays, and reads the functions through the Service's state.
//   - Each environment's credentials are a slot of its function version's
//     generation: a session of the execution role (session name: the
//     function name) whose source names the slot. The pool key hashes
//     them, so a slot is one warm environment, and the key of a call a
//     function makes names the invocation its slot runs. The pool's own
//     environment id names log streams. Execution generations only rise,
//     across resets too, because the pool remembers the highest it saw.
//   - A call made by function code links to the call that started the
//     invocation (Invoke, or the Invoke an asynchronous event came from),
//     which is also the Invocation event's parent: journal events cannot
//     be amended, so the Invocation event, which carries the duration, is
//     appended once the invocation ends, after the calls it caused. An
//     event source mapping's invocation and its function's calls hang from
//     the batch's earliest message's enqueue event instead, with the
//     others' as the Invocation event's links; the mapping's Invoke, which
//     fault rules match, is their sibling. Init-phase calls run inside the
//     first invocation and link the same way. A call between invocations
//     roots its own trace.
//   - Init is the wall time of the container start of a cold invocation;
//     the runtime's own init until its first /next is part of Duration,
//     as in Lambda's suppressed init.
//   - REPORT's Max Memory Used is the memory the container holds when the
//     invocation ends, read once from the daemon's statistics (AWS reports
//     the peak, which the Docker API does not expose under cgroup v2); it
//     is left out when the container is gone. A runtime failure adds the
//     new-style Status and Error Type.
//   - All of an invocation's log lines are stamped with the emulator time
//     its batch is cut at; output that arrives between invocations goes
//     ahead of the next START, or is cut on its own once it reaches a
//     PutLogEvents request's size or the environment's container stops.
//     A batch is written in order per stream, as tracked work.
//   - An image function runs the image its last deployment recorded: by
//     repository digest, or by image ID for a local build without one.
//     Resolved images are remembered until a container start fails, so a
//     warm invocation makes no Docker round trip.
//   - An empty payload reaches the function as {}. A weighted alias runs
//     its primary version. Asynchronous events keep their queue in the
//     Execution's own cells under the shared gate, which AsyncQueue
//     exports for snapshots; Drain empties it, and a reserved concurrency
//     of 0 discards events without retries.
//   - A client's synchronous invocation runs to its end or timeout after
//     the client goes away, as on AWS; Close ends it. Drain waits for the
//     invocations of its scope already running and refuses later ones
//     with ServiceException until RearmPollers, which the admin API calls
//     once the reset or load has replaced the state, so no invocation
//     outlives the state it started in.
//   - A hot-reload directory serves every version of its function; the
//     admin API's overlays end with a reset of their scope, configured
//     ones never. UpdateFunctionCode under an overlay records the
//     shadowed-update Advisory on its own call (Service.overlayDir).

// ExecutionOptions configure the data plane. The zero value has no Docker
// daemon.
type ExecutionOptions struct {
	// Pool runs execution environments. nil means no Docker daemon is
	// reachable: the control plane keeps working and Invoke answers
	// NotImplemented naming Docker. The caller owns the pool; Close
	// drains it for shutdown.
	Pool *pool.Pool
	// Docker names the Docker endpoint the pool uses, for the errors that
	// say it cannot be reached; empty when none is configured.
	Docker string
	// NoPool says why Pool is nil and how to change that, such as Docker
	// being disabled by configuration; empty means no Docker daemon is
	// reachable at Docker, and Invoke tells the caller to start one.
	NoPool string
	// Images makes base and function images present; required with Pool.
	Images ImageSource
	// Code extracts zip packages for the read-only /var/task mount;
	// required with Pool.
	Code *codestore.Store
	// Logs follows function containers' output; nil delivers only the
	// lines Lambda itself writes (INIT_START, START, END, REPORT).
	Logs LogSource
	// LogSettle is the settle barrier; zero means 20 ms.
	LogSettle time.Duration
	// HotReload binds directories as functions' code from the
	// configuration (lambda.hotReload).
	HotReload []HotReloadBinding
	// Watch times the hot-reload watchers; zero values take the defaults.
	Watch hotreload.Options
}

// Execution is the Lambda data plane: Invoke in its three modes, the
// asynchronous queue, function logs and hot reload. It reads the state of
// the Service it was built for. It links calls made by function code to
// their invocations (kernel.Linker), stops environments on reset (the
// admin API's Drainer), binds hot-reload directories (HotReloader) and
// pre-pulls runtime images (ImagePuller).
type Execution struct {
	svc    *Service
	k      *kernel.Kernel
	pool   *pool.Pool
	docker string
	noPool string
	images ImageSource
	logSrc LogSource
	settle time.Duration
	watch  hotreload.Options

	envs     *environments
	trees    *codeTrees
	running  *concurrency
	groups   logGroups
	overlays *overlays
	async    kernel.HandledKind
	queues   *store.Partitioned[aws.Scope, asyncQueue]

	imagesMu sync.Mutex
	resolved map[imageRequest]string

	// noDockerLogged logs the first invocation refused for want of a pool.
	noDockerLogged sync.Once

	ctx  context.Context //nolint:containedctx // log pumps and watchers started by later calls must outlive them and end at Close
	stop context.CancelFunc
	wg   sync.WaitGroup
}

var _ kernel.Linker = (*Execution)(nil)

// NewExecution starts the configured hot-reload watchers, registers the
// asynchronous queue's timer and binds Invoke for svc. d is the Lambda
// service's dependencies, as svc was built with; Close releases what it
// holds.
func NewExecution(svc *Service, d kernel.ServiceDeps, o ExecutionOptions) (*Execution, error) {
	switch {
	case svc == nil:
		return nil, errors.New("lambda: NewExecution needs the Lambda service whose functions it runs")
	case d.Identity == nil:
		return nil, errors.New("lambda: ServiceDeps.Identity is nil; Invoke mints each execution environment's credentials in the identity store")
	case o.Pool != nil && (o.Images == nil || o.Code == nil):
		return nil, errors.New("lambda: ExecutionOptions.Images and Code are required with a Pool; pass the launcher and the code store")
	}
	settle := o.LogSettle
	if settle == 0 {
		settle = defaultLogSettle
	}
	ctx, stop := context.WithCancel(context.Background())
	trees := newCodeTrees(o.Code, svc.blobs, d.Kernel.Log())
	x := &Execution{
		svc:      svc,
		k:        d.Kernel,
		pool:     o.Pool,
		docker:   o.Docker,
		noPool:   o.NoPool,
		images:   o.Images,
		logSrc:   o.Logs,
		settle:   settle,
		watch:    o.Watch,
		envs:     newEnvironments(d.Identity, d.Kernel.Clock(), trees),
		trees:    trees,
		running:  newConcurrency(),
		overlays: newOverlays(),
		queues:   store.NewPartitioned(d.Kernel.Gate(), asyncCodec(), newAsyncQueue),
		resolved: map[imageRequest]string{},
		ctx:      ctx,
		stop:     stop,
	}
	for _, b := range o.HotReload {
		if err := x.startOverlay(b.Scope, b.Function, b.Dir, configured); err != nil {
			// Only the watchers started so far are this Execution's: the
			// pool and the code store stay the caller's.
			x.stop()
			x.wg.Wait()
			return nil, fmt.Errorf("lambda: hot reload of %s from the configuration: %w", b.Function, err)
		}
	}
	x.async = d.Kernel.Timers().Handle(TimerAsync, x.onAsync)
	svc.overlayDir = x.overlays.dirOf
	lambdaapi.NewBinder(d.Binder).Invoke(x.invoke, partial(invokeNote), kernel.WithResources(invokedFunction))
	return x, nil
}

// invokedFunction names the function, qualified as the input names it, an
// Invoke addresses, so fault rules can match it by resource before the
// handler runs; an unparsable name names nothing, and the handler answers
// it.
func invokedFunction(c *kernel.Call, in *lambdaapi.InvokeInput) []aws.ARN {
	r, err := parseFunctionRef(c, in.FunctionName, in.Qualifier)
	if err != nil {
		return nil
	}
	a, perr := aws.ParseARN(r.qualifiedARN())
	if perr != nil {
		return nil
	}
	return []aws.ARN{a}
}

// Close stops every execution environment, log stream and watcher, and
// gives back the extracted code and the minted credentials, at shutdown.
// No invocation starts once it is called; those running are ended, an
// asynchronous attempt as one that never reached the function, and Close
// waits, while ctx lasts, until their environments are removed, so no
// container outlives the process and none runs on released code or
// revoked credentials.
func (x *Execution) Close(ctx context.Context) error {
	running := x.running.shutdown()
	var errs []error
	if x.pool != nil {
		errs = append(errs, x.pool.Drain(ctx, pool.All{}, pool.Shutdown))
	}
	x.stop()
	if err := x.running.wait(ctx, running); err != nil {
		errs = append(errs, fmt.Errorf("lambda: shutdown stopped waiting for its invocations to end: %w; their containers may be left running, labeled as understudy's", err))
	}
	x.wg.Wait()
	if x.trees.store != nil {
		errs = append(errs, x.trees.release())
	}
	x.envs.revokeAll()
	return errors.Join(errs...)
}

// Name is the service whose reset drains the environments: lambda.
func (x *Execution) Name() ref.ServiceID { return x.svc.Name() }

// Drain stops the execution environments and empties the asynchronous
// queues of scope before a reset or snapshot load replaces its state, and
// ends the hot-reload overlays bound through the admin API there. From
// the moment it is called no invocation of scope starts, until
// RearmPollers follows the reset or load; idle environments stop at once,
// and Drain waits, while ctx lasts, for the invocations of scope already
// running to finish, whose environments are removed on release. So none
// writes into the new state. A failed Drain lets invocations start again
// and keeps the queued events and the overlays, as the reset or load then
// replaces nothing; only the stopped environments are gone, and the next
// invocations start new ones.
func (x *Execution) Drain(ctx context.Context, scope kernel.ResetScope) error {
	running := x.running.hold(scope)
	var errs []error
	for _, fn := range x.envs.drop(scope) {
		if x.pool != nil {
			errs = append(errs, x.pool.Drain(ctx, pool.Function{ARN: fn}, pool.Reset))
		}
	}
	if err := x.running.wait(ctx, running); err != nil {
		errs = append(errs, fmt.Errorf("lambda: the drain stopped waiting for the invocations of the reset scope: %w; their environments are stopped, and the queued asynchronous invocations and hot-reload overlays are kept", err))
	}
	if err := errors.Join(errs...); err != nil {
		x.running.resume()
		return err
	}
	release := x.k.Gate().Exclusive()
	x.queues.Reset(func(s aws.Scope) bool { return !scope.Covers(s) })
	release()
	x.overlays.drop(scope)
	return nil
}

// Link links a call made with an environment's minted credentials to the
// invocation the environment runs, init phase included; calls between
// invocations link to nothing.
func (x *Execution) Link(p identity.Principal, _ netip.AddrPort) (ref.Cause, bool) {
	return x.envs.link(p)
}

// target is the function version an invocation runs, read from the state
// when it is dispatched.
type target struct {
	ref     functionRef
	version Version
	// invoked is the ARN the caller invoked, qualified when it named a
	// version or alias; role the version's execution role.
	invoked aws.ARN
	role    aws.ARN
	// generation is the function's code generation for $LATEST and zero
	// for a published version, which never changes.
	generation uint64
	// incarnation is the function's Incarnation, so a function deleted and
	// created again under its name, whose generations start over, still
	// differs here.
	incarnation string
	// reserved is the function's reserved concurrency, nil when none;
	// unreserved the account's concurrency left after every reservation.
	reserved   *int32
	unreserved int32
	async      asyncConfig
}

// resolve reads the version ref names, as it was before an update in
// progress (Version.serving); a version that is not Active cannot be
// invoked. $LATEST.PUBLISHED is NoPublishedVersionException for every
// function, as only Lambda Managed Instances publish it.
func (x *Execution) resolve(r functionRef) (target, *awserr.Error) {
	return view(x.svc, r.scope, func(st *State) (target, *awserr.Error) {
		if r.qualifier == latestPublished {
			if _, err := st.function(r); err != nil {
				return target{}, err
			}
			return target{}, errNoPublishedVersion(r.arn())
		}
		f, v, err := st.lookup(r)
		if err != nil {
			return target{}, err
		}
		switch v.Status.(type) {
		case Creating, PullingImage:
			return target{}, errFunctionState(lambdaapi.StatePending)
		case Failed:
			return target{}, errFunctionState(lambdaapi.StateFailed)
		case Active:
		}
		v, generation := v.serving(f.Generation)
		role, perr := aws.ParseARN(v.Config.Role)
		if perr != nil {
			return target{}, errService(fmt.Sprintf("Lambda cannot run %s: its stored execution role %q is not an ARN (%v). Set a valid role with UpdateFunctionConfiguration.", r.qualifiedARN(), v.Config.Role, perr))
		}
		v.Config.Environment = maps.Clone(v.Config.Environment)
		t := target{ref: r, version: v, invoked: invokedARN(r), role: role, incarnation: f.Incarnation, unreserved: st.unreserved(""), async: asyncConfigOf(f, r.qualifier)}
		if v.Number == latest {
			t.generation = generation
		}
		if f.Reserved != nil {
			t.reserved = new(*f.Reserved)
		}
		return t, nil
	})
}

// invoke is the Invoke operation: RequestResponse runs the function and
// answers 200 with its result, Event queues it and answers 202, DryRun
// only checks it can be invoked and answers 204 (lambda-api.md 8).
func (x *Execution) invoke(ctx context.Context, c *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
	r, err := parseFunctionRef(c, in.FunctionName, in.Qualifier)
	if err != nil {
		return nil, err
	}
	mode := lambdaapi.InvocationTypeRequestResponse
	if in.InvocationType != nil {
		mode = *in.InvocationType
	}
	payload, err := checkPayload(in.Payload, mode)
	if err != nil {
		return nil, err
	}
	clientContext, err := decodeClientContext(in.ClientContext)
	if err != nil {
		return nil, err
	}
	t, err := x.resolve(r)
	if err != nil {
		// Invoke names a missing function by its $LATEST ARN, unlike the
		// read operations (test_invoke_exceptions).
		if r.qualifier == "" && err.ID.Status == http.StatusNotFound {
			return nil, errFunctionNotFound(r.arn() + ":" + latest)
		}
		return nil, err
	}
	touch(c, r.qualifiedARN())
	switch mode {
	case lambdaapi.InvocationTypeDryRun:
		return &lambdaapi.InvokeOutput{StatusCode: http.StatusNoContent}, nil
	case lambdaapi.InvocationTypeEvent:
		if err := x.enqueue(c, &t, payload); err != nil {
			return nil, err
		}
		return &lambdaapi.InvokeOutput{StatusCode: http.StatusAccepted}, nil
	case lambdaapi.InvocationTypeRequestResponse:
	}
	tail := in.LogType != nil && *in.LogType == lambdaapi.LogTypeTail
	if _, external := c.Origin().(ref.External); external {
		ctx = outliveCaller(ctx)
	}
	res, err := x.runSync(ctx, c, t, syncRun{payload: payload, clientContext: clientContext})
	if err != nil {
		return nil, err
	}
	out := &lambdaapi.InvokeOutput{
		StatusCode:      http.StatusOK,
		ExecutedVersion: new(t.version.Number),
		Payload:         res.outcome.Response(ids.LambdaRequestID(c.ID())),
	}
	if h := runtimeapi.FunctionErrorHeader(res.outcome); h != "" {
		out.FunctionError = new(h)
	}
	if tail {
		out.LogResult = new(base64.StdEncoding.EncodeToString(lastBytes(res.tail, maxTail)))
	}
	return out, nil
}

// syncRun is a RequestResponse invocation run on behalf of a call; see
// invocationCause for where it hangs in the journal.
type syncRun struct {
	payload       []byte
	clientContext string
	// trace is the X-Ray trace header the function runs under, as a
	// function URL's event reports it; empty derives one when it runs.
	trace string
}

// outliveCaller is ctx without its cancellation: a synchronous invocation
// a client asked for runs to its end or its timeout after the client goes
// away, as on AWS. The function's Timeout bounds it, and Close ends it
// (execute). An internal caller's context, such as tracked work the kernel
// ends at shutdown, is kept as it is.
func outliveCaller(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

// runSync runs a RequestResponse invocation of t under c's request id: the
// Invoke operation's, and the in-package entry of function URLs, which
// resolve t with resolve first.
func (x *Execution) runSync(ctx context.Context, c *kernel.Call, t target, in syncRun) (ran, *awserr.Error) {
	cause, links := invocationCause(c)
	return x.execute(ctx, &run{
		target:        t,
		request:       ids.LambdaRequestID(c.ID()),
		payload:       in.payload,
		clientContext: in.clientContext,
		parent:        c,
		cause:         cause,
		links:         links,
		trace:         in.trace,
	})
}

// invocationCause is the parent and links of the Invocation event a
// synchronous run on behalf of c journals, which the calls the function
// makes hang from too: c's own event, except for an event source mapping's
// Invoke, whose invocation the batch's messages caused rather than the
// Invoke that carries it (ADR 0007). That Invoke continues the earliest
// message's enqueue event and links the others' (pollers.invoke), and so
// does its invocation.
func invocationCause(c *kernel.Call) (ref.Cause, []ref.EventID) {
	if in, ok := c.Origin().(ref.Internal); ok && in.Via == ref.LambdaESM {
		return c.Cause(), c.Links()
	}
	return ref.Cause{Parent: c.Event(), Trace: c.Cause().Trace}, nil
}

// checkPayload enforces the payload limit of the invocation mode and
// Lambda's JSON requirement, which includes UTF-8: Go's parser would
// replace invalid bytes rather than refuse them. An empty payload is the
// empty object, as SDKs send no body for a nil payload.
func checkPayload(p []byte, mode lambdaapi.InvocationType) ([]byte, *awserr.Error) {
	limit := maxSyncRequest
	if mode == lambdaapi.InvocationTypeEvent {
		limit = maxAsyncRequest
	}
	if len(p) > limit {
		return nil, errPayloadTooLarge(limit)
	}
	if len(p) == 0 {
		return []byte("{}"), nil
	}
	if at := invalidUTF8At(p); at >= 0 {
		return nil, errPayloadNotJSON(fmt.Errorf("invalid UTF-8 byte 0x%02x at offset %d", p[at], at))
	}
	var v any
	if err := json.Unmarshal(p, &v); err != nil {
		return nil, errPayloadNotJSON(err)
	}
	return p, nil
}

// invalidUTF8At is the offset of the first byte of p that is not valid
// UTF-8, or -1 when p is valid.
func invalidUTF8At(p []byte) int {
	for i := 0; i < len(p); {
		r, n := utf8.DecodeRune(p[i:])
		if r == utf8.RuneError && n == 1 {
			return i
		}
		i += n
	}
	return -1
}

// decodeClientContext reads X-Amz-Client-Context: base64 of a JSON object,
// handed to the function as Lambda-Runtime-Client-Context.
func decodeClientContext(cc *string) (string, *awserr.Error) {
	if cc == nil || *cc == "" {
		return "", nil
	}
	if len(*cc) > maxClientContext {
		return "", errClientContext("is " + strconv.Itoa(len(*cc)) + " bytes, more than the " + strconv.Itoa(maxClientContext) + " allowed")
	}
	raw, err := base64.StdEncoding.DecodeString(*cc)
	if err != nil {
		return "", errClientContext("is not valid base64")
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", errClientContext("does not decode to a JSON object")
	}
	return string(raw), nil
}

// lastBytes is the end of b, at most n bytes long.
func lastBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[len(b)-n:]
}

// run is one attempt at running an invocation.
type run struct {
	target        target
	request       ids.LambdaRequestID
	payload       []byte
	clientContext string
	// parent is the call the attempt's own calls (log delivery) are
	// children of: the Invoke call, or the asynchronous queue's timer.
	parent *kernel.Call
	// parentWrites is held by whoever writes on parent while the attempt's
	// log lines may be in delivery (onBehalf): settleAsync for the timer;
	// nil when nothing does.
	parentWrites *sync.Mutex
	// cause is the parent of the attempt's Invocation event and of every
	// call the function makes while it runs: the Invoke call's event,
	// carried in the queue for an asynchronous invocation. links are the
	// Invocation event's related events.
	cause ref.Cause
	links []ref.EventID
	// trace is the X-Ray trace header the caller fixed; empty derives one
	// from the request id when the attempt runs (traceID).
	trace string
}

// ran is an attempt that reached the function: its outcome and its log
// from START to REPORT.
type ran struct {
	outcome runtimeapi.Outcome
	tail    []byte
}

// execute runs r in an execution environment: concurrency is checked
// before any container work, then a slot's credentials and the version's
// spec lease a warm environment or start a cold one, the invocation runs,
// its log lines are cut after the settle barrier and delivered, and the
// journal records the Invocation. The error is a throttle (429) or a
// failure of the emulator (500); a function error is an outcome. Close
// ends the invocation, whoever started it.
func (x *Execution) execute(ctx context.Context, r *run) (ran, *awserr.Error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(x.ctx, cancel)() //nolint:contextcheck // ctx stays the invocation's; x.ctx only cancels it at Close
	t := &r.target
	fn := fnVersion{scope: t.ref.scope, name: t.ref.name, version: t.version.Number}
	release, err := x.running.acquire(t)
	if err != nil {
		// A drain's refusal is no throttle and is not journaled as one.
		if err.ID.Shape == lambdaapi.IdentityTooManyRequestsException().Shape {
			x.record(ctx, r, journal.Invocation{Function: fn.arn(), Version: fn.version, Outcome: journal.Throttled{}})
		}
		return ran{}, err
	}
	defer release()
	if x.pool == nil {
		return ran{}, x.errNoDocker(ctx)
	}
	l, err := x.lease(ctx, r, fn)
	if err != nil {
		// The pool's global cap throttles as the concurrency counters do,
		// and is journaled the same way.
		if err.ID.Shape == lambdaapi.IdentityTooManyRequestsException().Shape {
			x.record(ctx, r, journal.Invocation{Function: fn.arn(), Version: fn.version, Outcome: journal.Throttled{}})
		}
		return ran{}, err
	}
	defer x.envs.release(l.slot)
	return x.runIn(ctx, r, fn, l)
}

// leased is an environment leased for one invocation.
type leased struct {
	slot *slot
	env  *pool.Env
	// cold is set when the environment was started for this invocation;
	// init is then how long the start took.
	cold bool
	init time.Duration
}

// lease takes a slot of fn's current generation, builds its spec and
// leases the slot's warm environment or starts a cold one. The caller
// releases the slot.
func (x *Execution) lease(ctx context.Context, r *run, fn fnVersion) (leased, *awserr.Error) {
	t := &r.target
	w := leaseFor{code: t.generation, incarnation: t.incarnation, role: t.role, cause: r.cause}
	overlayDir := ""
	switch code := t.version.Code.(type) {
	case ZipCode:
		overlayDir, w.overlay = x.overlays.current(fnKey{scope: fn.scope, name: fn.name})
		if overlayDir == "" {
			w.tree = code.Sha256
		}
	case ImageCode:
	}
	s, gen, err := x.envs.lease(fn, w)
	if err != nil {
		return leased{}, errService(fmt.Sprintf("Lambda could not prepare an execution environment for %s: %v", fn.arn(), err))
	}
	spec, err := x.spec(ctx, t, s, gen, overlayDir)
	if err != nil {
		x.envs.release(s)
		return leased{}, errService(fmt.Sprintf("Lambda could not prepare an execution environment for %s: %v", fn.arn(), err))
	}
	started := x.k.Clock().Stopwatch()
	env, err := x.pool.Acquire(ctx, spec)
	if err != nil {
		x.envs.release(s)
		return leased{}, x.acquireError(fn, spec.Image, err)
	}
	l := leased{slot: s, env: env, cold: s.env != env}
	if l.cold {
		l.init = started()
		s.env, s.stream = env, x.openStream(env, t, spec.LogGroup) //nolint:contextcheck // the stream's follower outlives the invocation and ends at Close
	}
	return l, nil
}

// runIn runs r in the leased environment: START, the invocation, the
// settle barrier, END and REPORT; then it releases the environment with
// the outcome, journals the Invocation and delivers the log.
func (x *Execution) runIn(ctx context.Context, r *run, fn fnVersion, l leased) (ran, *awserr.Error) {
	t := &r.target
	stream := l.slot.stream
	stream.begin(l.cold, initStartLine(t), startLine(r.request, t.version.Number), &onBehalf{call: r.parent, writes: r.parentWrites})
	trace := r.trace
	if trace == "" {
		trace = traceID(r.request, x.k.Clock().Now().Wire().Unix())
	}
	elapsed := x.k.Clock().Stopwatch()
	outcome, invErr := l.env.Invoke(ctx, runtimeapi.Invocation{
		RequestID:     r.request,
		InvocationID:  string(x.k.IDs().LambdaRequestID()),
		FunctionARN:   t.invoked,
		TraceID:       trace,
		ClientContext: r.clientContext,
		Payload:       r.payload,
		Timeout:       time.Duration(t.version.Config.Timeout) * time.Second,
	})
	duration := elapsed()
	var deliver bool
	var tail []byte
	if invErr != nil {
		x.logRelease(ctx, x.pool.Release(ctx, l.env, nil))
		deliver = stream.cut()
	} else {
		if to, ok := outcome.(runtimeapi.Timeout); ok {
			duration = to.After
		}
		x.settleLogs()
		used := x.memoryUsed(ctx, l.env, r)
		tail, deliver = stream.end(endLine(r.request), reportLine(r.request, duration, t.version.Config.MemorySize, used, l.cold, l.init, outcome))
		x.logRelease(ctx, x.pool.Release(ctx, l.env, outcome))
		inv := journal.Invocation{Function: fn.arn(), Version: fn.version, Env: l.env.ID(), Cold: l.cold, Duration: duration, Outcome: invocationOutcome(outcome)}
		if l.cold {
			inv.Init = l.init
		}
		x.record(ctx, r, inv)
	}
	if deliver {
		x.startDelivery(stream) //nolint:contextcheck // log delivery outlives the invocation as tracked work of the kernel
	}
	if invErr != nil {
		return ran{}, errService(fmt.Sprintf("Lambda could not run %s: %v", fn.arn(), invErr))
	}
	return ran{outcome: outcome, tail: tail}, nil
}

// record journals d as caused by r's cause, with r's links, at its
// parent's admission instant, in the function's scope.
func (x *Execution) record(ctx context.Context, r *run, d journal.Detail) {
	e := journal.Event{Cause: r.cause, Links: r.links, At: r.parent.Admitted(), Scope: r.target.ref.scope, Detail: d}
	if _, err := x.k.Journal().Append(e); err != nil {
		x.k.Log().LogAttrs(ctx, slog.LevelWarn, "journal refused a Lambda invocation event",
			slog.String("function", r.target.ref.qualifiedARN()), slog.String("error", err.Error()))
	}
}

// memoryUsed is the memory env's container holds once r's invocation ended,
// for REPORT's Max Memory Used; nil when the daemon cannot say, as for a
// container a timeout or a crash already removed, and REPORT then leaves
// the field out rather than invent it.
func (x *Execution) memoryUsed(ctx context.Context, env *pool.Env, r *run) *uint64 {
	used, err := env.MemoryUsed(ctx)
	if err != nil {
		x.k.Log().LogAttrs(ctx, slog.LevelDebug, "reading a Lambda container's memory failed; REPORT leaves out Max Memory Used",
			slog.String("function", r.target.ref.qualifiedARN()), slog.String("error", err.Error()))
		return nil
	}
	return &used
}

func (x *Execution) logRelease(ctx context.Context, err error) {
	if err != nil {
		x.k.Log().LogAttrs(ctx, slog.LevelWarn, "removing a Lambda execution environment failed", slog.String("error", err.Error()))
	}
}

// acquireError maps a failed lease: the pool's global cap is a throttle,
// anything else a failure to start a container, after which image is
// resolved afresh in case it went missing.
func (x *Execution) acquireError(fn fnVersion, image string, err error) *awserr.Error {
	var capacity *pool.CapacityError
	var closed *pool.ClosedError
	switch {
	case errors.As(err, &capacity):
		return errThrottled(reasonAccountLimit)
	case errors.As(err, &closed):
		return errShuttingDown()
	}
	x.forgetImage(image)
	return errService(fmt.Sprintf("Lambda could not start an execution environment for %s: %v. Check that Docker is running%s, or run `understudy doctor` to diagnose.", fn.arn(), err, x.dockerAt()))
}

// errNoDocker answers an invocation when there is no pool: the reason the
// wiring gave, else no Docker daemon reachable. The Execution never gets a
// pool later, so it is 501 NotImplemented, which no SDK retries, rather
// than a 500 every SDK would retry in vain; the server log says it once.
func (x *Execution) errNoDocker(ctx context.Context) *awserr.Error {
	const kept = "; functions, versions and every other Lambda resource keep working."
	msg := "Lambda cannot run functions because no Docker daemon is reachable" + x.dockerAt() + ". Start Docker and restart understudy, or run `understudy doctor` to diagnose" + kept
	if x.noPool != "" {
		msg = "Lambda cannot run functions: " + x.noPool + kept
	}
	x.noDockerLogged.Do(func() {
		x.k.Log().LogAttrs(ctx, slog.LevelWarn, "Lambda refuses every invocation; later refusals are not logged", slog.String("reason", msg))
	})
	return &awserr.Error{ID: awserr.NotImplemented(), Message: msg, Origin: awserr.Generic{}}
}

func (x *Execution) dockerAt() string {
	if x.docker == "" {
		return " (no Docker endpoint is configured)"
	}
	return " at " + x.docker
}

// invokedARN is the ARN r names, as the runtime's
// Lambda-Runtime-Invoked-Function-Arn: qualified when it names a version
// or alias.
func invokedARN(r functionRef) aws.ARN {
	resource := "function:" + r.name
	if r.qualifier != "" {
		resource += ":" + r.qualifier
	}
	return aws.ARN{Partition: r.scope.Partition.String(), Service: "lambda", Region: r.scope.Region.String(), Account: r.scope.Account.String(), Resource: resource}
}

// traceID is the invocation's X-Ray trace header, derived from its request
// id so it draws from no identifier stream: Root=1-<epoch hex>-<24 hex>.
func traceID(request ids.LambdaRequestID, epoch int64) string {
	sum := sha256.Sum256([]byte(request))
	h := hex.EncodeToString(sum[:])
	return fmt.Sprintf("Root=1-%08x-%s;Parent=%s;Sampled=0", epoch, h[:24], h[24:40])
}

// invocationOutcome is how the journal records an outcome.
func invocationOutcome(o runtimeapi.Outcome) journal.InvocationOutcome {
	switch o := o.(type) {
	case runtimeapi.Success:
		return journal.InvocationOK{}
	case runtimeapi.FunctionError:
		return journal.FunctionError{Type: o.Type}
	case runtimeapi.Timeout:
		return journal.Timeout{}
	case runtimeapi.ExitError:
		return journal.ExitError{Code: o.Code}
	case runtimeapi.OOM:
		return journal.OOM{}
	case runtimeapi.InitError:
		return journal.FunctionError{Type: o.Type}
	case runtimeapi.ArchMismatch:
		return journal.FunctionError{Type: "Runtime.InvalidEntrypoint"}
	}
	return journal.FunctionError{Type: "Runtime.Unknown"}
}

// fnKey names a function in its scope.
type fnKey struct {
	scope aws.Scope
	name  string
}

// errService is a failure of the emulator, not of the request.
func errService(message string) *awserr.Error {
	return lambdaapi.ErrServiceException(message)
}

// errPayloadTooLarge is the front end's refusal of an oversized Invoke
// body: the unmodeled RequestEntityTooLargeException, with no Type, as for
// CreateFunction (errZippedTooLarge), worded as AWS's own Lambda test tool
// words it. The Event limit's wording is assumed to follow the same shape.
func errPayloadTooLarge(limit int) *awserr.Error {
	return lambdaapi.ErrRequestEntityTooLargeException("Request must be smaller than " + strconv.Itoa(limit) + " bytes for the InvokeFunction operation")
}

// errPayloadNotJSON is the recorded prefix with Go's parser message in
// place of Jackson's (lambda-api.md 8.1).
func errPayloadNotJSON(err error) *awserr.Error {
	return user(lambdaapi.ErrInvalidRequestContentException("Could not parse request body into json: Could not parse payload into json: " + err.Error()))
}

// errClientContext is understudy's text; AWS's is unrecorded.
func errClientContext(problem string) *awserr.Error {
	return user(lambdaapi.ErrInvalidRequestContentException("X-Amz-Client-Context " + problem + "; send the base64 encoding of a JSON object of at most " + strconv.Itoa(maxClientContext) + " bytes."))
}

// Drain has nothing to stop on the Service: execution environments,
// asynchronous queues and hot-reload overlays belong to the Execution built
// for it, which is the admin API's Drainer.
func (*Service) Drain(context.Context, kernel.ResetScope) error { return nil }

// hotReloadAdvisory runs after a committed UpdateFunctionCode of the
// function fn names: when a hot-reload overlay runs instead of the zip
// code just stored, the call records journal.AdvisoryShadowedCodeUpdate
// (ARCHITECTURE.md 14.4).
func (s *Service) hotReloadAdvisory(ctx context.Context, c *kernel.Call, fn functionRef) {
	if s.overlayDir == nil {
		return
	}
	dir, ok := s.overlayDir(fnKey{scope: fn.scope, name: fn.name})
	if !ok {
		return
	}
	sha, err := view(s, fn.scope, func(st *State) (string, *awserr.Error) {
		if f, ok := st.Functions[fn.name]; ok {
			if z, ok := f.Latest.Code.(ZipCode); ok {
				return z.Sha256, nil
			}
		}
		return "", nil
	})
	if err != nil || sha == "" {
		return
	}
	c.Record(ctx, fn.scope, journal.Advisory{
		Kind:     journal.AdvisoryShadowedCodeUpdate,
		Resource: invokedARN(functionRef{scope: fn.scope, name: fn.name}),
		Text: "UpdateFunctionCode stored new code for " + fn.name + " (CodeSha256 " + sha + "), but invocations keep running the hot-reload directory " + dir +
			" until the overlay is removed by a reset; GetFunction reports the stored code",
	})
}
