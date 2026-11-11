package sns

import (
	"strconv"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// TestRetryPolicyConstraints checks over random retry policies, each member
// drawn absent or from a range around its bounds, that a policy is refused
// when it breaks a documented constraint (sns.md 7.3): numRetries and
// minDelayTarget given (SNSH-18), numRetries 0 to 100, minDelayTarget 1 to
// maxDelayTarget, maxDelayTarget at most 3600, phase counts at least 0 and
// summing to at most numRetries, and a known backoffFunction. Constraints
// between two members apply when both are given; minDelayTarget is at most
// 3600 alone, since it can be at most a maxDelayTarget that is at most
// 3600. A policy that keeps them is accepted when its total retry time
// cannot pass 3,600 seconds (sns-http-delivery.md 6.3): no retry waits
// longer than the larger of minDelayTarget and maxDelayTarget (20, the
// system default's, when absent).
func TestRetryPolicyConstraints(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var retry document.Map
		member := func(name string, lo, hi int) (int, bool) {
			if !rapid.Bool().Draw(t, name+"Given") {
				return 0, false
			}
			n := rapid.IntRange(lo, hi).Draw(t, name)
			retry = append(retry, document.Entry{Key: name, Value: document.Number(strconv.Itoa(n))})
			return n, true
		}
		minDelay, hasMin := member("minDelayTarget", -2, 4000)
		maxDelay, hasMax := member("maxDelayTarget", -2, 4000)
		retries, hasRetries := member("numRetries", -2, 120)
		phases, phasesValid := 0, true
		for _, name := range []string{"numNoDelayRetries", "numMinDelayRetries", "numMaxDelayRetries"} {
			n, _ := member(name, -2, 60)
			phases += n
			phasesValid = phasesValid && n >= 0
		}
		backoff := rapid.SampledFrom([]string{"", "linear", "Exponential", "geometric", "arithmetic", "cubic"}).Draw(t, "backoffFunction")
		if backoff != "" {
			retry = append(retry, document.Entry{Key: "backoffFunction", Value: document.String(backoff)})
		}

		valid := hasMin && hasRetries &&
			minDelay >= 1 && minDelay <= 3600 &&
			(!hasMax || maxDelay <= 3600) &&
			(!hasMax || minDelay <= maxDelay) &&
			retries >= 0 && retries <= 100 && phases <= retries &&
			phasesValid && backoff != "cubic"
		longest := 20
		if hasMax {
			longest = maxDelay
		}
		longest = max(longest, minDelay)

		detail := checkSubscriptionDeliveryPolicy(document.Map{{Key: "healthyRetryPolicy", Value: retry}}, false)
		switch {
		case !valid && detail == "":
			t.Fatalf("policy %v breaks a constraint and is accepted", retry)
		case valid && retries*longest <= 3600 && detail != "":
			t.Fatalf("policy %v keeps every constraint and is refused: %q", retry, detail)
		}
	})
}
