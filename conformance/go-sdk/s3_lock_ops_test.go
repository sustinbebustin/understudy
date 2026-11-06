package gosdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Research: docs/research/s3-ssec-object-lock.md. Lines cite LocalStack's
// AWS-validated snapshot test_s3.snapshot.json (localstack@8b9a79f,
// validated 2026-02-21); behavior AWS has not been recorded doing names the
// fidelity switch whose declared value the test reads from understudy's
// coverage report. These cases cover what s3_lock_test.go does not: SSE-C
// copies and multipart uploads, and the retention operations.

// TestS3SSECCopyAndMultipart: over HTTPS, a copy of an SSE-C source needs
// the source key (L12950); a copy to an SSE-S3 target gets the plaintext MD5
// ETag and AES256 (L12913), and one to an SSE-C target is read with the
// target's key (L12926). An SSE-C upload's parts must carry the upload's key
// (L13154, L13164), and its part and object ETags are not MD5s
// (s3-ssec-object-lock.md 3.4).
func TestS3SSECCopyAndMultipart(t *testing.T) {
	t.Parallel()
	if tlsEndpoint == nil {
		t.Skip(tlsEndpointEnv + " is not set; run.sh sets it to understudy's TLS listener")
	}
	ctx := t.Context()
	client := s3Client(func(o *s3.Options) { o.BaseEndpoint = aws.String(tlsEndpoint.String()) })
	bucket := createBucket(t, client)
	allowSSEC(t, client, bucket)
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("src"), Body: strings.NewReader("test_data"),
		SSECustomerAlgorithm: aws.String("AES256"), SSECustomerKey: aws.String(ssecKey1.key), SSECustomerKeyMD5: aws.String(ssecKey1.md5),
	})
	ok(t, err, "SSE-C PutObject")
	source := aws.String(bucket + "/src")

	t.Run("copy", func(t *testing.T) {
		_, err := client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String(bucket), Key: aws.String("no-key"), CopySource: source})
		apiErr(t, err, "CopyObject of an SSE-C source without its key", "InvalidRequest")
		wantMessage(t, err, "CopyObject of an SSE-C source without its key", "The object was stored using a form of Server Side Encryption. The correct parameters must be provided to retrieve the object.")

		plain, err := client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket: aws.String(bucket), Key: aws.String("plain"), CopySource: source,
			CopySourceSSECustomerAlgorithm: aws.String("AES256"), CopySourceSSECustomerKey: aws.String(ssecKey1.key), CopySourceSSECustomerKeyMD5: aws.String(ssecKey1.md5),
		})
		ok(t, err, "CopyObject of an SSE-C source to an SSE-S3 target")
		if got := aws.ToString(plain.CopyObjectResult.ETag); got != quotedMD5([]byte("test_data")) || plain.ServerSideEncryption != types.ServerSideEncryptionAes256 {
			t.Errorf("CopyObject to SSE-S3 = ETag %s SSE %q, want %s and AES256", got, plain.ServerSideEncryption, quotedMD5([]byte("test_data")))
		}
		if got := getBody(t, client, bucket, "plain"); got != "test_data" {
			t.Errorf("GetObject of the SSE-S3 copy = %q, want test_data", got)
		}

		rekeyed, err := client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket: aws.String(bucket), Key: aws.String("rekeyed"), CopySource: source,
			CopySourceSSECustomerAlgorithm: aws.String("AES256"), CopySourceSSECustomerKey: aws.String(ssecKey1.key), CopySourceSSECustomerKeyMD5: aws.String(ssecKey1.md5),
			SSECustomerAlgorithm: aws.String("AES256"), SSECustomerKey: aws.String(ssecKey2.key), SSECustomerKeyMD5: aws.String(ssecKey2.md5),
		})
		ok(t, err, "CopyObject of an SSE-C source to an SSE-C target with another key")
		if aws.ToString(rekeyed.SSECustomerKeyMD5) != ssecKey2.md5 || rekeyed.ServerSideEncryption != "" {
			t.Errorf("CopyObject to SSE-C = key MD5 %q SSE %q, want %s and no SSE", aws.ToString(rekeyed.SSECustomerKeyMD5), rekeyed.ServerSideEncryption, ssecKey2.md5)
		}
		get, err := client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket), Key: aws.String("rekeyed"),
			SSECustomerAlgorithm: aws.String("AES256"), SSECustomerKey: aws.String(ssecKey2.key), SSECustomerKeyMD5: aws.String(ssecKey2.md5),
		})
		ok(t, err, "GetObject of the SSE-C copy with the target's key")
		if got := string(readAll(t, get.Body, "the SSE-C copy")); got != "test_data" {
			t.Errorf("GetObject of the SSE-C copy = %q, want test_data", got)
		}
		_, err = client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket), Key: aws.String("rekeyed"),
			SSECustomerAlgorithm: aws.String("AES256"), SSECustomerKey: aws.String(ssecKey1.key), SSECustomerKeyMD5: aws.String(ssecKey1.md5),
		})
		apiErr(t, err, "GetObject of the SSE-C copy with the source's key", "AccessDenied")
	})

	t.Run("multipart", func(t *testing.T) {
		create, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
			Bucket: aws.String(bucket), Key: aws.String("mpu"),
			SSECustomerAlgorithm: aws.String("AES256"), SSECustomerKey: aws.String(ssecKey1.key), SSECustomerKeyMD5: aws.String(ssecKey1.md5),
		})
		ok(t, err, "SSE-C CreateMultipartUpload")
		if aws.ToString(create.SSECustomerKeyMD5) != ssecKey1.md5 {
			t.Errorf("CreateMultipartUpload key MD5 = %q, want %s", aws.ToString(create.SSECustomerKeyMD5), ssecKey1.md5)
		}
		part := func(k *ssecKey) (*s3.UploadPartOutput, error) {
			in := &s3.UploadPartInput{Bucket: aws.String(bucket), Key: aws.String("mpu"), UploadId: create.UploadId, PartNumber: aws.Int32(1), Body: strings.NewReader("part")}
			if k != nil {
				in.SSECustomerAlgorithm, in.SSECustomerKey, in.SSECustomerKeyMD5 = aws.String("AES256"), aws.String(k.key), aws.String(k.md5)
			}
			return client.UploadPart(ctx, in)
		}
		_, err = part(nil)
		apiErr(t, err, "UploadPart without the upload's key", "InvalidRequest")
		wantMessage(t, err, "UploadPart without the upload's key", "The multipart upload initiate requested encryption. Subsequent part requests must include the appropriate encryption parameters.")
		_, err = part(&ssecKey2)
		apiErr(t, err, "UploadPart with another key", "InvalidRequest")
		wantMessage(t, err, "UploadPart with another key", "The provided encryption parameters did not match the ones used originally.")
		p1, err := part(&ssecKey1)
		ok(t, err, "UploadPart with the upload's key")
		if aws.ToString(p1.ETag) == quotedMD5([]byte("part")) || aws.ToString(p1.SSECustomerKeyMD5) != ssecKey1.md5 {
			t.Errorf("UploadPart = ETag %s key MD5 %q, want a non-MD5 ETag and %s", aws.ToString(p1.ETag), aws.ToString(p1.SSECustomerKeyMD5), ssecKey1.md5)
		}
		// The upload declared no checksum algorithm, so the part is listed
		// without one: S3 refuses a restated part checksum there (s3.md 6.4).
		done, err := client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
			Bucket: aws.String(bucket), Key: aws.String("mpu"), UploadId: create.UploadId,
			MultipartUpload: &types.CompletedMultipartUpload{Parts: []types.CompletedPart{{PartNumber: aws.Int32(1), ETag: p1.ETag}}},
		})
		ok(t, err, "CompleteMultipartUpload without SSE-C headers (L13083)")
		if etag := aws.ToString(done.ETag); !strings.HasSuffix(etag, `-1"`) || etag == multipartETag([][]byte{[]byte("part")}) {
			t.Errorf("CompleteMultipartUpload ETag = %s, want a non-MD5 ETag ending -1", etag)
		}
		get, err := client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket), Key: aws.String("mpu"),
			SSECustomerAlgorithm: aws.String("AES256"), SSECustomerKey: aws.String(ssecKey1.key), SSECustomerKeyMD5: aws.String(ssecKey1.md5),
		})
		ok(t, err, "GetObject of the SSE-C multipart object")
		if got := string(readAll(t, get.Body, "the SSE-C multipart object")); got != "part" {
			t.Errorf("GetObject of the SSE-C multipart object = %q, want part", got)
		}
	})
}

// TestS3ObjectRetention: PutObjectRetention sets retention on an unprotected
// version (L9607) and GetObjectRetention reads it back with its
// milliseconds (switch s3.object-lock.retain-until-format); GOVERNANCE is
// extended without bypass (L9871) but shortened only with it (L9478); a
// version that never had retention is 404 (L9458); a bucket without Object
// Lock is refused (L9510); and DeleteObjects with the bypass header removes
// a GOVERNANCE version (s3-ssec-object-lock.md 6.6, 7.2).
func TestS3ObjectRetention(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := s3Client()
	bucket := lockBucket(t, client)
	put, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v")})
	ok(t, err, "PutObject")
	version := put.VersionId

	_, err = client.GetObjectRetention(ctx, &s3.GetObjectRetentionInput{Bucket: aws.String(bucket), Key: aws.String("k"), VersionId: version})
	apiErr(t, err, "GetObjectRetention before any retention", "NoSuchObjectLockConfiguration")
	wantStatus(t, err, "GetObjectRetention before any retention", 404)

	retain := func(until time.Time, bypass bool) error {
		in := &s3.PutObjectRetentionInput{
			Bucket: aws.String(bucket), Key: aws.String("k"), VersionId: version,
			Retention: &types.ObjectLockRetention{Mode: types.ObjectLockRetentionModeGovernance, RetainUntilDate: aws.Time(until)},
		}
		if bypass {
			in.BypassGovernanceRetention = aws.Bool(true)
		}
		_, err := client.PutObjectRetention(ctx, in)
		return err
	}
	until := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Add(123 * time.Millisecond)
	ok(t, retain(until, false), "PutObjectRetention GOVERNANCE")
	// GetObjectRetention's body always carries milliseconds
	// (testdata/parity/s3/S3SOL/12-get-retention.json); the header form
	// S3SOL-12 recorded trimmed is not read here.
	if v := switchValue(t, "s3.object-lock.retain-until-format"); v != "milliseconds-header-trimmed" {
		t.Fatalf("switch s3.object-lock.retain-until-format declares %q; this test checks %q", v, "milliseconds-header-trimmed")
	}
	got, err := client.GetObjectRetention(ctx, &s3.GetObjectRetentionInput{Bucket: aws.String(bucket), Key: aws.String("k"), VersionId: version})
	ok(t, err, "GetObjectRetention")
	if got.Retention == nil || got.Retention.Mode != types.ObjectLockRetentionModeGovernance || !aws.ToTime(got.Retention.RetainUntilDate).Equal(until) {
		t.Errorf("GetObjectRetention = %+v, want GOVERNANCE until %v", got.Retention, until)
	}

	ok(t, retain(until.Add(time.Hour), false), "PutObjectRetention extending GOVERNANCE without bypass")
	err = retain(until, false)
	apiErr(t, err, "PutObjectRetention shortening GOVERNANCE without bypass", "AccessDenied")
	wantMessage(t, err, "PutObjectRetention shortening GOVERNANCE without bypass", lockedText)
	ok(t, retain(until, true), "PutObjectRetention shortening GOVERNANCE with bypass")

	batch, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket), BypassGovernanceRetention: aws.Bool(true),
		Delete: &types.Delete{Objects: []types.ObjectIdentifier{{Key: aws.String("k"), VersionId: version}}},
	})
	ok(t, err, "DeleteObjects with the GOVERNANCE bypass")
	if len(batch.Errors) != 0 || len(batch.Deleted) != 1 || aws.ToString(batch.Deleted[0].VersionId) != aws.ToString(version) {
		t.Errorf("DeleteObjects with bypass = deleted %+v errors %+v, want version %s deleted", batch.Deleted, batch.Errors, aws.ToString(version))
	}

	plain := createBucket(t, client)
	putObject(t, client, plain, "k", "v")
	_, err = client.PutObjectRetention(ctx, &s3.PutObjectRetentionInput{
		Bucket: aws.String(plain), Key: aws.String("k"),
		Retention: &types.ObjectLockRetention{Mode: types.ObjectLockRetentionModeGovernance, RetainUntilDate: aws.Time(until)},
	})
	apiErr(t, err, "PutObjectRetention on a bucket without Object Lock", "InvalidRequest")
	wantMessage(t, err, "PutObjectRetention on a bucket without Object Lock", "Bucket is missing Object Lock Configuration")
}
