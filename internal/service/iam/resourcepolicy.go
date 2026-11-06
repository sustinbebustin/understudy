package iam

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the trust policy of the role target names
// (authz.PolicyProvider; "The trust policy is the role's resource policy",
// iam-evaluation.md 3.1, iam-enforcement-surfaces.md 7.9). target must be
// the role's ARN as IAM builds it, path included; ok is false for a missing
// role, a path or name that differs from the stored one, and any other IAM
// ARN, as StoredPolicy answers.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	p, ok := s.StoredPolicy(target)
	if !ok {
		return nil, false
	}
	return authz.ProvidedDocument(p.Policy), true
}
