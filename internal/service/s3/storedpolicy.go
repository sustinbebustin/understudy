package s3

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// StoredPolicy returns a bucket's owning account and its bucket policy as
// IAM evaluation reads them (authz.StoredPolicyReader), for the bucket's ARN
// or an object's (iam-enforcement-surfaces.md 7.3). An S3 ARN names no
// account and bucket names are global, so the owner is the account that
// created the bucket. The policy is NoPolicy for a bucket without one,
// Parsed, or Unparsed with the parse error: PutBucketPolicy checks only the
// document's form (checkPolicy), and refusing the rest there would need
// the texts S3 answers grammar errors with, which s3.md does not record. It
// is false for a missing bucket and for any other S3 ARN, such as an access
// point's, which carries the region and account a bucket ARN leaves empty.
func (s *Service) StoredPolicy(target aws.ARN) (authz.StoredPolicy, bool) {
	if target.Service != "s3" || target.Partition != aws.PartitionAWS().String() || target.Region != "" || target.Account != "" {
		return authz.StoredPolicy{}, false
	}
	name, _, _ := strings.Cut(target.Resource, "/")
	if name == "" {
		return authz.StoredPolicy{}, false
	}
	type found struct {
		owner  aws.AccountID
		policy string
	}
	f, err := readBucket(s, name, func(b *Bucket) (found, *awserr.Error) {
		return found{owner: b.Home.OwnerAccount, policy: b.Policy}, nil
	})
	if err != nil {
		return authz.StoredPolicy{}, false
	}
	return authz.StoredPolicy{Owner: f.owner, Policy: authz.ReadStored(f.policy)}, true
}
