package sns

import (
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// Delivery policy limits (sns.md 7.3).
const (
	maxDeliveryRetries   = 100
	maxDelayTargetSecond = 3600
)

// backoffFunctions are the retry-backoff functions a retry policy may name
// (sns.md 7.3).
func backoffFunctions() []string {
	return []string{"arithmetic", "exponential", "geometric", "linear"}
}

// checkTopicDeliveryPolicy checks the http retry and throttle policies of
// a topic's DeliveryPolicy against AWS's constraints. It returns the text
// after "Invalid parameter: " (after "Attributes Reason: " at CreateTopic),
// "" when policy keeps them.
func checkTopicDeliveryPolicy(policy document.Map) string {
	http := mapMember(policy, "http")
	if detail := checkRetryPolicy("http.defaultHealthyRetryPolicy", mapMember(http, "defaultHealthyRetryPolicy")); detail != "" {
		return detail
	}
	return checkThrottlePolicy("http.defaultThrottlePolicy", mapMember(http, "defaultThrottlePolicy"))
}

// checkSubscriptionDeliveryPolicy is checkTopicDeliveryPolicy for a
// subscription's DeliveryPolicy, whose members carry neither the http.
// nor the default prefix.
func checkSubscriptionDeliveryPolicy(policy document.Map) string {
	if detail := checkRetryPolicy("healthyRetryPolicy", mapMember(policy, "healthyRetryPolicy")); detail != "" {
		return detail
	}
	return checkThrottlePolicy("throttlePolicy", mapMember(policy, "throttlePolicy"))
}

// checkRetryPolicy checks the retry policy at path (sns.md 7.3): numRetries
// 0 to 100, minDelayTarget at least 1, maxDelayTarget at most 3600, phase
// counts at least 0 and a known backoffFunction. A constraint between two
// members (minDelayTarget at most maxDelayTarget, the phase counts summing
// to at most numRetries) applies when the policy gives both, since AWS
// fills an absent member from a default that may differ. The numRetries
// upper-bound text is AWS-recorded (ballerina topics-tests.bal); the
// others follow its form and are inferred. A member that is not an integer
// is left to AWS's unrecorded type checks.
func checkRetryPolicy(path string, retry document.Map) string {
	if retry == nil {
		return ""
	}
	minDelay, hasMin := intMember(retry, "minDelayTarget")
	maxDelay, hasMax := intMember(retry, "maxDelayTarget")
	switch {
	case hasMin && minDelay < 1:
		return atLeast(path, "minDelayTarget", 1)
	case hasMin && hasMax && minDelay > maxDelay:
		return atMost(path, "minDelayTarget", maxDelay)
	case hasMax && maxDelay > maxDelayTargetSecond:
		return atMost(path, "maxDelayTarget", maxDelayTargetSecond)
	}
	retries, hasRetries := intMember(retry, "numRetries")
	switch {
	case hasRetries && retries < 0:
		return atLeast(path, "numRetries", 0)
	case hasRetries && retries > maxDeliveryRetries:
		return atMost(path, "numRetries", maxDeliveryRetries)
	}
	var phases int64
	for _, name := range []string{"numMaxDelayRetries", "numNoDelayRetries", "numMinDelayRetries"} {
		n, ok := intMember(retry, name)
		if ok && n < 0 {
			return atLeast(path, name, 0)
		}
		phases += n
	}
	if hasRetries && phases > retries {
		return atLeast(path, "numRetries", phases)
	}
	if v, ok := lookup(retry, "backoffFunction"); ok {
		if fn, isString := v.(document.String); isString && !slices.ContainsFunc(backoffFunctions(), func(known string) bool { return strings.EqualFold(known, string(fn)) }) {
			return attrDeliveryPolicy + ": " + path + ".backoffFunction must be one of " + strings.Join(backoffFunctions(), ", ")
		}
	}
	return ""
}

// checkThrottlePolicy checks the throttle policy at path: maxReceivesPerSecond
// at least 1 (sns.md 7.3; inferred text).
func checkThrottlePolicy(path string, throttle document.Map) string {
	if n, ok := intMember(throttle, "maxReceivesPerSecond"); ok && n < 1 {
		return atLeast(path, "maxReceivesPerSecond", 1)
	}
	return ""
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
