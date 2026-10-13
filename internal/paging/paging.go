// Package paging cuts keyset pages out of listings and issues the opaque
// markers that resume them.
//
// A marker is the unpadded base64url form of "v1|<listing>|<key>": the
// format version, the listing that issued it and the key of the next page's
// first item. Resuming seeks to the first item whose key is at least that
// key, so items created or deleted between pages never make a listing repeat
// an item or skip one present throughout. A marker of another format or
// another listing is refused with a *MarkerError, which each service maps to
// its own AWS error, rather than read as a seek that answers an empty page.
//
// The package imports nothing in the repository (ARCHITECTURE.md 2).
package paging

import (
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"slices"
	"sort"
	"strings"
)

// Listing names one paged listing, usually one operation, inside its
// markers, so a marker one listing issued is refused by every other. It
// never contains "|".
type Listing string

// Page is one page of a listing. Next is the marker of the page after it,
// set exactly when Truncated is.
type Page[T any] struct {
	Items     []T
	Next      *string
	Truncated bool
}

// markerVersion starts every marker, so a marker of another format is
// refused rather than misread.
const markerVersion = "v1"

// hashDigits is how many hex digits of a key's SHA-256 BoundedKey keeps.
const hashDigits = 16

// Keyset returns the page of items that starts at marker (the first page
// when marker is nil) and holds at most limit items, which must be at least
// 1. Items must be sorted by key ascending, and keys must be unique; a
// marker naming a key no item has resumes at the first greater key. The
// error is non-nil exactly when marker is not a well-formed marker of l, and
// no page is served then. It is a concrete *MarkerError, as *awserr.Error is
// elsewhere, so a caller checks it against nil before it becomes an error.
func Keyset[T any](l Listing, items []T, key func(T) string, limit int, marker *string) (Page[T], *MarkerError) {
	start := 0
	if marker != nil {
		from, err := l.parse(*marker)
		if err != nil {
			return Page[T]{}, err
		}
		start = sort.Search(len(items), func(i int) bool { return key(items[i]) >= from })
	}
	end := min(start+limit, len(items))
	p := Page[T]{Items: items[start:end]}
	if end < len(items) {
		p.Next = new(base64.RawURLEncoding.EncodeToString([]byte(l.prefix() + key(items[end]))))
		p.Truncated = true
	}
	return p, nil
}

// SortedKeyset sorts items by key in place, then cuts the page Keyset
// would. Keys must be unique.
func SortedKeyset[T any](l Listing, items []T, key func(T) string, limit int, marker *string) (Page[T], *MarkerError) {
	slices.SortFunc(items, func(a, b T) int { return cmp.Compare(key(a), key(b)) })
	return Keyset(l, items, key, limit, marker)
}

// BoundedKey is key when the marker naming it in l fits maxMarkerLen
// characters, and otherwise as much of key's start as leaves room for 16 hex
// digits of its SHA-256, followed by them. A listing whose model limits the
// marker a client sends back, and whose natural key can be longer, sorts and
// pages by this key instead, so every marker it issues is one the validator
// accepts. Two keys collide only on a 64-bit hash collision between keys
// sharing their kept start. maxMarkerLen must leave room for the marker's
// "v1|<listing>|" and the 16 digits.
func (l Listing) BoundedKey(key string, maxMarkerLen int) string {
	room := base64.RawURLEncoding.DecodedLen(maxMarkerLen) - len(l.prefix())
	if len(key) <= room {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return key[:room-hashDigits] + hex.EncodeToString(sum[:hashDigits/2])
}

func (l Listing) prefix() string { return markerVersion + "|" + string(l) + "|" }

// parse returns the key a marker l issued names.
func (l Listing) parse(marker string) (string, *MarkerError) {
	refuse := func(r MarkerReason) (string, *MarkerError) {
		return "", &MarkerError{Marker: marker, Listing: l, Reason: r}
	}
	raw, err := base64.RawURLEncoding.DecodeString(marker)
	if err != nil {
		return refuse(Undecodable)
	}
	version, rest, ok := strings.Cut(string(raw), "|")
	if !ok || version != markerVersion {
		return refuse(Malformed)
	}
	issuer, from, ok := strings.Cut(rest, "|")
	switch {
	case !ok || from == "":
		return refuse(Malformed)
	case Listing(issuer) != l:
		return refuse(OtherListing)
	}
	return from, nil
}
