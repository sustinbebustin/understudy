package cloudwatch

import (
	"encoding/base64"
	"sort"
	"strings"
	"unicode/utf8"
)

// pageTokenPrefix starts every key token's payload, so a token this
// service did not issue is refused even when it happens to be valid
// base64 ("garbage" is).
const pageTokenPrefix = "after:"

// pageOf returns the page of items, sorted by key, after the token's key,
// with at most limit items and the token of the next page when more
// remain. A token is the base64 of pageTokenPrefix and the last key a page
// returned, so a listing that changes between pages neither repeats nor
// skips an item still present. ok is false for a token this service did
// not issue.
func pageOf[T any](items []T, key func(T) string, token *string, limit int) (page []T, next *string, ok bool) {
	start := 0
	if token != nil {
		after, ok := parseKeyToken(*token)
		if !ok {
			return nil, nil, false
		}
		start = sort.Search(len(items), func(i int) bool { return key(items[i]) > after })
	}
	end := min(start+limit, len(items))
	if end < len(items) {
		next = new(base64.RawURLEncoding.EncodeToString([]byte(pageTokenPrefix + key(items[end-1]))))
	}
	return items[start:end], next, true
}

// parseKeyToken returns the key a token pageOf issued names.
func parseKeyToken(token string) (string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || !utf8.Valid(b) {
		return "", false
	}
	after, ok := strings.CutPrefix(string(b), pageTokenPrefix)
	return after, ok && after != ""
}
