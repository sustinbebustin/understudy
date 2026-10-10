package restjson

import (
	"encoding/base64"
	"math"
	"math/big"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/restbind"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

type writeFrame struct {
	kind    frameKind
	members int
}

// writer writes one restJson1 body document: members in model order under
// their jsonName, integers as plain digits, epoch-seconds timestamps with
// millisecond precision by default, padded base64 blobs, NaN and the
// infinities as strings. The first container write opens the root,
// whichever member holds it (the output itself or a payload member).
type writer struct {
	enc  *jsonx.Encoder
	open []writeFrame
	err  error
}

func newWriter() restbind.DocumentWriter {
	return &writer{enc: jsonx.NewEncoder(jsonx.WireOrder)}
}

func (w *writer) WriteStruct(m *smithy.Member, _ *smithy.Schema) {
	if w.member(m) {
		w.enc.BeginObject()
		w.open = append(w.open, writeFrame{kind: structFrame})
	}
}

func (w *writer) CloseStruct() { w.close(structFrame, "CloseStruct") }

func (w *writer) WriteUnion(m *smithy.Member, _ *smithy.Schema) {
	if w.member(m) {
		w.enc.BeginObject()
		w.open = append(w.open, writeFrame{kind: unionFrame})
	}
}

func (w *writer) CloseUnion() {
	if top := w.top(); top != nil && top.kind == unionFrame && top.members != 1 {
		w.fail("a union must have exactly one member written")
		return
	}
	w.close(unionFrame, "CloseUnion")
}

func (w *writer) WriteList(m *smithy.Member, _ int) {
	if w.member(m) {
		w.enc.BeginArray()
		w.open = append(w.open, writeFrame{kind: listFrame})
	}
}

func (w *writer) CloseList() { w.close(listFrame, "CloseList") }

func (w *writer) WriteMap(m *smithy.Member, _ int) {
	if w.member(m) {
		w.enc.BeginObject()
		w.open = append(w.open, writeFrame{kind: mapFrame})
	}
}

func (w *writer) WriteMapKey(key string) {
	if top := w.top(); top == nil || top.kind != mapFrame {
		w.fail("WriteMapKey outside a map")
		return
	}
	w.enc.Name(key)
}

func (w *writer) CloseMap() { w.close(mapFrame, "CloseMap") }

func (w *writer) WriteString(m *smithy.Member, v string) {
	if w.member(m) {
		w.enc.String(v)
	}
}

func (w *writer) WriteBool(m *smithy.Member, v bool) {
	if w.member(m) {
		w.enc.Bool(v)
	}
}

func (w *writer) WriteInt8(m *smithy.Member, v int8) { w.WriteInt64(m, int64(v)) }

func (w *writer) WriteInt16(m *smithy.Member, v int16) { w.WriteInt64(m, int64(v)) }

func (w *writer) WriteInt32(m *smithy.Member, v int32) { w.WriteInt64(m, int64(v)) }

func (w *writer) WriteInt64(m *smithy.Member, v int64) {
	if w.member(m) {
		w.enc.Int(v)
	}
}

func (w *writer) WriteFloat32(m *smithy.Member, v float32) {
	if !w.member(m) {
		return
	}
	if f := float64(v); math.IsNaN(f) || math.IsInf(f, 0) {
		w.enc.String(restbind.FormatFloat(f, 32))
		return
	}
	w.enc.Float32(v)
}

func (w *writer) WriteFloat64(m *smithy.Member, v float64) {
	if !w.member(m) {
		return
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		w.enc.String(restbind.FormatFloat(v, 64))
		return
	}
	w.enc.Float(v)
}

func (w *writer) WriteBigInt(m *smithy.Member, v *big.Int) {
	if v == nil {
		w.fail("WriteBigInt with a nil value; an absent member is not written")
		return
	}
	if w.member(m) {
		w.enc.Number(document.Number(v.String()))
	}
}

func (w *writer) WriteBigDecimal(m *smithy.Member, v smithy.BigDecimal) {
	if w.member(m) {
		w.enc.Number(v)
	}
}

func (w *writer) WriteTimestamp(m *smithy.Member, v time.Time) {
	if !w.member(m) {
		return
	}
	format := restbind.TimestampFormat(m, smithy.EpochSeconds)
	text := restbind.FormatTimestamp(v, format)
	if format == smithy.EpochSeconds {
		w.enc.Number(document.Number(text))
		return
	}
	w.enc.String(text)
}

func (w *writer) WriteBlob(m *smithy.Member, v []byte) {
	if w.member(m) {
		w.enc.String(base64.StdEncoding.EncodeToString(v))
	}
}

func (w *writer) WriteDocument(m *smithy.Member, v document.Value) {
	if w.member(m) {
		w.enc.Value(v)
	}
}

func (w *writer) WriteNull(m *smithy.Member) {
	if w.member(m) {
		w.enc.Null()
	}
}

// WriteBody fails: restbind takes a @streaming payload itself.
func (w *writer) WriteBody(*smithy.Member, smithy.BodySource) {
	w.fail("WriteBody on a body document")
}

// Bytes returns the document, or the first failure.
func (w *writer) Bytes() ([]byte, error) {
	if w.err != nil {
		return nil, w.err
	}
	return w.enc.Bytes()
}

// member prepares for a value held by m: inside a structure or union it
// writes m's wire name.
func (w *writer) member(m *smithy.Member) bool {
	if w.err != nil {
		return false
	}
	top := w.top()
	if top == nil || top.kind == listFrame || top.kind == mapFrame {
		return true
	}
	if m == nil {
		w.fail("a structure or union member was written without its member")
		return false
	}
	top.members++
	w.enc.Name(wireName(m))
	return true
}

func (w *writer) close(kind frameKind, call string) {
	top := w.top()
	if top == nil || top.kind != kind {
		w.fail(call + " does not match the open container")
		return
	}
	w.open = w.open[:len(w.open)-1]
	if kind == listFrame {
		w.enc.EndArray()
		return
	}
	w.enc.EndObject()
}

func (w *writer) top() *writeFrame {
	if len(w.open) == 0 {
		return nil
	}
	return &w.open[len(w.open)-1]
}

func (w *writer) fail(reason string) {
	if w.err == nil {
		w.err = &writerUsageError{reason: reason}
	}
}

// writerUsageError is generated code driving the writer in a way the open
// container does not allow: a bug.
type writerUsageError struct{ reason string }

func (e *writerUsageError) Error() string { return "restjson: serializer misuse: " + e.reason }
