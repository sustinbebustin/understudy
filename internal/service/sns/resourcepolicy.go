package sns

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the topic target names with its Policy
// attribute parsed, the default policy when none was set
// (authz.PolicyProvider, ARCHITECTURE.md 16). ok is false when no such
// topic exists, which skips the delivery access advisory. A stored policy
// policydoc refuses reads as none here; StoredPolicy tells the two apart.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	p, ok := s.StoredPolicy(target)
	if !ok {
		return nil, false
	}
	return authz.ProvidedDocument(p.Policy), true
}
