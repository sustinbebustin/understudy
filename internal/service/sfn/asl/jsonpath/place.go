package jsonpath

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// PlaceError is a ResultPath that cannot put a value into its input: a name
// step into a value that is not an object, or an index step into a value
// that is not an array holding that index. The engine words AWS's cause
// from Node and Step (stepfunctions-asl.md 4.4, ASL-03); the reason is
// understudy's.
type PlaceError struct {
	Path   string // as written
	Reason string // understudy's wording
	// Node is the value the step could not be taken into, nil when the path
	// is not a Reference Path; Step is that step's name, or its index as
	// decimal text.
	Node document.Value
	Step string
}

func (e *PlaceError) Error() string {
	return "Unable to apply ResultPath '" + e.Path + "' to the input: " + e.Reason
}

// Place returns root with value at p, a Reference Path into the input
// (stepfunctions-asl.md 4.4): an existing member or element is overwritten
// and missing intermediate objects are created. "$" gives value itself. root
// is not changed; the result shares the parts of root it does not replace.
// A step that cannot be taken is a *PlaceError; so is a path Place does not
// take, a "$$" path or one with a step beyond names and indexes, which the
// parser never gives a ResultPath.
func (p Path) Place(root, value document.Value) (document.Value, error) {
	if p.context || !p.definite() {
		return nil, &PlaceError{Path: p.src, Reason: "the path is not a Reference Path into the input"}
	}
	return p.place(root, 0, value)
}

// place puts value at steps[i:] below cur, which is the node steps[:i]
// names; nil when that node does not exist yet.
func (p Path) place(cur document.Value, i int, value document.Value) (document.Value, error) {
	if i == len(p.steps) {
		return value, nil
	}
	switch s := p.steps[i].(type) {
	case child:
		return p.placeMember(cur, i, s.name, value)
	case index:
		return p.placeElement(cur, i, s.n, value)
	case wildcard, descend, slice, union, filter, function:
	}
	return nil, &PlaceError{Path: p.src, Reason: "the path is not a Reference Path into the input"}
}

// placeMember takes name step i: cur must be an object, or missing, which
// makes it one.
func (p Path) placeMember(cur document.Value, i int, name string, value document.Value) (document.Value, error) {
	if cur == nil {
		cur = document.Map{}
	}
	m, ok := cur.(document.Map)
	if !ok {
		return nil, &PlaceError{Path: p.src, Reason: normalized(p.steps[:i]) + " is " + kind(cur) + ", not an object", Node: cur, Step: name}
	}
	old, _ := member(m, name)
	v, err := p.place(old, i+1, value)
	if err != nil {
		return nil, err
	}
	out := make(document.Map, 0, len(m)+1)
	replaced := false
	for _, e := range m {
		if e.Key == name && !replaced {
			e.Value, replaced = v, true
		}
		out = append(out, e)
	}
	if !replaced {
		out = append(out, document.Entry{Key: name, Value: v})
	}
	return out, nil
}

// placeElement takes index step i: cur must be an array holding index n.
// No recording shows a ResultPath growing an array, so it never does.
func (p Path) placeElement(cur document.Value, i, n int, value document.Value) (document.Value, error) {
	at := normalized(p.steps[:i])
	l, ok := cur.(document.List)
	if !ok {
		return nil, &PlaceError{Path: p.src, Reason: at + " is " + kind(cur) + ", not an array", Node: cur, Step: strconv.Itoa(n)}
	}
	pos, ok := position(n, len(l))
	if !ok {
		return nil, &PlaceError{Path: p.src, Reason: at + " has no element " + strconv.Itoa(n), Node: cur, Step: strconv.Itoa(n)}
	}
	v, err := p.place(l[pos], i+1, value)
	if err != nil {
		return nil, err
	}
	out := append(document.List(nil), l...)
	out[pos] = v
	return out, nil
}

// kind names v's JSON type for a PlaceError.
func kind(v document.Value) string {
	switch v.(type) {
	case document.Map:
		return "an object"
	case document.List:
		return "an array"
	case document.String:
		return "a string"
	case document.Number:
		return "a number"
	case document.Bool:
		return "a boolean"
	case document.Null:
		return "null"
	}
	return "missing"
}
