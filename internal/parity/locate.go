package parity

import (
	"slices"
	"strconv"
	"strings"
)

// bodyTokens are the tokens of a body/... path, raw as the path writes
// them; ok is false for any other path.
func bodyTokens(p Path) ([]string, bool) {
	rest, ok := strings.CutPrefix(string(p), string(bodyPath)+"/")
	if !ok {
		return nil, false
	}
	return strings.Split(rest, "/"), true
}

// memberName decodes an RFC 6901 reference token: ~1 is /, ~0 is ~.
func memberName(tok string) string {
	return strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
}

// xmlStep is one token of an XML body path: the k-th (0-based) child
// element called name, or the attribute name.
type xmlStep struct {
	name string
	k    int
	attr bool
}

// parseXMLStep reads tok, written name, name[i] or @attr.
func parseXMLStep(tok string) (xmlStep, bool) {
	if name, ok := strings.CutPrefix(tok, "@"); ok {
		return xmlStep{name: name, attr: true}, name != ""
	}
	name, rest, indexed := strings.Cut(tok, "[")
	if !indexed {
		return xmlStep{name: tok}, tok != ""
	}
	i, ok := strings.CutSuffix(rest, "]")
	if !ok || !isIndex(i) {
		return xmlStep{}, false
	}
	k, err := strconv.Atoi(i)
	return xmlStep{name: name, k: k}, err == nil && name != ""
}

// child is the index in ns of the element the step names, or -1.
func (s xmlStep) child(ns []xmlNode) int { return nthNamed(ns, s.name, s.k) }

// valueAt is the value a bind reads at p, a leaf path, in r: the status, the
// error code or message as the comparison derives them, a header's first
// value, a JSON string's value or number's text, or an XML element's text or
// attribute value. ok is false when r holds no such value.
func valueAt(r Response, p Path) (string, bool) {
	switch p {
	case statusPath:
		return strconv.Itoa(r.Status), r.Status != 0
	case errorCodePath:
		return errorCode(r.Headers, readBody(r).doc)
	case errorMessagePath:
		return errorMessage(readBody(r).doc)
	case bodyPath:
		b := readBody(r)
		return string(b.raw), b.known
	case errorPath, headerPath:
		// Groups, not leaves: the scenario loader refuses them here.
		return "", false
	}
	if name, ok := strings.CutPrefix(string(p), string(headerPath)+"/"); ok {
		return firstHeader(r.Headers, name)
	}
	tokens, ok := bodyTokens(p)
	if !ok {
		return "", false
	}
	switch d := readBody(r).doc.(type) {
	case jsonDocument:
		v, ok := d.root.at(tokens)
		return v.text, ok && (v.kind == jsonString || v.kind == jsonNumber)
	case xmlDocument:
		return d.root.valueAt(tokens)
	case textDocument, nil:
	}
	return "", false
}

// at is the value reached from v through the RFC 6901 tokens: an object's
// first member of each name, an array's element at each index.
func (v jsonNode) at(tokens []string) (jsonNode, bool) {
	for _, tok := range tokens {
		switch v.kind {
		case jsonObject:
			name := memberName(tok)
			i := slices.IndexFunc(v.members, func(m jsonMember) bool { return m.name == name })
			if i < 0 {
				return jsonNode{}, false
			}
			v = v.members[i].value
		case jsonArray:
			i, err := strconv.Atoi(tok)
			if !isIndex(tok) || err != nil || i >= len(v.elems) {
				return jsonNode{}, false
			}
			v = v.elems[i]
		case jsonNull, jsonBool, jsonString, jsonNumber:
			return jsonNode{}, false
		}
	}
	return v, true
}

// valueAt is the text of the element, or the value of the attribute, that
// tokens name; the first token names n, the root.
func (n xmlNode) valueAt(tokens []string) (string, bool) {
	root, ok := parseXMLStep(tokens[0])
	if !ok || root.attr || root.k != 0 || root.name != n.name {
		return "", false
	}
	last := len(tokens) - 1
	for j, tok := range tokens[1:] {
		s, ok := parseXMLStep(tok)
		if !ok {
			return "", false
		}
		if s.attr {
			i := slices.IndexFunc(n.attrs, func(a xmlAttr) bool { return a.name == s.name })
			if i < 0 || j+1 != last {
				return "", false
			}
			return n.attrs[i].value, true
		}
		i := s.child(n.children)
		if i < 0 {
			return "", false
		}
		n = n.children[i]
	}
	return n.text, true
}
