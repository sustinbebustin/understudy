package fault

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// sdkThrottleCodes are the error codes the AWS SDKs retry as throttling
// (aws-sdk-go-v2 retry/standard.go DefaultThrottleErrorCodes,
// tf-compute.md 1.3).
func sdkThrottleCodes() []string {
	return []string{
		"Throttling", "ThrottlingException", "ThrottledException", "RequestThrottledException",
		"TooManyRequestsException", "ProvisionedThroughputExceededException", "TransactionInProgressException",
		"RequestLimitExceeded", "BandwidthLimitExceeded", "LimitExceededException", "RequestThrottled",
		"SlowDown", "PriorRequestNotComplete", "EC2ThrottledException",
	}
}

// sdkTimeoutCodes are the transient error codes the AWS SDKs retry.
func sdkTimeoutCodes() []string { return []string{"RequestTimeout", "RequestTimeoutException"} }

// sdkRetryStatuses are the HTTP statuses the AWS SDKs retry whatever the
// code.
func sdkRetryStatuses() []int { return []int{500, 502, 503, 504} }

// RetryableWarning returns a warning for the CLI when r injects an error the
// AWS SDKs retry: every retry is a call the rule counts, so a NextN budget
// can be spent inside one client call; an action that runs after the handler
// runs the handler again on each attempt; and Terraform retries up to 25
// attempts (tf-compute.md 1.3). It returns false for a rule that injects
// nothing retryable or that Add would reject, and for a rule with origin
// internal: only the emulator's own calls see its errors, and their retries
// follow the delivery engine's policy, not an SDK's.
func (s *Store) RetryableWarning(r Rule) (string, bool) {
	if r.Match.Origin == InternalOnly {
		return "", false
	}
	res, err := resolve(s.cat, r)
	if err != nil {
		return "", false
	}
	for _, id := range res.all() {
		why, ok := sdkRetries(r.Action, id)
		if !ok {
			continue
		}
		var b strings.Builder
		if r.ID == "" {
			b.WriteString("this fault rule")
		} else {
			fmt.Fprintf(&b, "fault rule %q", r.ID)
		}
		fmt.Fprintf(&b, " injects %s, which AWS SDKs retry (%s): each retry is a call the rule counts", id.Shape.Name(), why)
		if t, ok := r.Trigger.(NextN); ok {
			fmt.Fprintf(&b, ", so one client call can spend the rule's next-n budget of %d", t.N)
		}
		if AfterHandler.runs(r) {
			b.WriteString("; the handler runs before the error is sent, so each retry runs the handler again against the state the first attempt already committed")
		}
		b.WriteString("; Terraform retries up to 25 attempts. To see the failure on the first attempt, ")
		if _, ok := r.Action.(StatusOKError); ok {
			b.WriteString("use an error action with a shape the SDKs do not retry")
		} else {
			b.WriteString("inject an error the SDKs do not retry")
		}
		return b.String(), true
	}
	return "", false
}

// sdkRetries says why the SDKs retry id when action a injects it, if they
// do. Both aws-sdk-go-v2 (handle_200_error.go) and botocore read an S3 200
// response whose body is <Error> as HTTP 500, so a StatusOKError is retried
// whatever its shape (wire-rest.md 3.7).
func sdkRetries(a Action, id awserr.Identity) (string, bool) {
	if _, ok := a.(StatusOKError); ok {
		return "they read a 200 response with an <Error> body as HTTP 500", true
	}
	codes := []string{id.Shape.Name(), id.QueryCode, id.JSONType[strings.LastIndexByte(id.JSONType, '#')+1:]}
	has := func(set []string) bool {
		return slices.ContainsFunc(codes, func(c string) bool { return slices.Contains(set, c) })
	}
	switch {
	case has(sdkThrottleCodes()):
		return "a throttling error", true
	case has(sdkTimeoutCodes()):
		return "a timeout error", true
	case slices.Contains(sdkRetryStatuses(), id.Status):
		return fmt.Sprintf("HTTP %d", id.Status), true
	case id.Retryable:
		return "the error is retryable", true
	default:
		return "", false
	}
}
