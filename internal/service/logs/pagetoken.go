package logs

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// tokenLife is how long a paging token stays valid on the emulator clock.
// AWS documents the 24 hours for GetLogEvents and FilterLogEvents tokens
// (logs-filter-patterns.md 6, rule N9); the listing tokens of
// DescribeLogGroups and DescribeLogStreams follow the same rule (inferred).
const tokenLife = 24 * time.Hour

// stampDigits is the width of a token's issue time in milliseconds.
const stampDigits = 20

// stamp appends the emulator instant a token is issued at to its body, as
// 20 digits of milliseconds.
func stamp(body string, issued clock.Instant) string {
	return fmt.Sprintf("%s%0*d", body, stampDigits, millis(issued))
}

// unstamp splits a stamped token into its body and issue instant. ok is
// false for a token without a stamp, and for one issued tokenLife or more
// before now, which has expired.
func unstamp(token string, now clock.Instant) (body string, issued clock.Instant, ok bool) {
	i := len(token) - stampDigits
	if i < 0 {
		return "", clock.Instant{}, false
	}
	ms, err := strconv.ParseUint(token[i:], 10, 63)
	if err != nil {
		return "", clock.Instant{}, false
	}
	issued = instantOf(int64(ms))
	if now.Sub(issued) >= tokenLife {
		return "", clock.Instant{}, false
	}
	return token[:i], issued, true
}

// listing is what a paging token pages: the operation and the scope, group
// or stream it lists. Every token carries its listing's fingerprint, so a
// token issued for another operation, group, stream, account or region is
// refused like any token the call did not issue, as AWS refuses it with
// "The specified nextToken is invalid." (secrets-logs-events-alarms.md 3.4,
// 3.5).
type listing string

func groupListing(scope aws.Scope) listing {
	return listing("DescribeLogGroups\x00" + groupRef{scope: scope}.arn().String())
}

func streamListing(ref groupRef) listing {
	return listing("DescribeLogStreams\x00" + ref.arn().String())
}

func eventListing(ref groupRef, stream string) listing {
	return listing("GetLogEvents\x00" + ref.streamARN(stream).String())
}

func filterListing(ref groupRef) listing {
	return listing("FilterLogEvents\x00" + ref.arn().String())
}

// fingerprintDigits is the width of a listing's fingerprint in a token.
const fingerprintDigits = 20

// fingerprint is the first 64 bits of the listing's SHA-256 digest as 20
// decimal digits, so a GetLogEvents token stays all digits after its
// direction.
func (l listing) fingerprint() string {
	sum := sha256.Sum256([]byte(l))
	return fmt.Sprintf("%0*d", fingerprintDigits, binary.BigEndian.Uint64(sum[:8]))
}

// A listing token of DescribeLogGroups and DescribeLogStreams is the
// listing's fingerprint and the sort key of the last item a page returned,
// so a page stays correct when items are created or deleted between calls,
// stamped with its issue time.

func keyToken(l listing, key string, issued clock.Instant) string {
	return base64.RawURLEncoding.EncodeToString([]byte(stamp(l.fingerprint()+key, issued)))
}

func parseKeyToken(token string, l listing, now clock.Instant) (string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", false
	}
	body, _, ok := unstamp(string(b), now)
	if !ok {
		return "", false
	}
	key, ok := strings.CutPrefix(body, l.fingerprint())
	if !ok || key == "" {
		return "", false
	}
	return key, true
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
// page of l, issued at now, when more remain. An unreadable or expired
// token, or one of another listing, is reported as !ok.
func pageOf[T any](items []keyed[T], l listing, descending bool, token *string, limit int, now clock.Instant) (page []T, next *string, ok bool) {
	slices.SortFunc(items, func(a, b keyed[T]) int {
		if descending {
			return strings.Compare(b.key, a.key)
		}
		return strings.Compare(a.key, b.key)
	})
	start := 0
	if token != nil {
		after, valid := parseKeyToken(*token, l, now)
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
		next = new(keyToken(l, items[end-1].key, now))
	}
	return page, next, true
}
