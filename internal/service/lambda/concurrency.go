package lambda

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

func (s *Service) bindConcurrency(b lambdaapi.Binder) {
	// Invoke enforces reserved concurrency, and what reservations leave of
	// the account's, before any container work (Execution).
	b.PutFunctionConcurrency(s.putFunctionConcurrency, full())
	b.GetFunctionConcurrency(s.getFunctionConcurrency, full())
	b.DeleteFunctionConcurrency(s.deleteFunctionConcurrency, full())
}

// functionOnly reads a FunctionName that must name the function itself,
// as reserved concurrency is function-level (lambda-api.md 7.4).
func functionOnly(c *kernel.Call, name string) (functionRef, *awserr.Error) {
	ref, err := parseFunctionRef(c, name, nil)
	if err != nil {
		return functionRef{}, err
	}
	if ref.qualifier != "" {
		return functionRef{}, errFunctionsOnly()
	}
	return ref, nil
}

func (s *Service) putFunctionConcurrency(_ context.Context, c *kernel.Call, in *lambdaapi.PutFunctionConcurrencyInput) (*lambdaapi.Concurrency, *awserr.Error) {
	ref, err := functionOnly(c, in.FunctionName)
	if err != nil {
		return nil, err
	}
	_, err = write(s, c, ref.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		f, ok := st.Functions[ref.name]
		if !ok {
			// Put names $LATEST, unlike Get and Delete
			// (test_function_concurrency_exceptions).
			return struct{}{}, errFunctionNotFound(ref.arn() + ":" + latest)
		}
		if st.unreserved(ref.name)-in.ReservedConcurrentExecutions < minUnreservedConcurrency {
			return struct{}{}, errUnreservedMinimum()
		}
		f = f.clone()
		f.Reserved = new(in.ReservedConcurrentExecutions)
		st.Functions[ref.name] = f
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.arn())
	return &lambdaapi.Concurrency{ReservedConcurrentExecutions: new(in.ReservedConcurrentExecutions)}, nil
}

func (s *Service) getFunctionConcurrency(_ context.Context, c *kernel.Call, in *lambdaapi.GetFunctionConcurrencyInput) (*lambdaapi.GetFunctionConcurrencyOutput, *awserr.Error) {
	ref, err := functionOnly(c, in.FunctionName)
	if err != nil {
		return nil, err
	}
	return view(s, ref.scope, func(st *State) (*lambdaapi.GetFunctionConcurrencyOutput, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return nil, err
		}
		out := &lambdaapi.GetFunctionConcurrencyOutput{}
		if f.Reserved != nil {
			out.ReservedConcurrentExecutions = new(*f.Reserved)
		}
		return out, nil
	})
}

func (s *Service) deleteFunctionConcurrency(_ context.Context, c *kernel.Call, in *lambdaapi.DeleteFunctionConcurrencyInput) (*lambdaapi.DeleteFunctionConcurrencyOutput, *awserr.Error) {
	ref, err := functionOnly(c, in.FunctionName)
	if err != nil {
		return nil, err
	}
	_, err = write(s, c, ref.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return struct{}{}, err
		}
		f = f.clone()
		f.Reserved = nil
		st.Functions[ref.name] = f
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.arn())
	return &lambdaapi.DeleteFunctionConcurrencyOutput{}, nil
}

// concurrency counts running invocations: per function with a
// reservation, and per account and region for the rest, which share what
// the reservations leave (lambda-api.md 7.4). It also keeps each running
// invocation's scope and the scopes a drain holds, so a reset or snapshot
// load can keep invocations of the state it replaces from starting and
// wait for those already running.
type concurrency struct {
	mu         sync.Mutex
	reserved   map[fnKey]int32
	unreserved map[aws.Scope]int32
	// running holds the scope of every running invocation by a serial;
	// ended closes, and is replaced, whenever one ends.
	running map[uint64]aws.Scope
	serial  uint64
	ended   chan struct{}
	// held are the scopes hold took: no invocation of a scope they cover
	// starts until resume.
	held []kernel.ResetScope
	// shut is set at shutdown, after which no invocation starts.
	shut bool
}

func newConcurrency() *concurrency {
	return &concurrency{reserved: map[fnKey]int32{}, unreserved: map[aws.Scope]int32{}, running: map[uint64]aws.Scope{}, ended: make(chan struct{})}
}

// Throttle reasons (lambda-api.md 7.4).
const (
	reasonReservedLimit = "ReservedFunctionConcurrentInvocationLimitExceeded"
	reasonAccountLimit  = "ConcurrentInvocationLimitExceeded"
)

// acquire counts one more invocation of t, or refuses it: 429 when t's
// concurrency is exhausted, ServiceException while a drain holds its
// scope or after shutdown. release uncounts it.
func (c *concurrency) acquire(t *target) (release func(), err *awserr.Error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	scope := t.ref.scope
	switch {
	case c.shut:
		return nil, errShuttingDown()
	case slices.ContainsFunc(c.held, func(rs kernel.ResetScope) bool { return rs.Covers(scope) }):
		return nil, errStateReplacing(scope)
	}
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

// hold keeps invocations of the scopes rs covers from starting until
// resume, and returns those running now, for wait. Taking both under one
// lock leaves no invocation that starts unheld and is not waited for.
func (c *concurrency) hold(rs kernel.ResetScope) (running []uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held = append(c.held, rs)
	for id, s := range c.running {
		if rs.Covers(s) {
			running = append(running, id)
		}
	}
	return running
}

// resume lets invocations of every held scope start again.
func (c *concurrency) resume() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held = nil
}

// shutdown keeps every invocation from starting from now on, and returns
// those running, for wait.
func (c *concurrency) shutdown() (running []uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shut = true
	return slices.Collect(maps.Keys(c.running))
}

// wait returns once every invocation in pending has ended. It fails when
// ctx ends first.
func (c *concurrency) wait(ctx context.Context, pending []uint64) error {
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
			return fmt.Errorf("%d Lambda invocations were still running (%w)", len(pending), ctx.Err())
		}
	}
}

// errThrottled is the recorded 429 (error-matrix.md T10).
func errThrottled(reason string) *awserr.Error {
	e := lambdaapi.ErrTooManyRequestsException("Rate Exceeded.")
	e.Fields = append(e.Fields, awserr.Field{Name: "Reason", Value: reason})
	return user(e)
}

// errShuttingDown refuses an invocation once understudy shuts down.
func errShuttingDown() *awserr.Error {
	return errService("Lambda is shutting down and starts no execution environments; retry once understudy is running again.")
}

// errStateReplacing refuses an invocation while a reset or snapshot load
// replaces the state of its scope. It is understudy's own condition,
// answered as a failure of the emulator, which callers retry.
func errStateReplacing(scope aws.Scope) *awserr.Error {
	return errService("Lambda starts no invocation in account " + scope.Account.String() + ", region " + scope.Region.String() +
		" while a reset or snapshot load replaces its state; the function did not run. Retry once the reset or load has answered.")
}
