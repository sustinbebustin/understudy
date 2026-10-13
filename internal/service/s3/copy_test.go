package s3_test

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/blob"
)

// TestCopyObject: COPY keeps the source's metadata and tags, REPLACE takes
// them from the request with binary/octet-stream as the default type, a
// copy onto itself that changes nothing is refused, the source key may be
// percent-encoded with its slash escaped as Terraform sends it, and the
// copy keeps the source's checksum (s3.md 7, tf-remaining.md 8).
func TestCopyObject(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("src")
		h.createBucket("dst")
		ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{
			Bucket: "src", Key: "dir/a b.txt", Body: bytesBody("hello"), ContentType: new("text/plain"),
			Metadata: map[string]string{"owner": "probe"}, Tagging: new("t1=v1"), ChecksumCRC32: new("NhCmhg=="),
		})
		out := ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "dst", Key: "copy", CopySource: "src%2Fdir%2Fa%20b.txt"})
		if *out.CopyObjectResult.ETag != `"`+md5hex("hello")+`"` || out.CopyObjectResult.ChecksumCRC32 == nil || *out.CopyObjectResult.ChecksumCRC32 != "NhCmhg==" {
			t.Fatalf("CopyObject result = %+v", out.CopyObjectResult)
		}
		head := ok(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "dst", Key: "copy"})
		if *head.ContentType != "text/plain" || head.Metadata["owner"] != "probe" || head.TagCount == nil || *head.TagCount != 1 {
			t.Fatalf("COPY directive = %s %v tags %v; want the source's metadata and tags", *head.ContentType, head.Metadata, head.TagCount)
		}

		ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{
			Bucket: "dst", Key: "replaced", CopySource: "/src/dir/a b.txt",
			MetadataDirective: new(s3api.MetadataDirectiveReplace), TaggingDirective: new(s3api.TaggingDirectiveReplace), Tagging: new(""),
		})
		head = ok(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "dst", Key: "replaced"})
		if *head.ContentType != "binary/octet-stream" || len(head.Metadata) != 0 || head.TagCount != nil {
			t.Fatalf("REPLACE directives = %s %v tags %v; want the default type and nothing kept", *head.ContentType, head.Metadata, head.TagCount)
		}

		_, err := as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "dst", Key: "copy", CopySource: "dst/copy"})
		wantCode(t, err, "InvalidRequest", http.StatusBadRequest)
		ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "dst", Key: "copy", CopySource: "dst/copy", MetadataDirective: new(s3api.MetadataDirectiveReplace)})

		_, err = as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "dst", Key: "x", CopySource: "no-key"})
		wantCode(t, err, "InvalidArgument", http.StatusBadRequest)
		_, err = as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "dst", Key: "x", CopySource: "src/missing"})
		wantCode(t, err, "NoSuchKey", http.StatusNotFound)
		_, err = as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "dst", Key: "x", CopySource: "gone/k"})
		wantCode(t, err, "NoSuchBucket", http.StatusNotFound)
		source := ok(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "dst", Key: "copy"})
		_, err = as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "dst", Key: "x", CopySource: "dst/copy", CopySourceIfNoneMatch: source.ETag})
		if err == nil || err.ID.Status != http.StatusPreconditionFailed {
			t.Fatalf("copy-source If-None-Match on a matching ETag = %v, want 412", err)
		}
	})
}

// TestCopySourceKeyHoldingPercent: a source key holding '%' copies whether
// the caller sent it unencoded, as the Go SDK does, or encoded as %25, as
// botocore does (s3.md 5.1, wire-rest.md 3.3).
func TestCopySourceKeyHoldingPercent(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("percent")
		h.put("percent", "test%percent", "v")
		for dst, source := range map[string]string{"go": "/percent/test%percent", "botocore": "percent/test%25percent"} {
			ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "percent", Key: dst, CopySource: source})
			if got, _ := h.read(&s3api.GetObjectInput{Bucket: "percent", Key: dst}); got != "v" {
				t.Fatalf("copy from %q = %q, want the source's bytes", source, got)
			}
		}
	})
}

// TestCopySourceExpectedOwner: x-amz-source-expected-bucket-owner naming
// another account is 403 AccessDenied, as x-amz-expected-bucket-owner is
// for the destination; naming the owner copies.
func TestCopySourceExpectedOwner(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("owned")
		h.put("owned", "k", "v")
		_, err := as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "owned", Key: "c", CopySource: "owned/k", ExpectedSourceBucketOwner: new("111122223333")})
		wantCode(t, err, "AccessDenied", http.StatusForbidden)
		ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "owned", Key: "c", CopySource: "owned/k", ExpectedSourceBucketOwner: new(aws.DefaultAccount().String())})
	})
}

// TestCopyObjectRewrites covers how a copy rewrites what it copies (s3.md
// 5.7, 7): a ChecksumAlgorithm recomputes the checksum under the new
// algorithm; an SSE-KMS source is decrypted as the caller and its copy gets
// the MD5 ETag back; a KMS destination gets a random ETag and the key ARN;
// in place, a new storage class, website redirect or encryption is a
// change, and an ACL alone is not.
func TestCopyObjectRewrites(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("rewrites")
		h.put("rewrites", "plain", "hello")
		key := ok(h, who{}, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata
		ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{
			Bucket: "rewrites", Key: "sealed", Body: bytesBody("hello"),
			ServerSideEncryption: new(s3api.ServerSideEncryptionAwsKms), SSEKMSKeyId: new(key.KeyId),
		})
		helloMD5 := `"` + md5hex("hello") + `"`
		sha := sha256.Sum256([]byte("hello"))
		for _, tc := range []struct {
			name string
			in   s3api.CopyObjectInput
			// md5ETag says whether the copy's ETag is the MD5 of its bytes.
			md5ETag bool
			sse     s3api.ServerSideEncryption
			keyARN  string
			check   func(r *s3api.CopyObjectResult) bool
		}{
			{
				name: "checksum algorithm", in: s3api.CopyObjectInput{Key: "sha", CopySource: "rewrites/plain", ChecksumAlgorithm: new(s3api.ChecksumAlgorithmSha256)},
				md5ETag: true, sse: s3api.ServerSideEncryptionAes256,
				check: func(r *s3api.CopyObjectResult) bool {
					return r.ChecksumSHA256 != nil && *r.ChecksumSHA256 == base64.StdEncoding.EncodeToString(sha[:]) && *r.ChecksumType == s3api.ChecksumTypeFullObject
				},
			},
			{name: "KMS source", in: s3api.CopyObjectInput{Key: "unsealed", CopySource: "rewrites/sealed"}, md5ETag: true, sse: s3api.ServerSideEncryptionAes256},
			{
				name: "KMS destination", in: s3api.CopyObjectInput{Key: "resealed", CopySource: "rewrites/plain", ServerSideEncryption: new(s3api.ServerSideEncryptionAwsKms), SSEKMSKeyId: new(key.KeyId)},
				sse: s3api.ServerSideEncryptionAwsKms, keyARN: *key.Arn,
			},
			{name: "in place, storage class", in: s3api.CopyObjectInput{Key: "plain", CopySource: "rewrites/plain", StorageClass: new(s3api.StorageClassStandardIa)}, md5ETag: true, sse: s3api.ServerSideEncryptionAes256},
			{name: "in place, website redirect", in: s3api.CopyObjectInput{Key: "plain", CopySource: "rewrites/plain", WebsiteRedirectLocation: new("/elsewhere")}, md5ETag: true, sse: s3api.ServerSideEncryptionAes256},
			{
				name: "in place, KMS object to AES256", in: s3api.CopyObjectInput{Key: "sealed", CopySource: "rewrites/sealed", ServerSideEncryption: new(s3api.ServerSideEncryptionAes256)},
				md5ETag: true, sse: s3api.ServerSideEncryptionAes256,
			},
		} {
			in := tc.in
			in.Bucket = "rewrites"
			out, err := as(h, who{}, s3api.OpCopyObject, &in)
			if err != nil {
				t.Errorf("%s: CopyObject = %s %q", tc.name, err.ID.QueryCode, err.Message)
				continue
			}
			if got := *out.CopyObjectResult.ETag == helloMD5; got != tc.md5ETag {
				t.Errorf("%s: ETag %s is the MD5 = %v, want %v", tc.name, *out.CopyObjectResult.ETag, got, tc.md5ETag)
			}
			keyARN := ""
			if out.SSEKMSKeyId != nil {
				keyARN = *out.SSEKMSKeyId
			}
			if out.ServerSideEncryption == nil || *out.ServerSideEncryption != tc.sse || keyARN != tc.keyARN {
				t.Errorf("%s: encryption = %v key %q, want %s key %q", tc.name, out.ServerSideEncryption, keyARN, tc.sse, tc.keyARN)
			}
			if tc.check != nil && !tc.check(out.CopyObjectResult) {
				t.Errorf("%s: result = %+v", tc.name, out.CopyObjectResult)
			}
		}
		if !h.usedAsCaller("kms:Decrypt") {
			t.Error("copying an SSE-KMS source recorded no kms:Decrypt for the caller")
		}
		_, err := as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "rewrites", Key: "plain", CopySource: "rewrites/plain", ACL: new(s3api.ObjectCannedACLBucketOwnerFullControl)})
		wantCode(t, err, "InvalidRequest", http.StatusBadRequest)
	})
}

// TestCopyInPlaceAccepted: a copy onto itself that restates nothing else
// is still accepted when it restates the storage class, even the same
// one, when the bucket has a default encryption rule of its own, or when
// the object is SSE-KMS and the bucket default would re-encrypt it as
// AES256 (LocalStack aws_validated test_s3_copy_object_in_place_storage_class,
// test_copy_in_place_with_bucket_encryption and
// test_s3_copy_object_in_place_with_encryption).
func TestCopyInPlaceAccepted(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("plain")
		h.put("plain", "k", "hello")
		ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "plain", Key: "k", CopySource: "plain/k", StorageClass: new(s3api.StorageClassStandard)})

		key := ok(h, who{}, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata
		ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{
			Bucket: "plain", Key: "sealed", Body: bytesBody("hello"),
			ServerSideEncryption: new(s3api.ServerSideEncryptionAwsKms), SSEKMSKeyId: new(key.KeyId),
		})
		out := ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "plain", Key: "sealed", CopySource: "plain/sealed"})
		if *out.ServerSideEncryption != s3api.ServerSideEncryptionAes256 || out.SSEKMSKeyId != nil {
			t.Fatalf("in-place copy of an SSE-KMS object = %s key %v, want AES256 under the bucket default", *out.ServerSideEncryption, out.SSEKMSKeyId)
		}

		h.createBucket("encrypted")
		ok(h, who{}, s3api.OpPutBucketEncryption, &s3api.PutBucketEncryptionInput{
			Bucket: "encrypted",
			ServerSideEncryptionConfiguration: s3api.ServerSideEncryptionConfiguration{Rules: []s3api.ServerSideEncryptionRule{{
				ApplyServerSideEncryptionByDefault: &s3api.ServerSideEncryptionByDefault{SSEAlgorithm: s3api.ServerSideEncryptionAes256},
			}}},
		})
		h.put("encrypted", "k", "hello")
		ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "encrypted", Key: "k", CopySource: "encrypted/k"})

		_, err := as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "plain", Key: "k", CopySource: "plain/k"})
		wantCode(t, err, "InvalidRequest", http.StatusBadRequest)
	})
}

// usedAsCaller reports that the default account's root was recorded using
// action through a forward-access call it made, as S3 calls KMS.
func (h *harness) usedAsCaller(action string) bool {
	h.Helper()
	root, err := access.ParsePrincipal("arn:aws:iam::000000000000:root")
	if err != nil {
		h.Fatal(err)
	}
	service, _, _ := strings.Cut(action, ":")
	uses := h.Access.Usage(access.Filter{Principal: root, Service: service})
	for i := range uses {
		if _, ok := uses[i].Mode.(access.AsCaller); ok && uses[i].Action.String() == action {
			return true
		}
	}
	return false
}

// TestCopyKeepsSharedBytesCounted:a copy shares its source's blob and
// holds its own reference on it, so deleting the source keeps the copy's
// bytes, and deleting the copy too leaves nothing stored.
func TestCopyKeepsSharedBytesCounted(t *testing.T) {
	runObjectsWith(t, func(h *harness, blobs *blob.Store) {
		h.createBucket("shared")
		h.put("shared", "src", "payload")
		ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "shared", Key: "dst", CopySource: "shared/src"})
		ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "shared", Key: "src"})
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "shared", Key: "dst"}); got != "payload" {
			t.Fatalf("GetObject of the copy after its source was deleted = %q", got)
		}
		ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "shared", Key: "dst"})
		if refs := blobs.Refs(); len(refs) != 0 {
			t.Fatalf("blob store holds %v after both objects were deleted, want nothing", refs)
		}
	})
}
