package iam

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sort"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

const (
	// defaultMaxItems is the page size of an IAM listing without MaxItems.
	defaultMaxItems = 100
	// maxMarkerLen is the model's length limit on a Marker a client sends
	// back (markerType).
	maxMarkerLen = 320
	// boundedKeyPrefix is how much of an over-long sort key boundedSortKey
	// keeps verbatim.
	boundedKeyPrefix = 160
)

// page is one page of an IAM listing: its items, whether more follow, and
// the Marker that fetches the rest.
type page[T any] struct {
	items     []T
	truncated bool
	marker    *string
}

// paginate cuts one page out of items, which are sorted by sortKey
// ascending, starting at marker. The Marker is opaque to clients: the
// base64 form of the sort key of the next page's first item, so a listing
// resumes correctly after entities before it are added or deleted.
func paginate[T any](items []T, sortKey func(T) string, marker *string, maxItems *int32) (page[T], *awserr.Error) {
	start := 0
	if marker != nil {
		from, err := base64.RawURLEncoding.DecodeString(*marker)
		if err != nil {
			return page[T]{}, errInvalidMarker()
		}
		start = sort.Search(len(items), func(i int) bool { return sortKey(items[i]) >= string(from) })
	}
	limit := defaultMaxItems
	if maxItems != nil {
		limit = int(*maxItems)
	}
	rest := items[start:]
	if len(rest) <= limit {
		return page[T]{items: rest}, nil
	}
	next := base64.RawURLEncoding.EncodeToString([]byte(sortKey(rest[limit])))
	return page[T]{items: rest[:limit], truncated: true, marker: &next}, nil
}

// boundedSortKey is key when its Marker fits maxMarkerLen, and otherwise
// key's first boundedKeyPrefix bytes followed by 16 hex digits of its
// SHA-256, whose Marker is 235 characters. A listing whose natural key can
// be longer, such as a policy ARN under a long path, sorts and paginates
// by this key instead, so every Marker it hands out is one the validator
// accepts back. Two keys collide only on a 64-bit hash collision between
// keys sharing their first boundedKeyPrefix bytes.
func boundedSortKey(key string) string {
	if base64.RawURLEncoding.EncodedLen(len(key)) <= maxMarkerLen {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return key[:boundedKeyPrefix] + hex.EncodeToString(sum[:8])
}
