package sfn_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/journal"
)

const failMachine = `{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"Bad","Cause":"because"}}}`

// ListExecutions lists closed executions by stopDate, most recent first,
// filters by status, and pages with tokens of its own listing
// (stepfunctions-api.md 6.5).
func TestListExecutionsOrderFilterAndPaging(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("m", failMachine)
		arns := make([]string, 0, 4)
		for _, name := range []string{"a", "b", "c"} {
			arns = append(arns, h.start(machine, name, nil))
			h.advance(time.Second)
		}
		must(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: machine, Definition: new(resultMachine)})
		arns = append(arns, h.start(machine, "d", nil))

		var got []string
		var token *string
		for {
			page := must(h, sfnapi.OpListExecutions, &sfnapi.ListExecutionsInput{StateMachineArn: new(machine), MaxResults: 1, NextToken: token})
			if len(page.Executions) > 1 {
				h.Fatalf("page of %d items, want at most 1", len(page.Executions))
			}
			for _, e := range page.Executions {
				got = append(got, e.ExecutionArn)
				if e.StopDate == nil || e.RedriveCount == nil || *e.RedriveCount != 0 {
					h.Errorf("item %+v, want a stopDate and redriveCount 0", e)
				}
			}
			if page.NextToken == nil {
				break
			}
			token = page.NextToken
		}
		if want := []string{arns[3], arns[2], arns[1], arns[0]}; !slices.Equal(got, want) {
			h.Errorf("paged executions = %v, want most recent first %v", got, want)
		}

		failed := sfnapi.ExecutionStatusFailed
		only := must(h, sfnapi.OpListExecutions, &sfnapi.ListExecutionsInput{StateMachineArn: new(machine), StatusFilter: &failed}).Executions
		if len(only) != 3 || only[0].Status != sfnapi.ExecutionStatusFailed {
			h.Errorf("FAILED filter = %+v, want the three failed executions", only)
		}

		first := must(h, sfnapi.OpListExecutions, &sfnapi.ListExecutionsInput{StateMachineArn: new(machine), MaxResults: 1})
		_, err := call(h, sfnapi.OpListExecutions, &sfnapi.ListExecutionsInput{StateMachineArn: new(machine), StatusFilter: &failed, NextToken: first.NextToken})
		wantError(h, err, "InvalidToken", 400, "Invalid Token: 'Invalid token'")
	})
}

// The documented member rules of ListExecutions (aws-doc
// api/API_ListExecutions): one of stateMachineArn and mapRunArn, and
// redriveFilter or PENDING_REDRIVE only with a Map Run. AWS's texts are
// unrecorded, so only the code is asserted. A well-formed mapRunArn lists
// nothing, since no Map Run exists.
func TestListExecutionsMemberRules(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("m", resultMachine)
		pending := sfnapi.ExecutionStatusPendingRedrive
		redriven := sfnapi.ExecutionRedriveFilterRedriven
		mapRun := "arn:aws:states:" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":mapRun:m/label:00000000-0000-4000-8000-000000000000"
		for name, in := range map[string]*sfnapi.ListExecutionsInput{
			"neither ARN":     {},
			"both ARNs":       {StateMachineArn: new(machine), MapRunArn: new(mapRun)},
			"redriveFilter":   {StateMachineArn: new(machine), RedriveFilter: &redriven},
			"PENDING_REDRIVE": {StateMachineArn: new(machine), StatusFilter: &pending},
		} {
			if _, err := call(h, sfnapi.OpListExecutions, in); err == nil || err.ID.Shape.Name() != "ValidationException" || err.ID.Status != 400 {
				h.Errorf("%s = %+v, want ValidationException", name, err)
			}
		}
		if out := must(h, sfnapi.OpListExecutions, &sfnapi.ListExecutionsInput{MapRunArn: new(mapRun), RedriveFilter: &redriven}); len(out.Executions) != 0 {
			h.Errorf("mapRunArn lists %+v, want nothing", out.Executions)
		}
		_, err := call(h, sfnapi.OpListExecutions, &sfnapi.ListExecutionsInput{MapRunArn: new(machine)})
		wantError(h, err, "InvalidArn", 400, "Invalid Arn: 'Resource type not valid in this context: stateMachine'")
	})
}

// Input that is not JSON and is not of a recorded kind answers the
// empty-input text, not Jackson's (the StartExecution note).
func TestSFN08OtherMalformedInput(t *testing.T) {
	// inferred, SFN-08
	run(t, func(h *harness) {
		machine := h.create("m", resultMachine)
		for _, in := range []string{`[1,`, `{"a":1} {"b":2}`, `{"a" 1}`} {
			_, err := call(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, Input: new(in)})
			wantError(h, err, "InvalidExecutionInput", 400, "Invalid State Machine Execution Input: 'Invalid execution input.'")
		}
	})
}

// A failed execution describes as REDRIVABLE without a reason, keeps no
// output, and lists as FAILED (stepfunctions-asl.md 12; aws-recorded
// localstack@8b9a79f v2/base/test_base.snapshot.json:L184-L199); the journal
// shows its move from RUNNING to FAILED.
func TestFailedExecutionDescribeAndJournal(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.start(h.create("f", failMachine), "x", nil)
		d := h.describeExecution(arn)
		if d.RedriveStatusReason != nil || d.Output != nil || d.OutputDetails != nil {
			h.Errorf("DescribeExecution = %+v, want no redriveStatusReason, output or outputDetails", d)
		}
		changes := h.Journal.Query(journal.Filter{Kind: journal.KindStateChange, Limit: 100}).Events
		var seen []journal.StateChange
		for _, e := range changes {
			if c, ok := e.Detail.(journal.StateChange); ok && c.Resource.String() == arn {
				seen = append(seen, c)
			}
		}
		if len(seen) != 1 || seen[0].From != "RUNNING" || seen[0].To != "FAILED" {
			h.Errorf("journal state changes of %s = %+v, want one RUNNING to FAILED", arn, seen)
		}
	})
}
