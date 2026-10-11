package s3

import (
	"encoding/base64"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// maxListKeys is the default and the cap of max-keys (s3.md 9.1).
const maxListKeys = 1000

// The listing texts (s3.md 9.1).
const (
	invalidEncodingMessage = "Invalid Encoding Method specified in Request"
	invalidMaxKeysMessage  = "Provided max-keys not an integer or within integer range"
)

// listQuery is what one listing page asks for: keys under prefix,
// grouped at delimiter when it is not empty, strictly after (the
// marker, start-after or continuation token), at most limit entries.
type listQuery struct {
	prefix    string
	delimiter string
	after     string
	limit     int
}

// listed is one entry of a listing page: a key, or a common prefix that
// stands for every key under it.
type listed struct {
	key    string
	prefix bool
}

// page lists keys, sorted by their bytes, as q asks: each common prefix
// counts once against the limit and is listed only when it sorts after
// q.after; truncated reports that another entry would follow. A zero limit
// is an empty page that is not truncated (s3.md 9.1), so no listing mints
// a marker or token from an empty page.
func page(keys []string, q listQuery) (entries []listed, truncated bool) {
	if q.limit == 0 {
		return nil, false
	}
	for i := 0; i < len(keys); i++ {
		if k := keys[i]; k <= q.after || !strings.HasPrefix(k, q.prefix) {
			continue
		}
		e, n := q.entry(keys[i:])
		i += n - 1
		if e.key <= q.after {
			continue
		}
		if len(entries) == q.limit {
			return entries, true
		}
		entries = append(entries, e)
	}
	return entries, false
}

// entry is the entry keys[0], a key under q.prefix, lists as: the key, or
// with a delimiter the common prefix up to its first delimiter after
// q.prefix, which stands for keys[0] and every key after it under that
// prefix. n is how many keys the entry covers.
func (q listQuery) entry(keys []string) (e listed, n int) {
	k := keys[0]
	if q.delimiter == "" {
		return listed{key: k}, 1
	}
	j := strings.Index(k[len(q.prefix):], q.delimiter)
	if j < 0 {
		return listed{key: k}, 1
	}
	common := k[:len(q.prefix)+j+len(q.delimiter)]
	n = 1
	for n < len(keys) && strings.HasPrefix(keys[n], common) {
		n++
	}
	return listed{key: common, prefix: true}, n
}

// last is the key or common prefix a page ended at: the next page starts
// after it.
func last(entries []listed) string {
	if len(entries) == 0 {
		return ""
	}
	return entries[len(entries)-1].key
}

// tokenMark starts every continuation token understudy mints, so a token
// it did not mint is told apart with high likelihood.
const tokenMark = 0x01

// continuationToken is the opaque ListObjectsV2 token of a page that ended
// at key: base64url of the mark and the key's bytes, so any key survives.
func continuationToken(key string) string {
	return base64.RawURLEncoding.EncodeToString(append([]byte{tokenMark}, key...))
}

// parseContinuationToken returns the key a token minted by
// continuationToken ended at; anything else is InvalidArgument.
func parseContinuationToken(token string) (string, *awserr.Error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < 2 || raw[0] != tokenMark {
		return "", errInvalidArgument(invalidContinuationMessage, "continuation-token", token)
	}
	return string(raw[1:]), nil
}

// listLimit reads max-keys: absent is 1,000, larger values are capped, and
// a negative one is refused.
func listLimit(maxKeys *int32) (int, *awserr.Error) {
	switch {
	case maxKeys == nil:
		return maxListKeys, nil
	case *maxKeys < 0:
		return 0, errInvalidArgument(invalidMaxKeysMessage, "max-keys", strconv.Itoa(int(*maxKeys)))
	}
	return min(int(*maxKeys), maxListKeys), nil
}

// parseEncoding reads encoding-type, which only url is (s3.md 9.1), and
// returns the EncodingType the listing echoes. The listings write keys,
// prefixes and markers raw: the restXml codec percent-encodes them once
// when the request asked for url and the output echoes it
// (wire-rest.md 3.4).
func parseEncoding(v *s3api.EncodingType) (*s3api.EncodingType, *awserr.Error) {
	if v != nil && *v != s3api.EncodingTypeUrl {
		return nil, errInvalidArgument(invalidEncodingMessage, "encoding-type", string(*v))
	}
	return v, nil
}

// listParams are the parameters the three listings share.
type listParams struct {
	prefix    string
	delimiter string
	limit     int
	// encoding is the EncodingType the listing echoes.
	encoding *s3api.EncodingType
}

// readListParams reads prefix, delimiter, max-keys and encoding-type.
func readListParams(prefix, delimiter *string, maxKeys *int32, encoding *s3api.EncodingType) (listParams, *awserr.Error) {
	limit, err := listLimit(maxKeys)
	if err != nil {
		return listParams{}, err
	}
	enc, err := parseEncoding(encoding)
	if err != nil {
		return listParams{}, err
	}
	return listParams{prefix: deref(prefix), delimiter: deref(delimiter), limit: limit, encoding: enc}, nil
}

// query is the page query that starts after after.
func (p listParams) query(after string) listQuery {
	return listQuery{prefix: p.prefix, delimiter: p.delimiter, after: after, limit: p.limit}
}

// liveKeys are the keys whose current version is an object, in byte
// order: what ListObjects and ListObjectsV2 list.
func liveKeys(o Objects) []string {
	keys := make([]string, 0, len(o.Keys))
	for k := range o.Keys {
		if _, ok := o.live(k); ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// listedObjects splits a page into the objects and common prefixes a
// listing reports.
func listedObjects(b *Bucket, entries []listed, owner bool) ([]s3api.Object, []s3api.CommonPrefix) {
	var contents []s3api.Object
	var prefixes []s3api.CommonPrefix
	for _, e := range entries {
		if e.prefix {
			prefixes = append(prefixes, s3api.CommonPrefix{Prefix: new(e.key)})
			continue
		}
		v, _ := b.Objects.live(e.key)
		o := v.Object
		entry := s3api.Object{
			Key:          new(e.key),
			LastModified: new(v.Modified.Wire()),
			ETag:         new(quote(o.ETag)),
			Size:         new(o.Size),
			StorageClass: new(s3api.ObjectStorageClass(o.StorageClass)),
		}
		entry.ChecksumAlgorithm, entry.ChecksumType = listedChecksum(o)
		if owner {
			entry.Owner = &s3api.Owner{ID: new(canonicalID(b.Home.OwnerAccount))}
		}
		contents = append(contents, entry)
	}
	return contents, prefixes
}

// listedChecksum is the ChecksumAlgorithm list and ChecksumType a listing
// reports for an object (s3.md 6.4).
func listedChecksum(o *Object) ([]s3api.ChecksumAlgorithm, *s3api.ChecksumType) {
	if o.Checksum.Algorithm == "" {
		return nil, nil
	}
	return []s3api.ChecksumAlgorithm{s3api.ChecksumAlgorithm(o.Checksum.Algorithm)}, new(o.Checksum.Type)
}

// listObjects is ListObjects (v1): v1 entries always carry Owner, Marker is
// echoed, and NextMarker is sent only with a delimiter (s3.md 9.1).
func listObjects(in *s3api.ListObjectsInput, b *Bucket) (*s3api.ListObjectsOutput, *awserr.Error) {
	p, err := readListParams(in.Prefix, in.Delimiter, in.MaxKeys, in.EncodingType)
	if err != nil {
		return nil, err
	}
	marker := deref(in.Marker)
	entries, truncated := page(liveKeys(b.Objects), p.query(marker))
	contents, prefixes := listedObjects(b, entries, true)
	out := &s3api.ListObjectsOutput{
		Name: new(b.Name), Prefix: new(p.prefix), Delimiter: in.Delimiter, Marker: new(marker),
		MaxKeys: count(p.limit), IsTruncated: new(truncated), EncodingType: p.encoding, Contents: contents, CommonPrefixes: prefixes,
	}
	if truncated && p.delimiter != "" {
		out.NextMarker = new(last(entries))
	}
	return out, nil
}

// listObjectsV2 is ListObjectsV2: the continuation token wins over
// start-after, and Owner is reported only with fetch-owner (s3.md 9.1).
func listObjectsV2(in *s3api.ListObjectsV2Input, b *Bucket) (*s3api.ListObjectsV2Output, *awserr.Error) {
	p, err := readListParams(in.Prefix, in.Delimiter, in.MaxKeys, in.EncodingType)
	if err != nil {
		return nil, err
	}
	after := deref(in.StartAfter)
	if in.ContinuationToken != nil {
		key, err := parseContinuationToken(*in.ContinuationToken)
		if err != nil {
			return nil, err
		}
		after = key
	}
	entries, truncated := page(liveKeys(b.Objects), p.query(after))
	contents, prefixes := listedObjects(b, entries, in.FetchOwner != nil && *in.FetchOwner)
	out := &s3api.ListObjectsV2Output{
		Name: new(b.Name), Prefix: new(p.prefix), Delimiter: in.Delimiter,
		MaxKeys: count(p.limit), KeyCount: count(len(entries)), IsTruncated: new(truncated), EncodingType: p.encoding,
		ContinuationToken: in.ContinuationToken, StartAfter: in.StartAfter, Contents: contents, CommonPrefixes: prefixes,
	}
	if truncated {
		out.NextContinuationToken = new(continuationToken(last(entries)))
	}
	return out, nil
}

// listObjectVersions is ListObjectVersions: keys in byte order, each
// key's versions newest first, delete markers in their own list; every
// version and common prefix counts against max-keys (s3.md 9.3).
func listObjectVersions(in *s3api.ListObjectVersionsInput, b *Bucket) (*s3api.ListObjectVersionsOutput, *awserr.Error) {
	p, err := readListParams(in.Prefix, in.Delimiter, in.MaxKeys, in.EncodingType)
	if err != nil {
		return nil, err
	}
	keyMarker, versionMarker := deref(in.KeyMarker), deref(in.VersionIdMarker)
	out := &s3api.ListObjectVersionsOutput{
		Name: new(b.Name), Prefix: new(p.prefix), Delimiter: in.Delimiter,
		KeyMarker: new(keyMarker), VersionIdMarker: new(versionMarker), MaxKeys: count(p.limit), EncodingType: p.encoding,
	}
	items := versionItems(b, p, keyMarker, versionMarker)
	truncated := len(items) > p.limit
	if truncated {
		items = items[:p.limit]
		lastItem := items[len(items)-1]
		out.NextKeyMarker = new(lastItem.key)
		if !lastItem.prefix {
			out.NextVersionIdMarker = new(lastItem.version.ID)
		}
	}
	out.IsTruncated = new(truncated)
	owner := &s3api.Owner{ID: new(canonicalID(b.Home.OwnerAccount))}
	for _, it := range items {
		switch {
		case it.prefix:
			out.CommonPrefixes = append(out.CommonPrefixes, s3api.CommonPrefix{Prefix: new(it.key)})
		case it.version.marker():
			out.DeleteMarkers = append(out.DeleteMarkers, s3api.DeleteMarkerEntry{
				Key: new(it.key), VersionId: new(it.version.ID), IsLatest: new(it.latest), LastModified: new(it.version.Modified.Wire()), Owner: owner,
			})
		default:
			o := it.version.Object
			v := s3api.ObjectVersion{
				Key: new(it.key), VersionId: new(it.version.ID), IsLatest: new(it.latest), LastModified: new(it.version.Modified.Wire()),
				ETag: new(quote(o.ETag)), Size: new(o.Size), StorageClass: new(s3api.ObjectVersionStorageClass(o.StorageClass)), Owner: owner,
			}
			v.ChecksumAlgorithm, v.ChecksumType = listedChecksum(o)
			out.Versions = append(out.Versions, v)
		}
	}
	return out, nil
}

// versionItem is one entry of a version listing: a version of a key, or a
// common prefix.
type versionItem struct {
	key     string
	prefix  bool
	version Version
	latest  bool
}

// versionItems lists, after the markers, up to one more item than the
// page holds, so the caller can tell a truncated page; a zero limit lists
// nothing and is not truncated (s3.md 9.1).
func versionItems(b *Bucket, p listParams, keyMarker, versionMarker string) []versionItem {
	if p.limit == 0 {
		return nil
	}
	var items []versionItem
	if versionMarker != "" {
		chain := b.Objects.Keys[keyMarker]
		if i := slices.IndexFunc(chain, func(v Version) bool { return v.ID == versionMarker }); i >= 0 && strings.HasPrefix(keyMarker, p.prefix) {
			for j := i + 1; j < len(chain); j++ {
				items = append(items, versionItem{key: keyMarker, version: chain[j], latest: j == 0})
			}
		}
	}
	entries, _ := page(slices.Sorted(maps.Keys(b.Objects.Keys)), listQuery{prefix: p.prefix, delimiter: p.delimiter, after: keyMarker, limit: p.limit + 1})
	for _, e := range entries {
		if len(items) > p.limit {
			break
		}
		if e.prefix {
			items = append(items, versionItem{key: e.key, prefix: true})
			continue
		}
		for j, v := range b.Objects.Keys[e.key] {
			items = append(items, versionItem{key: e.key, version: v, latest: j == 0})
		}
	}
	return items
}

// count is n as a wire integer; every count S3 reports is far below the
// int32 range.
func count(n int) *int32 {
	if n > math.MaxInt32 {
		n = math.MaxInt32
	}
	return new(int32(n))
}
