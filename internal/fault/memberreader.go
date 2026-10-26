package fault

import (
	"encoding/base64"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// membersField is the rule member that holds an error action's members;
// a refusal inside them names the path below it.
const membersField = "action.members"

// memberError is a rule's members that do not decode into the error
// shape: At is the value at fault ("action.members.CancellationReasons.0"),
// or for an unknown member the structure holding it, and Reason says what
// is wrong and what to give instead.
type memberError struct {
	At     string
	Reason string
}

func (e *memberError) Error() string { return e.At + " " + e.Reason }

// decodeMembers reads m into v through v's generated deserializer, the
// awsJson1_0 form of the shape (names as modeled, D6), and returns the
// schema of v's shape. It is strict where the codecs are lenient: an
// unknown member, a value of the wrong JSON kind, a number out of range,
// bad base64 and a null anywhere but in a @sparse collection are each a
// *memberError.
func decodeMembers(v smithy.Deserializable, m Members) (*smithy.Schema, error) {
	r := &memberReader{next: document.Map(m), at: membersField}
	if err := v.Deserialize(r); err != nil {
		return nil, err
	}
	if r.next != nil || len(r.stack) > 0 || r.root == nil {
		return nil, fmt.Errorf("fault: the generated decoder of %T stopped before reading all of %s", v, membersField)
	}
	return r.root, nil
}

// memberReader is a smithy.Deserializer over a document tree. next is the
// value the next Read call consumes, at its path; stack holds the
// structures, unions, lists and maps entered.
type memberReader struct {
	next  document.Value
	at    string
	stack []*frame
	// root is the schema of the top-level structure.
	root *smithy.Schema
}

// frame is one entered container: the remaining entries of a structure,
// union or map, or the elements of a list.
type frame struct {
	kind    frameKind
	at      string
	schema  *smithy.Schema // the structure or union
	entries document.Map
	list    document.List
	i       int
}

type frameKind uint8

const (
	structFrame frameKind = iota + 1
	unionFrame
	listFrame
	mapFrame
)

// ReadStruct enters a structure.
func (r *memberReader) ReadStruct(s *smithy.Schema) error {
	v, at, err := r.take()
	if err != nil {
		return err
	}
	m, ok := v.(document.Map)
	if !ok {
		return wrongKind(v, at, s, "a JSON object of its members")
	}
	if r.root == nil {
		r.root = s
	}
	r.stack = append(r.stack, &frame{kind: structFrame, at: at, schema: s, entries: m})
	return nil
}

// ReadStructMember returns the next member given, refusing a name the
// structure does not model.
func (r *memberReader) ReadStructMember() (*smithy.Member, bool, error) {
	f, err := r.top(structFrame)
	if err != nil {
		return nil, false, err
	}
	if f.i == len(f.entries) {
		r.pop()
		return nil, false, nil
	}
	e := f.entries[f.i]
	f.i++
	m, err := memberNamed(f.schema, f.at, e.Key)
	if err != nil {
		return nil, false, err
	}
	r.pend(e.Value, f.at+"."+e.Key)
	return m, true, nil
}

// ReadUnion enters a union, whose object holds exactly one member.
func (r *memberReader) ReadUnion(s *smithy.Schema) (*smithy.Member, error) {
	v, at, err := r.take()
	if err != nil {
		return nil, err
	}
	m, ok := v.(document.Map)
	if !ok {
		return nil, wrongKind(v, at, s, "a JSON object of one member")
	}
	if len(m) != 1 {
		return nil, &memberError{At: at, Reason: fmt.Sprintf("is an object of %d members; %s is a union, so give exactly one of %s",
			len(m), s.ID, orList(memberNames(s)))}
	}
	member, err := memberNamed(s, at, m[0].Key)
	if err != nil {
		return nil, err
	}
	r.stack = append(r.stack, &frame{kind: unionFrame, at: at, schema: s})
	r.pend(m[0].Value, at+"."+m[0].Key)
	return member, nil
}

// CloseUnion leaves a union; ReadUnion already refused a second member.
func (r *memberReader) CloseUnion() error {
	if _, err := r.top(unionFrame); err != nil {
		return err
	}
	r.pop()
	return nil
}

// ReadList enters a list.
func (r *memberReader) ReadList(m *smithy.Member) error {
	v, at, err := r.take()
	if err != nil {
		return err
	}
	l, ok := v.(document.List)
	if !ok {
		return wrongKind(v, at, m.Target, "a JSON array")
	}
	r.stack = append(r.stack, &frame{kind: listFrame, at: at, list: l})
	return nil
}

// ReadListElement reports whether another element follows.
func (r *memberReader) ReadListElement() (bool, error) {
	f, err := r.top(listFrame)
	if err != nil {
		return false, err
	}
	if f.i == len(f.list) {
		r.pop()
		return false, nil
	}
	r.pend(f.list[f.i], f.at+"."+strconv.Itoa(f.i))
	f.i++
	return true, nil
}

// ReadMap enters a map.
func (r *memberReader) ReadMap(m *smithy.Member) error {
	v, at, err := r.take()
	if err != nil {
		return err
	}
	entries, ok := v.(document.Map)
	if !ok {
		return wrongKind(v, at, m.Target, "a JSON object")
	}
	r.stack = append(r.stack, &frame{kind: mapFrame, at: at, entries: entries})
	return nil
}

// ReadMapKey returns the next key.
func (r *memberReader) ReadMapKey() (key string, ok bool, err error) {
	f, err := r.top(mapFrame)
	if err != nil {
		return "", false, err
	}
	if f.i == len(f.entries) {
		r.pop()
		return "", false, nil
	}
	e := f.entries[f.i]
	f.i++
	r.pend(e.Value, f.at+"."+e.Key)
	return e.Key, true, nil
}

// ReadString reads a string or an enum.
func (r *memberReader) ReadString(m *smithy.Member) (string, error) {
	v, at, err := r.take()
	if err != nil {
		return "", err
	}
	s, ok := v.(document.String)
	if !ok {
		return "", wrongKind(v, at, m.Target, "a JSON string")
	}
	return string(s), nil
}

// ReadBool reads a boolean.
func (r *memberReader) ReadBool(m *smithy.Member) (bool, error) {
	v, at, err := r.take()
	if err != nil {
		return false, err
	}
	b, ok := v.(document.Bool)
	if !ok {
		return false, wrongKind(v, at, m.Target, "true or false")
	}
	return bool(b), nil
}

// ReadInt8 reads a byte.
func (r *memberReader) ReadInt8(m *smithy.Member) (int8, error) {
	n, err := r.readInt(m, 8)
	return int8(n), err //nolint:gosec // readInt checked the range for 8 bits
}

// ReadInt16 reads a short.
func (r *memberReader) ReadInt16(m *smithy.Member) (int16, error) {
	n, err := r.readInt(m, 16)
	return int16(n), err //nolint:gosec // readInt checked the range for 16 bits
}

// ReadInt32 reads an integer or an intEnum.
func (r *memberReader) ReadInt32(m *smithy.Member) (int32, error) {
	n, err := r.readInt(m, 32)
	return int32(n), err //nolint:gosec // readInt checked the range for 32 bits
}

// ReadInt64 reads a long.
func (r *memberReader) ReadInt64(m *smithy.Member) (int64, error) {
	return r.readInt(m, 64)
}

// ReadFloat32 reads a float.
func (r *memberReader) ReadFloat32(m *smithy.Member) (float32, error) {
	f, err := r.readFloat(m, 32)
	return float32(f), err
}

// ReadFloat64 reads a double.
func (r *memberReader) ReadFloat64(m *smithy.Member) (float64, error) {
	return r.readFloat(m, 64)
}

// ReadBigInt reads a bigInteger.
func (r *memberReader) ReadBigInt(m *smithy.Member) (*big.Int, error) {
	n, at, err := r.number(m)
	if err != nil {
		return nil, err
	}
	rat, ok := new(big.Rat).SetString(string(n))
	if !ok || !rat.IsInt() {
		return nil, &memberError{At: at, Reason: fmt.Sprintf("is %s, which is not a whole number; %s is %s", n, m.Target.ID, typeName(m.Target))}
	}
	return rat.Num(), nil
}

// ReadBigDecimal reads a bigDecimal as its decimal text.
func (r *memberReader) ReadBigDecimal(m *smithy.Member) (smithy.BigDecimal, error) {
	n, _, err := r.number(m)
	return n, err
}

// ReadTimestamp reads epoch seconds, the awsJson1_0 form.
func (r *memberReader) ReadTimestamp(m *smithy.Member) (time.Time, error) {
	n, at, err := r.number(m)
	if err != nil {
		return time.Time{}, err
	}
	t, ok := epochSeconds(n)
	if !ok {
		return time.Time{}, &memberError{At: at, Reason: fmt.Sprintf("is %s, out of range for %s, a timestamp in epoch seconds", n, m.Target.ID)}
	}
	return t, nil
}

// ReadBlob reads a base64 string.
func (r *memberReader) ReadBlob(m *smithy.Member) ([]byte, error) {
	v, at, err := r.take()
	if err != nil {
		return nil, err
	}
	s, ok := v.(document.String)
	if !ok {
		return nil, wrongKind(v, at, m.Target, "a JSON string of base64")
	}
	b, err := base64.StdEncoding.DecodeString(string(s))
	if err != nil {
		return nil, &memberError{At: at, Reason: fmt.Sprintf("is not base64: %v; %s is a blob, given as a base64 JSON string such as \"aGk=\"", err, m.Target.ID)}
	}
	return b, nil
}

// ReadDocument reads any JSON value.
func (r *memberReader) ReadDocument(*smithy.Member) (document.Value, error) {
	v, _, err := r.take()
	return v, err
}

// ReadNull consumes a null, which the generated code asks for only in a
// @sparse collection.
func (r *memberReader) ReadNull(*smithy.Member) (bool, error) {
	if _, ok := r.next.(document.Null); !ok {
		return false, nil
	}
	r.next = nil
	return true, nil
}

// ReadBody refuses: an error shape has no @streaming member.
func (r *memberReader) ReadBody(m *smithy.Member) (smithy.Body, error) {
	return nil, &memberError{At: r.at, Reason: fmt.Sprintf("is a @streaming member of %s, which a fault rule cannot set; leave it out", m.Target.ID)}
}

// take consumes the pending value.
func (r *memberReader) take() (document.Value, string, error) {
	if r.next == nil {
		return nil, "", fmt.Errorf("fault: the generated decoder read a value of %s where none is pending", membersField)
	}
	v, at := r.next, r.at
	r.next = nil
	return v, at, nil
}

// pend makes v at path at the value the next Read call consumes.
func (r *memberReader) pend(v document.Value, at string) {
	r.next, r.at = v, at
}

// top returns the open container, which must be of kind k.
func (r *memberReader) top(k frameKind) (*frame, error) {
	if len(r.stack) == 0 || r.stack[len(r.stack)-1].kind != k {
		return nil, fmt.Errorf("fault: the generated decoder of %s read outside the container it entered", membersField)
	}
	return r.stack[len(r.stack)-1], nil
}

func (r *memberReader) pop() { r.stack = r.stack[:len(r.stack)-1] }

// readInt reads a whole number that fits in bits bits.
func (r *memberReader) readInt(m *smithy.Member, bits uint) (int64, error) {
	n, at, err := r.number(m)
	if err != nil {
		return 0, err
	}
	lo, hi := int64(-1)<<(bits-1), int64(1)<<(bits-1)-1
	if bits == 64 {
		lo, hi = -1<<63, 1<<63-1
	}
	rat, ok := new(big.Rat).SetString(string(n))
	switch {
	case !ok || !rat.IsInt():
		return 0, &memberError{At: at, Reason: fmt.Sprintf("is %s, which is not a whole number; %s is %s, a whole number from %d to %d", n, m.Target.ID, typeName(m.Target), lo, hi)}
	case !rat.Num().IsInt64() || rat.Num().Int64() < lo || rat.Num().Int64() > hi:
		return 0, &memberError{At: at, Reason: fmt.Sprintf("is %s, out of range; %s is %s, a whole number from %d to %d", n, m.Target.ID, typeName(m.Target), lo, hi)}
	}
	return rat.Num().Int64(), nil
}

func (r *memberReader) readFloat(m *smithy.Member, bits int) (float64, error) {
	n, at, err := r.number(m)
	if err != nil {
		return 0, err
	}
	f, err := strconv.ParseFloat(string(n), bits)
	if err != nil {
		return 0, &memberError{At: at, Reason: fmt.Sprintf("is %s, out of range for %s, %s", n, m.Target.ID, typeName(m.Target))}
	}
	return f, nil
}

// number reads a JSON number.
func (r *memberReader) number(m *smithy.Member) (document.Number, string, error) {
	v, at, err := r.take()
	if err != nil {
		return "", "", err
	}
	n, ok := v.(document.Number)
	if !ok {
		return "", "", wrongKind(v, at, m.Target, "a JSON number")
	}
	return n, at, nil
}

// epochSeconds converts decimal epoch seconds to a time, to the
// nanosecond, truncating finer digits.
func epochSeconds(n document.Number) (time.Time, bool) {
	rat, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return time.Time{}, false
	}
	sec, rem := new(big.Int).DivMod(rat.Num(), rat.Denom(), new(big.Int))
	if !sec.IsInt64() {
		return time.Time{}, false
	}
	nanos := new(big.Int).Quo(rem.Mul(rem, big.NewInt(int64(time.Second))), rat.Denom())
	return time.Unix(sec.Int64(), nanos.Int64()).UTC(), true
}

// memberNamed returns s's member name, or refuses it at, the structure or
// union that holds it, listing the members s has.
func memberNamed(s *smithy.Schema, at, name string) (*smithy.Member, error) {
	for _, m := range s.Members {
		if m.Name == name {
			return m, nil
		}
	}
	names := memberNames(s)
	if len(names) == 0 {
		return nil, &memberError{At: at, Reason: fmt.Sprintf("%s has no member %q; it has no members", s.ID, name)}
	}
	return nil, &memberError{At: at, Reason: fmt.Sprintf("%s has no member %q; its members are %s", s.ID, name, andList(names))}
}

func memberNames(s *smithy.Schema) []string {
	names := make([]string, len(s.Members))
	for i, m := range s.Members {
		names[i] = m.Name
	}
	return names
}

// andList joins items as "a, b and c".
func andList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// wrongKind refuses v, a value of the wrong JSON kind for target, saying
// what to give instead.
func wrongKind(v document.Value, at string, target *smithy.Schema, want string) error {
	got := jsonKind(v)
	if got == "null" {
		return &memberError{At: at, Reason: fmt.Sprintf("is a JSON null; %s is %s and takes no null here, since only a @sparse list or map holds one, so give %s",
			target.ID, typeName(target), want)}
	}
	return &memberError{At: at, Reason: fmt.Sprintf("is a JSON %s; %s is %s, so give %s", got, target.ID, typeName(target), want)}
}

// jsonKind names the JSON kind of v as RFC 8259 does.
func jsonKind(v document.Value) string {
	switch v.(type) {
	case document.Null:
		return "null"
	case document.Bool:
		return "boolean"
	case document.Number:
		return "number"
	case document.String:
		return "string"
	case document.List:
		return "array"
	case document.Map:
		return "object"
	default:
		return "value"
	}
}

// typeName is the Smithy type of s with its article: "a list", "an enum".
func typeName(s *smithy.Schema) string {
	names := map[smithy.ShapeType]string{
		smithy.TypeStructure: "a structure", smithy.TypeUnion: "a union", smithy.TypeList: "a list", smithy.TypeMap: "a map",
		smithy.TypeString: "a string", smithy.TypeEnum: "an enum", smithy.TypeIntEnum: "an intEnum", smithy.TypeBlob: "a blob",
		smithy.TypeBoolean: "a boolean", smithy.TypeByte: "a byte", smithy.TypeShort: "a short", smithy.TypeInteger: "an integer",
		smithy.TypeLong: "a long", smithy.TypeFloat: "a float", smithy.TypeDouble: "a double", smithy.TypeBigInteger: "a bigInteger",
		smithy.TypeBigDecimal: "a bigDecimal", smithy.TypeTimestamp: "a timestamp", smithy.TypeDocument: "a document",
	}
	if n, ok := names[s.Type]; ok {
		return n
	}
	return "a shape"
}
