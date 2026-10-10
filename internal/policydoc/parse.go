package policydoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// ParseProblem says which IAM parsing stage rejected a document.
type ParseProblem uint8

// ParseProblem values.
const (
	// Syntax is a document that is not a policy: bad JSON, an unknown
	// element, a value of the wrong type.
	Syntax ParseProblem = iota + 1
	// Legacy is a document that passes the syntax check but not IAM's
	// legacy parser: an Effect in the wrong case.
	Legacy
)

// ParseError reports a document Parse rejects. Message is the text IAM puts
// in a MalformedPolicyDocument error; Detail says what failed and where.
type ParseError struct {
	Problem ParseProblem
	Detail  string
}

// Error describes why the document could not be parsed.
func (e *ParseError) Error() string {
	return "policy document rejected: " + e.Detail + " (IAM reports \"" + e.Message() + "\")"
}

// Message returns the MalformedPolicyDocument text.
func (e *ParseError) Message() string {
	switch e.Problem {
	case Syntax:
		return "Syntax errors in policy."
	case Legacy:
		return "The policy failed legacy parsing"
	default:
		return fmt.Sprintf("ParseProblem(%d)", uint8(e.Problem))
	}
}

// Parse decodes a policy document. It checks the grammar the way IAM's
// syntax stage does: the whole document first, so a syntax error anywhere
// outranks a legacy error. Rules that depend on what the policy is for
// (version, required actions and resources) belong to ValidateIdentity and
// ValidateTrust.
func Parse(b []byte) (Document, *ParseError) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return Document{}, syntaxf("the document is not valid JSON: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Document{}, syntaxf("the document has content after its JSON value")
	}
	var p parser
	doc, err := p.document(v)
	if err != nil {
		return Document{}, err
	}
	if p.legacy != nil {
		return Document{}, p.legacy
	}
	return doc, nil
}

func syntaxf(format string, args ...any) *ParseError {
	return &ParseError{Problem: Syntax, Detail: fmt.Sprintf(format, args...)}
}

// parser holds the first legacy problem while the syntax check goes on.
type parser struct {
	legacy *ParseError
}

func (p *parser) document(v any) (Document, *ParseError) {
	obj, ok := v.(map[string]any)
	if !ok {
		return Document{}, syntaxf("the document is %s, not an object", kind(v))
	}
	if k, ok := unknownKey(obj, "Version", "Id", "Statement"); ok {
		return Document{}, syntaxf("%q is not a policy element; the elements are Version, Id and Statement", k)
	}
	var doc Document
	if raw, ok := obj["Version"]; ok {
		switch raw {
		case "2008-10-17":
			doc.Version = Version20081017
		case "2012-10-17":
			doc.Version = Version20121017
		default:
			return Document{}, syntaxf("Version is %s; it must be \"2012-10-17\" or \"2008-10-17\"", describe(raw))
		}
	}
	if raw, ok := obj["Id"]; ok {
		s, ok := raw.(string)
		if !ok {
			return Document{}, syntaxf("Id is %s, not a string", kind(raw))
		}
		doc.ID = s
	}
	raw, ok := obj["Statement"]
	if !ok {
		return Document{}, syntaxf("the document has no Statement")
	}
	var items []any
	switch s := raw.(type) {
	case map[string]any:
		items = []any{s}
	case []any:
		items = s
	default:
		return Document{}, syntaxf("Statement is %s, not an object or a list", kind(raw))
	}
	if len(items) == 0 {
		return Document{}, syntaxf("Statement is an empty list")
	}
	doc.Statements = make([]Statement, 0, len(items))
	for i, item := range items {
		st, err := p.statement(fmt.Sprintf("Statement[%d]", i), item)
		if err != nil {
			return Document{}, err
		}
		doc.Statements = append(doc.Statements, st)
	}
	return doc, nil
}

func (p *parser) statement(at string, v any) (Statement, *ParseError) {
	obj, ok := v.(map[string]any)
	if !ok {
		return Statement{}, syntaxf("%s is %s, not an object", at, kind(v))
	}
	if k, ok := unknownKey(obj, "Sid", "Effect", "Principal", "NotPrincipal", "Action", "NotAction", "Resource", "NotResource", "Condition"); ok {
		return Statement{}, syntaxf("%s has %q, which is not a statement element", at, k)
	}
	var st Statement
	if raw, ok := obj["Sid"]; ok {
		s, ok := raw.(string)
		if !ok {
			return Statement{}, syntaxf("%s.Sid is %s, not a string", at, kind(raw))
		}
		st.Sid = s
	}
	effect, err := p.effect(at, obj)
	if err != nil {
		return Statement{}, err
	}
	st.Effect = effect
	if st.Principal, err = principalElement(at, obj); err != nil {
		return Statement{}, err
	}
	pats, not, err := either(at, obj, "Action", "NotAction")
	switch {
	case err != nil:
		return Statement{}, err
	case pats == nil:
	case not:
		st.Action = NotAction(pats)
	default:
		st.Action = Action(pats)
	}
	pats, not, err = either(at, obj, "Resource", "NotResource")
	switch {
	case err != nil:
		return Statement{}, err
	case pats == nil:
	case not:
		st.Resource = NotResource(pats)
	default:
		st.Resource = Resource(pats)
	}
	if raw, ok := obj["Condition"]; ok {
		if st.Condition, err = condition(at+".Condition", raw); err != nil {
			return Statement{}, err
		}
	}
	return st, nil
}

// effect accepts Allow and Deny in any case, as IAM's syntax stage does, and
// records any case other than the exact one as a legacy problem.
func (p *parser) effect(at string, obj map[string]any) (Effect, *ParseError) {
	raw, ok := obj["Effect"]
	if !ok {
		return 0, syntaxf("%s has no Effect", at)
	}
	s, ok := raw.(string)
	if !ok {
		return 0, syntaxf("%s.Effect is %s, not a string", at, kind(raw))
	}
	for _, e := range []Effect{Allow, Deny} {
		if !strings.EqualFold(s, e.String()) {
			continue
		}
		if s != e.String() && p.legacy == nil {
			p.legacy = &ParseError{Problem: Legacy, Detail: fmt.Sprintf("%s.Effect is %q; IAM's legacy parser accepts only %q", at, s, e.String())}
		}
		return e, nil
	}
	return 0, syntaxf("%s.Effect is %q; it must be Allow or Deny", at, s)
}

// either reads the element named pos or its negation neg, which may not
// both be present. A nil list means neither is.
func either(at string, obj map[string]any, pos, neg string) (values []string, not bool, err *ParseError) {
	rawPos, hasPos := obj[pos]
	rawNeg, hasNeg := obj[neg]
	switch {
	case hasPos && hasNeg:
		return nil, false, syntaxf("%s has both %s and %s", at, pos, neg)
	case hasPos:
		values, err = stringOrList(at+"."+pos, rawPos)
		return values, false, err
	case hasNeg:
		values, err = stringOrList(at+"."+neg, rawNeg)
		return values, true, err
	default:
		return nil, false, nil
	}
}

// stringOrList reads a string or a list of strings into a non-nil slice.
func stringOrList(at string, v any) ([]string, *ParseError) {
	switch x := v.(type) {
	case string:
		return []string{x}, nil
	case []any:
		out := make([]string, 0, len(x))
		for i, item := range x {
			s, ok := item.(string)
			if !ok {
				return nil, syntaxf("%s[%d] is %s, not a string", at, i, kind(item))
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, syntaxf("%s is %s, not a string or a list of strings", at, kind(v))
	}
}

func principalElement(at string, obj map[string]any) (PrincipalElement, *ParseError) {
	rawPos, hasPos := obj["Principal"]
	rawNeg, hasNeg := obj["NotPrincipal"]
	switch {
	case hasPos && hasNeg:
		return nil, syntaxf("%s has both Principal and NotPrincipal", at)
	case hasPos:
		who, err := principal(at+".Principal", rawPos)
		if err != nil {
			return nil, err
		}
		return Principal{Who: who}, nil
	case hasNeg:
		who, err := principal(at+".NotPrincipal", rawNeg)
		if err != nil {
			return nil, err
		}
		return NotPrincipal{Who: who}, nil
	default:
		return nil, nil
	}
}

func principal(at string, v any) (Who, *ParseError) {
	switch x := v.(type) {
	case string:
		if x != "*" {
			return nil, syntaxf("%s is %q; the only string form is \"*\"", at, x)
		}
		return Anyone{}, nil
	case map[string]any:
		if len(x) == 0 {
			return nil, syntaxf("%s is an empty object", at)
		}
		if k, ok := unknownKey(x, "AWS", "CanonicalUser", "Federated", "Service"); ok {
			return nil, syntaxf("%s has %q; the principal types are AWS, CanonicalUser, Federated and Service", at, k)
		}
		var n Named
		for _, f := range []struct {
			key string
			dst *[]string
		}{{"AWS", &n.AWS}, {"CanonicalUser", &n.CanonicalUser}, {"Federated", &n.Federated}, {"Service", &n.Service}} {
			raw, ok := x[f.key]
			if !ok {
				continue
			}
			values, err := stringOrList(at+"."+f.key, raw)
			if err != nil {
				return nil, err
			}
			if len(values) == 0 {
				return nil, syntaxf("%s.%s is an empty list", at, f.key)
			}
			*f.dst = values
		}
		return n, nil
	default:
		return nil, syntaxf("%s is %s, not \"*\" or an object", at, kind(v))
	}
}

// condition reads the operator blocks into sorted clauses. An empty block
// contributes no clause, and IAM accepts an unknown operator only with an
// empty block.
func condition(at string, v any) (Condition, *ParseError) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, syntaxf("%s is %s, not an object", at, kind(v))
	}
	var c Condition
	for _, name := range slices.Sorted(maps.Keys(obj)) {
		rawBlock := obj[name]
		block, ok := rawBlock.(map[string]any)
		if !ok {
			return nil, syntaxf("%s.%s is %s, not an object", at, name, kind(rawBlock))
		}
		op, known := parseOperator(name)
		if len(block) == 0 {
			continue
		}
		if !known {
			return nil, syntaxf("%s has the operator %q, which IAM does not know", at, name)
		}
		for _, key := range slices.Sorted(maps.Keys(block)) {
			raw := block[key]
			if key == "" {
				return nil, syntaxf("%s.%s has an empty condition key", at, name)
			}
			values, err := conditionValues(fmt.Sprintf("%s.%s.%s", at, name, key), raw)
			if err != nil {
				return nil, err
			}
			c = append(c, Clause{Operator: op, Key: key, Values: values})
		}
	}
	slices.SortFunc(c, compareClauses)
	return c, nil
}

func compareClauses(a, b Clause) int {
	if c := strings.Compare(a.Operator.String(), b.Operator.String()); c != 0 {
		return c
	}
	return strings.Compare(a.Key, b.Key)
}

// parseOperator splits an operator name into its qualifier, test and
// IfExists suffix.
func parseOperator(name string) (Operator, bool) {
	var op Operator
	rest := name
	if r, ok := strings.CutPrefix(rest, "ForAnyValue:"); ok {
		op.Set, rest = ForAnyValue, r
	} else if r, ok := strings.CutPrefix(rest, "ForAllValues:"); ok {
		op.Set, rest = ForAllValues, r
	}
	test, ok := parseTest(rest)
	if !ok {
		base, cut := strings.CutSuffix(rest, "IfExists")
		if !cut {
			return Operator{}, false
		}
		if test, ok = parseTest(base); !ok {
			return Operator{}, false
		}
		op.IfExists = true
	}
	op.Test = test
	return op, true
}

func conditionValues(at string, v any) ([]Value, *ParseError) {
	if items, ok := v.([]any); ok {
		out := make([]Value, 0, len(items))
		for i, item := range items {
			value, err := conditionValue(fmt.Sprintf("%s[%d]", at, i), item)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	}
	value, err := conditionValue(at, v)
	if err != nil {
		return nil, err
	}
	return []Value{value}, nil
}

func conditionValue(at string, v any) (Value, *ParseError) {
	switch x := v.(type) {
	case string:
		return StringValue(x), nil
	case bool:
		return BoolValue(x), nil
	case json.Number:
		return NumberValue(x.String()), nil
	default:
		return nil, syntaxf("%s is %s, not a string, number or boolean", at, kind(v))
	}
}

// unknownKey returns the least key of obj, in sort order, that is not in
// known.
func unknownKey(obj map[string]any, known ...string) (string, bool) {
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if !slices.Contains(known, k) {
			return k, true
		}
	}
	return "", false
}

// kind names the JSON type of a decoded value.
func kind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case json.Number:
		return "a number"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	default:
		return fmt.Sprintf("a %T", v)
	}
}

// describe quotes a string value and names any other kind.
func describe(v any) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return kind(v)
}
