package ses

import (
	"slices"
	"unicode/utf16"
)

// hashSetOrder is ids, each once, in the iteration order of a
// java.util.HashSet<String> they were added to in order: SES lists the
// identities of a failed check in this order (ses.md 6.1; the rule is
// inferred from SES-03, whose two recorded lists it reproduces). A HashSet
// starts with 16 buckets and doubles them whenever it holds more than three
// quarters as many entries; it iterates bucket by bucket, each bucket in
// insertion order, and a doubling keeps that order within the buckets it
// splits. Java converts a bucket of eight or more entries to a tree, which
// orders it differently, only from 64 buckets up (doubling instead below):
// at most 50 recipients and three senders never get there, so it is left
// out.
func hashSetOrder(ids []string) []string {
	var set []string
	for _, id := range ids {
		if !slices.Contains(set, id) {
			set = append(set, id)
		}
	}
	buckets := 16
	for len(set) > buckets*3/4 {
		buckets *= 2
	}
	bucket := func(id string) int { return int(spread(javaStringHash(id))) & (buckets - 1) }
	slices.SortStableFunc(set, func(a, b string) int { return bucket(a) - bucket(b) })
	return set
}

// javaStringHash is java.lang.String.hashCode: s[0]*31^(n-1) + ... +
// s[n-1] over the UTF-16 code units of s, wrapping at 32 bits.
func javaStringHash(s string) uint32 {
	var h uint32
	for _, u := range utf16.Encode([]rune(s)) {
		h = 31*h + uint32(u)
	}
	return h
}

// spread is java.util.HashMap.hash: the high 16 bits of a hash folded into
// the low ones, which pick the bucket.
func spread(h uint32) uint32 { return h ^ h>>16 }
