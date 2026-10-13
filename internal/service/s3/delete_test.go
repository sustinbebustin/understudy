package s3_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/blob"
)

// enableVersioning turns the bucket's versioning on.
func (h *harness) enableVersioning(bucket string) {
	h.Helper()
	ok(h, who{}, s3api.OpPutBucketVersioning, &s3api.PutBucketVersioningInput{
		Bucket: bucket, VersioningConfiguration: s3api.VersioningConfiguration{Status: new(s3api.BucketVersioningStatusEnabled)},
	})
}

// TestDeleteUnversioned: DeleteObject answers alike for a present and a
// missing key, and only "null" is a version id (s3.md 8.1); the emptied
// bucket can be deleted.
func TestDeleteUnversioned(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("plain")
		h.put("plain", "k", "v")
		for range 2 {
			out := ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "plain", Key: "k"})
			if out.DeleteMarker != nil || out.VersionId != nil {
				t.Fatalf("DeleteObject on an unversioned bucket = %+v, want no marker or version", out)
			}
		}
		_, err := as(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "plain", Key: "k", VersionId: new("x")})
		wantCode(t, err, "InvalidArgument", http.StatusBadRequest)
		ok(h, who{}, s3api.OpDeleteBucket, &s3api.DeleteBucketInput{Bucket: "plain"})
	})
}

// TestVersionedDeleteMarkers: with versioning on, a plain delete adds a
// delete marker, the key then reads as NoSuchKey with x-amz-delete-marker,
// the marker addressed by id is 405, the old version is still readable by
// id, and deleting the marker by id brings the object back (s3.md 5.8,
// 8.1); a non-empty bucket cannot be deleted.
func TestVersionedDeleteMarkers(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("versioned")
		h.enableVersioning("versioned")
		first := *h.put("versioned", "k", "one").VersionId
		second := *h.put("versioned", "k", "two").VersionId
		if first == second || len(first) != 32 {
			t.Fatalf("version ids %q and %q, want two distinct 32-character ids", first, second)
		}
		del := ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "versioned", Key: "k"})
		if del.DeleteMarker == nil || !*del.DeleteMarker || del.VersionId == nil {
			t.Fatalf("DeleteObject = %+v, want a delete marker with a version id", del)
		}
		_, err := as(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "versioned", Key: "k"})
		wantCode(t, err, "NoSuchKey", http.StatusNotFound)
		if header(err, "x-amz-delete-marker") != "true" || header(err, "x-amz-version-id") != *del.VersionId {
			t.Fatalf("NoSuchKey behind a delete marker = %v, want x-amz-delete-marker and the marker's x-amz-version-id %s", err.Headers, *del.VersionId)
		}
		_, err = as(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "versioned", Key: "k", VersionId: del.VersionId})
		wantCode(t, err, "MethodNotAllowed", http.StatusMethodNotAllowed)
		if header(err, "x-amz-delete-marker") != "true" || header(err, "x-amz-version-id") != *del.VersionId || header(err, "Allow") != "DELETE" {
			t.Fatalf("405 for a delete marker = %v, want x-amz-delete-marker, x-amz-version-id %s and Allow: DELETE", err.Headers, *del.VersionId)
		}
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "versioned", Key: "k", VersionId: new(first)}); got != "one" {
			t.Fatalf("GetObject of the first version = %q", got)
		}
		_, err = as(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "versioned", Key: "k", VersionId: new("gS53zabD7XTvkrwbjMnXlBylVWetO8ym")})
		wantCode(t, err, "NoSuchVersion", http.StatusNotFound)
		ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "versioned", Key: "k", VersionId: del.VersionId})
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "versioned", Key: "k"}); got != "two" {
			t.Fatalf("GetObject after removing the marker = %q, want the newest version", got)
		}
		_, err = as(h, who{}, s3api.OpDeleteBucket, &s3api.DeleteBucketInput{Bucket: "versioned"})
		wantCode(t, err, "BucketNotEmpty", http.StatusConflict)
		// AWS-recorded for a versioned bucket (s3.md 3).
		if want := "The bucket you tried to delete is not empty. You must delete all versions in the bucket."; err.Message != want {
			t.Fatalf("DeleteBucket of a versioned bucket = %q, want %q", err.Message, want)
		}
		h.createBucket("plain")
		h.put("plain", "k", "v")
		_, err = as(h, who{}, s3api.OpDeleteBucket, &s3api.DeleteBucketInput{Bucket: "plain"})
		wantCode(t, err, "BucketNotEmpty", http.StatusConflict)
		if want := "The bucket you tried to delete is not empty"; err.Message != want {
			t.Fatalf("DeleteBucket of an unversioned bucket = %q, want %q", err.Message, want)
		}

		listed := ok(h, who{}, s3api.OpListObjectVersions, &s3api.ListObjectVersionsInput{Bucket: "versioned", Prefix: new("k")})
		if len(listed.Versions) != 2 || *listed.Versions[0].VersionId != second || !*listed.Versions[0].IsLatest || *listed.Versions[1].IsLatest {
			t.Fatalf("ListObjectVersions = %+v, want the two versions newest first", listed.Versions)
		}
	})
}

// TestDeleteUnknownVersion: on a versioned bucket, a well-formed version
// id that names no version of an existing key is InvalidArgument with no
// x-amz-version-id, and deletes nothing; on a key with no versions it is
// a 204 (LocalStack aws_validated test_delete_object_versioned, step
// delete-with-bad-version; s3.md 8.1).
func TestDeleteUnknownVersion(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("versioned")
		h.enableVersioning("versioned")
		v := *h.put("versioned", "k", "one").VersionId
		bad := v[:len(v)-4] + "ABCD"
		_, err := as(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "versioned", Key: "k", VersionId: new(bad)})
		wantCode(t, err, "InvalidArgument", http.StatusBadRequest)
		if err.Message != "Invalid version id specified" || header(err, "x-amz-version-id") != "" {
			t.Fatalf("DeleteObject of an unknown version = %q headers %v, want \"Invalid version id specified\" without x-amz-version-id", err.Message, err.Headers)
		}
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "versioned", Key: "k", VersionId: new(v)}); got != "one" {
			t.Fatalf("GetObject after the refused delete = %q, want the version kept", got)
		}
		ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "versioned", Key: "missing", VersionId: new(bad)})
	})
}

// TestSuspendedVersioningReplacesNullVersion: while versioning is
// suspended a put writes the null version, reporting no version id, and
// replaces the previous null version, whose body is released; versions
// written while it was enabled stay (s3.md 8.1, 8.3).
func TestSuspendedVersioningReplacesNullVersion(t *testing.T) {
	runObjectsWith(t, func(h *harness, blobs *blob.Store) {
		h.createBucket("suspended")
		h.enableVersioning("suspended")
		kept := *h.put("suspended", "k", "enabled").VersionId
		ok(h, who{}, s3api.OpPutBucketVersioning, &s3api.PutBucketVersioningInput{
			Bucket: "suspended", VersioningConfiguration: s3api.VersioningConfiguration{Status: new(s3api.BucketVersioningStatusSuspended)},
		})
		for _, content := range []string{"first null", "second null"} {
			if out := h.put("suspended", "k", content); out.VersionId != nil {
				t.Fatalf("PutObject while suspended reports version %q, want none", *out.VersionId)
			}
		}
		listed := ok(h, who{}, s3api.OpListObjectVersions, &s3api.ListObjectVersionsInput{Bucket: "suspended"})
		ids := make([]string, 0, len(listed.Versions))
		for _, v := range listed.Versions {
			ids = append(ids, *v.VersionId)
		}
		if !slices.Equal(ids, []string{"null", kept}) || !*listed.Versions[0].IsLatest {
			t.Fatalf("ListObjectVersions = %v, want the null version (latest) then %s", ids, kept)
		}
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "suspended", Key: "k", VersionId: new("null")}); got != "second null" {
			t.Fatalf("GetObject versionId=null = %q, want the second null write", got)
		}
		if refs := blobs.Refs(); len(refs) != 2 {
			t.Fatalf("blob store holds %d bodies, want 2: the replaced null version's body released", len(refs))
		}
	})
}

// TestSuspendedVersioningReportsNullOnReads: on a Suspended bucket a put
// reports no version id, but a read of the null version and a delete
// marker report "null" (LocalStack aws_validated snapshots
// test_put_object_on_suspended_bucket, test_delete_object_on_suspended_bucket).
func TestSuspendedVersioningReportsNullOnReads(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("suspended")
		h.enableVersioning("suspended")
		ok(h, who{}, s3api.OpPutBucketVersioning, &s3api.PutBucketVersioningInput{
			Bucket: "suspended", VersioningConfiguration: s3api.VersioningConfiguration{Status: new(s3api.BucketVersioningStatusSuspended)},
		})
		if out := h.put("suspended", "k", "v"); out.VersionId != nil {
			t.Fatalf("PutObject while suspended reports version %q, want none", *out.VersionId)
		}
		if _, got := h.read(&s3api.GetObjectInput{Bucket: "suspended", Key: "k"}); got.VersionId == nil || *got.VersionId != "null" {
			t.Fatalf("GetObject of the null version reports version %v, want null", got.VersionId)
		}
		if head := ok(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "suspended", Key: "k"}); head.VersionId == nil || *head.VersionId != "null" {
			t.Fatalf("HeadObject of the null version reports version %v, want null", head.VersionId)
		}
		del := ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "suspended", Key: "k"})
		if del.DeleteMarker == nil || !*del.DeleteMarker || del.VersionId == nil || *del.VersionId != "null" {
			t.Fatalf("DeleteObject while suspended = marker %v, version %v; want a null delete marker", del.DeleteMarker, del.VersionId)
		}

		h.createBucket("plain")
		h.put("plain", "k", "v")
		if _, got := h.read(&s3api.GetObjectInput{Bucket: "plain", Key: "k"}); got.VersionId != nil {
			t.Fatalf("GetObject on a bucket never versioned reports version %q, want none", *got.VersionId)
		}
	})
}

// TestVersionListingResumesAfterDeletedMarker is how Terraform's
// force_destroy empties a bucket: it deletes each ListObjectVersions page
// before it asks for the next, so the next page's version-id-marker names
// a version already gone. The listing resumes at the key's next older
// version, as AWS does (LocalStack aws_validated snapshot
// test_list_objects_versions_with_prefix_only_and_pagination), and the
// bucket then empties.
func TestVersionListingResumesAfterDeletedMarker(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("drain")
		h.enableVersioning("drain")
		contents := []string{"one", "two", "three", "four"}
		ids := make([]string, 0, len(contents))
		for _, content := range contents {
			ids = append(ids, *h.put("drain", "k", content).VersionId)
		}
		in := s3api.ListObjectVersionsInput{Bucket: "drain", MaxKeys: new(int32(2))}
		var listed []string
		for range 4 {
			page := ok(h, who{}, s3api.OpListObjectVersions, &in)
			for _, v := range page.Versions {
				listed = append(listed, *v.VersionId)
				ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "drain", Key: "k", VersionId: v.VersionId})
			}
			if !*page.IsTruncated {
				break
			}
			in.KeyMarker, in.VersionIdMarker = page.NextKeyMarker, page.NextVersionIdMarker
		}
		if want := []string{ids[3], ids[2], ids[1], ids[0]}; !slices.Equal(listed, want) {
			t.Fatalf("versions listed while draining = %v, want %v", listed, want)
		}
		ok(h, who{}, s3api.OpDeleteBucket, &s3api.DeleteBucketInput{Bucket: "drain"})
	})
}

// TestConditionalDeletes: If-Match deletes only a current object carrying
// the ETag ("*" any); a key whose current version is a delete marker fails
// the condition with 412, and a key without versions is NoSuchKey, alike in
// DeleteObject and as a DeleteObjects entry's ETag (s3.md 5.10, DOC
// conditional-deletes).
func TestConditionalDeletes(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("cond-delete")
		h.enableVersioning("cond-delete")
		etag := *h.put("cond-delete", "live", "v").ETag
		h.put("cond-delete", "marked", "v")
		ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "cond-delete", Key: "marked"})

		_, err := as(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "cond-delete", Key: "live", IfMatch: new(`"other"`)})
		wantCode(t, err, "PreconditionFailed", http.StatusPreconditionFailed)
		for _, ifMatch := range []string{"*", etag} {
			_, err = as(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "cond-delete", Key: "marked", IfMatch: new(ifMatch)})
			wantCode(t, err, "PreconditionFailed", http.StatusPreconditionFailed)
			wantFields(t, err, map[string]string{"Condition": "If-Match"})
		}
		_, err = as(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "cond-delete", Key: "never", IfMatch: new("*")})
		wantCode(t, err, "NoSuchKey", http.StatusNotFound)

		out := ok(h, who{}, s3api.OpDeleteObjects, &s3api.DeleteObjectsInput{Bucket: "cond-delete", Delete: s3api.Delete{Objects: []s3api.ObjectIdentifier{
			{Key: "marked", ETag: new("*")}, {Key: "never", ETag: new("*")}, {Key: "live", ETag: new(etag)},
		}}})
		codes := map[string]string{}
		for _, e := range out.Errors {
			codes[*e.Key] = *e.Code
		}
		if len(out.Deleted) != 1 || *out.Deleted[0].Key != "live" || codes["marked"] != "PreconditionFailed" || codes["never"] != "NoSuchKey" {
			t.Fatalf("conditional DeleteObjects = deleted %+v, errors %v; want live deleted, marked PreconditionFailed, never NoSuchKey", out.Deleted, codes)
		}
	})
}

// TestDeleteObjects: per-key results, missing keys reported as deleted,
// Quiet keeping only errors, and a bogus version id as a NoSuchVersion
// entry under a 200 (s3.md 8.2).
func TestDeleteObjects(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("batch")
		h.enableVersioning("batch")
		h.put("batch", "a", "1")
		out := ok(h, who{}, s3api.OpDeleteObjects, &s3api.DeleteObjectsInput{Bucket: "batch", Delete: s3api.Delete{Objects: []s3api.ObjectIdentifier{
			{Key: "a"}, {Key: "missing"}, {Key: "a", VersionId: new("gS53zabD7XTvkrwbjMnXlBylVWetO8ym")},
		}}})
		if len(out.Deleted) != 2 || len(out.Errors) != 1 {
			t.Fatalf("DeleteObjects = %d deleted, %d errors; want 2 and 1", len(out.Deleted), len(out.Errors))
		}
		if d := out.Deleted[0]; d.DeleteMarker == nil || !*d.DeleteMarker || d.DeleteMarkerVersionId == nil {
			t.Fatalf("Deleted entry of a versioned key = %+v, want a delete marker and its id", d)
		}
		if e := out.Errors[0]; *e.Code != "NoSuchVersion" || *e.Message != "The specified version does not exist." {
			t.Fatalf("Error entry = %s %s", *e.Code, *e.Message)
		}
		quiet := ok(h, who{}, s3api.OpDeleteObjects, &s3api.DeleteObjectsInput{Bucket: "batch", Delete: s3api.Delete{Quiet: new(true), Objects: []s3api.ObjectIdentifier{{Key: "a"}}}})
		if len(quiet.Deleted) != 0 || len(quiet.Errors) != 0 {
			t.Fatalf("quiet DeleteObjects = %+v, want an empty result", quiet)
		}
		_, err := as(h, who{}, s3api.OpDeleteObjects, &s3api.DeleteObjectsInput{Bucket: "batch", Delete: s3api.Delete{Objects: []s3api.ObjectIdentifier{}}})
		wantCode(t, err, "MalformedXML", http.StatusBadRequest)
	})
}

// TestDeleteObjectsWrongVersionUnversioned: on a bucket never versioned, a
// well-formed version id naming no version of a key is a NoSuchVersion
// entry carrying the key and id, and the object stays (LocalStack
// aws_validated TestS3ObjectCRUD::test_delete_objects, step
// delete-object-wrong-version-id).
func TestDeleteObjectsWrongVersionUnversioned(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("plain")
		h.put("plain", "test-delete", "body")
		id := "HPniJFCxqTsMuIH9KX8K8wEjNUgmABCD"
		out := ok(h, who{}, s3api.OpDeleteObjects, &s3api.DeleteObjectsInput{Bucket: "plain", Delete: s3api.Delete{Objects: []s3api.ObjectIdentifier{
			{Key: "test-delete", VersionId: new(id)},
		}}})
		if len(out.Deleted) != 0 || len(out.Errors) != 1 {
			t.Fatalf("DeleteObjects = %d deleted, %d errors; want 0 and 1", len(out.Deleted), len(out.Errors))
		}
		if e := out.Errors[0]; *e.Code != "NoSuchVersion" || *e.Message != "The specified version does not exist." || *e.Key != "test-delete" || e.VersionId == nil || *e.VersionId != id {
			t.Fatalf("Error entry = %s %s key %s version %v; want NoSuchVersion for test-delete %s", *e.Code, *e.Message, *e.Key, e.VersionId, id)
		}
		ok(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "plain", Key: "test-delete"})
	})
}
