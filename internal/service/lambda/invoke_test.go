package lambda_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
)

// dockerEndpoint is the endpoint the harness says Docker is at.
const dockerEndpoint = "unix:///var/run/docker.sock"

// execHarness is a kernel with the Lambda control plane, its Execution
// over a pool of fake containers, and stub CloudWatch Logs handlers that
// keep what function logs deliver.
type execHarness struct {
	*harness
	x        *lambda.Execution
	launcher *pooltest.FakeLauncher
	runtime  *pooltest.FakeRuntime
	images   *fakeImages
	code     *codestore.Store
	output   *fakeOutput
	logs     *logSink
}

// execConfig is what the harness builds the Execution from: the options of
// its pool of fake containers, nil when Docker is down, and the
// ExecutionOptions, whose Pool the harness sets from them.
type execConfig struct {
	pool *pool.Options
	exec lambda.ExecutionOptions
}

// execOptions adjusts the harness's execConfig.
type execOptions func(*execConfig)

// noDocker builds the Execution without a pool, as when Docker is down.
func noDocker(c *execConfig) { c.pool, c.exec.Images, c.exec.Code = nil, nil, nil }

// globalCap bounds the pool's environments to n.
func globalCap(n int) execOptions { return func(c *execConfig) { c.pool.GlobalCap = n } }

func runExec(t *testing.T, handler pooltest.Handler, fn func(h *execHarness), opts ...execOptions) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		withExec(k, handler, nil, fn, opts...)
	}, kerneltest.Services(lambdaapi.Service(), logsapi.Service()))
}

// linkTo forwards the kernel's Linker to the Execution, which is built
// after the kernel, as the server has to.
type linkTo struct {
	mu sync.Mutex
	x  *lambda.Execution
}

func (l *linkTo) Link(p identity.Principal, remote netip.AddrPort) (ref.Cause, bool) {
	l.mu.Lock()
	x := l.x
	l.mu.Unlock()
	if x == nil {
		return ref.Cause{}, false
	}
	return x.Link(p, remote)
}

func (l *linkTo) set(x *lambda.Execution) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.x = x
}

// runLinkedExec is runExec on a kernel whose Linker is the Execution, as
// the server wires it, in a synctest bubble with a frozen clock.
func runLinkedExec(t *testing.T, handler pooltest.Handler, fn func(h *execHarness)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		timers := kernel.NewTimers()
		clk, err := clock.New(clock.Options{Mode: clock.Frozen{}, Timers: timers.Scheduler()})
		if err != nil {
			t.Fatal(err)
		}
		j, err := journal.New(journal.Options{Clock: clk})
		if err != nil {
			t.Fatal(err)
		}
		reg, err := registry.New(lambdaapi.Service(), logsapi.Service())
		if err != nil {
			t.Fatal(err)
		}
		src := ids.NewSeeded(1)
		idStore := identity.New(src, aws.DefaultAccount())
		link := &linkTo{}
		kern, err := kernel.New(kernel.Options{Registry: reg, Clock: clk, Timers: timers, IDs: src, Journal: j, Identity: idStore, Linker: link, Strict: true})
		if err != nil {
			t.Fatal(err)
		}
		k := &kerneltest.K{
			T: t, Kernel: kern, Clock: clk, Journal: j, Identity: idStore,
			Scope:    aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()},
			Endpoint: kernel.Endpoint{Scheme: "http", Host: "localhost:4566"},
		}
		ctx, cancel := context.WithCancel(context.Background())
		stopped := make(chan error, 1)
		go func() { stopped <- kern.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-stopped; err != nil {
				t.Errorf("kernel.Run = %v", err)
			}
			if err := kern.BindErr(); err != nil {
				t.Errorf("bind mistakes: %v", err)
			}
		}()
		withExec(k, handler, link, fn)
	})
}

// withExec builds the Lambda control plane and its Execution over a pool
// of fake containers in k, runs fn and closes them. A non-nil link is
// pointed at the Execution.
func withExec(k *kerneltest.K, handler pooltest.Handler, link *linkTo, fn func(h *execHarness), opts ...execOptions) {
	blobs, err := blob.NewMemory("", 0)
	if err != nil {
		k.Fatal(err)
	}
	code, err := codestore.Open(k.TempDir())
	if err != nil {
		k.Fatal(err)
	}
	deps := k.Deps(lambdaapi.Service())
	deps.Blob = blobs
	images := &fakeImages{}
	svc := lambda.New(deps, lambda.Options{Images: images})
	h := &execHarness{
		harness:  &harness{K: k, svc: svc},
		launcher: pooltest.NewFakeLauncher(),
		runtime:  pooltest.NewFakeRuntime(handler),
		images:   images,
		code:     code,
		output:   newFakeOutput(),
		logs:     &logSink{},
	}
	cfg := execConfig{
		pool: &pool.Options{Launcher: h.launcher, Runtime: h.runtime, Clock: k.Clock},
		exec: lambda.ExecutionOptions{Docker: dockerEndpoint, Images: h.images, Code: code, Logs: h.output},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	runCtx, stopRun := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	if cfg.pool == nil {
		ran <- nil
	} else {
		p, err := pool.New(*cfg.pool)
		if err != nil {
			k.Fatal(err)
		}
		go func() { ran <- p.Run(runCtx) }()
		cfg.exec.Pool = p
	}
	h.x, err = lambda.NewExecution(svc, deps, cfg.exec)
	if err != nil {
		k.Fatal(err)
	}
	if link != nil {
		link.set(h.x)
	}
	h.logs.stub(k.Kernel)
	defer func() {
		if err := h.x.Close(context.Background()); err != nil {
			k.Errorf("Close: %v", err)
		}
		stopRun()
		if err := <-ran; err != nil {
			k.Errorf("pool Run: %v", err)
		}
		if err := code.Close(); err != nil {
			k.Error(err)
		}
		if err := blobs.Close(); err != nil {
			k.Error(err)
		}
	}()
	fn(h)
}

// invoke runs Invoke as a client request and fails the test on an error.
func (h *execHarness) invoke(in *lambdaapi.InvokeInput) *lambdaapi.InvokeOutput {
	h.Helper()
	return call(h.harness, lambdaapi.OpInvoke, in)
}

func (h *execHarness) invokeFails(in *lambdaapi.InvokeInput) *awserr.Error {
	h.Helper()
	return fail(h.harness, lambdaapi.OpInvoke, in)
}

// invocations returns the journal's Invocation events, oldest first.
func (h *execHarness) invocations() []journal.Event {
	h.Helper()
	return h.Journal.Query(journal.Filter{Kind: journal.KindInvocation, Limit: 1000}).Events
}

// started returns the specs the fake launcher started containers with.
func (h *execHarness) started() []launcher.Spec {
	started := h.launcher.Started()
	specs := make([]launcher.Spec, len(started))
	for i := range started {
		specs[i] = started[i].Spec
	}
	return specs
}

// fakeImages makes every image present at once and counts the requests.
type fakeImages struct {
	mu   sync.Mutex
	refs []string
}

func (f *fakeImages) EnsureImage(_ context.Context, image string, arch launcher.Architecture, _ func(docker.Progress)) (launcher.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs = append(f.refs, image)
	sum := sha256.Sum256([]byte(image))
	return launcher.Image{Ref: image, Digest: "sha256:" + hex.EncodeToString(sum[:]), Architecture: arch}, nil
}

func (f *fakeImages) requested() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.refs)
}

// fakeOutput is a LogSource whose containers print what Print sends: a
// handler prints, as function code would, to every container followed.
type fakeOutput struct {
	mu    sync.Mutex
	lines map[string]chan launcher.Line
	// unclaimed is what was printed while no container was followed yet:
	// the launcher follows a container's output from its start, so a
	// follower that subscribes late still reads it.
	unclaimed []launcher.Line
}

func newFakeOutput() *fakeOutput { return &fakeOutput{lines: map[string]chan launcher.Line{}} }

func (f *fakeOutput) Follow(ctx context.Context, containerID string) (lambda.Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan launcher.Line, max(64, len(f.unclaimed)))
	for _, line := range f.unclaimed {
		ch <- line
	}
	f.unclaimed = nil
	f.lines[containerID] = ch
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.lines[containerID] == ch {
			delete(f.lines, containerID)
			close(ch)
		}
	}()
	return followed(ch), nil
}

// followed is a container's output that ends cleanly.
type followed <-chan launcher.Line

func (o followed) Lines() <-chan launcher.Line { return o }
func (followed) Err() error                    { return nil }

// End stops every followed container's output, as their containers
// stopping does.
func (f *fakeOutput) End() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, ch := range f.lines {
		delete(f.lines, id)
		close(ch)
	}
}

// Print writes text as a line of output of every followed container.
func (f *fakeOutput) Print(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := launcher.Line{Stream: docker.Stdout, Text: text}
	if len(f.lines) == 0 {
		f.unclaimed = append(f.unclaimed, line)
	}
	for _, ch := range f.lines {
		ch <- line
	}
}

// logSink stubs the CloudWatch Logs operations function logs use and keeps
// what they received, with the principal of each call. Its stubs bind into
// k; a mistake shows in k.BindErr.
type logSink struct {
	mu      sync.Mutex
	groups  []string
	streams []string
	events  []logsapi.InputLogEvent
	callers []string
}

func (s *logSink) stub(k *kernel.Kernel) {
	b := k.Binder(logsapi.Service())
	kernel.Bind(b, logsapi.OpCreateLogGroup, func(_ context.Context, c *kernel.Call, in *logsapi.CreateLogGroupInput) (*logsapi.CreateLogGroupOutput, *awserr.Error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.groups = append(s.groups, in.LogGroupName)
		s.callers = append(s.callers, identity.Render(c.Principal()))
		return &logsapi.CreateLogGroupOutput{}, nil
	}, registry.Full{})
	kernel.Bind(b, logsapi.OpCreateLogStream, func(_ context.Context, c *kernel.Call, in *logsapi.CreateLogStreamInput) (*logsapi.CreateLogStreamOutput, *awserr.Error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.streams = append(s.streams, in.LogGroupName+" "+in.LogStreamName)
		s.callers = append(s.callers, identity.Render(c.Principal()))
		return &logsapi.CreateLogStreamOutput{}, nil
	}, registry.Full{})
	kernel.Bind(b, logsapi.OpPutLogEventsInternal, func(_ context.Context, c *kernel.Call, in *logsapi.PutLogEventsInternalInput) (*logsapi.PutLogEventsInternalOutput, *awserr.Error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.events = append(s.events, in.LogEvents...)
		s.callers = append(s.callers, identity.Render(c.Principal()))
		return &logsapi.PutLogEventsInternalOutput{}, nil
	}, registry.Full{})
}

// messages returns the messages delivered so far, in order.
func (s *logSink) messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.events))
	for i, e := range s.events {
		out[i] = e.Message
	}
	return out
}

// invocationOf is an Invocation event's detail.
func invocationOf(tb testing.TB, e journal.Event) journal.Invocation {
	tb.Helper()
	inv, ok := e.Detail.(journal.Invocation)
	if !ok {
		tb.Fatalf("event %d is %T, want an Invocation", e.ID, e.Detail)
	}
	return inv
}

func isMinted(r identity.Resolution) bool {
	_, ok := r.(identity.Minted)
	return ok
}

// invokeInput is a RequestResponse Invoke of name with payload.
func invokeInput(name, payload string) *lambdaapi.InvokeInput {
	return &lambdaapi.InvokeInput{FunctionName: name, Payload: []byte(payload)}
}

// script is a fake runtime whose behavior the event's action picks: fail,
// timeout, exit, block (until release or the invocation's end), or echo.
// during, when set, runs inside every invocation, as function code would.
type script struct {
	mu      sync.Mutex
	calls   []runtimeapi.Invocation
	release chan struct{}
	during  func(inv runtimeapi.Invocation)
}

func newScript() *script { return &script{} }

// released is the channel blocked invocations wait on. It is made on first
// use, inside the test's bubble, so waiting on it is durably blocking.
func (s *script) released() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil {
		s.release = make(chan struct{})
	}
	return s.release
}

// unblock releases every blocked invocation, now and later.
func (s *script) unblock() { close(s.released()) }

func (s *script) handle(ctx context.Context, inv runtimeapi.Invocation) (runtimeapi.Outcome, error) {
	s.mu.Lock()
	s.calls = append(s.calls, inv)
	during := s.during
	s.mu.Unlock()
	if during != nil {
		during(inv)
	}
	var event struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(inv.Payload, &event); err != nil {
		return nil, err
	}
	switch event.Action {
	case "fail":
		return runtimeapi.FunctionError{Type: "Runtime.Unknown", Payload: []byte(`{"errorMessage":"boom","errorType":"Error"}`)}, nil
	case "timeout":
		return runtimeapi.Timeout{After: inv.Timeout}, nil
	case "exit":
		return runtimeapi.ExitError{Code: 1}, nil
	case "block":
		select {
		case <-s.released():
		case <-ctx.Done():
		}
	}
	return runtimeapi.Success{Payload: inv.Payload}, nil
}

// setDuring makes fn run inside every later invocation.
func (s *script) setDuring(fn func(inv runtimeapi.Invocation)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.during = fn
}

// invoked returns the invocations the runtime received, in order.
func (s *script) invoked() []runtimeapi.Invocation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func TestInvokeRequestResponseRunsTheFunctionAndReturnsItsPayload(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.createFunction("fn")
		out := h.invoke(invokeInput("fn", `{"n":1}`))
		if out.StatusCode != http.StatusOK || string(out.Payload) != `{"n":1}` || out.FunctionError != nil {
			t.Fatalf("Invoke = %d %s (function error %v), want 200 echoing the payload", out.StatusCode, out.Payload, out.FunctionError)
		}
		if out.ExecutedVersion == nil || *out.ExecutedVersion != "$LATEST" {
			t.Errorf("ExecutedVersion = %v, want $LATEST", out.ExecutedVersion)
		}
		specs := h.started()
		if len(specs) != 1 {
			t.Fatalf("containers started = %d, want 1", len(specs))
		}
		spec := specs[0]
		img, _ := launcher.ImageForRuntime("provided.al2023")
		code, ok := spec.Code.(launcher.ZipCode)
		if spec.Image != img.Ref() || !ok || code.Handler != "bootstrap" || spec.MemoryMB != 128 || spec.LogGroup != "/aws/lambda/fn" {
			t.Errorf("spec = %+v, want the pinned provided.al2023 image, the bootstrap handler, 128 MB and /aws/lambda/fn", spec)
		}
		if p, res := h.Identity.Resolve(string(spec.Credentials.AccessKeyID)); identity.Render(p) != "arn:aws:sts::000000000000:assumed-role/lambda-role/fn" {
			t.Errorf("the environment's credentials resolve to %s (%T), want a session of the execution role", identity.Render(p), res)
		}
	})
}

func TestInvokeReusesAWarmEnvironment(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.createFunction("fn")
		h.invoke(invokeInput("fn", `{}`))
		h.invoke(invokeInput("fn", `{}`))
		if n := len(h.started()); n != 1 {
			t.Fatalf("containers started = %d, want 1 warm environment for both invocations", n)
		}
		events := h.invocations()
		if len(events) != 2 {
			t.Fatalf("journal Invocation events = %d, want 2", len(events))
		}
		first, firstOK := events[0].Detail.(journal.Invocation)
		second, secondOK := events[1].Detail.(journal.Invocation)
		if !firstOK || !secondOK || !first.Cold || second.Cold || first.Env != second.Env || first.Env == "" {
			t.Errorf("invocations = %+v, %+v; want a cold then a warm one in the same environment", first, second)
		}
		if first.Function.String() != "arn:aws:lambda:us-east-1:000000000000:function:fn" || first.Version != "$LATEST" || first.Outcome != (journal.InvocationOK{}) {
			t.Errorf("invocation = %+v, want fn $LATEST OK", first)
		}
	})
}

func TestFunctionErrorIsA200WithXAmzFunctionErrorUnhandled(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		out := h.invoke(invokeInput("fn", `{"action":"fail"}`))
		if out.StatusCode != http.StatusOK || out.FunctionError == nil || *out.FunctionError != "Unhandled" || string(out.Payload) != `{"errorMessage":"boom","errorType":"Error"}` {
			t.Fatalf("Invoke = %d %v %s, want 200 with FunctionError Unhandled and the error payload", out.StatusCode, out.FunctionError, out.Payload)
		}
		h.invoke(invokeInput("fn", `{}`))
		if n := len(h.started()); n != 1 {
			t.Errorf("containers started = %d; a function error keeps the environment warm", n)
		}
	})
}

func TestTimeoutAnswersSandboxTimedoutAndTheNextInvokeIsCold(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		out := h.invoke(invokeInput("fn", `{"action":"timeout"}`))
		calls := s.invoked()
		want := `{"errorType":"Sandbox.Timedout","errorMessage":"RequestId: ` + string(calls[0].RequestID) + ` Error: Task timed out after 3.00 seconds"}`
		if out.FunctionError == nil || string(out.Payload) != want {
			t.Fatalf("Invoke = %v %s, want Unhandled %s", out.FunctionError, out.Payload, want)
		}
		if calls[0].Timeout != 3*time.Second {
			t.Errorf("the runtime got timeout %s, want the function's 3 s", calls[0].Timeout)
		}
		h.invoke(invokeInput("fn", `{}`))
		if n, live := len(h.started()), len(h.launcher.Live()); n != 2 || live != 1 {
			t.Errorf("started %d containers with %d live, want the timed-out one removed and a cold start", n, live)
		}
	})
}

func TestDryRunAnswers204WithoutRunningTheFunction(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.createFunction("fn")
		out := h.invoke(&lambdaapi.InvokeInput{FunctionName: "fn", InvocationType: new(lambdaapi.InvocationTypeDryRun)})
		if out.StatusCode != http.StatusNoContent || out.Payload != nil || out.ExecutedVersion != nil {
			t.Errorf("DryRun = %d %q %v, want 204 with no body and no executed version", out.StatusCode, out.Payload, out.ExecutedVersion)
		}
		if n := len(h.started()); n != 0 {
			t.Errorf("DryRun started %d containers", n)
		}
		err := h.invokeFails(&lambdaapi.InvokeInput{FunctionName: "nope", InvocationType: new(lambdaapi.InvocationTypeDryRun)})
		errorIs(t, err, "ResourceNotFoundException", "Function not found: arn:aws:lambda:us-east-1:000000000000:function:nope")
	})
}

func TestInvokeRefusesBadRequestsBeforeRunning(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.createFunction("fn")
		// 6,291,457 bytes: one over the request limit, under the response's.
		big := `"` + strings.Repeat("x", 6_291_455) + `"`
		async := `"` + strings.Repeat("x", 1_048_576) + `"`
		for _, tc := range []struct {
			name    string
			in      *lambdaapi.InvokeInput
			shape   string
			message string
		}{
			{"missing function", invokeInput("nope", `{}`), "ResourceNotFoundException", "Function not found: arn:aws:lambda:us-east-1:000000000000:function:nope"},
			{"missing version", &lambdaapi.InvokeInput{FunctionName: "fn", Qualifier: new("7")}, "ResourceNotFoundException", "Function not found: arn:aws:lambda:us-east-1:000000000000:function:fn:7"},
			{"sync payload over 6 MB", invokeInput("fn", big), "RequestEntityTooLargeException", "Request must be smaller than 6291456 bytes for the InvokeFunction operation"},
			{"async payload over 1 MB", &lambdaapi.InvokeInput{FunctionName: "fn", InvocationType: new(lambdaapi.InvocationTypeEvent), Payload: []byte(async)}, "RequestEntityTooLargeException", "Request must be smaller than 1048576 bytes for the InvokeFunction operation"},
			{"payload not JSON", invokeInput("fn", `{nope`), "InvalidRequestContentException", "Could not parse request body into json: Could not parse payload into json: invalid character 'n' looking for beginning of object key string"},
			{"client context not base64", &lambdaapi.InvokeInput{FunctionName: "fn", ClientContext: new("%%%")}, "InvalidRequestContentException", "X-Amz-Client-Context is not valid base64; send the base64 encoding of a JSON object of at most 3583 bytes."},
		} {
			if err := h.invokeFails(tc.in); err.ID.Shape.Name() != tc.shape || err.Message != tc.message {
				t.Errorf("%s: %s %q, want %s %q", tc.name, err.ID.Shape.Name(), err.Message, tc.shape, tc.message)
			}
		}
		if n := len(h.started()); n != 0 {
			t.Errorf("refused invocations started %d containers", n)
		}
	})
}

func TestEmptyPayloadReachesTheFunctionAsAnEmptyObject(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.createFunction("fn")
		if out := h.invoke(&lambdaapi.InvokeInput{FunctionName: "fn"}); string(out.Payload) != "{}" {
			t.Errorf("payload = %q, want {}", out.Payload)
		}
	})
}

func TestReservedConcurrencyThrottlesBeforeAnyContainerWork(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		call(h.harness, lambdaapi.OpPutFunctionConcurrency, &lambdaapi.PutFunctionConcurrencyInput{FunctionName: "fn", ReservedConcurrentExecutions: 0})
		err := h.invokeFails(invokeInput("fn", `{}`))
		errorIs(t, err, "TooManyRequestsException", "Rate Exceeded.")
		if !slices.Contains(err.Fields, awserr.Field{Name: "Reason", Value: "ReservedFunctionConcurrentInvocationLimitExceeded"}) {
			t.Errorf("fields = %v, want Reason ReservedFunctionConcurrentInvocationLimitExceeded", err.Fields)
		}
		if n := len(h.started()); n != 0 {
			t.Errorf("a throttled invocation started %d containers", n)
		}
		if events := h.invocations(); len(events) != 1 || invocationOf(t, events[0]).Outcome != (journal.Throttled{}) {
			t.Errorf("journal = %+v, want one Throttled invocation", events)
		}

		// With one reserved, a second invocation while the first runs is
		// throttled; once the first ends, the next one runs.
		call(h.harness, lambdaapi.OpPutFunctionConcurrency, &lambdaapi.PutFunctionConcurrencyInput{FunctionName: "fn", ReservedConcurrentExecutions: 1})
		done := h.invokeInBackground(invokeInput("fn", `{"action":"block"}`))
		synctest.Wait()
		errorIs(t, h.invokeFails(invokeInput("fn", `{}`)), "TooManyRequestsException", "Rate Exceeded.")
		s.unblock()
		h.wait(done)
		h.invoke(invokeInput("fn", `{}`))
	})
}

// The account's unreserved concurrency, what the reservations leave of its
// 1,000, throttles before any container work with
// ConcurrentInvocationLimitExceeded; so does the pool's global cap.
func TestAccountConcurrencyThrottlesWithConcurrentInvocationLimitExceeded(t *testing.T) {
	accountLimit := func(t *testing.T, err *awserr.Error) {
		t.Helper()
		errorIs(t, err, "TooManyRequestsException", "Rate Exceeded.")
		if !slices.Contains(err.Fields, awserr.Field{Name: "Reason", Value: "ConcurrentInvocationLimitExceeded"}) {
			t.Errorf("fields = %v, want Reason ConcurrentInvocationLimitExceeded", err.Fields)
		}
	}
	t.Run("unreserved concurrency", func(t *testing.T) {
		s := newScript()
		runExec(t, s.handle, func(h *execHarness) {
			h.createFunction("reserved")
			h.createFunction("fn")
			call(h.harness, lambdaapi.OpPutFunctionConcurrency, &lambdaapi.PutFunctionConcurrencyInput{FunctionName: "reserved", ReservedConcurrentExecutions: 900})
			running := make([]<-chan *awserr.Error, 100)
			for i := range running {
				running[i] = h.invokeInBackground(invokeInput("fn", `{"action":"block"}`))
			}
			synctest.Wait()
			before := len(h.started())
			accountLimit(t, h.invokeFails(invokeInput("fn", `{}`)))
			if n := len(h.started()) - before; n != 0 {
				t.Errorf("a throttled invocation started %d containers", n)
			}
			s.unblock()
			for _, done := range running {
				h.wait(done)
			}
			h.invoke(invokeInput("fn", `{}`))
		}, globalCap(200))
	})
	t.Run("pool global cap", func(t *testing.T) {
		s := newScript()
		runExec(t, s.handle, func(h *execHarness) {
			h.createFunction("fn")
			h.createFunction("other")
			done := h.invokeInBackground(invokeInput("fn", `{"action":"block"}`))
			synctest.Wait()
			accountLimit(t, h.invokeFails(invokeInput("other", `{}`)))
			if n := len(h.started()); n != 1 {
				t.Errorf("containers started = %d, want only the running one", n)
			}
			s.unblock()
			h.wait(done)
			throttled := 0
			for _, e := range h.invocations() {
				if invocationOf(t, e).Outcome == (journal.Throttled{}) {
					throttled++
				}
			}
			if throttled != 1 {
				t.Errorf("journal holds %d Throttled invocations, want the pool cap's one", throttled)
			}
		}, globalCap(1))
	})
}

// A function deleted and created again under its name starts cold with the
// new function's role, though its generations start over and its code is
// the same: nothing of the old function's environment is reused.
func TestARecreatedFunctionStartsColdWithItsNewRole(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.createFunction("fn")
		h.invoke(invokeInput("fn", `{}`))
		call(h.harness, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "fn"})
		in := zipFunction(t, "fn")
		in.Role = "arn:aws:iam::000000000000:role/other-role"
		call(h.harness, lambdaapi.OpCreateFunction, in)
		h.invoke(invokeInput("fn", `{}`))
		specs := h.started()
		if len(specs) != 2 {
			t.Fatalf("containers started = %d, want a second one for the recreated function", len(specs))
		}
		if inv := h.invocations(); len(inv) != 2 || !invocationOf(t, inv[1]).Cold {
			t.Errorf("invocations = %+v, want the recreated function's first one cold", inv)
		}
		if p, res := h.Identity.Resolve(string(specs[1].Credentials.AccessKeyID)); identity.Render(p) != "arn:aws:sts::000000000000:assumed-role/other-role/fn" {
			t.Errorf("the recreated function's credentials resolve to %s (%T), want a session of other-role", identity.Render(p), res)
		}
	})
}

// A synchronous invocation runs to its end after its caller goes away, as
// on AWS: the client's cancellation does not reach the function.
func TestASyncInvocationOutlivesItsCaller(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		ctx, cancel := context.WithCancel(h.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = h.Invoke(ctx, lambdaapi.OpInvoke, invokeInput("fn", `{"action":"block"}`))
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if inv := h.invocations(); len(inv) != 0 {
			t.Errorf("invocations ended when the caller went away: %+v", inv)
		}
		s.unblock()
		<-done
		if inv := h.invocations(); len(inv) != 1 || invocationOf(t, inv[0]).Outcome != (journal.InvocationOK{}) {
			t.Errorf("invocations = %+v, want one that ran to its end", inv)
		}
	})
}

// Drain returns only once the invocations of the scope running when it was
// called have ended, so none writes into the state a reset installs.
func TestDrainWaitsForRunningInvocations(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		running := h.invokeInBackground(invokeInput("fn", `{"action":"block"}`))
		synctest.Wait()
		drained := make(chan error, 1)
		go func() { drained <- h.x.Drain(h.Context(), kernel.ResetScope{}) }()
		synctest.Wait()
		select {
		case err := <-drained:
			t.Fatalf("Drain returned (%v) while an invocation of its scope ran", err)
		default:
		}
		s.unblock()
		h.wait(running)
		if err := <-drained; err != nil {
			t.Fatal(err)
		}
	})
}

// invokeInBackground runs Invoke as a client request on another goroutine;
// wait reports its error on the test's.
func (h *execHarness) invokeInBackground(in *lambdaapi.InvokeInput) <-chan *awserr.Error {
	done := make(chan *awserr.Error, 1)
	go func() {
		_, err := h.Invoke(h.Context(), lambdaapi.OpInvoke, in)
		done <- err
	}()
	return done
}

func (h *execHarness) wait(done <-chan *awserr.Error) {
	h.Helper()
	if err := <-done; err != nil {
		h.Fatalf("background Invoke: %s %s", err.ID.Shape.Name(), err.Message)
	}
}

func TestWithoutDockerInvokeAnswersServiceExceptionNamingDocker(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.createFunction("fn")
		err := h.invokeFails(invokeInput("fn", `{}`))
		if err.ID.Shape.Name() != "ServiceException" || err.ID.Status != 500 ||
			!strings.Contains(err.Message, dockerEndpoint) || !strings.Contains(err.Message, "understudy doctor") {
			t.Fatalf("Invoke without Docker = %s %d %q, want ServiceException naming %s and understudy doctor", err.ID.Shape.Name(), err.ID.Status, err.Message, dockerEndpoint)
		}
		if out := h.invoke(&lambdaapi.InvokeInput{FunctionName: "fn", InvocationType: new(lambdaapi.InvocationTypeDryRun)}); out.StatusCode != http.StatusNoContent {
			t.Errorf("DryRun without Docker = %d, want 204", out.StatusCode)
		}
		call(h.harness, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn"})
	}, noDocker)
}

// A call made with an environment's minted credentials while it runs an
// invocation is that invocation's child in the journal: the kernel's
// Linker maps the key to the Invoke call, which is also the Invocation
// event's parent. A call between invocations roots its own trace.
func TestCallsWithTheEnvironmentsKeyLinkToTheInvocation(t *testing.T) {
	s := newScript()
	runLinkedExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		// inFunction calls GetFunction as function code would: with the
		// key of the environment the invocation runs in.
		inFunction := func() *awserr.Error {
			p, _ := h.Identity.Resolve(string(h.started()[0].Credentials.AccessKeyID))
			c := h.Kernel.NewCall(kernel.ExternalCall{
				RequestID: h.Kernel.IDs().RequestID(),
				Operation: lambdaapi.OpGetFunction.Descriptor().ID,
				Scope:     h.Scope,
				Principal: p,
				Origin:    ref.External{Protocol: ref.RESTJSON1, Listener: ref.ListenerMain},
				Admitted:  h.Clock.Now(),
				Endpoint:  h.Endpoint,
			})
			_, err := kerneltest.Dispatch[lambdaapi.GetFunctionInput, lambdaapi.GetFunctionOutput](h.Context(), h.K, c, &lambdaapi.GetFunctionInput{FunctionName: "fn"})
			return err
		}
		during := make(chan *awserr.Error, 1)
		s.setDuring(func(runtimeapi.Invocation) { during <- inFunction() })
		h.invoke(invokeInput("fn", `{}`))
		s.setDuring(nil)
		for _, err := range []*awserr.Error{<-during, inFunction()} {
			if err != nil {
				t.Fatalf("GetFunction with the environment's key: %s %s", err.ID.Shape.Name(), err.Message)
			}
		}
		invoke := h.Journal.Query(journal.Filter{Kind: journal.KindAPICall, Operation: lambdaapi.OpInvoke.Descriptor().ID}).Events
		gets := h.Journal.Query(journal.Filter{Kind: journal.KindAPICall, Operation: lambdaapi.OpGetFunction.Descriptor().ID}).Events
		if len(invoke) != 1 || len(gets) != 2 {
			t.Fatalf("journal holds %d Invoke and %d GetFunction calls, want 1 and 2", len(invoke), len(gets))
		}
		if got := gets[0].Cause; got.Parent != invoke[0].ID || got.Trace != invoke[0].Cause.Trace {
			t.Errorf("the call during the invocation has cause %+v, want the Invoke call's event %d in its trace %d", got, invoke[0].ID, invoke[0].Cause.Trace)
		}
		if inv := h.invocations(); len(inv) != 1 || inv[0].Cause.Parent != invoke[0].ID {
			t.Errorf("Invocation event = %+v, want it caused by the Invoke call", inv)
		}
		if got := gets[1].Cause; got.Parent != 0 {
			t.Errorf("the call between invocations has cause %+v, want none: it roots its own trace", got)
		}
		if _, ok := h.x.Link(identity.Root{Account: h.Scope.Account}, netip.AddrPort{}); ok {
			t.Error("Link linked a principal that is no environment's")
		}
	})
}

func TestDrainRemovesTheEnvironmentsOfTheResetScope(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.createFunction("fn")
		h.invoke(invokeInput("fn", `{}`))
		key := string(h.started()[0].Credentials.AccessKeyID)
		if err := h.x.Drain(h.Context(), kernel.ResetScope{}); err != nil {
			t.Fatal(err)
		}
		if live := h.launcher.Live(); len(live) != 0 {
			t.Errorf("containers live after Drain = %v, want none", live)
		}
		if _, res := h.Identity.Resolve(key); isMinted(res) {
			t.Errorf("the drained environment's key resolves as %T, want it revoked", res)
		}
		h.invoke(invokeInput("fn", `{}`))
		if n := len(h.started()); n != 2 {
			t.Errorf("containers started = %d, want a cold start after the drain", n)
		}
	})
}

func TestPullImagesPullsThePinnedRuntimeImages(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		img, _ := launcher.ImageForRuntime("python3.13")
		got, err := h.x.PullImages(h.Context(), []string{"python3.13"})
		if err != nil || !slices.Equal(got, []string{img.Ref()}) || !slices.Equal(h.images.requested(), got) {
			t.Fatalf("PullImages = %v, %v (requested %v), want %s", got, err, h.images.requested(), img.Ref())
		}
		if _, err := h.x.PullImages(h.Context(), []string{"cobol1"}); err == nil || !strings.Contains(err.Error(), "python3.13") {
			t.Errorf("PullImages of an unpinned runtime = %v, want an error listing the pinned ones", err)
		}
		all, err := h.x.PullImages(h.Context(), nil)
		if err != nil || len(all) != len(launcher.RuntimeImages()) {
			t.Errorf("PullImages() = %v, %v, want every pinned runtime", all, err)
		}
	})
}

// A reset after a code update keeps warm reuse: the execution generation
// keeps rising, so the pool, which remembers the highest it saw, does not
// drain the new environments as an older generation's.
func TestWarmReuseSurvivesADrainAfterACodeUpdate(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.createFunction("fn")
		h.invoke(invokeInput("fn", `{}`))
		call(h.harness, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{FunctionName: "fn", ZipFile: zipOf(t, "#!/bin/sh\necho v2\n")})
		h.invoke(invokeInput("fn", `{}`))
		if err := h.x.Drain(h.Context(), kernel.ResetScope{}); err != nil {
			t.Fatal(err)
		}
		before := len(h.started())
		for range 3 {
			h.invoke(invokeInput("fn", `{}`))
		}
		if n := len(h.started()) - before; n != 1 {
			t.Errorf("three invocations after the drain started %d containers, want 1 kept warm", n)
		}
		if inv := h.invocations(); invocationOf(t, inv[len(inv)-1]).Cold {
			t.Error("the last invocation after the drain was cold")
		}
	})
}

// An image function runs the image its deployment recorded, pinned by
// digest, and a warm invocation asks Docker for nothing.
func TestImageFunctionsRunTheDeployedDigest(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		call(h.harness, lambdaapi.OpCreateFunction, imageFunction("img", "local/fn:latest"))
		h.settle()
		h.invoke(invokeInput("img", `{}`))
		h.invoke(invokeInput("img", `{}`))
		sum := sha256.Sum256([]byte("local/fn:latest"))
		pinned := "local/fn@sha256:" + hex.EncodeToString(sum[:])
		specs := h.started()
		if len(specs) != 1 || specs[0].Image != pinned {
			t.Fatalf("started %+v, want one environment of %s", specs, pinned)
		}
		if n := countOf(h.images.requested(), pinned); n != 1 {
			t.Errorf("%s was asked of Docker %d times over two invocations, want once", pinned, n)
		}
	})
}

func countOf(refs []string, want string) int {
	n := 0
	for _, r := range refs {
		if r == want {
			n++
		}
	}
	return n
}

// LoggingConfig reaches the runtime interface client as environment
// variables, under the function's own.
func TestLoggingConfigReachesTheFunctionsEnvironment(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		in := zipFunction(t, "fn")
		in.LoggingConfig = &lambdaapi.LoggingConfig{LogFormat: new(lambdaapi.LogFormatJson), ApplicationLogLevel: new(lambdaapi.ApplicationLogLevelDebug)}
		in.Environment = &lambdaapi.Environment{Variables: map[string]string{"STAGE": "test"}}
		call(h.harness, lambdaapi.OpCreateFunction, in)
		h.invoke(invokeInput("fn", `{}`))
		env := h.started()[0].Environment
		if env["AWS_LAMBDA_LOG_FORMAT"] != "JSON" || env["AWS_LAMBDA_LOG_LEVEL"] != "DEBUG" || env["STAGE"] != "test" {
			t.Errorf("environment = %v, want AWS_LAMBDA_LOG_FORMAT JSON, AWS_LAMBDA_LOG_LEVEL DEBUG and STAGE", env)
		}
	})
}

// Code updates give the packages no environment runs any more back to the
// code store: two updates leave one extracted tree.
func TestCodeUpdatesReleaseTheExtractedTreesNoLongerRun(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		shas := make([]string, 0, 3)
		shas = append(shas, *h.createFunction("fn").CodeSha256)
		h.invoke(invokeInput("fn", `{}`))
		for _, body := range []string{"v2", "v3"} {
			out := call(h.harness, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{FunctionName: "fn", ZipFile: zipOf(t, "#!/bin/sh\necho "+body+"\n")})
			shas = append(shas, *out.CodeSha256)
			h.invoke(invokeInput("fn", `{}`))
		}
		synctest.Wait()
		for i, sha := range shas {
			raw, err := base64.StdEncoding.DecodeString(sha)
			if err != nil {
				t.Fatal(err)
			}
			_, err = h.code.Dir(codestore.Digest(raw))
			if extracted, want := err == nil, i == len(shas)-1; extracted != want {
				t.Errorf("package %d (%s) extracted = %v, want %v", i, sha, extracted, want)
			}
		}
	})
}
