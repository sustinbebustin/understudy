package xmlw

import (
	"bytes"
	"io"
	"slices"
	"strconv"
)

// XSINamespace is the XML Schema instance namespace that S3 binds to the xsi
// prefix for the type attribute of an ACL Grantee.
const XSINamespace = "http://www.w3.org/2001/XMLSchema-instance"

// The namespace names that Namespaces in XML 1.0 reserves: xmlNamespace is
// bound to the prefix xml in every document, and xmlnsNamespace is the
// namespace of the xmlns attributes themselves.
const (
	xmlNamespace   = "http://www.w3.org/XML/1998/namespace"
	xmlnsNamespace = "http://www.w3.org/2000/xmlns/"
)

const declaration = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

// phase is where the writer stands in the document.
type phase uint8

const (
	phaseStart  phase = iota // nothing written
	phaseProlog              // XML declaration written, no root yet
	phaseRoot                // inside the root element
	phaseEnd                 // root element closed
)

// Writer builds one XML document from a stream of calls and writes it to an
// io.Writer on Close.
//
// Calls append markup in order; there is no indentation and no reordering, so
// equal call sequences produce equal bytes. A call sequence that cannot produce
// a document that is well-formed and namespace-well-formed (Namespaces in XML
// 1.0) is a *UsageError, and the first one is sticky: later calls do nothing
// and Close returns it. The document is held in memory until Close, so a failed
// document writes nothing to the underlying writer, whatever its size, and the
// caller can still send an error response instead.
type Writer struct {
	out   io.Writer
	doc   bytes.Buffer
	phase phase
	// open holds the open elements, innermost last.
	open []element
	// startTagOpen is true while the innermost start tag still accepts
	// attributes, that is before its closing '>' is written.
	startTagOpen bool
	// attrs holds the attributes written on the open start tag, so a repeat
	// can be refused.
	attrs []writtenAttr
	err   error
}

// element is an open element.
type element struct {
	name string
	// bindings are the namespace prefixes its start tag declares.
	bindings []binding
}

// binding is a namespace prefix declaration, xmlns:prefix="uri".
type binding struct {
	prefix, uri string
}

// writtenAttr is an attribute on the open start tag. Two attributes on one
// tag must differ in expanded name, not only in qualified name, so a:x and
// b:x with a and b bound to one namespace are a repeat.
type writtenAttr struct {
	prefix, qname string
	expanded      expandedName
}

// expandedName is an attribute's namespace name and local name. A namespace
// declaration has the namespace xmlnsNamespace and the declared prefix as its
// local name, or the empty local name for the default namespace.
type expandedName struct {
	space, local string
}

// New returns a Writer that writes to w.
func New(w io.Writer) *Writer {
	return &Writer{out: w}
}

// Declaration writes the XML declaration and a line feed, as S3 does. It must
// be the first call.
func (w *Writer) Declaration() {
	if w.err != nil {
		return
	}
	if w.phase != phaseStart {
		w.fail("Declaration", "the XML declaration must be the first thing written")
		return
	}
	w.write(declaration)
	w.phase = phaseProlog
}

// Start opens an element. Attributes may follow until the next Start, Empty,
// Text or End.
func (w *Writer) Start(name string) {
	w.start("Start", name)
}

// StartNS opens an element that declares xmlns as its default namespace.
func (w *Writer) StartNS(name, xmlns string) {
	if w.start("StartNS", name) {
		w.attr("StartNS", "", "xmlns", xmlns)
	}
}

// StartXSIType opens an element that binds the xsi prefix to XSINamespace and
// carries xsi:type, the form of an S3 ACL Grantee:
//
//	<Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser">
func (w *Writer) StartXSIType(name, xsiType string) {
	if w.start("StartXSIType", name) {
		w.attr("StartXSIType", "xmlns", "xsi", XSINamespace)
		w.attr("StartXSIType", "xsi", "type", xsiType)
	}
}

// Attr adds an attribute to the element opened by the last Start.
func (w *Writer) Attr(name, value string) {
	w.attr("Attr", "", name, value)
}

// AttrNS adds an attribute named prefix:name to the element opened by the last
// Start. Use the prefix xmlns to declare a namespace prefix; the declaration
// must bind a non-empty namespace name, and xml and xmlns cannot be rebound.
// Any other prefix must already be declared, earlier on this start tag or on
// an enclosing element; xml is always declared.
func (w *Writer) AttrNS(prefix, name, value string) {
	if w.err == nil && !isName(prefix) {
		w.fail("AttrNS", "invalid attribute prefix "+strconv.Quote(prefix))
		return
	}
	w.attr("AttrNS", prefix, name, value)
}

// Text writes escaped character data inside the innermost open element.
func (w *Writer) Text(s string) {
	if w.err != nil {
		return
	}
	if w.phase != phaseRoot {
		w.fail("Text", "character data must be inside the root element")
		return
	}
	w.closeStartTag()
	escapeInto(w, s)
}

// End closes the innermost open element with an end tag, even when it has no
// content, so Start then End writes <Name></Name>.
func (w *Writer) End() {
	if w.err != nil {
		return
	}
	if len(w.open) == 0 {
		w.fail("End", "no element is open")
		return
	}
	w.closeStartTag()
	name := w.open[len(w.open)-1].name
	w.open = w.open[:len(w.open)-1]
	w.write("</" + name + ">")
	if len(w.open) == 0 {
		w.phase = phaseEnd
	}
}

// Empty writes a self-closing element without attributes, such as the empty
// collection <Roles/>.
func (w *Writer) Empty(name string) {
	if !w.beginElement("Empty", name) {
		return
	}
	w.write("<" + name + "/>")
	if len(w.open) == 0 {
		w.phase = phaseEnd
	}
}

// Close completes the document and writes it to the underlying writer, which it
// does not close. It returns the first *UsageError of the writer, a *UsageError
// when no root element was written or an element is still open, or the error of
// the underlying writer. Only a complete document is written, and only once.
func (w *Writer) Close() error {
	if w.err != nil {
		return w.err
	}
	switch w.phase {
	case phaseStart, phaseProlog:
		w.fail("Close", "no root element was written")
	case phaseRoot:
		w.fail("Close", "element "+strconv.Quote(w.open[len(w.open)-1].name)+" is not closed")
	case phaseEnd:
		if _, err := w.doc.WriteTo(w.out); err != nil {
			w.err = err
		}
	}
	return w.err
}

// start opens element name for op and reports whether it did.
func (w *Writer) start(op, name string) bool {
	if !w.beginElement(op, name) {
		return false
	}
	w.write("<" + name)
	w.open = append(w.open, element{name: name})
	w.startTagOpen = true
	w.attrs = w.attrs[:0]
	w.phase = phaseRoot
	return true
}

// beginElement checks that an element named name may be written next, and
// closes the parent's start tag.
func (w *Writer) beginElement(op, name string) bool {
	if w.err != nil {
		return false
	}
	if !isName(name) {
		w.fail(op, "invalid element name "+strconv.Quote(name))
		return false
	}
	if w.phase == phaseEnd {
		w.fail(op, "element "+strconv.Quote(name)+" would be a second root element")
		return false
	}
	w.closeStartTag()
	return true
}

// attr writes prefix:name="value", or name="value" when prefix is empty.
func (w *Writer) attr(op, prefix, name, value string) {
	if w.err != nil {
		return
	}
	if !w.startTagOpen {
		w.fail(op, "attribute "+strconv.Quote(name)+" must directly follow the start tag")
		return
	}
	if !isName(name) {
		w.fail(op, "invalid attribute name "+strconv.Quote(name))
		return
	}
	qname := name
	if prefix != "" {
		qname = prefix + ":" + name
	}
	expanded, reason := w.expand(prefix, name, value)
	if reason != "" {
		w.fail(op, "attribute "+strconv.Quote(qname)+": "+reason)
		return
	}
	top := &w.open[len(w.open)-1]
	if i := slices.IndexFunc(w.attrs, func(a writtenAttr) bool { return a.expanded == expanded }); i >= 0 {
		repeat := "is repeated"
		if prior := w.attrs[i].qname; prior != qname {
			repeat = "has the same namespace and local name as " + strconv.Quote(prior)
		}
		w.fail(op, "attribute "+strconv.Quote(qname)+" "+repeat+" on element "+strconv.Quote(top.name))
		return
	}
	if prefix == "xmlns" {
		top.bindings = append(top.bindings, binding{prefix: name, uri: value})
	}
	w.attrs = append(w.attrs, writtenAttr{prefix: prefix, qname: qname, expanded: expanded})
	w.write(" " + qname + `="`)
	escapeInto(w, value)
	w.write(`"`)
}

// expand returns the expanded name of attribute prefix:name with the given
// value on the open start tag, or a non-empty reason when Namespaces in XML 1.0
// forbids it.
func (w *Writer) expand(prefix, name, value string) (expanded expandedName, reason string) {
	switch {
	case prefix == "" && name == "xmlns":
		if value == xmlNamespace || value == xmlnsNamespace {
			return expandedName{}, "the reserved namespace " + strconv.Quote(value) + " cannot be the default namespace"
		}
		return expandedName{space: xmlnsNamespace}, ""
	case prefix == "":
		return expandedName{local: name}, ""
	case prefix == "xmlns":
		switch {
		case name == "xmlns":
			return expandedName{}, "the prefix xmlns is reserved and cannot be declared"
		case name == "xml" && value != xmlNamespace:
			return expandedName{}, "the prefix xml can be bound only to " + strconv.Quote(xmlNamespace)
		case name != "xml" && (value == xmlNamespace || value == xmlnsNamespace):
			return expandedName{}, "the reserved namespace " + strconv.Quote(value) + " cannot be bound to another prefix"
		case value == "":
			return expandedName{}, "a namespace prefix cannot be bound to the empty namespace name"
		case slices.ContainsFunc(w.attrs, func(a writtenAttr) bool { return a.prefix == name }):
			return expandedName{}, "the prefix " + strconv.Quote(name) + " must be declared before an attribute on this start tag uses it"
		}
		return expandedName{space: xmlnsNamespace, local: name}, ""
	}
	uri, ok := w.lookup(prefix)
	if !ok {
		return expandedName{}, "the prefix " + strconv.Quote(prefix) + " is not declared on this element or an enclosing one"
	}
	return expandedName{space: uri, local: name}, ""
}

// lookup returns the namespace name bound to prefix in the scope of the open
// start tag.
func (w *Writer) lookup(prefix string) (string, bool) {
	for _, v := range slices.Backward(w.open) {
		for _, b := range slices.Backward(v.bindings) {
			if b.prefix == prefix {
				return b.uri, true
			}
		}
	}
	if prefix == "xml" {
		return xmlNamespace, true
	}
	return "", false
}

func (w *Writer) closeStartTag() {
	if w.startTagOpen {
		w.write(">")
		w.startTagOpen = false
		w.attrs = w.attrs[:0]
	}
}

// write appends s to the document. A bytes.Buffer write cannot fail; it panics
// only when the document outgrows memory.
func (w *Writer) write(s string) {
	w.doc.WriteString(s)
}

func (w *Writer) fail(op, reason string) {
	w.err = &UsageError{Op: op, Reason: reason}
}

// isName reports whether s is a non-empty ASCII XML NCName: a letter or '_'
// followed by letters, digits, '_', '-' or '.'. Every element and attribute
// name in the AWS models has this form; anything else would break the markup.
func isName(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
		case i > 0 && (c >= '0' && c <= '9' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

// UsageError reports a call sequence that cannot produce a well-formed
// document. It is a defect in the caller, not in the data being written.
type UsageError struct {
	// Op is the Writer method that failed, such as "Attr".
	Op string
	// Reason is the human-readable cause.
	Reason string
}

// Error implements error.
func (e *UsageError) Error() string {
	return "xmlw: " + e.Op + ": " + e.Reason
}
