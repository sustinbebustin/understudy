package sqs

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/clock"
)

// dedupGenerationSize is how many keys a DedupCache generation takes
// before a newer one starts, which weighs what adding a key copies against
// how many maps a lookup reads.
const dedupGenerationSize = 128

// DedupCache is a FIFO queue's deduplication cache (sqs.md 8.2): each
// accepted deduplication key and the message it enqueued, until its window
// ends. It is a value like the rest of the state, laid out so a working
// copy adds a key without copying the whole cache, which would make
// filling a FIFO queue quadratic. Keys live in generations, oldest first;
// only the newest takes new keys, and a generation is never changed once a
// newer one starts. Adding a key copies the list of generations and the
// newest one, at most dedupGenerationSize keys. A lookup ignores ended
// entries, and a generation goes once all of its entries have ended. It
// persists as one JSON object from key to Dedup.
type DedupCache struct {
	gens []dedupGeneration
	// borrowed reports that a working copy (Queue.clone) still shares gens
	// and the newest generation's map with the stored record.
	borrowed bool
}

type dedupGeneration struct {
	entries map[string]Dedup
	// ends is when the last window of entries ends.
	ends clock.Instant
}

// IsZero reports a cache without keys, which the state leaves out.
func (c *DedupCache) IsZero() bool { return len(c.gens) == 0 }

// MarshalJSONTo writes c as one object from key to entry; a key in two
// generations is written with its newer entry.
func (c *DedupCache) MarshalJSONTo(enc *jsontext.Encoder) error {
	flat := map[string]Dedup{}
	for _, g := range c.gens {
		maps.Copy(flat, g.entries)
	}
	return json.MarshalEncode(enc, flat)
}

// UnmarshalJSONFrom reads what MarshalJSONTo writes, as one generation.
func (c *DedupCache) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var flat map[string]Dedup
	if err := json.UnmarshalDecode(dec, &flat); err != nil {
		return err
	}
	*c = DedupCache{}
	if len(flat) == 0 {
		return nil
	}
	g := dedupGeneration{entries: flat}
	for _, d := range flat {
		if g.ends.Before(d.Expires) {
			g.ends = d.Expires
		}
	}
	c.gens = []dedupGeneration{g}
	return nil
}

// lend marks c as a working copy's, sharing its generations with the
// record it was copied from.
func (c *DedupCache) lend() { c.borrowed = true }

// get returns the entry of key whose window is still open at now.
func (c *DedupCache) get(key string, now clock.Instant) (Dedup, bool) {
	for _, v := range slices.Backward(c.gens) {
		if d, ok := v.entries[key]; ok {
			return d, now.Before(d.Expires)
		}
	}
	return Dedup{}, false
}

// put adds key, whose lookups find d from then on.
func (c *DedupCache) put(key string, d Dedup) {
	n := len(c.gens)
	full := n == 0 || len(c.gens[n-1].entries) >= dedupGenerationSize
	if c.borrowed {
		c.gens = slices.Clone(c.gens)
		if !full {
			c.gens[n-1].entries = maps.Clone(c.gens[n-1].entries)
		}
		c.borrowed = false
	}
	if full {
		c.gens = append(c.gens, dedupGeneration{entries: map[string]Dedup{}})
	}
	g := &c.gens[len(c.gens)-1]
	g.entries[key] = d
	if g.ends.Before(d.Expires) {
		g.ends = d.Expires
	}
}

// prune drops the generations whose entries have all ended at now and
// reports whether it dropped any.
func (c *DedupCache) prune(now clock.Instant) bool {
	ended := func(g dedupGeneration) bool { return !now.Before(g.ends) }
	if !slices.ContainsFunc(c.gens, ended) {
		return false
	}
	c.gens = slices.DeleteFunc(slices.Clone(c.gens), ended)
	// The newest generation left may be one the stored record shares.
	c.borrowed = true
	return true
}
