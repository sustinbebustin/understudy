package s3

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the bucket policy of the bucket target names, or of
// the bucket holding the object it names (authz.PolicyProvider,
// iam-enforcement-surfaces.md 7.3), in whichever account owns the bucket,
// since bucket names are global; nil when the bucket has none. ok is false
// for a missing bucket and for any other S3 ARN, such as an access point's,
// which carries the region and account a bucket ARN leaves empty.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	if target.Service != "s3" || target.Partition != aws.PartitionAWS().String() || target.Region != "" || target.Account != "" {
		return nil, false
	}
	name, _, _ := strings.Cut(target.Resource, "/")
	if name == "" {
		return nil, false
	}
	policy, err := readBucket(s, name, func(b *Bucket) (string, *awserr.Error) { return b.Policy, nil })
	if err != nil {
		return nil, false
	}
	// PutBucketPolicy checks only the document's form (checkPolicy), so a
	// text policydoc refuses reads as no policy. The NOTE on
	// authz.PolicyProvider says why that must change before enforcement.
	// Refusing such a text at PutBucketPolicy instead would need the texts
	// S3 answers grammar errors with, which s3.md does not record: it has
	// only checkPolicy's two.
	return policydoc.ParseStored(policy), true
}
