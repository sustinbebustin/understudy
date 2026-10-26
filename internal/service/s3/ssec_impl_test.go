package s3_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// TestSSECReadWithoutKeyBeforeConditionsAndRange: a read of an SSE-C
// version without its key is refused before If-None-Match or Range is
// judged, so no 304 or 416 reports the version's ETag or size
// (s3-ssec-object-lock.md 11.7: never return an SSE-C object's metadata
// without the key; the order against the conditions is inferred).
func TestSSECReadWithoutKeyBeforeConditionsAndRange(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("ssec")
		h.allowSSEC("ssec")
		out := h.mustPutSSEC("ssec", "enc", "test_data", key1)
		needsKey := failure{status: 400, code: "InvalidRequest", message: textNeedsKey}
		_, err := as(h, overTLS, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "ssec", Key: "enc", IfNoneMatch: out.ETag})
		needsKey.is(t, "GET with a matching If-None-Match and no key", err)
		_, err = as(h, overTLS, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "ssec", Key: "enc", Range: new("bytes=100-200")})
		wantCode(t, err, "InvalidRequest", 400)
		got, _ := h.readAs(overTLS, &s3api.GetObjectInput{Bucket: "ssec", Key: "enc", Range: new("bytes=0-3"), SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		if got != "test" {
			t.Errorf("ranged GET with the key = %q, want %q", got, "test")
		}
	})
}

// TestPlainReadsReportNoCustomerKey: a read of an object that is not
// SSE-C reports x-amz-server-side-encryption and no SSE-C pair
// (s3-ssec-object-lock.md 3.4).
func TestPlainReadsReportNoCustomerKey(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("plain")
		h.put("plain", "k", "v")
		head := ok(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "plain", Key: "k"})
		if head.SSECustomerAlgorithm != nil || head.SSECustomerKeyMD5 != nil || head.ServerSideEncryption == nil {
			t.Errorf("HEAD of a plain object = SSE %v, SSE-C %v %v; want AES256 and no SSE-C", deref(head.ServerSideEncryption), deref(head.SSECustomerAlgorithm), deref(head.SSECustomerKeyMD5))
		}
	})
}

// TestSSECCopySourceOverPlainHTTP: copy-source SSE-C headers are a read,
// so over plain HTTP they are refused as a read's are (switch
// s3.sse-c.plain-http, reads included), on CopyObject and UploadPartCopy.
func TestSSECCopySourceOverPlainHTTP(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.follows("s3.sse-c.plain-http", "refused")
		insecure := failure{status: 400, code: "InvalidRequest", message: textInsecure}
		h.createBucket("ssec")
		h.allowSSEC("ssec")
		h.mustPutSSEC("ssec", "enc", "test_data", key1)
		_, err := as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{
			Bucket: "ssec", Key: "copy", CopySource: "ssec/enc",
			CopySourceSSECustomerAlgorithm: key1.alg(), CopySourceSSECustomerKey: key1.k(), CopySourceSSECustomerKeyMD5: key1.m(),
		})
		insecure.is(t, "CopyObject with copy-source SSE-C headers over HTTP", err)
		id := h.startUpload("ssec", "parts", s3api.CreateMultipartUploadInput{})
		_, err = as(h, who{}, s3api.OpUploadPartCopy, &s3api.UploadPartCopyInput{
			Bucket: "ssec", Key: "parts", UploadId: id, PartNumber: 1, CopySource: "ssec/enc",
			CopySourceSSECustomerAlgorithm: key1.alg(), CopySourceSSECustomerKey: key1.k(), CopySourceSSECustomerKeyMD5: key1.m(),
		})
		insecure.is(t, "UploadPartCopy with copy-source SSE-C headers over HTTP", err)
	})
}

// TestSSECKMSKeyIDIsRefused: a KMS key id beside SSE-C headers is refused
// as a key id without aws:kms is (AWS's answer is UNVERIFIED), and nothing
// is written.
func TestSSECKMSKeyIDIsRefused(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("ssec")
		h.allowSSEC("ssec")
		_, err := as(h, overTLS, s3api.OpPutObject, &s3api.PutObjectInput{
			Bucket: "ssec", Key: "k", Body: bytesBody("v"), SSEKMSKeyId: new("alias/x"),
			SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m(),
		})
		failure{
			status: 400, code: "InvalidArgument",
			message: "Server Side Encryption with AWS KMS managed key requires HTTP header x-amz-server-side-encryption : aws:kms",
			fields:  map[string]string{"ArgumentName": sseArgument, "ArgumentValue": "AES256"},
		}.is(t, "SSE-C with a KMS key id", err)
		if _, err := as(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "ssec", Key: "k"}); err == nil {
			t.Error("a refused SSE-C write left an object")
		}
	})
}

// TestSSECSelfCopyChangesEncryption: copying an SSE-C object onto itself
// with only its source key re-encrypts it under SSE-S3, which counts as a
// change (s3.md 7, as for an SSE-KMS object), and gives it the plaintext
// MD5 ETag (LocalStack test_s3.snapshot.json L12913).
func TestSSECSelfCopyChangesEncryption(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("ssec")
		h.allowSSEC("ssec")
		const content = "test_data"
		h.mustPutSSEC("ssec", "k", content, key1)
		out, err := as(h, overTLS, s3api.OpCopyObject, &s3api.CopyObjectInput{
			Bucket: "ssec", Key: "k", CopySource: "ssec/k",
			CopySourceSSECustomerAlgorithm: key1.alg(), CopySourceSSECustomerKey: key1.k(), CopySourceSSECustomerKeyMD5: key1.m(),
		})
		if err != nil {
			t.Fatalf("self-copy out of SSE-C: %s %q", err.ID.QueryCode, err.Message)
		}
		if out.CopyObjectResult == nil || deref(out.CopyObjectResult.ETag) != `"`+md5hex(content)+`"` {
			t.Errorf("self-copy ETag = %v, want the plaintext MD5", out.CopyObjectResult)
		}
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "ssec", Key: "k"}); got != content {
			t.Errorf("GET without a key after the self-copy = %q, want %q", got, content)
		}
	})
}

// TestSSECCompleteHeaders: under switch s3.sse-c.complete-headers, SSE-C
// headers sent on CompleteMultipartUpload are judged as UploadPart's are:
// over plain HTTP they are refused, and on an upload that is not SSE-C they
// get the initiate text (LocalStack test_s3.snapshot.json L13133); the
// upload is still there afterwards. ListParts ignores them.
func TestSSECCompleteHeaders(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.follows("s3.sse-c.complete-headers", "checked-when-sent")
		h.createBucket("mpu")
		h.allowSSEC("mpu")
		ssecID := h.startUploadAs(overTLS, "mpu", "enc", s3api.CreateMultipartUploadInput{SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		part := ok(h, overTLS, s3api.OpUploadPart, &s3api.UploadPartInput{
			Bucket: "mpu", Key: "enc", UploadId: ssecID, PartNumber: 1, Body: bytesBody("only"),
			SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m(),
		})
		complete := func(w who, key, id string, etag *string, c *customer) *awserr.Error {
			in := &s3api.CompleteMultipartUploadInput{Bucket: "mpu", Key: key, UploadId: id, MultipartUpload: &s3api.CompletedMultipartUpload{
				Parts: []s3api.CompletedPart{{PartNumber: new(int32(1)), ETag: etag}},
			}}
			if c != nil {
				in.SSECustomerAlgorithm, in.SSECustomerKey, in.SSECustomerKeyMD5 = c.alg(), c.k(), c.m()
			}
			_, err := as(h, w, s3api.OpCompleteMultipartUpload, in)
			return err
		}
		failure{status: 400, code: "InvalidRequest", message: textInsecure}.is(t, "Complete with the key over HTTP", complete(who{}, "enc", ssecID, part.ETag, &key1))
		listed := ok(h, overTLS, s3api.OpListParts, &s3api.ListPartsInput{Bucket: "mpu", Key: "enc", UploadId: ssecID, SSECustomerAlgorithm: key2.alg(), SSECustomerKey: key2.k(), SSECustomerKeyMD5: key2.m()})
		if len(listed.Parts) != 1 {
			t.Errorf("ListParts with another key lists %d parts, want 1", len(listed.Parts))
		}

		plainID := h.startUpload("mpu", "plain", s3api.CreateMultipartUploadInput{})
		plainPart := ok(h, who{}, s3api.OpUploadPart, &s3api.UploadPartInput{Bucket: "mpu", Key: "plain", UploadId: plainID, PartNumber: 1, Body: bytesBody("only")})
		failure{status: 400, code: "InvalidRequest", message: textInitiate}.is(t, "Complete of a plain upload with SSE-C headers", complete(overTLS, "plain", plainID, plainPart.ETag, &key1))
		if err := complete(overTLS, "plain", plainID, plainPart.ETag, nil); err != nil {
			t.Errorf("Complete of the plain upload without headers after the refusal: %s %q", err.ID.QueryCode, err.Message)
		}
		if err := complete(overTLS, "enc", ssecID, part.ETag, &key1); err != nil {
			t.Errorf("Complete of the SSE-C upload with its key: %s %q", err.ID.QueryCode, err.Message)
		}
	})
}

// TestSSECBucketInMiddleEastReadsUnblocked: a new me-south-1 bucket allows
// SSE-C (s3-ssec-object-lock.md 4.1), and GetBucketEncryption reads that
// back in the unblocked form (switch s3.encryption.unblocked-read).
func TestSSECBucketInMiddleEastReadsUnblocked(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.follows("s3.encryption.unblocked-read", "NONE-element")
		ok(h, signed("me-south-1"), s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "bahrain", CreateBucketConfiguration: &s3api.CreateBucketConfiguration{
			LocationConstraint: new(s3api.BucketLocationConstraint("me-south-1")),
		}})
		out := ok(h, signed("me-south-1"), s3api.OpGetBucketEncryption, &s3api.GetBucketEncryptionInput{Bucket: "bahrain"})
		want := s3api.ServerSideEncryptionRule{
			ApplyServerSideEncryptionByDefault: &s3api.ServerSideEncryptionByDefault{SSEAlgorithm: s3api.ServerSideEncryptionAes256},
			BucketKeyEnabled:                   new(false),
			BlockedEncryptionTypes:             &s3api.BlockedEncryptionTypes{EncryptionType: []s3api.EncryptionType{s3api.EncryptionTypeNone}},
		}
		if out.ServerSideEncryptionConfiguration == nil || len(out.ServerSideEncryptionConfiguration.Rules) != 1 || !ruleEqual(out.ServerSideEncryptionConfiguration.Rules[0], want) {
			t.Errorf("GetBucketEncryption of a new me-south-1 bucket = %+v, want %s", out.ServerSideEncryptionConfiguration, ruleString(want))
		}
	})
}
