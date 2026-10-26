package parity

import (
	"encoding/json/jsontext"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/normalize"
)

// jsonKind is the kind of a JSON value.
type jsonKind uint8

const (
	jsonNull jsonKind = iota + 1
	jsonBool
	jsonString
	jsonNumber
	jsonObject
	jsonArray
)

// jsonNode is a JSON value with its members in document order, duplicates
// kept, and numbers as their literal text, so the comparison sees what the
// client recorded.
type jsonNode struct {
	kind jsonKind
	// text is a string's decoded value, a number's literal, or "true",
	// "false" or "null".
	text    string
	members []jsonMember
	elems   []jsonNode
}

type jsonMember struct {
	name  string
	value jsonNode
}

// errNotJSON stops a JSON parse at a token no value starts with.
var errNotJSON = errors.New("not a JSON value")

// parseJSON parses text as exactly one JSON value.
func parseJSON(text string) (jsonNode, bool) {
	dec := jsontext.NewDecoder(strings.NewReader(text), jsontext.AllowDuplicateNames(true))
	v, err := readJSON(dec)
	if err != nil {
		return jsonNode{}, false
	}
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		return jsonNode{}, false
	}
	return v, true
}

func readJSON(dec *jsontext.Decoder) (jsonNode, error) {
	tok, err := dec.ReadToken()
	if err != nil {
		return jsonNode{}, err
	}
	switch tok.Kind() {
	case jsontext.KindNull:
		return jsonNode{kind: jsonNull, text: "null"}, nil
	case jsontext.KindFalse, jsontext.KindTrue:
		return jsonNode{kind: jsonBool, text: tok.String()}, nil
	case jsontext.KindString:
		return jsonNode{kind: jsonString, text: tok.String()}, nil
	case jsontext.KindNumber:
		return jsonNode{kind: jsonNumber, text: tok.String()}, nil
	case jsontext.KindBeginObject:
		v := jsonNode{kind: jsonObject}
		for dec.PeekKind() == jsontext.KindString {
			name, err := dec.ReadToken()
			if err != nil {
				return jsonNode{}, err
			}
			m := jsonMember{name: name.String()}
			if m.value, err = readJSON(dec); err != nil {
				return jsonNode{}, err
			}
			v.members = append(v.members, m)
		}
		_, err := dec.ReadToken()
		return v, err
	case jsontext.KindBeginArray:
		v := jsonNode{kind: jsonArray}
		for dec.PeekKind() != jsontext.KindEndArray {
			e, err := readJSON(dec)
			if err != nil {
				return jsonNode{}, err
			}
			v.elems = append(v.elems, e)
		}
		_, err := dec.ReadToken()
		return v, err
	case jsontext.KindInvalid, jsontext.KindEndObject, jsontext.KindEndArray:
	}
	return jsonNode{}, errNotJSON
}

// stringMember is the string value of v's first member called name, when v
// is an object that has one.
func (v jsonNode) stringMember(name string) (string, bool) {
	for _, m := range v.members {
		if m.name == name && m.value.kind == jsonString {
			return m.value.text, true
		}
	}
	return "", false
}

// normalize applies s to every scalar of v, the value of the member called
// name; an array's elements are read under the array's member name.
func (v jsonNode) normalize(name string, s side) jsonNode {
	switch v.kind {
	case jsonString:
		v.text = s.scalar(name, normalize.String, v.text)
	case jsonNumber:
		v.text = s.scalar(name, normalize.Number, v.text)
	case jsonObject:
		ms := make([]jsonMember, len(v.members))
		for i, m := range v.members {
			ms[i] = jsonMember{name: m.name, value: m.value.normalize(m.name, s)}
		}
		v.members = ms
	case jsonArray:
		es := make([]jsonNode, len(v.elems))
		for i, e := range v.elems {
			es[i] = e.normalize(name, s)
		}
		v.elems = es
	case jsonNull, jsonBool:
	}
	return v
}

// json compares the normalized values e (recorded) and a (understudy's) at
// p (Compare rule 6).
func (c *comparison) json(p Path, e, a jsonNode) {
	if e.kind != a.kind {
		c.add(p, Changed, e.render(), a.render())
		return
	}
	switch e.kind {
	case jsonObject:
		c.jsonObject(p, e.members, a.members)
	case jsonArray:
		for i := range max(len(e.elems), len(a.elems)) {
			ip := p.child(strconv.Itoa(i))
			switch {
			case i >= len(a.elems):
				c.add(ip, Missing, e.elems[i].display(), "")
			case i >= len(e.elems):
				c.add(ip, Extra, "", a.elems[i].display())
			default:
				c.json(ip, e.elems[i], a.elems[i])
			}
		}
	case jsonNull, jsonBool, jsonString, jsonNumber:
		if e.text != a.text {
			c.add(p, Changed, e.display(), a.display())
		}
	}
}

// jsonObject compares each recorded member with understudy's member of the
// same name (the n-th of a name with the n-th), then lists understudy's
// unmatched members as Extra, then one Reordered when the matched members
// appear in another relative order.
func (c *comparison) jsonObject(p Path, e, a []jsonMember) {
	used := make([]bool, len(a))
	var matched []int
	for _, m := range e {
		j := firstUnused(a, used, m.name)
		mp := p.member(m.name)
		if j < 0 {
			c.add(mp, Missing, m.value.display(), "")
			continue
		}
		used[j] = true
		matched = append(matched, j)
		c.json(mp, m.value, a[j].value)
	}
	for j, m := range a {
		if !used[j] {
			c.add(p.member(m.name), Extra, "", m.value.display())
		}
	}
	if slices.IsSorted(matched) {
		return
	}
	recorded := make([]string, len(matched))
	for i, j := range matched {
		recorded[i] = a[j].name
	}
	slices.Sort(matched)
	ours := make([]string, len(matched))
	for i, j := range matched {
		ours[i] = a[j].name
	}
	c.add(p, Reordered, strings.Join(recorded, ","), strings.Join(ours, ","))
}

// firstUnused is the index of the first member of ms called name that no
// recorded member has matched yet, or -1.
func firstUnused(ms []jsonMember, used []bool, name string) int {
	for j, m := range ms {
		if !used[j] && m.name == name {
			return j
		}
	}
	return -1
}

// display is a string's text, or any other value as compact JSON.
func (v jsonNode) display() string {
	if v.kind == jsonString {
		return v.text
	}
	return v.render()
}

// render writes v as compact JSON, members in their order.
func (v jsonNode) render() string {
	var b strings.Builder
	v.write(&b)
	return b.String()
}

func (v jsonNode) write(b *strings.Builder) {
	switch v.kind {
	case jsonString:
		b.WriteString(quoteJSON(v.text))
	case jsonObject:
		b.WriteByte('{')
		for i, m := range v.members {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(quoteJSON(m.name))
			b.WriteByte(':')
			m.value.write(b)
		}
		b.WriteByte('}')
	case jsonArray:
		b.WriteByte('[')
		for i, e := range v.elems {
			if i > 0 {
				b.WriteByte(',')
			}
			e.write(b)
		}
		b.WriteByte(']')
	case jsonNull, jsonBool, jsonNumber:
		b.WriteString(v.text)
	}
}

// quoteJSON quotes s as a JSON string. s came from a parsed document or a
// mask, so it is valid UTF-8; Go quoting stands in should it not be.
func quoteJSON(s string) string {
	q, err := jsontext.AppendQuote(nil, s)
	if err != nil {
		return strconv.Quote(s)
	}
	return string(q)
}
