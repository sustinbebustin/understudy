package iam

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
)

// Identity returns p's identity-based policies (authz.IdentitySource): a
// user's or a session's role's inline policies, attached customer managed
// policies' default versions and attached AWS managed policies from the
// catalogue, the permissions boundary, a session's session policies, and the
// principal tags.
//
// Not implemented yet: it reports every principal as missing.
func (s *Service) Identity(p identity.Principal) (authz.IdentitySet, error) {
	return authz.IdentitySet{}, &authz.NoSuchPrincipalError{Principal: identity.Render(p)}
}

// PrincipalOf returns the principal an IAM ARN names
// (authz.IdentitySource): a root, a user with its ID, or a role as a session
// named "understudy" with no attributes; false when the user or role does
// not exist.
//
// Not implemented yet: it reports every ARN as naming no principal.
func (s *Service) PrincipalOf(_ aws.ARN) (identity.Principal, bool) {
	return nil, false
}
