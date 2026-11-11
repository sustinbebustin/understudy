package sns

import (
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/paging"
)

// pageSize is how many topics or subscriptions one List call returns
// (sns.md 2.2, 3.4).
const pageSize = 100

// The SNS listings, each named inside the tokens it issues.
const (
	listingTopics             paging.Listing = "topics"
	listingSubscriptions      paging.Listing = "subscriptions"
	listingTopicSubscriptions paging.Listing = "topic-subscriptions"
)

// page sorts items by key in place and returns the page of listing l that a
// NextToken names. Keys must be unique. A token no page of l issued is
// "Invalid parameter: NextToken" (inferred text); items created or deleted
// between pages never make a listing repeat an item or skip one present
// throughout (inferred, see internal/paging).
func page[T any](l paging.Listing, items []T, key func(T) string, token *string) (paging.Page[T], *awserr.Error) {
	p, err := paging.SortedKeyset(l, items, key, pageSize, token)
	if err != nil {
		return paging.Page[T]{}, errInvalidParameter("NextToken")
	}
	return p, nil
}
