package logsfilter

import (
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

// Quotas on JSON and space-delimited patterns (syntax page).
const (
	maxRegexPerPattern       = 2
	maxWildcardsPerSelector  = 1
	maxWildcardsPerJSONQuery = 3
)

// jsonPattern is a { ... } pattern (rule N5).
type jsonPattern struct{ expr jsonExpr }

// jsonExpr is a compound JSON condition; && binds tighter than ||.
//
//sumtype:decl
type jsonExpr interface{ isJSONExpr() }

type jsonOr struct{ left, right jsonExpr }

type jsonAnd struct{ left, right jsonExpr }

type jsonCond struct {
	sel  selector
	test jsonTest
}

func (jsonOr) isJSONExpr()   {}
func (jsonAnd) isJSONExpr()  {}
func (jsonCond) isJSONExpr() {}

// jsonTest is what a condition asks of the nodes its selector reaches.
//
//sumtype:decl
type jsonTest interface{ isJSONTest() }

// compareTest holds when any reached scalar satisfies the condition.
type compareTest struct{ cond condition }

// isTest is IS NULL, IS TRUE or IS FALSE.
type isTest struct{ want isLiteral }

// isLiteral is the literal an IS test names.
type isLiteral uint8

const (
	isNull isLiteral = iota
	isTrue
	isFalse
)

// holds reports whether the scalar is the literal.
func (l isLiteral) holds(sub subject) bool {
	switch l {
	case isNull:
		_, ok := sub.(nullNode)
		return ok
	case isTrue:
		return sub == boolNode(true)
	case isFalse:
		return sub == boolNode(false)
	}
	return false
}

// notExistsTest holds when the selector reaches nothing; a JSON null exists.
type notExistsTest struct{}

func (compareTest) isJSONTest()   {}
func (isTest) isJSONTest()        {}
func (notExistsTest) isJSONTest() {}

// selector is $ followed by its steps.
type selector []step

// step moves from each node to zero or more child nodes.
//
//sumtype:decl
type step interface{ isStep() }

type keyStep struct{ key string }

// anyKeyStep is .* over an object's values.
type anyKeyStep struct{}

type indexStep struct{ index int }

// anyIndexStep is [*] over an array's elements.
type anyIndexStep struct{}

func (keyStep) isStep()      {}
func (anyKeyStep) isStep()   {}
func (indexStep) isStep()    {}
func (anyIndexStep) isStep() {}

type jsonParser struct {
	s         *scanner
	regexes   int
	wildcards int
}

func parseJSON(s *scanner) (jsonPattern, *Error) {
	s.pos++
	p := jsonParser{s: s}
	expr, err := p.or()
	if err != nil {
		return jsonPattern{}, err
	}
	s.skipSpace()
	if !s.accept("}") {
		return jsonPattern{}, s.fail("expected && or || or the closing }")
	}
	s.skipSpace()
	if !s.eof() {
		return jsonPattern{}, s.fail("unexpected text after the closing }")
	}
	return jsonPattern{expr: expr}, nil
}

func (p *jsonParser) or() (jsonExpr, *Error) {
	left, err := p.and()
	for err == nil {
		p.s.skipSpace()
		if !p.s.accept("||") {
			return left, nil
		}
		var right jsonExpr
		right, err = p.and()
		left = jsonOr{left: left, right: right}
	}
	return nil, err
}

func (p *jsonParser) and() (jsonExpr, *Error) {
	left, err := p.primary()
	for err == nil {
		p.s.skipSpace()
		if !p.s.accept("&&") {
			return left, nil
		}
		var right jsonExpr
		right, err = p.primary()
		left = jsonAnd{left: left, right: right}
	}
	return nil, err
}

func (p *jsonParser) primary() (jsonExpr, *Error) {
	p.s.skipSpace()
	if !p.s.accept("(") {
		return p.condition()
	}
	expr, err := p.or()
	if err != nil {
		return nil, err
	}
	p.s.skipSpace()
	if !p.s.accept(")") {
		return nil, p.s.fail("expected )")
	}
	return expr, nil
}

func (p *jsonParser) condition() (jsonExpr, *Error) {
	sel, err := p.selector()
	if err != nil {
		return nil, err
	}
	p.s.skipSpace()
	test, err := p.test()
	if err != nil {
		return nil, err
	}
	return jsonCond{sel: sel, test: test}, nil
}

func (p *jsonParser) test() (jsonTest, *Error) {
	s := p.s
	start := s.pos
	switch {
	case s.acceptWord("IS"):
		s.skipSpace()
		switch {
		case s.acceptWord("NULL"):
			return isTest{want: isNull}, nil
		case s.acceptWord("TRUE"):
			return isTest{want: isTrue}, nil
		case s.acceptWord("FALSE"):
			return isTest{want: isFalse}, nil
		case s.acceptWord("NOT"):
			return nil, invalidPattern("IS NOT is not supported; use != or NOT EXISTS", start)
		}
		return nil, s.fail("expected NULL, TRUE or FALSE after IS")
	case s.acceptWord("NOT"):
		s.skipSpace()
		if !s.acceptWord("EXISTS") {
			return nil, s.fail("expected EXISTS after NOT")
		}
		return notExistsTest{}, nil
	case s.acceptWord("EXISTS"):
		return nil, invalidPattern("EXISTS is not supported; use = * or NOT EXISTS", start)
	}
	o, ok := s.op()
	if !ok {
		return nil, s.fail("expected a comparison operator, IS or NOT EXISTS")
	}
	s.skipSpace()
	v, err := s.value()
	if err != nil {
		return nil, err
	}
	if err := countRegex(v, &p.regexes, start); err != nil {
		return nil, err
	}
	cond, err := newCondition(o, v, start)
	if err != nil {
		return nil, err
	}
	return compareTest{cond: cond}, nil
}

func (p *jsonParser) selector() (selector, *Error) {
	s := p.s
	start := s.pos
	if !s.accept("$") {
		return nil, s.fail("expected a selector starting with $")
	}
	var sel selector
	wildcards := 0
	for {
		st, ok, err := p.step()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		switch st.(type) {
		case anyKeyStep, anyIndexStep:
			wildcards++
		case keyStep, indexStep:
		}
		sel = append(sel, st)
	}
	switch {
	case len(sel) == 0:
		return nil, s.fail("selector needs a property or index after $")
	case wildcards > maxWildcardsPerSelector:
		return nil, invalidPattern("a selector may hold at most 1 wildcard", start)
	}
	if p.wildcards += wildcards; p.wildcards > maxWildcardsPerJSONQuery {
		return nil, invalidPattern("a pattern may hold at most 3 wildcard selectors", start)
	}
	return sel, nil
}

// step reads one selector step; ok is false when no step follows.
func (p *jsonParser) step() (st step, ok bool, err *Error) {
	s := p.s
	switch {
	case s.accept(".["):
		key, err := p.bracketKey()
		return keyStep{key: key}, err == nil, err
	case s.accept(".*"):
		return anyKeyStep{}, true, nil
	case s.accept("."):
		key := s.name()
		if key == "" {
			return nil, false, s.fail("expected a property name after .")
		}
		return keyStep{key: key}, true, nil
	case s.accept("[*]"):
		return anyIndexStep{}, true, nil
	case s.accept("["):
		start := s.pos
		index, convErr := strconv.Atoi(s.src[start:digits(s.src, start)])
		s.pos = digits(s.src, start)
		if convErr != nil || !s.accept("]") {
			return nil, false, invalidPattern("expected an array index or * inside []", start)
		}
		return indexStep{index: index}, true, nil
	}
	return nil, false, nil
}

// bracketKey reads 'key'] after .[; double quotes are a syntax error.
func (p *jsonParser) bracketKey() (string, *Error) {
	s := p.s
	if !s.accept("'") {
		return "", s.fail("bracket notation takes a single-quoted property name")
	}
	end := strings.IndexByte(s.src[s.pos:], '\'')
	if end < 0 {
		return "", s.fail("unterminated property name")
	}
	key := s.src[s.pos : s.pos+end]
	s.pos += end + 1
	if !s.accept("]") {
		return "", s.fail("expected ] after the property name")
	}
	return key, nil
}

// match parses the message as one JSON value; a message that is not JSON
// never matches.
func (p jsonPattern) match(message string, _ Options) bool {
	dec := json.NewDecoder(strings.NewReader(message))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return false
	}
	return eval(p.expr, root)
}

func eval(e jsonExpr, root any) bool {
	switch e := e.(type) {
	case jsonOr:
		return eval(e.left, root) || eval(e.right, root)
	case jsonAnd:
		return eval(e.left, root) && eval(e.right, root)
	case jsonCond:
		return holdsTest(e.test, e.sel.reach(root))
	}
	return false
}

func (sel selector) reach(root any) []any {
	nodes := []any{root}
	for _, st := range sel {
		var next []any
		for _, n := range nodes {
			next = appendChildren(next, st, n)
		}
		nodes = next
	}
	return nodes
}

func appendChildren(out []any, st step, n any) []any {
	switch st := st.(type) {
	case keyStep:
		if obj, ok := n.(map[string]any); ok {
			if child, ok := obj[st.key]; ok {
				out = append(out, child)
			}
		}
	case anyKeyStep:
		if obj, ok := n.(map[string]any); ok {
			for _, child := range obj {
				out = append(out, child)
			}
		}
	case indexStep:
		if arr, ok := n.([]any); ok && st.index < len(arr) {
			out = append(out, arr[st.index])
		}
	case anyIndexStep:
		if arr, ok := n.([]any); ok {
			out = append(out, arr...)
		}
	}
	return out
}

func holdsTest(t jsonTest, nodes []any) bool {
	switch t := t.(type) {
	case notExistsTest:
		return len(nodes) == 0
	case isTest:
		for _, n := range nodes {
			if sub, ok := scalar(n); ok && t.want.holds(sub) {
				return true
			}
		}
	case compareTest:
		for _, n := range nodes {
			if sub, ok := scalar(n); ok && t.cond.holds(sub) {
				return true
			}
		}
	}
	return false
}

// scalar converts a decoded node; objects and arrays never satisfy a test.
func scalar(n any) (subject, bool) {
	switch v := n.(type) {
	case nil:
		return nullNode{}, true
	case string:
		return stringNode(v), true
	case json.Number:
		return numberNode(v), true
	case bool:
		return boolNode(v), true
	}
	return nil, false
}
