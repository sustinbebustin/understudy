package cbor

import (
	"bytes"
	"errors"
	"math"
	"strconv"
	"time"
	"unicode/utf8"
)

// Reader reads one CBOR data item as a stream of calls, the way a
// smithy.Deserializer walks a body: Peek says what comes next, Begin and
// Next calls walk arrays and maps of either length form, and scalar reads
// take any argument width. Every method returns an error rather than
// panicking, whatever the input: *MalformedError for input that is not
// well-formed CBOR of the subset, *MismatchError for an item of another
// type than asked (the Reader stays positioned at it), *RangeError for a
// value the requested Go type cannot hold, and *UsageError for a call the
// open containers do not allow: inside an open map, every item read
// follows NextKey, and inside an open array, NextElement.
type Reader struct {
	data []byte
	off  int
	open []frame
	// owed is the fewest input bytes the elements and entries still to come
	// in the open definite-length containers take, one per element and two
	// per entry, counting the containers ReadValue and Skip walk as well.
	owed uint64
}

// frame is an open array or map.
type frame struct {
	isMap      bool
	indefinite bool
	// remaining counts the elements, or the entries of a map, still to come
	// in a definite-length container.
	remaining uint64
	// atItem is set while NextElement or NextKey has placed the Reader at
	// an element or value of this container that is not yet read.
	atItem bool
}

// NewReader returns a Reader over data, which it does not modify.
func NewReader(data []byte) *Reader {
	return &Reader{data: data}
}

// Offset is the byte offset of the next data item.
func (r *Reader) Offset() int64 { return int64(r.off) }

// Peek returns the kind of the next data item without consuming it.
func (r *Reader) Peek() (Kind, error) {
	at := r.off
	h, err := r.readHead()
	r.off = at
	if err != nil {
		return 0, err
	}
	return h.kind(), nil
}

// BeginArray enters an array of definite or indefinite length.
func (r *Reader) BeginArray() error { return r.begin("BeginArray", majorArray, "an array") }

// NextElement reports whether the open array has another element,
// leaving the array when it has none.
func (r *Reader) NextElement() (bool, error) {
	top := r.top()
	switch {
	case top == nil || top.isMap:
		return false, &UsageError{Call: "NextElement", Reason: "no array is open"}
	case top.atItem:
		return false, &UsageError{Call: "NextElement", Reason: "the previous element was not read"}
	}
	more, err := r.next(top)
	if more {
		top.atItem = true
	}
	return more, err
}

// BeginMap enters a map of definite or indefinite length.
func (r *Reader) BeginMap() error { return r.begin("BeginMap", majorMap, "a map") }

// NextKey returns the next key of the open map, positioned at its value,
// or false, leaving the map, when no entry is left.
func (r *Reader) NextKey() (key string, ok bool, err error) {
	top := r.top()
	switch {
	case top == nil || !top.isMap:
		return "", false, &UsageError{Call: "NextKey", Reason: "no map is open"}
	case top.atItem:
		return "", false, &UsageError{Call: "NextKey", Reason: "the value of the previous key was not read"}
	}
	more, err := r.next(top)
	if err != nil || !more {
		return "", false, err
	}
	b, err := r.readKey(true)
	if err != nil {
		return "", false, err
	}
	top.atItem = true
	return string(b), true, nil
}

// ReadText reads a text string of definite or indefinite length.
func (r *Reader) ReadText() (string, error) {
	return read(r, "ReadText", func() (string, error) {
		b, err := r.readString(majorText, true)
		return string(b), err
	})
}

// ReadBytes reads a byte string of definite or indefinite length.
func (r *Reader) ReadBytes() ([]byte, error) {
	return read(r, "ReadBytes", func() ([]byte, error) {
		b, err := r.readString(majorBytes, true)
		if err != nil {
			return nil, err
		}
		return bytes.Clone(b), nil
	})
}

// ReadBool reads false or true.
func (r *Reader) ReadBool() (bool, error) { return read(r, "ReadBool", r.readBool) }

// ReadNull reads null or undefined, which Smithy treats alike.
func (r *Reader) ReadNull() error {
	_, err := read(r, "ReadNull", func() (struct{}, error) {
		at := r.off
		h, err := r.readHead()
		if err != nil {
			return struct{}{}, err
		}
		if k := h.kind(); k != KindNull && k != KindUndefined {
			return struct{}{}, r.mismatch(at, "null", h)
		}
		return struct{}{}, nil
	})
	return err
}

// ReadInt64 reads an unsigned or negative integer of any argument width.
func (r *Reader) ReadInt64() (int64, error) { return read(r, "ReadInt64", r.readInt64) }

// ReadFloat64 reads a number: an integer, or a half, single or double
// float, widened to float64.
func (r *Reader) ReadFloat64() (float64, error) { return read(r, "ReadFloat64", r.readFloat64) }

// ReadTimestamp reads tag 1 wrapping epoch seconds: an integer, or a
// float rounded to the millisecond. An untagged number is a mismatch, as
// the Smithy RPC v2 CBOR specification requires.
func (r *Reader) ReadTimestamp() (time.Time, error) {
	return read(r, "ReadTimestamp", r.readTimestamp)
}

// ReadValue reads the next data item whole.
func (r *Reader) ReadValue() (Value, error) {
	return read(r, "ReadValue", func() (Value, error) { return r.item(len(r.open), true) })
}

// Skip consumes the next data item, checking it as ReadValue does but
// without building it.
func (r *Reader) Skip() error {
	_, err := read(r, "Skip", func() (Value, error) { return r.item(len(r.open), false) })
	return err
}

// End checks that every container was left and no input follows the
// top-level data item.
func (r *Reader) End() error {
	if len(r.open) != 0 {
		return &UsageError{Call: "End", Reason: "an array or map is still open"}
	}
	if r.off != len(r.data) {
		return r.malformed(r.off, TrailingBytes)
	}
	return nil
}

func (r *Reader) readBool() (bool, error) {
	at := r.off
	h, err := r.readHead()
	if err != nil {
		return false, err
	}
	if h.kind() != KindBool {
		return false, r.mismatch(at, "a boolean", h)
	}
	return h.info == simpleTrue, nil
}

func (r *Reader) readInt64() (int64, error) {
	at := r.off
	h, err := r.readHead()
	if err != nil {
		return 0, err
	}
	switch h.major {
	case majorUint:
		if h.arg > math.MaxInt64 {
			return 0, &RangeError{Offset: int64(at), Value: strconv.FormatUint(h.arg, 10), Target: "int64"}
		}
		return int64(h.arg), nil
	case majorNegInt:
		if h.arg > math.MaxInt64 {
			return 0, &RangeError{Offset: int64(at), Value: negIntText(h.arg), Target: "int64"}
		}
		return -1 - int64(h.arg), nil
	default:
		return 0, r.mismatch(at, "an integer", h)
	}
}

func (r *Reader) readFloat64() (float64, error) {
	at := r.off
	h, err := r.readHead()
	if err != nil {
		return 0, err
	}
	switch {
	case h.major == majorUint:
		return float64(h.arg), nil
	case h.major == majorNegInt:
		return -1 - float64(h.arg), nil
	case h.kind() == KindFloat:
		return h.float(), nil
	default:
		return 0, r.mismatch(at, "a number", h)
	}
}

// Timestamps are limited to the years 0001 to 9999, the range RFC 3339
// and every SDK's date type share.
const (
	minSeconds = -62135596800 // 0001-01-01T00:00:00Z
	maxSeconds = 253402300799 // 9999-12-31T23:59:59Z
)

func (r *Reader) readTimestamp() (time.Time, error) {
	const want = "a tag 1 epoch-seconds timestamp"
	at := r.off
	h, err := r.readHead()
	if err != nil {
		return time.Time{}, err
	}
	if h.kind() != KindTag || h.arg != 1 {
		return time.Time{}, r.mismatch(at, want, h)
	}
	content := r.off
	c, err := r.readHead()
	if err != nil {
		return time.Time{}, err
	}
	switch {
	case c.major == majorUint:
		if c.arg > maxSeconds {
			return time.Time{}, timestampRange(at, strconv.FormatUint(c.arg, 10))
		}
		return time.Unix(int64(c.arg), 0).UTC(), nil
	case c.major == majorNegInt:
		if c.arg > -minSeconds-1 {
			return time.Time{}, timestampRange(at, negIntText(c.arg))
		}
		return time.Unix(-1-int64(c.arg), 0).UTC(), nil
	case c.kind() == KindFloat:
		f := c.float()
		ms := math.Round(f * 1000)
		if math.IsNaN(ms) || ms < minSeconds*1000 || ms > maxSeconds*1000+999 {
			return time.Time{}, timestampRange(at, strconv.FormatFloat(f, 'g', -1, 64))
		}
		return time.UnixMilli(int64(ms)).UTC(), nil
	default:
		// The error names the content, but the Reader goes back to the
		// tag, so a caller that skips the item skips all of it.
		err := r.mismatch(content, "epoch seconds inside tag 1", c)
		r.off = at
		return time.Time{}, err
	}
}

// readHead parses the head at the offset and moves past it, checking what
// a head alone can show: reserved additional information, an indefinite
// length where none is allowed, a stray break, an unsupported simple value
// and truncation.
func (r *Reader) readHead() (head, error) {
	at := r.off
	if at >= len(r.data) {
		return head{}, r.malformed(at, Truncated)
	}
	b := r.data[at]
	h := head{major: b >> 5, info: b & 0x1f}
	width := 0
	switch {
	case h.info < infoUint8:
		h.arg = uint64(h.info)
	case h.info <= infoUint64:
		width = 1 << (h.info - infoUint8)
	case h.info == infoIndefinite:
		switch h.major {
		case majorBytes, majorText, majorArray, majorMap:
			h.indefinite = true
		case majorSimple:
			return head{}, r.malformed(at, UnexpectedBreak)
		default:
			return head{}, r.malformed(at, IndefiniteNotAllowed)
		}
	default:
		return head{}, r.malformed(at, ReservedInfo)
	}
	if len(r.data)-at-1 < width {
		return head{}, r.malformed(at, Truncated)
	}
	for _, c := range r.data[at+1 : at+1+width] {
		h.arg = h.arg<<8 | uint64(c)
	}
	if h.major == majorSimple && (h.info < simpleFalse || h.info == infoUint8) {
		return head{}, r.malformed(at, UnsupportedSimple)
	}
	r.off = at + 1 + width
	return h, nil
}

// begin enters an array or map for call.
func (r *Reader) begin(call string, major byte, want string) error {
	if err := r.atItem(call); err != nil {
		return err
	}
	at := r.off
	h, err := r.readHead()
	if err != nil {
		return err
	}
	if h.major != major {
		return r.mismatch(at, want, h)
	}
	if len(r.open) >= MaxDepth {
		return r.malformed(at, TooDeep)
	}
	isMap := major == majorMap
	if !h.indefinite {
		if err := r.reserve(at, h.arg, isMap); err != nil {
			return err
		}
	}
	r.took()
	r.open = append(r.open, frame{isMap: isMap, indefinite: h.indefinite, remaining: h.arg})
	return nil
}

// next reports whether the open container has another element or entry,
// leaving it when it has none.
func (r *Reader) next(top *frame) (bool, error) {
	more, err := r.advance(top)
	if err == nil && !more {
		r.open = r.open[:len(r.open)-1]
	}
	return more, err
}

// advance reports whether container f has another element or entry,
// consuming its break when it has none.
func (r *Reader) advance(f *frame) (bool, error) {
	if !f.indefinite {
		if f.remaining == 0 {
			return false, nil
		}
		f.remaining--
		r.owed -= weight(f.isMap)
		return true, nil
	}
	if r.off >= len(r.data) {
		return false, r.malformed(r.off, Truncated)
	}
	if r.data[r.off] == breakByte {
		r.off++
		return false, nil
	}
	return true, nil
}

// reserve refuses a declared count that the input left cannot hold beside
// what the open containers still owe, and adds the count to what they owe:
// every element takes at least one byte, every map entry two. Checking
// against what is owed, not the input left alone, keeps nested containers
// from each claiming the same bytes, so a container may be sized from its
// count before any of it is read.
func (r *Reader) reserve(at int, count uint64, isMap bool) error {
	w := weight(isMap)
	if count > (r.rest()-min(r.owed, r.rest()))/w {
		return r.malformed(at, LengthExceedsInput)
	}
	r.owed += count * w
	return nil
}

// weight is the fewest input bytes an element (one) or a map entry (two)
// takes.
func weight(isMap bool) uint64 {
	if isMap {
		return 2
	}
	return 1
}

// readKey reads a map key, which must be a text string.
func (r *Reader) readKey(keep bool) ([]byte, error) {
	at := r.off
	h, err := r.readHead()
	if err != nil {
		return nil, err
	}
	if h.major != majorText {
		return nil, r.malformed(at, NonTextKey)
	}
	r.off = at
	return r.readString(majorText, keep)
}

// readString reads a byte or text string of either length form. A
// definite string comes back as a slice of the input; the chunks of an
// indefinite one are joined only when keep is set.
func (r *Reader) readString(major byte, keep bool) ([]byte, error) {
	at := r.off
	h, err := r.readHead()
	if err != nil {
		return nil, err
	}
	if h.major != major {
		want := "a text string"
		if major == majorBytes {
			want = "a byte string"
		}
		return nil, r.mismatch(at, want, h)
	}
	if !h.indefinite {
		return r.chunk(at, h)
	}
	out := []byte{}
	for {
		if r.off >= len(r.data) {
			return nil, r.malformed(r.off, Truncated)
		}
		if r.data[r.off] == breakByte {
			r.off++
			return out, nil
		}
		cat := r.off
		c, err := r.readHead()
		if err != nil {
			return nil, err
		}
		if c.major != major || c.indefinite {
			return nil, r.malformed(cat, InvalidChunk)
		}
		b, err := r.chunk(cat, c)
		if err != nil {
			return nil, err
		}
		if keep {
			out = append(out, b...)
		}
	}
}

// chunk takes the content of a definite string whose head h, at offset at,
// was just read. Each text chunk must be valid UTF-8 by itself (RFC 8949
// section 3.2.3).
func (r *Reader) chunk(at int, h head) ([]byte, error) {
	if h.arg > r.rest() {
		return nil, r.malformed(at, LengthExceedsInput)
	}
	end := r.off + int(h.arg) //nolint:gosec // checked against the input length above
	b := r.data[r.off:end]
	r.off = end
	if h.major == majorText && !utf8.Valid(b) {
		return nil, r.malformed(at, InvalidUTF8)
	}
	return b, nil
}

// item reads one data item enclosed by depth levels. Without keep it
// checks the item and returns nil for strings and containers.
func (r *Reader) item(depth int, keep bool) (Value, error) {
	at := r.off
	h, err := r.readHead()
	if err != nil {
		return nil, err
	}
	switch h.kind() {
	case KindUint:
		return Uint(h.arg), nil
	case KindNegInt:
		return NegInt(h.arg), nil
	case KindBytes, KindText:
		r.off = at
		return r.stringItem(h.major, keep)
	case KindArray, KindMap:
		return r.container(at, h, depth, keep)
	case KindTag:
		if depth >= MaxDepth {
			return nil, r.malformed(at, TooDeep)
		}
		c, err := r.item(depth+1, keep)
		if err != nil || !keep {
			return nil, err
		}
		return Tag{Number: h.arg, Content: c}, nil
	case KindBool:
		return Bool(h.info == simpleTrue), nil
	case KindNull:
		return Null{}, nil
	case KindUndefined:
		return Undefined{}, nil
	case KindFloat:
		if h.info == floatDouble {
			return Float64(h.float()), nil
		}
		return Float32(h.float32()), nil
	default:
		return nil, r.malformed(at, UnsupportedSimple)
	}
}

func (r *Reader) stringItem(major byte, keep bool) (Value, error) {
	b, err := r.readString(major, keep)
	if err != nil || !keep {
		return nil, err
	}
	if major == majorText {
		return Text(b), nil
	}
	return Bytes(bytes.Clone(b)), nil
}

// container reads the array or map whose head h, at offset at, was just
// read, enclosed by depth levels.
func (r *Reader) container(at int, h head, depth int, keep bool) (Value, error) {
	if depth >= MaxDepth {
		return nil, r.malformed(at, TooDeep)
	}
	f := frame{isMap: h.major == majorMap, indefinite: h.indefinite, remaining: h.arg}
	if !h.indefinite {
		if err := r.reserve(at, h.arg, f.isMap); err != nil {
			return nil, err
		}
	}
	if f.isMap {
		return r.mapItems(&f, depth+1, keep)
	}
	return r.arrayItems(&f, depth+1, keep)
}

// arrayItems and mapItems size their result from a definite count, which
// reserve has checked against the input left and what the open containers
// owe, so the counts of all open containers together never exceed the
// input.
func (r *Reader) arrayItems(f *frame, depth int, keep bool) (Value, error) {
	var out Array
	if keep {
		out = make(Array, 0, f.remaining)
	}
	for {
		more, err := r.advance(f)
		if err != nil || !more {
			if err != nil || !keep {
				return nil, err
			}
			return out, nil
		}
		v, err := r.item(depth, keep)
		if err != nil {
			return nil, err
		}
		if keep {
			out = append(out, v)
		}
	}
}

func (r *Reader) mapItems(f *frame, depth int, keep bool) (Value, error) {
	var out Map
	if keep {
		out = make(Map, 0, f.remaining)
	}
	for {
		more, err := r.advance(f)
		if err != nil || !more {
			if err != nil || !keep {
				return nil, err
			}
			return out, nil
		}
		k, err := r.readKey(keep)
		if err != nil {
			return nil, err
		}
		v, err := r.item(depth, keep)
		if err != nil {
			return nil, err
		}
		if keep {
			out = append(out, Entry{Key: string(k), Value: v})
		}
	}
}

// rest is the number of input bytes after the offset.
func (r *Reader) rest() uint64 {
	return uint64(len(r.data) - r.off) //nolint:gosec // the offset never passes the input length
}

// read runs f, which reads one data item for call, once atItem allows it.
// The item counts as taken unless f reports a mismatch, which leaves the
// Reader at the item for another read.
func read[T any](r *Reader, call string, f func() (T, error)) (T, error) {
	if err := r.atItem(call); err != nil {
		var zero T
		return zero, err
	}
	v, err := f()
	if mismatch, ok := errors.AsType[*MismatchError](err); !ok || mismatch == nil {
		r.took()
	}
	return v, err
}

// atItem checks that call may read the next data item: at the top level,
// or where NextKey or NextElement placed the Reader in the open container.
func (r *Reader) atItem(call string) error {
	top := r.top()
	switch {
	case top == nil || top.atItem:
		return nil
	case top.isMap:
		return &UsageError{Call: call, Reason: "the open map is waiting for NextKey"}
	default:
		return &UsageError{Call: call, Reason: "the open array is waiting for NextElement"}
	}
}

// took records that the item NextKey or NextElement reached was read.
func (r *Reader) took() {
	if top := r.top(); top != nil {
		top.atItem = false
	}
}

func (r *Reader) top() *frame {
	if len(r.open) == 0 {
		return nil
	}
	return &r.open[len(r.open)-1]
}

// mismatch reports the item with head h at offset at as not what the
// caller wanted, leaving the Reader positioned at it.
func (r *Reader) mismatch(at int, want string, h head) error {
	r.off = at
	e := &MismatchError{Offset: int64(at), Want: want, Got: h.kind()}
	if e.Got == KindTag {
		e.GotTag = h.arg
	}
	return e
}

func (r *Reader) malformed(at int, p Problem) error {
	return &MalformedError{Offset: int64(at), Problem: p}
}

func timestampRange(at int, value string) error {
	return &RangeError{Offset: int64(at), Value: value, Target: "a timestamp in the years 0001 to 9999"}
}

// negIntText is -1-n in decimal; the most negative value, -2^64, does not
// fit uint64 arithmetic.
func negIntText(n uint64) string {
	if n == math.MaxUint64 {
		return "-18446744073709551616"
	}
	return "-" + strconv.FormatUint(n+1, 10)
}
