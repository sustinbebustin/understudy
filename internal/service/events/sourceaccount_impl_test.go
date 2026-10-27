package events

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/service/events/envelope"
)

// Each delivery carries the aws:SourceAccount its target kind gets: none to
// a function, since "A SourceAccount setting prevents EventBridge from being
// able to invoke the function", and the rule's account to a queue or topic
// (iam-enforcement-surfaces.md 8.1; switches
// events.delivery.source-account-lambda, aws-doc, and
// events.delivery.source-account-sqs, inferred).
func TestADeliverySendsTheSourceAccountItsTargetGets(t *testing.T) {
	t.Parallel()
	scope := aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()}
	rule := ruleRef{scope: scope, name: "nightly"}
	cases := []struct {
		target string
		want   aws.AccountID
	}{
		{"arn:aws:lambda:us-east-1:000000000000:function:worker", aws.AccountID{}},
		{"arn:aws:sqs:us-east-1:000000000000:queue", scope.Account},
		{"arn:aws:sns:us-east-1:000000000000:topic", scope.Account},
	}
	for _, c := range cases {
		dt, ok := targetOf(scope, c.target)
		if !ok {
			t.Fatalf("targetOf(%s) names no delivery target", c.target)
		}
		var f fanOut
		f.send(rule, &Target{ID: "t", ARN: c.target, Input: ConstantInput{JSON: "{}"}}, dt, envelope.Event{}, clock.Instant{})
		if len(f.requests) != 1 {
			t.Fatalf("send to %s queued %d deliveries (skipped %+v), want 1", c.target, len(f.requests), f.skipped)
		}
		if got := f.requests[0].SourceAccount; got != c.want {
			t.Errorf("delivery to %s sends aws:SourceAccount %q, want %q", c.target, got, c.want)
		}
	}
}

// The delivery switches report the values the fan-out and the outbox
// follow, each governing PutTargets, which configures the delivery
// (iam-enforcement-surfaces.md 12.8).
func TestDeliverySwitchesReportWhatDeliveriesDo(t *testing.T) {
	t.Parallel()
	want := map[string]struct {
		value string
		label evidence.Label
	}{
		"events.delivery.denied-code":           {delivery.EventBridgeDeniedCode, evidence.Inferred},
		"events.delivery.source-account-lambda": {"absent", evidence.AWSDoc},
		"events.delivery.source-account-sqs":    {"present", evidence.Inferred},
		"events.put-targets.sns-policy":         {"unchanged", evidence.Inferred},
	}
	got := deliverySwitches()
	if len(got) != len(want) {
		t.Fatalf("deliverySwitches = %+v, want %d switches", got, len(want))
	}
	for _, e := range got {
		w, ok := want[e.Name]
		if !ok || e.Value != w.value || e.Label != w.label || len(e.Ops) != 1 || e.Ops[0] != "eventbridge.PutTargets" {
			t.Errorf("switch %+v, want value %q, label %s and Ops [eventbridge.PutTargets]", e, w.value, w.label)
		}
	}
	if delivery.EventBridgeDeniedCode != "NO_PERMISSIONS" {
		t.Errorf("EventBridgeDeniedCode = %q, want NO_PERMISSIONS (iam-enforcement-surfaces.md 12.8)", delivery.EventBridgeDeniedCode)
	}
}
