// Package serdetest holds the test doubles for generated serde: Recorder
// implements smithy.Serializer by recording every call in order, and
// Replayer implements smithy.Deserializer by replaying recorded calls. A
// generated Serialize driven through a Recorder shows the exact member
// sequence a codec sees; replaying it into Deserialize proves the inverse
// without any wire format.
package serdetest

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// ErrReplay reports a Deserializer call the replayed sequence does not hold
// at that point.
var ErrReplay = errors.New("serdetest: replay mismatch")

// Op names one smithy.Serializer method, so a typo in a test's event list
// fails to compile instead of surfacing as a replay mismatch.
type Op uint8

// Op values, one per smithy.Serializer method.
const (
	OpWriteStruct Op = iota + 1
	OpCloseStruct
	OpWriteUnion
	OpCloseUnion
	OpWriteList
	OpCloseList
	OpWriteMap
	OpWriteMapKey
	OpCloseMap
	OpWriteString
	OpWriteBool
	OpWriteInt8
	OpWriteInt16
	OpWriteInt32
	OpWriteInt64
	OpWriteFloat32
	OpWriteFloat64
	OpWriteBigInt
	OpWriteBigDecimal
	OpWriteTimestamp
	OpWriteBlob
	OpWriteDocument
	OpWriteNull
	OpWriteBody
)

// String returns the method name: "WriteStruct".
func (o Op) String() string {
	switch o {
	case OpWriteStruct:
		return "WriteStruct"
	case OpCloseStruct:
		return "CloseStruct"
	case OpWriteUnion:
		return "WriteUnion"
	case OpCloseUnion:
		return "CloseUnion"
	case OpWriteList:
		return "WriteList"
	case OpCloseList:
		return "CloseList"
	case OpWriteMap:
		return "WriteMap"
	case OpWriteMapKey:
		return "WriteMapKey"
	case OpCloseMap:
		return "CloseMap"
	case OpWriteString:
		return "WriteString"
	case OpWriteBool:
		return "WriteBool"
	case OpWriteInt8:
		return "WriteInt8"
	case OpWriteInt16:
		return "WriteInt16"
	case OpWriteInt32:
		return "WriteInt32"
	case OpWriteInt64:
		return "WriteInt64"
	case OpWriteFloat32:
		return "WriteFloat32"
	case OpWriteFloat64:
		return "WriteFloat64"
	case OpWriteBigInt:
		return "WriteBigInt"
	case OpWriteBigDecimal:
		return "WriteBigDecimal"
	case OpWriteTimestamp:
		return "WriteTimestamp"
	case OpWriteBlob:
		return "WriteBlob"
	case OpWriteDocument:
		return "WriteDocument"
	case OpWriteNull:
		return "WriteNull"
	case OpWriteBody:
		return "WriteBody"
	default:
		return fmt.Sprintf("Op(%d)", uint8(o))
	}
}

// Event is one Serializer call: the method, the member it named (nil for a
// top-level structure, a map key or a close), the schema of a structure or
// union, the element count of a list or map, or the value written.
type Event struct {
	Op     Op
	Member *smithy.Member
	Schema *smithy.Schema
	N      int
	Value  any
}

// String renders an event as tests assert it: "WriteString QueueUrl u".
func (e Event) String() string {
	name := "<root>"
	if e.Member != nil {
		name = e.Member.Name
	}
	switch e.Op {
	case OpWriteStruct, OpWriteUnion:
		return fmt.Sprintf("%s %s %s", e.Op, name, e.Schema.ID)
	case OpCloseStruct, OpCloseUnion, OpCloseList, OpCloseMap:
		return e.Op.String()
	case OpWriteList, OpWriteMap:
		return fmt.Sprintf("%s %s %d", e.Op, name, e.N)
	case OpWriteMapKey:
		return fmt.Sprintf("%s %v", e.Op, e.Value)
	case OpWriteString, OpWriteBool, OpWriteInt8, OpWriteInt16, OpWriteInt32, OpWriteInt64,
		OpWriteFloat32, OpWriteFloat64, OpWriteBigInt, OpWriteBigDecimal, OpWriteTimestamp,
		OpWriteBlob, OpWriteDocument, OpWriteNull, OpWriteBody:
	}
	return fmt.Sprintf("%s %s %v", e.Op, name, e.Value)
}

// Recorder is a smithy.Serializer that records every call; the zero value
// is ready to use.
type Recorder struct {
	Events []Event
}

var _ smithy.Serializer = (*Recorder)(nil)

// Strings renders the recorded events.
func (r *Recorder) Strings() []string {
	out := make([]string, len(r.Events))
	for i, e := range r.Events {
		out[i] = e.String()
	}
	return out
}

// WriteStruct records the call.
func (r *Recorder) WriteStruct(m *smithy.Member, s *smithy.Schema) {
	r.add(Event{Op: OpWriteStruct, Member: m, Schema: s})
}

// CloseStruct records the call.
func (r *Recorder) CloseStruct() { r.add(Event{Op: OpCloseStruct}) }

// WriteUnion records the call.
func (r *Recorder) WriteUnion(m *smithy.Member, s *smithy.Schema) {
	r.add(Event{Op: OpWriteUnion, Member: m, Schema: s})
}

// CloseUnion records the call.
func (r *Recorder) CloseUnion() { r.add(Event{Op: OpCloseUnion}) }

// WriteList records the call.
func (r *Recorder) WriteList(m *smithy.Member, n int) {
	r.add(Event{Op: OpWriteList, Member: m, N: n})
}

// CloseList records the call.
func (r *Recorder) CloseList() { r.add(Event{Op: OpCloseList}) }

// WriteMap records the call.
func (r *Recorder) WriteMap(m *smithy.Member, n int) {
	r.add(Event{Op: OpWriteMap, Member: m, N: n})
}

// WriteMapKey records the call.
func (r *Recorder) WriteMapKey(key string) { r.add(Event{Op: OpWriteMapKey, Value: key}) }

// CloseMap records the call.
func (r *Recorder) CloseMap() { r.add(Event{Op: OpCloseMap}) }

// WriteString records the call.
func (r *Recorder) WriteString(m *smithy.Member, v string) { r.value(OpWriteString, m, v) }

// WriteBool records the call.
func (r *Recorder) WriteBool(m *smithy.Member, v bool) { r.value(OpWriteBool, m, v) }

// WriteInt8 records the call.
func (r *Recorder) WriteInt8(m *smithy.Member, v int8) { r.value(OpWriteInt8, m, v) }

// WriteInt16 records the call.
func (r *Recorder) WriteInt16(m *smithy.Member, v int16) { r.value(OpWriteInt16, m, v) }

// WriteInt32 records the call.
func (r *Recorder) WriteInt32(m *smithy.Member, v int32) { r.value(OpWriteInt32, m, v) }

// WriteInt64 records the call.
func (r *Recorder) WriteInt64(m *smithy.Member, v int64) { r.value(OpWriteInt64, m, v) }

// WriteFloat32 records the call.
func (r *Recorder) WriteFloat32(m *smithy.Member, v float32) { r.value(OpWriteFloat32, m, v) }

// WriteFloat64 records the call.
func (r *Recorder) WriteFloat64(m *smithy.Member, v float64) { r.value(OpWriteFloat64, m, v) }

// WriteBigInt records the call.
func (r *Recorder) WriteBigInt(m *smithy.Member, v *big.Int) { r.value(OpWriteBigInt, m, v) }

// WriteBigDecimal records the call.
func (r *Recorder) WriteBigDecimal(m *smithy.Member, v smithy.BigDecimal) {
	r.value(OpWriteBigDecimal, m, v)
}

// WriteTimestamp records the call.
func (r *Recorder) WriteTimestamp(m *smithy.Member, v time.Time) { r.value(OpWriteTimestamp, m, v) }

// WriteBlob records the call.
func (r *Recorder) WriteBlob(m *smithy.Member, v []byte) { r.value(OpWriteBlob, m, v) }

// WriteDocument records the call.
func (r *Recorder) WriteDocument(m *smithy.Member, v document.Value) {
	r.value(OpWriteDocument, m, v)
}

// WriteNull records the call.
func (r *Recorder) WriteNull(m *smithy.Member) { r.add(Event{Op: OpWriteNull, Member: m}) }

// WriteBody records the call.
func (r *Recorder) WriteBody(m *smithy.Member, b smithy.BodySource) { r.value(OpWriteBody, m, b) }

func (r *Recorder) add(e Event) { r.Events = append(r.Events, e) }

func (r *Recorder) value(op Op, m *smithy.Member, v any) {
	r.add(Event{Op: op, Member: m, Value: v})
}

// Replayer is a smithy.Deserializer that replays recorded events, failing
// with ErrReplay on any call the sequence does not hold next.
type Replayer struct {
	events []Event
	pos    int
}

var _ smithy.Deserializer = (*Replayer)(nil)

// NewReplayer replays events from the first.
func NewReplayer(events []Event) *Replayer { return &Replayer{events: events} }

// Done reports an error unless every event was consumed.
func (p *Replayer) Done() error {
	if p.pos == len(p.events) {
		return nil
	}
	return fmt.Errorf("%w: stopped at event %d of %d (%s)", ErrReplay, p.pos, len(p.events), p.events[p.pos])
}

// ReadStruct consumes WriteStruct of schema s.
func (p *Replayer) ReadStruct(s *smithy.Schema) error {
	e, err := p.take(OpWriteStruct, nil)
	if err == nil && e.Schema != s {
		return fmt.Errorf("%w: want structure %s, have %s", ErrReplay, s.ID, e.Schema.ID)
	}
	return err
}

// ReadStructMember returns the member of the next event, or false after
// consuming CloseStruct.
func (p *Replayer) ReadStructMember() (member *smithy.Member, ok bool, err error) {
	if p.closes(OpCloseStruct) {
		return nil, false, nil
	}
	e, ok := p.peek()
	if !ok || e.Member == nil {
		return nil, false, fmt.Errorf("%w: want a member, have %v", ErrReplay, e)
	}
	return e.Member, true, nil
}

// ReadUnion consumes WriteUnion of schema s and returns the next event's
// member.
func (p *Replayer) ReadUnion(s *smithy.Schema) (*smithy.Member, error) {
	e, err := p.take(OpWriteUnion, nil)
	if err != nil {
		return nil, err
	}
	if e.Schema != s {
		return nil, fmt.Errorf("%w: want union %s, have %s", ErrReplay, s.ID, e.Schema.ID)
	}
	next, ok := p.peek()
	if !ok || next.Member == nil {
		return nil, fmt.Errorf("%w: union %s has no member", ErrReplay, s.ID)
	}
	return next.Member, nil
}

// CloseUnion consumes CloseUnion.
func (p *Replayer) CloseUnion() error {
	_, err := p.take(OpCloseUnion, nil)
	return err
}

// ReadList consumes WriteList of member m.
func (p *Replayer) ReadList(m *smithy.Member) error {
	_, err := p.take(OpWriteList, m)
	return err
}

// ReadListElement reports whether an element follows, consuming CloseList
// when none does.
func (p *Replayer) ReadListElement() (bool, error) { return !p.closes(OpCloseList), nil }

// ReadMap consumes WriteMap of member m.
func (p *Replayer) ReadMap(m *smithy.Member) error {
	_, err := p.take(OpWriteMap, m)
	return err
}

// ReadMapKey consumes WriteMapKey, or CloseMap and returns false.
func (p *Replayer) ReadMapKey() (key string, ok bool, err error) {
	if p.closes(OpCloseMap) {
		return "", false, nil
	}
	e, err := p.take(OpWriteMapKey, nil)
	if err != nil {
		return "", false, err
	}
	key, ok = e.Value.(string)
	if !ok {
		return "", false, fmt.Errorf("%w: map key %v is a %T", ErrReplay, e.Value, e.Value)
	}
	return key, true, nil
}

// ReadString consumes WriteString of member m.
func (p *Replayer) ReadString(m *smithy.Member) (string, error) {
	return value[string](p, OpWriteString, m)
}

// ReadBool consumes WriteBool of member m.
func (p *Replayer) ReadBool(m *smithy.Member) (bool, error) { return value[bool](p, OpWriteBool, m) }

// ReadInt8 consumes WriteInt8 of member m.
func (p *Replayer) ReadInt8(m *smithy.Member) (int8, error) { return value[int8](p, OpWriteInt8, m) }

// ReadInt16 consumes WriteInt16 of member m.
func (p *Replayer) ReadInt16(m *smithy.Member) (int16, error) {
	return value[int16](p, OpWriteInt16, m)
}

// ReadInt32 consumes WriteInt32 of member m.
func (p *Replayer) ReadInt32(m *smithy.Member) (int32, error) {
	return value[int32](p, OpWriteInt32, m)
}

// ReadInt64 consumes WriteInt64 of member m.
func (p *Replayer) ReadInt64(m *smithy.Member) (int64, error) {
	return value[int64](p, OpWriteInt64, m)
}

// ReadFloat32 consumes WriteFloat32 of member m.
func (p *Replayer) ReadFloat32(m *smithy.Member) (float32, error) {
	return value[float32](p, OpWriteFloat32, m)
}

// ReadFloat64 consumes WriteFloat64 of member m.
func (p *Replayer) ReadFloat64(m *smithy.Member) (float64, error) {
	return value[float64](p, OpWriteFloat64, m)
}

// ReadBigInt consumes WriteBigInt of member m.
func (p *Replayer) ReadBigInt(m *smithy.Member) (*big.Int, error) {
	return value[*big.Int](p, OpWriteBigInt, m)
}

// ReadBigDecimal consumes WriteBigDecimal of member m.
func (p *Replayer) ReadBigDecimal(m *smithy.Member) (smithy.BigDecimal, error) {
	return value[smithy.BigDecimal](p, OpWriteBigDecimal, m)
}

// ReadTimestamp consumes WriteTimestamp of member m.
func (p *Replayer) ReadTimestamp(m *smithy.Member) (time.Time, error) {
	return value[time.Time](p, OpWriteTimestamp, m)
}

// ReadBlob consumes WriteBlob of member m.
func (p *Replayer) ReadBlob(m *smithy.Member) ([]byte, error) {
	return value[[]byte](p, OpWriteBlob, m)
}

// ReadDocument consumes WriteDocument of member m.
func (p *Replayer) ReadDocument(m *smithy.Member) (document.Value, error) {
	return value[document.Value](p, OpWriteDocument, m)
}

// ReadNull consumes WriteNull of member m when it is next.
func (p *Replayer) ReadNull(m *smithy.Member) (bool, error) {
	if e, ok := p.peek(); ok && e.Op == OpWriteNull && e.Member == m {
		p.pos++
		return true, nil
	}
	return false, nil
}

// ReadBody fails: a recorded WriteBody holds a smithy.BodySource, which is
// not the smithy.Body an input reads.
func (p *Replayer) ReadBody(m *smithy.Member) (smithy.Body, error) {
	return nil, fmt.Errorf("%w: ReadBody of %s: a recording holds no input body", ErrReplay, m.Name)
}

func (p *Replayer) peek() (Event, bool) {
	if p.pos >= len(p.events) {
		return Event{}, false
	}
	return p.events[p.pos], true
}

// take consumes the next event, which must be op naming member m (any
// member when m is nil).
func (p *Replayer) take(op Op, m *smithy.Member) (Event, error) {
	e, ok := p.peek()
	if !ok {
		return Event{}, fmt.Errorf("%w: %s past the end", ErrReplay, op)
	}
	if e.Op != op || (m != nil && e.Member != m) {
		return Event{}, fmt.Errorf("%w: want %s of %v, have %s", ErrReplay, op, memberName(m), e)
	}
	p.pos++
	return e, nil
}

func (p *Replayer) closes(op Op) bool {
	if e, ok := p.peek(); ok && e.Op == op {
		p.pos++
		return true
	}
	return false
}

func memberName(m *smithy.Member) string {
	if m == nil {
		return "<any>"
	}
	return m.Name
}

// value consumes a value event of op for member m and returns its value as
// T.
func value[T any](p *Replayer, op Op, m *smithy.Member) (T, error) {
	var zero T
	e, err := p.take(op, m)
	if err != nil {
		return zero, err
	}
	v, ok := e.Value.(T)
	if !ok {
		return zero, fmt.Errorf("%w: %s holds a %T", ErrReplay, op, e.Value)
	}
	return v, nil
}
