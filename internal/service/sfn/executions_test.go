package sfn_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
)

// The tracer bullet: one Pass state through the service. The execution
// succeeds with the Result as output and writes ExecutionStarted 1<-0,
// PassStateEntered 2<-0, PassStateExited 3<-2, ExecutionSucceeded 4<-3
// (stepfunctions-asl.md 11.2 rules 1, 2 and 4, 11.3 Pass row; aws-recorded
// localstack@8b9a79f v2/base/test_base.snapshot.json:L264-L277). A succeeded
// execution describes with redriveCount 0, NOT_REDRIVABLE and the recorded
// reason (stepfunctions-api.md 6.3; aws-recorded localstack@8b9a79f
// v2/test_sfn_api.snapshot.json:L1537-L1581).
func TestTracerBulletPassExecution(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("m", resultMachine)
		started := must(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, Name: new("run-1"), Input: new(`{"a":1}`)})
		if started.ExecutionArn != h.executionArn("m", "run-1") || !started.StartDate.Equal(h.now()) {
			h.Errorf("StartExecution = %+v, want the execution ARN of stepfunctions-api.md 4.1, started now", started)
		}
		h.settled()
		d := h.describeExecution(started.ExecutionArn)
		if d.Status != sfnapi.ExecutionStatusSucceeded || !sameJSON(h, val(d.Output), `{"done":true}`) || !sameJSON(h, val(d.Input), `{"a":1}`) ||
			d.StateMachineArn != machine || val(d.Name) != "run-1" || d.StopDate == nil {
			h.Fatalf("DescribeExecution = %+v, want SUCCEEDED with the Result", d)
		}
		if d.RedriveCount == nil || *d.RedriveCount != 0 || d.RedriveStatus == nil || *d.RedriveStatus != sfnapi.ExecutionRedriveStatusNotRedrivable ||
			val(d.RedriveStatusReason) != "Execution is SUCCEEDED and cannot be redriven" {
			h.Errorf("redrive members = %v %v %q, want 0, NOT_REDRIVABLE and the recorded reason", d.RedriveCount, d.RedriveStatus, val(d.RedriveStatusReason))
		}
		if d.InputDetails == nil || !d.InputDetails.Included || d.OutputDetails == nil || !d.OutputDetails.Included {
			h.Errorf("inputDetails %+v outputDetails %+v, want included true", d.InputDetails, d.OutputDetails)
		}
		events := h.history(started.ExecutionArn)
		if got, want := shape(events), "ExecutionStarted 1<-0\nPassStateEntered 2<-0\nPassStateExited 3<-2\nExecutionSucceeded 4<-3"; got != want {
			h.Fatalf("history:\n%s\nwant:\n%s", got, want)
		}
		if s := events[0].ExecutionStartedEventDetails; s == nil || val(s.RoleArn) != h.roleArn() || !sameJSON(h, val(s.Input), `{"a":1}`) || s.InputDetails == nil || s.InputDetails.Truncated {
			h.Errorf("ExecutionStarted details = %+v, want input, inputDetails and roleArn (test_sfn_api.snapshot.json:L313-L326)", s)
		}
		if x := events[2].StateExitedEventDetails; x == nil || x.Name != "P" || !sameJSON(h, val(x.Output), `{"done":true}`) {
			h.Errorf("PassStateExited details = %+v", x)
		}
		for i := range events {
			if !events[i].Timestamp.Equal(started.StartDate) {
				h.Errorf("%s at %v, want %v: steps run on the emulator clock, which did not move", events[i].Type, events[i].Timestamp, started.StartDate)
			}
		}
		reversed := must(h, sfnapi.OpGetExecutionHistory, &sfnapi.GetExecutionHistoryInput{ExecutionArn: started.ExecutionArn, ReverseOrder: true}).Events
		if len(reversed) != 4 || reversed[0].Id != 4 || reversed[3].Id != 1 {
			h.Errorf("reverseOrder history:\n%s", shape(reversed))
		}
		page := must(h, sfnapi.OpGetExecutionHistory, &sfnapi.GetExecutionHistoryInput{ExecutionArn: started.ExecutionArn, MaxResults: 3})
		if len(page.Events) != 3 || page.NextToken == nil {
			h.Errorf("maxResults 3 page = %d events, token %v", len(page.Events), page.NextToken)
		}
		sm := must(h, sfnapi.OpDescribeStateMachineForExecution, &sfnapi.DescribeStateMachineForExecutionInput{ExecutionArn: started.ExecutionArn})
		if sm.StateMachineArn != machine || sm.Definition != resultMachine || sm.RoleArn != h.roleArn() || sm.Name != "m" {
			h.Errorf("DescribeStateMachineForExecution = %+v", sm)
		}
	})
}

// StartExecution input texts (stepfunctions-api.md 6.1; aws-recorded
// localstack@8b9a79f v2/test_sfn_api.snapshot.json:L402-L470): the empty
// string and non-JSON answer InvalidExecutionInput with the recorded
// Jackson texts; numbers, strings and null start; an omitted input
// describes as {} (L1556-L1561).
func TestStartExecutionInput(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("m", resultMachine)
		for in, msg := range map[string]string{
			"":               "Invalid State Machine Execution Input: 'Invalid execution input.'",
			"not some json":  "Invalid State Machine Execution Input: 'Unrecognized token 'not': was expecting (JSON String, Number, Array, Object or token 'null', 'true' or 'false')'",
			"{'not': 'json'": "Invalid State Machine Execution Input: 'Unexpected character (''' (code 39)): was expecting double-quote to start field name'",
		} {
			_, err := call(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, Input: new(in)})
			wantError(h, err, "InvalidExecutionInput", 400, msg)
		}
		for _, in := range []string{"2", `"some text"`, "null"} {
			arn := h.start(machine, "", new(in))
			if d := h.describeExecution(arn); d.Status != sfnapi.ExecutionStatusSucceeded || val(d.Input) != in {
				h.Errorf("input %s: %+v", in, d)
			}
		}
		arn := h.start(machine, "omitted", nil)
		if d := h.describeExecution(arn); val(d.Input) != "{}" {
			h.Errorf("omitted input describes as %q, want {}", val(d.Input))
		}
	})
}

// StartExecution input is limited to 262,144 bytes in UTF-8, not
// characters, and the refusal is Coral's with AWS's "size ... bytes in
// UTF-8 encoding" wording (aws-recorded testdata/parity/sfn/SFN-08/
// 07-big-input.json, ASL-15/01-start-over.json). 131,073 two-byte
// characters are fewer than 262,144 characters but 262,146 bytes.
func TestStartExecutionInputSizeInUTF8Bytes(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("m", resultMachine)
		atLimit := `"` + strings.Repeat("a", 262142) + `"`
		arn := h.start(machine, "at-limit", &atLimit)
		if d := h.describeExecution(arn); d.Status != sfnapi.ExecutionStatusSucceeded {
			h.Errorf("input of 262,144 ASCII bytes: status %s, want SUCCEEDED", d.Status)
		}
		for name, in := range map[string]string{
			"262,145 ASCII bytes":         `"` + strings.Repeat("a", 262143) + `"`,
			"131,073 two-byte characters": strings.Repeat("é", 131073),
		} {
			_, err := call(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, Input: &in})
			wantError(h, err, "ValidationException", 400, "1 validation error detected: Value at 'input' failed to satisfy constraint: Member must have size less than or equal to 262144 bytes in UTF-8 encoding")
			if got := err.ID.JSONType; got != "com.amazon.coral.validate#ValidationException" {
				h.Errorf("%s: __type %s, want com.amazon.coral.validate#ValidationException", name, got)
			}
		}
	})
}

// Same name and input while running answers the same execution; a
// different input, or a closed execution, answers ExecutionAlreadyExists
// (stepfunctions-api.md 6.1; aws-recorded localstack@8b9a79f
// v2/test_sfn_api.snapshot.json:L2265-L2311; aws-doc api/API_StartExecution).
func TestStartExecutionIdempotency(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("w", waitMachine)
		first := must(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, Name: new("same"), Input: new(`{"k":1}`)})
		h.settled()
		again := must(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, Name: new("same"), Input: new(`{"k":1}`)})
		if again.ExecutionArn != first.ExecutionArn || !again.StartDate.Equal(first.StartDate) {
			h.Errorf("repeat start = %+v, want %+v", again, first)
		}
		_, err := call(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, Name: new("same"), Input: new(`{"k":2}`)})
		wantError(h, err, "ExecutionAlreadyExists", 400, "Execution Already Exists: '"+first.ExecutionArn+"'")

		pass := h.create("p", resultMachine)
		closed := h.start(pass, "done", new(`{}`))
		_, err = call(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: pass, Name: new("done"), Input: new(`{}`)})
		wantError(h, err, "ExecutionAlreadyExists", 400, "Execution Already Exists: '"+closed+"'")
	})
}

// ListExecutions lists newest first with redriveCount 0
// (stepfunctions-api.md 6.5; aws-recorded localstack@8b9a79f
// v2/test_sfn_api.snapshot.json:L2094-L2140, L301) and filters by status;
// a status outside the enum answers the recorded text in the recorded order
// (L1672).
func TestListExecutions(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("w", waitMachine)
		arns := make([]string, 0, 3)
		for i := range 3 {
			arns = append(arns, h.start(machine, fmt.Sprintf("e%d", i), nil))
			h.advance(time.Second)
		}
		out := must(h, sfnapi.OpListExecutions, &sfnapi.ListExecutionsInput{StateMachineArn: new(machine)})
		got := make([]string, len(out.Executions))
		for i, e := range out.Executions {
			got[i] = e.ExecutionArn
			if e.Status != sfnapi.ExecutionStatusRunning || e.StateMachineArn != machine || e.RedriveCount == nil || *e.RedriveCount != 0 {
				h.Errorf("item %+v, want RUNNING with redriveCount 0", e)
			}
		}
		if want := []string{arns[2], arns[1], arns[0]}; !slices.Equal(got, want) {
			h.Errorf("executions = %v, want newest first %v", got, want)
		}
		succeeded := sfnapi.ExecutionStatusSucceeded
		if none := must(h, sfnapi.OpListExecutions, &sfnapi.ListExecutionsInput{StateMachineArn: new(machine), StatusFilter: &succeeded}).Executions; len(none) != 0 {
			h.Errorf("SUCCEEDED filter lists %d running executions", len(none))
		}
		bad := sfnapi.ExecutionStatus("NOPE")
		_, err := call(h, sfnapi.OpListExecutions, &sfnapi.ListExecutionsInput{StateMachineArn: new(machine), StatusFilter: &bad})
		wantError(h, err, "ValidationException", 400, "1 validation error detected: Value 'NOPE' at 'statusFilter' failed to satisfy constraint: Member must satisfy enum value set: [SUCCEEDED, TIMED_OUT, PENDING_REDRIVE, ABORTED, FAILED, RUNNING]")
	})
}

// StopExecution of an execution in a Wait ends it ABORTED with history
// ExecutionStarted, WaitStateEntered, ExecutionAborted 3<-0 and empty
// details (stepfunctions-api.md 6.4; aws-recorded localstack@8b9a79f
// v2/test_sfn_api.snapshot.json:L472-L539).
func TestStopExecutionDuringWait(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("w", waitMachine)
		arn := h.start(machine, "s", nil)
		h.advance(time.Minute)
		stopped := must(h, sfnapi.OpStopExecution, &sfnapi.StopExecutionInput{ExecutionArn: arn})
		if !stopped.StopDate.Equal(h.now()) {
			h.Errorf("stopDate = %v, want now %v", stopped.StopDate, h.now())
		}
		h.settled()
		if d := h.describeExecution(arn); d.Status != sfnapi.ExecutionStatusAborted || d.StopDate == nil || !d.StopDate.Equal(stopped.StopDate) {
			h.Errorf("DescribeExecution = %+v, want ABORTED at the stop date", d)
		}
		events := h.history(arn)
		if got, want := shape(events), "ExecutionStarted 1<-0\nWaitStateEntered 2<-0\nExecutionAborted 3<-0"; got != want {
			h.Fatalf("history:\n%s\nwant:\n%s", got, want)
		}
		if a := events[2].ExecutionAbortedEventDetails; a == nil || a.Error != nil || a.Cause != nil {
			h.Errorf("ExecutionAborted details = %+v, want {}", a)
		}
	})
}

// A failed execution describes with its error and cause and REDRIVABLE
// (stepfunctions-asl.md 12; aws-recorded localstack@8b9a79f
// v2/base/test_base.snapshot.json:L184-L199); a Fail state with neither
// leaves both out (same file L181-L232).
func TestFailedExecutionDescribe(t *testing.T) {
	run(t, func(h *harness) {
		failing := h.create("f", `{"StartAt":"F","States":{"F":{"Type":"Fail","Error":"Bad","Cause":"because"}}}`)
		d := h.describeExecution(h.start(failing, "x", nil))
		if d.Status != sfnapi.ExecutionStatusFailed || val(d.Error) != "Bad" || val(d.Cause) != "because" || d.Output != nil {
			h.Errorf("DescribeExecution = %+v, want FAILED Bad/because", d)
		}
		if d.RedriveStatus == nil || *d.RedriveStatus != sfnapi.ExecutionRedriveStatusRedrivable || d.RedriveCount == nil || *d.RedriveCount != 0 {
			h.Errorf("redrive members = %v %v, want REDRIVABLE and 0", d.RedriveStatus, d.RedriveCount)
		}
		empty := h.create("e", `{"StartAt":"F","States":{"F":{"Type":"Fail"}}}`)
		d = h.describeExecution(h.start(empty, "x", nil))
		if d.Status != sfnapi.ExecutionStatusFailed || d.Error != nil || d.Cause != nil {
			h.Errorf("empty Fail describes as %+v, want FAILED with no error or cause", d)
		}
	})
}

// Running executions keep the definition and role they started with;
// DescribeStateMachineForExecution returns them (stepfunctions-api.md 5.3,
// 6.3; aws-recorded localstack@8b9a79f v2/test_sfn_api.snapshot.json:L1174-L1223).
func TestRunningExecutionKeepsItsDefinition(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("w", waitMachine)
		arn := h.start(machine, "old", nil)
		must(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: machine, Definition: new(resultMachine)})
		sm := must(h, sfnapi.OpDescribeStateMachineForExecution, &sfnapi.DescribeStateMachineForExecutionInput{ExecutionArn: arn})
		if sm.Definition != waitMachine {
			h.Errorf("definition for the running execution = %q, want the one it started with", sm.Definition)
		}
		h.advance(time.Hour)
		if d := h.describeExecution(arn); d.Status != sfnapi.ExecutionStatusSucceeded || !sameJSON(h, val(d.Output), `{}`) {
			h.Errorf("old execution = %+v, want it to finish its Wait and succeed with its input", d)
		}
	})
}

// traceHeader is stored and returned by DescribeExecution
// (stepfunctions-api.md 6.1).
func TestTraceHeaderIsKept(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("m", resultMachine)
		out := must(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, TraceHeader: new("Root=1-5759e988-bd862e3fe1be46a994272793")})
		h.settled()
		if d := h.describeExecution(out.ExecutionArn); val(d.TraceHeader) != "Root=1-5759e988-bd862e3fe1be46a994272793" {
			h.Errorf("traceHeader = %q", val(d.TraceHeader))
		}
	})
}
