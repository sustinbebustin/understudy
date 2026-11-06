package lambda_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
)

// roleGate is an Authorizer that refuses every call a function's execution
// role makes while refusing is set, allows every other call, and records
// each action it was asked about with the mode of the call.
type roleGate struct {
	refusing atomic.Bool
	mu       sync.Mutex
	asked    []gateQuestion
}

type gateQuestion struct {
	action string
	mode   kernel.PrincipalMode
}

func (g *roleGate) Authorize(_ context.Context, c *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	g.mu.Lock()
	for _, p := range q.Pairs {
		g.asked = append(g.asked, gateQuestion{action: p.Action.String(), mode: c.Mode()})
	}
	g.mu.Unlock()
	if _, role := c.Mode().(kernel.AsExecutionRole); role && g.refusing.Load() {
		return kernel.Denied{Message: "the role may not"}
	}
	return kernel.Allowed{}
}

// askedAbout reports whether the gate was asked about action in a call
// made as the execution role.
func (g *roleGate) askedAbout(action string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, q := range g.asked {
		if _, role := q.mode.(kernel.AsExecutionRole); role && q.action == action {
			return true
		}
	}
	return false
}

// runGatedESM is runESM under the Authorizer gate.
func runGatedESM(t *testing.T, rec *recorder, gate *roleGate, fn func(h *execHarness)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		sqs.New(k.Deps(sqsapi.Service()), sqs.Options{})
		withExec(k, rec.handle, nil, func(h *execHarness) {
			h.x.StartPollers()
			fn(h)
		})
	}, kerneltest.Services(lambdaapi.Service(), logsapi.Service(), sqsapi.Service()), kerneltest.Authorizer(gate))
}

// CreateEventSourceMapping reads the queue as the execution role, which
// needs sqs:GetQueueAttributes but not sqs:GetQueueUrl: the role AWS
// documents needs sqs:ReceiveMessage, sqs:DeleteMessage and
// sqs:GetQueueAttributes (iam-enforcement-surfaces.md 8.1). A role that may
// not read the queue is refused with InvalidParameterValueException and
// switch lambda.esm.create-denied-text's sentence (8.6, IAM-ENF-24).
func TestESMCreateRefusesARoleThatMayNotReadTheQueue(t *testing.T) {
	gate := &roleGate{}
	runGatedESM(t, &recorder{}, gate, func(h *execHarness) {
		_, arn := h.queue("orders", nil)
		h.createFunction("worker")
		h.quiesce()
		gate.refusing.Store(true)
		err := fail(h.harness, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{FunctionName: "worker", EventSourceArn: new(arn)})
		// testdata/parity/lambda/IAM-ENF/24-create.json
		const want = "The function execution role does not have permissions to call ReceiveMessage on SQS"
		if err.ID.Shape.Name() != "InvalidParameterValueException" || err.Message != want {
			h.Errorf("CreateEventSourceMapping = %s %q, want InvalidParameterValueException %q", err.ID.Shape, err.Message, want)
		}
		if !gate.askedAbout("sqs:GetQueueAttributes") || gate.askedAbout("sqs:GetQueueUrl") {
			h.Errorf("questions = %+v, want sqs:GetQueueAttributes asked as the role and sqs:GetQueueUrl not", gate.asked)
		}
	})
}

// A mapping whose role loses its permissions on the queue stays Enabled:
// its polls are refused, nothing is invoked and the messages stay in the
// queue (switch lambda.esm.permission-loss stay-enabled,
// iam-enforcement-surfaces.md 8.6, IAM-ENF-25).
func TestESMStaysEnabledWhenItsRoleLosesTheQueue(t *testing.T) {
	gate := &roleGate{}
	rec := &recorder{}
	runGatedESM(t, rec, gate, func(h *execHarness) {
		url, arn := h.queue("orders", nil)
		m := h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{})
		h.quiesce()
		gate.refusing.Store(true)
		h.send(url, "queued")
		h.advancePolled(time.Minute)
		wantBodies(h, rec.bodies(), nil)
		if visible, inFlight := h.depth(url); visible != 1 || inFlight != 0 {
			h.Errorf("queue holds %d visible and %d in flight, want the message still visible", visible, inFlight)
		}
		got := call(h.harness, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *m.UUID})
		if got.State == nil || *got.State != "Enabled" {
			h.Errorf("mapping State = %v, want Enabled", got.State)
		}
		if !gate.askedAbout("sqs:ReceiveMessage") {
			h.Errorf("questions = %+v, want the role's sqs:ReceiveMessage asked", gate.asked)
		}
		gate.refusing.Store(false)
		h.advancePolled(time.Minute)
		wantBodies(h, rec.bodies(), [][]string{{"queued"}})

		want := map[string]string{
			"lambda.esm.create-denied-text": "The function execution role does not have permissions to call ReceiveMessage on SQS; " +
				"for a DynamoDB stream: The function execution role does not have permissions to call DescribeStream on DynamoDB; " +
				"for a Kinesis stream: The function execution role does not have permissions to call DescribeStreamSummary on Kinesis",
			"lambda.esm.permission-loss": "stay-enabled",
		}
		for _, e := range h.svc.Switches() {
			if w, ok := want[e.Name]; ok {
				if e.Value != w {
					h.Errorf("switch %s = %q, want %q", e.Name, e.Value, w)
				}
				delete(want, e.Name)
			}
		}
		if len(want) != 0 {
			h.Errorf("Switches lists none of %v", want)
		}
	})
}
