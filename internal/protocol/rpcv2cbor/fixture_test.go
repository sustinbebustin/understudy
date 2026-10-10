package rpcv2cbor_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func structure(id string, members ...*smithy.Member) *smithy.Schema {
	return withMembers(&smithy.Schema{ID: smithy.ShapeID(id), Type: smithy.TypeStructure}, members...)
}

func union(id string, members ...*smithy.Member) *smithy.Schema {
	return withMembers(&smithy.Schema{ID: smithy.ShapeID(id), Type: smithy.TypeUnion}, members...)
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
		member("key", str), member("value", value))
}

var (
	unit       = &smithy.Schema{ID: "smithy.api#Unit", Type: smithy.TypeUnit}
	str        = scalar("smithy.api#String", smithy.TypeString)
	boolean    = scalar("smithy.api#Boolean", smithy.TypeBoolean)
	byteShape  = scalar("smithy.api#Byte", smithy.TypeByte)
	short      = scalar("smithy.api#Short", smithy.TypeShort)
	integer    = scalar("smithy.api#Integer", smithy.TypeInteger)
	long       = scalar("smithy.api#Long", smithy.TypeLong)
	float      = scalar("smithy.api#Float", smithy.TypeFloat)
	double     = scalar("smithy.api#Double", smithy.TypeDouble)
	timestamp  = scalar("smithy.api#Timestamp", smithy.TypeTimestamp)
	blob       = scalar("smithy.api#Blob", smithy.TypeBlob)
	bigInt     = scalar("smithy.api#BigInteger", smithy.TypeBigInteger)
	stringList = listOf("test#Strings", str)
)

// dynStruct is a structure value of any schema, so tests drive the codec
// without generated code. Fields hold, per target type: string, bool,
// int8 to int64, float32, float64, *big.Int, time.Time, []byte, []any
// (nil elements are the nulls of a sparse list), []entry for maps,
// dynStruct and dynUnion.
type dynStruct struct {
	schema *smithy.Schema
	fields map[string]any
}

type dynUnion struct {
	member string
	value  any
}

type entry struct {
	key   string
	value any
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
	case time.Time:
		s.WriteTimestamp(m, v)
	case []byte:
		s.WriteBlob(m, v)
	case []any:
		s.WriteList(m, len(v))
		for _, e := range v {
			writeValue(s, m.Target.Members[0], e)
		}
		s.CloseList()
	case []entry:
		s.WriteMap(m, len(v))
		for _, e := range v {
			s.WriteMapKey(e.key)
			writeValue(s, m.Target.Members[1], e.value)
		}
		s.CloseMap()
	case dynStruct:
		writeStruct(s, m, v)
	case dynUnion:
		s.WriteUnion(m, m.Target)
		for _, mm := range m.Target.Members {
			if mm.Name == v.member {
				writeValue(s, mm, v.value)
			}
		}
		s.CloseUnion()
	default:
		panic(fmt.Sprintf("test fixture: no write for %T", v))
	}
}

func readStruct(d smithy.Deserializer, s *smithy.Schema) (dynStruct, error) {
	out := dynStruct{schema: s, fields: map[string]any{}}
	if err := d.ReadStruct(s); err != nil {
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
	case smithy.TypeStructure:
		return readStruct(d, m.Target)
	case smithy.TypeUnion:
		mm, err := d.ReadUnion(m.Target)
		if err != nil {
			return nil, err
		}
		v, err := readValue(d, mm)
		if err != nil {
			return nil, err
		}
		return dynUnion{member: mm.Name, value: v}, d.CloseUnion()
	case smithy.TypeList:
		if err := d.ReadList(m); err != nil {
			return nil, err
		}
		out := []any{}
		for {
			more, err := d.ReadListElement()
			if err != nil || !more {
				return out, err
			}
			elem := m.Target.Members[0]
			if m.Target.Sparse {
				if isNull, err := d.ReadNull(elem); err != nil || isNull {
					out = append(out, nil)
					if err != nil {
						return nil, err
					}
					continue
				}
			}
			v, err := readValue(d, elem)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
	case smithy.TypeMap:
		if err := d.ReadMap(m); err != nil {
			return nil, err
		}
		out := []entry{}
		for {
			k, more, err := d.ReadMapKey()
			if err != nil || !more {
				return out, err
			}
			v, err := readValue(d, m.Target.Members[1])
			if err != nil {
				return nil, err
			}
			out = append(out, entry{key: k, value: v})
		}
	case smithy.TypeUnit:
	}
	return nil, fmt.Errorf("test fixture: no read for %s", m.Target.Type)
}

// The CloudWatch shapes of the phase 1 operations. Output members are in
// the order of the response vectors, which the serializer follows.
var (
	dimension  = structure("com.amazonaws.cloudwatch#Dimension", member("Name", str), member("Value", str))
	dimensions = listOf("com.amazonaws.cloudwatch#Dimensions", dimension)
	tag        = structure("com.amazonaws.cloudwatch#Tag", member("Key", str), member("Value", str))
	tagList    = listOf("com.amazonaws.cloudwatch#TagList", tag)

	putMetricAlarmInput = structure("com.amazonaws.cloudwatch#PutMetricAlarmInput",
		member("AlarmName", str), member("AlarmDescription", str), member("ActionsEnabled", boolean),
		member("AlarmActions", stringList), member("MetricName", str), member("Namespace", str),
		member("Statistic", str), member("Dimensions", dimensions), member("Period", integer),
		member("EvaluationPeriods", integer), member("Threshold", double),
		member("ComparisonOperator", str), member("TreatMissingData", str), member("Tags", tagList))

	describeAlarmsInput = structure("com.amazonaws.cloudwatch#DescribeAlarmsInput",
		member("AlarmNames", stringList), member("AlarmTypes", stringList),
		member("MaxRecords", integer), member("NextToken", str))

	metricAlarm = structure("com.amazonaws.cloudwatch#MetricAlarm",
		member("AlarmName", str), member("AlarmArn", str), member("AlarmDescription", str),
		member("ActionsEnabled", boolean), member("AlarmActions", stringList), member("OKActions", stringList),
		member("InsufficientDataActions", stringList), member("StateValue", str),
		member("StateUpdatedTimestamp", timestamp), member("AlarmConfigurationUpdatedTimestamp", timestamp),
		member("MetricName", str), member("Namespace", str), member("Statistic", str),
		member("Dimensions", dimensions), member("Period", integer), member("EvaluationPeriods", integer),
		member("Threshold", double), member("ComparisonOperator", str), member("TreatMissingData", str))

	describeAlarmsOutput = structure("com.amazonaws.cloudwatch#DescribeAlarmsOutput",
		member("MetricAlarms", listOf("com.amazonaws.cloudwatch#MetricAlarms", metricAlarm)),
		member("NextToken", str))

	metricDatum = structure("com.amazonaws.cloudwatch#MetricDatum",
		member("MetricName", str), member("Dimensions", dimensions), member("Timestamp", timestamp),
		member("Value", double), member("Unit", str))

	putMetricDataInput = structure("com.amazonaws.cloudwatch#PutMetricDataInput",
		member("Namespace", str), member("MetricData", listOf("com.amazonaws.cloudwatch#MetricData", metricDatum)))

	metric     = structure("com.amazonaws.cloudwatch#Metric", member("Namespace", str), member("MetricName", str), member("Dimensions", dimensions))
	metricStat = structure("com.amazonaws.cloudwatch#MetricStat", member("Metric", metric), member("Period", integer), member("Stat", str))

	metricDataQuery = structure("com.amazonaws.cloudwatch#MetricDataQuery",
		member("Id", str), member("MetricStat", metricStat), member("ReturnData", boolean))

	getMetricDataInput = structure("com.amazonaws.cloudwatch#GetMetricDataInput",
		member("MetricDataQueries", listOf("com.amazonaws.cloudwatch#MetricDataQueries", metricDataQuery)),
		member("StartTime", timestamp), member("EndTime", timestamp), member("ScanBy", str),
		member("MaxDatapoints", integer))

	metricDataResult = structure("com.amazonaws.cloudwatch#MetricDataResult",
		member("Id", str), member("Label", str), member("StatusCode", str),
		member("Timestamps", listOf("com.amazonaws.cloudwatch#Timestamps", timestamp)),
		member("Values", listOf("com.amazonaws.cloudwatch#DatapointValues", double)))

	getMetricDataOutput = structure("com.amazonaws.cloudwatch#GetMetricDataOutput",
		member("MetricDataResults", listOf("com.amazonaws.cloudwatch#MetricDataResults", metricDataResult)))

	listTagsForResourceOutput = structure("com.amazonaws.cloudwatch#ListTagsForResourceOutput", member("Tags", tagList))

	tagResourceOutput = structure("com.amazonaws.cloudwatch#TagResourceOutput")
)

// cloudWatch is a CloudWatch descriptor with the operations the tests
// use; its namespace and service name are the model's.
func cloudWatch() *registry.Service {
	svc := &registry.Service{
		ID:               "cloudwatch",
		Shape:            "com.amazonaws.cloudwatch#GraniteServiceVersion20100801",
		SigningName:      "monitoring",
		Protocols:        []ref.ProtocolID{ref.RPCv2CBOR, ref.AWSJSON10, ref.AWSQuery},
		JSONTargetPrefix: "GraniteServiceVersion20100801",
		QueryVersion:     "2010-08-01",
		QueryCompatible:  true,
	}
	addOperations(svc, []opSpec{
		{"DeleteAlarms", registry.Modeled, structure("com.amazonaws.cloudwatch#DeleteAlarmsInput", member("AlarmNames", stringList)), unit},
		{"DescribeAlarms", registry.Modeled, describeAlarmsInput, describeAlarmsOutput},
		{"GetMetricData", registry.Modeled, getMetricDataInput, getMetricDataOutput},
		{"ListTagsForResource", registry.Modeled, structure("com.amazonaws.cloudwatch#ListTagsForResourceInput", member("ResourceARN", str)), listTagsForResourceOutput},
		{"PutMetricAlarm", registry.Modeled, putMetricAlarmInput, unit},
		{"PutMetricData", registry.Modeled, putMetricDataInput, unit},
		{"SweepAlarms", registry.Internal, structure("dev.understudy#SweepAlarmsInput"), unit},
		{"TagResource", registry.Modeled, structure("com.amazonaws.cloudwatch#TagResourceInput", member("ResourceARN", str), member("Tags", tagList)), tagResourceOutput},
		{"UntagResource", registry.Modeled, structure("com.amazonaws.cloudwatch#UntagResourceInput", member("ResourceARN", str), member("TagKeys", stringList)), structure("com.amazonaws.cloudwatch#UntagResourceOutput")},
	})
	return svc
}

// sqsJSON speaks awsJson only, so the CBOR route never claims it.
func sqsJSON() *registry.Service {
	svc := &registry.Service{
		ID:               "sqs",
		Shape:            "com.amazonaws.sqs#AmazonSQS",
		SigningName:      "sqs",
		Protocols:        []ref.ProtocolID{ref.AWSJSON10},
		JSONTargetPrefix: "AmazonSQS",
	}
	addOperations(svc, []opSpec{{"ListQueues", registry.Modeled, structure("com.amazonaws.sqs#ListQueuesRequest"), unit}})
	return svc
}

type opSpec struct {
	name   string
	kind   registry.OperationKind
	input  *smithy.Schema
	output *smithy.Schema
}

func addOperations(svc *registry.Service, ops []opSpec) {
	for i, o := range ops {
		svc.Operations = append(svc.Operations, &registry.Operation{
			Service: svc,
			ID:      ref.OperationID{Service: svc.ID, Name: o.name},
			Ordinal: i,
			Kind:    o.kind,
			Shape:   smithy.ShapeID(svc.Shape.Namespace() + "#" + o.name),
			Input:   o.input,
			Output:  o.output,
		})
	}
}

func newRegistry(t *testing.T) (*registry.Registry, *registry.Service) {
	t.Helper()
	cw := cloudWatch()
	reg, err := registry.New(cw, sqsJSON())
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	return reg, cw
}

func operation(t *testing.T, svc *registry.Service, name string) *registry.Operation {
	t.Helper()
	op, ok := svc.Operation(name)
	if !ok {
		t.Fatalf("no operation %s", name)
	}
	return op
}

// goRequest builds a request as aws-sdk-go-v2 cloudwatch v1.73.0 sends it
// (wire-cbor.md section 4).
func goRequest(t *testing.T, op string, body []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/service/GraniteServiceVersion20100801/operation/"+op, bytes.NewReader(body))
	r.Header.Set("Smithy-Protocol", "rpc-v2-cbor")
	r.Header.Set("Content-Type", "application/cbor")
	r.Header.Set("Accept", "application/cbor")
	r.Header.Set("X-Amzn-Query-Mode", "true")
	return r
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex in test: %v", err)
	}
	return b
}
