//go:build docker

package pool_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/config/configtest"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// shellBootstrap is a provided.al2023 custom runtime written against the
// Runtime API with curl: it echoes events, fails on "fail", exits 3 on
// "exit", and counts invocations so warm reuse is visible.
const shellBootstrap = `#!/bin/sh
set -eu
API="http://${AWS_LAMBDA_RUNTIME_API}/2018-06-01/runtime"
COUNT=0
while true; do
  HEADERS=$(mktemp)
  EVENT=$(curl -sS -D "$HEADERS" "$API/invocation/next")
  ID=$(grep -i '^Lambda-Runtime-Aws-Request-Id:' "$HEADERS" | tr -d '\r' | cut -d' ' -f2)
  COUNT=$((COUNT + 1))
  case "$EVENT" in
    *exit*) exit 3 ;;
    *fail*) curl -sS -X POST "$API/invocation/$ID/error" -H 'Lambda-Runtime-Function-Error-Type: Function.Boom' -d '{"errorMessage":"boom","errorType":"Boom"}' >/dev/null ;;
    *) curl -sS -X POST "$API/invocation/$ID/response" -d "{\"count\":$COUNT,\"event\":$EVENT}" >/dev/null ;;
  esac
done
`

// pythonHandler exercises the Python runtime interface client.
const pythonHandler = `import time

calls = 0


def handler(event, context):
    global calls
    action = event.get("action")
    calls += 1
    if action == "remaining":
        return {"remaining": context.get_remaining_time_in_millis()}
    if action == "raise":
        raise ValueError("kaboom")
    if action == "sleep":
        time.sleep(event["seconds"])
    if action == "oom":
        hog = b"x" * (512 << 20)
        return len(hog)
    return {"count": calls, "function": context.function_name, "memory": context.memory_limit_in_mb}
`

type stack struct {
	client   *docker.Client
	clock    *clock.Clock
	server   *runtimeapi.Server
	launcher *launcher.Launcher
	pool     *pool.Pool
	store    *codestore.Store
	topology topology.Topology
	owner    string
	// stopRun ends the pool's Run, and with it the exit watch; it returns
	// Run's error and may be called more than once.
	stopRun func() error
}

func runID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func dockerClient(t *testing.T) *docker.Client {
	t.Helper()
	return configtest.EngineClient(t)
}

// newStack wires the real runtime API, launcher and pool for one test, as
// one understudy instance of a unique owner; every container it starts
// carries understudy.dev/test=1 and is removed when the test ends.
func newStack(t *testing.T, client *docker.Client, owner string) *stack {
	t.Helper()
	ctx := t.Context()
	top, err := new(topology.Detector).Detect(ctx, client, topology.Options{})
	if err != nil {
		t.Fatalf("detect topology: %v", err)
	}
	c, err := clock.New(clock.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := runtimeapi.NewServer(runtimeapi.Options{Clock: c, Topology: top})
	if err != nil {
		t.Fatal(err)
	}
	l, err := launcher.New(client, launcher.Options{
		Ownership:    docker.Ownership{Instance: "t18" + runID(t), Owner: owner},
		Test:         true,
		Topology:     top,
		EndpointPort: 4566,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := pool.New(pool.Options{Launcher: l, Runtime: pooltest.ServerRuntime(srv), Clock: c})
	if err != nil {
		t.Fatal(err)
	}
	store, err := codestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	stopRun := sync.OnceValue(func() error {
		cancel()
		return <-done
	})
	t.Cleanup(func() {
		ctx := context.Background()
		if err := p.Drain(ctx, pool.All{}, pool.Shutdown); err != nil {
			t.Errorf("drain: %v", err)
		}
		if err := stopRun(); err != nil {
			t.Errorf("Run: %v", err)
		}
		if err := srv.Close(); err != nil {
			t.Errorf("close runtime API: %v", err)
		}
		removeOwned(t, client, owner)
		if err := store.Close(); err != nil {
			t.Errorf("close code store: %v", err)
		}
	})
	return &stack{client: client, clock: c, server: srv, launcher: l, pool: p, store: store, topology: top, owner: owner, stopRun: stopRun}
}

// removeOwned deletes every container of the test's owner, whatever the
// test left: containers are removed by their unique owner label, never by
// the shared test label.
func removeOwned(t *testing.T, client *docker.Client, owner string) {
	t.Helper()
	ctx := context.Background()
	left, err := client.ContainerList(ctx, map[string]string{docker.LabelOwner: owner, docker.LabelTest: "1"})
	if err != nil {
		t.Errorf("list leftover containers: %v", err)
		return
	}
	for _, c := range left {
		if err := client.ContainerRemove(ctx, c.ID, docker.RemoveOptions{Force: true, Volumes: true}); err != nil && !docker.IsNotFound(err) {
			t.Errorf("remove leftover container %s: %v", c.ID, err)
		}
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o644)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// function stores code and returns the spec of a function running it.
func (s *stack) function(t *testing.T, name, runtime, handler string, files map[string]string) launcher.Spec {
	t.Helper()
	code, err := s.store.PutZip(t.Context(), bytes.NewReader(zipOf(t, files)), codestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	img, ok := launcher.ImageForRuntime(runtime)
	if !ok {
		t.Fatalf("no image for %s", runtime)
	}
	present, err := s.launcher.EnsureImage(t.Context(), img.Ref(), launcher.X86, nil)
	if err != nil {
		t.Fatalf("EnsureImage %s: %v", img.Ref(), err)
	}
	account, err := aws.ParseAccountID("123456789012")
	if err != nil {
		t.Fatal(err)
	}
	region, err := aws.KnownRegions().Parse("eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	return launcher.Spec{
		Function:           launcher.Function{Name: name, Version: "$LATEST", Account: account, Region: region},
		Generation:         1,
		Image:              present.Ref,
		Architecture:       launcher.X86,
		Code:               launcher.ZipCode{Dir: code.Dir, Handler: handler},
		MemoryMB:           128,
		EphemeralStorageMB: 512,
		Credentials:        launcher.Credentials{AccessKeyID: "ASIAQAAAAAAAAEXAMPLE", SecretAccessKey: "test", SessionToken: "test"},
		LogGroup:           "/aws/lambda/" + name,
	}
}

func (s *stack) shellFunction(t *testing.T) launcher.Spec {
	t.Helper()
	// The zip carries bootstrap as 0644: the code store makes it executable,
	// as a zip built on Windows needs.
	return s.function(t, "shell", "provided.al2023", "bootstrap", map[string]string{"bootstrap": shellBootstrap})
}

func (s *stack) pythonFunction(t *testing.T) launcher.Spec {
	t.Helper()
	return s.function(t, "py", "python3.13", "handler.handler", map[string]string{"handler.py": pythonHandler})
}

func newInvocation(t *testing.T, spec launcher.Spec, payload string, timeout time.Duration) runtimeapi.Invocation {
	t.Helper()
	id := runID(t) + runID(t)
	return runtimeapi.Invocation{
		RequestID:    ids.LambdaRequestID(id[:8] + "-" + id[8:12] + "-4" + id[13:16] + "-8" + id[17:20] + "-" + id + id[:8]),
		InvocationID: "inv-" + id,
		FunctionARN:  spec.Function.ARN(),
		TraceID:      "Root=1-6706a3c0-" + id + id[:12] + ";Parent=" + id[:16] + ";Sampled=0",
		Payload:      []byte(payload),
		Timeout:      timeout,
	}
}

// invoke runs one invocation through the pool and releases the
// environment with its outcome.
func (s *stack) invoke(t *testing.T, spec launcher.Spec, payload string, timeout time.Duration) (*pool.Env, runtimeapi.Outcome) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	env, err := s.pool.Acquire(ctx, spec)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	o, err := env.Invoke(ctx, newInvocation(t, spec, payload, timeout))
	if relErr := s.pool.Release(ctx, env, o); relErr != nil {
		t.Errorf("Release: %v", relErr)
	}
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	return env, o
}

func decode(t *testing.T, o runtimeapi.Outcome) map[string]any {
	t.Helper()
	success, ok := o.(runtimeapi.Success)
	if !ok {
		t.Fatalf("outcome = %#v (%s), want Success", o, o.Response("req"))
	}
	var m map[string]any
	if err := json.Unmarshal(success.Payload, &m); err != nil {
		t.Fatalf("payload %q: %v", success.Payload, err)
	}
	return m
}

func containerGone(t *testing.T, client *docker.Client, id string) bool {
	t.Helper()
	_, err := client.ContainerInspect(context.Background(), id)
	return docker.IsNotFound(err)
}

func TestDockerProvidedShellBootstrapReturnsAPayloadAndStaysWarm(t *testing.T) {
	s := newStack(t, dockerClient(t), "t18-owner-"+runID(t))
	spec := s.shellFunction(t)
	first, o := s.invoke(t, spec, `{"n":1}`, 10*time.Second)
	got := decode(t, o)
	if event, ok := got["event"].(map[string]any); !ok || got["count"] != 1.0 || event["n"] != 1.0 {
		t.Errorf("first payload = %v, want count 1 and the event echoed", got)
	}
	second, o := s.invoke(t, spec, `{"n":2}`, 10*time.Second)
	if got := decode(t, o); got["count"] != 2.0 {
		t.Errorf("second payload = %v, want count 2 from the same runtime process (no new init)", got)
	}
	if first != second {
		t.Error("second invocation started a new environment; want warm reuse")
	}
}

func TestDockerPythonHandlerRunsWarm(t *testing.T) {
	s := newStack(t, dockerClient(t), "t18-owner-"+runID(t))
	spec := s.pythonFunction(t)
	first, o := s.invoke(t, spec, `{}`, 10*time.Second)
	got := decode(t, o)
	if got["count"] != 1.0 || got["function"] != "py" || got["memory"] != "128" {
		t.Errorf("payload = %v, want count 1, function py, memory 128", got)
	}
	second, o := s.invoke(t, spec, `{}`, 10*time.Second)
	if got := decode(t, o); got["count"] != 2.0 {
		t.Errorf("second payload = %v, want count 2: the module was not re-imported", got)
	}
	if first != second {
		t.Error("second invocation was cold")
	}
}

func TestDockerUnhandledFunctionError(t *testing.T) {
	s := newStack(t, dockerClient(t), "t18-owner-"+runID(t))
	spec := s.pythonFunction(t)
	env, o := s.invoke(t, spec, `{"action":"raise"}`, 10*time.Second)
	fe, ok := o.(runtimeapi.FunctionError)
	if !ok {
		t.Fatalf("outcome = %#v, want FunctionError", o)
	}
	if runtimeapi.FunctionErrorHeader(fe) != "Unhandled" || !strings.Contains(string(fe.Payload), `"errorType": "ValueError"`) || !strings.Contains(string(fe.Payload), "kaboom") {
		t.Errorf("function error = %s, want an Unhandled ValueError payload", fe.Payload)
	}
	if _, idle := env.State().(pool.Idle); !idle {
		t.Errorf("environment after a handled exception = %#v, want warm", env.State())
	}
}

// A custom runtime's /error keeps its Lambda-Runtime-Function-Error-Type
// and its body as posted, and the environment stays warm.
func TestDockerProvidedRuntimeErrorKeepsItsType(t *testing.T) {
	s := newStack(t, dockerClient(t), "t18-owner-"+runID(t))
	spec := s.shellFunction(t)
	env, o := s.invoke(t, spec, `"fail"`, 10*time.Second)
	want := runtimeapi.FunctionError{Type: "Function.Boom", Payload: []byte(`{"errorMessage":"boom","errorType":"Boom"}`)}
	fe, ok := o.(runtimeapi.FunctionError)
	if !ok || fe.Type != want.Type || !bytes.Equal(fe.Payload, want.Payload) {
		t.Fatalf("outcome = %#v (%s), want FunctionError %s with the posted body unmodified", o, o.Response("rid"), want.Type)
	}
	if h := runtimeapi.FunctionErrorHeader(fe); h != "Unhandled" {
		t.Errorf("X-Amz-Function-Error = %q, want Unhandled", h)
	}
	if _, idle := env.State().(pool.Idle); !idle {
		t.Errorf("environment after a function error = %#v, want warm", env.State())
	}
}

func TestDockerRuntimeExitError(t *testing.T) {
	s := newStack(t, dockerClient(t), "t18-owner-"+runID(t))
	spec := s.shellFunction(t)
	env, o := s.invoke(t, spec, `"exit"`, 10*time.Second)
	if o != (runtimeapi.ExitError{Code: 3}) {
		t.Fatalf("outcome = %#v, want ExitError 3", o)
	}
	if got := string(o.Response("rid")); got != `{"errorType":"Runtime.ExitError","errorMessage":"RequestId: rid Error: Runtime exited with error: exit status 3"}` {
		t.Errorf("payload = %s", got)
	}
	if !containerGone(t, s.client, env.Container().ID) {
		t.Error("the exited container was not removed")
	}
	next, o := s.invoke(t, spec, `{"n":1}`, 10*time.Second)
	if got := decode(t, o); next == env || got["count"] != 1.0 {
		t.Errorf("invocation after the exit = %v in the same environment %v; want a cold start", got, next == env)
	}
}

func TestDockerTimeoutKillsTheContainerAndTheNextInvokeIsCold(t *testing.T) {
	s := newStack(t, dockerClient(t), "t18-owner-"+runID(t))
	spec := s.pythonFunction(t)
	s.invoke(t, spec, `{}`, 10*time.Second) // warm up, so the timeout covers only the handler
	env, o := s.invoke(t, spec, `{"action":"sleep","seconds":30}`, time.Second)
	if o != (runtimeapi.Timeout{After: time.Second}) {
		t.Fatalf("outcome = %#v, want Timeout after 1s", o)
	}
	if !strings.Contains(string(o.Response("rid")), "Task timed out after 1.00 seconds") {
		t.Errorf("payload = %s", o.Response("rid"))
	}
	if !containerGone(t, s.client, env.Container().ID) {
		t.Error("the timed-out container still exists; a timeout must kill it")
	}
	next, o := s.invoke(t, spec, `{}`, 10*time.Second)
	if got := decode(t, o); next == env || got["count"] != 1.0 {
		t.Errorf("invocation after the timeout = %v; want a cold start with a fresh module", got)
	}
}

func TestDockerOutOfMemoryAt128MB(t *testing.T) {
	s := newStack(t, dockerClient(t), "t18-owner-"+runID(t))
	spec := s.pythonFunction(t)
	env, o := s.invoke(t, spec, `{"action":"oom"}`, 30*time.Second)
	if o != (runtimeapi.OOM{}) {
		t.Fatalf("outcome = %#v (%s), want OOM", o, o.Response("rid"))
	}
	if !strings.Contains(string(o.Response("rid")), "Runtime.OutOfMemory") {
		t.Errorf("payload = %s", o.Response("rid"))
	}
	if !containerGone(t, s.client, env.Container().ID) {
		t.Error("the OOM-killed container was not removed")
	}
}

// /tmp is disk, as on Lambda: a 128 MB function can write 200 MB there
// without its pages counting against the memory limit.
func TestDockerTmpIsNotChargedToMemory(t *testing.T) {
	s := newStack(t, dockerClient(t), "t18-owner-"+runID(t))
	spec := s.function(t, "tmp", "python3.13", "handler.handler", map[string]string{"handler.py": `
import os


def handler(event, context):
    chunk = b"x" * (1 << 20)
    with open("/tmp/big", "wb") as f:
        for _ in range(200):
            f.write(chunk)
    return {"size": os.path.getsize("/tmp/big")}
`})
	_, o := s.invoke(t, spec, `{}`, 60*time.Second)
	if got := decode(t, o); got["size"] != float64(200<<20) {
		t.Errorf("payload = %v, want 200 MiB written to /tmp", got)
	}
}

func TestDockerConcurrentInvocationsUseTwoEnvironments(t *testing.T) {
	s := newStack(t, dockerClient(t), "t18-owner-"+runID(t))
	spec := s.pythonFunction(t)
	var wg sync.WaitGroup
	envs := make(chan *pool.Env, 2)
	for range 2 {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			env, err := s.pool.Acquire(ctx, spec)
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			o, err := env.Invoke(ctx, newInvocation(t, spec, `{"action":"sleep","seconds":1}`, 20*time.Second))
			if err != nil {
				t.Errorf("Invoke: %v", err)
			}
			if _, ok := o.(runtimeapi.Success); !ok {
				t.Errorf("outcome = %#v", o)
			}
			if err := s.pool.Release(ctx, env, o); err != nil {
				t.Errorf("Release: %v", err)
			}
			envs <- env
		})
	}
	wg.Wait()
	close(envs)
	a, b := <-envs, <-envs
	if a == nil || b == nil || a == b || a.Container().ID == b.Container().ID {
		t.Fatal("two concurrent invocations shared one environment")
	}
	if s.pool.Len() != 2 {
		t.Errorf("Len = %d, want 2 warm environments", s.pool.Len())
	}
}

// After a crash of understudy (simulated by stopping its exit watch, so
// nothing of it reacts any more, then SIGKILLing its function container),
// the next instance of the same owner sweeps the leftover.
func TestDockerCrashRecoverySweepsLeftovers(t *testing.T) {
	client := dockerClient(t)
	owner := "t18-owner-" + runID(t)
	crashed := newStack(t, client, owner)
	spec := crashed.shellFunction(t)
	env, _ := crashed.invoke(t, spec, `{}`, 10*time.Second)
	id := env.Container().ID
	if err := crashed.stopRun(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := client.ContainerKill(t.Context(), id, "KILL"); err != nil {
		t.Fatalf("kill -9: %v", err)
	}
	if containerGone(t, client, id) {
		t.Fatal("a killed container should linger until swept")
	}
	next, err := launcher.New(client, launcher.Options{
		Ownership: docker.Ownership{Instance: "t18" + runID(t), Owner: owner}, Test: true, Topology: crashed.topology, EndpointPort: 4566,
	})
	if err != nil {
		t.Fatal(err)
	}
	swept, err := next.Sweep(t.Context())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	found := false
	for _, s := range swept.Containers {
		found = found || s == id
	}
	if !found || !containerGone(t, client, id) {
		t.Errorf("Sweep removed %v, want the crashed instance's %s", swept.Containers, id)
	}
}

// G4: the deadline a function sees is wall time, so freezing and advancing
// the emulator clock does not change get_remaining_time_in_millis().
func TestDockerDeadlineIgnoresTheEmulatorClock(t *testing.T) {
	s := newStack(t, dockerClient(t), "t18-owner-"+runID(t))
	spec := s.pythonFunction(t)
	s.clock.Freeze()
	if _, err := s.clock.Advance(time.Hour); err != nil {
		t.Fatal(err)
	}
	const timeout = 3 * time.Second
	for i := range 2 {
		_, o := s.invoke(t, spec, `{"action":"remaining"}`, timeout)
		remaining, ok := decode(t, o)["remaining"].(float64)
		if !ok {
			t.Fatalf("payload = %s", o.Response("rid"))
		}
		if d := timeout - time.Duration(remaining)*time.Millisecond; d < 0 || d > 100*time.Millisecond {
			t.Errorf("invocation %d: get_remaining_time_in_millis() = %v, want within 100 ms of %s", i, remaining, timeout)
		}
	}
}

// The code tree is mounted read-only: a function cannot change its code.
func TestDockerCodeIsReadOnly(t *testing.T) {
	s := newStack(t, dockerClient(t), "t18-owner-"+runID(t))
	spec := s.function(t, "ro", "python3.13", "handler.handler", map[string]string{"handler.py": `
def handler(event, context):
    try:
        open("/var/task/handler.py", "a").write("#")
        return {"wrote": True}
    except OSError as e:
        return {"wrote": False, "errno": e.errno}
`})
	_, o := s.invoke(t, spec, `{}`, 10*time.Second)
	if got := decode(t, o); got["wrote"] != false {
		t.Errorf("payload = %v, want the write to /var/task refused", got)
	}
	code, ok := spec.Code.(launcher.ZipCode)
	if !ok {
		t.Fatal("not zip code")
	}
	info, err := os.Stat(code.Dir + "/handler.py")
	if err != nil || info.Mode().Perm() != fs.FileMode(0o644) {
		t.Errorf("host file = %v, %v; want it untouched at 0644", info, err)
	}
}
