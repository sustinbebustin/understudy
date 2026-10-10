package snapshot

import (
	"bytes"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

// tree is a parsed JSON value. Numbers keep their decimal text, so no
// precision is lost before two values are compared.
type tree struct {
	kind jsonx.Kind
	// text is a string's contents, a number's decimal text, or "true" or
	// "false".
	text string
	// members are an object's members, sorted by name.
	members []member
	elems   []tree
}

type member struct {
	name string
	val  tree
}

// parseTree parses one JSON document. Of duplicate member names the last
// wins.
func parseTree(data []byte) (tree, error) {
	d := jsonx.NewDecoder(data)
	t, err := parseValue(d)
	if err != nil {
		return tree{}, err
	}
	return t, d.End()
}

// parseLines parses NDJSON as an array with one element per non-empty line.
func parseLines(data []byte) (tree, error) {
	out := tree{kind: jsonx.Array}
	for line := range bytes.Lines(data) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		t, err := parseTree(line)
		if err != nil {
			return tree{}, err
		}
		out.elems = append(out.elems, t)
	}
	return out, nil
}

func parseValue(d *jsonx.Decoder) (tree, error) {
	k, err := d.Peek()
	if err != nil {
		return tree{}, err
	}
	switch k {
	case jsonx.Null:
		return tree{kind: k}, d.ReadNull()
	case jsonx.Bool:
		b, err := d.ReadBool()
		text := "false"
		if b {
			text = "true"
		}
		return tree{kind: k, text: text}, err
	case jsonx.String:
		s, err := d.ReadString()
		return tree{kind: k, text: s}, err
	case jsonx.Number:
		n, err := d.ReadNumber()
		return tree{kind: k, text: string(n)}, err
	case jsonx.Object:
		return parseObject(d)
	case jsonx.Array:
		return parseArray(d)
	}
	return tree{}, &jsonx.KindError{Offset: d.Offset(), Got: k}
}

func parseObject(d *jsonx.Decoder) (tree, error) {
	if err := d.BeginObject(); err != nil {
		return tree{}, err
	}
	byName := map[string]tree{}
	for {
		name, ok, err := d.NextName()
		if err != nil {
			return tree{}, err
		}
		if !ok {
			break
		}
		v, err := parseValue(d)
		if err != nil {
			return tree{}, err
		}
		byName[name] = v
	}
	out := tree{kind: jsonx.Object, members: make([]member, 0, len(byName))}
	for name, v := range byName {
		out.members = append(out.members, member{name: name, val: v})
	}
	sortMembers(out.members)
	return out, nil
}

func parseArray(d *jsonx.Decoder) (tree, error) {
	if err := d.BeginArray(); err != nil {
		return tree{}, err
	}
	out := tree{kind: jsonx.Array}
	for {
		ok, err := d.NextElement()
		if err != nil {
			return tree{}, err
		}
		if !ok {
			return out, nil
		}
		v, err := parseValue(d)
		if err != nil {
			return tree{}, err
		}
		out.elems = append(out.elems, v)
	}
}

func sortMembers(ms []member) {
	slices.SortFunc(ms, func(a, b member) int { return strings.Compare(a.name, b.name) })
}

// str is a string value.
func str(s string) tree { return tree{kind: jsonx.String, text: s} }

// json renders t as compact JSON with members in name order.
func (t tree) json() string {
	return string(t.appendJSON(nil))
}

func (t tree) appendJSON(b []byte) []byte {
	switch t.kind {
	case jsonx.Null:
		return append(b, "null"...)
	case jsonx.Bool, jsonx.Number:
		return append(b, t.text...)
	case jsonx.String:
		return jsonx.AppendString(b, t.text)
	case jsonx.Object:
		b = append(b, '{')
		for i, m := range t.members {
			if i > 0 {
				b = append(b, ',')
			}
			b = jsonx.AppendString(b, m.name)
			b = append(b, ':')
			b = m.val.appendJSON(b)
		}
		return append(b, '}')
	case jsonx.Array:
		b = append(b, '[')
		for i, e := range t.elems {
			if i > 0 {
				b = append(b, ',')
			}
			b = e.appendJSON(b)
		}
		return append(b, ']')
	}
	return b
}

// equal reports whether a and b are the same JSON value.
func equal(a, b tree) bool {
	return a.json() == b.json()
}
