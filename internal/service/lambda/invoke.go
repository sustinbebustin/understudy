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
	"slices"
	"strconv"
	"sync"
	"time"

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
//     appended once the invocation ends. Init-phase calls run inside the
//     first invocation and link the same way. A call between invocations
//     roots its own trace.
//   - Init is the wall time of the container start of a cold invocation;
//     the runtime's own init until its first /next is part of Duration,
//     as in Lambda's suppressed init.
//   - REPORT has no Max Memory Used: understudy does not measure it. A
//     runtime failure adds the new-style Status and Error Type.
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
//     invocations of its scope already running, not for later ones.
//   - A hot-reload directory serves every version of its function; the
//     admin API's overlays end with a reset of their scope, configured
//     ones never. UpdateFunctionCode under an overlay records the
//     shadowed-update Advisory on its own call (Service.overlayDir).

// ExecutionOptions configure the data plane. The zero value has no Docker
// daemon.
type ExecutionOptions struct {
	// Pool runs execution environments. nil means no Docker daemon is
	// reachable: the control plane keeps working and Invoke answers
	// ServiceException naming Docker. The caller owns the pool; Close
	// drains it for shutdown.
	Pool *pool.Pool
	// Docker names the Docker endpoint the pool uses, for the errors that
	// say it cannot be reached; empty when none is configured.
	Docker string
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
	lambdaapi.NewBinder(d.Binder).Invoke(x.invoke, partial(invokeNote))
	return x, nil
}

// Close stops every execution environment, log stream and watcher, and
// gives back the extracted code and the minted credentials, at shutdown.
func (x *Execution) Close(ctx context.Context) error {
	var errs []error
	if x.pool != nil {
		errs = append(errs, x.pool.Drain(ctx, pool.All{}, pool.Shutdown))
	}
	x.stop()
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
// ends the hot-reload overlays bound through the admin API there. Idle
// environments stop at once; Drain waits, while ctx lasts, for the
// invocations of scope running when it was called to finish, whose
// environments are removed on release, so none writes into the new state.
func (x *Execution) Drain(ctx context.Context, scope kernel.ResetScope) error {
	var errs []error
	for _, fn := range x.envs.drop(scope) {
		if x.pool != nil {
			errs = append(errs, x.pool.Drain(ctx, pool.Function{ARN: fn}, pool.Reset))
		}
	}
	errs = append(errs, x.running.wait(ctx, scope))
	release := x.k.Gate().Exclusive()
	x.queues.Reset(func(s aws.Scope) bool { return !scope.Covers(s) })
	release()
	x.overlays.drop(scope)
	return errors.Join(errs...)
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
	// revision is the version's RevisionId. Revision ids never repeat in a
	// scope until it is reset, so a function deleted and created again
	// under its name, whose generations start over, still differs here.
	revision string
	// reserved is the function's reserved concurrency, nil when none;
	// unreserved the account's concurrency left after every reservation.
	reserved   *int32
	unreserved int32
	async      asyncConfig
}

// resolve reads the version ref names; a version that is not Active
// cannot be invoked.
func (x *Execution) resolve(r functionRef) (target, *awserr.Error) {
	return view(x.svc, r.scope, func(st *State) (target, *awserr.Error) {
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
		role, perr := aws.ParseARN(v.Config.Role)
		if perr != nil {
			return target{}, errService(fmt.Sprintf("Lambda cannot run %s: its stored execution role %q is not an ARN (%v). Set a valid role with UpdateFunctionConfiguration.", r.qualifiedARN(), v.Config.Role, perr))
		}
		v.Config.Environment = maps.Clone(v.Config.Environment)
		t := target{ref: r, version: v, invoked: invokedARN(r), role: role, revision: v.RevisionID, unreserved: st.unreserved(""), async: asyncConfigOf(f, r.qualifier)}
		if v.Number == latest {
			t.generation = f.Generation
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
		var stop context.CancelFunc
		ctx, stop = x.outliveCaller(ctx)
		defer stop()
	}
	res, err := x.runSync(ctx, c, t, syncRun{
		payload:       payload,
		clientContext: clientContext,
		cause:         ref.Cause{Parent: c.Event(), Trace: c.Cause().Trace},
	})
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

// syncRun is a RequestResponse invocation run on behalf of a call.
type syncRun struct {
	payload       []byte
	clientContext string
	// cause is the Invocation event's parent and the cause of the calls
	// the function makes: the Invoke call's event, or for an event source
	// mapping its batch's earliest message's enqueue event. links are the
	// related events, the batch's other messages.
	cause ref.Cause
	links []ref.EventID
}

// outliveCaller is ctx without its cancellation, ending instead when the
// Execution closes. A synchronous invocation a client asked for runs to
// its end or its timeout after the client goes away, as on AWS; the
// function's Timeout bounds it. An internal caller's context, such as
// tracked work the kernel ends at shutdown, is kept as it is.
func (x *Execution) outliveCaller(ctx context.Context) (context.Context, context.CancelFunc) {
	run, cancel := context.WithCancel(context.WithoutCancel(ctx))
	unwatch := context.AfterFunc(x.ctx, cancel)
	return run, func() {
		unwatch()
		cancel()
	}
}

// runSync runs a RequestResponse invocation of t under c's request id: the
// Invoke operation's, and the in-package entry of event source mappings
// and function URLs, which resolve t with resolve first.
func (x *Execution) runSync(ctx context.Context, c *kernel.Call, t target, in syncRun) (ran, *awserr.Error) {
	return x.execute(ctx, &run{
		target:        t,
		request:       ids.LambdaRequestID(c.ID()),
		payload:       in.payload,
		clientContext: in.clientContext,
		parent:        c,
		cause:         in.cause,
		links:         in.links,
	})
}

// checkPayload enforces the payload limit of the invocation mode and
// Lambda's JSON requirement. An empty payload is the empty object, as
// SDKs send no body for a nil payload.
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
	var v any
	if err := json.Unmarshal(p, &v); err != nil {
		return nil, errPayloadNotJSON(err)
	}
	return p, nil
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
	// cause is the parent of the attempt's Invocation event and of every
	// call the function makes while it runs: the Invoke call's event,
	// carried in the queue for an asynchronous invocation. links are the
	// Invocation event's related events.
	cause ref.Cause
	links []ref.EventID
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
// failure of the emulator (500); a function error is an outcome.
func (x *Execution) execute(ctx context.Context, r *run) (ran, *awserr.Error) {
	t := &r.target
	fn := fnVersion{scope: t.ref.scope, name: t.ref.name, version: t.version.Number}
	release, err := x.running.acquire(t)
	if err != nil {
		x.record(ctx, r, journal.Invocation{Function: fn.arn(), Version: fn.version, Outcome: journal.Throttled{}})
		return ran{}, err
	}
	defer release()
	if x.pool == nil {
		return ran{}, x.errNoDocker()
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
	w := leaseFor{code: t.generation, revision: t.revision, role: t.role, cause: r.cause}
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
	stream.begin(l.cold, initStartLine(t), startLine(r.request, t.version.Number), r.parent)
	elapsed := x.k.Clock().Stopwatch()
	outcome, invErr := l.env.Invoke(ctx, runtimeapi.Invocation{
		RequestID:     r.request,
		InvocationID:  string(x.k.IDs().LambdaRequestID()),
		FunctionARN:   t.invoked,
		TraceID:       traceID(r.request, x.k.Clock().Now().Wire().Unix()),
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
		tail, deliver = stream.end(endLine(r.request), reportLine(r.request, duration, t.version.Config.MemorySize, l.cold, l.init, outcome))
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
		return errService("Lambda is shutting down and starts no execution environments; retry once understudy is running again.")
	}
	x.forgetImage(image)
	return errService(fmt.Sprintf("Lambda could not start an execution environment for %s: %v. Check that Docker is running%s, or run `understudy doctor` to diagnose.", fn.arn(), err, x.dockerAt()))
}

// errNoDocker answers an invocation when no Docker daemon is reachable.
func (x *Execution) errNoDocker() *awserr.Error {
	return errService("Lambda cannot run functions because no Docker daemon is reachable" + x.dockerAt() + ". Start Docker, or run `understudy doctor` to diagnose; functions, versions and every other Lambda resource keep working.")
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

// concurrency counts running invocations: per function with a
// reservation, and per account and region for the rest, which share what
// the reservations leave (lambda-api.md 7.4). It also keeps each running
// invocation's scope, so a drain can wait for those of the scope it
// replaces.
type concurrency struct {
	mu         sync.Mutex
	reserved   map[fnKey]int32
	unreserved map[aws.Scope]int32
	// running holds the scope of every running invocation by a serial;
	// ended closes, and is replaced, whenever one ends.
	running map[uint64]aws.Scope
	serial  uint64
	ended   chan struct{}
}

func newConcurrency() *concurrency {
	return &concurrency{reserved: map[fnKey]int32{}, unreserved: map[aws.Scope]int32{}, running: map[uint64]aws.Scope{}, ended: make(chan struct{})}
}

// Throttle reasons (lambda-api.md 7.4).
const (
	reasonReservedLimit = "ReservedFunctionConcurrentInvocationLimitExceeded"
	reasonAccountLimit  = "ConcurrentInvocationLimitExceeded"
)

// acquire counts one more invocation of t, or answers 429 when t's
// concurrency is exhausted; release uncounts it.
func (c *concurrency) acquire(t *target) (release func(), err *awserr.Error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	scope := t.ref.scope
	var uncount func()
	if t.reserved != nil {
		k := fnKey{scope: scope, name: t.ref.name}
		if c.reserved[k] >= *t.reserved {
			return nil, errThrottled(reasonReservedLimit)
		}
		c.reserved[k]++
		uncount = func() { c.reserved[k]-- }
	} else {
		if c.unreserved[scope] >= t.unreserved {
			return nil, errThrottled(reasonAccountLimit)
		}
		c.unreserved[scope]++
		uncount = func() { c.unreserved[scope]-- }
	}
	c.serial++
	id := c.serial
	c.running[id] = scope
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		uncount()
		delete(c.running, id)
		close(c.ended)
		c.ended = make(chan struct{})
	}, nil
}

// wait returns once every invocation of the scopes rs covers that runs
// now has ended; invocations started meanwhile are not waited for, so a
// steady stream of them cannot hold it. It fails when ctx ends first.
func (c *concurrency) wait(ctx context.Context, rs kernel.ResetScope) error {
	c.mu.Lock()
	var pending []uint64
	for id, s := range c.running {
		if rs.Covers(s) {
			pending = append(pending, id)
		}
	}
	c.mu.Unlock()
	for {
		c.mu.Lock()
		pending = slices.DeleteFunc(pending, func(id uint64) bool { _, ok := c.running[id]; return !ok })
		ended := c.ended
		c.mu.Unlock()
		if len(pending) == 0 {
			return nil
		}
		select {
		case <-ended:
		case <-ctx.Done():
			return fmt.Errorf("%d Lambda invocations of the reset scope were still running when the drain was given up (%w); their environments are stopped, and the invocations may still write to the new state when they end", len(pending), ctx.Err())
		}
	}
}

// errThrottled is the recorded 429 (error-matrix.md T10).
func errThrottled(reason string) *awserr.Error {
	e := lambdaapi.ErrTooManyRequestsException("Rate Exceeded.")
	e.Fields = append(e.Fields, awserr.Field{Name: "Reason", Value: reason})
	return user(e)
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

// Drain on the Service has nothing to stop: execution environments,
// asynchronous queues and hot-reload overlays belong to the Execution built
// for it, which is the Drainer to hook once the server builds one.
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
