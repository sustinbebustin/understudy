package events_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/events"
)

const invokerRole = "arn:aws:iam::000000000000:role/invoker"

// iamUses returns the root's recorded iam actions with their resources.
func iamUses(k *kerneltest.K) []string {
	uses := k.Access.Usage(access.Filter{Principal: access.Root{Account: k.Scope.Account}, Service: "iam"})
	out := make([]string, 0, len(uses))
	for i := range uses {
		on := "*"
		if r, ok := uses[i].Resource.(access.OnResource); ok {
			on = r.ARN.String()
		}
		out = append(out, uses[i].Action.String()+" on "+on)
	}
	return out
}

// Every bound EventBridge operation whose generated list holds more than
// one events action decides which of them its calls use, so none records
// the whole list.
func TestEveryMultiActionOperationDeclaresItsActions(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		events.New(k.Deps(eventbridgeapi.Service()), events.Options{})
		if got := kerneltest.UndeclaredActions(k.Kernel, eventbridgeapi.Service()); len(got) != 0 {
			t.Errorf("operations without an actions decision: %v; declare them in declareAccess", got)
		}
	}, kerneltest.Services(eventbridgeapi.Service()))
}

// A RoleArn on PutRule or on a PutTargets target passes that role, so the
// access record keeps iam:PassRole on it; a call that passes no role
// records no iam:PassRole.
func TestPutRuleAndPutTargetsRecordPassRoleOnThePassedRole(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		events.New(k.Deps(eventbridgeapi.Service()), events.Options{})
		ctx := k.Context()
		if _, err := k.Invoke(ctx, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "plain", ScheduleExpression: new("rate(1 hour)")}); err != nil {
			t.Fatalf("PutRule = %s", err.Message)
		}
		if got := iamUses(k); len(got) != 0 {
			t.Errorf("iam uses after a PutRule without a role = %v, want none", got)
		}

		if _, err := k.Invoke(ctx, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "roled", ScheduleExpression: new("rate(1 hour)"), RoleArn: new(invokerRole)}); err != nil {
			t.Fatalf("PutRule = %s", err.Message)
		}
		if got, want := iamUses(k), []string{"iam:PassRole on " + invokerRole}; !slices.Equal(got, want) {
			t.Errorf("iam uses after PutRule with a role = %v, want %v", got, want)
		}

		k.Access.Reset()
		targets := []eventbridgeapi.Target{{Id: "q", Arn: "arn:aws:sqs:us-east-1:000000000000:q", RoleArn: new(invokerRole)}}
		if _, err := k.Invoke(ctx, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: "plain", Targets: targets}); err != nil {
			t.Fatalf("PutTargets = %s", err.Message)
		}
		if got, want := iamUses(k), []string{"iam:PassRole on " + invokerRole}; !slices.Equal(got, want) {
			t.Errorf("iam uses after PutTargets with a role = %v, want %v", got, want)
		}
	}, kerneltest.Services(eventbridgeapi.Service()))
}
