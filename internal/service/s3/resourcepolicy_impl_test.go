package s3_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// A deleted bucket has no policy to read, and neither has an ARN in a
// partition understudy does not serve or one with no bucket name.
// PutBucketPolicy checks only the document's form (s3.md 4.2.5, "phase 1
// only stores"), so a stored text policydoc refuses reads as no policy of
// a bucket that exists, as SQS reads its Policy attribute
// (policydoc.ParseStored).
func TestTheBucketPolicyFollowsTheBucketsLifecycle(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		var provider authz.PolicyProvider = h.svc
		parse := func(s string) aws.ARN {
			h.Helper()
			a, err := aws.ParseARN(s)
			if err != nil {
				h.Fatal(err)
			}
			return a
		}
		h.createBucket("gone")
		ok(h, who{}, s3api.OpPutBucketPolicy, &s3api.PutBucketPolicyInput{Bucket: "gone", Policy: `{"Statement":[]}`})
		if doc, found := provider.ResourcePolicy(parse("arn:aws:s3:::gone")); !found || doc != nil {
			h.Errorf("ResourcePolicy of a bucket whose policy does not parse = %+v, %t; want nil, true", doc, found)
		}
		for _, miss := range []string{"arn:aws-cn:s3:::gone", "arn:aws:s3:::/gone"} {
			if doc, found := provider.ResourcePolicy(parse(miss)); found || doc != nil {
				h.Errorf("ResourcePolicy(%s) = %+v, %t; want nil, false", miss, doc, found)
			}
		}

		ok(h, who{}, s3api.OpDeleteBucket, &s3api.DeleteBucketInput{Bucket: "gone"})
		if doc, found := provider.ResourcePolicy(parse("arn:aws:s3:::gone")); found || doc != nil {
			h.Errorf("ResourcePolicy after DeleteBucket = %+v, %t; want nil, false", doc, found)
		}
	})
}
