package kms

import (
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/paging"
)

// limits are an operation's documented page sizes: def when Limit is
// absent, and max, the largest it serves however large Limit is (the model
// allows up to 1000 for every KMS listing).
type limits struct {
	def, max int
}

// The KMS listings, each named inside the markers it issues.
const (
	listingKeys    paging.Listing = "keys"
	listingAliases paging.Listing = "aliases"
	listingTags    paging.Listing = "tags"
)

// page sorts items by key in place and returns the page of listing l that
// starts at marker and holds at most limit items (lim.def when limit is nil,
// never more than lim.max). Keys must be unique. A marker no page of l
// issued is InvalidMarkerException rather than a silent seek.
func page[T any](l paging.Listing, items []T, key func(T) string, limit *int32, lim limits, marker *string) (paging.Page[T], *awserr.Error) {
	n := lim.def
	if limit != nil {
		n = min(int(*limit), lim.max)
	}
	p, err := paging.SortedKeyset(l, items, key, n, marker)
	if err != nil {
		return paging.Page[T]{}, errInvalidMarker(err.Marker)
	}
	return p, nil
}
