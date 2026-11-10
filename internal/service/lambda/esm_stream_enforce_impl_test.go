package lambda_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
)

// actionGate is an Authorizer that refuses the actions in refused when a
// function's execution role calls them, allows every other call, and
// records each action it was asked about with the mode of the call.
type actionGate struct {
	mu      sync.Mutex
	refused map[string]bool
	asked   []gateQuestion
}

func (g *actionGate) Authorize(_ context.Context, c *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, role := c.Mode().(kernel.AsExecutionRole)
	for _, p := range q.Pairs {
		g.asked = append(g.asked, gateQuestion{action: p.Action.String(), mode: c.Mode()})
		if role && g.refused[p.Action.String()] {
			return kernel.Denied{Message: "the role may not call " + p.Action.String()}
		}
	}
	return kernel.Allowed{}
}

// refuse makes the role's calls of actions denied; none allows them all.
func (g *actionGate) refuse(actions ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refused = map[string]bool{}
	for _, a := range actions {
		g.refused[a] = true
	}
}

// askedAbout reports whether the gate was asked about action in a call of
// mode's kind.
func (g *actionGate) askedAbout(action string, asRole bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.ContainsFunc(g.asked, func(q gateQuestion) bool {
		_, role := q.mode.(kernel.AsExecutionRole)
		return q.action == action && role == asRole
	})
}

// runGatedStream is runStream under gate.
func runGatedStream(t *testing.T, gate *actionGate, f esm.StreamFidelity, fn func(h *streamHarness)) {
	t.Helper()
	box := outbox.New()
	kerneltest.Run(t, func(k *kerneltest.K) {
		if err := box.Bind(k.Kernel, outbox.Options{}); err != nil {
			k.Fatal(err)
		}
		queues := sqs.New(k.Deps(sqsapi.Service()), sqs.Options{})
		topics := &published{}
		kernel.Bind(k.Kernel.Binder(snsapi.Service()), snsapi.OpPublish, func(_ context.Context, _ *kernel.Call, in *snsapi.PublishInput) (*snsapi.PublishOutput, *awserr.Error) {
			topics.mu.Lock()
			defer topics.mu.Unlock()
			topics.messages = append(topics.messages, *in)
			return &snsapi.PublishOutput{MessageId: new("stub")}, nil
		}, registry.Full{})
		stream := newFakeStream(k)
		rec := &streamRecorder{}
		withExec(k, rec.handle, nil, func(h *execHarness) {
			fn(&streamHarness{execHarness: h, stream: stream, rec: rec, topics: topics, queues: queues})
		}, func(c *execConfig) { c.svc.Streams = f })
	}, kerneltest.Deliver(box), kerneltest.Authorizer(gate),
		kerneltest.Services(lambdaapi.Service(), logsapi.Service(), sqsapi.Service(), snsapi.Service(), dynamodbstreamsapi.Service()))
}

// streamRoleDenied is CreateEventSourceMapping's and
// UpdateEventSourceMapping's answer when iam.enforce refuses the execution
// role's DescribeStream of the stream: switch lambda.esm.create-denied-text
// with the stream's action and service
// (iam-enforcement-surfaces.md 8.6; the queue's sentence is recorded,
// IAM-ENF-24, and a stream's follows it).
const streamRoleDenied = "The function execution role does not have permissions to call DescribeStream on DynamoDB"

// Under iam.enforce, Create and Update of a stream mapping whose
// execution role may not read the stream are refused with
// InvalidParameterValueException naming the stream's action and service,
// and nothing is stored.
func TestStreamMappingRefusesARoleThatMayNotReadTheStream(t *testing.T) {
	gate := &actionGate{}
	runGatedStream(t, gate, esm.StreamFidelity{}, func(h *streamHarness) {
		h.createFunction("worker")
		h.createFunction("other")
		h.quiesce()
		gate.refuse("dynamodb:DescribeStream")
		err := fail(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(lambdaapi.CreateEventSourceMappingInput{}))
		if err.ID.Shape.Name() != "InvalidParameterValueException" || err.Message != streamRoleDenied {
			h.Errorf("Create = %s %q, want InvalidParameterValueException %q", err.ID.Shape, err.Message, streamRoleDenied)
		}
		if !gate.askedAbout("dynamodb:DescribeStream", true) {
			h.Errorf("questions = %+v, want dynamodb:DescribeStream asked as the role", gate.asked)
		}
		list := call(h.harness, lambdaapi.OpListEventSourceMappings, &lambdaapi.ListEventSourceMappingsInput{})
		if len(list.EventSourceMappings) != 0 {
			h.Errorf("mappings after the refusal = %d, want none", len(list.EventSourceMappings))
		}

		gate.refuse()
		m := call(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(lambdaapi.CreateEventSourceMappingInput{}))
		h.quiesce()
		gate.refuse("dynamodb:DescribeStream")
		err = fail(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, FunctionName: new("other")})
		if err.ID.Shape.Name() != "InvalidParameterValueException" || err.Message != streamRoleDenied {
			h.Errorf("Update moving to another function = %s %q, want InvalidParameterValueException %q", err.ID.Shape, err.Message, streamRoleDenied)
		}
		if arn := h.get(*m.UUID).FunctionArn; arn == nil || !strings.HasSuffix(*arn, ":function:worker") {
			h.Errorf("FunctionArn after the refused update = %v, want worker's", derefAny(arn))
		}
	})
}

// At LATEST, Create reads each shard to its end as the role
// (lambda.esm-stream.latest-anchor), so a role refused those reads is
// refused naming the read; at TRIM_HORIZON Create reads no record, so the
// same role is accepted and its reads then fail at polling, as the
// CreateEventSourceMapping note says (AWS's check is UNVERIFIED,
// iam-enforcement-surfaces.md 8.6).
func TestStreamMappingAtLatestRefusesARoleThatMayNotReadRecords(t *testing.T) {
	gate := &actionGate{}
	runGatedStream(t, gate, esm.StreamFidelity{}, func(h *streamHarness) {
		h.createFunction("worker")
		h.quiesce()
		gate.refuse("dynamodb:GetShardIterator")
		latest := lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("LATEST"))}
		err := fail(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(latest))
		const want = "The function execution role does not have permissions to call GetShardIterator on DynamoDB"
		if err.ID.Shape.Name() != "InvalidParameterValueException" || err.Message != want {
			h.Errorf("Create at LATEST = %s %q, want InvalidParameterValueException %q", err.ID.Shape, err.Message, want)
		}
		m := call(h.harness, lambdaapi.OpCreateEventSourceMapping, h.streamInput(lambdaapi.CreateEventSourceMappingInput{}))
		if derefAny(m.StartingPosition) != lambdaapi.EventSourcePosition("TRIM_HORIZON") {
			h.Errorf("Create at TRIM_HORIZON = %+v, want it accepted", m)
		}
	})
}

// Under iam.enforce, a mapping whose role loses its reads
// of the stream stays Enabled with its fields unchanged; each denied read
// is journaled as the role's call and nothing is invoked
// (lambda.esm.permission-loss stay-enabled, iam-enforcement-surfaces.md
// 8.6, IAM-ENF-25). The poller's invocation of the function is never asked
// about: it is Lambda's own call (AsPlatform), as for an SQS mapping.
// Once the role may read again the mapping resumes where it stopped.
func TestStreamMappingStaysEnabledWhenItsRoleLosesTheStream(t *testing.T) {
	gate := &actionGate{}
	runGatedStream(t, gate, esm.StreamFidelity{}, func(h *streamHarness) {
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.start()
		h.stream.add(h.Clock.Now(), "a")
		h.quiesce()
		before := h.get(*m.UUID)

		gate.refuse("dynamodb:DescribeStream", "dynamodb:GetShardIterator", "dynamodb:GetRecords")
		h.stream.add(h.Clock.Now(), "b")
		h.advancePolled(time.Minute)
		if got, want := h.rec.ids(), [][]string{{"a"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations while denied = %v, want %v", got, want)
		}
		after := h.get(*m.UUID)
		if derefAny(after.State) != "Enabled" || derefAny(after.LastProcessingResult) != derefAny(before.LastProcessingResult) ||
			derefAny(after.StateTransitionReason) != derefAny(before.StateTransitionReason) {
			h.Errorf("mapping while denied = %v, %v, %v; want Enabled and unchanged from %v, %v",
				derefAny(after.State), derefAny(after.LastProcessingResult), derefAny(after.StateTransitionReason),
				derefAny(before.LastProcessingResult), derefAny(before.StateTransitionReason))
		}
		var denied int
		for _, op := range []ref.OperationID{dynamodbstreamsapi.OpGetShardIterator.Descriptor().ID, dynamodbstreamsapi.OpGetRecords.Descriptor().ID} {
			for _, e := range esmCalls(h.execHarness, op) {
				f, failed := e.Detail.(journal.APICall).Outcome.(journal.Failed)
				if failed && strings.HasSuffix(f.Identity.Shape, "#AccessDeniedException") && journalMode(e) == "execution-role" {
					denied++
				}
			}
		}
		if denied == 0 {
			h.Error("no denied read of the stream was journaled as the role's call")
		}
		if gate.askedAbout("lambda:InvokeFunction", true) || gate.askedAbout("lambda:InvokeFunction", false) {
			h.Errorf("questions = %+v, want the poller's invocation never asked about", gate.asked)
		}

		gate.refuse()
		h.advancePolled(time.Minute)
		if got, want := h.rec.ids(), [][]string{{"a"}, {"b"}}; !slices.EqualFunc(got, want, slices.Equal) {
			h.Errorf("invocations once allowed again = %v, want %v", got, want)
		}
	})
}

// Under iam.enforce, an on-failure record the execution role may
// not send (sqs:SendMessage, sns:Publish) fails as denied, is journaled as
// the esm-to-* delivery's one dropped attempt and never sent; the batch
// still counts as discarded, so the reader goes on past it.
func TestStreamOnFailureSendTheRoleMayNotMakeIsDropped(t *testing.T) {
	for _, c := range []struct {
		name, action string
		mechanism    delivery.Mechanism
		destination  func(h *streamHarness) string
		sent         func(h *streamHarness) int
	}{
		{
			"queue", "sqs:SendMessage", delivery.ESMToSQS,
			func(h *streamHarness) string { _, arn := h.queue("dlq", nil); return arn },
			func(h *streamHarness) int { url, _ := h.queue("dlq", nil); return len(h.failures(url)) },
		},
		{
			"topic", "sns:Publish", delivery.ESMToSNS,
			func(*streamHarness) string { return "arn:aws:sns:us-east-1:000000000000:failures" },
			func(h *streamHarness) int {
				h.topics.mu.Lock()
				defer h.topics.mu.Unlock()
				return len(h.topics.messages)
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			gate := &actionGate{}
			runGatedStream(t, gate, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *streamHarness) {
				dest := c.destination(h)
				h.rec.setAnswer(alwaysFail)
				h.mapStream(lambdaapi.CreateEventSourceMappingInput{
					MaximumRetryAttempts: new(int32(0)),
					DestinationConfig:    &lambdaapi.DestinationConfig{OnFailure: &lambdaapi.OnFailure{Destination: new(dest)}},
				})
				gate.refuse(c.action)
				h.start()
				h.stream.add(h.Clock.Now(), "a")
				h.quiesce()
				h.rec.setAnswer(nil)
				h.stream.add(h.Clock.Now(), "b")
				h.quiesce()
				if got, want := h.rec.ids(), [][]string{{"a"}, {"b"}}; !slices.EqualFunc(got, want, slices.Equal) {
					h.Errorf("invocations %v, want %v: the denied send still discards the batch", got, want)
				}
				if n := c.sent(h); n != 0 {
					h.Errorf("%d on-failure records sent, want none", n)
				}
				if !gate.askedAbout(c.action, true) {
					h.Errorf("questions = %+v, want %s asked as the role", gate.asked, c.action)
				}
				var sends []journal.Delivery
				for _, ev := range h.Journal.Query(journal.Filter{Kind: journal.KindDelivery, Limit: 100}).Events {
					if d, ok := ev.Detail.(journal.Delivery); ok && d.Mechanism == c.mechanism {
						sends = append(sends, d)
					}
				}
				if len(sends) != 1 {
					h.Fatalf("%d %s deliveries, want 1", len(sends), c.mechanism)
				}
				if _, dropped := sends[0].Outcome.(journal.Dropped); !dropped || sends[0].Attempt != 1 {
					h.Errorf("delivery %+v, want one dropped attempt", sends[0])
				}
			})
		})
	}
}
