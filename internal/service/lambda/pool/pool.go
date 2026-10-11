package pool

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
)

// Defaults (ARCHITECTURE.md 14.2, containers.md 6.3).
const (
	DefaultStartConcurrency = 16
	DefaultGlobalCap        = 64
	DefaultIdleTimeout      = 10 * time.Minute
	// MinIdleTimeout keeps the reap interval (a quarter of the idle
	// timeout) from spinning.
	MinIdleTimeout  = time.Second
	minIdleCap      = 4
	maxReapInterval = 30 * time.Second
)

// Launcher is what the pool needs from launcher.Launcher.
type Launcher interface {
	Start(ctx context.Context, spec launcher.Spec, slot launcher.Slot) (launcher.Container, error)
	Remove(ctx context.Context, id string) error
	// Exits has one consumer at a time; its channel closes after ctx ends,
	// once Exits may be called again.
	Exits(ctx context.Context) (<-chan launcher.Exit, error)
}

// Runtime opens a Runtime API endpoint per environment.
type Runtime interface {
	Open(ctx context.Context) (Endpoint, error)
}

// Endpoint is one environment's Runtime API, as runtimeapi.Environment
// implements it.
type Endpoint interface {
	Addr() netip.AddrPort
	Invoke(ctx context.Context, inv runtimeapi.Invocation) (runtimeapi.Outcome, error)
	Terminate(o runtimeapi.Outcome)
	Close() error
}

// ServerRuntime adapts a runtimeapi.Server to Runtime.
func ServerRuntime(s *runtimeapi.Server) Runtime { return serverRuntime{s} }

type serverRuntime struct{ s *runtimeapi.Server }

func (r serverRuntime) Open(ctx context.Context) (Endpoint, error) { return r.s.Open(ctx) }

// Options configures a Pool. Zero limits take the defaults.
type Options struct {
	Launcher Launcher
	Runtime  Runtime
	// Clock supplies monotonic stopwatches for idle reaping and emulator
	// time for log stream names.
	Clock *clock.Clock
	// StartConcurrency bounds containers starting at once; default 16.
	StartConcurrency int
	// IdleCap bounds idle environments per function version; default
	// max(4, CPUs).
	IdleCap int
	// GlobalCap bounds all environments; default 64. Acquiring past it
	// evicts the least recently used idle environment.
	GlobalCap int
	// IdleTimeout removes an environment idle that long; default 10
	// minutes, at least MinIdleTimeout.
	IdleTimeout time.Duration
	// Logger receives removal failures; nil discards them.
	Logger *slog.Logger
}

// Pool keeps warm execution environments and leases one per invocation.
// It is safe for concurrent use. Run must be running for crash detection
// and idle reaping.
type Pool struct {
	launcher    Launcher
	runtime     Runtime
	clock       *clock.Clock
	idleCap     int
	globalCap   int
	idleTimeout time.Duration
	log         *slog.Logger
	starts      chan struct{}

	mu          sync.Mutex
	envs        map[*Env]struct{}
	idle        []*Env // least recently used first
	byContainer map[string]*Env
	// unclaimed holds exits of unknown containers while a launcher Start is
	// in flight: one of them may be that Start's container, which exited
	// before Start returned. It is emptied whenever no Start is in flight,
	// so exits of containers already retired do not accumulate.
	unclaimed   map[string]launcher.Exit
	launching   int
	generations map[function]uint64
	closed      bool
}

// New validates opts.
func New(opts Options) (*Pool, error) {
	switch {
	case opts.Launcher == nil:
		return nil, errors.New("pool: Options.Launcher is nil")
	case opts.Runtime == nil:
		return nil, errors.New("pool: Options.Runtime is nil")
	case opts.Clock == nil:
		return nil, errors.New("pool: Options.Clock is nil")
	case opts.IdleTimeout != 0 && opts.IdleTimeout < MinIdleTimeout:
		return nil, fmt.Errorf("pool: Options.IdleTimeout is %s, below the minimum %s; use at least %s or zero for %s", opts.IdleTimeout, MinIdleTimeout, MinIdleTimeout, DefaultIdleTimeout)
	}
	for _, limit := range []struct {
		name  string
		value int
	}{{"StartConcurrency", opts.StartConcurrency}, {"IdleCap", opts.IdleCap}, {"GlobalCap", opts.GlobalCap}} {
		if limit.value < 0 {
			return nil, fmt.Errorf("pool: Options.%s is %d; use a positive value or zero for the default", limit.name, limit.value)
		}
	}
	p := &Pool{
		launcher:    opts.Launcher,
		runtime:     opts.Runtime,
		clock:       opts.Clock,
		idleCap:     cmp.Or(opts.IdleCap, max(minIdleCap, runtime.NumCPU())),
		globalCap:   cmp.Or(opts.GlobalCap, DefaultGlobalCap),
		idleTimeout: cmp.Or(opts.IdleTimeout, DefaultIdleTimeout),
		log:         cmp.Or(opts.Logger, slog.New(slog.DiscardHandler)),
		starts:      make(chan struct{}, cmp.Or(opts.StartConcurrency, DefaultStartConcurrency)),
		envs:        map[*Env]struct{}{},
		byContainer: map[string]*Env{},
		unclaimed:   map[string]launcher.Exit{},
		generations: map[function]uint64{},
	}
	return p, nil
}

// Acquire leases an environment for spec: the most recently used idle one
// of the same key, or a new one. A newer generation drains the function's
// idle environments of older generations at once; busy ones drain on
// release. At the global cap the least recently used idle environment is
// evicted; with none idle, Acquire fails with *CapacityError.
func (p *Pool) Acquire(ctx context.Context, spec launcher.Spec) (*Env, error) {
	key := NewKey(spec)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, &ClosedError{}
	}
	retire := p.noteGenerationLocked(key)
	if e := p.takeIdleLocked(key); e != nil {
		p.mu.Unlock()
		p.retireLogged(ctx, retire)
		return e, nil
	}
	if p.occupiedLocked() >= p.globalCap {
		if len(p.idle) == 0 {
			p.mu.Unlock()
			p.retireLogged(ctx, retire)
			return nil, &CapacityError{Cap: p.globalCap}
		}
		victim := p.idle[0]
		p.drainIdleLocked(victim, Evicted)
		retire = append(retire, victim)
	}
	e := &Env{pool: p, id: newEnvID(), key: key, state: Starting{Elapsed: p.clock.Stopwatch()}, leased: true}
	p.envs[e] = struct{}{}
	p.mu.Unlock()
	p.retireLogged(ctx, retire)
	if err := p.start(ctx, e, spec); err != nil {
		p.mu.Lock()
		delete(p.envs, e)
		e.state = Gone{}
		p.mu.Unlock()
		return nil, err
	}
	return e, nil
}

// Release returns a leased environment with the outcome of its
// invocation. Success and function errors keep it warm unless it must
// drain; a failing outcome (timeout, crash, OOM, init failure) removes its
// container so the next invocation starts cold. Pass a nil outcome when
// Invoke returned an error or was never called: a reserved environment
// goes back idle, a busy one drains because its runtime's state is unknown.
func (p *Pool) Release(ctx context.Context, e *Env, outcome runtimeapi.Outcome) error {
	p.mu.Lock()
	if !e.leased {
		p.mu.Unlock()
		return &LeaseError{Env: e.id}
	}
	e.leased = false
	var retire []*Env
	cause, failed := faultOf(outcome)
	_, reserved := e.state.(Reserved)
	switch {
	case isFaulted(e.state):
		retire = append(retire, e)
	case failed:
		e.state = Faulted{Cause: cause}
		retire = append(retire, e)
	case outcome == nil && !reserved:
		e.state = Draining{Reason: Abandoned}
		retire = append(retire, e)
	case e.drain != 0:
		e.state = Draining{Reason: e.drain}
		retire = append(retire, e)
	case p.closed:
		e.state = Draining{Reason: Shutdown}
		retire = append(retire, e)
	case e.key.Generation < p.generations[e.key.function()]:
		e.state = Draining{Reason: NewGeneration}
		retire = append(retire, e)
	default:
		e.state = Idle{Elapsed: p.clock.Stopwatch()}
		p.idle = append(p.idle, e)
		retire = p.capIdleLocked(e.key.function())
	}
	p.mu.Unlock()
	return p.retire(ctx, retire)
}

// Drain removes the selected environments with reason: idle ones now,
// leased ones when released. Draining with Shutdown also closes the pool
// to new leases. It returns once the idle ones are removed.
func (p *Pool) Drain(ctx context.Context, sel Selector, reason DrainReason) error {
	p.mu.Lock()
	if reason == Shutdown {
		p.closed = true
	}
	var retire []*Env
	for e := range p.envs {
		if !selects(sel, e.key) {
			continue
		}
		switch e.state.(type) {
		case Idle:
			p.drainIdleLocked(e, reason)
			retire = append(retire, e)
		case Starting, Reserved, Busy:
			e.drain = reason
		case Draining, Faulted, Gone:
		}
	}
	p.mu.Unlock()
	return p.retire(ctx, retire)
}

// Len is the number of environments not yet gone.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.envs)
}

// Reap removes environments idle for at least the idle timeout, measured
// on monotonic wall time.
func (p *Pool) Reap(ctx context.Context) error {
	p.mu.Lock()
	var retire []*Env
	for _, e := range slices.Clone(p.idle) {
		if idle, ok := e.state.(Idle); ok && idle.Elapsed() >= p.idleTimeout {
			p.drainIdleLocked(e, IdleExpired)
			retire = append(retire, e)
		}
	}
	p.mu.Unlock()
	return p.retire(ctx, retire)
}

// Run watches container exits and reaps idle environments until ctx ends.
// It fails only when the exit stream cannot be opened. It returns once
// the launcher has closed the stream, so Run can be called again at once.
func (p *Pool) Run(ctx context.Context) error {
	exits, err := p.launcher.Exits(ctx)
	if err != nil {
		return fmt.Errorf("pool: watch Lambda container exits: %w", err)
	}
	interval := min(p.idleTimeout/4, maxReapInterval)
	for p.handleExitsFor(ctx, exits, interval) {
		if err := p.Reap(ctx); err != nil {
			p.log.WarnContext(ctx, "reaping idle Lambda environments failed", slog.String("error", err.Error()))
		}
	}
	// The launcher frees its one exit watch only when it closes the
	// stream; exits arriving after ctx ended are dropped.
	for range exits {
	}
	return nil
}

// handleExitsFor handles container exits for one reap interval, measured
// from the call however many exits arrive, so frequent exits never
// postpone reaping. The interval is a context deadline because only
// internal/clock reads the wall clock. It reports false once ctx ends or
// the exit stream closes.
func (p *Pool) handleExitsFor(ctx context.Context, exits <-chan launcher.Exit, interval time.Duration) bool {
	tick, cancel := context.WithTimeout(context.WithoutCancel(ctx), interval)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return false
		case exit, ok := <-exits:
			if !ok {
				return false
			}
			p.handleExit(ctx, exit)
		case <-tick.Done():
			return true
		}
	}
}

// start opens the environment's Runtime API and starts its container,
// at most StartConcurrency at once.
func (p *Pool) start(ctx context.Context, e *Env, spec launcher.Spec) error {
	select {
	case p.starts <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("pool: wait for one of %d concurrent environment starts: %w", cap(p.starts), ctx.Err())
	}
	defer func() { <-p.starts }()
	endpoint, err := p.runtime.Open(ctx)
	if err != nil {
		return err
	}
	slot := launcher.Slot{
		ID:         e.id,
		RuntimeAPI: endpoint.Addr(),
		LogStream:  p.clock.Now().Wire().UTC().Format("2006/01/02") + "/[" + spec.Function.Version + "]" + e.id,
	}
	p.mu.Lock()
	p.launching++
	p.mu.Unlock()
	c, err := p.launcher.Start(ctx, spec, slot)
	p.mu.Lock()
	p.launching--
	var exit launcher.Exit
	exited := false
	if err == nil {
		e.endpoint, e.container, e.logStream = endpoint, c, slot.LogStream
		p.byContainer[c.ID] = e
		exit, exited = p.unclaimed[c.ID]
		delete(p.unclaimed, c.ID)
		if _, starting := e.state.(Starting); starting {
			e.state = Reserved{}
		}
	}
	if p.launching == 0 {
		clear(p.unclaimed)
	}
	p.mu.Unlock()
	if err != nil {
		return errors.Join(err, endpoint.Close())
	}
	if exited {
		p.handleExit(ctx, exit)
	}
	return nil
}

// handleExit fails the environment whose container stopped on its own:
// its invocation, if any, gets the exit's outcome, and an environment
// nobody leases is removed at once.
func (p *Pool) handleExit(ctx context.Context, exit launcher.Exit) {
	outcome := exitOutcome(exit.Cause)
	cause, _ := faultOf(outcome)
	p.mu.Lock()
	e, ok := p.byContainer[exit.ContainerID]
	if !ok {
		if p.launching > 0 {
			p.unclaimed[exit.ContainerID] = exit
		}
		p.mu.Unlock()
		return
	}
	switch e.state.(type) {
	case Idle:
		p.idle = slices.DeleteFunc(p.idle, func(x *Env) bool { return x == e })
		e.state = Faulted{Cause: cause}
	case Starting, Reserved, Busy:
		e.state = Faulted{Cause: cause}
	case Draining, Faulted, Gone:
		p.mu.Unlock()
		return
	}
	leased := e.leased
	p.mu.Unlock()
	e.endpoint.Terminate(outcome)
	if !leased {
		if err := p.retire(ctx, []*Env{e}); err != nil {
			p.log.WarnContext(ctx, "removing a crashed Lambda environment failed", slog.String("error", err.Error()))
		}
	}
}

// noteGenerationLocked records key's generation and, when it is newer
// than the function's, drains the idle environments of older generations
// and marks leased ones to drain on release. Callers hold p.mu.
func (p *Pool) noteGenerationLocked(key Key) []*Env {
	fn := key.function()
	if key.Generation <= p.generations[fn] {
		return nil
	}
	p.generations[fn] = key.Generation
	var retire []*Env
	for e := range p.envs {
		if e.key.function() != fn || e.key.Generation >= key.Generation {
			continue
		}
		switch e.state.(type) {
		case Idle:
			p.drainIdleLocked(e, NewGeneration)
			retire = append(retire, e)
		case Starting, Reserved, Busy:
			e.drain = NewGeneration
		case Draining, Faulted, Gone:
		}
	}
	return retire
}

// occupiedLocked counts the environments that hold a place under the
// global cap: all but those already on their way out. Callers hold p.mu.
func (p *Pool) occupiedLocked() int {
	n := 0
	for e := range p.envs {
		switch e.state.(type) {
		case Starting, Reserved, Busy, Idle:
			n++
		case Faulted:
			if e.leased {
				n++
			}
		case Draining, Gone:
		}
	}
	return n
}

// takeIdleLocked leases the most recently used idle environment of key.
// Callers hold p.mu.
func (p *Pool) takeIdleLocked(key Key) *Env {
	for i, e := range slices.Backward(p.idle) {
		if e.key == key {
			p.idle = slices.Delete(p.idle, i, i+1)
			e.state = Reserved{}
			e.leased = true
			return e
		}
	}
	return nil
}

// capIdleLocked evicts the least recently used idle environments of fn
// beyond the idle cap. Callers hold p.mu.
func (p *Pool) capIdleLocked(fn function) []*Env {
	var retire []*Env
	count := 0
	for _, e := range p.idle {
		if e.key.function() == fn {
			count++
		}
	}
	for _, e := range slices.Clone(p.idle) {
		if count <= p.idleCap {
			break
		}
		if e.key.function() == fn {
			p.drainIdleLocked(e, Evicted)
			retire = append(retire, e)
			count--
		}
	}
	return retire
}

// drainIdleLocked moves an idle environment to Draining. Callers hold p.mu.
func (p *Pool) drainIdleLocked(e *Env, reason DrainReason) {
	p.idle = slices.DeleteFunc(p.idle, func(x *Env) bool { return x == e })
	e.state = Draining{Reason: reason}
}

// retireLogged retires environments drained on the way to a lease. Their
// removal failing does not concern the caller, whose lease is fine, so it
// is logged rather than returned: Acquire never returns a lease together
// with an error.
func (p *Pool) retireLogged(ctx context.Context, envs []*Env) {
	if err := p.retire(ctx, envs); err != nil {
		p.log.WarnContext(ctx, "removing drained Lambda environments failed", slog.String("error", err.Error()))
	}
}

// retire closes each environment's listener, then removes its container,
// so a late request from the runtime finds nothing (ARCHITECTURE.md 14.2).
func (p *Pool) retire(ctx context.Context, envs []*Env) error {
	errs := make([]error, 0, len(envs))
	for _, e := range envs {
		var err error
		if e.endpoint != nil {
			err = errors.Join(e.endpoint.Close(), p.launcher.Remove(context.WithoutCancel(ctx), e.container.ID))
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("remove Lambda environment %s (container %s): %w", e.id, e.container.Name, err))
		}
		p.mu.Lock()
		delete(p.envs, e)
		delete(p.byContainer, e.container.ID)
		e.state = Gone{}
		p.mu.Unlock()
	}
	return errors.Join(errs...)
}

func selects(sel Selector, key Key) bool {
	switch s := sel.(type) {
	case Key:
		return s == key
	case Function:
		return s.ARN == key.Function
	case All:
		return true
	}
	return false
}

func isFaulted(s EnvState) bool {
	_, ok := s.(Faulted)
	return ok
}

// newEnvID is 32 lowercase hex characters, the form of Lambda's log stream
// suffixes (containers.md 3.4).
func newEnvID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
