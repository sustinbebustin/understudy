package s3_test

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// A bucket policy is the resource policy of the bucket and of its objects
// (iam-enforcement-surfaces.md 7.3), read by the bucket's ARN or an object's
// ARN, in whichever account owns the bucket (bucket names are global), as
// GetBucketPolicy returns it. A bucket without one reads as no policy; a
// bucket that does not exist and an access point ARN name none.
func TestTheBucketPolicyIsTheBucketsResourcePolicy(t *testing.T) {
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
		other := who{account: "111111111111"}
		ok(h, who{}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "docs"})
		ok(h, other, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "elsewhere"})

		if doc, found := provider.ResourcePolicy(parse("arn:aws:s3:::docs")); !found || doc != nil {
			h.Errorf("ResourcePolicy of a bucket without a policy = %+v, %t; want nil, true", doc, found)
		}

		const docsPolicy = `{"Version":"2012-10-17","Statement":[{"Sid":"Read","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:role/reader"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::docs/*"}]}`
		const elsewherePolicy = `{"Version":"2012-10-17","Statement":[{"Sid":"Deny","Effect":"Deny","Principal":"*","Action":"s3:DeleteObject","Resource":"arn:aws:s3:::elsewhere/*"}]}`
		ok(h, who{}, s3api.OpPutBucketPolicy, &s3api.PutBucketPolicyInput{Bucket: "docs", Policy: docsPolicy})
		ok(h, other, s3api.OpPutBucketPolicy, &s3api.PutBucketPolicyInput{Bucket: "elsewhere", Policy: elsewherePolicy})
		stored := func(w who, bucket string) *policydoc.Document {
			h.Helper()
			p := ok(h, w, s3api.OpGetBucketPolicy, &s3api.GetBucketPolicyInput{Bucket: bucket}).Policy
			if p == nil {
				h.Fatalf("GetBucketPolicy(%s) returned no policy", bucket)
			}
			return policydoc.ParseStored(*p)
		}
		for _, c := range []struct {
			arn  string
			want *policydoc.Document
		}{
			{"arn:aws:s3:::docs", stored(who{}, "docs")},
			{"arn:aws:s3:::docs/reports/2026.pdf", stored(who{}, "docs")},
			{"arn:aws:s3:::elsewhere", stored(other, "elsewhere")},
			{"arn:aws:s3:::elsewhere/k", stored(other, "elsewhere")},
		} {
			if doc, found := provider.ResourcePolicy(parse(c.arn)); !found || doc == nil || !reflect.DeepEqual(doc, c.want) {
				h.Errorf("ResourcePolicy(%s) = %+v, %t; want the bucket's policy", c.arn, doc, found)
			}
		}

		ok(h, who{}, s3api.OpDeleteBucketPolicy, &s3api.DeleteBucketPolicyInput{Bucket: "docs"})
		if doc, found := provider.ResourcePolicy(parse("arn:aws:s3:::docs/k")); !found || doc != nil {
			h.Errorf("ResourcePolicy after DeleteBucketPolicy = %+v, %t; want nil, true", doc, found)
		}

		for _, miss := range []string{
			"arn:aws:s3:::absent",
			"arn:aws:s3:::absent/k",
			"arn:aws:s3:us-east-1:000000000000:accesspoint/docs",
		} {
			if doc, found := provider.ResourcePolicy(parse(miss)); found || doc != nil {
				h.Errorf("ResourcePolicy(%s) = %+v, %t; want nil, false", miss, doc, found)
			}
		}
	})
}
