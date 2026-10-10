package restxml

import (
	"bytes"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/restbind"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/protocol/xmlw"
)

// decodeError is a well-formed body that does not fit the input shape: a
// value that does not parse as its member's type, or a union with no or
// several members set.
type decodeError struct{ Message string }

func (e *decodeError) Error() string {
	return "restxml: the request body does not match the input shape: " + e.Message
}

// usageError is generated code asking for something the open element does
// not hold: a bug.
type usageError struct{ reason string }

func (e *usageError) Error() string { return "restxml: deserializer misuse: " + e.reason }

// value is a positioned value: an element, or an attribute's text.
type value struct {
	node *xmlw.Node
	attr string
	// isAttr marks an attribute value.
	isAttr bool
}

func (v value) text() string {
	if v.isAttr {
		return v.attr
	}
	return v.node.Text
}

// field is one member present in a structure element. A flattened list or
// map gathers every element of its name.
type field struct {
	m    *smithy.Member
	v    value
	flat []*xmlw.Node
}

type frameKind uint8

const (
	structFrame frameKind = iota + 1
	unionFrame
	listFrame
	mapFrame
)

type readFrame struct {
	kind   frameKind
	fields []field
	items  []value
	// entries are a map's entry elements; keyName and valueName name their
	// children.
	entries            []*xmlw.Node
	keyName, valueName string
	next               int
}

// xmlDoc reads one restXml body document from the tree xmlw.Decode builds:
// elements matched on local names, so bodies with and without the S3
// namespace read the same (wire-rest.md 2.3); a member under its xmlName;
// flattened lists and maps gathered from repeated elements; attributes for
// xmlAttribute members; unknown elements skipped.
type xmlDoc struct {
	root *xmlw.Node
	// payload is the payload member the body holds, nil for the input's
	// unbound members; it names the root.
	payload *smithy.Member
	err     error
	open    []readFrame
	cur     value
	curFlat []*xmlw.Node
	entered bool
}

func newDocument(body []byte, payload *smithy.Member) restbind.Document {
	root, err := xmlw.Decode(bytes.NewReader(body), int64(len(body)))
	return &xmlDoc{root: root, payload: payload, err: err}
}

func (d *xmlDoc) ReadStruct(s *smithy.Schema) error {
	node, root, err := d.element("ReadStruct")
	if err != nil {
		return err
	}
	if root {
		if err := d.checkRoot(s); err != nil {
			return err
		}
	}
	d.open = append(d.open, readFrame{kind: structFrame, fields: fields(s, node, root)})
	return nil
}

// fields lists the members node holds, attributes first, then elements in
// document order with flattened collections gathered at their first
// element. At the root, members bound to HTTP are skipped: restbind reads
// them from the head.
func fields(s *smithy.Schema, node *xmlw.Node, root bool) []field {
	var out []field
	for _, m := range s.Members {
		if !m.XMLAttribute {
			continue
		}
		if v, ok := node.Attr(localName(elementName(m))); ok {
			out = append(out, field{m: m, v: value{attr: v, isAttr: true}})
		}
	}
	flat := map[*smithy.Member]int{}
	for _, c := range node.Children {
		m := memberFor(s, c.Name)
		if m == nil || root && m.HTTP != nil {
			continue
		}
		if !flattened(m) {
			out = append(out, field{m: m, v: value{node: c}})
			continue
		}
		if i, ok := flat[m]; ok {
			out[i].flat = append(out[i].flat, c)
			continue
		}
		flat[m] = len(out)
		out = append(out, field{m: m, flat: []*xmlw.Node{c}})
	}
	return out
}

func flattened(m *smithy.Member) bool {
	return m.XMLFlattened && (m.Target.Type == smithy.TypeList || m.Target.Type == smithy.TypeMap)
}

// memberFor is the element member of s named name.
func memberFor(s *smithy.Schema, name string) *smithy.Member {
	for _, m := range s.Members {
		if !m.XMLAttribute && elementName(m) == name {
			return m
		}
	}
	return nil
}

// elementName is m's element or attribute name: its xmlName, else its
// member name.
func elementName(m *smithy.Member) string {
	if m.XMLName != "" {
		return m.XMLName
	}
	return m.Name
}

// localName drops a prefix: xsi:type is the attribute type.
func localName(name string) string {
	if _, local, ok := strings.Cut(name, ":"); ok {
		return local
	}
	return name
}

func (d *xmlDoc) ReadStructMember() (*smithy.Member, bool, error) {
	top := d.top()
	if top == nil || top.kind != structFrame {
		return nil, false, &usageError{reason: "ReadStructMember outside a structure"}
	}
	if top.next == len(top.fields) {
		d.open = d.open[:len(d.open)-1]
		return nil, false, nil
	}
	f := top.fields[top.next]
	top.next++
	d.cur, d.curFlat = f.v, f.flat
	return f.m, true, nil
}

func (d *xmlDoc) ReadUnion(s *smithy.Schema) (*smithy.Member, error) {
	node, root, err := d.element("ReadUnion")
	if err != nil {
		return nil, err
	}
	if root {
		if err := d.checkRoot(s); err != nil {
			return nil, err
		}
	}
	fs := fields(s, node, root)
	if len(fs) != 1 {
		return nil, &decodeError{Message: fmt.Sprintf("the union %s must have exactly one member set, found %d", s.ID.Name(), len(fs))}
	}
	d.open = append(d.open, readFrame{kind: unionFrame})
	d.cur, d.curFlat = fs[0].v, fs[0].flat
	return fs[0].m, nil
}

func (d *xmlDoc) CloseUnion() error {
	if top := d.top(); top == nil || top.kind != unionFrame {
		return &usageError{reason: "CloseUnion outside a union"}
	}
	d.open = d.open[:len(d.open)-1]
	return nil
}

func (d *xmlDoc) ReadList(*smithy.Member) error {
	var items []value
	if d.curFlat != nil {
		for _, n := range d.curFlat {
			items = append(items, value{node: n})
		}
	} else {
		node, _, err := d.element("ReadList")
		if err != nil {
			return err
		}
		for _, c := range node.Children {
			items = append(items, value{node: c})
		}
	}
	d.curFlat = nil
	d.open = append(d.open, readFrame{kind: listFrame, items: items})
	return nil
}

func (d *xmlDoc) ReadListElement() (bool, error) {
	top := d.top()
	if top == nil || top.kind != listFrame {
		return false, &usageError{reason: "ReadListElement outside a list"}
	}
	if top.next == len(top.items) {
		d.open = d.open[:len(d.open)-1]
		return false, nil
	}
	d.cur = top.items[top.next]
	top.next++
	return true, nil
}

func (d *xmlDoc) ReadMap(m *smithy.Member) error {
	f := readFrame{kind: mapFrame, keyName: "key", valueName: "value"}
	if k := m.Target.Members[0].XMLName; k != "" {
		f.keyName = k
	}
	if v := m.Target.Members[1].XMLName; v != "" {
		f.valueName = v
	}
	if d.curFlat != nil {
		f.entries = d.curFlat
	} else {
		node, _, err := d.element("ReadMap")
		if err != nil {
			return err
		}
		f.entries = node.Children
	}
	d.curFlat = nil
	d.open = append(d.open, f)
	return nil
}

func (d *xmlDoc) ReadMapKey() (key string, ok bool, err error) {
	top := d.top()
	if top == nil || top.kind != mapFrame {
		return "", false, &usageError{reason: "ReadMapKey outside a map"}
	}
	if top.next == len(top.entries) {
		d.open = d.open[:len(d.open)-1]
		return "", false, nil
	}
	e := top.entries[top.next]
	top.next++
	k, hasKey := e.Child(top.keyName)
	v, hasValue := e.Child(top.valueName)
	if !hasKey || !hasValue {
		return "", false, &decodeError{Message: fmt.Sprintf("a map entry <%s> needs both <%s> and <%s>", e.Name, top.keyName, top.valueName)}
	}
	d.cur = value{node: v}
	return k.Text, true, nil
}

func (d *xmlDoc) ReadString(*smithy.Member) (string, error) { return d.cur.text(), nil }

func (d *xmlDoc) ReadBool(m *smithy.Member) (bool, error) { return parse(d, m, restbind.ParseBool) }

func (d *xmlDoc) ReadInt8(m *smithy.Member) (int8, error) {
	v, err := d.readInt(m, 8)
	return int8(v), err //nolint:gosec // readInt checked the range for 8 bits
}

func (d *xmlDoc) ReadInt16(m *smithy.Member) (int16, error) {
	v, err := d.readInt(m, 16)
	return int16(v), err //nolint:gosec // readInt checked the range for 16 bits
}

func (d *xmlDoc) ReadInt32(m *smithy.Member) (int32, error) {
	v, err := d.readInt(m, 32)
	return int32(v), err //nolint:gosec // readInt checked the range for 32 bits
}

func (d *xmlDoc) ReadInt64(m *smithy.Member) (int64, error) { return d.readInt(m, 64) }

func (d *xmlDoc) ReadFloat32(m *smithy.Member) (float32, error) {
	v, err := parse(d, m, func(s string) (float64, error) { return restbind.ParseFloat(s, 32) })
	return float32(v), err
}

func (d *xmlDoc) ReadFloat64(m *smithy.Member) (float64, error) {
	return parse(d, m, func(s string) (float64, error) { return restbind.ParseFloat(s, 64) })
}

func (d *xmlDoc) ReadBigInt(m *smithy.Member) (*big.Int, error) {
	return parse(d, m, restbind.ParseBigInt)
}

func (d *xmlDoc) ReadBigDecimal(m *smithy.Member) (smithy.BigDecimal, error) {
	return parse(d, m, restbind.ParseBigDecimal)
}

func (d *xmlDoc) ReadTimestamp(m *smithy.Member) (time.Time, error) {
	format := restbind.TimestampFormat(m, smithy.DateTime)
	return parse(d, m, func(s string) (time.Time, error) { return restbind.ParseTimestamp(s, format) })
}

func (d *xmlDoc) ReadBlob(m *smithy.Member) ([]byte, error) {
	return parse(d, m, restbind.ParseBase64)
}

// ReadDocument fails: XML has no document type.
func (d *xmlDoc) ReadDocument(m *smithy.Member) (document.Value, error) {
	return nil, &usageError{reason: "document member " + m.Name + " in an XML body"}
}

// ReadNull reports false: XML has no null.
func (d *xmlDoc) ReadNull(*smithy.Member) (bool, error) { return false, nil }

// ReadBody fails: restbind binds a @streaming payload from the body
// itself, never through a document.
func (d *xmlDoc) ReadBody(*smithy.Member) (smithy.Body, error) {
	return nil, &usageError{reason: "ReadBody on a body document"}
}

// End reports a body that is not well-formed XML; xmlw.Decode has checked
// the whole document, so nothing else can follow the root.
func (d *xmlDoc) End() error { return d.err }

// checkRoot compares the root's local name with the one s's document
// root must have: the payload member's xmlName, else s's xmlName, else
// s's name. A PutBucketTagging body rooted at <Foo> is S3's MalformedXML
// (wire-rest.md 2.3), not an empty Tagging.
func (d *xmlDoc) checkRoot(s *smithy.Schema) error {
	if want := localName(rootName(d.payload, s)); d.root.Name != want {
		return &decodeError{Message: fmt.Sprintf("the root element is <%s>, want <%s>", d.root.Name, want)}
	}
	return nil
}

// element returns the element a structure, union, list or map is read
// from: the document root first, then the positioned value.
func (d *xmlDoc) element(call string) (node *xmlw.Node, root bool, err error) {
	if d.err != nil {
		return nil, false, d.err
	}
	if !d.entered {
		d.entered = true
		return d.root, true, nil
	}
	if d.cur.isAttr || d.cur.node == nil {
		return nil, false, &usageError{reason: call + " on an attribute or no value"}
	}
	return d.cur.node, false, nil
}

func (d *xmlDoc) readInt(m *smithy.Member, bits int) (int64, error) {
	return parse(d, m, func(s string) (int64, error) { return restbind.ParseInt(s, bits) })
}

func (d *xmlDoc) top() *readFrame {
	if len(d.open) == 0 {
		return nil
	}
	return &d.open[len(d.open)-1]
}

// parse reads the positioned value's text through p.
func parse[T any](d *xmlDoc, m *smithy.Member, p func(string) (T, error)) (T, error) {
	v, err := p(d.cur.text())
	if err != nil {
		var zero T
		return zero, &decodeError{Message: fmt.Sprintf("%s: %v", m.Name, err)}
	}
	return v, nil
}
