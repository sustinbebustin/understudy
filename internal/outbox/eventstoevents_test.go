package outbox_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// theirBus is an event bus of another account in the rule's region: the
// destination of a cross-account event bus target (eventbridge-buses.md
// 6.3).
func theirBus() aws.ARN {
	return aws.ARN{Partition: "aws", Service: "events", Region: "us-east-1", Account: "111111111111", Resource: "event-bus/orders"}
}

func busRule() aws.ARN { return arn("events", "rule/source-bus/forward") }

// forwarded is the envelope of an event the rule matched and forwards.
func forwarded() *delivery.Forwarded {
	return &delivery.Forwarded{ID: "6a7e8feb-b491-4cf7-a9f1-bf3703467718", Account: "000000000000", Region: "us-east-1", Time: time.Date(2026, 10, 14, 7, 14, 15, 0, time.UTC)}
}

// toBus is an events-to-events delivery of one forwarded event to bus.
func toBus(bus aws.ARN, policy delivery.RetryPolicy) delivery.Request {
	return delivery.Request{
		Source:    busRule(),
		Mechanism: delivery.EventsToEvents,
		Target: delivery.EventBus{ARN: bus, Entries: []delivery.EventEntry{{
			Source: "shop", DetailType: "order", Detail: `{"total":250}`, Resources: []string{"arn:aws:s3:::bucket"}, Forwarded: forwarded(),
		}}},
		Policy: policy,
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// busCall is one PutEvents an events-to-events attempt made.
type busCall struct {
	in        eventbridgeapi.PutEventsInput
	scope     aws.Scope
	principal identity.Principal
	origin    ref.Origin
	forwarded delivery.Forwarded
	carried   bool
}

// stubBus stubs PutEvents, answering each entry with answer's code ("" puts
// it), and records every call with the Forwarded its context carried.
func stubBus(k *kerneltest.K, answer func(n int) string) *recorder[busCall] {
	var calls recorder[busCall]
	k.Stub(eventbridgeapi.OpPutEvents, func(ctx context.Context, c *kernel.Call, in *eventbridgeapi.PutEventsInput) (*eventbridgeapi.PutEventsOutput, *awserr.Error) {
		f, carried := delivery.ForwardedFrom(ctx)
		n := calls.add(busCall{in: *in, scope: c.Scope(), principal: c.Principal(), origin: c.Origin(), forwarded: f, carried: carried})
		out := &eventbridgeapi.PutEventsOutput{}
		for range in.Entries {
			if code := answer(n); code != "" {
				out.FailedEntryCount++
				out.Entries = append(out.Entries, eventbridgeapi.PutEventsResultEntry{ErrorCode: new(code), ErrorMessage: new("the stub failed it")})
				continue
			}
			out.Entries = append(out.Entries, eventbridgeapi.PutEventsResultEntry{EventId: new(f.ID)})
		}
		return out, nil
	})
	return &calls
}

// An events-to-events delivery is an event bus target's: Enqueue takes an
// EventBus target under EventsToEvents (eventbridge-buses.md 10.4.12), with
// the one entry the outbox puts per delivery and the envelope of the event
// it forwards. Without that envelope, with one under another mechanism, or
// with a Detail that is no JSON object (MalformedDetail,
// secrets-logs-events-alarms.md 4.7), the request is refused before
// anything is queued, naming the member at fault. Under EventsToEvents
// every other target kind is still refused, naming both.
func TestEnqueueEventsToEventsChecks(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		if _, err := o.Enqueue(toBus(theirBus(), delivery.EventBridgeTarget{MaxAge: time.Hour, MaxAttempts: 3}), ref.Cause{}, k.Clock.Now()); err != nil {
			k.Fatalf("Enqueue of an events-to-events delivery = %v, want it queued", err)
		}
		withEntry := func(edit func(e *delivery.EventEntry)) delivery.Request {
			req := toBus(theirBus(), delivery.None{})
			bus := delivery.EventBus{ARN: theirBus(), Entries: []delivery.EventEntry{{Source: "shop", DetailType: "order", Detail: `{"total":250}`, Forwarded: forwarded()}}}
			edit(&bus.Entries[0])
			req.Target = bus
			return req
		}
		noEnvelope := withEntry(func(e *delivery.EventEntry) { e.Forwarded = nil })
		otherMechanism := toBus(theirBus(), delivery.None{})
		otherMechanism.Mechanism = delivery.S3ToSQS
		array := withEntry(func(e *delivery.EventEntry) { e.Detail = `["a"]` })
		for name, c := range map[string]struct {
			req   delivery.Request
			field string
		}{
			"no Forwarded":                   {noEnvelope, "Target.Entries[0].Forwarded"},
			"Forwarded under S3ToSQS":        {otherMechanism, "Target.Entries[0].Forwarded"},
			"a Detail that is not an object": {array, "Target.Entries[0].Detail"},
		} {
			_, err := o.Enqueue(c.req, ref.Cause{}, k.Clock.Now())
			if rerr, ok := errors.AsType[*outbox.RequestError](err); !ok || rerr.Field != c.field {
				k.Errorf("%s: Enqueue = %v, want a *RequestError for %s", name, err, c.field)
			}
		}
		hook, err := url.Parse("http://127.0.0.1:8080/hook")
		if err != nil {
			k.Fatal(err)
		}
		for name, target := range map[string]delivery.Target{
			"SQSQueue":     delivery.SQSQueue{ARN: arn("sqs", "q"), Body: "{}"},
			"S3Object":     delivery.S3Object{Bucket: "audit", Key: "k", Body: []byte("b")},
			"HTTPEndpoint": delivery.HTTPEndpoint{URL: *hook, Body: []byte("b")},
		} {
			req := delivery.Request{Source: busRule(), Mechanism: delivery.EventsToEvents, Target: target, Policy: delivery.None{}}
			_, err := o.Enqueue(req, ref.Cause{}, k.Clock.Now())
			rerr, ok := errors.AsType[*outbox.RequestError](err)
			if !ok || !strings.HasPrefix(rerr.Field, "Target") || !strings.Contains(rerr.Reason, delivery.EventsToEvents.String()) {
				k.Errorf("%s under events-to-events: Enqueue = %v, want a *RequestError for its Target naming events-to-events", name, err)
			}
		}
		if r := o.Resources(k.Scope); len(r) != 0 {
			k.Errorf("Resources in the rule's scope = %v, want nothing queued there", r)
		}
	}, kerneltest.Services(eventbridgeapi.Service()))
}

// An events-to-events attempt is one PutEvents in the destination bus's
// scope, as EventBridge on the rule's behalf over an EventBridge target
// (the service principal events.amazonaws.com, iam-enforcement-surfaces.md
// 8.4), whose one entry names the bus by its ARN (API_PutEventsRequestEntry
// accepts an ARN, https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEventsRequestEntry.html)
// and carries the forwarded event's time, and whose context carries the
// envelope, so the receiving bus keeps the event's id, account and region
// (eventbridge-buses.md 6.3, 10.4.12). The delivery is Delivered.
func TestEventsToEventsAttemptCarriesTheEnvelope(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		calls := stubBus(k, func(int) string { return "" })
		req := toBus(theirBus(), delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: 185})
		enqueue(k, o, req, source(k))
		settled(k)
		got := calls.all()
		if len(got) != 1 {
			k.Fatalf("%d PutEvents calls, want 1", len(got))
		}
		c := got[0]
		if c.scope.Account.String() != "111111111111" || c.scope.Region.String() != "us-east-1" {
			k.Errorf("PutEvents in %v, want the destination bus's scope 111111111111 us-east-1", c.scope)
		}
		if c.principal != (identity.ServicePrincipal{Name: "events.amazonaws.com"}) || c.origin != (ref.Internal{Via: ref.EventsTarget}) {
			k.Errorf("PutEvents by %v from %v, want events.amazonaws.com over an EventBridge target", c.principal, c.origin)
		}
		want := forwarded()
		if !c.carried || c.forwarded != *want {
			k.Errorf("context carried %+v (%t), want %+v", c.forwarded, c.carried, *want)
		}
		if len(c.in.Entries) != 1 {
			k.Fatalf("%d request entries, want 1", len(c.in.Entries))
		}
		e := c.in.Entries[0]
		if deref(e.EventBusName) != theirBus().String() || deref(e.Source) != "shop" || deref(e.DetailType) != "order" ||
			deref(e.Detail) != `{"total":250}` || len(e.Resources) != 1 || e.Resources[0] != "arn:aws:s3:::bucket" {
			k.Errorf("entry = %+v, want the forwarded event for %s", e, theirBus())
		}
		if e.Time == nil || !e.Time.Equal(want.Time) {
			k.Errorf("entry Time = %v, want the forwarded event's time %v", e.Time, want.Time)
		}
		events := deliveries(k)
		if len(events) != 1 {
			k.Fatalf("delivery events = %+v, want one", events)
		}
		if d := detail[journal.Delivery](k, events[0]); d.Outcome != (journal.Delivered{}) || d.From != busRule() || d.To != theirBus() || d.Mechanism != delivery.EventsToEvents {
			k.Errorf("delivery = %+v, want Delivered from the rule to the bus under events-to-events", d)
		}
	}, kerneltest.Services(eventbridgeapi.Service()))
}

// An event bus target with a RoleArn is delivered as a session of that role
// (iam-enforcement-surfaces.md 8.4; eb-event-bus-perms.html): Enqueue takes
// an events-to-events request with As, the PutEvents runs as the role's
// session in the destination bus's scope, and a dead letter is still sent
// as events.amazonaws.com, which the queue policy grants (8.1,
// eb-rule-dlq.html). As stays refused on the other EventBridge mechanisms,
// and an As naming no IAM role is refused.
func TestEventsToEventsWithARoleRunsAsItsSession(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		calls := stubBus(k, func(n int) string {
			if n > 0 {
				return "InternalFailure"
			}
			return ""
		})
		role := aws.ARN{Partition: "aws", Service: "iam", Account: "000000000000", Resource: "role/forwarder"}
		req := toBus(theirBus(), delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: 0})
		req.As = delivery.ExecutionRole{Role: role}
		dlq := arn("sqs", "events-dlq")
		req.DLQ = &dlq
		cause := source(k)
		enqueue(k, o, req, cause)
		enqueue(k, o, req, cause)
		settled(k)
		advance(k, time.Hour)
		got := calls.all()
		if len(got) != 2 {
			k.Fatalf("%d PutEvents calls, want 2", len(got))
		}
		r, ok := got[0].principal.(identity.AssumedRole)
		if !ok || r.Account.String() != "000000000000" || r.Role.Name != "forwarder" || got[0].scope.Account.String() != "111111111111" {
			k.Errorf("PutEvents by %#v in %v, want a session of role forwarder in the bus's scope 111111111111", got[0].principal, got[0].scope)
		}
		sent := q.sent()
		if len(sent) != 1 || sent[0].principal != (identity.ServicePrincipal{Name: "events.amazonaws.com"}) {
			k.Errorf("dead letters = %+v, want one sent as events.amazonaws.com", sent)
		}
		other := toQueue("q", delivery.EventBridgeTarget{MaxAge: time.Hour, MaxAttempts: 3})
		other.Mechanism, other.Source = delivery.EventsToSQS, busRule()
		other.As = req.As
		if _, err := o.Enqueue(other, ref.Cause{}, k.Clock.Now()); err == nil {
			k.Error("Enqueue of an events-to-SQS delivery with As accepted, want it refused")
		}
		notRole := req
		notRole.As = delivery.ExecutionRole{Role: arn("sqs", "q")}
		if _, err := o.Enqueue(notRole, ref.Cause{}, k.Clock.Now()); err == nil {
			k.Error("Enqueue of an events-to-events delivery as a queue ARN accepted, want it refused")
		}
	}, kerneltest.Services(eventbridgeapi.Service()))
}

// The snapshot record of an event bus entry gained its envelope (version
// 2), then a Lambda delivery's sender and a queue target's trace header
// (version 3), then a request's aws:SourceAccount (version 4), then an
// SNS-to-Lambda request's built dead letter (version 5), so a section an
// older binary wrote is refused naming both versions rather than migrated
// or restored without it (ADR 0009).
func TestOutboxSnapshotRefusesAnEarlierVersion(t *testing.T) {
	for _, earlier := range []int{1, 2, 3, 4} {
		run(t, func(k *kerneltest.K, o *outbox.Outbox) {
			stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return throttled("") })
			enqueue(k, o, toQueue("pending", delivery.SNSManaged{}), source(k))
			settled(k)
			sections, err := o.Export()
			if err != nil {
				k.Fatal(err)
			}
			if len(sections) == 0 {
				k.Fatal("Export of a pending delivery wrote no section")
			}
			for i := range sections {
				sections[i].Version = earlier
			}
			err = o.Import(sections)
			vm, ok := errors.AsType[*store.VersionMismatchError](err)
			if !ok {
				k.Fatalf("Import(a version %d section) = %v, want a *store.VersionMismatchError", earlier, err)
			}
			if vm.Section != earlier || vm.Running != 5 || o.SchemaVersion() != 5 {
				k.Errorf("mismatch %+v, schema version %d; want section %d refused by running 5", vm, o.SchemaVersion(), earlier)
			}
		})
	}
}

// The access advisory models a service principal against the target's
// resource policy, which is not how AWS authorizes a bus target (its
// RoleArn, or the sender account against the bus policy,
// eventbridge-buses.md 6.2, 6.3), so an events-to-events delivery gets no
// Advisory even when the bus policy grants nothing, and is still made
// (eventbridge-buses.md 10.7.22: policies are not enforced).
func TestEventsToEventsMakesNoAdvisory(t *testing.T) {
	runWith(t, outbox.Options{Policies: policies{theirBus(): nil}}, func(k *kerneltest.K, o *outbox.Outbox) {
		calls := stubBus(k, func(int) string { return "" })
		enqueue(k, o, toBus(theirBus(), delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: 185}), source(k))
		settled(k)
		if a := k.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}).Events; len(a) != 0 {
			k.Errorf("advisories = %+v, want none for an events-to-events delivery", a)
		}
		if n := len(calls.all()); n != 1 {
			k.Errorf("%d PutEvents calls, want the delivery made", n)
		}
	}, kerneltest.Services(eventbridgeapi.Service()))
}

// An event bus target takes a dead-letter queue like any EventBridge target
// (eb-rule-dlq.html, iam-enforcement-surfaces.md 8.4): an events-to-events
// delivery whose retries are exhausted sends the event as the destination
// bus would have received it to the queue, with the EventBridge dead-letter
// attributes naming the rule and the bus.
func TestEventsToEventsDeadLetter(t *testing.T) {
	run(t, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		stubBus(k, func(int) string { return "InternalFailure" })
		dlq := arn("sqs", "events-dlq")
		req := toBus(theirBus(), delivery.EventBridgeTarget{MaxAge: 24 * time.Hour, MaxAttempts: 0})
		req.DLQ = &dlq
		enqueue(k, o, req, source(k))
		settled(k)
		advance(k, time.Hour)
		sent := q.sent()
		if len(sent) != 1 || sent[0].queue != "events-dlq" {
			k.Fatalf("SendMessage calls = %+v, want one dead letter to events-dlq", sent)
		}
		// The envelope's member order is the recorded one
		// (secrets-logs-events-alarms.md 4.3).
		const body = `{"version":"0","id":"6a7e8feb-b491-4cf7-a9f1-bf3703467718","detail-type":"order","source":"shop",` +
			`"account":"000000000000","time":"2026-10-14T07:14:15Z","region":"us-east-1","resources":["arn:aws:s3:::bucket"],"detail":{"total":250}}`
		if sent[0].in.MessageBody != body {
			k.Errorf("dead letter body = %s, want %s", sent[0].in.MessageBody, body)
		}
		attrs := map[string]string{}
		for name, v := range sent[0].in.MessageAttributes {
			if v.StringValue != nil {
				attrs[name] = *v.StringValue
			}
		}
		// RULE_ARN, TARGET_ARN, EXHAUSTED_RETRY_CONDITION and RETRY_ATTEMPTS
		// (eb-rule-dlq.html, iam-enforcement-surfaces.md 8.4).
		for name, want := range map[string]string{
			"RULE_ARN": busRule().String(), "TARGET_ARN": theirBus().String(),
			"EXHAUSTED_RETRY_CONDITION": "MaximumRetryAttempts", "RETRY_ATTEMPTS": "0",
		} {
			if attrs[name] != want {
				k.Errorf("dead letter attribute %s = %q, want %q (attributes %v)", name, attrs[name], want, attrs)
			}
		}
		events := deliveries(k)
		if d := detail[journal.Delivery](k, events[len(events)-1]); d.Outcome != (journal.DeadLettered{DLQ: dlq}) {
			k.Errorf("last outcome = %#v, want dead-lettered to %s", d.Outcome, dlq)
		}
	}, kerneltest.Services(eventbridgeapi.Service()))
}
