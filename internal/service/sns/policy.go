package sns

import (
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

// storedDeliveryPolicy is the DeliveryPolicy a topic stores for v, as
// AWS-recorded (sns.md 2.2): the http policy loses its null members and
// gains "disableSubscriptionOverrides": false when it has none. False
// means v is not a JSON object.
func storedDeliveryPolicy(v string) (string, bool) {
	m, ok := parseObject(v)
	if !ok {
		return "", false
	}
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
