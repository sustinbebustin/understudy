package events_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
)

// entry is a well-formed PutEvents entry with the given detail.
func entry(source, detailType, detail string) eventbridgeapi.PutEventsRequestEntry {
	return eventbridgeapi.PutEventsRequestEntry{Source: new(source), DetailType: new(detailType), Detail: new(detail)}
}

func (h *harness) putEvents(entries ...eventbridgeapi.PutEventsRequestEntry) *eventbridgeapi.PutEventsOutput {
	h.Helper()
	return must(h, eventbridgeapi.OpPutEvents, &eventbridgeapi.PutEventsInput{Entries: entries})
}

func (h *harness) patternRule(name, pattern string) {
	h.Helper()
	must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: name, EventPattern: new(pattern)})
}

// An event goes to the targets of the rules whose patterns match it and to
// no other, in the order the targets were created across those rules.
func TestPutEventsDeliversToMatchingTargetsInCreationOrder(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("orders", `{"source": ["shop"], "detail-type": ["order"]}`)
		h.patternRule("big", `{"detail": {"total": [{"numeric": [">", 100]}]}}`)
		h.patternRule("refunds", `{"detail-type": ["refund"]}`)
		const fn = "arn:aws:lambda:us-east-1:000000000000:function:worker"
		const topic = "arn:aws:sns:us-east-1:000000000000:alerts"
		h.target("big", eventbridgeapi.Target{Id: "1", Arn: topic})
		h.target("orders", eventbridgeapi.Target{Id: "2", Arn: queueARN})
		h.target("refunds", eventbridgeapi.Target{Id: "3", Arn: "arn:aws:sqs:us-east-1:000000000000:refunds"})
		h.target("orders", eventbridgeapi.Target{Id: "4", Arn: fn})
		h.target("big", eventbridgeapi.Target{Id: "5", Arn: queueARN})

		out := h.putEvents(entry("shop", "order", `{"total": 250}`))
		if out.FailedEntryCount != 0 || len(out.Entries) != 1 || out.Entries[0].EventId == nil {
			t.Fatalf("PutEvents = %+v", out)
		}
		got := h.sink.take()
		if want := []string{topic, queueARN, fn, queueARN}; !slices.Equal(targetARNs(got), want) {
			t.Fatalf("delivered to %v, want %v", targetARNs(got), want)
		}
		wantMech := []delivery.Mechanism{delivery.EventsToSNS, delivery.EventsToSQS, delivery.EventsToLambda, delivery.EventsToSQS}
		for i, d := range got {
			e := decode(t, d)
			if e.ID != *out.Entries[0].EventId || e.Source != "shop" || e.DetailType != "order" || string(e.Detail) != `{"total":250}` ||
				e.Account != account || e.Region != "us-east-1" || e.Resources == nil || len(e.Resources) != 0 {
				t.Errorf("delivery %d = %+v", i, e)
			}
			if d.Request.Mechanism != wantMech[i] {
				t.Errorf("delivery %d mechanism = %s, want %s", i, d.Request.Mechanism, wantMech[i])
			}
		}
		if f, ok := got[2].Request.Target.(delivery.LambdaFunction); !ok || f.Invocation != delivery.InvocationEvent {
			t.Errorf("Lambda delivery = %+v, want an asynchronous invocation", got[2].Request.Target)
		}

		h.putEvents(entry("shop", "order", `{"total": 5}`))
		if got := targetARNs(h.sink.take()); !slices.Equal(got, []string{queueARN, fn}) {
			t.Errorf("small order delivered to %v", got)
		}
		h.putEvents(entry("bank", "refund", `{}`), entry("bank", "other", `{}`))
		if got := targetARNs(h.sink.take()); !slices.Equal(got, []string{"arn:aws:sqs:us-east-1:000000000000:refunds"}) {
			t.Errorf("refund delivered to %v", got)
		}
	})
}

// A disabled rule matches nothing; a rule with only a schedule takes no
// PutEvents event.
func TestPutEventsSkipsDisabledAndScheduleOnlyRules(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("off", `{"source": ["s"]}`)
		h.target("off", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		must(h, eventbridgeapi.OpDisableRule, &eventbridgeapi.DisableRuleInput{Name: "off"})
		h.schedule("timer", "rate(1 day)")
		h.putEvents(entry("s", "t", `{}`))
		if got := h.sink.take(); len(got) != 0 {
			t.Errorf("delivered %v", targetARNs(got))
		}
	})
}

// The event time is the PutEvents instant to the second, or the entry's
// Time; Resources are delivered as sent.
func TestPutEventsTime(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("all", `{"source": ["s"]}`)
		h.target("all", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		h.advance(1500 * time.Millisecond)
		now := h.Clock.Now().Wire()
		sent := time.Date(2024, 5, 13, 12, 27, 7, 0, time.UTC)
		withTime := entry("s", "t", `{}`)
		withTime.Time = &sent
		withTime.Resources = []string{"arn:aws:s3:::bucket"}
		h.putEvents(entry("s", "t", `{}`), withTime)
		got := h.sink.take()
		if len(got) != 2 {
			t.Fatalf("%d deliveries, want 2", len(got))
		}
		if e := decode(t, got[0]); e.Time != now.Truncate(time.Second).Format(time.RFC3339) {
			t.Errorf("time = %s, want the call instant %s to the second", e.Time, now)
		}
		if e := decode(t, got[1]); e.Time != "2024-05-13T12:27:07Z" || !slices.Equal(e.Resources, []string{"arn:aws:s3:::bucket"}) {
			t.Errorf("event with Time = %+v", e)
		}
	})
}

// A target's input shapes what it receives: Input replaces the event,
// InputPath selects from it and InputTransformer fills a template.
func TestTargetInputsShapeThePayload(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["core.update-account-command"]}`)
		h.target("r", eventbridgeapi.Target{Id: "constant", Arn: queueARN, Input: new(`{"custom-value": "somecustominput"}`)})
		h.target("r", eventbridgeapi.Target{Id: "path", Arn: queueARN, InputPath: new("$.detail.payload")})
		h.target("r", eventbridgeapi.Target{Id: "string", Arn: queueARN, InputPath: new("$.detail.payload.sf_id")})
		h.target("r", eventbridgeapi.Target{Id: "transformer", Arn: queueARN, InputTransformer: &eventbridgeapi.InputTransformer{
			InputPathsMap: map[string]string{"detail-type": "$.detail-type", "command": "$.detail.command"},
			InputTemplate: `{"detailType": <detail-type>, "command": <command>, "rule": <aws.events.rule-name>}`,
		}})
		h.target("r", eventbridgeapi.Target{Id: "text", Arn: queueARN, InputTransformer: &eventbridgeapi.InputTransformer{
			InputPathsMap: map[string]string{"payload": "$.detail.payload"},
			InputTemplate: `"Payload of <payload> on <aws.events.rule-arn>"`,
		}})
		h.putEvents(entry("core.update-account-command", "core.update-account-command",
			`{"command": "update-account", "payload": {"acc_id": "0a787ecb-4015", "sf_id": "baz"}}`))
		got := h.sink.take()
		bodies := make([]string, len(got))
		for i, d := range got {
			bodies[i] = body(t, d)
		}
		want := []string{
			`{"custom-value": "somecustominput"}`,
			`{"acc_id":"0a787ecb-4015","sf_id":"baz"}`,
			`"baz"`,
			`{"detailType": "core.update-account-command", "command": "update-account", "rule": "r"}`,
			`"Payload of {acc_id:0a787ecb-4015,sf_id:baz} on ` + ruleARN + `r"`,
		}
		if !slices.Equal(bodies, want) {
			t.Errorf("bodies =\n%s\nwant\n%s", strings.Join(bodies, "\n"), strings.Join(want, "\n"))
		}
	})
}

// A target whose InputPath selects nothing receives nothing; the journal
// records the dropped delivery under the PutEvents call.
func TestUnmatchedInputPathIsJournaledAsDropped(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["s"]}`)
		h.target("r", eventbridgeapi.Target{Id: "path", Arn: queueARN, InputPath: new("$.detail.missing")})
		h.putEvents(entry("s", "t", `{}`))
		if got := h.sink.take(); len(got) != 0 {
			t.Fatalf("delivered %v", targetARNs(got))
		}
		page := h.Journal.Query(journal.Filter{Kind: journal.KindDelivery})
		if len(page.Events) != 1 {
			t.Fatalf("%d delivery events, want 1", len(page.Events))
		}
		d, ok := page.Events[0].Detail.(journal.Delivery)
		if !ok || d.From.String() != ruleARN+"r" || d.To.String() != queueARN || d.Mechanism != delivery.EventsToSQS {
			t.Fatalf("journaled %+v", page.Events[0].Detail)
		}
		if _, dropped := d.Outcome.(journal.Dropped); !dropped {
			t.Errorf("outcome = %T, want Dropped", d.Outcome)
		}
		calls := h.Journal.Query(journal.Filter{Kind: journal.KindAPICall, Operation: eventbridgeapi.OpPutEvents.Descriptor().ID})
		if len(calls.Events) != 1 || page.Events[0].Cause.Parent != calls.Events[0].ID {
			t.Errorf("the drop's parent is %d, want the PutEvents call", page.Events[0].Cause.Parent)
		}
	})
}

// Entries missing a required member or with a malformed Detail fail alone,
// with HTTP 200 and the recorded codes and messages.
func TestPutEventsEntryFailures(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["s"]}`)
		h.target("r", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		noSource := entry("", "t", `{}`)
		noSource.Source = nil
		noDetail := entry("s", "t", "")
		noDetail.Detail = nil
		out := h.putEvents(
			noSource,
			entry("s", "", `{}`),
			noDetail,
			entry("s", "t", `"string"`),
			entry("s", "t", `["a"]`),
			entry("s", "t", `{"a":`),
			entry("s", "t", `{"ok": true}`),
		)
		want := [][2]string{
			{"InvalidArgument", "Parameter Source is not valid. Reason: Source is a required argument."},
			{"InvalidArgument", "Parameter DetailType is not valid. Reason: DetailType is a required argument."},
			{"InvalidArgument", "Parameter Detail is not valid. Reason: Detail is a required argument."},
			{"MalformedDetail", "Detail is malformed."},
			{"MalformedDetail", "Detail is malformed."},
			{"MalformedDetail", "Detail is malformed."},
		}
		if out.FailedEntryCount != 6 || len(out.Entries) != 7 {
			t.Fatalf("PutEvents = %+v", out)
		}
		for i, w := range want {
			e := out.Entries[i]
			if e.EventId != nil || e.ErrorCode == nil || *e.ErrorCode != w[0] || *e.ErrorMessage != w[1] {
				t.Errorf("entry %d = %+v, want %s %q", i, e, w[0], w[1])
			}
		}
		if out.Entries[6].EventId == nil || out.Entries[6].ErrorCode != nil {
			t.Errorf("valid entry = %+v", out.Entries[6])
		}
		if n := len(h.sink.take()); n != 1 {
			t.Errorf("%d deliveries, want the valid entry's one", n)
		}
	})
}

// A request whose entries add up to 1 MiB or more fails whole. An entry's
// size counts 14 bytes for a Time and the bytes of Source, DetailType,
// Detail and each Resources element.
func TestPutEventsSizeLimit(t *testing.T) {
	run(t, func(h *harness) {
		big := `{"a": "` + strings.Repeat("x", 300<<10) + `"}`
		_, err := call(h, eventbridgeapi.OpPutEvents, &eventbridgeapi.PutEventsInput{Entries: []eventbridgeapi.PutEventsRequestEntry{
			entry("s", "t", big), entry("s", "t", big), entry("s", "t", big), entry("s", "t", big),
		}})
		wantError(t, err, "ValidationException", "Total size of the entries in the request is over the limit.")
		h.putEvents(entry("s", "t", big), entry("s", "t", big), entry("s", "t", big))

		// entries totals size bytes: three big entries and one with a Time,
		// a Resources element and a Detail that pads to size.
		const resource = "arn:aws:s3:::bucket"
		entries := func(size int) []eventbridgeapi.PutEventsRequestEntry {
			sent := time.Date(2024, 5, 13, 12, 27, 7, 0, time.UTC)
			used := 3*(len("s")+len("t")+len(big)) + len("s") + len("t") + 14 + len(resource) + len(`{"a": ""}`)
			last := entry("s", "t", `{"a": "`+strings.Repeat("y", size-used)+`"}`)
			last.Time = &sent
			last.Resources = []string{resource}
			return []eventbridgeapi.PutEventsRequestEntry{entry("s", "t", big), entry("s", "t", big), entry("s", "t", big), last}
		}
		_, err = call(h, eventbridgeapi.OpPutEvents, &eventbridgeapi.PutEventsInput{Entries: entries(1 << 20)})
		wantError(t, err, "ValidationException", "Total size of the entries in the request is over the limit.")
		if out := h.putEvents(entries(1<<20 - 1)...); out.FailedEntryCount != 0 {
			t.Errorf("1 MiB - 1 byte: PutEvents = %+v", out)
		}
	})
}

// An event whose evaluation against a rule's pattern exceeds the matcher's
// work bound reaches none of that rule's targets, and the journal records
// a dropped delivery to each; other rules still receive the event.
func TestPutEventsPatternEvaluationFailureIsJournaledAsDropped(t *testing.T) {
	run(t, func(h *harness) {
		pattern, detail := joinedArrays(6)
		h.patternRule("deep", `{"detail": `+pattern+`}`)
		h.patternRule("plain", `{"source": ["s"]}`)
		const other = "arn:aws:sqs:us-east-1:000000000000:other"
		h.target("deep", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		h.target("plain", eventbridgeapi.Target{Id: "o", Arn: other})
		h.putEvents(entry("s", "t", detail))
		if got := targetARNs(h.sink.take()); !slices.Equal(got, []string{other}) {
			t.Fatalf("delivered to %v, want only the plain rule's target", got)
		}
		page := h.Journal.Query(journal.Filter{Kind: journal.KindDelivery})
		if len(page.Events) != 1 {
			t.Fatalf("%d delivery events, want 1", len(page.Events))
		}
		d, ok := page.Events[0].Detail.(journal.Delivery)
		if !ok || d.From.String() != ruleARN+"deep" || d.To.String() != queueARN || d.Mechanism != delivery.EventsToSQS {
			t.Fatalf("journaled %+v", page.Events[0].Detail)
		}
		dropped, ok := d.Outcome.(journal.Dropped)
		if !ok || !strings.HasPrefix(dropped.Reason, "event pattern evaluation failed: ") ||
			!strings.HasSuffix(dropped.Reason, "; the event was not delivered to this rule's targets") {
			t.Errorf("outcome = %+v, want Dropped naming the evaluation failure", d.Outcome)
		}
	})
}

// joinedArrays is a pattern and an event detail that join six levels of
// 16-element arrays into one array-consistent search, which exceeds the
// matcher's work bound (internal/ruler TestSearchBudget).
func joinedArrays(levels int) (pattern, detail string) {
	const size = 16
	pattern = `{"b":[1],"z":[2]}`
	for range levels - 1 {
		pattern = `{"b":[1],"z":[1],"m":` + pattern + `}`
	}
	elems := "[" + strings.Repeat(`{"b":1,"z":1,"m":{"b":1}},`, size-1) + `{"b":1,"z":1,"m":{"b":1}}]`
	dotted := make([]string, levels)
	for j := range levels {
		dotted[j] = `"a` + strings.Repeat(".m", j) + `":` + elems
	}
	return `{"a":` + pattern + `}`, `{` + strings.Join(dotted, ",") + `}`
}

// Events for a bus that does not exist get an EventId and are dropped; the
// PutEvents call's journal entry names the bus.
func TestPutEventsToAMissingBusIsDropped(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["s"]}`)
		h.target("r", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		e := entry("s", "t", `{}`)
		e.EventBusName = new("nope")
		out := h.putEvents(e)
		if out.FailedEntryCount != 0 || out.Entries[0].EventId == nil {
			t.Fatalf("PutEvents = %+v", out)
		}
		if got := h.sink.take(); len(got) != 0 {
			t.Errorf("delivered %v", targetARNs(got))
		}
		calls := h.Journal.Query(journal.Filter{ResourceGlob: "arn:aws:events:us-east-1:000000000000:event-bus/nope"})
		if len(calls.Events) != 1 {
			t.Errorf("%d journal events name the missing bus, want the PutEvents call", len(calls.Events))
		}
		// A bus ARN names its own account and region, not the caller's.
		for _, bus := range []string{
			"arn:aws:events:us-east-1:111111111111:event-bus/default",
			"arn:aws:events:eu-west-1:000000000000:event-bus/custom",
		} {
			e.EventBusName = new(bus)
			h.putEvents(e)
			if got := h.sink.take(); len(got) != 0 {
				t.Errorf("%s: delivered %v", bus, targetARNs(got))
			}
			if n := len(h.Journal.Query(journal.Filter{ResourceGlob: bus}).Events); n != 1 {
				t.Errorf("%d journal events name %s, want the PutEvents call", n, bus)
			}
		}
		if n := len(h.Journal.Query(journal.Filter{ResourceGlob: "arn:aws:events:us-east-1:000000000000:event-bus/default"}).Events); n != 0 {
			t.Errorf("%d journal events name the caller's default bus, want none", n)
		}
		e.EventBusName = new("arn:aws:events:us-east-1:000000000000:event-bus/default")
		h.putEvents(e)
		if n := len(h.sink.take()); n != 1 {
			t.Errorf("the default bus by ARN delivered %d events, want 1", n)
		}
	})
}

// TestEventPattern evaluates a pattern against an event envelope.
func TestTestEventPattern(t *testing.T) {
	run(t, func(h *harness) {
		const event = `{"id": "1", "account": "123456789012", "source": "shop", "time": "2024-01-01T00:00:00Z", "region": "us-east-1", "resources": [], "detail-type": "order", "detail": {"total": 250}}`
		for pattern, want := range map[string]bool{
			`{"source": ["shop"]}`:                                                true,
			`{"source": ["bank"]}`:                                                false,
			`{"detail": {"total": [{"numeric": [">", 100]}]}}`:                    true,
			`{"detail": {"total": [{"numeric": ["<=", 100]}]}}`:                   false,
			`{"detail-type": [{"prefix": "ord"}], "account": [{"exists": true}]}`: true,
		} {
			out := must(h, eventbridgeapi.OpTestEventPattern, &eventbridgeapi.TestEventPatternInput{EventPattern: pattern, Event: event})
			if out.Result != want {
				t.Errorf("%s = %v, want %v", pattern, out.Result, want)
			}
		}
		_, err := call(h, eventbridgeapi.OpTestEventPattern, &eventbridgeapi.TestEventPatternInput{EventPattern: `{"source": "shop"}`, Event: event})
		wantError(t, err, "InvalidEventPatternException", `Event pattern is not valid. Reason: "source" must be an object or an array`)
		for _, bad := range []string{`"string"`, `{"source": "shop"}`, `{`} {
			_, err := call(h, eventbridgeapi.OpTestEventPattern, &eventbridgeapi.TestEventPatternInput{EventPattern: `{"source": ["shop"]}`, Event: bad})
			wantError(t, err, "ValidationException", "Parameter Event is not valid.")
		}
	})
}
