package events_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
)

// The forwarded event keeps its time and resources as well as the recorded
// members (eventbridge-buses.md 10.4.12): the recordings redact time and
// carry only resources: [], so keeping both is understudy's choice until
// EBB-08 records them.
func TestBusTargetForwardsTheEventTimeAndResources(t *testing.T) {
	run(t, func(h *harness) {
		dest := h.createBus("dest")
		h.patternRule("fwd", `{"source": ["shop"]}`)
		h.target("fwd", eventbridgeapi.Target{Id: "bus", Arn: dest})
		at := time.Date(2026, 10, 14, 7, 14, 15, 0, time.UTC)
		e := entry("shop", "order", `{}`)
		e.Time = &at
		e.Resources = []string{"arn:aws:s3:::bucket", "arn:aws:s3:::other"}
		h.putEvents(e)
		got := h.sink.take()
		if len(got) != 1 {
			t.Fatalf("%d deliveries, want 1", len(got))
		}
		_, fwd := forwardedEntry(t, got[0])
		if !fwd.Forwarded.Time.Equal(at) || !slices.Equal(fwd.Resources, e.Resources) {
			t.Errorf("forwarded time %v and resources %v, want %v and %v", fwd.Forwarded.Time, fwd.Resources, at, e.Resources)
		}
	})
}

// PutTargets takes an event bus that does not exist yet, which AWS's
// handling leaves unrecorded (eventbridge-buses.md 6.2, EBB-08), and the
// delivery to it is made. The receiving PutEvents then drops the event as it
// drops one put to a missing bus: an EventId and no delivery
// (eventbridge-buses.md 10.4.14; 6.1, testdata/parity/events/R14/03-missing-bus.json).
func TestBusTargetToAMissingBus(t *testing.T) {
	run(t, func(h *harness) {
		missing := busPrefix + "not-yet"
		h.patternRule("fwd", `{"source": ["shop"]}`)
		h.target("fwd", eventbridgeapi.Target{Id: "bus", Arn: missing})
		h.putEvents(entry("shop", "order", `{}`))
		got := h.sink.take()
		if len(got) != 1 {
			t.Fatalf("%d deliveries, want the one to %s", len(got), missing)
		}
		if bus, _ := forwardedEntry(t, got[0]); bus.ARN.String() != missing {
			t.Errorf("delivered to %s, want %s", bus.ARN, missing)
		}

		h.putRuleOn("default", "in", `{"source": ["shop"]}`)
		h.target("in", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		f := delivery.Forwarded{ID: "6a7e8feb-b491-4cf7-a9f1-bf3703467718", Account: account, Region: "us-east-1", Time: forwardedAt}
		out, err := h.forward(f, ruleARN+"fwd", toBus(missing))
		if err != nil {
			t.Fatalf("forwarded PutEvents to a missing bus = %v", err)
		}
		if out.FailedEntryCount != 0 || deref(out.Entries[0].EventId) != f.ID {
			t.Errorf("forwarded PutEvents to a missing bus = %+v, want the forwarded id %s", out, f.ID)
		}
		if got := h.sink.take(); len(got) != 0 {
			t.Errorf("deliveries %v, want none from a missing bus", targetARNs(got))
		}
	})
}

// A PutTargets call without a RoleArn on a cross-account event bus target
// that has one keeps the role when the Id and Arn are the same: "If you
// have an IAM role on a cross-account event bus target, a PutTargets call
// without a role on the same target (same Id and Arn) will not remove the
// role" (aws-doc API_PutTargets, eventbridge-buses.md 6.3). The rest of the
// target is replaced. Under another Arn the target is new and needs a role
// of its own (events.bus-target.cross-account-role).
func TestPutTargetsKeepsACrossAccountBusTargetsRole(t *testing.T) {
	run(t, func(h *harness) {
		const theirs = "arn:aws:events:us-east-1:111111111111:event-bus/default"
		h.patternRule("fwd", `{"source": ["shop"]}`)
		h.target("fwd", eventbridgeapi.Target{Id: "bus", Arn: theirs, RoleArn: new(invokerRole)})
		retry := &eventbridgeapi.RetryPolicy{MaximumRetryAttempts: new(int32(1))}
		h.target("fwd", eventbridgeapi.Target{Id: "bus", Arn: theirs, RetryPolicy: retry})
		want := []eventbridgeapi.Target{{Id: "bus", Arn: theirs, RoleArn: new(invokerRole), RetryPolicy: retry}}
		if listed := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "fwd"}).Targets; !reflect.DeepEqual(listed, want) {
			t.Errorf("ListTargetsByRule after a put without RoleArn = %+v\nwant %+v", listed, want)
		}
		if v := h.switchValue("events.bus-target.cross-account-role"); v != "required" {
			t.Fatalf("no expectation for events.bus-target.cross-account-role = %q", v)
		}
		const orders = "arn:aws:events:us-east-1:111111111111:event-bus/orders"
		_, err := call(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: "fwd", Targets: []eventbridgeapi.Target{{Id: "bus", Arn: orders}}})
		if !refused(err) {
			t.Errorf("PutTargets of the Id on another cross-account bus without RoleArn = %v, want a client error", err)
		}
		if listed := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "fwd"}).Targets; !reflect.DeepEqual(listed, want) {
			t.Errorf("ListTargetsByRule after the refused put = %+v\nwant %+v", listed, want)
		}
	})
}

// An event bus target ARN names a bus: event-bus/<name> in the rule's
// partition, the name non-empty and without '/' (CreateEventBus refuses one,
// eventbridge-buses.md 2.2). Any other EventBridge ARN is a target understudy
// does not deliver to, and fails its entry.
func TestPutTargetsRefusesEventBridgeARNsThatAreNoBus(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("fwd", `{"source": ["shop"]}`)
		for _, arn := range []string{
			"arn:aws:events:us-east-1:000000000000:event-bus/",
			"arn:aws:events:us-east-1:000000000000:event-bus/a/b",
			"arn:aws:events:us-east-1:000000000000:rule/other",
			"arn:aws-cn:events:cn-north-1:000000000000:event-bus/orders",
		} {
			out := must(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: "fwd", Targets: []eventbridgeapi.Target{{Id: "bus", Arn: arn}}})
			want := "understudy delivers EventBridge targets to Lambda functions, SQS queues and SNS topics in the rule's region (us-east-1) " +
				"and to event buses in any region, in an account it serves, and " + arn + " is none of them; the target was not added. " +
				"Run 'understudy coverage --service eventbridge' for what is supported."
			if out.FailedEntryCount != 1 || deref(out.FailedEntries[0].ErrorCode) != "NotImplemented" || deref(out.FailedEntries[0].ErrorMessage) != want {
				t.Errorf("PutTargets %s = %+v, want NotImplemented %q", arn, out, want)
			}
		}
		if n := len(must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "fwd"}).Targets); n != 0 {
			t.Errorf("%d targets stored, want none", n)
		}
	})
}

// A bus of the rule's own account in another region is a same-account
// target: it takes no RoleArn requirement (events.bus-target.cross-account-role
// concerns other accounts, eb-cross-account.html), and an input form on it
// fails its entry as on a same-region bus, AWS's handling being unrecorded
// (eventbridge-buses.md 6.2, EBB-08).
func TestSameAccountBusInAnotherRegion(t *testing.T) {
	run(t, func(h *harness) {
		const west = "arn:aws:events:eu-west-1:000000000000:event-bus/orders"
		h.patternRule("fwd", `{"source": ["shop"]}`)
		out := must(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: "fwd", Targets: []eventbridgeapi.Target{
			{Id: "plain", Arn: west},
			{Id: "shaped", Arn: west, InputPath: new("$.detail")},
		}})
		if out.FailedEntryCount != 1 || deref(out.FailedEntries[0].TargetId) != "shaped" || deref(out.FailedEntries[0].ErrorMessage) != msgBusTargetInputText {
			t.Errorf("PutTargets = %+v, want shaped failed with the bus-target input text", out)
		}
		listed := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "fwd"}).Targets
		if len(listed) != 1 || listed[0].Id != "plain" || listed[0].RoleArn != nil {
			t.Errorf("targets = %+v, want plain alone, without a RoleArn", listed)
		}
	})
}

// msgBusTargetInputText is the PutTargets entry failure for an input form
// on a bus target of the rule's account (TestPutTargetsSameAccountBusTargetWithInput).
const msgBusTargetInputText = "understudy forwards the matched event to an event bus target in the rule's account and does not implement Input, InputPath or " +
	"InputTransformer on it, since AWS's handling is unrecorded; the target was not added. Remove the input member, or run " +
	"'understudy coverage --service eventbridge' for what is supported."

// A bus target stored with an input form, which PutTargets never stores, can
// only come from an edited snapshot. Its rule's other targets still receive
// the event; the bus target receives nothing and is journaled as a dropped
// delivery saying why, rather than forwarded with its input silently ignored.
func TestStoredBusTargetWithAnInputIsDropped(t *testing.T) {
	run(t, func(h *harness) {
		dest := h.createBus("dest")
		h.patternRule("fwd", `{"source": ["shop"]}`)
		h.target("fwd", eventbridgeapi.Target{Id: "bus", Arn: dest})
		h.target("fwd", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		sec := exportOne(h)
		data := replaced(h, string(sec.Data),
			`"arn":"`+dest+`","id":"bus","input":{"kind":"matched-event"}`,
			`"arn":"`+dest+`","id":"bus","input":{"json":"{}","kind":"constant"}`)
		if err := importData(h, sec, data); err != nil {
			t.Fatalf("Import = %v", err)
		}
		h.putEvents(entry("shop", "order", `{}`))
		if got := targetARNs(h.sink.take()); !slices.Equal(got, []string{queueARN}) {
			t.Errorf("delivered to %v, want the queue alone", got)
		}
		var dropped []journal.Delivery
		for _, ev := range h.Journal.Query(journal.Filter{Kind: journal.KindDelivery}).Events {
			if d, ok := ev.Detail.(journal.Delivery); ok && d.Mechanism == delivery.EventsToEvents {
				dropped = append(dropped, d)
			}
		}
		const why = "the event bus target has an Input, InputPath or InputTransformer, which understudy does not implement on an event bus target " +
			"and PutTargets does not store, so it came from an edited snapshot; the event was not sent. " +
			"Remove the target with RemoveTargets and add it again with PutTargets"
		if len(dropped) != 1 || dropped[0].To.String() != dest || dropped[0].Outcome != (journal.Dropped{Reason: why}) {
			t.Errorf("journaled %+v, want one dropped delivery to %s", dropped, dest)
		}
	})
}

// A hop-marked event's bus targets are dropped whatever their account: one
// in another account is the documented case ("these events are not sent to
// the third account", eb-cross-account.html, eventbridge-buses.md 6.5).
func TestForwardedEventReachesNoCrossAccountBus(t *testing.T) {
	run(t, func(h *harness) {
		const theirs = "arn:aws:events:us-east-1:111111111111:event-bus/default"
		dest := h.createBus("dest")
		h.putRuleOn("dest", "in", `{"source": ["shop"]}`)
		h.targetOn("dest", "in", eventbridgeapi.Target{Id: "onward", Arn: theirs, RoleArn: new(invokerRole)})
		f := delivery.Forwarded{ID: "6a7e8feb-b491-4cf7-a9f1-bf3703467718", Account: "222222222222", Region: "us-east-1", Time: forwardedAt}
		if _, err := h.forward(f, "arn:aws:events:us-east-1:222222222222:rule/fwd", toBus(dest)); err != nil {
			t.Fatalf("forwarded PutEvents = %v", err)
		}
		if got := h.sink.take(); len(got) != 0 {
			t.Errorf("delivered to %v, want nothing", targetARNs(got))
		}
		var to []string
		for _, ev := range h.Journal.Query(journal.Filter{Kind: journal.KindDelivery}).Events {
			if d, ok := ev.Detail.(journal.Delivery); ok && d.Mechanism == delivery.EventsToEvents {
				if _, isDrop := d.Outcome.(journal.Dropped); isDrop {
					to = append(to, d.To.String())
				}
			}
		}
		if !slices.Equal(to, []string{theirs}) {
			t.Errorf("dropped second hops to %v, want %s", to, theirs)
		}
	})
}
