package iam

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// StoredPolicy returns a role's account and its trust policy as IAM
// evaluation reads them (authz.StoredPolicyReader; "The trust policy is the
// role's resource policy", iam-evaluation.md 3.1, iam-enforcement-surfaces.md
// 7.9). CreateRole and UpdateAssumeRolePolicy refuse a text policydoc
// refuses (checkTrustPolicy), so the policy is Parsed unless the stored
// state was edited. target must be the role's ARN as IAM builds it, path
// included; it is false for a missing role, a path or name that differs
// from the stored one, and any other IAM ARN, which has no resource policy.
func (s *Service) StoredPolicy(target aws.ARN) (authz.StoredPolicy, bool) {
	account, r, ok := s.roleAt(target)
	if !ok {
		return authz.StoredPolicy{}, false
	}
	return authz.StoredPolicy{Owner: account, Policy: authz.ReadStored(r.TrustPolicy)}, true
}
