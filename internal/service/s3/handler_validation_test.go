package s3_test

import (
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/checksum"
)

// TestGetObjectAttributes: the attributes asked for, with the ETag
// unquoted, the stored checksum and its type, the size and STANDARD named
// (s3.md 6.4, AWS-recorded test_get_object_attributes); an attribute not
// asked for is absent.
func TestGetObjectAttributes(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("bucket")
		const content = "attrs body"
		h.put("bucket", "k", content)
		got := ok(h, who{}, s3api.OpGetObjectAttributes, &s3api.GetObjectAttributesInput{
			Bucket: "bucket", Key: "k",
			ObjectAttributes: []s3api.ObjectAttributes{s3api.ObjectAttributesEtag, s3api.ObjectAttributesChecksum, s3api.ObjectAttributesObjectSize, s3api.ObjectAttributesStorageClass},
		})
		switch {
		case got.ETag == nil || *got.ETag != md5hex(content):
			t.Errorf("ETag = %v, want the unquoted MD5 %s", got.ETag, md5hex(content))
		case got.Checksum == nil || got.Checksum.ChecksumCRC64NVME == nil || *got.Checksum.ChecksumCRC64NVME != sumOf(t, checksum.CRC64NVME, []byte(content)) ||
			got.Checksum.ChecksumType == nil || *got.Checksum.ChecksumType != s3api.ChecksumTypeFullObject:
			t.Errorf("Checksum = %+v, want the CRC64NVME S3 computed, FULL_OBJECT", got.Checksum)
		case got.ObjectSize == nil || *got.ObjectSize != int64(len(content)):
			t.Errorf("ObjectSize = %v, want %d", got.ObjectSize, len(content))
		case got.StorageClass == nil || *got.StorageClass != s3api.StorageClassStandard:
			t.Errorf("StorageClass = %v, want STANDARD", got.StorageClass)
		case got.ObjectParts != nil || got.LastModified == nil:
			t.Errorf("ObjectParts = %+v, LastModified = %v; want none (a single-part object) and a time", got.ObjectParts, got.LastModified)
		}
		only := ok(h, who{}, s3api.OpGetObjectAttributes, &s3api.GetObjectAttributesInput{Bucket: "bucket", Key: "k", ObjectAttributes: []s3api.ObjectAttributes{s3api.ObjectAttributesObjectSize}})
		if only.ETag != nil || only.Checksum != nil || only.StorageClass != nil || only.ObjectSize == nil || *only.ObjectSize != int64(len(content)) {
			t.Errorf("only ObjectSize asked = %+v, want ObjectSize alone", only)
		}
		_, err := invoke(h, s3api.OpGetObjectAttributes, &s3api.GetObjectAttributesInput{Bucket: "bucket", Key: "missing", ObjectAttributes: []s3api.ObjectAttributes{s3api.ObjectAttributesEtag}})
		wantCode(t, err, "NoSuchKey", http.StatusNotFound)
	})
}

// TestHandlerOwnedValidationMessages: a copy source that is not bucket/key
// and an encoding-type other than url pass the generated validator and
// reach the handlers, which answer AWS's InvalidArgument texts (s3.md 7,
// 9.1) rather than the Coral text of the model's pattern and enum.
func TestHandlerOwnedValidationMessages(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("bucket")
		check := func(err *awserr.Error, message string, fields map[string]string) {
			t.Helper()
			wantCode(t, err, "InvalidArgument", http.StatusBadRequest)
			if err.Message != message {
				t.Errorf("message = %q, want %q", err.Message, message)
			}
			wantFields(t, err, fields)
		}
		_, err := invoke(h, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "bucket", Key: "dst", CopySource: "nobucketslash"})
		check(err, "Invalid copy source object key", map[string]string{"ArgumentName": "x-amz-copy-source", "ArgumentValue": "nobucketslash"})

		bogus := new(s3api.EncodingType("bogus"))
		encoding := map[string]string{"ArgumentName": "encoding-type", "ArgumentValue": "bogus"}
		const invalidEncoding = "Invalid Encoding Method specified in Request"
		_, err = invoke(h, s3api.OpListObjectsV2, &s3api.ListObjectsV2Input{Bucket: "bucket", EncodingType: bogus})
		check(err, invalidEncoding, encoding)
		_, err = invoke(h, s3api.OpListObjects, &s3api.ListObjectsInput{Bucket: "bucket", EncodingType: bogus})
		check(err, invalidEncoding, encoding)
		_, err = invoke(h, s3api.OpListObjectVersions, &s3api.ListObjectVersionsInput{Bucket: "bucket", EncodingType: bogus})
		check(err, invalidEncoding, encoding)
		_, err = invoke(h, s3api.OpListMultipartUploads, &s3api.ListMultipartUploadsInput{Bucket: "bucket", EncodingType: bogus})
		check(err, invalidEncoding, encoding)
	})
}
