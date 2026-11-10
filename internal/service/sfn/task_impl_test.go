package sfn_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// A Task invokes a function in another account in that account's scope,
// synchronously (RequestResponse), as tracked work whose call is an
// internal sfn-task call with a journal parent (ADR 0007).
func TestTaskInvokeTarget(t *testing.T) {
	run(t, func(h *harness) {
		fn := "arn:aws:lambda:eu-west-1:111111111111:function:remote"
		parsed, err := aws.ParseARN(fn)
		if err != nil {
			h.Fatal(err)
		}
		other, err := aws.ScopeOf(parsed)
		if err != nil {
			h.Fatal(err)
		}
		type seen struct {
			scope  aws.Scope
			origin ref.Origin
			parent ref.EventID
		}
		got := make(chan seen, 1)
		h.answer(func(c *kernel.Call, _ *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			got <- seen{scope: c.Scope(), origin: c.Origin(), parent: c.Cause().Parent}
			return &lambdaapi.InvokeOutput{StatusCode: 200, Payload: []byte(`{}`)}, nil
		})
		h.start(h.invokeTaskMachine("m", fn), "x", new(`{}`))
		s := <-got
		if s.scope != other {
			h.Errorf("invoked in %+v, want the function's scope %+v", s.scope, other)
		}
		if s.origin != (ref.Internal{Via: ref.SFNTask}) || s.parent == 0 {
			h.Errorf("origin %#v parent %d, want an internal sfn-task call with a parent", s.origin, s.parent)
		}
		if calls := h.invoked(); len(calls) != 1 || calls[0].in.InvocationType == nil || *calls[0].in.InvocationType != lambdaapi.InvocationTypeRequestResponse {
			h.Errorf("Invoke calls = %+v, want one RequestResponse", calls)
		}
	})
}

// Task TimeoutSeconds is emulator time while the function runs on wall
// time: advancing the clock past it times the task out, and the function's
// late result is dropped (stepfunctions-asl.md 8.6, 9.1).
func TestTaskTimeoutOnEmulatorClock(t *testing.T) {
	run(t, func(h *harness) {
		started, release := make(chan struct{}), make(chan struct{})
		h.answer(func(_ *kernel.Call, _ *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			close(started)
			<-release
			return &lambdaapi.InvokeOutput{StatusCode: 200, Payload: []byte(`{"late":true}`)}, nil
		})
		def := fmt.Sprintf(`{"StartAt":"T","States":{"T":{"Type":"Task","Resource":%q,"TimeoutSeconds":5,"End":true}}}`, h.functionArn("slow"))
		out := must(h, sfnapi.OpStartExecution, &sfnapi.StartExecutionInput{StateMachineArn: h.create("m", def), Name: new("x")})
		select {
		case <-started:
		case <-time.After(settle):
			close(release)
			h.Fatal("the Task never invoked its function")
		}
		if _, err := h.Clock.Advance(6 * time.Second); err != nil {
			close(release)
			h.Fatal(err)
		}
		close(release)
		h.settled()
		d := h.describeExecution(out.ExecutionArn)
		if d.Status != sfnapi.ExecutionStatusFailed || val(d.Error) != "States.Timeout" || d.Cause != nil {
			h.Errorf("DescribeExecution = %s %s %q, want FAILED States.Timeout without a cause", d.Status, val(d.Error), val(d.Cause))
		}
		want := "ExecutionStarted 1<-0\nTaskStateEntered 2<-0\nLambdaFunctionScheduled 3<-2\nLambdaFunctionStarted 4<-3\nLambdaFunctionTimedOut 5<-4\nExecutionFailed 6<-5"
		if got := shape(h.history(out.ExecutionArn)); got != want {
			h.Errorf("history:\n%s\nwant:\n%s", got, want)
		}
	})
}

// A function error whose payload has no errorType is Lambda.Unknown, the
// documented name for an unhandled error (stepfunctions-api.md 9.4); a
// payload that is not JSON is the task result as a string.
func TestFunctionPayloadEdges(t *testing.T) {
	run(t, func(h *harness) {
		h.answer(func(_ *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			if in.FunctionName == h.functionArn("raw") {
				return &lambdaapi.InvokeOutput{StatusCode: 200, Payload: []byte(`not json`)}, nil
			}
			return &lambdaapi.InvokeOutput{StatusCode: 200, FunctionError: new("Unhandled"), Payload: []byte(`{"errorMessage":"x"}`)}, nil
		})
		d := h.describeExecution(h.start(h.legacyTaskMachine("unknown", h.functionArn("bad")), "x", new(`{}`)))
		if val(d.Error) != "Lambda.Unknown" || val(d.Cause) != `{"errorMessage":"x"}` {
			h.Errorf("no errorType: %s %q, want Lambda.Unknown with the payload", val(d.Error), val(d.Cause))
		}
		d = h.describeExecution(h.start(h.legacyTaskMachine("raw", h.functionArn("raw")), "x", new(`{}`)))
		if d.Status != sfnapi.ExecutionStatusSucceeded || val(d.Output) != `"not json"` {
			h.Errorf("non-JSON payload: %s %s, want the text as a JSON string", d.Status, val(d.Output))
		}
	})
}

// lambda:invoke's TaskScheduled and TaskSucceeded details reach the wire,
// and includeExecutionData false removes the result but keeps the members
// (inferred, SFN-05 as for the other events).
func TestTaskEventsOnTheWire(t *testing.T) {
	run(t, func(h *harness) {
		def := fmt.Sprintf(`{"StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke","TimeoutSeconds":9,"Parameters":{"FunctionName":%q,"Payload":{"a":1}},"End":true}}}`, h.functionArn("f"))
		arn := h.start(h.create("m", def), "x", new(`{}`))
		events := h.history(arn)
		sched := events[2].TaskScheduledEventDetails
		if sched == nil || sched.Region != h.Scope.Region.String() || sched.TimeoutInSeconds == nil || *sched.TimeoutInSeconds != 9 ||
			!sameJSON(h, sched.Parameters, fmt.Sprintf(`{"FunctionName":%q,"Payload":{"a":1}}`, h.functionArn("f"))) {
			h.Errorf("TaskScheduled details = %+v", sched)
		}
		out := must(h, sfnapi.OpGetExecutionHistory, &sfnapi.GetExecutionHistoryInput{ExecutionArn: arn, IncludeExecutionData: new(false)})
		ok := out.Events[4].TaskSucceededEventDetails
		if ok == nil || ok.Output != nil || ok.OutputDetails == nil || ok.Resource != "invoke" {
			h.Errorf("TaskSucceeded without execution data = %+v, want no output and the other members", ok)
		}
	})
}
