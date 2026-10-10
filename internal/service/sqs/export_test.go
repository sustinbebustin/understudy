package sqs

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Scopes lists the scopes the service holds a cell for, whether or not
// any queue lives there.
func (s *Service) Scopes() []aws.Scope {
	return s.cells.Keys(store.ScopeKeys())
}
