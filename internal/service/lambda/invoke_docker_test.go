//go:build docker

package lambda_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/config/configtest"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// shellRuntime is a provided.al2023 custom runtime written against the
// Runtime API with curl: it prints each event, fails on "fail", and
// answers with an invocation count, so warm reuse is visible, and with its
// VERSION, so a hot reload is.
const shellRuntime = `#!/bin/sh
set -eu
API="http://${AWS_LAMBDA_RUNTIME_API}/2018-06-01/runtime"
COUNT=0
while true; do
  HEADERS=$(mktemp)
  EVENT=$(curl -sS -D "$HEADERS" "$API/invocation/next")
  ID=$(grep -i '^Lambda-Runtime-Aws-Request-Id:' "$HEADERS" | tr -d '\r' | cut -d' ' -f2)
  COUNT=$((COUNT + 1))
  echo "handling $EVENT"
  case "$EVENT" in
    *fail*) curl -sS -X POST "$API/invocation/$ID/error" -d '{"errorMessage":"boom","errorType":"Boom"}' >/dev/null ;;
    *) curl -sS -X POST "$API/invocation/$ID/response" -d "{\"count\":$COUNT,\"version\":VERSION}" >/dev/null ;;
  esac
done
`

// pythonFunction exercises the Python runtime interface client.
const pythonFunction = `import time

calls = 0


def handler(event, context):
    global calls
    action = event.get("action")
    calls += 1
    if action == "raise":
        raise ValueError("kaboom")
    if action == "sleep":
        time.sleep(event["seconds"])
    if action == "remaining":
        return {"remaining": context.get_remaining_time_in_millis()}
    return {"count": calls}
`

// dockerStack is the whole Lambda path on a real Docker daemon: a running
// kernel with the Lambda service, its Execution over the real runtime API,
// launcher and pool, and stub CloudWatch Logs handlers.
type dockerStack struct {
	k     *kernel.Kernel
	clock *clock.Clock
	x     *lambda.Execution
	logs  *logSink
	scope aws.Scope
	// top is the detected topology the launcher and the runtime API
	// were built for.
	top topology.Topology
}

func randomID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// stackServices are services a dockerStack serves beside Lambda: their
// models, and bind, which binds them with the deps of each model before
// the kernel runs.
type stackServices struct {
	apis []*registry.Service
	bind func(t *testing.T, deps func(api *registry.Service) kernel.ServiceDeps)
}

func newDockerStack(t *testing.T, extra ...stackServices) *dockerStack {
	t.Helper()
	ctx := t.Context()
	client := configtest.EngineClient(t)
	top, err := new(topology.Detector).Detect(ctx, client, topology.Options{})
	if err != nil {
		t.Fatalf("detect topology: %v", err)
	}

	timers := kernel.NewTimers()
	clk, err := clock.New(clock.Options{Timers: timers.Scheduler()})
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.New(journal.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	apis := []*registry.Service{lambdaapi.Service(), logsapi.Service()}
	for _, e := range extra {
		apis = append(apis, e.apis...)
	}
	reg, err := registry.New(apis...)
	if err != nil {
		t.Fatal(err)
	}
	src := ids.NewSeeded(1)
	idStore := identity.New(src, aws.DefaultAccount())
	link := &linkTo{}
	k, err := kernel.New(kernel.Options{Registry: reg, Clock: clk, Timers: timers, IDs: src, Journal: j, Identity: idStore, Linker: link, Strict: true})
	if err != nil {
		t.Fatal(err)
	}

	owner := "t38-owner-" + randomID(t)
	l, err := launcher.New(client, launcher.Options{
		Ownership:    docker.Ownership{Instance: "t38" + randomID(t), Owner: owner},
		Test:         true,
		Topology:     top,
		EndpointPort: 4566,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := runtimeapi.NewServer(runtimeapi.Options{Clock: clk, Topology: top})
	if err != nil {
		t.Fatal(err)
	}
	p, err := pool.New(pool.Options{Launcher: l, Runtime: pooltest.ServerRuntime(srv), Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	code, err := codestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := blob.NewMemory("", 0)
	if err != nil {
		t.Fatal(err)
	}
	deps := kernel.ServiceDeps{Kernel: k, Binder: k.Binder(lambdaapi.Service()), Clock: clk, IDs: src, Blob: blobs, Identity: idStore, Docker: client, Log: k.Log()}
	svc := lambda.New(deps, lambda.Options{Images: l})
	x, err := lambda.NewExecution(svc, deps, lambda.ExecutionOptions{
		Pool: p, Docker: client.Endpoint().String(), Images: l, Code: code, Logs: lambda.ContainerLogs(l),
	})
	if err != nil {
		t.Fatal(err)
	}
	link.set(x)
	logs := &logSink{}
	logs.stub(k)
	for _, e := range extra {
		e.bind(t, func(api *registry.Service) kernel.ServiceDeps {
			return kernel.ServiceDeps{Kernel: k, Binder: k.Binder(api), Clock: clk, IDs: src, Blob: blobs, Identity: idStore, Docker: client, Log: k.Log()}
		})
	}
	if err := k.BindErr(); err != nil {
		t.Fatal(err)
	}

	runCtx, stop := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := k.Run(runCtx); err != nil {
			t.Errorf("kernel Run: %v", err)
		}
	})
	wg.Go(func() {
		if err := p.Run(runCtx); err != nil {
			t.Errorf("pool Run: %v", err)
		}
	})
	t.Cleanup(func() {
		ctx := context.Background()
		if err := x.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
		stop()
		wg.Wait()
		if err := srv.Close(); err != nil {
			t.Errorf("close runtime API: %v", err)
		}
		removeOwnedContainers(t, client, owner)
		if err := code.Close(); err != nil {
			t.Error(err)
		}
		if err := blobs.Close(); err != nil {
			t.Error(err)
		}
	})
	return &dockerStack{
		k: k, clock: clk, x: x, logs: logs,
		scope: aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()},
		top:   top,
	}
}

// removeOwnedContainers deletes whatever the test's owner left running.
func removeOwnedContainers(t *testing.T, client *docker.Client, owner string) {
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

// dispatch runs op as a client request through the kernel.
func dispatch[In, Out any](t *testing.T, s *dockerStack, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	c := s.k.NewCall(kernel.ExternalCall{
		RequestID: s.k.IDs().RequestID(),
		Operation: op.Descriptor().ID,
		Scope:     s.scope,
		Principal: identity.Root{Account: s.scope.Account},
		Origin:    ref.External{Protocol: ref.RESTJSON1, Listener: ref.ListenerMain},
		Admitted:  s.clock.Now(),
		Endpoint:  kernel.Endpoint{Scheme: "http", Host: "localhost:4566"},
	})
	switch r := s.k.Dispatch(ctx, c, kernel.Ok{In: in}).(type) {
	case kernel.Output:
		out, ok := r.Out.(*Out)
		if !ok {
			t.Fatalf("%s returned %T", op.Descriptor().ID, r.Out)
		}
		return out, nil
	case kernel.Failure:
		return nil, r.Err
	case kernel.StatusOKFailure:
		return nil, r.Err
	}
	t.Fatalf("%s returned no result", op.Descriptor().ID)
	return nil, nil
}

func mustDispatch[In, Out any](t *testing.T, s *dockerStack, op kernel.Op[In, Out], in *In) *Out {
	t.Helper()
	out, err := dispatch(t, s, op, in)
	if err != nil {
		t.Fatalf("%s: %s %s", op.Descriptor().ID, err.ID.Shape.Name(), err.Message)
	}
	return out
}

func zipFiles(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
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

// create creates a function of runtime with the files as its package.
func (s *dockerStack) create(t *testing.T, name, runtime, handler string, timeout int32, files map[string]string) {
	t.Helper()
	mustDispatch(t, s, lambdaapi.OpCreateFunction, &lambdaapi.CreateFunctionInput{
		FunctionName: name,
		Role:         role,
		Runtime:      new(lambdaapi.Runtime(runtime)),
		Handler:      new(handler),
		Code:         lambdaapi.FunctionCode{ZipFile: zipFiles(t, files)},
		MemorySize:   new(int32(128)),
		Timeout:      new(timeout),
		PackageType:  new(lambdaapi.PackageTypeZip),
	})
}

func (s *dockerStack) invoke(t *testing.T, name, payload string) *lambdaapi.InvokeOutput {
	t.Helper()
	return mustDispatch(t, s, lambdaapi.OpInvoke, &lambdaapi.InvokeInput{FunctionName: name, Payload: []byte(payload)})
}

// result decodes a successful invocation's JSON payload.
func result(t *testing.T, out *lambdaapi.InvokeOutput) map[string]any {
	t.Helper()
	if out.FunctionError != nil {
		t.Fatalf("function error %s: %s", *out.FunctionError, out.Payload)
	}
	var m map[string]any
	if err := json.Unmarshal(out.Payload, &m); err != nil {
		t.Fatalf("payload %q: %v", out.Payload, err)
	}
	return m
}

func shellFiles(version int) map[string]string {
	return map[string]string{"bootstrap": strings.ReplaceAll(shellRuntime, "VERSION", strconv.Itoa(version))}
}

func TestDockerProvidedRuntimeRunsWarmAndReportsUnhandledErrors(t *testing.T) {
	s := newDockerStack(t)
	s.create(t, "shell", "provided.al2023", "bootstrap", 10, shellFiles(1))
	if got := result(t, s.invoke(t, "shell", `{"n":1}`)); got["count"] != 1.0 {
		t.Errorf("first payload = %v, want count 1", got)
	}
	if got := result(t, s.invoke(t, "shell", `{"n":2}`)); got["count"] != 2.0 {
		t.Errorf("second payload = %v, want count 2 from the warm runtime", got)
	}
	out := s.invoke(t, "shell", `"fail"`)
	if out.FunctionError == nil || *out.FunctionError != "Unhandled" || string(out.Payload) != `{"errorMessage":"boom","errorType":"Boom"}` {
		t.Errorf("failing invocation = %v %s, want X-Amz-Function-Error Unhandled with the posted error", out.FunctionError, out.Payload)
	}
	if out.ExecutedVersion == nil || *out.ExecutedVersion != "$LATEST" {
		t.Errorf("ExecutedVersion = %v, want $LATEST", out.ExecutedVersion)
	}
}

func TestDockerPythonRunsWarmAndReportsUnhandledErrors(t *testing.T) {
	s := newDockerStack(t)
	s.create(t, "py", "python3.13", "handler.handler", 10, map[string]string{"handler.py": pythonFunction})
	if got := result(t, s.invoke(t, "py", `{}`)); got["count"] != 1.0 {
		t.Errorf("first payload = %v, want count 1", got)
	}
	if got := result(t, s.invoke(t, "py", `{}`)); got["count"] != 2.0 {
		t.Errorf("second payload = %v, want count 2: the module stays imported", got)
	}
	out := s.invoke(t, "py", `{"action":"raise"}`)
	if out.FunctionError == nil || *out.FunctionError != "Unhandled" || !strings.Contains(string(out.Payload), "kaboom") {
		t.Errorf("raising invocation = %v %s, want Unhandled with the ValueError", out.FunctionError, out.Payload)
	}
}

func TestDockerTimeoutThenColdStart(t *testing.T) {
	s := newDockerStack(t)
	s.create(t, "py", "python3.13", "handler.handler", 1, map[string]string{"handler.py": pythonFunction})
	s.invoke(t, "py", `{}`)
	out := s.invoke(t, "py", `{"action":"sleep","seconds":30}`)
	if out.FunctionError == nil || !strings.Contains(string(out.Payload), "Task timed out after 1.00 seconds") {
		t.Fatalf("sleeping invocation = %v %s, want Sandbox.Timedout after 1.00 seconds", out.FunctionError, out.Payload)
	}
	if got := result(t, s.invoke(t, "py", `{}`)); got["count"] != 1.0 {
		t.Errorf("invocation after the timeout = %v, want count 1 from a cold start", got)
	}
}

func TestDockerLogsArriveWithTheSynthesizedLinesInOrder(t *testing.T) {
	s := newDockerStack(t)
	s.create(t, "shell", "provided.al2023", "bootstrap", 10, shellFiles(1))
	s.invoke(t, "shell", `{"n":1}`)
	if _, err := s.k.Settle(t.Context(), time.Minute); err != nil {
		t.Fatal(err)
	}
	got := s.logs.messages()
	var kinds []string
	for _, m := range got {
		switch {
		case strings.HasPrefix(m, "INIT_START "), strings.HasPrefix(m, "START "), strings.HasPrefix(m, "END "), strings.HasPrefix(m, "REPORT "):
			kinds = append(kinds, strings.Fields(m)[0])
		case strings.HasPrefix(m, "handling "):
			kinds = append(kinds, "output")
		}
	}
	if want := []string{"INIT_START", "START", "output", "END", "REPORT"}; strings.Join(kinds, " ") != strings.Join(want, " ") {
		t.Errorf("log lines %q, want %v in order", got, want)
	}
}

func TestDockerHotReloadEditColdStartsWithTheNewCode(t *testing.T) {
	s := newDockerStack(t)
	s.create(t, "shell", "provided.al2023", "bootstrap", 10, shellFiles(1))
	dir := t.TempDir()
	write := func(version int) {
		if err := os.WriteFile(filepath.Join(dir, "bootstrap"), []byte(shellFiles(version)["bootstrap"]), 0o755); err != nil { //nolint:gosec // the container's user must execute it
			t.Fatal(err)
		}
	}
	write(2)
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // the container's user must read the mounted directory
		t.Fatal(err)
	}
	if err := s.x.HotReload(t.Context(), s.scope, "shell", dir); err != nil {
		t.Fatalf("HotReload: %s", err.Message)
	}
	if got := result(t, s.invoke(t, "shell", `{}`)); got["version"] != 2.0 {
		t.Fatalf("payload = %v, want version 2 from the hot-reload directory", got)
	}
	result(t, s.invoke(t, "shell", `{}`))
	write(3)
	// The watcher reports the edit once the directory stays unchanged for
	// its quiet period; until then invocations keep running version 2.
	reloaded, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		got := result(t, s.invoke(t, "shell", `{}`))
		if got["version"] == 3.0 {
			if got["count"] != 1.0 {
				t.Errorf("first invocation of the new code: payload %v, want count 1 from a cold start", got)
			}
			return
		}
		if reloaded.Err() != nil {
			t.Fatalf("10 s after the edit invocations still run %v, want version 3", got)
		}
	}
}

// The deadline a function sees is wall time (lambda-runtime-contract.md 3),
// so a frozen and advanced emulator clock leaves
// get_remaining_time_in_millis() at the function's timeout.
func TestDockerDeadlineIgnoresTheEmulatorClock(t *testing.T) {
	s := newDockerStack(t)
	s.create(t, "py", "python3.13", "handler.handler", 3, map[string]string{"handler.py": pythonFunction})
	s.clock.Freeze()
	if _, err := s.clock.Advance(time.Hour); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		remaining, ok := result(t, s.invoke(t, "py", `{"action":"remaining"}`))["remaining"].(float64)
		// The function reads the deadline on its own clock. Docker
		// Desktop's VM clock trailed the host's by up to 4 ms (macOS run,
		// 2026-11-06), so a few milliseconds above the timeout are skew.
		if d := 3*time.Second - time.Duration(remaining)*time.Millisecond; !ok || d < -50*time.Millisecond || d > 100*time.Millisecond {
			t.Errorf("invocation %d: get_remaining_time_in_millis() = %v, want within 100 ms of 3 s", i, remaining)
		}
	}
}

// callerRuntime is a custom runtime that, in each invocation, calls the
// server at $CALLS twice, as function code calling understudy would: once
// with the environment's minted access key and once with the static key
// "test" a function's configuration might set; X-Key carries the key.
const callerRuntime = `#!/bin/sh
set -eu
API="http://${AWS_LAMBDA_RUNTIME_API}/2018-06-01/runtime"
while true; do
  HEADERS=$(mktemp)
  curl -sS -D "$HEADERS" -o /dev/null "$API/invocation/next"
  ID=$(grep -i '^Lambda-Runtime-Aws-Request-Id:' "$HEADERS" | tr -d '\r' | cut -d' ' -f2)
  MINTED=$(curl -sS -o /dev/null -w '%{http_code}' -H "X-Key: $AWS_ACCESS_KEY_ID" "http://$CALLS/")
  FOREIGN=$(curl -sS -o /dev/null -w '%{http_code}' -H "X-Key: test" "http://$CALLS/")
  curl -sS -X POST "$API/invocation/$ID/response" -d "{\"minted\":$MINTED,\"foreign\":$FOREIGN}" >/dev/null
done
`

// A real function container's calls reach understudy from the address
// the launcher recorded for its environment on the topologies where that
// is the peer address (Linux bridge, in a container), so the call with
// the static key journals one unminted-credentials Advisory naming the
// function, and the call with the minted key links to the Invoke. On the
// other topologies neither call journals one (ARCHITECTURE.md 10).
func TestDockerCallsWithUnmintedCredentialsFromAFunctionContainerJournalAnAdvisory(t *testing.T) {
	s := newDockerStack(t)
	addrs, err := s.top.Addresses()
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	switch s.top.(type) {
	case topology.LinuxBridge, topology.InContainer:
		want = 1
	}
	ln, err := net.Listen("tcp", netip.AddrPortFrom(addrs.ListenerBind, 0).String())
	if err != nil {
		t.Fatal(err)
	}
	// The handler serves each call as the edge does: the principal its key
	// resolves to and the connection's remote address.
	srv := &http.Server{ReadHeaderTimeout: time.Minute, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote, _ := netip.ParseAddrPort(r.RemoteAddr) //nolint:errcheck // the edge journals a malformed remote address as the zero address too
		p, _ := s.k.Identity().Resolve(r.Header.Get("X-Key"))
		c := s.k.NewCall(kernel.ExternalCall{
			RequestID: s.k.IDs().RequestID(),
			Operation: lambdaapi.OpGetFunction.Descriptor().ID,
			Scope:     s.scope,
			Principal: p,
			Origin:    ref.External{Protocol: ref.RESTJSON1, Listener: ref.ListenerContainer, RemoteAddr: remote},
			Admitted:  s.clock.Now(),
			Endpoint:  kernel.Endpoint{Scheme: "http", Host: r.Host},
		})
		if _, ok := s.k.Dispatch(r.Context(), c, kernel.Ok{In: &lambdaapi.GetFunctionInput{FunctionName: "caller"}}).(kernel.Output); !ok {
			w.WriteHeader(http.StatusInternalServerError)
		}
	})}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close the caller server: %v", err)
		}
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("caller server: %v", err)
		}
	})
	mustDispatch(t, s, lambdaapi.OpCreateFunction, &lambdaapi.CreateFunctionInput{
		FunctionName: "caller",
		Role:         role,
		Runtime:      new(lambdaapi.RuntimeProvidedal2023),
		Handler:      new("bootstrap"),
		Code:         lambdaapi.FunctionCode{ZipFile: zipFiles(t, map[string]string{"bootstrap": callerRuntime})},
		MemorySize:   new(int32(128)),
		Timeout:      new(int32(30)),
		PackageType:  new(lambdaapi.PackageTypeZip),
		Environment:  &lambdaapi.Environment{Variables: map[string]string{"CALLS": ln.Addr().String()}},
	})
	if got := result(t, s.invoke(t, "caller", `{}`)); got["minted"] != 200.0 || got["foreign"] != 200.0 {
		t.Fatalf("the function's calls answered %v, want 200 to both: an advisory must not change the call", got)
	}
	j := s.k.Journal()
	invoke := j.Query(journal.Filter{Kind: journal.KindAPICall, Operation: lambdaapi.OpInvoke.Descriptor().ID}).Events
	gets := j.Query(journal.Filter{Kind: journal.KindAPICall, Operation: lambdaapi.OpGetFunction.Descriptor().ID}).Events
	if len(invoke) != 1 || len(gets) != 2 {
		t.Fatalf("journal holds %d Invoke and %d GetFunction calls, want 1 and 2", len(invoke), len(gets))
	}
	if gets[0].Cause.Parent != invoke[0].ID {
		t.Errorf("the minted call's parent = %d, want the Invoke call's event %d", gets[0].Cause.Parent, invoke[0].ID)
	}
	advisories := j.Query(journal.Filter{Kind: journal.KindAdvisory, Limit: 100}).Events
	if len(advisories) != want {
		t.Fatalf("on %s the journal holds %d advisories, want %d: %+v", s.top, len(advisories), want, advisories)
	}
	function := "arn:aws:lambda:" + s.scope.Region.String() + ":" + s.scope.Account.String() + ":function:caller"
	for _, adv := range advisories {
		if a, ok := adv.Detail.(journal.Advisory); !ok || a.Kind != journal.AdvisoryUnmintedCredentials || a.Resource.String() != function || adv.Cause.Parent != gets[1].ID {
			t.Errorf("advisory = %+v caused by %d, want unminted-credentials naming %s caused by the static-key call %d", adv.Detail, adv.Cause.Parent, function, gets[1].ID)
		}
	}
}
