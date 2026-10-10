package rpcv2cbor

import (
	"fmt"
	"math/big"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// decodeError is a well-formed body the codec cannot map onto the input
// shape: an integer beyond its member's range, a union with no member or
// two, a type the protocol does not support. The request is answered with
// the service's malformed-request error.
type decodeError struct {
	Offset  int64
	Message string
}

func (e *decodeError) Error() string {
	return fmt.Sprintf("%s at byte %d", e.Message, e.Offset)
}

// memberError names the member whose value failed to decode.
type memberError struct {
	Member string
	Err    error
}

func (e *memberError) Error() string { return "member " + e.Member + ": " + describe(e.Err) }

func (e *memberError) Unwrap() error { return e.Err }

// misuseError is a call sequence the generated Deserialize code never
// makes. It is a generator bug, not a client error, so Decode answers it
// with InternalFailure and keeps the text inside the emulator.
type misuseError struct {
	offset int64
	what   string
}

func (e *misuseError) Error() string {
	return fmt.Sprintf("rpcv2cbor: deserializer misuse at byte %d: %s; the generated Deserialize code is out of step with the codec", e.offset, e.what)
}

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
	// holder names the member a list or map frame was opened for, so a
	// failed element read names it.
	holder string
}

// deserializer implements smithy.Deserializer over one rpcv2Cbor body
// (wire-cbor.md 3.2 and 3.6). It is lenient as a server must be: maps,
// lists and strings of either length form, integers of any width (range
// checked against the member), any number for a float or double, tag 1
// around any number for a timestamp; unknown members are skipped whatever
// they hold, and null or undefined members are absent.
type deserializer struct {
	r     *cbor.Reader
	empty bool
	open  []readFrame
}

func newDeserializer(body []byte) *deserializer {
	return &deserializer{r: cbor.NewReader(body), empty: len(body) == 0}
}

func (d *deserializer) ReadStruct(s *smithy.Schema) error {
	if len(d.open) == 0 && d.empty {
		d.open = append(d.open, readFrame{kind: structFrame, schema: s, empty: true})
		return nil
	}
	if err := d.r.BeginMap(); err != nil {
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
	at := d.r.Offset()
	if err := d.r.BeginMap(); err != nil {
		return nil, err
	}
	m, err := d.nextMember(s)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, &decodeError{Offset: at, Message: fmt.Sprintf("union %s has no member set; exactly one member must be set", s.ID.Name())}
	}
	d.open = append(d.open, readFrame{kind: unionFrame, schema: s, chosen: m})
	return m, nil
}

func (d *deserializer) CloseUnion() error {
	top := d.top()
	if top == nil || top.kind != unionFrame {
		return d.usage("CloseUnion outside a union")
	}
	at := d.r.Offset()
	m, err := d.nextMember(top.schema)
	if err != nil {
		return err
	}
	if m != nil {
		return &decodeError{Offset: at, Message: fmt.Sprintf("union %s has more than one member set: %s follows %s", top.schema.ID.Name(), m.Name, top.chosen.Name)}
	}
	d.pop()
	return nil
}

func (d *deserializer) ReadList(m *smithy.Member) error {
	if err := d.r.BeginArray(); err != nil {
		return d.wrap(m, err)
	}
	d.open = append(d.open, readFrame{kind: listFrame, schema: m.Target, sparse: m.Target.Sparse, holder: d.name(m)})
	return nil
}

func (d *deserializer) ReadListElement() (bool, error) {
	top := d.top()
	if top == nil || top.kind != listFrame {
		return false, d.usage("ReadListElement outside a list")
	}
	for {
		more, err := d.r.NextElement()
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
	if err := d.r.BeginMap(); err != nil {
		return d.wrap(m, err)
	}
	d.open = append(d.open, readFrame{kind: mapFrame, schema: m.Target, sparse: m.Target.Sparse, holder: d.name(m)})
	return nil
}

func (d *deserializer) ReadMapKey() (key string, ok bool, err error) {
	top := d.top()
	if top == nil || top.kind != mapFrame {
		return "", false, d.usage("ReadMapKey outside a map")
	}
	for {
		k, more, err := d.r.NextKey()
		if err != nil || !more {
			if err == nil {
				d.pop()
			}
			return "", false, err
		}
		if top.sparse {
			return k, true, nil
		}
		skipped, err := d.skipNull()
		if err != nil {
			return "", false, err
		}
		if !skipped {
			return k, true, nil
		}
	}
}

func (d *deserializer) ReadString(m *smithy.Member) (string, error) {
	v, err := d.r.ReadText()
	return v, d.wrap(m, err)
}

func (d *deserializer) ReadBool(m *smithy.Member) (bool, error) {
	v, err := d.r.ReadBool()
	return v, d.wrap(m, err)
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
	v, err := d.r.ReadFloat64()
	return float32(v), d.wrap(m, err)
}

func (d *deserializer) ReadFloat64(m *smithy.Member) (float64, error) {
	v, err := d.r.ReadFloat64()
	return v, d.wrap(m, err)
}

// ReadBigInt reads an integer of any size the subset carries: major types
// 0 and 1. Bignum tags (2 and 3) are not in the subset.
func (d *deserializer) ReadBigInt(m *smithy.Member) (*big.Int, error) {
	k, err := d.r.Peek()
	if err != nil {
		return nil, d.wrap(m, err)
	}
	if k != cbor.KindUint && k != cbor.KindNegInt {
		_, err := d.r.ReadInt64()
		return nil, d.wrap(m, err)
	}
	v, err := d.r.ReadValue()
	if err != nil {
		return nil, d.wrap(m, err)
	}
	switch v := v.(type) {
	case cbor.Uint:
		return new(big.Int).SetUint64(uint64(v)), nil
	case cbor.NegInt:
		n := new(big.Int).SetUint64(uint64(v))
		return n.Neg(n.Add(n, big.NewInt(1))), nil
	default:
		return nil, d.usage(fmt.Sprintf("Peek said integer but ReadValue returned %T", v))
	}
}

func (d *deserializer) ReadBigDecimal(m *smithy.Member) (smithy.BigDecimal, error) {
	return "", d.unsupported(m, "bigDecimal")
}

func (d *deserializer) ReadTimestamp(m *smithy.Member) (time.Time, error) {
	v, err := d.r.ReadTimestamp()
	return v, d.wrap(m, err)
}

func (d *deserializer) ReadBlob(m *smithy.Member) ([]byte, error) {
	v, err := d.r.ReadBytes()
	return v, d.wrap(m, err)
}

// ReadDocument fails: the Smithy RPC v2 CBOR specification does not
// support document types.
func (d *deserializer) ReadDocument(m *smithy.Member) (document.Value, error) {
	return nil, d.unsupported(m, "document")
}

func (d *deserializer) ReadNull(*smithy.Member) (bool, error) {
	return d.skipNull()
}

// ReadBody fails: rpcv2Cbor models carry no @streaming member, so generated
// code for them never asks for a body.
func (d *deserializer) ReadBody(m *smithy.Member) (smithy.Body, error) {
	name := "a nil member"
	if m != nil {
		name = m.Name
	}
	return nil, d.usage("ReadBody for " + name + ": rpcv2Cbor models no @streaming member")
}

// end checks that the body holds nothing after the input structure.
func (d *deserializer) end() error {
	if d.empty {
		return nil
	}
	return d.r.End()
}

// nextMember advances to the next member of the open map that s declares
// and that is not null, skipping unknown members (__type in a union among
// them) and nulls, both of which mean absent. It returns nil, consuming
// the end of the map, when none is left.
func (d *deserializer) nextMember(s *smithy.Schema) (*smithy.Member, error) {
	for {
		name, ok, err := d.r.NextKey()
		if err != nil || !ok {
			return nil, err
		}
		m := memberNamed(s, name)
		if m == nil {
			if err := d.r.Skip(); err != nil {
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

// memberNamed finds the member of s with the given wire name: rpcv2Cbor
// keys members by their model names.
func memberNamed(s *smithy.Schema, name string) *smithy.Member {
	for _, m := range s.Members {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// skipNull consumes a null or undefined value, reporting whether there was
// one.
func (d *deserializer) skipNull() (bool, error) {
	k, err := d.r.Peek()
	if err != nil || (k != cbor.KindNull && k != cbor.KindUndefined) {
		return false, err
	}
	return true, d.r.ReadNull()
}

// readInt reads an integer of any width and checks it fits the member's
// bits.
func (d *deserializer) readInt(m *smithy.Member, bits uint) (int64, error) {
	at := d.r.Offset()
	v, err := d.r.ReadInt64()
	if err != nil {
		return 0, d.wrap(m, err)
	}
	if bits < 64 {
		limit := int64(1) << (bits - 1)
		if v < -limit || v >= limit {
			return 0, d.wrap(m, &decodeError{Offset: at, Message: fmt.Sprintf("the value %d is out of range of %s", v, m.Target.Type)})
		}
	}
	return v, nil
}

func (d *deserializer) unsupported(m *smithy.Member, kind string) error {
	return d.wrap(m, &decodeError{Offset: d.r.Offset(), Message: kind + " values are not supported by rpcv2Cbor"})
}

// wrap names the member that held a value that failed to decode.
func (d *deserializer) wrap(m *smithy.Member, err error) error {
	if err == nil || m == nil {
		return err
	}
	return &memberError{Member: d.name(m), Err: err}
}

// name is how a failure names m: its own name in a structure or union,
// "<holder> element" for an element of a list or a value of a map.
func (d *deserializer) name(m *smithy.Member) string {
	if top := d.top(); top != nil && (top.kind == listFrame || top.kind == mapFrame) {
		return top.holder + " element"
	}
	return m.Name
}

func (d *deserializer) usage(what string) error {
	return &misuseError{offset: d.r.Offset(), what: what}
}

func (d *deserializer) top() *readFrame {
	if len(d.open) == 0 {
		return nil
	}
	return &d.open[len(d.open)-1]
}

func (d *deserializer) pop() { d.open = d.open[:len(d.open)-1] }
