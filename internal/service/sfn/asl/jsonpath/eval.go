package jsonpath

import (
	"math/big"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// definite reports whether p names at most one node: names and indexes
// only (stepfunctions-asl.md 4.5).
func (p Path) definite() bool {
	for _, s := range p.steps {
		switch s.(type) {
		case child, index:
		case wildcard, descend, slice, union, filter:
			return false
		}
	}
	return true
}

// evalDefinite walks p's names and indexes. The texts follow the recorded
// ones (stepfunctions-asl.md 4.6): a member missing from an object before
// the last step is "Missing property in path" up to it; any other miss is
// "No results for path" with the whole path. Only the object-member cases
// are recorded; an index out of range or a step into a value of the wrong
// type uses the second form by understudy's choice (ASL-19).
func (p Path) evalDefinite(root document.Value) (document.Value, error) {
	v := root
	for i, s := range p.steps {
		if c, isChild := s.(child); isChild && i < len(p.steps)-1 {
			if m, isMap := v.(document.Map); isMap {
				if _, found := member(m, c.name); !found {
					return nil, &MissingError{Path: p.src, Reason: "Missing property in path " + normalized(p.steps[:i+1]), Node: v, Step: c.name}
				}
			}
		}
		got := s.selectFrom(v, nil)
		if len(got) != 1 {
			e := &MissingError{Path: p.src, Reason: "No results for path: " + normalized(p.steps)}
			switch s := s.(type) {
			case child:
				e.Node, e.Step = v, s.name
			case index:
				e.Node, e.Step = v, strconv.Itoa(s.n)
			case wildcard, descend, slice, union, filter:
			}
			return nil, e
		}
		v = got[0]
	}
	return v, nil
}

// normalized writes steps as the recorded texts do: $['a'][0]
// (stepfunctions-asl.md 4.6).
func normalized(steps []step) string {
	var b strings.Builder
	b.WriteString("$")
	for _, s := range steps {
		if c, ok := s.(child); ok {
			b.WriteString("['" + c.name + "']")
			continue
		}
		s.write(&b)
	}
	return b.String()
}

// selectAll applies steps to every node of nodes, gathering the matches in
// document order (stepfunctions-asl.md 4.5: "a multi-value result is
// gathered into an array").
func selectAll(nodes []document.Value, steps []step) []document.Value {
	for _, s := range steps {
		var next []document.Value
		for _, v := range nodes {
			next = s.selectFrom(v, next)
		}
		nodes = next
	}
	return nodes
}

// A step that does not fit the value it meets selects nothing.

func (s child) selectFrom(v document.Value, out []document.Value) []document.Value {
	if m, ok := v.(document.Map); ok {
		if x, found := member(m, s.name); found {
			out = append(out, x)
		}
	}
	return out
}

func (s index) selectFrom(v document.Value, out []document.Value) []document.Value {
	if l, ok := v.(document.List); ok {
		if n, ok := position(s.n, len(l)); ok {
			out = append(out, l[n])
		}
	}
	return out
}

func (wildcard) selectFrom(v document.Value, out []document.Value) []document.Value {
	return append(out, children(v)...)
}

// selectFrom applies the target to v, then to every descendant in
// document order.
func (s descend) selectFrom(v document.Value, out []document.Value) []document.Value {
	out = s.target.selectFrom(v, out)
	for _, c := range children(v) {
		out = s.selectFrom(c, out)
	}
	return out
}

func (s slice) selectFrom(v document.Value, out []document.Value) []document.Value {
	if l, ok := v.(document.List); ok {
		for _, n := range s.positions(len(l)) {
			out = append(out, l[n])
		}
	}
	return out
}

func (s union) selectFrom(v document.Value, out []document.Value) []document.Value {
	if l, ok := v.(document.List); ok {
		for _, i := range s.indexes {
			if n, ok := position(i, len(l)); ok {
				out = append(out, l[n])
			}
		}
	}
	return out
}

// selectFrom keeps the elements of a list that pass the filter. An object
// is tested as itself, as Jayway applies a filter to one (4.5: the recorded
// texts are Jayway's; an inference).
func (s filter) selectFrom(v document.Value, out []document.Value) []document.Value {
	switch x := v.(type) {
	case document.List:
		for _, e := range x {
			if s.holds(e) {
				out = append(out, e)
			}
		}
	case document.Map:
		if s.holds(x) {
			out = append(out, x)
		}
	case document.Null, document.Bool, document.Number, document.String:
	}
	return out
}

// children lists an object's member values or an array's elements in
// order; a scalar has none.
func children(v document.Value) []document.Value {
	switch x := v.(type) {
	case document.Map:
		out := make([]document.Value, len(x))
		for i, e := range x {
			out[i] = e.Value
		}
		return out
	case document.List:
		return x
	case document.Null, document.Bool, document.Number, document.String:
	}
	return nil
}

// member returns m's value for name, the first one when keys repeat.
func member(m document.Map, name string) (document.Value, bool) {
	for _, e := range m {
		if e.Key == name {
			return e.Value, true
		}
	}
	return nil, false
}

// position resolves index i of a list of n elements, negative counting
// from the end.
func position(i, n int) (int, bool) {
	if i < 0 {
		i += n
	}
	return i, i >= 0 && i < n
}

// positions lists the indexes s selects from a list of n elements, with
// Python's slice rules: bounds clamp, negatives count from the end, a
// negative step walks backwards.
func (s slice) positions(n int) []int {
	step := 1
	if s.step.set {
		step = s.step.n
	}
	resolve := func(b bound, absent, lo, hi int) int {
		if !b.set {
			return absent
		}
		i := b.n
		if i < 0 {
			i += n
		}
		return min(max(i, lo), hi)
	}
	var out []int
	if step > 0 {
		for i, end := resolve(s.start, 0, 0, n), resolve(s.end, n, 0, n); i < end; i += step {
			out = append(out, i)
		}
		return out
	}
	for i, end := resolve(s.start, n-1, -1, n-1), resolve(s.end, -1, -1, n-1); i > end; i += step {
		out = append(out, i)
	}
	return out
}

// holds reports whether the filter's comparison is true of v. A missing
// member never matches; values of different JSON types are unequal, and
// only numbers and strings are ordered (understudy's reading of the one
// recorded filter, ASL-19).
func (s filter) holds(v document.Value) bool {
	got := selectAll([]document.Value{v}, s.ref)
	if len(got) != 1 {
		return false
	}
	c, same := compare(got[0], s.literal)
	switch s.op {
	case "==":
		return same && c == 0
	case "!=":
		return !same || c != 0
	}
	if !same || !ordered(got[0]) {
		return false
	}
	switch s.op {
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	}
	return false
}

// ordered reports whether values of v's type compare by < and >.
func ordered(v document.Value) bool {
	switch v.(type) {
	case document.Number, document.String:
		return true
	case document.Null, document.Bool, document.List, document.Map:
	}
	return false
}

// compare orders a and b when they are the same scalar type: numbers by
// value, strings by code point, booleans and nulls by equality only. The
// second result is false when they cannot be compared.
func compare(a, b document.Value) (int, bool) {
	switch a := a.(type) {
	case document.Number:
		b, ok := b.(document.Number)
		if !ok {
			return 0, false
		}
		x, okx := new(big.Rat).SetString(string(a))
		y, oky := new(big.Rat).SetString(string(b))
		if !okx || !oky {
			return 0, false
		}
		return x.Cmp(y), true
	case document.String:
		b, ok := b.(document.String)
		if !ok {
			return 0, false
		}
		return strings.Compare(string(a), string(b)), true
	case document.Bool:
		b, ok := b.(document.Bool)
		if !ok {
			return 0, false
		}
		if a == b {
			return 0, true
		}
		return 1, true
	case document.Null:
		_, ok := b.(document.Null)
		return 0, ok
	case document.List, document.Map:
	}
	return 0, false
}
