package secretsmanager

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the resource policy of the secret whose full ARN
// target is (authz.PolicyProvider, iam-enforcement-surfaces.md 7.6); nil when
// it has none. ok is false for a missing secret and a partial ARN.
//
// It is not implemented yet: it finds nothing.
func (s *Service) ResourcePolicy(_ aws.ARN) (doc *policydoc.Document, ok bool) {
	return nil, false
}
