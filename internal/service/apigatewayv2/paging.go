package apigatewayv2

import (
	"encoding/base64"
	"slices"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// page is one page of a Get list, which pages through maxResults and
// nextToken without the @paginated trait (apigateway-http.md 2.2). items
// are in list order, and pos gives each a position that increases along
// the list. Without maxResults the page holds every item from the token
// on; with it, at most that many, and maxResults must be a positive
// integer. The token is the position of the next page's first item, so a
// page follows on whatever was deleted in between. AWS's token format and
// its answers to a bad maxResults or token are unrecorded (APIGW2-03); a
// token no page answered is BadRequestException.
func page[T any](items []T, pos func(T) uint64, maxResults, nextToken *string) (got []T, next *string, err *awserr.Error) {
	limit := len(items)
	if maxResults != nil {
		n, perr := strconv.Atoi(*maxResults)
		if perr != nil || n < 1 {
			return nil, nil, errBadRequest("maxResults %q is not a positive integer", *maxResults)
		}
		limit = n
	}
	if nextToken != nil {
		from, ok := decodeToken(*nextToken)
		if !ok {
			return nil, nil, errBadRequest("nextToken %q is not a token a previous page answered; start again without nextToken", *nextToken)
		}
		start := slices.IndexFunc(items, func(t T) bool { return pos(t) >= from })
		if start < 0 {
			start = len(items)
		}
		items = items[start:]
	}
	if len(items) <= limit {
		return items, nil, nil
	}
	return items[:limit], new(encodeToken(pos(items[limit]))), nil
}

// encodeToken writes a page position as an opaque token.
func encodeToken(pos uint64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatUint(pos, 10)))
}

// decodeToken reads a token encodeToken wrote.
func decodeToken(token string) (uint64, bool) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, false
	}
	pos, err := strconv.ParseUint(string(b), 10, 64)
	return pos, err == nil
}
