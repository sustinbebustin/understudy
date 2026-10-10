package pool_test

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
)

// fakeWall is a wall clock the test moves by hand, so idle stopwatches are
// deterministic.
type fakeWall struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeWall() *fakeWall {
	return &fakeWall{t: time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)}
}

func (w *fakeWall) now() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.t
}

func (w *fakeWall) advance(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.t = w.t.Add(d)
}

type harness struct {
	pool     *pool.Pool
	launcher *pooltest.FakeLauncher
	runtime  *pooltest.FakeRuntime
	wall     *fakeWall
}

// outcomeHandler answers by the payload: success, error, timeout or exit;
// "block" runs until its invocation ends.
func outcomeHandler(ctx context.Context, inv runtimeapi.Invocation) (runtimeapi.Outcome, error) {
	switch string(inv.Payload) {
	case "error":
		return runtimeapi.FunctionError{Type: "Runtime.Unknown", Payload: []byte(`{"errorMessage":"boom"}`)}, nil
	case "timeout":
		return runtimeapi.Timeout{After: inv.Timeout}, nil
	case "exit":
		return runtimeapi.ExitError{Code: 1}, nil
	case "block":
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return runtimeapi.Success{Payload: inv.Payload}, nil
}

// testingT is what the helpers need; *testing.T and *rapid.T both have it.
type testingT interface {
	rapid.TB
	Cleanup(func())
}

// newStoppedHarness builds a pool on fakes without running it.
func newStoppedHarness(t testingT, opts pool.Options, handler pooltest.Handler) *harness {
	t.Helper()
	w := newFakeWall()
	c, err := clock.New(clock.Options{Wall: w.now})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{launcher: pooltest.NewFakeLauncher(), runtime: pooltest.NewFakeRuntime(handler), wall: w}
	opts.Launcher, opts.Runtime, opts.Clock = h.launcher, h.runtime, c
	if h.pool, err = pool.New(opts); err != nil {
		t.Fatal(err)
	}
	return h
}

// newHarness is newStoppedHarness with the pool's Run going until the test
// ends.
func newHarness(t testingT, opts pool.Options, handler pooltest.Handler) *harness {
	t.Helper()
	h := newStoppedHarness(t, opts, handler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.pool.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	return h
}

func fnSpec(name string, generation uint64) launcher.Spec {
	account, _ := aws.ParseAccountID("123456789012")   //nolint:errcheck // a valid literal
	region, _ := aws.KnownRegions().Parse("eu-west-1") //nolint:errcheck // a valid literal
	return launcher.Spec{
		Function:           launcher.Function{Name: name, Version: "$LATEST", Account: account, Region: region},
		Generation:         generation,
		Image:              "public.ecr.aws/lambda/provided@sha256:e10c4765865b384b15eacc855a3f25ac61745d8dd1f510217deecf23b88fdbd5",
		Architecture:       launcher.X86,
		Code:               launcher.ZipCode{Dir: "/code/" + name, Handler: "bootstrap"},
		MemoryMB:           128,
		EphemeralStorageMB: 512,
		Credentials:        launcher.Credentials{AccessKeyID: "ASIAEXAMPLE"},
		LogGroup:           "/aws/lambda/" + name,
	}
}

var requestSeq atomic.Int64

func invocationOf(payload string) runtimeapi.Invocation {
	n := requestSeq.Add(1)
	return runtimeapi.Invocation{
		RequestID:    ids.LambdaRequestID("req-" + time.Duration(n).String()),
		InvocationID: "inv",
		FunctionARN:  fnSpec("f", 0).Function.ARN(),
		TraceID:      "Root=1-0-0",
		Payload:      []byte(payload),
		Timeout:      time.Second,
	}
}

// invoke runs one invocation through a leased environment and releases it.
func (h *harness) invoke(t rapid.TB, e *pool.Env, payload string) runtimeapi.Outcome {
	t.Helper()
	o, err := e.Invoke(context.Background(), invocationOf(payload))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if err := h.pool.Release(context.Background(), e, o); err != nil {
		t.Fatalf("Release: %v", err)
	}
	return o
}

func acquire(t rapid.TB, p *pool.Pool, spec launcher.Spec) *pool.Env {
	t.Helper()
	e, err := p.Acquire(context.Background(), spec)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	return e
}

func TestWarmEnvironmentIsReused(t *testing.T) {
	h := newHarness(t, pool.Options{}, pooltest.Echo)
	first := acquire(t, h.pool, fnSpec("f", 1))
	if _, ok := first.State().(pool.Reserved); !ok {
		t.Errorf("acquired state = %#v, want Reserved", first.State())
	}
	h.invoke(t, first, "one")
	if _, ok := first.State().(pool.Idle); !ok {
		t.Errorf("state after a success = %#v, want Idle", first.State())
	}
	second := acquire(t, h.pool, fnSpec("f", 1))
	if second != first {
		t.Error("second Acquire started a new environment; want the warm one")
	}
	h.invoke(t, second, "two")
	if n := len(h.launcher.Started()); n != 1 {
		t.Errorf("containers started = %d, want 1 (warm reuse)", n)
	}
}

func TestStartSlotNamesTheLogStreamAndRuntimeAPI(t *testing.T) {
	h := newHarness(t, pool.Options{}, pooltest.Echo)
	e := acquire(t, h.pool, fnSpec("f", 1))
	started := h.launcher.Started()[0]
	if !regexp.MustCompile(`^2026/10/09/\[\$LATEST\][0-9a-f]{32}$`).MatchString(started.Slot.LogStream) || !strings.HasSuffix(started.Slot.LogStream, e.ID()) {
		t.Errorf("LogStream = %q, want YYYY/MM/DD/[$LATEST]<environment id>", started.Slot.LogStream)
	}
	if started.Slot.RuntimeAPI != h.runtime.Endpoints()[0].Addr() {
		t.Errorf("RuntimeAPI = %v, want the endpoint's address %v", started.Slot.RuntimeAPI, h.runtime.Endpoints()[0].Addr())
	}
}

func TestConcurrentAcquiresGetDistinctEnvironments(t *testing.T) {
	h := newHarness(t, pool.Options{}, pooltest.Echo)
	const n = 6
	envs := make(chan *pool.Env, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			e, err := h.pool.Acquire(context.Background(), fnSpec("f", 1))
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			envs <- e
		})
	}
	wg.Wait()
	close(envs)
	seen := map[*pool.Env]bool{}
	for e := range envs {
		if seen[e] {
			t.Fatal("one environment leased twice")
		}
		seen[e] = true
	}
	if h.pool.Len() != n {
		t.Errorf("Len = %d, want %d", h.pool.Len(), n)
	}
}

func TestFailingOutcomesRemoveTheEnvironment(t *testing.T) {
	for _, payload := range []string{"timeout", "exit"} {
		t.Run(payload, func(t *testing.T) {
			h := newHarness(t, pool.Options{}, outcomeHandler)
			e := acquire(t, h.pool, fnSpec("f", 1))
			h.invoke(t, e, payload)
			if _, ok := e.State().(pool.Gone); !ok {
				t.Errorf("state = %#v, want Gone", e.State())
			}
			if len(h.launcher.Live()) != 0 || !h.runtime.Endpoints()[0].Closed() {
				t.Errorf("live containers %v, endpoint closed %v; want the container removed and the listener closed", h.launcher.Live(), h.runtime.Endpoints()[0].Closed())
			}
			next := acquire(t, h.pool, fnSpec("f", 1))
			if next == e || len(h.launcher.Started()) != 2 {
				t.Error("the invocation after a failure reused the environment; it must start cold")
			}
		})
	}
}

func TestFunctionErrorKeepsTheEnvironmentWarm(t *testing.T) {
	h := newHarness(t, pool.Options{}, outcomeHandler)
	e := acquire(t, h.pool, fnSpec("f", 1))
	h.invoke(t, e, "error")
	if _, ok := e.State().(pool.Idle); !ok {
		t.Errorf("state after a function error = %#v, want Idle", e.State())
	}
}

// settled asserts e's state once every goroutine of the synctest bubble is
// blocked: the pool's Run has handled whatever it was sent.
func settled(t rapid.TB, e *pool.Env, want func(pool.EnvState) bool) {
	t.Helper()
	synctest.Wait()
	if s := e.State(); !want(s) {
		t.Fatalf("environment state = %#v once the pool settled", s)
	}
}

func isGone(s pool.EnvState) bool    { _, ok := s.(pool.Gone); return ok }
func isFaulted(s pool.EnvState) bool { _, ok := s.(pool.Faulted); return ok }
func isBusy(s pool.EnvState) bool    { _, ok := s.(pool.Busy); return ok }

// Run gives the launcher's one exit watch back before it returns, so the
// pool can run again at once and still sees exits.
func TestRunCanRunAgainAsSoonAsItReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newStoppedHarness(t, pool.Options{}, pooltest.Echo)
		run := func(ctx context.Context) <-chan error {
			done := make(chan error, 1)
			go func() { done <- h.pool.Run(ctx) }()
			return done
		}
		for i := range 50 {
			ctx, cancel := context.WithCancel(context.Background())
			done := run(ctx)
			synctest.Wait()
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run %d: %v", i+1, err)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := run(ctx)
		e := acquire(t, h.pool, fnSpec("f", 1))
		h.invoke(t, e, "x")
		h.launcher.Crash(e.Container().ID, launcher.Exited{Code: 1})
		settled(t, e, isGone)
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("last Run: %v", err)
		}
	})
}

func TestCrashOfAnIdleEnvironmentRemovesIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, pool.Options{}, pooltest.Echo)
		e := acquire(t, h.pool, fnSpec("f", 1))
		h.invoke(t, e, "x")
		h.launcher.Crash(e.Container().ID, launcher.OOMKilled{})
		settled(t, e, isGone)
		if h.pool.Len() != 0 {
			t.Errorf("Len = %d after the only environment crashed", h.pool.Len())
		}
	})
}

// The handler blocks until its invocation ends, so a crash must both fail
// the invocation and stop the handler: the bubble fails on a leak.
func TestCrashDuringAnInvocationFailsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, pool.Options{}, outcomeHandler)
		e := acquire(t, h.pool, fnSpec("f", 1))
		done := make(chan runtimeapi.Outcome, 1)
		go func() {
			o, err := e.Invoke(context.Background(), invocationOf("block"))
			if err != nil {
				t.Errorf("Invoke: %v", err)
			}
			done <- o
		}()
		settled(t, e, isBusy)
		h.launcher.Crash(e.Container().ID, launcher.Exited{Code: 3})
		o := <-done
		if o != (runtimeapi.ExitError{Code: 3}) {
			t.Fatalf("outcome = %#v, want ExitError 3", o)
		}
		if f, ok := e.State().(pool.Faulted); !ok || f.Cause != (pool.Crashed{Code: 3}) {
			t.Errorf("state = %#v, want Faulted Crashed 3", e.State())
		}
		if err := h.pool.Release(context.Background(), e, o); err != nil {
			t.Fatal(err)
		}
		if !isGone(e.State()) {
			t.Errorf("state after release = %#v, want Gone", e.State())
		}
	})
}

// Removing environments drained on the way to a lease can fail; the lease
// is still clean, and Acquire returns it without an error.
func TestAcquireNeverReturnsALeaseWithAnError(t *testing.T) {
	h := newHarness(t, pool.Options{}, pooltest.Echo)
	old := acquire(t, h.pool, fnSpec("f", 1))
	h.invoke(t, old, "x")
	h.launcher.FailNextRemove(errors.New("daemon unavailable"))
	e, err := h.pool.Acquire(context.Background(), fnSpec("f", 2))
	if err != nil || e == nil {
		t.Fatalf("Acquire while the drained generation fails to be removed = %v, %v; want a lease and no error", e, err)
	}
	if !isGone(old.State()) {
		t.Errorf("older generation = %#v, want drained", old.State())
	}
	if o := h.invoke(t, e, "y"); o == nil {
		t.Error("the lease could not run an invocation")
	}
}

func TestNewRefusesAnIdleTimeoutThatWouldSpin(t *testing.T) {
	c, err := clock.New(clock.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.New(pool.Options{
		Launcher:    pooltest.NewFakeLauncher(),
		Runtime:     pooltest.NewFakeRuntime(pooltest.Echo),
		Clock:       c,
		IdleTimeout: time.Nanosecond,
	})
	if err == nil || !strings.Contains(err.Error(), "IdleTimeout") {
		t.Errorf("New with a 1 ns idle timeout = %v, want an error naming IdleTimeout", err)
	}
}

func TestNewGenerationDrainsOlderEnvironments(t *testing.T) {
	h := newHarness(t, pool.Options{}, pooltest.Echo)
	idle := acquire(t, h.pool, fnSpec("f", 1))
	busy := acquire(t, h.pool, fnSpec("f", 1))
	other := acquire(t, h.pool, fnSpec("g", 1))
	h.invoke(t, idle, "x")
	h.invoke(t, other, "x")
	updated := acquire(t, h.pool, fnSpec("f", 2))
	if !isGone(idle.State()) {
		t.Errorf("idle environment of the old generation = %#v, want drained at once", idle.State())
	}
	if _, ok := other.State().(pool.Idle); !ok {
		t.Errorf("another function's environment = %#v, want untouched", other.State())
	}
	if _, err := busy.Invoke(context.Background(), invocationOf("x")); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.Release(context.Background(), busy, runtimeapi.Success{}); err != nil {
		t.Fatal(err)
	}
	if !isGone(busy.State()) {
		t.Errorf("busy environment of the old generation after release = %#v, want drained", busy.State())
	}
	h.invoke(t, updated, "x")
	if again := acquire(t, h.pool, fnSpec("f", 2)); again != updated {
		t.Error("the new generation's environment was not reused")
	}
}

func TestIdleEnvironmentsAreReapedOnMonotonicTime(t *testing.T) {
	h := newHarness(t, pool.Options{IdleTimeout: 10 * time.Minute}, pooltest.Echo)
	e := acquire(t, h.pool, fnSpec("f", 1))
	h.invoke(t, e, "x")
	h.wall.advance(9 * time.Minute)
	if err := h.pool.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.State().(pool.Idle); !ok {
		t.Fatalf("state after 9 idle minutes = %#v, want Idle", e.State())
	}
	h.wall.advance(time.Minute)
	if err := h.pool.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !isGone(e.State()) || len(h.launcher.Live()) != 0 {
		t.Errorf("state after 10 idle minutes = %#v, live %v; want removed", e.State(), h.launcher.Live())
	}
}

// Run reaps every quarter of the idle timeout, here every second, without
// a call to Reap; container exits arriving more often than that must not
// postpone it.
func TestRunReapsIdleEnvironmentsWhileExitsKeepArriving(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			idleTimeout = 4 * time.Second
			interval    = idleTimeout / 4
		)
		h := newHarness(t, pool.Options{IdleTimeout: idleTimeout}, pooltest.Echo)
		idle := acquire(t, h.pool, fnSpec("f", 1))
		h.invoke(t, idle, "x")
		// Leased environments are never reaped, so each can crash on cue.
		crashing := []*pool.Env{acquire(t, h.pool, fnSpec("g", 1)), acquire(t, h.pool, fnSpec("g", 1)), acquire(t, h.pool, fnSpec("g", 1))}
		h.wall.advance(idleTimeout)
		for _, e := range crashing {
			time.Sleep(interval * 3 / 5)
			if !h.launcher.Crash(e.Container().ID, launcher.Exited{Code: 1}) {
				t.Fatalf("container of %s was not live", e.ID())
			}
			settled(t, e, isFaulted)
		}
		// Three exits 600 ms apart span 1.8 s, past the first reap at 1 s.
		if !isGone(idle.State()) || slices.Contains(h.launcher.Live(), idle.Container().ID) {
			t.Errorf("environment idle past the timeout = %#v with exits every %s; want reaped by Run within %s", idle.State(), interval*3/5, interval)
		}
	})
}

func TestIdleCapEvictsTheLeastRecentlyUsed(t *testing.T) {
	h := newHarness(t, pool.Options{IdleCap: 2}, pooltest.Echo)
	envs := []*pool.Env{acquire(t, h.pool, fnSpec("f", 1)), acquire(t, h.pool, fnSpec("f", 1)), acquire(t, h.pool, fnSpec("f", 1))}
	for _, e := range envs {
		h.invoke(t, e, "x")
	}
	if !isGone(envs[0].State()) {
		t.Errorf("least recently used = %#v, want evicted", envs[0].State())
	}
	for _, e := range envs[1:] {
		if _, ok := e.State().(pool.Idle); !ok {
			t.Errorf("recent environment = %#v, want Idle", e.State())
		}
	}
}

func TestGlobalCapEvictsIdleOrRefuses(t *testing.T) {
	h := newHarness(t, pool.Options{GlobalCap: 2}, pooltest.Echo)
	a := acquire(t, h.pool, fnSpec("f", 1))
	b := acquire(t, h.pool, fnSpec("g", 1))
	_, err := h.pool.Acquire(context.Background(), fnSpec("h", 1))
	if capErr, ok := errors.AsType[*pool.CapacityError](err); !ok || capErr.Cap != 2 {
		t.Fatalf("Acquire with every environment busy = %v, want *CapacityError", err)
	}
	h.invoke(t, a, "x")
	c := acquire(t, h.pool, fnSpec("h", 1))
	if !isGone(a.State()) {
		t.Errorf("idle environment at the global cap = %#v, want evicted", a.State())
	}
	if h.pool.Len() != 2 {
		t.Errorf("Len = %d, want the cap 2", h.pool.Len())
	}
	h.invoke(t, b, "x")
	h.invoke(t, c, "x")
}

func TestStartConcurrencyIsBounded(t *testing.T) {
	synctest.Test(t, testStartConcurrencyIsBounded)
}

func testStartConcurrencyIsBounded(t *testing.T) {
	gate := make(chan struct{})
	var running, peak atomic.Int32
	l := &gatedLauncher{FakeLauncher: pooltest.NewFakeLauncher(), gate: gate, running: &running, peak: &peak}
	w := newFakeWall()
	c, err := clock.New(clock.Options{Wall: w.now})
	if err != nil {
		t.Fatal(err)
	}
	p, err := pool.New(pool.Options{Launcher: l, Runtime: pooltest.NewFakeRuntime(pooltest.Echo), Clock: c, StartConcurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if _, err := p.Acquire(context.Background(), fnSpec("f", 1)); err != nil {
				t.Errorf("Acquire: %v", err)
			}
		})
	}
	for released := range 5 {
		// Every starter is blocked: in the launcher or against the limit.
		synctest.Wait()
		if got, want := running.Load(), int32(min(2, 5-released)); got != want {
			t.Errorf("%d containers starting at once with %d acquires pending, want %d", got, 5-released, want)
		}
		gate <- struct{}{}
	}
	wg.Wait()
	if got := peak.Load(); got != 2 {
		t.Errorf("peak concurrent starts = %d, want the limit 2", got)
	}
}

// An Acquire whose context ends while it waits for a start slot says so,
// which is the clue to throttled cold starts.
func TestAcquireWaitingForAStartSlotNamesTheLimit(t *testing.T) {
	synctest.Test(t, testAcquireWaitingForAStartSlotNamesTheLimit)
}

func testAcquireWaitingForAStartSlotNamesTheLimit(t *testing.T) {
	gate := make(chan struct{})
	var running, peak atomic.Int32
	l := &gatedLauncher{FakeLauncher: pooltest.NewFakeLauncher(), gate: gate, running: &running, peak: &peak}
	c, err := clock.New(clock.Options{Wall: newFakeWall().now})
	if err != nil {
		t.Fatal(err)
	}
	p, err := pool.New(pool.Options{Launcher: l, Runtime: pooltest.NewFakeRuntime(pooltest.Echo), Clock: c, StartConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := p.Acquire(context.Background(), fnSpec("f", 1)); err != nil {
			t.Errorf("first Acquire: %v", err)
		}
	})
	synctest.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = p.Acquire(ctx, fnSpec("f", 1))
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "one of 1 concurrent environment starts") {
		t.Errorf("Acquire = %v, want the deadline wrapped with the start limit", err)
	}
	gate <- struct{}{}
	wg.Wait()
}

type gatedLauncher struct {
	*pooltest.FakeLauncher
	gate          chan struct{}
	running, peak *atomic.Int32
}

func (g *gatedLauncher) Start(ctx context.Context, spec launcher.Spec, slot launcher.Slot) (launcher.Container, error) {
	n := g.running.Add(1)
	for {
		p := g.peak.Load()
		if n <= p || g.peak.CompareAndSwap(p, n) {
			break
		}
	}
	<-g.gate
	g.running.Add(-1)
	return g.FakeLauncher.Start(ctx, spec, slot)
}

func TestFailedStartLeavesNothing(t *testing.T) {
	h := newHarness(t, pool.Options{}, pooltest.Echo)
	scripted := errors.New("no such image")
	h.launcher.FailNextStart(scripted)
	if _, err := h.pool.Acquire(context.Background(), fnSpec("f", 1)); !errors.Is(err, scripted) {
		t.Fatalf("Acquire = %v, want the launcher's error", err)
	}
	if h.pool.Len() != 0 || !h.runtime.Endpoints()[0].Closed() {
		t.Errorf("Len = %d, endpoint closed %v; want nothing left", h.pool.Len(), h.runtime.Endpoints()[0].Closed())
	}
}

func TestShutdownDrainsAndRefusesNewLeases(t *testing.T) {
	h := newHarness(t, pool.Options{}, pooltest.Echo)
	idle := acquire(t, h.pool, fnSpec("f", 1))
	h.invoke(t, idle, "x")
	busy := acquire(t, h.pool, fnSpec("g", 1))
	if err := h.pool.Drain(context.Background(), pool.All{}, pool.Shutdown); err != nil {
		t.Fatal(err)
	}
	if !isGone(idle.State()) {
		t.Errorf("idle environment after shutdown = %#v, want Gone", idle.State())
	}
	if _, err := h.pool.Acquire(context.Background(), fnSpec("f", 1)); !errors.As(err, new(*pool.ClosedError)) {
		t.Errorf("Acquire after shutdown = %v, want *ClosedError", err)
	}
	h.invoke(t, busy, "x")
	if !isGone(busy.State()) || h.pool.Len() != 0 {
		t.Errorf("busy environment after release = %#v, Len %d; want drained", busy.State(), h.pool.Len())
	}
}

func TestDrainByFunctionSparesOthers(t *testing.T) {
	h := newHarness(t, pool.Options{}, pooltest.Echo)
	f := acquire(t, h.pool, fnSpec("f", 1))
	g := acquire(t, h.pool, fnSpec("g", 1))
	h.invoke(t, f, "x")
	h.invoke(t, g, "x")
	if err := h.pool.Drain(context.Background(), pool.Function{ARN: fnSpec("f", 1).Function.ARN()}, pool.Reset); err != nil {
		t.Fatal(err)
	}
	if !isGone(f.State()) {
		t.Errorf("drained function's environment = %#v", f.State())
	}
	if _, ok := g.State().(pool.Idle); !ok {
		t.Errorf("other function's environment = %#v, want Idle", g.State())
	}
}

func TestReleaseWithoutAnOutcome(t *testing.T) {
	h := newHarness(t, pool.Options{}, pooltest.Echo)
	reserved := acquire(t, h.pool, fnSpec("f", 1))
	if err := h.pool.Release(context.Background(), reserved, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := reserved.State().(pool.Idle); !ok {
		t.Errorf("released unused = %#v, want Idle", reserved.State())
	}
	if err := h.pool.Release(context.Background(), reserved, nil); !errors.As(err, new(*pool.LeaseError)) {
		t.Errorf("second Release = %v, want *LeaseError", err)
	}
	busy := acquire(t, h.pool, fnSpec("f", 1))
	if _, err := busy.Invoke(context.Background(), invocationOf("x")); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.Release(context.Background(), busy, nil); err != nil {
		t.Fatal(err)
	}
	if !isGone(busy.State()) {
		t.Errorf("busy environment released without an outcome = %#v, want drained", busy.State())
	}
}

func TestInvalidInvocationLeavesTheEnvironmentWarm(t *testing.T) {
	h := newHarness(t, pool.Options{}, pooltest.Echo)
	e := acquire(t, h.pool, fnSpec("f", 1))
	inv := invocationOf("x")
	inv.TraceID = ""
	if _, err := e.Invoke(context.Background(), inv); !errors.As(err, new(*runtimeapi.InvocationError)) {
		t.Fatalf("Invoke without a trace id = %v, want *runtimeapi.InvocationError", err)
	}
	if err := h.pool.Release(context.Background(), e, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.State().(pool.Idle); !ok {
		t.Errorf("environment after a refused invocation = %#v, want Idle: nothing reached the runtime", e.State())
	}
}

func TestNewNamesANegativeLimit(t *testing.T) {
	c, err := clock.New(clock.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for name, opts := range map[string]pool.Options{
		"StartConcurrency": {StartConcurrency: -1},
		"IdleCap":          {IdleCap: -1},
		"GlobalCap":        {GlobalCap: -1},
		"IdleTimeout":      {IdleTimeout: -time.Second},
	} {
		opts.Launcher = pooltest.NewFakeLauncher()
		opts.Runtime = pooltest.NewFakeRuntime(pooltest.Echo)
		opts.Clock = c
		_, err := pool.New(opts)
		if err == nil || !strings.Contains(err.Error(), "Options."+name+" is -") {
			t.Errorf("New with a negative %s = %v, want an error naming Options.%s", name, err, name)
		}
	}
}
