package apigateway

import (
	"encoding/base64"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The page sizes of a paginated Get list: "The default value is 25 and the
// maximum value is 500" (documented, apigateway-rest.md 2.5).
const (
	defaultPageLimit = 25
	maxPageLimit     = 500
)

// page is one page of a paginated Get list ([S] @paginated with position
// and limit, apigateway-rest.md 2.5). items are in list order, and pos
// gives each a position that increases along the list. The position
// answered is that of the next page's first item, so a page follows on
// whatever was deleted in between. AWS's position format and its answers
// to a limit out of range or a position it never answered are unrecorded;
// both are BadRequestException with inferred texts.
func page[T any](items []T, pos func(T) uint64, limit *int32, position *string) (got []T, next *string, err *awserr.Error) {
	n, err := pageLimit(limit)
	if err != nil {
		return nil, nil, err
	}
	if position != nil {
		from, ok := decodePosition(*position)
		if !ok {
			return nil, nil, errBadRequest("position %q is not a position a previous page answered; start again without position", *position)
		}
		start := slices.IndexFunc(items, func(t T) bool { return pos(t) >= from })
		if start < 0 {
			start = len(items)
		}
		items = items[start:]
	}
	if len(items) <= int(n) {
		return items, nil, nil
	}
	return items[:n], new(encodePosition(pos(items[n]))), nil
}

// pageByID is one page of a list AWS sorts by id, GetResources's
// (APIGW1-04, recorded 2026-11-03): items sorted by id ascending, and the
// position answered is base64 of "id=S" and the page's last id,
// URL-escaped as AWS sends it. The next page starts after that id. AWS
// took the position escaped once more, so it is unescaped until it is
// base64.
func pageByID[T any](items []T, id func(T) string, limit *int32, position *string) (got []T, next *string, err *awserr.Error) {
	n, err := pageLimit(limit)
	if err != nil {
		return nil, nil, err
	}
	items = slices.SortedFunc(slices.Values(items), func(a, b T) int { return strings.Compare(id(a), id(b)) })
	if position != nil {
		after, ok := decodeIDPosition(*position)
		if !ok {
			return nil, nil, errBadRequest("position %q is not a position a previous page answered; start again without position", *position)
		}
		start := slices.IndexFunc(items, func(t T) bool { return id(t) > after })
		if start < 0 {
			start = len(items)
		}
		items = items[start:]
	}
	if len(items) <= int(n) {
		return items, nil, nil
	}
	last := id(items[n-1])
	return items[:n], new(url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(idPositionPrefix + last)))), nil
}

// idPositionPrefix starts a decoded pageByID position.
const idPositionPrefix = "id=S"

// decodeIDPosition reads the id a pageByID position names.
func decodeIDPosition(token string) (string, bool) {
	for range 2 {
		if b, err := base64.StdEncoding.DecodeString(token); err == nil {
			return strings.CutPrefix(string(b), idPositionPrefix)
		}
		unescaped, err := url.QueryUnescape(token)
		if err != nil || unescaped == token {
			return "", false
		}
		token = unescaped
	}
	return "", false
}

// pageLimit is the page size limit asks for, the default when absent.
func pageLimit(limit *int32) (int32, *awserr.Error) {
	if limit == nil {
		return defaultPageLimit, nil
	}
	if *limit < 1 || *limit > maxPageLimit {
		return 0, errBadRequest("limit %d is out of range: it must be between 1 and %d", *limit, maxPageLimit)
	}
	return *limit, nil
}

// encodePosition writes a page position as an opaque token.
func encodePosition(pos uint64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatUint(pos, 10)))
}

// decodePosition reads a token encodePosition wrote.
func decodePosition(token string) (uint64, bool) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, false
	}
	pos, err := strconv.ParseUint(string(b), 10, 64)
	return pos, err == nil
}
