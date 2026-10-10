package jsonx

import (
	"bytes"
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// Order is how an Encoder orders object members.
type Order uint8

// Order values.
const (
	// WireOrder writes members in call order: wire responses, where the
	// codec walks the schema in model order.
	WireOrder Order = iota + 1
	// SortedKeys writes every object's members sorted by name, compared
	// bytewise: snapshots and other committed documents, whose bytes must not
	// depend on the order code happened to write members in.
	SortedKeys
)

// UsageError reports a call sequence that cannot produce one JSON value,
// such as a value with no member name inside an object, or a value that
// JSON cannot represent, such as a NaN number.
type UsageError struct {
	// Call names the Encoder method.
	Call   string
	Reason string
}

func (e *UsageError) Error() string {
	return fmt.Sprintf("jsonx: Encoder.%s: %s", e.Call, e.Reason)
}

// Encoder writes one JSON value from a stream of calls, compact, with no
// insignificant whitespace. Strings are escaped as the package
// documentation describes. The first usage error is sticky: later calls do
// nothing and Bytes returns it.
type Encoder struct {
	order Order
	buf   []byte
	open  []container
	done  bool
	err   error
}

// container is an open object or array.
type container struct {
	object bool
	// count is the number of values (array) or members (object) written.
	count int
	// named is set between an object member's name and its value.
	named bool
	// start is the offset just after the opening bracket.
	start int
	// members are the offsets where each member starts, after its comma;
	// kept only for SortedKeys.
	members []int
	// names are the member names, parallel to members.
	names []string
}

// NewEncoder returns an Encoder that orders object members by order.
func NewEncoder(order Order) *Encoder {
	return &Encoder{order: order}
}

// Bytes returns the encoded value, or the first usage error. It fails when
// the value is incomplete.
func (e *Encoder) Bytes() ([]byte, error) {
	if e.err != nil {
		return nil, e.err
	}
	if !e.done {
		return nil, &UsageError{Call: "Bytes", Reason: "the value is incomplete: an object or array is still open, or nothing was written"}
	}
	return e.buf, nil
}

// BeginObject opens an object.
func (e *Encoder) BeginObject() {
	if !e.value("BeginObject") {
		return
	}
	e.buf = append(e.buf, '{')
	e.open = append(e.open, container{object: true, start: len(e.buf)})
}

// Name writes the name of the next member of the open object.
func (e *Encoder) Name(name string) {
	if e.err != nil {
		return
	}
	top := e.top()
	if top == nil || !top.object || top.named {
		e.fail("Name", "a member name belongs directly inside an object, before its value")
		return
	}
	if top.count > 0 {
		e.buf = append(e.buf, ',')
	}
	if e.order == SortedKeys {
		top.members = append(top.members, len(e.buf))
		top.names = append(top.names, name)
	}
	e.buf = AppendString(e.buf, name)
	e.buf = append(e.buf, ':')
	top.named = true
}

// EndObject closes the open object.
func (e *Encoder) EndObject() {
	if e.err != nil {
		return
	}
	top := e.top()
	if top == nil || !top.object || top.named {
		e.fail("EndObject", "no object is open, or its last member has no value")
		return
	}
	if e.order == SortedKeys {
		e.sortMembers(top)
	}
	e.buf = append(e.buf, '}')
	e.popped()
}

// BeginArray opens an array.
func (e *Encoder) BeginArray() {
	if !e.value("BeginArray") {
		return
	}
	e.buf = append(e.buf, '[')
	e.open = append(e.open, container{start: len(e.buf)})
}

// EndArray closes the open array.
func (e *Encoder) EndArray() {
	if e.err != nil {
		return
	}
	if top := e.top(); top == nil || top.object {
		e.fail("EndArray", "no array is open")
		return
	}
	e.buf = append(e.buf, ']')
	e.popped()
}

// Null writes null.
func (e *Encoder) Null() {
	if e.value("Null") {
		e.buf = append(e.buf, "null"...)
		e.close()
	}
}

// Bool writes true or false.
func (e *Encoder) Bool(v bool) {
	if e.value("Bool") {
		e.buf = strconv.AppendBool(e.buf, v)
		e.close()
	}
}

// String writes a string.
func (e *Encoder) String(v string) {
	if e.value("String") {
		e.buf = AppendString(e.buf, v)
		e.close()
	}
}

// Int writes an integer as plain digits.
func (e *Encoder) Int(v int64) {
	if e.value("Int") {
		e.buf = strconv.AppendInt(e.buf, v, 10)
		e.close()
	}
}

// Float writes a finite number in the shortest form that parses back to v:
// plain digits from 1e-6 up to 1e21, exponent form outside that range, as
// JavaScript and encoding/json print numbers. NaN and the infinities have
// no JSON form and are a usage error.
func (e *Encoder) Float(v float64) {
	e.float("Float", v, 64)
}

// Float32 writes a finite float32 the way Float writes a float64, in the
// shortest form that parses back to the same float32.
func (e *Encoder) Float32(v float32) {
	e.float("Float32", float64(v), 32)
}

// Number writes decimal text verbatim. Text that is not a JSON number is a
// usage error.
func (e *Encoder) Number(v document.Number) {
	if _, err := document.ParseNumber(string(v)); err != nil {
		e.fail("Number", err.Error())
		return
	}
	if e.value("Number") {
		e.buf = append(e.buf, v...)
		e.close()
	}
}

// Value writes a document tree. Map entries are written in order, or sorted
// under SortedKeys.
func (e *Encoder) Value(v document.Value) {
	switch v := v.(type) {
	case document.Null:
		e.Null()
	case document.Bool:
		e.Bool(bool(v))
	case document.Number:
		e.Number(v)
	case document.String:
		e.String(string(v))
	case document.List:
		e.BeginArray()
		for _, x := range v {
			e.Value(x)
		}
		e.EndArray()
	case document.Map:
		e.BeginObject()
		for _, entry := range v {
			e.Name(entry.Key)
			e.Value(entry.Value)
		}
		e.EndObject()
	case nil:
		e.fail("Value", "the document value is nil; use document.Null{} for null")
	}
}

func (e *Encoder) float(call string, v float64, bits int) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		e.fail(call, fmt.Sprintf("%v has no JSON number form", v))
		return
	}
	if e.value(call) {
		e.buf = appendFloat(e.buf, v, bits)
		e.close()
	}
}

// value prepares for one value: it checks that a value may come next and
// writes the comma an array element needs.
func (e *Encoder) value(call string) bool {
	if e.err != nil {
		return false
	}
	top := e.top()
	switch {
	case top == nil && e.done:
		e.fail(call, "the top-level value is already complete")
		return false
	case top == nil:
		return true
	case top.object && !top.named:
		e.fail(call, "a value inside an object needs a member name first")
		return false
	case !top.object && top.count > 0:
		e.buf = append(e.buf, ',')
	}
	return true
}

// popped records that the top container was closed, then counts it as a
// value in its parent.
func (e *Encoder) popped() {
	e.open = e.open[:len(e.open)-1]
	e.close()
}

// close records that a value is complete and counts it in its parent.
func (e *Encoder) close() {
	top := e.top()
	if top == nil {
		e.done = true
		return
	}
	top.count++
	top.named = false
}

func (e *Encoder) top() *container {
	if len(e.open) == 0 {
		return nil
	}
	return &e.open[len(e.open)-1]
}

// sortMembers reorders the members of top, which are complete, by name. A
// stable sort keeps duplicate names in call order.
func (e *Encoder) sortMembers(top *container) {
	n := len(top.members)
	if n < 2 {
		return
	}
	type segment struct {
		name string
		text []byte
	}
	segs := make([]segment, n)
	for i, from := range top.members {
		to := len(e.buf)
		if i+1 < n {
			to = top.members[i+1] - 1 // drop the comma before the next member
		}
		segs[i] = segment{name: top.names[i], text: bytes.Clone(e.buf[from:to])}
	}
	slices.SortStableFunc(segs, func(a, b segment) int { return cmp.Compare(a.name, b.name) })
	e.buf = e.buf[:top.start]
	for i, s := range segs {
		if i > 0 {
			e.buf = append(e.buf, ',')
		}
		e.buf = append(e.buf, s.text...)
	}
}

func (e *Encoder) fail(call, reason string) {
	if e.err == nil {
		e.err = &UsageError{Call: call, Reason: reason}
	}
}

// appendFloat formats like encoding/json: the shortest representation, with
// an exponent only for very small or very large magnitudes, and the
// exponent's leading zero dropped (1e-07 becomes 1e-7).
func appendFloat(b []byte, f float64, bits int) []byte {
	format := byte('f')
	if abs := math.Abs(f); abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	b = strconv.AppendFloat(b, f, format, -1, bits)
	if format == 'e' {
		n := len(b)
		if n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return b
}
