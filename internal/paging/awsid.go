package paging

import (
	"cmp"
	"encoding/base64"
	"net/url"
	"slices"
	"strings"
)

// awsIDPrefix starts a decoded AWS id position.
const awsIDPrefix = "id=S"

// AWSIDKeyset returns the page of items an AWS list that sorts by id gives:
// items sorted by id ascending, at most limit of them (which must be at
// least 1), starting after the id token names (the first page when token is
// nil). The token it issues is AWS's: the URL-escaped base64 of "id=S" and
// the page's last id (API Gateway REST GetResources, APIGW1-04, and HTTP
// GetApis, APIGW2-03, both recorded). AWS took the token escaped once more,
// so ok is false only for a token that is not that after unescaping up to
// twice, and no page is served then. Unlike Keyset's marker, the token names
// the id the previous page ended on, so the next page starts after it.
// items is not modified.
func AWSIDKeyset[T any](items []T, id func(T) string, limit int, token *string) (p Page[T], ok bool) {
	items = slices.SortedFunc(slices.Values(items), func(a, b T) int { return cmp.Compare(id(a), id(b)) })
	if token != nil {
		after, ok := decodeAWSID(*token)
		if !ok {
			return Page[T]{}, false
		}
		start := slices.IndexFunc(items, func(t T) bool { return id(t) > after })
		if start < 0 {
			start = len(items)
		}
		items = items[start:]
	}
	if len(items) <= limit {
		return Page[T]{Items: items}, true
	}
	last := id(items[limit-1])
	return Page[T]{
		Items:     items[:limit],
		Next:      new(url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(awsIDPrefix + last)))),
		Truncated: true,
	}, true
}

// decodeAWSID reads the id an AWSIDKeyset token names.
func decodeAWSID(token string) (string, bool) {
	for range 2 {
		if b, err := base64.StdEncoding.DecodeString(token); err == nil {
			return strings.CutPrefix(string(b), awsIDPrefix)
		}
		unescaped, err := url.QueryUnescape(token)
		if err != nil || unescaped == token {
			return "", false
		}
		token = unescaped
	}
	return "", false
}
