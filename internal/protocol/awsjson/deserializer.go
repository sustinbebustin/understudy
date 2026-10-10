package awsjson

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// decodeError is a request body the codec cannot map onto the input shape:
// a value of the wrong JSON kind, a number out of range, a bad base64 blob.
// The request is answered with the service's malformed-request error
// (malformedRequest), carrying Message.
type decodeError struct {
	// Offset is the byte offset of the offending value in the body.
	Offset int64
	// Message is the text AWS's Coral front end sends for the condition.
	Message string
}

func (e *decodeError) Error() string {
	return fmt.Sprintf("awsjson: the request body does not match the input shape at byte %d: %s", e.Offset, e.Message)
}

// frameKind is the kind of an open container.
type frameKind uint8

const (
	structFrame frameKind = iota + 1
	unionFrame
	listFrame
	mapFrame
)

// readFrame is an open container on the wire.
type readFrame struct {
	kind   frameKind
	schema *smithy.Schema
	// sparse lists and maps keep null values; dense ones skip them.
	sparse bool
	// empty marks the top-level structure of an empty body.
	empty bool
	// chosen is the member a union frame was opened for.
	chosen *smithy.Member
}

// deserializer implements smithy.Deserializer over one awsJson body.
type deserializer struct {
	dec   *jsonx.Decoder
	empty bool
	open  []readFrame
}

func newDeserializer(body []byte) *deserializer {
	return &deserializer{dec: jsonx.NewDecoder(body), empty: isJSONSpace(body)}
}

// isJSONSpace reports whether b holds only JSON whitespace: an empty body,
// which servers must accept as {}.
func isJSONSpace(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return false
		}
	}
	return true
}

func (d *deserializer) ReadStruct(s *smithy.Schema) error {
	if len(d.open) == 0 && d.empty {
		d.open = append(d.open, readFrame{kind: structFrame, schema: s, empty: true})
		return nil
	}
	if err := d.enterObject(s); err != nil {
		return err
	}
	d.open = append(d.open, readFrame{kind: structFrame, schema: s})
	return nil
}

func (d *deserializer) ReadStructMember() (*smithy.Member, bool, error) {
	top := d.top()
	if top == nil || top.kind != structFrame {
		return nil, false, d.usage("ReadStructMember outside a structure")
	}
	if top.empty {
		d.pop()
		return nil, false, nil
	}
	m, err := d.nextMember(top.schema)
	if err != nil || m == nil {
		if err == nil {
			d.pop()
		}
		return nil, false, err
	}
	return m, true, nil
}

func (d *deserializer) ReadUnion(s *smithy.Schema) (*smithy.Member, error) {
	if err := d.enterObject(s); err != nil {
		return nil, err
	}
	at := d.dec.Offset()
	m, err := d.nextMember(s)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, &decodeError{Offset: at, Message: "Union has no member set; exactly one member must be set"}
	}
	d.open = append(d.open, readFrame{kind: unionFrame, schema: s, chosen: m})
	return m, nil
}

func (d *deserializer) CloseUnion() error {
	top := d.top()
	if top == nil || top.kind != unionFrame {
		return d.usage("CloseUnion outside a union")
	}
	at := d.dec.Offset()
	m, err := d.nextMember(top.schema)
	if err != nil {
		return err
	}
	switch m {
	case nil:
		d.pop()
		return nil
	case top.chosen:
		return &decodeError{Offset: at, Message: fmt.Sprintf("Union member %s appears more than once", m.Name)}
	default:
		return &decodeError{Offset: at, Message: fmt.Sprintf("Union has more than one member set: %s follows %s", m.Name, top.chosen.Name)}
	}
}

func (d *deserializer) ReadList(m *smithy.Member) error {
	k, err := d.dec.Peek()
	if err != nil {
		return err
	}
	if k != jsonx.Array {
		return d.containerError(k, "Start of structure or map found where not expected")
	}
	if err := d.dec.BeginArray(); err != nil {
		return err
	}
	d.open = append(d.open, readFrame{kind: listFrame, schema: m.Target, sparse: m.Target.Sparse})
	return nil
}

func (d *deserializer) ReadListElement() (bool, error) {
	top := d.top()
	if top == nil || top.kind != listFrame {
		return false, d.usage("ReadListElement outside a list")
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

func (d *deserializer) ReadMap(m *smithy.Member) error {
	k, err := d.dec.Peek()
	if err != nil {
		return err
	}
	if k != jsonx.Object {
		return d.containerError(k, "Unrecognized collection type java.util.Map<java.lang.String, "+mapValueClass(m.Target)+">")
	}
	if err := d.dec.BeginObject(); err != nil {
		return err
	}
	d.open = append(d.open, readFrame{kind: mapFrame, schema: m.Target, sparse: m.Target.Sparse})
	return nil
}

func (d *deserializer) ReadMapKey() (key string, ok bool, err error) {
	top := d.top()
	if top == nil || top.kind != mapFrame {
		return "", false, d.usage("ReadMapKey outside a map")
	}
	for {
		name, more, nerr := d.dec.NextName()
		if nerr != nil || !more {
			if nerr == nil {
				d.pop()
			}
			return "", false, nerr
		}
		if top.sparse {
			return name, true, nil
		}
		skipped, serr := d.skipNull()
		if serr != nil {
			return "", false, serr
		}
		if !skipped {
			return name, true, nil
		}
	}
}

func (d *deserializer) ReadString(m *smithy.Member) (string, error) {
	if err := d.scalar(m, jsonx.String); err != nil {
		return "", err
	}
	return d.dec.ReadString()
}

func (d *deserializer) ReadBool(m *smithy.Member) (bool, error) {
	k, err := d.dec.Peek()
	if err != nil {
		return false, err
	}
	if k == jsonx.String {
		// Coral converts the strings Jackson's boolean coercion accepts.
		at := d.dec.Offset()
		s, err := d.dec.ReadString()
		if err != nil {
			return false, err
		}
		switch strings.ToUpper(s) {
		case "TRUE", "1", "YES":
			return true, nil
		case "FALSE", "0", "NO":
			return false, nil
		default:
			return false, &decodeError{Offset: at, Message: "Unexpected token received from parser"}
		}
	}
	if err := d.scalar(m, jsonx.Bool); err != nil {
		return false, err
	}
	return d.dec.ReadBool()
}

func (d *deserializer) ReadInt8(m *smithy.Member) (int8, error) {
	v, err := d.readInt(m, 8)
	return int8(v), err //nolint:gosec // readInt checked the range for 8 bits
}

func (d *deserializer) ReadInt16(m *smithy.Member) (int16, error) {
	v, err := d.readInt(m, 16)
	return int16(v), err //nolint:gosec // readInt checked the range for 16 bits
}

func (d *deserializer) ReadInt32(m *smithy.Member) (int32, error) {
	v, err := d.readInt(m, 32)
	return int32(v), err //nolint:gosec // readInt checked the range for 32 bits
}

func (d *deserializer) ReadInt64(m *smithy.Member) (int64, error) {
	return d.readInt(m, 64)
}

func (d *deserializer) ReadFloat32(m *smithy.Member) (float32, error) {
	v, err := d.readFloat(m, 32)
	return float32(v), err
}

func (d *deserializer) ReadFloat64(m *smithy.Member) (float64, error) {
	return d.readFloat(m, 64)
}

func (d *deserializer) ReadBigInt(m *smithy.Member) (*big.Int, error) {
	at, n, err := d.number(m)
	if err != nil {
		return nil, err
	}
	dec, ok := parseDecimal(string(n))
	if !ok {
		return nil, outOfRange(at, n, m)
	}
	v, ok := dec.floor()
	if !ok {
		return nil, outOfRange(at, n, m)
	}
	return v, nil
}

func (d *deserializer) ReadBigDecimal(m *smithy.Member) (smithy.BigDecimal, error) {
	_, n, err := d.number(m)
	return n, err
}

func (d *deserializer) ReadTimestamp(m *smithy.Member) (time.Time, error) {
	format := timestampFormat(m)
	if format == smithy.EpochSeconds {
		return d.readEpochSeconds(m)
	}
	at := d.dec.Offset()
	s, err := d.ReadString(m)
	if err != nil {
		return time.Time{}, err
	}
	var t time.Time
	if format == smithy.HTTPDate {
		t, err = http.ParseTime(s)
	} else {
		t, err = time.Parse(time.RFC3339Nano, s)
	}
	if err != nil {
		return time.Time{}, &decodeError{Offset: at, Message: fmt.Sprintf("Value %s cannot be parsed as a %s timestamp", s, formatName(format))}
	}
	return t.Truncate(time.Millisecond).UTC(), nil
}

func (d *deserializer) ReadBlob(m *smithy.Member) ([]byte, error) {
	k, err := d.dec.Peek()
	if err != nil {
		return nil, err
	}
	at := d.dec.Offset()
	if k == jsonx.Bool || k == jsonx.Number {
		return nil, &decodeError{Offset: at, Message: "only base-64-encoded strings are convertible to bytes"}
	}
	if err := d.scalar(m, jsonx.String); err != nil {
		return nil, err
	}
	s, err := d.dec.ReadString()
	if err != nil {
		return nil, err
	}
	if len(s)%4 != 0 {
		return nil, &decodeError{Offset: at, Message: fmt.Sprintf("Base64 encoded length is expected a multiple of 4 bytes but found: %d", len(s))}
	}
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil {
		// Coral's text, misspelling included.
		return nil, &decodeError{Offset: at, Message: "Invalid last non-pad Base64 character dectected"}
	}
	return b, nil
}

func (d *deserializer) ReadDocument(*smithy.Member) (document.Value, error) {
	return d.dec.ReadValue()
}

func (d *deserializer) ReadNull(*smithy.Member) (bool, error) {
	return d.skipNull()
}

// ReadBody fails: awsJson models carry no @streaming member, so generated
// code for them never asks for a body.
func (d *deserializer) ReadBody(m *smithy.Member) (smithy.Body, error) {
	name := "a nil member"
	if m != nil {
		name = m.Name
	}
	return nil, d.usage("ReadBody for " + name + ": awsJson models no @streaming member")
}

func (d *deserializer) readEpochSeconds(m *smithy.Member) (time.Time, error) {
	at, n, err := d.number(m)
	if err != nil {
		return time.Time{}, err
	}
	dec, ok := parseDecimal(string(n))
	if !ok {
		return time.Time{}, outOfRange(at, n, m)
	}
	t, ok := dec.epochTime()
	if !ok {
		return time.Time{}, outOfRange(at, n, m)
	}
	return t, nil
}

// end checks that the body holds nothing after the input structure.
func (d *deserializer) end() error {
	if d.empty {
		return nil
	}
	return d.dec.End()
}

// nextMember advances to the next member of the open object that s
// declares and that is not null, skipping unknown members (__type in a
// union among them) and nulls, both of which mean absent. It returns nil,
// consuming the end of the object, when none is left.
func (d *deserializer) nextMember(s *smithy.Schema) (*smithy.Member, error) {
	for {
		name, ok, err := d.dec.NextName()
		if err != nil || !ok {
			return nil, err
		}
		m := memberNamed(s, name)
		if m == nil {
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

// memberNamed finds the member of s with the given wire name. awsJson
// ignores jsonName: members travel under their model names.
func memberNamed(s *smithy.Schema, name string) *smithy.Member {
	for _, m := range s.Members {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// enterObject consumes the start of the object holding a structure or
// union.
func (d *deserializer) enterObject(s *smithy.Schema) error {
	k, err := d.dec.Peek()
	if err != nil {
		return err
	}
	if k != jsonx.Object {
		msg := "Unexpected field type"
		if k == jsonx.Array {
			msg = "Unrecognized collection type class " + javaClass(s)
		}
		if len(d.open) == 0 {
			msg = fmt.Sprintf("The request body must be a JSON object, found a JSON %v for %s", k, s.ID.Name())
		}
		return d.mismatch(msg)
	}
	return d.dec.BeginObject()
}

func (d *deserializer) skipNull() (bool, error) {
	k, err := d.dec.Peek()
	if err != nil || k != jsonx.Null {
		return false, err
	}
	return true, d.dec.ReadNull()
}

func (d *deserializer) readInt(m *smithy.Member, bits int) (int64, error) {
	at, n, err := d.number(m)
	if err != nil {
		return 0, err
	}
	if v, err := strconv.ParseInt(string(n), 10, bits); err == nil {
		return v, nil
	}
	dec, ok := parseDecimal(string(n))
	if !ok {
		return 0, outOfRange(at, n, m)
	}
	v, ok := dec.int64In(bits)
	if !ok {
		return 0, outOfRange(at, n, m)
	}
	return v, nil
}

// readFloat accepts a number or one of the strings the Smithy JSON
// protocols use for non-finite values. A number beyond the float range
// becomes an infinity, as Java's Double.parseDouble makes it.
func (d *deserializer) readFloat(m *smithy.Member, bits int) (float64, error) {
	k, err := d.dec.Peek()
	if err != nil {
		return 0, err
	}
	if k == jsonx.String {
		at := d.dec.Offset()
		s, err := d.dec.ReadString()
		if err != nil {
			return 0, err
		}
		switch s {
		case "NaN":
			return math.NaN(), nil
		case "Infinity":
			return math.Inf(1), nil
		case "-Infinity":
			return math.Inf(-1), nil
		default:
			return 0, &decodeError{Offset: at, Message: "STRING_VALUE cannot be converted to " + javaType(m)}
		}
	}
	at, n, err := d.number(m)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseFloat(string(n), bits)
	if err != nil && !isRangeError(err) {
		// jsonx has already validated n against the JSON number grammar,
		// which ParseFloat accepts in full, so only a codec bug reaches
		// here; Decode answers it with InternalFailure, not the client.
		return 0, fmt.Errorf("awsjson: ParseFloat rejected the JSON number %s at byte %d: %w", n, at, err)
	}
	return v, nil
}

func isRangeError(err error) bool {
	ne, ok := err.(*strconv.NumError) //nolint:errorlint // ParseFloat returns *NumError unwrapped
	return ok && errors.Is(ne.Err, strconv.ErrRange)
}

// number consumes a number for m, returning its offset and text.
func (d *deserializer) number(m *smithy.Member) (int64, document.Number, error) {
	if err := d.scalar(m, jsonx.Number); err != nil {
		return 0, "", err
	}
	at := d.dec.Offset()
	n, err := d.dec.ReadNumber()
	return at, n, err
}

// scalar checks that the next value has kind want, else returns Coral's
// text for the mismatch (error-matrix.md 5.5 T2).
func (d *deserializer) scalar(m *smithy.Member, want jsonx.Kind) error {
	got, err := d.dec.Peek()
	if err != nil || got == want {
		return err
	}
	at := d.dec.Offset()
	switch got {
	case jsonx.Bool:
		v, err := d.dec.ReadBool()
		if err != nil {
			return err
		}
		return &decodeError{Offset: at, Message: strings.ToUpper(strconv.FormatBool(v)) + "_VALUE cannot be converted to " + javaType(m)}
	case jsonx.Number:
		n, err := d.dec.ReadNumber()
		if err != nil {
			return err
		}
		kind := "DECIMAL_VALUE"
		if dec, ok := parseDecimal(string(n)); ok && dec.integral() {
			kind = "NUMBER_VALUE"
		}
		return &decodeError{Offset: at, Message: kind + " cannot be converted to " + javaType(m)}
	case jsonx.String:
		return d.mismatch("STRING_VALUE cannot be converted to " + javaType(m))
	case jsonx.Array:
		return d.mismatch("Unrecognized collection type class " + javaClass(m.Target))
	case jsonx.Object:
		return d.mismatch("Start of structure or map found where not expected")
	case jsonx.Null:
		return d.mismatch("null cannot be converted to " + javaType(m))
	default:
		return d.mismatch("Unexpected field type")
	}
}

// containerError is the text for a value of kind got where a list or map
// is expected; wrongContainer covers an object for a list or an array for
// a map.
func (d *deserializer) containerError(got jsonx.Kind, wrongContainer string) error {
	msg := "Unexpected field type"
	if got == jsonx.Object || got == jsonx.Array {
		msg = wrongContainer
	}
	return d.mismatch(msg)
}

// mismatch reports a well-formed value of the wrong kind. It consumes the
// value first, so text that only starts like a value (nul, [1,) is reported
// as the syntax error it is.
func (d *deserializer) mismatch(msg string) error {
	at := d.dec.Offset()
	if err := d.dec.Skip(); err != nil {
		return err
	}
	return &decodeError{Offset: at, Message: msg}
}

func outOfRange(at int64, n document.Number, m *smithy.Member) error {
	return &decodeError{Offset: at, Message: fmt.Sprintf("Numeric value (%s) out of range of %s", n, javaType(m))}
}

// misuseError is a call sequence the generated Deserialize code never
// makes. It is a generator bug, not a client error, so Decode answers it
// with InternalFailure and keeps the text inside the emulator.
type misuseError struct {
	offset int64
	what   string
}

func (e *misuseError) Error() string {
	return fmt.Sprintf("awsjson: deserializer misuse at byte %d: %s; the generated Deserialize code is out of step with the codec", e.offset, e.what)
}

func (d *deserializer) usage(what string) error {
	return &misuseError{offset: d.dec.Offset(), what: what}
}

func (d *deserializer) top() *readFrame {
	if len(d.open) == 0 {
		return nil
	}
	return &d.open[len(d.open)-1]
}

func (d *deserializer) pop() { d.open = d.open[:len(d.open)-1] }

// javaType is the Java type Coral names in conversion errors.
func javaType(m *smithy.Member) string {
	return javaTypeOf(m.Target)
}

func javaTypeOf(s *smithy.Schema) string {
	switch s.Type {
	case smithy.TypeBoolean:
		return "Boolean"
	case smithy.TypeByte:
		return "Byte"
	case smithy.TypeShort:
		return "Short"
	case smithy.TypeInteger, smithy.TypeIntEnum:
		return "Integer"
	case smithy.TypeLong:
		return "Long"
	case smithy.TypeFloat:
		return "Float"
	case smithy.TypeDouble:
		return "Double"
	case smithy.TypeBigInteger:
		return "BigInteger"
	case smithy.TypeBigDecimal:
		return "BigDecimal"
	case smithy.TypeTimestamp:
		return "Date"
	case smithy.TypeBlob:
		return "ByteBuffer"
	case smithy.TypeString, smithy.TypeEnum:
		return "String"
	case smithy.TypeStructure, smithy.TypeUnion, smithy.TypeList, smithy.TypeMap, smithy.TypeDocument, smithy.TypeUnit:
		return s.ID.Name()
	default:
		return s.ID.Name()
	}
}

// javaClass is the qualified class Coral names for s when an array arrives
// where s is expected, or as a map's value class. A modeled aggregate is
// named by its Smithy namespace and name: Coral's own package adds the API
// version (com.amazonaws.dynamodb.v20120810), which the schema does not
// carry, so that part is understudy's approximation.
func javaClass(s *smithy.Schema) string {
	switch s.Type {
	case smithy.TypeBlob:
		return "java.nio.ByteBuffer"
	case smithy.TypeTimestamp:
		return "java.util.Date"
	case smithy.TypeBigInteger, smithy.TypeBigDecimal:
		return "java.math." + javaTypeOf(s)
	case smithy.TypeList:
		return "java.util.List"
	case smithy.TypeMap:
		return "java.util.Map"
	case smithy.TypeStructure, smithy.TypeUnion, smithy.TypeDocument, smithy.TypeUnit:
		return s.ID.Namespace() + "." + s.ID.Name()
	case smithy.TypeString, smithy.TypeEnum, smithy.TypeIntEnum, smithy.TypeBoolean, smithy.TypeByte,
		smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat, smithy.TypeDouble:
		return "java.lang." + javaTypeOf(s)
	default:
		return "java.lang." + javaTypeOf(s)
	}
}

// mapValueClass is the Java class of the values of the map s.
func mapValueClass(s *smithy.Schema) string {
	for _, m := range s.Members {
		if m.Name == "value" {
			return javaClass(m.Target)
		}
	}
	return "java.lang.Object"
}

// timestampFormat is the member's effective timestampFormat, defaulting to
// epoch seconds, the awsJson default.
func timestampFormat(m *smithy.Member) smithy.TimestampFormat {
	if m.TimestampFormat != smithy.TimestampProtocolDefault {
		return m.TimestampFormat
	}
	if m.Target != nil && m.Target.TimestampFormat != smithy.TimestampProtocolDefault {
		return m.Target.TimestampFormat
	}
	return smithy.EpochSeconds
}

func formatName(f smithy.TimestampFormat) string {
	switch f {
	case smithy.DateTime:
		return "date-time"
	case smithy.HTTPDate:
		return "http-date"
	case smithy.EpochSeconds, smithy.TimestampProtocolDefault:
		return "epoch-seconds"
	default:
		return "epoch-seconds"
	}
}
