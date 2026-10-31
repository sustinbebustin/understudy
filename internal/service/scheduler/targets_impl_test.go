package scheduler_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/identity"
)

// Each served target is called once per invocation over its own Scheduler
// mechanism, as a session of the execution role: Lambda asynchronously,
// a qualified ARN passed through as Invoke's Qualifier (scheduler.md 6.4;
// UNVERIFIED SCH-07), SQS FIFO with the message group and no deduplication
// id (14.4.3), SNS with the payload as the message and no Subject
// (UNVERIFIED SCH-07), and one PutEvents entry on the bus with no
// resources (UNVERIFIED SCH-07, 14.4.5). The context attributes are
// substituted in every target's payload (6.3).
func TestServedTargetsAreInvokedOverTheirMechanisms(t *testing.T) {
	run(t, func(h *harness) {
		at := h.now().Add(10 * time.Minute).Truncate(time.Second)
		when := atExpr(at)
		input := `{"attempt":"<aws.scheduler.attempt-number>","at":"<aws.scheduler.scheduled-time>"}`
		payload := `{"attempt":"1","at":"` + rfc3339(at) + `"}`
		h.create(schedule("lambda", when, schedulerapi.Target{Arn: h.functionArn("f") + ":live", RoleArn: h.roleArn(), Input: new(input)}))
		h.create(schedule("fifo", when, schedulerapi.Target{
			Arn: h.queueArn("orders.fifo"), RoleArn: h.roleArn(), Input: new(input),
			SqsParameters: &schedulerapi.SqsParameters{MessageGroupId: new("g1")},
		}))
		h.create(schedule("sns", when, schedulerapi.Target{Arn: h.topicArn("t"), RoleArn: h.roleArn(), Input: new(input)}))
		h.create(schedule("bus", when, schedulerapi.Target{
			Arn: h.busArn("default"), RoleArn: h.roleArn(), Input: new(input),
			EventBridgeParameters: &schedulerapi.EventBridgeParameters{Source: "my.app", DetailType: "tick"},
		}))
		h.advance(15 * time.Minute)

		if got := h.callsOf("Invoke"); len(got) != 1 {
			h.Errorf("Lambda calls = %d, want 1", len(got))
		} else if in := got[0].lambda; in.FunctionName != h.functionArn("f") || val(in.Qualifier) != "live" || string(in.Payload) != payload ||
			in.InvocationType == nil || *in.InvocationType != lambdaapi.InvocationTypeEvent {
			h.Errorf("Invoke = %s qualifier %s type %v payload %s, want %s live Event %s", in.FunctionName, val(in.Qualifier), in.InvocationType, in.Payload, h.functionArn("f"), payload)
		}
		if got := h.sentTo("orders.fifo"); len(got) != 1 || got[0].sqs.MessageBody != payload || val(got[0].sqs.MessageGroupId) != "g1" || got[0].sqs.MessageDeduplicationId != nil {
			h.Errorf("FIFO sends = %+v, want one with %s, group g1 and no deduplication id", got, payload)
		}
		if got := h.callsOf("Publish"); len(got) != 1 || val(got[0].sns.TopicArn) != h.topicArn("t") || got[0].sns.Message != payload || got[0].sns.Subject != nil {
			h.Errorf("SNS calls = %+v, want one Publish of %s to %s without Subject", got, payload, h.topicArn("t"))
		}
		if got := h.callsOf("PutEvents"); len(got) != 1 || len(got[0].events.Entries) != 1 {
			h.Errorf("PutEvents calls = %+v, want one with one entry", got)
		} else if e := got[0].events.Entries[0]; val(e.Source) != "my.app" || val(e.DetailType) != "tick" || val(e.Detail) != payload ||
			val(e.EventBusName) != h.busArn("default") || len(e.Resources) != 0 {
			h.Errorf("entry = %+v, want my.app, tick, %s on %s without resources", e, payload, h.busArn("default"))
		}
		for _, c := range h.targetCalls() {
			if r, ok := c.principal.(identity.AssumedRole); !ok || r.Role.Name != roleName || !sessionName.MatchString(r.Session) || c.origin != schedulerOrigin {
				h.Errorf("%s ran as %#v over %v, want a 32-hex session of %s over Scheduler", c.op, c.principal, c.origin, roleName)
			}
		}
		want := map[string]string{"lambda": "scheduler-to-lambda", "fifo": "scheduler-to-sqs", "sns": "scheduler-to-sns", "bus": "scheduler-to-events"}
		for _, d := range h.sink.all() {
			s := d.Request.Source.String()
			name := s[strings.LastIndexByte(s, '/')+1:]
			if got := d.Request.Mechanism.String(); got != want[name] {
				h.Errorf("schedule %s delivered over %s, want %s", name, got, want[name])
			}
		}
	})
}

// Target ARNs the plan cannot invoke are refused at create, nothing
// stored: a service with no templated target with the doc's text
// (aws-doc troubleshooting; scheduler.md 14.4.1), and, with understudy's
// texts (UNVERIFIED SCH-04), an ARN that is not one, a templated service's
// ARN naming no target resource, an event bus without
// EventBridgeParameters, an event bus in another Region (aws-doc
// managing-targets-templated) and an Input over 262,144 bytes (aws-doc
// API_Target, "256 KB").
func TestTargetRefusals(t *testing.T) {
	run(t, func(h *harness) {
		bus := func(arn string) schedulerapi.Target {
			return schedulerapi.Target{Arn: arn, RoleArn: h.roleArn(), EventBridgeParameters: &schedulerapi.EventBridgeParameters{Source: "my.app", DetailType: "tick"}}
		}
		cases := []struct {
			target  schedulerapi.Target
			message string
		}{
			{schedulerapi.Target{Arn: "arn:aws:dynamodb:us-east-1:000000000000:table/t", RoleArn: h.roleArn()}, "Invalid request provided: dynamodb is not a supported service for a target"},
			{schedulerapi.Target{Arn: "not-an-arn", RoleArn: h.roleArn()}, ""},
			{schedulerapi.Target{Arn: "arn:aws:scheduler:us-east-1:000000000000:schedule/default/s", RoleArn: h.roleArn()}, "Invalid request provided: scheduler is not a supported service for a target"},
			{schedulerapi.Target{Arn: "arn:aws:lambda:us-east-1:000000000000:layer:l", RoleArn: h.roleArn()}, ""},
			{schedulerapi.Target{Arn: h.functionArn("f") + ":1:2", RoleArn: h.roleArn()}, ""},
			{schedulerapi.Target{Arn: "arn:aws:events:us-east-1:000000000000:rule/r", RoleArn: h.roleArn()}, ""},
			{schedulerapi.Target{Arn: h.busArn("default"), RoleArn: h.roleArn()}, ""},
			{bus("arn:aws:events:eu-west-1:000000000000:event-bus/default"), ""},
			{h.queueTarget("q", new(strings.Repeat("x", 262145))), "Target.Input is 262145 bytes; the maximum is 262144 bytes (256 KB)."},
		}
		for _, c := range cases {
			_, err := call(h, schedulerapi.OpCreateSchedule, schedule("s", "rate(1 hour)", c.target))
			wantError(h, err, "ValidationException", 400, c.message)
		}
		wantError(h, errOf(call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: "s"})), "ResourceNotFoundException", 404, "")
		h.create(schedule("max", "rate(1 hour)", h.queueTarget("q", new(strings.Repeat("x", 262144)))))
		// A custom bus in the schedule's Region is a target too: it need not
		// exist (scheduler.md 4.1, 6.1).
		h.create(schedule("custom", "rate(1 hour)", bus(h.busArn("ops"))))
	})
}

// A parameter block that does not match the target is stored, echoed by
// GetSchedule and ignored by the invocation. UNVERIFIED (SCH-04),
// scheduler.md 6.1.
func TestMismatchedParameterBlocksAreEchoedAndIgnored(t *testing.T) {
	run(t, func(h *harness) {
		tg := schedulerapi.Target{
			Arn: h.functionArn("f"), RoleArn: h.roleArn(),
			SqsParameters:         &schedulerapi.SqsParameters{MessageGroupId: new("g1")},
			EventBridgeParameters: &schedulerapi.EventBridgeParameters{Source: "my.app", DetailType: "tick"},
			KinesisParameters:     &schedulerapi.KinesisParameters{PartitionKey: "k"},
		}
		h.create(schedule("s", atExpr(h.now().Add(10*time.Minute)), tg))
		got := h.get("", "s").Target
		if got.SqsParameters == nil || val(got.SqsParameters.MessageGroupId) != "g1" ||
			got.EventBridgeParameters == nil || got.EventBridgeParameters.Source != "my.app" || got.EventBridgeParameters.DetailType != "tick" ||
			got.KinesisParameters == nil || got.KinesisParameters.PartitionKey != "k" || got.EcsParameters != nil || got.SageMakerPipelineParameters != nil {
			h.Errorf("GetSchedule Target = %+v, want the three sent blocks echoed and no others", got)
		}
		h.advance(15 * time.Minute)
		if n := len(h.callsOf("Invoke")); n != 1 || len(h.targetCalls()) != 1 {
			h.Errorf("target calls = %+v, want one Invoke", h.targetCalls())
		}
	})
}

// Without Input, SNS and event bus targets receive the
// scheduler.input.default payload too (scheduler.md 6.3).
func TestDefaultPayloadOnSNSAndBusTargets(t *testing.T) {
	run(t, func(h *harness) {
		when := atExpr(h.now().Add(10 * time.Minute))
		h.create(schedule("sns", when, schedulerapi.Target{Arn: h.topicArn("t"), RoleArn: h.roleArn()}))
		h.create(schedule("bus", when, schedulerapi.Target{
			Arn: h.busArn("default"), RoleArn: h.roleArn(),
			EventBridgeParameters: &schedulerapi.EventBridgeParameters{Source: "my.app", DetailType: "tick"},
		}))
		h.advance(15 * time.Minute)
		if got := h.callsOf("Publish"); len(got) != 1 || got[0].sns.Message != "{}" {
			h.Errorf("SNS calls = %+v, want one Publish of {}", got)
		}
		if got := h.callsOf("PutEvents"); len(got) != 1 || len(got[0].events.Entries) != 1 || val(got[0].events.Entries[0].Detail) != "{}" {
			h.Errorf("PutEvents calls = %+v, want one entry with detail {}", got)
		}
	})
}
