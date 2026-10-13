package s3

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/s3/notify"
)

// bindObjects binds the object operations.
func (s *Service) bindObjects(b s3api.Binder) {
	put := func(in *s3api.PutObjectInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutObject(s.putObject, registry.Partial{Note: "no SSE-C or Object Lock; an object ACL a bucket accepts is not stored; user metadata is stored as sent, without RFC 2047 decoding; chunk signatures are not verified"}, touches(put))
	get := func(in *s3api.GetObjectInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetObject(s.getObject, registry.Partial{Note: "no SSE-C, so SSE-C headers are refused; presigned URLs are not verified"}, touches(get))
	head := func(in *s3api.HeadObjectInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.HeadObject(s.headObject, registry.Partial{Note: "no SSE-C, so SSE-C headers are refused; presigned URLs are not verified"}, touches(head))
	attrs := func(in *s3api.GetObjectAttributesInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetObjectAttributes(reader(s, attrs, getObjectAttributes), registry.Partial{Note: "no SSE-C, so SSE-C headers are refused"}, touches(attrs))
	del := func(in *s3api.DeleteObjectInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.DeleteObject(s.deleteObject, registry.Partial{Note: "no MFA delete or Object Lock retention"}, touches(del))
	dels := func(in *s3api.DeleteObjectsInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.DeleteObjects(s.deleteObjects, registry.Partial{Note: "no MFA delete or Object Lock retention"}, touches(dels))
	cp := func(in *s3api.CopyObjectInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.CopyObject(s.copyObject, registry.Partial{Note: "no SSE-C or Object Lock; an object ACL a bucket accepts is not stored; errors after the copy starts are not sent as 200-with-error"}, touches(cp))
	lv1 := func(in *s3api.ListObjectsInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.ListObjects(reader(s, lv1, listObjects), registry.Partial{Note: "no RestoreStatus"}, touches(lv1))
	lv2 := func(in *s3api.ListObjectsV2Input) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.ListObjectsV2(reader(s, lv2, listObjectsV2), registry.Partial{Note: "no RestoreStatus"}, touches(lv2))
	lvv := func(in *s3api.ListObjectVersionsInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.ListObjectVersions(reader(s, lvv, listObjectVersions), registry.Partial{Note: "no RestoreStatus"}, touches(lvv))
	s.bindObjectTagging(b)
}

// nullVersion is the version id of an object written while the bucket was
// unversioned or versioning was suspended (s3.md 8.3).
const nullVersion = "null"

// Objects is a bucket's keys, each with its versions newest first, by
// Version.Seq. Keys are opaque strings, never paths: an object's bytes
// live in the blob store under their content address.
//
// Unlike the subresource documents, Keys is changed in place, so a write
// costs no copy of the bucket's key set: only inside the bucket's
// kernel.Write, after every check of the write passed, and no read returns
// the map or a chain, so a reader never sees it change (store.Read holds
// the cell's mutex).
type Objects struct {
	Keys map[string][]Version `json:"keys,omitempty"`
}

// Version is one version of a key: an object, or a delete marker when
// Object is nil.
type Version struct {
	// ID is the version id: nullVersion, or a minted 32-character id that
	// carries Seq (mintVersionID).
	ID string `json:"id"`
	// Seq is the version's place in its bucket's order of versions, larger
	// is newer (Bucket.nextSeq).
	Seq uint64 `json:"seq"`
	// Modified is Last-Modified, truncated to the second (s3.md 5.5).
	Modified clock.Instant `json:"modified"`
	Object   *Object       `json:"object,omitempty"`
}

// marker reports a delete marker.
func (v Version) marker() bool { return v.Object == nil }

// Object is the stored state of one object version (s3.md Implications
// 3). A write replaces it whole; a tag change replaces it with a copy.
type Object struct {
	Blob blob.Ref `json:"blob"`
	Size int64    `json:"size"`
	// ETag is the entity tag without quotes: the MD5 hex of the bytes, or
	// random hex for SSE-KMS (s3.md 5.3).
	ETag     string            `json:"etag"`
	Checksum StoredChecksum    `json:"checksum"`
	Header   SystemMetadata    `json:"header"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
	// StorageClass is the class written; STANDARD when the write named none.
	StorageClass s3api.StorageClass `json:"storageClass"`
	Encryption   Encryption         `json:"encryption"`
	// Parts are a multipart object's parts, in part order; empty for an
	// object written in one request.
	Parts []ObjectPart `json:"parts,omitempty"`
}

// ObjectPart is one part of a multipart object: its size and, for a
// COMPOSITE object, its own checksum in wire form, which a partNumber read
// reports (s3.md 5.8).
type ObjectPart struct {
	Size     int64  `json:"size"`
	Checksum string `json:"checksum,omitempty"`
}

// SystemMetadata is the system metadata an object keeps, returned as the
// write sent it (s3.md 5.4).
type SystemMetadata struct {
	ContentType        string  `json:"contentType"`
	CacheControl       *string `json:"cacheControl,omitempty"`
	ContentDisposition *string `json:"contentDisposition,omitempty"`
	ContentEncoding    *string `json:"contentEncoding,omitempty"`
	ContentLanguage    *string `json:"contentLanguage,omitempty"`
	// Expires is the string sent, which S3 returns unparsed.
	Expires         *string `json:"expires,omitempty"`
	WebsiteRedirect *string `json:"websiteRedirect,omitempty"`
}

// validator is what conditional headers are judged against.
func (v Version) validator() validator {
	return validator{etag: v.Object.ETag, lastModified: v.Modified.Wire()}
}

// empty reports a bucket without objects, versions or delete markers,
// which DeleteBucket requires.
func (o *Objects) empty() bool { return len(o.Keys) == 0 }

// blobRefs lists one entry per reference the versions hold on a body: one
// per version that is an object, so versions and copies sharing bytes list
// their blob once each, as many times as the store counts it.
func (o *Objects) blobRefs() []string {
	var refs []string
	for _, key := range slices.Sorted(maps.Keys(o.Keys)) {
		for _, v := range o.Keys[key] {
			if !v.marker() {
				refs = append(refs, v.Object.Blob.String())
			}
		}
	}
	return refs
}

// BlobRefs lists the blob references the bucket's object versions and
// its uploads' parts hold, for the snapshot section of the cell
// (store.Partitioned.Export): an archive then embeds every body, a load
// retains them, and the admin reset releases the references a dropped
// bucket held.
func (c *BucketCell) BlobRefs() []string {
	if c.Bucket == nil {
		return nil
	}
	return append(c.Bucket.Objects.blobRefs(), c.Bucket.Uploads.blobRefs()...)
}

// current returns the newest version of key, which may be a delete
// marker.
func (o *Objects) current(key string) (Version, bool) {
	chain := o.Keys[key]
	if len(chain) == 0 {
		return Version{}, false
	}
	return chain[0], true
}

// live returns key's current object: false when the key has none or its
// newest version is a delete marker.
func (o *Objects) live(key string) (Version, bool) {
	v, ok := o.current(key)
	return v, ok && !v.marker()
}

// version returns the version of key with id.
func (o *Objects) version(key, id string) (Version, bool) {
	i := slices.IndexFunc(o.Keys[key], func(v Version) bool { return v.ID == id })
	if i < 0 {
		return Version{}, false
	}
	return o.Keys[key][i], true
}

// add places v among key's versions by its Seq: ahead of every older
// version, so a version minted by the write is the newest, and a multipart
// object dated by its upload's Seq lands behind the versions of uploads
// initiated after it (s3.md 10). A null version replaces the key's
// previous null version, which it returns as released; versions with
// minted ids accumulate (s3.md 8.1, 8.3).
func (o *Objects) add(key string, v Version) (released *Version) {
	chain := o.Keys[key]
	if v.ID == nullVersion {
		if i := slices.IndexFunc(chain, func(x Version) bool { return x.ID == nullVersion }); i >= 0 {
			old := chain[i]
			released = &old
			chain = slices.Delete(slices.Clone(chain), i, i+1)
		}
	}
	if o.Keys == nil {
		o.Keys = map[string][]Version{}
	}
	at := slices.IndexFunc(chain, func(x Version) bool { return x.Seq < v.Seq })
	if at < 0 {
		at = len(chain)
	}
	o.Keys[key] = slices.Insert(slices.Clone(chain), at, v)
	return released
}

// remove deletes the version of key with id and returns it.
func (o *Objects) remove(key, id string) (Version, bool) {
	chain := o.Keys[key]
	i := slices.IndexFunc(chain, func(v Version) bool { return v.ID == id })
	if i < 0 {
		return Version{}, false
	}
	removed := chain[i]
	rest := slices.Delete(slices.Clone(chain), i, i+1)
	if len(rest) == 0 {
		delete(o.Keys, key)
	} else {
		o.Keys[key] = rest
	}
	return removed, true
}

// replace swaps the record of version v.ID of key for v.
func (o *Objects) replace(key string, v Version) {
	chain := slices.Clone(o.Keys[key])
	if i := slices.IndexFunc(chain, func(x Version) bool { return x.ID == v.ID }); i >= 0 {
		chain[i] = v
		o.Keys[key] = chain
	}
}

// versioning is a bucket's versioning state (s3.md Implications 2).
type versioning uint8

const (
	unversioned versioning = iota + 1
	versionsEnabled
	versionsSuspended
)

// versioning returns the bucket's state; a bucket never versioned is
// unversioned.
func (b *Bucket) versioning() versioning {
	switch {
	case b.Versioning == nil || b.Versioning.Status == nil:
		return unversioned
	case *b.Versioning.Status == s3api.BucketVersioningStatusEnabled:
		return versionsEnabled
	}
	return versionsSuspended
}

// nextSeq takes the bucket's next version Seq. Uploads take theirs at
// initiation, so a completed upload is ordered by when it began.
func (b *Bucket) nextSeq() uint64 {
	b.VersionSeq++
	return b.VersionSeq
}

// newVersion is the version a write makes of obj, or a delete marker when
// obj is nil, at place seq: with a minted id while versioning is enabled,
// null otherwise.
func (s *Service) newVersion(b *Bucket, seq uint64, modified clock.Instant, obj *Object) Version {
	id := nullVersion
	if b.versioning() == versionsEnabled {
		id = mintVersionID(string(s.k.IDs().VersionID()), seq)
	}
	return Version{ID: id, Seq: seq, Modified: clock.FromWire(seconds(modified.Wire())), Object: obj}
}

// versionSeqLen is how many leading characters of a minted version id
// carry its Seq: eleven base-64 digits hold any uint64.
const versionSeqLen = 11

// mintVersionID is a version id that carries seq, so a listing can place
// a version-id-marker whose version was deleted since (versionSeq). Its
// first versionSeqLen characters are seq's base-64 digits, each offset by
// the random character versionSeqLen places after it so the id stays as
// opaque as AWS's; the rest is random's.
func mintVersionID(random string, seq uint64) string {
	id := []byte(random)
	for i := versionSeqLen - 1; i >= 0; i-- {
		mask := strings.IndexByte(versionIDChars, id[i+versionSeqLen])
		id[i] = versionIDChars[(int(seq&63)+mask)&63]
		seq >>= 6
	}
	return string(id)
}

// versionSeq reads the Seq a version id minted by mintVersionID carries;
// false for "null" and for an id not of the minted form. Any id of that
// form decodes, so an id understudy did not mint reads as some Seq.
func versionSeq(id string) (uint64, bool) {
	if checkVersionID(&id) != nil || id == nullVersion {
		return 0, false
	}
	var seq uint64
	for i := range versionSeqLen {
		if seq>>58 != 0 {
			return 0, false
		}
		digit := strings.IndexByte(versionIDChars, id[i]) - strings.IndexByte(versionIDChars, id[i+versionSeqLen])
		seq = seq<<6 | uint64(digit&63)
	}
	return seq, true
}

// reportedVersion is the x-amz-version-id a read, a delete or a tag change
// reports: "null" for a null version once the bucket was ever versioned,
// none on a bucket that never was (tf-remaining.md 8: a "null" there sends
// Terraform down its versioned delete path). AWS reports "null" on reads
// and delete markers of a Suspended bucket (LocalStack recordings
// test_delete_object_on_suspended_bucket, test_put_object_on_suspended_bucket).
func reportedVersion(b *Bucket, id string) *string {
	if id == nullVersion && b.versioning() == unversioned {
		return nil
	}
	return new(id)
}

// writtenVersion is the x-amz-version-id of a write that made an object:
// PutObject, CopyObject and CompleteMultipartUpload leave out a null
// version unless versioning is enabled, so a write to a Suspended bucket
// reports none (s3.md 8.1).
func writtenVersion(b *Bucket, id string) *string {
	if id == nullVersion && b.versioning() != versionsEnabled {
		return nil
	}
	return new(id)
}

// findVersion resolves the version a read addresses (s3.md 5.8): the
// current object without an id, else the id named; only "null" names a
// version on an unversioned bucket. A delete marker addressed by id is 405
// for method.
func findVersion(b *Bucket, key string, id *string, method string) (Version, *awserr.Error) {
	if id == nil {
		v, ok := b.Objects.live(key)
		if !ok {
			return Version{}, errNoSuchKeyAt(b, key)
		}
		return v, nil
	}
	if b.versioning() == unversioned && *id != nullVersion {
		return Version{}, errInvalidVersionID(*id)
	}
	v, ok := b.Objects.version(key, *id)
	switch {
	case !ok:
		return Version{}, errNoSuchVersion(key, *id)
	case v.marker():
		return Version{}, errMarkerAddressed(method, v)
	}
	return v, nil
}

// errNoSuchKeyAt is NoSuchKey for key, with x-amz-delete-marker and the
// marker's x-amz-version-id when its current version is a delete marker
// (s3.md 5.8; LocalStack aws_validated test_delete_object_versioned).
func errNoSuchKeyAt(b *Bucket, key string) *awserr.Error {
	e := errNoSuchKey(key)
	if v, ok := b.Objects.current(key); ok && v.marker() {
		e.Headers = append(e.Headers,
			awserr.Header{Name: "x-amz-delete-marker", Value: "true"},
			awserr.Header{Name: "x-amz-version-id", Value: v.ID})
	}
	return e
}

func errNoSuchVersion(key, id string) *awserr.Error {
	return withField(withField(s3api.ErrNoSuchVersion("The specified version does not exist."), "Key", key), "VersionId", id)
}

// errMarkerAddressed answers a version id that names a delete marker: 405
// with x-amz-delete-marker, the marker's x-amz-version-id, Last-Modified
// and Allow: DELETE, the one method a marker takes (s3.md 5.8; LocalStack
// aws_validated test_delete_object_versioned).
func errMarkerAddressed(method string, v Version) *awserr.Error {
	e := s3api.ErrMethodNotAllowed("The specified method is not allowed against this resource.")
	e = withField(withField(e, "Method", method), "ResourceType", "DeleteMarker")
	e.Headers = append(e.Headers,
		awserr.Header{Name: "x-amz-delete-marker", Value: "true"},
		awserr.Header{Name: "x-amz-version-id", Value: v.ID},
		awserr.Header{Name: "Last-Modified", Value: v.Modified.Wire().UTC().Format(http.TimeFormat)},
		awserr.Header{Name: "Allow", Value: http.MethodDelete})
	return e
}

func errInvalidVersionID(id string) *awserr.Error {
	return errInvalidArgument("Invalid version id specified", "versionId", id)
}

// objectARN is an object's ARN: the encryption context S3 adds.
func objectARN(bucket, key string) string {
	return bucketARN(bucket).String() + "/" + key
}

// objectWrite is a change to a bucket's objects: it changes b.Objects and
// returns the blob refs the change released, which writeObjects releases
// once the write commits, outside the cell lock (ARCHITECTURE.md 8 rule 4).
type objectWrite[R any] func(b *Bucket, w objectTx) (R, []blob.Ref, *awserr.Error)

// objectTx is one attempt of a bucket write as an object change sees it:
// the write's effects, its commit instant, and the call as the change's
// event notifications echo it.
type objectTx struct {
	tx      *kernel.Tx
	now     clock.Instant
	request notify.Request
}

// writeObjects runs fn on the bucket a names once the request is admitted,
// as change does, with the write's Tx for commitObject. landed reports
// that fn's change is in the state, which a non-nil error does not rule
// out: kernel.Write commits first and then answers Internal when an effect
// of the commit fails (a refused delivery, a journal record), so the
// change stands and its released refs are still released here. A caller
// that handed the change a blob reference keeps it only when landed is
// false.
func writeObjects[R any](ctx context.Context, s *Service, c *kernel.Call, a access, fn objectWrite[R]) (_ R, landed bool, _ *awserr.Error) {
	var zero R
	if _, ok := s.buckets.Lookup(a.name); !ok {
		return zero, false, errNoSuchBucket(a.name)
	}
	src := s.regionSource(c, a.name)
	req := requestOf(c)
	type result struct {
		r        R
		released []blob.Ref
		home     aws.Region
		ad       admitted
	}
	// committed is set by the attempt whose fn succeeded: commit applies
	// the state exactly when fn returns nil, and runs fn once per attempt
	// that reaches it.
	var committed *result
	_, err := kernel.Write(c, s.buckets, a.name, func(bc *BucketCell, now clock.Instant, tx *kernel.Tx) (result, *awserr.Error) {
		if bc.Bucket == nil {
			return result{}, s.vanished(tx, a.name)
		}
		ad, err := s.admit(c, src, a, bc.Bucket)
		if err != nil {
			return result{}, err
		}
		next := *bc.Bucket
		r, released, err := fn(&next, objectTx{tx: tx, now: now, request: req})
		if err != nil {
			tx.Unchanged()
			return result{}, inRegion(err, next.Home)
		}
		bc.Bucket = &next
		committed = &result{r: r, released: released, home: next.Home.Region, ad: ad}
		return *committed, nil
	})
	if committed == nil {
		return zero, false, err
	}
	s.release(ctx, committed.released...)
	if err != nil {
		return zero, true, err
	}
	s.warnCrossed(ctx, c, a.name, committed.home, committed.ad)
	return committed.r, true, nil
}

// release drops the references removed object versions held. A failure is
// a bug in the reference counting; the versions are gone either way, so it
// is logged and the request still succeeds.
func (s *Service) release(ctx context.Context, refs ...blob.Ref) {
	for _, ref := range refs {
		if err := s.blobs.Release(ref); err != nil {
			s.log.LogAttrs(ctx, slog.LevelError, "s3 could not release an object body; the bytes stay stored until a restart prunes them",
				slog.String("blob", ref.String()), slog.String("error", err.Error()))
		}
	}
}

// blobsOf returns the blob refs the removed versions held.
func blobsOf(vs ...*Version) []blob.Ref {
	var out []blob.Ref
	for _, v := range vs {
		if v != nil && v.Object != nil {
			out = append(out, v.Object.Blob)
		}
	}
	return out
}

// blobStore returns the store object bodies live in, or Internal when the
// service was built without one.
func (s *Service) blobStore() (*blob.Store, *awserr.Error) {
	if s.blobs == nil {
		return nil, awserr.Internalf(&NoBlobStoreError{})
	}
	return s.blobs, nil
}

// NoBlobStoreError is an object operation on a service built without a
// blob store: kernel.ServiceDeps.Blob was nil.
type NoBlobStoreError struct{}

func (*NoBlobStoreError) Error() string {
	return "s3: the service has no blob store for object bodies; pass kernel.ServiceDeps.Blob to s3.New"
}

// openedBody is the bytes a GetObject serves as a streaming output, opened
// from the blob store while the read still held a reference on the blob:
// an open reader outlives the blob's removal (blob.Store), so a concurrent
// overwrite or delete that releases the version's last reference cannot
// fail a read already admitted, and no body is held in memory whole. The
// restXml encoder takes the whole body once through Range and closes it; a
// body it never takes, because a fault, a contract check or a failed
// encode dropped the output, is closed through Close by kernel.Release.
type openedBody struct {
	rc    io.ReadCloser
	n     int64
	taken bool
}

// Size returns the bytes the response carries.
func (b *openedBody) Size() int64 { return b.n }

// Range hands over the opened reader: once, for the whole body.
func (b *openedBody) Range(off, n int64) (io.ReadCloser, error) {
	if b.taken || off != 0 || n != b.n {
		return nil, &BodyRangeError{Offset: off, Length: n, Size: b.n, Taken: b.taken}
	}
	b.taken = true
	return b.rc, nil
}

// Close closes the opened reader unless Range handed it over, after which
// it is the encoder's to close. A second Close does nothing.
func (b *openedBody) Close() error {
	if b.taken {
		return nil
	}
	b.taken = true
	return b.rc.Close()
}

// BodyRangeError is a GetObject body asked for other than once and whole:
// the encoder's contract with openedBody changed, a bug.
type BodyRangeError struct {
	Offset, Length, Size int64
	// Taken reports that the body was handed over before.
	Taken bool
}

func (e *BodyRangeError) Error() string {
	if e.Taken {
		return "s3: the GetObject body was already handed to the encoder; it streams once"
	}
	return fmt.Sprintf("s3: the GetObject body serves its %d bytes whole; %d bytes at offset %d were asked for", e.Size, e.Length, e.Offset)
}

// commitObject is the one place an object change lands in its bucket: the
// object operations call it inside the bucket's kernel.Write, after every
// check and after changing b.Objects, and it raises the change's event
// notifications in the same commit, so a notification never reports a
// change that did not happen.
func (s *Service) commitObject(w objectTx, b *Bucket, ev event) {
	s.notify(w, b, ev)
}
