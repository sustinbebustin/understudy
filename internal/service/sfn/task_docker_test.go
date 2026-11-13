//go:build docker

package sfn_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
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
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/service/logs"
	"github.com/sustinbebustin/understudy/internal/service/sfn"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// echoRuntime is a provided.al2023 custom runtime written against the
// Runtime API with curl: it answers every event with the event.
const echoRuntime = `#!/bin/sh
set -eu
API="http://${AWS_LAMBDA_RUNTIME_API}/2018-06-01/runtime"
while true; do
  HEADERS=$(mktemp)
  EVENT=$(curl -sS -D "$HEADERS" "$API/invocation/next")
  ID=$(grep -i '^Lambda-Runtime-Aws-Request-Id:' "$HEADERS" | tr -d '\r' | cut -d' ' -f2)
  curl -sS -X POST "$API/invocation/$ID/response" -d "$EVENT" >/dev/null
done
`

// A lambda:invoke Task against a real function on Docker succeeds with the
// function's output, and its SdkHttpMetadata has exactly golden 21's header
// key sets, so the stub-backed goldens cannot hide a different shape on the
// real invoke path (localstack@8b9a79f
// v2/services/test_lambda_task_service.snapshot.json:L67-L114).
func TestDockerLambdaInvokeTaskHeaderKeys(t *testing.T) {
	s := newTaskDockerStack(t)
	role := fmt.Sprintf("arn:aws:iam::%s:role/sfn-role", s.scope.Account)
	dockerMust(t, s, lambdaapi.OpCreateFunction, &lambdaapi.CreateFunctionInput{
		FunctionName: "echo", Role: role, Runtime: new(lambdaapi.Runtime("provided.al2023")), Handler: new("bootstrap"),
		Code: lambdaapi.FunctionCode{ZipFile: zipOf(t, "bootstrap", echoRuntime)}, MemorySize: new(int32(128)), Timeout: new(int32(30)),
		PackageType: new(lambdaapi.PackageTypeZip),
	})
	def := `{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","Parameters":{"FunctionName":"echo","Payload.$":"$"},"End":true}}}`
	machine := dockerMust(t, s, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "docker", Definition: def, RoleArn: role})
	started := dockerMust(t, s, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine.StateMachineArn, Input: new(`{"hello":"world"}`)})
	if p, err := s.k.Settle(t.Context(), 3*time.Minute); err != nil {
		t.Fatalf("Settle = %v, %v", p, err)
	}
	d := dockerMust(t, s, sfnapi.OpDescribeExecution, &sfnapi.DescribeExecutionInput{ExecutionArn: started.ExecutionArn})
	if d.Status != sfnapi.ExecutionStatusSucceeded {
		t.Fatalf("execution %s: %s %q", d.Status, val(d.Error), val(d.Cause))
	}
	out := decode(t, val(d.Output))
	if !equal(at(out, "Payload"), decode(t, `{"hello":"world"}`)) || !equal(at(out, "StatusCode"), decode(t, "200")) {
		t.Errorf("result = %s, want the echoed payload and StatusCode 200", val(d.Output))
	}
	want := goldenHeaderKeys(t)
	meta := at(out, "SdkHttpMetadata")
	for _, member := range []string{"HttpHeaders", "AllHttpHeaders"} {
		if got := mapKeys(at(meta, member)); !slices.Equal(got, want) {
			t.Errorf("%s keys = %v, want golden 21's %v", member, got, want)
		}
	}
}

// goldenHeaderKeys are the header names of golden 21's TaskSucceeded
// result, sorted.
func goldenHeaderKeys(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "localstack", "21-lambda-invoke.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g goldenFixture
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	events, ok := decode(t, string(g.History)).(document.List)
	if !ok {
		t.Fatal("golden 21's history is not a list")
	}
	for _, e := range events {
		if headers := at(at(at(at(e, "taskSucceededEventDetails"), "output"), "SdkHttpMetadata"), "HttpHeaders"); headers != nil {
			return mapKeys(headers)
		}
	}
	t.Fatal("golden 21 has no TaskSucceeded result")
	return nil
}

// taskDockerStack is a running kernel with Step Functions, CloudWatch Logs
// and the whole Lambda path on a real Docker daemon.
type taskDockerStack struct {
	k     *kernel.Kernel
	clock *clock.Clock
	scope aws.Scope
}

// linker routes runtime API callers to the Lambda Execution, as the server
// wires it.
type linker struct {
	mu sync.Mutex
	x  *lambda.Execution
}

func (l *linker) Link(p identity.Principal, remote netip.AddrPort) kernel.Link {
	l.mu.Lock()
	x := l.x
	l.mu.Unlock()
	if x == nil {
		return kernel.Unlinked{}
	}
	return x.Link(p, remote)
}

func newTaskDockerStack(t *testing.T) *taskDockerStack {
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
	reg, err := registry.New(lambdaapi.Service(), logsapi.Service(), sfnapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	src := ids.NewSeeded(1)
	idStore := identity.New(src, aws.DefaultAccount())
	link := &linker{}
	k, err := kernel.New(kernel.Options{Registry: reg, Clock: clk, Timers: timers, IDs: src, Journal: j, Identity: idStore, Linker: link, Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	owner := "sfn-task-" + hex.EncodeToString(suffix)
	l, err := launcher.New(client, launcher.Options{
		Ownership: docker.Ownership{Instance: owner, Owner: owner}, Test: true, Topology: top, EndpointPort: 4566,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtimeapi.NewServer(runtimeapi.Options{Clock: clk, Topology: top})
	if err != nil {
		t.Fatal(err)
	}
	p, err := pool.New(pool.Options{Launcher: l, Runtime: pooltest.ServerRuntime(rt), Clock: clk})
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
	deps := func(svc *registry.Service) kernel.ServiceDeps {
		return kernel.ServiceDeps{Kernel: k, Binder: k.Binder(svc), Clock: clk, IDs: src, Blob: blobs, Identity: idStore, Docker: client, Log: k.Log()}
	}
	logs.New(deps(logsapi.Service()), logs.Options{})
	sfn.New(deps(sfnapi.Service()), sfn.Options{})
	lambdaDeps := deps(lambdaapi.Service())
	x, err := lambda.NewExecution(lambda.New(lambdaDeps, lambda.Options{Images: l}), lambdaDeps, lambda.ExecutionOptions{
		Pool: p, Docker: client.Endpoint().String(), Images: l, Code: code, Logs: lambda.ContainerLogs(l),
	})
	if err != nil {
		t.Fatal(err)
	}
	link.mu.Lock()
	link.x = x
	link.mu.Unlock()

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
		if err := rt.Close(); err != nil {
			t.Errorf("close runtime API: %v", err)
		}
		left, err := client.ContainerList(ctx, map[string]string{docker.LabelOwner: owner, docker.LabelTest: "1"})
		if err != nil {
			t.Errorf("list leftover containers: %v", err)
		}
		for _, c := range left {
			if err := client.ContainerRemove(ctx, c.ID, docker.RemoveOptions{Force: true, Volumes: true}); err != nil && !docker.IsNotFound(err) {
				t.Errorf("remove leftover container %s: %v", c.ID, err)
			}
		}
		if err := code.Close(); err != nil {
			t.Error(err)
		}
		if err := blobs.Close(); err != nil {
			t.Error(err)
		}
	})
	return &taskDockerStack{k: k, clock: clk, scope: aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()}}
}

// dockerMust runs op as a client request through the kernel and fails the
// test on an error.
func dockerMust[In, Out any](t *testing.T, s *taskDockerStack, op kernel.Op[In, Out], in *In) *Out {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	c := s.k.NewCall(kernel.ExternalCall{
		RequestID: s.k.IDs().RequestID(),
		Operation: op.Descriptor().ID,
		Scope:     s.scope,
		Principal: identity.Root{Account: s.scope.Account},
		Origin:    ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain},
		Admitted:  s.clock.Now(),
		Endpoint:  kernel.Endpoint{Scheme: "http", Host: "localhost:4566"},
	})
	var err *awserr.Error
	switch r := s.k.Dispatch(ctx, c, kernel.Ok{In: in}).(type) {
	case kernel.Output:
		if out, ok := r.Out.(*Out); ok {
			return out
		}
		t.Fatalf("%s returned %T", op.Descriptor().ID, r.Out)
	case kernel.Failure:
		err = r.Err
	case kernel.StatusOKFailure:
		err = r.Err
	}
	if err == nil {
		t.Fatalf("%s returned no result", op.Descriptor().ID)
	}
	t.Fatalf("%s: %s %s", op.Descriptor().ID, err.ID.Shape.Name(), err.Message)
	return nil
}

func zipOf(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
