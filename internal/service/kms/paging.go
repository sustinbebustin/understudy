package kms

import (
	"cmp"
	"encoding/base64"
	"slices"
	"sort"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// listPage is one page of a KMS listing and the marker of the next.
type listPage[T any] struct {
	items     []T
	next      *string
	truncated bool
}

// limits are an operation's documented page sizes: def when Limit is
// absent, and max, the largest it serves however large Limit is (the model
// allows up to 1000 for every KMS listing).
type limits struct {
	def, max int
}

// listing names a paged listing inside its markers, so a marker of one
// listing is refused by another rather than read as a seek. It never
// contains "|".
type listing string

const (
	listingKeys    listing = "keys"
	listingAliases listing = "aliases"
	listingTags    listing = "tags"
)

// markerVersion starts every marker, so a marker of another format is
// refused rather than misread.
const markerVersion = "v1"

func (l listing) prefix() string { return markerVersion + "|" + string(l) + "|" }

// page sorts items by key in place and returns the page that starts at
// marker and holds at most limit items (lim.def when limit is nil, never
// more than lim.max). Keys must be unique. The marker is opaque to clients:
// the base64 of the format version, the listing and the next item's key, so
// a listing that changes between pages neither repeats nor skips an item
// still present, and a marker no page of l returned is
// InvalidMarkerException rather than a silent seek.
func page[T any](l listing, items []T, key func(T) string, limit *int32, lim limits, marker *string) (listPage[T], *awserr.Error) {
	slices.SortFunc(items, func(a, b T) int { return cmp.Compare(key(a), key(b)) })
	start := 0
	if marker != nil {
		from, ok := parseMarker(l, *marker)
		if !ok {
			return listPage[T]{}, errInvalidMarker(*marker)
		}
		start = sort.Search(len(items), func(i int) bool { return key(items[i]) >= from })
	}
	n := lim.def
	if limit != nil {
		n = min(int(*limit), lim.max)
	}
	end := min(start+n, len(items))
	p := listPage[T]{items: items[start:end]}
	if end < len(items) {
		p.next = new(base64.RawURLEncoding.EncodeToString([]byte(l.prefix() + key(items[end]))))
		p.truncated = true
	}
	return p, nil
}

// parseMarker returns the key a marker page issued for l names.
func parseMarker(l listing, marker string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(marker)
	if err != nil {
		return "", false
	}
	from, ok := strings.CutPrefix(string(raw), l.prefix())
	return from, ok && from != ""
}
