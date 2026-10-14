package s3

import (
	"context"
	"encoding/hex"
	"maps"
	"slices"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// The part limits of s3.md 10.
const (
	maxPartNumber = 10000
	// minPartSize is the least size of every part a Complete lists but the
	// last: 5 MiB.
	minPartSize = 5 << 20
	// maxListParts is the default and the cap of max-parts and
	// max-uploads.
	maxListParts = 1000
)

// headerContentEncoding is the header that marks an UploadPart body as
// aws-chunked without a STREAMING-* x-amz-content-sha256 (kernel.CallHeaders).
const headerContentEncoding = "Content-Encoding"

// partNumberMessage is S3's text for a part number outside 1 to 10,000
// (REC test_complete_multipart_parts_order).
const partNumberMessage = "Part number must be an integer between 1 and 10000, inclusive"

// Part is one uploaded part of a multipart upload.
type Part struct {
	Blob blob.Ref `json:"blob"`
	Size int64    `json:"size"`
	// ETag is the part's ETag (partETag), unquoted.
	ETag string `json:"etag"`
	// Checksum is the part's checksum under its upload's part algorithm
	// (checksumMode.partAlgorithm), in its wire form.
	Checksum string        `json:"checksum"`
	Modified clock.Instant `json:"modified"`
}

// checkPartNumber refuses a part number outside 1 to 10,000.
func checkPartNumber(n int32) *awserr.Error {
	if n < 1 || n > maxPartNumber {
		return errInvalidArgument(partNumberMessage, "partNumber", strconv.Itoa(int(n)))
	}
	return nil
}

// partTarget is what a part write learns of its upload before it reads
// the body: how its parts are checksummed and how its object is
// encrypted, which the part's response reports.
type partTarget struct {
	mode checksumMode
	enc  Encryption
}

// partETag is the ETag of a part of an upload encrypted as enc, whose
// bytes have the MD5 md5Hex: that MD5, or for SSE-KMS a random 128-bit
// value, which is not the MD5 of the bytes (REC test_s3_multipart_upload_sse
// step multi-sse-upload-part; s3.md 5.3).
func (s *Service) partETag(enc Encryption, md5Hex string) string {
	if enc.KMS != nil {
		return string(s.k.IDs().ETag())
	}
	return md5Hex
}

// readTarget reads the upload a part write addresses: NoSuchUpload when
// the id names no upload in progress for key.
func (s *Service) readTarget(ctx context.Context, c *kernel.Call, a access, id, key string) (partTarget, *awserr.Error) {
	return read(ctx, s, c, a, func(b *Bucket) (partTarget, *awserr.Error) {
		u, err := b.Uploads.find(id, key)
		if err != nil {
			return partTarget{}, err
		}
		m, merr := u.Checksum.mode()
		if merr != nil {
			return partTarget{}, awserr.Internalf(merr)
		}
		return partTarget{mode: m, enc: u.Encryption}, nil
	})
}

// uploadPart stores one part (s3.md 10): the checks that need no body run
// first, as for PutObject, then the body streams into the blob store while
// it is hashed and is verified at EOF; the part is committed under the
// bucket's lock, replacing a part of the same number.
func (s *Service) uploadPart(ctx context.Context, c *kernel.Call, in *s3api.UploadPartInput) (*s3api.UploadPartOutput, *awserr.Error) {
	blobs, err := s.blobStore()
	if err != nil {
		return nil, err
	}
	if err := checkPartNumber(in.PartNumber); err != nil {
		return nil, err
	}
	if customerKeyed(in.SSECustomerAlgorithm, in.SSECustomerKey, in.SSECustomerKeyMD5) {
		return nil, errSSECNotApplicable()
	}
	// UploadPart models no Content-Encoding, so it is read from the call to
	// tell an aws-chunked body as PutObject does.
	var encoding *string
	if v, ok := c.Header(headerContentEncoding); ok {
		encoding = &v
	}
	u, err := readUpload(c, s.k.Identity(), encoding, bodyLength(in.ContentLength, in.Body), uploadPartInputFields(in).members(), in.ChecksumAlgorithm, in.ContentMD5)
	if err != nil {
		return nil, err
	}
	a := at(in.Bucket, in.ExpectedBucketOwner)
	t, err := s.readTarget(ctx, c, a, in.UploadId, in.Key)
	if err != nil {
		return nil, err
	}
	if err := checkPartChecksum(t.mode, u.checksum); err != nil {
		return nil, err
	}
	// The part is checksummed with its upload's part algorithm too, which
	// a part may differ from (an undeclared upload accepts any algorithm)
	// or leave unsent (S3 then computes CRC64NVME).
	got, err := s.receive(ctx, blobs, in.Body, u, t.mode.partAlgorithm())
	if err != nil {
		return nil, err
	}
	p, err := s.commitPart(ctx, c, a, in.UploadId, in.Key, in.PartNumber, Part{Blob: got.ref, Size: got.size, ETag: s.partETag(t.enc, hex.EncodeToString(got.md5)), Checksum: got.kept.String()})
	if err != nil {
		return nil, err
	}
	out := &s3api.UploadPartOutput{ETag: new(quote(p.ETag))}
	uploadPartOutputFields(out).set(uploadedPartChecksum(t.mode, u.checksum, got.sum, p))
	h := t.enc.headers()
	out.ServerSideEncryption, out.SSEKMSKeyId, out.BucketKeyEnabled = h.algorithm, h.keyID, h.bucketKey
	return out, nil
}

// bodyLength is the length a body-carrying write declares: Content-Length,
// else the body's own length; an input built without a body is empty.
func bodyLength(contentLength *int64, body smithy.Body) *int64 {
	switch {
	case contentLength != nil:
		return contentLength
	case body == nil:
		return new(int64(0))
	}
	if n, ok := body.Len(); ok {
		return &n
	}
	return nil
}

// commitPart makes p part n of the upload under the bucket's lock and
// takes over the reference the caller holds on p.Blob: the upload keeps it
// once the write landed, and it is released when the write did not land,
// as when the upload was completed or aborted meanwhile. A part it
// replaces releases its own.
func (s *Service) commitPart(ctx context.Context, c *kernel.Call, a access, id, key string, n int32, p Part) (Part, *awserr.Error) {
	got, landed, err := writeObjects(ctx, s, c, a, func(b *Bucket, w objectTx) (Part, []blob.Ref, *awserr.Error) {
		u, err := b.Uploads.find(id, key)
		if err != nil {
			return Part{}, nil, err
		}
		p.Modified = clock.FromWire(seconds(w.now.Wire()))
		var released []blob.Ref
		if old, had := u.setPart(n, p); had {
			released = append(released, old.Blob)
		}
		return p, released, nil
	})
	if !landed {
		s.release(ctx, p.Blob)
	}
	return got, err
}

// listParts lists an upload's parts in part number order after
// part-number-marker (s3.md 9.2): NextPartNumberMarker is the last part
// listed, 0 when none is. A zero max-parts lists nothing and is not
// truncated, as the other listings do, so a paginator cannot loop on it
// (UNVERIFIED: AWS's answer is unrecorded).
func listParts(in *s3api.ListPartsInput, b *Bucket) (*s3api.ListPartsOutput, *awserr.Error) {
	u, err := b.Uploads.find(in.UploadId, in.Key)
	if err != nil {
		return nil, err
	}
	m, merr := u.Checksum.mode()
	if merr != nil {
		return nil, awserr.Internalf(merr)
	}
	limit, err := pageLimit(in.MaxParts, "max-parts")
	if err != nil {
		return nil, err
	}
	marker, err := partNumberMarker(in.PartNumberMarker)
	if err != nil {
		return nil, err
	}
	numbers := slices.Sorted(maps.Keys(u.Parts))
	numbers = numbers[countAtMost(numbers, marker):]
	truncated := limit > 0 && len(numbers) > limit
	numbers = numbers[:min(len(numbers), limit)]
	var next int32
	parts := make([]s3api.Part, 0, len(numbers))
	for _, n := range numbers {
		p := u.Parts[n]
		entry := s3api.Part{PartNumber: new(n), LastModified: new(p.Modified.Wire()), ETag: new(quote(p.ETag)), Size: new(p.Size)}
		partFields(&entry).set(reportedPartChecksum(m, p))
		parts = append(parts, entry)
		next = n
	}
	out := &s3api.ListPartsOutput{
		Bucket: new(b.Name), Key: new(u.Key), UploadId: new(u.ID),
		PartNumberMarker: new(strconv.Itoa(int(marker))), NextPartNumberMarker: new(strconv.Itoa(int(next))),
		MaxParts: count(limit), IsTruncated: new(truncated), Parts: parts,
		Initiator: &s3api.Initiator{ID: new(u.Initiator)}, Owner: &s3api.Owner{ID: new(canonicalID(b.Home.OwnerAccount))},
		StorageClass: new(u.StorageClass),
	}
	out.ChecksumAlgorithm, out.ChecksumType = declaredOf(m)
	return out, nil
}

// countAtMost is how many of the sorted numbers are at most marker.
func countAtMost(sorted []int32, marker int32) int {
	i, found := slices.BinarySearch(sorted, marker)
	if found {
		return i + 1
	}
	return i
}

// pageLimit reads max-parts or max-uploads, named name: absent is 1,000,
// larger values are capped and a negative one is refused. The text is
// UNVERIFIED, written as max-keys's is (s3.md 9.1).
func pageLimit(v *int32, name string) (int, *awserr.Error) {
	switch {
	case v == nil:
		return maxListParts, nil
	case *v < 0:
		return 0, errInvalidArgument("Provided "+name+" not an integer or within integer range", name, strconv.Itoa(int(*v)))
	}
	return min(int(*v), maxListParts), nil
}

// partNumberMarker reads part-number-marker, a string in the model: absent
// or empty is 0. A value that is not a part number is InvalidArgument (the
// text is UNVERIFIED).
func partNumberMarker(v *string) (int32, *awserr.Error) {
	if v == nil || *v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(*v, 10, 32)
	if err != nil || n < 0 {
		return 0, errInvalidArgument("Provided part-number-marker not an integer or within integer range", "part-number-marker", *v)
	}
	return int32(n), nil
}
