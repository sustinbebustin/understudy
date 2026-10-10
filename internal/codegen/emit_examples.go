package codegen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// hasExamples reports whether any operation carries a model example.
func hasExamples(ir *ServiceIR) bool {
	return slices.ContainsFunc(ir.Operations, func(o *OperationIR) bool { return len(o.Examples) > 0 })
}

// emitExamples writes examples_gen_test.go: every model example input as a
// typed Go literal, decoded from the example's JSON at generation time, run
// through Validate. An over-strict overlay or a wrong pattern translation
// fails here (ARCHITECTURE.md 5.5).
func emitExamples(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "examples_gen_test.go")
	f.use("awserr", "testing")
	f.p("// TestModelExamples validates the input of every model example; AWS's")
	f.p("// own examples must satisfy the generated constraints.")
	f.p("func TestModelExamples(t *testing.T) {")
	f.p("\ttests := []struct {")
	f.p("\t\tname string")
	f.p("\t\tin   interface{ Validate() []awserr.Violation }")
	f.p("\t}{")
	for _, o := range ir.Operations {
		for _, e := range o.Examples {
			lit, err := f.exampleLit(o.Input, e.Input, o.Name+" example "+strconv.Quote(e.Title))
			if err != nil {
				return nil, err
			}
			f.p("\t\t{%s, &%s},", goString(o.Name+"/"+e.Title), lit)
		}
	}
	f.p("\t}")
	f.p("\tfor _, tt := range tests {")
	f.p("\t\tt.Run(tt.name, func(t *testing.T) {")
	f.p("\t\t\tif vs := tt.in.Validate(); len(vs) != 0 {")
	f.p("\t\t\t\tt.Fatalf(\"the model's example violates the generated constraints: %%v\", vs)")
	f.p("\t\t\t}")
	f.p("\t\t})")
	f.p("\t}")
	f.p("}")
	return f.bytes()
}

// exampleLit renders the JSON value raw as a Go literal of shape s; subject
// names the example position in messages.
func (f *file) exampleLit(s *ShapeIR, raw json.RawMessage, subject string) (string, error) {
	fail := func(format string, args ...any) (string, error) {
		return "", failf(StageEmit, subject, "%s: %s", s.ID, fmt.Sprintf(format, args...))
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return fail("%v", err)
	}
	switch s.Type {
	case smithy.TypeStructure, smithy.TypeUnit:
		return f.structLit(s, raw, subject)
	case smithy.TypeUnion:
		return f.unionLit(s, raw, subject)
	case smithy.TypeList:
		return f.listLit(s, raw, subject)
	case smithy.TypeMap:
		return f.mapLit(s, raw, subject)
	case smithy.TypeString:
		text, ok := v.(string)
		if !ok {
			return fail("want a string, have %s", raw)
		}
		return goString(text), nil
	case smithy.TypeEnum:
		text, ok := v.(string)
		if !ok {
			return fail("want a string, have %s", raw)
		}
		return s.GoName + "(" + goString(text) + ")", nil
	case smithy.TypeBlob:
		text, ok := v.(string)
		if !ok {
			return fail("want a string, have %s", raw)
		}
		return "[]byte(" + goString(text) + ")", nil
	case smithy.TypeBoolean:
		b, ok := v.(bool)
		if !ok {
			return fail("want a boolean, have %s", raw)
		}
		return strconv.FormatBool(b), nil
	case smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeIntEnum:
		n, ok := v.(json.Number)
		if _, err := n.Int64(); !ok || err != nil {
			return fail("want an integer, have %s", raw)
		}
		return f.valueType(s) + "(" + n.String() + ")", nil
	case smithy.TypeFloat, smithy.TypeDouble, smithy.TypeBigDecimal:
		n, ok := v.(json.Number)
		if _, err := n.Float64(); !ok || err != nil {
			return fail("want a number, have %s", raw)
		}
		if s.Type == smithy.TypeBigDecimal {
			return f.valueType(s) + "(" + goString(n.String()) + ")", nil
		}
		return f.valueType(s) + "(" + n.String() + ")", nil
	case smithy.TypeTimestamp:
		at, err := exampleTime(v)
		if err != nil {
			return fail("%v", err)
		}
		f.use("time")
		return fmt.Sprintf("time.Date(%d, time.%s, %d, %d, %d, %d, %d, time.UTC)",
			at.Year(), at.Month(), at.Day(), at.Hour(), at.Minute(), at.Second(), at.Nanosecond()), nil
	case smithy.TypeDocument:
		return f.documentLit(raw)
	case smithy.TypeBigInteger:
		return fail("bigInteger example values are not rendered")
	default:
		return fail("shape type %s has no example rendering", s.Type)
	}
}

// exampleTime reads a timestamp as Smithy examples write it: epoch seconds
// or an RFC 3339 date-time.
func exampleTime(v any) (time.Time, error) {
	switch x := v.(type) {
	case json.Number:
		secs, err := x.Float64()
		if err != nil {
			return time.Time{}, err
		}
		whole, frac := math.Modf(secs)
		return time.Unix(int64(whole), int64(math.Round(frac*1e9))).UTC(), nil
	case string:
		return time.Parse(time.RFC3339Nano, x)
	default:
		return time.Time{}, fmt.Errorf("a timestamp is epoch seconds or a date-time string, not %v", v)
	}
}

func objectOf(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func (f *file) structLit(s *ShapeIR, raw json.RawMessage, subject string) (string, error) {
	obj, err := objectOf(raw)
	if err != nil {
		return "", failf(StageEmit, subject, "%s: want an object: %w", s.ID, err)
	}
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if !slices.ContainsFunc(s.Members, func(m *MemberIR) bool { return m.Name == k }) {
			return "", failf(StageEmit, subject, "%s has no member %q", s.ID, k)
		}
	}
	var fields []string
	for _, m := range s.Members {
		v, ok := obj[m.Name]
		if !ok || string(v) == "null" {
			continue
		}
		lit, err := f.exampleLit(m.Target, v, subject)
		if err != nil {
			return "", err
		}
		if !m.Value && pointerable(m.Target) {
			lit = "new(" + lit + ")"
		}
		fields = append(fields, m.GoName+": "+lit)
	}
	return s.GoName + "{" + strings.Join(fields, ", ") + "}", nil
}

func (f *file) unionLit(s *ShapeIR, raw json.RawMessage, subject string) (string, error) {
	obj, err := objectOf(raw)
	if err != nil || len(obj) != 1 {
		return "", failf(StageEmit, subject, "%s: a union value is an object with exactly one member, not %s", s.ID, raw)
	}
	for _, m := range s.Members {
		v, ok := obj[m.Name]
		if !ok {
			continue
		}
		variant := s.GoName + "Member" + m.GoName
		if m.Target.Type == smithy.TypeUnit {
			return variant + "{}", nil
		}
		lit, err := f.exampleLit(m.Target, v, subject)
		if err != nil {
			return "", err
		}
		return variant + "{Value: " + lit + "}", nil
	}
	return "", failf(StageEmit, subject, "%s has no member %s", s.ID, slices.Collect(maps.Keys(obj)))
}

func (f *file) listLit(s *ShapeIR, raw json.RawMessage, subject string) (string, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", failf(StageEmit, subject, "%s: want an array: %w", s.ID, err)
	}
	elems := make([]string, len(items))
	for i, item := range items {
		lit, err := f.elemLit(s.Members[0], item, subject)
		if err != nil {
			return "", err
		}
		elems[i] = lit
	}
	return f.valueType(s) + "{" + strings.Join(elems, ", ") + "}", nil
}

func (f *file) mapLit(s *ShapeIR, raw json.RawMessage, subject string) (string, error) {
	obj, err := objectOf(raw)
	if err != nil {
		return "", failf(StageEmit, subject, "%s: want an object: %w", s.ID, err)
	}
	key := s.Members[0].Target
	entries := make([]string, 0, len(obj))
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		keyLit := goString(k)
		if key.Type == smithy.TypeEnum {
			keyLit = key.GoName + "(" + keyLit + ")"
		}
		lit, err := f.elemLit(s.Members[1], obj[k], subject)
		if err != nil {
			return "", err
		}
		entries = append(entries, keyLit+": "+lit)
	}
	return f.valueType(s) + "{" + strings.Join(entries, ", ") + "}", nil
}

// elemLit renders a list element or map value; sparse collections hold
// pointers, with nil for null.
func (f *file) elemLit(m *MemberIR, raw json.RawMessage, subject string) (string, error) {
	sparse := m.Owner.Sparse && pointerable(m.Target)
	if sparse && string(raw) == "null" {
		return "nil", nil
	}
	lit, err := f.exampleLit(m.Target, raw, subject)
	if err != nil || !sparse {
		return lit, err
	}
	return "new(" + lit + ")", nil
}
