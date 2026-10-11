package s3_test

import (
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
)

// TestObjectTags: x-amz-tagging on PutObject, the tagging operations, a
// PUT without x-amz-tagging clearing the tags, the 10-tag limit and the
// header format errors (s3.md 11.1).
func TestObjectTags(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("tags")
		ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "tags", Key: "k", Body: bytesBody("x"), Tagging: new("a=1&b%20c=d%26e&flag")})
		got := ok(h, who{}, s3api.OpGetObjectTagging, &s3api.GetObjectTaggingInput{Bucket: "tags", Key: "k"})
		want := map[string]string{"a": "1", "b c": "d&e", "flag": ""}
		if len(got.TagSet) != len(want) {
			t.Fatalf("GetObjectTagging = %+v, want %v", got.TagSet, want)
		}
		for _, tag := range got.TagSet {
			if want[tag.Key] != tag.Value {
				t.Fatalf("GetObjectTagging = %+v, want %v", got.TagSet, want)
			}
		}

		ok(h, who{}, s3api.OpPutObjectTagging, &s3api.PutObjectTaggingInput{Bucket: "tags", Key: "k", Tagging: s3api.Tagging{TagSet: []s3api.Tag{{Key: "only", Value: "one"}}}})
		if head := ok(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "tags", Key: "k"}); *head.TagCount != 1 {
			t.Fatalf("x-amz-tagging-count = %d, want 1", *head.TagCount)
		}
		ok(h, who{}, s3api.OpDeleteObjectTagging, &s3api.DeleteObjectTaggingInput{Bucket: "tags", Key: "k"})
		if got := ok(h, who{}, s3api.OpGetObjectTagging, &s3api.GetObjectTaggingInput{Bucket: "tags", Key: "k"}); len(got.TagSet) != 0 {
			t.Fatalf("tags after DeleteObjectTagging = %+v", got.TagSet)
		}

		ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "tags", Key: "k", Body: bytesBody("x"), Tagging: new("a=1")})
		h.put("tags", "k", "y")
		if got := ok(h, who{}, s3api.OpGetObjectTagging, &s3api.GetObjectTaggingInput{Bucket: "tags", Key: "k"}); len(got.TagSet) != 0 {
			t.Fatalf("tags after an overwrite without x-amz-tagging = %+v, want none", got.TagSet)
		}

		for _, header := range []string{"key1=val1,key2=val2", "a=1&a=2"} {
			_, err := as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "tags", Key: "bad", Body: bytesBody("x"), Tagging: new(header)})
			wantCode(t, err, "InvalidArgument", http.StatusBadRequest)
			wantFields(t, err, map[string]string{"ArgumentName": "x-amz-tagging", "ArgumentValue": header})
		}
		eleven := make([]s3api.Tag, 11)
		for i := range eleven {
			eleven[i] = s3api.Tag{Key: string(rune('a' + i))}
		}
		_, err := as(h, who{}, s3api.OpPutObjectTagging, &s3api.PutObjectTaggingInput{Bucket: "tags", Key: "k", Tagging: s3api.Tagging{TagSet: eleven}})
		wantCode(t, err, "InvalidTag", http.StatusBadRequest)
		_, err = as(h, who{}, s3api.OpGetObjectTagging, &s3api.GetObjectTaggingInput{Bucket: "tags", Key: "missing"})
		wantCode(t, err, "NoSuchKey", http.StatusNotFound)
	})
}

// TestListObjectsOverHandlers: v2 pages with an opaque token, groups at
// the delimiter, reports KeyCount, echoes encoding-type=url with the keys
// raw (the codec encodes them, TestListingKeysEncodedOnceOverHTTP), and
// refuses a token it did not mint; v1 sends NextMarker only with a
// delimiter; max-keys=0 is an empty page that is not truncated and names
// no next page (s3.md 9.1).
func TestListObjectsOverHandlers(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("list")
		for _, k := range []string{"a+b", "dir/x", "dir/y", "z=1"} {
			h.put("list", k, k)
		}
		first := ok(h, who{}, s3api.OpListObjectsV2, &s3api.ListObjectsV2Input{Bucket: "list", MaxKeys: new(int32(2)), Delimiter: new("/"), EncodingType: new(s3api.EncodingTypeUrl)})
		if *first.KeyCount != 2 || !*first.IsTruncated || *first.Contents[0].Key != "a+b" || *first.CommonPrefixes[0].Prefix != "dir/" || *first.EncodingType != s3api.EncodingTypeUrl {
			t.Fatalf("first page = %d keys, truncated %v, %+v %+v, encoding %v", *first.KeyCount, *first.IsTruncated, first.Contents, first.CommonPrefixes, first.EncodingType)
		}
		second := ok(h, who{}, s3api.OpListObjectsV2, &s3api.ListObjectsV2Input{Bucket: "list", MaxKeys: new(int32(2)), Delimiter: new("/"), ContinuationToken: first.NextContinuationToken})
		if *second.KeyCount != 1 || *second.IsTruncated || *second.Contents[0].Key != "z=1" || *second.ContinuationToken != *first.NextContinuationToken {
			t.Fatalf("second page = %+v", second)
		}
		_, err := as(h, who{}, s3api.OpListObjectsV2, &s3api.ListObjectsV2Input{Bucket: "list", ContinuationToken: new("made-up")})
		wantCode(t, err, "InvalidArgument", http.StatusBadRequest)

		v1 := ok(h, who{}, s3api.OpListObjects, &s3api.ListObjectsInput{Bucket: "list", MaxKeys: new(int32(1))})
		if !*v1.IsTruncated || v1.NextMarker != nil || *v1.Marker != "" || v1.Contents[0].Owner == nil {
			t.Fatalf("v1 without a delimiter = %+v, want truncated, no NextMarker, an empty Marker and Owner", v1)
		}
		v1 = ok(h, who{}, s3api.OpListObjects, &s3api.ListObjectsInput{Bucket: "list", MaxKeys: new(int32(2)), Delimiter: new("/")})
		if v1.NextMarker == nil || *v1.NextMarker != "dir/" {
			t.Fatalf("v1 with a delimiter NextMarker = %v, want dir/", v1.NextMarker)
		}

		zero := ok(h, who{}, s3api.OpListObjectsV2, &s3api.ListObjectsV2Input{Bucket: "list", MaxKeys: new(int32(0))})
		if *zero.KeyCount != 0 || *zero.IsTruncated || zero.NextContinuationToken != nil {
			t.Fatalf("v2 max-keys=0 = %d keys, truncated %v, next token %v; want an empty page, not truncated, no token", *zero.KeyCount, *zero.IsTruncated, zero.NextContinuationToken)
		}
		zeroV1 := ok(h, who{}, s3api.OpListObjects, &s3api.ListObjectsInput{Bucket: "list", MaxKeys: new(int32(0)), Delimiter: new("/")})
		if len(zeroV1.Contents) != 0 || *zeroV1.IsTruncated || zeroV1.NextMarker != nil {
			t.Fatalf("v1 max-keys=0 = %+v, want an empty page, not truncated, no NextMarker", zeroV1)
		}
		zeroVersions := ok(h, who{}, s3api.OpListObjectVersions, &s3api.ListObjectVersionsInput{Bucket: "list", MaxKeys: new(int32(0))})
		if len(zeroVersions.Versions) != 0 || *zeroVersions.IsTruncated || zeroVersions.NextKeyMarker != nil {
			t.Fatalf("ListObjectVersions max-keys=0 = %+v, want an empty page, not truncated, no NextKeyMarker", zeroVersions)
		}
		_, err = as(h, who{}, s3api.OpListObjects, &s3api.ListObjectsInput{Bucket: "missing"})
		wantCode(t, err, "NoSuchBucket", http.StatusNotFound)
	})
}
