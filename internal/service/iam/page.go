package iam

import (
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/paging"
)

const (
	// defaultMaxItems is the page size of an IAM listing without MaxItems.
	defaultMaxItems = 100
	// maxMarkerLen is the model's length limit on a Marker a client sends
	// back (markerType).
	maxMarkerLen = 320
)

// paginate cuts one page of listing l out of items, which are sorted by
// sortKey ascending with unique keys, starting at marker. Each operation
// names its own listing, so a Marker one listing issued is InvalidInput to
// every other rather than a seek to an empty page.
func paginate[T any](l paging.Listing, items []T, sortKey func(T) string, marker *string, maxItems *int32) (paging.Page[T], *awserr.Error) {
	limit := defaultMaxItems
	if maxItems != nil {
		limit = int(*maxItems)
	}
	p, err := paging.Keyset(l, items, sortKey, limit, marker)
	if err != nil {
		return paging.Page[T]{}, errInvalidMarker()
	}
	return p, nil
}

// boundedSortKey is key bounded for listing l so that the Marker naming it
// fits maxMarkerLen (paging.Listing.BoundedKey). A listing whose natural key
// can be longer, such as a policy ARN under a long path or a tag key of
// multibyte characters, sorts and paginates by this key instead, so every
// Marker it hands out is one the validator accepts back.
func boundedSortKey(l paging.Listing, key string) string {
	return l.BoundedKey(key, maxMarkerLen)
}
