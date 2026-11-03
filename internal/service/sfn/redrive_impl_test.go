package sfn_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
)

// An execution failed at the 25,000-event history limit is NOT_REDRIVABLE
// with AWS's reason (ASL-16, recorded 2026-11-03; stepfunctions-api.md
// 6.3).
func TestRedriveStatusAtTheHistoryLimit(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("loop", `{"StartAt":"A","States":{"A":{"Type":"Pass","Parameters":{"n.$":"States.MathAdd($.n, 1)"},"Next":"C"},
			"C":{"Type":"Choice","Choices":[{"Variable":"$.n","NumericLessThan":20000,"Next":"A"}],"Default":"S"},"S":{"Type":"Succeed"}}}`)
		arn := h.start(machine, "x", new(`{"n":0}`))
		h.settled()
		d := h.describeExecution(arn)
		if d.Status != sfnapi.ExecutionStatusFailed || val(d.Cause) != "The execution reached the maximum number of history events (25000)." ||
			d.RedriveStatus == nil || *d.RedriveStatus != sfnapi.ExecutionRedriveStatusNotRedrivable ||
			val(d.RedriveStatusReason) != "Execution has exceeded the maximum history event limit" {
			h.Errorf("execution at the history limit = %+v, want FAILED, NOT_REDRIVABLE with ASL-16's reason", d)
		}
	})
}

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
