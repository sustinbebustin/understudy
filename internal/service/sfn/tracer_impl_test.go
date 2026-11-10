package sfn_test

import (
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// A snapshot round trip keeps the machine and a finished execution with
// its history, the history compared event by event rather than by shape
// (Stateful, ADR 0009).
func TestSnapshotRoundTripKeepsMachineAndExecution(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("m", resultMachine)
		arn := h.start(machine, "x", new(`{"a":1}`))
		describe := h.describeExecution(arn)
		history := h.history(arn)
		sections, err := h.svc.Export()
		if err != nil || len(sections) == 0 {
			h.Fatalf("Export = %d sections, %v", len(sections), err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			h.Fatalf("Import = %v", err)
		}
		if diff := cmp.Diff(describe, h.describeExecution(arn)); diff != "" {
			h.Errorf("DescribeExecution after the round trip (-before +after):\n%s", diff)
		}
		if diff := cmp.Diff(history, h.history(arn)); diff != "" {
			h.Errorf("history after the round trip (-before +after):\n%s", diff)
		}
		if d := must(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: machine}); d.Definition != resultMachine {
			h.Errorf("definition after the round trip = %q", d.Definition)
		}
	})
}

// A Succeed state ends the execution with its input as output, through the
// service (stepfunctions-asl.md 11.3 Succeed row).
func TestSucceedExecution(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("s", `{"StartAt":"P","States":{"P":{"Type":"Pass","Next":"S"},"S":{"Type":"Succeed"}}}`)
		arn := h.start(machine, "x", new(`{"k":"v"}`))
		if d := h.describeExecution(arn); d.Status != sfnapi.ExecutionStatusSucceeded || !sameJSON(h, val(d.Output), `{"k":"v"}`) {
			h.Fatalf("DescribeExecution = %+v", d)
		}
		got := shape(h.history(arn))
		want := "ExecutionStarted 1<-0\nPassStateEntered 2<-0\nPassStateExited 3<-2\nSucceedStateEntered 4<-3\nSucceedStateExited 5<-4\nExecutionSucceeded 6<-5"
		if got != want {
			h.Fatalf("history:\n%s\nwant:\n%s", got, want)
		}
	})
}

// CreateStateMachine renders every problem in one InvalidDefinition
// message, schema problems first (ASL-01 16, recorded 2026-11-03).
func TestASL01CreateJoinsEveryProblem(t *testing.T) {
	run(t, func(h *harness) {
		def := `{"StartAt":"A","States":{"A":{"Type":"Pass","Next":"Nope"},"B":{"Type":"Bogus"}}}`
		_, err := call(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "m", Definition: def, RoleArn: h.roleArn()})
		want := `Invalid State Machine Definition: 'SCHEMA_VALIDATION_FAILED: The field 'Type' should have one of these values: [Task, Wait, Pass, Succeed, Fail, Choice, Parallel, Map] at /States/B/Type, ` +
			`MISSING_TRANSITION_TARGET: Missing 'Next' target: Nope at /States/A/Next, MISSING_TRANSITION_TARGET: State "B" is not reachable. at /States/B, MISSING_END_STATE: Workflow has no terminal state at null'`
		if err == nil || err.ID.Shape.Name() != "InvalidDefinition" || err.Message != want {
			h.Fatalf("CreateStateMachine = %+v, want InvalidDefinition %q", err, want)
		}
	})
}

// Input that is not JSON, other than the recorded cases, answers
// 'Invalid execution input.'.
func TestSFN08MalformedInput(t *testing.T) {
	// inferred, SFN-08
	run(t, func(h *harness) {
		machine := h.create("m", resultMachine)
		_, err := call(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, Input: new(`[1,`)})
		wantError(h, err, "InvalidExecutionInput", 400, "Invalid State Machine Execution Input: 'Invalid execution input.'")
	})
}

// includeExecutionData false leaves out input and output and keeps the
// *Details members.
func TestSFN05HistoryWithoutExecutionData(t *testing.T) {
	// inferred, SFN-05
	run(t, func(h *harness) {
		arn := h.start(h.create("m", resultMachine), "x", new(`{"a":1}`))
		events := must(h, sfnapi.OpGetExecutionHistory, &sfnapi.GetExecutionHistoryInput{ExecutionArn: arn, IncludeExecutionData: new(false)}).Events
		if len(events) != 4 {
			h.Fatalf("history:\n%s", shape(events))
		}
		started, entered, exited, succeeded := events[0].ExecutionStartedEventDetails, events[1].StateEnteredEventDetails, events[2].StateExitedEventDetails, events[3].ExecutionSucceededEventDetails
		switch {
		case started == nil || started.Input != nil || started.InputDetails == nil || val(started.RoleArn) != h.roleArn():
			h.Errorf("ExecutionStarted details = %+v", started)
		case entered == nil || entered.Input != nil || entered.InputDetails == nil || entered.Name != "P":
			h.Errorf("PassStateEntered details = %+v", entered)
		case exited == nil || exited.Output != nil || exited.OutputDetails == nil:
			h.Errorf("PassStateExited details = %+v", exited)
		case succeeded == nil || succeeded.Output != nil || succeeded.OutputDetails == nil:
			h.Errorf("ExecutionSucceeded details = %+v", succeeded)
		}
	})
}

// History pages carry tokens of their own listing only: a token from the
// forward listing is refused in reverse order, with the InvalidToken text
// SFN-05 recorded 2026-11-03 for a bad history token (stepfunctions-api.md
// 3.2).
func TestHistoryTokenBelongsToItsListing(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.start(h.create("m", resultMachine), "x", nil)
		page := must(h, sfnapi.OpGetExecutionHistory, &sfnapi.GetExecutionHistoryInput{ExecutionArn: arn, MaxResults: 1})
		if page.NextToken == nil {
			h.Fatal("no token after one event of four")
		}
		_, err := call(h, sfnapi.OpGetExecutionHistory, &sfnapi.GetExecutionHistoryInput{ExecutionArn: arn, NextToken: page.NextToken, ReverseOrder: true})
		wantError(h, err, "InvalidToken", 400, "Invalid Token")
		_, err = call(h, sfnapi.OpGetExecutionHistory, &sfnapi.GetExecutionHistoryInput{ExecutionArn: arn, NextToken: new("garbage")})
		wantError(h, err, "InvalidToken", 400, "Invalid Token")
		rest := must(h, sfnapi.OpGetExecutionHistory, &sfnapi.GetExecutionHistoryInput{ExecutionArn: arn, NextToken: page.NextToken})
		if len(rest.Events) != 3 || rest.Events[0].Id != 2 || rest.NextToken != nil {
			h.Errorf("second page:\n%s", shape(rest.Events))
		}
	})
}

// A reverse-order history page token resumes after the last event it
// returned, even when the running execution wrote events between the
// pages: a page never repeats or skips an event.
func TestReverseHistoryPagingOfARunningExecution(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("w", `{"StartAt":"A","States":{"A":{"Type":"Wait","Seconds":10,"Next":"B"},"B":{"Type":"Wait","Seconds":10,"End":true}}}`)
		arn := h.start(machine, "x", nil)
		first := must(h, sfnapi.OpGetExecutionHistory, &sfnapi.GetExecutionHistoryInput{ExecutionArn: arn, MaxResults: 1, ReverseOrder: true})
		if len(first.Events) != 1 || first.Events[0].Id != 2 || first.NextToken == nil {
			h.Fatalf("first page:\n%s", shape(first.Events))
		}
		h.advance(10 * time.Second)
		rest := must(h, sfnapi.OpGetExecutionHistory, &sfnapi.GetExecutionHistoryInput{ExecutionArn: arn, NextToken: first.NextToken, ReverseOrder: true})
		if len(rest.Events) != 1 || rest.Events[0].Id != 1 || rest.NextToken != nil {
			h.Errorf("second page after two more events, want only event 1:\n%s", shape(rest.Events))
		}
	})
}

// State machine and execution names follow the character rules of
// stepfunctions-api.md 4.2; the text is the recorded InvalidName form,
// whose use for state machines and executions is UNVERIFIED (SFN-08).
func TestSFN08InvalidNames(t *testing.T) {
	// inferred, SFN-08
	run(t, func(h *harness) {
		control := "a" + string(rune(0x01)) + "b"
		for _, name := range []string{"a b", "a/b", "a:b", "a\tb", control} {
			_, err := call(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: name, Definition: resultMachine, RoleArn: h.roleArn()})
			wantError(h, err, "InvalidName", 400, "Invalid Name: '"+name+"'")
		}
		machine := h.create("m", resultMachine)
		_, err := call(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine, Name: new("x y")})
		wantError(h, err, "InvalidName", 400, "Invalid Name: 'x y'")
	})
}

// A generated execution name is a v4 UUID (stepfunctions-api.md 6.1).
func TestGeneratedExecutionName(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.start(h.create("m", resultMachine), "", nil)
		name := val(h.describeExecution(arn).Name)
		if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(name) {
			h.Errorf("generated name %q, want a v4 UUID", name)
		}
	})
}

// Every recording id a coverage note of the sfn bindings names has a test
// under internal/service/sfn commented "inferred, <id>".
func TestEveryNoteRecordingIDHasAnInferredTest(t *testing.T) {
	var notes []string
	run(t, func(h *harness) {
		for _, op := range sfnapi.Service().Operations {
			if b, ok := h.Kernel.Bindings().Status(op).(kernel.Bound); ok {
				if p, ok := b.Coverage.(registry.Partial); ok {
					notes = append(notes, p.Note)
				}
			}
		}
	})
	ids := regexp.MustCompile(`\b(?:ASL|SFN)-\d{2}\b`).FindAllString(strings.Join(notes, "\n"), -1)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		t.Fatal("no coverage note names a recording id; the check found nothing to check")
	}
	var sources strings.Builder
	root, err := os.OpenRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	files := root.FS()
	err = fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, err := fs.ReadFile(files, path)
		sources.Write(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if !strings.Contains(sources.String(), "// inferred, "+id+"\n") {
			t.Errorf("recording id %s is named in a coverage note but no test under internal/service/sfn is commented \"// inferred, %s\"", id, id)
		}
	}
}
