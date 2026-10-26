package iam

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the trust policy of the role target names
// (authz.PolicyProvider, iam-enforcement-surfaces.md 7.9); ok is false for a
// missing role and for any other IAM ARN, which has no resource policy.
//
// It is not implemented yet: it finds nothing.
func (s *Service) ResourcePolicy(_ aws.ARN) (doc *policydoc.Document, ok bool) {
	return nil, false
}
