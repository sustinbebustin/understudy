package sns

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// Delivery policy limits (sns.md 7.3, sns-http-delivery.md 6.3).
const (
	maxDeliveryRetries   = 100
	maxDelayTargetSecond = 3600
	maxTotalRetryTime    = 3600 * time.Second
)

// backoffFunctions are the retry-backoff functions a retry policy may name
// (sns.md 7.3).
func backoffFunctions() []string {
	return []string{"arithmetic", "exponential", "geometric", "linear"}
}

// policyShape is the JSON type a DeliveryPolicy member must have.
type policyShape struct {
	kind policyKind
	// members are an object's members; any other is unexpected.
	members []policyMember
	// backoff marks the string that names a backoffFunction.
	backoff bool
}

type policyMember struct {
	name  string
	shape policyShape
}

type policyKind uint8

const (
	kindObject policyKind = iota + 1
	kindInteger
	kindString
	kindBoolean
)

// subscriptionPolicyShape is the documented members of a subscription's
// DeliveryPolicy (sns.md 3.3).
func subscriptionPolicyShape() policyShape {
	return policyShape{kind: kindObject, members: []policyMember{
		{"healthyRetryPolicy", retryShape()},
		{"sicklyRetryPolicy", retryShape()},
		{"throttlePolicy", throttleShape()},
		{"requestPolicy", requestShape()},
		{"guaranteed", policyShape{kind: kindBoolean}},
	}}
}

// topicPolicyShape is the documented members of a topic's DeliveryPolicy
// (sns.md 3.3).
func topicPolicyShape() policyShape {
	return policyShape{kind: kindObject, members: []policyMember{
		{"http", policyShape{kind: kindObject, members: []policyMember{
			{"defaultHealthyRetryPolicy", retryShape()},
			{"defaultSicklyRetryPolicy", retryShape()},
			{"defaultThrottlePolicy", throttleShape()},
			{"defaultRequestPolicy", requestShape()},
			{"disableSubscriptionOverrides", policyShape{kind: kindBoolean}},
		}}},
	}}
}

func retryShape() policyShape {
	integer := policyShape{kind: kindInteger}
	return policyShape{kind: kindObject, members: []policyMember{
		{"minDelayTarget", integer},
		{"maxDelayTarget", integer},
		{"numRetries", integer},
		{"numMaxDelayRetries", integer},
		{"numNoDelayRetries", integer},
		{"numMinDelayRetries", integer},
		{"backoffFunction", policyShape{kind: kindString, backoff: true}},
	}}
}

func throttleShape() policyShape {
	return policyShape{kind: kindObject, members: []policyMember{{"maxReceivesPerSecond", policyShape{kind: kindInteger}}}}
}

func requestShape() policyShape {
	return policyShape{kind: kindObject, members: []policyMember{{"headerContentType", policyShape{kind: kindString}}}}
}

// checkTopicDeliveryPolicy checks a topic's DeliveryPolicy as AWS does
// (sns-http-delivery.md 6.7, SNSH-18): each member's type first, then the
// http retry, throttle and request policies. It returns the text after
// "Invalid parameter: " (after "Attributes Reason: " at CreateTopic), ""
// when policy keeps them.
func checkTopicDeliveryPolicy(policy document.Map) string {
	if detail := checkShape("", policy, topicPolicyShape()); detail != "" {
		return detail
	}
	http := mapMember(policy, "http")
	for _, name := range []string{"defaultHealthyRetryPolicy", "defaultSicklyRetryPolicy"} {
		if detail := checkRetryPolicy("http."+name, member(http, name)); detail != "" {
			return detail
		}
	}
	if detail := checkThrottlePolicy("http.defaultThrottlePolicy", mapMember(http, "defaultThrottlePolicy")); detail != "" {
		return detail
	}
	if v, ok := lookup(mapMember(http, "defaultRequestPolicy"), "headerContentType"); ok && !plainOrJSON(v) {
		return attrDeliveryPolicy + ": http.defaultRequestPolicy: Content-Type set at the Topic DeliveryPolicy level can only be application/json or text/plain."
	}
	return ""
}

// checkSubscriptionDeliveryPolicy is checkTopicDeliveryPolicy for a
// subscription's DeliveryPolicy, whose members carry neither the http.
// nor the default prefix; raw is the subscription's RawMessageDelivery,
// without which a requestPolicy content type is application/json or
// text/plain (sns-http-delivery.md 3.3). Which content types raw delivery
// admits is not checked.
func checkSubscriptionDeliveryPolicy(policy document.Map, raw bool) string {
	if detail := checkShape("", policy, subscriptionPolicyShape()); detail != "" {
		return detail
	}
	for _, name := range []string{"healthyRetryPolicy", "sicklyRetryPolicy"} {
		if detail := checkRetryPolicy(name, member(policy, name)); detail != "" {
			return detail
		}
	}
	if detail := checkThrottlePolicy("throttlePolicy", mapMember(policy, "throttlePolicy")); detail != "" {
		return detail
	}
	if v, ok := lookup(mapMember(policy, "requestPolicy"), "headerContentType"); ok && !raw && !plainOrJSON(v) {
		return attrDeliveryPolicy + ": requestPolicy: If RawMessageDelivery is disabled the Content-Type can only be set to application/json or text/plain."
	}
	return ""
}

// checkShape checks that v, the member at path ("" for the policy itself),
// has shape, in member order: an unknown member, a value of the wrong
// type and a backoffFunction that names none are refused before any
// constraint is checked, as AWS refused an unknown backoffFunction and a
// string numRetries in policies that lacked a required member (SNSH-18).
// Those three texts are AWS-recorded; null is accepted for any member,
// and the type names other than "a string" are inferred.
func checkShape(path string, v document.Value, shape policyShape) string {
	if _, null := v.(document.Null); null {
		return ""
	}
	switch shape.kind {
	case kindObject:
		m, ok := v.(document.Map)
		if !ok {
			return typeMismatch(path, "object", v)
		}
		return checkMembers(path, m, shape.members)
	case kindInteger:
		if n, ok := v.(document.Number); !ok {
			return typeMismatch(path, "integer", v)
		} else if _, err := n.Int64(); err != nil {
			return typeMismatch(path, "integer", v)
		}
	case kindString:
		s, ok := v.(document.String)
		if !ok {
			return typeMismatch(path, "string", v)
		}
		if shape.backoff && !slices.ContainsFunc(backoffFunctions(), func(known string) bool { return strings.EqualFold(known, string(s)) }) {
			return attrDeliveryPolicy + ": Invalid JSON member value [" + path + "]: the value [" + string(s) + "] is not a valid backoff function"
		}
	case kindBoolean:
		if _, ok := v.(document.Bool); !ok {
			return typeMismatch(path, "boolean", v)
		}
	}
	return ""
}

// checkMembers is checkShape for each member of m, the object at path.
func checkMembers(path string, m document.Map, members []policyMember) string {
	for _, e := range m {
		at := e.Key
		if path != "" {
			at = path + "." + e.Key
		}
		i := slices.IndexFunc(members, func(member policyMember) bool { return member.name == e.Key })
		if i < 0 {
			return attrDeliveryPolicy + ": Unexpected JSON member: " + at
		}
		if detail := checkShape(at, e.Value, members[i].shape); detail != "" {
			return detail
		}
	}
	return ""
}

func typeMismatch(path, want string, got document.Value) string {
	var received string
	switch got.(type) {
	case document.Null:
		received = "null"
	case document.Number:
		received = "a number"
	case document.String:
		received = "a string"
	case document.Bool:
		received = "a boolean"
	case document.Map:
		received = "an object"
	case document.List:
		received = "an array"
	}
	return attrDeliveryPolicy + ": " + path + " type mismatch, expecting " + want + ", received " + received
}

// checkRetryPolicy checks the retry policy at path, whose members checkShape
// has typed. numRetries and then minDelayTarget must be given, before any
// bound is checked (AWS-recorded, SNSH-18). The bounds follow (sns.md 7.3):
// minDelayTarget at least 1, maxDelayTarget at most 3600, numRetries 0 to
// 100, phase counts at least 0 and summing to at most numRetries; a
// minDelayTarget at most maxDelayTarget when the policy gives both, since
// AWS fills an absent member from a default that may differ, and a
// minDelayTarget above 3600 refused alone, since no maxDelayTarget can
// reach it. The numRetries upper-bound text is AWS-recorded (ballerina
// topics-tests.bal); the other bound texts follow its form and are
// inferred, since SNSH-18 never reached them. Last, the total retry time
// may not pass 3,600 seconds (sns-http-delivery.md 6.3): its text is
// AWS-recorded for a far-over policy, while the boundary, the sum of the
// delays of retrySchedule's schedule, is inferred.
// v is the policy's value, nil when it is absent; an absent or null policy
// is not checked, and an empty one lacks numRetries.
func checkRetryPolicy(path string, v document.Value) string {
	retry, ok := v.(document.Map)
	if !ok {
		return ""
	}
	retries, hasRetries := intMember(retry, "numRetries")
	if !hasRetries {
		return attrDeliveryPolicy + ": " + path + ".numRetries must be specified"
	}
	minDelay, hasMin := intMember(retry, "minDelayTarget")
	if !hasMin {
		return attrDeliveryPolicy + ": " + path + ".minDelayTarget must be specified"
	}
	maxDelay, hasMax := intMember(retry, "maxDelayTarget")
	switch {
	case minDelay < 1:
		return atLeast(path, "minDelayTarget", 1)
	case hasMax && minDelay > maxDelay:
		return atMost(path, "minDelayTarget", maxDelay)
	case hasMax && maxDelay > maxDelayTargetSecond:
		return atMost(path, "maxDelayTarget", maxDelayTargetSecond)
	case minDelay > maxDelayTargetSecond:
		return atMost(path, "minDelayTarget", maxDelayTargetSecond)
	case retries < 0:
		return atLeast(path, "numRetries", 0)
	case retries > maxDeliveryRetries:
		return atMost(path, "numRetries", maxDeliveryRetries)
	}
	var phases int64
	for _, name := range []string{"numMaxDelayRetries", "numNoDelayRetries", "numMinDelayRetries"} {
		n, _ := intMember(retry, name)
		if n < 0 {
			return atLeast(path, name, 0)
		}
		phases += n
	}
	if phases > retries {
		return atLeast(path, "numRetries", phases)
	}
	if retrySchedule(retry).TotalDelay() > maxTotalRetryTime {
		return attrDeliveryPolicy + ": " + path + ": Total policy retry time cannot be greater than 3600 seconds"
	}
	return ""
}

// checkThrottlePolicy checks the throttle policy at path: maxReceivesPerSecond
// at least 1 (sns.md 7.3). The text is AWS-recorded for a subscription
// (SNSH-18) and inferred for a topic.
func checkThrottlePolicy(path string, throttle document.Map) string {
	if n, ok := intMember(throttle, "maxReceivesPerSecond"); ok && n < 1 {
		return attrDeliveryPolicy + ": " + path + ".maxReceivesPerSecond must be >= 1"
	}
	return ""
}

// plainOrJSON reports whether a headerContentType names application/json
// or text/plain, whatever its parameters: SNSH-18 recorded
// "application/json; charset=UTF-8" accepted with raw delivery off. The
// media type is compared case-insensitively (inferred).
func plainOrJSON(v document.Value) bool {
	s, ok := v.(document.String)
	if !ok {
		return false
	}
	mediaType, _, _ := strings.Cut(string(s), ";")
	mediaType = strings.TrimSpace(mediaType)
	return strings.EqualFold(mediaType, "application/json") || strings.EqualFold(mediaType, "text/plain")
}

// member returns the member name of m, nil when m has none.
func member(m document.Map, name string) document.Value {
	v, _ := lookup(m, name)
	return v
}

// intMember returns the member name of m when it is an integer.
func intMember(m document.Map, name string) (int64, bool) {
	v, _ := lookup(m, name)
	n, ok := v.(document.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil
}

func atMost(path, member string, limit int64) string {
	return attrDeliveryPolicy + ": " + path + "." + member + " must be less than or equal to " + strconv.FormatInt(limit, 10)
}

func atLeast(path, member string, limit int64) string {
	return attrDeliveryPolicy + ": " + path + "." + member + " must be greater than or equal to " + strconv.FormatInt(limit, 10)
}
