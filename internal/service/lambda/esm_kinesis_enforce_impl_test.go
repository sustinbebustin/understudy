package lambda_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// The spec's enforce matrix rows 2 to 4 for a Kinesis stream mapping over
// the real Kinesis service, with the execution role's calls refused by an
// actionGate (spec B6; iam-enforcement-surfaces.md 8.6).

// runGatedKinesis is runRealKinesis under gate.
func runGatedKinesis(t *testing.T, gate *actionGate, f esm.StreamFidelity, fn func(h *realKinesisHarness)) {
	t.Helper()
	runRealKinesis(t, f, fn, kerneltest.Authorizer(gate))
}

// kinesisRoleDenied is lambda.esm.create-denied-text's sentence for a
// Kinesis stream naming op (08b contract 6.1; inferred, IAM-ENF-24).
func kinesisRoleDenied(op string) string {
	return "The provided execution role does not have permissions to call " + op + " on Kinesis"
}

// Row 2: Create and Update of a Kinesis mapping whose execution role may
// not call kinesis:DescribeStreamSummary on the stream answer
// InvalidParameterValueException naming that operation on Kinesis, and
// nothing is stored or changed.
func TestKinesisMappingRefusesARoleThatMayNotDescribeTheStream(t *testing.T) {
	gate := &actionGate{}
	runGatedKinesis(t, gate, esm.StreamFidelity{}, func(h *realKinesisHarness) {
		h.createStream(1)
		h.createFunction("worker")
		h.createFunction("other")
		h.quiesce()
		gate.refuse("kinesis:DescribeStreamSummary")
		err := fail(h.harness, lambdaapi.OpCreateEventSourceMapping, mapInput(lambdaapi.CreateEventSourceMappingInput{}))
		if want := kinesisRoleDenied("DescribeStreamSummary"); err.ID.Shape.Name() != "InvalidParameterValueException" || err.Message != want {
			h.Errorf("Create = %s %q, want InvalidParameterValueException %q", err.ID.Shape, err.Message, want)
		}
		if !gate.askedAbout("kinesis:DescribeStreamSummary", true) {
			h.Errorf("questions = %+v, want kinesis:DescribeStreamSummary asked as the role", gate.asked)
		}
		if list := call(h.harness, lambdaapi.OpListEventSourceMappings, &lambdaapi.ListEventSourceMappingsInput{}); len(list.EventSourceMappings) != 0 {
			h.Errorf("mappings after the refusal = %d, want none", len(list.EventSourceMappings))
		}

		gate.refuse()
		m := call(h.harness, lambdaapi.OpCreateEventSourceMapping, mapInput(lambdaapi.CreateEventSourceMappingInput{}))
		h.quiesce()
		gate.refuse("kinesis:DescribeStreamSummary")
		err = fail(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, FunctionName: new("other")})
		if want := kinesisRoleDenied("DescribeStreamSummary"); err.ID.Shape.Name() != "InvalidParameterValueException" || err.Message != want {
			h.Errorf("Update moving to another function = %s %q, want InvalidParameterValueException %q", err.ID.Shape, err.Message, want)
		}
		got := call(h.harness, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *m.UUID})
		if arn := got.FunctionArn; arn == nil || !strings.HasSuffix(*arn, ":function:worker") {
			h.Errorf("FunctionArn after the refused update = %v, want worker's", derefAny(arn))
		}
	})
}

// At LATEST, Create reads each shard to its end as the role
// (lambda.esm-stream.latest-anchor) through ListShards, GetShardIterator
// and GetRecords, so a role refused any of them is refused naming it; at
// TRIM_HORIZON Create reads no record, so the same role is accepted, as
// for a DynamoDB stream (the ESM note; AWS's check is UNVERIFIED,
// iam-enforcement-surfaces.md 8.6).
func TestKinesisMappingAtLatestRefusesARoleThatMayNotReadRecords(t *testing.T) {
	for _, op := range []string{"ListShards", "GetShardIterator", "GetRecords"} {
		t.Run(op, func(t *testing.T) {
			gate := &actionGate{}
			runGatedKinesis(t, gate, esm.StreamFidelity{}, func(h *realKinesisHarness) {
				h.createStream(1)
				h.createFunction("worker")
				h.quiesce()
				gate.refuse("kinesis:" + op)
				latest := lambdaapi.CreateEventSourceMappingInput{StartingPosition: new(lambdaapi.EventSourcePosition("LATEST"))}
				err := fail(h.harness, lambdaapi.OpCreateEventSourceMapping, mapInput(latest))
				if want := kinesisRoleDenied(op); err.ID.Shape.Name() != "InvalidParameterValueException" || err.Message != want {
					h.Errorf("Create at LATEST = %s %q, want InvalidParameterValueException %q", err.ID.Shape, err.Message, want)
				}
				m := call(h.harness, lambdaapi.OpCreateEventSourceMapping, mapInput(lambdaapi.CreateEventSourceMappingInput{}))
				if derefAny(m.StartingPosition) != lambdaapi.EventSourcePosition("TRIM_HORIZON") {
					h.Errorf("Create at TRIM_HORIZON = %+v, want it accepted", m)
				}
			})
		})
	}
}

// Rows 3 and 5: a Kinesis mapping whose role loses its reads stays
// Enabled with its fields unchanged, each denied read is journaled as the
// role's call, nothing is invoked, and the poller's invocation is never
// asked about (lambda.esm.permission-loss stay-enabled, IAM-ENF-25). Once
// the role may read again the mapping delivers what it missed.
func TestKinesisMappingStaysEnabledWhenItsRoleLosesTheStream(t *testing.T) {
	gate := &actionGate{}
	runGatedKinesis(t, gate, esm.StreamFidelity{}, func(h *realKinesisHarness) {
		h.createStream(1)
		m := h.mapStream(lambdaapi.CreateEventSourceMappingInput{})
		h.start()
		h.put("k", "", "a")
		h.quiesce()
		before := call(h.harness, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *m.UUID})

		gate.refuse("kinesis:DescribeStreamSummary", "kinesis:ListShards", "kinesis:GetShardIterator", "kinesis:GetRecords")
		h.put("k", "", "b")
		h.advancePolled(time.Minute)
		if got := h.data(); !slices.Equal(got, []string{"a"}) {
			h.Errorf("delivered while denied %v, want [a]", got)
		}
		after := call(h.harness, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *m.UUID})
		if derefAny(after.State) != "Enabled" || derefAny(after.LastProcessingResult) != derefAny(before.LastProcessingResult) ||
			derefAny(after.StateTransitionReason) != derefAny(before.StateTransitionReason) {
			h.Errorf("mapping while denied = %v, %v, %v; want Enabled and unchanged from %v, %v",
				derefAny(after.State), derefAny(after.LastProcessingResult), derefAny(after.StateTransitionReason),
				derefAny(before.LastProcessingResult), derefAny(before.StateTransitionReason))
		}
		var denied int
		for _, op := range []ref.OperationID{kinesisapi.OpGetShardIterator.Descriptor().ID, kinesisapi.OpGetRecords.Descriptor().ID} {
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
		if got := h.data(); !slices.Equal(got, []string{"a", "b"}) {
			h.Errorf("delivered once allowed again %v, want [a b]", got)
		}
	})
}

// Row 4: an on-failure record the role may not send to its queue
// (sqs:SendMessage) is journaled as the esm-to-sqs delivery's one dropped
// attempt and never sent; the batch still counts as discarded, so the
// reader goes on past it.
func TestKinesisOnFailureSendTheRoleMayNotMakeIsDropped(t *testing.T) {
	gate := &actionGate{}
	runGatedKinesis(t, gate, esm.StreamFidelity{RetryDelay: esm.RetryImmediately}, func(h *realKinesisHarness) {
		h.createStream(1)
		url, dlq := h.queue("dlq", nil)
		h.rec.setAnswer(failWith)
		h.mapStream(lambdaapi.CreateEventSourceMappingInput{MaximumRetryAttempts: new(int32(0)), DestinationConfig: onFailureTo(dlq)})
		gate.refuse("sqs:SendMessage")
		h.start()
		h.put("k", "", "a")
		h.quiesce()
		h.rec.setAnswer(nil)
		h.put("k", "", "b")
		h.quiesce()
		if got := h.invocations(); !slices.EqualFunc(got, [][]string{{"a"}, {"b"}}, slices.Equal) {
			h.Errorf("invocations %v, want [[a] [b]]: the denied send still discards the batch", got)
		}
		if n := len(h.failures(url)); n != 0 {
			h.Errorf("%d on-failure records sent, want none", n)
		}
		if !gate.askedAbout("sqs:SendMessage", true) {
			h.Errorf("questions = %+v, want sqs:SendMessage asked as the role", gate.asked)
		}
		var sends []journal.Delivery
		for _, ev := range h.Journal.Query(journal.Filter{Kind: journal.KindDelivery, Limit: 100}).Events {
			if d, ok := ev.Detail.(journal.Delivery); ok && d.Mechanism == delivery.ESMToSQS {
				sends = append(sends, d)
			}
		}
		if len(sends) != 1 {
			h.Fatalf("%d esm-to-sqs deliveries, want 1", len(sends))
		}
		if _, dropped := sends[0].Outcome.(journal.Dropped); !dropped || sends[0].Attempt != 1 {
			h.Errorf("delivery %+v, want one dropped attempt", sends[0])
		}
	})
}
