package s3_test

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
)

// statusOf is the status a call answered: 200, or its error's.
func statusOf(err *awserr.Error) int {
	if err == nil {
		return http.StatusOK
	}
	return err.ID.Status
}

// conditionOf is the Condition element of a 412, or "".
func conditionOf(err *awserr.Error) string { return fieldValue(err, "Condition") }

// fieldValue is an error's context element name, or "" for none or no
// error.
func fieldValue(err *awserr.Error, name string) string {
	if err == nil {
		return ""
	}
	for _, f := range err.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}

// TestConditionalReads is the recorded GetObject and HeadObject table of
// s3.md 5.10 through both operations: 412 PreconditionFailed naming the
// condition, 304 Not Modified, a future If-Modified-Since ignored, and the
// DOC combinations of RFC 9110's evaluation order.
func TestConditionalReads(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("cond")
		etag := *h.put("cond", "k", "content").ETag
		modified := h.Clock.Now().Wire().Truncate(time.Second)
		before, future := modified.Add(-time.Hour), modified.Add(24*time.Hour)
		type conds struct {
			ifMatch, ifNoneMatch               *string
			ifModifiedSince, ifUnmodifiedSince *time.Time
		}
		for _, tc := range []struct {
			name      string
			c         conds
			status    int
			condition string
		}{
			{"If-Match equal", conds{ifMatch: new(etag)}, http.StatusOK, ""},
			{"If-Match differs", conds{ifMatch: new(`"other"`)}, http.StatusPreconditionFailed, "If-Match"},
			{"If-Unmodified-Since earlier", conds{ifUnmodifiedSince: &before}, http.StatusPreconditionFailed, "If-Unmodified-Since"},
			{"If-Unmodified-Since equal", conds{ifUnmodifiedSince: &modified}, http.StatusOK, ""},
			{"If-None-Match equal", conds{ifNoneMatch: new(etag)}, http.StatusNotModified, ""},
			{"If-None-Match other", conds{ifNoneMatch: new(`"other"`)}, http.StatusOK, ""},
			{"If-Modified-Since equal", conds{ifModifiedSince: &modified}, http.StatusNotModified, ""},
			{"If-Modified-Since earlier", conds{ifModifiedSince: &before}, http.StatusOK, ""},
			{"If-Modified-Since in the future", conds{ifModifiedSince: &future}, http.StatusOK, ""},
			{"If-Match true, If-Unmodified-Since false", conds{ifMatch: new(etag), ifUnmodifiedSince: &before}, http.StatusOK, ""},
			{"If-None-Match false, If-Modified-Since true", conds{ifNoneMatch: new(`"other"`), ifModifiedSince: &modified}, http.StatusOK, ""},
		} {
			_, gerr := as(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{
				Bucket: "cond", Key: "k", IfMatch: tc.c.ifMatch, IfNoneMatch: tc.c.ifNoneMatch, IfModifiedSince: tc.c.ifModifiedSince, IfUnmodifiedSince: tc.c.ifUnmodifiedSince,
			})
			_, herr := as(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{
				Bucket: "cond", Key: "k", IfMatch: tc.c.ifMatch, IfNoneMatch: tc.c.ifNoneMatch, IfModifiedSince: tc.c.ifModifiedSince, IfUnmodifiedSince: tc.c.ifUnmodifiedSince,
			})
			for op, err := range map[string]*awserr.Error{"GetObject": gerr, "HeadObject": herr} {
				if statusOf(err) != tc.status || conditionOf(err) != tc.condition {
					t.Errorf("%s: %s = %d %q, want %d %q", tc.name, op, statusOf(err), conditionOf(err), tc.status, tc.condition)
				}
			}
		}
	})
}

// TestRangeReads: the three range forms with Content-Range, clipping past
// the end, a start past the end or any range of an empty object as 416
// InvalidRange with ActualObjectSize and RangeRequested, and a malformed
// header ignored (s3.md 5.9).
func TestRangeReads(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("ranges")
		h.put("ranges", "k", "0123456789")
		h.put("ranges", "empty", "")
		for _, tc := range []struct {
			spec, body, contentRange string
		}{
			{"bytes=2-4", "234", "bytes 2-4/10"},
			{"bytes=7-", "789", "bytes 7-9/10"},
			{"bytes=-3", "789", "bytes 7-9/10"},
			{"bytes=8-100", "89", "bytes 8-9/10"},
			{"bytes=-100", "0123456789", "bytes 0-9/10"},
			{"bytes=5-2", "0123456789", ""},
			{"items=0-1", "0123456789", ""},
			{"bytes=0-1,4-5", "0123456789", ""},
		} {
			got, out := h.read(&s3api.GetObjectInput{Bucket: "ranges", Key: "k", Range: new(tc.spec)})
			cr := ""
			if out.ContentRange != nil {
				cr = *out.ContentRange
			}
			if got != tc.body || cr != tc.contentRange || *out.ContentLength != int64(len(tc.body)) {
				t.Errorf("Range %s = %q, Content-Range %q, length %d; want %q, %q", tc.spec, got, cr, *out.ContentLength, tc.body, tc.contentRange)
			}
		}
		for key, spec := range map[string]string{"k": "bytes=10-20", "empty": "bytes=0-1"} {
			_, err := as(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "ranges", Key: key, Range: new(spec)})
			wantCode(t, err, "InvalidRange", http.StatusRequestedRangeNotSatisfiable)
			if err.Message != "The requested range is not satisfiable" || fieldValue(err, "RangeRequested") != spec {
				t.Errorf("%s %s: %s with RangeRequested %q", key, spec, err.Message, fieldValue(err, "RangeRequested"))
			}
		}
		_, err := as(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "ranges", Key: "k", Range: new("bytes=0-1"), PartNumber: new(int32(1))})
		wantCode(t, err, "InvalidRequest", http.StatusBadRequest)
		_, err = as(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "ranges", Key: "k", PartNumber: new(int32(2))})
		wantCode(t, err, "InvalidPartNumber", http.StatusRequestedRangeNotSatisfiable)
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "ranges", Key: "k", PartNumber: new(int32(1))}); got != "0123456789" {
			t.Errorf("partNumber=1 of a single-part object = %q, want the whole object", got)
		}
	})
}

// TestReadSurvivesOverwrite: a GetObject admitted while the object existed
// streams its bytes even when an overwrite and a delete of the key release
// the version's last reference before the response body is read, as S3
// serves the old bytes; this holds for a body kept in memory and one
// spilled to disk. The released bytes are gone once the read is done.
func TestReadSurvivesOverwrite(t *testing.T) {
	runObjectsWith(t, func(h *harness, blobs *blob.Store) {
		h.createBucket("overwrite")
		for name, old := range map[string]string{"in memory": "old bytes", "on disk": strings.Repeat("o", 2<<20)} {
			h.put("overwrite", "k", old)
			out := ok(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "overwrite", Key: "k", Range: new("bytes=1-")})
			h.put("overwrite", "k", "new bytes")
			ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "overwrite", Key: "k"})
			rc, err := out.Body.Range(0, out.Body.Size())
			if err != nil {
				t.Fatalf("%s: opening the body after the overwrite: %v", name, err)
			}
			got, err := io.ReadAll(rc)
			if cerr := rc.Close(); err != nil || cerr != nil {
				t.Fatalf("%s: reading the body after the overwrite: %v, %v", name, err, cerr)
			}
			if string(got) != old[1:] {
				t.Fatalf("%s: GetObject body after the overwrite = %d bytes, want the old object's last %d", name, len(got), len(old)-1)
			}
			if refs := blobs.Refs(); len(refs) != 0 {
				t.Fatalf("%s: blob store holds %v after the key was deleted and the read finished, want nothing", name, refs)
			}
		}
	})
}

// TestArchivedObjects: a GLACIER object answers GetObject with 403
// InvalidObjectState naming its class and refuses to be a copy source with
// 403 ObjectNotInActiveTierError, while HeadObject reads it and reports the
// class (s3.md 5.6, 5.8, 7).
func TestArchivedObjects(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("archive")
		ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "archive", Key: "cold", Body: bytesBody("v"), StorageClass: new(s3api.StorageClassGlacier)})
		_, err := as(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "archive", Key: "cold"})
		wantCode(t, err, "InvalidObjectState", http.StatusForbidden)
		wantFields(t, err, map[string]string{"StorageClass": "GLACIER"})
		head := ok(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "archive", Key: "cold"})
		if head.StorageClass == nil || *head.StorageClass != s3api.StorageClassGlacier {
			t.Fatalf("HeadObject of a GLACIER object reports class %v, want GLACIER", head.StorageClass)
		}
		_, err = as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "archive", Key: "warm", CopySource: "archive/cold"})
		wantCode(t, err, "ObjectNotInActiveTierError", http.StatusForbidden)
	})
}

// TestResponseOverrides: the response-* query parameters replace the
// headers GetObject answers with, and response-expires is sent in HTTP
// date form (s3.md 5.4).
func TestResponseOverrides(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("overrides")
		ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "overrides", Key: "k", Body: bytesBody("v"), ContentType: new("text/plain"), CacheControl: new("no-cache")})
		expires := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
		_, out := h.read(&s3api.GetObjectInput{
			Bucket: "overrides", Key: "k",
			ResponseContentType: new("application/json"), ResponseCacheControl: new("max-age=60"), ResponseContentDisposition: new("attachment"),
			ResponseContentEncoding: new("gzip"), ResponseContentLanguage: new("de"), ResponseExpires: &expires,
		})
		got := []string{*out.ContentType, *out.CacheControl, *out.ContentDisposition, *out.ContentEncoding, *out.ContentLanguage, *out.Expires}
		want := []string{"application/json", "max-age=60", "attachment", "gzip", "de", "Wed, 02 Jan 2030 03:04:05 GMT"}
		if !slices.Equal(got, want) {
			t.Fatalf("GetObject with response-* overrides = %q, want %q", got, want)
		}
	})
}

// TestMissingKeysAndVersions: NoSuchKey with Key; a version id other than
// null on an unversioned bucket is InvalidArgument; null reads the object
// (s3.md 5.8).
func TestMissingKeysAndVersions(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("reads")
		h.put("reads", "k", "v")
		_, err := as(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "reads", Key: "K"})
		wantCode(t, err, "NoSuchKey", http.StatusNotFound)
		wantFields(t, err, map[string]string{"Key": "K"})
		_, err = as(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "reads", Key: "k", VersionId: new("abc")})
		wantCode(t, err, "InvalidArgument", http.StatusBadRequest)
		wantFields(t, err, map[string]string{"ArgumentName": "versionId", "ArgumentValue": "abc"})
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "reads", Key: "k", VersionId: new("null")}); got != "v" {
			t.Fatalf("GetObject versionId=null = %q, want the object", got)
		}
	})
}
