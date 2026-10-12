package s3

import (
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// getObjectAttributes answers the attributes the request names of the
// version it addresses (s3.md 6.4, AWS-recorded test_get_object_attributes):
// the ETag without quotes, the stored checksum with its type, the part
// count of a multipart object, the size and the storage class, STANDARD
// included; Last-Modified and the version id always.
func getObjectAttributes(in *s3api.GetObjectAttributesInput, b *Bucket) (*s3api.GetObjectAttributesOutput, *awserr.Error) {
	if customerKeyed(in.SSECustomerAlgorithm, in.SSECustomerKey, in.SSECustomerKeyMD5) {
		return nil, errSSECNotApplicable()
	}
	v, err := findVersion(b, in.Key, in.VersionId, "GET")
	if err != nil {
		return nil, err
	}
	o := v.Object
	out := &s3api.GetObjectAttributesOutput{
		LastModified: new(v.Modified.Wire().UTC().Truncate(time.Second)),
		VersionId:    reportedVersion(b, v.ID),
	}
	wants := func(a s3api.ObjectAttributes) bool { return slices.Contains(in.ObjectAttributes, a) }
	if wants(s3api.ObjectAttributesEtag) {
		out.ETag = new(o.ETag)
	}
	if wants(s3api.ObjectAttributesChecksum) && o.Checksum.Value != "" {
		m := membersOf(o.Checksum)
		out.Checksum = &s3api.Checksum{
			ChecksumCRC32: m[0], ChecksumCRC32C: m[1], ChecksumCRC64NVME: m[2], ChecksumSHA1: m[3], ChecksumSHA256: m[4],
			ChecksumSHA512: m[5], ChecksumMD5: m[6], ChecksumXXHASH64: m[7], ChecksumXXHASH3: m[8], ChecksumXXHASH128: m[9],
			ChecksumType: new(o.Checksum.Type),
		}
	}
	if wants(s3api.ObjectAttributesObjectParts) && len(o.Parts) > 0 {
		out.ObjectParts = &s3api.GetObjectAttributesParts{TotalPartsCount: new(int32(len(o.Parts)))} //nolint:gosec // an object has at most 10,000 parts
	}
	if wants(s3api.ObjectAttributesObjectSize) {
		out.ObjectSize = new(o.Size)
	}
	if wants(s3api.ObjectAttributesStorageClass) {
		out.StorageClass = new(o.StorageClass)
	}
	return out, nil
}
