package outbox

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/delivery"
)

// A pending Step Functions delivery survives a snapshot with its state
// machine, input and execution name, so a loaded retry starts the same
// execution.
func TestStateMachineEntryRoundTrip(t *testing.T) {
	c := codec()
	st := newState(aws.Scope{})
	e := schedulerEntry()
	machine := aws.ARN{Partition: "aws", Service: "states", Region: "us-east-1", Account: "000000000000", Resource: "stateMachine:flow:live"}
	e.Request.Mechanism = delivery.SchedulerToStates
	e.Request.Target = delivery.StateMachine{ARN: machine, Input: e.Request.Invocation.Template, Name: e.Request.Invocation.ExecutionID}
	e.Request.Invocation.TargetARN = machine.String()
	st.Entries[e.ID] = e
	data, err := c.Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `"target":{"input":"n <aws.scheduler.attempt-number>","kind":"state-machine","machine":"arn:aws:states:us-east-1:000000000000:stateMachine:flow:live","name":"ad06616e51cdf74a"}`
	if !bytes.Contains(data, []byte(want)) {
		t.Errorf("snapshot\n%s\ndoes not hold %s", data, want)
	}
	back := newState(aws.Scope{})
	if err := c.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, st) {
		t.Errorf("round trip =\n%+v\nwant\n%+v", back, st)
	}
}
