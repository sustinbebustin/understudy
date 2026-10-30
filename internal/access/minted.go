package access

import "github.com/sustinbebustin/understudy/internal/aws"

// MintedPattern returns the policy Resource pattern Policy renders for a
// resource whose ARN carries a segment understudy draws rather than the
// caller names (a KMS key id, a Secrets Manager suffix, an SNS
// subscription id, an event source mapping UUID, the log stream of a
// function's execution environment), and true; for any other ARN it
// returns "" and false, and Policy renders the ARN exactly. mode is the
// recorded use's Mode: a log stream is minted only under AsExecutionRole.
//
// It is not implemented yet: it reports every ARN as not minted.
func MintedPattern(mode Mode, a aws.ARN) (pattern string, minted bool) {
	_, _ = mode, a
	return "", false
}
