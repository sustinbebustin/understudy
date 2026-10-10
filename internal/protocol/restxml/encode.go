package restxml

import (
	"bytes"
	"encoding/base64"
	"maps"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/restbind"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/protocol/xmlw"
)

// element is an element being built. The writer builds a tree before it
// writes, because a structure's xmlAttribute members must land on its
// start tag although model order may put them after child elements (an S3
// Grantee's Type follows its ID).
type element struct {
	name     string
	decls    []nsDecl
	attrs    []attribute
	children []*element
	text     string
}

type nsDecl struct{ prefix, uri string }

type attribute struct{ prefix, local, value string }

type writeFrame struct {
	kind frameKind
	// node receives the children: the element itself, or the parent of a
	// flattened list or map.
	node *element
	// item names a list's elements or a map's entries.
	item               string
	keyName, valueName string
	// itemMember, the list's member, declares its xmlNamespace on each list
	// element, flattened or not (XmlNamespaces, XmlLists). keyMember and
	// valueMember do the same for a map's keys and values, and entryDecls
	// is a flattened map's own member namespace on each entry
	// (RestXmlFlattenedXmlMapWithXmlNamespace).
	itemMember, keyMember, valueMember *smithy.Member
	entryDecls                         []nsDecl
	entry                              *element
	members                            int
	// unwrapped marks an s3UnwrappedXmlOutput root not yet written.
	unwrapped *smithy.Schema
}

// writerOptions are the per-response choices of a writer.
type writerOptions struct {
	// namespace is the service's xmlNamespace, declared on the root.
	namespace string
	// millis writes date-time with milliseconds always (S3).
	millis bool
	// encodeKeys applies encoding-type=url to the key fields.
	encodeKeys bool
	// unwrapped writes the output's one member as the root element.
	unwrapped bool
}

// writer writes one restXml body document (wire-rest.md 2.1): structures
// as elements named by member (or xmlName), the root named by the shape's
// xmlName or name and carrying the namespace, wrapped lists with <member>
// (or the list member's xmlName), flattened lists and maps repeated in
// their parent, map entries as <entry><key/><value/></entry>, attributes
// with their namespace prefix, date-time timestamps, base64 blobs.
type writer struct {
	opts writerOptions
	root *element
	open []writeFrame
	err  error
}

func newWriter(o writerOptions) restbind.DocumentWriter {
	return &writer{opts: o}
}

func (w *writer) WriteStruct(m *smithy.Member, s *smithy.Schema) {
	if w.err != nil {
		return
	}
	if w.root == nil && len(w.open) == 0 && w.opts.unwrapped {
		w.open = append(w.open, writeFrame{kind: structFrame, unwrapped: s})
		return
	}
	if e := w.place(m, s); e != nil {
		w.open = append(w.open, writeFrame{kind: structFrame, node: e})
	}
}

func (w *writer) CloseStruct() {
	if top := w.top(); top != nil && top.unwrapped != nil && w.root == nil {
		// The member was absent: S3 answers a us-east-1 bucket's
		// GetBucketLocation with an empty LocationConstraint.
		w.root = &element{name: rootName(nil, top.unwrapped), decls: w.rootDecls(nil, top.unwrapped)}
	}
	w.close(structFrame, "CloseStruct")
}

func (w *writer) WriteUnion(m *smithy.Member, s *smithy.Schema) {
	if e := w.place(m, s); e != nil {
		w.open = append(w.open, writeFrame{kind: unionFrame, node: e})
	}
}

func (w *writer) CloseUnion() {
	if top := w.top(); top != nil && top.kind == unionFrame && top.members != 1 {
		w.fail("a union must have exactly one member written")
		return
	}
	w.close(unionFrame, "CloseUnion")
}

func (w *writer) WriteList(m *smithy.Member, _ int) {
	if w.err != nil {
		return
	}
	if parent := w.top(); m.XMLFlattened && parent != nil && parent.kind == structFrame {
		parent.members++
		w.open = append(w.open, writeFrame{kind: listFrame, node: parent.node, item: elementName(m), itemMember: m.Target.Members[0]})
		return
	}
	e := w.place(m, m.Target)
	if e == nil {
		return
	}
	member := m.Target.Members[0]
	item := "member"
	if member.XMLName != "" {
		item = member.XMLName
	}
	w.open = append(w.open, writeFrame{kind: listFrame, node: e, item: item, itemMember: member})
}

func (w *writer) CloseList() { w.close(listFrame, "CloseList") }

func (w *writer) WriteMap(m *smithy.Member, _ int) {
	if w.err != nil {
		return
	}
	f := writeFrame{
		kind: mapFrame, item: "entry", keyName: "key", valueName: "value",
		keyMember: m.Target.Members[0], valueMember: m.Target.Members[1],
	}
	if k := f.keyMember.XMLName; k != "" {
		f.keyName = k
	}
	if v := f.valueMember.XMLName; v != "" {
		f.valueName = v
	}
	if parent := w.top(); m.XMLFlattened && parent != nil && parent.kind == structFrame {
		parent.members++
		f.node, f.item, f.entryDecls = parent.node, elementName(m), memberDecls(m, nil)
	} else if f.node = w.place(m, m.Target); f.node == nil {
		return
	}
	w.open = append(w.open, f)
}

func (w *writer) WriteMapKey(key string) {
	top := w.top()
	if w.err != nil || top == nil || top.kind != mapFrame {
		w.fail("WriteMapKey outside a map")
		return
	}
	top.entry = &element{name: top.item, decls: top.entryDecls, children: []*element{
		{name: top.keyName, decls: memberDecls(top.keyMember, nil), text: key},
	}}
	top.node.children = append(top.node.children, top.entry)
}

func (w *writer) CloseMap() { w.close(mapFrame, "CloseMap") }

func (w *writer) WriteString(m *smithy.Member, v string) {
	if w.opts.encodeKeys && m != nil && keyField(m.Name) {
		v = encodeKey(v)
	}
	w.scalar(m, v)
}

func (w *writer) WriteBool(m *smithy.Member, v bool) { w.scalar(m, strconv.FormatBool(v)) }

func (w *writer) WriteInt8(m *smithy.Member, v int8) { w.WriteInt64(m, int64(v)) }

func (w *writer) WriteInt16(m *smithy.Member, v int16) { w.WriteInt64(m, int64(v)) }

func (w *writer) WriteInt32(m *smithy.Member, v int32) { w.WriteInt64(m, int64(v)) }

func (w *writer) WriteInt64(m *smithy.Member, v int64) { w.scalar(m, strconv.FormatInt(v, 10)) }

func (w *writer) WriteFloat32(m *smithy.Member, v float32) {
	w.scalar(m, restbind.FormatFloat(float64(v), 32))
}

func (w *writer) WriteFloat64(m *smithy.Member, v float64) {
	w.scalar(m, restbind.FormatFloat(v, 64))
}

func (w *writer) WriteBigInt(m *smithy.Member, v *big.Int) {
	if v == nil {
		w.fail("WriteBigInt with a nil value; an absent member is not written")
		return
	}
	w.scalar(m, v.String())
}

func (w *writer) WriteBigDecimal(m *smithy.Member, v smithy.BigDecimal) { w.scalar(m, string(v)) }

func (w *writer) WriteTimestamp(m *smithy.Member, v time.Time) {
	format := restbind.TimestampFormat(m, smithy.DateTime)
	if format == smithy.DateTime && w.opts.millis {
		w.scalar(m, v.UTC().Truncate(time.Millisecond).Format(restbind.DateTimeMillis))
		return
	}
	w.scalar(m, restbind.FormatTimestamp(v, format))
}

func (w *writer) WriteBlob(m *smithy.Member, v []byte) {
	w.scalar(m, base64.StdEncoding.EncodeToString(v))
}

// WriteDocument fails: XML has no document type.
func (w *writer) WriteDocument(*smithy.Member, document.Value) {
	w.fail("a document member in an XML body")
}

// WriteNull writes nothing: XML has no null, so a sparse collection's null
// value is an absent element.
func (w *writer) WriteNull(*smithy.Member) {
	if top := w.top(); top != nil && top.kind == mapFrame {
		top.entry = nil
	}
}

// WriteBody fails: restbind takes a @streaming payload itself.
func (w *writer) WriteBody(*smithy.Member, smithy.BodySource) {
	w.fail("WriteBody on a body document")
}

// Bytes writes the document: the XML declaration and line feed S3 sends,
// then the tree, through xmlw.
func (w *writer) Bytes() ([]byte, error) {
	if w.err != nil {
		return nil, w.err
	}
	if w.root == nil || len(w.open) != 0 {
		return nil, &writerUsageError{reason: "the document has no complete root element"}
	}
	var buf bytes.Buffer
	x := xmlw.New(&buf)
	x.Declaration()
	emit(x, w.root, nil)
	if err := x.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// emit writes e and its children. inScope maps each prefix ("" for the
// default namespace) to the URI an ancestor declared; a declaration that
// repeats it is redundant and left out, so a nested structure whose shape
// has the root's namespace does not declare it again (XmlNamespaces).
func emit(x *xmlw.Writer, e *element, inScope map[string]string) {
	x.Start(e.name)
	declared := false
	for _, d := range e.decls {
		if uri, ok := inScope[d.prefix]; ok && uri == d.uri {
			continue
		}
		if !declared {
			inScope, declared = maps.Clone(inScope), true
			if inScope == nil {
				inScope = map[string]string{}
			}
		}
		inScope[d.prefix] = d.uri
		if d.prefix == "" {
			x.Attr("xmlns", d.uri)
		} else {
			x.AttrNS("xmlns", d.prefix, d.uri)
		}
	}
	for _, a := range e.attrs {
		if a.prefix == "" {
			x.Attr(a.local, a.value)
		} else {
			x.AttrNS(a.prefix, a.local, a.value)
		}
	}
	if e.text != "" {
		x.Text(e.text)
	}
	for _, c := range e.children {
		emit(x, c, inScope)
	}
	x.End()
}

// scalar writes a scalar value held by m: an attribute of the enclosing
// element for an xmlAttribute member, else an element.
func (w *writer) scalar(m *smithy.Member, text string) {
	if w.err != nil {
		return
	}
	if top := w.top(); m != nil && m.XMLAttribute && top != nil && (top.kind == structFrame || top.kind == unionFrame) {
		top.members++
		prefix, local, ok := strings.Cut(elementName(m), ":")
		if !ok {
			prefix, local = "", prefix
		}
		top.node.attrs = append(top.node.attrs, attribute{prefix: prefix, local: local, value: text})
		return
	}
	if e := w.place(m, nil); e != nil {
		e.text = text
	}
}

// place creates the element for a value held by m (of shape s, when
// known) where the open container puts it, and returns it.
func (w *writer) place(m *smithy.Member, s *smithy.Schema) *element {
	if w.err != nil {
		return nil
	}
	top := w.top()
	switch {
	case top == nil:
		if w.root != nil || s == nil {
			w.fail("the document has one root element: a structure or union")
			return nil
		}
		w.root = &element{name: rootName(m, s), decls: w.rootDecls(m, s)}
		return w.root
	case top.unwrapped != nil:
		// The output's one member is the root itself.
		top.members++
		w.root = &element{name: rootName(nil, top.unwrapped), decls: w.rootDecls(nil, top.unwrapped)}
		return w.root
	case top.kind == listFrame:
		e := &element{name: top.item, decls: memberDecls(top.itemMember, s)}
		top.node.children = append(top.node.children, e)
		return e
	case top.kind == mapFrame:
		if top.entry == nil {
			w.fail("a map value was written without its key")
			return nil
		}
		e := &element{name: top.valueName, decls: memberDecls(top.valueMember, s)}
		top.entry.children = append(top.entry.children, e)
		top.entry = nil
		return e
	case m == nil:
		w.fail("a structure or union member was written without its member")
		return nil
	}
	top.members++
	e := &element{name: elementName(m), decls: memberDecls(m, s)}
	top.node.children = append(top.node.children, e)
	return e
}

// rootName names the document root: the payload member's xmlName, else
// the shape's xmlName, else the shape's name.
func rootName(m *smithy.Member, s *smithy.Schema) string {
	switch {
	case m != nil && m.XMLName != "":
		return m.XMLName
	case s.XMLName != "":
		return s.XMLName
	default:
		return s.ID.Name()
	}
}

// rootDecls is the root's namespace: the member's or shape's xmlNamespace,
// else the service's, so every S3 success root carries
// xmlns="http://s3.amazonaws.com/doc/2006-03-01/" (wire-rest.md 2.2).
func (w *writer) rootDecls(m *smithy.Member, s *smithy.Schema) []nsDecl {
	if d := memberDecls(m, s); d != nil {
		return d
	}
	if w.opts.namespace != "" {
		return []nsDecl{{uri: w.opts.namespace}}
	}
	return nil
}

// memberDecls is the xmlNamespace of a member or its shape, declared on
// the element: an S3 Grantee binds the xsi prefix its Type attribute uses.
func memberDecls(m *smithy.Member, s *smithy.Schema) []nsDecl {
	ns := (*smithy.XMLNamespace)(nil)
	switch {
	case m != nil && m.XMLNamespace != nil:
		ns = m.XMLNamespace
	case s != nil && s.XMLNamespace != nil:
		ns = s.XMLNamespace
	default:
		return nil
	}
	return []nsDecl{{prefix: ns.Prefix, uri: ns.URI}}
}

func (w *writer) close(kind frameKind, call string) {
	if w.err != nil {
		return
	}
	top := w.top()
	if top == nil || top.kind != kind {
		w.fail(call + " does not match the open container")
		return
	}
	w.open = w.open[:len(w.open)-1]
}

func (w *writer) top() *writeFrame {
	if len(w.open) == 0 {
		return nil
	}
	return &w.open[len(w.open)-1]
}

func (w *writer) fail(reason string) {
	if w.err == nil {
		w.err = &writerUsageError{reason: reason}
	}
}

// writerUsageError is generated code driving the writer in a way the open
// element does not allow: a bug.
type writerUsageError struct{ reason string }

func (e *writerUsageError) Error() string { return "restxml: serializer misuse: " + e.reason }

// keyField reports whether encoding-type=url applies to a member of this
// name in S3 listings: ListObjects Delimiter, Marker, NextMarker, Key and
// the common prefixes' Prefix; ListObjectsV2 StartAfter; ListObjectVersions
// and ListMultipartUploads KeyMarker and NextKeyMarker (wire-rest.md 3.4).
func keyField(name string) bool {
	switch name {
	case "Key", "Prefix", "Delimiter", "Marker", "NextMarker", "StartAfter", "KeyMarker", "NextKeyMarker":
		return true
	default:
		return false
	}
}

// encodeKey percent-encodes every byte but the RFC 3986 unreserved
// characters and "/", in uppercase hex: a literal "+" becomes %2B, so
// botocore's unquote_plus reads it back (wire-rest.md 3.4).
func encodeKey(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		switch c := s[i]; {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_', c == '~', c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
