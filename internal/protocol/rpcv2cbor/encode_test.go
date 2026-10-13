package rpcv2cbor_test

import (
	"context"
	"encoding/hex"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/rpcv2cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

func encodeOK(t *testing.T, op *registry.Operation, out *dynStruct) rpcv2cbor.Response {
	t.Helper()
	resp, err := rpcv2cbor.Codec{}.Encode(op, "req-1", out, http.StatusOK)
	if err != nil {
		t.Fatalf("Encode: %+v", err)
	}
	return resp
}

func checkBody(t *testing.T, got []byte, wantHex string) {
	t.Helper()
	if hex.EncodeToString(got) != wantHex {
		t.Errorf("body\n got %x\nwant %s", got, wantHex)
	}
}

// A Unit output has no body and no Content-Type (G11), even when the
// handler's value carries members: one member panics the Go client (G22).
func TestEncodeUnitOutputHasNoBody(t *testing.T) {
	_, cw := newRegistry(t)
	for _, name := range []string{"PutMetricAlarm", "DeleteAlarms", "PutMetricData"} {
		t.Run(name, func(t *testing.T) {
			op := operation(t, cw, name)
			stray := fields(structure("test#Stray", member("x", integer)), "x", int32(1))
			resp := encodeOK(t, op, &stray)
			if resp.Status != http.StatusOK || len(resp.Body) != 0 {
				t.Errorf("Encode = %d with %x, want 200 and no body", resp.Status, resp.Body)
			}
			want := http.Header{"Smithy-Protocol": {"rpc-v2-cbor"}, "X-Amzn-Requestid": {"req-1"}, "Content-Length": {"0"}}
			if !equalHeader(resp.Header, want) {
				t.Errorf("headers = %v, want %v", resp.Header, want)
			}
		})
	}
}

func equalHeader(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || len(w) != len(v) || (len(v) > 0 && w[0] != v[0]) {
			return false
		}
	}
	return true
}

// The success responses the Go client accepted (wire-cbor.md 8.1),
// reproduced byte for byte from typed output values.
func TestEncodeGoClientResponses(t *testing.T) {
	_, cw := newRegistry(t)
	stamp := time.UnixMilli(1767323045123).UTC()
	arn := "arn:aws:cloudwatch:us-east-1:000000000000:alarm:tf-alarm"
	tf := func(k, v string) dynStruct { return fields(tag, "Key", k, "Value", v) }
	tests := []struct {
		name, op string
		out      dynStruct
		hex      string
	}{
		{"G12 TagResource", "TagResource", fields(tagResourceOutput), "a0"},
		{"G13 DescribeAlarms none", "DescribeAlarms", fields(describeAlarmsOutput, "MetricAlarms", []any{}), "a16c4d6574726963416c61726d7380"},
		{
			"G14 DescribeAlarms one", "DescribeAlarms",
			fields(describeAlarmsOutput, "MetricAlarms", []any{fields(metricAlarm,
				"AlarmName", "tf-alarm", "AlarmArn", arn, "AlarmDescription", "desc", "ActionsEnabled", true,
				"AlarmActions", []any{"arn:aws:sns:us-east-1:000000000000:t"}, "OKActions", []any{},
				"InsufficientDataActions", []any{}, "StateValue", "INSUFFICIENT_DATA",
				"StateUpdatedTimestamp", stamp, "AlarmConfigurationUpdatedTimestamp", stamp,
				"MetricName", "ApproximateNumberOfMessagesVisible", "Namespace", "AWS/SQS", "Statistic", "Sum",
				"Dimensions", []any{fields(dimension, "Name", "QueueName", "Value", "q")},
				"Period", int32(300), "EvaluationPeriods", int32(1), "Threshold", 10.0,
				"ComparisonOperator", "GreaterThanThreshold", "TreatMissingData", "missing")}),
			"a16c4d6574726963416c61726d7381b369416c61726d4e616d656874662d616c61726d68416c61726d41726e783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726d70416c61726d4465736372697074696f6e64646573636e416374696f6e73456e61626c6564f56c416c61726d416374696f6e7381782461726e3a6177733a736e733a75732d656173742d313a3030303030303030303030303a74694f4b416374696f6e738077496e73756666696369656e7444617461416374696f6e73806a537461746556616c756571494e53554646494349454e545f444154417553746174655570646174656454696d657374616d70c1fb41da55cd6947df3b7822416c61726d436f6e66696775726174696f6e5570646174656454696d657374616d70c1fb41da55cd6947df3b6a4d65747269634e616d657822417070726f78696d6174654e756d6265724f664d6573736167657356697369626c65694e616d657370616365674157532f535153695374617469737469636353756d6a44696d656e73696f6e7381a2644e616d656951756575654e616d656556616c7565617166506572696f6419012c714576616c756174696f6e506572696f647301695468726573686f6c64fb402400000000000072436f6d70617269736f6e4f70657261746f7274477265617465725468616e5468726573686f6c647054726561744d697373696e6744617461676d697373696e67",
		},
		{
			"G15 ListTagsForResource", "ListTagsForResource",
			fields(listTagsForResourceOutput, "Tags", []any{tf("env", "dev"), tf("team", "x")}),
			"a1645461677382a2634b657963656e766556616c756563646576a2634b6579647465616d6556616c75656178",
		},
		{
			"G16 GetMetricData", "GetMetricData",
			fields(getMetricDataOutput, "MetricDataResults", []any{fields(metricDataResult,
				"Id", "m1", "Label", "Jobs", "StatusCode", "Complete",
				"Timestamps", []any{time.Unix(1767323040, 0), time.Unix(1767323100, 0)},
				"Values", []any{1.5, 2.0})}),
			"a1714d657472696344617461526573756c747381a5624964626d31654c6162656c644a6f62736a537461747573436f646568436f6d706c6574656a54696d657374616d707382c1fb41da55cd68000000c1fb41da55cd770000006656616c75657382fb3ff8000000000000fb4000000000000000",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := encodeOK(t, operation(t, cw, tt.op), &tt.out)
			checkBody(t, resp.Body, tt.hex)
			want := http.Header{
				"Smithy-Protocol": {"rpc-v2-cbor"}, "Content-Type": {"application/cbor"},
				"X-Amzn-Requestid": {"req-1"}, "Content-Length": {strconv.Itoa(len(resp.Body))},
			}
			if !equalHeader(resp.Header, want) {
				t.Errorf("headers = %v, want %v", resp.Header, want)
			}
		})
	}
}

// Floats keep their modeled width and NaN its canonical bits
// (RpcV2CborSupportsNaNFloatInputs); a sparse list keeps its nulls.
func TestEncodeScalarForms(t *testing.T) {
	sparse := listOf("test#SparseStrings", str)
	sparse.Sparse = true
	out := structure("test#Scalars", member("double", double), member("float", float), member("sparse", sparse),
		member("byte", byteShape), member("short", short), member("long", long), member("blob", blob), member("big", bigInt))
	op := corpusOp(simpleScalars)
	op.Output = out
	v := fields(out, "double", math.NaN(), "float", float32(math.NaN()), "sparse", []any{"a", nil},
		"byte", int8(-1), "short", int16(300), "long", int64(-1)<<40, "blob", []byte("foo"), "big", bigIntOf(t, "18446744073709551615"))
	resp := encodeOK(t, op, &v)
	checkBody(t, resp.Body, "a866646f75626c65fb7ff8000000000000"+"65666c6f6174fa7fc00000"+"66737061727365826161f6"+
		"6462797465"+"20"+"6573686f7274"+"19012c"+"646c6f6e67"+"3b000000ffffffffff"+"64626c6f62"+"43666f6f"+"63626967"+"1bffffffffffffffff")
}

// Documents and decimal values have no form in the subset; a handler that
// returns one is an emulator bug, answered with InternalFailure.
func TestEncodeUnsupportedValuesAreInternal(t *testing.T) {
	doc := structure("test#Doc", member("doc", scalar("smithy.api#Document", smithy.TypeDocument)))
	op := corpusOp(simpleScalars)
	op.Output = doc
	_, err := rpcv2cbor.Codec{}.Encode(op, "req-1", &docOutput{schema: doc}, http.StatusOK)
	if err == nil || err.ID != awserr.InternalFailure() {
		t.Errorf("Encode = %+v, want InternalFailure", err)
	}
}

type docOutput struct{ schema *smithy.Schema }

func (d *docOutput) Serialize(s smithy.Serializer) {
	s.WriteStruct(nil, d.schema)
	s.WriteDocument(d.schema.Members[0], document.String("x"))
	s.CloseStruct()
}

func cwIdentity(name, queryCode string, status int, fault awserr.Fault, key awserr.MessageKey) awserr.Identity {
	return awserr.Identity{
		Shape:     smithy.ShapeID("com.amazonaws.cloudwatch#" + name),
		JSONType:  "com.amazonaws.cloudwatch#" + name,
		QueryCode: queryCode, Status: status, Fault: fault, MessageKey: key,
	}
}

// The CloudWatch errors the Go client resolved to its typed errors
// (wire-cbor.md 5.2, vectors G17 to G21), byte for byte with their
// status and x-amzn-query-error.
func TestEncodeErrorGoClientVectors(t *testing.T) {
	_, cw := newRegistry(t)
	arn := "arn:aws:cloudwatch:us-east-1:000000000000:alarm:tf-alarm"
	tests := []struct {
		name       string
		err        *awserr.Error
		status     int
		queryError string
		hex        string
	}{
		{
			"G17 ResourceNotFound",
			&awserr.Error{ID: cwIdentity("ResourceNotFound", "ResourceNotFound", 404, awserr.Sender, awserr.MessageLower), Message: "Alarm not found", Origin: awserr.Modeled{}},
			404, "ResourceNotFound;Sender",
			"a2665f5f747970657829636f6d2e616d617a6f6e6177732e636c6f75647761746368235265736f757263654e6f74466f756e64676d6573736167656f416c61726d206e6f7420666f756e64",
		},
		{
			"G18 ResourceNotFoundException",
			&awserr.Error{
				ID:      cwIdentity("ResourceNotFoundException", "ResourceNotFoundException", 404, awserr.Sender, awserr.MessageUpper),
				Message: "Unknown resource.", Fields: []awserr.Field{{Name: "ResourceType", Value: "alarm"}, {Name: "ResourceId", Value: arn}},
				Origin: awserr.Modeled{},
			},
			404, "ResourceNotFoundException;Sender",
			"a4665f5f747970657832636f6d2e616d617a6f6e6177732e636c6f75647761746368235265736f757263654e6f74466f756e64457863657074696f6e674d65737361676571556e6b6e6f776e207265736f757263652e6c5265736f757263655479706565616c61726d6a5265736f757263654964783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726d",
		},
		{
			"G19 InvalidNextToken",
			&awserr.Error{ID: cwIdentity("InvalidNextToken", "InvalidNextToken", 400, awserr.Sender, awserr.MessageLower), Message: "The next token is invalid", Origin: awserr.Modeled{}},
			400, "InvalidNextToken;Sender",
			"a2665f5f747970657829636f6d2e616d617a6f6e6177732e636c6f7564776174636823496e76616c69644e657874546f6b656e676d6573736167657819546865206e65787420746f6b656e20697320696e76616c6964",
		},
		{
			"G20 InternalServiceFault",
			&awserr.Error{ID: cwIdentity("InternalServiceFault", "InternalServiceError", 500, awserr.Receiver, awserr.MessageUpper), Message: "internal error", Origin: awserr.Modeled{}},
			500, "InternalServiceError;Receiver",
			"a2665f5f74797065782d636f6d2e616d617a6f6e6177732e636c6f7564776174636823496e7465726e616c536572766963654661756c74674d6573736167656e696e7465726e616c206572726f72",
		},
		{
			"G21 ValidationException",
			&awserr.Error{ID: cwIdentity("ValidationException", "ValidationError", 400, awserr.Sender, awserr.MessageLower), Message: "bad input", Origin: awserr.Validation{}},
			400, "ValidationError;Sender",
			"a2665f5f74797065782c636f6d2e616d617a6f6e6177732e636c6f756477617463682356616c69646174696f6e457863657074696f6e676d6573736167656962616420696e707574",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := rpcv2cbor.Codec{}.EncodeError(errorRequest(false), cw, "req-1", tt.err)
			if resp.Status != tt.status {
				t.Errorf("status = %d, want %d", resp.Status, tt.status)
			}
			checkBody(t, resp.Body, tt.hex)
			want := http.Header{
				"Smithy-Protocol": {"rpc-v2-cbor"}, "Content-Type": {"application/cbor"}, "X-Amzn-Requestid": {"req-1"},
				"Content-Length": {strconv.Itoa(len(resp.Body))}, "X-Amzn-Query-Error": {tt.queryError},
			}
			if !equalHeader(resp.Header, want) {
				t.Errorf("headers = %v, want %v", resp.Header, want)
			}
		})
	}
}

// errorRequest is a CloudWatch request an error answers, sending
// x-amzn-query-mode: true when queryMode is set, as the Go client does.
func errorRequest(queryMode bool) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/service/GraniteServiceVersion20100801/operation/SetAlarmState", http.NoBody)
	r.Header.Set("Smithy-Protocol", "rpc-v2-cbor")
	if queryMode {
		r.Header.Set("X-Amzn-Query-Mode", "true")
	}
	return r
}

// A query-compatible service answers a client in query mode with the
// awsQueryError status: AWS returns CloudWatch's ResourceNotFound as 400
// by default and 404 under x-amzn-query-mode: true (error-matrix.md 5.13),
// and the Go client always sends the header.
func TestEncodeErrorQueryModeStatus(t *testing.T) {
	_, cw := newRegistry(t)
	id := cwIdentity("ResourceNotFound", "ResourceNotFound", 400, awserr.Sender, awserr.MessageLower)
	id.QueryModeStatus = http.StatusNotFound
	e := &awserr.Error{ID: id, Message: "Alarm not found", Origin: awserr.Modeled{}}
	for _, tt := range []struct {
		queryMode bool
		svc       *registry.Service
		want      int
	}{
		{queryMode: true, svc: cw, want: http.StatusNotFound},
		{queryMode: false, svc: cw, want: http.StatusBadRequest},
		{queryMode: true, svc: &registry.Service{ID: "plain", Shape: cw.Shape, Protocols: cw.Protocols}, want: http.StatusBadRequest},
	} {
		resp := rpcv2cbor.Codec{}.EncodeError(errorRequest(tt.queryMode), tt.svc, "req-1", e)
		if resp.Status != tt.want {
			t.Errorf("query mode %t, query compatible %t: status = %d, want %d", tt.queryMode, tt.svc.QueryCompatible, resp.Status, tt.want)
		}
	}
}

// canonical is the encoder's form of a corpus body: same tree, definite
// lengths, minimal widths.
func canonical(t *testing.T, h string) string {
	t.Helper()
	v, err := cbor.Decode(unhex(t, h))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	b, err := cbor.Encode(v)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return hex.EncodeToString(b)
}

// RpcV2CborInvalidGreetingError (S09): the modeled Message member, no
// x-amzn-query-error for a service that is not query compatible.
func TestEncodeErrorCorpusInvalidGreeting(t *testing.T) {
	svc := corpusOp(simpleScalars).Service
	e := &awserr.Error{
		ID: awserr.Identity{
			Shape: "smithy.protocoltests.rpcv2Cbor#InvalidGreeting", JSONType: "InvalidGreeting", QueryCode: "InvalidGreeting",
			Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageUpper,
		},
		Message: "Hi", Origin: awserr.Modeled{},
	}
	resp := rpcv2cbor.Codec{}.EncodeError(errorRequest(false), svc, "req-1", e)
	checkBody(t, resp.Body, canonical(t, "bf665f5f74797065782e736d697468792e70726f746f636f6c74657374732e727063763243626f7223496e76616c69644772656574696e67674d657373616765624869ff"))
	if resp.Status != 400 || resp.Header.Get("X-Amzn-Query-Error") != "" {
		t.Errorf("status %d, query error %q; want 400 and none", resp.Status, resp.Header.Get("X-Amzn-Query-Error"))
	}
}

// QueryCompatibleRpcV2CborCustomCodeError (S10): the header carries the
// awsQueryError code, and its httpResponseCode does not change the status
// of a request that did not send x-amzn-query-mode.
func TestEncodeErrorCorpusCustomCode(t *testing.T) {
	svc := &registry.Service{ID: "querycompatible", Shape: "aws.protocoltests.rpcv2cbor#QueryCompatibleRpcV2Protocol", Protocols: []ref.ProtocolID{ref.RPCv2CBOR}, QueryCompatible: true}
	e := &awserr.Error{
		ID: awserr.Identity{
			Shape: "aws.protocoltests.rpcv2cbor#CustomCodeError", JSONType: "CustomCodeError", QueryCode: "Customized",
			Status: 400, QueryModeStatus: 402, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
		},
		Message: "Hi", Origin: awserr.Modeled{},
	}
	resp := rpcv2cbor.Codec{}.EncodeError(errorRequest(false), svc, "req-1", e)
	checkBody(t, resp.Body, canonical(t, "b90002665f5f74797065782b6177732e70726f746f636f6c74657374732e727063763263626f7223437573746f6d436f64654572726f72676d657373616765624869"))
	if resp.Status != 400 || resp.Header.Get("X-Amzn-Query-Error") != "Customized;Sender" {
		t.Errorf("status %d, query error %q; want 400 and Customized;Sender", resp.Status, resp.Header.Get("X-Amzn-Query-Error"))
	}
}

// A shape outside the service's namespace is an unknown type to the Go
// client, which reads its message from message whatever the shape's own
// spelling (wire-cbor.md 3.9 and 5.2); an unbound operation's 501 is that
// case.
func TestEncodeErrorGenericShapes(t *testing.T) {
	_, cw := newRegistry(t)
	for _, e := range []*awserr.Error{
		{ID: awserr.SerializationException(), Message: "bad body", Origin: awserr.Generic{}},
		{ID: awserr.NotImplemented(), Message: "understudy does not implement cloudwatch.PutDashboard. Run 'understudy coverage --service cloudwatch' for what is supported; to ask for it, see https://github.com/sustinbebustin/understudy/issues?q=cloudwatch.PutDashboard", Origin: awserr.Unimplemented{}},
	} {
		resp := rpcv2cbor.Codec{}.EncodeError(errorRequest(false), cw, "req-1", e)
		want := cbor.Map{{Key: "__type", Value: cbor.Text(e.ID.Shape)}, {Key: "message", Value: cbor.Text(e.Message)}}
		got, err := cbor.Decode(resp.Body)
		if err != nil || !equalMap(got, want) {
			t.Errorf("%s body = %#v (%v), want %#v", e.ID.Shape, got, err, want)
		}
		if resp.Status != e.ID.Status {
			t.Errorf("%s status = %d, want %d", e.ID.Shape, resp.Status, e.ID.Status)
		}
	}
}

func equalMap(v cbor.Value, want cbor.Map) bool {
	m, ok := v.(cbor.Map)
	if !ok || len(m) != len(want) {
		return false
	}
	for i := range m {
		if m[i] != want[i] {
			return false
		}
	}
	return true
}

// An error body is never empty and always starts with __type, whatever the
// error: the Go client cannot read an empty body (G23) or one without
// __type (G24).
func TestEncodeErrorIsNeverEmpty(t *testing.T) {
	_, cw := newRegistry(t)
	identities := []awserr.Identity{
		awserr.SerializationException(), awserr.UnknownOperationException(), awserr.NotImplemented(),
		awserr.InternalFailure(), awserr.ThrottlingException(),
		cwIdentity("ResourceNotFound", "ResourceNotFound", 404, awserr.Sender, awserr.MessageLower),
		cwIdentity("ConflictException", "ConflictException", 409, awserr.Sender, awserr.MessageUpper),
		cwIdentity("Silent", "Silent", 400, awserr.Sender, awserr.NoMessage),
	}
	rapid.Check(t, func(t *rapid.T) {
		e := &awserr.Error{
			ID:      rapid.SampledFrom(identities).Draw(t, "identity"),
			Message: rapid.String().Draw(t, "message"),
			Origin:  awserr.Generic{},
		}
		for _, name := range rapid.SliceOfNDistinct(rapid.StringMatching(`[A-Z][a-z]{1,8}`), 0, 3, rapid.ID).Draw(t, "fields") {
			e.Fields = append(e.Fields, awserr.Field{Name: name, Value: rapid.String().Draw(t, "value")})
		}
		var svc *registry.Service
		if rapid.Bool().Draw(t, "service known") {
			svc = cw
		}
		resp := rpcv2cbor.Codec{}.EncodeError(errorRequest(rapid.Bool().Draw(t, "query mode")), svc, "req-1", e)
		v, err := cbor.Decode(resp.Body)
		if err != nil {
			t.Fatalf("error body %x does not decode: %v", resp.Body, err)
		}
		m, ok := v.(cbor.Map)
		if !ok || len(m) == 0 || m[0] != (cbor.Entry{Key: "__type", Value: cbor.Text(e.ID.Shape)}) {
			t.Fatalf("error body %s lacks a leading __type", hex.EncodeToString(resp.Body))
		}
		if resp.Header.Get("Smithy-Protocol") != "rpc-v2-cbor" || resp.Status != e.ID.Status ||
			resp.Header.Get("Content-Length") != strconv.Itoa(len(resp.Body)) {
			t.Fatalf("headers %v, status %d", resp.Header, resp.Status)
		}
	})
}

func bigIntOf(t *testing.T, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad big integer %q", s)
	}
	return v
}
