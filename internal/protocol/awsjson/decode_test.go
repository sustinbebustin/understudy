package awsjson_test

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

func mustDecode(t *testing.T, body string, header ...string) dynStruct {
	t.Helper()
	f := newFixture(t)
	v, aerr := decode(f.sink, []byte(body), header...)
	if aerr != nil {
		t.Fatalf("Decode(%s) = %s: %q", body, aerr.ID.Shape, aerr.Message)
	}
	return v
}

// The Smithy specification requires servers to accept an empty body for
// awsJson (AwsJson10ServiceSupportsNoPayloadForNoInput) and to treat null
// members as absent (AwsJson11ServersDontDeserializeNullStructureValues);
// unknown members are skipped as every AWS front end skips them.
func TestDecodeAcceptsEmptyNullAndUnknown(t *testing.T) {
	for _, body := range []string{"", " \r\n\t", "{}", `{"String":null,"Unknown":{"a":[1,2]},"Nested":null}`} {
		if v := mustDecode(t, body); len(v.fields) != 0 {
			t.Errorf("Decode(%q) set %v, want nothing", body, v.fields)
		}
	}
}

func TestDecodeLeniency(t *testing.T) {
	v := mustDecode(t, `{
		"Integer": 1, "Integer": 2,
		"List": ["a", null, "b"],
		"SparseList": ["a", null],
		"Map": {"x": 1, "y": null},
		"Choice": {"__type": "test#Choice", "Text": null, "Number": 4},
		"Long": 1.9, "Short": -1.5, "Byte": 1e2,
		"Boolean": "yes",
		"Timestamp": 1.7000000001239E9,
		"Double": "-Infinity"
	}`)
	want := map[string]any{
		"Integer":    int32(2),
		"List":       []any{"a", "b"},
		"SparseList": []any{"a", nil},
		"Map":        map[string]any{"x": int32(1)},
		"Choice":     dynUnion{member: "Number", value: int32(4)},
		"Long":       int64(1),
		"Short":      int16(-2),
		"Byte":       int8(100),
		"Boolean":    true,
		"Timestamp":  time.UnixMilli(1700000000123),
		"Double":     mustFloat(t, "-Infinity"),
	}
	if !equalValues(v.fields, want) {
		t.Errorf("Decode =\n%#v\nwant\n%#v", v.fields, want)
	}
}

func mustFloat(t *testing.T, body string) float64 {
	t.Helper()
	f := newFixture(t)
	v, aerr := decode(f.sink, []byte(`{"Double":"`+body+`"}`))
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	d, ok := v.fields["Double"].(float64)
	if !ok {
		t.Fatalf("Double = %#v", v.fields["Double"])
	}
	return d
}

// SQS reports InvalidMessageContents for a body with invalid UTF-8, a
// validation outcome: the decoder must keep the bytes and leave the verdict
// to the validator, which reads utf8.ValidString.
func TestDecodeKeepsInvalidUTF8(t *testing.T) {
	v := mustDecode(t, "{\"String\":\"ok\xff\",\"List\":[\"\\ud800\"]}")
	s, ok := v.fields["String"].(string)
	if !ok || s != "ok\xff" || utf8.ValidString(s) {
		t.Errorf("String = %q, want the raw bytes ok\\xff, flagged invalid", s)
	}
	l, ok := v.fields["List"].([]any)
	if !ok || len(l) != 1 || l[0] != "\xed\xa0\x80" {
		t.Errorf("List = %#v, want the lone surrogate kept", v.fields["List"])
	}
}

func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// CloudWatch PutMetricData clients gzip bodies of 10 KiB and more
// (wire-json-query.md 3.2).
func TestDecodeGzip(t *testing.T) {
	f := newFixture(t)
	body := gzipped(t, `{"String":"compressed"}`)
	v, aerr := decode(f.sink, body, "Content-Encoding", "gzip")
	if aerr != nil || v.fields["String"] != "compressed" {
		t.Fatalf("Decode = %#v, %v", v.fields, aerr)
	}

	f.sink.MaxRequestBytes = 10
	_, aerr = decode(f.sink, body, "Content-Encoding", "gzip")
	if aerr == nil || aerr.ID.Status != 413 {
		t.Errorf("over the cap: %v, want 413 RequestEntityTooLarge", aerr)
	}
	f.sink.MaxRequestBytes = 0

	// An operation without its own cap still gets one: a body that
	// expands past DefaultMaxRequestBytes is refused, not decompressed in
	// full.
	bomb := gzipped(t, `{"String":"`+strings.Repeat("a", int(awsjson.DefaultMaxRequestBytes))+`"}`)
	if _, aerr := decode(f.sink, bomb, "Content-Encoding", "gzip"); aerr == nil || aerr.ID != awserr.RequestEntityTooLarge() {
		t.Errorf("over the default cap: %v, want RequestEntityTooLarge", aerr)
	}

	for name, header := range map[string][]string{
		"corrupt gzip":         {"Content-Encoding", "gzip"},
		"unsupported encoding": {"Content-Encoding", "br"},
	} {
		_, aerr := decode(f.sink, []byte(`{"String":"x"}`), header...)
		if aerr == nil || aerr.ID != awserr.SerializationException() {
			t.Errorf("%s: %v, want SerializationException", name, aerr)
		}
	}
}

// error-matrix.md 5.5 T2 and T6: Coral's SerializationException texts, as
// dynalite records them. Where a text names a modeled shape's class,
// understudy uses the Smithy namespace for Coral's versioned Java package
// (test.KitchenSink for com.amazonaws.dynamodb.v20120810.KitchenSink); the
// union, range, timestamp and top-level texts are understudy's own.
func TestDecodeRejectsWithCoralText(t *testing.T) {
	tests := map[string]string{
		`{"Integer":"5"}`:                      "STRING_VALUE cannot be converted to Integer",
		`{"String":5}`:                         "NUMBER_VALUE cannot be converted to String",
		`{"String":5.5}`:                       "DECIMAL_VALUE cannot be converted to String",
		`{"String":true}`:                      "TRUE_VALUE cannot be converted to String",
		`{"Long":false}`:                       "FALSE_VALUE cannot be converted to Long",
		`{"Integer":{}}`:                       "Start of structure or map found where not expected",
		`{"Double":[1]}`:                       "Unrecognized collection type class java.lang.Double",
		`{"List":"a"}`:                         "Unexpected field type",
		`{"List":{}}`:                          "Start of structure or map found where not expected",
		`{"Map":[]}`:                           "Unrecognized collection type java.util.Map<java.lang.String, java.lang.Integer>",
		`{"Nested":"x"}`:                       "Unexpected field type",
		`{"Nested":[]}`:                        "Unrecognized collection type class test.KitchenSink",
		`{"Choice":[]}`:                        "Unrecognized collection type class test.Choice",
		`{"Blob":5}`:                           "only base-64-encoded strings are convertible to bytes",
		`{"Blob":"aGk"}`:                       "Base64 encoded length is expected a multiple of 4 bytes but found: 3",
		`{"Blob":"a*k="}`:                      "Invalid last non-pad Base64 character dectected",
		`{"Boolean":"maybe"}`:                  "Unexpected token received from parser",
		`{"Boolean":1}`:                        "NUMBER_VALUE cannot be converted to Boolean",
		`{"Integer":2147483648}`:               "Numeric value (2147483648) out of range of Integer",
		`{"Byte":-129}`:                        "Numeric value (-129) out of range of Byte",
		`{"Choice":{}}`:                        "Union has no member set; exactly one member must be set",
		`{"Choice":{"Text":"a","Number":1}}`:   "Union has more than one member set: Number follows Text",
		`{"Choice":{"Text":"a","Text":"b"}}`:   "Union member Text appears more than once",
		`{"Entries":[[]]}`:                     "Unrecognized collection type class test.Entry",
		`{"Choices":{"k":[]}}`:                 "Unrecognized collection type class test.Choice",
		`{"Timestamp":"2014-04-29T18:30:38Z"}`: "STRING_VALUE cannot be converted to Date",
		`{"DateTime":"yesterday"}`:             "Value yesterday cannot be parsed as a date-time timestamp",
		`[]`:                                   "The request body must be a JSON object, found a JSON array for KitchenSink",
	}
	f := newFixture(t)
	for body, want := range tests {
		_, aerr := decode(f.sink, []byte(body))
		if aerr == nil || aerr.ID != awserr.SerializationException() || aerr.Message != want {
			t.Errorf("Decode(%s) = %v, want SerializationException %q", body, aerr, want)
		}
	}
	for _, body := range []string{`{`, `{"String":"a"} x`, `{"String":"a",}`, `nul`} {
		_, aerr := decode(f.sink, []byte(body))
		if aerr == nil || aerr.ID != awserr.SerializationException() || aerr.Message != "" {
			t.Errorf("Decode(%s) = %v, want SerializationException with no message", body, aerr)
		}
	}
}

// A service whose overlay sets malformedRequestError answers an undecodable
// body with that identity instead of SerializationException.
func TestDecodeRejectsWithTheServiceMalformedRequestError(t *testing.T) {
	f := newFixture(t)
	own := awserr.Identity{Shape: "test#InvalidRequestContentException", JSONType: "InvalidRequestContentException", Status: 400, Fault: awserr.Sender}
	f.sink.Service.MalformedRequestError = own
	for body, want := range map[string]string{`{"String":5}`: "NUMBER_VALUE cannot be converted to String", `{`: ""} {
		_, aerr := decode(f.sink, []byte(body))
		if aerr == nil || aerr.ID != own || aerr.Message != want {
			t.Errorf("Decode(%s) = %v, want %s %q", body, aerr, own.Shape, want)
		}
	}
}

func TestDecodeBodyReadFailures(t *testing.T) {
	f := newFixture(t)
	read := func(err error) *awserr.Error {
		r := request("AmazonSQS.Echo", "application/x-amz-json-1.0", nil)
		r.Body = failingBody{err: err}
		v := dynStruct{schema: f.sink.Input}
		return awsjson.Codec{Version: awsjson.V10}.Decode(r, f.sink, &v)
	}
	if aerr := read(io.ErrUnexpectedEOF); aerr == nil || aerr.ID != awserr.RequestAbortedException() {
		t.Errorf("truncated body: %v, want RequestAborted", aerr)
	}
	if aerr := read(&awserr.BoundaryError{}); aerr == nil || aerr.ID != awserr.RequestAbortedException() {
		t.Errorf("BoundaryError without Err: %v, want RequestAborted", aerr)
	}
	capped := &awserr.Error{ID: awserr.RequestEntityTooLarge(), Message: "too big", Origin: awserr.Generic{}}
	if aerr := read(capped.AsError()); aerr != capped {
		t.Errorf("edge cap: %v, want the edge's own error passed through", aerr)
	}
}

// A Deserialize method that fails for its own reasons is a generator bug,
// not a client error.
func TestDecodeNonCodecErrorIsInternal(t *testing.T) {
	f := newFixture(t)
	r := request("AmazonSQS.Echo", "application/x-amz-json-1.0", []byte(`{}`))
	aerr := awsjson.Codec{Version: awsjson.V10}.Decode(r, f.sink, brokenInput{})
	if aerr == nil || aerr.ID != awserr.InternalFailure() {
		t.Fatalf("Decode = %v, want InternalFailure", aerr)
	}
	if _, ok := aerr.Origin.(awserr.Internal); !ok {
		t.Errorf("Origin = %#v, want Internal carrying the cause", aerr.Origin)
	}
}

type brokenInput struct{}

func (brokenInput) Deserialize(smithy.Deserializer) error { return errors.New("generated code bug") }

// Generated code that calls the deserializer out of order is a generator
// bug too: the client gets InternalFailure, not the misuse text.
func TestDecodeDeserializerMisuseIsInternal(t *testing.T) {
	f := newFixture(t)
	for name, in := range map[string]misusingInput{
		"member outside a structure": func(d smithy.Deserializer) error {
			_, _, err := d.ReadStructMember()
			return err
		},
		"close outside a union": func(d smithy.Deserializer) error { return d.CloseUnion() },
		"body of an awsJson input": func(d smithy.Deserializer) error {
			_, err := d.ReadBody(nil)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := request("AmazonSQS.Echo", "application/x-amz-json-1.0", []byte(`{}`))
			aerr := awsjson.Codec{Version: awsjson.V10}.Decode(r, f.sink, in)
			if aerr == nil || aerr.ID != awserr.InternalFailure() {
				t.Fatalf("Decode = %v, want InternalFailure", aerr)
			}
			if strings.Contains(aerr.Message, "misuse") {
				t.Errorf("Message = %q leaks the generator bug to the client", aerr.Message)
			}
		})
	}
}

type misusingInput func(smithy.Deserializer) error

func (f misusingInput) Deserialize(d smithy.Deserializer) error { return f(d) }
