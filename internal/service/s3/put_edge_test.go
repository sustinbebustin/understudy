package s3_test

import (
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocol/restxml"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// readCounter counts the reads of a request body.
type readCounter struct {
	r     io.Reader
	reads int
}

func (c *readCounter) Read(p []byte) (int, error) {
	c.reads++
	return c.r.Read(p)
}

// objectEdge is an S3 service with a blob store behind the edge: send
// signs a request to target on the S3 host with an unsigned payload and
// returns the recorded answer.
type objectEdge struct {
	k     *kerneltest.K
	front http.Handler
}

// newObjectEdge builds an objectEdge on k; the blob store closes when the
// test ends.
func newObjectEdge(k *kerneltest.K) objectEdge {
	t := k.T
	blobs, err := blob.NewMemory(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := blobs.Close(); err != nil {
			t.Error(err)
		}
	})
	d := k.Deps(s3api.Service())
	d.Blob = blobs
	svc := s3.New(d, s3.Options{})
	front, err := edge.New(edge.Options{
		Kernel: k.Kernel, Registry: k.Kernel.Registry(), Identity: k.Identity, Regions: aws.KnownRegions(),
		Codecs:       map[ref.ProtocolID]edge.Codec{ref.RESTXML: xmlCodec{c: restxml.Codec{Dialect: s3Dialect}}},
		BucketExists: svc.BucketExists, S3BaseDomains: []string{"localhost"},
		Listener: ref.ListenerMain, Unclaimed: ref.RESTXML,
	})
	if err != nil {
		t.Fatal(err)
	}
	return objectEdge{k: k, front: front}
}

// send sends method to target with body and header, declaring length.
func (e objectEdge) send(method, target string, body io.Reader, length int64, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(e.k.Context(), method, "http://"+s3Host+target, body)
	r.ContentLength = length
	maps.Copy(r.Header, header)
	r.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	auth.Sign(r, nil, auth.Signer{AccessKeyID: aws.DefaultAccount().String(), SecretAccessKey: "secret", Region: aws.DefaultRegion(), Service: "s3"}, e.k.Clock.Wall())
	rec := httptest.NewRecorder()
	e.front.ServeHTTP(rec, r)
	return rec
}

// TestExpectContinueOverHTTP sends PutObject through the edge with
// Expect: 100-continue, as botocore does for every upload: to a missing
// bucket it answers 404 NoSuchBucket without reading the body, which is
// what keeps net/http from sending 100 Continue (the recorder cannot
// observe that line itself); to an existing bucket it reads the body and
// stores it (s3.md 6.3, sigv4-checksums-endpoints.md 3.4).
func TestExpectContinueOverHTTP(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		e := newObjectEdge(k)
		expect := http.Header{"Expect": {"100-continue"}}
		missing := &readCounter{r: strings.NewReader("hello")}
		rec := e.send(http.MethodPut, "/nowhere/key.txt", missing, 5, expect)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "<Code>NoSuchBucket</Code>") || missing.reads != 0 {
			t.Fatalf("PutObject to a missing bucket = %d %s after %d body reads; want 404 NoSuchBucket and no read", rec.Code, rec.Body, missing.reads)
		}

		if rec := e.send(http.MethodPut, "/expect", strings.NewReader(""), 0, nil); rec.Code != http.StatusOK {
			t.Fatalf("CreateBucket = %d %s", rec.Code, rec.Body)
		}
		stored := &readCounter{r: strings.NewReader("hello")}
		rec = e.send(http.MethodPut, "/expect/key.txt", stored, 5, expect)
		if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"`+md5hex("hello")+`"` || stored.reads == 0 {
			t.Fatalf("PutObject = %d %s, ETag %q after %d reads", rec.Code, rec.Body, rec.Header().Get("ETag"), stored.reads)
		}
		rec = e.send(http.MethodGet, "/expect/key.txt", strings.NewReader(""), 0, nil)
		if rec.Code != http.StatusOK || rec.Body.String() != "hello" {
			t.Fatalf("GetObject = %d %q, want 200 hello", rec.Code, rec.Body)
		}
	}, kerneltest.Services(s3api.Service()))
}

// TestPartialReadsOverHTTP: a ranged GetObject or HeadObject answers 206
// Partial Content with Content-Range, and a malformed Range is ignored
// with 200 and the whole object (s3.md 5.8, 5.9).
func TestPartialReadsOverHTTP(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		e := newObjectEdge(k)
		if rec := e.send(http.MethodPut, "/partial", strings.NewReader(""), 0, nil); rec.Code != http.StatusOK {
			t.Fatalf("CreateBucket = %d %s", rec.Code, rec.Body)
		}
		if rec := e.send(http.MethodPut, "/partial/k", strings.NewReader("0123456789"), 10, nil); rec.Code != http.StatusOK {
			t.Fatalf("PutObject = %d %s", rec.Code, rec.Body)
		}
		for _, tc := range []struct {
			method, target, rangeSpec string
			status                    int
			contentRange, body        string
		}{
			{http.MethodGet, "/partial/k", "bytes=2-4", http.StatusPartialContent, "bytes 2-4/10", "234"},
			// An object written in one request is its own part 1, served
			// whole (s3.md 5.8).
			{http.MethodGet, "/partial/k?partNumber=1", "", http.StatusOK, "", "0123456789"},
			{http.MethodHead, "/partial/k", "bytes=-3", http.StatusPartialContent, "bytes 7-9/10", ""},
			{http.MethodGet, "/partial/k", "items=0-1", http.StatusOK, "", "0123456789"},
			{http.MethodGet, "/partial/k", "", http.StatusOK, "", "0123456789"},
		} {
			header := http.Header{}
			if tc.rangeSpec != "" {
				header.Set("Range", tc.rangeSpec)
			}
			rec := e.send(tc.method, tc.target, strings.NewReader(""), 0, header)
			if rec.Code != tc.status || rec.Header().Get("Content-Range") != tc.contentRange || rec.Body.String() != tc.body {
				t.Errorf("%s %s Range %q = %d, Content-Range %q, body %q; want %d, %q, %q",
					tc.method, tc.target, tc.rangeSpec, rec.Code, rec.Header().Get("Content-Range"), rec.Body, tc.status, tc.contentRange, tc.body)
			}
		}
	}, kerneltest.Services(s3api.Service()))
}

// listEntry is a key or common prefix a listing reports.
type listEntry struct {
	value  string
	prefix bool
}

// expectedListing is the reference page of keys under prefix, grouped at
// delimiter, strictly after after, at most limit entries, in byte order
// (s3.md 9.1), and whether more entries follow.
func expectedListing(keys []string, prefix, delimiter, after string, limit int) ([]listEntry, bool) {
	sorted := slices.Sorted(slices.Values(keys))
	var all []listEntry
	for _, k := range sorted {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		e := listEntry{value: k}
		if delimiter != "" {
			if i := strings.Index(k[len(prefix):], delimiter); i >= 0 {
				e = listEntry{value: k[:len(prefix)+i+len(delimiter)], prefix: true}
			}
		}
		if e.value > after && (len(all) == 0 || all[len(all)-1] != e) {
			all = append(all, e)
		}
	}
	if len(all) > limit {
		return all[:limit], true
	}
	return all, false
}

// encodedListing is the part of a listing document encoding-type=url
// applies to (wire-rest.md 3.4).
type encodedListing struct {
	Prefix        string  `xml:"Prefix"`
	Delimiter     *string `xml:"Delimiter"`
	Marker        *string `xml:"Marker"`
	NextMarker    *string `xml:"NextMarker"`
	StartAfter    *string `xml:"StartAfter"`
	KeyMarker     *string `xml:"KeyMarker"`
	NextKeyMarker *string `xml:"NextKeyMarker"`
	IsTruncated   bool    `xml:"IsTruncated"`
	Contents      []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
	Versions []struct {
		Key string `xml:"Key"`
	} `xml:"Version"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

// TestListingEncodingRoundTripsEveryKey: for keys, prefixes, delimiters
// and markers drawn from characters URL encoding treats specially ('+',
// ' ', '%', '=', '/', a control character and a multi-byte rune), every
// field encoding-type=url applies to in ListObjects, ListObjectsV2 and
// ListObjectVersions decodes back to its raw value both with unquote and
// with unquote_plus, as botocore reads it, and the listing is the
// reference page (wire-rest.md 3.4, s3.md 9.1).
func TestListingEncodingRoundTripsEveryKey(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		e := newObjectEdge(k)
		chars := rapid.SampledFrom([]rune{'a', 'b', '/', '+', ' ', '%', '=', '\x01', 'é'})
		buckets := 0
		// k, not k.T: rapid asks a *testing.T for its deadline, which a
		// synctest bubble refuses; any other TB gets rapid's default.
		rapid.Check(k, func(t *rapid.T) {
			buckets++
			bucket := fmt.Sprintf("encoded-%d", buckets)
			if rec := e.send(http.MethodPut, "/"+bucket, strings.NewReader(""), 0, nil); rec.Code != http.StatusOK {
				t.Fatalf("CreateBucket = %d %s", rec.Code, rec.Body)
			}
			keys := rapid.SliceOfNDistinct(rapid.StringOfN(chars, 1, 5, -1), 1, 6, func(s string) string { return s }).Draw(t, "keys")
			for _, key := range keys {
				if rec := e.send(http.MethodPut, "/"+bucket+"/"+url.PathEscape(key), strings.NewReader("v"), 1, nil); rec.Code != http.StatusOK {
					t.Fatalf("PutObject %q = %d %s", key, rec.Code, rec.Body)
				}
			}
			prefix := rapid.StringOfN(chars, 0, 2, -1).Draw(t, "prefix")
			delimiter := rapid.SampledFrom([]string{"", "/", "+", " ", "%", "="}).Draw(t, "delimiter")
			after := rapid.StringOfN(chars, 0, 3, -1).Draw(t, "after")
			limit := rapid.IntRange(1, 4).Draw(t, "limit")
			want, truncated := expectedListing(keys, prefix, delimiter, after, limit)

			decode := func(name, encoded string) string {
				unquoted, err := url.PathUnescape(encoded)
				if err != nil {
					t.Fatalf("%s %q does not unquote: %v", name, encoded, err)
				}
				plus, err := url.QueryUnescape(encoded)
				if err != nil || plus != unquoted {
					t.Fatalf("%s %q unquotes to %q but unquote_plus gives %q (%v)", name, encoded, unquoted, plus, err)
				}
				return unquoted
			}
			for _, l := range []struct {
				name, query, marker string
			}{
				{"ListObjectsV2", "list-type=2", "start-after"},
				{"ListObjects", "", "marker"},
				{"ListObjectVersions", "versions", "key-marker"},
			} {
				q := url.Values{"encoding-type": {"url"}, "max-keys": {strconv.Itoa(limit)}}
				for name, v := range map[string]string{"prefix": prefix, "delimiter": delimiter, l.marker: after} {
					if v != "" {
						q.Set(name, v)
					}
				}
				target := "/" + bucket + "?" + strings.TrimPrefix(l.query+"&"+q.Encode(), "&")
				rec := e.send(http.MethodGet, target, strings.NewReader(""), 0, nil)
				var doc encodedListing
				if err := xml.Unmarshal(rec.Body.Bytes(), &doc); rec.Code != http.StatusOK || err != nil {
					t.Fatalf("%s %s = %d %s (%v)", l.name, target, rec.Code, rec.Body, err)
				}
				got := make([]listEntry, 0, len(doc.Contents)+len(doc.Versions)+len(doc.CommonPrefixes))
				for _, c := range slices.Concat(doc.Contents, doc.Versions) {
					got = append(got, listEntry{value: decode(l.name+" Key", c.Key)})
				}
				for _, p := range doc.CommonPrefixes {
					got = append(got, listEntry{value: decode(l.name+" CommonPrefixes", p.Prefix), prefix: true})
				}
				slices.SortFunc(got, func(a, b listEntry) int { return strings.Compare(a.value, b.value) })
				if !slices.Equal(got, want) || doc.IsTruncated != truncated {
					t.Fatalf("%s %s = %v truncated %v, want %v truncated %v", l.name, target, got, doc.IsTruncated, want, truncated)
				}
				if p := decode(l.name+" Prefix", doc.Prefix); p != prefix {
					t.Fatalf("%s Prefix = %q, want %q", l.name, p, prefix)
				}
				if delimiter != "" && (doc.Delimiter == nil || decode(l.name+" Delimiter", *doc.Delimiter) != delimiter) {
					t.Fatalf("%s Delimiter = %v, want %q", l.name, doc.Delimiter, delimiter)
				}
				echoed := map[string]*string{"ListObjectsV2": doc.StartAfter, "ListObjects": doc.Marker, "ListObjectVersions": doc.KeyMarker}[l.name]
				if after != "" && (echoed == nil || decode(l.name+" "+l.marker, *echoed) != after) {
					t.Fatalf("%s %s echo = %v, want %q", l.name, l.marker, echoed, after)
				}
				next := map[string]*string{"ListObjects": doc.NextMarker, "ListObjectVersions": doc.NextKeyMarker}[l.name]
				if truncated && next != nil && decode(l.name+" next marker", *next) != want[len(want)-1].value {
					t.Fatalf("%s next marker = %q, want the last entry %q", l.name, *next, want[len(want)-1].value)
				}
			}
		})
	}, kerneltest.Services(s3api.Service()))
}

// TestListingKeysEncodedOnceOverHTTP lists a key holding a space, '=', '+'
// and a non-ASCII rune through the edge with encoding-type=url, as boto3
// and the AWS CLI always ask: every key field is percent-encoded exactly
// once, so one unquote gives the key back (wire-rest.md 3.4, s3.md 9.1).
func TestListingKeysEncodedOnceOverHTTP(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		e := newObjectEdge(k)
		const key = "a b=c+d/é"
		if rec := e.send(http.MethodPut, "/enc", strings.NewReader(""), 0, nil); rec.Code != http.StatusOK {
			t.Fatalf("CreateBucket = %d %s", rec.Code, rec.Body)
		}
		if rec := e.send(http.MethodPut, "/enc/"+url.PathEscape(key), strings.NewReader("v"), 1, nil); rec.Code != http.StatusOK {
			t.Fatalf("PutObject = %d %s", rec.Code, rec.Body)
		}
		var doc struct {
			Contents []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
			Versions []struct {
				Key string `xml:"Key"`
			} `xml:"Version"`
			CommonPrefixes []struct {
				Prefix string `xml:"Prefix"`
			} `xml:"CommonPrefixes"`
			Prefix string `xml:"Prefix"`
		}
		decode := func(name, encoded string) string {
			t.Helper()
			got, err := url.QueryUnescape(encoded)
			if err != nil {
				t.Fatalf("%s: %q is not one level of URL encoding: %v", name, encoded, err)
			}
			return got
		}
		for _, target := range []string{"/enc?list-type=2&encoding-type=url", "/enc?encoding-type=url", "/enc?versions&encoding-type=url"} {
			rec := e.send(http.MethodGet, target, strings.NewReader(""), 0, nil)
			doc.Contents, doc.Versions = nil, nil
			if err := xml.Unmarshal(rec.Body.Bytes(), &doc); rec.Code != http.StatusOK || err != nil {
				t.Fatalf("GET %s = %d %s (%v)", target, rec.Code, rec.Body, err)
			}
			keys := slices.Concat(doc.Contents, doc.Versions)
			if len(keys) != 1 || decode(target, keys[0].Key) != key {
				t.Fatalf("GET %s keys = %+v, want one key that unquotes to %q", target, keys, key)
			}
		}
		target := "/enc?list-type=2&encoding-type=url&delimiter=%2F&prefix=" + url.QueryEscape("a b=")
		rec := e.send(http.MethodGet, target, strings.NewReader(""), 0, nil)
		doc.CommonPrefixes = nil
		if err := xml.Unmarshal(rec.Body.Bytes(), &doc); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("GET %s = %d %s (%v)", target, rec.Code, rec.Body, err)
		}
		if len(doc.CommonPrefixes) != 1 || decode("CommonPrefix", doc.CommonPrefixes[0].Prefix) != "a b=c+d/" || decode("Prefix", doc.Prefix) != "a b=" {
			t.Fatalf("GET %s = prefix %q, common prefixes %+v; want a b= and a b=c+d/ encoded once", target, doc.Prefix, doc.CommonPrefixes)
		}
	}, kerneltest.Services(s3api.Service()))
}
