package s3_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// checkNotRun counts the check-not-run advisories journaled so far.
func checkNotRun(h *harness) int {
	n := 0
	events := h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}).Events
	for i := range events {
		if a, ok := events[i].Detail.(journal.Advisory); ok && a.Kind == journal.AdvisoryCheckNotRun && a.Resource.String() == "arn:aws:s3:::blocked" {
			n++
		}
	}
	return n
}

// AWS refuses a public bucket policy while BlockPublicPolicy is true, and
// a public ACL while BlockPublicAcls is true (iam-enforcement-surfaces.md
// 7.3). understudy checks neither, so PutBucketPolicy while
// BlockPublicPolicy is true, and PutBucketAcl granting AllUsers while
// BlockPublicAcls is true, journal a check-not-run advisory on the bucket.
// A new bucket blocks both (s3.md 4); with the flags off, or a private ACL,
// nothing is journaled.
func TestUncheckedPublicAccessJournalsCheckNotRun(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		t := h.T
		h.createBucket("blocked")
		const policy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::blocked/*"}]}`
		ok(h, who{}, s3api.OpPutBucketPolicy, &s3api.PutBucketPolicyInput{Bucket: "blocked", Policy: policy})
		if n := checkNotRun(h); n != 1 {
			t.Fatalf("check-not-run advisories after a policy on a new bucket = %d, want 1", n)
		}
		open := s3api.PublicAccessBlockConfiguration{BlockPublicAcls: new(false), IgnorePublicAcls: new(false), BlockPublicPolicy: new(false), RestrictPublicBuckets: new(false)}
		ok(h, who{}, s3api.OpPutPublicAccessBlock, &s3api.PutPublicAccessBlockInput{Bucket: "blocked", PublicAccessBlockConfiguration: open})
		ok(h, who{}, s3api.OpPutBucketPolicy, &s3api.PutBucketPolicyInput{Bucket: "blocked", Policy: policy})
		if n := checkNotRun(h); n != 1 {
			t.Fatalf("check-not-run advisories after a policy with BlockPublicPolicy false = %d, want still 1", n)
		}

		ok(h, who{}, s3api.OpPutBucketOwnershipControls, &s3api.PutBucketOwnershipControlsInput{
			Bucket:            "blocked",
			OwnershipControls: s3api.OwnershipControls{Rules: []s3api.OwnershipControlsRule{{ObjectOwnership: s3api.ObjectOwnershipBucketOwnerPreferred}}},
		})
		acls := s3api.PublicAccessBlockConfiguration{BlockPublicAcls: new(true)}
		ok(h, who{}, s3api.OpPutPublicAccessBlock, &s3api.PutPublicAccessBlockInput{Bucket: "blocked", PublicAccessBlockConfiguration: acls})
		ok(h, who{}, s3api.OpPutBucketAcl, &s3api.PutBucketAclInput{Bucket: "blocked", ACL: new(s3api.BucketCannedACLPrivate)})
		if n := checkNotRun(h); n != 1 {
			t.Fatalf("check-not-run advisories after a private ACL = %d, want still 1", n)
		}
		ok(h, who{}, s3api.OpPutBucketAcl, &s3api.PutBucketAclInput{Bucket: "blocked", ACL: new(s3api.BucketCannedACLPublicRead)})
		if n := checkNotRun(h); n != 2 {
			t.Errorf("check-not-run advisories after a public-read ACL with BlockPublicAcls true = %d, want 2", n)
		}
	})
}
