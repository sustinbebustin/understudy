package s3

import (
	"context"
	"crypto/md5" //nolint:gosec // S3's ETag and Content-MD5 are MD5 by definition; an integrity check, not a security control
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awschunked"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/checksum"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/service/s3/notify"
)

// The x-amz-content-sha256 values that frame a body as aws-chunked
// (sigv4-checksums-endpoints.md 3.1).
const (
	streamingPrefix          = "STREAMING-"
	streamingUnsignedTrailer = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
	streamingSigned          = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	streamingSignedTrailer   = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
)

// The request headers a body-carrying write reads beyond its input
// (kernel.CallHeaders).
const (
	headerContentSHA256  = "X-Amz-Content-Sha256"
	headerTrailer        = "X-Amz-Trailer"
	headerDecodedLength  = "X-Amz-Decoded-Content-Length"
	incompleteBodyText   = "You did not provide the number of bytes specified by the Content-Length HTTP header."
	entityTooLargeText   = "Your proposed upload exceeds the maximum allowed object size."
	malformedTrailerText = "The request contained trailing data that was not well-formed or did not conform to our published schema."
)

// putObject writes an object (s3.md 5.2). Every check that needs no body
// runs first: the key, metadata, tags, conditions, checksum headers, the
// bucket and its owner, the encryption key through KMS. Only then is the
// body pulled, so a refused request never reads it and net/http never
// sends 100 Continue. The body streams into the blob store while it is
// hashed; the checksums are verified at EOF, and the object is committed
// under the bucket's lock, where the write condition is judged again.
func (s *Service) putObject(ctx context.Context, c *kernel.Call, in *s3api.PutObjectInput) (*s3api.PutObjectOutput, *awserr.Error) {
	blobs, err := s.blobStore()
	if err != nil {
		return nil, err
	}
	req, err := readPut(c, in)
	if err != nil {
		return nil, err
	}
	a := at(in.Bucket, in.ExpectedBucketOwner)
	pre, err := read(ctx, s, c, a, func(b *Bucket) (preWrite, *awserr.Error) {
		return prepareWrite(b, in.Key, req.cond, req.sse, req.acl)
	})
	if err != nil {
		return nil, err
	}
	enc, err := s.encrypt(ctx, c, pre.home, pre.sse)
	if err != nil {
		return nil, err
	}
	got, err := s.receive(ctx, blobs, in.Body, req.body, req.body.checksum.algorithm())
	if err != nil {
		return nil, err
	}
	etag := hex.EncodeToString(got.md5)
	if enc.KMS != nil {
		etag = string(s.k.IDs().ETag())
	}
	obj := &Object{
		Blob: got.ref, Size: got.size, ETag: etag,
		Checksum:     StoredChecksum{Algorithm: got.sum.Algorithm().String(), Value: got.sum.String(), Type: s3api.ChecksumTypeFullObject},
		Header:       req.headers.metadata(),
		Metadata:     in.Metadata,
		Tags:         req.tags,
		StorageClass: req.class,
		Encryption:   enc,
	}
	committed, err := s.commitWrite(ctx, c, a, in.Key, req.cond, obj, notify.CreatedPut)
	if err != nil {
		return nil, err
	}
	out := &s3api.PutObjectOutput{ETag: new(quote(etag)), ChecksumType: new(s3api.ChecksumTypeFullObject), VersionId: committed.reported}
	m := membersOf(obj.Checksum)
	out.ChecksumCRC32, out.ChecksumCRC32C, out.ChecksumCRC64NVME, out.ChecksumSHA1, out.ChecksumSHA256 = m[0], m[1], m[2], m[3], m[4]
	out.ChecksumSHA512, out.ChecksumMD5, out.ChecksumXXHASH64, out.ChecksumXXHASH3, out.ChecksumXXHASH128 = m[5], m[6], m[7], m[8], m[9]
	h := enc.headers()
	out.ServerSideEncryption, out.SSEKMSKeyId, out.BucketKeyEnabled = h.algorithm, h.keyID, h.bucketKey
	if enc.KMS != nil && in.SSEKMSEncryptionContext != nil {
		out.SSEKMSEncryptionContext = in.SSEKMSEncryptionContext
	}
	return out, nil
}

// putRequest is a PutObject read and checked before its bucket is.
type putRequest struct {
	headers systemHeaders
	tags    map[string]string
	class   s3api.StorageClass
	cond    writeCondition
	sse     sseRequest
	acl     objectACL
	body    upload
}

// objectACL is the access control an object write asks for: its canned
// ACL, and whether it sent any x-amz-grant-* header.
type objectACL struct {
	canned *s3api.ObjectCannedACL
	grants bool
}

// writeACL is the objectACL of a write's x-amz-acl and grant headers.
func writeACL(canned *s3api.ObjectCannedACL, grants ...*string) objectACL {
	return objectACL{canned: canned, grants: slices.ContainsFunc(grants, func(g *string) bool { return g != nil })}
}

// check refuses an ACL a bucket whose ownership is BucketOwnerEnforced
// does not accept: any canned ACL but bucket-owner-full-control, or a grant
// header (s3.md 4.2.4, 5.2). Other buckets accept it; this build stores no
// object ACL (the registry's Partial note).
func (a objectACL) check(b *Bucket) *awserr.Error {
	if !aclsDisabled(b) {
		return nil
	}
	if a.grants || a.canned != nil && *a.canned != s3api.ObjectCannedACLBucketOwnerFullControl {
		return errACLsDisabled()
	}
	return nil
}

// readPut runs the checks of a PutObject that need neither the bucket nor
// the body.
func readPut(c *kernel.Call, in *s3api.PutObjectInput) (putRequest, *awserr.Error) {
	if err := checkKey(in.Key); err != nil {
		return putRequest{}, err
	}
	if err := checkUserMetadata(in.Metadata); err != nil {
		return putRequest{}, err
	}
	class, err := storageClass(in.StorageClass)
	if err != nil {
		return putRequest{}, err
	}
	tags, err := parseTaggingHeader(in.Tagging)
	if err != nil {
		return putRequest{}, err
	}
	cond, err := parseWriteCondition(in.IfMatch, in.IfNoneMatch)
	if err != nil {
		return putRequest{}, err
	}
	length := in.ContentLength
	switch {
	case length != nil:
	case in.Body == nil:
		// An input built without a body writes an empty object.
		length = new(int64(0))
	default:
		if n, ok := in.Body.Len(); ok {
			length = &n
		}
	}
	body, err := readUpload(c, in.ContentEncoding, length, checksumMembers{
		in.ChecksumCRC32, in.ChecksumCRC32C, in.ChecksumCRC64NVME, in.ChecksumSHA1, in.ChecksumSHA256,
		in.ChecksumSHA512, in.ChecksumMD5, in.ChecksumXXHASH64, in.ChecksumXXHASH3, in.ChecksumXXHASH128,
	}, in.ChecksumAlgorithm, in.ContentMD5)
	if err != nil {
		return putRequest{}, err
	}
	return putRequest{
		headers: systemHeaders{
			contentType: in.ContentType, cacheControl: in.CacheControl, contentDisposition: in.ContentDisposition,
			contentEncoding: in.ContentEncoding, contentLanguage: in.ContentLanguage, expires: in.Expires, websiteRedirect: in.WebsiteRedirectLocation,
		},
		tags: tags, class: class, cond: cond, body: body,
		acl: writeACL(in.ACL, in.GrantFullControl, in.GrantRead, in.GrantReadACP, in.GrantWriteACP),
		sse: sseRequest{
			algorithm: in.ServerSideEncryption, keyID: in.SSEKMSKeyId, context: in.SSEKMSEncryptionContext, bucketKey: in.BucketKeyEnabled,
			customer: in.SSECustomerAlgorithm != nil || in.SSECustomerKey != nil,
		},
	}, nil
}

// preWrite is what a write learns from its bucket before the body: where
// the bucket lives, for KMS, and the encryption it resolves to.
type preWrite struct {
	home Home
	sse  sseChoice
}

// prepareWrite judges a write against its bucket before the body is read:
// its ACL, the encryption it resolves to and its condition, which is
// judged again at commit.
func prepareWrite(b *Bucket, key string, cond writeCondition, r sseRequest, acl objectACL) (preWrite, *awserr.Error) {
	if err := acl.check(b); err != nil {
		return preWrite{}, err
	}
	choice, err := resolveSSE(r, b, objectARN(b.Name, key))
	if err != nil {
		return preWrite{}, err
	}
	if err := checkWrite(cond, key, currentValidator(b, key)); err != nil {
		return preWrite{}, err
	}
	return preWrite{home: b.Home, sse: choice}, nil
}

// currentValidator is the validator of key's current object, nil when the
// key has none or its current version is a delete marker.
func currentValidator(b *Bucket, key string) *validator {
	v, ok := b.Objects.live(key)
	if !ok {
		return nil
	}
	return new(v.validator())
}

// committed is a version a write committed, with the id its response
// reports.
type committed struct {
	version  Version
	reported *string
}

// commitWrite commits obj as key's newest version under the bucket's lock,
// taking over the reference the caller holds on obj.Blob (commitOwned).
func (s *Service) commitWrite(ctx context.Context, c *kernel.Call, a access, key string, cond writeCondition, obj *Object, action notify.CreatedAction) (committed, *awserr.Error) {
	return s.commitOwned(ctx, c, a, obj, s.addVersion(key, cond, obj, action))
}

// commitOwned runs add, which makes obj a version of its key, and hands the
// version the reference the caller holds on obj.Blob: the version keeps it
// once the write landed, even when an effect after the commit then fails
// the call, and it is released when the write did not land.
func (s *Service) commitOwned(ctx context.Context, c *kernel.Call, a access, obj *Object, add objectWrite[committed]) (committed, *awserr.Error) {
	got, landed, err := writeObjects(ctx, s, c, a, add)
	if !landed {
		s.release(ctx, obj.Blob)
	}
	return got, err
}

// addVersion adds obj as key's newest version. The condition was judged
// before the body was read; judged again here, a failure means another
// write or delete landed in between, which S3 answers with 409
// ConditionalRequestConflict (s3.md 5.10).
func (s *Service) addVersion(key string, cond writeCondition, obj *Object, action notify.CreatedAction) objectWrite[committed] {
	return func(b *Bucket, w objectTx) (committed, []blob.Ref, *awserr.Error) {
		if err := checkWrite(cond, key, currentValidator(b, key)); err != nil {
			return committed{}, nil, errConditionalConflict(cond, key)
		}
		v := Version{ID: s.newVersionID(b), Modified: clock.FromWire(seconds(w.now.Wire())), Object: obj}
		released := b.Objects.add(key, v)
		s.commitObject(w, b, event{key: key, versionID: v.ID, change: notify.Created{Action: action, Size: obj.Size, ETag: obj.ETag}})
		return committed{version: v, reported: reportedVersion(b, v.ID)}, blobsOf(released), nil
	}
}

// errConditionalConflict answers a conditional write that lost a race.
func errConditionalConflict(cond writeCondition, key string) *awserr.Error {
	condition := headerIfMatch
	if _, absent := cond.(ifAbsent); absent {
		condition = headerIfNoneMatch
	}
	return withField(withField(s3api.ErrConditionalRequestConflict(conditionalConflictMessage), "Condition", condition), "Key", key)
}

// upload is how a write's body arrives and what it declares about its
// bytes, read from the headers before the body.
type upload struct {
	// chunked is set for an aws-chunked body, with its framing.
	chunked *chunkFraming
	// declared is the decoded length the request states, -1 when unknown.
	declared int64
	checksum declaredChecksum
	md5      *contentMD5
	// payloadSHA256 is a hex x-amz-content-sha256, which the bytes must
	// hash to; empty for UNSIGNED-PAYLOAD, streaming values and none.
	payloadSHA256 string
}

// chunkFraming is an aws-chunked body's mode and reader options.
type chunkFraming struct {
	mode awschunked.Mode
	opts awschunked.Options
}

// readUpload reads the framing and checksum headers of a body-carrying
// write: x-amz-content-sha256, x-amz-trailer, x-amz-decoded-content-length,
// Content-Encoding, Content-Length, Content-MD5 and the checksum members.
// A declared size over 5 GiB is refused before the body is read.
func readUpload(c *kernel.Call, contentEncoding *string, contentLength *int64, sums checksumMembers, sdk *s3api.ChecksumAlgorithm, contentMD5 *string) (upload, *awserr.Error) {
	sha, _ := c.Header(headerContentSHA256)
	trailer, _ := c.Header(headerTrailer)
	u := upload{declared: -1}
	if contentLength != nil {
		u.declared = *contentLength
	}
	if chunked(contentEncoding) || strings.HasPrefix(sha, streamingPrefix) {
		f, declared, err := readChunkFraming(c, sha, trailer)
		if err != nil {
			return upload{}, err
		}
		u.chunked, u.declared = &f, declared
	} else if len(sha) == 2*sha256.Size {
		u.payloadSHA256 = sha
	}
	switch {
	case u.chunked == nil && u.declared < 0:
		return upload{}, s3api.ErrMissingContentLength("You must provide the Content-Length HTTP header.")
	case u.declared > maxSinglePut:
		return upload{}, errEntityTooLarge(u.declared)
	}
	if u.chunked == nil {
		trailer = ""
	}
	d, err := parseDeclared(sums, sdk, trailer)
	if err != nil {
		return upload{}, err
	}
	md, err := parseContentMD5(contentMD5)
	if err != nil {
		return upload{}, err
	}
	u.checksum, u.md5 = d, md
	return u, nil
}

// readChunkFraming reads an aws-chunked body's mode from
// x-amz-content-sha256 and its declared decoded length. A trailer mode
// without x-amz-trailer is refused before any byte is read.
func readChunkFraming(c *kernel.Call, sha, trailer string) (chunkFraming, int64, *awserr.Error) {
	mode := awschunked.PlainChunked
	switch sha {
	case streamingUnsignedTrailer:
		mode = awschunked.UnsignedTrailer
	case streamingSigned:
		mode = awschunked.Signed
	case streamingSignedTrailer:
		mode = awschunked.SignedTrailer
	default:
		if strings.HasPrefix(sha, streamingPrefix) {
			return chunkFraming{}, 0, s3api.ErrNotImplemented("understudy does not accept x-amz-content-sha256 " + sha + "; SigV4a streaming signatures are not supported. Sign with SigV4.")
		}
	}
	if (mode == awschunked.UnsignedTrailer || mode == awschunked.SignedTrailer) && trailer == "" {
		return chunkFraming{}, 0, errInvalidRequest("x-amz-content-sha256 " + sha + " requires the x-amz-trailer header naming the trailing checksum")
	}
	raw, _ := c.Header(headerDecodedLength)
	length, err := awschunked.ParseLength(raw)
	if err != nil {
		return chunkFraming{}, 0, errInvalidArgument("x-amz-decoded-content-length must be a non-negative integer", "x-amz-decoded-content-length", raw)
	}
	declared := int64(-1)
	if n, ok := length.Get(); ok {
		declared = n
	}
	return chunkFraming{mode: mode, opts: awschunked.Options{DecodedLength: length, Trailer: trailer}}, declared, nil
}

// received is a body stored in the blob store, with what was computed
// over it: sum under the algorithm the write declared (CRC64NVME when it
// declared none), and kept under the algorithm the caller keeps.
type received struct {
	ref  blob.Ref
	size int64
	md5  []byte
	sum  checksum.Sum
	kept checksum.Sum
}

// receive streams the body into the blob store while hashing it, then
// checks everything that could only be judged at EOF: the length, the
// declared checksum (header or trailer), Content-MD5 and a hex
// x-amz-content-sha256. On any failure the stored bytes are released.
// keep is the algorithm the caller stores the body's checksum under; when
// it differs from the declared one, the body is checksummed with both in
// the same pass, so it is never read back.
func (s *Service) receive(ctx context.Context, blobs *blob.Store, body smithy.Body, u upload, keep checksum.Algorithm) (received, *awserr.Error) {
	src, err := openBody(body)
	if err != nil {
		return received{}, awserr.Internalf(err)
	}
	defer func() { _ = src.Close() }()
	var decoder *awschunked.Reader
	var r io.Reader = src
	if u.chunked != nil {
		decoder = awschunked.NewReader(src, u.chunked.mode, u.chunked.opts)
		r = decoder
	}
	sum, err := checksum.New(u.checksum.algorithm())
	if err != nil {
		return received{}, awserr.Internalf(err)
	}
	md := md5.New() //nolint:gosec // see the import
	hashes := []io.Writer{md, sum}
	kept := sum
	if keep != u.checksum.algorithm() {
		if kept, err = checksum.New(keep); err != nil {
			return received{}, awserr.Internalf(err)
		}
		hashes = append(hashes, kept)
	}
	var payload hash.Hash
	if u.payloadSHA256 != "" {
		payload = sha256.New()
		hashes = append(hashes, payload)
	}
	counted := &limitReader{r: r, limit: maxSinglePut}
	ref, size, perr := blobs.Put(ctx, io.TeeReader(counted, io.MultiWriter(hashes...)), u.declared)
	if perr != nil {
		return received{}, bodyError(ctx, perr)
	}
	got := received{ref: ref, size: size, md5: md.Sum(nil), sum: sum.Checksum(), kept: kept.Checksum()}
	if err := u.verify(got, decoder, payload); err != nil {
		s.release(ctx, ref)
		return received{}, err
	}
	return got, nil
}

// verify judges the body once it reached EOF.
func (u upload) verify(got received, decoder *awschunked.Reader, payload hash.Hash) *awserr.Error {
	if u.chunked == nil && u.declared >= 0 && got.size != u.declared {
		return s3api.ErrIncompleteBody(incompleteBodyText)
	}
	if payload != nil && hex.EncodeToString(payload.Sum(nil)) != u.payloadSHA256 {
		return s3api.ErrXAmzContentSHA256Mismatch("The provided 'x-amz-content-sha256' header does not match what was computed.")
	}
	var trailerValue string
	if t, ok := u.checksum.(trailerChecksum); ok && decoder != nil {
		if fields, done := decoder.Trailers(); done {
			trailerValue = fields.Get(t.alg.HeaderName())
		}
	}
	if err := verifyDeclared(u.checksum, got.sum, trailerValue); err != nil {
		return err
	}
	return u.md5.verify(got.md5)
}

// openBody opens a write's body; an input built without one has none.
func openBody(body smithy.Body) (io.ReadCloser, error) {
	if body == nil {
		return io.NopCloser(strings.NewReader("")), nil
	}
	return body.Open()
}

// limitReader fails once more than limit bytes were read: the counting
// reader that refuses an object over 5 GiB without buffering it.
type limitReader struct {
	r     io.Reader
	n     int64
	limit int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.n += int64(n)
	if l.n > l.limit {
		return n, errEntityTooLarge(l.n).AsError()
	}
	return n, err
}

// bodyError is S3's answer for a body that failed while it was read
// (DEFERRED.md, S3 objects and checksums).
func bodyError(ctx context.Context, err error) *awserr.Error {
	if b, ok := errors.AsType[*awserr.BoundaryError](err); ok && b.Err != nil {
		if b.Err.ID == awserr.RequestEntityTooLarge() {
			return errEntityTooLarge(-1)
		}
		return b.Err
	}
	if short, ok := errors.AsType[*awschunked.ChunkTooSmallError](err); ok {
		e := s3api.ErrInvalidChunkSizeError("Only the last chunk is allowed to have a size less than 8192 bytes")
		return withField(withField(e, "Chunk", strconv.FormatInt(short.Chunk, 10)), "BadChunkSize", strconv.FormatInt(short.Size, 10))
	}
	if trailerFault(err) {
		return s3api.ErrMalformedTrailerError(malformedTrailerText)
	}
	if chunkFault(err) || errors.Is(err, io.ErrUnexpectedEOF) {
		return s3api.ErrIncompleteBody(incompleteBodyText)
	}
	if ctx.Err() != nil {
		return awserr.RequestAborted()
	}
	return awserr.Internalf(err)
}

// trailerFault reports an aws-chunked trailer that is missing, misnamed or
// malformed.
func trailerFault(err error) bool {
	if isA[*awschunked.MissingTrailerError](err) || isA[*awschunked.TrailerMismatchError](err) {
		return true
	}
	m, ok := errors.AsType[*awschunked.MalformedChunkError](err)
	return ok && (m.Defect == awschunked.BadTrailerLine || m.Defect == awschunked.ExtraTrailer || m.Defect == awschunked.MissingTrailerSignature)
}

// chunkFault reports an aws-chunked body that ended short of its declared
// length or whose chunk framing is broken, which S3 answers as an
// incomplete body.
func chunkFault(err error) bool {
	return isA[*awschunked.IncompleteBodyError](err) || isA[*awschunked.DecodedLengthExceededError](err) || isA[*awschunked.MalformedChunkError](err)
}

// isA reports whether err's chain holds an error of type T. It stands in
// for a blank-error errors.AsType, which the repository's errcheck
// configuration refuses.
func isA[T error](err error) bool {
	var target T
	return errors.As(err, &target)
}

// errEntityTooLarge answers a body over 5 GiB; proposed is its size, or
// negative when only the edge's cap saw it.
func errEntityTooLarge(proposed int64) *awserr.Error {
	e := withField(s3api.ErrEntityTooLarge(entityTooLargeText), "MaxSizeAllowed", strconv.FormatInt(maxSinglePut, 10))
	if proposed >= 0 {
		e = withField(e, "ProposedSize", strconv.FormatInt(proposed, 10))
	}
	return e
}
