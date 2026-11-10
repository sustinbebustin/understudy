package parity

import (
	"slices"
	"strconv"
	"strings"
)

// Mutate flips the recorded value at field, for the sensitivity check: a
// string changes its last ASCII digit to (d+1) mod
// 10, else its last ASCII letter to the next one in the same case (z to a),
// else gains a trailing "x"; a number (status included) changes its
// literal's last digit the same way; a bool flips; null, an object, an array
// or an element with children is removed. An absent field whose parent is
// present gets the string "x": a member appended to its object, an element
// <name>x</name> to its parent, an element at an array's length, a header
// with the value "x". error/code and error/message flip the node the
// comparison reads them from (the code part of an x-amzn-ErrorType or
// __type). The field body empties the body, or makes an empty one "x". A
// changed body is written back compactly, members in their order.
//
// A field that is not a leaf, or whose parent is absent, is a *PathError.
// r is not changed.
func Mutate(r Response, field Path) (Response, error) {
	if _, err := ParsePath(string(field)); err != nil {
		return Response{}, err
	}
	if reason := field.leafReason(); reason != "" {
		return Response{}, &PathError{Text: string(field), Reason: reason}
	}
	m := r
	m.Headers = slices.Clone(r.Headers)
	var reason string
	switch field {
	case statusPath:
		m.Status = r.Status - r.Status%10 + (r.Status%10+1)%10
	case errorCodePath:
		reason = m.flipErrorCode()
	case errorMessagePath:
		reason = m.editBody(func(d document) (document, string) { return flipErrorMember(d, "Message", "message", "Message") })
	case bodyPath:
		m.Body = NoBody{}
		if b := readBody(r); b.known && len(b.raw) == 0 {
			m.Body = TextBody{Text: "x"}
		}
	case errorPath, headerPath:
		// leafReason refused both.
	default:
		if name, ok := strings.CutPrefix(string(field), string(headerPath)+"/"); ok {
			m.Headers = flipHeader(m.Headers, name)
			break
		}
		tokens, _ := bodyTokens(field)
		reason = m.editBody(func(d document) (document, string) { return mutateDocument(d, tokens) })
	}
	if reason != "" {
		return Response{}, &PathError{Text: string(field), Reason: reason}
	}
	return m, nil
}

// flipText flips a string as Mutate does.
func flipText(s string) string {
	if i := strings.LastIndexFunc(s, func(r rune) bool { return '0' <= r && r <= '9' }); i >= 0 {
		return s[:i] + string(rune('0'+(s[i]-'0'+1)%10)) + s[i+1:]
	}
	isLetter := func(r rune) bool { return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' }
	if i := strings.LastIndexFunc(s, isLetter); i >= 0 {
		c := s[i] + 1
		switch s[i] {
		case 'z':
			c = 'a'
		case 'Z':
			c = 'A'
		}
		return s[:i] + string(rune(c)) + s[i+1:]
	}
	return s + "x"
}

// flipCode flips the code part of an x-amzn-ErrorType value or a __type:
// the text after the last '#' and before any ':' (codePart).
func flipCode(v string) string {
	before, rest, colon := strings.Cut(v, ":")
	h := strings.LastIndexByte(before, '#') + 1
	before = before[:h] + flipText(before[h:])
	if colon {
		return before + ":" + rest
	}
	return before
}

// flipHeader flips the first value of the header name in hs, a copy, or
// adds the header with the value "x".
func flipHeader(hs []Header, name string) []Header {
	i := slices.IndexFunc(hs, func(h Header) bool { return strings.EqualFold(h.Name, name) })
	if i < 0 {
		return append(hs, Header{Name: name, Value: "x"})
	}
	hs[i].Value = flipText(hs[i].Value)
	return hs
}

// flipErrorCode flips the error code where errorCode reads it: the
// x-amzn-ErrorType header, else the body.
func (r *Response) flipErrorCode() string {
	if i := slices.IndexFunc(r.Headers, func(h Header) bool { return strings.EqualFold(h.Name, "x-amzn-ErrorType") }); i >= 0 {
		r.Headers[i].Value = flipCode(r.Headers[i].Value)
		return ""
	}
	return r.editBody(func(d document) (document, string) { return flipErrorMember(d, "Code", "__type", "code", "Code") })
}

// editBody replaces r's body by edit's change to its parsed document.
func (r *Response) editBody(edit func(document) (document, string)) string {
	b := readBody(*r)
	if !b.known {
		return "the recording kept no body bytes to change"
	}
	d, reason := edit(b.doc)
	if reason != "" {
		return reason
	}
	switch d := d.(type) {
	case jsonDocument:
		r.Body = TextBody{Text: d.root.render()}
	case xmlDocument:
		r.Body = TextBody{Text: d.root.encode()}
	case textDocument:
		r.Body = TextBody{Text: d.text}
	case nil:
		r.Body = NoBody{}
	}
	return ""
}

// flipErrorMember flips the error code or message in d where errorCode and
// errorMessage read it: in a JSON body the first member of names that holds
// a string (a code by its code part), else names[0] added as "x"; in an XML
// body the child element of the root's Error, else that child added as "x".
func flipErrorMember(d document, element string, names ...string) (out document, reason string) {
	flip := flipText
	if element == "Code" {
		flip = flipCode
	}
	switch d := d.(type) {
	case jsonDocument:
		if d.root.kind != jsonObject {
			return nil, "the JSON body is not an object, so it holds no error member"
		}
		ms := slices.Clone(d.root.members)
		i := -1
		for _, name := range names {
			if i = slices.IndexFunc(ms, func(m jsonMember) bool { return m.name == name && m.value.kind == jsonString }); i >= 0 {
				break
			}
		}
		if i >= 0 {
			ms[i].value.text = flip(ms[i].value.text)
		} else {
			ms = append(ms, jsonMember{name: names[0], value: jsonNode{kind: jsonString, text: "x"}})
		}
		d.root.members = ms
		return d, ""
	case xmlDocument:
		d.root, reason = d.root.mutated([]string{"Error", element}, flip)
		return d, reason
	case textDocument:
	}
	return nil, "the body is neither JSON nor XML, so it holds no error member"
}

// mutateDocument mutates the body field whose tokens follow body/; a nil
// document is a body emptied by removing the XML root.
func mutateDocument(d document, tokens []string) (out document, reason string) {
	switch d := d.(type) {
	case jsonDocument:
		d.root, reason = d.root.mutated(tokens)
		return d, reason
	case xmlDocument:
		s, ok := parseXMLStep(tokens[0])
		switch {
		case !ok || s.attr || s.k != 0 || s.name != d.root.name:
			return nil, "names no root element of the XML body, whose root is " + d.root.name
		case len(tokens) == 1 && len(d.root.children) > 0:
			return nil, ""
		case len(tokens) == 1:
			d.root.text = flipText(d.root.text)
			return d, ""
		}
		d.root, reason = d.root.mutated(tokens[1:], flipText)
		return d, reason
	case textDocument:
	}
	return nil, "the body is neither JSON nor XML, so body/ names nothing in it; settle body"
}

// flipped is the scalar v flipped; removable reports whether Mutate removes
// v instead.
func (v jsonNode) flipped() jsonNode {
	switch v.kind {
	case jsonString, jsonNumber:
		v.text = flipText(v.text)
	case jsonBool:
		v.text = strconv.FormatBool(v.text != "true")
	case jsonNull, jsonObject, jsonArray:
	}
	return v
}

func (v jsonNode) removable() bool {
	return v.kind == jsonNull || v.kind == jsonObject || v.kind == jsonArray
}

// mutated returns v with the value tokens name below it flipped, removed or
// added.
func (v jsonNode) mutated(tokens []string) (out jsonNode, reason string) {
	if len(tokens) == 0 {
		return v, "names no member"
	}
	tok, rest := tokens[0], tokens[1:]
	switch v.kind {
	case jsonObject:
		name := memberName(tok)
		i := slices.IndexFunc(v.members, func(m jsonMember) bool { return m.name == name })
		ms := slices.Clone(v.members)
		switch {
		case i < 0 && len(rest) > 0:
			return v, "the member " + name + " is absent, so nothing below it can change"
		case i < 0:
			ms = append(ms, jsonMember{name: name, value: jsonNode{kind: jsonString, text: "x"}})
		case len(rest) == 0 && ms[i].value.removable():
			ms = slices.Delete(ms, i, i+1)
		case len(rest) == 0:
			ms[i].value = ms[i].value.flipped()
		default:
			ms[i].value, reason = ms[i].value.mutated(rest)
		}
		v.members = ms
		return v, reason
	case jsonArray:
		v.elems, reason = mutatedElems(v.elems, tok, rest)
		return v, reason
	case jsonNull, jsonBool, jsonString, jsonNumber:
	}
	return v, "a JSON " + v.render() + " holds no member " + tok
}

// mutatedElems is the array elems with the element at the index tok
// changed as mutated changes a member; tok equal to the length appends "x".
func mutatedElems(elems []jsonNode, tok string, rest []string) (out []jsonNode, reason string) {
	i, err := strconv.Atoi(tok)
	es := slices.Clone(elems)
	switch {
	case !isIndex(tok) || err != nil || i > len(es) || i == len(es) && len(rest) > 0:
		return elems, "the array has " + strconv.Itoa(len(es)) + " elements, so " + tok + " names no element and is not its end"
	case i == len(es):
		es = append(es, jsonNode{kind: jsonString, text: "x"})
	case len(rest) == 0 && es[i].removable():
		es = slices.Delete(es, i, i+1)
	case len(rest) == 0:
		es[i] = es[i].flipped()
	default:
		es[i], reason = es[i].mutated(rest)
	}
	return es, reason
}

// mutated returns n with the element or attribute tokens name below n
// flipped by flip, removed or added.
func (n xmlNode) mutated(tokens []string, flip func(string) string) (out xmlNode, reason string) {
	if len(tokens) == 0 {
		return n, "names no element"
	}
	s, ok := parseXMLStep(tokens[0])
	switch {
	case !ok:
		return n, tokens[0] + " is not an element name, name[i] or @attr"
	case s.attr && len(tokens) > 1:
		return n, "an attribute has nothing below it"
	case s.attr:
		attrs := slices.Clone(n.attrs)
		if i := slices.IndexFunc(attrs, func(a xmlAttr) bool { return a.name == s.name }); i >= 0 {
			attrs[i].value = flip(attrs[i].value)
		} else {
			attrs = append(attrs, xmlAttr{name: s.name, value: "x"})
		}
		n.attrs = attrs
		return n, ""
	}
	cs := slices.Clone(n.children)
	i := s.child(cs)
	named := len(slices.DeleteFunc(childNames(cs), func(c string) bool { return c != s.name }))
	switch {
	case i < 0 && (len(tokens) > 1 || s.k != named):
		return n, "the element " + tokens[0] + " under " + n.name + " is absent, so it cannot be changed or added there"
	case i < 0:
		cs = append(cs, xmlNode{name: s.name, text: "x"})
	case len(tokens) == 1 && len(cs[i].children) > 0:
		cs = slices.Delete(cs, i, i+1)
	case len(tokens) == 1:
		cs[i].text = flip(cs[i].text)
	default:
		cs[i], reason = cs[i].mutated(tokens[1:], flip)
	}
	n.children = cs
	return n, reason
}
