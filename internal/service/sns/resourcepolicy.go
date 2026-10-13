package sns

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the topic target names with its Policy
// attribute parsed, the default policy when none was set, for the
// delivery access advisory (outbox.PolicySource, ARCHITECTURE.md 16). ok
// is false when no such topic exists, which skips the advisory. A stored
// policy policydoc refuses reads as none.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	ref, ok := s.topicOf(target.String())
	if !ok {
		return nil, false
	}
	t, ok := s.readTopic(ref)
	if !ok {
		return nil, false
	}
	text, set := t.Attributes[attrPolicy]
	if !set {
		text = defaultPolicy(ref)
	}
	return policydoc.ParseStored(text), true
}
