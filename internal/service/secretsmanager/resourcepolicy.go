package secretsmanager

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the resource policy of the secret whose full ARN
// target is (authz.PolicyProvider, iam-enforcement-surfaces.md 7.6); nil when
// it has none. ok is false for a missing secret and a partial ARN, as
// StoredPolicy answers.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	p, ok := s.StoredPolicy(target)
	if !ok {
		return nil, false
	}
	return authz.ProvidedDocument(p.Policy), true
}
