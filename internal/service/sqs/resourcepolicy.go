package sqs

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the queue target names with its stored Policy
// attribute parsed (authz.PolicyProvider, ARCHITECTURE.md 16); nil when the
// queue has none. ok is false when no such queue exists, which skips the
// delivery access advisory. SetQueueAttributes accepts any JSON object, so
// a text policydoc refuses reads as no policy here; StoredPolicy tells the
// two apart.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	p, ok := s.StoredPolicy(target)
	if !ok {
		return nil, false
	}
	return authz.ProvidedDocument(p.Policy), true
}
