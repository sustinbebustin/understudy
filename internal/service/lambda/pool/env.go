package pool

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
)

// Env is one execution environment: a container and its Runtime API
// listener. Its state is guarded by the pool's lock.
type Env struct {
	pool      *Pool
	id        string
	key       Key
	endpoint  Endpoint
	container launcher.Container
	// logStream is the AWS_LAMBDA_LOG_STREAM_NAME the container was
	// started with.
	logStream string
	state     EnvState
	// leased: a caller holds the environment between Acquire and Release.
	leased bool
	// drain, when set, is why the environment drains on release.
	drain DrainReason
}

// ID names the environment; its log stream and container name end in it.
func (e *Env) ID() string { return e.id }

// Key is the environment's pool key.
func (e *Env) Key() Key { return e.key }

// Container is the environment's container.
func (e *Env) Container() launcher.Container {
	e.pool.mu.Lock()
	defer e.pool.mu.Unlock()
	return e.container
}

// LogStream is the log stream name the environment's container was
// started with as AWS_LAMBDA_LOG_STREAM_NAME, YYYY/MM/DD/[version]<id>,
// dated when the pool started it.
func (e *Env) LogStream() string {
	e.pool.mu.Lock()
	defer e.pool.mu.Unlock()
	return e.logStream
}

// MemoryUsed reads the bytes of memory the environment's container holds
// now; the REPORT line's Max Memory Used reads it after each invocation.
func (e *Env) MemoryUsed(ctx context.Context) (uint64, error) {
	return e.pool.launcher.MemoryUsed(ctx, e.Container().ID)
}

// State is the environment's current state.
func (e *Env) State() EnvState {
	e.pool.mu.Lock()
	defer e.pool.mu.Unlock()
	return e.state
}

// Invoke runs one invocation in the leased environment and returns its
// outcome; pass the outcome to Release. Only the leaseholder may call it,
// once per lease. An invalid inv is refused with its
// *runtimeapi.InvocationError before the environment turns busy, so a
// Release without an outcome returns it to the idle set.
func (e *Env) Invoke(ctx context.Context, inv runtimeapi.Invocation) (runtimeapi.Outcome, error) {
	p := e.pool
	p.mu.Lock()
	if !e.leased {
		p.mu.Unlock()
		return nil, &LeaseError{Env: e.id}
	}
	if err := inv.Validate(); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	switch e.state.(type) {
	case Reserved:
		e.state = Busy{Invocation: inv.RequestID}
	case Faulted:
		// The container already stopped; the endpoint returns the exit's
		// outcome.
	case Starting, Busy, Idle, Draining, Gone:
		p.mu.Unlock()
		return nil, &LeaseError{Env: e.id}
	}
	p.mu.Unlock()
	return e.endpoint.Invoke(ctx, inv)
}
