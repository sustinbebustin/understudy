package s3

import (
	"context"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/s3/notify"
)

// maxDeleteKeys is the most keys one DeleteObjects names (s3.md 8.2).
const maxDeleteKeys = 1000

// versionIDLen and versionIDChars are the form of a minted version id
// (s3.md 8.1).
const (
	versionIDLen   = 32
	versionIDChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._"
)

// deletion is what one key's delete asks for.
type deletion struct {
	key       string
	versionID *string
	// ifMatch is If-Match, or a DeleteObjects entry's ETag.
	ifMatch *string
}

// deleted is what one key's delete did (s3.md 8.1).
type deleted struct {
	// versionID is the version removed, or the delete marker created.
	versionID *string
	marker    bool
}

func (s *Service) deleteObject(ctx context.Context, c *kernel.Call, in *s3api.DeleteObjectInput) (*s3api.DeleteObjectOutput, *awserr.Error) {
	switch {
	case in.IfMatchLastModifiedTime != nil:
		return nil, withField(s3api.ErrNotImplemented(notImplementedHeader), "Header", "x-amz-if-match-last-modified-time")
	case in.IfMatchSize != nil:
		return nil, withField(s3api.ErrNotImplemented(notImplementedHeader), "Header", "x-amz-if-match-size")
	}
	d := deletion{key: in.Key, versionID: in.VersionId, ifMatch: in.IfMatch}
	if err := checkVersionID(d.versionID); err != nil {
		return nil, err
	}
	got, _, err := writeObjects(ctx, s, c, at(in.Bucket, in.ExpectedBucketOwner), func(b *Bucket, w objectTx) (deleted, []blob.Ref, *awserr.Error) {
		return s.deleteOne(b, d, w)
	})
	if err != nil {
		return nil, err
	}
	out := &s3api.DeleteObjectOutput{VersionId: got.versionID}
	if got.marker {
		out.DeleteMarker = new(true)
	}
	return out, nil
}

func (s *Service) deleteObjects(ctx context.Context, c *kernel.Call, in *s3api.DeleteObjectsInput) (*s3api.DeleteObjectsOutput, *awserr.Error) {
	if n := len(in.Delete.Objects); n == 0 || n > maxDeleteKeys {
		return nil, errMalformedXML()
	}
	quiet := in.Delete.Quiet != nil && *in.Delete.Quiet
	got, _, err := writeObjects(ctx, s, c, at(in.Bucket, in.ExpectedBucketOwner), func(b *Bucket, w objectTx) (*s3api.DeleteObjectsOutput, []blob.Ref, *awserr.Error) {
		out := &s3api.DeleteObjectsOutput{}
		var released []blob.Ref
		for _, o := range in.Delete.Objects {
			got, refs, err := s.deleteEntry(b, o, w)
			if err != nil {
				out.Errors = append(out.Errors, s3api.Error{Key: new(o.Key), VersionId: o.VersionId, Code: new(err.ID.QueryCode), Message: new(err.Message)})
				continue
			}
			released = append(released, refs...)
			if quiet {
				continue
			}
			entry := s3api.DeletedObject{Key: new(o.Key)}
			switch {
			case o.VersionId != nil:
				entry.VersionId = o.VersionId
				if got.marker {
					entry.DeleteMarker, entry.DeleteMarkerVersionId = new(true), o.VersionId
				}
			case got.marker:
				entry.DeleteMarker, entry.DeleteMarkerVersionId = new(true), got.versionID
			}
			out.Deleted = append(out.Deleted, entry)
		}
		return out, released, nil
	})
	return got, err
}

// deleteEntry deletes one entry of a DeleteObjects; its error goes into
// the response's Error list instead of failing the request (s3.md 8.2). A
// version id naming no version of the key is NoSuchVersion whatever the
// bucket's versioning, where DeleteObject answers InvalidArgument (LocalStack
// aws_validated test_delete_objects, step delete-object-wrong-version-id);
// only "null" on a bucket never versioned falls through to deleteOne.
func (s *Service) deleteEntry(b *Bucket, o s3api.ObjectIdentifier, w objectTx) (deleted, []blob.Ref, *awserr.Error) {
	switch {
	case o.LastModifiedTime != nil:
		return deleted{}, nil, withField(s3api.ErrNotImplemented(notImplementedHeader), "Header", "LastModifiedTime")
	case o.Size != nil:
		return deleted{}, nil, withField(s3api.ErrNotImplemented(notImplementedHeader), "Header", "Size")
	}
	if err := checkVersionID(o.VersionId); err != nil {
		return deleted{}, nil, err
	}
	d := deletion{key: o.Key, versionID: o.VersionId, ifMatch: o.ETag}
	if o.VersionId != nil {
		if _, ok := b.Objects.version(o.Key, *o.VersionId); !ok && (*o.VersionId != nullVersion || b.versioning() != unversioned) {
			return deleted{}, nil, errNoSuchVersion(o.Key, *o.VersionId)
		}
	}
	return s.deleteOne(b, d, w)
}

// deleteOne applies s3.md 8.1 to one key inside the bucket's write: a
// version id removes that version for good; without one, an unversioned
// bucket drops the object, and a versioned one gains a delete marker (a
// null one while suspended, which replaces the null version).
func (s *Service) deleteOne(b *Bucket, d deletion, w objectTx) (deleted, []blob.Ref, *awserr.Error) {
	if d.ifMatch != nil {
		if err := checkDeleteMatch(b, d.key, *d.ifMatch); err != nil {
			return deleted{}, nil, err
		}
	}
	if d.versionID != nil {
		return s.deleteVersion(b, d.key, *d.versionID, w)
	}
	if b.versioning() == unversioned {
		removed, ok := b.Objects.remove(d.key, nullVersion)
		if ok {
			s.commitObject(w, b, event{key: d.key, change: notify.Removed{Action: notify.RemovedDelete}})
		}
		return deleted{}, blobsOf(&removed), nil
	}
	marker := s.newVersion(b, b.nextSeq(), w.now, nil)
	released := b.Objects.add(d.key, marker)
	s.commitObject(w, b, event{key: d.key, versionID: marker.ID, change: notify.Removed{Action: notify.RemovedDeleteMarkerCreated}})
	return deleted{versionID: reportedVersion(b, marker.ID), marker: true}, blobsOf(released), nil
}

// deleteVersion removes version id of key for good. An id naming none of
// an existing key's versions is InvalidArgument; on a key with no versions
// it deletes nothing with 204 (LocalStack aws_validated
// test_delete_object_versioned, step delete-with-bad-version).
func (s *Service) deleteVersion(b *Bucket, key, id string, w objectTx) (deleted, []blob.Ref, *awserr.Error) {
	if b.versioning() == unversioned && id != nullVersion {
		return deleted{}, nil, errInvalidVersionID(id)
	}
	removed, ok := b.Objects.remove(key, id)
	if !ok {
		if _, has := b.Objects.current(key); has {
			return deleted{}, nil, errInvalidVersionID(id)
		}
		return deleted{versionID: &id}, nil, nil
	}
	s.commitObject(w, b, event{key: key, versionID: removed.ID, change: notify.Removed{Action: notify.RemovedDelete}})
	return deleted{versionID: &id, marker: removed.marker()}, blobsOf(&removed), nil
}

// checkDeleteMatch applies a conditional delete's If-Match: the current
// object must exist ("*") or carry the ETag (s3.md 5.10). A key without
// versions is NoSuchKey; one whose current version is a delete marker
// fails the condition (DOC conditional-deletes).
func checkDeleteMatch(b *Bucket, key, ifMatch string) *awserr.Error {
	cur, ok := b.Objects.current(key)
	switch {
	case !ok:
		return errNoSuchKey(key)
	case cur.marker():
		return errPreconditionFailed(headerIfMatch)
	case !etagListMatches(ifMatch, cur.Object.ETag):
		return errPreconditionFailed(headerIfMatch)
	}
	return nil
}

// checkVersionID refuses a version id that is neither "null" nor of the
// minted form (s3.md 8.1).
func checkVersionID(id *string) *awserr.Error {
	if id == nil || *id == nullVersion {
		return nil
	}
	if len(*id) != versionIDLen || strings.Trim(*id, versionIDChars) != "" {
		return errInvalidVersionID(*id)
	}
	return nil
}
