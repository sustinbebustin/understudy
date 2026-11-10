package httpevent_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/httpevent"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

// The REST 1.0 event this file pins is the recorded LocalStack proxy event
// for a GET on ANY /{proxy+} with authorization NONE (aws-recorded,
// localstack@8b9a79f
// tests/aws/services/apigateway/test_apigateway_lambda.snapshot.json:L5-L121,
// apigateway-rest.md 5.1), with the path and query rows of 4.2 and 4.5.

func restRequest() httpevent.Request {
	return httpevent.Request{
		Method: http.MethodGet, RawPath: "/stage/proxy-value",
		RawQuery: "category=toys&category=books&email=test%2Balias@gmail.com&plus=test+alias@gmail.com",
		Protocol: "HTTP/1.1", Host: "a1b2c3d4e5.execute-api.us-east-1.localhost:4566",
		Header: http.Header{
			"Accept":          {"*/*"},
			"User-Agent":      {"curl/8"},
			"X-Multi":         {"a", "b"},
			"X-Forwarded-For": {"203.0.113.9"},
		},
		SourceIP: "192.0.2.1", Scheme: "http", Port: "4566",
	}
}

func restContext() httpevent.Context {
	return httpevent.Context{
		AccountID: "123456789012", APIID: "a1b2c3d4e5", DomainName: "a1b2c3d4e5.execute-api.us-east-1.localhost", DomainPrefix: "a1b2c3d4e5",
		Stage: "stage", PathParameters: map[string]string{"proxy": "proxy-value"}, RequestID: "c6af9ac6-7b61-11e6-9a41-93e8deadbeef",
		At: time.UnixMilli(1583349317135), TraceID: "Root=1-5e5f1f45-0123456789abcdef01234567", Authorizer: httpevent.NoAuthorizer{},
	}
}

func restFront() httpevent.REST {
	return httpevent.REST{ResourceID: "abc123", ResourcePath: "/{proxy+}", DeploymentID: "dep123", ExtendedRequestID: "JKJaXmPLvHcESHA="}
}

func restEvent(tb testing.TB, r httpevent.Request, c httpevent.Context, f httpevent.REST) map[string]any {
	tb.Helper()
	raw, err := httpevent.EventV1(r, c, f)
	if err != nil {
		tb.Fatalf("EventV1(REST) = %v", err)
	}
	var ev map[string]any
	if err := json.Unmarshal(raw, &ev); err != nil {
		tb.Fatalf("event %s: %v", raw, err)
	}
	return ev
}

func member(tb testing.TB, m map[string]any, key string) map[string]any {
	tb.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		tb.Fatalf("%s = %#v, want an object", key, m[key])
	}
	return v
}

// stringsOf reads a JSON array of strings; anything else reads as nil, and
// a non-string element as "".
func stringsOf(v any) []string {
	xs, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		s, ok := x.(string)
		if !ok {
			s = ""
		}
		out = append(out, s)
	}
	return out
}

// The recorded top-level members (apigateway-rest.md 5.1): no version key;
// resource the template; path without the stage, not decoded; the real
// verb; every value of a repeated header in multiValueHeaders (which value
// headers keeps is UNVERIFIED, APIGW1-16, and not asserted), the
// front's Host and X-Forwarded-* replacing the client's and
// X-Amzn-Trace-Id from the trace; query values form-decoded, the last one
// single (4.5); pathParameters; null stageVariables and body for a GET;
// isBase64Encoded false.
func TestEventV1RESTMembers(t *testing.T) {
	ev := restEvent(t, restRequest(), restContext(), restFront())
	if _, has := ev["version"]; has {
		t.Errorf("version = %v, want no key", ev["version"])
	}
	if ev["resource"] != "/{proxy+}" || ev["path"] != "/proxy-value" || ev["httpMethod"] != http.MethodGet || ev["isBase64Encoded"] != false {
		t.Errorf("resource %v, path %v, httpMethod %v, isBase64Encoded %v", ev["resource"], ev["path"], ev["httpMethod"], ev["isBase64Encoded"])
	}
	headers, multi := member(t, ev, "headers"), member(t, ev, "multiValueHeaders")
	if !slices.Equal(stringsOf(multi["X-Multi"]), []string{"a", "b"}) {
		t.Errorf("multiValueHeaders.X-Multi = %v, want [a b]", multi["X-Multi"])
	}
	for k, v := range map[string]string{
		"Host": "a1b2c3d4e5.execute-api.us-east-1.localhost:4566", "X-Forwarded-For": "192.0.2.1", "X-Forwarded-Port": "4566",
		"X-Forwarded-Proto": "http", "X-Amzn-Trace-Id": "Root=1-5e5f1f45-0123456789abcdef01234567", "Accept": "*/*",
	} {
		if headers[k] != v {
			t.Errorf("headers.%s = %#v, want %q", k, headers[k], v)
		}
	}
	q, mq := member(t, ev, "queryStringParameters"), member(t, ev, "multiValueQueryStringParameters")
	if q["category"] != "books" || q["email"] != "test+alias@gmail.com" || q["plus"] != "test alias@gmail.com" {
		t.Errorf("queryStringParameters = %v", q)
	}
	if !slices.Equal(stringsOf(mq["category"]), []string{"toys", "books"}) {
		t.Errorf("multiValueQueryStringParameters.category = %v, want [toys books]", mq["category"])
	}
	if p := member(t, ev, "pathParameters"); p["proxy"] != "proxy-value" {
		t.Errorf("pathParameters = %v", p)
	}
	for _, k := range []string{"stageVariables", "body"} {
		if v, has := ev[k]; !has || v != nil {
			t.Errorf("%s = %#v (present %v), want null", k, v, has)
		}
	}
}

// requestContext (apigateway-rest.md 5.1): the owner account, api id,
// deployment id, domain name and prefix, extendedRequestId, the verb, the
// 1.0 identity with only sourceIp and userAgent set, the path with the
// stage, protocol, requestId, requestTime in CLF dd/MMM/yyyy:HH:mm:ss
// +-hhmm and requestTimeEpoch in milliseconds (aws-doc
// api-gateway-mapping-template-reference), the resource id and path, the
// stage, and no authorizer for NONE.
func TestEventV1RESTRequestContext(t *testing.T) {
	ev := restEvent(t, restRequest(), restContext(), restFront())
	rc := member(t, ev, "requestContext")
	for k, v := range map[string]any{
		"accountId": "123456789012", "apiId": "a1b2c3d4e5", "deploymentId": "dep123", "domainName": "a1b2c3d4e5.execute-api.us-east-1.localhost",
		"domainPrefix": "a1b2c3d4e5", "extendedRequestId": "JKJaXmPLvHcESHA=", "httpMethod": http.MethodGet, "path": "/stage/proxy-value",
		"protocol": "HTTP/1.1", "requestId": "c6af9ac6-7b61-11e6-9a41-93e8deadbeef", "resourceId": "abc123", "resourcePath": "/{proxy+}",
		"stage": "stage", "requestTimeEpoch": float64(1583349317135),
	} {
		if rc[k] != v {
			t.Errorf("requestContext.%s = %#v, want %#v", k, rc[k], v)
		}
	}
	if got, want := rc["requestTime"], time.UnixMilli(1583349317135).UTC().Format("02/Jan/2006:15:04:05 -0700"); got != want {
		t.Errorf("requestTime = %v, want %s", got, want)
	}
	if _, has := rc["authorizer"]; has {
		t.Errorf("authorizer = %#v, want no key for NONE", rc["authorizer"])
	}
	id := member(t, rc, "identity")
	for _, k := range []string{"accessKey", "accountId", "caller", "cognitoAuthenticationProvider", "cognitoAuthenticationType", "cognitoIdentityId", "cognitoIdentityPoolId", "principalOrgId", "user", "userArn"} {
		if v, has := id[k]; !has || v != nil {
			t.Errorf("identity.%s = %#v (present %v), want null", k, v, has)
		}
	}
	if id["sourceIp"] != "192.0.2.1" || id["userAgent"] != "curl/8" {
		t.Errorf("identity sourceIp %v, userAgent %v", id["sourceIp"], id["userAgent"])
	}
}

// path and requestContext.path, the recorded normalization rows
// (aws-recorded, apigateway-rest.md 4.2 table).
func TestEventV1RESTPaths(t *testing.T) {
	for raw, want := range map[string][2]string{
		"/stage/proxy-value":               {"/proxy-value", "/stage/proxy-value"},
		"/stage/proxy-value/":              {"/proxy-value/", "/stage/proxy-value/"},
		"/stage/proxy-value//double-slash": {"/proxy-value//double-slash", "/stage/proxy-value//double-slash"},
		"//stage/proxy-value":              {"/proxy-value", "/stage/proxy-value"},
		"/stage//proxy-value":              {"/proxy-value", "/stage//proxy-value"},
		"/stage/proxy-value/api/user/test%2Balias@gmail.com/plus/test+alias@gmail.com": {
			"/proxy-value/api/user/test%2Balias@gmail.com/plus/test+alias@gmail.com",
			"/stage/proxy-value/api/user/test%2Balias@gmail.com/plus/test+alias@gmail.com",
		},
	} {
		r := restRequest()
		r.RawPath = raw
		ev := restEvent(t, r, restContext(), restFront())
		if ev["path"] != want[0] || member(t, ev, "requestContext")["path"] != want[1] {
			t.Errorf("RawPath %s: path %v, requestContext.path %v; want %s, %s", raw, ev["path"], member(t, ev, "requestContext")["path"], want[0], want[1])
		}
	}
}

// Empty query and path parameters are null; stage variables are reported
// when set (aws-recorded, apigateway-rest.md 4.5, 4.2 table, 5.1).
func TestEventV1RESTNullAndPresentMaps(t *testing.T) {
	r := restRequest()
	r.RawQuery = ""
	c := restContext()
	c.PathParameters = nil
	c.StageVariables = map[string]string{"fn": "a"}
	ev := restEvent(t, r, c, restFront())
	for _, k := range []string{"queryStringParameters", "multiValueQueryStringParameters", "pathParameters"} {
		if v, has := ev[k]; !has || v != nil {
			t.Errorf("%s = %#v (present %v), want null", k, v, has)
		}
	}
	if sv := member(t, ev, "stageVariables"); sv["fn"] != "a" {
		t.Errorf("stageVariables = %v, want fn=a", sv)
	}
}

// A POST body is the raw string (aws-recorded, apigateway-rest.md 5.1); a
// bodyless POST follows switch apigateway.proxy.unrecorded (APIGW1-16).
func TestEventV1RESTBody(t *testing.T) {
	r := restRequest()
	r.Method, r.Body = http.MethodPost, []byte(`{"a":1}`)
	if ev := restEvent(t, r, restContext(), restFront()); ev["body"] != `{"a":1}` || ev["isBase64Encoded"] != false {
		t.Errorf("POST body = %#v, isBase64Encoded %v; want the raw string, false", ev["body"], ev["isBase64Encoded"])
	}
	sw := apigateway.ProxyUnrecordedSwitch().Value
	r.Body = nil
	ev := restEvent(t, r, restContext(), restFront())
	if sw.BodylessNull && ev["body"] != nil || !sw.BodylessNull && ev["body"] != "" {
		t.Errorf("bodyless POST body = %#v, want per BodylessNull %v", ev["body"], sw.BodylessNull)
	}
	if ev["isBase64Encoded"] != sw.BodylessBase64 {
		t.Errorf("bodyless POST isBase64Encoded = %v, want %v", ev["isBase64Encoded"], sw.BodylessBase64)
	}
}

// A nil front or authorizer is a caller bug answered with an error.
func TestEventV1RESTRefusesNilAuthorizer(t *testing.T) {
	c := restContext()
	c.Authorizer = nil
	if _, err := httpevent.EventV1(restRequest(), c, restFront()); err == nil {
		t.Error("EventV1 with a nil Authorizer succeeded")
	}
}

// Properties of the REST event: it parses as JSON for any request;
// queryStringParameters[k] is the last of
// multiValueQueryStringParameters[k] (aws-recorded, apigateway-rest.md
// 4.5); path never starts with // (4.2). The single header value is
// inferred (APIGW1-16) and left out.
func TestEventV1RESTProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := restRequest()
		segs := rapid.SliceOfN(rapid.StringMatching(`[a-z%2B+]{0,4}`), 0, 4).Draw(t, "segments")
		r.RawPath = strings.Repeat("/", rapid.IntRange(1, 3).Draw(t, "leading")) + "stage/" + strings.Join(segs, "/")
		r.RawQuery = rapid.StringMatching(`([a-c]=[a-z+%2B]{0,3}&?){0,4}`).Draw(t, "query")
		r.Header = http.Header{}
		for range rapid.IntRange(0, 4).Draw(t, "headers") {
			r.Header.Add(rapid.SampledFrom([]string{"X-A", "X-B", "Accept"}).Draw(t, "name"), rapid.StringMatching(`[ -~]{0,6}`).Draw(t, "value"))
		}
		r.Body = []byte(rapid.String().Draw(t, "body"))
		raw, err := httpevent.EventV1(r, restContext(), restFront())
		if err != nil {
			t.Fatalf("EventV1 = %v", err)
		}
		var ev map[string]any
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatalf("event %s does not parse: %v", raw, err)
		}
		if p, ok := ev["path"].(string); !ok || strings.HasPrefix(p, "//") || !strings.HasPrefix(p, "/") {
			t.Fatalf("path %#v", ev["path"])
		}
		single, sok := ev["queryStringParameters"].(map[string]any)
		multi, mok := ev["multiValueQueryStringParameters"].(map[string]any)
		if sok != mok {
			t.Fatalf("queryStringParameters %#v, multiValueQueryStringParameters %#v: want both null or both objects", ev["queryStringParameters"], ev["multiValueQueryStringParameters"])
		}
		for k, v := range single {
			vs := stringsOf(multi[k])
			if len(vs) == 0 || vs[len(vs)-1] != v {
				t.Fatalf("queryStringParameters[%s] = %v, multiValueQueryStringParameters[%s] = %v", k, v, k, vs)
			}
		}
	})
}

func inferREST(tb testing.TB, p string) (httpevent.Response, error) {
	tb.Helper()
	return httpevent.InferV1For([]byte(p), httpevent.REST{})
}

// REST response mapping, the recorded outcomes (aws-recorded,
// test_apigateway_lambda.snapshot.json:L1669-L1797,
// test_apigateway_lambda.py:L1113-L1186; apigateway-rest.md 5.2) and the
// default Content-Type application/json (4.8, aws-recorded and aws-doc).
func TestInferV1ForREST(t *testing.T) {
	for _, c := range []struct {
		name, p string
		status  int
		body    string
		header  map[string][]string
	}{
		{"status only", `{"statusCode": 200}`, 200, "", map[string][]string{"Content-Type": {"application/json"}}},
		{"non-string header", `{"statusCode": 200, "headers": {"test-header": "value", "header-bool": true}}`, 200, "", map[string][]string{"Test-Header": {"value"}, "Header-Bool": {"true"}}},
		{"null headers", `{"statusCode": 200, "headers": null}`, 200, "", map[string][]string{"Content-Type": {"application/json"}}},
		{"base64 not decoded", `{"statusCode": 200, "body": "dGVzdC1kYXRh", "isBase64Encoded": true}`, 200, "dGVzdC1kYXRh", nil},
		{"multi", `{"statusCode": 200, "multiValueHeaders": {"test-multi": ["value1", "value2"]}}`, 200, "", map[string][]string{"Test-Multi": {"value1", "value2"}}},
		{"merge", `{"statusCode": 200, "multiValueHeaders": {"test-multi": ["value-multi"]}, "headers": {"test-multi": "value-solo"}}`, 200, "", map[string][]string{"Test-Multi": {"value-multi", "value-solo"}}},
		{"string status", `{"statusCode": "201"}`, 201, "", nil},
		{"no status", `{"headers": {"test-header": "value"}}`, 200, "", map[string][]string{"Test-Header": {"value"}}},
		{"explicit content type", `{"statusCode": 200, "headers": {"content-type": "text/plain"}, "body": "hi"}`, 200, "hi", map[string][]string{"Content-Type": {"text/plain"}}},
	} {
		got, err := inferREST(t, c.p)
		if err != nil {
			t.Errorf("%s: InferV1For = %v", c.name, err)
			continue
		}
		if got.Status != c.status || string(got.Body) != c.body {
			t.Errorf("%s: status %d body %q, want %d %q", c.name, got.Status, got.Body, c.status, c.body)
		}
		for k, v := range c.header {
			if vs := got.Header.Values(k); strings.Join(vs, ", ") != strings.Join(v, ", ") {
				t.Errorf("%s: header %s = %q, want %q", c.name, k, vs, v)
			}
		}
	}
	for name, p := range map[string]string{
		"an extra key":         `{"statusCode": 200, "wrongValue": "value"}`,
		"multi value not list": `{"statusCode": 200, "multiValueHeaders": {"test-multi-invalid": "value1"}}`,
		"a non-numeric status": `{"statusCode": "test"}`,
		"a bare string":        `"justAString"`,
	} {
		if _, err := inferREST(t, p); !isMalformed(err) {
			t.Errorf("%s: InferV1For error = %v, want *MalformedError", name, err)
		}
	}
	if _, err := httpevent.InferV1For([]byte(`{"statusCode":200}`), nil); err == nil || isMalformed(err) {
		t.Errorf("InferV1For with a nil front = %v, want an error naming it", err)
	}
}

// InferV1For with the HTTP API front is InferV1 for every payload, and
// with the REST front never panics.
func TestInferV1ForProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := genPayload().Draw(t, "payload")
		a, aerr := httpevent.InferV1For(p, httpevent.HTTPAPI{})
		b, berr := httpevent.InferV1(p)
		if (aerr == nil) != (berr == nil) || (aerr == nil && !sameResponse(a, b)) {
			t.Fatalf("InferV1For(%s, HTTPAPI) = %+v, %v; InferV1 = %+v, %v", p, a, aerr, b, berr)
		}
	})
	rapid.Check(t, func(t *rapid.T) {
		p := rapid.OneOf(genPayload(), rapid.SliceOfN(rapid.Byte(), 0, 64)).Draw(t, "payload")
		if _, err := httpevent.InferV1For(p, httpevent.REST{}); err != nil && !isMalformed(err) {
			t.Fatalf("InferV1For(%q, REST) error %v is not a *MalformedError", p, err)
		}
	})
}
