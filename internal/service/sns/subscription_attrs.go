package sns

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
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

// Filter policy scopes.
const (
	scopeMessageAttributes = "MessageAttributes"
	scopeMessageBody       = "MessageBody"
)

// bindSubscriptionAttributes binds Get and SetSubscriptionAttributes.
func (s *Service) bindSubscriptionAttributes(b snsapi.Binder) {
	b.GetSubscriptionAttributes(s.getSubscriptionAttributes, registry.Full{},
		arnResource(func(in *snsapi.GetSubscriptionAttributesInput) string { return in.SubscriptionArn }))
	b.SetSubscriptionAttributes(s.setSubscriptionAttributes, registry.Partial{Note: "FilterPolicy and DeliveryPolicy are stored and echoed; publish does not filter yet; RedrivePolicy is stored; failed deliveries are not sent to the DLQ yet"},
		arnResource(func(in *snsapi.SetSubscriptionAttributesInput) string { return in.SubscriptionArn }))
}

// subscriber is what a subscription attribute is checked against.
type subscriber struct {
	protocol  Protocol
	endpoint  string
	topicFIFO bool
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
// FilterPolicy and DeliveryPolicy are stored as given once they parse as
// JSON objects.
func subscriptionAttribute(sub subscriber, name string, value *string) (set subscriptionSetting, detail string) {
	v := ""
	if value != nil {
		v = *value
	}
	switch name {
	case attrRawMessageDelivery:
		// Case-insensitive, shown lower case (sns.md 3.3).
		raw := strings.ToLower(v)
		if raw != "true" && raw != "false" {
			return subscriptionSetting{}, attrRawMessageDelivery + ": Invalid value [" + v + "]. Must be true or false."
		}
		on := raw == "true"
		return subscriptionSetting{name: name, shown: raw, apply: func(sub *Subscription) { sub.RawMessageDelivery = on }}, ""
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
		if _, ok := parseObject(v); !ok {
			return subscriptionSetting{}, attrFilterPolicy + ": failed to parse JSON."
		}
	case attrFilterPolicyScope:
		if v != scopeMessageAttributes && v != scopeMessageBody {
			return subscriptionSetting{}, attrFilterPolicyScope + ": Invalid value [" + v + "]. Please use either MessageBody or MessageAttributes"
		}
	case attrDeliveryPolicy:
		if _, ok := parseObject(v); v != "" && !ok {
			return subscriptionSetting{}, attrDeliveryPolicy + ": failed to parse JSON."
		}
	case attrSubscriptionRoleArn, attrReplayPolicy:
	default:
		return subscriptionSetting{}, "AttributeName"
	}
	return storedSubscriptionAttribute(name, v), ""
}

// checkRedrivePolicy checks a RedrivePolicy (sns.md 3.3): a JSON object
// whose deadLetterTargetArn is an SQS queue ARN, a FIFO queue for a FIFO
// queue subscribed to a FIFO topic.
func checkRedrivePolicy(sub subscriber, v string) string {
	m, ok := parseObject(v)
	if !ok {
		return attrRedrivePolicy + ": failed to parse JSON."
	}
	dlq, err := aws.ParseARN(stringMember(m, "deadLetterTargetArn"))
	if err != nil || dlq.Service != "sqs" {
		return attrRedrivePolicy + ": deadLetterTargetArn is an invalid arn"
	}
	if sub.topicFIFO && isFIFOQueue(sub.endpoint) && !isFIFOQueue(dlq.String()) {
		return attrRedrivePolicy + ": must use a FIFO queue as DLQ for a FIFO Subscription to a FIFO Topic."
	}
	return ""
}

// isFIFOQueue reports an SQS queue ARN or URL naming a FIFO queue.
func isFIFOQueue(queue string) bool { return strings.HasSuffix(queue, fifoSuffix) }

// subscriptionSettings checks Subscribe's Attributes in name order.
func subscriptionSettings(sub subscriber, attrs map[string]string) ([]subscriptionSetting, *awserr.Error) {
	out := make([]subscriptionSetting, 0, len(attrs))
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		v := attrs[name]
		set, detail := subscriptionAttribute(sub, name, &v)
		if detail != "" {
			return nil, errReason("Attributes", detail)
		}
		out = append(out, set)
	}
	return out, nil
}

// renderSubscriptionAttributes is GetSubscriptionAttributes of sub
// (sns.md 3.3, tf-messaging.md 3.5): the fixed members, then every
// attribute set, with FilterPolicyScope shown only beside a FilterPolicy.
func renderSubscriptionAttributes(ref subscriptionRef, sub *Subscription) map[string]string {
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
	return out
}

// filterPolicyScope is the subscription's FilterPolicyScope,
// MessageAttributes unless set.
func (sub *Subscription) filterPolicyScope() string {
	if scope, ok := sub.Attributes[attrFilterPolicyScope]; ok {
		return scope
	}
	return scopeMessageAttributes
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

// getSubscriptionAttributes returns every attribute of a subscription.
func (s *Service) getSubscriptionAttributes(_ context.Context, c *kernel.Call, in *snsapi.GetSubscriptionAttributesInput) (*snsapi.GetSubscriptionAttributesOutput, *awserr.Error) {
	ref, err := resolveSubscription(c, in.SubscriptionArn)
	if err != nil {
		return nil, err
	}
	sub, ok := s.readSubscription(ref)
	if !ok {
		return nil, errSubscriptionNotFound()
	}
	return &snsapi.GetSubscriptionAttributesOutput{Attributes: renderSubscriptionAttributes(ref, &sub)}, nil
}

// setSubscriptionAttributes sets one attribute of a subscription.
func (s *Service) setSubscriptionAttributes(_ context.Context, c *kernel.Call, in *snsapi.SetSubscriptionAttributesInput) (*snsapi.SetSubscriptionAttributesOutput, *awserr.Error) {
	ref, err := resolveSubscription(c, in.SubscriptionArn)
	if err != nil {
		return nil, err
	}
	_, err = writeSubscription(s, c, ref, func(sub *Subscription) *awserr.Error {
		set, detail := subscriptionAttribute(subscriber{protocol: sub.Protocol, endpoint: sub.Endpoint, topicFIFO: ref.topic.fifo()}, in.AttributeName, in.AttributeValue)
		if detail != "" {
			return errInvalidParameter(detail)
		}
		set.apply(sub)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &snsapi.SetSubscriptionAttributesOutput{}, nil
}

// writeSubscription runs fn on a copy of the subscription ref names and
// stores it when fn succeeds; a subscription that does not exist, or
// expired, is NotFound.
func writeSubscription(s *Service, c *kernel.Call, ref subscriptionRef, fn func(sub *Subscription) *awserr.Error) (struct{}, *awserr.Error) {
	return kernel.Write(c, s.cells, ref.topic.scope, func(st *State, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		t, ok := st.writable(ref.topic.name, now)
		if !ok {
			return struct{}{}, errSubscriptionNotFound()
		}
		i := t.indexOf(ref.id)
		if i < 0 {
			return struct{}{}, errSubscriptionNotFound()
		}
		if err := fn(&t.Subscriptions[i]); err != nil {
			return struct{}{}, err
		}
		st.Topics[ref.topic.name] = t
		return struct{}{}, nil
	})
}
