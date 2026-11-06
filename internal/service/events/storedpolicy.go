package events

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// StoredPolicy returns an event bus's owner, the bus's account, and its
// policy as IAM evaluation reads it (authz.StoredPolicyReader): NoPolicy
// until PutPermission stores one, the default bus included, else the stored
// text, parsed or Unparsed, since PutPermission stores any document whose
// statements are objects. It is false for the targets ResourcePolicy refuses.
func (s *Service) StoredPolicy(target aws.ARN) (authz.StoredPolicy, bool) {
	name, isBus := strings.CutPrefix(target.Resource, "event-bus/")
	if target.Service != "events" || !isBus || name == "" || strings.Contains(name, "/") {
		return authz.StoredPolicy{}, false
	}
	scope, err := aws.ScopeOf(target)
	if err != nil {
		return authz.StoredPolicy{}, false
	}
	// The bus's times are not read, so any instant serves.
	bus, ok := s.lookupBus(scope, name, clock.Instant{})
	if !ok {
		return authz.StoredPolicy{}, false
	}
	text := ""
	if bus.Policy != nil {
		text = bus.Policy.text()
	}
	return authz.StoredPolicy{Owner: scope.Account, Policy: authz.ReadStored(text)}, true
}
