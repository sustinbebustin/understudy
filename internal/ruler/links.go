package ruler

import "strconv"

// links indexes, once per Match call, the fields at every path the pattern
// looks at and which of those paths have fields in a shared array. A branch
// then finds its connected components in work proportional to its own
// conditions, not to the arrays at its paths: a Lambda pattern can have
// 16384 branches over 28 paths, and an event thousands of arrays under them.
//
// Two paths share an array exactly when they share an outermost one: an
// array sits inside one element of each enclosing array, up to an outermost
// array, and a path's arrays include every array enclosing one of them.
// Outermost arrays that hold the same paths connect the same conditions, so
// links keeps one group per distinct set of paths, and a key repeated with a
// new array each time adds no work per branch.
type links struct {
	// id numbers the pattern's paths in the order the branches first use
	// them; paths holds their fields.
	id    map[string]int
	paths []*pathFields
	// groups lists, per path, the groups it is in. A group is a set of two or
	// more paths that have fields in one outermost array.
	groups [][]int
}

// newLinks indexes the paths of every branch, charging one step per
// condition, the indexing of the paths' fields (newPathFields) and the
// grouping of their outermost arrays (group). It stops early once the budget
// is spent, and the caller then gives up.
func newLinks(branches [][]*condition, fs *fields, work *budget) *links {
	ln := &links{id: map[string]int{}}
	for _, b := range branches {
		work.charge(len(b))
		for _, c := range b {
			if _, ok := ln.id[c.path]; ok {
				continue
			}
			ln.id[c.path] = len(ln.paths)
			ln.paths = append(ln.paths, newPathFields(fs.byPath[c.path], work))
			if work.exceeded() {
				return ln
			}
		}
	}
	ln.group(work)
	return ln
}

// group fills groups from the outermost arrays of the paths, charging one
// step per path and outermost array it holds, twice. That is at most twice
// the elements newPathFields has charged, so group needs no early stop.
func (ln *links) group(work *budget) {
	// holders lists, per outermost array, the paths with fields in it, in
	// ascending order; arrays lists the arrays in the order first seen.
	holders := map[int][]int{}
	var arrays []int
	for i, pf := range ln.paths {
		work.charge(len(pf.roots))
		for _, r := range pf.roots {
			if _, ok := holders[r]; !ok {
				arrays = append(arrays, r)
			}
			holders[r] = append(holders[r], i)
		}
	}
	ln.groups = make([][]int, len(ln.paths))
	group := map[string]struct{}{}
	var key []byte
	for _, r := range arrays {
		hs := holders[r]
		work.charge(len(hs))
		if len(hs) < 2 {
			continue
		}
		key = key[:0]
		for _, h := range hs {
			key = strconv.AppendInt(append(key, ' '), int64(h), 10)
		}
		if _, dup := group[string(key)]; dup {
			continue
		}
		g := len(group)
		group[string(key)] = struct{}{}
		for _, h := range hs {
			ln.groups[h] = append(ln.groups[h], g)
		}
	}
}

// components groups the indexes of a branch's conditions into connected
// components, charging one step per condition and per group of its path.
// Two conditions are connected when fields at their paths sit in one array;
// a branch holds one condition per path. Each component lists its indexes in
// ascending order, so a search over it keeps the sorted path order, and the
// components come in the order of their first index.
func (ln *links) components(conds []*condition, work *budget) [][]int {
	// root is a union-find forest whose roots are the smallest index of their
	// component.
	root := make([]int, len(conds))
	for i := range root {
		root[i] = i
	}
	find := func(i int) int {
		for root[i] != i {
			root[i] = root[root[i]]
			i = root[i]
		}
		return i
	}
	// first holds, per group, the first condition whose path is in it.
	first := map[int]int{}
	for i, c := range conds {
		gs := ln.groups[ln.id[c.path]]
		work.charge(1 + len(gs))
		for _, g := range gs {
			j, ok := first[g]
			if !ok {
				first[g] = i
				continue
			}
			ri, rj := find(i), find(j)
			root[max(ri, rj)] = min(ri, rj)
		}
	}
	var out [][]int
	slot := map[int]int{}
	for i := range conds {
		r := find(i)
		k, ok := slot[r]
		if !ok {
			k = len(out)
			slot[r] = k
			out = append(out, nil)
		}
		out[k] = append(out[k], i)
	}
	return out
}
