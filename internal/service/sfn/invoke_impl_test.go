package sfn_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/logs"
	"github.com/sustinbebustin/understudy/internal/service/sfn"
)

// heldInvoke makes the stub Lambda hold its first invocation until release
// is called, and counts every invocation.
func heldInvoke(h *harness) (calls *atomic.Int32, started <-chan struct{}, release func()) {
	calls = new(atomic.Int32)
	start, held := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(held) }) }
	h.answer(func(_ *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
		if calls.Add(1) == 1 {
			close(start)
			<-held
		}
		return &lambdaapi.InvokeOutput{StatusCode: 200, Payload: in.Payload}, nil
	})
	return calls, start, release
}

// A Rearm that follows a reset of another account, as every admin reset
// does, does not invoke a Lambda task again while its function runs: only
// a snapshot load re-invokes (the StartExecution coverage note).
func TestRearmDoesNotInvokeARunningTaskAgain(t *testing.T) {
	run(t, func(h *harness) {
		calls, started, release := heldInvoke(h)
		defer release()
		machine := h.legacyTaskMachine("m", h.functionArn("f"))
		out := must(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, Name: new("x")})
		<-started
		other, err := aws.ParseAccountID("999999999999")
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{Accounts: []aws.AccountID{other}})
		h.Kernel.Timers().Scheduler().Clear()
		h.svc.Rearm(h.Clock.Now())
		synctest.Wait()
		if n := calls.Load(); n != 1 {
			h.Errorf("Invoke calls while the first runs = %d, want 1", n)
		}
		release()
		h.settled()
		if d := h.describeExecution(out.ExecutionArn); d.Status != sfnapi.ExecutionStatusSucceeded {
			h.Errorf("execution = %s, want SUCCEEDED", d.Status)
		}
		if n := calls.Load(); n != 1 {
			h.Errorf("Invoke calls = %d, want 1", n)
		}
	})
}

// A reset of the execution's own scope frees its task: an execution of the
// same name started after the reset invokes its function even while the
// dropped execution's function still runs.
func TestResetFreesTheTaskOfADroppedExecution(t *testing.T) {
	run(t, func(h *harness) {
		calls, started, release := heldInvoke(h)
		defer release()
		machine := h.legacyTaskMachine("m", h.functionArn("f"))
		must(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, Name: new("x")})
		<-started
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		machine = h.legacyTaskMachine("m", h.functionArn("f"))
		out := must(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, Name: new("x")})
		synctest.Wait()
		if n := calls.Load(); n != 2 {
			h.Errorf("Invoke calls = %d, want the dropped execution's and the new one's", n)
		}
		release()
		h.settled()
		if d := h.describeExecution(out.ExecutionArn); d.Status != sfnapi.ExecutionStatusSucceeded {
			h.Errorf("execution after the reset = %s, want SUCCEEDED", d.Status)
		}
	})
}

// A Lambda task whose invocation is cut short by the kernel stopping, as at
// shutdown before the final checkpoint, stays in flight: it is not failed,
// so a load of the state saved then invokes it again (the StartExecution
// coverage note).
func TestShutdownLeavesTheTaskInFlight(t *testing.T) {
	var (
		svc  *sfn.Service
		exec string
	)
	// The stub needs the invocation's context, which the harness's answer
	// does not see, so this kernel binds its own.
	kerneltest.Run(t, func(k *kerneltest.K) {
		logs.New(k.Deps(logsapi.Service()), logs.Options{})
		h := &harness{K: k, svc: sfn.New(k.Deps(sfnapi.Service()), sfn.Options{})}
		started := make(chan struct{})
		k.Stub(lambdaapi.OpInvoke, func(ctx context.Context, _ *kernel.Call, _ *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			close(started)
			<-ctx.Done()
			return nil, awserr.Internalf(ctx.Err())
		})
		machine := h.legacyTaskMachine("m", h.functionArn("f"))
		exec = must(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, Name: new("x")}).ExecutionArn
		<-started
		svc = h.svc
	}, kerneltest.Services(sfnapi.Service(), lambdaapi.Service(), logsapi.Service()))
	sections, err := svc.Export()
	if err != nil {
		t.Fatal(err)
	}
	run(t, func(h *harness) {
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		h.svc.Rearm(h.Clock.Now())
		h.settled()
		if d := h.describeExecution(exec); d.Status != sfnapi.ExecutionStatusSucceeded {
			h.Errorf("execution after load = %s, want SUCCEEDED from the re-invoked task; history:\n%s", d.Status, shape(h.history(exec)))
		}
	})
}
