package cbor

import (
	"encoding/binary"
	"math"
	"slices"
	"strings"
	"time"
)

// Writer writes one CBOR data item from a stream of calls, the way a
// smithy.Serializer emits a response. Output is canonical for the subset:
// every array and map has a definite length, counted as it is written and
// put in front when it closes, so a structure need not know how many
// members are set; arguments take their smallest width; floats keep the
// width they were written with. A misuse, such as a map value without a
// key, is recorded and returned by Bytes, so call sites need not check
// each write.
type Writer struct {
	buf  []byte
	open []wframe
	// done is set once the top-level item has started.
	done bool
	err  error
}

// wframe is an open array or map.
type wframe struct {
	isMap bool
	// start is where the container's head goes when it closes.
	start int
	// count is the number of elements, or of map keys, written so far.
	count uint64
	// keyed is set when a map key waits for its value.
	keyed bool
}

// NewWriter returns an empty Writer.
func NewWriter() *Writer {
	return &Writer{}
}

// Bytes returns the encoded item, or the first misuse.
func (w *Writer) Bytes() ([]byte, error) {
	switch {
	case w.err != nil:
		return nil, w.err
	case len(w.open) != 0:
		return nil, &UsageError{Call: "Bytes", Reason: "an array or map is still open"}
	case !w.done:
		return nil, &UsageError{Call: "Bytes", Reason: "no data item was written"}
	default:
		return w.buf, nil
	}
}

// BeginArray opens an array.
func (w *Writer) BeginArray() {
	if w.item("BeginArray") {
		w.open = append(w.open, wframe{start: len(w.buf)})
	}
}

// EndArray closes the open array.
func (w *Writer) EndArray() { w.end(false, "EndArray") }

// BeginMap opens a map; each entry is Key then one value.
func (w *Writer) BeginMap() {
	if w.item("BeginMap") {
		w.open = append(w.open, wframe{isMap: true, start: len(w.buf)})
	}
}

// Key writes the next key of the open map.
func (w *Writer) Key(k string) {
	if w.err != nil {
		return
	}
	top := w.top()
	switch {
	case top == nil || !top.isMap:
		w.fail("Key", "no map is open")
	case top.keyed:
		w.fail("Key", "the previous key has no value")
	default:
		top.count++
		top.keyed = true
		w.buf = AppendText(w.buf, k)
	}
}

// EndMap closes the open map.
func (w *Writer) EndMap() { w.end(true, "EndMap") }

// Text writes a text string. Invalid UTF-8 bytes become U+FFFD, so the
// output is always well-formed.
func (w *Writer) Text(s string) {
	if w.item("Text") {
		w.buf = AppendText(w.buf, s)
	}
}

// ByteString writes a byte string.
func (w *Writer) ByteString(b []byte) {
	if w.item("ByteString") {
		w.buf = append(appendHead(w.buf, majorBytes, uint64(len(b))), b...)
	}
}

// Uint writes an unsigned integer.
func (w *Writer) Uint(v uint64) {
	if w.item("Uint") {
		w.buf = appendHead(w.buf, majorUint, v)
	}
}

// Int writes an integer: major type 0 when it is not negative, else 1.
func (w *Writer) Int(v int64) {
	if w.item("Int") {
		w.buf = appendInt(w.buf, v)
	}
}

// Bool writes false or true.
func (w *Writer) Bool(v bool) {
	if w.item("Bool") {
		w.buf = appendBool(w.buf, v)
	}
}

// Null writes null.
func (w *Writer) Null() {
	if w.item("Null") {
		w.buf = append(w.buf, majorSimple<<5|simpleNull)
	}
}

// Float32 writes a single-precision float, NaN payloads as given.
func (w *Writer) Float32(v float32) {
	if w.item("Float32") {
		w.buf = appendFloat32(w.buf, v)
	}
}

// Float64 writes a double-precision float, NaN payloads as given.
func (w *Writer) Float64(v float64) {
	if w.item("Float64") {
		w.buf = appendFloat64(w.buf, v)
	}
}

// Timestamp writes tag 1 wrapping t's epoch seconds as a double, at
// millisecond resolution. When the double nearest the instant lies just on
// the zero side of it, so that smithy-go's int64(f*1e3), which truncates
// toward zero, would read it back one millisecond off (wire-cbor.md 5.1),
// the next double away from zero is written instead; ReadTimestamp reads
// either back as the same instant.
func (w *Writer) Timestamp(t time.Time) {
	if w.item("Timestamp") {
		w.buf = appendFloat64(appendHead(w.buf, majorTag, 1), epochSeconds(t))
	}
}

// Value writes v whole.
func (w *Writer) Value(v Value) {
	if !w.item("Value") {
		return
	}
	b, err := appendValue(w.buf, v)
	if err != nil {
		w.err = err
		return
	}
	w.buf = b
}

// item prepares for the next data item; it reports false once the Writer
// has failed.
func (w *Writer) item(call string) bool {
	if w.err != nil {
		return false
	}
	top := w.top()
	switch {
	case top == nil && w.done:
		w.fail(call, "the top-level data item is already written")
		return false
	case top == nil:
		w.done = true
	case top.isMap && !top.keyed:
		w.fail(call, "a map value needs its key first")
		return false
	case top.isMap:
		top.keyed = false
	default:
		top.count++
	}
	return true
}

// end closes the open container, putting its head at its start.
func (w *Writer) end(isMap bool, call string) {
	if w.err != nil {
		return
	}
	top := w.top()
	switch {
	case top == nil || top.isMap != isMap:
		w.fail(call, "it does not match the open container")
		return
	case top.keyed:
		w.fail(call, "the last key has no value")
		return
	}
	major := majorArray
	if isMap {
		major = majorMap
	}
	w.buf = slices.Insert(w.buf, top.start, appendHead(nil, major, top.count)...)
	w.open = w.open[:len(w.open)-1]
}

func (w *Writer) top() *wframe {
	if len(w.open) == 0 {
		return nil
	}
	return &w.open[len(w.open)-1]
}

func (w *Writer) fail(call, reason string) {
	if w.err == nil {
		w.err = &UsageError{Call: call, Reason: reason}
	}
}

// Encode returns v in canonical form.
func Encode(v Value) ([]byte, error) {
	w := NewWriter()
	w.Value(v)
	return w.Bytes()
}

// Decode reads exactly one data item from data.
func Decode(data []byte) (Value, error) {
	r := NewReader(data)
	v, err := r.ReadValue()
	if err != nil {
		return nil, err
	}
	if err := r.End(); err != nil {
		return nil, err
	}
	return v, nil
}

func appendValue(b []byte, v Value) ([]byte, error) {
	switch v := v.(type) {
	case Uint:
		return appendHead(b, majorUint, uint64(v)), nil
	case NegInt:
		return appendHead(b, majorNegInt, uint64(v)), nil
	case Bytes:
		return append(appendHead(b, majorBytes, uint64(len(v))), v...), nil
	case Text:
		return AppendText(b, string(v)), nil
	case Array:
		b = appendHead(b, majorArray, uint64(len(v)))
		for _, e := range v {
			var err error
			if b, err = appendValue(b, e); err != nil {
				return nil, err
			}
		}
		return b, nil
	case Map:
		b = appendHead(b, majorMap, uint64(len(v)))
		for _, e := range v {
			var err error
			if b, err = appendValue(AppendText(b, e.Key), e.Value); err != nil {
				return nil, err
			}
		}
		return b, nil
	case Tag:
		return appendValue(appendHead(b, majorTag, v.Number), v.Content)
	case Bool:
		return appendBool(b, bool(v)), nil
	case Null:
		return append(b, majorSimple<<5|simpleNull), nil
	case Undefined:
		return append(b, majorSimple<<5|simpleUndefined), nil
	case Float32:
		return appendFloat32(b, float32(v)), nil
	case Float64:
		return appendFloat64(b, float64(v)), nil
	case nil:
		return nil, &UsageError{Call: "Value", Reason: "a nil Value; write Null{} for null"}
	}
	return nil, &UsageError{Call: "Value", Reason: "an unknown Value type"}
}

// AppendText appends s as a definite-length text string, invalid UTF-8
// replaced by U+FFFD, for codecs that build a small fixed body by hand.
func AppendText(b []byte, s string) []byte {
	s = strings.ToValidUTF8(s, "�")
	return append(appendHead(b, majorText, uint64(len(s))), s...)
}

// AppendMapHead appends the head of a definite-length map of n entries;
// the caller appends n keys and values after it.
func AppendMapHead(b []byte, n int) []byte {
	return appendHead(b, majorMap, uint64(max(n, 0)))
}

func appendInt(b []byte, v int64) []byte {
	if v >= 0 {
		return appendHead(b, majorUint, uint64(v))
	}
	return appendHead(b, majorNegInt, uint64(^v)) //nolint:gosec // v is negative, so ^v, which is -1-v, is not
}

func appendBool(b []byte, v bool) []byte {
	if v {
		return append(b, majorSimple<<5|simpleTrue)
	}
	return append(b, majorSimple<<5|simpleFalse)
}

func appendFloat32(b []byte, v float32) []byte {
	return binary.BigEndian.AppendUint32(append(b, majorSimple<<5|floatSingle), math.Float32bits(v))
}

func appendFloat64(b []byte, v float64) []byte {
	return binary.BigEndian.AppendUint64(append(b, majorSimple<<5|floatDouble), math.Float64bits(v))
}

// epochSeconds is t's epoch seconds at millisecond resolution, nudged one
// ulp away from zero when smithy-go's truncating read would otherwise be a
// millisecond off.
func epochSeconds(t time.Time) float64 {
	ms := t.UnixMilli()
	f := float64(ms) / 1000
	if int64(f*1000) != ms {
		if up := math.Nextafter(f, math.Copysign(math.Inf(1), f)); int64(up*1000) == ms {
			return up
		}
	}
	return f
}
