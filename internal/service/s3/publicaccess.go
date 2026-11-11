package s3

import (
	"context"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// S3 Block Public Access under iam.enforce. In one account only
// BlockPublicPolicy, at PutBucketPolicy, and BlockPublicAcls, at ACL
// writes, change an outcome: AWS answers 403 AccessDenied with the 4.2
// text (iam-enforcement-surfaces.md 7.3, 12.2). understudy checks neither
// under any mode, since which policies AWS counts as public is not
// researched, and refusing only some would let the rest pass as checked.
// So each put the check would apply to journals a check-not-run advisory
// instead, and the coverage notes say so. Object ACL writes are not
// advised.

// The check-not-run advisory texts.
const (
	publicPolicyUnchecked = "understudy did not check whether this bucket policy is public: the bucket's BlockPublicPolicy is true, " +
		"and AWS refuses a public policy then with 403 AccessDenied (iam-enforcement-surfaces.md 7.3); the policy was stored"
	publicACLUnchecked = "understudy did not apply BlockPublicAcls: the bucket's BlockPublicAcls is true and the ACL grants AllUsers or AuthenticatedUsers, " +
		"which AWS refuses with 403 AccessDenied (iam-enforcement-surfaces.md 7.3); the ACL was stored"
)

// publicACLNote is PutBucketAcl's coverage note.
const publicACLNote = "the access control list is stored and echoed; not applied; BlockPublicAcls is not applied under any iam.enforce mode, " +
	"so an ACL granting AllUsers or AuthenticatedUsers while it is true is stored where AWS answers 403 AccessDenied, and journals a check-not-run advisory"

// putBucketPolicy stores the policy as sent, except that a bare account id
// named as an AWS principal becomes the account's root ARN
// (iam-evaluation.md 5.4, IAM-EVAL-25), and journals a check-not-run
// advisory when the bucket's BlockPublicPolicy is true.
func (s *Service) putBucketPolicy(ctx context.Context, c *kernel.Call, in *s3api.PutBucketPolicyInput) (*s3api.PutBucketPolicyOutput, *awserr.Error) {
	blocked := false
	policy := policydoc.RootAccountPrincipals(in.Policy, c.Scope().Partition)
	err := s.change(ctx, c, at(in.Bucket, in.ExpectedBucketOwner), func(b *Bucket, _ clock.Instant) *awserr.Error {
		blocked = b.PublicAccessBlock != nil && isTrue(b.PublicAccessBlock.BlockPublicPolicy)
		b.Policy = policy
		return nil
	})
	if err != nil {
		return nil, err
	}
	if blocked {
		c.Record(ctx, c.Scope(), journal.Advisory{Kind: journal.AdvisoryCheckNotRun, Resource: bucketARN(in.Bucket), Text: publicPolicyUnchecked})
	}
	return &s3api.PutBucketPolicyOutput{}, nil
}

// putBucketACL stores the ACL, and journals a check-not-run advisory when
// the bucket's BlockPublicAcls is true and the stored ACL grants AllUsers
// or AuthenticatedUsers.
func (s *Service) putBucketACL(ctx context.Context, c *kernel.Call, in *s3api.PutBucketAclInput) (*s3api.PutBucketAclOutput, *awserr.Error) {
	blocked := false
	err := s.change(ctx, c, at(in.Bucket, in.ExpectedBucketOwner), func(b *Bucket, _ clock.Instant) *awserr.Error {
		if err := putACL(in, b); err != nil {
			return err
		}
		blocked = b.PublicAccessBlock != nil && isTrue(b.PublicAccessBlock.BlockPublicAcls) && grantsToEveryone(b.Grants)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if blocked {
		c.Record(ctx, c.Scope(), journal.Advisory{Kind: journal.AdvisoryCheckNotRun, Resource: bucketARN(in.Bucket), Text: publicACLUnchecked})
	}
	return &s3api.PutBucketAclOutput{}, nil
}

// grantsToEveryone reports whether grants include one to the AllUsers or
// AuthenticatedUsers group.
func grantsToEveryone(grants []s3api.Grant) bool {
	return slices.ContainsFunc(grants, func(g s3api.Grant) bool {
		return g.Grantee != nil && g.Grantee.URI != nil && (*g.Grantee.URI == allUsersURI || *g.Grantee.URI == authenticatedUsersURI)
	})
}

// isTrue reports whether a stored flag is set.
func isTrue(v *bool) bool { return v != nil && *v }
