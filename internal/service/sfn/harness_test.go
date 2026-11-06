package sfn_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/logs"
	"github.com/sustinbebustin/understudy/internal/service/sfn"
)

// settle is how long a test waits for the kernel's tracked work and due
// timers, in the bubble's fake time.
const settle = time.Minute

// harness is a kernel with Step Functions, a real Logs service (log group
// checks), and a stub Lambda Invoke that records every call and answers
// with lambda.
type harness struct {
	*kerneltest.K
	svc *sfn.Service

	mu      sync.Mutex
	invokes []invocation
	lambda  func(c *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error)
}

// invocation is one Invoke the stub received.
type invocation struct {
	in   lambdaapi.InvokeInput
	mode kernel.PrincipalMode
}

func run(t *testing.T, fn func(h *harness), opts ...kerneltest.Option) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		logs.New(k.Deps(logsapi.Service()), logs.Options{})
		h := &harness{K: k, svc: sfn.New(k.Deps(sfnapi.Service()), sfn.Options{})}
		h.lambda = func(_ *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			return &lambdaapi.InvokeOutput{StatusCode: 200, Payload: in.Payload, ExecutedVersion: new("$LATEST")}, nil
		}
		k.Stub(lambdaapi.OpInvoke, func(_ context.Context, c *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			h.mu.Lock()
			h.invokes = append(h.invokes, invocation{in: *in, mode: c.Mode()})
			answer := h.lambda
			h.mu.Unlock()
			return answer(c, in)
		})
		fn(h)
	}, append([]kerneltest.Option{kerneltest.Services(sfnapi.Service(), lambdaapi.Service(), logsapi.Service())}, opts...)...)
}

// invoked returns the Invoke calls the stub received.
func (h *harness) invoked() []invocation {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]invocation(nil), h.invokes...)
}

// answer sets how the stub Lambda answers.
func (h *harness) answer(fn func(c *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lambda = fn
}

func call[In, Out any](h *harness, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	return h.Invoke(h.Context(), op, in)
}

func must[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := call(h, op, in)
	if err != nil {
		h.Fatalf("%s = %s (%d): %s", op.Descriptor().ID.Name, err.ID.Shape, err.ID.Status, err.Message)
	}
	return out
}

func errOf[Out any](_ *Out, err *awserr.Error) *awserr.Error { return err }

// wantError fails unless err is the shape (by name, contract section 1:
// clients strip the __type namespace) with status and message.
func wantError(tb testing.TB, err *awserr.Error, shape string, status int, message string) {
	tb.Helper()
	switch {
	case err == nil:
		tb.Fatalf("no error, want %s %d %q", shape, status, message)
	case err.ID.Shape.Name() != shape || err.ID.Status != status || err.Message != message:
		tb.Fatalf("error = %s %d %q\nwant  %s %d %q", err.ID.Shape.Name(), err.ID.Status, err.Message, shape, status, message)
	}
}

func (h *harness) settled() {
	h.Helper()
	if p, err := h.Kernel.Settle(h.Context(), settle); err != nil {
		h.Fatalf("Settle = %v, %v", p, err)
	}
}

// advance moves the emulator clock and settles what came due.
func (h *harness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
	h.settled()
}

func (h *harness) now() time.Time { return h.Clock.Now().Wire() }

func (h *harness) machineArn(name string) string {
	return fmt.Sprintf("arn:aws:states:%s:%s:stateMachine:%s", h.Scope.Region, h.Scope.Account, name)
}

func (h *harness) executionArn(machine, name string) string {
	return fmt.Sprintf("arn:aws:states:%s:%s:execution:%s:%s", h.Scope.Region, h.Scope.Account, machine, name)
}

func (h *harness) roleArn() string {
	return fmt.Sprintf("arn:aws:iam::%s:role/sfn-role", h.Scope.Account)
}

func (h *harness) functionArn(name string) string {
	return fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", h.Scope.Region, h.Scope.Account, name)
}

const (
	// resultMachine is one Pass state with a Result: the tracer bullet's machine.
	resultMachine = `{"StartAt":"P","States":{"P":{"Type":"Pass","Result":{"done":true},"End":true}}}`
	waitMachine   = `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":3600,"End":true}}}`
)

// create creates a Standard state machine and returns its ARN.
func (h *harness) create(name, definition string) string {
	h.Helper()
	out := must(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: name, Definition: definition, RoleArn: h.roleArn()})
	return out.StateMachineArn
}

// start starts an execution named name (generated when empty) with input
// (omitted when nil) and settles.
func (h *harness) start(machine, name string, input *string) string {
	h.Helper()
	in := &sfnapi.StartExecutionInput{StateMachineArn: machine, Input: input}
	if name != "" {
		in.Name = new(name)
	}
	out := must(h, sfnapi.OpStartExecution, in)
	h.settled()
	return out.ExecutionArn
}

func (h *harness) describeExecution(arn string) *sfnapi.DescribeExecutionOutput {
	h.Helper()
	return must(h, sfnapi.OpDescribeExecution, &sfnapi.DescribeExecutionInput{ExecutionArn: arn})
}

func (h *harness) history(arn string) []sfnapi.HistoryEvent {
	h.Helper()
	var events []sfnapi.HistoryEvent
	var token *string
	for {
		out := must(h, sfnapi.OpGetExecutionHistory, &sfnapi.GetExecutionHistoryInput{ExecutionArn: arn, NextToken: token})
		events = append(events, out.Events...)
		if out.NextToken == nil {
			return events
		}
		token = out.NextToken
	}
}

// shape writes a history as "<Type> <id><-<previous>" lines
// (stepfunctions-asl.md 11.3).
func shape(events []sfnapi.HistoryEvent) string {
	lines := make([]string, len(events))
	for i := range events {
		lines[i] = fmt.Sprintf("%s %d<-%d", events[i].Type, events[i].Id, events[i].PreviousEventId)
	}
	return strings.Join(lines, "\n")
}

func val(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// decode reads JSON text into a document value.
func decode(tb testing.TB, s string) document.Value {
	tb.Helper()
	d := jsonx.NewDecoder([]byte(s))
	v, err := d.ReadValue()
	if err == nil {
		err = d.End()
	}
	if err != nil {
		tb.Fatalf("decode %q: %v", s, err)
	}
	return v
}

// sameJSON compares two JSON texts as values: objects as member sets.
func sameJSON(tb testing.TB, a, b string) bool {
	tb.Helper()
	return equal(decode(tb, a), decode(tb, b))
}

func equal(a, b document.Value) bool {
	switch a := a.(type) {
	case document.Map:
		b, ok := b.(document.Map)
		if !ok || len(a) != len(b) {
			return false
		}
		for _, ea := range a {
			vb, ok := member(b, ea.Key)
			if !ok || !equal(ea.Value, vb) {
				return false
			}
		}
		return true
	case document.List:
		b, ok := b.(document.List)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !equal(a[i], b[i]) {
				return false
			}
		}
		return true
	case document.Number, document.String, document.Bool, document.Null:
		return a == b
	}
	return false
}

func member(m document.Map, key string) (document.Value, bool) {
	for _, e := range m {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

// at reads the member key of v, nil when v is not an object or lacks it.
func at(v document.Value, key string) document.Value {
	m, ok := v.(document.Map)
	if !ok {
		return nil
	}
	out, _ := member(m, key)
	return out
}
