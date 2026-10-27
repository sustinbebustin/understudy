package sfn_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/sfn"
)

// Stopping a closed execution answers 200 with the stopDate it closed at
// and changes nothing; a stop's error and cause are kept in
// DescribeExecution and ExecutionAborted (StopExecution coverage note).
func TestStopExecutionKeepsErrorAndClosedIsUnchanged(t *testing.T) {
	// inferred, SFN-04
	run(t, func(h *harness) {
		machine := h.create("w", waitMachine)
		arn := h.start(machine, "s", nil)
		h.advance(time.Second)
		stopped := must(h, sfnapi.OpStopExecution, &sfnapi.StopExecutionInput{ExecutionArn: arn, Error: new("Halt"), Cause: new("by hand")})
		d := h.describeExecution(arn)
		if d.Status != sfnapi.ExecutionStatusAborted || val(d.Error) != "Halt" || val(d.Cause) != "by hand" {
			h.Errorf("DescribeExecution = %+v, want ABORTED Halt/by hand", d)
		}
		events := h.history(arn)
		if a := events[len(events)-1].ExecutionAbortedEventDetails; a == nil || val(a.Error) != "Halt" || val(a.Cause) != "by hand" {
			h.Errorf("ExecutionAborted details = %+v, want the error and cause", a)
		}
		before := shape(events)

		h.advance(time.Hour)
		again := must(h, sfnapi.OpStopExecution, &sfnapi.StopExecutionInput{ExecutionArn: arn, Error: new("Other")})
		if !again.StopDate.Equal(stopped.StopDate) {
			h.Errorf("second stop stopDate = %v, want the first %v", again.StopDate, stopped.StopDate)
		}
		if after := shape(h.history(arn)); after != before || val(h.describeExecution(arn).Error) != "Halt" {
			h.Errorf("a stop of a closed execution changed it:\n%s", after)
		}

		pass := h.create("p", resultMachine)
		done := h.start(pass, "done", nil)
		closed := must(h, sfnapi.OpStopExecution, &sfnapi.StopExecutionInput{ExecutionArn: done})
		if d := h.describeExecution(done); d.Status != sfnapi.ExecutionStatusSucceeded || d.StopDate == nil || !closed.StopDate.Equal(*d.StopDate) {
			h.Errorf("stop of a SUCCEEDED execution = %v, execution %+v", closed.StopDate, d)
		}
		missing := h.executionArn("w", "nope")
		wantError(h, errOf(call(h, sfnapi.OpStopExecution, &sfnapi.StopExecutionInput{ExecutionArn: missing})),
			"ExecutionDoesNotExist", 400, "Execution Does Not Exist: '"+missing+"'")
	})
}

// A DELETING machine refuses new executions, a create of its name and
// updates with StateMachineDeleting in understudy's words; under switch
// sfn.delete.running-execution the running execution ends ABORTED at its
// next transition and the machine goes with it.
func TestDeletingMachineRefusesChanges(t *testing.T) {
	// inferred, SFN-06
	run(t, func(h *harness) {
		machine := h.create("w", waitMachine)
		arn := h.start(machine, "x", nil)
		must(h, sfnapi.OpDeleteStateMachine, &sfnapi.DeleteStateMachineInput{StateMachineArn: machine})
		deleting := "State Machine is being deleted: '" + machine + "'"
		wantError(h, errOf(call(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine})),
			"StateMachineDeleting", 400, deleting)
		wantError(h, errOf(call(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "w", Definition: waitMachine, RoleArn: h.roleArn()})),
			"StateMachineDeleting", 400, deleting)
		wantError(h, errOf(call(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: machine, Definition: new(resultMachine)})),
			"StateMachineDeleting", 400, deleting)
		must(h, sfnapi.OpDeleteStateMachine, &sfnapi.DeleteStateMachineInput{StateMachineArn: machine})
		if !sfn.DeleteRunningExecution().Value {
			return
		}
		// The engine half of the switch flip is asl's
		// TestSwitchDeleteRunningExecution; here the machine goes with the
		// abort, and the closed execution stays.
		h.advance(time.Hour)
		if d := h.describeExecution(arn); d.Status != sfnapi.ExecutionStatusAborted || d.Error != nil {
			h.Errorf("execution after its next transition = %+v, want ABORTED without error", d)
		}
		if again := h.create("w", resultMachine); again != machine {
			h.Errorf("re-created machine = %s, want %s", again, machine)
		}
	})
}

// An execution that ends TIMED_OUT writes ExecutionTimedOut with empty
// details on the wire, and ListExecutions filters it as TIMED_OUT.
func TestTimedOutExecutionOnTheWire(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("t", `{"StartAt":"W","TimeoutSeconds":5,"States":{"W":{"Type":"Wait","Seconds":60,"End":true}}}`)
		arn := h.start(machine, "x", nil)
		h.advance(5 * time.Second)
		events := h.history(arn)
		last := events[len(events)-1]
		if last.Type != sfnapi.HistoryEventTypeExecutionTimedOut || last.ExecutionTimedOutEventDetails == nil ||
			last.ExecutionTimedOutEventDetails.Error != nil || last.ExecutionTimedOutEventDetails.Cause != nil {
			h.Errorf("last event = %+v, want ExecutionTimedOut with {}", last)
		}
		timedOut := sfnapi.ExecutionStatusTimedOut
		if got := must(h, sfnapi.OpListExecutions, &sfnapi.ListExecutionsInput{StateMachineArn: new(machine), StatusFilter: &timedOut}).Executions; len(got) != 1 || got[0].ExecutionArn != arn {
			h.Errorf("TIMED_OUT filter = %+v", got)
		}
	})
}

// A closed execution's 90-day retention survives a snapshot: Rearm arms
// its removal from the stop date (ADR 0009; stepfunctions-api.md 6.1).
func TestRetentionAfterSnapshotLoad(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("m", resultMachine)
		arn := h.start(machine, "keep", nil)
		h.advance(30 * 24 * time.Hour)
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
		h.advance(59 * 24 * time.Hour)
		h.describeExecution(arn)
		h.advance(2 * 24 * time.Hour)
		wantError(h, errOf(call(h, sfnapi.OpDescribeExecution, &sfnapi.DescribeExecutionInput{ExecutionArn: arn})),
			"ExecutionDoesNotExist", 400, "Execution Does Not Exist: '"+arn+"'")
	})
}
