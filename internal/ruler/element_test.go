package ruler

import (
	"testing"

	"pgregory.net/rapid"
)

// elementTree draws array elements the way events nest them: each new
// element joins an array under a parent element (or under no element), either
// a new array or one that parent already holds. Parents are mostly recent
// elements, so chains run deep enough to exercise the jump pointers.
func elementTree(t *rapid.T) []*element {
	n := rapid.IntRange(1, 300).Draw(t, "elements")
	elems := make([]*element, 0, n)
	arraysUnder := map[*element][]int{}
	arrays := 0
	for id := range n {
		var parent *element
		if len(elems) > 0 && rapid.IntRange(0, 9).Draw(t, "nest") > 0 {
			back := rapid.IntRange(0, min(len(elems)-1, 3)).Draw(t, "back")
			parent = elems[len(elems)-1-back]
		}
		var array int
		if held := arraysUnder[parent]; len(held) > 0 && rapid.Bool().Draw(t, "same array") {
			array = rapid.SampledFrom(held).Draw(t, "array")
		} else {
			array = arrays
			arrays++
			arraysUnder[parent] = append(arraysUnder[parent], array)
		}
		elems = append(elems, newElement(parent, array, id, 0))
	}
	return elems
}

// chain lists e and its ancestors, deepest first.
func chain(e *element) []*element {
	var out []*element
	for ; e != nil; e = e.parent {
		out = append(out, e)
	}
	return out
}

// naiveSplit counts the arrays both chains hold, and reports whether some
// such array holds them in different elements.
func naiveSplit(a, b *element) (shared int, conflict bool) {
	in := map[int]*element{}
	for _, e := range chain(b) {
		in[e.array] = e
	}
	for _, e := range chain(a) {
		if other, ok := in[e.array]; ok {
			shared++
			conflict = conflict || other != e
		}
	}
	return shared, conflict
}

// The jump-pointer queries agree with plain walks up the parent chains.
func TestPropertyElementQueries(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		elems := elementTree(t)
		pick := func(label string) *element {
			if rapid.IntRange(0, 9).Draw(t, label+" none") == 0 {
				return nil
			}
			return rapid.SampledFrom(elems).Draw(t, label)
		}
		a, b := pick("a"), pick("b")
		d := rapid.IntRange(0, depthOf(a)).Draw(t, "depth")
		ancestors := chain(a)
		want := (*element)(nil)
		if d > 0 {
			want = ancestors[len(ancestors)-d]
		}
		if got := ancestorAt(a, d); got != want {
			t.Fatalf("ancestorAt(depth %d, %d) = %p, want %p", depthOf(a), d, got, want)
		}
		shared, conflict := split(a, b)
		wantShared, wantConflict := naiveSplit(a, b)
		if shared != wantShared || conflict != wantConflict {
			t.Fatalf("split = %d, %t; want %d, %t", shared, conflict, wantShared, wantConflict)
		}
	})
}
