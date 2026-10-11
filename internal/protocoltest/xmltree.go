package protocoltest

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
)

// xmlElement is an element of a body the XML comparator reads: its name
// and attribute names resolved to namespace URI and local name, so two
// documents that bind one namespace to different prefixes compare equal
// and two that use different namespaces do not. Namespace declarations
// are not attributes.
type xmlElement struct {
	Name     xml.Name
	Attrs    []xml.Attr
	Children []*xmlElement
	// Text is the character data directly inside the element, concatenated
	// around any children.
	Text string
}

// child returns the first child of local name name.
func (e *xmlElement) child(name string) (*xmlElement, bool) {
	for _, c := range e.Children {
		if c.Name.Local == name {
			return c, true
		}
	}
	return nil, false
}

// qualified renders a name as {uri}local, or local alone outside any
// namespace.
func qualified(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return "{" + n.Space + "}" + n.Local
}

// decodeXML reads one document of at most maxCompareBytes.
func decodeXML(data []byte) (*xmlElement, error) {
	if len(data) > maxCompareBytes {
		return nil, errors.New("the document is larger than " + strconv.Itoa(maxCompareBytes) + " bytes")
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	var b treeBuilder
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if err := b.add(tok); err != nil {
			return nil, err
		}
	}
	if b.root == nil {
		return nil, errors.New("the document has no root element")
	}
	return b.root, nil
}

// treeBuilder assembles elements from tokens: root once its start is read,
// open the elements started and not yet ended.
type treeBuilder struct {
	root *xmlElement
	open []*xmlElement
}

func (b *treeBuilder) add(tok xml.Token) error {
	switch t := tok.(type) {
	case xml.StartElement:
		e := &xmlElement{Name: t.Name}
		for _, a := range t.Attr {
			if a.Name.Space != "xmlns" && (a.Name.Space != "" || a.Name.Local != "xmlns") {
				e.Attrs = append(e.Attrs, a)
			}
		}
		switch {
		case len(b.open) > 0:
			parent := b.open[len(b.open)-1]
			parent.Children = append(parent.Children, e)
		case b.root != nil:
			return errors.New("the document has more than one root element")
		default:
			b.root = e
		}
		b.open = append(b.open, e)
	case xml.EndElement:
		b.open = b.open[:len(b.open)-1]
	case xml.CharData:
		if len(b.open) > 0 {
			b.open[len(b.open)-1].Text += string(t)
		}
	case xml.Comment, xml.ProcInst, xml.Directive:
	}
	return nil
}
