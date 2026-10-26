package events

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/ruler"
	"github.com/sustinbebustin/understudy/internal/service/events/envelope"
)

// deliveryTarget is a target ARN understudy delivers to, parsed: a Lambda
// function, an SQS queue, an SNS topic or an event bus. Each kind fixes its
// mechanism.
type deliveryTarget interface {
	arn() aws.ARN
	// mechanism is how the outbox delivers to the target.
	mechanism() delivery.Mechanism
}

// payloadTarget is a deliveryTarget that receives the payload the stored
// target's input shapes from the event: a function, a queue or a topic. An
// event bus receives the event itself (fanOut.forward).
type payloadTarget interface {
	deliveryTarget
	// receiver is the delivery of body, the payload for event ev, to the
	// target for stored target t. An error is a target parameter ev
	// cannot fill, and the target receives nothing.
	receiver(body []byte, t *Target, ev envelope.Event) (delivery.Target, error)
}

// The payload targets. busTarget is none: it receives the event itself.
var (
	_ payloadTarget = functionTarget{}
	_ payloadTarget = queueTarget{}
	_ payloadTarget = topicTarget{}
)

// functionTarget is a Lambda function, with or without a qualifier, which
// receives an asynchronous invocation.
type functionTarget struct{ function aws.ARN }

// queueTarget is an SQS queue.
type queueTarget struct{ queue aws.ARN }

// topicTarget is an SNS topic.
type topicTarget struct{ topic aws.ARN }

// busTarget is an event bus of any served account and region, to which
// the rule forwards the matched event (eventbridge-buses.md 6.2, 10.4.12).
type busTarget struct{ bus aws.ARN }

func (f functionTarget) arn() aws.ARN { return f.function }
func (q queueTarget) arn() aws.ARN    { return q.queue }
func (t topicTarget) arn() aws.ARN    { return t.topic }
func (b busTarget) arn() aws.ARN      { return b.bus }

func (functionTarget) mechanism() delivery.Mechanism { return delivery.EventsToLambda }
func (queueTarget) mechanism() delivery.Mechanism    { return delivery.EventsToSQS }
func (topicTarget) mechanism() delivery.Mechanism    { return delivery.EventsToSNS }
func (busTarget) mechanism() delivery.Mechanism      { return delivery.EventsToEvents }

// crossAccount reports whether the bus is in another account than scope,
// the rule's: a target that needs a RoleArn and takes no input form
// (eventbridge-buses.md 6.3).
func (b busTarget) crossAccount(scope aws.Scope) bool { return b.bus.Account != scope.Account.String() }

func (f functionTarget) receiver(body []byte, _ *Target, _ envelope.Event) (delivery.Target, error) {
	return delivery.LambdaFunction{ARN: f.function, Event: body, Invocation: delivery.InvocationEvent}, nil
}

func (q queueTarget) receiver(body []byte, t *Target, ev envelope.Event) (delivery.Target, error) {
	out := delivery.SQSQueue{ARN: q.queue, Body: string(body)}
	if t.SQS != nil && t.SQS.MessageGroupID != nil {
		group, err := messageGroup(*t.SQS.MessageGroupID, ev)
		if err != nil {
			return nil, err
		}
		out.GroupID = group
	}
	return out, nil
}

func (t topicTarget) receiver(body []byte, _ *Target, _ envelope.Event) (delivery.Target, error) {
	return delivery.SNSTopic{ARN: t.topic, Message: string(body)}, nil
}

// messageGroup is the message group of an SQS target whose
// SqsParameters.MessageGroupId is id, for event ev. An id that is a JSON
// path is a dynamic path parameter, read from ev as it was matched, not
// from the target's shaped input (eb-targets.html#dynamic-path-parameters);
// any other id is the group as sent.
//
// AWS's behavior when the path selects no value, null, an object or an
// array is unrecorded (doc.go), so understudy delivers nothing and the
// error says why, rather than guess a group.
func messageGroup(id string, ev envelope.Event) (string, error) {
	if p, err := envelope.ParsePath(id); err == nil {
		return dynamicGroup(id, p, ev)
	}
	return id, nil
}

// dynamicGroup is the message group path p, parsed from id, selects in ev.
func dynamicGroup(id string, p envelope.Path, ev envelope.Event) (string, error) {
	v, ok := p.Select(ev.Document())
	if !ok {
		return "", errors.New("SqsParameters.MessageGroupId " + id + " selects nothing in the event")
	}
	switch v := v.(type) {
	case document.String:
		return string(v), nil
	case document.Number:
		return string(v), nil
	case document.Bool:
		return strconv.FormatBool(bool(v)), nil
	case document.Null, document.List, document.Map:
	}
	return "", errors.New("SqsParameters.MessageGroupId " + id + " selects null, an object or an array in the event, not a string, number or boolean")
}

// fifo reports whether the queue is a FIFO queue.
func (q queueTarget) fifo() bool { return strings.HasSuffix(q.queue.Resource, ".fifo") }

// targetOf parses the ARN of a target of a rule in scope: a Lambda function
// (with or without a qualifier), an SQS queue or an SNS topic in the rule's
// partition and region, or an event bus in the rule's partition and any
// region, in an account understudy serves. AWS delivers to the first three
// across accounts but not across regions (eb-targets.html), and to a bus
// across both (eventbridge-buses.md 6.3, 6.4). A bus need not exist yet: a
// delivery to a missing bus is dropped as PutEvents drops one
// (eventbridge-buses.md 10.4.14).
func targetOf(scope aws.Scope, s string) (deliveryTarget, bool) {
	arn, err := aws.ParseARN(s)
	if err != nil {
		return nil, false
	}
	at, err := aws.ScopeOf(arn)
	if err != nil || at.Partition != scope.Partition {
		return nil, false
	}
	if arn.Service == "events" {
		name, isBus := strings.CutPrefix(arn.Resource, "event-bus/")
		if !isBus || name == "" || strings.Contains(name, "/") {
			return nil, false
		}
		return busTarget{bus: arn}, true
	}
	if at.Region != scope.Region {
		return nil, false
	}
	switch {
	case arn.Service == "lambda":
		return functionOf(arn)
	case arn.Service == "sqs" && !strings.ContainsAny(arn.Resource, ":/"):
		return queueTarget{queue: arn}, true
	case arn.Service == "sns" && !strings.ContainsAny(arn.Resource, ":/"):
		return topicTarget{topic: arn}, true
	}
	return nil, false
}

// functionOf reads a Lambda ARN as a function target: function:<name> with
// at most one qualifier, a version or alias, each part non-empty, as
// Lambda's FunctionName pattern requires. Any other form names no function,
// so every delivery to it would fail.
func functionOf(arn aws.ARN) (deliveryTarget, bool) {
	rest, ok := strings.CutPrefix(arn.Resource, "function:")
	if !ok {
		return nil, false
	}
	name, qualifier, qualified := strings.Cut(rest, ":")
	if name == "" || qualified && (qualifier == "" || strings.Contains(qualifier, ":")) {
		return nil, false
	}
	return functionTarget{function: arn}, true
}

// deadLetterQueue parses the DeadLetterConfig Arn of a target of a rule in
// scope: a standard SQS queue in the rule's region, in a served account.
// AWS takes no FIFO queue and no queue in another region as an EventBridge
// dead-letter queue (eb-rule-dlq.html).
func deadLetterQueue(scope aws.Scope, s string) (aws.ARN, bool) {
	t, ok := targetOf(scope, s)
	if !ok {
		return aws.ARN{}, false
	}
	q, ok := t.(queueTarget)
	if !ok || q.fifo() {
		return aws.ARN{}, false
	}
	return q.queue, true
}

// The retry policy of a target without RetryPolicy, and the bounds of one
// that sets only one member: 24 hours and 185 retries.
const (
	defaultEventAge = 24 * time.Hour
	defaultRetries  = 185
)

// retryPolicy is the outbox policy of a target's RetryPolicy.
func retryPolicy(p *RetryPolicy) delivery.EventBridgeTarget {
	out := delivery.EventBridgeTarget{MaxAge: defaultEventAge, MaxAttempts: defaultRetries}
	if p == nil {
		return out
	}
	if p.MaximumEventAgeInSeconds != nil {
		out.MaxAge = time.Duration(*p.MaximumEventAgeInSeconds) * time.Second
	}
	if p.MaximumRetryAttempts != nil {
		out.MaxAttempts = int(*p.MaximumRetryAttempts)
	}
	return out
}

// fanOut collects, while the state lock is held, what a scheduled fire or a
// PutEvents call sends: the deliveries to make, the matched targets that
// receive nothing, the stored targets understudy cannot deliver to and the
// stored dead-letter queues it cannot use.
type fanOut struct {
	// newID draws an event id, for an event forwarded to another region's
	// bus (eventbridge-buses.md 6.4).
	newID func() string
	// forwarded is set when the events arrived through an event bus target:
	// they make their one hop, so no bus target receives them
	// (eventbridge-buses.md 6.5, 10.4.13).
	forwarded bool
	requests  []delivery.Request
	skipped   []skip
	strays    []stray
	strayDLQs []strayDLQ
}

// newFanOut is the fan-out of a scheduled fire or a PutEvents call, whose
// events arrived through an event bus target when forwarded is set.
func (s *Service) newFanOut(forwarded bool) fanOut {
	return fanOut{newID: func() string { return s.ids.UUID(ids.KindEventID).String() }, forwarded: forwarded}
}

// skip is a matched target that receives nothing, which the journal
// records as a dropped delivery.
type skip struct {
	rule   aws.ARN
	target deliveryTarget
	reason string
}

// stray is a stored target whose ARN is not a delivery target. PutTargets
// refuses such targets, so one comes from an edited snapshot. The journal
// cannot record it, since a delivery needs a mechanism, so it is logged.
type stray struct {
	rule   aws.ARN
	target string
}

// strayDLQ is a stored DeadLetterConfig Arn that is not a standard SQS
// queue in the rule's region. PutTargets refuses such an Arn, so one comes
// from an edited snapshot. Its target is delivered to with no dead-letter
// queue, and it is logged.
type strayDLQ struct {
	rule   aws.ARN
	target aws.ARN
	dlq    string
}

// resolve parses the ARN of target t of rule r, recording a stray when it
// is not a delivery target.
func (f *fanOut) resolve(r ruleRef, t *Target) (deliveryTarget, bool) {
	dt, ok := targetOf(r.scope, t.ARN)
	if !ok {
		f.strays = append(f.strays, stray{rule: r.arn(), target: t.ARN})
	}
	return dt, ok
}

// send adds the delivery of ev to target t of rule r, parsed as dt: its
// payload shaped by the target's input, or for an event bus the event
// itself, or a skip when AWS would deliver nothing, ev cannot fill the
// target's parameters or ev already made its one hop.
func (f *fanOut) send(r ruleRef, t *Target, dt deliveryTarget, ev envelope.Event, ingested clock.Instant) {
	var receiver delivery.Target
	var why string
	switch dt := dt.(type) {
	case busTarget:
		receiver, why = f.forward(dt, t, ev)
	case payloadTarget:
		receiver, why = shape(r, t, dt, ev, ingested)
	default:
		why = "understudy has no delivery for a " + dt.mechanism().String() + " target; this is a bug in understudy, please report it"
	}
	if receiver == nil {
		f.skipped = append(f.skipped, skip{rule: r.arn(), target: dt, reason: why})
		return
	}
	req := delivery.Request{Source: r.arn(), Mechanism: dt.mechanism(), Policy: retryPolicy(t.Retry), Target: receiver}
	if t.DeadLetter != nil && t.DeadLetter.ARN != nil {
		if dlq, ok := deadLetterQueue(r.scope, *t.DeadLetter.ARN); ok {
			req.DLQ = &dlq
		} else {
			f.strayDLQs = append(f.strayDLQs, strayDLQ{rule: r.arn(), target: dt.arn(), dlq: *t.DeadLetter.ARN})
		}
	}
	f.requests = append(f.requests, req)
}

// shape is the delivery of ev to target t of rule r, parsed as dt: the
// payload its input shapes. A nil delivery comes with why the target
// receives nothing.
func shape(r ruleRef, t *Target, dt payloadTarget, ev envelope.Event, ingested clock.Instant) (receiver delivery.Target, why string) {
	body, why, ok := payload(t.Input, ev, envelope.Context{RuleARN: r.arn().String(), RuleName: r.name, Ingested: ingested.Wire()})
	if !ok {
		return nil, why
	}
	receiver, err := dt.receiver(body, t, ev)
	if err != nil {
		return nil, err.Error()
	}
	return receiver, ""
}

// forward is the delivery of ev to the event bus target t, parsed as dt:
// the event with its envelope, which the receiving bus keeps
// (eventbridge-buses.md 10.4.12). The forwarded event keeps its source,
// detail-type, detail, account and region (recorded, eventbridge-buses.md
// 6.2), and its time and resources (inferred, EBB-08). Its id is new when
// the bus is in another region (eventbridge-buses.md 6.4), and kept in the
// same region, for another account (6.3) and for the rule's own
// (events.bus-target.same-account-event-id).
//
// A nil delivery comes with why the bus receives nothing: the event
// already made its one hop (msgSecondHop), or the stored target has an
// input form, which PutTargets refuses on a bus target, so one comes only
// from an edited snapshot.
func (f *fanOut) forward(dt busTarget, t *Target, ev envelope.Event) (receiver delivery.Target, why string) {
	if f.forwarded {
		return nil, msgSecondHop
	}
	if _, matched := t.Input.(MatchedEvent); !matched {
		return nil, msgStoredBusTargetInput
	}
	detail, err := ev.DetailJSON()
	if err != nil {
		return nil, unwritable(err)
	}
	id := ev.ID
	if dt.bus.Region != ev.Region {
		id = f.newID()
	}
	return delivery.EventBus{ARN: dt.bus, Entries: []delivery.EventEntry{{
		Source: ev.Source, DetailType: ev.DetailType, Detail: string(detail), Resources: slices.Clone(ev.Resources),
		Forwarded: &delivery.Forwarded{ID: id, Account: ev.Account, Region: ev.Region, Time: ev.Time},
	}}}, ""
}

// payload is what a target receives for ev. ok is false, with why, when
// AWS delivers nothing or understudy cannot write the payload.
func payload(in Input, ev envelope.Event, ctx envelope.Context) (body []byte, why string, ok bool) {
	switch in := in.(type) {
	case MatchedEvent:
		body, err := ev.JSON()
		if err != nil {
			return nil, unwritable(err), false
		}
		return body, "", true
	case ConstantInput:
		return []byte(in.JSON), "", true
	case PathInput:
		p, err := envelope.ParsePath(in.Path)
		if err != nil {
			return nil, err.Error(), false
		}
		body, selected, err := p.Payload(ev)
		switch {
		case err != nil:
			return nil, unwritable(err), false
		case !selected:
			return nil, "InputPath " + in.Path + " selects nothing in the event (" + unmatchedInputPath().Name + ")", false
		}
		return body, "", true
	case TransformedInput:
		tr, err := envelope.NewTransformer(in.Variables, in.Template)
		if err != nil {
			return nil, err.Error(), false
		}
		body, delivered, err := tr.Render(ev, ctx)
		switch {
		case err != nil:
			return nil, unwritable(err), false
		case !delivered:
			return nil, "the InputTransformer output is not JSON, or places an object or array inside a string of a JSON template", false
		}
		return body, "", true
	}
	return nil, "the target has no input form", false
}

// unwritable is the drop reason for a payload understudy could not write
// as JSON: a bug, since every event value comes from parsed JSON.
func unwritable(err error) string {
	return "understudy could not write the event as JSON (" + err.Error() + "); this is a bug in understudy, please report it"
}

// matchEvent sends ev to the targets of every enabled rule of bus in st
// whose pattern matches it, in target creation order.
func (s *Service) matchEvent(f *fanOut, scope aws.Scope, st *State, bus string, ev envelope.Event, ingested clock.Instant) {
	// hit is a target of a rule whose pattern matched ev, or, with
	// failure set, of a rule whose pattern could not be evaluated against
	// it.
	type hit struct {
		rule    ruleRef
		target  *Target
		failure error
	}
	var hits []hit
	doc, docErr := ev.JSON()
	if docErr != nil {
		docErr = errors.New(unwritable(docErr))
	}
	rules := st.Rules[bus]
	for name := range rules {
		rule := rules[name]
		pattern, ok := rule.Trigger.pattern()
		if !ok || !rule.enabled() || len(rule.Targets) == 0 {
			continue
		}
		matched, err := false, docErr
		if docErr == nil {
			matched, err = s.matches(pattern, doc)
		}
		if !matched && err == nil {
			continue
		}
		for i := range rule.Targets {
			hits = append(hits, hit{rule: ruleRef{scope: scope, bus: bus, name: name}, target: &rule.Targets[i], failure: err})
		}
	}
	slices.SortFunc(hits, func(a, b hit) int { return cmp.Compare(a.target.Seq, b.target.Seq) })
	for _, h := range hits {
		dt, ok := f.resolve(h.rule, h.target)
		switch {
		case !ok:
			// resolve recorded the stray target.
		case h.failure != nil:
			f.fail(h.rule, dt, h.failure)
		default:
			f.send(h.rule, h.target, dt, ev, ingested)
		}
	}
}

// fail adds a skip for target dt of rule r, whose pattern could not be
// evaluated against an event.
func (f *fanOut) fail(r ruleRef, dt deliveryTarget, err error) {
	f.skipped = append(f.skipped, skip{
		rule:   r.arn(),
		target: dt,
		reason: "event pattern evaluation failed: " + err.Error() + "; the event was not delivered to this rule's targets",
	})
}

// matches reports whether event, an envelope's JSON, matches a stored
// pattern. An error is a pattern this build does not compile (from a
// snapshot) or an event past the matcher's work bound
// (*ruler.SearchBudgetError): the event neither matched nor failed to
// match.
func (s *Service) matches(pattern string, event []byte) (bool, error) {
	p, err := s.patterns.compile(pattern)
	if err != nil {
		return false, err
	}
	return ruler.Match(p, event)
}

// report journals each skipped target as a dropped delivery caused by c,
// at c's admission, and logs each stray target and dead-letter queue.
func (s *Service) report(ctx context.Context, c *kernel.Call, scope aws.Scope, f *fanOut) {
	for i := range f.skipped {
		sk := &f.skipped[i]
		c.Record(ctx, scope, journal.Delivery{
			From: sk.rule, To: sk.target.arn(), Mechanism: sk.target.mechanism(),
			Outcome: journal.Dropped{Reason: sk.reason},
		})
	}
	for _, st := range f.strays {
		s.k.Log().LogAttrs(ctx, slog.LevelError,
			"an EventBridge rule's stored target is not a Lambda function, SQS queue or SNS topic in the rule's region and a served account; "+
				"nothing was delivered to it. Remove it with RemoveTargets and add it again with PutTargets",
			slog.String("rule", st.rule.String()), slog.String("target", st.target))
	}
	for i := range f.strayDLQs {
		sd := &f.strayDLQs[i]
		s.k.Log().LogAttrs(ctx, slog.LevelError,
			"an EventBridge target's stored DeadLetterConfig Arn is not a standard SQS queue in the rule's region and a served account; "+
				"the event was delivered without a dead-letter queue, so if delivery fails for good the event is dropped. "+
				"Call PutTargets again for this target with a valid DeadLetterConfig",
			slog.String("rule", sd.rule.String()), slog.String("target", sd.target.String()), slog.String("dead_letter_queue", sd.dlq))
	}
}
