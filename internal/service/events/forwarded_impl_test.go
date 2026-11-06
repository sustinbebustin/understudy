package events_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// forwardedAt is the time of the forwarded events below.
var forwardedAt = time.Date(2026, 10, 14, 7, 14, 15, 0, time.UTC)

// A forwarded event reaches the receiving bus's rules with the envelope it
// was first put with: the forwarded id, the sender's account and the source
// region, here another region than the receiving bus's, and its time
// (eventbridge-buses.md 6.3, 6.4, 10.4.12). The call's EventId is that id.
func TestForwardedEntryKeepsItsEnvelope(t *testing.T) {
	run(t, func(h *harness) {
		dest := h.createBus("dest")
		h.putRuleOn("dest", "in", `{"source": ["shop"]}`)
		h.targetOn("dest", "in", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		f := delivery.Forwarded{ID: "6a7e8feb-b491-4cf7-a9f1-bf3703467718", Account: "111111111111", Region: "eu-west-1", Time: forwardedAt}
		e := toBus(dest)
		e.Time = &f.Time
		e.Resources = []string{"arn:aws:s3:::bucket"}
		out, err := h.forward(f, "arn:aws:events:eu-west-1:111111111111:rule/fwd", e)
		if err != nil {
			t.Fatalf("forwarded PutEvents = %v", err)
		}
		if out.FailedEntryCount != 0 || len(out.Entries) != 1 || deref(out.Entries[0].EventId) != f.ID {
			t.Fatalf("forwarded PutEvents = %+v, want the forwarded id %s", out, f.ID)
		}
		got := h.sink.take()
		if !slices.Equal(targetARNs(got), []string{queueARN}) {
			t.Fatalf("delivered to %v, want the queue", targetARNs(got))
		}
		d := decode(t, got[0])
		if d.ID != f.ID || d.Account != f.Account || d.Region != f.Region || d.Time != "2026-10-14T07:14:15Z" ||
			d.Source != "shop" || d.DetailType != "order" || !slices.Equal(d.Resources, e.Resources) || !sameJSON(t, string(d.Detail), `{"total": 250}`) {
			t.Errorf("delivered %+v, want the forwarded event %+v", d, f)
		}
	})
}

// An event put once may be forwarded with a reserved aws. source, as a
// scheduled event is (secrets-logs-events-alarms.md 4.3), so the receiving
// bus does not refuse it as a client's PutEvents is refused (4.7). Its
// Detail must still be a JSON object (4.7: MalformedDetail).
func TestForwardedEntryChecks(t *testing.T) {
	run(t, func(h *harness) {
		dest := h.createBus("dest")
		h.putRuleOn("dest", "in", `{"source": ["aws.events"]}`)
		h.targetOn("dest", "in", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		f := delivery.Forwarded{ID: "6a7e8feb-b491-4cf7-a9f1-bf3703467718", Account: account, Region: "us-east-1", Time: forwardedAt}

		scheduled := entry("aws.events", "Scheduled Event", `{}`)
		scheduled.EventBusName = new(dest)
		out, err := h.forward(f, ruleARN+"timer", scheduled)
		if err != nil || out.FailedEntryCount != 0 || deref(out.Entries[0].EventId) != f.ID {
			t.Fatalf("forwarded aws.events entry = %+v, %v; want it accepted as %s", out, err, f.ID)
		}
		if n := len(h.sink.take()); n != 1 {
			t.Errorf("%d deliveries of the forwarded aws.events event, want 1", n)
		}

		malformed := entry("aws.events", "Scheduled Event", `["a"]`)
		malformed.EventBusName = new(dest)
		out, err = h.forward(f, ruleARN+"timer", malformed)
		if err != nil {
			t.Fatalf("forwarded malformed entry = %v", err)
		}
		if e := out.Entries[0]; out.FailedEntryCount != 1 || e.EventId != nil || deref(e.ErrorCode) != "MalformedDetail" || deref(e.ErrorMessage) != "Detail is malformed." {
			t.Errorf("forwarded malformed entry = %+v, want MalformedDetail", e)
		}
		if got := h.sink.take(); len(got) != 0 {
			t.Errorf("delivered %v for the malformed entry", targetARNs(got))
		}
	})
}

// A forwarded event whose destination bus does not exist is dropped as an
// event put to a missing bus is (eventbridge-buses.md 10.3.10, 10.4.14): the
// call succeeds with the forwarded id, and its journal event names the bus.
func TestForwardedEntryToAMissingBus(t *testing.T) {
	run(t, func(h *harness) {
		const gone = busPrefix + "gone"
		h.patternRule("r", `{"source": ["shop"]}`)
		h.target("r", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		f := delivery.Forwarded{ID: "6a7e8feb-b491-4cf7-a9f1-bf3703467718", Account: account, Region: "us-east-1", Time: forwardedAt}
		out, err := h.forward(f, ruleARN+"fwd", toBus(gone))
		if err != nil || out.FailedEntryCount != 0 || deref(out.Entries[0].EventId) != f.ID {
			t.Fatalf("forwarded PutEvents = %+v, %v; want the forwarded id %s", out, err, f.ID)
		}
		if got := h.sink.take(); len(got) != 0 {
			t.Errorf("delivered %v", targetARNs(got))
		}
		if n := len(h.Journal.Query(journal.Filter{ResourceGlob: gone}).Events); n != 1 {
			t.Errorf("%d journal events name the missing bus, want the forwarded PutEvents call", n)
		}
	})
}

// An events-to-events attempt forwards one event per PutEvents call, so a
// forwarded call with another number of entries is a bug in understudy:
// it fails whole and sends nothing.
func TestForwardedCallTakesOneEntry(t *testing.T) {
	run(t, func(h *harness) {
		h.patternRule("r", `{"source": ["shop"]}`)
		h.target("r", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		f := delivery.Forwarded{ID: "6a7e8feb-b491-4cf7-a9f1-bf3703467718", Account: account, Region: "us-east-1", Time: forwardedAt}
		in := &eventbridgeapi.PutEventsInput{Entries: []eventbridgeapi.PutEventsRequestEntry{entry("shop", "order", `{}`), entry("shop", "order", `{}`)}}
		target := kernel.Target{Scope: h.Scope, As: kernel.AsService{Name: "events.amazonaws.com", Source: h.mustARN(ruleARN + "fwd")}, Via: ref.EventsTarget}
		_, err := kernel.Invoke(delivery.WithForwarded(h.Context(), f), h.Kernel, h.Call(eventbridgeapi.OpPutEvents.Descriptor()), eventbridgeapi.OpPutEvents, target, in)
		if err == nil || err.ID != awserr.InternalFailure() {
			t.Errorf("forwarded PutEvents of two entries = %v, want an internal failure", err)
		}
		if got := h.sink.take(); len(got) != 0 {
			t.Errorf("delivered %v", targetARNs(got))
		}
	})
}
