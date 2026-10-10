package ruler

// element is one container element of an event array: an object or array
// that sits in an array. The chain from a field's innermost element up
// through its parents is the field's array membership (matching-engines.md
// 4.3). Every field inside one element shares it, so a membership costs one
// pointer per field however deep the field sits. Ruler records a membership
// only for container elements; a scalar array element has none of its own.
//
// Two elements of one array conflict: no match may take fields from both.
type element struct {
	parent *element
	// jump is a skew-binary jump pointer to an ancestor (Myers, "An
	// applicative random-access stack", 1983). It makes ancestorAt and split
	// take O(log depth) steps. The depth it jumps to depends on the depth
	// alone, so two elements at one depth jump to one depth.
	jump *element
	// depth counts the elements on the chain, this one included: the number
	// of arrays the element's fields sit in.
	depth int
	// array identifies the array the element belongs to.
	array int
	// id numbers the elements of an event in document order.
	id int
	// first and end bound the seq of the fields inside the element.
	first, end int
}

func newElement(parent *element, array, id, first int) *element {
	e := &element{parent: parent, jump: parent, depth: depthOf(parent) + 1, array: array, id: id, first: first}
	if parent != nil && parent.jump != nil &&
		parent.depth-parent.jump.depth == parent.jump.depth-depthOf(parent.jump.jump) {
		e.jump = parent.jump.jump
	}
	return e
}

// depthOf is e's depth; nil, outside every array element, has depth 0.
func depthOf(e *element) int {
	if e == nil {
		return 0
	}
	return e.depth
}

// ancestorAt returns the element at depth d on e's chain, nil for d == 0.
// d must not exceed e's depth.
func ancestorAt(e *element, d int) *element {
	for depthOf(e) > d {
		if depthOf(e.jump) >= d {
			e = e.jump
		} else {
			e = e.parent
		}
	}
	return e
}

// split compares the memberships of two fields' innermost elements: shared
// is the number of arrays both sit in, and conflict reports whether they sit
// in different elements of the deepest of those arrays. Elements of a shared
// array above that one are then the same, because each element sits in
// exactly one element of every enclosing array.
func split(a, b *element) (shared int, conflict bool) {
	m := min(depthOf(a), depthOf(b))
	a, b = ancestorAt(a, m), ancestorAt(b, m)
	if a == b {
		return m, false
	}
	// Climb to the children of the lowest common ancestor. Jumps from one
	// depth land on one depth, so different jump targets mean the common
	// ancestor lies above them.
	for a.parent != b.parent {
		if a.jump != b.jump {
			a, b = a.jump, b.jump
		} else {
			a, b = a.parent, b.parent
		}
	}
	if a.array == b.array {
		return a.depth, true
	}
	return a.depth - 1, false
}

// compatible reports whether one match may take fields from both elements.
func compatible(a, b *element) bool {
	_, conflict := split(a, b)
	return !conflict
}
