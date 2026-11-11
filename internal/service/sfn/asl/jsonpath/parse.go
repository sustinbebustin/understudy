package jsonpath

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// step is one selector of a path after its root.
//
//sumtype:decl
type step interface {
	write(b *strings.Builder)
	// selectFrom appends to out what the step selects from v.
	selectFrom(v document.Value, out []document.Value) []document.Value
}

// child selects an object member: .name or ['name'].
type child struct{ name string }

// index selects an array element; negative counts from the end (Plain
// only).
type index struct{ n int }

// wildcard selects every member or element: .* or [*].
type wildcard struct{}

// descend selects what target selects at any depth: ..name, ..*, ..[n].
type descend struct{ target step }

// bound is an optional slice bound.
type bound struct {
	n   int
	set bool
}

// slice selects [start:end:step] of an array.
type slice struct {
	start, end, step bound
}

// union selects several indexes: [0,1].
type union struct{ indexes []int }

// function is a JSONPath function such as .length(): it selects nothing,
// so a definite path through it is missing. AWS accepts $.a.length() at
// create and fails on it at run time, unlike Jayway JsonPath, which has
// length() (ASL-19); other names are inferred to read
// the same.
type function struct {
	text string // the name and argument list as written: length()
}

// filter selects the elements for which @<ref> <op> <literal> holds: one
// comparison only (stepfunctions-asl.md 4.5; ASL-19).
type filter struct {
	ref     []step // child and index steps from @
	op      string // == != < <= > >=
	literal document.Value
}

func (s child) write(b *strings.Builder) {
	if shorthand(s.name) {
		b.WriteString("." + s.name)
		return
	}
	b.WriteString("[" + quote(s.name) + "]")
}

func (s index) write(b *strings.Builder) { b.WriteString("[" + strconv.Itoa(s.n) + "]") }

func (wildcard) write(b *strings.Builder) { b.WriteString("[*]") }

func (s function) write(b *strings.Builder) { b.WriteString("." + s.text) }

func (s descend) write(b *strings.Builder) {
	b.WriteString("..")
	switch t := s.target.(type) {
	case child:
		if shorthand(t.name) {
			b.WriteString(t.name)
			return
		}
		t.write(b)
	case wildcard:
		b.WriteString("*")
	case function:
		b.WriteString(t.text)
	case index, descend, slice, union, filter:
		t.write(b)
	}
}

func (s slice) write(b *strings.Builder) {
	b.WriteString("[")
	writeBound(b, s.start)
	b.WriteString(":")
	writeBound(b, s.end)
	if s.step.set {
		b.WriteString(":")
		writeBound(b, s.step)
	}
	b.WriteString("]")
}

func writeBound(b *strings.Builder, n bound) {
	if n.set {
		b.WriteString(strconv.Itoa(n.n))
	}
}

func (s union) write(b *strings.Builder) {
	parts := make([]string, len(s.indexes))
	for i, n := range s.indexes {
		parts[i] = strconv.Itoa(n)
	}
	b.WriteString("[" + strings.Join(parts, ",") + "]")
}

func (s filter) write(b *strings.Builder) {
	b.WriteString("[?(@")
	for _, r := range s.ref {
		r.write(b)
	}
	b.WriteString(" " + s.op + " ")
	switch v := s.literal.(type) {
	case document.String:
		b.WriteString(quote(string(v)))
	case document.Number:
		b.WriteString(string(v))
	case document.Bool:
		b.WriteString(strconv.FormatBool(bool(v)))
	case document.Null, document.List, document.Map:
		// literal reads no list or object.
		b.WriteString("null")
	}
	b.WriteString(")]")
}

// shorthand reports whether name prints as .name: letters, digits, _ and -.
func shorthand(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// quote writes s as a single-quoted bracket name, escaping \ and '.
func quote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// dotStop are the characters that end a shorthand name or cannot appear in
// one unescaped.
const dotStop = ".[]()'\"@,:?$* \t\n\r!=<>&|"

// parser reads one path.
type parser struct {
	src  string
	kind Kind
	pos  int
}

func parse(s string, k Kind) (Path, error) {
	p := &parser{src: s, kind: k}
	return p.path()
}

func (p *parser) syntax(reason string) error { return &SyntaxError{Path: p.src, Reason: reason} }

func (p *parser) unsupported(construct string) error {
	if p.kind == Reference {
		return p.syntax("a Reference Path cannot use '" + construct + "'")
	}
	return &UnsupportedError{Path: p.src, Construct: construct}
}

// plainOnly refuses a Plain-only construct in a Reference Path
// (stepfunctions-asl.md 4.5).
func (p *parser) plainOnly(construct string) error {
	if p.kind == Reference {
		return p.syntax("a Reference Path allows only names and non-negative indexes, not " + construct)
	}
	return nil
}

func (p *parser) peek() byte {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

func (p *parser) eat(prefix string) bool {
	if strings.HasPrefix(p.src[p.pos:], prefix) {
		p.pos += len(prefix)
		return true
	}
	return false
}

func (p *parser) path() (Path, error) {
	out := Path{src: p.src}
	if !p.eat("$") {
		return Path{}, p.syntax("a path starts with $")
	}
	out.context = p.eat("$")
	for p.pos < len(p.src) {
		s, err := p.step()
		if err != nil {
			return Path{}, err
		}
		out.steps = append(out.steps, s)
	}
	return out, nil
}

func (p *parser) step() (step, error) {
	switch {
	case p.eat(".."):
		if err := p.plainOnly("recursive descent '..'"); err != nil {
			return nil, err
		}
		var target step
		var err error
		switch {
		case p.eat("*"):
			target = wildcard{}
		case p.peek() == '[':
			p.pos++
			target, err = p.bracket()
		default:
			target, err = p.dotName()
		}
		if err != nil {
			return nil, err
		}
		if _, ok := target.(descend); ok {
			return nil, p.syntax("'...' is not a path step")
		}
		return descend{target: target}, nil
	case p.eat("."):
		if p.eat("*") {
			if err := p.plainOnly("the wildcard '*'"); err != nil {
				return nil, err
			}
			return wildcard{}, nil
		}
		if p.peek() == '[' {
			// $.abc.['def ghi'] (aws-doc dg/amazon-states-language-paths).
			p.pos++
			return p.bracket()
		}
		return p.dotName()
	case p.eat("["):
		return p.bracket()
	}
	return nil, p.syntax("unexpected '" + p.src[p.pos:] + "'")
}

// dotName reads a shorthand name; a backslash escapes the next character
// ($.store\.book, aws-doc dg/amazon-states-language-paths). A name followed
// by "(" is a function such as length(), which a Reference Path refuses.
func (p *parser) dotName() (step, error) {
	var b strings.Builder
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '\\' {
			r, size := utf8.DecodeRuneInString(p.src[p.pos+1:])
			if size == 0 {
				return nil, p.syntax("a backslash ends the path")
			}
			b.WriteRune(r)
			p.pos += 1 + size
			continue
		}
		if strings.IndexByte(dotStop, c) >= 0 {
			break
		}
		r, size := utf8.DecodeRuneInString(p.src[p.pos:])
		b.WriteRune(r)
		p.pos += size
	}
	if b.Len() == 0 {
		return nil, p.syntax("a name is expected at offset " + strconv.Itoa(p.pos))
	}
	if p.peek() == '(' {
		end := matching(p.src, p.pos)
		if end < 0 {
			return nil, p.syntax("unterminated function call")
		}
		text := p.src[start : end+1]
		if p.kind == Reference {
			return nil, p.syntax("a Reference Path cannot use the function '" + text + "'")
		}
		p.pos = end + 1
		return function{text: text}, nil
	}
	return child{name: b.String()}, nil
}

// bracket reads what follows "[" up to and including "]".
func (p *parser) bracket() (step, error) {
	c := p.peek()
	switch {
	case c == '\'' || c == '"':
		name, err := p.quoted(c)
		if err != nil {
			return nil, err
		}
		return child{name: name}, p.close()
	case c == '*':
		p.pos++
		if err := p.plainOnly("the wildcard '*'"); err != nil {
			return nil, err
		}
		return wildcard{}, p.close()
	case c == '?':
		if err := p.plainOnly("a filter"); err != nil {
			return nil, err
		}
		p.pos++
		return p.filter()
	case c == '(':
		end := matching(p.src, p.pos)
		if end < 0 {
			return nil, p.syntax("unterminated filter script")
		}
		return nil, p.unsupported(p.src[p.pos : end+1])
	case c == '-' || c == ':' || c >= '0' && c <= '9':
		return p.indexes()
	}
	return nil, p.syntax("unexpected character in brackets at offset " + strconv.Itoa(p.pos))
}

func (p *parser) close() error {
	if !p.eat("]") {
		return p.syntax("']' expected at offset " + strconv.Itoa(p.pos))
	}
	return nil
}

// quoted reads a quoted name; a backslash takes the next character
// literally ($['Test\\\"\"Name\"'], stepfunctions-asl.md 4.5 recorded).
func (p *parser) quoted(q byte) (string, error) {
	p.pos++
	var b strings.Builder
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch c {
		case q:
			p.pos++
			return b.String(), nil
		case '\\':
			r, size := utf8.DecodeRuneInString(p.src[p.pos+1:])
			if size == 0 {
				return "", p.syntax("unterminated quoted name")
			}
			b.WriteRune(r)
			p.pos += 1 + size
		default:
			r, size := utf8.DecodeRuneInString(p.src[p.pos:])
			b.WriteRune(r)
			p.pos += size
		}
	}
	return "", p.syntax("unterminated quoted name")
}

// integer reads an optional sign and digits.
func (p *parser) integer() (int, bool) {
	start := p.pos
	if p.peek() == '-' {
		p.pos++
	}
	for p.peek() >= '0' && p.peek() <= '9' {
		p.pos++
	}
	n, err := strconv.Atoi(p.src[start:p.pos])
	if err != nil {
		p.pos = start
		return 0, false
	}
	return n, true
}

// indexes reads [n], [a:b(:c)] or [n,m,...].
func (p *parser) indexes() (step, error) {
	first, ok := p.integer()
	switch p.peek() {
	case ']':
		if !ok {
			return nil, p.syntax("an index is expected")
		}
		p.pos++
		if first < 0 {
			if err := p.plainOnly("a negative index"); err != nil {
				return nil, err
			}
		}
		return index{n: first}, nil
	case ':':
		if err := p.plainOnly("a slice"); err != nil {
			return nil, err
		}
		return p.slice(bound{n: first, set: ok})
	case ',':
		if err := p.plainOnly("an index union"); err != nil {
			return nil, err
		}
		if !ok {
			return nil, p.syntax("an index is expected")
		}
		return p.union(first)
	}
	return nil, p.syntax("']' expected at offset " + strconv.Itoa(p.pos))
}

// slice reads ":b(:c)]" after the start bound.
func (p *parser) slice(start bound) (step, error) {
	s := slice{start: start}
	p.pos++
	n, set := p.integer()
	s.end = bound{n: n, set: set}
	if p.eat(":") {
		n, set := p.integer()
		if set && n == 0 {
			return nil, p.syntax("a slice step cannot be 0")
		}
		s.step = bound{n: n, set: set}
	}
	return s, p.close()
}

// union reads ",m,...]" after the first index.
func (p *parser) union(first int) (step, error) {
	u := union{indexes: []int{first}}
	for p.eat(",") {
		p.skipSpace()
		n, ok := p.integer()
		if !ok {
			return nil, p.syntax("an index is expected after ','")
		}
		u.indexes = append(u.indexes, n)
	}
	return u, p.close()
}

func (p *parser) skipSpace() {
	for p.peek() == ' ' {
		p.pos++
	}
}

// filter reads "(@<ref> <op> <literal>)]" after "[?".
func (p *parser) filter() (step, error) {
	open := p.pos
	if !p.eat("(") {
		return nil, p.syntax("'(' expected after '?'")
	}
	end := matching(p.src, open)
	if end < 0 {
		return nil, p.syntax("unterminated filter")
	}
	body := p.src[open+1 : end]
	for _, op := range []string{"&&", "||"} {
		if strings.Contains(body, op) {
			return nil, p.unsupported(op)
		}
	}
	p.skipSpace()
	ref, err := p.current()
	if err != nil {
		return nil, err
	}
	f := filter{ref: ref}
	p.skipSpace()
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if p.eat(op) {
			f.op = op
			break
		}
	}
	if f.op == "" {
		return nil, p.syntax("a filter needs one of == != < <= > >=")
	}
	p.skipSpace()
	lit, err := p.literal()
	if err != nil {
		return nil, err
	}
	f.literal = lit
	p.skipSpace()
	if p.pos != end {
		return nil, p.syntax("a filter holds one comparison")
	}
	p.pos++
	return f, p.close()
}

// current reads "@" and the name and index steps after it.
func (p *parser) current() ([]step, error) {
	if !p.eat("@") {
		return nil, p.syntax("a filter compares a member of '@'")
	}
	var ref []step
	for p.peek() == '.' || p.peek() == '[' {
		var s step
		var err error
		if p.eat(".") {
			s, err = p.dotName()
		} else {
			p.pos++
			s, err = p.bracket()
		}
		if err != nil {
			return nil, err
		}
		switch f := s.(type) {
		case child, index:
			ref = append(ref, s)
		case function:
			// A function in a filter keeps the filter-script refusal.
			return nil, p.unsupported(f.text)
		case wildcard, descend, slice, union, filter:
			return nil, p.syntax("a filter reads '@' by names and indexes only")
		}
	}
	return ref, nil
}

// literal reads a JSON number, a quoted string, true, false or null.
func (p *parser) literal() (document.Value, error) {
	c := p.peek()
	switch {
	case c == '\'' || c == '"':
		s, err := p.quoted(c)
		return document.String(s), err
	case p.eat("true"):
		return document.Bool(true), nil
	case p.eat("false"):
		return document.Bool(false), nil
	case p.eat("null"):
		return document.Null{}, nil
	}
	start := p.pos
	for p.pos < len(p.src) && strings.IndexByte("+-.eE0123456789", p.src[p.pos]) >= 0 {
		p.pos++
	}
	n, err := document.ParseNumber(p.src[start:p.pos])
	if err != nil {
		return nil, p.syntax("a filter compares with a number, a string, true, false or null")
	}
	return n, nil
}

// matching returns the index of the ")" closing the "(" at open, skipping
// quoted text; -1 when there is none.
func matching(s string, open int) int {
	depth := 0
	var quote byte
	for i := open; i < len(s); i++ {
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
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
