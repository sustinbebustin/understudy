package lambda

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// StoredPolicy returns a function's owner and the permissions of the
// qualifier target names as IAM evaluation reads them
// (authz.StoredPolicyReader): NoPolicy for a function without any, else
// Parsed. It is false for a target ResourcePolicy does not find.
//
// Not implemented yet: it reports every target as not found.
func (s *Service) StoredPolicy(_ aws.ARN) (authz.StoredPolicy, bool) {
	return authz.StoredPolicy{}, false
}
