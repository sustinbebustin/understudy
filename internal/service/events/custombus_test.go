package events_test

import (
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/journal"
)

// putRuleOn puts a pattern rule on bus and returns its ARN.
func (h *harness) putRuleOn(bus, name, pattern string) string {
	h.Helper()
	return deref(must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: name, EventBusName: new(bus), EventPattern: new(pattern)}).RuleArn)
}

// targetOn puts one target on a rule of bus, failing on a failed entry.
func (h *harness) targetOn(bus, rule string, t eventbridgeapi.Target) {
	h.Helper()
	out := must(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: rule, EventBusName: new(bus), Targets: []eventbridgeapi.Target{t}})
	if out.FailedEntryCount != 0 {
		h.Fatalf("PutTargets failed entries: %+v", out.FailedEntries)
	}
}

// toBus is a well-formed PutEvents entry for bus.
func toBus(bus string) eventbridgeapi.PutEventsRequestEntry {
	e := entry("shop", "order", `{"total": 250}`)
	e.EventBusName = new(bus)
	return e
}

// Rules on a custom bus behave as on the default bus, under ARNs that name
// the bus, rule/<bus>/<rule>, also for a rule created through the bus ARN
// (eventbridge-buses.md 5.1, 5.2; third-party-recorded
// localstack@8b9a79f tests/aws/services/events/test_events.snapshot.json:L1545-L1620
// for put, list, describe and delete, L1549 for the ARN; TF-ACC
// rule_test.go:L763 for the ARN form). DescribeRule answers {Arn,
// CreatedBy: <account>, EventBusName: <bus>, EventPattern, Name, State}
// (L1583-L1605) and ListRules entries carry EventBusName (L1556-L1575).
// Rule names are per bus (eventbridge-buses.md 5.2), and a missing rule of
// a custom bus names it (ResourceNotFoundException "Rule <rule> does not
// exist on EventBus <bus>.", third-party-recorded
// test_events_tags.snapshot.json:L190-L203).
func TestRulesOnACustomBus(t *testing.T) {
	run(t, func(h *harness) {
		const pattern = `{"source":["shop"]}`
		bus := h.createBus("orders")
		if got := h.putRuleOn("orders", "r", pattern); got != ruleARNOn("orders", "r") {
			t.Errorf("RuleArn = %s, want %s", got, ruleARNOn("orders", "r"))
		}
		if got := h.putRuleOn(bus, "by-arn", pattern); got != ruleARNOn("orders", "by-arn") {
			t.Errorf("RuleArn of a rule created through the bus ARN = %s, want %s", got, ruleARNOn("orders", "by-arn"))
		}
		h.patternRule("r", `{"source":["bank"]}`)

		want := &eventbridgeapi.DescribeRuleOutput{
			Name: new("r"), Arn: new(ruleARNOn("orders", "r")), EventPattern: new(pattern),
			State: new(eventbridgeapi.RuleStateEnabled), EventBusName: new("orders"), CreatedBy: new(account),
		}
		if got := must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r", EventBusName: new("orders")}); !reflect.DeepEqual(got, want) {
			t.Errorf("DescribeRule on orders = %+v\nwant %+v", got, want)
		}
		if got := must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r"}); deref(got.Arn) != ruleARN+"r" || deref(got.EventBusName) != "default" {
			t.Errorf("DescribeRule on the default bus = %+v, want the default bus's own rule r", got)
		}

		listed := must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{EventBusName: new("orders")}).Rules
		names := make([]string, len(listed))
		for i, r := range listed {
			names[i] = deref(r.Name)
			if deref(r.EventBusName) != "orders" || deref(r.Arn) != ruleARNOn("orders", deref(r.Name)) {
				t.Errorf("listed %+v, want a rule of orders", r)
			}
		}
		slices.Sort(names)
		if !slices.Equal(names, []string{"by-arn", "r"}) {
			t.Errorf("ListRules on orders = %v, want by-arn and r", names)
		}
		if n := len(must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{}).Rules); n != 1 {
			t.Errorf("ListRules on the default bus = %d rules, want its own one", n)
		}

		// Disable and enable act on the named bus's rule only
		// (third-party-recorded test_events.snapshot.json:L1894-L1958).
		must(h, eventbridgeapi.OpDisableRule, &eventbridgeapi.DisableRuleInput{Name: "r", EventBusName: new("orders")})
		if s := must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r", EventBusName: new("orders")}).State; *s != eventbridgeapi.RuleStateDisabled {
			t.Errorf("State after DisableRule = %s, want DISABLED", *s)
		}
		if s := must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r"}).State; *s != eventbridgeapi.RuleStateEnabled {
			t.Errorf("the default bus's rule r = %s after disabling orders' r, want ENABLED", *s)
		}
		must(h, eventbridgeapi.OpEnableRule, &eventbridgeapi.EnableRuleInput{Name: "r", EventBusName: new("orders")})
		if s := must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r", EventBusName: new("orders")}).State; *s != eventbridgeapi.RuleStateEnabled {
			t.Errorf("State after EnableRule = %s, want ENABLED", *s)
		}

		must(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: "r", EventBusName: new("orders")})
		_, err := call(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r", EventBusName: new("orders")})
		wantError(t, err, "ResourceNotFoundException", "Rule r does not exist on EventBus orders.")
		must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "r"})
		if n := len(must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{EventBusName: new("orders")}).Rules); n != 1 {
			t.Errorf("ListRules on orders after the delete = %d rules, want 1", n)
		}
	})
}

// ENABLED_WITH_ALL_CLOUDTRAIL_MANAGEMENT_EVENTS "is only valid for rules on
// the default event bus or custom event buses" (aws-doc API_PutRule,
// eventbridge-buses.md 5.3), so a custom bus takes it.
func TestCloudTrailManagementStateOnACustomBus(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("audit")
		must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{
			Name: "trail", EventBusName: new("audit"), EventPattern: new(`{"source":["aws.s3"]}`),
			State: new(eventbridgeapi.RuleStateEnabledWithAllCloudtrailManagementEvents),
		})
		got := must(h, eventbridgeapi.OpDescribeRule, &eventbridgeapi.DescribeRuleInput{Name: "trail", EventBusName: new("audit")})
		if got.State == nil || *got.State != eventbridgeapi.RuleStateEnabledWithAllCloudtrailManagementEvents {
			t.Errorf("State = %v, want ENABLED_WITH_ALL_CLOUDTRAIL_MANAGEMENT_EVENTS", got.State)
		}
	})
}

// A ScheduleExpression is refused on any bus but the default one, whether
// the bus exists (ValidationException "ScheduleExpression is supported
// only on the default event bus.", third-party-recorded
// localstack@8b9a79f tests/aws/services/events/test_events_schedule.snapshot.json:L33-L46)
// or not (testdata/parity/events/R14/04-custom-bus-schedule.json, which
// names a bus the recording never created, aws-recorded), and named by
// name or ARN (eventbridge-buses.md 5.3, 10.3.7). The default bus takes it.
func TestScheduleExpressionOnlyOnTheDefaultBus(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("orders")
		for _, bus := range []string{"orders", busPrefix + "orders", "never-created", busPrefix + "never-created"} {
			_, err := call(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "timer", EventBusName: new(bus), ScheduleExpression: new("rate(5 minutes)")})
			wantError(t, err, "ValidationException", "ScheduleExpression is supported only on the default event bus.")
		}
		if n := len(must(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{EventBusName: new("orders")}).Rules); n != 0 {
			t.Errorf("%d rules on orders after the refused calls", n)
		}
		for _, bus := range []string{"default", busPrefix + "default"} {
			must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "timer", EventBusName: new(bus), ScheduleExpression: new("rate(5 minutes)")})
		}
	})
}

// R14 step 04 as recorded: PutRule of rate(5 minutes) on a bus the
// recording never created answers 400 ValidationException "ScheduleExpression
// is supported only on the default event bus."
// (testdata/parity/events/R14/04-custom-bus-schedule.json:L20 request,
// L23 status, L31 body; aws-recorded).
func TestR14CustomBusScheduleAnswersAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{
			Name: "usr-rec-20261014-busrule", ScheduleExpression: new("rate(5 minutes)"), EventBusName: new("usr-rec-20261014-bus"),
		})
		wantError(t, err, "ValidationException", "ScheduleExpression is supported only on the default event bus.")
		if err != nil && err.ID.Status != 400 {
			t.Errorf("status = %d, want 400", err.ID.Status)
		}
	})
}

// The 300-rule quota counts per event bus (aws-doc
// https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-quota.html,
// "300 per event bus", eventbridge-buses.md 7): a full default bus leaves a
// custom bus room.
func TestRuleQuotaCountsPerBus(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("orders")
		for i := range 300 {
			h.patternRule("r-"+strconv.Itoa(i), `{"source": ["s"]}`)
		}
		_, err := call(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "extra", EventPattern: new(`{"source": ["s"]}`)})
		wantError(t, err, "LimitExceededException", "The requested resource exceeds the maximum number allowed.")
		h.putRuleOn("orders", "extra", `{"source": ["s"]}`)
	})
}

// Targets of a custom-bus rule are put, listed and removed through the
// rule's bus, which every target call names (eventbridge-buses.md 5.1,
// 8.2); ListRuleNamesByTarget lists the rules of the named bus only.
func TestTargetsOfACustomBusRule(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("orders")
		h.putRuleOn("orders", "r", `{"source": ["shop"]}`)
		h.targetOn("orders", "r", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		listed := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "r", EventBusName: new("orders")}).Targets
		if len(listed) != 1 || listed[0].Id != "q" || listed[0].Arn != queueARN {
			t.Errorf("ListTargetsByRule on orders = %+v", listed)
		}
		_, err := call(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "r"})
		wantError(t, err, "ResourceNotFoundException", "Rule r does not exist on EventBus default.")

		if names := must(h, eventbridgeapi.OpListRuleNamesByTarget, &eventbridgeapi.ListRuleNamesByTargetInput{TargetArn: queueARN, EventBusName: new("orders")}).RuleNames; !slices.Equal(names, []string{"r"}) {
			t.Errorf("ListRuleNamesByTarget on orders = %v, want r", names)
		}
		if names := must(h, eventbridgeapi.OpListRuleNamesByTarget, &eventbridgeapi.ListRuleNamesByTargetInput{TargetArn: queueARN}).RuleNames; len(names) != 0 {
			t.Errorf("ListRuleNamesByTarget on the default bus = %v, want none", names)
		}

		_, err = call(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: "r", EventBusName: new("orders")})
		wantError(t, err, "ValidationException", "Rule can't be deleted since it has targets.")
		removed := must(h, eventbridgeapi.OpRemoveTargets, &eventbridgeapi.RemoveTargetsInput{Rule: "r", EventBusName: new("orders"), Ids: []string{"q"}})
		if removed.FailedEntryCount != 0 {
			t.Errorf("RemoveTargets = %+v", removed)
		}
		must(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: "r", EventBusName: new("orders")})
	})
}

// "Only the rules that are associated with this event bus are used to
// match the event" (aws-doc API_PutEventsRequestEntry, eventbridge-buses.md
// 6.1, 10.4.11): an entry for a custom bus, by name or ARN, reaches that
// bus's rules and no other, and an entry without a bus the default bus's.
func TestPutEventsMatchesOnlyTheNamedBus(t *testing.T) {
	run(t, func(h *harness) {
		const ordersQueue = "arn:aws:sqs:us-east-1:000000000000:orders"
		bus := h.createBus("orders")
		h.putRuleOn("orders", "r", `{"source": ["shop"]}`)
		h.targetOn("orders", "r", eventbridgeapi.Target{Id: "q", Arn: ordersQueue})
		h.patternRule("r", `{"source": ["shop"]}`)
		h.target("r", eventbridgeapi.Target{Id: "q", Arn: queueARN})

		for _, tt := range []struct {
			entry eventbridgeapi.PutEventsRequestEntry
			want  string
		}{
			{entry("shop", "order", `{}`), queueARN},
			{toBus("orders"), ordersQueue},
			{toBus(bus), ordersQueue},
			{toBus(busPrefix + "default"), queueARN},
		} {
			out := h.putEvents(tt.entry)
			got := h.sink.take()
			if !slices.Equal(targetARNs(got), []string{tt.want}) {
				t.Errorf("entry for %v delivered to %v, want %s", tt.entry.EventBusName, targetARNs(got), tt.want)
				continue
			}
			if e := decode(t, got[0]); e.ID != deref(out.Entries[0].EventId) || e.Account != account || e.Region != "us-east-1" {
				t.Errorf("entry for %v delivered %+v", tt.entry.EventBusName, e)
			}
			if got[0].Request.Source.String() != ruleARNOn(map[string]string{queueARN: "default", ordersQueue: "orders"}[tt.want], "r") {
				t.Errorf("entry for %v delivered from %s", tt.entry.EventBusName, got[0].Request.Source)
			}
		}
	})
}

// An entry for a custom bus that does not exist gets an EventId and is
// dropped (testdata/parity/events/R14/03-missing-bus.json:L29,
// aws-recorded; eventbridge-buses.md 6.1, 10.3.10), and the call's journal
// event names the bus.
func TestPutEventsToAMissingCustomBus(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["rec"]}`)
		h.target("r", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		e := entry("rec", "x", `{}`)
		e.EventBusName = new("usr-rec-20261014-missing")
		out := h.putEvents(e)
		if out.FailedEntryCount != 0 || len(out.Entries) != 1 || out.Entries[0].EventId == nil || out.Entries[0].ErrorCode != nil {
			t.Fatalf("PutEvents = %+v, want an EventId and no failure", out)
		}
		if got := h.sink.take(); len(got) != 0 {
			t.Errorf("delivered %v", targetARNs(got))
		}
		if n := len(h.Journal.Query(journal.Filter{ResourceGlob: busPrefix + "usr-rec-20261014-missing"}).Events); n != 1 {
			t.Errorf("%d journal events name the missing bus, want the PutEvents call", n)
		}
	})
}

// An event put by ARN to another account's custom bus in the caller's
// region reaches that bus's rules and carries the sender's account
// (eventbridge-buses.md 6.1, third-party-recorded
// localstack@8b9a79f tests/aws/services/events/test_events_cross_account_region.snapshot.json:L474-L479).
// No bus policy is consulted (eventbridge-buses.md 10.7.22).
func TestPutEventsToAnotherAccountsCustomBus(t *testing.T) {
	run(t, func(h *harness) {
		const theirQueue = "arn:aws:sqs:us-east-1:111111111111:theirs"
		var shared string
		h.as("111111111111", "us-east-1", func() {
			shared = h.createBus("shared")
			h.putRuleOn("shared", "r", `{"source": ["shop"]}`)
			h.targetOn("shared", "r", eventbridgeapi.Target{Id: "q", Arn: theirQueue})
		})
		out := h.putEvents(toBus(shared))
		got := h.sink.take()
		if !slices.Equal(targetARNs(got), []string{theirQueue}) {
			t.Fatalf("delivered to %v, want their queue", targetARNs(got))
		}
		if e := decode(t, got[0]); e.ID != deref(out.Entries[0].EventId) || e.Account != account || e.Region != "us-east-1" {
			t.Errorf("cross-account event = %+v, want the sender's account %s", e, account)
		}
	})
}

// Whether PutEvents to a bus in another region is accepted is unrecorded
// (eventbridge-buses.md 6.1, EBB-10): understudy fails the entry with
// NotImplemented rather than drop it unseen, for a custom bus as for a
// default one, and still sends the other entries.
func TestPutEventsToAnotherRegionsBus(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["shop"]}`)
		h.target("r", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		const west = "arn:aws:events:eu-west-1:000000000000:event-bus/orders"
		out := h.putEvents(toBus(west), entry("shop", "order", `{}`))
		if out.FailedEntryCount != 1 || len(out.Entries) != 2 {
			t.Fatalf("PutEvents = %+v", out)
		}
		want := "understudy sends PutEvents entries to event buses in the caller's region (us-east-1), and " + west +
			" is in eu-west-1; the event was not sent. Call PutEvents in eu-west-1, or run 'understudy coverage --service eventbridge' for what is supported."
		if e := out.Entries[0]; e.EventId != nil || deref(e.ErrorCode) != "NotImplemented" || deref(e.ErrorMessage) != want {
			t.Errorf("entry for %s = %+v, want NotImplemented %q", west, e, want)
		}
		if got := targetARNs(h.sink.take()); !slices.Equal(got, []string{queueARN}) {
			t.Errorf("delivered to %v, want the other entry's target", got)
		}
	})
}

// A rule or target call naming another account's or region's bus by ARN
// answers NotImplemented (eventbridge-buses.md 10.1, the PutRule note).
func TestRuleCallsOnAForeignBus(t *testing.T) {
	run(t, func(h *harness) {
		const foreign = "arn:aws:events:us-east-1:111111111111:event-bus/shared"
		want := "understudy manages rules, targets and buses only on the caller's own event buses, and " + foreign +
			" belongs to account 111111111111 in us-east-1. Call with that account's credentials in that region. " +
			"Run 'understudy coverage --service eventbridge' for what is supported."
		_, err := call(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "r", EventBusName: new(foreign), EventPattern: new(`{"source": ["s"]}`)})
		wantError(t, err, "NotImplemented", want)
		_, err = call(h, eventbridgeapi.OpListRules, &eventbridgeapi.ListRulesInput{EventBusName: new(foreign)})
		wantError(t, err, "NotImplemented", want)
		_, err = call(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: "r", EventBusName: new(foreign), Targets: []eventbridgeapi.Target{{Id: "q", Arn: queueARN}}})
		wantError(t, err, "NotImplemented", want)
	})
}
