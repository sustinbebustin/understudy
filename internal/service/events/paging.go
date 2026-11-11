package events

import (
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/paging"
)

// defaultPageSize is the page size of a List call without Limit; the
// models cap Limit at 100 and AWS documents no default.
const defaultPageSize = 100

// The EventBridge listings, each named inside the tokens it issues.
const (
	listingBuses             paging.Listing = "buses"
	listingRules             paging.Listing = "rules"
	listingRuleNamesByTarget paging.Listing = "rule-names-by-target"
	listingTargets           paging.Listing = "targets"
)

// page sorts items by key in place and returns the page of listing l that
// a NextToken and Limit select, and the token of the next page. Keys must
// be unique. A token no page of l issued answers ValidationException. An
// empty page is an empty slice, never nil: the serializers leave a nil list
// out, and AWS sends an empty one ({"Rules": []}). Items created or deleted
// between pages never make a listing repeat an item or skip one present
// throughout (inferred, see internal/paging).
func page[T any](l paging.Listing, items []T, key func(T) string, token *string, limit *int32) (out []T, next *string, err *awserr.Error) {
	size := defaultPageSize
	if limit != nil && *limit > 0 {
		size = int(*limit)
	}
	p, markerErr := paging.SortedKeyset(l, items, key, size, token)
	if markerErr != nil {
		return nil, nil, errInvalidParameter("NextToken")
	}
	if len(p.Items) == 0 {
		return []T{}, nil, nil
	}
	return p.Items, p.Next, nil
}
