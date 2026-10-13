package events_test

import (
	"slices"
	"strconv"
	"strings"
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

// A list with nothing in it is an empty list, not an absent member: AWS
// answers {"Rules": []}, {"RuleNames": []} and {"EventBuses": []}, and the
// serializers leave out a nil list.
func TestEmptyListsArePresent(t *testing.T) {
	run(t, func(h *harness) {
		if rules := must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{}).Rules; rules == nil || len(rules) != 0 {
			t.Errorf("ListRules in a fresh scope = %#v, want an empty list", rules)
		}
		h.patternRule("r", `{"source": ["s"]}`)
		must(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: "r"})
		if rules := must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{}).Rules; rules == nil || len(rules) != 0 {
			t.Errorf("ListRules after the delete = %#v, want an empty list", rules)
		}
		names := must(h, eventbridgeapi.OpListRuleNamesByTarget, &eventbridgeapi.ListRuleNamesByTargetInput{TargetArn: queueARN}).RuleNames
		if names == nil || len(names) != 0 {
			t.Errorf("ListRuleNamesByTarget = %#v, want an empty list", names)
		}
		buses := must(h, eventbridgeapi.OpListEventBuses, &eventbridgeapi.ListEventBusesInput{NamePrefix: new("nomatch")}).EventBuses
		if buses == nil || len(buses) != 0 {
			t.Errorf("ListEventBuses = %#v, want an empty list", buses)
		}
	})
}

// DeleteRule is idempotent: deleting a rule that does not exist succeeds.
func TestDeleteUnknownRuleSucceeds(t *testing.T) {
	run(t, func(h *harness) {
		out, err := call(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: "never-created"})
		if err != nil || out == nil || *out != (eventbridgeapi.DeleteRuleOutput{}) {
			t.Errorf("DeleteRule of an unknown rule = %+v, %v; want an empty output", out, err)
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
		// Another account's or region's default bus exists, but understudy
		// does not manage it for this caller.
		for _, foreign := range []struct{ bus, account, region string }{
			{"arn:aws:events:us-east-1:111111111111:event-bus/default", "111111111111", "us-east-1"},
			{"arn:aws:events:eu-west-1:000000000000:event-bus/default", "000000000000", "eu-west-1"},
		} {
			_, err = call(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r", EventBusName: new(foreign.bus)})
			wantError(t, err, "NotImplemented", "understudy manages rules, targets and buses only on the caller's own default event bus, and "+
				foreign.bus+" belongs to account "+foreign.account+" in "+foreign.region+
				". Call with that account's credentials in that region, or name the bus 'default'. "+
				"Run 'understudy coverage --service eventbridge' for what is supported.")
		}
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

// A rule holds at most 50 tags (API_TagResource): PutRule creating a rule
// with more, or a TagResource that would leave more, is refused and the
// rule's tags stay as they were. Replacing a tag's value at the limit
// succeeds.
func TestTagLimitPerRule(t *testing.T) {
	run(t, func(h *harness) {
		tags := func(prefix string, n int) []eventbridgeapi.Tag {
			out := make([]eventbridgeapi.Tag, n)
			for i := range out {
				out[i] = eventbridgeapi.Tag{Key: prefix + strconv.Itoa(i), Value: "v"}
			}
			return out
		}
		const over = "Tags are not valid. Reason: a resource can have at most 50 tags, and this request would leave 51."
		_, err := call(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "r", EventPattern: new(`{"source": ["s"]}`), Tags: tags("k", 51)})
		wantError(t, err, "ValidationException", over)
		if _, err := call(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r"}); err == nil {
			t.Error("the refused PutRule created the rule")
		}

		must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "r", EventPattern: new(`{"source": ["s"]}`), Tags: tags("k", 50)})
		_, err = call(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: ruleARN + "r", Tags: tags("extra", 1)})
		wantError(t, err, "ValidationException", over)
		must(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{
			ResourceARN: ruleARN + "r", Tags: []eventbridgeapi.Tag{{Key: "k0", Value: "replaced"}},
		})
		got := must(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: ruleARN + "r"}).Tags
		if len(got) != 50 || slices.ContainsFunc(got, func(tag eventbridgeapi.Tag) bool { return strings.HasPrefix(tag.Key, "extra") }) {
			t.Errorf("tags after the refused TagResource = %d, %v", len(got), got)
		}
		if i := slices.IndexFunc(got, func(tag eventbridgeapi.Tag) bool { return tag.Key == "k0" }); i < 0 || got[i].Value != "replaced" {
			t.Errorf("k0 after replacing its value = %v", got)
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

// A bus holds at most 30 rules whose patterns use a wildcard, whether the
// 31st is created or an existing rule is changed to use one; rules without
// a wildcard are not counted, and a wildcard rule may be replaced at the
// limit.
func TestWildcardRulesPerBusAreLimitedTo30(t *testing.T) {
	run(t, func(h *harness) {
		put := func(name, pattern string) *awserr.Error {
			_, err := call(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: name, EventPattern: new(pattern)})
			return err
		}
		const wildcard, plain = `{"source": [{"wildcard": "app.*"}]}`, `{"source": ["app"]}`
		for i := range 30 {
			if err := put("w-"+strconv.Itoa(i), wildcard); err != nil {
				h.Fatalf("PutRule w-%d = %s: %s", i, err.ID.JSONType, err.Message)
			}
		}
		wantError(h.T, put("w-extra", `{"detail": {"key": [{"anything-but": {"wildcard": "*.tmp"}}]}}`), "LimitExceededException", "The requested resource exceeds the maximum number allowed.")
		if err := put("plain", plain); err != nil {
			h.Errorf("a 31st rule without a wildcard = %s: %s", err.ID.JSONType, err.Message)
		}
		wantError(h.T, put("plain", wildcard), "LimitExceededException", "The requested resource exceeds the maximum number allowed.")
		if err := put("w-0", `{"source": [{"wildcard": "other.*"}]}`); err != nil {
			h.Errorf("replacing a wildcard rule at the limit = %s: %s", err.ID.JSONType, err.Message)
		}
	})
}
