package kms_test

import (
	"encoding/base64"
	"fmt"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/paging/pagingtest"
)

// A NextMarker is the unpadded base64url of "v1|<listing>|<next key>", the
// format earlier builds issued, so a marker a client holds across an
// upgrade still resumes its listing.
func TestMarkerFormatIsStable(t *testing.T) {
	run(t, func(h *harness) {
		ids := []string{h.key(), h.key()}
		slices.Sort(ids)
		out := must(h, kmsapi.OpListKeys, &kmsapi.ListKeysInput{Limit: new(int32(1))})
		want := base64.RawURLEncoding.EncodeToString([]byte("v1|keys|" + ids[1]))
		if out.NextMarker == nil || *out.NextMarker != want {
			t.Fatalf("NextMarker = %v, want %q", out.NextMarker, want)
		}
	})
}

// pagedListing is one KMS listing, driven as a client drives it.
type pagedListing struct {
	op string
	of func(h *harness) pagingtest.Listing
}

// pagedListings are the three KMS listings. The alias and tag listings
// work on one key.
func pagedListings() []pagedListing {
	return []pagedListing{
		{"ListKeys", func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				// Keys list by id, so where a new one sorts is chance.
				Add: func(string) string { return h.key() },
				// A key leaves ListKeys only once its deletion window ends.
				Remove: func(id string) {
					must(h, kmsapi.OpScheduleKeyDeletion, &kmsapi.ScheduleKeyDeletionInput{KeyId: id, PendingWindowInDays: new(int32(7))})
					h.advance(7 * day)
				},
				Page: func(limit int32, marker *string) ([]string, *string) {
					out := must(h, kmsapi.OpListKeys, &kmsapi.ListKeysInput{Limit: &limit, Marker: marker})
					ids := make([]string, len(out.Keys))
					for i, k := range out.Keys {
						ids[i] = deref(k.KeyId)
					}
					return ids, out.NextMarker
				},
			}
		}},
		{"ListAliases", func(h *harness) pagingtest.Listing {
			id := h.key()
			return pagingtest.Listing{
				Add: func(name string) string {
					alias := "alias/" + name
					must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: alias, TargetKeyId: id})
					return alias
				},
				Remove: func(alias string) {
					must(h, kmsapi.OpDeleteAlias, &kmsapi.DeleteAliasInput{AliasName: alias})
				},
				// The predefined alias/aws/ aliases are listed too, present
				// throughout.
				Page: func(limit int32, marker *string) ([]string, *string) {
					out := must(h, kmsapi.OpListAliases, &kmsapi.ListAliasesInput{Limit: &limit, Marker: marker})
					names := make([]string, len(out.Aliases))
					for i, a := range out.Aliases {
						names[i] = deref(a.AliasName)
					}
					return names, out.NextMarker
				},
			}
		}},
		{"ListResourceTags", func(h *harness) pagingtest.Listing {
			id := h.key()
			return pagingtest.Listing{
				Add: func(name string) string {
					must(h, kmsapi.OpTagResource, &kmsapi.TagResourceInput{KeyId: id, Tags: []kmsapi.Tag{{TagKey: name, TagValue: "v"}}})
					return name
				},
				Remove: func(key string) {
					must(h, kmsapi.OpUntagResource, &kmsapi.UntagResourceInput{KeyId: id, TagKeys: []string{key}})
				},
				Page: func(limit int32, marker *string) ([]string, *string) {
					out := must(h, kmsapi.OpListResourceTags, &kmsapi.ListResourceTagsInput{KeyId: id, Limit: &limit, Marker: marker})
					keys := make([]string, len(out.Tags))
					for i, t := range out.Tags {
						keys[i] = t.TagKey
					}
					return keys, out.NextMarker
				},
			}
		}},
	}
}

// Every KMS listing, paged one to three items at a time while the item
// each marker names, an item already served and a new item come and go
// between pages, serves each item once and skips none present throughout.
func TestListingsSurviveChangesBetweenPages(t *testing.T) {
	for _, l := range pagedListings() {
		for limit := int32(1); limit <= 3; limit++ {
			t.Run(fmt.Sprintf("%s/Limit=%d", l.op, limit), func(t *testing.T) {
				run(t, func(h *harness) {
					pagingtest.SurvivesChanges(h, l.of(h), 8, limit)
				})
			})
		}
	}
}
