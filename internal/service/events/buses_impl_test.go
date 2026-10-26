package events_test

import (
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
)

// loadStoredV2 replaces the caller's scope with storedV2: the default bus
// with the rule d, and the custom bus orders, with a policy, and its rule r.
func loadStoredV2(h *harness) {
	h.Helper()
	h.patternRule("seed", `{"source": ["s"]}`)
	if err := importData(h, exportOne(h), storedV2); err != nil {
		h.Fatalf("Import = %v", err)
	}
}

// resourceARNs lists the ARNs of the resources view of the caller's scope.
func (h *harness) resourceARNs() []string {
	resources := h.svc.Resources(h.Scope)
	out := make([]string, 0, len(resources))
	for _, r := range resources {
		out = append(out, r.ARN.String())
	}
	return out
}

// DeleteEventBus refuses a bus that still has a rule under
// events.delete-bus.with-rules (eventbridge-buses.md 3.5, EBB-01): the code
// is that of the recorded default-bus refusal and the text is inferred. The
// bus and its rule stay. Rules on a custom bus come from a snapshot here.
func TestDeleteEventBusRefusesABusWithARule(t *testing.T) {
	run(t, func(h *harness) {
		if v := h.switchValue("events.delete-bus.with-rules"); v != "rejected" {
			t.Fatalf("no expectation for events.delete-bus.with-rules = %q", v)
		}
		loadStoredV2(h)
		_, err := call(h, eventbridgeapi.OpDeleteEventBus, &eventbridgeapi.DeleteEventBusInput{Name: "orders"})
		wantError(t, err, "ValidationException", "Cannot delete event bus orders. All rules associated with this event bus need to be deleted.")
		h.describeBus(new("orders"))
		if arns := h.resourceARNs(); !slices.Contains(arns, ruleARNOn("orders", "r")) {
			t.Errorf("resources after the refused delete = %v, want the rule kept", arns)
		}
	})
}

// Tags address a rule of a custom bus by its rule/<bus>/<name> ARN and list
// by key (eventbridge-buses.md 5.2, 5.5). A rule of a bus that does not
// exist is not found on that bus; another account's or region's bus or rule
// is not found by the texts of a missing one; an ARN that is neither a bus
// nor a rule is not valid, a text that is inferred.
func TestTagsOnACustomBusRuleAndOtherARNs(t *testing.T) {
	run(t, func(h *harness) {
		loadStoredV2(h)
		rule := ruleARNOn("orders", "r")
		must(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: rule, Tags: []eventbridgeapi.Tag{{Key: "b", Value: "2"}, {Key: "a", Value: "1"}}})
		tags := must(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: rule}).Tags
		if want := []eventbridgeapi.Tag{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}}; !slices.Equal(tags, want) {
			t.Errorf("rule tags = %v, want %v", tags, want)
		}
		must(h, eventbridgeapi.OpUntagResource, &eventbridgeapi.UntagResourceInput{ResourceARN: rule, TagKeys: []string{"a"}})
		tags = must(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: rule}).Tags
		if want := []eventbridgeapi.Tag{{Key: "b", Value: "2"}}; !slices.Equal(tags, want) {
			t.Errorf("rule tags after untagging a = %v, want %v", tags, want)
		}

		for _, tt := range []struct{ arn, jsonType, message string }{
			{ruleARNOn("ghost", "r"), "ResourceNotFoundException", "Rule r does not exist on EventBus ghost."},
			{"arn:aws:events:us-east-1:111111111111:event-bus/orders", "ResourceNotFoundException", "Event bus orders does not exist."},
			{"arn:aws:events:eu-west-1:000000000000:rule/orders/r", "ResourceNotFoundException", "Rule r does not exist on EventBus orders."},
			{"arn:aws:events:us-east-1:000000000000:connection/c", "ValidationException", "Parameter ResourceARN is not valid."},
			{"arn:aws:events:us-east-1:000000000000:rule/default/r", "ValidationException", "Parameter ResourceARN is not valid."},
			{queueARN, "ValidationException", "Parameter ResourceARN is not valid."},
		} {
			_, err := call(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: tt.arn})
			wantError(t, err, tt.jsonType, tt.message)
			_, err = call(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: tt.arn, Tags: []eventbridgeapi.Tag{{Key: "k", Value: "v"}}})
			wantError(t, err, tt.jsonType, tt.message)
		}
	})
}

// A bus's stored policy is the Policy member of DescribeEventBus and
// ListEventBuses: its statements in order in a 2012-10-17 document without
// an Id (eventbridge-buses.md 4.2), or the document PutPermission's Policy
// member stored, verbatim (eventbridge-buses.md 10.2.5). A bus without a
// policy has no Policy member.
func TestStoredPolicyIsThePolicyMember(t *testing.T) {
	run(t, func(h *harness) {
		loadStoredV2(h)
		const want = `{"Version":"2012-10-17","Statement":[{"Sid":"s1"},{},{}]}`
		if got := deref(h.describeBus(new("orders")).Policy); got != want {
			t.Errorf("DescribeEventBus orders Policy = %s, want %s", got, want)
		}
		for _, b := range h.listBuses(nil) {
			switch deref(b.Name) {
			case "orders":
				if deref(b.Policy) != want {
					t.Errorf("listed orders Policy = %s, want %s", deref(b.Policy), want)
				}
			default:
				if b.Policy != nil {
					t.Errorf("listed %s Policy = %s, want none", deref(b.Name), *b.Policy)
				}
			}
		}
		if p := h.describeBus(nil).Policy; p != nil {
			t.Errorf("default bus Policy = %s, want none", *p)
		}

		const document = `{ "Version": "2012-10-17", "Statement": [ {"Sid": "s1"} ] }`
		data := replaced(h, storedV2, `"policy":{"statements":`, `"policy":{"document":`+strconv.Quote(document)+`,"statements":`)
		if err := importData(h, exportOne(h), data); err != nil {
			t.Fatal(err)
		}
		if got := deref(h.describeBus(new("orders")).Policy); got != document {
			t.Errorf("Policy with a stored document = %s, want it verbatim", got)
		}
	})
}

// UpdateEventBus without a Name updates the default bus, which stores it
// with the scope's first use as its creation time and the update as its
// last modification (eventbridge-buses.md 3.4, 3.6, 10.2.2;
// events.default-bus.creation-time). A KmsKeyIdentifier of "" clears the
// key and keeps the omitted Description (events.update-bus.omitted-members).
func TestUpdateEventBusOnTheDefaultBus(t *testing.T) {
	run(t, func(h *harness) {
		const key = "alias/bus"
		first := h.Clock.Now().Wire()
		h.createBus("seed")
		h.advance(time.Minute)
		updated := h.Clock.Now().Wire()
		out := must(h, eventbridgeapi.OpUpdateEventBus, &eventbridgeapi.UpdateEventBusInput{Description: new("d"), KmsKeyIdentifier: new(key)})
		if deref(out.Name) != "default" || deref(out.Arn) != busPrefix+"default" || deref(out.Description) != "d" ||
			deref(out.KmsKeyIdentifier) != key || out.DeadLetterConfig != nil || out.LogConfig != nil {
			t.Errorf("UpdateEventBus of the default bus = %+v", out)
		}
		d := h.describeBus(nil)
		if !d.CreationTime.Equal(first) || !d.LastModifiedTime.Equal(updated) || deref(d.Description) != "d" || deref(d.KmsKeyIdentifier) != key {
			t.Errorf("default bus after the update = %+v, want created %v and modified %v", d, first, updated)
		}
		if !slices.Contains(h.resourceARNs(), busPrefix+"default") {
			t.Error("the updated default bus is not stored")
		}

		out = must(h, eventbridgeapi.OpUpdateEventBus, &eventbridgeapi.UpdateEventBusInput{Name: new("default"), KmsKeyIdentifier: new("")})
		if out.KmsKeyIdentifier != nil || deref(out.Description) != "d" {
			t.Errorf("UpdateEventBus with KmsKeyIdentifier \"\" = %+v, want no key and the description kept", out)
		}
		if d := h.describeBus(nil); d.KmsKeyIdentifier != nil || deref(d.Description) != "d" {
			t.Errorf("default bus after clearing the key = %+v", d)
		}
	})
}

// A tag call that changes no tag changes nothing: untagging a key the
// default bus lacks neither stores it nor counts as the scope's first use
// (eventbridge-buses.md 10.2.2).
func TestUntaggingAnAbsentKeyStoresNothing(t *testing.T) {
	run(t, func(h *harness) {
		must(h, eventbridgeapi.OpUntagResource, &eventbridgeapi.UntagResourceInput{ResourceARN: busPrefix + "default", TagKeys: []string{"k"}})
		if arns := h.resourceARNs(); len(arns) != 0 {
			t.Errorf("resources after a no-op untag = %v, want none", arns)
		}
		h.advance(time.Minute)
		if d := h.describeBus(nil); !d.CreationTime.Equal(h.Clock.Now().Wire()) {
			t.Errorf("default bus CreationTime = %v, want the describe instant %v of a scope never written", d.CreationTime, h.Clock.Now().Wire())
		}
	})
}

// CreateEventBus refuses a partner bus before it checks the name, then the
// name default, then a '/' (eventbridge-buses.md 10.7, 2.2); the two name
// texts are the CreateEventBus page's sentences, inferred until EBB-02. The
// quota counts the default bus among the 100 (eventbridge-buses.md 7), with
// the recorded target-limit text, inferred.
func TestCreateEventBusRefusalTexts(t *testing.T) {
	run(t, func(h *harness) {
		const partner = "aws.partner/example.com/123/orders"
		for _, tt := range []struct {
			in                eventbridgeapi.CreateEventBusInput
			jsonType, message string
		}{
			{
				eventbridgeapi.CreateEventBusInput{Name: "default", EventSourceName: new(partner)},
				"NotImplemented",
				"understudy does not implement partner event buses, so CreateEventBus with EventSourceName created nothing. " +
					"Create a custom bus without EventSourceName, or run 'understudy coverage --service eventbridge' for what is supported.",
			},
			{eventbridgeapi.CreateEventBusInput{Name: "default"}, "ValidationException", "You can't use the name default for a custom event bus."},
			{eventbridgeapi.CreateEventBusInput{Name: "team/orders"}, "ValidationException", "Custom event bus names can't contain the / character."},
		} {
			_, err := call(h, eventbridgeapi.OpCreateEventBus, &tt.in)
			wantError(t, err, tt.jsonType, tt.message)
		}

		for i := range 99 {
			h.createBus("bus-" + strconv.Itoa(i))
		}
		_, err := call(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{Name: "bus-99"})
		wantError(t, err, "LimitExceededException", "The requested resource exceeds the maximum number allowed.")
	})
}

// CreateEventBus echoes DeadLetterConfig and LogConfig as sent
// (eventbridge-buses.md 3.1), and is the scope's first use, which the
// default bus reports as its creation time (events.default-bus.creation-time).
// A tag call changes no bus's LastModifiedTime, which is inferred.
func TestCreateEventBusEchoesItsSettings(t *testing.T) {
	run(t, func(h *harness) {
		created := h.Clock.Now().Wire()
		dlq := &eventbridgeapi.DeadLetterConfig{Arn: new("arn:aws:sqs:us-east-1:000000000000:bus-dlq")}
		logs := &eventbridgeapi.LogConfig{IncludeDetail: new(eventbridgeapi.IncludeDetailFull), Level: new(eventbridgeapi.LevelTrace)}
		out := must(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{Name: "echo", DeadLetterConfig: dlq, LogConfig: logs})
		if !reflect.DeepEqual(out.DeadLetterConfig, dlq) || !reflect.DeepEqual(out.LogConfig, logs) {
			t.Errorf("CreateEventBus = %+v, want the dead-letter queue and log config as sent", out)
		}
		h.advance(time.Minute)
		must(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: busPrefix + "echo", Tags: []eventbridgeapi.Tag{{Key: "k", Value: "v"}}})
		if d := h.describeBus(new("echo")); !d.LastModifiedTime.Equal(created) {
			t.Errorf("LastModifiedTime after tagging = %v, want the create instant %v", d.LastModifiedTime, created)
		}
		if d := h.describeBus(nil); !d.CreationTime.Equal(created) {
			t.Errorf("default bus CreationTime = %v, want the first write's instant %v", d.CreationTime, created)
		}
	})
}

// PutEvents matches an event only against the rules of the bus it names
// (eventbridge-buses.md 6.1, 10.4.11); rules on a custom bus come from a
// snapshot here.
func TestPutEventsMatchesTheNamedBusRules(t *testing.T) {
	run(t, func(h *harness) {
		loadStoredV2(h)
		for _, tt := range []struct{ bus, rule string }{
			{"orders", ruleARNOn("orders", "r")},
			{busPrefix + "orders", ruleARNOn("orders", "r")},
			{"default", ruleARN + "d"},
		} {
			e := entry("s", "t", `{}`)
			e.EventBusName = new(tt.bus)
			h.putEvents(e)
			got := h.sink.take()
			if len(got) != 1 || got[0].Request.Source.String() != tt.rule {
				t.Errorf("event on %s delivered %v, want one delivery from %s", tt.bus, got, tt.rule)
			}
		}
	})
}

// A snapshot keeps a created bus's settings and tags and the default bus's
// tags (eventbridge-buses.md 10.2.6): a bus deleted after the export is
// back after the import, described and tagged as before.
func TestSnapshotKeepsCreatedBuses(t *testing.T) {
	run(t, func(h *harness) {
		must(h, eventbridgeapi.OpCreateEventBus, &eventbridgeapi.CreateEventBusInput{
			Name: "orders", Description: new("d"), KmsKeyIdentifier: new("alias/orders"),
			DeadLetterConfig: &eventbridgeapi.DeadLetterConfig{Arn: new("arn:aws:sqs:us-east-1:000000000000:bus-dlq")},
			LogConfig:        &eventbridgeapi.LogConfig{IncludeDetail: new(eventbridgeapi.IncludeDetailNone), Level: new(eventbridgeapi.LevelOff)},
			Tags:             []eventbridgeapi.Tag{{Key: "team", Value: "a"}},
		})
		must(h, eventbridgeapi.OpTagResource, &eventbridgeapi.TagResourceInput{ResourceARN: busPrefix + "default", Tags: []eventbridgeapi.Tag{{Key: "k", Value: "v"}}})
		look := func() []any {
			return []any{
				h.describeBus(new("orders")), h.describeBus(nil),
				must(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: busPrefix + "orders"}).Tags,
				must(h, eventbridgeapi.OpListTagsForResource, &eventbridgeapi.ListTagsForResourceInput{ResourceARN: busPrefix + "default"}).Tags,
			}
		}
		before := look()
		sec := exportOne(h)
		must(h, eventbridgeapi.OpDeleteEventBus, &eventbridgeapi.DeleteEventBusInput{Name: "orders"})
		if err := importData(h, sec, string(sec.Data)); err != nil {
			t.Fatal(err)
		}
		if after := look(); !reflect.DeepEqual(after, before) {
			t.Errorf("after the round trip = %+v, want %+v", after, before)
		}
	})
}
