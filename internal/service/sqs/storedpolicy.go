package sqs

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// StoredPolicy returns a queue's owner and its Policy attribute as IAM
// evaluation reads it (authz.StoredPolicyReader): NoPolicy for a queue
// without one, Parsed, or Unparsed with the parse error, since
// SetQueueAttributes checks only a policy's JSON form. It is false for a
// target ResourcePolicy does not find.
//
// Not implemented yet: it reports every target as not found.
func (s *Service) StoredPolicy(_ aws.ARN) (authz.StoredPolicy, bool) {
	return authz.StoredPolicy{}, false
}
