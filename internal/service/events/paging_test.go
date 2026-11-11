package events_test

import (
	"fmt"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/paging/pagingtest"
)

// pagedListing is one EventBridge listing, driven as a client drives it.
type pagedListing struct {
	op string
	// seed exceeds the largest Limit tried, and stays within what the
	// listing's items allow (a rule holds at most five targets).
	seed int
	of   func(h *harness) pagingtest.Listing
}

// pagedListings are the four listings that page by NextToken. The listings
// of rules and targets work on the default bus.
func pagedListings() []pagedListing {
	return []pagedListing{
		{"ListEventBuses", 8, func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				Add: func(name string) string {
					must(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{Name: name})
					return name
				},
				Remove: func(name string) {
					must(h, eventbridgeapi.OpDeleteEventBus, &eventbridgeapi.DeleteEventBusInput{Name: name})
				},
				// The default bus is listed too, present throughout.
				Page: func(limit int32, marker *string) ([]string, *string) {
					out := must(h, eventbridgeapi.OpListEventBuses, &eventbridgeapi.ListEventBusesInput{Limit: &limit, NextToken: marker})
					return busNames(out.EventBuses), out.NextToken
				},
			}
		}},
		{"ListRules", 8, func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				Add: func(name string) string {
					h.patternRule(name, `{"source": ["s"]}`)
					return name
				},
				Remove: func(name string) {
					must(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: name})
				},
				Page: func(limit int32, marker *string) ([]string, *string) {
					out := must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{Limit: &limit, NextToken: marker})
					names := make([]string, len(out.Rules))
					for i, r := range out.Rules {
						names[i] = deref(r.Name)
					}
					return names, out.NextToken
				},
			}
		}},
		{"ListRuleNamesByTarget", 8, func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				Add: func(name string) string {
					h.patternRule(name, `{"source": ["s"]}`)
					h.target(name, eventbridgeapi.Target{Id: "t", Arn: queueARN})
					return name
				},
				Remove: func(name string) {
					must(h, eventbridgeapi.OpRemoveTargets, &eventbridgeapi.RemoveTargetsInput{Rule: name, Ids: []string{"t"}})
					must(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: name})
				},
				Page: func(limit int32, marker *string) ([]string, *string) {
					out := must(h, eventbridgeapi.OpListRuleNamesByTarget, &eventbridgeapi.ListRuleNamesByTargetInput{TargetArn: queueARN, Limit: &limit, NextToken: marker})
					return out.RuleNames, out.NextToken
				},
			}
		}},
		{"ListTargetsByRule", 4, func(h *harness) pagingtest.Listing {
			h.patternRule("r", `{"source": ["s"]}`)
			return pagingtest.Listing{
				Add: func(name string) string {
					h.target("r", eventbridgeapi.Target{Id: name, Arn: queueARN})
					return name
				},
				Remove: func(id string) {
					must(h, eventbridgeapi.OpRemoveTargets, &eventbridgeapi.RemoveTargetsInput{Rule: "r", Ids: []string{id}})
				},
				Page: func(limit int32, marker *string) ([]string, *string) {
					out := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "r", Limit: &limit, NextToken: marker})
					ids := make([]string, len(out.Targets))
					for i := range out.Targets {
						ids[i] = out.Targets[i].Id
					}
					return ids, out.NextToken
				},
			}
		}},
	}
}

// Every EventBridge listing, paged one to three items at a time while the
// item each token names, an item already served and a new item come and go
// between pages, serves each item once and skips none present throughout.
// The no-skip rule is inferred: no recording deletes mid-listing.
func TestListingsSurviveChangesBetweenPages(t *testing.T) {
	for _, l := range pagedListings() {
		for limit := int32(1); limit <= 3; limit++ {
			t.Run(fmt.Sprintf("%s/Limit=%d", l.op, limit), func(t *testing.T) {
				run(t, func(h *harness) {
					pagingtest.SurvivesChanges(h, l.of(h), l.seed, limit)
				})
			})
		}
	}
}

// A NextToken another listing issued answers the same ValidationException
// as one understudy never issued.
func TestListingsRefuseForeignTokens(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["s"]}`)
		h.target("r", eventbridgeapi.Target{Id: "1", Arn: queueARN})
		h.target("r", eventbridgeapi.Target{Id: "2", Arn: queueARN})
		foreign := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "r", Limit: new(int32(1))}).NextToken
		if foreign == nil {
			t.Fatal("ListTargetsByRule with Limit 1 of two targets gave no NextToken")
		}
		for _, token := range []string{"bogus", *foreign} {
			const msg = "Parameter NextToken is not valid."
			_, err := call(h, eventbridgeapi.OpListEventBuses, &eventbridgeapi.ListEventBusesInput{NextToken: &token})
			wantError(t, err, "ValidationException", msg)
			_, err = call(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{NextToken: &token})
			wantError(t, err, "ValidationException", msg)
			_, err = call(h, eventbridgeapi.OpListRuleNamesByTarget, &eventbridgeapi.ListRuleNamesByTargetInput{TargetArn: queueARN, NextToken: &token})
			wantError(t, err, "ValidationException", msg)
		}
		_, err := call(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "r", NextToken: new("bogus")})
		wantError(t, err, "ValidationException", "Parameter NextToken is not valid.")
	})
}

// A rule with no targets lists an empty list of targets, not an absent
// member.
func TestListTargetsByRuleWithNoneIsAnEmptyList(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["s"]}`)
		out := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "r"})
		if out.Targets == nil || len(out.Targets) != 0 || out.NextToken != nil {
			t.Errorf("ListTargetsByRule = %#v, want an empty list and no NextToken", out)
		}
	})
}
