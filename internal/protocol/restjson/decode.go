package restjson

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/restbind"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// decodeError is a body that is JSON but does not fit the input shape: a
// value of the wrong kind, a number out of range, a bad base64 blob.
type decodeError struct {
	Offset  int64
	Message string
}

func (e *decodeError) Error() string {
	return fmt.Sprintf("restjson: the request body does not match the input shape at byte %d: %s", e.Offset, e.Message)
}

// usageError is generated code asking for something the open container
// does not hold: a bug.
type usageError struct{ reason string }

func (e *usageError) Error() string { return "restjson: deserializer misuse: " + e.reason }

type frameKind uint8

const (
	structFrame frameKind = iota + 1
	unionFrame
	listFrame
	mapFrame
)

type readFrame struct {
	kind   frameKind
	schema *smithy.Schema
	sparse bool
	// root marks the document's root structure, whose members bound to
	// HTTP are read from the head, never the body.
	root   bool
	chosen *smithy.Member
}

// jsonDoc reads one restJson1 body document (wire-rest.md 4.1): members
// under their jsonName, unknown and null members absent,
// epoch-seconds timestamps by default, base64 blobs, NaN and the
// infinities as strings. Types are strict: a string is never coerced to a
// number or boolean, a fraction is never an integer.
type jsonDoc struct {
	dec     *jsonx.Decoder
	open    []readFrame
	entered bool
}

// newDocument opens a JSON body; a JSON root carries no name, so the
// payload member does not matter.
func newDocument(body []byte, _ *smithy.Member) restbind.Document {
	return &jsonDoc{dec: jsonx.NewDecoder(body)}
}

func (d *jsonDoc) ReadStruct(s *smithy.Schema) error {
	if err := d.enterObject(s); err != nil {
		return err
	}
	d.open = append(d.open, readFrame{kind: structFrame, schema: s, root: !d.entered})
	d.entered = true
	return nil
}

func (d *jsonDoc) ReadStructMember() (*smithy.Member, bool, error) {
	top := d.top()
	if top == nil || top.kind != structFrame {
		return nil, false, &usageError{reason: "ReadStructMember outside a structure"}
	}
	m, err := d.nextMember(top.schema, top.root)
	if err != nil {
		return nil, false, err
	}
	if m == nil {
		d.pop()
		return nil, false, nil
	}
	return m, true, nil
}

func (d *jsonDoc) ReadUnion(s *smithy.Schema) (*smithy.Member, error) {
	if err := d.enterObject(s); err != nil {
		return nil, err
	}
	d.entered = true
	at := d.dec.Offset()
	m, err := d.nextMember(s, false)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, &decodeError{Offset: at, Message: "the union " + s.ID.Name() + " has no member set; set exactly one"}
	}
	d.open = append(d.open, readFrame{kind: unionFrame, schema: s, chosen: m})
	return m, nil
}

func (d *jsonDoc) CloseUnion() error {
	top := d.top()
	if top == nil || top.kind != unionFrame {
		return &usageError{reason: "CloseUnion outside a union"}
	}
	at := d.dec.Offset()
	m, err := d.nextMember(top.schema, false)
	if err != nil {
		return err
	}
	if m != nil {
		return &decodeError{Offset: at, Message: fmt.Sprintf("the union %s has more than one member set: %s and %s", top.schema.ID.Name(), top.chosen.Name, m.Name)}
	}
	d.pop()
	return nil
}

func (d *jsonDoc) ReadList(m *smithy.Member) error {
	if err := d.expect(m, jsonx.Array); err != nil {
		return err
	}
	if err := d.dec.BeginArray(); err != nil {
		return err
	}
	d.open = append(d.open, readFrame{kind: listFrame, schema: m.Target, sparse: m.Target.Sparse})
	return nil
}

func (d *jsonDoc) ReadListElement() (bool, error) {
	top := d.top()
	if top == nil || top.kind != listFrame {
		return false, &usageError{reason: "ReadListElement outside a list"}
	}
	for {
		more, err := d.dec.NextElement()
		if err != nil || !more {
			if err == nil {
				d.pop()
			}
			return false, err
		}
		if top.sparse {
			return true, nil
		}
		skipped, err := d.skipNull()
		if err != nil || !skipped {
			return err == nil, err
		}
	}
}

func (d *jsonDoc) ReadMap(m *smithy.Member) error {
	if err := d.expect(m, jsonx.Object); err != nil {
		return err
	}
	if err := d.dec.BeginObject(); err != nil {
		return err
	}
	d.open = append(d.open, readFrame{kind: mapFrame, schema: m.Target, sparse: m.Target.Sparse})
	return nil
}

func (d *jsonDoc) ReadMapKey() (key string, ok bool, err error) {
	top := d.top()
	if top == nil || top.kind != mapFrame {
		return "", false, &usageError{reason: "ReadMapKey outside a map"}
	}
	for {
		name, more, err := d.dec.NextName()
		if err != nil || !more {
			if err == nil {
				d.pop()
			}
			return "", false, err
		}
		if top.sparse {
			return name, true, nil
		}
		skipped, err := d.skipNull()
		if err != nil {
			return "", false, err
		}
		if !skipped {
			return name, true, nil
		}
	}
}

func (d *jsonDoc) ReadString(m *smithy.Member) (string, error) {
	if err := d.expect(m, jsonx.String); err != nil {
		return "", err
	}
	return d.dec.ReadString()
}

func (d *jsonDoc) ReadBool(m *smithy.Member) (bool, error) {
	if err := d.expect(m, jsonx.Bool); err != nil {
		return false, err
	}
	return d.dec.ReadBool()
}

func (d *jsonDoc) ReadInt8(m *smithy.Member) (int8, error) {
	v, err := d.readInt(m, 8)
	return int8(v), err //nolint:gosec // readInt checked the range for 8 bits
}

func (d *jsonDoc) ReadInt16(m *smithy.Member) (int16, error) {
	v, err := d.readInt(m, 16)
	return int16(v), err //nolint:gosec // readInt checked the range for 16 bits
}

func (d *jsonDoc) ReadInt32(m *smithy.Member) (int32, error) {
	v, err := d.readInt(m, 32)
	return int32(v), err //nolint:gosec // readInt checked the range for 32 bits
}

func (d *jsonDoc) ReadInt64(m *smithy.Member) (int64, error) { return d.readInt(m, 64) }

func (d *jsonDoc) ReadFloat32(m *smithy.Member) (float32, error) {
	v, err := d.readFloat(m, 32)
	return float32(v), err
}

func (d *jsonDoc) ReadFloat64(m *smithy.Member) (float64, error) { return d.readFloat(m, 64) }

func (d *jsonDoc) ReadBigInt(m *smithy.Member) (*big.Int, error) {
	return number(d, m, restbind.ParseBigInt)
}

func (d *jsonDoc) ReadBigDecimal(m *smithy.Member) (smithy.BigDecimal, error) {
	return number(d, m, restbind.ParseBigDecimal)
}

func (d *jsonDoc) ReadTimestamp(m *smithy.Member) (time.Time, error) {
	format := restbind.TimestampFormat(m, smithy.EpochSeconds)
	parse := func(s string) (time.Time, error) { return restbind.ParseTimestamp(s, format) }
	if format == smithy.EpochSeconds {
		return number(d, m, parse)
	}
	at := d.dec.Offset()
	s, err := d.ReadString(m)
	if err != nil {
		return time.Time{}, err
	}
	t, err := parse(s)
	if err != nil {
		return time.Time{}, &decodeError{Offset: at, Message: err.Error() + " for " + m.Name}
	}
	return t, nil
}

func (d *jsonDoc) ReadBlob(m *smithy.Member) ([]byte, error) {
	at := d.dec.Offset()
	s, err := d.ReadString(m)
	if err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, &decodeError{Offset: at, Message: "the value of " + m.Name + " is not valid padded base64"}
	}
	return b, nil
}

func (d *jsonDoc) ReadDocument(*smithy.Member) (document.Value, error) {
	return d.dec.ReadValue()
}

func (d *jsonDoc) ReadNull(*smithy.Member) (bool, error) { return d.skipNull() }

// ReadBody fails: restbind binds a @streaming payload from the body
// itself, never through a document.
func (d *jsonDoc) ReadBody(*smithy.Member) (smithy.Body, error) {
	return nil, &usageError{reason: "ReadBody on a body document"}
}

// End checks that nothing follows the root value.
func (d *jsonDoc) End() error { return d.dec.End() }

func (d *jsonDoc) readInt(m *smithy.Member, bits int) (int64, error) {
	return number(d, m, func(s string) (int64, error) { return restbind.ParseInt(s, bits) })
}

// readFloat accepts a number or the strings NaN, Infinity and -Infinity.
func (d *jsonDoc) readFloat(m *smithy.Member, bits int) (float64, error) {
	k, err := d.dec.Peek()
	if err != nil {
		return 0, err
	}
	if k != jsonx.String {
		return number(d, m, func(s string) (float64, error) { return restbind.ParseFloat(s, bits) })
	}
	at := d.dec.Offset()
	s, err := d.dec.ReadString()
	if err != nil {
		return 0, err
	}
	if s != "NaN" && s != "Infinity" && s != "-Infinity" {
		return 0, &decodeError{Offset: at, Message: fmt.Sprintf("the string %q is not a number for %s", s, m.Name)}
	}
	return restbind.ParseFloat(s, bits)
}

// number reads a JSON number and parses its text.
func number[T any](d *jsonDoc, m *smithy.Member, parse func(string) (T, error)) (T, error) {
	var zero T
	if err := d.expect(m, jsonx.Number); err != nil {
		return zero, err
	}
	at := d.dec.Offset()
	n, err := d.dec.ReadNumber()
	if err != nil {
		return zero, err
	}
	v, err := parse(string(n))
	if err != nil {
		return zero, &decodeError{Offset: at, Message: err.Error() + " for " + m.Name}
	}
	return v, nil
}

// expect checks that the next value is of kind want.
func (d *jsonDoc) expect(m *smithy.Member, want jsonx.Kind) error {
	got, err := d.dec.Peek()
	if err != nil {
		return err
	}
	if got != want {
		return &decodeError{Offset: d.dec.Offset(), Message: fmt.Sprintf("expected a JSON %v for %s, found a JSON %v", want, m.Name, got)}
	}
	return nil
}

// nextMember advances to the next member of the open object that s
// declares, is not null and, at the root, is not bound to HTTP; unknown
// members (__type in a union among them) and nulls are absent.
func (d *jsonDoc) nextMember(s *smithy.Schema, root bool) (*smithy.Member, error) {
	for {
		name, ok, err := d.dec.NextName()
		if err != nil || !ok {
			return nil, err
		}
		m := memberNamed(s, name)
		if m == nil || root && m.HTTP != nil {
			if err := d.dec.Skip(); err != nil {
				return nil, err
			}
			continue
		}
		skipped, err := d.skipNull()
		if err != nil {
			return nil, err
		}
		if !skipped {
			return m, nil
		}
	}
}

// memberNamed finds the member whose wire name, its jsonName or else its
// model name, is name.
func memberNamed(s *smithy.Schema, name string) *smithy.Member {
	for _, m := range s.Members {
		if wireName(m) == name {
			return m
		}
	}
	return nil
}

func wireName(m *smithy.Member) string {
	if m.JSONName != "" {
		return m.JSONName
	}
	return m.Name
}

func (d *jsonDoc) enterObject(s *smithy.Schema) error {
	k, err := d.dec.Peek()
	if err != nil {
		return err
	}
	if k != jsonx.Object {
		return &decodeError{Offset: d.dec.Offset(), Message: fmt.Sprintf("expected a JSON object for %s, found a JSON %v", s.ID.Name(), k)}
	}
	return d.dec.BeginObject()
}

func (d *jsonDoc) skipNull() (bool, error) {
	k, err := d.dec.Peek()
	if err != nil || k != jsonx.Null {
		return false, err
	}
	return true, d.dec.ReadNull()
}

func (d *jsonDoc) top() *readFrame {
	if len(d.open) == 0 {
		return nil
	}
	return &d.open[len(d.open)-1]
}

func (d *jsonDoc) pop() { d.open = d.open[:len(d.open)-1] }

// bodyProblem reports whether err is a body that is not JSON or does not
// fit the input, which the service answers as a malformed request.
func bodyProblem(err error) bool {
	var decode *decodeError
	var syntax *jsonx.SyntaxError
	return errors.As(err, &decode) || errors.As(err, &syntax)
}
