package sfn_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
)

// A TIMED_OUT execution is REDRIVABLE without a reason, error or cause
// (recorded), and so is an ABORTED one (documented); a RUNNING one has no
// redriveStatus (stepfunctions-api.md 6.3).
func TestRedriveStatusOfClosedExecutions(t *testing.T) {
	run(t, func(h *harness) {
		timeout := h.create("t", `{"StartAt":"W","TimeoutSeconds":5,"States":{"W":{"Type":"Wait","Seconds":60,"End":true}}}`)
		timedOut := h.start(timeout, "x", nil)
		h.advance(5 * time.Second)
		if d := h.describeExecution(timedOut); d.Status != sfnapi.ExecutionStatusTimedOut || d.RedriveStatus == nil ||
			*d.RedriveStatus != sfnapi.ExecutionRedriveStatusRedrivable || d.RedriveStatusReason != nil || d.Error != nil || d.Cause != nil {
			h.Errorf("TIMED_OUT execution = %+v, want REDRIVABLE without reason, error or cause", d)
		}

		machine := h.create("w", waitMachine)
		running := h.start(machine, "r", nil)
		if d := h.describeExecution(running); d.Status != sfnapi.ExecutionStatusRunning || d.RedriveStatus != nil || d.RedriveStatusReason != nil {
			h.Errorf("RUNNING execution = %+v, want no redriveStatus", d)
		}
		must(h, sfnapi.OpStopExecution, &sfnapi.StopExecutionInput{ExecutionArn: running})
		if d := h.describeExecution(running); d.Status != sfnapi.ExecutionStatusAborted || d.RedriveStatus == nil ||
			*d.RedriveStatus != sfnapi.ExecutionRedriveStatusRedrivable || d.RedriveStatusReason != nil {
			h.Errorf("ABORTED execution = %+v, want REDRIVABLE without reason", d)
		}
	})
}
