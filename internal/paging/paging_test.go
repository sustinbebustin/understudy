package paging_test

import (
	"encoding/base64"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/paging"
)

const keys paging.Listing = "keys"

// sortedKeys draws up to n distinct keys, sorted ascending.
func sortedKeys(t *rapid.T, n int) []string {
	return slices.Sorted(slices.Values(rapid.SliceOfNDistinct(rapid.String(), 0, n, rapid.ID[string]).Draw(t, "items")))
}

func identity(s string) string { return s }

// markerFor is the marker Keyset hands out for a page that ends before key.
func markerFor(tb rapid.TB, l paging.Listing, key string) string {
	tb.Helper()
	p, err := paging.Keyset(l, []string{"", key}, identity, 1, nil)
	if err != nil || p.Next == nil {
		tb.Fatalf("Keyset(%q, [\"\" %q], limit 1) = %+v, %v; want a marker", l, key, p, err)
	}
	return *p.Next
}

// pageThrough follows markers from the first page until one is not
// truncated, calling between with the items served so far before each
// later page, and returns every item served.
func pageThrough(t *rapid.T, l paging.Listing, items func() []string, limit int, between func(served []string)) []string {
	var served []string
	var marker *string
	for range 10_000 {
		p, err := paging.Keyset(l, items(), identity, limit, marker)
		if err != nil {
			t.Fatalf("Keyset(marker %v) = %v; a marker Keyset handed out was refused", marker, err)
		}
		served = append(served, p.Items...)
		if p.Truncated != (p.Next != nil) {
			t.Fatalf("page truncated %v with Next %v", p.Truncated, p.Next)
		}
		if !p.Truncated {
			return served
		}
		marker = p.Next
		between(served)
	}
	t.Fatalf("listing did not end; served %q", served)
	return nil
}

// Paging through any sorted listing with any limit serves every item once,
// in order, and every page but the last is full.
func TestKeysetServesEveryItemOnce(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		items := sortedKeys(t, 40)
		limit := rapid.IntRange(1, 12).Draw(t, "limit")
		var sizes []int
		var marker *string
		var served []string
		for {
			p, err := paging.Keyset(keys, items, identity, limit, marker)
			if err != nil {
				t.Fatal(err)
			}
			served = append(served, p.Items...)
			sizes = append(sizes, len(p.Items))
			if !p.Truncated {
				break
			}
			marker = p.Next
		}
		if !slices.Equal(served, items) {
			t.Fatalf("pages served %q, want %q", served, items)
		}
		for i, n := range sizes[:len(sizes)-1] {
			if n != limit {
				t.Fatalf("page %d held %d items, want the limit %d", i, n, limit)
			}
		}
	})
}

// How one change between pages picks its item.
const (
	// deleteNext deletes the next unserved item, the one the marker names.
	deleteNext = iota
	// deleteAny deletes any present item, served or not.
	deleteAny
	// insert creates an item, ahead of the cursor or behind it.
	insert
)

// Items created or deleted between pages never make the listing repeat an
// item or skip one present from the first page to the last.
func TestKeysetSurvivesChangesBetweenPages(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		present := map[string]bool{}
		for _, k := range sortedKeys(t, 30) {
			present[k] = true
		}
		throughout := maps.Clone(present)
		items := func() []string { return slices.Sorted(maps.Keys(present)) }
		remove := func(k string) {
			delete(present, k)
			delete(throughout, k)
		}
		between := func(served []string) {
			for range rapid.IntRange(0, 3).Draw(t, "changes") {
				now := items()
				switch rapid.IntRange(deleteNext, insert).Draw(t, "change") {
				case deleteNext:
					// A truncated page served at least one item, and the
					// marker names the first present key after the last.
					i, found := slices.BinarySearch(now, served[len(served)-1])
					if found {
						i++
					}
					if i < len(now) {
						remove(now[i])
					}
				case deleteAny:
					if len(now) > 0 {
						remove(rapid.SampledFrom(now).Draw(t, "deleted"))
					}
				case insert:
					present[rapid.String().Draw(t, "inserted")] = true
				}
			}
		}
		served := pageThrough(t, keys, items, rapid.IntRange(1, 6).Draw(t, "limit"), between)
		seen := map[string]bool{}
		for _, k := range served {
			if seen[k] {
				t.Fatalf("%q served twice in %q", k, served)
			}
			seen[k] = true
		}
		for k := range throughout {
			if !seen[k] {
				t.Fatalf("%q, present throughout, was skipped; served %q", k, served)
			}
		}
	})
}

// A marker names the key the next page starts at, and the listing it came
// from: given back, it resumes there, and when that item is gone, at the
// first greater key.
func TestMarkerRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		items := sortedKeys(t, 20)
		key := rapid.String().Filter(func(s string) bool { return s != "" }).Draw(t, "key")
		marker := markerFor(t, keys, key)
		p, err := paging.Keyset(keys, items, identity, len(items)+1, &marker)
		if err != nil {
			t.Fatalf("Keyset refused its own marker for %q: %v", key, err)
		}
		i, _ := slices.BinarySearch(items, key)
		if !slices.Equal(p.Items, items[i:]) {
			t.Fatalf("marker for %q resumed at %q, want %q", key, p.Items, items[i:])
		}
	})
}

// The marker is base64url, unpadded, of "v1|<listing>|<key>": the format is
// pinned, so a client holding a marker from an earlier build can still send
// it back.
func TestMarkerFormat(t *testing.T) {
	got := markerFor(t, "aliases", "alias/b")
	if want := base64.RawURLEncoding.EncodeToString([]byte("v1|aliases|alias/b")); got != want {
		t.Fatalf("marker = %q, want %q", got, want)
	}
}

// A marker of another listing is refused, never read as a seek.
func TestForeignListingMarkerRefused(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		listing := rapid.StringMatching(`[a-z-]{1,12}`)
		from, to := paging.Listing(listing.Draw(t, "from")), paging.Listing(listing.Draw(t, "to"))
		if from == to {
			t.Skip("same listing")
		}
		marker := markerFor(t, from, rapid.StringN(1, 20, -1).Draw(t, "key"))
		_, err := paging.Keyset(to, sortedKeys(t, 10), identity, 5, &marker)
		wantRefused(t, err, marker, to, paging.OtherListing)
	})
}

// A string Keyset never handed out is refused, whatever it decodes to.
func TestRandomMarkerRefused(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		marker := rapid.OneOf(
			rapid.String(),
			rapid.Map(rapid.SliceOf(rapid.Byte()), base64.RawURLEncoding.EncodeToString),
		).Filter(func(s string) bool {
			raw, err := base64.RawURLEncoding.DecodeString(s)
			key, issued := strings.CutPrefix(string(raw), "v1|keys|")
			return err != nil || !issued || key == ""
		}).Draw(t, "marker")
		p, err := paging.Keyset(keys, sortedKeys(t, 10), identity, 5, &marker)
		if err == nil {
			t.Fatalf("Keyset accepted marker %q and served %+v", marker, p)
		}
		if err.Marker != marker || err.Listing != keys {
			t.Fatalf("MarkerError = %+v, want marker %q of listing %q", err, marker, keys)
		}
	})
}

// Each way a marker can be wrong has its own reason.
func TestMarkerErrorReasons(t *testing.T) {
	encode := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for _, tc := range []struct {
		marker string
		reason paging.MarkerReason
	}{
		{"!!", paging.Undecodable},
		{encode("v1|aliases|alias/a"), paging.OtherListing},
		{encode("v2|keys|a"), paging.Malformed},
		{encode("v1|keys|"), paging.Malformed},
		{encode("v1|keys"), paging.Malformed},
		{encode("Zm9v"), paging.Malformed},
		{"Zm9v", paging.Malformed},
	} {
		_, err := paging.Keyset(keys, []string{"a", "b"}, identity, 1, &tc.marker)
		wantRefused(t, err, tc.marker, keys, tc.reason)
	}
}

func wantRefused(tb rapid.TB, err *paging.MarkerError, marker string, l paging.Listing, reason paging.MarkerReason) {
	tb.Helper()
	want := &paging.MarkerError{Marker: marker, Listing: l, Reason: reason}
	if err == nil || *err != *want {
		tb.Fatalf("Keyset(%q, marker %q) error = %v, want %v", l, marker, err, want)
	}
	var target *paging.MarkerError
	if !errors.As(error(err), &target) || target.Error() == "" {
		tb.Fatalf("MarkerError %v does not describe itself", err)
	}
}

// SortedKeyset sorts the items by key before cutting the page.
func TestSortedKeysetSorts(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		items := sortedKeys(t, 20)
		shuffled := rapid.Permutation(items).Draw(t, "shuffled")
		limit := rapid.IntRange(1, 25).Draw(t, "limit")
		got, err := paging.SortedKeyset(keys, shuffled, identity, limit, nil)
		if err != nil {
			t.Fatal(err)
		}
		want, err := paging.Keyset(keys, items, identity, limit, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Items, want.Items) || got.Truncated != want.Truncated || (got.Next == nil) != (want.Next == nil) || (got.Next != nil && *got.Next != *want.Next) {
			t.Fatalf("SortedKeyset = %+v, Keyset of the sorted items = %+v", got, want)
		}
	})
}

// A bounded key's marker fits the limit; a key whose marker already fits is
// kept as it is; distinct keys stay distinct.
func TestBoundedKeyFitsTheMarkerLimit(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		l := paging.Listing(rapid.StringMatching(`[a-z-]{1,30}`).Draw(t, "listing"))
		maxLen := rapid.IntRange(100, 400).Draw(t, "maxMarkerLen")
		a := rapid.StringN(1, 300, -1).Draw(t, "a")
		b := rapid.StringN(1, 300, -1).Draw(t, "b")
		for _, k := range []string{a, b} {
			bounded := l.BoundedKey(k, maxLen)
			if n := len(markerFor(t, l, bounded)); n > maxLen {
				t.Fatalf("marker of BoundedKey(%q, %d) is %d characters", k, maxLen, n)
			}
			if fits := len(markerFor(t, l, k)) <= maxLen; fits && bounded != k {
				t.Fatalf("BoundedKey(%q) = %q, but the key's marker already fits", k, bounded)
			}
		}
		if a != b && l.BoundedKey(a, maxLen) == l.BoundedKey(b, maxLen) {
			t.Fatalf("BoundedKey maps %q and %q to the same key", a, b)
		}
	})
}
