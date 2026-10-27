package sfn

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TestSFN03SameNameComparesInputBytes: a StartExecution with the name of a
// running execution answers that execution only when its input is the same
// bytes; JSON-equal input written differently answers
// ExecutionAlreadyExists (stepfunctions-api.md 6.1 leaves byte or JSON
// equality open). The running execution is placed in the state directly, so
// it cannot finish before the second start.
func TestSFN03SameNameComparesInputBytes(t *testing.T) {
	// inferred, SFN-03
	kerneltest.Run(t, func(k *kerneltest.K) {
		s := New(k.Deps(sfnapi.Service()), Options{})
		def := `{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}`
		machine, err := k.Invoke(k.Context(), sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "m", Definition: def, RoleArn: "arn:aws:iam::" + k.Scope.Account.String() + ":role/r"})
		if err != nil {
			k.Fatalf("CreateStateMachine = %v", err.Message)
		}
		started := clock.FromWire(machine.CreationDate)
		store.Update(s.cells.For(k.Scope), func(st *State) (struct{}, bool) {
			st.Executions["m:run"] = Execution{Machine: "m", Name: "run", Input: `{"a":1}`, Started: started, Run: st.Machines["m"]}
			return struct{}{}, true
		})
		arn := executionRef{machine: machineRef{scope: k.Scope, name: "m"}, name: "run"}.arn().String()

		same, err := k.Invoke(k.Context(), sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine.StateMachineArn, Name: new("run"), Input: new(`{"a":1}`)})
		if err != nil || same.ExecutionArn != arn || !same.StartDate.Equal(started.Wire()) {
			k.Fatalf("same bytes = %+v, %v; want the running execution %s", same, err, arn)
		}
		_, err = k.Invoke(k.Context(), sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: machine.StateMachineArn, Name: new("run"), Input: new(`{ "a": 1 }`)})
		if err == nil || err.ID.Shape.Name() != "ExecutionAlreadyExists" || err.Message != "Execution Already Exists: '"+arn+"'" {
			k.Fatalf("JSON-equal input in other bytes = %+v, want ExecutionAlreadyExists", err)
		}
	}, kerneltest.Services(sfnapi.Service()))
}
