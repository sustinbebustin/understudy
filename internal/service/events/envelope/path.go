package envelope

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// Path is a parsed InputPath or InputPathsMap value.
type Path struct {
	steps []step
	// definite is false when a step is a wildcard: the path then selects
	// a list.
	definite bool
}

// step is one step of a path.
//
//sumtype:decl
type step interface{ isStep() }

// member selects an object member by name.
type member struct{ name string }

// index selects an array element.
type index struct{ i int }

// wildcard selects every member of an object or element of an array.
type wildcard struct{}

func (member) isStep()   {}
func (index) isStep()    {}
func (wildcard) isStep() {}

// PathError is a path outside the supported JSONPath subset.
type PathError struct {
	Path string
	// Offset is the byte where reading stopped.
	Offset int
}

func (e *PathError) Error() string {
	return "envelope: " + strconv.Quote(e.Path) + " is not a JSONPath of the form EventBridge supports ($ then .name, [index], .* or [*]); reading stopped at byte " + strconv.Itoa(e.Offset)
}

// ParsePath parses s: "$" followed by .name, .*, [n] or [*] steps.
func ParsePath(s string) (Path, error) {
	if s == "" || s[0] != '$' {
		return Path{}, &PathError{Path: s}
	}
	p := Path{definite: true}
	for i := 1; i < len(s); {
		st, next, ok := readStep(s, i)
		if !ok {
			return Path{}, &PathError{Path: s, Offset: i}
		}
		if _, ok := st.(wildcard); ok {
			p.definite = false
		}
		p.steps = append(p.steps, st)
		i = next
	}
	return p, nil
}

// readStep reads the step at s[i:], returning it and the offset after it.
func readStep(s string, i int) (step, int, bool) {
	switch s[i] {
	case '.':
		j := i + 1
		if j < len(s) && s[j] == '*' {
			return wildcard{}, j + 1, true
		}
		for j < len(s) && nameByte(s[j]) {
			j++
		}
		if j == i+1 {
			return nil, 0, false
		}
		return member{name: s[i+1 : j]}, j, true
	case '[':
		end := i + 1
		for end < len(s) && s[end] != ']' {
			end++
		}
		if end == len(s) {
			return nil, 0, false
		}
		inner := s[i+1 : end]
		if inner == "*" {
			return wildcard{}, end + 1, true
		}
		n, err := strconv.Atoi(inner)
		if err != nil || n < 0 || inner[0] == '+' {
			return nil, 0, false
		}
		return index{i: n}, end + 1, true
	}
	return nil, 0, false
}

// nameByte reports whether b may appear in a member name: alphanumerics,
// "-", "_" and "/", the characters the documentation lists.
func nameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '/'
}

// Select returns what the path selects in v. A definite path selects one
// value, or nothing; a path with a wildcard selects the list of every value
// it reaches, which may be empty.
func (p Path) Select(v document.Value) (document.Value, bool) {
	found := walk(v, p.steps, nil)
	if !p.definite {
		return document.List(found), true
	}
	if len(found) == 0 {
		return nil, false
	}
	return found[0], true
}

// Payload returns the InputPath payload for e: the JSON of what p selects
// in the envelope, so a string comes out with its quotes ("baz"). ok is
// false when p selects nothing. An error is a selected value JSON cannot
// hold, as for Event.JSON.
func (p Path) Payload(e Event) (body []byte, ok bool, err error) {
	v, ok := p.Select(e.Document())
	if !ok {
		return nil, false, nil
	}
	body, err = encode(v)
	if err != nil {
		return nil, false, err
	}
	return body, true, nil
}

// walk appends to out every value steps reach from v.
func walk(v document.Value, steps []step, out []document.Value) []document.Value {
	if len(steps) == 0 {
		return append(out, v)
	}
	rest := steps[1:]
	switch st := steps[0].(type) {
	case member:
		if m, ok := v.(document.Map); ok {
			if e, ok := lookup(m, st.name); ok {
				return walk(e, rest, out)
			}
		}
	case index:
		if l, ok := v.(document.List); ok && st.i < len(l) {
			return walk(l[st.i], rest, out)
		}
	case wildcard:
		for _, e := range children(v) {
			out = walk(e, rest, out)
		}
	}
	return out
}

// children returns the member values of an object or the elements of an
// array, in order; a scalar has none.
func children(v document.Value) []document.Value {
	switch c := v.(type) {
	case document.Map:
		out := make([]document.Value, len(c))
		for i, e := range c {
			out[i] = e.Value
		}
		return out
	case document.List:
		return c
	case document.Null, document.Bool, document.Number, document.String:
	}
	return nil
}

// lookup returns the value of m's member name.
func lookup(m document.Map, name string) (document.Value, bool) {
	for _, e := range m {
		if e.Key == name {
			return e.Value, true
		}
	}
	return nil, false
}
