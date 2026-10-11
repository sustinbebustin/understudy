package cloudwatch

import (
	"encoding/base64"
	"sort"
)

// pageOf returns the page of items, sorted by key, after the token's key,
// with at most limit items and the token of the next page when more
// remain. A token is the base64 of the last key a page returned, so a
// listing that changes between pages neither repeats nor skips an item
// still present. ok is false for a token this service did not issue.
func pageOf[T any](items []T, key func(T) string, token *string, limit int) (page []T, next *string, ok bool) {
	start := 0
	if token != nil {
		after, err := base64.RawURLEncoding.DecodeString(*token)
		if err != nil || len(after) == 0 {
			return nil, nil, false
		}
		start = sort.Search(len(items), func(i int) bool { return key(items[i]) > string(after) })
	}
	end := min(start+limit, len(items))
	if end < len(items) {
		next = new(base64.RawURLEncoding.EncodeToString([]byte(key(items[end-1]))))
	}
	return items[start:end], next, true
}
