package s3

import (
	"context"
	"crypto/md5" //nolint:gosec // a part's ETag is the MD5 of its bytes by definition; an integrity check, not a security control
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/checksum"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// copyRangeMessage is S3's text for an x-amz-copy-source-range that is not
// bytes=first-last (REC TestS3Multipart::test_upload_part_copy_range).
const copyRangeMessage = "The x-amz-copy-source-range value must be of the form bytes=first-last where first and last are the zero-based offsets of the first and last bytes to copy"

// copyRange is a parsed x-amz-copy-source-range: the inclusive byte
// offsets first to last, and the header as sent, which S3 echoes when the
// range does not fit the source.
type copyRange struct {
	first, last int64
	sent        string
}

// parseCopyRange reads x-amz-copy-source-range, which only the form
// bytes=first-last with first <= last takes (s3.md 7); nil is the whole
// source.
func parseCopyRange(v *string) (*copyRange, *awserr.Error) {
	if v == nil {
		return nil, nil
	}
	bad := errInvalidArgument(copyRangeMessage, "x-amz-copy-source-range", *v)
	spec, ok := strings.CutPrefix(*v, "bytes=")
	if !ok {
		return nil, bad
	}
	a, b, ok := strings.Cut(spec, "-")
	if !ok {
		return nil, bad
	}
	first, ferr := strconv.ParseUint(a, 10, 63)
	last, lerr := strconv.ParseUint(b, 10, 63)
	if ferr != nil || lerr != nil || first > last {
		return nil, bad
	}
	return &copyRange{first: int64(first), last: int64(last), sent: *v}, nil
}

// span is the offset and length r copies from a source of size bytes. A
// range that ends at or past the end is InvalidArgument naming the header
// as sent (testdata/parity/s3/R12/09-upload-part-copy-range-beyond.json).
func (r *copyRange) span(size int64) (first, n int64, err *awserr.Error) {
	if r == nil {
		return 0, size, nil
	}
	if r.last >= size {
		return 0, 0, errInvalidArgument("Range specified is not valid for source object of size: "+strconv.FormatInt(size, 10), "x-amz-copy-source-range", r.sent)
	}
	return r.first, r.last - r.first + 1, nil
}

// uploadPartCopy stores part n of an upload from a range of an existing
// object (s3.md 7): the copy-source conditions apply as for CopyObject,
// and the part's ETag is the MD5 of the bytes copied, or random for an
// SSE-KMS upload (partETag). The range streams through the blob store
// once, hashed on the way.
func (s *Service) uploadPartCopy(ctx context.Context, c *kernel.Call, in *s3api.UploadPartCopyInput) (*s3api.UploadPartCopyOutput, *awserr.Error) {
	blobs, err := s.blobStore()
	if err != nil {
		return nil, err
	}
	if err := checkPartNumber(in.PartNumber); err != nil {
		return nil, err
	}
	srcRef, err := parseCopySource(in.CopySource)
	if err != nil {
		return nil, err
	}
	rng, err := parseCopyRange(in.CopySourceRange)
	if err != nil {
		return nil, err
	}
	if customerKeyed(in.SSECustomerAlgorithm, in.SSECustomerKey, in.SSECustomerKeyMD5) ||
		customerKeyed(in.CopySourceSSECustomerAlgorithm, in.CopySourceSSECustomerKey, in.CopySourceSSECustomerKeyMD5) {
		return nil, errSSECNotApplicable()
	}
	a := at(in.Bucket, in.ExpectedBucketOwner)
	t, err := s.readTarget(ctx, c, a, in.UploadId, in.Key)
	if err != nil {
		return nil, err
	}
	src, err := s.readSource(blobs, srcRef, in.ExpectedSourceBucketOwner, readConditions{
		ifMatch: in.CopySourceIfMatch, ifNoneMatch: in.CopySourceIfNoneMatch,
		ifModifiedSince: in.CopySourceIfModifiedSince, ifUnmodifiedSince: in.CopySourceIfUnmodifiedSince,
	}, c.Admitted().Wire())
	if err != nil {
		return nil, err
	}
	so := src.version.Object
	defer s.release(ctx, so.Blob)
	if archived(so.StorageClass) {
		return nil, s3api.ErrObjectNotInActiveTierError("The source object of the COPY action is not in the active tier and is only stored in Amazon S3 Glacier.")
	}
	first, n, err := rng.span(so.Size)
	if err != nil {
		return nil, err
	}
	if n > maxSinglePut {
		return nil, errEntityTooLarge(n)
	}
	if err := s.decrypt(ctx, c, src.home, so.Encryption); err != nil {
		return nil, err
	}
	p, cerr := copyPart(ctx, blobs, so.Blob, first, n, t.mode.partAlgorithm())
	if cerr != nil {
		return nil, awserr.Internalf(fmt.Errorf("copying %d bytes at offset %d of %s/%s into part %d of upload %s: %w", n, first, srcRef.bucket, srcRef.key, in.PartNumber, in.UploadId, cerr))
	}
	p.ETag = s.partETag(t.enc, p.ETag)
	p, err = s.commitPart(ctx, c, a, in.UploadId, in.Key, in.PartNumber, p)
	if err != nil {
		return nil, err
	}
	result := &s3api.CopyPartResult{ETag: new(quote(p.ETag)), LastModified: new(p.Modified.Wire())}
	copyPartResultFields(result).set(reportedPartChecksum(t.mode, p))
	out := &s3api.UploadPartCopyOutput{CopyPartResult: result, CopySourceVersionId: src.reported}
	h := t.enc.headers()
	out.ServerSideEncryption, out.SSEKMSKeyId, out.BucketKeyEnabled = h.algorithm, h.keyID, h.bucketKey
	return out, nil
}

// copyPart stores n bytes of src from offset first as a new part's body,
// hashing them with MD5 and alg on the way. The caller owns the reference
// the returned part holds on its blob.
func copyPart(ctx context.Context, blobs *blob.Store, src blob.Ref, first, n int64, alg checksum.Algorithm) (Part, error) {
	sum, err := checksum.New(alg)
	if err != nil {
		return Part{}, err
	}
	r, err := blobs.Range(src, first, n)
	if err != nil {
		return Part{}, err
	}
	defer func() { _ = r.Close() }()
	md := md5.New() //nolint:gosec // see the import
	ref, size, err := blobs.Put(ctx, io.TeeReader(r, io.MultiWriter(md, sum)), n)
	if err != nil {
		return Part{}, err
	}
	return Part{Blob: ref, Size: size, ETag: hex.EncodeToString(md.Sum(nil)), Checksum: sum.Checksum().String()}, nil
}
