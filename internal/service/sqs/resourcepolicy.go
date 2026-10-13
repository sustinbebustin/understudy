package sqs

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/store"
)

// ResourcePolicy returns the queue target names with its stored Policy
// attribute parsed, for the delivery access advisory (outbox.PolicySource,
// ARCHITECTURE.md 16); nil when the queue has none. ok is false when no
// such queue exists, which skips the advisory.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	if target.Service != "sqs" {
		return nil, false
	}
	q, ok := queueOf(target)
	if !ok {
		return nil, false
	}
	cell, ok := s.cells.Lookup(q.scope)
	if !ok {
		return nil, false
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
		return nil, false
	}
	// SetQueueAttributes accepts any JSON object, so a text policydoc
	// refuses reads as no policy.
	return policydoc.ParseStored(f.policy), true
}
