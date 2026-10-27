package ssm

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
)

// name is a parameter name as one request gave it.
type name struct {
	// canonical is the key in State.Parameters: a single-level name
	// without its leading slash, a hierarchical name as written
	// (ssm-parameters.md 3.2).
	canonical string
	// spelling is the request's form after trimming, which reads echo
	// (ssm-parameters.md 3.2).
	spelling string
}

// parseName trims outer spaces from raw and finds its canonical form.
// Not implemented yet: the character, prefix, hierarchy, depth and length
// rules of ssm-parameters.md 3.1 and 3.4, so every name is accepted.
func parseName(raw string) name {
	spelling := strings.Trim(raw, " ")
	canonical := spelling
	if rest, ok := strings.CutPrefix(spelling, "/"); ok && !strings.Contains(rest, "/") {
		canonical = rest
	}
	return name{canonical: canonical, spelling: spelling}
}

// parameterARN is the ARN of the parameter canonical in scope:
// parameter/<canonical name without its leading slash> (ssm-parameters.md
// 3.3).
func parameterARN(scope aws.Scope, canonical string) aws.ARN {
	return aws.ARN{
		Partition: scope.Partition.String(), Service: "ssm", Region: scope.Region.String(),
		Account: scope.Account.String(), Resource: "parameter/" + strings.TrimPrefix(canonical, "/"),
	}
}
