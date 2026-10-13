package sns

import (
	"slices"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// defaultPolicy is the Policy of a topic nobody set one on, AWS-recorded
// (sns.md 2.2). It is rendered, never stored, so it always names the
// topic's own ARN and account.
func defaultPolicy(t topicRef) string {
	return `{"Version":"2008-10-17","Id":"__default_policy_ID","Statement":[{"Sid":"__default_statement_ID","Effect":"Allow",` +
		`"Principal":{"AWS":"*"},"Action":["SNS:GetTopicAttributes","SNS:SetTopicAttributes","SNS:AddPermission","SNS:RemovePermission",` +
		`"SNS:DeleteTopic","SNS:Subscribe","SNS:ListSubscriptionsByTopic","SNS:Publish"],"Resource":"` + t.arn().String() +
		`","Condition":{"StringEquals":{"AWS:SourceOwner":"` + t.scope.Account.String() + `"}}}]}`
}

// defaultEffectiveDeliveryPolicy is EffectiveDeliveryPolicy of a topic
// without a DeliveryPolicy, AWS-recorded (sns.md 2.2).
const defaultEffectiveDeliveryPolicy = `{"http":{"defaultHealthyRetryPolicy":{"minDelayTarget":20,"maxDelayTarget":20,"numRetries":3,` +
	`"numMaxDelayRetries":0,"numNoDelayRetries":0,"numMinDelayRetries":0,"backoffFunction":"linear"},"disableSubscriptionOverrides":false,` +
	`"defaultRequestPolicy":{"headerContentType":"text/plain; charset=UTF-8"}}}`

// parseObject decodes s as one JSON object.
func parseObject(s string) (document.Map, bool) {
	v, ok := parseJSON(s)
	if !ok {
		return nil, false
	}
	m, ok := v.(document.Map)
	return m, ok
}

// uniqueMembers decodes s as one JSON object whose member names are all
// distinct; false when s is not one. parseObject lets the last of
// duplicate names win.
func uniqueMembers(s string) (document.Map, bool) {
	d := jsonx.NewDecoder([]byte(s))
	if d.BeginObject() != nil {
		return nil, false
	}
	m := document.Map{}
	seen := map[string]struct{}{}
	for {
		name, more, err := d.NextName()
		if err != nil {
			return nil, false
		}
		if !more {
			break
		}
		if _, dup := seen[name]; dup {
			return nil, false
		}
		seen[name] = struct{}{}
		v, err := d.ReadValue()
		if err != nil {
			return nil, false
		}
		m = append(m, document.Entry{Key: name, Value: v})
	}
	return m, d.End() == nil
}

// parseJSON decodes s as one JSON value.
func parseJSON(s string) (document.Value, bool) {
	d := jsonx.NewDecoder([]byte(s))
	v, err := d.ReadValue()
	if err != nil || d.End() != nil {
		return nil, false
	}
	return v, true
}

// encode renders v as compact JSON in member order.
func encode(v document.Value) (string, bool) {
	e := jsonx.NewEncoder(jsonx.WireOrder)
	e.Value(v)
	b, err := e.Bytes()
	return string(b), err == nil
}

// storedDeliveryPolicy is the DeliveryPolicy a topic stores for m, as
// AWS-recorded (sns.md 2.2): the http policy loses its null members and
// gains "disableSubscriptionOverrides": false when it has none. False
// means m does not encode.
func storedDeliveryPolicy(m document.Map) (string, bool) {
	out := make(document.Map, 0, len(m))
	for _, e := range m {
		if http, ok := e.Value.(document.Map); ok && e.Key == "http" {
			kept := make(document.Map, 0, len(http)+1)
			for _, member := range http {
				if _, null := member.Value.(document.Null); !null {
					kept = append(kept, member)
				}
			}
			if _, ok := lookup(kept, "disableSubscriptionOverrides"); !ok {
				kept = append(kept, document.Entry{Key: "disableSubscriptionOverrides", Value: document.Bool(false)})
			}
			e.Value = kept
		}
		out = append(out, e)
	}
	return encode(out)
}

// effectiveDeliveryPolicy is EffectiveDeliveryPolicy for a stored
// DeliveryPolicy: the default with the stored members laid over it, a
// partial defaultHealthyRetryPolicy filled from the default's (sns.md 2.2).
func effectiveDeliveryPolicy(stored string) string {
	m, ok := parseObject(stored)
	if !ok {
		return defaultEffectiveDeliveryPolicy
	}
	base, _ := parseObject(defaultEffectiveDeliveryPolicy)
	for _, e := range m {
		if http, ok := e.Value.(document.Map); ok && e.Key == "http" {
			e.Value = overlayHTTP(mapMember(base, "http"), http)
		}
		base = set(base, e)
	}
	if s, ok := encode(base); ok {
		return s
	}
	return defaultEffectiveDeliveryPolicy
}

// subscriptionPolicyMembers are the members of a subscription's
// DeliveryPolicy in the order AWS shows them (LocalStack
// test_subscribe_external_http_endpoint_content_type, AWS-recorded).
func subscriptionPolicyMembers() []string {
	return []string{"healthyRetryPolicy", "sicklyRetryPolicy", "throttlePolicy", "requestPolicy", "guaranteed"}
}

// defaultHealthyRetryPolicy is the http defaultHealthyRetryPolicy of
// defaultEffectiveDeliveryPolicy.
func defaultHealthyRetryPolicy() document.Map {
	def, _ := parseObject(defaultEffectiveDeliveryPolicy)
	return mapMember(mapMember(def, "http"), "defaultHealthyRetryPolicy")
}

// storedSubscriptionDeliveryPolicy is the DeliveryPolicy a subscription
// stores for m, as AWS-recorded: the members of subscriptionPolicyMembers
// in that order, an absent one null except guaranteed, which is false, and
// a healthyRetryPolicy's members in the default's order. Members AWS does
// not name follow as given, which is inferred. False means m does not
// encode.
func storedSubscriptionDeliveryPolicy(m document.Map) (string, bool) {
	known := subscriptionPolicyMembers()
	out := make(document.Map, 0, len(m)+len(known))
	for _, key := range known {
		value, ok := lookup(m, key)
		switch {
		case !ok && key == "guaranteed":
			value = document.Bool(false)
		case !ok:
			value = document.Null{}
		case key == "healthyRetryPolicy":
			if retry, isMap := value.(document.Map); isMap {
				value = inOrderOf(defaultHealthyRetryPolicy(), retry)
			}
		}
		out = append(out, document.Entry{Key: key, Value: value})
	}
	for _, e := range m {
		if !slices.Contains(known, e.Key) {
			out = append(out, e)
		}
	}
	return encode(out)
}

// inOrderOf returns m's members, those order names first in order's order.
func inOrderOf(order, m document.Map) document.Map {
	out := make(document.Map, 0, len(m))
	for _, o := range order {
		if v, ok := lookup(m, o.Key); ok {
			out = append(out, document.Entry{Key: o.Key, Value: v})
		}
	}
	for _, e := range m {
		if _, ok := lookup(order, e.Key); !ok {
			out = append(out, e)
		}
	}
	return out
}

// subscriptionEffectiveDeliveryPolicy is EffectiveDeliveryPolicy of an
// http or https subscription whose stored DeliveryPolicy is stored ("" for
// none) on a topic whose stored DeliveryPolicy is topic ("" for none): the
// subscription's policy, its healthyRetryPolicy laid over the topic's
// effective http defaultHealthyRetryPolicy, and a null sicklyRetryPolicy,
// throttlePolicy or requestPolicy taken from the topic's http default of
// that name when it has one. Both AWS recordings
// (test_subscribe_external_http_endpoint_content_type) agree with this;
// the sickly and throttle fallbacks are inferred.
func subscriptionEffectiveDeliveryPolicy(stored, topic string) string {
	if stored == "" {
		stored, _ = storedSubscriptionDeliveryPolicy(document.Map{})
	}
	m, ok := parseObject(stored)
	if !ok {
		return stored
	}
	effective := defaultEffectiveDeliveryPolicy
	if topic != "" {
		effective = effectiveDeliveryPolicy(topic)
	}
	parsed, _ := parseObject(effective)
	http := mapMember(parsed, "http")
	out := make(document.Map, 0, len(m))
	for _, e := range m {
		out = append(out, document.Entry{Key: e.Key, Value: effectiveMember(http, e)})
	}
	if s, ok := encode(out); ok {
		return s
	}
	return stored
}

// effectiveMember is the effective value of member e of a subscription's
// DeliveryPolicy under the topic's effective http policy.
func effectiveMember(http document.Map, e document.Entry) document.Value {
	topicDefault := ""
	switch e.Key {
	case "healthyRetryPolicy":
		filled := append(document.Map(nil), mapMember(http, "defaultHealthyRetryPolicy")...)
		if retry, ok := e.Value.(document.Map); ok {
			for _, member := range retry {
				filled = set(filled, member)
			}
		}
		return filled
	case "sicklyRetryPolicy":
		topicDefault = "defaultSicklyRetryPolicy"
	case "throttlePolicy":
		topicDefault = "defaultThrottlePolicy"
	case "requestPolicy":
		topicDefault = "defaultRequestPolicy"
	}
	if _, null := e.Value.(document.Null); null && topicDefault != "" {
		if def, ok := lookup(http, topicDefault); ok {
			return def
		}
	}
	return e.Value
}

// overlayHTTP lays a stored http policy over the default one.
func overlayHTTP(def, http document.Map) document.Map {
	out := append(document.Map(nil), def...)
	for _, e := range http {
		if retry, ok := e.Value.(document.Map); ok && e.Key == "defaultHealthyRetryPolicy" {
			filled := append(document.Map(nil), mapMember(def, e.Key)...)
			for _, member := range retry {
				filled = set(filled, member)
			}
			e.Value = filled
		}
		out = set(out, e)
	}
	return out
}

// lookup returns the value of key in m.
func lookup(m document.Map, key string) (document.Value, bool) {
	for _, e := range m {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

// mapMember returns the member key of m when it is an object, else nil.
func mapMember(m document.Map, key string) document.Map {
	v, _ := lookup(m, key)
	if member, ok := v.(document.Map); ok {
		return member
	}
	return nil
}

// stringMember returns the member key of m when it is a string, else "".
func stringMember(m document.Map, key string) string {
	v, _ := lookup(m, key)
	if member, ok := v.(document.String); ok {
		return string(member)
	}
	return ""
}

// set replaces the member of e's key in m, or appends e.
func set(m document.Map, e document.Entry) document.Map {
	for i := range m {
		if m[i].Key == e.Key {
			m[i] = e
			return m
		}
	}
	return append(m, e)
}
