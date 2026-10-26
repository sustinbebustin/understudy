package events_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
)

// providerPolicy is the Statement member of the struct
// aws_cloudwatch_event_permission parses a bus Policy into: Action and
// Resource are strings, so an array in either fails the read
// (eventbridge-buses.md 8.4, TF permission.go L272-L297).
type providerPolicy struct {
	Statement []struct {
		Sid       string
		Effect    string
		Action    string
		Condition map[string]map[string]string
		Principal any
		Resource  string
	}
}

// principalOf maps a statement's Principal back to the account id, or "*",
// as the provider does (eventbridge-buses.md 8.4, TF permission.go
// L151-L167); "" for any other form.
func (h *harness) principalOf(p any) string {
	h.Helper()
	switch p := p.(type) {
	case string:
		return p
	case map[string]any:
		if arn, ok := p["AWS"].(string); ok {
			return h.mustARN(arn).Account
		}
	}
	return ""
}

// sameJSON reports whether a and b decode to equal values, the equivalence
// aws_cloudwatch_event_bus_policy reads a stored policy by, since AWS may
// reorder or reformat it (eventbridge-buses.md 8.3).
func (h *harness) sameJSON(a, b string) bool {
	h.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		h.Fatalf("%s: %v", a, err)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		h.Fatalf("%s: %v", b, err)
	}
	return reflect.DeepEqual(x, y)
}

// The custom-bus resources of conformance/terraform/events.tf, as the
// provider calls them (eventbridge-buses.md 8.1-8.4, 10.6): every rule and
// target call names the bus by the ARN the configuration gives, every read
// after a write returns what the next plan compares (so it is empty), and
// the destroy leaves nothing in the scope. The default bus is never
// written, so it is not materialized and the leftover check lists nothing.
func TestTerraformCustomBusConfiguration(t *testing.T) {
	run(t, func(h *harness) {
		const (
			pattern       = `{"source":["conformance.orders"],"detail-type":["Order Placed"]}`
			updated       = `{"source":["conformance.orders"],"detail-type":["Order Paid"]}`
			partnerPolicy = `{"Statement":[{"Action":"events:PutEvents","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111111111111:root"},` +
				`"Resource":"` + busPrefix + `orders","Sid":"AllowPartnerPutEvents"}],"Version":"2012-10-17"}`
			updatedPolicy = `{"Statement":[{"Action":"events:PutEvents","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::222222222222:root"},` +
				`"Resource":"` + busPrefix + `orders","Sid":"AllowPartnerPutEvents"}],"Version":"2012-10-17"}`
		)
		purpose := eventbridgeapi.Tag{Key: "Purpose", Value: "orders"}

		// aws_cloudwatch_event_bus: create, then read and list tags
		// (eventbridge-buses.md 8.1, 10.6.15). Unset members read back
		// absent (TF-ACC bus_test.go L43-L46).
		bus := deref(must(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{
			Name: "orders", Description: new("Order events"), Tags: []eventbridgeapi.Tag{purpose},
		}).EventBusArn)
		if bus != busPrefix+"orders" {
			t.Fatalf("EventBusArn = %q, want %q", bus, busPrefix+"orders")
		}
		d := h.describeBus(new("orders"))
		if deref(d.Name) != "orders" || deref(d.Arn) != bus || deref(d.Description) != "Order events" ||
			d.LogConfig != nil || d.DeadLetterConfig != nil || d.KmsKeyIdentifier != nil {
			t.Errorf("DescribeEventBus after create = %+v, want orders with its description and nothing else set", d)
		}
		h.wantTags(bus, purpose)

		// aws_cloudwatch_event_rule and aws_cloudwatch_event_target with
		// event_bus_name = the bus ARN (eventbridge-buses.md 8.2, 10.6.16).
		rule := deref(must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{
			Name: "orders", EventBusName: new(bus), Description: new("Placed orders"), EventPattern: new(pattern),
			Tags: []eventbridgeapi.Tag{purpose},
		}).RuleArn)
		if rule != ruleARNOn("orders", "orders") {
			t.Fatalf("RuleArn = %q, want %q", rule, ruleARNOn("orders", "orders"))
		}
		h.wantRule(bus, "Placed orders", pattern)
		h.wantTags(rule, purpose)
		h.putTarget(bus, "$.detail")

		// aws_cloudwatch_event_bus_policy: the policy is readable at once
		// (eventbridge-buses.md 8.3, 10.6.17).
		must(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{EventBusName: new("orders"), Policy: new(partnerPolicy)})
		if p := h.describeBus(new("orders")).Policy; p == nil || !h.sameJSON(*p, partnerPolicy) {
			t.Errorf("Policy after the bus policy create = %v, want %s", p, partnerPolicy)
		}

		// aws_cloudwatch_event_permission on a second bus: the statement
		// parses into the provider's struct and maps back to the principal
		// (eventbridge-buses.md 8.4, 10.6.18).
		h.createBus("partners")
		h.permit(new("partners"), "PartnerAccount", "111111111111")
		h.wantPermission("111111111111")

		// Update: the bus's description, log config and tags; the rule's
		// pattern and description; the target's input path; the policy's
		// principal; the permission's principal, in place under its
		// statement id (TF-ACC permission_test.go L79-L90).
		none, off := eventbridgeapi.IncludeDetailNone, eventbridgeapi.LevelOff
		must(h, eventbridgeapi.OpUpdateEventBus, &eventbridgeapi.UpdateEventBusInput{
			Name: new("orders"), Description: new("Order events (updated)"),
			LogConfig: &eventbridgeapi.LogConfig{IncludeDetail: &none, Level: &off},
		})
		stage := eventbridgeapi.Tag{Key: "Stage", Value: "updated"}
		must(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: bus, Tags: []eventbridgeapi.Tag{stage}})
		d = h.describeBus(new("orders"))
		if deref(d.Description) != "Order events (updated)" || !reflect.DeepEqual(d.LogConfig, &eventbridgeapi.LogConfig{IncludeDetail: &none, Level: &off}) {
			t.Errorf("DescribeEventBus after update = %+v (log config %+v), want the new description and NONE/OFF as sent (TF-ACC bus_test.go L335-L339)", d, d.LogConfig)
		}
		h.wantTags(bus, purpose, stage)

		must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{
			Name: "orders", EventBusName: new(bus), Description: new("Paid orders"), EventPattern: new(updated),
		})
		h.wantRule(bus, "Paid orders", updated)
		h.wantTags(rule, purpose)
		h.putTarget(bus, "$.detail.order")

		must(h, eventbridgeapi.OpPutPermission, &eventbridgeapi.PutPermissionInput{EventBusName: new("orders"), Policy: new(updatedPolicy)})
		if p := h.describeBus(new("orders")).Policy; p == nil || !h.sameJSON(*p, updatedPolicy) {
			t.Errorf("Policy after the bus policy update = %v, want %s", p, updatedPolicy)
		}
		h.permit(new("partners"), "PartnerAccount", "*")
		h.wantPermission("*")

		// Destroy: dependents first, the buses last (eventbridge-buses.md
		// 10.6.19); each delete's not-found would be success to the
		// provider, and nothing is left.
		must(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: new("orders"), RemoveAllPermissions: true})
		must(h, eventbridgeapi.OpRemovePermission, &eventbridgeapi.RemovePermissionInput{EventBusName: new("partners"), StatementId: new("PartnerAccount")})
		if removed := must(h, eventbridgeapi.OpRemoveTargets, &eventbridgeapi.RemoveTargetsInput{
			Rule: "orders", EventBusName: new(bus), Ids: []string{"queue"},
		}); removed.FailedEntryCount != 0 {
			t.Errorf("RemoveTargets = %+v", removed)
		}
		must(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: "orders", EventBusName: new(bus)})
		_, err := call(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "orders", EventBusName: new(bus)})
		wantError(t, err, "ResourceNotFoundException", "Rule orders does not exist on EventBus orders.")
		must(h, eventbridgeapi.OpDeleteEventBus, &eventbridgeapi.DeleteEventBusInput{Name: "partners"})
		must(h, eventbridgeapi.OpDeleteEventBus, &eventbridgeapi.DeleteEventBusInput{Name: "orders"})
		_, err = call(h, eventbridgeapi.OpDescribeEventBus, &eventbridgeapi.DescribeEventBusInput{Name: new("orders")})
		wantError(t, err, "ResourceNotFoundException", "Event bus orders does not exist.")
		if arns := h.resourceARNs(); len(arns) != 0 {
			t.Errorf("resources after the destroy = %v, want none", arns)
		}
	})
}

// wantTags checks the tags ListTagsForResource lists for arn, sorted by key.
func (h *harness) wantTags(arn string, want ...eventbridgeapi.Tag) {
	h.Helper()
	if got := must(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: arn}).Tags; !reflect.DeepEqual(got, want) {
		h.Errorf("tags of %s = %+v, want %+v", arn, got, want)
	}
}

// wantRule checks the orders rule as DescribeRule answers it through the bus
// ARN. EventBusName is the bus's name, which the provider ignores, keeping
// the bus from its own resource id (eventbridge-buses.md 5.2, EBB-07).
func (h *harness) wantRule(bus, description, pattern string) {
	h.Helper()
	got := must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "orders", EventBusName: new(bus)})
	want := &eventbridgeapi.DescribeRuleOutput{
		Name: new("orders"), Arn: new(ruleARNOn("orders", "orders")), EventPattern: new(pattern), Description: new(description),
		State: new(eventbridgeapi.RuleStateEnabled), EventBusName: new("orders"), CreatedBy: new(account),
	}
	if !reflect.DeepEqual(got, want) {
		h.Errorf("DescribeRule = %+v\nwant %+v", got, want)
	}
}

// putTarget puts the queue target of the orders rule with inputPath through
// the bus ARN, then checks ListTargetsByRule lists it alone, as sent.
func (h *harness) putTarget(bus, inputPath string) {
	h.Helper()
	target := eventbridgeapi.Target{Id: "queue", Arn: queueARN, InputPath: new(inputPath)}
	h.targetOn(bus, "orders", target)
	got := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "orders", EventBusName: new(bus)}).Targets
	if !reflect.DeepEqual(got, []eventbridgeapi.Target{target}) {
		h.Errorf("ListTargetsByRule = %+v, want %+v", got, target)
	}
}

// wantPermission checks the partners bus Policy holds the one PartnerAccount
// statement, readable as aws_cloudwatch_event_permission reads it.
func (h *harness) wantPermission(principal string) {
	h.Helper()
	text := deref(h.describeBus(new("partners")).Policy)
	var doc providerPolicy
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		h.Fatalf("Policy %s does not parse into the provider's struct: %v", text, err)
	}
	if len(doc.Statement) != 1 {
		h.Fatalf("Policy %s, want one statement", text)
	}
	s := doc.Statement[0]
	if s.Sid != "PartnerAccount" || s.Effect != "Allow" || s.Action != "events:PutEvents" || s.Resource != busPrefix+"partners" ||
		h.principalOf(s.Principal) != principal {
		h.Errorf("statement = %+v, want PartnerAccount allowing %s events:PutEvents on %s", s, principal, busPrefix+"partners")
	}
}
