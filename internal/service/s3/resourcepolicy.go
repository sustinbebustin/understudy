package s3

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the bucket policy of the bucket target names, or of
// the bucket holding the object it names (authz.PolicyProvider,
// iam-enforcement-surfaces.md 7.3); nil when the bucket has none. ok is false
// for a missing bucket and any other S3 ARN.
//
// It is not implemented yet: it finds nothing.
func (s *Service) ResourcePolicy(_ aws.ARN) (doc *policydoc.Document, ok bool) {
	return nil, false
}
