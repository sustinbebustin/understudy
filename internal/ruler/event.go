package ruler

import "slices"

// field is one flattened event value. elem is the innermost array element
// the value sits in through an object or nested array, nil when there is
// none; Ruler uses the memberships to keep matches array-consistent. seq is
// the field's position in document order.
type field struct {
	value value
	elem  *element
	seq   int
}

// fields is a flattened event: values by dotted path.
type fields struct {
	byPath map[string][]field
	count  int
	// empty is set when the input has no top-level member: the event {} or
	// no message attributes. It is not the same as having no fields:
	// {"x":[]} has a member but no field.
	empty bool
}

func newFields() *fields {
	return &fields{byPath: map[string][]field{}}
}

func (fs *fields) add(path string, v value, elem *element) {
	at := fs.byPath[path]
	if len(at) == cap(at) {
		// Double, where append grows a large slice by a quarter: a path with
		// many fields then costs twice its size in allocations, not five times.
		at = slices.Grow(at, max(len(at), 4))
	}
	fs.byPath[path] = append(at, field{value: v, elem: elem, seq: fs.count})
	fs.count++
}

// flattener turns JSON into fields the way Ruler's Event does: object keys
// join with ".", every scalar is a field, an empty array or object yields
// nothing, and each array element is its own field.
type flattener struct {
	lx       *lexer
	out      *fields
	arrays   int
	elements int
	// current is the innermost array element being read, nil outside every
	// array element.
	current *element
}

func newFlattener(src []byte) *flattener {
	return &flattener{lx: newLexer(src), out: newFields()}
}

// flattenEvent parses an event, which must be a single JSON object: content
// after its closing brace makes the event malformed, as Compile treats a
// pattern.
func flattenEvent(src []byte) (*fields, *EventError) {
	f := newFlattener(src)
	f.lx.source = sourceRedacted
	t, err := f.lx.next()
	if err != nil {
		return nil, &EventError{Detail: err.message}
	}
	if t.kind != tokStartObject {
		return nil, &EventError{Detail: "Event must be a JSON object"}
	}
	members, err := f.object("")
	if err != nil {
		return nil, &EventError{Detail: err.message}
	}
	if t, err := f.lx.next(); err != nil || t.kind != tokEOF {
		return nil, &EventError{Detail: "Event must be a single JSON object; found content after it"}
	}
	f.out.empty = members == 0
	return f.out, nil
}

// object reads the members of an object whose '{' was consumed and returns
// how many it had. prefix is what each member's path starts with: "" for the
// event itself, else the object's path and the "." separator. A nested
// object always adds the separator, even when its path is empty, as Ruler's
// Path.name() joins every step: {"":{"a":1}} has the field ".a".
func (f *flattener) object(prefix string) (int, *syntaxError) {
	for members := 0; ; members++ {
		t, err := f.lx.next()
		if err != nil {
			return members, err
		}
		if t.kind == tokEndObject {
			return members, nil
		}
		path := prefix + t.text
		v, err := f.lx.next()
		if err != nil {
			return members, err
		}
		if err := f.value(path, v); err != nil {
			return members, err
		}
	}
}

// array reads the elements of an array whose '[' was consumed.
func (f *flattener) array(path string) *syntaxError {
	id := f.arrays
	f.arrays++
	for {
		t, err := f.lx.next()
		if err != nil {
			return err
		}
		if t.kind == tokEndArray {
			return nil
		}
		// Only container elements carry this array's membership; Ruler adds a
		// scalar element without it.
		if t.kind != tokStartObject && t.kind != tokStartArray {
			f.out.add(path, newValue(t), f.current)
			continue
		}
		parent := f.current
		f.current = newElement(parent, id, f.elements, f.out.count)
		f.elements++
		err = f.value(path, t)
		f.current.end = f.out.count
		f.current = parent
		if err != nil {
			return err
		}
	}
}

func (f *flattener) value(path string, t token) *syntaxError {
	switch t.kind {
	case tokStartObject:
		_, err := f.object(path + ".")
		return err
	case tokStartArray:
		return f.array(path)
	case tokString, tokNumber, tokTrue, tokFalse, tokNull:
		f.out.add(path, newValue(t), f.current)
	case tokEOF, tokEndObject, tokEndArray, tokFieldName:
	}
	return nil
}
