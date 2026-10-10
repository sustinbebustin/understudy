package restjson_test

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/restjson"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

const requestID = "7c7cb5ce-1a2b-4c3d-9e8f-0a1b2c3d4e5f"

func newRequest(method, target, body string, header map[string]string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), method, "http://localhost:4566"+target, strings.NewReader(body))
	for k, v := range header {
		r.Header.Set(k, v)
	}
	return r
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantHeader(t *testing.T, got http.Header, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("headers = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("header %s = %q, want %q", k, got.Get(k), v)
		}
	}
}

// Lambda answers a missing function with 404, x-amzn-errortype naming the
// bare shape and a body keyed exactly as the model spells the members:
// Type and Message (wire-rest.md 4.2, lambda-api.md 2.1).
func TestResourceNotFoundGolden(t *testing.T) {
	e := &awserr.Error{
		ID: awserr.Identity{
			Shape: "com.amazonaws.lambda#ResourceNotFoundException", JSONType: "ResourceNotFoundException",
			QueryCode: "ResourceNotFoundException", Status: 404, Fault: awserr.Sender, MessageKey: awserr.MessageUpper,
		},
		Message: "Function not found: arn:aws:lambda:us-east-1:000000000000:function:missing",
		Fields:  []awserr.Field{{Name: "Type", Value: "User"}},
		Origin:  awserr.Modeled{},
	}
	resp := restjson.Codec{}.EncodeError(newRequest(http.MethodGet, "/2015-03-31/functions/missing", "", nil), lambda(), requestID, e)
	const body = `{"Type":"User","Message":"Function not found: arn:aws:lambda:us-east-1:000000000000:function:missing"}`
	if resp.Status != http.StatusNotFound {
		t.Errorf("status = %d", resp.Status)
	}
	wantHeader(t, resp.Header, map[string]string{
		"Content-Type": "application/json", "X-Amzn-Errortype": "ResourceNotFoundException",
		"X-Amzn-Requestid": requestID, "Content-Length": "102",
	})
	if got := readAll(t, resp.Body); got != body {
		t.Errorf("body = %s\nwant   %s", got, body)
	}
}

// The lowercase message spelling and the Retry-After header of
// TooManyRequestsException travel as modeled; an error with neither
// fields nor message is {}.
func TestErrorMessageSpellingAndHeaders(t *testing.T) {
	e := &awserr.Error{
		ID:      awserr.Identity{Shape: "com.amazonaws.lambda#TooManyRequestsException", Status: 429, MessageKey: awserr.MessageLower},
		Message: "Rate Exceeded.",
		Fields:  []awserr.Field{{Name: "Type", Value: "User"}, {Name: "Reason", Value: "ReservedFunctionConcurrentInvocationLimitExceeded"}},
		Headers: []awserr.Header{{Name: "Retry-After", Value: "1"}},
	}
	resp := restjson.Codec{}.EncodeError(newRequest(http.MethodPost, "/", "", nil), nil, requestID, e)
	if got := readAll(t, resp.Body); got != `{"Type":"User","Reason":"ReservedFunctionConcurrentInvocationLimitExceeded","message":"Rate Exceeded."}` {
		t.Errorf("body = %s", got)
	}
	if resp.Status != 429 || resp.Header.Get("Retry-After") != "1" {
		t.Errorf("status %d, Retry-After %q", resp.Status, resp.Header.Get("Retry-After"))
	}
	bare := &awserr.Error{ID: awserr.Identity{Shape: "aws.protocoltests.restjson#ComplexError", Status: 403, MessageKey: awserr.MessageUpper}}
	if got := readAll(t, restjson.Codec{}.EncodeError(newRequest(http.MethodPost, "/", "", nil), nil, requestID, bare).Body); got != "{}" {
		t.Errorf("an error with no members = %s, want {}", got)
	}
}

func invokeOp() *registry.Operation {
	return op(lambda(), "Invoke", http.MethodPost, "/2015-03-31/functions/{FunctionName}/invocations", invokeIn, invokeOut)
}

// A synchronous Invoke whose function failed answers 200 with
// X-Amz-Function-Error and X-Amz-Executed-Version and the raw error
// payload (wire-rest.md 4.4, lambda-api.md 8.1, 8.3).
func TestInvokeResponseGolden(t *testing.T) {
	payload := `{"errorMessage":"Handler fails","errorType":"Exception"}`
	out := &invokeOutput{StatusCode: 200, FunctionError: new("Unhandled"), Payload: []byte(payload), ExecutedVersion: new("$LATEST")}
	resp, err := restjson.Codec{}.Encode(nil, invokeOp(), requestID, out, http.StatusOK)
	if err != nil {
		t.Fatal(err.AsError())
	}
	if resp.Status != http.StatusOK {
		t.Errorf("status = %d", resp.Status)
	}
	wantHeader(t, resp.Header, map[string]string{
		"Content-Type": "application/octet-stream", "Content-Length": "56",
		"X-Amz-Function-Error": "Unhandled", "X-Amz-Executed-Version": "$LATEST", "X-Amzn-Requestid": requestID,
	})
	if got := readAll(t, resp.Body); got != payload {
		t.Errorf("body = %s", got)
	}

	// An Event invoke: 202 from the StatusCode member, no body.
	resp, err = restjson.Codec{}.Encode(nil, invokeOp(), requestID, &invokeOutput{StatusCode: 202}, http.StatusOK)
	if err != nil {
		t.Fatal(err.AsError())
	}
	wantHeader(t, resp.Header, map[string]string{"X-Amzn-Requestid": requestID})
	if resp.Status != http.StatusAccepted || readAll(t, resp.Body) != "" {
		t.Errorf("Event invoke = %d %v", resp.Status, resp.Header)
	}
}

// Invoke takes its payload as raw bytes under any Content-Type, or none,
// and an empty body; the name label arrives with ":" percent-encoded
// (wire-rest.md 4.3, 4.4).
func TestInvokeRequestAcceptsAnyContentType(t *testing.T) {
	codec := restjson.Codec{MediaTypeErrors: mediaErrors}
	for _, contentType := range []string{"application/octet-stream", "", "application/json", "text/whatever"} {
		r := newRequest(http.MethodPost, "/2015-03-31/functions/my%3Afn/invocations?Qualifier=1", `{"k":1}`,
			map[string]string{"Content-Type": contentType, "X-Amz-Invocation-Type": "Event"})
		in := &invokeInput{}
		if err := codec.Decode(r, invokeOp(), edgeBody{r}, in); err != nil {
			t.Fatalf("Content-Type %q: %v", contentType, err.AsError())
		}
		want := &invokeInput{FunctionName: "my:fn", InvocationType: new("Event"), Qualifier: new("1"), Payload: []byte(`{"k":1}`)}
		if !reflect.DeepEqual(in, want) {
			t.Errorf("Content-Type %q: %+v", contentType, in)
		}
	}
	r := newRequest(http.MethodPost, "/2015-03-31/functions/fn/invocations", "", nil)
	in := &invokeInput{}
	if err := codec.Decode(r, invokeOp(), edgeBody{r}, in); err != nil || in.Payload != nil {
		t.Errorf("empty body: %+v, %v", in, err)
	}
	// Whitespace is a payload, passed on as its bytes: only an empty body
	// is an empty invoke.
	r = newRequest(http.MethodPost, "/2015-03-31/functions/fn/invocations", " \n", nil)
	in = &invokeInput{}
	if err := codec.Decode(r, invokeOp(), edgeBody{r}, in); err != nil || !reflect.DeepEqual(in.Payload, []byte(" \n")) {
		t.Errorf("whitespace body: %+v, %v", in, err)
	}
}

// A label or query value that cannot be read is Lambda's
// InvalidRequestContentException naming where it was, never a value bound
// as absent: Qualifier=%zz must not invoke $LATEST.
func TestHeadValueErrors(t *testing.T) {
	for target, prefix := range map[string]string{
		"/2015-03-31/functions/a%zz/invocations":             "The request's label is invalid: ",
		"/2015-03-31/functions/fn/invocations?Qualifier=%zz": "The request's query parameter is invalid: ",
	} {
		// url.Parse refuses %zz in a path, so the request target carries it
		// as a server reads it off the wire.
		r := newRequest(http.MethodPost, "/2015-03-31/functions/fn/invocations", "", nil)
		r.RequestURI = target
		r.URL.RawQuery = strings.TrimPrefix(target[strings.IndexByte(target+"?", '?'):], "?")
		err := restjson.Codec{}.Decode(r, invokeOp(), edgeBody{r}, &invokeInput{})
		if err == nil || err.ID.Shape != "com.amazonaws.lambda#InvalidRequestContentException" || !strings.HasPrefix(err.Message, prefix) {
			t.Errorf("%s: %+v", target, err)
		}
	}
	r := newRequest(http.MethodPost, "/2015-03-31/functions/fn/invocations?Qualifier=a;b", "", nil)
	in := &invokeInput{}
	if err := (restjson.Codec{}).Decode(r, invokeOp(), edgeBody{r}, in); err != nil || in.Qualifier == nil || *in.Qualifier != "a;b" {
		t.Errorf("Qualifier=a;b: %+v, %v", in, err)
	}
}

// The error-matrix.md 5.6 T6 row: a body that is not JSON is 400
// InvalidRequestContentException with Type User ahead of the lowercase
// message, the fields the codec's ClientFaultFields hook supplies.
func TestMalformedBodyGolden(t *testing.T) {
	codec := restjson.Codec{ClientFaultFields: func(ref.ServiceID) []awserr.Field { return []awserr.Field{{Name: "Type", Value: "User"}} }}
	r := newRequest(http.MethodPut, "/2015-03-31/functions/f/configuration", "{bad", map[string]string{"Content-Type": "application/json"})
	err := codec.Decode(r, configOp(lambda()), edgeBody{r}, &config{})
	if err == nil {
		t.Fatal("a body that is not JSON decoded")
	}
	resp := codec.EncodeError(r, lambda(), requestID, err)
	want := `{"Type":"User","message":"Could not parse request body into json: the body is not valid JSON at byte 1"}`
	if got := readAll(t, resp.Body); resp.Status != http.StatusBadRequest || got != want {
		t.Errorf("%d %s\nwant %s", resp.Status, got, want)
	}
	if resp.Header.Get("X-Amzn-Errortype") != "InvalidRequestContentException" {
		t.Errorf("headers = %v", resp.Header)
	}
}

// An operation without MaxRequestBytes is capped at Lambda's frontend
// limit, which admits UpdateFunctionCode with a 50 MB zip as base64
// (lambda-api.md 2.1).
func TestBodyLimit(t *testing.T) {
	if got := (restjson.Codec{}).BodyLimit(configOp(lambda())); got != 70167211 {
		t.Errorf("default BodyLimit = %d", got)
	}
	o := configOp(lambda())
	o.MaxRequestBytes = 1 << 20
	if got := (restjson.Codec{}).BodyLimit(o); got != 1<<20 {
		t.Errorf("BodyLimit with MaxRequestBytes = %d", got)
	}
}

func configOp(svc *registry.Service) *registry.Operation {
	return op(svc, "UpdateConfig", http.MethodPut, "/2015-03-31/functions/{Path}/configuration", configSchema, configSchema)
}

// The body carries the unbound members under their jsonName; unknown and
// null members are absent, null list entries skipped, and a body member
// named like a URI-bound one is ignored in favor of the URI.
func TestBodyMembers(t *testing.T) {
	body := `{"name":"f","Timeout":3,"CodeSize":9007199254740993,"Ratio":"-Infinity","Enabled":null,"Created":1576540098.5,` +
		`"LastModified":"2019-12-16T23:48:18Z","Tags":{"a":"b","n":null},"Layers":["x",null,"y"],` +
		`"Code":{"ZipFile":"YmxvYg=="},"Path":"from-body","Unknown":{"deep":[1]}}`
	r := newRequest(http.MethodPut, "/2015-03-31/functions/from-uri/configuration", body, map[string]string{"Content-Type": "application/json"})
	got := &config{}
	if err := (restjson.Codec{}).Decode(r, configOp(lambda()), edgeBody{r}, got); err != nil {
		t.Fatal(err.AsError())
	}
	want := &config{
		Name: new("f"), Timeout: new(int32(3)), Size: new(int64(9007199254740993)), Ratio: new(math.Inf(-1)),
		Created:  new(time.UnixMilli(1576540098500).UTC()),
		Modified: new(time.Date(2019, 12, 16, 23, 48, 18, 0, time.UTC)),
		Tags:     map[string]string{"a": "b"}, Layers: []string{"x", "y"},
		Code: &code{Zip: []byte("blob")}, Path: new("from-uri"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Decode = %+v\nwant     %+v", got, want)
	}
}

// A body that is not JSON or does not fit the input is the service's
// malformedRequestError, Lambda's InvalidRequestContentException (400);
// without one, SerializationException.
func TestMalformedBodies(t *testing.T) {
	for _, body := range []string{`{"Timeout":"3"}`, `{"Timeout":1.5}`, `{"Timeout":2147483648}`, `{bad`, `[]`, `{"Zip":"YmxvYg"}`, `{"Enabled":"true"}`, `{} {}`, `{"Ratio":"nan"}`} {
		r := newRequest(http.MethodPut, "/2015-03-31/functions/f/configuration", body, map[string]string{"Content-Type": "application/json"})
		err := restjson.Codec{}.Decode(r, configOp(lambda()), edgeBody{r}, &config{})
		if err == nil || err.ID.Shape != "com.amazonaws.lambda#InvalidRequestContentException" || !strings.HasPrefix(err.Message, "Could not parse request body into json: ") {
			t.Errorf("body %s: %+v", body, err)
		}
	}
	svc := lambda()
	svc.MalformedRequestError = awserr.Identity{}
	r := newRequest(http.MethodPut, "/2015-03-31/functions/f/configuration", `{bad`, nil)
	if err := (restjson.Codec{}).Decode(r, configOp(svc), edgeBody{r}, &config{}); err == nil || err.ID != awserr.SerializationException() {
		t.Errorf("no malformedRequestError: %+v", err)
	}
}

func mediaErrors(ref.ServiceID) (restjson.MediaTypeErrors, bool) {
	return restjson.MediaTypeErrors{
		UnsupportedMediaType: awserr.Identity{Shape: "aws.protocoltests.restjson#UnsupportedMediaTypeException", Status: 415},
		NotAcceptable:        awserr.Identity{Shape: "aws.protocoltests.restjson#NotAcceptableException", Status: 406},
	}, true
}

// The corpus's malformed-content-type and malformed-accept cases, and the
// empty-input-output cases that must still pass.
func TestMediaTypeChecks(t *testing.T) {
	svc := lambda()
	jpeg := &smithy.Schema{ID: "aws.protocoltests.shared#JpegBlob", Type: smithy.TypeBlob, MediaType: "image/jpeg"}
	jpegPayload := &smithy.Schema{ID: "test#JpegPayload", Type: smithy.TypeStructure, Members: []*smithy.Member{{Name: "payload", Target: jpeg, HTTP: smithy.HTTPPayload{}}}}
	stringPayload := &smithy.Schema{ID: "test#StringPayload", Type: smithy.TypeStructure, Members: []*smithy.Member{{Name: "payload", Target: str, HTTP: smithy.HTTPPayload{}}}}
	headerOnly := &smithy.Schema{ID: "test#HeaderOnly", Type: smithy.TypeStructure, Members: []*smithy.Member{{Name: "header", Target: str, HTTP: smithy.HTTPHeader{Name: "header"}}}}
	emptyStruct := &smithy.Schema{ID: "test#Empty", Type: smithy.TypeStructure}
	tests := []struct {
		name    string
		in, out *smithy.Schema
		body    string
		header  map[string]string
		status  int
	}{
		{"RestJsonWithoutBodyExpectsEmptyContentType", unit, unit, "{}", map[string]string{"Content-Type": "application/json"}, 415},
		{"RestJsonWithoutBodyEmptyInputExpectsEmptyContentType", headerOnly, unit, "{}", map[string]string{"Content-Type": "application/json"}, 415},
		{"RestJsonWithBodyExpectsApplicationJsonContentType", configSchema, unit, "{}", map[string]string{"Content-Type": "application/hal+json"}, 415},
		{"RestJsonWithBodyExpectsApplicationJsonContentTypeNoHeaders", configSchema, unit, "{}", nil, 415},
		{"RestJsonWithPayloadExpectsModeledContentType", jpegPayload, unit, "{}", map[string]string{"Content-Type": "application/json"}, 415},
		{"RestJsonWithBodyExpectsApplicationJsonAccept", unit, configSchema, "", map[string]string{"Accept": "application/hal+json"}, 406},
		{"RestJsonWithPayloadExpectsModeledAccept", unit, jpegPayload, "", map[string]string{"Accept": "application/json"}, 406},
		{"RestJsonWithPayloadExpectsImpliedAccept", unit, stringPayload, "", map[string]string{"Accept": "application/json"}, 406},
		{"RestJsonNoInputAllowsAccept", unit, unit, "", map[string]string{"Accept": "application/json"}, 0},
		{"an all-bound output is still a JSON object", unit, headerOnly, "", map[string]string{"Accept": "text/plain"}, 406},
		{"RestJsonEmptyInputAndEmptyOutputWithJson", emptyStruct, emptyStruct, "{}", map[string]string{"Content-Type": "application/json"}, 0},
		{"RestJsonEmptyInputAndEmptyOutput", emptyStruct, emptyStruct, "", nil, 0},
		{"charset and a list of ranges", configSchema, configSchema, "{}", map[string]string{"Content-Type": "application/json; charset=utf-8", "Accept": "text/html, application/*"}, 0},
	}
	for _, tt := range tests {
		o := op(svc, tt.name, http.MethodPost, "/op", tt.in, tt.out)
		r := newRequest(http.MethodPost, "/op", tt.body, tt.header)
		var in smithy.Deserializable = empty{}
		if tt.in == configSchema {
			in = &config{}
		}
		err := restjson.Codec{MediaTypeErrors: mediaErrors}.Decode(r, o, edgeBody{r}, in)
		switch {
		case tt.status == 0 && err != nil:
			t.Errorf("%s: %+v", tt.name, err)
		case tt.status != 0 && (err == nil || err.ID.Status != tt.status):
			t.Errorf("%s: %+v, want %d", tt.name, err, tt.status)
		}
	}
}

// An output with no member bound to the body is still a JSON object; a
// Unit output has no body (restJson1 empty-input-output), and neither has
// a structure output sent with 204 (Lambda's DeleteFunction).
func TestEmptyOutputs(t *testing.T) {
	svc := lambda()
	resp, err := restjson.Codec{}.Encode(nil, configOp(svc), requestID, &config{}, http.StatusOK)
	if err != nil || readAll(t, resp.Body) != "{}" || resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("empty structure output: %+v, %v", resp, err)
	}
	resp, err = restjson.Codec{}.Encode(nil, op(svc, "Delete", http.MethodDelete, "/x", unit, unit), requestID, empty{}, http.StatusNoContent)
	if err != nil || resp.Status != http.StatusNoContent || readAll(t, resp.Body) != "" || resp.Header.Get("Content-Type") != "" {
		t.Errorf("Unit output: %+v, %v", resp, err)
	}
	resp, err = restjson.Codec{}.Encode(nil, configOp(svc), requestID, &config{}, http.StatusNoContent)
	if err != nil || resp.Status != http.StatusNoContent || readAll(t, resp.Body) != "" ||
		resp.Header.Get("Content-Type") != "" || resp.Header.Get("Content-Length") != "" {
		t.Errorf("structure output at 204: %+v, %v", resp, err)
	}
}

func drawConfig(t *rapid.T) *config {
	opt := func(name string) bool { return rapid.Bool().Draw(t, name) }
	v := &config{}
	if opt("name?") {
		v.Name = new(rapid.String().Draw(t, "name"))
	}
	if opt("timeout?") {
		v.Timeout = new(rapid.Int32().Draw(t, "timeout"))
	}
	if opt("size?") {
		v.Size = new(rapid.Int64().Draw(t, "size"))
	}
	if opt("ratio?") {
		v.Ratio = new(rapid.OneOf(rapid.Float64(), rapid.SampledFrom([]float64{math.Inf(1), math.Inf(-1)})).Filter(func(f float64) bool { return !math.IsNaN(f) }).Draw(t, "ratio"))
	}
	if opt("enabled?") {
		v.Enabled = new(rapid.Bool().Draw(t, "enabled"))
	}
	ms := rapid.Custom(func(t *rapid.T) time.Time { return time.UnixMilli(rapid.Int64Range(-1e13, 1e13).Draw(t, "ms")).UTC() })
	if opt("created?") {
		v.Created = new(ms.Draw(t, "created"))
	}
	if opt("modified?") {
		v.Modified = new(ms.Filter(func(t time.Time) bool { return t.Year() >= 0 && t.Year() < 10000 }).Draw(t, "modified"))
	}
	if opt("zip?") {
		v.Zip = rapid.SliceOf(rapid.Byte()).Draw(t, "zip")
	}
	if opt("tags?") {
		v.Tags = rapid.MapOf(rapid.String(), rapid.String()).Draw(t, "tags")
	}
	if opt("layers?") {
		v.Layers = rapid.SliceOf(rapid.String()).Draw(t, "layers")
	}
	if opt("code?") {
		v.Code = &code{Zip: rapid.SliceOf(rapid.Byte()).Draw(t, "codezip")}
	}
	return v
}

// Encoding a value then decoding the body gives the value back, for every
// member kind and arbitrary strings, numbers, times and bytes.
func TestDocumentRoundTrip(t *testing.T) {
	svc := lambda()
	o := configOp(svc)
	rapid.Check(t, func(t *rapid.T) {
		want := drawConfig(t)
		resp, aerr := restjson.Codec{}.Encode(nil, o, requestID, want, http.StatusOK)
		if aerr != nil {
			t.Fatal(aerr.AsError())
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		path := rapid.StringN(1, 10, -1).Draw(t, "path")
		r := newRequest(http.MethodPut, "/2015-03-31/functions/"+url.PathEscape(path)+"/configuration", string(body), map[string]string{"Content-Type": "application/json"})
		got := &config{}
		if aerr := (restjson.Codec{}).Decode(r, o, edgeBody{r}, got); aerr != nil {
			t.Fatalf("Decode(%s): %v", body, aerr.AsError())
		}
		want.Path = &path
		if want.Code != nil && len(want.Code.Zip) == 0 {
			want.Code.Zip = []byte{}
		}
		if !reflect.DeepEqual(got, normalize(want)) {
			t.Fatalf("round trip of %s = %+v\nwant %+v", body, got, want)
		}
	})
}

// normalize maps empty collections and blobs to what a decode produces.
func normalize(v *config) *config {
	if v.Zip != nil && len(v.Zip) == 0 {
		v.Zip = []byte{}
	}
	return v
}

// Random bytes as a JSON body never panic the decoder, and never make an
// internal error: a client's body decodes or is the malformed-request
// error.
func TestRandomBodiesNeverPanic(t *testing.T) {
	o := configOp(lambda())
	rapid.Check(t, func(t *rapid.T) {
		body := rapid.OneOf(
			rapid.SliceOf(rapid.Byte()),
			rapid.SliceOf(rapid.SampledFrom([]byte(`{}[]",:0123456789.eE-+truefalsnul\ "Timeout" "Tags" "Layers" "Code"`))),
		).Draw(t, "body")
		r := newRequest(http.MethodPut, "/2015-03-31/functions/f/configuration", string(body), nil)
		if err := (restjson.Codec{}).Decode(r, o, edgeBody{r}, &config{}); err != nil {
			if _, internal := err.Origin.(awserr.Internal); internal {
				t.Fatalf("body %q produced an internal error: %v", body, err.AsError())
			}
		}
	})
}
