package kms

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the stored key policy of the key target names
// (authz.PolicyProvider, iam-enforcement-surfaces.md 7.5); ok is false for a
// missing key, an alias ARN and any other KMS ARN.
//
// It is not implemented yet: it finds nothing.
func (s *Service) ResourcePolicy(_ aws.ARN) (doc *policydoc.Document, ok bool) {
	return nil, false
}
