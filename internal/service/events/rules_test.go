package events_test

import (
	"slices"
	"strconv"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// PutRule needs a pattern or a schedule, and refuses an invalid one with
// the recorded texts.
func TestPutRuleRefusesInvalidRules(t *testing.T) {
	run(t, func(h *harness) {
		for _, tt := range []struct {
			in      eventbridgeapi.PutRuleInput
			id, msg string
		}{
			{eventbridgeapi.PutRuleInput{Name: "r"}, "ValidationException", "Parameter(s) EventPattern or ScheduleExpression must be specified."},
			{eventbridgeapi.PutRuleInput{Name: "r", ScheduleExpression: new("rate(1 minutes)")}, "ValidationException", "Parameter ScheduleExpression is not valid."},
			{eventbridgeapi.PutRuleInput{Name: "r", ScheduleExpression: new("cron(0 1 * * * *)")}, "ValidationException", "Parameter ScheduleExpression is not valid."},
			{eventbridgeapi.PutRuleInput{Name: "r", EventPattern: new(`{"source": "s"}`)}, "InvalidEventPatternException", `Event pattern is not valid. Reason: "source" must be an object or an array`},
			{eventbridgeapi.PutRuleInput{Name: "r", EventPattern: new(`{"source": ["s"]}`), EventBusName: new("custom")}, "ResourceNotFoundException", "Event bus custom does not exist."},
		} {
			_, err := call(h, eventbridgeapi.OpPutRule, &tt.in)
			wantError(t, err, tt.id, tt.msg)
		}
		if n := len(must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{}).Rules); n != 0 {
			t.Errorf("%d rules stored by refused calls", n)
		}
	})
}

// ListRules lists by name, filtered by prefix and paged by Limit.
func TestListRulesByPrefixAndPage(t *testing.T) {
	run(t, func(h *harness) {
		for _, name := range []string{"b-2", "a", "b-1", "b-3"} {
			h.patternRule(name, `{"source": ["s"]}`)
		}
		first := must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{NamePrefix: new("b-"), Limit: new(int32(2))})
		second := must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{NamePrefix: new("b-"), NextToken: first.NextToken})
		listed := slices.Concat(first.Rules, second.Rules)
		names := make([]string, len(listed))
		for i, r := range listed {
			names[i] = *r.Name
			if r.Arn == nil || *r.Arn != ruleARN+*r.Name || *r.State != eventbridgeapi.RuleStateEnabled || *r.EventBusName != "default" {
				t.Errorf("listed %+v", r)
			}
		}
		if !slices.Equal(names, []string{"b-1", "b-2", "b-3"}) || second.NextToken != nil {
			t.Errorf("pages = %v, next %v", names, second.NextToken)
		}
		_, err := call(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{NextToken: new("bogus")})
		wantError(t, err, "ValidationException", "Parameter NextToken is not valid.")
	})
}

// Only the default bus exists: it is described and listed by name or ARN;
// any other bus is not found.
func TestDefaultBusOnly(t *testing.T) {
	run(t, func(h *harness) {
		const busARN = "arn:aws:events:us-east-1:000000000000:event-bus/default"
		for _, name := range []*string{nil, new("default"), new(busARN)} {
			out := must(h, eventbridgeapi.OpDescribeEventBus, &eventbridgeapi.DescribeEventBusInput{Name: name})
			if *out.Name != "default" || *out.Arn != busARN {
				t.Errorf("DescribeEventBus = %+v", out)
			}
		}
		_, err := call(h, eventbridgeapi.OpDescribeEventBus, &eventbridgeapi.DescribeEventBusInput{Name: new("custom")})
		wantError(t, err, "ResourceNotFoundException", "Event bus custom does not exist.")
		if n := len(must(h, eventbridgeapi.OpListEventBuses, &eventbridgeapi.ListEventBusesInput{}).EventBuses); n != 1 {
			t.Errorf("%d buses listed, want the default one", n)
		}
		if n := len(must(h, eventbridgeapi.OpListEventBuses, &eventbridgeapi.ListEventBusesInput{NamePrefix: new("x")}).EventBuses); n != 0 {
			t.Errorf("%d buses listed for prefix x", n)
		}
	})
}

// Tags address rules by ARN; a missing rule is not found and the bus takes
// no tags here.
func TestTagsAddressRulesByARN(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: ruleARN + "missing"})
		wantError(t, err, "ResourceNotFoundException", "Rule missing does not exist on EventBus default.")
		_, err = call(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{
			ResourceARN: "arn:aws:events:us-east-1:000000000000:event-bus/default", Tags: []eventbridgeapi.Tag{{Key: "k", Value: "v"}},
		})
		if err == nil || err.ID.JSONType != "NotImplemented" {
			t.Errorf("tagging the bus = %v, want NotImplemented", err)
		}
	})
}

// A bus holds 300 rules, or 100 in af-south-1 and eu-south-1: a new rule
// past that is refused, while replacing a rule at the limit, or creating
// one after a delete, succeeds.
func TestPutRuleQuotaPerBus(t *testing.T) {
	for _, tt := range []struct {
		region string
		limit  int
	}{
		{"us-east-1", 300},
		{"eu-south-1", 100},
	} {
		t.Run(tt.region, func(t *testing.T) {
			run(t, func(h *harness) {
				region, err := aws.KnownRegions().Parse(tt.region)
				if err != nil {
					h.Fatal(err)
				}
				h.Scope.Region = region
				put := func(name, description string) *awserr.Error {
					_, err := call(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{
						Name: name, EventPattern: new(`{"source": ["s"]}`), Description: new(description),
					})
					return err
				}
				for i := range tt.limit {
					if err := put("r-"+strconv.Itoa(i), ""); err != nil {
						h.Fatalf("PutRule r-%d = %s: %s", i, err.ID.JSONType, err.Message)
					}
				}
				wantError(h.T, put("extra", ""), "LimitExceededException", "The requested resource exceeds the maximum number allowed.")
				if err := put("r-0", "replaced"); err != nil {
					h.Errorf("replacing a rule at the limit = %s: %s", err.ID.JSONType, err.Message)
				}
				must(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: "r-1"})
				if err := put("extra", ""); err != nil {
					h.Errorf("PutRule after a delete = %s: %s", err.ID.JSONType, err.Message)
				}
			})
		})
	}
}
