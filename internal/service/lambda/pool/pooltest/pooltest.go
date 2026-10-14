// Package pooltest provides in-memory fakes of the pool's Launcher and
// Runtime, so the pool and the Lambda service can be tested without Docker
// or a runtime in a container.
//
// FakeLauncher starts nothing: it records specs, hands out container IDs,
// reports the exits a test scripts with Crash, and fails the Start or
// Remove calls a test queues. FakeRuntime opens endpoints whose
// invocations a test handler answers; a Terminate fails the in-flight and
// every later invocation with the exit's outcome, as the real Runtime API
// does.
package pooltest

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sync"

	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
)

// Started is one Start call the fake launcher served.
type Started struct {
	Container launcher.Container
	Spec      launcher.Spec
	Slot      launcher.Slot
}

// FakeLauncher implements pool.Launcher in memory. It is safe for
// concurrent use.
type FakeLauncher struct {
	mu         sync.Mutex
	started    []Started
	live       map[string]bool
	failNext   []error
	failRemove []error
	exits      chan launcher.Exit
	watching   bool
}

var _ pool.Launcher = (*FakeLauncher)(nil)

// NewFakeLauncher returns a launcher with no containers.
func NewFakeLauncher() *FakeLauncher {
	return &FakeLauncher{live: map[string]bool{}, exits: make(chan launcher.Exit, 1024)}
}

// Start records the call and returns a new container, or the error queued
// by FailNextStart.
func (f *FakeLauncher) Start(ctx context.Context, spec launcher.Spec, slot launcher.Slot) (launcher.Container, error) {
	if err := ctx.Err(); err != nil {
		return launcher.Container{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.failNext) > 0 {
		err := f.failNext[0]
		f.failNext = f.failNext[1:]
		return launcher.Container{}, err
	}
	n := len(f.started) + 1
	c := launcher.Container{ID: fmt.Sprintf("fake%060d", n), Name: fmt.Sprintf("understudy-fake-%s-%d", spec.Function.Name, n)}
	f.started = append(f.started, Started{Container: c, Spec: spec, Slot: slot})
	f.live[c.ID] = true
	return c, nil
}

// Remove deletes the container; removing an unknown one is not an error,
// as with the real launcher. A failure queued by FailNextRemove leaves the
// container live and returns that error.
func (f *FakeLauncher) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.failRemove) > 0 {
		err := f.failRemove[0]
		f.failRemove = f.failRemove[1:]
		return err
	}
	delete(f.live, id)
	return nil
}

// FakeMemoryUsed is what MemoryUsed reports for a live container: 36 MiB,
// the Max Memory Used of AWS's recorded REPORT line (lambda-api.md 9.7).
const FakeMemoryUsed = 36 << 20

// MemoryUsed reports FakeMemoryUsed for a live container and fails for any
// other, as the daemon has no statistics of a removed one.
func (f *FakeLauncher) MemoryUsed(_ context.Context, id string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.live[id] {
		return 0, fmt.Errorf("pooltest: no container %s runs", id)
	}
	return FakeMemoryUsed, nil
}

// FailNextRemove makes the next Remove fail with err.
func (f *FakeLauncher) FailNextRemove(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failRemove = append(f.failRemove, err)
}

// Exits forwards the exits Crash queues until ctx ends; then the channel
// closes, and Exits may be called again. Like the real launcher it has one
// consumer at a time, frees the watch before the close, drops an exit
// still in flight when ctx ends, and keeps queued exits for the next
// consumer.
func (f *FakeLauncher) Exits(ctx context.Context) (<-chan launcher.Exit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.watching {
		return nil, &launcher.WatchError{}
	}
	f.watching = true
	out := make(chan launcher.Exit)
	go func() {
		defer close(out)
		defer func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.watching = false
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case exit := <-f.exits:
				select {
				case out <- exit:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// Crash makes a live container stop with cause, as if its process died.
// It reports whether the container was live. The exit is sent without
// f.mu held: the consumer may call Remove before it receives the next one.
func (f *FakeLauncher) Crash(id string, cause launcher.ExitCause) bool {
	f.mu.Lock()
	live := f.live[id]
	delete(f.live, id)
	f.mu.Unlock()
	if !live {
		return false
	}
	f.exits <- launcher.Exit{ContainerID: id, Cause: cause}
	return true
}

// FailNextStart makes the next Start fail with err.
func (f *FakeLauncher) FailNextStart(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = append(f.failNext, err)
}

// Started returns every Start served, in order.
func (f *FakeLauncher) Started() []Started {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.started)
}

// Live returns the IDs of containers started and neither removed nor
// crashed, sorted.
func (f *FakeLauncher) Live() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.live))
	for id := range f.live {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Handler answers one invocation for a FakeRuntime.
type Handler func(ctx context.Context, inv runtimeapi.Invocation) (runtimeapi.Outcome, error)

// Echo answers every invocation with its own payload.
func Echo(_ context.Context, inv runtimeapi.Invocation) (runtimeapi.Outcome, error) {
	return runtimeapi.Success{Payload: inv.Payload}, nil
}

// FakeRuntime implements pool.Runtime with in-memory endpoints. It is safe
// for concurrent use.
type FakeRuntime struct {
	handler Handler

	mu        sync.Mutex
	endpoints []*FakeEndpoint
}

var _ pool.Runtime = (*FakeRuntime)(nil)

// NewFakeRuntime answers every invocation with handler.
func NewFakeRuntime(handler Handler) *FakeRuntime {
	return &FakeRuntime{handler: handler}
}

// Open returns a new endpoint with a unique loopback address.
func (r *FakeRuntime) Open(ctx context.Context) (pool.Endpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := &FakeEndpoint{
		rt:    r,
		addr:  netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(20000+len(r.endpoints))), //nolint:gosec // tests open far fewer than 45000 endpoints
		ended: make(chan struct{}),
	}
	r.endpoints = append(r.endpoints, e)
	return e, nil
}

// Endpoints returns every endpoint opened, in order.
func (r *FakeRuntime) Endpoints() []*FakeEndpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.endpoints)
}

// FakeEndpoint is one environment's fake Runtime API.
type FakeEndpoint struct {
	rt   *FakeRuntime
	addr netip.AddrPort

	mu          sync.Mutex
	invocations int
	inFlight    bool
	terminated  runtimeapi.Outcome
	ended       chan struct{} // closed by Terminate or Close
	closed      bool
}

// Addr is the endpoint's address.
func (e *FakeEndpoint) Addr() netip.AddrPort { return e.addr }

// Invoke runs the runtime's handler, unless the endpoint is closed,
// terminated or already busy with an invocation (a *runtimeapi.BusyError);
// a Terminate or Close during the handler wins with its outcome. The
// handler's context ends when Invoke returns, so a handler
// that blocks until ctx is done never outlives its invocation.
func (e *FakeEndpoint) Invoke(ctx context.Context, inv runtimeapi.Invocation) (runtimeapi.Outcome, error) {
	e.mu.Lock()
	switch {
	case e.closed:
		e.mu.Unlock()
		return nil, &runtimeapi.ClosedError{Addr: e.addr}
	case e.terminated != nil:
		e.mu.Unlock()
		return e.terminated, nil
	case e.inFlight:
		e.mu.Unlock()
		return nil, &runtimeapi.BusyError{Addr: e.addr}
	}
	e.inFlight = true
	e.invocations++
	ended := e.ended
	e.mu.Unlock()
	type result struct {
		o   runtimeapi.Outcome
		err error
	}
	handlerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan result, 1)
	go func() {
		o, err := e.rt.handler(handlerCtx, inv)
		done <- result{o, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-ended:
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inFlight = false
	switch {
	case e.terminated != nil:
		return e.terminated, nil
	case e.closed:
		return nil, &runtimeapi.ClosedError{Addr: e.addr}
	}
	return r.o, r.err
}

// Terminate fails the in-flight and later invocations with o.
func (e *FakeEndpoint) Terminate(o runtimeapi.Outcome) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.terminated == nil && !e.closed {
		e.terminated = o
		e.end()
	}
}

// Close ends the endpoint; closing twice is not an error.
func (e *FakeEndpoint) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed {
		e.closed = true
		e.end()
	}
	return nil
}

// Invocations counts the invocations the handler ran.
func (e *FakeEndpoint) Invocations() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.invocations
}

// Closed reports whether the endpoint was closed.
func (e *FakeEndpoint) Closed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closed
}

// ServerRuntime adapts a real runtimeapi.Server to pool.Runtime, for the
// Docker tests that run functions in containers without the server's
// container host.
func ServerRuntime(s *runtimeapi.Server) pool.Runtime { return serverRuntime{s} }

type serverRuntime struct{ s *runtimeapi.Server }

func (r serverRuntime) Open(ctx context.Context) (pool.Endpoint, error) { return r.s.Open(ctx) }

// end wakes an in-flight Invoke, once. Callers hold e.mu.
func (e *FakeEndpoint) end() {
	select {
	case <-e.ended:
	default:
		close(e.ended)
	}
}
