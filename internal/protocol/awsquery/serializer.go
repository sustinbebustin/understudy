package awsquery

import (
	"encoding/base64"
	"errors"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/form"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/protocol/xmlw"
)

// element is an element the serializer has opened. Its start tag is written
// only when content follows, so an element that stays empty is written as
// <Name/>: the empty Result, the empty list.
type element struct {
	name  string
	xmlns string
}

// writeFrame is an open container being written.
type writeFrame struct {
	kind frameKind
	// element reports whether the container has an element of its own: the
	// top-level structure (inside <OpResult>) and a flattened collection
	// have none.
	element bool
	// itemName names a list's items, or a map's entries.
	itemName string
	// keyName and valueName name a map entry's key and value.
	keyName, valueName string
	// inEntry is set from a map key until its value is written.
	inEntry bool
	// members counts the members written into a union.
	members int
}

// serializer implements smithy.Serializer for awsQuery XML bodies
// (wire-json-query.md 8.3): members under their xmlName or member name,
// lists as <member> items (or the list member's xmlName), maps as
// <entry><key/><value/></entry>, flattened collections as repeated
// elements named by the structure member, booleans lowercase, timestamps
// date-time with millisecond precision, blobs base64, IAM policy documents
// percent-encoded.
type serializer struct {
	w *xmlw.Writer
	// els are the open elements; the first flushed of them are written.
	els     []element
	flushed int
	open    []writeFrame
	err     error
}

func newSerializer(w *xmlw.Writer) *serializer {
	return &serializer{w: w}
}

func (s *serializer) WriteStruct(m *smithy.Member, _ *smithy.Schema) {
	if m == nil && len(s.open) == 0 && s.err == nil {
		s.open = append(s.open, writeFrame{kind: structFrame})
		return
	}
	if name, ok := s.begin(m); ok {
		s.start(name, "")
		s.open = append(s.open, writeFrame{kind: structFrame, element: true})
	}
}

func (s *serializer) CloseStruct() { s.close(structFrame, "CloseStruct") }

func (s *serializer) WriteUnion(m *smithy.Member, _ *smithy.Schema) {
	if name, ok := s.begin(m); ok {
		s.start(name, "")
		s.open = append(s.open, writeFrame{kind: unionFrame, element: true})
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
	flat := s.flattened(m)
	name, ok := s.begin(m)
	if !ok {
		return
	}
	if flat {
		s.open = append(s.open, writeFrame{kind: listFrame, itemName: name})
		return
	}
	s.start(name, "")
	s.open = append(s.open, writeFrame{kind: listFrame, element: true, itemName: elementName(listMember(m.Target), "member")})
}

func (s *serializer) CloseList() { s.close(listFrame, "CloseList") }

func (s *serializer) WriteMap(m *smithy.Member, _ int) {
	flat := s.flattened(m)
	name, ok := s.begin(m)
	if !ok {
		return
	}
	key, value := mapMembers(m.Target)
	f := writeFrame{kind: mapFrame, itemName: name, keyName: elementName(key, "key"), valueName: elementName(value, "value")}
	if !flat {
		s.start(name, "")
		f.element, f.itemName = true, "entry"
	}
	s.open = append(s.open, f)
}

func (s *serializer) WriteMapKey(key string) {
	if s.err != nil {
		return
	}
	top := s.top()
	if top == nil || top.kind != mapFrame || top.inEntry {
		s.fail("WriteMapKey outside a map, or before the previous entry's value")
		return
	}
	s.start(top.itemName, "")
	s.leaf(top.keyName, key)
	top.inEntry = true
}

func (s *serializer) CloseMap() { s.close(mapFrame, "CloseMap") }

func (s *serializer) WriteString(m *smithy.Member, v string) {
	if m != nil && m.Target != nil && m.Target.ID == policyDocument {
		v = form.Escape(v)
	}
	s.scalar(m, v)
}

// WriteBool writes true or false: botocore reads only the exact text true
// as true (wire-json-query.md 8.3).
func (s *serializer) WriteBool(m *smithy.Member, v bool) { s.scalar(m, strconv.FormatBool(v)) }

func (s *serializer) WriteInt8(m *smithy.Member, v int8) { s.WriteInt64(m, int64(v)) }

func (s *serializer) WriteInt16(m *smithy.Member, v int16) { s.WriteInt64(m, int64(v)) }

func (s *serializer) WriteInt32(m *smithy.Member, v int32) { s.WriteInt64(m, int64(v)) }

func (s *serializer) WriteInt64(m *smithy.Member, v int64) { s.scalar(m, strconv.FormatInt(v, 10)) }

func (s *serializer) WriteFloat32(m *smithy.Member, v float32) {
	s.scalar(m, formatFloat(float64(v), 32))
}

func (s *serializer) WriteFloat64(m *smithy.Member, v float64) { s.scalar(m, formatFloat(v, 64)) }

func (s *serializer) WriteBigInt(m *smithy.Member, v *big.Int) {
	if v == nil {
		s.fail("WriteBigInt with a nil value; an absent member is not written")
		return
	}
	s.scalar(m, v.String())
}

func (s *serializer) WriteBigDecimal(m *smithy.Member, v smithy.BigDecimal) { s.scalar(m, string(v)) }

// WriteTimestamp writes the member's format, date-time by default with
// millisecond precision and trailing zeros trimmed, as Go v2 writes it
// (wire-json-query.md 8.3): a whole second has no fraction, the form IAM
// sends (iam-sts-kms.md 1.2).
func (s *serializer) WriteTimestamp(m *smithy.Member, v time.Time) {
	if m == nil {
		s.fail("WriteTimestamp without its member")
		return
	}
	switch timestampFormat(m) {
	case smithy.HTTPDate:
		s.scalar(m, v.UTC().Format(http.TimeFormat))
	case smithy.EpochSeconds:
		s.scalar(m, formatEpochSeconds(v))
	case smithy.DateTime, smithy.TimestampProtocolDefault:
		s.scalar(m, v.Truncate(time.Millisecond).UTC().Format("2006-01-02T15:04:05.999Z"))
	}
}

func (s *serializer) WriteBlob(m *smithy.Member, v []byte) {
	s.scalar(m, base64.StdEncoding.EncodeToString(v))
}

// WriteDocument records a failure: awsQuery cannot carry a document.
func (s *serializer) WriteDocument(m *smithy.Member, _ document.Value) {
	s.fail("WriteDocument for " + memberName(m) + ": awsQuery has no document encoding")
}

// WriteNull writes nothing: XML has no null, and an absent element reads as
// an absent value.
func (s *serializer) WriteNull(m *smithy.Member) {
	if _, ok := s.begin(m); ok {
		s.finish()
	}
}

// WriteBody records a failure: awsQuery models carry no @streaming member.
func (s *serializer) WriteBody(m *smithy.Member, _ smithy.BodySource) {
	s.fail("WriteBody for " + memberName(m) + ": awsQuery models no @streaming member")
}

// scalar writes text as the element m names in the open container.
func (s *serializer) scalar(m *smithy.Member, text string) {
	if name, ok := s.begin(m); ok {
		s.leaf(name, text)
		s.finish()
	}
}

// begin prepares for a value held by m and returns its element name: the
// member's in a structure or union, the item name in a list, the value
// name in a map. It reports false once the serializer has failed.
func (s *serializer) begin(m *smithy.Member) (string, bool) {
	if s.err != nil {
		return "", false
	}
	top := s.top()
	switch {
	case top == nil:
		s.fail("a value was written outside the output structure")
	case top.kind == listFrame:
		return top.itemName, true
	case top.kind == mapFrame:
		if !top.inEntry {
			s.fail("a map value was written without its key")
			return "", false
		}
		return top.valueName, true
	case m == nil:
		s.fail("a structure or union member was written without its member")
	default:
		top.members++
		return elementName(m, m.Name), true
	}
	return "", false
}

// finish ends the value just written: in a map it closes the entry.
func (s *serializer) finish() {
	if top := s.top(); top != nil && top.kind == mapFrame && top.inEntry {
		top.inEntry = false
		s.end()
	}
}

// flattened reports whether m's collection is flattened: the trait applies
// only to a structure or union member.
func (s *serializer) flattened(m *smithy.Member) bool {
	top := s.top()
	return m != nil && m.XMLFlattened && top != nil && (top.kind == structFrame || top.kind == unionFrame)
}

func (s *serializer) close(kind frameKind, call string) {
	if s.err != nil {
		return
	}
	top := s.top()
	if top == nil || top.kind != kind || top.inEntry {
		s.fail(call + " does not match the open container")
		return
	}
	withElement := top.element
	s.open = s.open[:len(s.open)-1]
	if withElement {
		s.end()
	}
	s.finish()
}

func (s *serializer) top() *writeFrame {
	if len(s.open) == 0 {
		return nil
	}
	return &s.open[len(s.open)-1]
}

// start opens an element; its start tag waits for content. xmlns, when not
// empty, is declared as its default namespace.
func (s *serializer) start(name, xmlns string) {
	s.els = append(s.els, element{name: name, xmlns: xmlns})
}

// leaf writes <name>text</name>, or <name/> for the empty text.
func (s *serializer) leaf(name, text string) {
	s.start(name, "")
	if text != "" {
		s.flush(len(s.els))
		s.w.Text(text)
	}
	s.end()
}

// end closes the innermost open element: an end tag after content, or a
// self-closing tag when nothing was written into it.
func (s *serializer) end() {
	n := len(s.els)
	if s.flushed == n {
		s.w.End()
		s.flushed--
	} else {
		s.flush(n - 1)
		s.w.Empty(s.els[n-1].name)
	}
	s.els = s.els[:n-1]
}

// flush writes the start tags of the first n open elements.
func (s *serializer) flush(n int) {
	for ; s.flushed < n; s.flushed++ {
		el := s.els[s.flushed]
		if el.xmlns != "" {
			s.w.StartNS(el.name, el.xmlns)
		} else {
			s.w.Start(el.name)
		}
	}
}

// done checks that every container the generated code opened was closed,
// and returns the first failure.
func (s *serializer) done() error {
	if s.err == nil && len(s.open) > 0 {
		s.fail("a container is still open after Serialize")
	}
	return s.err
}

func (s *serializer) fail(reason string) {
	if s.err == nil {
		s.err = errors.New("awsquery: serializer misuse: " + reason + "; the generated Serialize code is out of step with the codec")
	}
}

// formatFloat writes v in plain decimal, the form Go v2 writes, and NaN,
// Infinity and -Infinity for the non-finite values (wire-json-query.md
// 8.3).
func formatFloat(v float64, bits int) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "Infinity"
	case math.IsInf(v, -1):
		return "-Infinity"
	default:
		return strconv.FormatFloat(v, 'f', -1, bits)
	}
}

// formatEpochSeconds writes t as seconds since the epoch with at most
// millisecond precision: 1398796238, 1398796238.12.
func formatEpochSeconds(t time.Time) string {
	ms := t.UnixMilli()
	sign := ""
	if ms < 0 {
		sign, ms = "-", -ms
	}
	out := sign + strconv.FormatInt(ms/1000, 10)
	if frac := ms % 1000; frac != 0 {
		out += strings.TrimRight("."+strconv.FormatInt(1000+frac, 10)[1:], "0")
	}
	return out
}
