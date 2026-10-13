package sns

import (
	"strconv"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// TestRetryPolicyConstraints checks over random retry policies, each member
// drawn absent or from a range around its bounds, that a policy is refused
// exactly when it breaks a documented constraint (sns.md 7.3): numRetries
// 0 to 100, minDelayTarget 1 to maxDelayTarget, maxDelayTarget at most
// 3600, phase counts at least 0 and summing to at most numRetries, and a
// known backoffFunction. Constraints between two members apply when both
// are given.
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

		valid := (!hasMin || minDelay >= 1) &&
			(!hasMax || maxDelay <= maxDelayTargetSecond) &&
			(!hasMin || !hasMax || minDelay <= maxDelay) &&
			(!hasRetries || (retries >= 0 && retries <= maxDeliveryRetries && phases <= retries)) &&
			phasesValid && backoff != "cubic"

		detail := checkSubscriptionDeliveryPolicy(document.Map{{Key: "healthyRetryPolicy", Value: retry}})
		if valid != (detail == "") {
			t.Fatalf("policy %v: valid = %t, detail = %q", retry, valid, detail)
		}
	})
}
