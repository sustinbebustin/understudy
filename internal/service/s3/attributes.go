package s3

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/checksum"
)

// getObjectAttributes answers the attributes the request names of the
// version it addresses (s3.md 6.4, AWS-recorded test_get_object_attributes):
// the ETag without quotes, the stored checksum with its type, a page of a
// multipart object's parts, the size and the storage class, STANDARD
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
		m := membersOf(attributeChecksum(o.Checksum))
		out.Checksum = &s3api.Checksum{
			ChecksumCRC32: m[checksum.CRC32], ChecksumCRC32C: m[checksum.CRC32C], ChecksumCRC64NVME: m[checksum.CRC64NVME],
			ChecksumSHA1: m[checksum.SHA1], ChecksumSHA256: m[checksum.SHA256], ChecksumSHA512: m[checksum.SHA512], ChecksumMD5: m[checksum.MD5],
			ChecksumXXHASH64: m[checksum.XXHASH64], ChecksumXXHASH3: m[checksum.XXHASH3], ChecksumXXHASH128: m[checksum.XXHASH128],
			ChecksumType: new(o.Checksum.Type),
		}
	}
	if wants(s3api.ObjectAttributesObjectParts) && len(o.Parts) > 0 {
		parts, err := objectPartsOf(o, in.MaxParts, in.PartNumberMarker)
		if err != nil {
			return nil, err
		}
		out.ObjectParts = parts
	}
	if wants(s3api.ObjectAttributesObjectSize) {
		out.ObjectSize = new(o.Size)
	}
	if wants(s3api.ObjectAttributesStorageClass) {
		out.StorageClass = new(o.StorageClass)
	}
	return out, nil
}

// objectPartsOf is ObjectParts of multipart object o, paged as ListParts
// is: the parts after part-number-marker, at most max-parts of them, each
// with its size and, for a COMPOSITE object, its own checksum.
// NextPartNumberMarker is the last part listed, 0 when none is (LocalStack
// aws_validated test_list_parts_via_object_attrs_pagination).
func objectPartsOf(o *Object, maxParts *int32, markerText *string) (*s3api.GetObjectAttributesParts, *awserr.Error) {
	limit, err := pageLimit(maxParts, "max-parts")
	if err != nil {
		return nil, err
	}
	marker, err := partNumberMarker(markerText)
	if err != nil {
		return nil, err
	}
	rest := o.Parts[min(int(marker), len(o.Parts)):]
	truncated := limit > 0 && len(rest) > limit
	rest = rest[:min(len(rest), limit)]
	var parts []s3api.ObjectPart
	n, next := marker, int32(0)
	for _, p := range rest {
		n++
		entry := s3api.ObjectPart{PartNumber: new(n), Size: new(p.Size)}
		if p.Checksum != "" {
			m := membersOf(StoredChecksum{Algorithm: o.Checksum.Algorithm, Value: p.Checksum})
			entry.ChecksumCRC32, entry.ChecksumCRC32C, entry.ChecksumCRC64NVME, entry.ChecksumSHA1, entry.ChecksumSHA256 = m[checksum.CRC32], m[checksum.CRC32C], m[checksum.CRC64NVME], m[checksum.SHA1], m[checksum.SHA256]
			entry.ChecksumSHA512, entry.ChecksumMD5, entry.ChecksumXXHASH64, entry.ChecksumXXHASH3, entry.ChecksumXXHASH128 = m[checksum.SHA512], m[checksum.MD5], m[checksum.XXHASH64], m[checksum.XXHASH3], m[checksum.XXHASH128]
		}
		parts = append(parts, entry)
		next = n
	}
	return &s3api.GetObjectAttributesParts{
		TotalPartsCount: count(len(o.Parts)), PartNumberMarker: new(strconv.Itoa(int(marker))), NextPartNumberMarker: new(strconv.Itoa(int(next))),
		MaxParts: count(limit), IsTruncated: new(truncated), Parts: parts,
	}, nil
}

// attributeChecksum is s as GetObjectAttributes reports it: a COMPOSITE
// checksum without the -N parts suffix GetObject and HeadObject carry
// (LocalStack aws_validated
// test_complete_multipart_parts_checksum_composite, steps get-object-attrs
// and head-object).
func attributeChecksum(s StoredChecksum) StoredChecksum {
	if s.Type == s3api.ChecksumTypeComposite {
		s.Value, _, _ = strings.Cut(s.Value, "-")
	}
	return s
}
