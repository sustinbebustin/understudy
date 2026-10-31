package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
)

type startCall struct {
	in        sfnapi.StartExecutionInput
	principal identity.Principal
}

// stubStarts binds sfn.StartExecution to answer err for the nth call (nil
// starts it) and records each call.
func stubStarts(k *kerneltest.K, answer func(n int) *awserr.Error) *recorder[startCall] {
	var calls recorder[startCall]
	k.Stub(sfnapi.OpStartExecution, func(_ context.Context, c *kernel.Call, in *sfnapi.StartExecutionInput) (*sfnapi.StartExecutionOutput, *awserr.Error) {
		n := len(calls.all())
		calls.add(startCall{in: *in, principal: c.Principal()})
		if err := answer(n); err != nil {
			return nil, err
		}
		return &sfnapi.StartExecutionOutput{ExecutionArn: in.StateMachineArn + ":" + *in.Name, StartDate: c.Admitted().Wire()}, nil
	})
	return &calls
}

// A Step Functions target is a StartExecution of the state machine with
// the payload as the execution's input (aws-doc managing-targets-templated;
// scheduler.md 6.4), as the execution role, under the name the producer
// chose; a retried attempt renders its own attempt number and keeps the
// name.
func TestSchedulerStateMachineTargetStartsAnExecution(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		calls := stubStarts(k, func(n int) *awserr.Error {
			if n == 0 {
				return throttled("")
			}
			return nil
		})
		machine := arn("states", "stateMachine:flow")
		tmpl := `{"attempt":"<aws.scheduler.attempt-number>"}`
		target := delivery.StateMachine{ARN: machine, Input: tmpl, Name: schedExecID}
		enqueue(k, o, toSchedulerTarget(delivery.SchedulerToStates, target, machine, tmpl, delivery.PermanentNotRetried, 185, nil), source(k))
		settled(k)
		for range 10 {
			advance(k, time.Minute)
		}
		got := calls.all()
		if len(got) != 2 {
			k.Fatalf("%d StartExecution calls, want the throttled one and its retry", len(got))
		}
		for i, c := range got {
			want := `{"attempt":"` + string(rune('1'+i)) + `"}`
			if c.in.StateMachineArn != machine.String() || deref(c.in.Input) != want || deref(c.in.Name) != schedExecID {
				k.Errorf("StartExecution %d = %s input %q name %q, want %s, %s, %s", i, c.in.StateMachineArn, deref(c.in.Input), deref(c.in.Name), machine, want, schedExecID)
			}
			wantRoleSession(k, "StartExecution", c.principal)
		}
		noAdvisories(k)
	}, kerneltest.Services(sfnapi.Service()))
}

// A missing state machine is a client-side error: dead-lettered at once
// with the target's own code and the universal TARGET_ARN
// aws-sdk:sfn:startExecution (aws-doc managing-targets-universal names the
// sfn service; scheduler.md 7.2). UNVERIFIED (SCH-08): the spelling.
func TestSchedulerStateMachineTargetDeadLetter(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		calls := stubStarts(k, func(int) *awserr.Error {
			return sfnapi.ErrStateMachineDoesNotExist("State Machine Does Not Exist: 'flow'")
		})
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		dlq := arn("sqs", "sch-dlq")
		machine := arn("states", "stateMachine:flow")
		target := delivery.StateMachine{ARN: machine, Input: "{}", Name: schedExecID}
		enqueue(k, o, toSchedulerTarget(delivery.SchedulerToStates, target, machine, "{}", delivery.PermanentNotRetried, 185, &dlq), source(k))
		settled(k)
		if n := len(calls.all()); n != 1 {
			k.Errorf("%d StartExecution calls, want 1: a client-side error is not retried", n)
		}
		got := q.sent()
		if len(got) != 1 || got[0].in.MessageBody != "{}" {
			k.Fatalf("sent %+v, want one dead letter with body {}", got)
		}
		for name, want := range map[string]string{
			"ERROR_CODE":     "StateMachineDoesNotExist",
			"RETRY_ATTEMPTS": "0",
			"TARGET_ARN":     "arn:aws:scheduler:::aws-sdk:sfn:startExecution",
		} {
			if v := stringAttr(got[0], name); v != want {
				k.Errorf("%s = %q, want %q", name, v, want)
			}
		}
	}, kerneltest.Services(sfnapi.Service()))
}

// The outbox takes a StateMachine target only under SchedulerToStates,
// naming a state machine (a version or alias too) and an execution name.
func TestStateMachineTargetIsChecked(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		cause := source(k)
		machine := arn("states", "stateMachine:flow")
		ok := func(target delivery.StateMachine) delivery.Request {
			return toSchedulerTarget(delivery.SchedulerToStates, target, target.ARN, "{}", delivery.PermanentNotRetried, 185, nil)
		}
		for _, a := range []string{"stateMachine:flow", "stateMachine:flow:1", "stateMachine:flow:live"} {
			if _, err := o.Enqueue(ok(delivery.StateMachine{ARN: arn("states", a), Name: "n"}), cause, k.Clock.Now()); err != nil {
				k.Errorf("Enqueue of a StartExecution of %s = %v, want accepted", a, err)
			}
		}
		for name, req := range map[string]delivery.Request{
			"an activity":          ok(delivery.StateMachine{ARN: arn("states", "activity:a"), Name: "n"}),
			"two qualifiers":       ok(delivery.StateMachine{ARN: arn("states", "stateMachine:flow:1:2"), Name: "n"}),
			"no execution name":    ok(delivery.StateMachine{ARN: machine}),
			"another mechanism":    toSchedulerTarget(delivery.SchedulerToSQS, delivery.StateMachine{ARN: machine, Name: "n"}, machine, "{}", delivery.PermanentNotRetried, 185, nil),
			"a queue under states": toSchedulerTarget(delivery.SchedulerToStates, delivery.SQSQueue{ARN: arn("sqs", "q")}, machine, "{}", delivery.PermanentNotRetried, 185, nil),
		} {
			if _, err := o.Enqueue(req, cause, k.Clock.Now()); err == nil {
				k.Errorf("Enqueue with %s accepted", name)
			}
		}
	})
}
