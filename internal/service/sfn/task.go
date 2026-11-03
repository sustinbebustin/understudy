package sfn

import (
	"context"
	"log/slog"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerInvoke invokes the Lambda function of a Task in flight: the engine
// asked for it, or a snapshot load found it waiting (the StartExecution
// coverage note). The key's ID is "<execution ARN>/<TaskID>". Arming it in
// the step's write means a committed step always gets its invocation.
const TimerInvoke kernel.TimerKind = "sfn.invoke"

// invocations are the Lambda tasks whose function is running, by taskKey,
// so a TimerInvoke that a Rearm armed again does not invoke one twice. Each
// claim has its own token, so an invocation that ends after a reset or a
// load dropped its claim never frees a newer one. mu is a leaf lock
// (ARCHITECTURE.md 8, lock hierarchy).
type invocations struct {
	mu     sync.Mutex
	tokens uint64
	keys   map[string]invocationClaim
}

type invocationClaim struct {
	token uint64
	scope aws.Scope
}

// claim records key as running in scope; false when it already is.
func (r *invocations) claim(key string, scope aws.Scope) (token uint64, free bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, running := r.keys[key]; running {
		return 0, false
	}
	if r.keys == nil {
		r.keys = map[string]invocationClaim{}
	}
	r.tokens++
	r.keys[key] = invocationClaim{token: r.tokens, scope: scope}
	return r.tokens, true
}

// release ends the claim token on key, if it still holds it.
func (r *invocations) release(key string, token uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys[key].token == token {
		delete(r.keys, key)
	}
}

// drop forgets the claims whose scope covers reports true: their
// executions are gone, and one started again under the same name invokes
// its own task.
func (r *invocations) drop(covers func(aws.Scope) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	maps.DeleteFunc(r.keys, func(_ string, c invocationClaim) bool { return covers(c.scope) })
}

// taskKey is the TimerInvoke ID of task of the execution exec.
func taskKey(exec executionRef, task asl.TaskID) string {
	return exec.arn().String() + "/" + strconv.FormatInt(int64(task), 10)
}

// onInvoke starts the invocation a TimerInvoke names, as tracked work
// parented to the timer's call (spec "Runtime decisions"; ADR 0007), and
// applies its result to the execution. A task no longer in flight, because
// it timed out or its execution ended or is gone, is not invoked.
func (s *Service) onInvoke(c *kernel.Call, key clock.TimerKey, _ clock.Instant) {
	exec, n, ok := timerOf(key.ID)
	if !ok {
		s.k.Log().LogAttrs(context.Background(), slog.LevelError, "a Step Functions invoke timer has a malformed ID and was dropped; this is a bug in understudy",
			slog.String("timer", key.ID))
		return
	}
	cell, ok := s.cells.Lookup(exec.machine.scope)
	if !ok {
		return
	}
	type pending struct {
		invoke asl.InvokeLambda
		role   string
		ok     bool
	}
	got := store.Read(cell, func(st *State) pending {
		rec, ok := st.Executions[exec.key()]
		if !ok {
			return pending{}
		}
		for _, inv := range rec.Engine.InFlight() {
			if inv.Task == asl.TaskID(n) {
				return pending{invoke: inv, role: rec.Run.RoleArn, ok: true}
			}
		}
		return pending{}
	})
	if !got.ok {
		return
	}
	id := taskKey(exec, asl.TaskID(n))
	claim, free := s.running.claim(id, exec.machine.scope)
	if !free {
		// Its function is still running: a Rearm after a reset or a clock
		// restart re-arms every task in flight.
		return
	}
	// Drawn here, in timer order, not in the goroutine, so a seeded run
	// gives the same ids whatever order concurrent invocations run in
	// (ADR 0018).
	requestID := string(s.ids.RequestID())
	s.k.Go(kernel.InvocationRunning, func(ctx context.Context) {
		defer s.running.release(id, claim)
		answer := s.invokeTask(ctx, c, exec, got.role, got.invoke, requestID)
		if ctx.Err() != nil {
			// The kernel is stopping: the invocation was cut short, not
			// answered. The task stays in flight, so the final checkpoint
			// saves it and a load invokes it again.
			return
		}
		if _, ok := s.cells.Lookup(exec.machine.scope); !ok {
			// A reset dropped the execution while the function ran.
			return
		}
		_, err := kernel.Write(c, s.cells, exec.machine.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
			return struct{}{}, s.advance(tx, st, exec, answer(now.Wire()), now)
		})
		if err != nil {
			s.k.Log().LogAttrs(ctx, slog.LevelError, "a Step Functions execution could not take its Lambda task's result and stays RUNNING; start a new execution after fixing the error",
				slog.String("execution", exec.arn().String()), slog.String("error", err.Message))
		}
	})
}

// invokeTask invokes inv's function synchronously as the execution role
// (spec "Runtime decisions") and returns the engine event for its result,
// given the instant the result is applied at. A Lambda service error is
// named by switch sfn.integration.lambda-error; a function error by the
// payload's errorType with the payload as cause (stepfunctions-asl.md 8.1).
func (s *Service) invokeTask(ctx context.Context, c *kernel.Call, exec executionRef, role string, inv asl.InvokeLambda, requestID string) func(at time.Time) asl.Event {
	failed := func(e asl.StateError) func(time.Time) asl.Event {
		return func(at time.Time) asl.Event { return asl.LambdaFailed{At: at, Task: inv.Task, Err: e} }
	}
	roleARN, perr := aws.ParseARN(role)
	if perr != nil {
		// CreateStateMachine checked the role ARN's syntax, so this is a bug.
		return failed(LambdaTaskError(LambdaError().Value, inv.Resource, awserr.Internalf(perr), requestID))
	}
	in := &lambdaapi.InvokeInput{FunctionName: inv.Function, InvocationType: new(lambdaapi.InvocationTypeRequestResponse)}
	if inv.Payload != nil {
		in.Payload = []byte(docText(inv.Payload))
	}
	target := kernel.Target{Scope: functionScope(inv.Function, exec.machine.scope), As: kernel.AsExecutionRole{Role: roleARN}, Via: ref.SFNTask}
	out, err := kernel.Invoke(ctx, s.k, c, lambdaapi.OpInvoke, target, in)
	switch {
	case err != nil:
		return failed(LambdaTaskError(LambdaError().Value, inv.Resource, err, requestID))
	case out.FunctionError != nil:
		return failed(functionError(out.Payload))
	}
	payload := payloadValue(out.Payload)
	return func(at time.Time) asl.Event {
		result := payload
		if inv.Resource == asl.LambdaInvoke {
			result = invokeResult(out, payload, requestID, at)
		}
		return asl.LambdaSucceeded{At: at, Task: inv.Task, Output: result}
	}
}

// functionScope is the account and region of the function fn names: a
// function ARN's own, a partial ARN's account in the execution's region,
// and the execution's scope for a bare name.
func functionScope(fn string, home aws.Scope) aws.Scope {
	if strings.HasPrefix(fn, "arn:") {
		if a, err := aws.ParseARN(fn); err == nil {
			if sc, err := aws.ScopeOf(a); err == nil {
				return sc
			}
		}
		return home
	}
	if account, rest, ok := strings.Cut(fn, ":"); ok && strings.HasPrefix(rest, "function:") {
		if id, err := aws.ParseAccountID(account); err == nil {
			home.Account = id
		}
	}
	return home
}

// functionError is the task error of a function that failed: its payload's
// errorType, and the payload as cause (stepfunctions-asl.md 8.1). A payload
// without an errorType is Lambda.Unknown, the documented name for an
// unhandled error (stepfunctions-api.md 9.4).
func functionError(payload []byte) asl.StateError {
	name := "Lambda.Unknown"
	if t, ok := at(payloadValue(payload), "errorType").(document.String); ok && t != "" {
		name = string(t)
	}
	return asl.StateError{Name: name, Cause: string(payload)}
}

// payloadValue is a function's response payload as a value: its JSON, null
// when empty, and the text as a string when it is not JSON.
func payloadValue(b []byte) document.Value {
	if len(b) == 0 {
		return document.Null{}
	}
	d := jsonx.NewDecoder(b)
	v, err := d.ReadValue()
	if err == nil {
		err = d.End()
	}
	if err != nil {
		return document.String(string(b))
	}
	return v
}

// invokeResult is lambda:invoke's task result (stepfunctions-api.md 9.2;
// aws-recorded localstack@8b9a79f
// v2/services/test_lambda_task_service.snapshot.json:L67-L114): the
// InvokeOutput values, and SdkHttpMetadata with exactly the recorded header
// names, in the recorded orders. Content-Type, Connection and
// x-amzn-Remapped-Content-Length are the recorded constants; the request id
// fills x-amzn-RequestId and SdkResponseMetadata (API 9.4), Date is the
// emulator instant, and the X-Amzn-Trace-Id value is understudy's
// (StartExecution coverage note).
func invokeResult(out *lambdaapi.InvokeOutput, payload document.Value, requestID string, at time.Time) document.Map {
	version := "$LATEST"
	if out.ExecutedVersion != nil {
		version = *out.ExecutedVersion
	}
	headers := map[string]string{
		"X-Amz-Executed-Version":         version,
		"x-amzn-Remapped-Content-Length": "0",
		"Connection":                     "keep-alive",
		"x-amzn-RequestId":               requestID,
		"Content-Length":                 strconv.Itoa(len(out.Payload)),
		"Date":                           at.UTC().Format(http.TimeFormat),
		"X-Amzn-Trace-Id":                traceID(requestID, at),
		"Content-Type":                   "application/json",
	}
	all := make(document.Map, 0, len(headers))
	for _, k := range []string{"X-Amz-Executed-Version", "x-amzn-Remapped-Content-Length", "Connection", "x-amzn-RequestId", "Content-Length", "Date", "X-Amzn-Trace-Id", "Content-Type"} {
		all = append(all, document.Entry{Key: k, Value: document.List{document.String(headers[k])}})
	}
	single := make(document.Map, 0, len(headers))
	for _, k := range []string{"Connection", "Content-Length", "Content-Type", "Date", "X-Amz-Executed-Version", "x-amzn-Remapped-Content-Length", "x-amzn-RequestId", "X-Amzn-Trace-Id"} {
		single = append(single, document.Entry{Key: k, Value: document.String(headers[k])})
	}
	status := document.Number(strconv.Itoa(int(out.StatusCode)))
	return document.Map{
		{Key: "ExecutedVersion", Value: document.String(version)},
		{Key: "Payload", Value: payload},
		{Key: "SdkHttpMetadata", Value: document.Map{
			{Key: "AllHttpHeaders", Value: all},
			{Key: "HttpHeaders", Value: single},
			{Key: "HttpStatusCode", Value: status},
		}},
		{Key: "SdkResponseMetadata", Value: document.Map{{Key: "RequestId", Value: document.String(requestID)}}},
		{Key: "StatusCode", Value: status},
	}
}

// traceID is understudy's X-Amzn-Trace-Id value, in X-Ray's root form:
// the instant's epoch seconds and 24 hex digits of the request id.
func traceID(requestID string, at time.Time) string {
	digits := strings.ReplaceAll(requestID, "-", "")
	if len(digits) > 24 {
		digits = digits[:24]
	}
	return "Root=1-" + strconv.FormatInt(at.Unix(), 16) + "-" + digits
}

// at reads the member key of v; nil when v is not an object or lacks it.
func at(v document.Value, key string) document.Value {
	m, ok := v.(document.Map)
	if !ok {
		return nil
	}
	for _, e := range m {
		if e.Key == key {
			return e.Value
		}
	}
	return nil
}
