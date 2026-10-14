package s3

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/s3/notify"
)

// bindMultipart binds the multipart upload operations.
func (s *Service) bindMultipart(b s3api.Binder) {
	create := func(in *s3api.CreateMultipartUploadInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.CreateMultipartUpload(s.createMultipartUpload, registry.Partial{Note: "no SSE-C or Object Lock; an object ACL a bucket accepts is not stored; no lifecycle abort date"}, touches(create))
	part := func(in *s3api.UploadPartInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.UploadPart(s.uploadPart, registry.Partial{Note: "no SSE-C; chunk signatures are verified under sigv4 verify"}, touches(part))
	partCopy := func(in *s3api.UploadPartCopyInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.UploadPartCopy(s.uploadPartCopy, registry.Partial{Note: "no SSE-C; errors are answered before the response starts, so only a StatusOKError fault rule sends 200-with-error"}, touches(partCopy))
	complete := func(in *s3api.CompleteMultipartUploadInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.CompleteMultipartUpload(s.completeMultipartUpload, registry.Partial{Note: "no SSE-C; errors are answered before the response starts, so only a StatusOKError fault rule sends 200-with-error"}, touches(complete))
	abort := func(in *s3api.AbortMultipartUploadInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.AbortMultipartUpload(s.abortMultipartUpload, registry.Partial{Note: "x-amz-if-match-initiated-time, a directory bucket header, answers 501"}, touches(abort))
	lp := func(in *s3api.ListPartsInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.ListParts(reader(s, lp, listParts), registry.Full{}, touches(lp))
	lu := func(in *s3api.ListMultipartUploadsInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.ListMultipartUploads(reader(s, lu, listMultipartUploads), registry.Full{}, touches(lu))
}

// Uploads is a bucket's multipart uploads in progress, by upload id. Like
// Objects.Keys, ByID and each upload's Parts are changed in place: only
// inside the bucket's kernel.Write, after every check of the write passed,
// and no read returns either map.
type Uploads struct {
	ByID map[string]*Upload `json:"byId,omitempty"`
}

// Upload is one multipart upload in progress: what CreateMultipartUpload
// said about the object it completes to (s3.md 10), and its parts by
// number.
type Upload struct {
	ID  string `json:"id"`
	Key string `json:"key"`
	// Initiated is when Create committed it.
	Initiated clock.Instant `json:"initiated"`
	// Seq is the bucket's version Seq taken at Create: the completed object
	// is ordered among its key's versions by when the upload began.
	Seq uint64 `json:"seq"`
	// Initiator is the canonical user id of the account that created it.
	Initiator    string             `json:"initiator"`
	Checksum     UploadChecksum     `json:"checksum"`
	Header       SystemMetadata     `json:"header"`
	Metadata     map[string]string  `json:"metadata,omitempty"`
	Tags         map[string]string  `json:"tags,omitempty"`
	StorageClass s3api.StorageClass `json:"storageClass"`
	Encryption   Encryption         `json:"encryption"`
	Parts        map[int32]Part     `json:"parts,omitempty"`
}

// find returns the upload id names, which must be one in progress for key
// (s3.md 9.2: a wrong key is NoSuchUpload too).
func (u *Uploads) find(id, key string) (*Upload, *awserr.Error) {
	x, ok := u.ByID[id]
	if !ok || x.Key != key {
		return nil, errNoSuchUpload(id)
	}
	return x, nil
}

// add records a new upload.
func (u *Uploads) add(x *Upload) {
	if u.ByID == nil {
		u.ByID = map[string]*Upload{}
	}
	u.ByID[x.ID] = x
}

// setPart makes p part n of x and returns the part it replaced.
func (x *Upload) setPart(n int32, p Part) (Part, bool) {
	if x.Parts == nil {
		x.Parts = map[int32]Part{}
	}
	old, had := x.Parts[n]
	x.Parts[n] = p
	return old, had
}

// remove drops upload id.
func (u *Uploads) remove(id string) { delete(u.ByID, id) }

// blobs lists the reference every part of every upload holds, uploads in
// id order and parts in number order: what DeleteBucket releases with the
// uploads it drops.
func (u *Uploads) blobs() []blob.Ref {
	var out []blob.Ref
	for _, id := range slices.Sorted(maps.Keys(u.ByID)) {
		out = append(out, u.ByID[id].blobs()...)
	}
	return out
}

// blobRefs lists blobs in BucketCell.BlobRefs's form, one entry per
// reference, so a snapshot embeds every part's body and the admin reset
// releases it.
func (u *Uploads) blobRefs() []string {
	refs := u.blobs()
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.String()
	}
	return out
}

// blobs lists the reference each of the upload's parts holds.
func (x *Upload) blobs() []blob.Ref {
	out := make([]blob.Ref, 0, len(x.Parts))
	for _, n := range slices.Sorted(maps.Keys(x.Parts)) {
		out = append(out, x.Parts[n].Blob)
	}
	return out
}

func errNoSuchUpload(id string) *awserr.Error {
	e := s3api.ErrNoSuchUpload("The specified upload does not exist. The upload ID may be invalid, or the upload may have been aborted or completed.")
	return withField(e, "UploadId", id)
}

// createRequest is a CreateMultipartUpload read and checked before its
// bucket is.
type createRequest struct {
	headers systemHeaders
	tags    map[string]string
	class   s3api.StorageClass
	sse     sseRequest
	acl     objectACL
	mode    checksumMode
}

func readCreate(in *s3api.CreateMultipartUploadInput) (createRequest, *awserr.Error) {
	if err := checkKey(in.Key); err != nil {
		return createRequest{}, err
	}
	if err := checkUserMetadata(in.Metadata); err != nil {
		return createRequest{}, err
	}
	class, err := storageClass(in.StorageClass)
	if err != nil {
		return createRequest{}, err
	}
	tags, err := parseTaggingHeader(in.Tagging)
	if err != nil {
		return createRequest{}, err
	}
	mode, err := parseUploadChecksum(in.ChecksumAlgorithm, in.ChecksumType)
	if err != nil {
		return createRequest{}, err
	}
	return createRequest{
		headers: systemHeaders{
			contentType: in.ContentType, cacheControl: in.CacheControl, contentDisposition: in.ContentDisposition,
			contentEncoding: in.ContentEncoding, contentLanguage: in.ContentLanguage, expires: in.Expires, websiteRedirect: in.WebsiteRedirectLocation,
		},
		tags: tags, class: class, mode: mode,
		acl: writeACL(in.ACL, in.GrantFullControl, in.GrantRead, in.GrantReadACP, in.GrantWriteACP),
		sse: sseRequest{
			algorithm: in.ServerSideEncryption, keyID: in.SSEKMSKeyId, context: in.SSEKMSEncryptionContext, bucketKey: in.BucketKeyEnabled,
			customer: in.SSECustomerAlgorithm != nil || in.SSECustomerKey != nil,
		},
	}, nil
}

// createMultipartUpload starts an upload (s3.md 10): everything that
// shapes the object is checked and fixed here, the KMS data key included,
// under an upload id from the emulator's id source.
func (s *Service) createMultipartUpload(ctx context.Context, c *kernel.Call, in *s3api.CreateMultipartUploadInput) (*s3api.CreateMultipartUploadOutput, *awserr.Error) {
	req, err := readCreate(in)
	if err != nil {
		return nil, err
	}
	a := at(in.Bucket, in.ExpectedBucketOwner)
	pre, err := read(ctx, s, c, a, func(b *Bucket) (preWrite, *awserr.Error) {
		return prepareWrite(b, in.Key, unconditional{}, req.sse, req.acl)
	})
	if err != nil {
		return nil, err
	}
	enc, err := s.encrypt(ctx, c, pre.home, pre.sse)
	if err != nil {
		return nil, err
	}
	u := &Upload{
		ID: string(s.k.IDs().UploadID()), Key: in.Key, Initiator: canonicalID(c.Scope().Account), Checksum: persisted(req.mode),
		Header: req.headers.metadata(), Metadata: in.Metadata, Tags: req.tags, StorageClass: req.class, Encryption: enc,
	}
	_, _, err = writeObjects(ctx, s, c, a, func(b *Bucket, w objectTx) (struct{}, []blob.Ref, *awserr.Error) {
		x := *u
		x.Initiated = w.now
		x.Seq = b.nextSeq()
		b.Uploads.add(&x)
		return struct{}{}, nil, nil
	})
	if err != nil {
		return nil, err
	}
	out := &s3api.CreateMultipartUploadOutput{Bucket: new(in.Bucket), Key: new(in.Key), UploadId: new(u.ID)}
	out.ChecksumAlgorithm, out.ChecksumType = declaredOf(req.mode)
	h := enc.headers()
	out.ServerSideEncryption, out.SSEKMSKeyId, out.BucketKeyEnabled = h.algorithm, h.keyID, h.bucketKey
	if enc.KMS != nil && in.SSEKMSEncryptionContext != nil {
		out.SSEKMSEncryptionContext = in.SSEKMSEncryptionContext
	}
	return out, nil
}

// abortMultipartUpload drops an upload and its parts' bytes; the id is
// dead afterwards (s3.md 10).
func (s *Service) abortMultipartUpload(ctx context.Context, c *kernel.Call, in *s3api.AbortMultipartUploadInput) (*s3api.AbortMultipartUploadOutput, *awserr.Error) {
	if in.IfMatchInitiatedTime != nil {
		return nil, withField(s3api.ErrNotImplemented(notImplementedHeader), "Header", "x-amz-if-match-initiated-time")
	}
	_, _, err := writeObjects(ctx, s, c, at(in.Bucket, in.ExpectedBucketOwner), func(b *Bucket, _ objectTx) (struct{}, []blob.Ref, *awserr.Error) {
		u, err := b.Uploads.find(in.UploadId, in.Key)
		if err != nil {
			return struct{}{}, nil, err
		}
		b.Uploads.remove(u.ID)
		return struct{}{}, u.blobs(), nil
	})
	if err != nil {
		return nil, err
	}
	return &s3api.AbortMultipartUploadOutput{}, nil
}

// completeRequest is a CompleteMultipartUpload read and checked before
// its bucket is.
type completeRequest struct {
	id, key string
	parts   []s3api.CompletedPart
	// sent is the object checksum header, nil when none was sent.
	sent *sentChecksum
	typ  *s3api.ChecksumType
	// size is x-amz-mp-object-size.
	size *int64
	cond writeCondition
}

// readComplete reads a Complete. A request without a body lists no parts,
// which judgeCompletion answers after the upload is found (REC
// test_multipart_complete_multipart_too_small step complete-exc-no-parts,
// test_multipart_no_such_upload step complete-exc).
func readComplete(in *s3api.CompleteMultipartUploadInput) (completeRequest, *awserr.Error) {
	var parts []s3api.CompletedPart
	if in.MultipartUpload != nil {
		parts = in.MultipartUpload.Parts
	}
	if slices.ContainsFunc(parts, func(p s3api.CompletedPart) bool { return p.PartNumber == nil }) {
		return completeRequest{}, errMalformedXML()
	}
	cond, err := parseWriteCondition(in.IfMatch, in.IfNoneMatch)
	if err != nil {
		return completeRequest{}, err
	}
	sums := completeMultipartUploadInputFields(in).members().sent()
	r := completeRequest{id: in.UploadId, key: in.Key, parts: parts, typ: in.ChecksumType, size: in.MpuObjectSize, cond: cond}
	switch len(sums) {
	case 0:
	case 1:
		r.sent = &sums[0]
	default:
		return completeRequest{}, errInvalidRequest(multipleChecksumsMessage)
	}
	return r, nil
}

// completion is a Complete judged against its upload under the bucket's
// lock: the upload without its part map, the listed parts and the
// object's ETag, checksum and size.
type completion struct {
	upload Upload
	parts  []listedPart
	etag   string
	sum    StoredChecksum
	size   int64
}

// completeMultipartUpload assembles the listed parts into the object
// (s3.md 10, 6.4). Every check runs under the bucket's lock against the
// upload: the part list, the parts' ETags and sizes, the checksum mode and
// values, x-amz-mp-object-size and the write condition. Then the parts
// are concatenated into the object's blob, and the object is committed
// under the lock, where the upload must still hold the listed parts and
// the write condition is judged again.
func (s *Service) completeMultipartUpload(ctx context.Context, c *kernel.Call, in *s3api.CompleteMultipartUploadInput) (*s3api.CompleteMultipartUploadOutput, *awserr.Error) {
	blobs, err := s.blobStore()
	if err != nil {
		return nil, err
	}
	if customerKeyed(in.SSECustomerAlgorithm, in.SSECustomerKey, in.SSECustomerKeyMD5) {
		return nil, errSSECNotApplicable()
	}
	req, err := readComplete(in)
	if err != nil {
		return nil, err
	}
	a := at(in.Bucket, in.ExpectedBucketOwner)
	plan, err := read(ctx, s, c, a, func(b *Bucket) (completion, *awserr.Error) { return s.judgeCompletion(ctx, blobs, b, req) })
	if err != nil {
		return nil, err
	}
	refs := make([]blob.Ref, len(plan.parts))
	for i := range plan.parts {
		refs[i] = plan.parts[i].part.Blob
	}
	// judgeCompletion retained every listed part's blob for the
	// concatenation below.
	defer s.release(ctx, refs...)
	parts := &partsReader{blobs: blobs, refs: refs}
	ref, size, perr := blobs.Put(ctx, parts, plan.size)
	if cerr := parts.Close(); perr == nil {
		perr = cerr
	}
	if perr != nil {
		return nil, awserr.Internalf(fmt.Errorf("concatenating the %d parts of upload %s into %s/%s: %w", len(refs), req.id, in.Bucket, in.Key, perr))
	}
	u := plan.upload
	obj := &Object{
		Blob: ref, Size: size, ETag: plan.etag, Checksum: plan.sum, Header: u.Header, Metadata: u.Metadata, Tags: u.Tags,
		StorageClass: u.StorageClass, Encryption: u.Encryption, Parts: objectParts(plan.parts, plan.sum.Type),
	}
	done, err := s.commitOwned(ctx, c, a, obj, s.completeWrite(req, plan, obj))
	if err != nil {
		return nil, err
	}
	out := &s3api.CompleteMultipartUploadOutput{
		Location: new(objectLocation(c.Endpoint(), in.Bucket, in.Key)), Bucket: new(in.Bucket), Key: new(in.Key),
		ETag: new(quote(obj.ETag)), VersionId: done.reported,
	}
	// An SSE-KMS result reports no checksum, though the object keeps one
	// that reads report (REC test_s3_multipart_upload_sse steps
	// multi-sse-compete-multipart and get-obj; recorded for an undeclared
	// upload, UNVERIFIED for a declared one).
	if obj.Encryption.KMS == nil {
		out.ChecksumType = new(obj.Checksum.Type)
		completeMultipartUploadOutputFields(out).set(membersOf(obj.Checksum))
	}
	h := obj.Encryption.headers()
	out.ServerSideEncryption, out.SSEKMSKeyId, out.BucketKeyEnabled = h.algorithm, h.keyID, h.bucketKey
	return out, nil
}

// objectParts is what the completed object keeps of its listed parts: each
// part's size and, for a COMPOSITE object, its checksum, which a partNumber
// read reports (s3.md 5.8). A FULL_OBJECT part checksum is never reported,
// so it is not kept.
func objectParts(parts []listedPart, typ s3api.ChecksumType) []ObjectPart {
	out := make([]ObjectPart, len(parts))
	for i := range parts {
		out[i].Size = parts[i].part.Size
		if typ == s3api.ChecksumTypeComposite {
			out[i].Checksum = parts[i].part.Checksum
		}
	}
	return out
}

// judgeCompletion runs every check of a Complete against b, in S3's order
// (s3.md Implications 4): the upload; a non-empty, strictly ascending part
// list; the checksum type; each listed part uploaded with the ETag sent;
// each but the last at least 5 MiB; the checksum values;
// x-amz-mp-object-size; the write condition. A Complete that passes holds
// a reference on every listed part's blob, which the caller releases.
func (s *Service) judgeCompletion(ctx context.Context, blobs *blob.Store, b *Bucket, req completeRequest) (completion, *awserr.Error) {
	u, err := b.Uploads.find(req.id, req.key)
	if err != nil {
		return completion{}, err
	}
	m, merr := u.Checksum.mode()
	if merr != nil {
		return completion{}, awserr.Internalf(merr)
	}
	parts, err := orderedParts(u.ID, req.parts)
	if err != nil {
		return completion{}, err
	}
	if err := checkCompleteType(m, req.typ); err != nil {
		return completion{}, err
	}
	if err := matchParts(u, parts); err != nil {
		return completion{}, err
	}
	sum, err := objectChecksum(m, req.id, parts)
	if err != nil {
		return completion{}, err
	}
	if err := checkCompleteHeader(m, req.sent, req.typ, sum); err != nil {
		return completion{}, err
	}
	var size int64
	for i := range parts {
		size += parts[i].part.Size
	}
	if req.size != nil && *req.size != size {
		return completion{}, errInvalidRequest("The provided 'x-amz-mp-object-size' header value " + strconv.FormatInt(*req.size, 10) +
			" does not match what was computed: " + strconv.FormatInt(size, 10))
	}
	if err := checkWrite(req.cond, req.key, currentValidator(b, req.key)); err != nil {
		return completion{}, err
	}
	etag, eerr := multipartETag(parts)
	if eerr != nil {
		return completion{}, awserr.Internalf(eerr)
	}
	retained := make([]blob.Ref, 0, len(parts))
	for i := range parts {
		p := &parts[i]
		if rerr := blobs.Retain(p.part.Blob); rerr != nil {
			s.release(ctx, retained...)
			return completion{}, awserr.Internalf(fmt.Errorf("retaining part %d of upload %s for CompleteMultipartUpload: %w", p.number, req.id, rerr))
		}
		retained = append(retained, p.part.Blob)
	}
	x := *u
	x.Parts = nil
	return completion{upload: x, parts: parts, etag: etag, sum: sum, size: size}, nil
}

// orderedParts reads a Complete's part list of upload id, which must not
// be empty and must be strictly ascending (s3.md 10). The parts it returns
// are not yet matched to the upload's.
func orderedParts(id string, sent []s3api.CompletedPart) ([]listedPart, *awserr.Error) {
	if len(sent) == 0 {
		return nil, errInvalidRequest("You must specify at least one part")
	}
	parts := make([]listedPart, len(sent))
	for i := range sent {
		cp := &sent[i]
		if i > 0 && *cp.PartNumber <= parts[i-1].number {
			return nil, errInvalidPartOrder(id)
		}
		parts[i] = listedPart{
			number: *cp.PartNumber, etag: unquote(deref(cp.ETag)),
			restated: completedPartFields(cp).members(),
		}
	}
	return parts, nil
}

// matchParts matches the ordered parts to the upload's, filling in each
// listed part: each exists with the ETag sent, quoted or not, and each but
// the last is at least 5 MiB (s3.md 10).
func matchParts(u *Upload, parts []listedPart) *awserr.Error {
	for i := range parts {
		p := &parts[i]
		got, ok := u.Parts[p.number]
		if !ok || got.ETag != p.etag {
			return errInvalidPart(u.ID, p)
		}
		p.part = got
	}
	for i := range parts[:len(parts)-1] {
		p := &parts[i]
		if p.part.Size < minPartSize {
			e := withField(s3api.ErrEntityTooSmall("Your proposed upload is smaller than the minimum allowed size"), "ProposedSize", strconv.FormatInt(p.part.Size, 10))
			e = withField(withField(e, "MinSizeAllowed", strconv.Itoa(minPartSize)), "PartNumber", strconv.Itoa(int(p.number)))
			return withField(e, "ETag", p.part.ETag)
		}
	}
	return nil
}

// errInvalidPartOrder answers a part list that is not strictly ascending
// with S3's text and UploadId (s3.md 10, wire-rest.md 3.6).
func errInvalidPartOrder(uploadID string) *awserr.Error {
	return withField(s3api.ErrInvalidPartOrder("The list of parts was not in ascending order. Parts must be ordered by part number."), "UploadId", uploadID)
}

// completeWrite commits a judged Complete: the upload must still hold
// every listed part as judged, and the write condition is judged again; a
// failure there means another request landed in between. The object's
// version is dated at the upload's initiation (s3.md 10) and placed by
// completedSeq, the upload is dropped, and every part's reference is
// released.
func (s *Service) completeWrite(req completeRequest, plan completion, obj *Object) objectWrite[committed] {
	return func(b *Bucket, w objectTx) (committed, []blob.Ref, *awserr.Error) {
		u, err := b.Uploads.find(req.id, req.key)
		if err != nil {
			return committed{}, nil, err
		}
		for i := range plan.parts {
			lp := &plan.parts[i]
			if p, ok := u.Parts[lp.number]; !ok || p != lp.part {
				return committed{}, nil, errInvalidPart(req.id, lp)
			}
		}
		if err := checkWrite(req.cond, req.key, currentValidator(b, req.key)); err != nil {
			return committed{}, nil, errConditionalConflict(req.cond, req.key)
		}
		v := s.newVersion(b, completedSeq(b, u), u.Initiated, obj)
		released := b.Objects.add(req.key, v)
		b.Uploads.remove(req.id)
		s.commitObject(w, b, event{key: req.key, versionID: v.ID, change: notify.Created{Action: notify.CreatedCompleteMultipartUpload, Size: obj.Size, ETag: obj.ETag}})
		return committed{version: v, reported: writtenVersion(b, v.ID)}, append(blobsOf(released), u.blobs()...), nil
	}
}

// completedSeq is the Seq a completed upload's version takes. With
// versioning enabled it is the upload's own, taken at Create: of two
// uploads to one key, the one initiated last stays current whichever
// completes last (DOC mpuoverview, "Concurrent multipart upload
// operations"). Otherwise the null version it makes takes a fresh Seq and
// becomes current, as any later write may.
func completedSeq(b *Bucket, u *Upload) uint64 {
	if b.versioning() == versionsEnabled {
		return u.Seq
	}
	return b.nextSeq()
}

// objectLocation is a completed object's URL, the result's Location, in
// the addressing style the request used; clients ignore it (s3.md 10).
func objectLocation(ep kernel.Endpoint, bucket, key string) string {
	path := (&url.URL{Path: "/" + key}).EscapedPath()
	if strings.HasPrefix(ep.Host, bucket+".") {
		return ep.Scheme + "://" + ep.Host + path
	}
	return ep.Scheme + "://" + ep.Host + "/" + bucket + path
}

// partsReader reads the parts' blobs one after another, opening each only
// once the previous one is used up, so a 10,000-part object holds one open
// blob at a time.
type partsReader struct {
	blobs *blob.Store
	refs  []blob.Ref
	cur   io.ReadCloser
}

// Read reads the parts in order, across part boundaries.
func (r *partsReader) Read(p []byte) (int, error) {
	for {
		if r.cur == nil {
			if len(r.refs) == 0 {
				return 0, io.EOF
			}
			rc, err := r.blobs.Open(r.refs[0])
			if err != nil {
				return 0, err
			}
			r.cur, r.refs = rc, r.refs[1:]
		}
		n, err := r.cur.Read(p)
		if err == io.EOF {
			cerr := r.cur.Close()
			r.cur = nil
			if cerr != nil {
				return n, cerr
			}
			if n == 0 {
				continue
			}
			return n, nil
		}
		return n, err
	}
}

// Close closes the blob being read, if any.
func (r *partsReader) Close() error {
	if r.cur == nil {
		return nil
	}
	err := r.cur.Close()
	r.cur = nil
	return err
}

// uploadItem is one entry of an upload listing: an uploadEntry or a
// prefixEntry.
//
//sumtype:decl
type uploadItem interface{ uploadItem() }

// uploadEntry is an upload in progress.
type uploadEntry struct{ upload *Upload }

// prefixEntry is a common prefix that stands for every upload under it.
type prefixEntry struct{ prefix string }

func (uploadEntry) uploadItem() {}
func (prefixEntry) uploadItem() {}

// listMultipartUploads lists the uploads in progress by key, then by
// initiation within a key, after key-marker and upload-id-marker (s3.md
// 9.2). NextKeyMarker and NextUploadIdMarker name the last upload listed,
// truncated or not, and are empty for a page that lists none, even one of
// common prefixes alone (REC test_list_multipart_uploads_marker_common_prefixes
// step list-multiparts-start). Prefix is reported only when sent (REC
// test_list_multiparts_next_marker step list-multiparts-empty).
func listMultipartUploads(in *s3api.ListMultipartUploadsInput, b *Bucket) (*s3api.ListMultipartUploadsOutput, *awserr.Error) {
	limit, err := pageLimit(in.MaxUploads, "max-uploads")
	if err != nil {
		return nil, err
	}
	enc, err := parseEncoding(in.EncodingType)
	if err != nil {
		return nil, err
	}
	keyMarker, idMarker := deref(in.KeyMarker), deref(in.UploadIdMarker)
	if keyMarker == "" {
		// upload-id-marker counts only with key-marker, and is then neither
		// used nor echoed (REC test_list_multiparts_next_marker step
		// list-multiparts-next-upload-only).
		idMarker = ""
	}
	items, err := uploadItems(b.Uploads, listQuery{prefix: deref(in.Prefix), delimiter: deref(in.Delimiter), after: keyMarker, limit: limit}, idMarker)
	if err != nil {
		return nil, err
	}
	truncated := len(items) > limit
	items = items[:min(len(items), limit)]
	out := &s3api.ListMultipartUploadsOutput{
		Bucket: new(b.Name), KeyMarker: new(keyMarker), UploadIdMarker: new(idMarker), Prefix: in.Prefix, Delimiter: in.Delimiter,
		NextKeyMarker: new(""), NextUploadIdMarker: new(""), MaxUploads: count(limit), IsTruncated: new(truncated), EncodingType: enc,
	}
	owner := &s3api.Owner{ID: new(canonicalID(b.Home.OwnerAccount))}
	for _, it := range items {
		switch it := it.(type) {
		case prefixEntry:
			out.CommonPrefixes = append(out.CommonPrefixes, s3api.CommonPrefix{Prefix: new(it.prefix)})
		case uploadEntry:
			u := it.upload
			out.NextKeyMarker, out.NextUploadIdMarker = new(u.Key), new(u.ID)
			m, merr := u.Checksum.mode()
			if merr != nil {
				return nil, awserr.Internalf(merr)
			}
			entry := s3api.MultipartUpload{
				UploadId: new(u.ID), Key: new(u.Key), Initiated: new(seconds(u.Initiated.Wire())), StorageClass: new(u.StorageClass),
				Owner: owner, Initiator: &s3api.Initiator{ID: new(u.Initiator)},
			}
			entry.ChecksumAlgorithm, entry.ChecksumType = declaredOf(m)
			out.Uploads = append(out.Uploads, entry)
		}
	}
	return out, nil
}

// uploadItems lists, after the markers, up to one more item than the page
// holds, so the caller can tell a truncated page; a zero limit lists
// nothing and is not truncated, as the object listings do. idMarker, set
// only with key-marker, must name an upload of the marker key (REC
// test_list_multiparts_next_marker step list-multiparts-wrong-id-for-key):
// the marker key's uploads after it come first.
func uploadItems(u Uploads, q listQuery, idMarker string) ([]uploadItem, *awserr.Error) {
	byKey := map[string][]*Upload{}
	for _, x := range u.ByID {
		byKey[x.Key] = append(byKey[x.Key], x)
	}
	for _, xs := range byKey {
		slices.SortFunc(xs, func(a, b *Upload) int { return cmp.Or(a.Initiated.Compare(b.Initiated), cmp.Compare(a.ID, b.ID)) })
	}
	var items []uploadItem
	if idMarker != "" {
		xs := byKey[q.after]
		i := slices.IndexFunc(xs, func(x *Upload) bool { return x.ID == idMarker })
		if i < 0 {
			return nil, errInvalidArgument("Invalid uploadId marker", "upload-id-marker", idMarker)
		}
		if strings.HasPrefix(q.after, q.prefix) {
			for _, x := range xs[i+1:] {
				items = append(items, uploadEntry{upload: x})
			}
		}
	}
	if q.limit == 0 {
		return nil, nil
	}
	entries, _ := page(slices.Sorted(maps.Keys(byKey)), listQuery{prefix: q.prefix, delimiter: q.delimiter, after: q.after, limit: q.limit + 1})
	for _, e := range entries {
		if len(items) > q.limit {
			break
		}
		if e.prefix {
			items = append(items, prefixEntry{prefix: e.key})
			continue
		}
		for _, x := range byKey[e.key] {
			items = append(items, uploadEntry{upload: x})
		}
	}
	return items, nil
}
