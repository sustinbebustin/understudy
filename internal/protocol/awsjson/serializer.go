package awsjson

import (
	"encoding/base64"
	"errors"
	"math"
	"math/big"
	"net/http"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// writeFrame is an open container being written.
type writeFrame struct {
	kind frameKind
	// members counts the members written into a union.
	members int
}

// serializer implements smithy.Serializer for awsJson bodies: members in
// model order under their model names, integers as plain digits,
// timestamps as epoch seconds with millisecond precision, blobs as padded
// base64, non-finite floats as the strings NaN, Infinity and -Infinity.
type serializer struct {
	enc  *jsonx.Encoder
	open []writeFrame
	err  error
}

func newSerializer() *serializer {
	return &serializer{enc: jsonx.NewEncoder(jsonx.WireOrder)}
}

func (s *serializer) WriteStruct(m *smithy.Member, _ *smithy.Schema) {
	if s.member(m) {
		s.enc.BeginObject()
		s.open = append(s.open, writeFrame{kind: structFrame})
	}
}

func (s *serializer) CloseStruct() { s.close(structFrame, "CloseStruct") }

func (s *serializer) WriteUnion(m *smithy.Member, _ *smithy.Schema) {
	if s.member(m) {
		s.enc.BeginObject()
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
		s.enc.BeginArray()
		s.open = append(s.open, writeFrame{kind: listFrame})
	}
}

func (s *serializer) CloseList() { s.close(listFrame, "CloseList") }

func (s *serializer) WriteMap(m *smithy.Member, _ int) {
	if s.member(m) {
		s.enc.BeginObject()
		s.open = append(s.open, writeFrame{kind: mapFrame})
	}
}

func (s *serializer) WriteMapKey(key string) {
	if top := s.top(); top == nil || top.kind != mapFrame {
		s.fail("WriteMapKey outside a map")
		return
	}
	s.enc.Name(key)
}

func (s *serializer) CloseMap() { s.close(mapFrame, "CloseMap") }

func (s *serializer) WriteString(m *smithy.Member, v string) {
	if s.member(m) {
		s.enc.String(v)
	}
}

func (s *serializer) WriteBool(m *smithy.Member, v bool) {
	if s.member(m) {
		s.enc.Bool(v)
	}
}

func (s *serializer) WriteInt8(m *smithy.Member, v int8) { s.WriteInt64(m, int64(v)) }

func (s *serializer) WriteInt16(m *smithy.Member, v int16) { s.WriteInt64(m, int64(v)) }

func (s *serializer) WriteInt32(m *smithy.Member, v int32) { s.WriteInt64(m, int64(v)) }

func (s *serializer) WriteInt64(m *smithy.Member, v int64) {
	if s.member(m) {
		s.enc.Int(v)
	}
}

func (s *serializer) WriteFloat32(m *smithy.Member, v float32) {
	if !s.member(m) {
		return
	}
	if f := float64(v); math.IsNaN(f) || math.IsInf(f, 0) {
		s.enc.String(nonFinite(f))
		return
	}
	s.enc.Float32(v)
}

func (s *serializer) WriteFloat64(m *smithy.Member, v float64) {
	if !s.member(m) {
		return
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		s.enc.String(nonFinite(v))
		return
	}
	s.enc.Float(v)
}

func (s *serializer) WriteBigInt(m *smithy.Member, v *big.Int) {
	if v == nil {
		s.fail("WriteBigInt with a nil value; an absent member is not written")
		return
	}
	if s.member(m) {
		s.enc.Number(document.Number(v.String()))
	}
}

func (s *serializer) WriteBigDecimal(m *smithy.Member, v smithy.BigDecimal) {
	if s.member(m) {
		s.enc.Number(v)
	}
}

func (s *serializer) WriteTimestamp(m *smithy.Member, v time.Time) {
	if !s.member(m) {
		return
	}
	switch timestampFormat(m) {
	case smithy.DateTime:
		s.enc.String(v.Truncate(time.Millisecond).UTC().Format("2006-01-02T15:04:05.999Z"))
	case smithy.HTTPDate:
		s.enc.String(v.UTC().Format(http.TimeFormat))
	case smithy.EpochSeconds, smithy.TimestampProtocolDefault:
		s.enc.Number(document.Number(appendEpochSeconds(nil, v)))
	}
}

func (s *serializer) WriteBlob(m *smithy.Member, v []byte) {
	if s.member(m) {
		s.enc.String(base64.StdEncoding.EncodeToString(v))
	}
}

func (s *serializer) WriteDocument(m *smithy.Member, v document.Value) {
	if s.member(m) {
		s.enc.Value(v)
	}
}

func (s *serializer) WriteNull(m *smithy.Member) {
	if s.member(m) {
		s.enc.Null()
	}
}

// WriteBody records a failure: awsJson models carry no @streaming member, so
// generated code for them never hands over a body.
func (s *serializer) WriteBody(m *smithy.Member, _ smithy.BodySource) {
	name := "a nil member"
	if m != nil {
		name = m.Name
	}
	s.fail("WriteBody for " + name + ": awsJson models no @streaming member")
}

// bytes returns the body, or the first failure.
func (s *serializer) bytes() ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.enc.Bytes()
}

// member prepares for a value held by m: inside a structure or union it
// writes m's name. It reports false once the serializer has failed.
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
		s.enc.Name(m.Name)
	case top.kind == listFrame, top.kind == mapFrame:
		// A list element carries no name; a map value's key was written by
		// WriteMapKey.
	}
	return true
}

func (s *serializer) close(kind frameKind, call string) {
	top := s.top()
	if top == nil || top.kind != kind {
		s.fail(call + " does not match the open container")
		return
	}
	s.open = s.open[:len(s.open)-1]
	switch kind {
	case listFrame:
		s.enc.EndArray()
	case structFrame, unionFrame, mapFrame:
		s.enc.EndObject()
	}
}

func (s *serializer) top() *writeFrame {
	if len(s.open) == 0 {
		return nil
	}
	return &s.open[len(s.open)-1]
}

func (s *serializer) fail(reason string) {
	if s.err == nil {
		s.err = errors.New("awsjson: serializer misuse: " + reason)
	}
}

// nonFinite is the Smithy JSON string form of NaN or an infinity.
func nonFinite(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case v > 0:
		return "Infinity"
	default:
		return "-Infinity"
	}
}
