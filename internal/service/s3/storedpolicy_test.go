package s3_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// The evaluator reads a bucket policy through StoredPolicyReader, by the
// bucket's or an object's ARN, owned by the bucket's account: an S3 ARN
// names no account, and bucket names are global, so the owner is the
// account that created the bucket (iam-evaluation.md 2.4, the cross-account
// rule needs it). A bucket without a policy reads NoPolicy; a policy
// PutBucketPolicy stored but policydoc cannot read is Unparsed, never no
// policy (iam-enforcement-surfaces.md 12.10); a bucket that does not exist
// names none.
func TestStoredPolicyReadsTheBucketPolicyAndItsOwner(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		var reader authz.StoredPolicyReader = h.svc
		parse := func(s string) aws.ARN {
			h.Helper()
			a, err := aws.ParseARN(s)
			if err != nil {
				h.Fatal(err)
			}
			return a
		}
		otherAccount, err := aws.ParseAccountID("111111111111")
		if err != nil {
			h.Fatal(err)
		}
		other := who{account: "111111111111"}
		ok(h, who{}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "docs"})
		ok(h, other, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "elsewhere"})

		if got, found := reader.StoredPolicy(parse("arn:aws:s3:::docs/k")); !found || got != (authz.StoredPolicy{Owner: aws.DefaultAccount(), Policy: authz.NoPolicy{}}) {
			h.Errorf("StoredPolicy of an object in a bucket without a policy = %+v, %t; want NoPolicy owned by 000000000000", got, found)
		}

		const policy = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:DeleteObject","Resource":"arn:aws:s3:::elsewhere/*"}]}`
		ok(h, other, s3api.OpPutBucketPolicy, &s3api.PutBucketPolicyInput{Bucket: "elsewhere", Policy: policy})
		stored := ok(h, other, s3api.OpGetBucketPolicy, &s3api.GetBucketPolicyInput{Bucket: "elsewhere"}).Policy
		if stored == nil {
			h.Fatal("GetBucketPolicy returned no policy")
		}
		want := authz.StoredPolicy{Owner: otherAccount, Policy: authz.Parsed{Document: *policydoc.ParseStored(*stored)}}
		for _, arn := range []string{"arn:aws:s3:::elsewhere", "arn:aws:s3:::elsewhere/k"} {
			if got, found := reader.StoredPolicy(parse(arn)); !found || !reflect.DeepEqual(got, want) {
				h.Errorf("StoredPolicy(%s) = %+v, %t; want %+v", arn, got, found, want)
			}
		}

		// A JSON object with a Statement, which PutBucketPolicy accepts, that
		// policydoc refuses (an unknown element).
		const unreadable = `{"Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:*","Resource":"arn:aws:s3:::docs/*","Bogus":1}]}`
		ok(h, who{}, s3api.OpPutBucketPolicy, &s3api.PutBucketPolicyInput{Bucket: "docs", Policy: unreadable})
		got, found := reader.StoredPolicy(parse("arn:aws:s3:::docs"))
		u, isUnparsed := got.Policy.(authz.Unparsed)
		var parseErr *policydoc.ParseError
		if !found || got.Owner != aws.DefaultAccount() || !isUnparsed || u.Text == "" || !errors.As(u.Err, &parseErr) {
			h.Errorf("StoredPolicy of an unreadable bucket policy = %+v, %t; want Unparsed with the text and the *policydoc.ParseError", got, found)
		}

		for _, miss := range []string{"arn:aws:s3:::absent", "arn:aws:s3:::absent/k", "arn:aws:s3:us-east-1:000000000000:accesspoint/docs"} {
			if got, found := reader.StoredPolicy(parse(miss)); found {
				h.Errorf("StoredPolicy(%s) = %+v, want none", miss, got)
			}
		}
	})
}
