package enforce

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// The advisories Authorize journals tell what AWS would not tell the caller
// (iam-enforcement-surfaces.md 12.2): which policy decided a denial, a pair
// understudy could not decide, and a condition key it evaluated as absent.

// servedUnderAdvise ends the text of an advisory for a call understudy
// served although AWS would not have.
const servedUnderAdvise = "; iam.enforce is advise, so understudy served the call"

// deniedText is an access-denied advisory: the message the call answers
// with, then the policy type and policy that decided it.
func deniedText(message string, e evaluation, pair kernel.Pair) string {
	return message + "; decided by " + decidedBy(e.outcome.Decision) + " for " + identity.Render(e.denial.Principal) +
		" calling " + pair.Action.String() + " on " + resourceText(pair)
}

// unevaluatedText is an access-unevaluated advisory.
func unevaluatedText(p identity.Principal, pair kernel.Pair, cause error) string {
	return "understudy could not evaluate " + pair.Action.String() + " on " + resourceText(pair) + " for " + identity.Render(p) +
		": " + cause.Error() + servedUnderAdvise
}

// unsuppliedText is a condition-key-unsupplied advisory naming the key and
// the statement that tested it.
func unsuppliedText(u authz.Unsupplied, pair kernel.Pair) string {
	return "the condition key " + u.Key + " in " + named(u.PolicyType, u.Policy) + " statement " + statementName(u) +
		" was evaluated as absent for " + pair.Action.String() + " on " + resourceText(pair) +
		": AWS sends it with this request and understudy cannot compute it, so AWS may decide differently (iam-enforcement-surfaces.md 12.2)"
}

// decidedBy names the policy type of a denial and, for an explicit deny in
// a managed policy, its ARN.
func decidedBy(d authz.Decision) string {
	switch d := d.(type) {
	case authz.ImplicitDeny:
		return d.PolicyType.String()
	case authz.ExplicitDeny:
		return named(d.PolicyType, d.PolicyARN)
	case authz.Allow, nil:
	}
	return ""
}

// named is a policy type followed by the policy's ARN or name when there is
// one.
func named(t authz.PolicyType, policy string) string {
	if policy == "" {
		return t.String()
	}
	return t.String() + " " + policy
}

// statementName is a statement by its Sid, or by its position in the
// document when it has none.
func statementName(u authz.Unsupplied) string {
	if u.Statement.Sid != "" {
		return u.Statement.Sid
	}
	return "[" + strconv.Itoa(u.Statement.Index) + "]"
}

// pairARN is the resource an advisory is about: the pair's ARN, zero for a
// pair on no resource.
func pairARN(pair kernel.Pair) aws.ARN {
	if on, ok := pair.Resource.(kernel.ResourceARN); ok {
		return on.ARN
	}
	return aws.ARN{}
}

// resourceText is a pair's resource as a policy names it: its ARN, or "*".
func resourceText(pair kernel.Pair) string {
	if on, ok := pair.Resource.(kernel.ResourceARN); ok {
		return on.ARN.String()
	}
	return "*"
}
