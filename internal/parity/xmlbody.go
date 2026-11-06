package parity

import (
	"encoding/xml"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/normalize"
)

// xmlNode is an XML element as the comparison reads it: its local name, its
// attributes as written (namespace declarations included, a prefixed name
// as prefix:local), its text and its child elements. Comments, processing
// instructions and the XML declaration are dropped, and so is whitespace-only
// text in an element with children.
type xmlNode struct {
	name     string
	attrs    []xmlAttr
	text     string
	children []xmlNode
}

type xmlAttr struct {
	name, value string
}

// parseXML parses text as one XML element, with nothing but whitespace,
// comments and processing instructions around it. It reads raw tokens, so
// names keep their prefixes as written, and checks the nesting itself.
func parseXML(text string) (xmlNode, bool) {
	dec := xml.NewDecoder(strings.NewReader(text))
	var b xmlBuilder
	for {
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			return b.root, len(b.open) == 0 && b.roots == 1
		}
		if err != nil || !b.add(tok) {
			return xmlNode{}, false
		}
	}
}

// xmlBuilder assembles the element tree of parseXML from raw tokens.
type xmlBuilder struct {
	open  []*openElement
	root  xmlNode
	roots int
}

// openElement is an element the builder has read the start of.
type openElement struct {
	node  xmlNode
	qname xml.Name
	text  []string
}

// add takes the next token; false means the text is not one well-formed
// element.
func (b *xmlBuilder) add(tok xml.Token) bool {
	switch t := tok.(type) {
	case xml.StartElement:
		if len(b.open) == 0 && b.roots > 0 {
			return false
		}
		n := xmlNode{name: t.Name.Local}
		for _, a := range t.Attr {
			n.attrs = append(n.attrs, xmlAttr{name: rawName(a.Name), value: a.Value})
		}
		b.open = append(b.open, &openElement{node: n, qname: t.Name})
	case xml.EndElement:
		if len(b.open) == 0 || b.open[len(b.open)-1].qname != t.Name {
			return false
		}
		done := b.open[len(b.open)-1]
		b.open = b.open[:len(b.open)-1]
		done.node.text = elementText(done.text, len(done.node.children) == 0)
		if len(b.open) == 0 {
			b.root, b.roots = done.node, b.roots+1
			return true
		}
		parent := b.open[len(b.open)-1]
		parent.node.children = append(parent.node.children, done.node)
	case xml.CharData:
		if len(b.open) == 0 {
			return strings.TrimSpace(string(t)) == ""
		}
		top := b.open[len(b.open)-1]
		top.text = append(top.text, string(t))
	case xml.Comment, xml.ProcInst, xml.Directive:
	}
	return true
}

// rawName is an attribute name as written: prefix:local, or local alone.
func rawName(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

// elementText is an element's text: all of it in a leaf, and in an element
// with children nothing when it is only the whitespace between them.
func elementText(chunks []string, leaf bool) string {
	s := strings.Join(chunks, "")
	if !leaf && strings.TrimSpace(s) == "" {
		return ""
	}
	return s
}

// leafText is the text of the element reached from n through the first
// child of each name in path.
func (n xmlNode) leafText(path ...string) (string, bool) {
	for _, name := range path {
		i := slices.IndexFunc(n.children, func(c xmlNode) bool { return c.name == name })
		if i < 0 {
			return "", false
		}
		n = n.children[i]
	}
	return n.text, true
}

// normalize applies s to n's text, by n's name first, and to its attribute
// values, and so on down its children.
func (n xmlNode) normalize(s side) xmlNode {
	n.text = s.scalar(n.name, normalize.String, n.text)
	attrs := make([]xmlAttr, len(n.attrs))
	for i, a := range n.attrs {
		attrs[i] = xmlAttr{name: a.name, value: s.text(a.value)}
	}
	n.attrs = attrs
	children := make([]xmlNode, len(n.children))
	for i, c := range n.children {
		children[i] = c.normalize(s)
	}
	n.children = children
	return n
}

// xmlElement compares the normalized elements e (recorded) and a
// (understudy's), which share a name, at p: their text, their attributes,
// then their children (Compare rule 7).
func (c *comparison) xmlElement(p Path, e, a xmlNode) {
	if e.text != a.text {
		c.add(p, Changed, e.text, a.text)
	}
	used := make([]bool, len(a.attrs))
	for _, ea := range e.attrs {
		ap := p.child("@" + ea.name)
		j := slices.IndexFunc(a.attrs, func(x xmlAttr) bool { return x.name == ea.name })
		if j < 0 {
			c.add(ap, Missing, ea.value, "")
			continue
		}
		used[j] = true
		if ea.value != a.attrs[j].value {
			c.add(ap, Changed, ea.value, a.attrs[j].value)
		}
	}
	for j, aa := range a.attrs {
		if !used[j] {
			c.add(p.child("@"+aa.name), Extra, "", aa.value)
		}
	}
	c.xmlChildren(p, e.children, a.children)
}

// xmlChildren compares the children of the elements at p: the n-th child
// of a name with understudy's n-th child of that name, then understudy's
// unmatched children as Extra, then one Reordered when the sequence of child
// names differs while the names are the same.
func (c *comparison) xmlChildren(p Path, e, a []xmlNode) {
	used := make([]bool, len(a))
	for i, ec := range e {
		k := occurrence(e, i)
		cp := p.child(xmlToken(ec.name, k))
		j := nthNamed(a, ec.name, k)
		if j < 0 {
			c.add(cp, Missing, ec.display(), "")
			continue
		}
		used[j] = true
		c.xmlElement(cp, ec, a[j])
	}
	for j, ac := range a {
		if !used[j] {
			c.add(p.child(xmlToken(ac.name, occurrence(a, j))), Extra, "", ac.display())
		}
	}
	en, an := childNames(e), childNames(a)
	if slices.Equal(en, an) {
		return
	}
	if sorted, sortedOurs := slices.Sorted(slices.Values(en)), slices.Sorted(slices.Values(an)); slices.Equal(sorted, sortedOurs) {
		c.add(p, Reordered, strings.Join(en, ","), strings.Join(an, ","))
	}
}

// occurrence is how many siblings before ns[i] share its name.
func occurrence(ns []xmlNode, i int) int {
	k := 0
	for _, n := range ns[:i] {
		if n.name == ns[i].name {
			k++
		}
	}
	return k
}

// nthNamed is the index of the k-th (0-based) node of ns called name, or -1.
func nthNamed(ns []xmlNode, name string, k int) int {
	for i, n := range ns {
		if n.name != name {
			continue
		}
		if k == 0 {
			return i
		}
		k--
	}
	return -1
}

// xmlToken is the path token of the k-th (0-based) sibling called name.
func xmlToken(name string, k int) string {
	if k == 0 {
		return name
	}
	return name + "[" + strconv.Itoa(k) + "]"
}

func childNames(ns []xmlNode) []string {
	names := make([]string, len(ns))
	for i, n := range ns {
		names[i] = n.name
	}
	return names
}

// display is a leaf's text, as a JSON string displays as its text, or any
// other element as compact XML.
func (n xmlNode) display() string {
	if len(n.attrs) == 0 && len(n.children) == 0 {
		return n.text
	}
	return n.render()
}

// encode writes n as a document parseXML reads back as n: its text and
// attribute values escaped, its text before its children. Element names
// are local names, so a prefixed element loses its prefix, which the
// comparison does not read.
func (n xmlNode) encode() string {
	text := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\r", "&#xD;")
	attr := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "\r", "&#xD;", "\n", "&#xA;", "\t", "&#x9;")
	var b strings.Builder
	var write func(xmlNode)
	write = func(n xmlNode) {
		b.WriteString("<" + n.name)
		for _, a := range n.attrs {
			b.WriteString(" " + a.name + `="` + attr.Replace(a.value) + `"`)
		}
		b.WriteString(">" + text.Replace(n.text))
		for _, c := range n.children {
			write(c)
		}
		b.WriteString("</" + n.name + ">")
	}
	write(n)
	return b.String()
}

// render writes n compactly for a difference's value.
func (n xmlNode) render() string {
	var b strings.Builder
	n.write(&b)
	return b.String()
}

func (n xmlNode) write(b *strings.Builder) {
	b.WriteString("<" + n.name)
	for _, a := range n.attrs {
		b.WriteString(" " + a.name + "=" + strconv.Quote(a.value))
	}
	if n.text == "" && len(n.children) == 0 {
		b.WriteString("/>")
		return
	}
	b.WriteString(">" + n.text)
	for _, ch := range n.children {
		ch.write(b)
	}
	b.WriteString("</" + n.name + ">")
}
