package awsquery_test

import (
	"context"
	"encoding/base64"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/form"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Schemas are built by hand here, the way the generator emits them.

func scalar(id string, t smithy.ShapeType) *smithy.Schema {
	return &smithy.Schema{ID: smithy.ShapeID(id), Type: t}
}

func member(name string, target *smithy.Schema) *smithy.Member {
	return &smithy.Member{Name: name, Target: target}
}

// renamed is a member with an xmlName, flattened when flat is set.
func renamed(name, xmlName string, flat bool, target *smithy.Schema) *smithy.Member {
	return &smithy.Member{Name: name, XMLName: xmlName, XMLFlattened: flat, Target: target}
}

func structure(id string, members ...*smithy.Member) *smithy.Schema {
	return withMembers(&smithy.Schema{ID: smithy.ShapeID(id), Type: smithy.TypeStructure}, members...)
}

func withMembers(s *smithy.Schema, members ...*smithy.Member) *smithy.Schema {
	for i, m := range members {
		m.Index = i
	}
	s.Members = members
	return s
}

func listOf(id string, elem *smithy.Member) *smithy.Schema {
	return withMembers(&smithy.Schema{ID: smithy.ShapeID(id), Type: smithy.TypeList}, elem)
}

func mapOf(id string, key, value *smithy.Member) *smithy.Schema {
	return withMembers(&smithy.Schema{ID: smithy.ShapeID(id), Type: smithy.TypeMap}, key, value)
}

var (
	str     = scalar("smithy.api#String", smithy.TypeString)
	boolean = scalar("smithy.api#Boolean", smithy.TypeBoolean)
	integer = scalar("smithy.api#Integer", smithy.TypeInteger)
	long    = scalar("smithy.api#Long", smithy.TypeLong)
	double  = scalar("smithy.api#Double", smithy.TypeDouble)
	tstamp  = scalar("smithy.api#Timestamp", smithy.TypeTimestamp)
	blob    = scalar("smithy.api#Blob", smithy.TypeBlob)
	unit    = &smithy.Schema{ID: "smithy.api#Unit", Type: smithy.TypeUnit}
)

func stringList(id string) *smithy.Schema { return listOf(id, member("member", str)) }

func stringMap(id string) *smithy.Schema {
	return mapOf(id, member("key", str), member("value", str))
}

// kitchenSink covers every collection form of wire-json-query.md 7.2 and
// 8.3: wrapped, renamed and flattened lists, default and flattened maps
// with renamed key and value, nested structures and collections, and the
// scalar kinds the Query models use.
func kitchenSink() *smithy.Schema {
	s := &smithy.Schema{ID: "test#KitchenSink", Type: smithy.TypeStructure}
	epoch := member("Epoch", tstamp)
	epoch.TimestampFormat = smithy.EpochSeconds
	httpDate := member("HttpDate", tstamp)
	httpDate.TimestampFormat = smithy.HTTPDate
	entry := structure("test#Entry", member("Id", str), member("Count", integer))
	choice := withMembers(&smithy.Schema{ID: "test#Choice", Type: smithy.TypeUnion}, member("Text", str), member("Number", integer))
	return withMembers(s,
		member("String", str),
		renamed("Alias", "AliasName", false, str),
		member("Boolean", boolean),
		member("Integer", integer),
		member("Long", long),
		member("Double", double),
		member("Timestamp", tstamp),
		epoch,
		httpDate,
		member("Blob", blob),
		member("List", stringList("test#Strings")),
		renamed("FlatList", "Flat", true, stringList("test#FlatStrings")),
		member("ItemList", listOf("test#Items", renamed("member", "item", false, str))),
		member("Map", stringMap("test#StringMap")),
		renamed("FlatMap", "Attribute", true, mapOf("test#NamedMap", renamed("key", "Name", false, str), renamed("value", "Value", false, entry))),
		member("Entries", listOf("test#Entries", member("member", entry))),
		member("MapOfLists", mapOf("test#MapOfLists", member("key", str), member("value", stringList("test#Inner")))),
		member("Choice", choice),
		member("Nested", s),
	)
}

// dynStruct is a structure value of any schema, so tests drive the codec
// without generated code. Fields hold, per target type: string, bool,
// int32, int64, float64, time.Time, []byte, *big.Int, []any,
// map[string]any, dynStruct and dynUnion.
type dynStruct struct {
	schema *smithy.Schema
	fields map[string]any
}

type dynUnion struct {
	member string
	value  any
}

func (v *dynStruct) Serialize(s smithy.Serializer) { writeStruct(s, nil, *v) }

func (v *dynStruct) Deserialize(d smithy.Deserializer) error {
	got, err := readStruct(d, v.schema)
	*v = got
	return err
}

func writeStruct(s smithy.Serializer, m *smithy.Member, v dynStruct) {
	s.WriteStruct(m, v.schema)
	for _, mm := range v.schema.Members {
		if x, ok := v.fields[mm.Name]; ok {
			writeValue(s, mm, x)
		}
	}
	s.CloseStruct()
}

func writeValue(s smithy.Serializer, m *smithy.Member, v any) {
	switch x := v.(type) {
	case string:
		s.WriteString(m, x)
	case bool:
		s.WriteBool(m, x)
	case int32:
		s.WriteInt32(m, x)
	case int64:
		s.WriteInt64(m, x)
	case float64:
		s.WriteFloat64(m, x)
	case time.Time:
		s.WriteTimestamp(m, x)
	case []byte:
		s.WriteBlob(m, x)
	case *big.Int:
		s.WriteBigInt(m, x)
	case dynStruct:
		writeStruct(s, m, x)
	case dynUnion:
		s.WriteUnion(m, m.Target)
		writeValue(s, memberNamed(m.Target, x.member), x.value)
		s.CloseUnion()
	case []any:
		s.WriteList(m, len(x))
		for _, e := range x {
			writeValue(s, m.Target.Members[0], e)
		}
		s.CloseList()
	case map[string]any:
		s.WriteMap(m, len(x))
		for _, k := range sortedKeys(x) {
			s.WriteMapKey(k)
			writeValue(s, m.Target.Members[1], x[k])
		}
		s.CloseMap()
	}
}

func readStruct(d smithy.Deserializer, schema *smithy.Schema) (dynStruct, error) {
	out := dynStruct{schema: schema, fields: map[string]any{}}
	if err := d.ReadStruct(schema); err != nil {
		return out, err
	}
	for {
		m, ok, err := d.ReadStructMember()
		if err != nil || !ok {
			return out, err
		}
		v, err := readValue(d, m)
		if err != nil {
			return out, err
		}
		out.fields[m.Name] = v
	}
}

func readValue(d smithy.Deserializer, m *smithy.Member) (any, error) {
	switch m.Target.Type {
	case smithy.TypeString, smithy.TypeEnum:
		return d.ReadString(m)
	case smithy.TypeBoolean:
		return d.ReadBool(m)
	case smithy.TypeInteger, smithy.TypeIntEnum:
		return d.ReadInt32(m)
	case smithy.TypeLong:
		return d.ReadInt64(m)
	case smithy.TypeDouble:
		return d.ReadFloat64(m)
	case smithy.TypeTimestamp:
		return d.ReadTimestamp(m)
	case smithy.TypeBlob:
		return d.ReadBlob(m)
	case smithy.TypeBigInteger:
		return d.ReadBigInt(m)
	case smithy.TypeStructure:
		return readStruct(d, m.Target)
	case smithy.TypeUnion:
		return readUnion(d, m.Target)
	case smithy.TypeList:
		return readList(d, m)
	case smithy.TypeMap:
		return readMap(d, m)
	case smithy.TypeByte, smithy.TypeShort, smithy.TypeFloat, smithy.TypeBigDecimal, smithy.TypeDocument, smithy.TypeUnit:
	}
	return nil, &unsupportedError{m.Target.Type}
}

func readUnion(d smithy.Deserializer, s *smithy.Schema) (any, error) {
	um, err := d.ReadUnion(s)
	if err != nil {
		return nil, err
	}
	v, err := readValue(d, um)
	if err != nil {
		return nil, err
	}
	return dynUnion{member: um.Name, value: v}, d.CloseUnion()
}

func readList(d smithy.Deserializer, m *smithy.Member) (any, error) {
	if err := d.ReadList(m); err != nil {
		return nil, err
	}
	out := []any{}
	for {
		more, err := d.ReadListElement()
		if err != nil || !more {
			return out, err
		}
		v, err := readValue(d, m.Target.Members[0])
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
}

func readMap(d smithy.Deserializer, m *smithy.Member) (any, error) {
	if err := d.ReadMap(m); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for {
		k, more, err := d.ReadMapKey()
		if err != nil || !more {
			return out, err
		}
		v, err := readValue(d, m.Target.Members[1])
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
}

type unsupportedError struct{ t smithy.ShapeType }

func (e *unsupportedError) Error() string { return "the test reader does not read " + e.t.String() }

func memberNamed(s *smithy.Schema, name string) *smithy.Member {
	for _, m := range s.Members {
		if m.Name == name {
			return m
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// encodeForm writes v as an SDK writes an awsQuery request, by Smithy's
// key rules (wire-json-query.md 7.2): an independent client-side encoder
// the decoder is checked against.
func encodeForm(prefix string, v dynStruct) []form.Pair {
	var out []form.Pair
	for _, m := range v.schema.Members {
		if x, ok := v.fields[m.Name]; ok {
			out = encodeValue(out, join(prefix, keyOf(m, m.Name)), m, true, x)
		}
	}
	return out
}

func encodeValue(out []form.Pair, key string, m *smithy.Member, inStruct bool, v any) []form.Pair {
	switch x := v.(type) {
	case dynStruct:
		return append(out, encodeForm(key, x)...)
	case dynUnion:
		um := memberNamed(m.Target, x.member)
		return encodeValue(out, key+"."+keyOf(um, um.Name), um, true, x.value)
	case []any:
		if len(x) == 0 {
			return append(out, form.Pair{Key: key})
		}
		elem := m.Target.Members[0]
		base := key
		if !inStruct || !m.XMLFlattened {
			base += "." + keyOf(elem, "member")
		}
		for i, e := range x {
			out = encodeValue(out, base+"."+strconv.Itoa(i+1), elem, false, e)
		}
		return out
	case map[string]any:
		base := key
		if !inStruct || !m.XMLFlattened {
			base += ".entry"
		}
		km, vm := m.Target.Members[0], m.Target.Members[1]
		for i, k := range sortedKeys(x) {
			entry := base + "." + strconv.Itoa(i+1)
			out = append(out, form.Pair{Key: entry + "." + keyOf(km, "key"), Value: k})
			out = encodeValue(out, entry+"."+keyOf(vm, "value"), vm, false, x[k])
		}
		return out
	default:
		return append(out, form.Pair{Key: key, Value: scalarText(m, v)})
	}
}

func scalarText(m *smithy.Member, v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int32:
		return strconv.Itoa(int(x))
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		switch {
		case math.IsNaN(x):
			return "NaN"
		case math.IsInf(x, 1):
			return "Infinity"
		case math.IsInf(x, -1):
			return "-Infinity"
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case time.Time:
		switch m.TimestampFormat {
		case smithy.EpochSeconds:
			ms := x.UnixMilli()
			sign := ""
			if ms < 0 {
				sign, ms = "-", -ms
			}
			return sign + strconv.FormatInt(ms/1000, 10) + "." + strconv.FormatInt(1000+ms%1000, 10)[1:]
		case smithy.HTTPDate:
			return x.UTC().Format(http.TimeFormat)
		case smithy.DateTime, smithy.TimestampProtocolDefault:
		}
		return x.UTC().Format(time.RFC3339Nano)
	case []byte:
		return base64.StdEncoding.EncodeToString(x)
	}
	return ""
}

func keyOf(m *smithy.Member, fallback string) string {
	if m.XMLName != "" {
		return m.XMLName
	}
	return fallback
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// Registries are built by hand with the descriptors' invariants: operations
// sorted by name, ordinals equal to the index.

type opSpec struct {
	name    string
	in, out *smithy.Schema
}

func newService(id, version, xmlns string, ops ...opSpec) *registry.Service {
	svc := &registry.Service{
		ID: ref.ServiceID(id), SigningName: id, Protocols: []ref.ProtocolID{ref.AWSQuery},
		QueryVersion: version, XMLNamespace: xmlns,
	}
	slices.SortFunc(ops, func(a, b opSpec) int { return strings.Compare(a.name, b.name) })
	for i, o := range ops {
		in := o.in
		if in == nil {
			in = structure("test#" + o.name + "Input")
		}
		svc.Operations = append(svc.Operations, &registry.Operation{
			Service: svc, ID: ref.OperationID{Service: svc.ID, Name: o.name}, Ordinal: i,
			Kind: registry.Modeled, Input: in, Output: o.out,
		})
	}
	return svc
}

func operation(t *testing.T, svc *registry.Service, name string) *registry.Operation {
	t.Helper()
	op, ok := svc.Operation(name)
	if !ok {
		t.Fatalf("service %s has no operation %s", svc.ID, name)
	}
	return op
}

func newRegistry(t *testing.T, services ...*registry.Service) *registry.Registry {
	t.Helper()
	reg, err := registry.New(services...)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// formPost is a form POST to target, as botocore and Go v2 send it.
func formPost(target, body string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	return r
}

const requestID = "01234567-89ab-cdef-0123-456789abcdef"
