package intrinsic

import (
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/jsonpath"
)

// arg is one argument of a call (stepfunctions-asl.md 5.1).
//
//sumtype:decl
type arg interface{ isArg() }

// stringArg is a quoted string. raw is the body as written, which keeps
// the escapes States.Format needs to tell \{ from a placeholder.
type stringArg struct {
	value, raw string
}

// literalArg is a number, true, false or null.
type literalArg struct{ value document.Value }

// pathArg is a "$" or "$$" path, read with the Plain grammar.
type pathArg struct{ path jsonpath.Path }

// callArg is a nested call.
type callArg struct{ call Call }

func (stringArg) isArg()  {}
func (literalArg) isArg() {}
func (pathArg) isArg()    {}
func (callArg) isArg()    {}

// arity is a function's argument count: min, and max (-1 for any).
type arity struct{ min, max int }

// functions are the 18 functions of stepfunctions-asl.md 5.2 and their
// argument counts.
func functions() map[string]arity {
	return map[string]arity{
		"States.Format":         {1, -1},
		"States.StringToJson":   {1, 1},
		"States.JsonToString":   {1, 1},
		"States.Array":          {0, -1},
		"States.ArrayPartition": {2, 2},
		"States.ArrayContains":  {2, 2},
		"States.ArrayRange":     {3, 3},
		"States.ArrayGetItem":   {2, 2},
		"States.ArrayLength":    {1, 1},
		"States.ArrayUnique":    {1, 1},
		"States.Base64Encode":   {1, 1},
		"States.Base64Decode":   {1, 1},
		"States.Hash":           {2, 2},
		"States.JsonMerge":      {3, 3},
		"States.MathRandom":     {2, 3},
		"States.MathAdd":        {2, 2},
		"States.StringSplit":    {2, 2},
		"States.UUID":           {0, 0},
	}
}

// parser reads one call expression.
type parser struct {
	src   string
	pos   int
	known map[string]arity
}

func parse(s string) (Call, error) {
	p := &parser{src: s, known: functions()}
	c, err := p.call(1)
	if err != nil {
		return Call{}, err
	}
	p.space()
	if p.pos != len(p.src) {
		return Call{}, p.syntax("unexpected text after the call at offset "+strconv.Itoa(p.pos), nil)
	}
	c.text = s
	return c, nil
}

func (p *parser) syntax(reason string, err error) error {
	return &SyntaxError{Expr: p.src, Reason: reason, Err: err}
}

func (p *parser) peek() byte {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

func (p *parser) space() {
	for p.peek() == ' ' {
		p.pos++
	}
}

// call reads Name(args) at nesting depth.
func (p *parser) call(depth int) (Call, error) {
	if depth > MaxDepth {
		return Call{}, p.syntax("calls nest at most "+strconv.Itoa(MaxDepth)+" levels", nil)
	}
	start := p.pos
	for p.pos < len(p.src) && nameChar(p.src[p.pos]) {
		p.pos++
	}
	name := p.src[start:p.pos]
	if !strings.HasPrefix(name, "States.") {
		return Call{}, p.syntax("a function name starts with States.", nil)
	}
	ar, ok := p.known[name]
	if !ok {
		return Call{}, p.syntax("unknown function "+name, nil)
	}
	if p.peek() != '(' {
		return Call{}, p.syntax("'(' expected after "+name, nil)
	}
	p.pos++
	c := Call{function: name}
	p.space()
	if p.peek() == ')' {
		p.pos++
	} else {
		args, err := p.args(depth)
		if err != nil {
			return Call{}, err
		}
		c.args = args
	}
	if len(c.args) < ar.min || ar.max >= 0 && len(c.args) > ar.max {
		return Call{}, p.syntax(name+" takes "+arityText(ar)+", not "+strconv.Itoa(len(c.args)), nil)
	}
	return c, nil
}

// args reads arguments up to and including ")".
func (p *parser) args(depth int) ([]arg, error) {
	var out []arg
	for {
		p.space()
		a, err := p.arg(depth)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
		p.space()
		switch p.peek() {
		case ',':
			p.pos++
		case ')':
			p.pos++
			return out, nil
		default:
			return nil, p.syntax("',' or ')' expected at offset "+strconv.Itoa(p.pos), nil)
		}
	}
}

func arityText(a arity) string {
	switch {
	case a.max < 0:
		return "at least " + strconv.Itoa(a.min) + " arguments"
	case a.min == a.max:
		return strconv.Itoa(a.min) + " arguments"
	default:
		return strconv.Itoa(a.min) + " to " + strconv.Itoa(a.max) + " arguments"
	}
}

func nameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_'
}

func (p *parser) arg(depth int) (arg, error) {
	c := p.peek()
	switch {
	case c == '\'':
		return p.str()
	case c == '$':
		return p.path()
	case strings.HasPrefix(p.src[p.pos:], "States."):
		call, err := p.call(depth + 1)
		return callArg{call: call}, err
	}
	end := p.pos
	for end < len(p.src) && p.src[end] != ',' && p.src[end] != ')' && p.src[end] != ' ' {
		end++
	}
	word := p.src[p.pos:end]
	var v document.Value
	switch word {
	case "null":
		v = document.Null{}
	case "true":
		v = document.Bool(true)
	case "false":
		v = document.Bool(false)
	default:
		n, err := document.ParseNumber(word)
		if err != nil {
			return nil, p.syntax("an argument is a string, a number, true, false, null, a path or a call, not '"+word+"'", nil)
		}
		v = n
	}
	p.pos = end
	return literalArg{value: v}, nil
}

// str reads a quoted string: \', \{, \} and \\ are the escapes; any other
// backslash is an error (stepfunctions-asl.md 5.1, 3.5).
func (p *parser) str() (arg, error) {
	p.pos++
	start := p.pos
	var b strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch c {
		case '\'':
			raw := p.src[start:p.pos]
			p.pos++
			return stringArg{value: b.String(), raw: raw}, nil
		case '\\':
			next := byte(0)
			if p.pos+1 < len(p.src) {
				next = p.src[p.pos+1]
			}
			if strings.IndexByte(`'{}\`, next) < 0 || next == 0 {
				return nil, p.syntax("a backslash in a string escapes only ', {, } or \\", nil)
			}
			b.WriteByte(next)
			p.pos += 2
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
	return nil, p.syntax("unterminated string", nil)
}

// path reads a path argument up to the "," or ")" that ends it, outside
// brackets and quotes.
func (p *parser) path() (arg, error) {
	end := pathEnd(p.src, p.pos)
	text := strings.TrimRight(p.src[p.pos:end], " ")
	path, err := jsonpath.Parse(text, jsonpath.Plain)
	if err != nil {
		return nil, p.syntax("the path argument '"+text+"' does not parse", err)
	}
	p.pos = end
	return pathArg{path: path}, nil
}

// pathEnd is the offset of the "," or ")" that ends the path at start.
func pathEnd(s string, start int) int {
	depth := 0
	var quote byte
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			switch c {
			case '\\':
				i++
			case quote:
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '[' || c == '(':
			depth++
		case c == ']' || c == ')' && depth > 0:
			depth--
		case depth == 0 && (c == ',' || c == ')'):
			return i
		}
	}
	return len(s)
}
