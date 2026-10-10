package ruler

import "math"

// numeric compiles {"numeric": [...]}: one comparison, or a lower bound (">"
// or ">=") followed by an upper bound ("<" or "<="), following Ruler's
// processNumericMatchExpression and its messages.
func (c *compiler) numeric() (matcher, *CompileError) {
	v, e := c.next()
	if e != nil {
		return nil, e
	}
	if v.kind != tokStartArray {
		return nil, numericError("Value of numeric must be an array.")
	}
	m, e := c.numericRange()
	if e != nil {
		return nil, e
	}
	if e := c.end(OperatorNumeric, tooManyElements); e != nil {
		return nil, e
	}
	return m, nil
}

const tooManyElements = "Too many elements in numeric expression"

func (c *compiler) numericRange() (matcher, *CompileError) {
	t, e := c.next()
	if e != nil {
		return nil, e
	}
	if t.kind != tokString {
		return nil, numericError("Invalid member in numeric match: " + t.display())
	}
	op, e := stringText(t)
	if e != nil {
		return nil, e
	}
	operand, e := c.next()
	if e != nil {
		return nil, e
	}
	switch op {
	case "=":
		return c.numericEquals(operand)
	case ">=", ">":
		if operand.kind != tokNumber {
			return nil, numericError("Value of " + op + " must be numeric")
		}
		return c.numericLower(operand.text, op == ">=")
	case "<=", "<":
		if operand.kind != tokNumber {
			return nil, numericError("Value of " + op + " must be numeric")
		}
		if e := c.arrayEnd(tooManyElements); e != nil {
			return nil, e
		}
		high, e := ruleNumber(operand.text)
		if e != nil {
			return nil, e
		}
		return matchNumeric{low: math.Inf(-1), high: high, highInclusive: op == "<="}, nil
	}
	return nil, numericError("Unrecognized numeric range operator: " + op)
}

func (c *compiler) numericEquals(operand token) (matcher, *CompileError) {
	if operand.kind != tokNumber {
		return nil, numericError("Value of equals must be numeric")
	}
	if e := c.arrayEnd(tooManyElements); e != nil {
		return nil, e
	}
	f, e := ruleNumber(operand.text)
	if e != nil {
		return nil, e
	}
	return matchNumeric{low: f, high: f, lowInclusive: true, highInclusive: true}, nil
}

// numericLower reads what follows a lower bound: the end of the array, or an
// upper bound that completes a range.
func (c *compiler) numericLower(bottom string, inclusive bool) (matcher, *CompileError) {
	t, e := c.next()
	if e != nil {
		return nil, e
	}
	if t.kind == tokEndArray {
		low, e := ruleNumber(bottom)
		if e != nil {
			return nil, e
		}
		return matchNumeric{low: low, high: math.Inf(1), lowInclusive: inclusive}, nil
	}
	if t.kind != tokString {
		return nil, numericError("Bad value in numeric range: " + t.display())
	}
	op, e := stringText(t)
	if e != nil {
		return nil, e
	}
	if op != "<" && op != "<=" {
		return nil, numericError("Bad numeric range operator: " + op)
	}
	top, e := c.next()
	if e != nil {
		return nil, e
	}
	if top.kind != tokNumber {
		return nil, numericError("Value of " + op + " must be numeric")
	}
	if e := c.arrayEnd("Too many terms in numeric range expression"); e != nil {
		return nil, e
	}
	low, e := ruleNumber(bottom)
	if e != nil {
		return nil, e
	}
	high, e := ruleNumber(top.text)
	if e != nil {
		return nil, e
	}
	if low >= high {
		return nil, numericError("Bottom must be less than top")
	}
	return matchNumeric{low: low, high: high, lowInclusive: inclusive, highInclusive: op == "<="}, nil
}

// arrayEnd consumes the ']' closing a numeric expression.
func (c *compiler) arrayEnd(detail string) *CompileError {
	t, e := c.next()
	if e != nil {
		return e
	}
	if t.kind != tokEndArray {
		return numericError(detail)
	}
	return nil
}

// ruleNumber converts a numeric operand, rejecting numbers Ruler cannot
// compare (V20, V21).
func ruleNumber(text string) (float64, *CompileError) {
	f, ok := comparableNumber(text)
	if !ok {
		return 0, numericError(cannotCompare(text))
	}
	return f, nil
}

func numericError(detail string) *CompileError {
	return operandError(OperatorNumeric, detail)
}
