package e2e_test

import (
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// TestS3WorkerObjectsUseTheBucketKMSKey checks that an object a worker
// wrote without SSE headers took the bucket's default rule: aws:kms with
// the pipeline key's full ARN and the bucket key on (s3.md 4.2.2, 5.7).
func TestS3WorkerObjectsUseTheBucketKMSKey(t *testing.T) {
	p := pipe(t)
	enc, err := env.s3.GetBucketEncryption(t.Context(), &s3.GetBucketEncryptionInput{Bucket: aws.String(bucketName)})
	if err != nil {
		t.Fatalf("s3 GetBucketEncryption: %v", err)
	}
	if rules := enc.ServerSideEncryptionConfiguration.Rules; len(rules) != 1 || rules[0].ApplyServerSideEncryptionByDefault == nil ||
		rules[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm != s3types.ServerSideEncryptionAwsKms ||
		aws.ToString(rules[0].ApplyServerSideEncryptionByDefault.KMSMasterKeyID) != p.keyARN || !aws.ToBool(rules[0].BucketKeyEnabled) {
		t.Errorf("bucket encryption %+v, want one aws:kms rule with KMSMasterKeyID %s as sent and BucketKeyEnabled", enc.ServerSideEncryptionConfiguration, p.keyARN)
	}

	id := newID(t)
	publish(t, message{ID: id}, map[string]string{"kind": "order"})
	waitProcessed(t, goWorker, id)
	head, err := env.s3.HeadObject(t.Context(), &s3.HeadObjectInput{Bucket: aws.String(bucketName), Key: aws.String(processedKey(goWorker, id))})
	if err != nil {
		t.Fatalf("s3 HeadObject: %v", err)
	}
	if head.ServerSideEncryption != s3types.ServerSideEncryptionAwsKms || aws.ToString(head.SSEKMSKeyId) != p.keyARN || !aws.ToBool(head.BucketKeyEnabled) {
		t.Errorf("HeadObject ServerSideEncryption %q, SSEKMSKeyId %q, BucketKeyEnabled %v; want aws:kms, %s, true",
			head.ServerSideEncryption, aws.ToString(head.SSEKMSKeyId), aws.ToBool(head.BucketKeyEnabled), p.keyARN)
	}
	if aws.ToString(head.ContentType) != "application/json" {
		t.Errorf("ContentType %q, want application/json as the worker sent", aws.ToString(head.ContentType))
	}
}

// TestS3ListObjectsV2GroupsWorkerOutput lists the processed/ prefix with a
// delimiter: one common prefix per worker, and the keys under one worker's
// prefix (s3.md 9.1).
func TestS3ListObjectsV2GroupsWorkerOutput(t *testing.T) {
	pipe(t)
	ids := map[string]string{goWorker: newID(t), pyWorker: newID(t), imageWorker: newID(t)}
	publish(t, message{ID: ids[goWorker]}, map[string]string{"kind": "order"})
	publish(t, message{ID: ids[pyWorker], Priority: 6}, nil)
	publish(t, message{ID: ids[imageWorker]}, map[string]string{"kind": "refund", "amount": "101"})
	advance(t, pyWindow())
	for fn, id := range ids {
		waitProcessed(t, fn, id)
	}

	out, err := env.s3.ListObjectsV2(t.Context(), &s3.ListObjectsV2Input{Bucket: aws.String(bucketName), Prefix: aws.String("processed/"), Delimiter: aws.String("/")})
	if err != nil {
		t.Fatalf("s3 ListObjectsV2: %v", err)
	}
	var prefixes []string
	for _, cp := range out.CommonPrefixes {
		prefixes = append(prefixes, aws.ToString(cp.Prefix))
	}
	want := []string{"processed/go-worker/", "processed/image-worker/", "processed/py-worker/"}
	if !slices.Equal(prefixes, want) || len(out.Contents) != 0 {
		t.Errorf("CommonPrefixes %v and %d keys, want %v and no keys directly under processed/", prefixes, len(out.Contents), want)
	}

	var keys []string
	pages := s3.NewListObjectsV2Paginator(env.s3, &s3.ListObjectsV2Input{Bucket: aws.String(bucketName), Prefix: aws.String("processed/" + goWorker + "/"), MaxKeys: aws.Int32(5)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(t.Context())
		if err != nil {
			t.Fatalf("s3 ListObjectsV2 page: %v", err)
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	if !slices.Contains(keys, processedKey(goWorker, ids[goWorker])) || !slices.IsSorted(keys) {
		t.Errorf("keys under processed/go-worker/ %v, want them in ascending order and %s among them", keys, processedKey(goWorker, ids[goWorker]))
	}
}
