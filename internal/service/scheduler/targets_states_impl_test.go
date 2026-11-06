package scheduler_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/identity"
)

// executionName is the name understudy gives a Step Functions execution:
// the invocation's execution id, a UUID. UNVERIFIED (SCH-07 recorded no
// Step Functions target), scheduler.md 6.4: the name AWS gives is
// unrecorded.
var executionName = executionID

// A Step Functions target is one StartExecution of the state machine, a
// version or alias ARN passed as given, with the payload as the input
// (scheduler.md 6.4), over SchedulerToStates as a session of the
// execution role, the execution named after the invocation's execution
// id.
func TestStateMachineTargetIsStarted(t *testing.T) {
	run(t, func(h *harness) {
		at := h.now().Add(10 * time.Minute).Truncate(time.Second)
		input := `{"attempt":"<aws.scheduler.attempt-number>","id":"<aws.scheduler.execution-id>"}`
		machine := h.machineArn("flow") + ":live"
		h.create(schedule("sfn", atExpr(at), schedulerapi.Target{Arn: machine, RoleArn: h.roleArn(), Input: new(input)}))
		h.advance(15 * time.Minute)

		ds := h.sink.all()
		if len(ds) != 1 {
			h.Fatalf("%d deliveries, want 1", len(ds))
		}
		r := ds[0].Request
		target, ok := r.Target.(delivery.StateMachine)
		if r.Mechanism != delivery.SchedulerToStates || !ok || r.Invocation == nil {
			h.Fatalf("delivery %+v, want a StateMachine over scheduler-to-states with an invocation", r)
		}
		id := r.Invocation.ExecutionID
		if target.ARN.String() != machine || target.Name != id || !executionName.MatchString(target.Name) {
			h.Errorf("target = %+v, want %s named %s", target, machine, id)
		}
		got := h.callsOf("StartExecution")
		if len(got) != 1 {
			h.Fatalf("%d StartExecution calls, want 1", len(got))
		}
		c := got[0]
		if want := `{"attempt":"1","id":"` + id + `"}`; c.sfn.StateMachineArn != machine || val(c.sfn.Input) != want || val(c.sfn.Name) != id {
			h.Errorf("StartExecution = %s input %s name %s, want %s, %s, %s", c.sfn.StateMachineArn, val(c.sfn.Input), val(c.sfn.Name), machine, want, id)
		}
		if p, ok := c.principal.(identity.AssumedRole); !ok || p.Role.Name != roleName || !sessionName.MatchString(p.Session) || c.origin != schedulerOrigin {
			h.Errorf("StartExecution ran as %#v over %v, want a 32-hex session of %s over Scheduler", c.principal, c.origin, roleName)
		}
	})
}

// A states ARN that names no state machine is refused with
// ValidationException, and a state machine in another Region answers 501,
// as Lambda, SQS and SNS targets there do; nothing is stored. UNVERIFIED
// (SCH-04): the texts are understudy's.
func TestStateMachineTargetRefusals(t *testing.T) {
	run(t, func(h *harness) {
		for _, a := range []string{
			"arn:aws:states:us-east-1:000000000000:activity:a",
			h.machineArn("flow") + ":1:2",
			"arn:aws:states:us-east-1:000000000000:stateMachine:",
		} {
			_, err := call(h, schedulerapi.OpCreateSchedule, schedule("s", "rate(1 hour)", schedulerapi.Target{Arn: a, RoleArn: h.roleArn()}))
			wantError(h, err, "ValidationException", 400, "")
		}
		_, err := call(h, schedulerapi.OpCreateSchedule, schedule("s", "rate(1 hour)",
			schedulerapi.Target{Arn: "arn:aws:states:eu-west-1:000000000000:stateMachine:flow", RoleArn: h.roleArn()}))
		wantError(h, err, "NotImplemented", 501, "")
		wantError(h, errOf(call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: "s"})), "ResourceNotFoundException", 404, "")
	})
}
