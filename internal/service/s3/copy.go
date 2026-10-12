package s3

import (
	"context"
	"crypto/md5" //nolint:gosec // S3's ETag is MD5 by definition; an integrity check, not a security control
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/checksum"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/s3/notify"
	"github.com/sustinbebustin/understudy/internal/store"
)

// selfCopyMessage is S3's text for a copy onto itself that changes
// nothing (s3.md 7).
const selfCopyMessage = "This copy request is illegal because it is trying to copy an object to itself without changing the object's metadata, storage class, website redirect location or encryption attributes."

// copySourceTooLargeMessage is S3's text for a copy source over 5 GiB,
// which needs UploadPartCopy (s3.md 7). The limit is documented; the text
// is UNVERIFIED, as quoted by SDK users' reports of the error.
const copySourceTooLargeMessage = "The specified copy source is larger than the maximum allowable size for a copy source: 5368709120"

// copySource is a parsed x-amz-copy-source.
type copySource struct {
	bucket    string
	key       string
	versionID *string
}

// parseCopySource reads x-amz-copy-source: [/]bucket/key[?versionId=id],
// percent-decoded before the bucket is split from the key, since
// Terraform escapes the slash between them (tf-remaining.md 8,
// wire-rest.md 3.3).
func parseCopySource(header string) (copySource, *awserr.Error) {
	raw, version := header, (*string)(nil)
	if i := strings.LastIndex(raw, "?versionId="); i >= 0 {
		raw, version = raw[:i], new(raw[i+len("?versionId="):])
	}
	bucket, key, ok := strings.Cut(strings.TrimPrefix(percentDecode(raw), "/"), "/")
	if !ok || bucket == "" || key == "" {
		return copySource{}, errInvalidArgument("Invalid copy source object key", "x-amz-copy-source", header)
	}
	return copySource{bucket: bucket, key: key, versionID: version}, nil
}

// percentDecode decodes every valid %XX in s and keeps any other '%' as
// it is (wire-rest.md 3.3: "decode %XX leniently"), so an unencoded key
// holding '%', which the Go SDK passes through untouched, still names its
// object. '+' stays a '+': botocore escapes a key's '+' as %2B and the Go
// SDK sends the key raw, so a literal '+' is the key's own. Terraform's
// provider escapes the source with url.QueryEscape, which writes a space
// as '+'; whether S3 reads that '+' as a space is UNVERIFIED, so such a
// key, holding a space, is not found here.
func percentDecode(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, hok := unhex(s[i+1])
			lo, lok := unhex(s[i+2])
			if hok && lok {
				b.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// unhex is the value of the hex digit c; false when c is not one.
func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// source is the copy source a CopyObject read: its version, its bucket's
// home and the version id the response reports.
type source struct {
	version  Version
	home     Home
	reported *string
	current  bool
}

// sourceOwnerHeader asserts the copy source bucket's owner.
const sourceOwnerHeader ownerHeader = "x-amz-source-expected-bucket-owner"

// readSource reads the copy source without admitting the request against
// the source bucket: a copy may cross regions, and phase 1 checks no
// access. x-amz-source-expected-bucket-owner naming another account is
// AccessDenied; source conditions fail with 412 (s3.md 5.10, 7). A source
// read holds a reference on the source's blob, taken under the source
// bucket's lock so a concurrent delete cannot drop the bytes before the
// copy commits; the caller hands it to the new version or releases it.
func (s *Service) readSource(blobs *blob.Store, src copySource, owner *string, conds readConditions, now time.Time) (source, *awserr.Error) {
	var claim *ownerClaim
	if owner != nil {
		claim = &ownerClaim{header: sourceOwnerHeader, account: *owner}
	}
	return readBucket(s, src.bucket, func(b *Bucket) (source, *awserr.Error) {
		if err := checkOwner(claim, b.Home); err != nil {
			return source{}, err
		}
		v, err := sourceVersion(b, src)
		if err != nil {
			return source{}, err
		}
		if err := copySourceError(conds.evaluate(v.validator(), now), conds); err != nil {
			return source{}, err
		}
		if rerr := blobs.Retain(v.Object.Blob); rerr != nil {
			return source{}, awserr.Internalf(fmt.Errorf("retaining the body of copy source %s/%s version %s: %w", src.bucket, src.key, v.ID, rerr))
		}
		cur, _ := b.Objects.current(src.key)
		return source{version: v, home: b.Home, reported: reportedVersion(b, v.ID), current: cur.ID == v.ID}, nil
	})
}

// sourceVersion is the object version a copy source names: the current
// object, or the version its id names, which may not be a delete marker.
func sourceVersion(b *Bucket, src copySource) (Version, *awserr.Error) {
	if src.versionID == nil {
		live, ok := b.Objects.live(src.key)
		if !ok {
			return Version{}, errNoSuchKey(src.key)
		}
		return live, nil
	}
	found, ok := b.Objects.version(src.key, *src.versionID)
	switch {
	case b.versioning() == unversioned && *src.versionID != nullVersion:
		return Version{}, errInvalidVersionID(*src.versionID)
	case !ok:
		return Version{}, errNoSuchVersion(src.key, *src.versionID)
	case found.marker():
		return Version{}, errInvalidRequest("The source of a copy request may not specifically refer to a delete marker by version id.")
	}
	return found, nil
}

// readBucket runs fn on bucket name, which need not be the request's own:
// unlike read, it admits nothing. A cell a snapshot load or reset retired
// is looked up again; no bucket is NoSuchBucket.
func readBucket[R any](s *Service, name string, fn func(b *Bucket) (R, *awserr.Error)) (R, *awserr.Error) {
	type result struct {
		r       R
		err     *awserr.Error
		retired bool
	}
	for {
		cell, ok := s.buckets.Lookup(name)
		if !ok {
			var zero R
			return zero, errNoSuchBucket(name)
		}
		got := store.Read(cell, func(bc *BucketCell) result {
			switch {
			case cell.Retired():
				return result{retired: true}
			case bc.Bucket == nil:
				return result{err: errNoSuchBucket(name)}
			}
			r, err := fn(bc.Bucket)
			return result{r: r, err: err}
		})
		if !got.retired {
			return got.r, got.err
		}
	}
}

// copyObject copies an object (s3.md 7). The copy shares the source's
// bytes: the blob store is content-addressed, so the new version keeps the
// reference the source read took on the source's blob; the bytes are read
// again only to compute an MD5 ETag or a FULL_OBJECT checksum the source
// does not carry.
func (s *Service) copyObject(ctx context.Context, c *kernel.Call, in *s3api.CopyObjectInput) (*s3api.CopyObjectOutput, *awserr.Error) {
	blobs, err := s.blobStore()
	if err != nil {
		return nil, err
	}
	srcRef, err := parseCopySource(in.CopySource)
	if err != nil {
		return nil, err
	}
	req, err := readCopy(in)
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
	// The source read's reference goes to commitWrite, which hands it to
	// the new version; every return before that releases it.
	handedOver := false
	defer func() {
		if !handedOver {
			s.release(ctx, so.Blob)
		}
	}()
	if customerKeyed(in.CopySourceSSECustomerAlgorithm, in.CopySourceSSECustomerKey, in.CopySourceSSECustomerKeyMD5) {
		return nil, errSSECNotApplicable()
	}
	if archived(so.StorageClass) {
		return nil, s3api.ErrObjectNotInActiveTierError("The source object of the COPY action is not in the active tier and is only stored in Amazon S3 Glacier.")
	}
	if so.Size > maxSinglePut {
		return nil, errInvalidRequest(copySourceTooLargeMessage)
	}
	if srcRef.bucket == in.Bucket && srcRef.key == in.Key && src.current && !req.changes(in, so) {
		return nil, errInvalidRequest(selfCopyMessage)
	}
	a := at(in.Bucket, in.ExpectedBucketOwner)
	pre, err := read(ctx, s, c, a, func(b *Bucket) (preWrite, *awserr.Error) {
		return prepareWrite(b, in.Key, req.cond, req.sse, req.acl)
	})
	if err != nil {
		return nil, err
	}
	if err := s.decrypt(ctx, c, src.home, so.Encryption); err != nil {
		return nil, err
	}
	enc, err := s.encrypt(ctx, c, pre.home, pre.sse)
	if err != nil {
		return nil, err
	}
	obj, err := s.copied(blobs, so, req, in, enc)
	if err != nil {
		return nil, err
	}
	handedOver = true
	done, err := s.commitWrite(ctx, c, a, in.Key, req.cond, obj, notify.CreatedCopy)
	if err != nil {
		return nil, err
	}
	result := &s3api.CopyObjectResult{ETag: new(quote(obj.ETag)), LastModified: new(done.version.Modified.Wire()), ChecksumType: new(obj.Checksum.Type)}
	m := membersOf(obj.Checksum)
	result.ChecksumCRC32, result.ChecksumCRC32C, result.ChecksumCRC64NVME, result.ChecksumSHA1, result.ChecksumSHA256 = m[0], m[1], m[2], m[3], m[4]
	result.ChecksumSHA512, result.ChecksumMD5, result.ChecksumXXHASH64, result.ChecksumXXHASH3, result.ChecksumXXHASH128 = m[5], m[6], m[7], m[8], m[9]
	out := &s3api.CopyObjectOutput{CopyObjectResult: result, VersionId: done.reported, CopySourceVersionId: src.reported}
	h := enc.headers()
	out.ServerSideEncryption, out.SSEKMSKeyId, out.BucketKeyEnabled = h.algorithm, h.keyID, h.bucketKey
	return out, nil
}

// copyRequest is a CopyObject read and checked before any bucket is.
type copyRequest struct {
	replaceMetadata bool
	replaceTags     bool
	tags            map[string]string
	class           s3api.StorageClass
	cond            writeCondition
	sse             sseRequest
	acl             objectACL
	algorithm       *checksum.Algorithm
}

func readCopy(in *s3api.CopyObjectInput) (copyRequest, *awserr.Error) {
	if err := checkKey(in.Key); err != nil {
		return copyRequest{}, err
	}
	r := copyRequest{
		replaceMetadata: in.MetadataDirective != nil && *in.MetadataDirective == s3api.MetadataDirectiveReplace,
		replaceTags:     in.TaggingDirective != nil && *in.TaggingDirective == s3api.TaggingDirectiveReplace,
		acl:             writeACL(in.ACL, in.GrantFullControl, in.GrantRead, in.GrantReadACP, in.GrantWriteACP),
		sse: sseRequest{
			algorithm: in.ServerSideEncryption, keyID: in.SSEKMSKeyId, context: in.SSEKMSEncryptionContext, bucketKey: in.BucketKeyEnabled,
			customer: in.SSECustomerAlgorithm != nil || in.SSECustomerKey != nil,
		},
	}
	if r.replaceMetadata {
		if err := checkUserMetadata(in.Metadata); err != nil {
			return copyRequest{}, err
		}
	}
	if r.replaceTags {
		tags, err := parseTaggingHeader(in.Tagging)
		if err != nil {
			return copyRequest{}, err
		}
		r.tags = tags
	}
	class, err := storageClass(in.StorageClass)
	if err != nil {
		return copyRequest{}, err
	}
	r.class = class
	if in.ChecksumAlgorithm != nil {
		a, perr := checksum.ParseAlgorithm(string(*in.ChecksumAlgorithm))
		if perr != nil {
			return copyRequest{}, errInvalidRequest("Checksum algorithm provided is unsupported.")
		}
		r.algorithm = &a
	}
	cond, err := parseWriteCondition(in.IfMatch, in.IfNoneMatch)
	if err != nil {
		return copyRequest{}, err
	}
	r.cond = cond
	return r, nil
}

// changes reports whether a copy onto itself changes something S3 counts:
// the metadata (REPLACE), the storage class, the website redirect, the
// encryption or the checksum algorithm (s3.md 7).
func (r copyRequest) changes(in *s3api.CopyObjectInput, so *Object) bool {
	return r.replaceMetadata ||
		in.StorageClass != nil && *in.StorageClass != so.StorageClass ||
		in.WebsiteRedirectLocation != nil ||
		in.ServerSideEncryption != nil || in.SSEKMSKeyId != nil ||
		r.algorithm != nil && r.algorithm.String() != so.Checksum.Algorithm
}

// copied is the destination record of a copy (s3.md 7): metadata and tags
// by their directives, never the website redirect unless restated, the
// source's checksum unless another algorithm is asked for, and an MD5 ETag
// unless the destination is SSE-KMS.
func (s *Service) copied(blobs *blob.Store, so *Object, r copyRequest, in *s3api.CopyObjectInput, enc Encryption) (*Object, *awserr.Error) {
	obj := &Object{Blob: so.Blob, Size: so.Size, Checksum: so.Checksum, StorageClass: r.class, Encryption: enc}
	obj.Header, obj.Metadata = so.Header, so.Metadata
	obj.Header.WebsiteRedirect = in.WebsiteRedirectLocation
	if r.replaceMetadata {
		obj.Header = systemHeaders{
			contentType: in.ContentType, cacheControl: in.CacheControl, contentDisposition: in.ContentDisposition,
			contentEncoding: in.ContentEncoding, contentLanguage: in.ContentLanguage, expires: in.Expires, websiteRedirect: in.WebsiteRedirectLocation,
		}.metadata()
		obj.Metadata = in.Metadata
	}
	obj.Tags = so.Tags
	if r.replaceTags {
		obj.Tags = r.tags
	}
	alg := checksum.CRC64NVME
	if a, err := checksum.ParseAlgorithm(so.Checksum.Algorithm); err == nil {
		alg = a
	}
	if r.algorithm != nil {
		alg = *r.algorithm
	}
	// A copy's checksum is always FULL_OBJECT (s3.md 7), so a composite
	// source checksum is recomputed over the bytes, as is an MD5 ETag the
	// source does not carry.
	needMD5 := enc.KMS == nil && (so.Encryption.KMS != nil || len(so.Parts) > 0)
	if alg.String() != so.Checksum.Algorithm || so.Checksum.Type != s3api.ChecksumTypeFullObject || needMD5 {
		md, sum, err := hashBlob(blobs, so.Blob, alg)
		if err != nil {
			return nil, awserr.Internalf(err)
		}
		obj.Checksum = StoredChecksum{Algorithm: alg.String(), Value: sum.String(), Type: s3api.ChecksumTypeFullObject}
		obj.ETag = hex.EncodeToString(md)
	} else {
		obj.ETag = so.ETag
	}
	if enc.KMS != nil {
		obj.ETag = string(s.k.IDs().ETag())
	}
	return obj, nil
}

// hashBlob streams a stored body once through MD5 and alg.
func hashBlob(blobs *blob.Store, ref blob.Ref, alg checksum.Algorithm) ([]byte, checksum.Sum, error) {
	sum, err := checksum.New(alg)
	if err != nil {
		return nil, checksum.Sum{}, err
	}
	r, err := blobs.Open(ref)
	if err != nil {
		return nil, checksum.Sum{}, err
	}
	defer func() { _ = r.Close() }()
	md := md5.New() //nolint:gosec // see the import
	if _, err := io.Copy(io.MultiWriter(md, sum), r); err != nil {
		return nil, checksum.Sum{}, err
	}
	return md.Sum(nil), sum.Checksum(), nil
}
