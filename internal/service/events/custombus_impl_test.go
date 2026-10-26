package events_test

import (
	"strconv"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Every rule and target call on a custom bus that does not exist, named by
// name or by ARN, or deleted with DeleteEventBus, answers the bus
// operations' not-found text, ResourceNotFoundException "Event bus <name>
// does not exist." (testdata/parity/events/R07/04-describe-event-bus-missing.json).
// AWS's answer for rule and target calls is unrecorded
// (eventbridge-buses.md 5.4, EBB-03), so the shared text is understudy's
// assumption.
func TestRuleAndTargetCallsOnAMissingBus(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("gone")
		must(h, eventbridgeapi.OpDeleteEventBus, &eventbridgeapi.DeleteEventBusInput{Name: "gone"})
		for _, tt := range []struct{ bus, name string }{
			{"never-created", "never-created"},
			{busPrefix + "never-created", "never-created"},
			{"gone", "gone"},
		} {
			bus := new(tt.bus)
			for op, err := range map[string]*awserr.Error{
				"PutRule": errOf(call(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{
					Name: "r", EventBusName: bus, EventPattern: new(`{"source": ["s"]}`),
				})),
				"DescribeRule":          errOf(call(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r", EventBusName: bus})),
				"ListRules":             errOf(call(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{EventBusName: bus})),
				"DeleteRule":            errOf(call(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: "r", EventBusName: bus})),
				"EnableRule":            errOf(call(h, eventbridgeapi.OpEnableRule, &eventbridgeapi.EnableRuleInput{Name: "r", EventBusName: bus})),
				"DisableRule":           errOf(call(h, eventbridgeapi.OpDisableRule, &eventbridgeapi.DisableRuleInput{Name: "r", EventBusName: bus})),
				"ListRuleNamesByTarget": errOf(call(h, eventbridgeapi.OpListRuleNamesByTarget, &eventbridgeapi.ListRuleNamesByTargetInput{TargetArn: queueARN, EventBusName: bus})),
				"PutTargets": errOf(call(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{
					Rule: "r", EventBusName: bus, Targets: []eventbridgeapi.Target{{Id: "q", Arn: queueARN}},
				})),
				"RemoveTargets":     errOf(call(h, eventbridgeapi.OpRemoveTargets, &eventbridgeapi.RemoveTargetsInput{Rule: "r", EventBusName: bus, Ids: []string{"q"}})),
				"ListTargetsByRule": errOf(call(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "r", EventBusName: bus})),
			} {
				if err == nil || err.ID.JSONType != "ResourceNotFoundException" || err.Message != "Event bus "+tt.name+" does not exist." {
					t.Errorf("%s on %s = %v, want ResourceNotFoundException %q", op, tt.bus, err, "Event bus "+tt.name+" does not exist.")
				}
			}
		}
	})
}

// PutRule refuses a ScheduleExpression on a bus whose name is not default
// before anything else (eventbridge-buses.md 10.3.7): before it parses the
// expression or compiles the pattern, an order that is inferred (EBB-03),
// and before it resolves the bus, so another account's custom bus named by
// ARN gets the same answer. Another account's default bus passes that check
// and is then not one understudy manages for the caller (eventbridge-buses.md
// 10.1, the PutRule note). A pattern rule on a missing bus has its pattern
// checked before the bus is looked up.
func TestPutRuleCheckOrder(t *testing.T) {
	run(t, func(h *harness) {
		const shared = "arn:aws:events:us-east-1:111111111111:event-bus/shared"
		for _, in := range []eventbridgeapi.PutRuleInput{
			{Name: "r", EventBusName: new("never-created"), ScheduleExpression: new("rate(1 minutes)")},
			{Name: "r", EventBusName: new("never-created"), ScheduleExpression: new("rate(5 minutes)"), EventPattern: new(`{"source": "s"}`)},
			{Name: "r", EventBusName: new(shared), ScheduleExpression: new("rate(5 minutes)")},
		} {
			_, err := call(h, eventbridgeapi.OpPutRule, &in)
			wantError(t, err, "ValidationException", "ScheduleExpression is supported only on the default event bus.")
		}

		const theirDefault = "arn:aws:events:us-east-1:111111111111:event-bus/default"
		_, err := call(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "r", EventBusName: new(theirDefault), ScheduleExpression: new("rate(5 minutes)")})
		wantError(t, err, "NotImplemented", "understudy manages rules, targets and buses only on the caller's own event buses, and "+theirDefault+
			" belongs to account 111111111111 in us-east-1. Call with that account's credentials in that region. "+
			"Run 'understudy coverage --service eventbridge' for what is supported.")

		_, err = call(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "r", EventBusName: new("never-created"), EventPattern: new(`{"source": "s"}`)})
		wantError(t, err, "InvalidEventPatternException", `Event pattern is not valid. Reason: "source" must be an object or an array`)
	})
}

// The 30-wildcard-rule quota counts per event bus, as the 300-rule one does
// (aws-doc https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-quota.html,
// "Rules containing wildcards ... 30 per event bus", eventbridge-buses.md 7):
// a default bus at the limit leaves a custom bus room.
func TestWildcardRuleQuotaCountsPerBus(t *testing.T) {
	run(t, func(h *harness) {
		const wildcard = `{"source": [{"wildcard": "app.*"}]}`
		h.createBus("orders")
		for i := range 30 {
			h.patternRule("w-"+strconv.Itoa(i), wildcard)
		}
		_, err := call(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "w-extra", EventPattern: new(wildcard)})
		wantError(t, err, "LimitExceededException", "The requested resource exceeds the maximum number allowed.")
		h.putRuleOn("orders", "w-extra", wildcard)
	})
}

// A rule and target call names the custom-bus rule it addresses by its
// rule/<bus>/<name> ARN (eventbridge-buses.md 5.2), so the call's journal
// event, and a fault rule scoped by resource, finds it under that ARN.
// DescribeRule answers EventBusName with the bus's name when the request
// named the bus by ARN, which is inferred (eventbridge-buses.md 5.2, EBB-07).
func TestCustomBusRuleCallsNameTheRule(t *testing.T) {
	run(t, func(h *harness) {
		bus := h.createBus("orders")
		h.putRuleOn(bus, "r", `{"source": ["shop"]}`)
		h.targetOn(bus, "r", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		got := must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r", EventBusName: new(bus)})
		if deref(got.EventBusName) != "orders" || deref(got.Arn) != ruleARNOn("orders", "r") {
			t.Errorf("DescribeRule through the bus ARN = %+v, want EventBusName orders and the rule/orders/r ARN", got)
		}
		for _, op := range []string{"PutRule", "PutTargets", "DescribeRule"} {
			events := h.Journal.Query(journal.Filter{
				ResourceGlob: ruleARNOn("orders", "r"),
				Operation:    ref.OperationID{Service: "eventbridge", Name: op},
			}).Events
			if len(events) != 1 {
				t.Errorf("%d %s journal events name %s, want 1", len(events), op, ruleARNOn("orders", "r"))
			}
		}
	})
}

// A custom bus whose rules are deleted can be deleted (eventbridge-buses.md
// 3.5), and one created again under its name starts without rules.
func TestDeletedBusLeavesNoRules(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("orders")
		h.putRuleOn("orders", "r", `{"source": ["shop"]}`)
		must(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: "r", EventBusName: new("orders")})
		must(h, eventbridgeapi.OpDeleteEventBus, &eventbridgeapi.DeleteEventBusInput{Name: "orders"})
		h.createBus("orders")
		if rules := must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{EventBusName: new("orders")}).Rules; len(rules) != 0 {
			t.Errorf("rules of the recreated bus = %+v, want none", rules)
		}
	})
}
