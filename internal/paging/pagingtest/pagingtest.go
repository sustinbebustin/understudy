// Package pagingtest checks, from a client's side, that a service listing
// built on package paging keeps its keyset promise: paging through it a few
// items at a time serves each item once, though items are created and
// deleted between pages.
package pagingtest

import (
	"fmt"
	"slices"
	"testing"
)

// Listing is one service listing as a client drives it. Items are named by
// the id a page reports them under.
type Listing struct {
	// Add creates one item from name, a lowercase letter and two digits,
	// and returns its id. A listing ordered by name lists names starting
	// "a" before those starting "m", and those before names starting "z".
	Add func(name string) string
	// Remove deletes the item id names.
	Remove func(id string)
	// Page reads the page of at most maxItems items that marker starts
	// (the first page when marker is nil), and the marker of the next page
	// when it is truncated.
	Page func(maxItems int32, marker *string) (ids []string, next *string)
}

// maxPages bounds a listing a check follows, so a pager that never ends
// fails the test instead of hanging it.
const maxPages = 1000

// SurvivesChanges adds seed items to l, then pages through it maxItems at
// a time. Between pages it removes the item the marker names (the next
// page's first), removes one item already served, and adds one, behind the
// cursor after even pages and ahead of it after odd ones; it removes only
// items it added. It fails tb when an item is served twice, when an item
// present from the first page to the last is not served, or when no page
// boundary had an item of its own to remove under the marker, since the
// check then proves nothing about seeking past a deleted key. seed must
// exceed maxItems.
func SurvivesChanges(tb testing.TB, l Listing, seed int, maxItems int32) {
	tb.Helper()
	ours := map[string]bool{}
	for n := range seed {
		ours[l.Add(fmt.Sprintf("m%02d", n))] = true
	}
	throughout := map[string]bool{}
	for _, id := range pageThrough(tb, l, maxItems, func(int, *string, []string) {}) {
		throughout[id] = true
	}
	remove := func(id string) {
		l.Remove(id)
		delete(ours, id)
		delete(throughout, id)
	}
	markerRemovals := 0
	served := pageThrough(tb, l, maxItems, func(page int, marker *string, served []string) {
		next, _ := l.Page(1, marker)
		if len(next) != 1 {
			tb.Fatalf("after page %d, the marker of a truncated page resumed at %v, want one item", page, next)
		}
		if ours[next[0]] {
			remove(next[0])
			markerRemovals++
		}
		if i := slices.IndexFunc(served, func(id string) bool { return ours[id] }); i >= 0 {
			remove(served[i])
		}
		name := fmt.Sprintf("a%02d", page)
		if page%2 == 1 {
			name = fmt.Sprintf("z%02d", page)
		}
		ours[l.Add(name)] = true
	})
	seen := map[string]bool{}
	for _, id := range served {
		if seen[id] {
			tb.Fatalf("%s served twice: %v", id, served)
		}
		seen[id] = true
	}
	for id := range throughout {
		if !seen[id] {
			tb.Errorf("%s, present throughout, was skipped: %v", id, served)
		}
	}
	if markerRemovals == 0 {
		tb.Errorf("no page boundary removed the item its marker named; served %v", served)
	}
}

// pageThrough follows markers from the first page until one is not
// truncated, calling between after each truncated page, and returns every
// id served.
func pageThrough(tb testing.TB, l Listing, maxItems int32, between func(page int, marker *string, served []string)) []string {
	tb.Helper()
	var served []string
	var marker *string
	for page := range maxPages {
		ids, next := l.Page(maxItems, marker)
		if len(ids) > int(maxItems) {
			tb.Fatalf("page %d served %d items, more than MaxItems %d: %v", page, len(ids), maxItems, ids)
		}
		served = append(served, ids...)
		if next == nil {
			return served
		}
		marker = next
		between(page, marker, served)
	}
	tb.Fatalf("listing did not end after %d pages; it served %d items, the last %v", maxPages, len(served), served[max(0, len(served)-5):])
	return nil
}
