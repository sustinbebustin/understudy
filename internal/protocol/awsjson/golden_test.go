package awsjson_test

import (
	"context"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

const requestID = "66916324-67ca-54bb-a410-3f567a7a0571"

type golden struct {
	status int
	header http.Header
	body   string
}

func checkResponse(t *testing.T, got awsjson.Response, want golden) {
	t.Helper()
	if got.Status != want.status {
		t.Errorf("status = %d, want %d", got.Status, want.status)
	}
	wantHeader := want.header.Clone()
	wantHeader.Set("Content-Length", strconv.Itoa(len(want.body)))
	if !reflect.DeepEqual(got.Header, wantHeader) {
		t.Errorf("headers =\n%v\nwant\n%v", got.Header, wantHeader)
	}
	if string(got.Body) != want.body {
		t.Errorf("body =\n%s\nwant\n%s", got.Body, want.body)
	}
}

// headers is the expected header set apart from Content-Length, which
// checkResponse derives from the expected body.
func headers(contentType string, extra ...string) http.Header {
	h := http.Header{
		"Content-Type":     {contentType},
		"X-Amzn-Requestid": {requestID},
	}
	for i := 0; i+1 < len(extra); i += 2 {
		h.Add(extra[i], extra[i+1])
	}
	return h
}

func newRequest(header ...string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", http.NoBody)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	return r
}

// wire-json-query.md 6.2: errors without awsQueryError still carry the
// header on a query-compatible service, with the shape name as the code
// (error-matrix.md 3: boto3 derives Code from it for InvalidParameterValue).
func TestGoldenQueryCompatibleUnmodeledError(t *testing.T) {
	f := newFixture(t)
	e := &awserr.Error{ID: awserr.SerializationException(), Origin: awserr.Generic{}}
	checkResponse(t, awsjson.Codec{Version: awsjson.V10}.EncodeError(newRequest(), f.sqs, requestID, e), golden{
		status: 400,
		header: headers("application/x-amz-json-1.0", "X-Amzn-Query-Error", "SerializationException;Sender"),
		body:   `{"__type":"com.amazon.coral.service#SerializationException"}`,
	})
}

// wire-json-query.md 5.3: DynamoDB's documented error, a 1.0 service that
// is not query-compatible, so no x-amzn-query-error.
func TestGoldenDynamoDBResourceNotFound(t *testing.T) {
	ddb := &registry.Service{ID: "dynamodb", JSONTargetPrefix: "DynamoDB_20120810"}
	e := &awserr.Error{
		ID: awserr.Identity{
			Shape: "com.amazonaws.dynamodb#ResourceNotFoundException", JSONType: "com.amazonaws.dynamodb.v20120810#ResourceNotFoundException",
			QueryCode: "ResourceNotFoundException", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
		},
		Message: "Requested resource not found: Table: {{tablename}} not found",
		Origin:  awserr.Modeled{},
	}
	checkResponse(t, awsjson.Codec{Version: awsjson.V10}.EncodeError(newRequest(), ddb, requestID, e), golden{
		status: 400,
		header: headers("application/x-amz-json-1.0"),
		body:   `{"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException","message":"Requested resource not found: Table: {{tablename}} not found"}`,
	})
}

// awsJson1_1 sends the bare shape name (wire-json-query.md 5.1): a Logs
// modeled error, and a generic error whose identity carries the Coral
// namespace.
func TestGoldenLogsErrors(t *testing.T) {
	f := newFixture(t)
	codec := awsjson.Codec{Version: awsjson.V11}
	notFound := &awserr.Error{
		ID: awserr.Identity{
			Shape: "com.amazonaws.cloudwatchlogs#ResourceNotFoundException", JSONType: "ResourceNotFoundException",
			QueryCode: "ResourceNotFoundException", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
		},
		Message: "The specified log group does not exist.",
		Origin:  awserr.Modeled{},
	}
	checkResponse(t, codec.EncodeError(newRequest(), f.logs, requestID, notFound), golden{
		status: 400,
		header: headers("application/x-amz-json-1.1"),
		body:   `{"__type":"ResourceNotFoundException","message":"The specified log group does not exist."}`,
	})
	generic := &awserr.Error{ID: awserr.SerializationException(), Message: "STRING_VALUE cannot be converted to Integer", Origin: awserr.Generic{}}
	checkResponse(t, codec.EncodeError(newRequest(), f.logs, requestID, generic), golden{
		status: 400,
		header: headers("application/x-amz-json-1.1"),
		body:   `{"__type":"SerializationException","Message":"STRING_VALUE cannot be converted to Integer"}`,
	})
}

// wire-json-query.md 5.3 (gap G7): Secrets Manager's recorded error, bare
// name and capital Message.
func TestGoldenSecretsManagerCapitalMessage(t *testing.T) {
	sm := &registry.Service{ID: "secretsmanager", JSONTargetPrefix: "secretsmanager"}
	e := &awserr.Error{
		ID: awserr.Identity{
			Shape: "com.amazonaws.secretsmanager#InvalidRequestException", JSONType: "InvalidRequestException",
			QueryCode: "InvalidRequestException", Status: 400, Fault: awserr.Sender, MessageKey: awserr.MessageUpper,
		},
		Message: "You must provide a ClientRequestToken value. We recommend a UUID-type value.",
		Origin:  awserr.Modeled{},
	}
	checkResponse(t, awsjson.Codec{Version: awsjson.V11}.EncodeError(newRequest(), sm, requestID, e), golden{
		status: 400,
		header: headers("application/x-amz-json-1.1"),
		body:   `{"__type":"InvalidRequestException","Message":"You must provide a ClientRequestToken value. We recommend a UUID-type value."}`,
	})
}

// error-matrix.md 3: CloudWatch's ResourceNotFound has no message member
// and answers 404 to a client in query mode, 400 otherwise.
func TestGoldenCloudWatchQueryModeStatus(t *testing.T) {
	f := newFixture(t)
	e := &awserr.Error{
		ID: awserr.Identity{
			Shape: "com.amazonaws.cloudwatch#ResourceNotFound", JSONType: "com.amazonaws.cloudwatch.v2010_08_01#ResourceNotFound",
			QueryCode: "ResourceNotFound", Status: 400, QueryModeStatus: 404, Fault: awserr.Sender, MessageKey: awserr.NoMessage,
		},
		Message: "ignored: the shape has no message member",
		Origin:  awserr.Modeled{},
	}
	codec := awsjson.Codec{Version: awsjson.V10}
	body := `{"__type":"com.amazonaws.cloudwatch.v2010_08_01#ResourceNotFound"}`
	h := headers("application/x-amz-json-1.0", "X-Amzn-Query-Error", "ResourceNotFound;Sender")
	checkResponse(t, codec.EncodeError(newRequest(), f.cloudwatch, requestID, e), golden{status: 400, header: h, body: body})
	checkResponse(t, codec.EncodeError(newRequest("X-Amzn-Query-Mode", "true"), f.cloudwatch, requestID, e), golden{status: 404, header: h, body: body})
}

func TestGoldenErrorFieldsHeadersAndProtocolMessage(t *testing.T) {
	f := newFixture(t)
	e := &awserr.Error{
		ID:       awserr.ThrottlingException(),
		Message:  "Rate exceeded",
		Messages: []awserr.ProtocolMessage{{Protocol: ref.AWSQuery, Message: "query text"}},
		Fields:   []awserr.Field{{Name: "Reason", Value: "CallerRateLimitExceeded"}},
		Headers:  []awserr.Header{{Name: "Retry-After", Value: "1"}},
		Origin:   awserr.Injected{Rule: "r1"},
	}
	checkResponse(t, awsjson.Codec{Version: awsjson.V11}.EncodeError(newRequest(), f.logs, requestID, e), golden{
		status: 400,
		header: headers("application/x-amz-json-1.1", "Retry-After", "1"),
		body:   `{"__type":"ThrottlingException","message":"Rate exceeded","Reason":"CallerRateLimitExceeded"}`,
	})
}

// wire-json-query.md 4.1: the documented CreateQueue response.
func TestGoldenSQSCreateQueueOutput(t *testing.T) {
	f := newFixture(t)
	out := &dynStruct{schema: f.createQueue.Output, fields: map[string]any{"QueueUrl": "https://sqs.us-east-1.amazonaws.com/111122223333/MyQueue"}}
	resp, aerr := awsjson.Codec{Version: awsjson.V10}.Encode(f.createQueue, requestID, out, 200)
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	checkResponse(t, resp, golden{
		status: 200,
		header: headers("application/x-amz-json-1.0"),
		body:   `{"QueueUrl":"https://sqs.us-east-1.amazonaws.com/111122223333/MyQueue"}`,
	})
}

// wire-json-query.md 4.2 and sqs.md 2.1: a modeled output with nothing set
// is {}; an operation without output answers an empty body that still
// carries the Content-Type.
func TestGoldenEmptyOutputs(t *testing.T) {
	f := newFixture(t)
	codec := awsjson.Codec{Version: awsjson.V10}
	resp, aerr := codec.Encode(f.createQueue, requestID, &dynStruct{schema: f.createQueue.Output}, 200)
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	checkResponse(t, resp, golden{status: 200, header: headers("application/x-amz-json-1.0"), body: `{}`})
	resp, aerr = codec.Encode(f.deleteMessage, requestID, &dynStruct{schema: unit}, 200)
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	checkResponse(t, resp, golden{status: 200, header: headers("application/x-amz-json-1.0"), body: ``})
}

// wire-json-query.md 4.3 and 4.4: what every shape kind looks like on the
// wire. Integers are plain digits, timestamps epoch seconds with at most
// millisecond precision, blobs padded base64, non-finite floats strings,
// empty collections that the output sets are written.
func TestGoldenShapeSerialization(t *testing.T) {
	f := newFixture(t)
	s := f.sink.Output
	out := &dynStruct{schema: s, fields: map[string]any{
		"String":     "café \"q\"\n",
		"Enum":       "RED",
		"IntEnum":    int32(2),
		"Blob":       []byte("hi!!"),
		"Boolean":    false,
		"Byte":       int8(-128),
		"Short":      int16(32767),
		"Integer":    int32(5),
		"Long":       int64(9007199254740993),
		"Float":      float32(1.5),
		"Double":     math.Inf(-1),
		"BigInteger": new(big.Int).Lsh(big.NewInt(1), 70),
		"BigDecimal": document.Number("1.000000000000000000001"),
		"Timestamp":  time.UnixMilli(1700000000123).Add(456 * time.Microsecond),
		"DateTime":   time.Date(2014, 4, 29, 18, 30, 38, 120_000_000, time.UTC),
		"HttpDate":   time.Date(2014, 4, 29, 18, 30, 38, 0, time.UTC),
		"Document":   document.Map{{Key: "b", Value: document.List{document.Null{}, document.Number("1e3")}}, {Key: "a", Value: document.Bool(true)}},
		"List":       []any{},
		"SparseList": []any{"x", nil},
		"Map":        map[string]any{"a": int32(1)},
		"Nested":     dynStruct{schema: s, fields: map[string]any{"Double": math.NaN()}},
		"Choice":     dynUnion{member: "Number", value: int32(7)},
	}}
	resp, aerr := awsjson.Codec{Version: awsjson.V10}.Encode(f.sink, requestID, out, 200)
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	want := `{"String":"caf` + "é" + ` \"q\"\n","Enum":"RED","IntEnum":2,"Blob":"aGkhIQ==","Boolean":false,` +
		`"Byte":-128,"Short":32767,"Integer":5,"Long":9007199254740993,"Float":1.5,"Double":"-Infinity",` +
		`"BigInteger":1180591620717411303424,"BigDecimal":1.000000000000000000001,"Timestamp":1700000000.123,` +
		`"DateTime":"2014-04-29T18:30:38.12Z","HttpDate":"Tue, 29 Apr 2014 18:30:38 GMT",` +
		`"Document":{"b":[null,1e3],"a":true},"List":[],"SparseList":["x",null],"Map":{"a":1},` +
		`"Nested":{"Double":"NaN"},"Choice":{"Number":7}}`
	if string(resp.Body) != want {
		t.Errorf("body =\n%s\nwant\n%s", resp.Body, want)
	}
}

// Smithy timestamp rules: an epoch-seconds instant finer than a millisecond
// is truncated toward zero on the way out, before 1970 as after, so the
// encoder agrees with how Decode reads the same digits.
func TestGoldenEpochSecondsTruncateTowardZero(t *testing.T) {
	f := newFixture(t)
	codec := awsjson.Codec{Version: awsjson.V10}
	cases := []struct {
		in   time.Time
		want string // the wire form
		ms   int64  // the instant Decode reads back, in Unix milliseconds
	}{
		{time.Unix(0, 0), "0", 0},
		{time.Unix(0, -500_000), "0", 0},
		{time.Unix(0, -1_500_000), "-0.001", -1},
		{time.Unix(0, -500_000_000), "-0.5", -500},
		{time.Unix(-1, -999_999), "-1", -1000},
		{time.Unix(-2, 123_900_000), "-1.876", -1876},
		{time.Unix(1515531081, 120_999_999), "1515531081.12", 1515531081120},
	}
	for _, tc := range cases {
		out := &dynStruct{schema: f.sink.Output, fields: map[string]any{"Timestamp": tc.in}}
		resp, aerr := codec.Encode(f.sink, requestID, out, 200)
		if aerr != nil {
			t.Fatal(aerr.AsError())
		}
		if want := `{"Timestamp":` + tc.want + `}`; string(resp.Body) != want {
			t.Errorf("Encode(%s) body = %s, want %s", tc.in.UTC().Format(time.RFC3339Nano), resp.Body, want)
			continue
		}
		back := dynStruct{schema: f.sink.Input}
		if aerr := codec.Decode(request("AmazonSQS.Echo", "application/x-amz-json-1.0", resp.Body), f.sink, &back); aerr != nil {
			t.Fatalf("Decode(%s): %s %q", resp.Body, aerr.ID.Shape, aerr.Message)
		}
		if got, ok := back.fields["Timestamp"].(time.Time); !ok || !got.Equal(time.UnixMilli(tc.ms)) {
			t.Errorf("Decode(%s) Timestamp = %v, want %v", resp.Body, back.fields["Timestamp"], time.UnixMilli(tc.ms).UTC())
		}
	}
}

// The success status is the pipeline's choice, which may differ from the
// operation's modeled code (a fault rule's or a handler's): Encode writes
// the status it is given.
func TestEncodeUsesTheGivenStatus(t *testing.T) {
	f := newFixture(t)
	resp, aerr := awsjson.Codec{Version: awsjson.V10}.Encode(f.createQueue, requestID, &dynStruct{schema: f.createQueue.Output}, http.StatusAccepted)
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	checkResponse(t, resp, golden{status: http.StatusAccepted, header: headers("application/x-amz-json-1.0"), body: `{}`})
}
