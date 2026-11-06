package s3_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// The golden cases below replay ten of LocalStack's AWS-validated S3
// snapshots (third-party-recorded, s3-ssec-object-lock.md 1). Each cites
// localstack@8b9a79f <path>:L<n>, the line of the snapshot entry, and the
// test's last_validated_date in the sibling *.validation.json; each asserts
// the recorded status, code, message and elements, whose order the
// snapshots do not record. The SSE-C cases ran on buckets that allowed
// SSE-C, before AWS blocked it on new buckets in April 2026
// (s3-ssec-object-lock.md 1, 4.1), so they unblock first; and SSE-C is
// sent over HTTPS, as every AWS recording was (3.9).

// goldenSSEC is a bucket that allows SSE-C, as LocalStack's
// TestS3SSECEncryption buckets did.
func goldenSSEC(h *harness) {
	h.Helper()
	h.createBucket("golden")
	h.allowSSEC("golden")
}

// TestGoldenPutObjectSSECValidation replays test_put_object_validation_sse_c
// (localstack@8b9a79f tests/aws/services/s3/test_s3.py:L12117, inputs at
// L12122-L12202; last_validated_date 2026-02-21T00:36:34Z).
func TestGoldenPutObjectSSECValidation(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		goldenSSEC(h)
		put := func(sse *s3api.ServerSideEncryption, alg, k, m *string) *awserr.Error {
			_, err := as(h, overTLS, s3api.OpPutObject, &s3api.PutObjectInput{
				Bucket: "golden", Key: "test-sse-c", Body: bytesBody("test_data"),
				ServerSideEncryption: sse, SSECustomerAlgorithm: alg, SSECustomerKey: k, SSECustomerKeyMD5: m,
			})
			return err
		}
		// localstack@8b9a79f tests/aws/services/s3/test_s3.snapshot.json:L12706
		failure{
			status: 400, code: "InvalidArgument", message: "Server Side Encryption with Customer provided key is incompatible with the encryption method specified",
			fields: map[string]string{"ArgumentName": "x-amz-server-side-encryption", "ArgumentValue": "AES256"},
		}.is(t, "put-obj-sse-c-both-encryption", put(new(s3api.ServerSideEncryptionAes256), new("KMS"), key1.k(), key1.m()))
		// localstack@8b9a79f tests/aws/services/s3/test_s3.snapshot.json:L12718
		failure{
			status: 400, code: "InvalidEncryptionAlgorithmError", message: "The Encryption request you specified is not valid. Supported value: AES256.",
			fields: map[string]string{"ArgumentName": "x-amz-server-side-encryption", "ArgumentValue": "KMS"},
		}.is(t, "put-obj-sse-c-wrong-algo", put(nil, new("KMS"), key1.k(), key1.m()))
		// localstack@8b9a79f tests/aws/services/s3/test_s3.snapshot.json:L12741
		failure{
			status: 400, code: "InvalidArgument", message: "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key.",
			fields: map[string]string{"ArgumentName": "x-amz-server-side-encryption"},
		}.is(t, "put-obj-sse-c-no-key", put(nil, key1.alg(), nil, key1.m()))
		// localstack@8b9a79f tests/aws/services/s3/test_s3.snapshot.json:L12774;
		// the MD5 with its first character replaced (test_s3.py:L12191-L12192).
		bad := "a" + key1.md5[1:]
		failure{
			status: 400, code: "InvalidArgument", message: "The calculated MD5 hash of the key did not match the hash that was provided.",
			fields: map[string]string{"ArgumentName": "x-amz-server-side-encryption"},
		}.is(t, "put-obj-sse-c-bad-md5", put(nil, key1.alg(), key1.k(), &bad))
	})
}

// TestGoldenGetObjectSSECRetrieval replays test_object_retrieval_sse_c
// (localstack@8b9a79f tests/aws/services/s3/test_s3.py:L12204;
// last_validated_date 2026-02-21T00:36:39Z).
func TestGoldenGetObjectSSECRetrieval(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		goldenSSEC(h)
		out := h.mustPutSSEC("golden", "test-sse-c", "test_data", key1)
		// localstack@8b9a79f tests/aws/services/s3/test_s3.snapshot.json:L12790-L12795
		if deref(out.SSECustomerAlgorithm) != "AES256" || deref(out.SSECustomerKeyMD5) != "JMwgiexXqwuPqIPjYFmIZQ==" || out.ServerSideEncryption != nil {
			t.Errorf("put-obj-sse-c = algorithm %v key MD5 %v SSE %v, want AES256, JMwgiexXqwuPqIPjYFmIZQ== and no SSE", deref(out.SSECustomerAlgorithm), deref(out.SSECustomerKeyMD5), deref(out.ServerSideEncryption))
		}
		get := func(c *customer) *awserr.Error {
			in := &s3api.GetObjectInput{Bucket: "golden", Key: "test-sse-c"}
			if c != nil {
				in.SSECustomerAlgorithm, in.SSECustomerKey, in.SSECustomerKeyMD5 = c.alg(), c.k(), c.m()
			}
			_, err := as(h, overTLS, s3api.OpGetObject, in)
			return err
		}
		// localstack@8b9a79f tests/aws/services/s3/test_s3.snapshot.json:L12801
		failure{
			status: 400, code: "InvalidRequest",
			message: "The object was stored using a form of Server Side Encryption. The correct parameters must be provided to retrieve the object.",
		}.is(t, "get-obj-no-sse-c", get(nil))
		// localstack@8b9a79f tests/aws/services/s3/test_s3.snapshot.json:L12811
		failure{
			status: 403, code: "AccessDenied", message: "Requests specifying Server Side Encryption with Customer provided keys must provide the correct secret key.",
		}.is(t, "get-obj-sse-c-wrong-key", get(&key2))
	})
}

// TestGoldenObjectRetentionExceptions replays test_s3_object_retention_exc
// (localstack@8b9a79f tests/aws/services/s3/test_s3.py:L9688; recorded
// 2026-02-21T00:33:51Z, last_validated_date 2026-02-21T00:33:53Z). The
// emulator clock is set to the recording's date, so the test's
// current_year + 5 and its 2020 date stand where they stood.
func TestGoldenObjectRetentionExceptions(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		if _, err := h.Clock.AdvanceTo(clock.FromWire(time.Date(2026, 2, 21, 0, 33, 51, 0, time.UTC))); err != nil {
			t.Fatal(err)
		}
		h.lockBucket("golden")
		v := h.mustPutLocked("golden", "test-key", "test", s3api.PutObjectInput{})
		future := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
		gov := s3api.ObjectLockRetentionModeGovernance
		if err := h.putRetention("golden", "test-key", nil, gov, future, nil); err != nil {
			t.Fatalf("set a retention: %s %q", err.ID.QueryCode, err.Message)
		}
		// localstack@8b9a79f tests/aws/services/s3/test_s3.snapshot.json:L9478
		failure{status: 403, code: "AccessDenied", message: "Access Denied because object protected by object lock."}.
			is(t, "update-retention-no-bypass", h.putRetention("golden", "test-key", v, gov, future.AddDate(0, 0, -365), nil))
		// localstack@8b9a79f tests/aws/services/s3/test_s3.snapshot.json:L9488
		failure{
			status: 400, code: "InvalidArgument", message: "The retain until date must be in the future!",
			fields: map[string]string{"ArgumentName": "RetainUntilDate", "ArgumentValue": "Tue Dec 31 16:00:00 PST 2019"},
		}.is(t, "update-retention-past-date", h.putRetention("golden", "test-key", v, gov, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), nil))
	})
}

// TestGoldenDeleteObjectsLocked replays test_delete_locked_object
// (localstack@8b9a79f tests/aws/services/s3/test_s3.py:L10310;
// last_validated_date 2026-02-21T00:34:45Z): a version under a legal hold
// is reported per key in a 200.
func TestGoldenDeleteObjectsLocked(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("golden")
		v := h.mustPutLocked("golden", "test-delete-locked", "test", s3api.PutObjectInput{})
		if err := h.putHold("golden", "test-delete-locked", v, s3api.ObjectLockLegalHoldStatusOn); err != nil {
			t.Fatalf("put-object-legal-hold: %s %q", err.ID.QueryCode, err.Message)
		}
		out := ok(h, who{}, s3api.OpDeleteObjects, &s3api.DeleteObjectsInput{Bucket: "golden", Delete: s3api.Delete{
			Objects: []s3api.ObjectIdentifier{{Key: "test-delete-locked", VersionId: v}},
		}})
		// localstack@8b9a79f tests/aws/services/s3/test_s3.snapshot.json:L10054-L10066
		if len(out.Errors) != 1 || len(out.Deleted) != 0 {
			t.Fatalf("delete-objects-locked = errors %+v deleted %+v, want one error", out.Errors, out.Deleted)
		}
		e := out.Errors[0]
		if str(e.Code) != "AccessDenied" || str(e.Key) != "test-delete-locked" || str(e.Message) != "Access Denied because object protected by object lock." || str(e.VersionId) != *v {
			t.Errorf("delete-objects-locked error = %s %s %q %s, want AccessDenied test-delete-locked %q %s",
				str(e.Code), str(e.Key), str(e.Message), str(e.VersionId), "Access Denied because object protected by object lock.", *v)
		}
	})
}

// TestGoldenBypassWithoutObjectLock replays test_delete_object_with_no_locking
// (localstack@8b9a79f tests/aws/services/s3/test_s3_api.py:L2345, snapshot
// test_s3_api.snapshot.json:L3532; last_validated_date
// 2026-02-21T01:04:11Z).
func TestGoldenBypassWithoutObjectLock(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("golden")
		h.put("golden", "test-key", "test")
		_, err := h.deleteVersion("golden", "test-key", nil, new(true))
		// localstack@8b9a79f tests/aws/services/s3/test_s3_api.snapshot.json:L3535
		failure{
			status: 400, code: "InvalidArgument", message: "x-amz-bypass-governance-retention is only applicable to Object Lock enabled buckets.",
			fields: map[string]string{"ArgumentName": "x-amz-bypass-governance-retention"},
		}.is(t, "delete-object-bypass-no-lock", err)
	})
}
