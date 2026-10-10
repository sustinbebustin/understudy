package jsonx

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// Kind is the kind of a JSON value.
type Kind uint8

// Kind values.
const (
	Null Kind = iota + 1
	Bool
	String
	Number
	Object
	Array
)

// String returns the JSON name of the kind: "null", "boolean", "string",
// "number", "object" or "array".
func (k Kind) String() string {
	switch k {
	case Null:
		return "null"
	case Bool:
		return "boolean"
	case String:
		return "string"
	case Number:
		return "number"
	case Object:
		return "object"
	case Array:
		return "array"
	default:
		return fmt.Sprintf("Kind(%d)", uint8(k))
	}
}

// SyntaxError reports input that is not JSON: malformed text, a truncated
// document or data after the top-level value.
type SyntaxError struct {
	// Offset is the byte offset of the problem in the input.
	Offset int64
	Err    error
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("the input is not valid JSON at byte %d: %v", e.Offset, e.Err)
}

// Unwrap returns the underlying cause.
func (e *SyntaxError) Unwrap() error { return e.Err }

// KindError reports a well-formed value of a kind the caller did not ask for.
type KindError struct {
	// Offset is the byte offset of the value in the input.
	Offset int64
	Want   Kind
	Got    Kind
}

func (e *KindError) Error() string {
	return fmt.Sprintf("expected a JSON %v at byte %d, found a JSON %v", e.Want, e.Offset, e.Got)
}

// Decoder reads one JSON document as a stream of values. It is lenient in
// the ways the AWS JSON protocols need: duplicate object names are allowed
// (the caller sees each; ReadValue keeps the last), strings keep invalid
// UTF-8 bytes and lone surrogate escapes (see the package documentation),
// and numbers are returned as their decimal text.
//
// The first error is sticky: every later call returns it.
type Decoder struct {
	dec *jsontext.Decoder
	err error
}

// NewDecoder returns a Decoder over data.
func NewDecoder(data []byte) *Decoder {
	return &Decoder{dec: jsontext.NewDecoder(bytes.NewReader(data),
		jsontext.AllowDuplicateNames(true),
		jsontext.AllowInvalidUTF8(true),
	)}
}

// Offset returns the byte offset of the next unread input.
func (d *Decoder) Offset() int64 { return d.dec.InputOffset() }

// Peek returns the kind of the next value without consuming it. It fails
// at the end of the input or at the end of an object or array.
func (d *Decoder) Peek() (Kind, error) {
	if d.err != nil {
		return 0, d.err
	}
	switch d.dec.PeekKind() {
	case jsontext.KindNull:
		return Null, nil
	case jsontext.KindFalse, jsontext.KindTrue:
		return Bool, nil
	case jsontext.KindString:
		return String, nil
	case jsontext.KindNumber:
		return Number, nil
	case jsontext.KindBeginObject:
		return Object, nil
	case jsontext.KindBeginArray:
		return Array, nil
	case jsontext.KindEndObject, jsontext.KindEndArray:
		return 0, d.fail(&SyntaxError{Offset: d.Offset(), Err: errors.New("expected a value, found the end of an object or array")})
	case jsontext.KindInvalid:
		return 0, d.fail(d.tokenError())
	default:
		return 0, d.fail(d.tokenError())
	}
}

// ReadNull consumes a null.
func (d *Decoder) ReadNull() error {
	_, err := d.token(Null)
	return err
}

// ReadBool consumes a boolean.
func (d *Decoder) ReadBool() (bool, error) {
	t, err := d.token(Bool)
	if err != nil {
		return false, err
	}
	return t.Bool(), nil
}

// ReadString consumes a string. Its bytes are kept as sent: invalid UTF-8
// is not replaced, and a lone surrogate escape becomes the three-byte
// generalized UTF-8 form of the surrogate, so utf8.ValidString reports
// false for both.
func (d *Decoder) ReadString() (string, error) {
	raw, err := d.raw(String)
	if err != nil {
		return "", err
	}
	return unquote(raw), nil
}

// ReadNumber consumes a number and returns its decimal text verbatim.
func (d *Decoder) ReadNumber() (document.Number, error) {
	raw, err := d.raw(Number)
	if err != nil {
		return "", err
	}
	return document.Number(raw), nil
}

// BeginObject consumes the start of an object.
func (d *Decoder) BeginObject() error {
	_, err := d.token(Object)
	return err
}

// NextName returns the next member name of the current object, leaving the
// decoder at the member's value. It returns false, consuming the end of the
// object, when no member is left.
func (d *Decoder) NextName() (name string, ok bool, err error) {
	if d.err != nil {
		return "", false, d.err
	}
	if d.dec.PeekKind() == '}' {
		if _, err := d.dec.ReadToken(); err != nil {
			return "", false, d.fail(d.syntax(err))
		}
		return "", false, nil
	}
	raw, err := d.dec.ReadValue()
	if err != nil {
		return "", false, d.fail(d.syntax(err))
	}
	return unquote(raw), true, nil
}

// BeginArray consumes the start of an array.
func (d *Decoder) BeginArray() error {
	_, err := d.token(Array)
	return err
}

// NextElement reports whether another element of the current array
// follows, consuming the end of the array when none does.
func (d *Decoder) NextElement() (bool, error) {
	if d.err != nil {
		return false, d.err
	}
	if d.dec.PeekKind() != ']' {
		return true, nil
	}
	if _, err := d.dec.ReadToken(); err != nil {
		return false, d.fail(d.syntax(err))
	}
	return false, nil
}

// Skip consumes the next value, whatever its kind.
func (d *Decoder) Skip() error {
	if d.err != nil {
		return d.err
	}
	if err := d.dec.SkipValue(); err != nil {
		return d.fail(d.syntax(err))
	}
	return nil
}

// ReadValue consumes the next value as a document tree. Object members keep
// wire order; of duplicate names the last value wins, in the position of
// the first.
func (d *Decoder) ReadValue() (document.Value, error) {
	k, err := d.Peek()
	if err != nil {
		return nil, err
	}
	switch k {
	case Null:
		return document.Null{}, d.ReadNull()
	case Bool:
		b, err := d.ReadBool()
		return document.Bool(b), err
	case String:
		s, err := d.ReadString()
		return document.String(s), err
	case Number:
		return d.ReadNumber()
	case Object:
		return d.readMap()
	case Array:
		return d.readList()
	default:
		return nil, d.fail(fmt.Errorf("jsonx: unhandled kind %v", k))
	}
}

// End checks that nothing but whitespace follows the top-level value.
func (d *Decoder) End() error {
	if d.err != nil {
		return d.err
	}
	_, err := d.dec.ReadToken()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		err = errors.New("unexpected data after the top-level value")
	}
	return d.fail(&SyntaxError{Offset: d.Offset(), Err: err})
}

func (d *Decoder) readMap() (document.Value, error) {
	if err := d.BeginObject(); err != nil {
		return nil, err
	}
	m := document.Map{}
	index := map[string]int{}
	for {
		name, ok, err := d.NextName()
		if err != nil {
			return nil, err
		}
		if !ok {
			return m, nil
		}
		v, err := d.ReadValue()
		if err != nil {
			return nil, err
		}
		if i, dup := index[name]; dup {
			m[i].Value = v
			continue
		}
		index[name] = len(m)
		m = append(m, document.Entry{Key: name, Value: v})
	}
}

func (d *Decoder) readList() (document.Value, error) {
	if err := d.BeginArray(); err != nil {
		return nil, err
	}
	l := document.List{}
	for {
		more, err := d.NextElement()
		if err != nil {
			return nil, err
		}
		if !more {
			return l, nil
		}
		v, err := d.ReadValue()
		if err != nil {
			return nil, err
		}
		l = append(l, v)
	}
}

// token consumes a value of kind want that ReadToken represents fully:
// null, a boolean or the start of an object or array.
func (d *Decoder) token(want Kind) (jsontext.Token, error) {
	if err := d.expect(want); err != nil {
		return jsontext.Token{}, err
	}
	t, err := d.dec.ReadToken()
	if err != nil {
		return jsontext.Token{}, d.fail(d.syntax(err))
	}
	return t, nil
}

// raw consumes a string or number of kind want and returns its exact bytes.
func (d *Decoder) raw(want Kind) (jsontext.Value, error) {
	if err := d.expect(want); err != nil {
		return nil, err
	}
	v, err := d.dec.ReadValue()
	if err != nil {
		return nil, d.fail(d.syntax(err))
	}
	return v, nil
}

func (d *Decoder) expect(want Kind) error {
	got, err := d.Peek()
	if err != nil {
		return err
	}
	if got != want {
		return d.fail(&KindError{Offset: d.Offset(), Want: want, Got: got})
	}
	return nil
}

// tokenError returns the error PeekKind found, which ReadToken reports.
func (d *Decoder) tokenError() error {
	_, err := d.dec.ReadToken()
	if err == nil {
		err = errors.New("unexpected token")
	}
	return d.syntax(err)
}

func (d *Decoder) syntax(err error) error {
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return &SyntaxError{Offset: d.Offset(), Err: err}
}

func (d *Decoder) fail(err error) error {
	d.err = err
	return err
}
