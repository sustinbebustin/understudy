package kms

import (
	"encoding/base64"
	"sort"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// listPage is one page of a KMS listing and the marker of the next.
type listPage[T any] struct {
	items     []T
	next      *string
	truncated bool
}

// limits are an operation's documented page sizes: def when Limit is
// absent, and max, the largest it serves however large Limit is (the model
// allows up to 1000 for every KMS listing).
type limits struct {
	def, max int
}

// page returns the page of items, sorted by key, that starts at marker and
// holds at most limit items (lim.def when limit is nil, never more than
// lim.max). The marker is opaque to clients: the base64 of the next item's
// key, so a listing that changes between pages neither repeats nor skips an
// item still present.
func page[T any](items []T, key func(T) string, limit *int32, lim limits, marker *string) (listPage[T], *awserr.Error) {
	start := 0
	if marker != nil {
		from, err := base64.RawURLEncoding.DecodeString(*marker)
		if err != nil {
			return listPage[T]{}, kmsapi.ErrInvalidMarkerException("The marker " + *marker + " is not one a previous page returned.")
		}
		start = sort.Search(len(items), func(i int) bool { return key(items[i]) >= string(from) })
	}
	n := lim.def
	if limit != nil {
		n = min(int(*limit), lim.max)
	}
	end := min(start+n, len(items))
	p := listPage[T]{items: items[start:end]}
	if end < len(items) {
		p.next = new(base64.RawURLEncoding.EncodeToString([]byte(key(items[end]))))
		p.truncated = true
	}
	return p, nil
}
