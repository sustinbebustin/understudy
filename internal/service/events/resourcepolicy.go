package events

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the event bus target names with its stored policy
// parsed, for the delivery access advisory and IAM enforcement
// (outbox.PolicySource, ARCHITECTURE.md 16). ok is false when target is not
// an event bus ARN of a served scope or names a bus the scope does not
// hold. The default bus and a bus without a policy have none (nil, true);
// so does a bus whose policy policydoc refuses, since PutPermission stores
// any document whose statements are objects, as SQS does.
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
