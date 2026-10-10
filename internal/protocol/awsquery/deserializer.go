package awsquery

import (
	"encoding/base64"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/form"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// decodeError is a form value the codec cannot map onto its member: text
// that is not a number, a boolean, a timestamp or base64, or a union with
// no member or two. Decode answers it with the service's malformed-request
// error, carrying Message.
type decodeError struct {
	// Key is the form key of the offending value.
	Key     string
	Message string
}

func (e *decodeError) Error() string {
	return "awsquery: the form does not match the input shape at " + e.Key + ": " + e.Message
}

// misuseError is a call sequence the generated Deserialize code never
// makes. It is a generator bug, not a client error, so Decode answers it
// with InternalFailure and keeps the text inside the emulator.
type misuseError struct {
	what string
}

func (e *misuseError) Error() string {
	return "awsquery: deserializer misuse: " + e.what + "; the generated Deserialize code is out of step with the codec"
}

// frameKind is the kind of an open container.
type frameKind uint8

const (
	structFrame frameKind = iota + 1
	unionFrame
	listFrame
	mapFrame
)

// readFrame is a container being read from the form tree.
type readFrame struct {
	kind   frameKind
	schema *smithy.Schema
	node   *form.Node
	// next is the next member index of a structure, or the next item of a
	// list or map.
	next int
	// items are a list's elements or a map's entries, in index order.
	items []*form.Node
	// keyName and valueName are a map entry's key and value segments.
	keyName, valueName string
	// chosen is the member a union frame was opened for.
	chosen *smithy.Member
}

// deserializer implements smithy.Deserializer over a parsed form. Members
// are found by their Query key (wire-json-query.md 7.2): xmlName, else the
// member name; lists under member.N (or the list member's xmlName), or
// directly under N when flattened; maps under entry.N.key and
// entry.N.value (or the key and value members' xmlNames), or N.key when
// flattened.
type deserializer struct {
	root *form.Node
	open []readFrame
	// value is the node the next Read call reads: set by ReadStructMember,
	// ReadUnion, ReadListElement and ReadMapKey.
	value *form.Node
}

func newDeserializer(v *form.Values) *deserializer {
	return &deserializer{root: v.Root()}
}

func (d *deserializer) ReadStruct(s *smithy.Schema) error {
	node := d.root
	if len(d.open) > 0 {
		if d.value == nil {
			return d.usage("ReadStruct with no member value positioned")
		}
		node = d.value
	}
	d.open = append(d.open, readFrame{kind: structFrame, schema: s, node: node})
	d.value = nil
	return nil
}

func (d *deserializer) ReadStructMember() (*smithy.Member, bool, error) {
	top := d.top()
	if top == nil || top.kind != structFrame {
		return nil, false, d.usage("ReadStructMember outside a structure")
	}
	for top.next < len(top.schema.Members) {
		m := top.schema.Members[top.next]
		top.next++
		if n, ok := present(top.node, m); ok {
			d.value = n
			return m, true, nil
		}
	}
	d.pop()
	return nil, false, nil
}

func (d *deserializer) ReadUnion(s *smithy.Schema) (*smithy.Member, error) {
	node, err := d.take("ReadUnion")
	if err != nil {
		return nil, err
	}
	for _, m := range s.Members {
		if n, ok := present(node, m); ok {
			d.open = append(d.open, readFrame{kind: unionFrame, schema: s, node: node, chosen: m})
			d.value = n
			return m, nil
		}
	}
	return nil, &decodeError{Key: form.Excerpt(node.Key()), Message: "union " + s.ID.Name() + " has no member set; exactly one member must be set"}
}

func (d *deserializer) CloseUnion() error {
	top := d.top()
	if top == nil || top.kind != unionFrame {
		return d.usage("CloseUnion outside a union")
	}
	for _, m := range top.schema.Members {
		if _, ok := present(top.node, m); ok && m != top.chosen {
			return &decodeError{Key: form.Excerpt(top.node.Key()), Message: fmt.Sprintf("union %s has more than one member set: %s and %s", top.schema.ID.Name(), top.chosen.Name, m.Name)}
		}
	}
	d.pop()
	return nil
}

func (d *deserializer) ReadList(m *smithy.Member) error {
	node, err := d.take("ReadList")
	if err != nil {
		return err
	}
	items := node
	if !d.flattened(m) {
		items, _ = node.Child(elementName(listMember(m.Target), "member"))
	}
	f := readFrame{kind: listFrame, schema: m.Target, node: node}
	if items != nil {
		f.items = items.Indexed()
	}
	d.open = append(d.open, f)
	return nil
}

func (d *deserializer) ReadListElement() (bool, error) {
	top := d.top()
	if top == nil || top.kind != listFrame {
		return false, d.usage("ReadListElement outside a list")
	}
	if top.next == len(top.items) {
		d.pop()
		return false, nil
	}
	d.value = top.items[top.next]
	top.next++
	return true, nil
}

func (d *deserializer) ReadMap(m *smithy.Member) error {
	node, err := d.take("ReadMap")
	if err != nil {
		return err
	}
	entries := node
	if !d.flattened(m) {
		entries, _ = node.Child("entry")
	}
	key, value := mapMembers(m.Target)
	f := readFrame{kind: mapFrame, schema: m.Target, node: node, keyName: elementName(key, "key"), valueName: elementName(value, "value")}
	if entries != nil {
		f.items = entries.Indexed()
	}
	d.open = append(d.open, f)
	return nil
}

// ReadMapKey returns the next entry's key. An entry missing its key or its
// value is skipped: the form has no null, and a dense map holds none.
func (d *deserializer) ReadMapKey() (key string, ok bool, err error) {
	top := d.top()
	if top == nil || top.kind != mapFrame {
		return "", false, d.usage("ReadMapKey outside a map")
	}
	for top.next < len(top.items) {
		entry := top.items[top.next]
		top.next++
		keyNode, hasKey := entry.Child(top.keyName)
		valueNode, hasValue := entry.Child(top.valueName)
		if !hasKey || !hasValue {
			continue
		}
		if key, ok := keyNode.Value(); ok {
			d.value = valueNode
			return key, true, nil
		}
	}
	d.pop()
	return "", false, nil
}

func (d *deserializer) ReadString(*smithy.Member) (string, error) {
	n, err := d.take("ReadString")
	if err != nil {
		return "", err
	}
	v, _ := n.Value()
	return v, nil
}

// ReadBool accepts true and false in any case.
func (d *deserializer) ReadBool(m *smithy.Member) (bool, error) {
	n, v, err := d.scalar("ReadBool")
	if err != nil {
		return false, err
	}
	switch strings.ToLower(v) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, invalid(n, m, v, "Must be true or false")
	}
}

func (d *deserializer) ReadInt8(m *smithy.Member) (int8, error) {
	v, err := d.readInt(m, 8)
	return int8(v), err //nolint:gosec // readInt parsed v as 8 bits
}

func (d *deserializer) ReadInt16(m *smithy.Member) (int16, error) {
	v, err := d.readInt(m, 16)
	return int16(v), err //nolint:gosec // readInt parsed v as 16 bits
}

func (d *deserializer) ReadInt32(m *smithy.Member) (int32, error) {
	v, err := d.readInt(m, 32)
	return int32(v), err //nolint:gosec // readInt parsed v as 32 bits
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
	n, v, err := d.scalar("ReadBigInt")
	if err != nil {
		return nil, err
	}
	x, ok := new(big.Int).SetString(v, 10)
	if !ok || len(v) > maxIntegerDigits {
		return nil, invalid(n, m, v, "Must be an integer of at most "+strconv.Itoa(maxIntegerDigits)+" digits")
	}
	return x, nil
}

func (d *deserializer) ReadBigDecimal(m *smithy.Member) (smithy.BigDecimal, error) {
	n, v, err := d.scalar("ReadBigDecimal")
	if err != nil {
		return "", err
	}
	x, perr := document.ParseNumber(v)
	if perr != nil {
		return "", invalid(n, m, v, "Must be a decimal number")
	}
	return x, nil
}

// ReadTimestamp reads the member's format, date-time by default, with
// fractional seconds and offsets accepted (wire-json-query.md 7.3).
func (d *deserializer) ReadTimestamp(m *smithy.Member) (time.Time, error) {
	n, v, err := d.scalar("ReadTimestamp")
	if err != nil {
		return time.Time{}, err
	}
	var t time.Time
	var perr error
	switch timestampFormat(m) {
	case smithy.HTTPDate:
		t, perr = http.ParseTime(v)
	case smithy.EpochSeconds:
		t, perr = parseEpochSeconds(v)
	case smithy.DateTime, smithy.TimestampProtocolDefault:
		t, perr = time.Parse(time.RFC3339Nano, v)
	}
	if perr != nil {
		return time.Time{}, invalid(n, m, v, "Must be a "+formatName(timestampFormat(m))+" timestamp")
	}
	return t.Truncate(time.Millisecond).UTC(), nil
}

// ReadBlob reads padded standard base64.
func (d *deserializer) ReadBlob(m *smithy.Member) ([]byte, error) {
	n, v, err := d.scalar("ReadBlob")
	if err != nil {
		return nil, err
	}
	b, berr := base64.StdEncoding.DecodeString(v)
	if berr != nil {
		return nil, invalid(n, m, v, "Must be base64-encoded")
	}
	return b, nil
}

// ReadDocument fails: awsQuery cannot carry a document (wire-json-query.md
// 7.3), so generated code for a Query model never asks for one.
func (d *deserializer) ReadDocument(m *smithy.Member) (document.Value, error) {
	return nil, d.usage("ReadDocument for " + memberName(m) + ": awsQuery has no document encoding")
}

// ReadNull reports false: the form has no null.
func (d *deserializer) ReadNull(*smithy.Member) (bool, error) { return false, nil }

// ReadBody fails: awsQuery models carry no @streaming member.
func (d *deserializer) ReadBody(m *smithy.Member) (smithy.Body, error) {
	return nil, d.usage("ReadBody for " + memberName(m) + ": awsQuery models no @streaming member")
}

// take returns the positioned value and clears it, so a value is read once.
func (d *deserializer) take(call string) (*form.Node, error) {
	n := d.value
	if n == nil {
		return nil, d.usage(call + " with no value positioned")
	}
	d.value = nil
	return n, nil
}

// scalar takes the positioned value's text.
func (d *deserializer) scalar(call string) (*form.Node, string, error) {
	n, err := d.take(call)
	if err != nil {
		return nil, "", err
	}
	v, _ := n.Value()
	return n, v, nil
}

func (d *deserializer) readInt(m *smithy.Member, bits int) (int64, error) {
	n, v, err := d.scalar("ReadInt")
	if err != nil {
		return 0, err
	}
	x, perr := strconv.ParseInt(v, 10, bits)
	if perr != nil {
		return 0, invalid(n, m, v, "Must be an integer from "+strconv.FormatInt(-1<<(bits-1), 10)+" to "+strconv.FormatInt(1<<(bits-1)-1, 10))
	}
	return x, nil
}

// readFloat accepts decimal and exponent forms and the strings NaN,
// Infinity and -Infinity (wire-json-query.md 7.3). A value beyond the
// float range is refused rather than rounded to an infinity.
func (d *deserializer) readFloat(m *smithy.Member, bits int) (float64, error) {
	n, v, err := d.scalar("ReadFloat")
	if err != nil {
		return 0, err
	}
	switch v {
	case "NaN":
		return math.NaN(), nil
	case "Infinity":
		return math.Inf(1), nil
	case "-Infinity":
		return math.Inf(-1), nil
	}
	if _, perr := document.ParseNumber(v); perr != nil {
		return 0, invalid(n, m, v, "Must be a number")
	}
	x, perr := strconv.ParseFloat(v, bits)
	if perr != nil {
		return 0, invalid(n, m, v, "Must be within the range of a "+strconv.Itoa(bits)+"-bit float")
	}
	return x, nil
}

// flattened reports whether m's collection is flattened: the trait applies
// only to a structure or union member, never to a list element or map
// value.
func (d *deserializer) flattened(m *smithy.Member) bool {
	if !m.XMLFlattened || len(d.open) == 0 {
		return false
	}
	k := d.open[len(d.open)-1].kind
	return k == structFrame || k == unionFrame
}

func (d *deserializer) usage(what string) error { return &misuseError{what: what} }

func (d *deserializer) top() *readFrame {
	if len(d.open) == 0 {
		return nil
	}
	return &d.open[len(d.open)-1]
}

func (d *deserializer) pop() { d.open = d.open[:len(d.open)-1] }

// present returns the node of m under parent when the form carries m: a
// value for a scalar, a value or longer keys for a structure, union, list
// or map (ListArg= is an empty list). A member whose queryKeyAlias says to
// ignore the key it shares is never present: its sibling receives the
// values.
func present(parent *form.Node, m *smithy.Member) (*form.Node, bool) {
	if a := m.QueryKeyAlias; a != nil && a.Decode == smithy.AliasIgnore {
		return nil, false
	}
	n, ok := parent.Child(elementName(m, m.Name))
	if !ok {
		return nil, false
	}
	switch m.Target.Type {
	case smithy.TypeStructure, smithy.TypeUnion, smithy.TypeList, smithy.TypeMap:
		return n, true
	case smithy.TypeString, smithy.TypeEnum, smithy.TypeIntEnum, smithy.TypeBlob, smithy.TypeBoolean,
		smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat,
		smithy.TypeDouble, smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeTimestamp,
		smithy.TypeDocument, smithy.TypeUnit:
	}
	_, hasValue := n.Value()
	return n, hasValue
}

// invalid is the decode error for value v of node n, read for member m, in
// the form of the Query services' own InvalidParameterValue texts
// (error-matrix.md 5.1). The key and the value pass through form.Excerpt,
// so a 16 MiB value or index segment does not come back as a 16 MiB
// error, and the value is left out for a sensitive member, whose value
// must stay out of responses and the journal.
func invalid(n *form.Node, m *smithy.Member, v, reason string) error {
	key := form.Excerpt(n.Key())
	if m != nil && m.Sensitive {
		return &decodeError{Key: key, Message: fmt.Sprintf("The value for parameter %s is invalid. Reason: %s.", key, reason)}
	}
	return &decodeError{Key: key, Message: fmt.Sprintf("Value %s for parameter %s is invalid. Reason: %s.", form.Excerpt(v), key, reason)}
}

// maxIntegerDigits bounds a bigInteger value, so a huge digit string costs
// nothing to refuse; 4096 digits exceeds any value an AWS model describes.
const maxIntegerDigits = 4096

// parseEpochSeconds reads seconds since the epoch with an optional decimal
// fraction, truncated toward zero to milliseconds, within the years 1 to
// 9999.
func parseEpochSeconds(v string) (time.Time, error) {
	whole, frac, _ := strings.Cut(v, ".")
	sec, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || sec < minEpochSeconds || sec > maxEpochSeconds || strings.Trim(frac, "0123456789") != "" {
		return time.Time{}, fmt.Errorf("%q is not epoch seconds", v)
	}
	ms, _ := strconv.Atoi((frac + "000")[:3]) //nolint:errcheck // three ASCII digits always parse
	nanos := int64(ms) * int64(time.Millisecond)
	if strings.HasPrefix(whole, "-") {
		nanos = -nanos
	}
	return time.Unix(sec, nanos), nil
}

// The epoch seconds of the years 1 and 9999, the range date-time can print.
const (
	minEpochSeconds = -62135596800
	maxEpochSeconds = 253402300799
)

func memberName(m *smithy.Member) string {
	if m == nil {
		return "a nil member"
	}
	return m.Name
}
