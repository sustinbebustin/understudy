package envelope

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// The reserved template variables (secrets-logs-events-alarms.md 4.6). An
// InputPathsMap key cannot name one: their dots are outside the key
// pattern.
const (
	varRuleARN       = "aws.events.rule-arn"
	varRuleName      = "aws.events.rule-name"
	varIngestionTime = "aws.events.event.ingestion-time"
	varEvent         = "aws.events.event"
	varEventJSON     = "aws.events.event.json"
)

// reserved reports whether name is a reserved variable.
func reserved(name string) bool {
	switch name {
	case varRuleARN, varRuleName, varIngestionTime, varEvent, varEventJSON:
		return true
	}
	return false
}

// Context is what the reserved variables read besides the event: the rule
// that matched it and when EventBridge took the event in.
type Context struct {
	RuleARN  string
	RuleName string
	Ingested time.Time
}

// Transformer is a compiled InputTransformer: its variables and template.
type Transformer struct {
	paths map[string]Path
	// text is a string template, whose first non-blank character is a
	// double quote; otherwise the template is JSON.
	text     bool
	segments []segment
}

// segment is a piece of a template.
//
//sumtype:decl
type segment interface{ isSegment() }

// literal is template text copied as is.
type literal string

// placeholder is a <name> reference. inString says it sits inside a
// string literal of a JSON template.
type placeholder struct {
	name     string
	inString bool
}

func (literal) isSegment()     {}
func (placeholder) isSegment() {}

// PlaceholderError is a template placeholder that names neither a variable
// of InputPathsMap nor a reserved one.
type PlaceholderError struct{ Name string }

func (e *PlaceholderError) Error() string {
	return "envelope: the InputTemplate placeholder <" + e.Name + "> names no InputPathsMap variable and no reserved variable"
}

// VariableError is an InputPathsMap value that is not a supported path.
type VariableError struct {
	Name string
	Err  error
}

func (e *VariableError) Error() string {
	return "envelope: InputPathsMap variable " + e.Name + ": " + e.Err.Error()
}

func (e *VariableError) Unwrap() error { return e.Err }

// NewTransformer compiles an InputTransformer. Each InputPathsMap value
// must be a path (a *VariableError names the first variable in name order
// that is not), and each placeholder must name a variable or a reserved one
// (a *PlaceholderError names the first, in template order, that does not).
func NewTransformer(paths map[string]string, template string) (*Transformer, error) {
	t := &Transformer{paths: make(map[string]Path, len(paths))}
	for _, name := range slices.Sorted(maps.Keys(paths)) {
		p, err := ParsePath(paths[name])
		if err != nil {
			return nil, &VariableError{Name: name, Err: err}
		}
		t.paths[name] = p
	}
	t.text = strings.HasPrefix(strings.TrimLeft(template, " \t\r\n"), `"`)
	t.segments = scan(template)
	for _, s := range t.segments {
		if p, ok := s.(placeholder); ok {
			if _, defined := t.paths[p.name]; !defined && !reserved(p.name) {
				return nil, &PlaceholderError{Name: p.name}
			}
		}
	}
	return t, nil
}

// scan splits a template into literals and placeholders, noting which
// placeholders sit inside a JSON string literal.
func scan(template string) []segment {
	var out []segment
	inString, escaped := false, false
	start := 0
	for i := 0; i < len(template); i++ {
		c := template[i]
		switch {
		case inString && escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case c == '<':
			end, ok := placeholderEnd(template, i)
			if !ok {
				continue
			}
			if start < i {
				out = append(out, literal(template[start:i]))
			}
			out = append(out, placeholder{name: template[i+1 : end], inString: inString})
			i = end
			start = end + 1
		}
	}
	if start < len(template) {
		out = append(out, literal(template[start:]))
	}
	return out
}

// placeholderEnd returns the index of the ">" closing the placeholder that
// opens at template[i], which is "<".
func placeholderEnd(template string, i int) (int, bool) {
	j := i + 1
	for j < len(template) && placeholderByte(template[j]) {
		j++
	}
	if j == i+1 || j == len(template) || template[j] != '>' {
		return 0, false
	}
	return j, true
}

// placeholderByte reports whether b may appear in a placeholder name: the
// InputPathsMap key characters and the dots of the reserved names.
func placeholderByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '.'
}

// Render builds the payload for e. ok is false when AWS would not deliver
// it: a JSON template whose output is not JSON, or an object or array
// placed inside one of its string literals. An error is a placed value JSON
// cannot hold, as for Event.JSON.
func (t *Transformer) Render(e Event, ctx Context) (body []byte, ok bool, err error) {
	doc := e.Document()
	var out []byte
	for _, s := range t.segments {
		switch s := s.(type) {
		case literal:
			out = append(out, s...)
		case placeholder:
			v, found := t.value(s.name, e, doc, ctx)
			out, ok, err = t.place(out, v, found, s.inString)
			if !ok || err != nil {
				return nil, false, err
			}
		}
	}
	if !t.text {
		if _, valid := ParseJSON(string(out)); !valid {
			return nil, false, nil
		}
	}
	return out, true, nil
}

// value returns what a placeholder stands for; found is false for a
// variable whose path selects nothing.
func (t *Transformer) value(name string, e Event, doc document.Map, ctx Context) (document.Value, bool) {
	switch name {
	case varRuleARN:
		return document.String(ctx.RuleARN), true
	case varRuleName:
		return document.String(ctx.RuleName), true
	case varIngestionTime:
		return document.String(formatTime(ctx.Ingested)), true
	case varEvent:
		return e.withoutDetail(), true
	case varEventJSON:
		return doc, true
	}
	return t.paths[name].Select(doc)
}

// place appends the rendering of v at a placeholder; ok is false when AWS
// drops the delivery.
func (t *Transformer) place(out []byte, v document.Value, found, inString bool) (placed []byte, ok bool, err error) {
	switch {
	case !found && (t.text || inString):
		return out, true, nil
	case !found:
		return append(out, `""`...), true, nil
	case inString && !t.text && container(v):
		return nil, false, nil
	case t.text || inString:
		placed, err = appendFlat(out, v)
		return placed, err == nil, err
	}
	if s, isString := v.(document.String); isString {
		out = append(out, '"')
		out = append(out, s...)
		return append(out, '"'), true, nil
	}
	b, err := encode(v)
	if err != nil {
		return nil, false, err
	}
	return append(out, b...), true, nil
}

// container reports whether v is an object or an array.
func container(v document.Value) bool {
	switch v.(type) {
	case document.Map, document.List:
		return true
	case document.Null, document.Bool, document.Number, document.String:
	}
	return false
}

// appendFlat appends v with strings raw and every quote and blank of its
// JSON form left out: {acc_id:0a787ecb-4015,sf_id:baz}, [ACTIVE,INACTIVE].
func appendFlat(b []byte, v document.Value) ([]byte, error) {
	var err error
	switch v := v.(type) {
	case document.String:
		return append(b, v...), nil
	case document.List:
		b = append(b, '[')
		for i, e := range v {
			if i > 0 {
				b = append(b, ',')
			}
			if b, err = appendFlat(b, e); err != nil {
				return nil, err
			}
		}
		return append(b, ']'), nil
	case document.Map:
		b = append(b, '{')
		for i, e := range v {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, e.Key...)
			b = append(b, ':')
			if b, err = appendFlat(b, e.Value); err != nil {
				return nil, err
			}
		}
		return append(b, '}'), nil
	case document.Null, document.Bool, document.Number:
	}
	scalar, err := encode(v)
	if err != nil {
		return nil, err
	}
	return append(b, scalar...), nil
}
