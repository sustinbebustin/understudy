package events_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// forwardedEntry returns the one entry of an events-to-events delivery.
func forwardedEntry(t *testing.T, d kernel.Delivery) (delivery.EventBus, delivery.EventEntry) {
	t.Helper()
	if d.Request.Mechanism != delivery.EventsToEvents {
		t.Fatalf("delivery mechanism = %s, want events-to-events", d.Request.Mechanism)
	}
	bus, ok := d.Request.Target.(delivery.EventBus)
	if !ok || len(bus.Entries) != 1 {
		t.Fatalf("delivery target = %#v, want an event bus with one entry", d.Request.Target)
	}
	if bus.Entries[0].Forwarded == nil {
		t.Fatalf("forwarded entry %+v has no envelope", bus.Entries[0])
	}
	return bus, bus.Entries[0]
}

func sameJSON(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y bytes.Buffer
	if err := json.Compact(&x, []byte(a)); err != nil {
		t.Fatalf("%s: %v", a, err)
	}
	if err := json.Compact(&y, []byte(b)); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return x.String() == y.String()
}

// An event bus target sends each matched event to that bus as an
// events-to-events delivery from the rule, with the default EventBridge
// retry policy (secrets-logs-events-alarms.md 4.8: 185 attempts, 24 hours).
// The forwarded event keeps its source, detail-type, detail, account and
// region (eventbridge-buses.md 6.2, 10.4.12, third-party-recorded
// localstack@8b9a79f tests/aws/services/events/test_events_targets.snapshot.json:L148-L176,
// test_events.snapshot.json:L1329-L1357). Whether it keeps its id within
// one account and region is unrecorded (EBB-08); understudy follows
// events.bus-target.same-account-event-id.
func TestBusTargetForwardsTheMatchedEvent(t *testing.T) {
	run(t, func(h *harness) {
		h.createBus("source")
		dest := h.createBus("dest")
		rule := h.putRuleOn("source", "fwd", `{"source": ["shop"]}`)
		h.targetOn("source", "fwd", eventbridgeapi.Target{Id: "bus", Arn: dest, RoleArn: new(invokerRole)})
		out := h.putEvents(toBus("source"))
		got := h.sink.take()
		if len(got) != 1 {
			t.Fatalf("%d deliveries, want 1", len(got))
		}
		req := got[0].Request
		bus, e := forwardedEntry(t, got[0])
		if bus.ARN.String() != dest || req.Source.String() != rule || req.DLQ != nil ||
			req.Policy != (delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: 185}) {
			t.Errorf("request = %+v, want the default retry policy from %s to %s", req, rule, dest)
		}
		if e.Source != "shop" || e.DetailType != "order" || !sameJSON(t, e.Detail, `{"total": 250}`) ||
			e.Forwarded.Account != account || e.Forwarded.Region != "us-east-1" {
			t.Errorf("forwarded entry = %+v with %+v, want the matched event from %s in us-east-1", e, *e.Forwarded, account)
		}
		eventID := deref(out.Entries[0].EventId)
		switch v := h.switchValue("events.bus-target.same-account-event-id"); v {
		case "kept":
			if e.Forwarded.ID != eventID {
				t.Errorf("forwarded id = %q, want the event's id %q", e.Forwarded.ID, eventID)
			}
		case "new":
			if e.Forwarded.ID == "" || e.Forwarded.ID == eventID {
				t.Errorf("forwarded id = %q, want a new id, not %q", e.Forwarded.ID, eventID)
			}
		default:
			t.Fatalf("no expectation for events.bus-target.same-account-event-id = %q", v)
		}
	})
}

// Forwarded to another account's bus in the same region, the event keeps
// its id and the sender's account and region (eventbridge-buses.md 6.3,
// third-party-recorded
// localstack@8b9a79f tests/aws/services/events/test_events_cross_account_region.snapshot.json:L89, L113, L116
// for default buses, L320, L344, L347 for custom buses).
func TestBusTargetInAnotherAccountKeepsTheEventID(t *testing.T) {
	run(t, func(h *harness) {
		const theirs = "arn:aws:events:us-east-1:111111111111:event-bus/inbox"
		h.as("111111111111", "us-east-1", func() { h.createBus("inbox") })
		h.patternRule("fwd", `{"source": ["shop"]}`)
		h.target("fwd", eventbridgeapi.Target{Id: "bus", Arn: theirs, RoleArn: new(invokerRole)})
		out := h.putEvents(entry("shop", "order", `{}`))
		got := h.sink.take()
		if len(got) != 1 {
			t.Fatalf("%d deliveries, want 1", len(got))
		}
		bus, e := forwardedEntry(t, got[0])
		if bus.ARN.String() != theirs || e.Forwarded.ID != deref(out.Entries[0].EventId) || e.Forwarded.Account != account || e.Forwarded.Region != "us-east-1" {
			t.Errorf("forwarded to %s: %+v, want the event's id %s, account %s, region us-east-1", bus.ARN, *e.Forwarded, deref(out.Entries[0].EventId), account)
		}
	})
}

// Forwarded to a bus in another region, the event gets a new id, and keeps
// the source region and the sender's account (eventbridge-buses.md 6.4,
// third-party-recorded test_events_cross_account_region.snapshot.json:L243
// and L267 for the ids, L272 for the region, L424 for the account).
func TestBusTargetInAnotherRegionGetsANewEventID(t *testing.T) {
	run(t, func(h *harness) {
		const west = "arn:aws:events:eu-west-1:000000000000:event-bus/default"
		h.patternRule("fwd", `{"source": ["shop"]}`)
		h.target("fwd", eventbridgeapi.Target{Id: "bus", Arn: west, RoleArn: new(invokerRole)})
		out := h.putEvents(entry("shop", "order", `{}`))
		got := h.sink.take()
		if len(got) != 1 {
			t.Fatalf("%d deliveries, want 1", len(got))
		}
		bus, e := forwardedEntry(t, got[0])
		if bus.ARN.String() != west || e.Forwarded.ID == "" || e.Forwarded.ID == deref(out.Entries[0].EventId) ||
			e.Forwarded.Account != account || e.Forwarded.Region != "us-east-1" {
			t.Errorf("forwarded to %s: %+v, want a new id, account %s, region us-east-1", bus.ARN, *e.Forwarded, account)
		}
	})
}

// A scheduled rule's bus target forwards the scheduled event
// (secrets-logs-events-alarms.md 4.3: source aws.events, detail-type
// Scheduled Event, detail {}), from the rule's account and region.
func TestScheduledRuleForwardsToABus(t *testing.T) {
	run(t, func(h *harness) {
		dest := h.createBus("dest")
		must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "timer", ScheduleExpression: new("rate(5 minutes)")})
		h.target("timer", eventbridgeapi.Target{Id: "bus", Arn: dest, RoleArn: new(invokerRole)})
		h.advance(5 * time.Minute)
		got := h.sink.take()
		if len(got) != 1 {
			t.Fatalf("%d deliveries after one interval, want 1", len(got))
		}
		bus, e := forwardedEntry(t, got[0])
		if bus.ARN.String() != dest || e.Source != "aws.events" || e.DetailType != "Scheduled Event" || !sameJSON(t, e.Detail, `{}`) ||
			e.Forwarded.Account != account || e.Forwarded.Region != "us-east-1" || e.Forwarded.ID == "" {
			t.Errorf("forwarded scheduled event = %+v with %+v", e, *e.Forwarded)
		}
	})
}

// A bus target's RetryPolicy and DeadLetterConfig apply to its deliveries
// (eventbridge-buses.md 10.1, the PutTargets note), and ListTargetsByRule
// echoes the target as sent, RoleArn included (secrets-logs-events-alarms.md
// 4.9; TF-ACC target_test.go:L329-L360 for a bus target with role_arn).
func TestBusTargetRetryPolicyAndDeadLetter(t *testing.T) {
	run(t, func(h *harness) {
		dest := h.createBus("dest")
		h.patternRule("fwd", `{"source": ["shop"]}`)
		target := eventbridgeapi.Target{
			Id: "bus", Arn: dest, RoleArn: new(invokerRole),
			RetryPolicy:      &eventbridgeapi.RetryPolicy{MaximumRetryAttempts: new(int32(2)), MaximumEventAgeInSeconds: new(int32(600))},
			DeadLetterConfig: &eventbridgeapi.DeadLetterConfig{Arn: new("arn:aws:sqs:us-east-1:000000000000:dlq")},
		}
		h.target("fwd", target)
		if listed := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "fwd"}).Targets; !reflect.DeepEqual(listed, []eventbridgeapi.Target{target}) {
			t.Errorf("ListTargetsByRule = %+v\nwant %+v", listed, target)
		}
		h.putEvents(entry("shop", "order", `{}`))
		got := h.sink.take()
		if len(got) != 1 {
			t.Fatalf("%d deliveries, want 1", len(got))
		}
		forwardedEntry(t, got[0])
		if p := got[0].Request.Policy; p != (delivery.EventBridgeTarget{MaxAge: 10 * time.Minute, MaxAttempts: 2}) {
			t.Errorf("policy = %+v, want 600 s and 2 retries", p)
		}
		if dlq := got[0].Request.DLQ; dlq == nil || dlq.String() != "arn:aws:sqs:us-east-1:000000000000:dlq" {
			t.Errorf("DLQ = %v, want the target's dead-letter queue", dlq)
		}
	})
}

// A cross-account bus target needs a RoleArn as
// events.bus-target.cross-account-role says ("EventBridge now requires all
// new cross account event bus targets to add IAM roles", aws-doc
// eb-cross-account.html, eventbridge-buses.md 6.3, 9), and takes no Input,
// InputPath or InputTransformer ("Input, InputPath, and InputTransformer
// are not available with PutTarget if the target is an event bus of a
// different AWS account", aws-doc API_PutTargets; texts unrecorded,
// EBB-09). Refused targets are not stored.
func TestPutTargetsCrossAccountBusTarget(t *testing.T) {
	run(t, func(h *harness) {
		const theirs = "arn:aws:events:us-east-1:111111111111:event-bus/default"
		h.patternRule("fwd", `{"source": ["shop"]}`)
		_, err := call(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: "fwd", Targets: []eventbridgeapi.Target{{Id: "bus", Arn: theirs}}})
		switch v := h.switchValue("events.bus-target.cross-account-role"); v {
		case "required":
			if !refused(err) {
				t.Errorf("PutTargets of a cross-account bus without RoleArn = %v, want a client error", err)
			}
		case "organization-only":
			if err != nil {
				t.Errorf("PutTargets of a cross-account bus without RoleArn = %v, want it accepted", err)
			}
			must(h, eventbridgeapi.OpRemoveTargets, &eventbridgeapi.RemoveTargetsInput{Rule: "fwd", Ids: []string{"bus"}})
		default:
			t.Fatalf("no expectation for events.bus-target.cross-account-role = %q", v)
		}
		for name, target := range map[string]eventbridgeapi.Target{
			"Input":            {Id: "bus", Arn: theirs, RoleArn: new(invokerRole), Input: new(`{"a": 1}`)},
			"InputPath":        {Id: "bus", Arn: theirs, RoleArn: new(invokerRole), InputPath: new("$.detail")},
			"InputTransformer": {Id: "bus", Arn: theirs, RoleArn: new(invokerRole), InputTransformer: &eventbridgeapi.InputTransformer{InputTemplate: `{"a": 1}`}},
		} {
			if _, err := call(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: "fwd", Targets: []eventbridgeapi.Target{target}}); !refused(err) {
				t.Errorf("PutTargets of a cross-account bus with %s = %v, want a client error", name, err)
			}
		}
		if n := len(must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "fwd"}).Targets); n != 0 {
			t.Errorf("%d targets after the refused calls, want none", n)
		}
		h.target("fwd", eventbridgeapi.Target{Id: "bus", Arn: theirs, RoleArn: new(invokerRole)})
	})
}

// AWS's handling of an input form on a bus target of the rule's own account
// is unrecorded (eventbridge-buses.md 6.2, EBB-08), so understudy fails
// that entry with NotImplemented rather than guess, and stores the rest.
func TestPutTargetsSameAccountBusTargetWithInput(t *testing.T) {
	run(t, func(h *harness) {
		dest := h.createBus("dest")
		h.patternRule("fwd", `{"source": ["shop"]}`)
		out := must(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: "fwd", Targets: []eventbridgeapi.Target{
			{Id: "shaped", Arn: dest, Input: new(`{"a": 1}`)},
			{Id: "q", Arn: queueARN},
		}})
		want := "understudy forwards the matched event to an event bus target in the rule's account and does not implement Input, InputPath or " +
			"InputTransformer on it, since AWS's handling is unrecorded; the target was not added. Remove the input member, or run " +
			"'understudy coverage --service eventbridge' for what is supported."
		if out.FailedEntryCount != 1 || len(out.FailedEntries) != 1 || deref(out.FailedEntries[0].TargetId) != "shaped" ||
			deref(out.FailedEntries[0].ErrorCode) != "NotImplemented" || deref(out.FailedEntries[0].ErrorMessage) != want {
			t.Errorf("PutTargets = %+v, want shaped failed with NotImplemented %q", out, want)
		}
		listed := must(h, eventbridgeapi.OpListTargetsByRule, &eventbridgeapi.ListTargetsByRuleInput{Rule: "fwd"}).Targets
		if len(listed) != 1 || listed[0].Id != "q" {
			t.Errorf("targets = %+v, want q alone", listed)
		}
	})
}

// forward runs the PutEvents an events-to-events attempt makes for f: an
// internal call over an EventBridge target as events.amazonaws.com for the
// sending rule, in the caller's scope, its context carrying f
// (eventbridge-buses.md 10.4.12).
func (h *harness) forward(f delivery.Forwarded, sender string, e eventbridgeapi.PutEventsRequestEntry) (*eventbridgeapi.PutEventsOutput, error) {
	h.Helper()
	in := &eventbridgeapi.PutEventsInput{Entries: []eventbridgeapi.PutEventsRequestEntry{e}}
	target := kernel.Target{Scope: h.Scope, As: kernel.AsService{Name: "events.amazonaws.com", Source: h.mustARN(sender)}, Via: ref.EventsTarget}
	out, err := kernel.Invoke(delivery.WithForwarded(h.Context(), f), h.Kernel, h.Call(eventbridgeapi.OpPutEvents.Descriptor()), eventbridgeapi.OpPutEvents, target, in)
	if err != nil {
		return nil, err.AsError()
	}
	return out, nil
}

// An event that arrives through an event bus target keeps its id, account,
// region and time on the receiving bus (eventbridge-buses.md 6.3, 6.4,
// 10.4.12), and the receiving bus's rules match it as usual: its non-bus
// targets receive it, while its bus targets do not, since "those events are
// not sent to the third event bus" (aws-doc eb-cross-account.html,
// eventbridge-buses.md 6.5, 10.4.13). Each skipped bus target is journaled
// as a dropped delivery and is not dead-lettered. An event put once may be
// forwarded with a reserved aws. source, as a scheduled event is.
func TestForwardedEventOnTheReceivingBus(t *testing.T) {
	run(t, func(h *harness) {
		dest := h.createBus("dest")
		third := h.createBus("third")
		rule := h.putRuleOn("dest", "in", `{"source": ["aws.events"]}`)
		h.targetOn("dest", "in", eventbridgeapi.Target{Id: "onward", Arn: third, DeadLetterConfig: &eventbridgeapi.DeadLetterConfig{Arn: new("arn:aws:sqs:us-east-1:000000000000:dlq")}})
		h.targetOn("dest", "in", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		h.targetOn("dest", "in", eventbridgeapi.Target{Id: "back", Arn: busPrefix + "default"})

		at := time.Date(2026, 10, 14, 7, 14, 15, 0, time.UTC)
		f := delivery.Forwarded{ID: "6a7e8feb-b491-4cf7-a9f1-bf3703467718", Account: "111111111111", Region: "us-east-1", Time: at}
		e := entry("aws.events", "Scheduled Event", `{}`)
		e.EventBusName = new(dest)
		e.Time = &at
		e.Resources = []string{"arn:aws:events:us-east-1:111111111111:rule/timer"}
		out, err := h.forward(f, "arn:aws:events:us-east-1:111111111111:rule/timer", e)
		if err != nil {
			t.Fatalf("forwarded PutEvents = %v", err)
		}
		if out.FailedEntryCount != 0 || len(out.Entries) != 1 || deref(out.Entries[0].EventId) != f.ID {
			t.Fatalf("forwarded PutEvents = %+v, want the forwarded id %s", out, f.ID)
		}
		got := h.sink.take()
		if !slices.Equal(targetARNs(got), []string{queueARN}) {
			t.Fatalf("delivered to %v, want only the queue", targetARNs(got))
		}
		if d := decode(t, got[0]); d.ID != f.ID || d.Account != f.Account || d.Region != f.Region || d.Time != "2026-10-14T07:14:15Z" ||
			d.Source != "aws.events" || d.DetailType != "Scheduled Event" || string(d.Detail) != `{}` {
			t.Errorf("delivered %+v, want the forwarded event", d)
		}

		var skipped []string
		for _, ev := range h.Journal.Query(journal.Filter{Kind: journal.KindDelivery}).Events {
			d, ok := ev.Detail.(journal.Delivery)
			if !ok || d.Mechanism != delivery.EventsToEvents {
				continue
			}
			dropped, ok := d.Outcome.(journal.Dropped)
			if !ok || d.From.String() != rule ||
				dropped.Reason != "the event reached this bus through an event bus target, and EventBridge does not forward it to another bus (eb-cross-account.html); it was not sent" {
				t.Errorf("journaled %+v, want a dropped second hop from %s", d, rule)
			}
			skipped = append(skipped, d.To.String())
		}
		if !slices.Equal(skipped, []string{third, busPrefix + "default"}) {
			t.Errorf("journaled second hops to %v, want %s then the default bus, in target order", skipped, third)
		}
	})
}

// Only the internal call of an events-to-events attempt reads the
// envelope: a client call whose context carries one stamps a new event
// with the caller's account and refuses a reserved source as usual
// (secrets-logs-events-alarms.md 4.7).
func TestClientPutEventsIgnoresAForwardedContext(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["shop"]}`)
		h.target("r", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		f := delivery.Forwarded{ID: "6a7e8feb-b491-4cf7-a9f1-bf3703467718", Account: "111111111111", Region: "us-east-1", Time: time.Date(2026, 10, 14, 7, 14, 15, 0, time.UTC)}
		ctx := delivery.WithForwarded(h.Context(), f)
		out, err := h.Invoke(ctx, eventbridgeapi.OpPutEvents, &eventbridgeapi.PutEventsInput{Entries: []eventbridgeapi.PutEventsRequestEntry{
			entry("shop", "order", `{}`), entry("aws.events", "Scheduled Event", `{}`),
		}})
		if err != nil {
			t.Fatalf("PutEvents = %v", err)
		}
		if id := deref(out.Entries[0].EventId); id == "" || id == f.ID {
			t.Errorf("EventId = %q, want a new id", id)
		}
		if deref(out.Entries[1].ErrorCode) != "NotAuthorizedForSourceException" {
			t.Errorf("aws.events entry = %+v, want NotAuthorizedForSourceException", out.Entries[1])
		}
		got := h.sink.take()
		if len(got) != 1 {
			t.Fatalf("%d deliveries, want 1", len(got))
		}
		if d := decode(t, got[0]); d.Account != account || d.ID == f.ID {
			t.Errorf("delivered %+v, want a new event from %s", d, account)
		}
	})
}
