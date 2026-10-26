package s3

import (
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// StoredPolicy returns a bucket's owning account and its bucket policy as
// IAM evaluation reads them (authz.StoredPolicyReader), for the bucket or an
// object ARN: NoPolicy for a bucket without one, Parsed, or Unparsed with the
// parse error, since PutBucketPolicy checks only a policy's JSON form. It is
// false for a target ResourcePolicy does not find.
//
// Not implemented yet: it reports every target as not found.
func (s *Service) StoredPolicy(_ aws.ARN) (authz.StoredPolicy, bool) {
	return authz.StoredPolicy{}, false
}
