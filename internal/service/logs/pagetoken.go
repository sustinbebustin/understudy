package logs

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
)

// A listing token of DescribeLogGroups and DescribeLogStreams is the sort
// key of the last item a page returned, so a page stays correct when items
// are created or deleted between calls.

func keyToken(key string) string { return base64.RawURLEncoding.EncodeToString([]byte(key)) }

func parseKeyToken(token string) (string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) == 0 {
		return "", false
	}
	return string(b), true
}

// timeKey is a sort key that orders by an event time, then by name.
func timeKey(ms int64, name string) string { return fmt.Sprintf("%020d/%s", ms, name) }

// keyed is a listed item with its sort key.
type keyed[T any] struct {
	key  string
	item T
}

// pageOf sorts items by key, ascending or descending, and returns the page
// after the token's key of at most limit items, with the token of the next
// page when more remain. An unreadable token is reported as !ok.
func pageOf[T any](items []keyed[T], descending bool, token *string, limit int) (page []T, next *string, ok bool) {
	slices.SortFunc(items, func(a, b keyed[T]) int {
		if descending {
			return strings.Compare(b.key, a.key)
		}
		return strings.Compare(a.key, b.key)
	})
	start := 0
	if token != nil {
		after, valid := parseKeyToken(*token)
		if !valid {
			return nil, nil, false
		}
		start = slices.IndexFunc(items, func(it keyed[T]) bool {
			if descending {
				return it.key < after
			}
			return it.key > after
		})
		if start < 0 {
			start = len(items)
		}
	}
	end := min(start+limit, len(items))
	for _, it := range items[start:end] {
		page = append(page, it.item)
	}
	if end < len(items) {
		next = new(keyToken(items[end-1].key))
	}
	return page, next, true
}
