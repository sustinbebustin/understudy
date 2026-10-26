package iam

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// StoredPolicy returns a role's account and its trust policy as IAM
// evaluation reads them (authz.StoredPolicyReader): Parsed, or Unparsed with
// the parse error. It is false for a target ResourcePolicy does not find.
//
// Not implemented yet: it reports every target as not found.
func (s *Service) StoredPolicy(_ aws.ARN) (authz.StoredPolicy, bool) {
	return authz.StoredPolicy{}, false
}
