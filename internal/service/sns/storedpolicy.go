package sns

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// StoredPolicy returns a topic's owner, the topic's account, and its Policy
// attribute as IAM evaluation reads it (authz.StoredPolicyReader): the
// default topic policy until one is set (iam-enforcement-surfaces.md 7.2),
// parsed, or Unparsed with the parse error. It is false when no such topic
// exists.
func (s *Service) StoredPolicy(target aws.ARN) (authz.StoredPolicy, bool) {
	ref, ok := s.topicOf(target.String())
	if !ok {
		return authz.StoredPolicy{}, false
	}
	t, ok := s.readTopic(ref)
	if !ok {
		return authz.StoredPolicy{}, false
	}
	text, set := t.Attributes[attrPolicy]
	if !set {
		text = defaultPolicy(ref)
	}
	return authz.StoredPolicy{Owner: ref.scope.Account, Policy: authz.ReadStored(text)}, true
}
