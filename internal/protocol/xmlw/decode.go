package xmlw

import (
	"bufio"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"iter"
	"strconv"
	"strings"
)

// byteOrderMark is the UTF-8 encoding of U+FEFF, which may open a document.
const byteOrderMark = "\xEF\xBB\xBF"

// MaxDepth is the deepest element nesting Decode accepts. S3 configuration
// bodies, the deepest XML requests understudy reads, nest about six levels
// (LifecycleConfiguration/Rule/Filter/And/Tag/Key).
const MaxDepth = 32

// Node is an element of a decoded request body, named by its local name with
// any namespace or prefix dropped.
type Node struct {
	Name string
	// Attrs are the attributes in document order, without namespace
	// declarations.
	Attrs []Attr
	// Children are the child elements in document order.
	Children []*Node
	// Text is the character data directly inside the element (CDATA
	// included, comments dropped), concatenated around any children.
	Text string
}

// Attr is an attribute of a Node, named by its local name.
type Attr struct {
	Name, Value string
}

// Child returns the first child element named name.
func (n *Node) Child(name string) (*Node, bool) {
	for _, c := range n.Children {
		if c.Name == name {
			return c, true
		}
	}
	return nil, false
}

// ChildrenNamed yields the child elements named name in document order, the
// shape of a flattened list such as the Object entries of an S3 Delete body.
func (n *Node) ChildrenNamed(name string) iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		for _, c := range n.Children {
			if c.Name == name && !yield(c) {
				return
			}
		}
	}
}

// Attr returns the value of the first attribute with local name name, so the
// xsi:type of an S3 Grantee is Attr("type").
func (n *Node) Attr(name string) (string, bool) {
	for _, a := range n.Attrs {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

// Decode reads one XML document of at most maxBytes bytes from r and returns
// its root element. A negative maxBytes is treated as 0. On success Decode has
// read r to EOF; on error r may be partly unread. One leading UTF-8 byte order
// mark is skipped, as XML 1.0 allows.
//
// A body that is not a single well-formed document, declares a DOCTYPE, or
// nests deeper than MaxDepth yields *MalformedXMLError; a body longer than
// maxBytes yields *TooLargeError; a failure to read r is returned wrapped with
// %w. Well-formed means accepted by encoding/xml in strict mode, which requires
// UTF-8 and XML 1.0, and also having exactly one root element, nothing but
// whitespace, comments and processing instructions around it, an XML
// declaration only at the very start, and no repeated attribute.
//
// These encoding/xml leniencies remain: a character reference to a surrogate,
// such as &#xD800;, decodes to U+FFFD; the XML declaration's pseudo-attributes
// are checked only for the values of version and encoding when present, so a
// declaration without version or with unknown pseudo-attributes passes; and
// namespace well-formedness is not checked, so undeclared prefixes on elements
// and attributes and a prefix bound to the empty namespace name pass. The tree
// names elements and attributes by local name, so none of these changes it.
func Decode(r io.Reader, maxBytes int64) (*Node, error) {
	limit := max(maxBytes, 0)
	body := &limitedBody{r: r, remaining: limit}
	in := bufio.NewReader(body)
	if prefix, err := in.Peek(len(byteOrderMark)); err == nil && string(prefix) == byteOrderMark {
		if _, err := in.Discard(len(byteOrderMark)); err != nil {
			return nil, fmt.Errorf("xmlw: skip the byte order mark of the XML body: %w", err)
		}
	}
	root, err := buildTree(xml.NewDecoder(in))
	switch {
	case body.tooLarge:
		return nil, &TooLargeError{Limit: limit}
	case body.readErr != nil:
		return nil, fmt.Errorf("xmlw: read XML body: %w", body.readErr)
	case err != nil:
		return nil, err
	}
	return root, nil
}

// buildTree builds the element tree from d's tokens.
func buildTree(d *xml.Decoder) (*Node, error) {
	var b treeBuilder
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			if b.root == nil {
				return nil, malformed("the body has no root element")
			}
			return b.root, nil
		}
		if err != nil {
			return nil, &MalformedXMLError{Reason: err.Error(), Err: err}
		}
		if err := b.add(tok); err != nil {
			return nil, err
		}
	}
}

// treeBuilder checks what encoding/xml leaves to the caller: one root element,
// only whitespace and markup outside it, an XML declaration only at the very
// start, no DOCTYPE, no repeated attribute and at most MaxDepth levels.
// encoding/xml itself rejects mismatched end tags and a document that ends
// inside an element.
type treeBuilder struct {
	root *Node
	// open holds the open elements, innermost last.
	open []openElement
	// started is true once any token has been added.
	started bool
}

type openElement struct {
	node *Node
	text []byte
}

func (b *treeBuilder) add(tok xml.Token) error {
	first := !b.started
	b.started = true
	switch t := tok.(type) {
	case xml.StartElement:
		return b.start(t)
	case xml.EndElement:
		top := b.open[len(b.open)-1]
		top.node.Text = string(top.text)
		b.open = b.open[:len(b.open)-1]
	case xml.CharData:
		if len(b.open) == 0 {
			// XML 1.0 whitespace (production S) only; strings.TrimSpace
			// would also accept Unicode spaces such as U+00A0.
			if strings.Trim(string(t), " \t\r\n") != "" {
				return malformed("character data outside the root element")
			}
			return nil
		}
		top := &b.open[len(b.open)-1]
		top.text = append(top.text, t...)
	case xml.Directive:
		return malformed("DOCTYPE and other directives are not accepted")
	case xml.ProcInst:
		// XML 1.0 reserves the target xml in any case for the declaration,
		// which may only open the document.
		if strings.EqualFold(t.Target, "xml") && (!first || t.Target != "xml") {
			return malformed("the XML declaration must open the document, before any whitespace")
		}
	case xml.Comment:
	}
	return nil
}

func (b *treeBuilder) start(t xml.StartElement) error {
	if b.root != nil && len(b.open) == 0 {
		return malformed("element <" + t.Name.Local + "> follows the root element")
	}
	if len(b.open) == MaxDepth {
		return malformed("elements nest deeper than " + strconv.Itoa(MaxDepth) + " levels")
	}
	n, err := newNode(t)
	if err != nil {
		return err
	}
	if len(b.open) == 0 {
		b.root = n
	} else {
		parent := b.open[len(b.open)-1].node
		parent.Children = append(parent.Children, n)
	}
	b.open = append(b.open, openElement{node: n})
	return nil
}

func newNode(t xml.StartElement) (*Node, error) {
	n := &Node{Name: t.Name.Local}
	seen := make(map[xml.Name]bool, len(t.Attr))
	for _, a := range t.Attr {
		if seen[a.Name] {
			return nil, malformed("element <" + t.Name.Local + "> repeats attribute " + strconv.Quote(a.Name.Local))
		}
		seen[a.Name] = true
		if a.Name.Space == "xmlns" || a.Name.Space == "" && a.Name.Local == "xmlns" {
			continue
		}
		n.Attrs = append(n.Attrs, Attr{Name: a.Name.Local, Value: a.Value})
	}
	return n, nil
}

// limitedBody reads at most remaining bytes from r and records why reading
// stopped, so Decode can tell an oversized or unreadable body from malformed
// XML whatever error encoding/xml reports.
type limitedBody struct {
	r         io.Reader
	remaining int64
	tooLarge  bool
	readErr   error
}

var errBodyTooLarge = errors.New("xmlw: body exceeds the size limit")

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.tooLarge {
		return 0, errBodyTooLarge
	}
	// Read one byte past the limit, so a body of exactly the limit is not
	// mistaken for a longer one.
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining+1]
	}
	n, err := b.r.Read(p)
	if int64(n) > b.remaining {
		b.tooLarge = true
		return 0, errBodyTooLarge
	}
	b.remaining -= int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		b.readErr = err
	}
	return n, err
}

// MalformedXMLError reports a request body that is not a well-formed XML
// document within the limits of Decode. Codecs answer it with S3's
// MalformedXML; Reason is diagnostic detail and is not sent to clients.
type MalformedXMLError struct {
	Reason string
	// Err is the encoding/xml error behind Reason, usually an
	// *xml.SyntaxError with the line of the defect, or nil when Decode's own
	// checks found it.
	Err error
}

func malformed(reason string) *MalformedXMLError {
	return &MalformedXMLError{Reason: reason}
}

// Error implements error.
func (e *MalformedXMLError) Error() string {
	return "xmlw: malformed XML: " + e.Reason
}

// Unwrap returns the encoding/xml error, if any.
func (e *MalformedXMLError) Unwrap() error {
	return e.Err
}

// TooLargeError reports a request body longer than the limit given to Decode.
type TooLargeError struct {
	// Limit is the maxBytes given to Decode, or 0 for a negative one.
	Limit int64
}

// Error implements error.
func (e *TooLargeError) Error() string {
	return "xmlw: XML body exceeds " + strconv.FormatInt(e.Limit, 10) + " bytes"
}
