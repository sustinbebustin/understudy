package lambda

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the permissions of what target names
// (authz.PolicyProvider, ARCHITECTURE.md 16): for a function,
// arn:...:function:<name>[:<qualifier>], the policy GetPolicy returns; for
// a layer version, arn:...:layer:<name>:<version>, the policy
// GetLayerVersionPolicy returns (lambda-destinations-layers.md 7.4). doc is
// nil when there are no statements; ok is false when no such function or
// layer version exists, which skips the delivery access advisory.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	p, ok := s.StoredPolicy(target)
	if !ok {
		return nil, false
	}
	return authz.ProvidedDocument(p.Policy), true
}
