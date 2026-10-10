package rpcv2cbor_test

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/rpcv2cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

func decodeOK(t *testing.T, op *registry.Operation, body []byte) dynStruct {
	t.Helper()
	in := dynStruct{schema: op.Input}
	if err := (rpcv2cbor.Codec{}).Decode(goRequest(t, op.ID.Name, body), op, &in); err != nil {
		t.Fatalf("Decode: %s: %s", err.ID.Shape, err.Message)
	}
	return in
}

func fields(s *smithy.Schema, kv ...any) dynStruct {
	out := dynStruct{schema: s, fields: map[string]any{}}
	for pair := range slices.Chunk(kv, 2) {
		name, ok := pair[0].(string)
		if !ok || len(pair) != 2 {
			panic("fields: arguments must be member name and value pairs")
		}
		out.fields[name] = pair[1]
	}
	return out
}

func checkDecoded(t *testing.T, got, want dynStruct) {
	t.Helper()
	if !reflect.DeepEqual(got.fields, want.fields) {
		t.Errorf("decoded\n got %#v\nwant %#v", got.fields, want.fields)
	}
}

// The request bodies aws-sdk-go-v2 cloudwatch v1.73.0 sends (wire-cbor.md
// section 4): indefinite maps and lists, integer-valued doubles as fb,
// tag 1 timestamps.
func TestDecodeGoClientRequests(t *testing.T) {
	_, cw := newRegistry(t)
	tf := func(k, v string) dynStruct { return fields(tag, "Key", k, "Value", v) }
	dim := func(n, v string) dynStruct { return fields(dimension, "Name", n, "Value", v) }
	tests := []struct {
		name, op, hex string
		want          dynStruct
	}{
		{
			"G01 PutMetricAlarm", "PutMetricAlarm",
			"bf6e416374696f6e73456e61626c6564f56c416c61726d416374696f6e739f782461726e3a6177733a736e733a75732d656173742d313a3030303030303030303030303a74ff70416c61726d4465736372697074696f6e646465736369416c61726d4e616d656874662d616c61726d72436f6d70617269736f6e4f70657261746f7274477265617465725468616e5468726573686f6c646a44696d656e73696f6e739fbf644e616d656951756575654e616d656556616c75656171ffff714576616c756174696f6e506572696f6473016a4d65747269634e616d657822417070726f78696d6174654e756d6265724f664d6573736167657356697369626c65694e616d657370616365674157532f53515366506572696f6419012c695374617469737469636353756d64546167739fbf634b657963656e766556616c756563646576ffbf634b6579647465616d6556616c75656178ffff695468726573686f6c64fb40240000000000007054726561744d697373696e6744617461676d697373696e67ff",
			fields(putMetricAlarmInput,
				"ActionsEnabled", true, "AlarmActions", []any{"arn:aws:sns:us-east-1:000000000000:t"},
				"AlarmDescription", "desc", "AlarmName", "tf-alarm", "ComparisonOperator", "GreaterThanThreshold",
				"Dimensions", []any{dim("QueueName", "q")}, "EvaluationPeriods", int32(1),
				"MetricName", "ApproximateNumberOfMessagesVisible", "Namespace", "AWS/SQS", "Period", int32(300),
				"Statistic", "Sum", "Tags", []any{tf("env", "dev"), tf("team", "x")}, "Threshold", 10.0,
				"TreatMissingData", "missing"),
		},
		{"G02 DescribeAlarms empty", "DescribeAlarms", "bfff", fields(describeAlarmsInput)},
		{
			"G03 DescribeAlarms by name", "DescribeAlarms",
			"bf6a416c61726d4e616d65739f6874662d616c61726dff6a416c61726d54797065739f6b4d6574726963416c61726dffff",
			fields(describeAlarmsInput, "AlarmNames", []any{"tf-alarm"}, "AlarmTypes", []any{"MetricAlarm"}),
		},
		{
			"G04 DescribeAlarms paging", "DescribeAlarms", "bf6a4d61785265636f7264731864694e657874546f6b656e63746f6bff",
			fields(describeAlarmsInput, "MaxRecords", int32(100), "NextToken", "tok"),
		},
		{
			"G05 ListTagsForResource", "ListTagsForResource",
			"bf6b5265736f7572636541524e783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726dff",
			fields(operation(t, cw, "ListTagsForResource").Input, "ResourceARN", "arn:aws:cloudwatch:us-east-1:000000000000:alarm:tf-alarm"),
		},
		{
			"G06 TagResource", "TagResource",
			"bf6b5265736f7572636541524e783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726d64546167739fbf634b657963656e766556616c756563646576ffffff",
			fields(operation(t, cw, "TagResource").Input,
				"ResourceARN", "arn:aws:cloudwatch:us-east-1:000000000000:alarm:tf-alarm", "Tags", []any{tf("env", "dev")}),
		},
		{
			"G07 UntagResource", "UntagResource",
			"bf6b5265736f7572636541524e783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726d675461674b6579739f63656e76ffff",
			fields(operation(t, cw, "UntagResource").Input,
				"ResourceARN", "arn:aws:cloudwatch:us-east-1:000000000000:alarm:tf-alarm", "TagKeys", []any{"env"}),
		},
		{"G08 DeleteAlarms", "DeleteAlarms", "bf6a416c61726d4e616d65739f6874662d616c61726dffff", fields(operation(t, cw, "DeleteAlarms").Input, "AlarmNames", []any{"tf-alarm"})},
		{
			"G09 PutMetricData", "PutMetricData",
			"bf6a4d6574726963446174619fbf6a44696d656e73696f6e739fbf644e616d656553746167656556616c756563646576ffff6a4d65747269634e616d65644a6f62736954696d657374616d70c1fb41da55cd6947df3b64556e697465436f756e746556616c7565fb3ff8000000000000ffff694e616d65737061636563417070ff",
			fields(putMetricDataInput, "Namespace", "App", "MetricData", []any{fields(metricDatum,
				"Dimensions", []any{dim("Stage", "dev")}, "MetricName", "Jobs",
				"Timestamp", time.UnixMilli(1767323045123).UTC(), "Unit", "Count", "Value", 1.5)}),
		},
		{
			"G10 GetMetricData", "GetMetricData",
			"bf67456e6454696d65c1fb41da55d0ed47df3b6d4d617844617461706f696e74731864714d657472696344617461517565726965739fbf624964626d316a4d657472696353746174bf664d6574726963bf6a44696d656e73696f6e739fbf644e616d656553746167656556616c756563646576ffff6a4d65747269634e616d65644a6f6273694e616d65737061636563417070ff66506572696f64183c64537461746353756dff6a52657475726e44617461f5ffff665363616e42797354696d657374616d7044657363656e64696e6769537461727454696d65c1fb41da55cd6947df3bff",
			fields(getMetricDataInput,
				"EndTime", time.UnixMilli(1767326645123).UTC(), "MaxDatapoints", int32(100),
				"MetricDataQueries", []any{fields(metricDataQuery,
					"Id", "m1", "ReturnData", true,
					"MetricStat", fields(metricStat,
						"Metric", fields(metric, "Dimensions", []any{dim("Stage", "dev")}, "MetricName", "Jobs", "Namespace", "App"),
						"Period", int32(60), "Stat", "Sum"))},
				"ScanBy", "TimestampDescending", "StartTime", time.UnixMilli(1767323045123).UTC()),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkDecoded(t, decodeOK(t, operation(t, cw, tt.op), unhex(t, tt.hex)), tt.want)
		})
	}
}

// PutMetricData bodies of 10240 bytes or more arrive gzipped
// (wire-cbor.md 3.2).
func TestDecodeGzippedBody(t *testing.T) {
	_, cw := newRegistry(t)
	op := operation(t, cw, "PutMetricData")
	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	if _, err := zw.Write(unhex(t, "bf694e616d65737061636563417070ff")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	r := goRequest(t, "PutMetricData", z.Bytes())
	r.Header.Set("Content-Encoding", "gzip")
	in := dynStruct{schema: op.Input}
	if err := (rpcv2cbor.Codec{}).Decode(r, op, &in); err != nil {
		t.Fatalf("Decode: %+v", err)
	}
	checkDecoded(t, in, fields(putMetricDataInput, "Namespace", "App"))
}

func TestDecodeRefusesAnExpandingGzipBody(t *testing.T) {
	_, cw := newRegistry(t)
	op := *operation(t, cw, "PutMetricData")
	op.MaxRequestBytes = 64
	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	if _, err := zw.Write(make([]byte, 1000)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	r := goRequest(t, "PutMetricData", z.Bytes())
	r.Header.Set("Content-Encoding", "gzip")
	err := (rpcv2cbor.Codec{}).Decode(r, &op, &dynStruct{schema: op.Input})
	if err == nil || err.ID != awserr.RequestEntityTooLarge() {
		t.Fatalf("Decode = %+v, want RequestEntityTooLarge", err)
	}
}

// An empty body, an empty definite map and an empty indefinite map are all
// an input with no member set (TEST empty_input, empty_input_no_body,
// NoInputServerAllowsEmptyCbor).
func TestDecodeEmptyInputs(t *testing.T) {
	_, cw := newRegistry(t)
	op := operation(t, cw, "DescribeAlarms")
	for _, h := range []string{"", "a0", "bfff"} {
		checkDecoded(t, decodeOK(t, op, unhex(t, h)), fields(describeAlarmsInput))
	}
}

// Shapes of the Smithy rpcv2Cbor corpus (smithy-protocol-tests 1.74.0).
var (
	simpleScalars = structure("smithy.protocoltests.rpcv2Cbor#SimpleScalarStructure",
		member("trueBooleanValue", boolean), member("falseBooleanValue", boolean),
		member("byteValue", byteShape), member("doubleValue", double), member("floatValue", float),
		member("integerValue", integer), member("longValue", long), member("shortValue", short),
		member("stringValue", str), member("blobValue", blob))
	stringListInput = structure("smithy.protocoltests.rpcv2Cbor#RpcV2CborListInputOutput", member("stringList", stringList))
	myUnion         = union("smithy.protocoltests.rpcv2Cbor#MyUnion", member("stringValue", str), member("booleanValue", boolean), member("numberValue", integer))
	unionInput      = structure("smithy.protocoltests.rpcv2Cbor#UnionInputOutput", member("contents", myUnion))
)

func corpusOp(input *smithy.Schema) *registry.Operation {
	svc := &registry.Service{ID: "rpcv2protocol", Shape: "smithy.protocoltests.rpcv2Cbor#RpcV2Protocol", Protocols: []ref.ProtocolID{ref.RPCv2CBOR}}
	return &registry.Operation{Service: svc, ID: ref.OperationID{Service: svc.ID, Name: "Op"}, Kind: registry.Modeled, Input: input, Output: unit}
}

func TestDecodeCorpusRequests(t *testing.T) {
	scalars := fields(simpleScalars, "byteValue", int8(5), "doubleValue", 1.889, "falseBooleanValue", false,
		"floatValue", float32(7.625), "integerValue", int32(256), "longValue", int64(9873), "shortValue", int16(9898),
		"stringValue", "simple", "trueBooleanValue", true, "blobValue", []byte("foo"))
	tests := []struct {
		name  string
		input *smithy.Schema
		hex   string
		want  dynStruct
	}{
		{"S01 empty_input", simpleScalars, "bfff", fields(simpleScalars)},
		{
			"S02 indefinite keys and strings", simpleScalars,
			"aa696279746556616c7565057f66646f75626c656556616c7565fffb3ffe395810624dd37f6566616c736567426f6f6c65616e6556616c7565fff46a666c6f617456616c7565fa40f400006c696e746567657256616c7565190100696c6f6e6756616c75651926916a73686f727456616c75651926aa7f66737472696e676556616c7565ff6673696d706c657074727565426f6f6c65616e56616c7565f569626c6f6256616c756543666f6f",
			scalars,
		},
		{
			"S03 upcasting: half floats, wide integers", simpleScalars,
			"bf6b646f75626c6556616c7565f93e006a666c6f617456616c7565f947a06c696e746567657256616c75651838696c6f6e6756616c75651901006a73686f727456616c75650aff",
			fields(simpleScalars, "doubleValue", 1.5, "floatValue", float32(7.625), "integerValue", int32(56), "longValue", int64(256), "shortValue", int16(10)),
		},
		{"S06 null member is absent", simpleScalars, "bf6b737472696e6756616c7565f6ff", fields(simpleScalars)},
		{
			"S07 indefinite byte string", simpleScalars,
			"a169626c6f6256616c75655f5822416e206578616d706c6520696e646566696e6974652d6279746520737472696e672c51206368756e6b6564206f6e20636f6d6d61ff",
			fields(simpleScalars, "blobValue", []byte("An example indefinite-byte string, chunked on comma")),
		},
		{
			"S08 indefinite strings in a definite list", stringListInput,
			"a16a737472696e674c697374837f781d416e206578616d706c6520696e646566696e69746520737472696e672c772077686963682077696c6c206265206368756e6b65642c6e206f6e206561636820636f6d6d61ff7f7835416e6f74686572206578616d706c6520696e646566696e69746520737472696e672077697468206f6e6c79206f6e65206368756e6bff7654686973206973206120706c61696e20737472696e67",
			fields(stringListInput, "stringList", []any{
				"An example indefinite string, which will be chunked, on each comma",
				"Another example indefinite string with only one chunk", "This is a plain string",
			}),
		},
		{
			"S12 extra fields are skipped", simpleScalars,
			"bf696279746556616c7565056b646f75626c6556616c7565fb3ffe395810624dd37166616c7365426f6f6c65616e56616c7565f46a666c6f617456616c7565fa40f400006b65787472614f626a656374bf73696e646566696e6974654c656e6774684d6170bf6b77697468416e41727261799f010203ffff71646566696e6974654c656e6774684d6170a3727769746841446566696e697465417272617983010203781d616e64536f6d65496e646566696e6974654c656e677468537472696e67781f74686174206861732c206265656e206368756e6b6564206f6e20636f6d6d616c6e6f726d616c537472696e6763666f6f6a73686f727456616c756519270f6e736f6d654f746865724669656c6476746869732073686f756c6420626520736b6970706564ff6c696e746567657256616c7565190100696c6f6e6756616c75651926916a73686f727456616c75651926aa6b737472696e6756616c75656673696d706c657074727565426f6f6c65616e56616c7565f569626c6f6256616c756543666f6fff",
			scalars,
		},
		{
			"S13 union", unionInput, "a268636f6e74656e7473a16b737472696e6756616c756563666f6f6a6f7468657256616c756563626172",
			fields(unionInput, "contents", dynUnion{member: "stringValue", value: "foo"}),
		},
		{
			"union ignores __type and null members", unionInput,
			"a168636f6e74656e7473a3665f5f74797065674d79556e696f6e6c626f6f6c65616e56616c7565f66b737472696e6756616c756563666f6f",
			fields(unionInput, "contents", dynUnion{member: "stringValue", value: "foo"}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkDecoded(t, decodeOK(t, corpusOp(tt.input), unhex(t, tt.hex)), tt.want)
		})
	}
}

func TestDecodeNaNInputs(t *testing.T) {
	// S04 RpcV2CborSupportsNaNFloatInputs.
	got := decodeOK(t, corpusOp(simpleScalars), unhex(t, "bf6b646f75626c6556616c7565fb7ff80000000000006a666c6f617456616c7565fa7fc00000ff"))
	d, okD := got.fields["doubleValue"].(float64)
	f, okF := got.fields["floatValue"].(float32)
	if !okD || !okF || !math.IsNaN(d) || !math.IsNaN(float64(f)) {
		t.Errorf("decoded %#v, want NaN for doubleValue and floatValue", got.fields)
	}
}

// A body that does not fit the input is the service's malformed-request
// answer, SerializationException by default, with a message that says
// what is wrong and where.
func TestDecodeRefusals(t *testing.T) {
	_, cw := newRegistry(t)
	tests := []struct {
		name, op, hex string
		mention       string
	}{
		{"JSON body", "DescribeAlarms", "7b22416c61726d4e616d6573223a5b5d7d", "map"},
		{"integer beyond its member", "DescribeAlarms", "a16a4d61785265636f7264731b0000000100000000", "MaxRecords"},
		{"integer member sent as text", "DescribeAlarms", "a16a4d61785265636f7264736333303000", "MaxRecords"},
		{"untagged timestamp", "PutMetricData", "a16a4d65747269634461746181a16954696d657374616d70fb41da55cd6947df3b", "Timestamp"},
		{"trailing bytes", "DescribeAlarms", "a000", "follow"},
		{"truncated body", "DescribeAlarms", "bf6a416c61726d4e616d65739f6874662d61", "AlarmNames element"},
		{"non-text map key", "DescribeAlarms", "a10102", "key"},
		{"nesting beyond 128 levels", "DescribeAlarms", "a1614a" + strings.Repeat("81", 130) + "00", "nest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			op := operation(t, cw, tt.op)
			err := (rpcv2cbor.Codec{}).Decode(goRequest(t, tt.op, unhex(t, tt.hex)), op, &dynStruct{schema: op.Input})
			if err == nil {
				t.Fatal("Decode succeeded, want SerializationException")
			}
			if err.ID != awserr.SerializationException() {
				t.Errorf("Decode error %s, want SerializationException", err.ID.Shape)
			}
			if !strings.Contains(err.Message, tt.mention) || !strings.Contains(err.Message, tt.op) {
				t.Errorf("message %q does not name %q and the operation", err.Message, tt.mention)
			}
		})
	}
}

func TestDecodeUnionRefusals(t *testing.T) {
	for name, h := range map[string]string{
		"no member set":         "a168636f6e74656e7473a0",
		"two members set":       "a168636f6e74656e7473a26b737472696e6756616c756563666f6f6c626f6f6c65616e56616c7565f5",
		"only a __type set":     "a168636f6e74656e7473a1665f5f74797065674d79556e696f6e",
		"same member set twice": "a168636f6e74656e7473a26b737472696e6756616c756563666f6f6b737472696e6756616c756563626172",
	} {
		t.Run(name, func(t *testing.T) {
			op := corpusOp(unionInput)
			err := (rpcv2cbor.Codec{}).Decode(goRequest(t, "Op", unhex(t, h)), op, &dynStruct{schema: op.Input})
			if err == nil || err.ID != awserr.SerializationException() || !strings.Contains(err.Message, "MyUnion") {
				t.Errorf("Decode = %+v, want SerializationException naming the union", err)
			}
		})
	}
}

// A service whose overlay sets a malformedRequestError answers with it.
func TestDecodeUsesTheServiceMalformedRequestError(t *testing.T) {
	op := corpusOp(simpleScalars)
	custom := awserr.Identity{Shape: "smithy.protocoltests.rpcv2Cbor#Malformed", JSONType: "Malformed", QueryCode: "Malformed", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageLower}
	op.Service.MalformedRequestError = custom
	err := (rpcv2cbor.Codec{}).Decode(goRequest(t, "Op", unhex(t, "ff")), op, &dynStruct{schema: op.Input})
	if err == nil || err.ID != custom {
		t.Errorf("Decode = %+v, want the service's malformedRequestError", err)
	}
}

func TestDecodeRefusesUnknownContentEncoding(t *testing.T) {
	_, cw := newRegistry(t)
	op := operation(t, cw, "DescribeAlarms")
	r := goRequest(t, "DescribeAlarms", unhex(t, "a0"))
	r.Header.Set("Content-Encoding", "br")
	if err := (rpcv2cbor.Codec{}).Decode(r, op, &dynStruct{schema: op.Input}); err == nil || err.ID != awserr.SerializationException() {
		t.Errorf("Decode = %+v, want SerializationException", err)
	}
}

type failingBody struct{ err error }

func (f failingBody) Read([]byte) (int, error) { return 0, f.err }

// The edge's body cap reports its own error through BoundaryError, which
// Decode passes on; any other read failure is the client going away.
func TestDecodeBodyReadFailures(t *testing.T) {
	_, cw := newRegistry(t)
	op := operation(t, cw, "DescribeAlarms")
	tooLarge := &awserr.Error{ID: awserr.RequestEntityTooLarge(), Message: "too large", Origin: awserr.Generic{}}
	for _, tt := range []struct {
		name string
		err  error
		want awserr.Identity
	}{
		{"body cap", tooLarge.AsError(), awserr.RequestEntityTooLarge()},
		{"client went away", errors.New("connection reset"), awserr.RequestAbortedException()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := goRequest(t, "DescribeAlarms", nil)
			r.Body = io.NopCloser(failingBody{err: tt.err})
			if err := (rpcv2cbor.Codec{}).Decode(r, op, &dynStruct{schema: op.Input}); err == nil || err.ID != tt.want {
				t.Errorf("Decode = %+v, want %s", err, tt.want.Shape)
			}
		})
	}
}
