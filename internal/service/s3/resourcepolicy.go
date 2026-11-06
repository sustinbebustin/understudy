package s3

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the bucket policy of the bucket target names, or of
// the bucket holding the object it names (authz.PolicyProvider,
// iam-enforcement-surfaces.md 7.3), in whichever account owns the bucket,
// since bucket names are global; nil when the bucket has none. ok is false
// for a missing bucket and for any other S3 ARN, as StoredPolicy answers. A
// stored text policydoc refuses reads as no policy here; StoredPolicy tells
// the two apart for the evaluator.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	p, ok := s.StoredPolicy(target)
	if !ok {
		return nil, false
	}
	return authz.ProvidedDocument(p.Policy), true
}
