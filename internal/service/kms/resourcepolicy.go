package kms

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the stored key policy of the key target names
// (authz.PolicyProvider, iam-enforcement-surfaces.md 7.5), the one CreateKey
// or PutKeyPolicy sent or the default one: every key has a key policy. ok is
// false for a missing key, an alias ARN and any other KMS ARN.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	p, ok := s.StoredPolicy(target)
	if !ok {
		return nil, false
	}
	return authz.ProvidedDocument(p.Policy), true
}
