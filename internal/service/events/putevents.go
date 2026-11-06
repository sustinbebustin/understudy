package events

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/ruler"
	"github.com/sustinbebustin/understudy/internal/service/events/envelope"
)

// maxEntriesSize is the PutEvents request size limit: the entries' sizes
// must sum to less than 1 MiB (secrets-logs-events-alarms.md 4.7).
const maxEntriesSize = 1 << 20

// timeSize is what an entry's Time adds to its size.
const timeSize = 14

// bindEvents binds PutEvents and TestEventPattern.
func (s *Service) bindEvents(b eventbridgeapi.Binder) {
	b.PutEvents(s.putEvents, registry.Partial{Note: "default and custom buses in the caller's region, its own or another account's by ARN; EndpointId and TraceHeader are accepted and not used"})
	b.TestEventPattern(s.testEventPattern, registry.Full{})
}

// putEvents takes each entry onto its bus and sends each bus's events to
// the targets of that bus's rules that match them. An entry missing a
// member, with a Source in the reserved aws. namespace, with a malformed
// Detail or for another region's bus fails alone. An entry for a bus of
// another account in the caller's region reaches that account's rules,
// stamped with the sender's account, as on AWS; one for a bus that does not
// exist gets an EventId and is dropped, as on AWS.
//
// The internal call of an events-to-events attempt puts the one event an
// event bus target forwards, which keeps the id, account, region and time
// it was first put with (eventbridge-buses.md 10.4.12). It was accepted
// once, so only its members and Detail are checked again: a scheduled
// event's aws.events source is forwarded as it is.
func (s *Service) putEvents(ctx context.Context, c *kernel.Call, in *eventbridgeapi.PutEventsInput) (*eventbridgeapi.PutEventsOutput, *awserr.Error) {
	if entriesSize(in.Entries) >= maxEntriesSize {
		return nil, errEntriesTooLarge()
	}
	parse := parseEntry
	fwd, forwarded := forwardedCall(ctx, c)
	if forwarded {
		if len(in.Entries) != 1 {
			return nil, awserr.Internalf(&forwardedEntriesError{Entries: len(in.Entries)})
		}
		parse = parseForwardedEntry
	}
	scope := c.Scope()
	out := &eventbridgeapi.PutEventsOutput{Entries: make([]eventbridgeapi.PutEventsResultEntry, len(in.Entries))}
	var buses busEvents
	for i := range in.Entries {
		e := &in.Entries[i]
		bus := parseBus(scope, e.EventBusName)
		detail, failure := parse(e)
		if failure == nil {
			failure = foreignRegion(c, bus)
		}
		if failure != nil {
			out.Entries[i] = *failure
			out.FailedEntryCount++
			continue
		}
		ev := envelope.Event{
			DetailType: *e.DetailType,
			Source:     *e.Source,
			Resources:  slices.Clone(e.Resources),
			Detail:     detail,
		}
		if forwarded {
			ev.ID, ev.Account, ev.Region, ev.Time = fwd.ID, fwd.Account, fwd.Region, fwd.Time
		} else {
			ev.ID = s.ids.UUID(ids.KindEventID).String()
			ev.Account, ev.Region, ev.Time = scope.Account.String(), scope.Region.String(), eventTime(e, c.Admitted())
		}
		out.Entries[i] = eventbridgeapi.PutEventsResultEntry{EventId: new(ev.ID)}
		switch b := bus.(type) {
		case ownBus:
			buses.add(scope, b.name, ev)
		case foreignBus:
			buses.add(b.scope, b.name, ev)
		case missingBus:
			c.Touch(b.arn)
		}
	}
	for _, g := range buses {
		c.Touch(busARN(g.scope, g.bus))
		if err := s.publish(ctx, c, g.scope, g.bus, g.events, forwarded); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// forwardedCall returns the envelope of the event c forwards when c is the
// internal PutEvents call of an events-to-events attempt, which alone puts
// one on ctx. Any other call ignores ctx.
func forwardedCall(ctx context.Context, c *kernel.Call) (delivery.Forwarded, bool) {
	if o, ok := c.Origin().(ref.Internal); !ok || o.Via != ref.EventsTarget {
		return delivery.Forwarded{}, false
	}
	return delivery.ForwardedFrom(ctx)
}

// forwardedEntriesError is the cause of an events-to-events attempt's
// PutEvents call that carries other than one entry: the outbox forwards one
// event per call, so this is a bug in understudy, and nothing was sent.
type forwardedEntriesError struct {
	// Entries is how many entries the call carried.
	Entries int
}

func (e *forwardedEntriesError) Error() string {
	return "events: an events-to-events delivery's PutEvents call carried " + strconv.Itoa(e.Entries) +
		" entries, and the outbox forwards exactly one event per call; nothing was sent. This is a bug in understudy, please report it"
}

// foreignRegion is the failure of an entry for a bus of another region,
// which understudy does not send to, or nil. The call's journal entry
// still names that bus, as it names a missing one.
func foreignRegion(c *kernel.Call, bus busRef) *eventbridgeapi.PutEventsResultEntry {
	if b, ok := bus.(foreignBus); ok && b.scope.Region != c.Scope().Region {
		c.Touch(b.arn())
		return entryFailure(codeNotImplemented, msgForeignRegionBus(c.Scope(), b.arn()))
	}
	return nil
}

// busBatch is the accepted events of a PutEvents call on one bus of one
// scope.
type busBatch struct {
	scope  aws.Scope
	bus    string
	events []envelope.Event
}

// busEvents holds a call's batches in the order their buses first appear.
type busEvents []busBatch

func (b *busEvents) add(scope aws.Scope, bus string, ev envelope.Event) {
	for i := range *b {
		if (*b)[i].scope == scope && (*b)[i].bus == bus {
			(*b)[i].events = append((*b)[i].events, ev)
			return
		}
	}
	*b = append(*b, busBatch{scope: scope, bus: bus, events: []envelope.Event{ev}})
}

// entriesSize is a PutEvents request's size as AWS counts it: per entry,
// 14 bytes for a Time and the UTF-8 bytes of Source, DetailType, Detail
// and each Resources element.
func entriesSize(entries []eventbridgeapi.PutEventsRequestEntry) int {
	n := 0
	for i := range entries {
		e := &entries[i]
		if e.Time != nil {
			n += timeSize
		}
		for _, p := range []*string{e.Source, e.DetailType, e.Detail} {
			if p != nil {
				n += len(*p)
			}
		}
		for _, r := range e.Resources {
			n += len(r)
		}
	}
	return n
}

// maxDetailType is the most characters a DetailType holds
// (API_PutEventsRequestEntry).
const maxDetailType = 128

// parseEntry checks an entry's required members, its DetailType's length
// and its Source, and parses its Detail; a problem is the entry's failure.
func parseEntry(e *eventbridgeapi.PutEventsRequestEntry) (document.Map, *eventbridgeapi.PutEventsResultEntry) {
	if failure := requireMembers(e); failure != nil {
		return nil, failure
	}
	if utf8.RuneCountInString(*e.DetailType) > maxDetailType {
		return nil, entryFailure(codeInvalidArgument, msgTooLong("DetailType", maxDetailType))
	}
	if strings.HasPrefix(*e.Source, reservedSourcePrefix) {
		return nil, entryFailure(codeNotAuthorizedForSource, msgNotAuthorizedForSource)
	}
	return parseDetail(*e.Detail)
}

// parseForwardedEntry checks the required members of the entry an event
// bus target forwards and parses its Detail. The event was accepted when it
// was first put, so its DetailType and Source are not checked again.
func parseForwardedEntry(e *eventbridgeapi.PutEventsRequestEntry) (document.Map, *eventbridgeapi.PutEventsResultEntry) {
	if failure := requireMembers(e); failure != nil {
		return nil, failure
	}
	return parseDetail(*e.Detail)
}

// requireMembers is the failure of an entry without a Source, DetailType
// or Detail, or nil.
func requireMembers(e *eventbridgeapi.PutEventsRequestEntry) *eventbridgeapi.PutEventsResultEntry {
	for _, m := range []struct {
		name  string
		value *string
	}{{"Source", e.Source}, {"DetailType", e.DetailType}, {"Detail", e.Detail}} {
		if m.value == nil || *m.value == "" {
			return entryFailure(codeInvalidArgument, msgRequired(m.name))
		}
	}
	return nil
}

// parseDetail parses an entry's Detail, which must be a JSON object whose
// integers are in range.
func parseDetail(s string) (document.Map, *eventbridgeapi.PutEventsResultEntry) {
	detail, ok := envelope.ParseDetail(s)
	if !ok || !integersInRange(detail) {
		return nil, entryFailure(codeMalformedDetail, msgMalformedDetail)
	}
	return detail, nil
}

// integersInRange reports whether every integer in v, a number written
// without a fraction or an exponent, is a signed 64-bit integer, the range
// API_PutEvents documents for a JSON integer. A number with a fraction or an
// exponent is a float, which the range does not bound.
func integersInRange(v document.Value) bool {
	switch v := v.(type) {
	case document.Number:
		if strings.ContainsAny(string(v), ".eE") {
			return true
		}
		_, err := v.Int64()
		return err == nil
	case document.List:
		return !slices.ContainsFunc(v, func(e document.Value) bool { return !integersInRange(e) })
	case document.Map:
		return !slices.ContainsFunc(v, func(e document.Entry) bool { return !integersInRange(e.Value) })
	case document.Null, document.Bool, document.String:
	}
	return true
}

func entryFailure(code, message string) *eventbridgeapi.PutEventsResultEntry {
	return &eventbridgeapi.PutEventsResultEntry{ErrorCode: new(code), ErrorMessage: new(message)}
}

// eventTime is the entry's Time, or the instant PutEvents took it in.
func eventTime(e *eventbridgeapi.PutEventsRequestEntry, admitted clock.Instant) time.Time {
	if e.Time != nil {
		return *e.Time
	}
	return admitted.Wire()
}

// publish sends events on scope's bus to the targets of the bus's rules
// that match them, as Deliver effects of one write that changes nothing. A
// bus the scope does not hold has no rules, so its events reach no target,
// as AWS drops events to a missing bus (eventbridge-buses.md 10.3), and a
// delivery from an event bus target to a missing bus with them
// (eventbridge-buses.md 10.4.14). Events that arrived through an event bus
// target (forwarded) reach no bus target (fanOut.forwarded).
func (s *Service) publish(ctx context.Context, c *kernel.Call, scope aws.Scope, bus string, events []envelope.Event, forwarded bool) *awserr.Error {
	if _, ok := s.cells.Lookup(scope); !ok {
		return nil
	}
	var f fanOut
	_, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		tx.Unchanged()
		f = s.newFanOut(forwarded)
		for i := range events {
			s.matchEvent(&f, scope, st, bus, events[i], c.Admitted())
		}
		for i := range f.requests {
			tx.Deliver(f.requests[i])
		}
		return struct{}{}, nil
	})
	if err != nil {
		return err
	}
	s.report(ctx, c, scope, &f)
	return nil
}

// testEventPattern reports whether an event matches a pattern. The event
// must be a JSON object with the members of an event envelope.
func (s *Service) testEventPattern(_ context.Context, _ *kernel.Call, in *eventbridgeapi.TestEventPatternInput) (*eventbridgeapi.TestEventPatternOutput, *awserr.Error) {
	p, compileErr := ruler.Compile([]byte(in.EventPattern), s.profile)
	if compileErr != nil {
		return nil, errInvalidPattern(compileErr.Message())
	}
	if !isEnvelope(in.Event) {
		return nil, errInvalidParameter("Event")
	}
	// isEnvelope has read the event as a JSON object, so a Match error is
	// the matcher's work bound or a parser disagreement: understudy's fault.
	ok, err := ruler.Match(p, []byte(in.Event))
	if err != nil {
		return nil, eventbridgeapi.ErrInternalException("understudy could not evaluate the event against the pattern: " + err.Error())
	}
	return &eventbridgeapi.TestEventPatternOutput{Result: ok}, nil
}

// isEnvelope reports whether s is a JSON object with every member
// TestEventPattern requires: id, account, source, time, region and
// detail-type. API_TestEventPattern also lists resources, but AWS matches
// an event without it (localstack test_events_patterns.snapshot.json,
// test_event_pattern_source, recorded against AWS).
func isEnvelope(s string) bool {
	required := [...]string{"id", "account", "source", "time", "region", "detail-type"}
	v, ok := envelope.ParseJSON(s)
	if !ok {
		return false
	}
	m, ok := v.(document.Map)
	if !ok {
		return false
	}
	for _, name := range required {
		if !slices.ContainsFunc(m, func(e document.Entry) bool { return e.Key == name }) {
			return false
		}
	}
	return true
}
