package sns

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/ruler"
	"github.com/sustinbebustin/understudy/internal/service/sns/filterpolicy"
)

// Subscription attribute names (sns.md 3.3).
const (
	attrRawMessageDelivery           = "RawMessageDelivery"
	attrRedrivePolicy                = "RedrivePolicy"
	attrFilterPolicy                 = "FilterPolicy"
	attrFilterPolicyScope            = "FilterPolicyScope"
	attrSubscriptionRoleArn          = "SubscriptionRoleArn"
	attrReplayPolicy                 = "ReplayPolicy"
	attrConfirmationWasAuthenticated = "ConfirmationWasAuthenticated"
	attrEndpoint                     = "Endpoint"
	attrPendingConfirmation          = "PendingConfirmation"
	attrProtocol                     = "Protocol"
	attrSubscriptionArn              = "SubscriptionArn"
	attrSubscriptionPrincipal        = "SubscriptionPrincipal"
)

// bindSubscriptionAttributes binds Get and SetSubscriptionAttributes.
func (s *Service) bindSubscriptionAttributes(b snsapi.Binder) {
	b.GetSubscriptionAttributes(s.getSubscriptionAttributes, registry.Full{},
		arnResource(func(in *snsapi.GetSubscriptionAttributesInput) string { return in.SubscriptionArn }))
	b.SetSubscriptionAttributes(s.setSubscriptionAttributes, registry.Partial{Note: "DeliveryPolicy is stored and echoed but not applied: http and https endpoints receive nothing in phase 1; the quota of 10,000 filter policies per account is not enforced"},
		arnResource(func(in *snsapi.SetSubscriptionAttributesInput) string { return in.SubscriptionArn }))
}

// subscriber is what a subscription attribute is checked against.
type subscriber struct {
	protocol  Protocol
	endpoint  string
	topicFIFO bool
	// home is the topic's scope, the subscription's account and Region,
	// where a RedrivePolicy queue must live (sns.md 7.4).
	home aws.Scope
	// scope and policy are the FilterPolicyScope and FilterPolicy in
	// effect: a new policy compiles for scope, and a new scope must keep
	// policy valid.
	scope  filterpolicy.Scope
	policy string
}

// subscriptionSetting is one checked subscription attribute.
type subscriptionSetting struct {
	name string
	// shown is the value the setting gives the attribute; "" for an
	// attribute it unsets.
	shown string
	apply func(sub *Subscription)
}

// storedSubscriptionAttribute is a setting kept in Attributes as shown;
// the empty string unsets it.
func storedSubscriptionAttribute(name, value string) subscriptionSetting {
	return subscriptionSetting{name: name, shown: value, apply: func(sub *Subscription) {
		if value == "" {
			delete(sub.Attributes, name)
			return
		}
		if sub.Attributes == nil {
			sub.Attributes = map[string]string{}
		}
		sub.Attributes[name] = value
	}}
}

// subscriptionAttribute checks a settable attribute of a subscription; nil
// value is an absent AttributeValue, which Terraform sends to clear a
// RedrivePolicy (tf-messaging.md 3.5). detail is the text after "Invalid
// parameter: " (after "Attributes Reason: " at Subscribe), recorded where
// sns.md has it and inferred otherwise; it is "" when the setting is good.
// FilterPolicy is stored as given once it compiles for the scope in
// effect; a DeliveryPolicy that parses as a JSON object and keeps
// checkSubscriptionDeliveryPolicy's constraints is stored as
// storedSubscriptionDeliveryPolicy shapes it.
func (s *Service) subscriptionAttribute(sub subscriber, name string, value *string) (set subscriptionSetting, detail string) {
	v := ""
	if value != nil {
		v = *value
	}
	switch name {
	case attrRawMessageDelivery:
		return rawMessageDelivery(sub.protocol, v)
	case attrRedrivePolicy:
		if value == nil {
			return storedSubscriptionAttribute(name, ""), ""
		}
		if detail := checkRedrivePolicy(sub, v); detail != "" {
			return subscriptionSetting{}, detail
		}
	case attrFilterPolicy:
		if v == "" || v == "{}" {
			return storedSubscriptionAttribute(name, ""), ""
		}
		if _, err := s.policies.Compile(v, sub.scope); err != nil {
			return subscriptionSetting{}, err.Reason
		}
	case attrFilterPolicyScope:
		if detail := s.checkFilterPolicyScope(sub, v); detail != "" {
			return subscriptionSetting{}, detail
		}
	case attrDeliveryPolicy:
		return subscriptionDeliveryPolicy(v)
	case attrReplayPolicy:
		// Message replay exists only for FIFO topics (sns.md 3.3); the
		// text is inferred, in the form of a standard topic's
		// ArchivePolicy.
		if !sub.topicFIFO {
			return subscriptionSetting{}, name
		}
	case attrSubscriptionRoleArn:
	default:
		return subscriptionSetting{}, "AttributeName"
	}
	return storedSubscriptionAttribute(name, v), ""
}

// subscriptionDeliveryPolicy checks a subscription DeliveryPolicy: "" unsets
// it, and anything else must be a JSON object that keeps
// checkSubscriptionDeliveryPolicy's constraints.
func subscriptionDeliveryPolicy(v string) (set subscriptionSetting, detail string) {
	if v == "" {
		return storedSubscriptionAttribute(attrDeliveryPolicy, ""), ""
	}
	m, ok := parseObject(v)
	if !ok {
		return subscriptionSetting{}, attrDeliveryPolicy + ": failed to parse JSON."
	}
	if detail := checkSubscriptionDeliveryPolicy(m); detail != "" {
		return subscriptionSetting{}, detail
	}
	stored, ok := storedSubscriptionDeliveryPolicy(m)
	if !ok {
		return subscriptionSetting{}, attrDeliveryPolicy + ": failed to parse JSON."
	}
	return storedSubscriptionAttribute(attrDeliveryPolicy, stored), ""
}

// rawMessageDelivery checks a RawMessageDelivery for a subscription of
// protocol: true or false in any case, shown lower case (sns.md 3.3), and
// true only where the protocol takes raw delivery.
func rawMessageDelivery(protocol Protocol, v string) (set subscriptionSetting, detail string) {
	raw := strings.ToLower(v)
	if raw != "true" && raw != "false" {
		return subscriptionSetting{}, attrRawMessageDelivery + ": Invalid value [" + v + "]. Must be true or false."
	}
	on := raw == "true"
	if on && !protocol.rawDelivery() {
		return subscriptionSetting{}, "Delivery protocol [" + protocol.String() + "] does not support raw message delivery."
	}
	return subscriptionSetting{name: attrRawMessageDelivery, shown: raw, apply: func(sub *Subscription) { sub.RawMessageDelivery = on }}, ""
}

// checkFilterPolicyScope checks a FilterPolicyScope (sns.md 3.3, 6.5): one
// of the two scope names, for which the policy in effect compiles. A scope
// the policy does not compile for is refused with the policy's reason, as
// the recorded nested-policy text under MessageAttributes is.
func (s *Service) checkFilterPolicyScope(sub subscriber, v string) string {
	scope, ok := filterpolicy.ParseScope(v)
	if !ok {
		return attrFilterPolicyScope + ": Invalid value [" + v + "]. Please use either MessageBody or MessageAttributes"
	}
	if sub.policy == "" {
		return ""
	}
	if _, err := s.policies.Compile(sub.policy, scope); err != nil {
		return err.Reason
	}
	return ""
}

// checkRedrivePolicy checks a RedrivePolicy (sns.md 3.3, 7.4): a JSON
// object, malformed JSON answered with Jackson's recorded syntax message
// without its location line, whose deadLetterTargetArn is an SQS queue in the subscription's
// account and Region, a FIFO queue for a FIFO queue subscribed to a FIFO
// topic. A queue ARN without the aws partition, a Region or a 12-digit
// account is the recorded invalid-arn text; the text for a queue in
// another account or Region is inferred, as no recording shows AWS's.
func checkRedrivePolicy(sub subscriber, v string) string {
	m, ok := parseObject(v)
	if !ok {
		if message, ok := ruler.SyntaxError([]byte(v)); ok {
			return attrRedrivePolicy + ": failed to parse JSON. " + message
		}
		return attrRedrivePolicy + ": failed to parse JSON."
	}
	dlq, scope, ok := deadLetterTarget(m)
	if !ok {
		return attrRedrivePolicy + ": deadLetterTargetArn is an invalid arn"
	}
	if scope != sub.home {
		return attrRedrivePolicy + ": deadLetterTargetArn must be an Amazon SQS queue in the account and Region of the subscription (" +
			sub.home.Account.String() + ", " + sub.home.Region.String() + ")"
	}
	if sub.topicFIFO && isFIFOQueue(sub.endpoint) && !isFIFOQueue(dlq.Resource) {
		return attrRedrivePolicy + ": must use a FIFO queue as DLQ for a FIFO Subscription to a FIFO Topic."
	}
	return ""
}

// deadLetterTarget parses the deadLetterTargetArn of a RedrivePolicy: an
// SQS queue ARN naming a scope understudy serves, as the outbox requires of
// a DLQ.
func deadLetterTarget(policy document.Map) (aws.ARN, aws.Scope, bool) {
	dlq, err := aws.ParseARN(stringMember(policy, "deadLetterTargetArn"))
	if err != nil || dlq.Service != "sqs" {
		return aws.ARN{}, aws.Scope{}, false
	}
	scope, err := aws.ScopeOf(dlq)
	if err != nil {
		return aws.ARN{}, aws.Scope{}, false
	}
	return dlq, scope, true
}

// isFIFOQueue reports an SQS queue ARN or URL naming a FIFO queue.
func isFIFOQueue(queue string) bool { return strings.HasSuffix(queue, fifoSuffix) }

// subscriptionSettings checks Subscribe's Attributes in name order; a
// FilterPolicy compiles for the FilterPolicyScope beside it, when that
// scope is valid, else for the default.
func (s *Service) subscriptionSettings(sub subscriber, attrs map[string]string) ([]subscriptionSetting, *awserr.Error) {
	if scope, ok := filterpolicy.ParseScope(attrs[attrFilterPolicyScope]); ok {
		sub.scope = scope
	}
	out := make([]subscriptionSetting, 0, len(attrs))
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		v := attrs[name]
		set, detail := s.subscriptionAttribute(sub, name, &v)
		if detail != "" {
			return nil, errReason("Attributes", detail)
		}
		out = append(out, set)
	}
	return out, nil
}

// renderSubscriptionAttributes is GetSubscriptionAttributes of sub
// (sns.md 3.3, tf-messaging.md 3.5): the fixed members, then every
// attribute set, with FilterPolicyScope shown only beside a FilterPolicy,
// and an http or https subscription's EffectiveDeliveryPolicy under
// topicPolicy, its topic's stored DeliveryPolicy ("" for none).
func renderSubscriptionAttributes(ref subscriptionRef, sub *Subscription, topicPolicy string) map[string]string {
	pending, authenticated := "false", "false"
	switch st := sub.Status.(type) {
	case Pending:
		pending = "true"
	case Confirmed:
		authenticated = strconv.FormatBool(st.authenticated())
	}
	out := map[string]string{
		attrConfirmationWasAuthenticated: authenticated,
		attrEndpoint:                     sub.Endpoint,
		attrOwner:                        sub.OwnerAccountID,
		attrPendingConfirmation:          pending,
		attrProtocol:                     sub.Protocol.String(),
		attrRawMessageDelivery:           strconv.FormatBool(sub.RawMessageDelivery),
		attrSubscriptionArn:              ref.arn().String(),
		attrTopicArn:                     ref.topic.arn().String(),
	}
	if sub.Principal != "" {
		out[attrSubscriptionPrincipal] = sub.Principal
	}
	for k, v := range sub.Attributes {
		if k != attrFilterPolicyScope {
			out[k] = v
		}
	}
	if _, ok := sub.Attributes[attrFilterPolicy]; ok {
		out[attrFilterPolicyScope] = sub.filterPolicyScope()
	}
	if sub.Protocol == ProtocolHTTP || sub.Protocol == ProtocolHTTPS {
		out[attrEffectiveDeliveryPolicy] = subscriptionEffectiveDeliveryPolicy(sub.Attributes[attrDeliveryPolicy], topicPolicy)
	}
	return out
}

// filterPolicyScope is the subscription's FilterPolicyScope,
// MessageAttributes unless set.
func (sub *Subscription) filterPolicyScope() string {
	if scope, ok := sub.Attributes[attrFilterPolicyScope]; ok {
		return scope
	}
	return filterpolicy.MessageAttributes.String()
}

// policyScope is the stored FilterPolicyScope, parsed. Only a corrupt
// snapshot can store one SetSubscriptionAttributes refuses, which is
// Internal.
func (sub *Subscription) policyScope() (filterpolicy.Scope, *awserr.Error) {
	scope, ok := filterpolicy.ParseScope(sub.filterPolicyScope())
	if !ok {
		return scope, awserr.Internalf(fmt.Errorf("subscription %s stores FilterPolicyScope %q, which SetSubscriptionAttributes refuses; "+
			"the snapshot it was loaded from is corrupt", sub.ID, sub.filterPolicyScope()))
	}
	return scope, nil
}

// filter is the subscription's FilterPolicy compiled for its scope through
// policies, nil when it has none. A stored policy that does not compile,
// which only a corrupt snapshot can hold, is Internal.
func (sub *Subscription) filter(policies *filterpolicy.Cache) (*filterpolicy.Policy, *awserr.Error) {
	text, ok := sub.Attributes[attrFilterPolicy]
	if !ok {
		return nil, nil
	}
	scope, err := sub.policyScope()
	if err != nil {
		return nil, err
	}
	p, cerr := policies.Compile(text, scope)
	if cerr != nil {
		return nil, awserr.Internalf(fmt.Errorf("subscription %s stores a FilterPolicy that does not compile for scope %s (%s); "+
			"the snapshot it was loaded from is corrupt", sub.ID, scope, cerr.Reason))
	}
	return p, nil
}

// deadLetterQueue is the deadLetterTargetArn of the subscription's
// RedrivePolicy, nil when it has none. A stored policy checkRedrivePolicy
// would refuse, which only a corrupt snapshot can hold, is Internal.
func (sub *Subscription) deadLetterQueue() (*aws.ARN, *awserr.Error) {
	v, ok := sub.Attributes[attrRedrivePolicy]
	if !ok {
		return nil, nil
	}
	m, ok := parseObject(v)
	if !ok {
		return nil, awserr.Internalf(fmt.Errorf("subscription %s stores a RedrivePolicy that is not a JSON object; the snapshot it was loaded from is corrupt", sub.ID))
	}
	dlq, _, ok := deadLetterTarget(m)
	if !ok {
		return nil, awserr.Internalf(fmt.Errorf("subscription %s stores RedrivePolicy %s, whose deadLetterTargetArn is not an SQS queue in a scope understudy serves; "+
			"the snapshot it was loaded from is corrupt", sub.ID, v))
	}
	return &dlq, nil
}

// setting returns the value of a settable attribute of sub, or its default
// when unset: RawMessageDelivery "false" (sns.md 3.3), FilterPolicyScope
// "MessageAttributes", and "" for the others.
func (sub *Subscription) setting(name string) string {
	switch name {
	case attrRawMessageDelivery:
		return strconv.FormatBool(sub.RawMessageDelivery)
	case attrFilterPolicyScope:
		return sub.filterPolicyScope()
	}
	return sub.Attributes[name]
}

// getSubscriptionAttributes returns every attribute of a subscription, of
// a live topic or detached from a deleted one (LocalStack
// test_delete_subscriptions_on_delete_topic, AWS-recorded).
func (s *Service) getSubscriptionAttributes(_ context.Context, c *kernel.Call, in *snsapi.GetSubscriptionAttributesInput) (*snsapi.GetSubscriptionAttributesOutput, *awserr.Error) {
	ref, err := resolveSubscription(c, in.SubscriptionArn)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	attrs, _ := read(s, ref.topic.scope, func(st *State) map[string]string {
		if t, ok := st.Topics[ref.topic.name]; ok {
			if i := t.indexOf(ref.id); i >= 0 && !t.Subscriptions[i].expired(now) {
				return renderSubscriptionAttributes(ref, &t.Subscriptions[i], t.Attributes[attrDeliveryPolicy])
			}
		}
		if i := st.detachedIndex(ref, now); i >= 0 {
			return renderSubscriptionAttributes(ref, &st.Detached[i].Subscription, "")
		}
		return nil
	})
	if attrs == nil {
		return nil, errSubscriptionNotFound()
	}
	return &snsapi.GetSubscriptionAttributesOutput{Attributes: attrs}, nil
}

// setSubscriptionAttributes sets one attribute of a subscription.
func (s *Service) setSubscriptionAttributes(_ context.Context, c *kernel.Call, in *snsapi.SetSubscriptionAttributesInput) (*snsapi.SetSubscriptionAttributesOutput, *awserr.Error) {
	ref, err := resolveSubscription(c, in.SubscriptionArn)
	if err != nil {
		return nil, err
	}
	_, err = writeSubscription(s, c, ref, func(t *Topic, sub *Subscription) *awserr.Error {
		scope, err := sub.policyScope()
		if err != nil {
			return err
		}
		current := subscriber{
			protocol: sub.Protocol, endpoint: sub.Endpoint, topicFIFO: ref.topic.fifo(), home: ref.topic.scope,
			scope: scope, policy: sub.Attributes[attrFilterPolicy],
		}
		set, detail := s.subscriptionAttribute(current, in.AttributeName, in.AttributeValue)
		if detail != "" {
			return errInvalidParameter(detail)
		}
		if set.name == attrFilterPolicy && set.shown != "" {
			if err := t.checkFilterPolicyQuota(sub.ID); err != nil {
				return err
			}
		}
		set.apply(sub)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &snsapi.SetSubscriptionAttributesOutput{}, nil
}

// maxFilterPoliciesPerTopic is the quota of subscriptions with a
// FilterPolicy on one topic (sns.md 6.5).
const maxFilterPoliciesPerTopic = 200

// checkFilterPolicyQuota is FilterPolicyLimitExceeded when the
// subscriptions of t other than id already hold maxFilterPoliciesPerTopic
// policies, so a FilterPolicy on id would pass the quota. Replacing id's
// own policy leaves the count unchanged.
func (t *Topic) checkFilterPolicyQuota(id string) *awserr.Error {
	n := 0
	for i := range t.Subscriptions {
		if _, ok := t.Subscriptions[i].Attributes[attrFilterPolicy]; ok && t.Subscriptions[i].ID != id {
			n++
		}
	}
	if n >= maxFilterPoliciesPerTopic {
		return errFilterPolicyLimit()
	}
	return nil
}

// admitFilterPolicy checks the FilterPolicy of sub, a subscription about
// to join t, against the per-topic quota. A policy past the quota is
// removed from sub and its error returned; sub joins t without it.
func (t *Topic) admitFilterPolicy(sub *Subscription) *awserr.Error {
	if _, ok := sub.Attributes[attrFilterPolicy]; !ok {
		return nil
	}
	err := t.checkFilterPolicyQuota(sub.ID)
	if err != nil {
		delete(sub.Attributes, attrFilterPolicy)
	}
	return err
}

// errFilterPolicyLimit answers a filter policy past the per-topic quota.
// The code and status are AWS's (sns.md 6.5); the text is inferred, as no
// recording shows AWS's.
func errFilterPolicyLimit() *awserr.Error {
	return snsapi.ErrFilterPolicyLimitExceededException("Filter policy limit exceeded: the topic already has " + strconv.Itoa(maxFilterPoliciesPerTopic) +
		" subscriptions with a filter policy, the most it allows. Remove the FilterPolicy of another subscription to the topic, then retry.")
}

// writeSubscription runs fn on a copy of the topic and the subscription
// ref names and stores both when fn succeeds; a subscription that does not
// exist, or expired, is NotFound.
func writeSubscription(s *Service, c *kernel.Call, ref subscriptionRef, fn func(t *Topic, sub *Subscription) *awserr.Error) (struct{}, *awserr.Error) {
	return kernel.Write(c, s.cells, ref.topic.scope, func(st *State, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		t, ok := st.writable(ref.topic.name, now)
		if !ok {
			return struct{}{}, errSubscriptionNotFound()
		}
		i := t.indexOf(ref.id)
		if i < 0 {
			return struct{}{}, errSubscriptionNotFound()
		}
		if err := fn(&t, &t.Subscriptions[i]); err != nil {
			return struct{}{}, err
		}
		st.Topics[ref.topic.name] = t
		return struct{}{}, nil
	})
}
