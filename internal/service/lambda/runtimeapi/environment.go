package runtimeapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// Runtime API header names (lambda-api.md 9.1, containers.md 4.5).
const (
	headerRequestID       = "Lambda-Runtime-Aws-Request-Id"
	headerDeadline        = "Lambda-Runtime-Deadline-Ms"
	headerFunctionARN     = "Lambda-Runtime-Invoked-Function-Arn"
	headerTraceID         = "Lambda-Runtime-Trace-Id"
	headerClientContext   = "Lambda-Runtime-Client-Context"
	headerCognitoIdentity = "Lambda-Runtime-Cognito-Identity"
	headerInvocationID    = "Lambda-Runtime-Invocation-Id"
	headerErrorType       = "Lambda-Runtime-Function-Error-Type"
	trailerErrorBody      = "Lambda-Runtime-Function-Error-Body"
)

// call is one invocation queued on or running in an environment. budget is
// set at dispatch and outcome by the handler that completes it, both under
// the environment's lock.
type call struct {
	inv Invocation
	// budget is what is left of the invocation's timeout at dispatch: all
	// of it, or what the init retry left.
	budget  time.Duration
	outcome Outcome
}

// phase is where an environment's runtime is in its life.
//
//sumtype:decl
type phase interface{ isPhase() }

// starting: the runtime has not asked for an invocation yet.
type starting struct{}

// idle: the runtime is parked in /next, or about to be.
type idle struct{}

// queued: an invocation waits for the runtime's /next.
type queued struct{ call *call }

// retrying: a cold runtime missed the init limit, so the invocation waits
// for its first /next under the function's timeout, as Lambda retries init
// within the first invocation (lambda-api.md 9.6). The invocation's
// timeout has been running for elapsed, monotonic wall time.
type retrying struct {
	call    *call
	elapsed func() time.Duration
}

// running: the runtime holds an invocation and owes /response or /error.
type running struct{ call *call }

// failed: initialization failed or the container ended; every invocation
// gets outcome.
type failed struct{ outcome Outcome }

// spoiled: the runtime is alive but cannot be trusted with an invocation.
type spoiled struct{ reason Spoilage }

// closed: the listener is closed.
type closed struct{}

func (starting) isPhase() {}
func (idle) isPhase()     {}
func (queued) isPhase()   {}
func (retrying) isPhase() {}
func (running) isPhase()  {}
func (failed) isPhase()   {}
func (spoiled) isPhase()  {}
func (closed) isPhase()   {}

// Environment is the Runtime API of one execution environment: one
// listener, one runtime, at most one invocation in flight.
type Environment struct {
	srv    *Server
	addr   netip.AddrPort
	http   *http.Server
	served chan struct{}

	mu      sync.Mutex
	phase   phase
	polled  bool          // the runtime has called /next at least once
	changed chan struct{} // closed and replaced on every change
}

func newEnvironment(s *Server, addr netip.AddrPort) *Environment {
	return &Environment{srv: s, addr: addr, served: make(chan struct{}), phase: starting{}, changed: make(chan struct{})}
}

// Addr is the address the container must dial: AWS_LAMBDA_RUNTIME_API.
func (e *Environment) Addr() netip.AddrPort { return e.addr }

// Invoke hands inv to the runtime and waits for its outcome. On a cold
// environment it first waits for the runtime's first /next: up to the init
// limit, then, as Lambda retries init within the first invocation, up to
// the function's timeout more, which the invocation then shares. On a warm
// environment it waits up to the timeout for a runtime still between
// invocations. A function error, timeout, crash or init failure is an
// Outcome; an error means the invocation could not be run (see
// InvocationError, BusyError, ClosedError, UnusableError, NoPollError, or
// the context's error). After a Timeout, an UnusableError or NoPollError,
// or a context that ends while the runtime runs the invocation, the
// environment takes no further invocations; after a container exit or an
// init failure, every later Invoke gets the same outcome. An
// InvocationError, a BusyError, or a context that ends before the runtime
// takes the invocation leaves it usable.
func (e *Environment) Invoke(ctx context.Context, inv Invocation) (Outcome, error) {
	if err := inv.Validate(); err != nil {
		return nil, err
	}
	c := &call{inv: inv}
	e.mu.Lock()
	switch p := e.phase.(type) {
	case starting, idle:
	case queued, retrying, running:
		e.mu.Unlock()
		return nil, &BusyError{Addr: e.addr}
	case failed:
		e.mu.Unlock()
		return p.outcome, nil
	case spoiled:
		e.mu.Unlock()
		return nil, &UnusableError{Addr: e.addr, Reason: p.reason}
	case closed:
		e.mu.Unlock()
		return nil, &ClosedError{Addr: e.addr}
	}
	cold := !e.polled
	e.setPhase(queued{call: c})
	e.mu.Unlock()
	if ended, o, err := e.awaitDispatch(ctx, c, cold); ended {
		return o, err
	}
	return e.awaitResult(ctx, c)
}

// Terminate records that the environment's container ended, with the
// failing outcome its exit maps to (ExitError, OOM or ArchMismatch). An
// invocation queued or running gets that outcome, and so does every later
// Invoke. An environment already spoiled or closed keeps its state.
func (e *Environment) Terminate(o Outcome) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch e.phase.(type) {
	case starting, idle, queued, retrying, running:
		e.setPhase(failed{outcome: o})
	case failed, spoiled, closed:
	}
}

// Close removes the environment from its server and closes its listener,
// so a late request from its runtime finds nothing. Close it before the
// container is deleted. Invocations in flight end with a ClosedError.
func (e *Environment) Close() error {
	e.mu.Lock()
	if _, done := e.phase.(closed); done {
		e.mu.Unlock()
		return nil
	}
	e.setPhase(closed{})
	e.mu.Unlock()
	e.srv.forget(e)
	err := e.http.Close()
	<-e.served
	if err != nil {
		return fmt.Errorf("runtime API environment %s: close listener: %w", e.addr, err)
	}
	return nil
}

// awaitDispatch waits until the runtime takes c from /next. ended reports
// that c finished without running (init failure, exit, closed, a runtime
// that never asked for it, or the caller's context), with its outcome or
// error.
//
// The wait is bounded on monotonic timers: a cold environment by the init
// limit, then by the function's timeout for the init retry; a warm one by
// the invocation's timeout, since its runtime answered the last invocation
// and owes the next /next.
func (e *Environment) awaitDispatch(ctx context.Context, c *call, cold bool) (ended bool, o Outcome, err error) {
	limit := c.inv.Timeout
	if cold {
		limit = e.srv.initTimeout
	}
	for {
		end, o, err := e.waitDispatch(ctx, c, limit)
		switch end {
		case waitDispatched:
			return false, nil, nil
		case waitEnded:
			return true, o, err
		case waitExpired:
		}
		if err := e.expire(c); err != nil {
			return true, nil, err
		}
		// The init retry began, or c moved on and the next wait returns at
		// once.
		limit = c.inv.Timeout
	}
}

// waitEnd is how one bounded wait for the runtime's /next ended.
type waitEnd uint8

const (
	// waitDispatched: the runtime took the invocation.
	waitDispatched waitEnd = iota + 1
	// waitEnded: the invocation ended without running, with an outcome or
	// error.
	waitEnded
	// waitExpired: the bound passed with the invocation still waiting.
	waitExpired
)

// waitDispatch waits up to limit for the runtime to take c.
func (e *Environment) waitDispatch(ctx context.Context, c *call, limit time.Duration) (waitEnd, Outcome, error) {
	waitCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	for {
		e.mu.Lock()
		if !e.awaitsDispatch(c) {
			if r, ok := e.phase.(running); ok && r.call == c {
				e.mu.Unlock()
				return waitDispatched, nil, nil
			}
			o, err := e.settled(c)
			e.mu.Unlock()
			return waitEnded, o, err
		}
		changed := e.changed
		e.mu.Unlock()
		select {
		case <-changed:
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				o, err := e.abandon(ctx, c)
				return waitEnded, o, err
			}
			return waitExpired, nil, nil
		}
	}
}

// awaitsDispatch reports whether c still waits for the runtime's /next.
// Callers hold e.mu.
func (e *Environment) awaitsDispatch(c *call) bool {
	switch p := e.phase.(type) {
	case queued:
		return p.call == c
	case retrying:
		return p.call == c
	case starting, idle, running, failed, spoiled, closed:
	}
	return false
}

// expire acts on c's wait for /next running out. A cold runtime that
// missed the init limit gets the init retry. One that misses the retry too
// never polled: NoPollError. A warm runtime that answered its last
// invocation but has not asked for the next within this one's timeout is
// wedged: UnusableError NeverRepolled. Otherwise the wait goes on.
func (e *Environment) expire(c *call) error {
	e.mu.Lock()
	switch p := e.phase.(type) {
	case queued:
		if p.call != c {
			break
		}
		if e.polled {
			e.setPhase(spoiled{reason: NeverRepolled})
			e.mu.Unlock()
			return &UnusableError{Addr: e.addr, Reason: NeverRepolled}
		}
		e.setPhase(retrying{call: c, elapsed: e.srv.clock.Stopwatch()})
		e.mu.Unlock()
		e.srv.log.Warn("Lambda runtime missed the init limit; waiting up to the function timeout more, as Lambda retries init",
			slog.String("addr", e.addr.String()),
			slog.String("request_id", string(c.inv.RequestID)),
			slog.Duration("init_limit", e.srv.initTimeout),
			slog.Duration("timeout", c.inv.Timeout))
		return nil
	case retrying:
		if p.call != c {
			break
		}
		e.setPhase(spoiled{reason: NeverPolled})
		e.mu.Unlock()
		return &NoPollError{Addr: e.addr, Bind: e.srv.bind, Topology: e.srv.topology, Waited: e.srv.initTimeout + c.inv.Timeout}
	case starting, idle, running, failed, spoiled, closed:
	}
	e.mu.Unlock()
	return nil
}

// awaitResult waits for the running invocation c to finish. Its timeout
// budget runs on a monotonic timer from dispatch; the emulator clock never
// touches it (lambda-runtime-contract.md 3).
func (e *Environment) awaitResult(ctx context.Context, c *call) (Outcome, error) {
	e.mu.Lock()
	budget := c.budget
	e.mu.Unlock()
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for {
		e.mu.Lock()
		if r, ok := e.phase.(running); !ok || r.call != c {
			o, err := e.settled(c)
			e.mu.Unlock()
			return o, err
		}
		changed := e.changed
		e.mu.Unlock()
		select {
		case <-changed:
		case <-runCtx.Done():
			if ctx.Err() != nil {
				return e.abandon(ctx, c)
			}
			return e.timedOut(c)
		}
	}
}

// timedOut ends a running invocation whose timeout passed. A reply that
// raced the timer wins.
func (e *Environment) timedOut(c *call) (Outcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.phase.(running); ok && r.call == c {
		e.setPhase(spoiled{reason: TimedOut})
		return Timeout{After: c.inv.Timeout}, nil
	}
	return e.settled(c)
}

// abandon withdraws c after the caller's context ended. A queued
// invocation is withdrawn cleanly; a running one spoils the environment.
func (e *Environment) abandon(ctx context.Context, c *call) (Outcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch p := e.phase.(type) {
	case queued:
		if p.call == c {
			if e.polled {
				e.setPhase(idle{})
			} else {
				e.setPhase(starting{})
			}
		}
	case retrying:
		if p.call == c {
			e.setPhase(starting{})
		}
	case running:
		if p.call == c {
			e.setPhase(spoiled{reason: Abandoned})
		}
	case starting, idle, failed, spoiled, closed:
	}
	if c.outcome != nil {
		return c.outcome, nil
	}
	return nil, fmt.Errorf("runtime API environment %s: invocation %s: %w", e.addr, c.inv.RequestID, ctx.Err())
}

// settled reports how c ended once its phase moved on. Callers hold e.mu.
func (e *Environment) settled(c *call) (Outcome, error) {
	if c.outcome != nil {
		return c.outcome, nil
	}
	switch p := e.phase.(type) {
	case failed:
		return p.outcome, nil
	case spoiled:
		return nil, &UnusableError{Addr: e.addr, Reason: p.reason}
	case closed:
		return nil, &ClosedError{Addr: e.addr}
	case starting, idle, queued, retrying, running:
	}
	return nil, fmt.Errorf("runtime API environment %s: invocation %s left in phase %T without an outcome", e.addr, c.inv.RequestID, e.phase)
}

// setPhase changes the phase and wakes every waiter. Callers hold e.mu.
func (e *Environment) setPhase(p phase) {
	e.phase = p
	close(e.changed)
	e.changed = make(chan struct{})
}

func (e *Environment) serve(ln net.Listener) {
	defer close(e.served)
	if err := e.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		e.srv.log.Warn("runtime API listener stopped", slog.String("addr", e.addr.String()), slog.String("error", err.Error()))
	}
}

func (e *Environment) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /2018-06-01/runtime/invocation/next", e.next)
	mux.HandleFunc("POST /2018-06-01/runtime/invocation/{id}/response", e.response)
	mux.HandleFunc("POST /2018-06-01/runtime/invocation/{id}/error", e.invocationError)
	mux.HandleFunc("POST /2018-06-01/runtime/init/error", e.initError)
	return mux
}

// next blocks until an invocation is queued, then hands it over. The
// Content-Type is always set: the Python runtime crashes without it
// (containers.md finding 8).
func (e *Environment) next(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	if !e.polled {
		e.polled = true
		if _, ok := e.phase.(starting); ok {
			e.setPhase(idle{})
		}
	}
	for {
		switch p := e.phase.(type) {
		case queued:
			e.dispatch(w, p.call, p.call.inv.Timeout)
			return
		case retrying:
			e.dispatch(w, p.call, p.call.inv.Timeout-p.elapsed())
			return
		case failed, spoiled, closed:
			e.mu.Unlock()
			writeError(w, http.StatusGone, "EnvironmentGone", "this execution environment takes no more invocations; exit so it can be replaced")
			return
		case starting, idle, running:
		}
		changed := e.changed
		e.mu.Unlock()
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		}
		e.mu.Lock()
	}
}

// dispatch hands c to the runtime with budget left of its timeout: all of
// it, or what the init retry left. Lambda-Runtime-Deadline-Ms is wall time
// plus budget. Callers hold e.mu; dispatch releases it.
func (e *Environment) dispatch(w http.ResponseWriter, c *call, budget time.Duration) {
	c.budget = budget
	e.setPhase(running{call: c})
	e.mu.Unlock()
	writeInvocation(w, c.inv, e.srv.clock.Wall().Add(budget))
}

func writeInvocation(w http.ResponseWriter, inv Invocation, deadline time.Time) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set(headerRequestID, string(inv.RequestID))
	h.Set(headerDeadline, strconv.FormatInt(deadline.UnixMilli(), 10))
	h.Set(headerFunctionARN, inv.FunctionARN.String())
	h.Set(headerTraceID, inv.TraceID)
	h.Set(headerInvocationID, inv.InvocationID)
	if inv.ClientContext != "" {
		h.Set(headerClientContext, inv.ClientContext)
	}
	if inv.CognitoIdentity != "" {
		h.Set(headerCognitoIdentity, inv.CognitoIdentity)
	}
	h.Set("Content-Length", strconv.Itoa(len(inv.Payload)))
	w.WriteHeader(http.StatusOK)
	// A failed write means the runtime went away; the timeout or the
	// container's exit ends the invocation.
	_, _ = w.Write(inv.Payload) //nolint:errcheck // see above
}

// response completes the running invocation with the posted payload, or
// with a function error when the runtime failed mid-stream and said so in
// the Lambda-Runtime-Function-Error-Type trailer (aws-lambda-go does).
func (e *Environment) response(w http.ResponseWriter, r *http.Request) {
	body, tooLarge, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "InvalidRequestBody", "could not read the response body: "+err.Error())
		return
	}
	var o Outcome = Success{Payload: body}
	switch {
	case tooLarge:
		o = FunctionError{Type: "Function.ResponseSizeTooLarge", Payload: messageFirstErrorPayload("Function.ResponseSizeTooLarge",
			fmt.Sprintf("Response payload size exceeded maximum allowed payload size (%d bytes).", MaxResponseBytes))}
	case r.Trailer.Get(headerErrorType) != "":
		o = FunctionError{Type: normalizeErrorType(r.Trailer.Get(headerErrorType)), Payload: trailerBody(r, body)}
	}
	if !e.complete(w, r, o) {
		return
	}
	if tooLarge {
		writeError(w, http.StatusRequestEntityTooLarge, "RequestEntityTooLarge",
			fmt.Sprintf("Exceeded maximum allowed payload size (%d bytes).", MaxResponseBytes))
		return
	}
	writeAccepted(w)
}

// invocationError completes the running invocation as a function error.
// Every /error is Unhandled, whatever the runtime's error type header says
// (containers.md 4.5).
func (e *Environment) invocationError(w http.ResponseWriter, r *http.Request) {
	body, _, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "InvalidRequestBody", "could not read the error body: "+err.Error())
		return
	}
	errorType := r.Header.Get(headerErrorType)
	if errorType == "" {
		errorType = r.Trailer.Get(headerErrorType)
	}
	if e.complete(w, r, FunctionError{Type: normalizeErrorType(errorType), Payload: body}) {
		writeAccepted(w)
	}
}

// complete hands o to the running invocation named by the request path. A
// reply for any other invocation, including one that timed out, gets 400
// InvalidInvocationId, and so does an echoed Lambda-Runtime-Invocation-Id
// that does not match.
func (e *Environment) complete(w http.ResponseWriter, r *http.Request, o Outcome) bool {
	id := r.PathValue("id")
	echoed := r.Header.Get(headerInvocationID)
	e.mu.Lock()
	run, ok := e.phase.(running)
	if !ok || string(run.call.inv.RequestID) != id || echoed != "" && echoed != run.call.inv.InvocationID {
		e.mu.Unlock()
		writeError(w, http.StatusBadRequest, "InvalidInvocationId",
			fmt.Sprintf("invocation %s is not in progress in this execution environment; it finished, timed out or never existed", id))
		return false
	}
	run.call.outcome = o
	e.setPhase(idle{})
	e.mu.Unlock()
	return true
}

// initError records an initialization failure. It is accepted only before
// the runtime's first /next.
func (e *Environment) initError(w http.ResponseWriter, r *http.Request) {
	body, _, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "InvalidRequestBody", "could not read the error body: "+err.Error())
		return
	}
	o := InitError{Type: normalizeErrorType(r.Header.Get(headerErrorType)), Payload: body}
	e.mu.Lock()
	if e.polled {
		e.mu.Unlock()
		writeError(w, http.StatusForbidden, "InvalidStateTransition", "initialization already finished: the runtime asked for an invocation")
		return
	}
	switch e.phase.(type) {
	case starting, queued, retrying:
		e.setPhase(failed{outcome: o})
	case idle, running, failed, spoiled, closed:
	}
	e.mu.Unlock()
	writeAccepted(w)
}

// readBody reads up to MaxResponseBytes; tooLarge reports a longer body,
// whose rest is left unread. A body read to its end exposes its trailers.
func readBody(r *http.Request) (body []byte, tooLarge bool, err error) {
	body, err = io.ReadAll(io.LimitReader(r.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(body) > MaxResponseBytes {
		return nil, true, nil
	}
	return body, false, nil
}

// trailerBody is the error document from the Lambda-Runtime-Function-
// Error-Body trailer, which aws-lambda-go sends base64-encoded; the
// streamed body stands in when the trailer is absent or not base64.
func trailerBody(r *http.Request, body []byte) []byte {
	if raw := r.Trailer.Get(trailerErrorBody); raw != "" {
		if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
			return decoded
		}
	}
	return body
}

func writeAccepted(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(w, `{"status":"OK"}`) //nolint:errcheck // the runtime went away; nothing to do
}

// writeError answers with the Runtime API's ErrorResponse document.
func writeError(w http.ResponseWriter, status int, errorType, message string) {
	raw, _ := json.Marshal(struct { //nolint:errcheck,errchkjson // a struct of two strings always encodes
		ErrorMessage string `json:"errorMessage"`
		ErrorType    string `json:"errorType"`
	}{message, errorType})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw) //nolint:errcheck // the runtime went away; nothing to do
}
