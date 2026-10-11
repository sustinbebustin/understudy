package s3

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// readRequest is what GetObject and HeadObject ask for, beyond the key.
type readRequest struct {
	key        string
	versionID  *string
	method     string
	conds      readConditions
	rangeSpec  *string
	partNumber *int32
	// retain is set by GetObject: the version's blob gains a reference
	// under the bucket's lock, which the caller releases once it opened
	// the body, so a concurrent overwrite or delete cannot drop the bytes
	// in between.
	retain *blob.Store
}

// served is an object version a read resolved, with the part of it the
// response carries.
type served struct {
	version  Version
	reported *string
	home     Home
	span     span
}

// span is the bytes a read returns: the whole object, or a range of it
// reported in Content-Range, with the parts count of a partNumber read.
type span struct {
	partial bool
	first   int64
	n       int64
	size    int64
	parts   *int32
}

// contentRange is the Content-Range of a partial read.
func (s span) contentRange() *string {
	if !s.partial {
		return nil
	}
	return new("bytes " + strconv.FormatInt(s.first, 10) + "-" + strconv.FormatInt(s.first+s.n-1, 10) + "/" + strconv.FormatInt(s.size, 10))
}

// readObject resolves the version a read addresses and applies, in S3's
// order, the conditional headers, then partNumber or Range (s3.md 5.8 to
// 5.10). With r.retain set, a read that succeeds holds a reference on the
// version's blob, which the caller releases.
func (s *Service) readObject(ctx context.Context, c *kernel.Call, a access, r readRequest) (served, *awserr.Error) {
	if r.partNumber != nil && r.rangeSpec != nil {
		return served{}, errInvalidRequest("Cannot specify both Range header and partNumber query parameter")
	}
	now := c.Admitted().Wire()
	return read(ctx, s, c, a, func(b *Bucket) (served, *awserr.Error) {
		v, err := findVersion(b, r.key, r.versionID, r.method)
		if err != nil {
			return served{}, err
		}
		if err := readError(r.conds.evaluate(v.validator(), now)); err != nil {
			return served{}, withValidatorHeaders(err, v)
		}
		sp, err := readSpan(v.Object, r.rangeSpec, r.partNumber)
		if err != nil {
			return served{}, err
		}
		if r.retain != nil {
			if rerr := r.retain.Retain(v.Object.Blob); rerr != nil {
				return served{}, awserr.Internalf(fmt.Errorf("retaining the body of %s version %s for GetObject: %w", r.key, v.ID, rerr))
			}
		}
		return served{version: v, reported: reportedVersion(b, v.ID), home: b.Home, span: sp}, nil
	})
}

// withValidatorHeaders adds the ETag and Last-Modified a 304 carries.
func withValidatorHeaders(e *awserr.Error, v Version) *awserr.Error {
	if e.ID.Status != http.StatusNotModified {
		return e
	}
	e.Headers = append(e.Headers,
		awserr.Header{Name: "ETag", Value: quote(v.Object.ETag)},
		awserr.Header{Name: "Last-Modified", Value: v.Modified.Wire().UTC().Format(http.TimeFormat)})
	return e
}

// readSpan resolves partNumber or Range against o.
func readSpan(o *Object, rangeSpec *string, partNumber *int32) (span, *awserr.Error) {
	whole := span{n: o.Size, size: o.Size}
	if partNumber != nil {
		return partSpan(o, *partNumber)
	}
	if rangeSpec == nil {
		return whole, nil
	}
	switch rg := parseRange(*rangeSpec, o.Size).(type) {
	case rangeIgnored:
		return whole, nil
	case rangeUnsatisfiable:
		e := withField(s3api.ErrInvalidRange("The requested range is not satisfiable"), "ActualObjectSize", strconv.FormatInt(o.Size, 10))
		return span{}, withField(e, "RangeRequested", *rangeSpec)
	case rangeBytes:
		return span{partial: true, first: rg.first, n: rg.last - rg.first + 1, size: o.Size}, nil
	}
	return whole, nil
}

// partSpan is part n of o: a multipart object's part, or for an object
// written in one request the whole object as part 1 (s3.md 5.8).
func partSpan(o *Object, n int32) (span, *awserr.Error) {
	parts := o.Parts
	if len(parts) == 0 {
		parts = []int64{o.Size}
	}
	if n < 1 || int(n) > len(parts) {
		e := withField(s3api.ErrInvalidPartNumber("The requested partnumber is not satisfiable"), "ActualPartCount", strconv.Itoa(len(parts)))
		return span{}, withField(e, "PartNumberRequested", strconv.Itoa(int(n)))
	}
	if len(o.Parts) == 0 {
		return span{n: o.Size, size: o.Size}, nil
	}
	var first int64
	for _, size := range parts[:n-1] {
		first += size
	}
	return span{partial: true, first: first, n: parts[n-1], size: o.Size, parts: count(len(parts))}, nil
}

// byteRange is how a Range header resolves against an object (s3.md 5.9).
//
//sumtype:decl
type byteRange interface{ isByteRange() }

// rangeIgnored is a header S3 ignores, serving the whole object: not one
// bytes range (RFC 9110 section 14.2).
type rangeIgnored struct{}

// rangeUnsatisfiable is a range that starts at or after the end: 416.
type rangeUnsatisfiable struct{}

// rangeBytes is the inclusive range first to last, clipped to the object.
type rangeBytes struct{ first, last int64 }

func (rangeIgnored) isByteRange()       {}
func (rangeUnsatisfiable) isByteRange() {}
func (rangeBytes) isByteRange()         {}

// parseRange reads bytes=a-b, bytes=a- or bytes=-n against an object of
// size bytes. Any range on an empty object is unsatisfiable.
func parseRange(h string, size int64) byteRange {
	spec, ok := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return rangeIgnored{}
	}
	a, b, ok := strings.Cut(spec, "-")
	if !ok {
		return rangeIgnored{}
	}
	if a == "" {
		n, err := strconv.ParseInt(b, 10, 64)
		switch {
		case err != nil || n < 0:
			return rangeIgnored{}
		case n == 0 || size == 0:
			return rangeUnsatisfiable{}
		}
		return rangeBytes{first: max(size-n, 0), last: size - 1}
	}
	first, err := strconv.ParseInt(a, 10, 64)
	if err != nil || first < 0 {
		return rangeIgnored{}
	}
	last := size - 1
	if b != "" {
		l, err := strconv.ParseInt(b, 10, 64)
		if err != nil || l < first {
			return rangeIgnored{}
		}
		last = min(l, size-1)
	}
	if first >= size {
		return rangeUnsatisfiable{}
	}
	return rangeBytes{first: first, last: last}
}

func (s *Service) getObject(ctx context.Context, c *kernel.Call, in *s3api.GetObjectInput) (*s3api.GetObjectOutput, *awserr.Error) {
	blobs, err := s.blobStore()
	if err != nil {
		return nil, err
	}
	got, err := s.readObject(ctx, c, at(in.Bucket, in.ExpectedBucketOwner), readRequest{
		key: in.Key, versionID: in.VersionId, method: http.MethodGet, rangeSpec: in.Range, partNumber: in.PartNumber,
		conds:  readConditions{ifMatch: in.IfMatch, ifNoneMatch: in.IfNoneMatch, ifModifiedSince: in.IfModifiedSince, ifUnmodifiedSince: in.IfUnmodifiedSince},
		retain: blobs,
	})
	if err != nil {
		return nil, err
	}
	o := got.version.Object
	defer s.release(ctx, o.Blob)
	if customerKeyed(in.SSECustomerAlgorithm, in.SSECustomerKey, in.SSECustomerKeyMD5) {
		return nil, errSSECNotApplicable()
	}
	if archived(o.StorageClass) {
		return nil, withField(s3api.ErrInvalidObjectState("The operation is not valid for the object's storage class"), "StorageClass", string(o.StorageClass))
	}
	if err := s.decrypt(ctx, c, got.home, o.Encryption); err != nil {
		return nil, err
	}
	rc, rerr := blobs.Range(o.Blob, got.span.first, got.span.n)
	if rerr != nil {
		return nil, awserr.Internalf(fmt.Errorf("opening the body of %s version %s for GetObject: %w", in.Key, got.version.ID, rerr))
	}
	h := headOf(got, in.ChecksumMode)
	out := &s3api.GetObjectOutput{
		Body:         &openedBody{rc: rc, n: got.span.n},
		AcceptRanges: h.AcceptRanges, LastModified: h.LastModified, ContentLength: h.ContentLength, ETag: h.ETag,
		ContentRange: h.ContentRange, PartsCount: h.PartsCount, VersionId: h.VersionId, Metadata: h.Metadata,
		CacheControl: h.CacheControl, ContentDisposition: h.ContentDisposition, ContentEncoding: h.ContentEncoding,
		ContentLanguage: h.ContentLanguage, ContentType: h.ContentType, Expires: h.Expires, WebsiteRedirectLocation: h.WebsiteRedirectLocation,
		ServerSideEncryption: h.ServerSideEncryption, SSEKMSKeyId: h.SSEKMSKeyId, BucketKeyEnabled: h.BucketKeyEnabled,
		StorageClass: h.StorageClass, TagCount: h.TagCount, ChecksumType: h.ChecksumType,
		ChecksumCRC32: h.ChecksumCRC32, ChecksumCRC32C: h.ChecksumCRC32C, ChecksumCRC64NVME: h.ChecksumCRC64NVME,
		ChecksumSHA1: h.ChecksumSHA1, ChecksumSHA256: h.ChecksumSHA256, ChecksumSHA512: h.ChecksumSHA512, ChecksumMD5: h.ChecksumMD5,
		ChecksumXXHASH64: h.ChecksumXXHASH64, ChecksumXXHASH3: h.ChecksumXXHASH3, ChecksumXXHASH128: h.ChecksumXXHASH128,
	}
	override(&out.CacheControl, in.ResponseCacheControl)
	override(&out.ContentDisposition, in.ResponseContentDisposition)
	override(&out.ContentEncoding, in.ResponseContentEncoding)
	override(&out.ContentLanguage, in.ResponseContentLanguage)
	override(&out.ContentType, in.ResponseContentType)
	if in.ResponseExpires != nil {
		out.Expires = new(in.ResponseExpires.UTC().Format(http.TimeFormat))
	}
	return out, nil
}

// override applies a response-* query override (s3.md 5.4).
func override(field **string, v *string) {
	if v != nil {
		*field = v
	}
}

// headOf is the response head of a read: the headers GetObject and
// HeadObject share (s3.md 5.8). A checksum is reported only with
// x-amz-checksum-mode: ENABLED, and only when it describes the bytes sent,
// so a ranged read never carries the whole object's checksum (boto3 would
// fail it).
func headOf(got served, mode *s3api.ChecksumMode) *s3api.HeadObjectOutput {
	o := got.version.Object
	h := &s3api.HeadObjectOutput{
		AcceptRanges:            new("bytes"),
		LastModified:            new(got.version.Modified.Wire().UTC().Truncate(time.Second)),
		ContentLength:           new(got.span.n),
		ETag:                    new(quote(o.ETag)),
		ContentRange:            got.span.contentRange(),
		PartsCount:              got.span.parts,
		VersionId:               got.reported,
		Metadata:                o.Metadata,
		CacheControl:            o.Header.CacheControl,
		ContentDisposition:      o.Header.ContentDisposition,
		ContentEncoding:         o.Header.ContentEncoding,
		ContentLanguage:         o.Header.ContentLanguage,
		ContentType:             new(o.Header.ContentType),
		Expires:                 o.Header.Expires,
		WebsiteRedirectLocation: o.Header.WebsiteRedirect,
		StorageClass:            reportedClass(o.StorageClass),
		TagCount:                tagCount(o.Tags),
	}
	sse := o.Encryption.headers()
	h.ServerSideEncryption, h.SSEKMSKeyId, h.BucketKeyEnabled = sse.algorithm, sse.keyID, sse.bucketKey
	if mode != nil && *mode == s3api.ChecksumModeEnabled && got.span.n == o.Size && o.Checksum.Value != "" {
		m := membersOf(o.Checksum)
		h.ChecksumCRC32, h.ChecksumCRC32C, h.ChecksumCRC64NVME, h.ChecksumSHA1, h.ChecksumSHA256 = m[0], m[1], m[2], m[3], m[4]
		h.ChecksumSHA512, h.ChecksumMD5, h.ChecksumXXHASH64, h.ChecksumXXHASH3, h.ChecksumXXHASH128 = m[5], m[6], m[7], m[8], m[9]
		h.ChecksumType = new(o.Checksum.Type)
	}
	return h
}
