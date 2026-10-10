package form

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// dump renders the tree under n in a canonical order: n's own value, then
// its non-index children in segment order, then its indexed children in
// the order Indexed returns them. Two trees dump alike exactly when they
// hold the same keys and values and Indexed orders them alike.
func dump(b *strings.Builder, n *Node) {
	if v, ok := n.Value(); ok {
		b.WriteString(n.Key() + "=" + strconv.Quote(v) + "\n")
	}
	var named []string
	for seg := range n.children {
		if !isIndex(seg) {
			named = append(named, seg)
		}
	}
	slices.Sort(named)
	for _, seg := range named {
		dump(b, n.children[seg])
	}
	for _, c := range n.Indexed() {
		dump(b, c)
	}
}

func dumpOf(t *rapid.T, s string) string {
	v, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	var b strings.Builder
	dump(&b, v.Root())
	return b.String()
}

// item, entry and value are a drawn list of maps of lists, the shape of
// the keys an SDK sends for one.
type (
	item struct {
		index   int
		id      string
		entries []entry
	}
	entry struct {
		index  int
		values []value
	}
	value struct {
		index int
		text  string
	}
)

// drawItems draws distinct indexes up to 12 at every level, in random
// order, so two-digit indexes fall among one-digit ones.
func drawItems(t *rapid.T) []item {
	indexes := rapid.SliceOfNDistinct(rapid.IntRange(1, 12), 0, 6, rapid.ID[int])
	drawn := indexes.Draw(t, "items")
	items := make([]item, 0, len(drawn))
	for _, i := range drawn {
		it := item{index: i, id: rapid.String().Draw(t, "id")}
		for _, j := range indexes.Draw(t, "entries") {
			e := entry{index: j}
			for _, k := range indexes.Draw(t, "values") {
				e.values = append(e.values, value{index: k, text: rapid.String().Draw(t, "value")})
			}
			it.entries = append(it.entries, e)
		}
		items = append(items, it)
	}
	return items
}

func itemKey(i int) string { return "Items.member." + strconv.Itoa(i) }

func entryKey(i, j int) string { return itemKey(i) + ".Tags.entry." + strconv.Itoa(j) }

// pairsOf writes items as form pairs in their drawn order.
func pairsOf(items []item) []Pair {
	var pairs []Pair
	for _, it := range items {
		pairs = append(pairs, Pair{Key: itemKey(it.index) + ".Id", Value: it.id})
		for _, e := range it.entries {
			pairs = append(pairs, Pair{Key: entryKey(it.index, e.index) + ".key", Value: "k" + strconv.Itoa(e.index)})
			for _, v := range e.values {
				pairs = append(pairs, Pair{Key: entryKey(it.index, e.index) + ".value.member." + strconv.Itoa(v.index), Value: v.text})
			}
		}
	}
	return pairs
}

// expectedDump is dump's rendering of items worked out from the drawing,
// every level in numeric index order: Id before Tags, key before value.
func expectedDump(items []item) string {
	byIndex := cmp.Compare[int]
	var b strings.Builder
	line := func(key, v string) { b.WriteString(key + "=" + strconv.Quote(v) + "\n") }
	items = slices.Clone(items)
	slices.SortFunc(items, func(x, y item) int { return byIndex(x.index, y.index) })
	for _, it := range items {
		line(itemKey(it.index)+".Id", it.id)
		entries := slices.Clone(it.entries)
		slices.SortFunc(entries, func(x, y entry) int { return byIndex(x.index, y.index) })
		for _, e := range entries {
			line(entryKey(it.index, e.index)+".key", "k"+strconv.Itoa(e.index))
			values := slices.Clone(e.values)
			slices.SortFunc(values, func(x, y value) int { return byIndex(x.index, y.index) })
			for _, v := range values {
				line(entryKey(it.index, e.index)+".value.member."+strconv.Itoa(v.index), v.text)
			}
		}
	}
	return b.String()
}

// wire-json-query.md Implication 10: a form decodes to the same tree in
// any pair order, Go's string-sorted indexes included, and that tree
// holds the items in numeric index order.
func TestPropertyIndexPermutationsDecodeAlike(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		items := drawItems(t)
		shuffled := rapid.Permutation(pairsOf(items)).Draw(t, "shuffled")
		if got, want := dumpOf(t, Encode(shuffled)), expectedDump(items); got != want {
			t.Fatalf("shuffled form decodes to\n%s\nwant\n%s", got, want)
		}
	})
}

// Escaped text decodes to itself, with spaces sent as %20 or as '+'.
func TestPropertyEscapeRoundTrips(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		key := rapid.StringMatching(`[A-Za-z0-9]{1,8}`).Draw(t, "key")
		value := rapid.String().Draw(t, "value")
		text := key + "=" + Escape(value)
		if rapid.Bool().Draw(t, "plus") {
			text = strings.ReplaceAll(text, "%20", "+")
		}
		v, err := Parse(text)
		if err != nil {
			t.Fatalf("Parse(%q): %v", text, err)
		}
		if got, _ := v.Get(key); got != value {
			t.Fatalf("value = %q, want %q", got, value)
		}
	})
}

// Parse returns a tree or a ParseError for any bytes, never a panic, and
// the error names, by its raw key alone, a pair that starts at its offset.
func TestPropertyParseNeverPanics(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		text := rapid.OneOf(
			rapid.StringOf(rapid.SampledFrom([]rune("a.1=&%+2F"))),
			rapid.String(),
			rapid.Map(rapid.SliceOf(rapid.Byte()), func(b []byte) string { return string(b) }),
		).Draw(t, "text")
		v, err := Parse(text)
		if err != nil {
			if err.Offset > len(text) || strings.Contains(err.RawKey, "=") || !strings.HasPrefix(text[err.Offset:], strings.TrimSuffix(err.RawKey, "...")) {
				t.Fatalf("Parse(%q) error %+v does not name the pair at its offset", text, err)
			}
			return
		}
		var b strings.Builder
		dump(&b, v.Root())
	})
}
