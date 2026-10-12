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
		if header(err, "x-amz-delete-marker") != "true" {
			t.Fatalf("NoSuchKey behind a delete marker lacks x-amz-delete-marker: %v", err.Headers)
		}
		_, err = as(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "versioned", Key: "k", VersionId: del.VersionId})
		wantCode(t, err, "MethodNotAllowed", http.StatusMethodNotAllowed)
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
