package ruler

// anythingBut compiles {"anything-but": ...} in all its forms: a string, a
// number, a list of strings or of numbers, or an object naming prefix,
// suffix, equals-ignore-case or wildcard with a string or a list of strings.
// The messages are Ruler's (JsonRuleCompiler.processMatchExpression).
func (c *compiler) anythingBut() (matcher, *CompileError) {
	v, e := c.next()
	if e != nil {
		return nil, e
	}
	if v.kind != tokStartObject {
		if e := anythingButValueShape(v); e != nil {
			return nil, e
		}
		m, e := c.anythingButPlain(v)
		if e != nil {
			return nil, e
		}
		if e := c.end(OperatorAnythingBut, tooManyElements); e != nil {
			return nil, e
		}
		return m, nil
	}
	t, e := c.next()
	if e != nil {
		return nil, e
	}
	if t.kind != tokFieldName {
		return nil, operandError(OperatorAnythingBut, "Anything-But expression name not found")
	}
	op, known := anythingButVariant(t.text)
	if !known {
		return nil, operandError(OperatorAnythingBut, "Unsupported anything-but pattern: "+t.text)
	}
	if !c.profile.allows(op) {
		return nil, unsupported(op, "Unsupported anything-but pattern: "+t.text)
	}
	v, e = c.next()
	if e != nil {
		return nil, e
	}
	if e := c.anythingButVariantShape(op, t.text, v); e != nil {
		return nil, e
	}
	m, e := c.anythingButSet(op, v)
	if e != nil {
		return nil, e
	}
	if e := c.end(op, tooManyElements); e != nil {
		return nil, e
	}
	if e := c.end(op, tooManyElements); e != nil {
		return nil, e
	}
	return m, nil
}

func anythingButVariant(name string) (Operator, bool) {
	switch name {
	case "equals-ignore-case":
		return OperatorAnythingButEqualsIgnoreCase, true
	case "prefix":
		return OperatorAnythingButPrefix, true
	case "suffix":
		return OperatorAnythingButSuffix, true
	case "wildcard":
		return OperatorAnythingButWildcard, true
	}
	return 0, false
}

const anythingButShape = "Value of anything-but must be an array or single string/number value."

func anythingButValueShape(v token) *CompileError {
	switch v.kind {
	case tokStartArray, tokString, tokNumber:
		return nil
	case tokEOF, tokStartObject, tokEndObject, tokEndArray, tokFieldName, tokTrue, tokFalse, tokNull:
	}
	return operandError(OperatorAnythingBut, anythingButShape)
}

// anythingButVariantShape checks the operand of an anything-but object form.
// SNS words a non-string operand as "<name> match pattern must be a string"
// (recorded for prefix, sns.md 6.5) where Ruler reports the shape message.
func (c *compiler) anythingButVariantShape(op Operator, name string, v token) *CompileError {
	if _, ok := c.profile.(SNS); ok && v.kind != tokStartArray && v.kind != tokString {
		return operandError(op, name+" match pattern must be a string")
	}
	if anythingButValueShape(v) != nil {
		return operandError(op, anythingButShape)
	}
	return nil
}

// anythingButPlain compiles a string, a number or a list of either.
func (c *compiler) anythingButPlain(v token) (matcher, *CompileError) {
	switch v.kind {
	case tokString:
		s, e := stringText(v)
		if e != nil {
			return nil, e
		}
		return matchAnythingBut{texts: []string{s}}, nil
	case tokNumber:
		f, ok := comparableNumber(v.text)
		if !ok {
			return nil, operandError(OperatorAnythingBut, cannotCompare(v.text))
		}
		return matchAnythingButNumbers{values: []float64{f}}, nil
	case tokEOF, tokStartObject, tokEndObject, tokStartArray, tokEndArray, tokFieldName, tokTrue, tokFalse, tokNull:
	}
	return c.anythingButList()
}

func (c *compiler) anythingButList() (matcher, *CompileError) {
	var texts []string
	var numbers []float64
	for {
		t, e := c.next()
		if e != nil {
			return nil, e
		}
		switch t.kind {
		case tokEndArray:
			if (len(texts) > 0) == (len(numbers) > 0) {
				return nil, operandError(OperatorAnythingBut, "Inside anything but list, either all values are number or string, "+
					"mixed type is not supported")
			}
			if len(numbers) > 0 {
				return matchAnythingButNumbers{values: numbers}, nil
			}
			return matchAnythingBut{texts: texts}, nil
		case tokString:
			s, e := stringText(t)
			if e != nil {
				return nil, e
			}
			texts = append(texts, s)
		case tokNumber:
			f, ok := comparableNumber(t.text)
			if !ok {
				return nil, operandError(OperatorAnythingBut, cannotCompare(t.text))
			}
			numbers = append(numbers, f)
		case tokEOF, tokStartObject, tokEndObject, tokStartArray, tokFieldName, tokTrue, tokFalse, tokNull:
			return nil, operandError(OperatorAnythingBut, "Inside anything but list, start|null|boolean is not supported.")
		}
	}
}

// anythingButSet compiles the string or list operand of an object form.
func (c *compiler) anythingButSet(op Operator, v token) (matcher, *CompileError) {
	if v.kind != tokStartArray {
		s, e := anythingButSetMember(op, v)
		if e != nil {
			return nil, e
		}
		return anythingButSetMatcher(op, []string{s})
	}
	var values []string
	for {
		t, e := c.next()
		if e != nil {
			return nil, e
		}
		if t.kind == tokEndArray {
			return anythingButSetMatcher(op, values)
		}
		s, e := anythingButSetMember(op, t)
		if e != nil {
			return nil, e
		}
		values = append(values, s)
	}
}

func anythingButSetMember(op Operator, t token) (string, *CompileError) {
	affix := op == OperatorAnythingButPrefix || op == OperatorAnythingButSuffix
	switch {
	case t.kind == tokString:
		s, e := stringText(t)
		if e == nil && s == "" && affix {
			return "", operandError(op, "Null prefix/suffix not allowed")
		}
		return s, e
	case op == OperatorAnythingButEqualsIgnoreCase:
		return "", operandError(op, "Inside anything-but/equals-ignore-case list, number|start|null|boolean is not supported.")
	case op == OperatorAnythingButWildcard:
		return "", operandError(op, "wildcard match pattern must be a string")
	default:
		return "", operandError(op, "prefix/suffix match pattern must be a string")
	}
}

func anythingButSetMatcher(op Operator, values []string) (matcher, *CompileError) {
	if op == OperatorAnythingButPrefix {
		return matchAnythingButPrefix{prefixes: values}, nil
	}
	if op == OperatorAnythingButSuffix {
		return matchAnythingButSuffix{suffixes: values}, nil
	}
	if op == OperatorAnythingButEqualsIgnoreCase {
		folds := make([]caseless, 0, len(values))
		for _, s := range values {
			folds = append(folds, newCaseless(s))
		}
		return matchAnythingButEqualsIgnoreCase{folds: folds}, nil
	}
	globs := make([]glob, 0, len(values))
	for _, s := range values {
		g, detail := parseGlob(s)
		if detail != "" {
			return nil, operandError(op, detail)
		}
		globs = append(globs, g)
	}
	return matchAnythingButWildcard{patterns: globs}, nil
}
