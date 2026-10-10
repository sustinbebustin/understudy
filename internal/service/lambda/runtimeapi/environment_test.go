package runtimeapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strconv"
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
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// wallAt is the fixed wall time the tests' clock reports, so deadlines are
// exact.
func wallAt() time.Time { return time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC) }

// newServer opens a server with the default init limit and the wall clock
// fixed at wallAt.
func newServer(t *testing.T) *runtimeapi.Server {
	t.Helper()
	return newServerAt(t, 0, wallAt)
}

func newServerAt(t *testing.T, initTimeout time.Duration, wall func() time.Time) *runtimeapi.Server {
	t.Helper()
	c, err := clock.New(clock.Options{Wall: wall})
	if err != nil {
		t.Fatal(err)
	}
	s, err := runtimeapi.NewServer(runtimeapi.Options{
		Clock:       c,
		Topology:    topology.LinuxBridge{Gateway: netip.MustParseAddr("127.0.0.1")},
		InitTimeout: initTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Server.Close: %v", err)
		}
	})
	return s
}

// openEnv opens an environment of newServer on a real loopback listener.
func openEnv(t *testing.T) (*runtimeapi.Environment, *simulator) {
	t.Helper()
	env, err := newServer(t).Open(t.Context())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return env, newSimulator(t, env.Addr().String(), &http.Transport{})
}

// openBubbleEnv opens an environment on an in-memory network, with the
// bubble's fake time as the wall clock, so the init limit, the init retry
// and timeouts run on fake time. Call it inside synctest.Test.
func openBubbleEnv(t *testing.T, initTimeout time.Duration) (*runtimeapi.Environment, *simulator) {
	t.Helper()
	s := newServerAt(t, initTimeout, time.Now)
	n := newMemNet()
	runtimeapi.SetListen(s, n.listen)
	env, err := s.Open(t.Context())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return env, newSimulator(t, env.Addr().String(), &http.Transport{DialContext: n.dial})
}

func functionARN() aws.ARN {
	return aws.ARN{Partition: "aws", Service: "lambda", Region: "eu-west-1", Account: "123456789012", Resource: "function:worker:7"}
}

var invocationSeq atomic.Int64

func invocation(timeout time.Duration, payload string) runtimeapi.Invocation {
	n := strconv.FormatInt(invocationSeq.Add(1), 10)
	return runtimeapi.Invocation{
		RequestID:    ids.LambdaRequestID("8476a536-e9f4-11e8-9739-2dfe598c" + strings.Repeat("0", 4-len(n)) + n),
		InvocationID: "inv-" + n,
		FunctionARN:  functionARN(),
		TraceID:      "Root=1-5bef4de7-ad49b0e87f6ef6c87fc2e700;Parent=9a9197af755a6419;Sampled=0",
		Payload:      []byte(payload),
		Timeout:      timeout,
	}
}

type result struct {
	outcome runtimeapi.Outcome
	err     error
}

func invokeAsync(ctx context.Context, env *runtimeapi.Environment, inv runtimeapi.Invocation) <-chan result {
	done := make(chan result, 1)
	go func() {
		o, err := env.Invoke(ctx, inv)
		done <- result{o, err}
	}()
	return done
}

func await(t *testing.T, done <-chan result) result {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("Invoke did not return within 10s")
		return result{}
	}
}

func TestNextHandsTheInvocationOverWithEveryRuntimeHeader(t *testing.T) {
	env, sim := openEnv(t)
	inv := invocation(time.Minute, `{"key":"value"}`)
	inv.ClientContext = `{"custom":{"k":"v"}}`
	inv.CognitoIdentity = `{"cognitoIdentityId":"id","cognitoIdentityPoolId":"pool"}`
	got := sim.serveOnce(t.Context(), func(event) string { return `"ok"` })
	r := await(t, invokeAsync(t.Context(), env, inv))
	if r.err != nil {
		t.Fatalf("Invoke: %v", r.err)
	}
	ev := receive(t, got)
	want := map[string]string{
		"Content-Type":                        "application/json",
		"Lambda-Runtime-Aws-Request-Id":       string(inv.RequestID),
		"Lambda-Runtime-Deadline-Ms":          strconv.FormatInt(wallAt().Add(time.Minute).UnixMilli(), 10),
		"Lambda-Runtime-Invoked-Function-Arn": "arn:aws:lambda:eu-west-1:123456789012:function:worker:7",
		"Lambda-Runtime-Trace-Id":             inv.TraceID,
		"Lambda-Runtime-Invocation-Id":        inv.InvocationID,
		"Lambda-Runtime-Client-Context":       inv.ClientContext,
		"Lambda-Runtime-Cognito-Identity":     inv.CognitoIdentity,
	}
	if ev.status != http.StatusOK {
		t.Errorf("/next status = %d, want 200 (aws-lambda-go exits on anything else)", ev.status)
	}
	for k, v := range want {
		if got := ev.header.Get(k); got != v {
			t.Errorf("/next %s = %q, want %q", k, got, v)
		}
	}
	if string(ev.body) != `{"key":"value"}` {
		t.Errorf("/next body = %q, want the payload", ev.body)
	}
	if s, ok := r.outcome.(runtimeapi.Success); !ok || string(s.Payload) != `"ok"` {
		t.Errorf("outcome = %#v, want Success with the posted payload", r.outcome)
	}
	if h := runtimeapi.FunctionErrorHeader(r.outcome); h != "" {
		t.Errorf("X-Amz-Function-Error for a success = %q, want none", h)
	}
}

func TestNextOmitsClientContextAndCognitoIdentityWhenUnset(t *testing.T) {
	env, sim := openEnv(t)
	got := sim.serveOnce(t.Context(), func(event) string { return "{}" })
	if r := await(t, invokeAsync(t.Context(), env, invocation(time.Minute, "{}"))); r.err != nil {
		t.Fatal(r.err)
	}
	ev := receive(t, got)
	for _, k := range []string{"Lambda-Runtime-Client-Context", "Lambda-Runtime-Cognito-Identity"} {
		if _, present := ev.header[k]; present {
			t.Errorf("/next sent %s = %q for an invocation without one", k, ev.header.Get(k))
		}
	}
}

// The Python runtime segfaults when /next has no Content-Type (containers.md
// finding 8): every hand-over carries application/json, whatever the
// payload, and a parseable deadline. Timeouts start at a minute so the real
// timer never fires before the simulator answers; the deadline is arithmetic
// on the fixed wall clock, so a long timeout costs nothing.
func TestNextAlwaysSendsJSONContentTypeAndAnIntegerDeadline(t *testing.T) {
	env, sim := openEnv(t)
	rapid.Check(t, func(rt *rapid.T) {
		payload := rapid.SliceOfN(rapid.Byte(), 0, 512).Draw(rt, "payload")
		timeout := time.Duration(rapid.IntRange(60_000, 900_000).Draw(rt, "timeoutMs")) * time.Millisecond
		inv := invocation(timeout, "")
		inv.Payload = payload
		got := sim.serveOnce(t.Context(), func(event) string { return "null" })
		r := await(t, invokeAsync(t.Context(), env, inv))
		if r.err != nil {
			rt.Fatal(r.err)
		}
		ev := receive(rt, got)
		if ct := ev.header.Get("Content-Type"); ct != "application/json" {
			rt.Fatalf("Content-Type = %q, want application/json", ct)
		}
		ms, err := strconv.ParseInt(ev.header.Get("Lambda-Runtime-Deadline-Ms"), 10, 64)
		if err != nil || ms != wallAt().Add(timeout).UnixMilli() {
			rt.Fatalf("Lambda-Runtime-Deadline-Ms = %q (%v), want wall time plus %s", ev.header.Get("Lambda-Runtime-Deadline-Ms"), err, timeout)
		}
		if !bytes.Equal(ev.body, payload) {
			rt.Fatalf("body = %q, want %q", ev.body, payload)
		}
	})
}

func TestErrorIsAnUnhandledFunctionErrorWithThePayloadUnmodified(t *testing.T) {
	cases := []struct {
		header, wantType string
	}{
		{"Runtime.NoSuchHandler", "Runtime.NoSuchHandler"},
		{"", "Runtime.Unknown"}, // aws-lambda-go and the Python RIC send none
		{"Unhandled", "Runtime.Unknown"},
		{"Function.bad", "Function.Unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.wantType+"/"+tc.header, func(t *testing.T) {
			env, sim := openEnv(t)
			done := invokeAsync(t.Context(), env, invocation(time.Minute, "{}"))
			ev, err := sim.next(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			body := `{"errorMessage": "kaboom", "errorType": "ValueError", "stackTrace": []}`
			if r := sim.fail(t.Context(), ev.requestID(), tc.header, body); r.status != http.StatusAccepted || r.body != `{"status":"OK"}` {
				t.Errorf("POST /error = %d %s, want 202 {\"status\":\"OK\"}", r.status, r.body)
			}
			r := await(t, done)
			fe, ok := r.outcome.(runtimeapi.FunctionError)
			if r.err != nil || !ok {
				t.Fatalf("Invoke = %#v, %v; want FunctionError", r.outcome, r.err)
			}
			if fe.Type != tc.wantType || string(fe.Response(ids.LambdaRequestID(ev.requestID()))) != body {
				t.Errorf("FunctionError = %q %s, want %q with the body unmodified", fe.Type, fe.Payload, tc.wantType)
			}
			if h := runtimeapi.FunctionErrorHeader(fe); h != "Unhandled" {
				t.Errorf("X-Amz-Function-Error = %q, want Unhandled", h)
			}
		})
	}
}

// aws-lambda-go streams /response chunked and reports a failure that
// happens mid-stream in the Lambda-Runtime-Function-Error-Type and
// -Error-Body trailers, the body base64-encoded (containers.md finding 8).
func TestResponseTrailerTurnsTheInvocationIntoAFunctionError(t *testing.T) {
	env, sim := openEnv(t)
	done := invokeAsync(t.Context(), env, invocation(time.Minute, "{}"))
	ev, err := sim.next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	errDoc := `{"errorMessage":"stream broke","errorType":"Runtime.TruncatedResponse"}`
	pr, pw := io.Pipe()
	trailer := http.Header{"Lambda-Runtime-Function-Error-Type": nil, "Lambda-Runtime-Function-Error-Body": nil}
	go func() {
		if _, err := pw.Write([]byte(`{"partial":`)); err != nil {
			pw.CloseWithError(err)
			return
		}
		trailer.Set("Lambda-Runtime-Function-Error-Type", "Runtime.TruncatedResponse")
		trailer.Set("Lambda-Runtime-Function-Error-Body", base64.StdEncoding.EncodeToString([]byte(errDoc)))
		_ = pw.Close()
	}()
	if r := sim.post(t.Context(), "/invocation/"+ev.requestID()+"/response", pr, http.Header{"Content-Type": {"application/octet-stream"}}, trailer); r.status != http.StatusAccepted {
		t.Fatalf("POST /response = %d %s", r.status, r.body)
	}
	r := await(t, done)
	fe, ok := r.outcome.(runtimeapi.FunctionError)
	if r.err != nil || !ok || fe.Type != "Runtime.TruncatedResponse" || string(fe.Payload) != errDoc {
		t.Fatalf("Invoke = %#v, %v; want FunctionError Runtime.TruncatedResponse with the decoded trailer body", r.outcome, r.err)
	}
}

func TestChunkedResponseWithoutAnErrorTrailerSucceeds(t *testing.T) {
	env, sim := openEnv(t)
	done := invokeAsync(t.Context(), env, invocation(time.Minute, "{}"))
	ev, err := sim.next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	trailer := http.Header{"Lambda-Runtime-Function-Error-Type": nil}
	if r := sim.post(t.Context(), "/invocation/"+ev.requestID()+"/response", strings.NewReader(`{"chunked":true}`), nil, trailer); r.status != http.StatusAccepted {
		t.Fatalf("POST /response = %d %s", r.status, r.body)
	}
	if r := await(t, done); r.err != nil || string(r.outcome.Response("")) != `{"chunked":true}` {
		t.Fatalf("Invoke = %#v, %v; want Success", r.outcome, r.err)
	}
}

func TestOversizedResponseIsResponseSizeTooLarge(t *testing.T) {
	env, sim := openEnv(t)
	done := invokeAsync(t.Context(), env, invocation(time.Minute, "{}"))
	ev, err := sim.next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("a", runtimeapi.MaxResponseBytes+1)
	if r := sim.respond(t.Context(), ev.requestID(), big); r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("POST /response of %d bytes = %d, want 413", len(big), r.status)
	}
	r := await(t, done)
	// The bytes AWS recorded (lambda-api.md 8.2): errorMessage first.
	want := `{"errorMessage":"Response payload size exceeded maximum allowed payload size (6291556 bytes).","errorType":"Function.ResponseSizeTooLarge"}`
	if r.err != nil || string(r.outcome.Response("")) != want || runtimeapi.FunctionErrorHeader(r.outcome) != "Unhandled" {
		t.Fatalf("Invoke = %s, %v; want %s", r.outcome.Response(""), r.err, want)
	}
}

func TestTimeoutReturnsSandboxTimedoutAndRefusesTheLateReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		env, sim := openBubbleEnv(t, 0)
		inv := invocation(150*time.Millisecond, "{}")
		start := time.Now()
		done := invokeAsync(t.Context(), env, inv)
		ev, err := sim.next(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		r := await(t, done)
		if _, ok := r.outcome.(runtimeapi.Timeout); r.err != nil || !ok {
			t.Fatalf("Invoke = %#v, %v; want Timeout", r.outcome, r.err)
		}
		if took := time.Since(start); took != inv.Timeout {
			t.Errorf("Invoke timed out after %s, want exactly the timeout %s", took, inv.Timeout)
		}
		want := `{"errorType":"Sandbox.Timedout","errorMessage":"RequestId: ` + string(inv.RequestID) + ` Error: Task timed out after 0.15 seconds"}`
		if got := string(r.outcome.Response(inv.RequestID)); got != want {
			t.Errorf("timeout payload = %s, want %s", got, want)
		}
		late := sim.respond(t.Context(), ev.requestID(), `"too late"`)
		if late.status != http.StatusBadRequest || !strings.Contains(late.body, `"errorType":"InvalidInvocationId"`) {
			t.Errorf("late /response = %d %s, want 400 InvalidInvocationId", late.status, late.body)
		}
		var unusable *runtimeapi.UnusableError
		if _, err := env.Invoke(t.Context(), invocation(time.Minute, "{}")); !errors.As(err, &unusable) || unusable.Reason != runtimeapi.TimedOut {
			t.Errorf("Invoke after a timeout = %v, want *UnusableError TimedOut (the next invoke must be cold)", err)
		}
	})
}

func TestContainerExitFailsTheRunningInvocation(t *testing.T) {
	inv := invocation(time.Minute, "{}")
	cases := []struct {
		exit runtimeapi.Outcome
		want string
	}{
		{runtimeapi.ExitError{Code: 3}, `{"errorType":"Runtime.ExitError","errorMessage":"RequestId: ` + string(inv.RequestID) + ` Error: Runtime exited with error: exit status 3"}`},
		{runtimeapi.ExitError{Code: 0}, `{"errorType":"Runtime.ExitError","errorMessage":"RequestId: ` + string(inv.RequestID) + ` Error: Runtime exited without providing a reason"}`},
		{runtimeapi.OOM{}, `{"errorType":"Runtime.OutOfMemory","errorMessage":"RequestId: ` + string(inv.RequestID) + ` Error: Runtime exited with error: signal: killed"}`},
	}
	for _, tc := range cases {
		t.Run(string(tc.exit.Response("x")), func(t *testing.T) {
			env, sim := openEnv(t)
			done := invokeAsync(t.Context(), env, inv)
			if _, err := sim.next(t.Context()); err != nil {
				t.Fatal(err)
			}
			env.Terminate(tc.exit)
			r := await(t, done)
			if r.err != nil || r.outcome != tc.exit {
				t.Fatalf("Invoke = %#v, %v; want %#v", r.outcome, r.err, tc.exit)
			}
			if got := string(r.outcome.Response(inv.RequestID)); got != tc.want {
				t.Errorf("payload = %s, want %s", got, tc.want)
			}
			if o, err := env.Invoke(t.Context(), invocation(time.Minute, "{}")); err != nil || o != tc.exit {
				t.Errorf("Invoke after the exit = %#v, %v; want the same outcome", o, err)
			}
		})
	}
}

func TestExitBeforeTheFirstNextFailsTheColdInvocation(t *testing.T) {
	env, _ := openEnv(t)
	done := invokeAsync(t.Context(), env, invocation(time.Minute, "{}"))
	mismatch := runtimeapi.ArchMismatch{Declared: "arm64", Host: "x86_64"}
	env.Terminate(mismatch)
	r := await(t, done)
	if r.err != nil || r.outcome != mismatch {
		t.Fatalf("Invoke = %#v, %v; want %#v", r.outcome, r.err, mismatch)
	}
	payload := string(r.outcome.Response("req"))
	for _, s := range []string{`"errorType":"Runtime.InvalidEntrypoint"`, "arm64", "x86_64", "exec format error"} {
		if !strings.Contains(payload, s) {
			t.Errorf("payload %s lacks %s", payload, s)
		}
	}
}

// exec format error on the host's own architecture means the code, not
// the platform, is built for another architecture: the payload must not
// tell the user to switch to the architecture they already declared.
func TestArchMismatchOnTheHostArchitectureBlamesTheBuild(t *testing.T) {
	payload := string(runtimeapi.ArchMismatch{Declared: "x86_64", Host: "x86_64"}.Response("req"))
	for _, s := range []string{`"errorType":"Runtime.InvalidEntrypoint"`, "exec format error", "rebuild it for x86_64", "GOARCH=amd64", "set Architectures to the architecture it is built for"} {
		if !strings.Contains(payload, s) {
			t.Errorf("payload %s lacks %q", payload, s)
		}
	}
	if strings.Contains(payload, "binfmt") {
		t.Errorf("payload %s suggests emulation for the host's own architecture", payload)
	}
}

func TestInitErrorFailsTheColdInvocationWithThePostedPayload(t *testing.T) {
	env, sim := openEnv(t)
	done := invokeAsync(t.Context(), env, invocation(time.Minute, "{}"))
	body := `{"errorMessage":"no bootstrap found at /var/task/bootstrap or /opt/bootstrap","errorType":"Runtime.InvalidEntrypoint"}`
	if r := sim.initFail(t.Context(), "Runtime.InvalidEntrypoint", body); r.status != http.StatusAccepted {
		t.Fatalf("POST /init/error = %d %s, want 202", r.status, r.body)
	}
	r := await(t, done)
	ie, ok := r.outcome.(runtimeapi.InitError)
	if r.err != nil || !ok || ie.Type != "Runtime.InvalidEntrypoint" || string(ie.Response("")) != body {
		t.Fatalf("Invoke = %#v, %v; want InitError with the posted payload", r.outcome, r.err)
	}
	if runtimeapi.FunctionErrorHeader(ie) != "Unhandled" {
		t.Error("an init failure must be an Unhandled function error")
	}
}

func TestInitErrorAfterTheFirstNextIsForbidden(t *testing.T) {
	env, sim := openEnv(t)
	got := sim.serveOnce(t.Context(), func(event) string { return "{}" })
	if r := await(t, invokeAsync(t.Context(), env, invocation(time.Minute, "{}"))); r.err != nil {
		t.Fatal(r.err)
	}
	receive(t, got)
	if r := sim.initFail(t.Context(), "Runtime.Late", "{}"); r.status != http.StatusForbidden {
		t.Errorf("POST /init/error after init = %d %s, want 403", r.status, r.body)
	}
}

// A runtime that misses the init limit gets Lambda's init retry: the cold
// invocation waits up to the function's timeout more for the first /next,
// and its handler gets what the retry left of that timeout (lambda-api.md
// 9.6).
func TestSlowInitIsRetriedUnderTheFunctionTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			initLimit = 100 * time.Millisecond
			initTook  = 300 * time.Millisecond
		)
		env, sim := openBubbleEnv(t, initLimit)
		inv := invocation(time.Minute, `{"slow":"init"}`)
		start := time.Now()
		done := invokeAsync(t.Context(), env, inv)
		time.Sleep(initTook)
		synctest.Wait()
		ev, err := sim.next(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		// The invocation's timeout has run since the retry began at the init
		// limit, so the handler gets the timeout minus what the retry
		// already used (initTook - initLimit).
		want := start.Add(initLimit + inv.Timeout).UnixMilli()
		if got := ev.header.Get("Lambda-Runtime-Deadline-Ms"); got != strconv.FormatInt(want, 10) {
			t.Errorf("Lambda-Runtime-Deadline-Ms = %s, want %d: the init limit plus the timeout from Invoke", got, want)
		}
		if r := sim.respond(t.Context(), ev.requestID(), `"warm at last"`); r.status != http.StatusAccepted {
			t.Fatalf("POST /response = %d %s", r.status, r.body)
		}
		if r := await(t, done); r.err != nil || string(r.outcome.Response("")) != `"warm at last"` {
			t.Fatalf("Invoke = %#v, %v; want Success after the init retry", r.outcome, r.err)
		}
	})
}

func TestRuntimeThatNeverPollsGetsTheReachabilityDiagnostic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		env, _ := openBubbleEnv(t, 100*time.Millisecond)
		start := time.Now()
		_, err := env.Invoke(t.Context(), invocation(200*time.Millisecond, "{}"))
		noPoll, ok := errors.AsType[*runtimeapi.NoPollError](err)
		if !ok || noPoll.Addr != env.Addr() {
			t.Fatalf("Invoke = %v, want *NoPollError naming %s", err, env.Addr())
		}
		if took := time.Since(start); noPoll.Waited != 300*time.Millisecond || took != noPoll.Waited {
			t.Errorf("NoPollError.Waited = %s after %s, want the init limit plus the timeout of the init retry, 300ms", noPoll.Waited, took)
		}
		msg := err.Error()
		for _, s := range []string{env.Addr().String(), "Linux bridge", "loopback", "firewall", "rootless"} {
			if !strings.Contains(msg, s) {
				t.Errorf("diagnostic %q does not mention %q", msg, s)
			}
		}
		var unusable *runtimeapi.UnusableError
		if _, err := env.Invoke(t.Context(), invocation(time.Minute, "{}")); !errors.As(err, &unusable) || unusable.Reason != runtimeapi.NeverPolled {
			t.Errorf("second Invoke = %v, want *UnusableError NeverPolled", err)
		}
	})
}

// A runtime that answers and then never asks for its next invocation would
// otherwise hold the next Invoke forever; the wait is bounded by that
// invocation's timeout and the environment is then unusable.
func TestRuntimeThatNeverRepollsFailsTheNextInvocation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		env, sim := openBubbleEnv(t, 0)
		got := sim.serveOnce(t.Context(), func(event) string { return "{}" })
		if r := await(t, invokeAsync(t.Context(), env, invocation(time.Minute, "{}"))); r.err != nil {
			t.Fatal(r.err)
		}
		receive(t, got)
		r := await(t, invokeAsync(t.Context(), env, invocation(150*time.Millisecond, "{}")))
		if !isUnusable(r.err, runtimeapi.NeverRepolled) {
			t.Fatalf("Invoke on a runtime that stopped polling = %#v, %v; want *UnusableError NeverRepolled", r.outcome, r.err)
		}
		if _, err := env.Invoke(t.Context(), invocation(time.Minute, "{}")); !isUnusable(err, runtimeapi.NeverRepolled) {
			t.Errorf("later Invoke = %v, want *UnusableError NeverRepolled", err)
		}
	})
}

func isUnusable(err error, reason runtimeapi.Spoilage) bool {
	unusable, ok := errors.AsType[*runtimeapi.UnusableError](err)
	return ok && unusable.Reason == reason
}

func TestWarmEnvironmentServesInvocationsOneAtATime(t *testing.T) {
	env, sim := openEnv(t)
	for i := range 3 {
		payload := `{"n":` + strconv.Itoa(i) + `}`
		got := sim.serveOnce(t.Context(), func(ev event) string { return string(ev.body) })
		r := await(t, invokeAsync(t.Context(), env, invocation(time.Minute, payload)))
		if r.err != nil || string(r.outcome.Response("")) != payload {
			t.Fatalf("invocation %d = %#v, %v", i, r.outcome, r.err)
		}
		receive(t, got)
	}
	done := invokeAsync(t.Context(), env, invocation(time.Minute, "{}"))
	ev, err := sim.next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var busy *runtimeapi.BusyError
	if _, err := env.Invoke(t.Context(), invocation(time.Minute, "{}")); !errors.As(err, &busy) {
		t.Errorf("concurrent Invoke = %v, want *BusyError", err)
	}
	sim.respond(t.Context(), ev.requestID(), "{}")
	if r := await(t, done); r.err != nil {
		t.Fatal(r.err)
	}
}

func TestReplyForAnotherInvocationIsRefused(t *testing.T) {
	env, sim := openEnv(t)
	inv := invocation(time.Minute, "{}")
	done := invokeAsync(t.Context(), env, inv)
	ev, err := sim.next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if r := sim.respond(t.Context(), "not-the-request", "{}"); r.status != http.StatusBadRequest || !strings.Contains(r.body, "InvalidInvocationId") {
		t.Errorf("/response for an unknown request = %d %s, want 400 InvalidInvocationId", r.status, r.body)
	}
	wrongEcho := sim.post(t.Context(), "/invocation/"+ev.requestID()+"/response", strings.NewReader("{}"),
		http.Header{"Lambda-Runtime-Invocation-Id": {"someone-else"}}, nil)
	if wrongEcho.status != http.StatusBadRequest || !strings.Contains(wrongEcho.body, "InvalidInvocationId") {
		t.Errorf("/response echoing another invocation id = %d %s, want 400 InvalidInvocationId", wrongEcho.status, wrongEcho.body)
	}
	rightEcho := sim.post(t.Context(), "/invocation/"+ev.requestID()+"/response", strings.NewReader(`"ok"`),
		http.Header{"Lambda-Runtime-Invocation-Id": {inv.InvocationID}}, nil)
	if rightEcho.status != http.StatusAccepted {
		t.Fatalf("/response echoing the invocation id = %d %s, want 202", rightEcho.status, rightEcho.body)
	}
	if r := await(t, done); r.err != nil || string(r.outcome.Response("")) != `"ok"` {
		t.Fatalf("Invoke = %#v, %v", r.outcome, r.err)
	}
}

func TestCancelledQueuedInvocationLeavesTheEnvironmentReusable(t *testing.T) {
	env, sim := openEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := invokeAsync(ctx, env, invocation(time.Minute, "{}"))
	cancel()
	if r := await(t, done); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("Invoke = %v, want context.Canceled", r.err)
	}
	got := sim.serveOnce(t.Context(), func(event) string { return `"later"` })
	if r := await(t, invokeAsync(t.Context(), env, invocation(time.Minute, "{}"))); r.err != nil {
		t.Fatalf("Invoke after a withdrawn one = %v", r.err)
	}
	receive(t, got)
}

func TestCancelledRunningInvocationSpoilsTheEnvironment(t *testing.T) {
	env, sim := openEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := invokeAsync(ctx, env, invocation(time.Minute, "{}"))
	if _, err := sim.next(t.Context()); err != nil {
		t.Fatal(err)
	}
	cancel()
	if r := await(t, done); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("Invoke = %v, want context.Canceled", r.err)
	}
	var unusable *runtimeapi.UnusableError
	if _, err := env.Invoke(t.Context(), invocation(time.Minute, "{}")); !errors.As(err, &unusable) || unusable.Reason != runtimeapi.Abandoned {
		t.Errorf("Invoke = %v, want *UnusableError Abandoned", err)
	}
}

func TestCloseEndsTheInvocationAndTheListener(t *testing.T) {
	env, sim := openEnv(t)
	done := invokeAsync(t.Context(), env, invocation(time.Minute, "{}"))
	if _, err := sim.next(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := env.Close(); err != nil {
		t.Fatal(err)
	}
	var closedErr *runtimeapi.ClosedError
	if r := await(t, done); !errors.As(r.err, &closedErr) {
		t.Errorf("Invoke after Close = %#v, %v; want *ClosedError", r.outcome, r.err)
	}
	if _, err := sim.next(t.Context()); err == nil {
		t.Error("GET /next after Close reached a listener; a late runtime must find nothing")
	}
}

// Shutdown can race a cold start: every Open either returns an environment
// that Close then closes, or a *ClosedError, and nothing is left listening.
func TestServerCloseRacingOpenClosesEveryEnvironment(t *testing.T) {
	s := newServer(t)
	const opens = 32
	envs := make(chan *runtimeapi.Environment, opens)
	var wg sync.WaitGroup
	for range opens {
		wg.Go(func() {
			e, err := s.Open(t.Context())
			var closedErr *runtimeapi.ClosedError
			if err != nil && !errors.As(err, &closedErr) {
				t.Errorf("Open racing Close = %v, want an environment or *ClosedError", err)
			}
			if e != nil {
				envs <- e
			}
		})
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close racing Open = %v", err)
	}
	wg.Wait()
	close(envs)
	for e := range envs {
		var closedErr *runtimeapi.ClosedError
		if _, err := e.Invoke(t.Context(), invocation(time.Minute, "{}")); !errors.As(err, &closedErr) {
			t.Errorf("environment %s survived Server.Close: Invoke = %v", e.Addr(), err)
		}
	}
}

func TestInvokeRefusesAnIncompleteInvocation(t *testing.T) {
	env, _ := openEnv(t)
	inv := invocation(0, "{}")
	var invalid *runtimeapi.InvocationError
	if _, err := env.Invoke(t.Context(), inv); !errors.As(err, &invalid) || invalid.Field != "Timeout" {
		t.Errorf("Invoke with no timeout = %v, want *InvocationError Timeout", err)
	}
}

func TestNewServerRefusesAnUnsupportedTopology(t *testing.T) {
	c, err := clock.New(clock.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtimeapi.NewServer(runtimeapi.Options{Clock: c, Topology: topology.RootlessDocker{HostAddr: netip.MustParseAddr("10.0.2.2")}})
	if unsupported, ok := errors.AsType[*topology.UnsupportedError](err); !ok || unsupported.Remedy == "" {
		t.Errorf("NewServer on rootless Docker = %v, want *topology.UnsupportedError", err)
	}
}
