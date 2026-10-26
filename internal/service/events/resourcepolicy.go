package events

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the event bus target names with its stored policy
// parsed (outbox.PolicySource). It is wired for IAM enforcement of bus
// policies (ARCHITECTURE.md 21; eventbridge-buses.md 10.7.22), which no
// caller makes yet. The delivery access advisory never reads it: it skips
// events-to-events deliveries, the only ones to a bus (ADR 0015 amendment
// 2026-10-26; eventbridge-buses.md 6.2, 6.3). ok is false when target is
// not an event bus ARN of a served scope or names a bus the scope does not
// hold. A bus, the default bus included, has no policy (nil, true) until
// PutPermission stores one; so does a bus whose policy policydoc refuses,
// since PutPermission stores any document whose statements are objects, as
// SQS does.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	name, isBus := strings.CutPrefix(target.Resource, "event-bus/")
	if target.Service != "events" || !isBus || name == "" || strings.Contains(name, "/") {
		return nil, false
	}
	scope, err := aws.ScopeOf(target)
	if err != nil {
		return nil, false
	}
	// The bus's times are not read, so any instant serves.
	bus, ok := s.lookupBus(scope, name, clock.Instant{})
	if !ok {
		return nil, false
	}
	if bus.Policy == nil {
		return nil, true
	}
	return policydoc.ParseStored(bus.Policy.text()), true
}
