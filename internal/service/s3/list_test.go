package s3

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
)

// keyGen draws keys from a small alphabet so prefixes, delimiters and
// shared stems collide often, plus characters encoding-type=url encodes.
func keyGen() *rapid.Generator[string] {
	return rapid.StringOfN(rapid.SampledFrom([]rune{'a', 'b', '/', '-', '+', ' ', '%', '=', 'é', '\x01'}), 1, 6, -1)
}

func sortedKeys(t *rapid.T) []string {
	keys := rapid.SliceOfDistinct(keyGen(), func(s string) string { return s }).Draw(t, "keys")
	slices.Sort(keys)
	return keys
}

// reference lists every entry in one pass the slow way: each key under
// prefix, or the common prefix up to its first delimiter after prefix,
// deduplicated, in byte order.
func reference(keys []string, prefix, delimiter string) []listed {
	var out []listed
	for _, k := range keys {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		e := listed{key: k}
		if delimiter != "" {
			if rest, _, found := strings.Cut(k[len(prefix):], delimiter); found {
				e = listed{key: prefix + rest + delimiter, prefix: true}
			}
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b listed) int { return strings.Compare(a.key, b.key) })
	return out
}

// TestListingPagesConcatenate checks the paging property of s3.md 9.1:
// following each page from the entry it ended at yields exactly the
// unpaginated listing, and the grouping matches the reference.
func TestListingPagesConcatenate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		keys := sortedKeys(t)
		prefix := rapid.SampledFrom([]string{"", "a", "a/", "b"}).Draw(t, "prefix")
		delimiter := rapid.SampledFrom([]string{"", "/", "-", "a/"}).Draw(t, "delimiter")
		limit := rapid.IntRange(1, 4).Draw(t, "limit")

		whole, truncated := page(keys, listQuery{prefix: prefix, delimiter: delimiter, limit: maxListKeys})
		if truncated {
			t.Fatalf("a listing under the cap was truncated")
		}
		if want := reference(keys, prefix, delimiter); !slices.Equal(whole, want) {
			t.Fatalf("listing = %v, reference = %v", whole, want)
		}
		var paged []listed
		after := ""
		for {
			entries, more := page(keys, listQuery{prefix: prefix, delimiter: delimiter, after: after, limit: limit})
			if len(entries) > limit {
				t.Fatalf("page of %d entries over the limit %d", len(entries), limit)
			}
			paged = append(paged, entries...)
			if !more {
				break
			}
			after = last(entries)
		}
		if !slices.Equal(paged, whole) {
			t.Fatalf("pages = %v, unpaginated = %v", paged, whole)
		}
	})
}

// liveBucket is a bucket whose keys each hold one object.
func liveBucket(keys ...string) *Bucket {
	b := &Bucket{Name: "listing", Objects: Objects{Keys: map[string][]Version{}}}
	for _, k := range keys {
		b.Objects.Keys[k] = []Version{{ID: nullVersion, Object: &Object{StorageClass: s3api.StorageClassStandard}}}
	}
	return b
}

// TestListObjectsV2StartAfter: start-after lists the keys strictly after
// it, whether or not it names a key, and is echoed; a continuation token
// wins over it (s3.md 9.1).
func TestListObjectsV2StartAfter(t *testing.T) {
	b := liveBucket("a", "b", "c", "d")
	for _, tc := range []struct {
		name       string
		startAfter string
		token      *string
		want       []string
	}{
		{"a key", "b", nil, []string{"c", "d"}},
		{"between keys", "bb", nil, []string{"c", "d"}},
		{"after every key", "z", nil, nil},
		{"with a continuation token", "a", new(continuationToken("c")), []string{"d"}},
	} {
		out, err := listObjectsV2(&s3api.ListObjectsV2Input{Bucket: b.Name, StartAfter: new(tc.startAfter), ContinuationToken: tc.token}, b)
		if err != nil {
			t.Fatalf("%s: %s %s", tc.name, err.ID.QueryCode, err.Message)
		}
		got := make([]string, 0, len(out.Contents))
		for _, o := range out.Contents {
			got = append(got, *o.Key)
		}
		if !slices.Equal(got, tc.want) || out.StartAfter == nil || *out.StartAfter != tc.startAfter {
			t.Errorf("%s: keys %v, StartAfter %v; want %v and the echo %q", tc.name, got, out.StartAfter, tc.want, tc.startAfter)
		}
	}
}

// versionedBucket draws a bucket whose keys hold one to three versions
// each, newest first, some of them delete markers.
func versionedBucket(t *rapid.T) *Bucket {
	b := &Bucket{Name: "versions", Objects: Objects{Keys: map[string][]Version{}}}
	n := 0
	for _, k := range rapid.SliceOfDistinct(keyGen(), func(s string) string { return s }).Draw(t, "keys") {
		chain := make([]Version, rapid.IntRange(1, 3).Draw(t, "versions"))
		for i := range chain {
			n++
			chain[i] = Version{ID: fmt.Sprintf("v%03d", n)}
			if !rapid.Bool().Draw(t, "marker") {
				chain[i].Object = &Object{StorageClass: s3api.StorageClassStandard}
			}
		}
		b.Objects.Keys[k] = chain
	}
	return b
}

// versionEntries lists a ListObjectVersions page as comparable entries:
// each version and delete marker with its key, id and IsLatest, and each
// common prefix.
func versionEntries(out *s3api.ListObjectVersionsOutput) []string {
	entries := make([]string, 0, len(out.Versions)+len(out.DeleteMarkers)+len(out.CommonPrefixes))
	for _, v := range out.Versions {
		entries = append(entries, fmt.Sprintf("version %q %s latest=%t", *v.Key, *v.VersionId, *v.IsLatest))
	}
	for _, m := range out.DeleteMarkers {
		entries = append(entries, fmt.Sprintf("marker %q %s latest=%t", *m.Key, *m.VersionId, *m.IsLatest))
	}
	for _, p := range out.CommonPrefixes {
		entries = append(entries, fmt.Sprintf("prefix %q", *p.Prefix))
	}
	return entries
}

// pagedVersions follows NextKeyMarker and NextVersionIdMarker from the
// first page of in, limit entries a page, and returns every entry listed.
func pagedVersions(t *rapid.T, b *Bucket, in s3api.ListObjectVersionsInput, limit int32) []string {
	in.MaxKeys = new(limit)
	var all []string
	for range 1000 {
		out, err := listObjectVersions(&in, b)
		if err != nil {
			t.Fatalf("ListObjectVersions: %s %s", err.ID.QueryCode, err.Message)
		}
		page := versionEntries(out)
		if len(page) > int(limit) {
			t.Fatalf("a page of %d entries over max-keys %d", len(page), limit)
		}
		all = append(all, page...)
		if !*out.IsTruncated {
			return all
		}
		in.KeyMarker, in.VersionIdMarker = out.NextKeyMarker, out.NextVersionIdMarker
	}
	t.Fatalf("ListObjectVersions still truncated after 1000 pages")
	return nil
}

// TestVersionPagesConcatenate checks the paging property of s3.md 9.3:
// following NextKeyMarker and NextVersionIdMarker page by page with a
// small max-keys lists exactly the unpaginated versions, delete markers
// and common prefixes, IsLatest included, also when a key's versions span
// a page boundary.
func TestVersionPagesConcatenate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		b := versionedBucket(t)
		in := s3api.ListObjectVersionsInput{
			Bucket:    b.Name,
			Prefix:    new(rapid.SampledFrom([]string{"", "a", "a/", "b"}).Draw(t, "prefix")),
			Delimiter: new(rapid.SampledFrom([]string{"", "/", "-"}).Draw(t, "delimiter")),
		}
		limit := rapid.Int32Range(1, 4).Draw(t, "limit")
		whole, err := listObjectVersions(&in, b)
		if err != nil || *whole.IsTruncated {
			t.Fatalf("unpaginated ListObjectVersions = %v, truncated %v", err, whole != nil && *whole.IsTruncated)
		}
		want := versionEntries(whole)
		got := pagedVersions(t, b, in, limit)
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("pages = %v, unpaginated = %v", got, want)
		}
	})
}

// TestVersionPageSplitsAKey: a page boundary inside a key's versions
// resumes at the next version of that key, which is not the latest.
func TestVersionPageSplitsAKey(t *testing.T) {
	b := &Bucket{Name: "versions", Objects: Objects{Keys: map[string][]Version{
		"a": {{ID: "a3", Object: &Object{}}, {ID: "a2"}, {ID: "a1", Object: &Object{}}},
		"b": {{ID: "b1", Object: &Object{}}},
	}}}
	first, err := listObjectVersions(&s3api.ListObjectVersionsInput{Bucket: b.Name, MaxKeys: new(int32(2))}, b)
	if err != nil {
		t.Fatal(err.Message)
	}
	if !*first.IsTruncated || *first.NextKeyMarker != "a" || *first.NextVersionIdMarker != "a2" {
		t.Fatalf("first page truncated %v at %v/%v, want truncated at a/a2", *first.IsTruncated, first.NextKeyMarker, first.NextVersionIdMarker)
	}
	second, err := listObjectVersions(&s3api.ListObjectVersionsInput{Bucket: b.Name, MaxKeys: new(int32(2)), KeyMarker: first.NextKeyMarker, VersionIdMarker: first.NextVersionIdMarker}, b)
	if err != nil {
		t.Fatal(err.Message)
	}
	want := []string{`version "a" a1 latest=false`, `version "b" b1 latest=true`}
	if got := versionEntries(second); !slices.Equal(got, want) || *second.IsTruncated {
		t.Fatalf("second page = %v, truncated %v; want %v, not truncated", got, *second.IsTruncated, want)
	}
}

// TestVersionIDMarkerNeedsKeyMarker: a version-id-marker without a
// key-marker is InvalidArgument (LocalStack aws_validated snapshot
// test_list_objects_versions_markers, step list-objects-next-version-only).
func TestVersionIDMarkerNeedsKeyMarker(t *testing.T) {
	b := &Bucket{Name: "versions"}
	id := mintVersionID(strings.Repeat("A", versionIDLen), 1)
	_, err := listObjectVersions(&s3api.ListObjectVersionsInput{Bucket: b.Name, VersionIdMarker: new(id)}, b)
	if err == nil || err.ID.QueryCode != "InvalidArgument" || err.Message != "A version-id marker cannot be specified without a key marker." {
		t.Fatalf("ListObjectVersions with only a version-id-marker = %v, want InvalidArgument", err)
	}
	if name, value := fieldOf(err.Fields, "ArgumentName"), fieldOf(err.Fields, "ArgumentValue"); name != "version-id-marker" || value != id {
		t.Fatalf("ArgumentName %q, ArgumentValue %q; want version-id-marker, %q", name, value, id)
	}
}

// TestVersionIDsCarryTheirSeq: every minted version id is of the version
// id form, keeps the random characters past the Seq digits, and reads
// back the Seq it was minted with.
func TestVersionIDsCarryTheirSeq(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		random := rapid.StringOfN(rapid.SampledFrom([]rune(versionIDChars)), versionIDLen, versionIDLen, -1).Draw(t, "random")
		seq := rapid.Uint64().Draw(t, "seq")
		id := mintVersionID(random, seq)
		if err := checkVersionID(&id); err != nil {
			t.Fatalf("minted id %q is not a version id: %s", id, err.Message)
		}
		if id[versionSeqLen:] != random[versionSeqLen:] {
			t.Fatalf("minted id %q changed the random tail of %q", id, random)
		}
		if got, ok := versionSeq(id); !ok || got != seq {
			t.Fatalf("versionSeq(%q) = %d, %v; want %d", id, got, ok, seq)
		}
	})
	if _, ok := versionSeq(nullVersion); ok {
		t.Fatal("versionSeq(null) reads a Seq")
	}
}

// TestZeroMaxKeysListsNothing: max-keys=0 is an empty page, not truncated
// (s3.md 9.1).
func TestZeroMaxKeysListsNothing(t *testing.T) {
	entries, truncated := page([]string{"a", "b"}, listQuery{limit: 0})
	if len(entries) != 0 || truncated {
		t.Fatalf("page = %v, truncated %v; want no entries, not truncated", entries, truncated)
	}
}

// TestContinuationTokenRoundTrips: a token survives every key (keys hold
// at least one byte), and one understudy did not mint is refused (s3.md
// 9.1).
func TestContinuationTokenRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		key := rapid.StringN(1, -1, -1).Draw(t, "key")
		got, err := parseContinuationToken(continuationToken(key))
		if err != nil || got != key {
			t.Fatalf("round trip of %q = %q, %v", key, got, err)
		}
	})
	for _, bad := range []string{"", "abc", "!!", continuationToken("")} {
		if _, err := parseContinuationToken(bad); err == nil || err.ID.QueryCode != "InvalidArgument" {
			t.Errorf("token %q: got %v, want InvalidArgument", bad, err)
		}
	}
}
