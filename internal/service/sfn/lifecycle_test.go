package sfn_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/sfn"
)

// A Standard execution may wait up to a year (aws-doc dg/state-wait,
// dg/service-quotas): a Wait of 30,000,000 seconds finishes on one clock
// advance, because the step is an emulator-clock timer (spec Acceptance;
// invariant 5).
func TestLongWaitFinishesOnOneClockAdvance(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("long", `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":30000000,"End":true}}}`)
		arn := h.start(machine, "x", nil)
		if d := h.describeExecution(arn); d.Status != sfnapi.ExecutionStatusRunning {
			h.Fatalf("status before the advance = %s, want RUNNING", d.Status)
		}
		h.advance(30_000_000 * time.Second)
		d := h.describeExecution(arn)
		if d.Status != sfnapi.ExecutionStatusSucceeded || d.StopDate == nil || !d.StopDate.Equal(h.now()) {
			h.Fatalf("after one advance: %+v, want SUCCEEDED now", d)
		}
	})
}

// Execution TimeoutSeconds ends the execution TIMED_OUT with no error or
// cause in DescribeExecution (stepfunctions-asl.md 8.6; aws-recorded
// localstack@8b9a79f v2/timeouts/test_timeouts.snapshot.json:L688-L708).
func TestExecutionTimeoutSeconds(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("t", `{"StartAt":"W","TimeoutSeconds":10,"States":{"W":{"Type":"Wait","Seconds":100,"End":true}}}`)
		arn := h.start(machine, "x", nil)
		h.advance(10 * time.Second)
		d := h.describeExecution(arn)
		if d.Status != sfnapi.ExecutionStatusTimedOut || d.Error != nil || d.Cause != nil {
			h.Fatalf("DescribeExecution = %+v, want TIMED_OUT without error or cause", d)
		}
	})
}

// Closed Standard executions are kept 90 days, then removed, and the name
// can be reused (aws-doc dg/service-quotas; stepfunctions-api.md 4.3, 6.1).
func TestClosedExecutionRetention(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("m", resultMachine)
		arn := h.start(machine, "keep", nil)
		h.advance(89 * 24 * time.Hour)
		if d := h.describeExecution(arn); d.Status != sfnapi.ExecutionStatusSucceeded {
			h.Fatalf("after 89 days: %+v", d)
		}
		h.advance(2 * 24 * time.Hour)
		wantError(h, errOf(call(h, sfnapi.OpDescribeExecution, &sfnapi.DescribeExecutionInput{ExecutionArn: arn})),
			"ExecutionDoesNotExist", 400, "Execution Does Not Exist: '"+arn+"'")
		if again := h.start(machine, "keep", nil); again != arn {
			h.Errorf("reused name gave %s, want %s", again, arn)
		}
	})
}

// DeleteStateMachine with a running execution: the machine is DELETING
// and the execution ends FAILED at its next transition (SFN-06, recorded
// 2026-11-03; switch sfn.delete.running-execution), then the machine is
// gone (understudy's choice: AWS still described it DELETING 45 s later,
// SFN-06 08; stepfunctions-api.md 5.4).
func TestDeleteWithRunningExecution(t *testing.T) {
	// inferred, SFN-06
	run(t, func(h *harness) {
		// With the switch off the execution runs on to SUCCEEDED; either way
		// the machine is removed once it has no running execution.
		wantStatus := sfnapi.ExecutionStatusSucceeded
		if sfn.DeleteRunningExecution().Value {
			wantStatus = sfnapi.ExecutionStatusFailed
		}
		machine := h.create("w", `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":60,"Next":"P"},"P":{"Type":"Pass","End":true}}}`)
		arn := h.start(machine, "x", nil)
		must(h, sfnapi.OpDeleteStateMachine, &sfnapi.DeleteStateMachineInput{StateMachineArn: machine})
		h.settled()
		d := must(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: machine})
		if d.Status == nil || *d.Status != sfnapi.StateMachineStatusDeleting {
			h.Fatalf("status after delete = %v, want DELETING", d.Status)
		}
		h.advance(time.Minute)
		if e := h.describeExecution(arn); e.Status != wantStatus {
			h.Errorf("execution after its next transition = %s, want %s (switch sfn.delete.running-execution)", e.Status, wantStatus)
		}
		wantError(h, errOf(call(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: machine})),
			"StateMachineDoesNotExist", 400, "State Machine Does Not Exist: '"+machine+"'")
	})
}

// A snapshot round trip keeps the machine and a finished execution with its
// history (Stateful, ADR 0009; ARCHITECTURE.md 13).
func TestSnapshotRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("m", resultMachine)
		arn := h.start(machine, "x", new(`{"a":1}`))
		before, beforeHistory := *h.describeExecution(arn), shape(h.history(arn))
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		if len(sections) == 0 {
			h.Fatal("Export wrote no section for a scope with a machine and an execution")
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		if _, err := call(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: machine}); err == nil {
			h.Fatal("the machine survived the reset")
		}
		if err := h.svc.Import(sections); err != nil {
			h.Fatalf("Import = %v", err)
		}
		if diff := cmp.Diff(before, *h.describeExecution(arn)); diff != "" {
			h.Errorf("DescribeExecution after the round trip (-before +after):\n%s", diff)
		}
		if diff := cmp.Diff(beforeHistory, shape(h.history(arn))); diff != "" {
			h.Errorf("history after the round trip (-before +after):\n%s", diff)
		}
		if d := must(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: machine}); d.Definition != resultMachine {
			h.Errorf("definition after the round trip = %q", d.Definition)
		}
	})
}

// A snapshot loaded mid-Wait re-arms the step: Rearm rebuilds the timers
// from state, and the Wait finishes when the clock reaches it (spec
// Acceptance; ADR 0009).
func TestSnapshotMidWaitRearms(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("w", `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":60,"End":true}}}`)
		arn := h.start(machine, "x", nil)
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		if err := h.svc.Import(sections); err != nil {
			h.Fatalf("Import = %v", err)
		}
		h.svc.Rearm(h.Clock.Now())
		if d := h.describeExecution(arn); d.Status != sfnapi.ExecutionStatusRunning {
			h.Fatalf("after load: %s, want RUNNING", d.Status)
		}
		h.advance(time.Minute)
		if d := h.describeExecution(arn); d.Status != sfnapi.ExecutionStatusSucceeded {
			h.Fatalf("after the re-armed Wait: %s, want SUCCEEDED", d.Status)
		}
	})
}

// A Lambda task in flight when the snapshot was saved is invoked again
// after load (StartExecution coverage note).
func TestSnapshotReinvokesInFlightTask(t *testing.T) {
	run(t, func(h *harness) {
		// The first invocation holds until the state it belongs to is reset,
		// so its late answer finds no execution; the second is the re-invoke.
		var calls atomic.Int32
		started, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		releaseFirst := func() { releaseOnce.Do(func() { close(release) }) }
		// A failing test still lets the held invocation return, so the
		// bubble can end.
		defer releaseFirst()
		h.answer(func(_ *kernel.Call, _ *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			if calls.Add(1) == 1 {
				close(started)
				<-release
				return nil, lambdaapi.ErrServiceException("answered after the reset")
			}
			return &lambdaapi.InvokeOutput{StatusCode: 200, Payload: []byte(`{"second":true}`)}, nil
		})
		machine := h.legacyTaskMachine("m", h.functionArn("f"))
		out := must(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, Name: new("x")})
		select {
		case <-started:
		case <-time.After(settle):
			h.Fatal("the Task never invoked its function")
		}
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		releaseFirst()
		h.settled()
		if err := h.svc.Import(sections); err != nil {
			h.Fatalf("Import = %v", err)
		}
		h.svc.Rearm(h.Clock.Now())
		h.settled()
		if d := h.describeExecution(out.ExecutionArn); d.Status != sfnapi.ExecutionStatusSucceeded || !sameJSON(h, val(d.Output), `{"second":true}`) {
			h.Errorf("after load: %+v, want the re-invoked task's output", d)
		}
		if n := calls.Load(); n != 2 {
			h.Errorf("Invoke calls = %d, want the original and one re-invoke", n)
		}
	})
}
