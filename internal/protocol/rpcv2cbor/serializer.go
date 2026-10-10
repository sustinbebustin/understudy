package rpcv2cbor

import (
	"errors"
	"math"
	"math/big"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// frameKind is the kind of an open container.
type frameKind uint8

const (
	structFrame frameKind = iota + 1
	unionFrame
	listFrame
	mapFrame
)

// writeFrame is an open container being written.
type writeFrame struct {
	kind frameKind
	// members counts the members written into a union.
	members int
}

// serializer implements smithy.Serializer for rpcv2Cbor bodies
// (wire-cbor.md 3.6): structures and maps as definite-length maps keyed by
// member name, lists as definite-length arrays, integers in their smallest
// width, float as single and double as double (NaN in its canonical
// form), timestamps as tag 1 wrapping epoch seconds, blobs as byte
// strings, sparse nulls as null. Documents, bigDecimal values and
// bigInteger values beyond 64 bits have no rpcv2Cbor form in the subset
// and fail the response.
type serializer struct {
	w    *cbor.Writer
	open []writeFrame
	err  error
}

func newSerializer() *serializer {
	return &serializer{w: cbor.NewWriter()}
}

func (s *serializer) WriteStruct(m *smithy.Member, _ *smithy.Schema) {
	if s.member(m) {
		s.w.BeginMap()
		s.open = append(s.open, writeFrame{kind: structFrame})
	}
}

func (s *serializer) CloseStruct() { s.close(structFrame, "CloseStruct") }

func (s *serializer) WriteUnion(m *smithy.Member, _ *smithy.Schema) {
	if s.member(m) {
		s.w.BeginMap()
		s.open = append(s.open, writeFrame{kind: unionFrame})
	}
}

func (s *serializer) CloseUnion() {
	if top := s.top(); top != nil && top.kind == unionFrame && top.members != 1 {
		s.fail("a union must have exactly one member written")
		return
	}
	s.close(unionFrame, "CloseUnion")
}

func (s *serializer) WriteList(m *smithy.Member, _ int) {
	if s.member(m) {
		s.w.BeginArray()
		s.open = append(s.open, writeFrame{kind: listFrame})
	}
}

func (s *serializer) CloseList() { s.close(listFrame, "CloseList") }

func (s *serializer) WriteMap(m *smithy.Member, _ int) {
	if s.member(m) {
		s.w.BeginMap()
		s.open = append(s.open, writeFrame{kind: mapFrame})
	}
}

func (s *serializer) WriteMapKey(key string) {
	if top := s.top(); top == nil || top.kind != mapFrame {
		s.fail("WriteMapKey outside a map")
		return
	}
	s.w.Key(key)
}

func (s *serializer) CloseMap() { s.close(mapFrame, "CloseMap") }

func (s *serializer) WriteString(m *smithy.Member, v string) {
	if s.member(m) {
		s.w.Text(v)
	}
}

func (s *serializer) WriteBool(m *smithy.Member, v bool) {
	if s.member(m) {
		s.w.Bool(v)
	}
}

func (s *serializer) WriteInt8(m *smithy.Member, v int8) { s.WriteInt64(m, int64(v)) }

func (s *serializer) WriteInt16(m *smithy.Member, v int16) { s.WriteInt64(m, int64(v)) }

func (s *serializer) WriteInt32(m *smithy.Member, v int32) { s.WriteInt64(m, int64(v)) }

func (s *serializer) WriteInt64(m *smithy.Member, v int64) {
	if s.member(m) {
		s.w.Int(v)
	}
}

// The canonical NaNs of the corpus (RpcV2CborSupportsNaNFloatInputs):
// Go's math.NaN() sets a payload bit.
const (
	nan32Bits = 0x7fc00000
	nan64Bits = 0x7ff8000000000000
)

func (s *serializer) WriteFloat32(m *smithy.Member, v float32) {
	if !s.member(m) {
		return
	}
	if math.IsNaN(float64(v)) {
		v = math.Float32frombits(nan32Bits)
	}
	s.w.Float32(v)
}

func (s *serializer) WriteFloat64(m *smithy.Member, v float64) {
	if !s.member(m) {
		return
	}
	if math.IsNaN(v) {
		v = math.Float64frombits(nan64Bits)
	}
	s.w.Float64(v)
}

func (s *serializer) WriteBigInt(m *smithy.Member, v *big.Int) {
	switch {
	case v == nil:
		s.fail("WriteBigInt with a nil value; an absent member is not written")
	case v.IsInt64():
		s.WriteInt64(m, v.Int64())
	case v.IsUint64():
		if s.member(m) {
			s.w.Uint(v.Uint64())
		}
	default:
		s.fail("WriteBigInt of a value beyond 64 bits: bignum tags are not in the rpcv2Cbor subset")
	}
}

func (s *serializer) WriteBigDecimal(*smithy.Member, smithy.BigDecimal) {
	s.fail("WriteBigDecimal: decimal fraction tags are not in the rpcv2Cbor subset")
}

func (s *serializer) WriteTimestamp(m *smithy.Member, v time.Time) {
	if s.member(m) {
		s.w.Timestamp(v)
	}
}

func (s *serializer) WriteBlob(m *smithy.Member, v []byte) {
	if s.member(m) {
		s.w.ByteString(v)
	}
}

// WriteDocument fails: the Smithy RPC v2 CBOR specification does not
// support document types.
func (s *serializer) WriteDocument(*smithy.Member, document.Value) {
	s.fail("WriteDocument: rpcv2Cbor does not support document types")
}

func (s *serializer) WriteNull(m *smithy.Member) {
	if s.member(m) {
		s.w.Null()
	}
}

// WriteBody fails: rpcv2Cbor models carry no @streaming blob member.
func (s *serializer) WriteBody(m *smithy.Member, _ smithy.BodySource) {
	name := "a nil member"
	if m != nil {
		name = m.Name
	}
	s.fail("WriteBody for " + name + ": rpcv2Cbor models no @streaming member")
}

// bytes returns the body, or the first failure.
func (s *serializer) bytes() ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.w.Bytes()
}

// member prepares for a value held by m: inside a structure or union it
// writes m's name as the key. It reports false once the serializer has
// failed.
func (s *serializer) member(m *smithy.Member) bool {
	if s.err != nil {
		return false
	}
	top := s.top()
	switch {
	case top == nil:
		if m != nil {
			s.fail("the top-level value must be the output or error structure, written with a nil member")
			return false
		}
	case top.kind == structFrame || top.kind == unionFrame:
		if m == nil {
			s.fail("a structure or union member was written without its member")
			return false
		}
		top.members++
		s.w.Key(m.Name)
	case top.kind == listFrame, top.kind == mapFrame:
		// A list element carries no key; a map value's key was written by
		// WriteMapKey.
	}
	return true
}

func (s *serializer) close(kind frameKind, call string) {
	top := s.top()
	if s.err != nil {
		return
	}
	if top == nil || top.kind != kind {
		s.fail(call + " does not match the open container")
		return
	}
	s.open = s.open[:len(s.open)-1]
	if kind == listFrame {
		s.w.EndArray()
		return
	}
	s.w.EndMap()
}

func (s *serializer) top() *writeFrame {
	if len(s.open) == 0 {
		return nil
	}
	return &s.open[len(s.open)-1]
}

func (s *serializer) fail(reason string) {
	if s.err == nil {
		s.err = errors.New("rpcv2cbor: cannot encode the output: " + reason)
	}
}
