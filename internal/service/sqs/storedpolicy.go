package sqs

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/store"
)

// StoredPolicy returns a queue's owner, the queue's account, and its Policy
// attribute as IAM evaluation reads it (authz.StoredPolicyReader): NoPolicy
// for a queue without one ("A new SQS queue has an empty policy",
// iam-enforcement-surfaces.md 7.1), Parsed, or Unparsed with the parse
// error, since SetQueueAttributes checks only a policy's JSON form. It is
// false when no such queue exists.
func (s *Service) StoredPolicy(target aws.ARN) (authz.StoredPolicy, bool) {
	if target.Service != "sqs" {
		return authz.StoredPolicy{}, false
	}
	q, ok := queueOf(target)
	if !ok {
		return authz.StoredPolicy{}, false
	}
	cell, ok := s.cells.Lookup(q.scope)
	if !ok {
		return authz.StoredPolicy{}, false
	}
	type found struct {
		policy string
		ok     bool
	}
	f := store.Read(cell, func(st *State) found {
		queue, ok := st.Queues[q.name]
		return found{policy: queue.Attributes.Policy, ok: ok}
	})
	if !f.ok {
		return authz.StoredPolicy{}, false
	}
	return authz.StoredPolicy{Owner: q.scope.Account, Policy: authz.ReadStored(f.policy)}, true
}
