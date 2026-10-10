package awsjson_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
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

func listOf(id string, elem *smithy.Schema) *smithy.Schema {
	return withMembers(&smithy.Schema{ID: smithy.ShapeID(id), Type: smithy.TypeList}, member("member", elem))
}

func mapOf(id string, value *smithy.Schema) *smithy.Schema {
	return withMembers(&smithy.Schema{ID: smithy.ShapeID(id), Type: smithy.TypeMap},
		member("key", scalar("smithy.api#String", smithy.TypeString)), member("value", value))
}

var unit = &smithy.Schema{ID: "smithy.api#Unit", Type: smithy.TypeUnit}

// kitchenSink covers every shape kind the AWS models and the protocol
// corpus use, with a recursive member for nesting and aggregates inside
// collections: a list of structures and a map of unions.
func kitchenSink() *smithy.Schema {
	str := scalar("smithy.api#String", smithy.TypeString)
	integer := scalar("smithy.api#Integer", smithy.TypeInteger)
	ts := scalar("smithy.api#Timestamp", smithy.TypeTimestamp)
	s := &smithy.Schema{ID: "test#KitchenSink", Type: smithy.TypeStructure}
	sparse := listOf("test#SparseStrings", str)
	sparse.Sparse = true
	dateTime := member("DateTime", ts)
	dateTime.TimestampFormat = smithy.DateTime
	httpDate := member("HttpDate", ts)
	httpDate.TimestampFormat = smithy.HTTPDate
	union := withMembers(&smithy.Schema{ID: "test#Choice", Type: smithy.TypeUnion},
		member("Text", str), member("Number", integer), member("Sink", s))
	// Entry, like SQS's batch entries, has no structure member, so a list
	// of them needs no depth limit of its own.
	entry := structure("test#Entry", member("Id", str), member("Count", integer))
	return withMembers(s,
		member("String", str),
		member("Enum", scalar("test#Color", smithy.TypeEnum)),
		member("IntEnum", scalar("test#Level", smithy.TypeIntEnum)),
		member("Blob", scalar("smithy.api#Blob", smithy.TypeBlob)),
		member("Boolean", scalar("smithy.api#Boolean", smithy.TypeBoolean)),
		member("Byte", scalar("smithy.api#Byte", smithy.TypeByte)),
		member("Short", scalar("smithy.api#Short", smithy.TypeShort)),
		member("Integer", integer),
		member("Long", scalar("smithy.api#Long", smithy.TypeLong)),
		member("Float", scalar("smithy.api#Float", smithy.TypeFloat)),
		member("Double", scalar("smithy.api#Double", smithy.TypeDouble)),
		member("BigInteger", scalar("smithy.api#BigInteger", smithy.TypeBigInteger)),
		member("BigDecimal", scalar("smithy.api#BigDecimal", smithy.TypeBigDecimal)),
		member("Timestamp", ts),
		dateTime,
		httpDate,
		member("Document", scalar("smithy.api#Document", smithy.TypeDocument)),
		member("List", listOf("test#Strings", str)),
		member("SparseList", sparse),
		member("Map", mapOf("test#Counts", integer)),
		member("Entries", listOf("test#Entries", entry)),
		member("Choices", mapOf("test#Choices", union)),
		member("Nested", s),
		member("Choice", union),
	)
}

// dynStruct is a structure value of any schema, so tests drive the codec
// without generated code. Fields hold, per target type: string, bool,
// int8 to int64, float32, float64, *big.Int, document.Number (bigDecimal),
// time.Time, []byte, document.Value, []any (nil elements are nulls of a
// sparse list), map[string]any, dynStruct and dynUnion.
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
	// A document member goes through WriteDocument whatever its value, so a
	// top-level number, string or bool document is not written as the
	// scalar shape whose Go type it shares.
	if d, ok := v.(document.Value); ok && m.Target.Type == smithy.TypeDocument {
		s.WriteDocument(m, d)
		return
	}
	switch v := v.(type) {
	case nil:
		s.WriteNull(m)
	case string:
		s.WriteString(m, v)
	case bool:
		s.WriteBool(m, v)
	case int8:
		s.WriteInt8(m, v)
	case int16:
		s.WriteInt16(m, v)
	case int32:
		s.WriteInt32(m, v)
	case int64:
		s.WriteInt64(m, v)
	case float32:
		s.WriteFloat32(m, v)
	case float64:
		s.WriteFloat64(m, v)
	case *big.Int:
		s.WriteBigInt(m, v)
	case document.Number:
		s.WriteBigDecimal(m, v)
	case time.Time:
		s.WriteTimestamp(m, v)
	case []byte:
		s.WriteBlob(m, v)
	case []any:
		s.WriteList(m, len(v))
		for _, x := range v {
			writeValue(s, m.Target.Members[0], x)
		}
		s.CloseList()
	case map[string]any:
		s.WriteMap(m, len(v))
		for _, k := range sortedKeys(v) {
			s.WriteMapKey(k)
			writeValue(s, m.Target.Members[1], v[k])
		}
		s.CloseMap()
	case dynStruct:
		writeStruct(s, m, v)
	case dynUnion:
		s.WriteUnion(m, m.Target)
		for _, um := range m.Target.Members {
			if um.Name == v.member {
				writeValue(s, um, v.value)
			}
		}
		s.CloseUnion()
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func readStruct(d smithy.Deserializer, s *smithy.Schema) (dynStruct, error) {
	v := dynStruct{schema: s, fields: map[string]any{}}
	if err := d.ReadStruct(s); err != nil {
		return v, err
	}
	for {
		m, ok, err := d.ReadStructMember()
		if err != nil || !ok {
			return v, err
		}
		x, err := readValue(d, m)
		if err != nil {
			return v, err
		}
		v.fields[m.Name] = x
	}
}

func readValue(d smithy.Deserializer, m *smithy.Member) (any, error) {
	switch m.Target.Type {
	case smithy.TypeString, smithy.TypeEnum:
		return d.ReadString(m)
	case smithy.TypeBoolean:
		return d.ReadBool(m)
	case smithy.TypeByte:
		return d.ReadInt8(m)
	case smithy.TypeShort:
		return d.ReadInt16(m)
	case smithy.TypeInteger, smithy.TypeIntEnum:
		return d.ReadInt32(m)
	case smithy.TypeLong:
		return d.ReadInt64(m)
	case smithy.TypeFloat:
		return d.ReadFloat32(m)
	case smithy.TypeDouble:
		return d.ReadFloat64(m)
	case smithy.TypeBigInteger:
		return d.ReadBigInt(m)
	case smithy.TypeBigDecimal:
		return d.ReadBigDecimal(m)
	case smithy.TypeTimestamp:
		return d.ReadTimestamp(m)
	case smithy.TypeBlob:
		return d.ReadBlob(m)
	case smithy.TypeDocument:
		return d.ReadDocument(m)
	case smithy.TypeList:
		return readList(d, m)
	case smithy.TypeMap:
		return readMap(d, m)
	case smithy.TypeStructure:
		return readStruct(d, m.Target)
	case smithy.TypeUnion:
		um, err := d.ReadUnion(m.Target)
		if err != nil {
			return nil, err
		}
		x, err := readValue(d, um)
		if err != nil {
			return nil, err
		}
		return dynUnion{member: um.Name, value: x}, d.CloseUnion()
	case smithy.TypeUnit:
		return nil, errors.New("test schema: a member cannot target smithy.api#Unit")
	default:
		return nil, fmt.Errorf("test schema: unknown shape type %v", m.Target.Type)
	}
}

func readList(d smithy.Deserializer, m *smithy.Member) (any, error) {
	if err := d.ReadList(m); err != nil {
		return nil, err
	}
	out := []any{}
	elem := m.Target.Members[0]
	for {
		more, err := d.ReadListElement()
		if err != nil || !more {
			return out, err
		}
		if m.Target.Sparse {
			null, err := d.ReadNull(elem)
			if err != nil {
				return nil, err
			}
			if null {
				out = append(out, nil)
				continue
			}
		}
		x, err := readValue(d, elem)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
}

func readMap(d smithy.Deserializer, m *smithy.Member) (any, error) {
	if err := d.ReadMap(m); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for {
		k, ok, err := d.ReadMapKey()
		if err != nil || !ok {
			return out, err
		}
		x, err := readValue(d, m.Target.Members[1])
		if err != nil {
			return nil, err
		}
		out[k] = x
	}
}

// equalValues compares decoded values: NaN equals NaN, times by instant,
// big integers by value, and nil equals an empty slice.
func equalValues(a, b any) bool {
	switch a := a.(type) {
	case float64:
		bf, ok := b.(float64)
		return ok && (a == bf || math.IsNaN(a) && math.IsNaN(bf))
	case float32:
		bf, ok := b.(float32)
		return ok && (a == bf || a != a && bf != bf)
	case time.Time:
		bt, ok := b.(time.Time)
		return ok && a.Equal(bt)
	case *big.Int:
		bi, ok := b.(*big.Int)
		return ok && a.Cmp(bi) == 0
	case []byte:
		bb, ok := b.([]byte)
		return ok && bytes.Equal(a, bb)
	case []any:
		bl, ok := b.([]any)
		return ok && slices.EqualFunc(a, bl, equalValues)
	case map[string]any:
		bm, ok := b.(map[string]any)
		if !ok || len(a) != len(bm) {
			return false
		}
		for k, x := range a {
			if y, ok := bm[k]; !ok || !equalValues(x, y) {
				return false
			}
		}
		return true
	case dynStruct:
		bs, ok := b.(dynStruct)
		return ok && equalValues(a.fields, bs.fields)
	case dynUnion:
		bu, ok := b.(dynUnion)
		return ok && a.member == bu.member && equalValues(a.value, bu.value)
	default:
		return equalDocuments(a, b)
	}
}

func equalDocuments(a, b any) bool {
	switch a := a.(type) {
	case document.List:
		bl, ok := b.(document.List)
		return ok && slices.EqualFunc(a, bl, func(x, y document.Value) bool { return equalDocuments(x, y) })
	case document.Map:
		bm, ok := b.(document.Map)
		return ok && slices.EqualFunc(a, bm, func(x, y document.Entry) bool { return x.Key == y.Key && equalDocuments(x.Value, y.Value) })
	default:
		return a == b
	}
}

// Services, shaped like the generated descriptors.

func service(id ref.ServiceID, prefix string, proto ref.ProtocolID, queryCompatible bool, ops ...*registry.Operation) *registry.Service {
	svc := &registry.Service{
		ID: id, SigningName: string(id), JSONTargetPrefix: prefix,
		Protocols: []ref.ProtocolID{proto}, QueryCompatible: queryCompatible,
	}
	for i, op := range ops {
		op.Service, op.Ordinal, op.ID = svc, i, ref.OperationID{Service: id, Name: op.ID.Name}
		if op.Kind == 0 {
			op.Kind = registry.Modeled
		}
	}
	svc.Operations = ops
	return svc
}

func op(name string, in, out *smithy.Schema) *registry.Operation {
	return &registry.Operation{ID: ref.OperationID{Name: name}, Input: in, Output: out, SuccessCode: 200}
}

type fixture struct {
	reg                        *registry.Registry
	sqs, logs, cloudwatch      *registry.Service
	createQueue, deleteMessage *registry.Operation
	sink                       *registry.Operation
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	str := scalar("smithy.api#String", smithy.TypeString)
	createIn := structure("com.amazonaws.sqs#CreateQueueRequest",
		member("QueueName", str),
		member("Attributes", mapOf("com.amazonaws.sqs#QueueAttributeMap", str)),
		member("tags", mapOf("com.amazonaws.sqs#TagMap", str)),
	)
	createOut := structure("com.amazonaws.sqs#CreateQueueResult", member("QueueUrl", str))
	deleteIn := structure("com.amazonaws.sqs#DeleteMessageRequest", member("QueueUrl", str), member("ReceiptHandle", str))
	f := fixture{
		createQueue:   op("CreateQueue", createIn, createOut),
		deleteMessage: op("DeleteMessage", deleteIn, unit),
		sink:          op("Echo", kitchenSink(), kitchenSink()),
	}
	hold := op("HoldVisibility", deleteIn, unit)
	hold.Kind = registry.Internal
	f.sqs = service("sqs", "AmazonSQS", ref.AWSJSON10, true, f.createQueue, f.deleteMessage, f.sink, hold)
	f.logs = service("logs", "Logs_20140328", ref.AWSJSON11, false,
		op("DescribeLogGroups", structure("com.amazonaws.cloudwatchlogs#DescribeLogGroupsRequest"), unit))
	f.cloudwatch = service("cloudwatch", "GraniteServiceVersion20100801", ref.AWSJSON10, true,
		op("PutMetricData", structure("com.amazonaws.cloudwatch#PutMetricDataInput", member("Namespace", str)), unit))
	reg, err := registry.New(f.sqs, f.logs, f.cloudwatch)
	if err != nil {
		t.Fatal(err)
	}
	f.reg = reg
	return f
}

// request builds an awsJson POST the way the SDKs send one.
func request(target, contentType string, body []byte) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", bytes.NewReader(body))
	if target != "" {
		r.Header.Set("X-Amz-Target", target)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	return r
}

// decode runs Codec.Decode of a 1.0 body into a fresh value of op's input.
func decode(op *registry.Operation, body []byte, header ...string) (dynStruct, *awserr.Error) {
	r := request("AmazonSQS."+op.ID.Name, "application/x-amz-json-1.0", body)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	v := dynStruct{schema: op.Input}
	aerr := awsjson.Codec{Version: awsjson.V10}.Decode(r, op, &v)
	return v, aerr
}

// failingBody is a request body whose read fails partway.
type failingBody struct{ err error }

func (b failingBody) Read([]byte) (int, error) { return 0, b.err }

func (failingBody) Close() error { return nil }

var _ io.ReadCloser = failingBody{}
