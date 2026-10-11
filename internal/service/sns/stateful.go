package sns

import (
	"maps"
	"slices"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// Name returns "sns".
func (*Service) Name() ref.ServiceID { return "sns" }

// SchemaVersion returns the version of State.
func (*Service) SchemaVersion() int { return stateVersion }

// Export serializes every non-empty scope to one section.
func (s *Service) Export() ([]store.Section, error) {
	return s.cells.Export(store.ScopeKeys(), stateVersion)
}

// Import loads sections written by Export of this schema version, and
// forgets the signers parsed from the keys it replaces.
func (s *Service) Import(ss []store.Section) error {
	defer s.signers.clear()
	return s.cells.Import(store.ScopeKeys(), stateVersion, ss)
}

// Reset drops the scopes rs covers and the signers parsed from their keys.
func (s *Service) Reset(rs kernel.ResetScope) {
	defer s.signers.clear()
	s.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
}

// Rearm does nothing: SNS arms no timers. Deduplication windows and token
// expiry are instants the next call compares against.
func (*Service) Rearm(clock.Instant) {}

// Scopes lists the scopes the service holds a cell for, whether or not any
// topic lives there.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }

// Resources lists the topics of scope by name, each with its subscription
// count.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	now := s.clock.Now()
	return store.Read(cell, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Topics))
		for _, name := range slices.Sorted(maps.Keys(st.Topics)) {
			t := st.Topics[name]
			out = append(out, kernel.Resource{
				ARN:     topicRef{scope: scope, name: name}.arn(),
				Summary: subscriptionCount(t.live(now)),
			})
		}
		return out
	})
}

// subscriptionCount is "1 subscription" or "<n> subscriptions".
func subscriptionCount(n int) string {
	if n == 1 {
		return "1 subscription"
	}
	return strconv.Itoa(n) + " subscriptions"
}

// live counts t's subscriptions that have not expired by now.
func (t *Topic) live(now clock.Instant) int {
	n := 0
	for i := range t.Subscriptions {
		if !t.Subscriptions[i].expired(now) {
			n++
		}
	}
	return n
}

// SubscriptionToken returns the confirmation token of a pending
// subscription, for the admin API (ADR 0015): tests confirm http, https
// and email subscriptions with it. A confirmed subscription has no token
// to read.
func (s *Service) SubscriptionToken(subscription aws.ARN) (string, *awserr.Error) {
	named, ok := s.subscriptionOf(subscription.String())
	if !ok {
		return "", errSubscriptionNotFound()
	}
	sub, ok := s.readSubscription(named)
	if !ok {
		return "", errSubscriptionNotFound()
	}
	pending, ok := sub.Status.(Pending)
	if !ok {
		return "", errReason("SubscriptionArn", "the subscription is confirmed, so it has no pending token")
	}
	return pending.Token, nil
}

// readSubscription returns a copy of the subscription ref names, without
// creating a cell; an expired pending subscription is gone.
func (s *Service) readSubscription(named subscriptionRef) (Subscription, bool) {
	type found struct {
		sub Subscription
		ok  bool
	}
	now := s.clock.Now()
	r, _ := read(s, named.topic.scope, func(st *State) found {
		sub, ok := st.subscription(named, now)
		return found{sub: sub, ok: ok}
	})
	return r.sub, r.ok
}
