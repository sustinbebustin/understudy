package httpevent_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/httpevent"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
)

// The payload format 1.0 example this file pins against is the HTTP API
// guide's (https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html,
// apigateway-http.md 5.2): keys in its order, every header name lowercased,
// absent maps null.

// v1Request is a GET with a repeated header, a repeated query parameter, a
// cookie and a text body to the root of an HTTP API's $default stage.
func v1Request() httpevent.Request {
	return httpevent.Request{
		Method: http.MethodGet, RawPath: "/my/path", RawQuery: "parameter1=value1&parameter1=value2&parameter2=value",
		Protocol: "HTTP/1.1", Host: "a1b2c3d4e5.execute-api.us-east-1.localhost:4566",
		Header: http.Header{
			"Header1":      {"value1"},
			"Header2":      {"value1", "value2"},
			"User-Agent":   {"user-agent"},
			"Content-Type": {"text/plain"},
			"Cookie":       {"s=1"},
		},
		SourceIP: "192.0.2.1", Scheme: "http", Port: "4566", Body: []byte("Hello from Lambda!"),
	}
}

func v1Context() httpevent.Context {
	return httpevent.Context{
		AccountID: "123456789012", APIID: "a1b2c3d4e5", DomainName: "a1b2c3d4e5.execute-api.us-east-1.localhost", DomainPrefix: "a1b2c3d4e5",
		RouteKey: "GET /my/path", Stage: "$default", RequestID: "JKJaXmPLvHcESHA=",
		At: time.UnixMilli(1583349317135), Authorizer: httpevent.NoAuthorizer{},
	}
}

// v1Event is the 1.0 event's members this file reads.
type v1Event struct {
	Version                         string              `json:"version"`
	Resource                        string              `json:"resource"`
	Path                            string              `json:"path"`
	HTTPMethod                      string              `json:"httpMethod"`
	Headers                         map[string]string   `json:"headers"`
	MultiValueHeaders               map[string][]string `json:"multiValueHeaders"`
	QueryStringParameters           map[string]string   `json:"queryStringParameters"`
	MultiValueQueryStringParameters map[string][]string `json:"multiValueQueryStringParameters"`
	RequestContext                  struct {
		AccountID        string          `json:"accountId"`
		APIID            string          `json:"apiId"`
		DomainName       string          `json:"domainName"`
		DomainPrefix     string          `json:"domainPrefix"`
		HTTPMethod       string          `json:"httpMethod"`
		Identity         map[string]any  `json:"identity"`
		Path             string          `json:"path"`
		Protocol         string          `json:"protocol"`
		RequestID        string          `json:"requestId"`
		RequestTime      string          `json:"requestTime"`
		RequestTimeEpoch int64           `json:"requestTimeEpoch"`
		ResourceID       json.RawMessage `json:"resourceId"`
		ResourcePath     string          `json:"resourcePath"`
		Stage            string          `json:"stage"`
	} `json:"requestContext"`
	PathParameters  json.RawMessage `json:"pathParameters"`
	StageVariables  json.RawMessage `json:"stageVariables"`
	Body            *string         `json:"body"`
	IsBase64Encoded bool            `json:"isBase64Encoded"`
}

func eventV1(t *testing.T, r httpevent.Request, c httpevent.Context) ([]byte, v1Event) {
	t.Helper()
	raw, err := httpevent.EventV1(r, c, httpevent.HTTPAPI{})
	if err != nil {
		t.Fatalf("EventV1: %v", err)
	}
	var ev v1Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("event %s: %v", raw, err)
	}
	return raw, ev
}

// objectKeys lists the keys of the object at member name of the JSON object
// raw, in the order written.
func objectKeys(t *testing.T, raw []byte, name string) []string {
	t.Helper()
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	inner, ok := members[name]
	if !ok {
		t.Fatalf("%s has no %s", raw, name)
	}
	return topLevelKeys(t, inner)
}

// without returns keys without the named ones.
func without(keys []string, names ...string) []string {
	return slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return slices.Contains(names, k) })
}

// The event's keys follow the guide example's order at every level. The
// example's requestContext.authorizer is a JWT authorizer's and its
// identity.clientCert a mutual-TLS custom domain's; what a NONE route sends
// for them is UNVERIFIED (apigateway-http.md 5.2, APIGW2-08), so neither is
// part of the order checked.
func TestEventV1KeyOrder(t *testing.T) {
	raw, _ := eventV1(t, v1Request(), v1Context())
	top := []string{
		"version", "resource", "path", "httpMethod", "headers", "multiValueHeaders", "queryStringParameters",
		"multiValueQueryStringParameters", "requestContext", "pathParameters", "stageVariables", "body", "isBase64Encoded",
	}
	if got := topLevelKeys(t, raw); !slices.Equal(got, top) {
		t.Errorf("keys = %v\nwant   %v\nfrom %s", got, top, raw)
	}
	rc := []string{
		"accountId", "apiId", "domainName", "domainPrefix", "extendedRequestId", "httpMethod", "identity", "path",
		"protocol", "requestId", "requestTime", "requestTimeEpoch", "resourceId", "resourcePath", "stage",
	}
	if got := without(objectKeys(t, raw, "requestContext"), "authorizer"); !slices.Equal(got, rc) {
		t.Errorf("requestContext keys = %v\nwant              %v", got, rc)
	}
	var ev struct {
		RequestContext json.RawMessage `json:"requestContext"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	identity := []string{
		"accessKey", "accountId", "caller", "cognitoAuthenticationProvider", "cognitoAuthenticationType",
		"cognitoIdentityId", "cognitoIdentityPoolId", "principalOrgId", "sourceIp", "user", "userAgent", "userArn",
	}
	if got := without(objectKeys(t, ev.RequestContext, "identity"), "clientCert"); !slices.Equal(got, identity) {
		t.Errorf("identity keys = %v\nwant          %v", got, identity)
	}
}

// The members the guide's example fixes: version "1.0", the method twice,
// header names in the case sent (APIGW2-08, recorded 2026-11-03, against
// the guide's "All headernames are lowercased") with every value in
// multiValueHeaders, every
// decoded query value in multiValueQueryStringParameters, the context's
// ids, requestTime as 04/Mar/2020:19:15:17 +0000 with requestTimeEpoch in
// milliseconds, resourceId null, resourcePath equal to resource, the
// identity's sourceIp and userAgent, pathParameters and stageVariables null
// when there are none, and a text body as sent with isBase64Encoded false.
func TestEventV1Members(t *testing.T) {
	raw, ev := eventV1(t, v1Request(), v1Context())
	if ev.Version != "1.0" || ev.HTTPMethod != http.MethodGet || ev.RequestContext.HTTPMethod != http.MethodGet {
		t.Errorf("version %q, httpMethod %q, requestContext.httpMethod %q; want 1.0, GET, GET", ev.Version, ev.HTTPMethod, ev.RequestContext.HTTPMethod)
	}
	if ev.Path != "/my/path" || ev.RequestContext.Path != ev.Path {
		t.Errorf("path %q, requestContext.path %q; want the request's raw path /my/path in both", ev.Path, ev.RequestContext.Path)
	}
	if ev.RequestContext.ResourcePath != ev.Resource {
		t.Errorf("requestContext.resourcePath %q, resource %q; want them equal", ev.RequestContext.ResourcePath, ev.Resource)
	}
	for name := range ev.Headers {
		if name != http.CanonicalHeaderKey(name) {
			t.Errorf("header %q is not in the case it was sent in", name)
		}
	}
	if got := ev.MultiValueHeaders["Header2"]; !slices.Equal(got, []string{"value1", "value2"}) {
		t.Errorf("multiValueHeaders.Header2 = %q, want [value1 value2]", got)
	}
	if got := ev.MultiValueQueryStringParameters["parameter1"]; !slices.Equal(got, []string{"value1", "value2"}) {
		t.Errorf("multiValueQueryStringParameters.parameter1 = %q, want [value1 value2]", got)
	}
	if got := ev.MultiValueQueryStringParameters["parameter2"]; !slices.Equal(got, []string{"value"}) {
		t.Errorf("multiValueQueryStringParameters.parameter2 = %q, want [value]", got)
	}
	rc := ev.RequestContext
	if rc.AccountID != "123456789012" || rc.APIID != "a1b2c3d4e5" || rc.DomainName != "a1b2c3d4e5.execute-api.us-east-1.localhost" ||
		rc.DomainPrefix != "a1b2c3d4e5" || rc.RequestID != "JKJaXmPLvHcESHA=" || rc.Stage != "$default" || rc.Protocol != "HTTP/1.1" {
		t.Errorf("requestContext = %+v, want the context's account, api, domain, request id and stage, and HTTP/1.1", rc)
	}
	if rc.RequestTime != "04/Mar/2020:19:15:17 +0000" || rc.RequestTimeEpoch != 1583349317135 {
		t.Errorf("requestTime %q, requestTimeEpoch %d; want 04/Mar/2020:19:15:17 +0000 and 1583349317135", rc.RequestTime, rc.RequestTimeEpoch)
	}
	if string(rc.ResourceID) != "null" {
		t.Errorf("requestContext.resourceId = %s, want null", rc.ResourceID)
	}
	if rc.Identity["sourceIp"] != "192.0.2.1" || rc.Identity["userAgent"] != "user-agent" {
		t.Errorf("identity sourceIp %v, userAgent %v; want 192.0.2.1 and user-agent", rc.Identity["sourceIp"], rc.Identity["userAgent"])
	}
	if string(ev.PathParameters) != "null" || string(ev.StageVariables) != "null" {
		t.Errorf("pathParameters %s, stageVariables %s; want null when there are none", ev.PathParameters, ev.StageVariables)
	}
	if ev.Body == nil || *ev.Body != "Hello from Lambda!" || ev.IsBase64Encoded {
		t.Errorf("body %v, isBase64Encoded %v; want the text body as sent and false\nfrom %s", ev.Body, ev.IsBase64Encoded, raw)
	}
}

// An HTTP API's 1.0 event keeps header names in the client's case and names
// the front's own headers in canonical case, Content-Length included on a
// bodyless GET, with accept the one name lowercased (APIGW2-08, recorded
// 2026-11-03: 01-get, apigateway-http.md 5.2). net/http hands the client's
// names over canonicalized, which is how curl sent them.
func TestEventV1HeaderNamesAsAnHTTPAPIRecordedThem(t *testing.T) {
	r := httpevent.Request{
		Method: http.MethodGet, RawPath: "/v1/a/b", Protocol: "HTTP/1.1",
		Host:     "roj3rnqmie.execute-api.us-east-2.amazonaws.com",
		Header:   http.Header{"User-Agent": {"curl/8.23.0"}, "Accept": {"*/*"}},
		SourceIP: "198.51.100.2", Scheme: "https", Port: "443",
	}
	c := v1Context()
	c.TraceID = "Root=1-6ae957b1-04e2cd734698c4f569a95d02"
	_, ev := eventV1(t, r, c)
	want := map[string]string{
		"Content-Length":    "0",
		"Host":              "roj3rnqmie.execute-api.us-east-2.amazonaws.com",
		"User-Agent":        "curl/8.23.0",
		"X-Amzn-Trace-Id":   "Root=1-6ae957b1-04e2cd734698c4f569a95d02",
		"X-Forwarded-For":   "198.51.100.2",
		"X-Forwarded-Port":  "443",
		"X-Forwarded-Proto": "https",
		"accept":            "*/*",
	}
	if !maps.Equal(ev.Headers, want) {
		t.Errorf("headers = %v\nwant      %v", ev.Headers, want)
	}
	for name, value := range want {
		if got := ev.MultiValueHeaders[name]; !slices.Equal(got, []string{value}) {
			t.Errorf("multiValueHeaders.%s = %q, want [%q]", name, got, value)
		}
	}
	if len(ev.MultiValueHeaders) != len(want) {
		t.Errorf("multiValueHeaders = %v, want the names of headers", ev.MultiValueHeaders)
	}
}

// Route and stage variables, when there are some, are objects of strings,
// as in the 2.0 event (apigateway-http.md 5.1, 5.2).
func TestEventV1ReportsVariables(t *testing.T) {
	c := v1Context()
	c.RouteKey, c.PathParameters, c.StageVariables = "GET /pets/{id}", map[string]string{"id": "dog 1"}, map[string]string{"color": "blue"}
	r := v1Request()
	r.RawPath = "/pets/dog%201"
	_, ev := eventV1(t, r, c)
	var params, vars map[string]string
	if err := json.Unmarshal(ev.PathParameters, &params); err != nil || params["id"] != "dog 1" || len(params) != 1 {
		t.Errorf("pathParameters = %s, want {\"id\":\"dog 1\"}", ev.PathParameters)
	}
	if err := json.Unmarshal(ev.StageVariables, &vars); err != nil || vars["color"] != "blue" || len(vars) != 1 {
		t.Errorf("stageVariables = %s, want {\"color\":\"blue\"}", ev.StageVariables)
	}
	if ev.Path != "/pets/dog%201" {
		t.Errorf("path = %q, want the raw path as sent", ev.Path)
	}
}

// A binary body reaches the function base64 with isBase64Encoded true, by
// the 2.0 event's text rule (apigateway-http.md 5.1: "base64 for binary
// content").
func TestEventV1EncodesABinaryBody(t *testing.T) {
	r := v1Request()
	r.Header.Set("Content-Type", "image/png")
	r.Body = []byte{0x89, 'P', 'N', 'G', 0xff}
	_, ev := eventV1(t, r, v1Context())
	if ev.Body == nil || !ev.IsBase64Encoded {
		t.Fatalf("body %v, isBase64Encoded %v; want a base64 body", ev.Body, ev.IsBase64Encoded)
	}
	if got, err := base64.StdEncoding.DecodeString(*ev.Body); err != nil || !bytes.Equal(got, r.Body) {
		t.Errorf("body decodes to %x, %v; want %x", got, err, r.Body)
	}
}

// A missing front or authorizer is a caller bug, refused rather than
// guessed, as EventV2 refuses a nil Authorizer.
func TestEventV1RefusesNilFrontAndAuthorizer(t *testing.T) {
	if raw, err := httpevent.EventV1(v1Request(), v1Context(), nil); err == nil {
		t.Errorf("EventV1 with a nil front = %s, want an error", raw)
	}
	c := v1Context()
	c.Authorizer = nil
	if raw, err := httpevent.EventV1(v1Request(), c, httpevent.HTTPAPI{}); err == nil {
		t.Errorf("EventV1 with a nil Authorizer = %s, want an error", raw)
	}
}

// pick is the value of values a 1.0 single-value map keeps under p.
func pick(t *rapid.T, p apigatewayv2.Pick, values []string) string {
	switch p {
	case apigatewayv2.First:
		return values[0]
	case apigatewayv2.Last:
		return values[len(values)-1]
	}
	t.Fatalf("unknown pick %v", p)
	return ""
}

// For any request the event is JSON, and each single-value map keeps the
// value of the matching multi-value list that the
// apigatewayv2.payload.unrecorded switch names: which one AWS keeps is
// UNVERIFIED (apigateway-http.md 5.2, 12 item 6, APIGW2-08), so the test
// reads the switch's V1Header and V1Query rather than the guide's example.
// Every multi-value list holds the request's values in the order sent.
func TestEventV1SingleValueMapsFollowTheSwitch(t *testing.T) {
	choices := apigatewayv2.PayloadSwitch().Value
	rapid.Check(t, func(t *rapid.T) {
		r := httpevent.Request(genRequest().Draw(t, "request"))
		raw, err := httpevent.EventV1(r, v1Context(), httpevent.HTTPAPI{})
		if err != nil {
			t.Fatalf("EventV1: %v", err)
		}
		if !json.Valid(raw) {
			t.Fatalf("event is not JSON: %s", raw)
		}
		var ev v1Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatalf("event %s: %v", raw, err)
		}
		if len(ev.Headers) != len(ev.MultiValueHeaders) {
			t.Fatalf("headers %v and multiValueHeaders %v name different headers", ev.Headers, ev.MultiValueHeaders)
		}
		for name, values := range ev.MultiValueHeaders {
			if len(values) == 0 {
				t.Fatalf("multiValueHeaders.%s is empty", name)
			}
			if got, want := ev.Headers[name], pick(t, choices.V1Header, values); got != want {
				t.Fatalf("headers.%s = %q, want %q of %q (V1Header %v)", name, got, want, values, choices.V1Header)
			}
		}
		for name, values := range r.Header {
			lower := strings.ToLower(name)
			if lower == "host" || strings.HasPrefix(lower, "x-forwarded-") || lower == "x-amzn-trace-id" || lower == "content-length" {
				continue // the front's own headers are set over the client's
			}
			// The client's names keep their case, but accept (APIGW2-08).
			key := http.CanonicalHeaderKey(name)
			if lower == "accept" {
				key = lower
			}
			if got := ev.MultiValueHeaders[key]; !slices.Equal(got, values) {
				t.Fatalf("multiValueHeaders.%s = %q, want the values sent, %q", key, got, values)
			}
		}
		query, err := url.ParseQuery(r.RawQuery)
		if err != nil {
			t.Fatalf("query %q: %v", r.RawQuery, err)
		}
		if len(query) == 0 && (ev.QueryStringParameters != nil || ev.MultiValueQueryStringParameters != nil) {
			t.Fatalf("query %q: queryStringParameters %v, multiValueQueryStringParameters %v; want null", r.RawQuery, ev.QueryStringParameters, ev.MultiValueQueryStringParameters)
		}
		for name, values := range query {
			if got := ev.MultiValueQueryStringParameters[name]; !slices.Equal(got, values) {
				t.Fatalf("multiValueQueryStringParameters.%s = %q, want the decoded values in order, %q", name, got, values)
			}
			if got, want := ev.QueryStringParameters[name], pick(t, choices.V1Query, values); got != want {
				t.Fatalf("queryStringParameters.%s = %q, want %q of %q (V1Query %v)", name, got, want, values, choices.V1Query)
			}
		}
	})
}

// inferV1 is InferV1 on a JSON-encoded value.
func inferV1(t *testing.T, v any) (httpevent.Response, error) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return httpevent.InferV1(b)
}

// A 1.0 response is "a response in the following JSON format":
// isBase64Encoded, statusCode, headers, multiValueHeaders and body; there
// is no inference (apigateway-http.md 5.4). Single and multi-value headers
// both reach the client, a base64 body is decoded, and InferV1 adds no
// Content-Type: the HTTP API front applies its default
// (switch apigatewayv2.payload.unrecorded, DefaultContentType).
func TestInferV1ReadsTheDocumentedFormat(t *testing.T) {
	resp, err := inferV1(t, map[string]any{
		"statusCode": 201, "headers": map[string]string{"x-one": "1"},
		"multiValueHeaders": map[string][]string{"x-many": {"a", "b"}}, "body": "hello", "isBase64Encoded": false,
	})
	if err != nil {
		t.Fatalf("InferV1: %v", err)
	}
	if resp.Status != 201 || string(resp.Body) != "hello" {
		t.Errorf("response %d %q, want 201 hello", resp.Status, resp.Body)
	}
	if got := resp.Header.Values("X-One"); !slices.Equal(got, []string{"1"}) {
		t.Errorf("x-one = %q, want [1]", got)
	}
	if got := resp.Header.Values("X-Many"); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("x-many = %q, want [a b], one field each", got)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		t.Errorf("Content-Type = %q, want none added by InferV1", ct)
	}
	if len(resp.Cookies) != 0 {
		t.Errorf("cookies = %q, want none: 1.0 has no cookies member", resp.Cookies)
	}

	resp, err = inferV1(t, map[string]any{"statusCode": 200, "body": base64.StdEncoding.EncodeToString([]byte{0, 1, 2}), "isBase64Encoded": true})
	if err != nil || !bytes.Equal(resp.Body, []byte{0, 1, 2}) {
		t.Errorf("base64 body = %x, %v; want 000102", resp.Body, err)
	}
	resp, err = inferV1(t, map[string]any{"statusCode": 204, "body": nil, "headers": nil, "multiValueHeaders": nil})
	if err != nil || resp.Status != 204 || len(resp.Body) != 0 {
		t.Errorf("null members = %+v, %v; want 204 with an empty body", resp, err)
	}
}

// Output that is not the documented format has no inference to fall back
// on (apigateway-http.md 5.4), so it is malformed and the front answers
// with the function-error answer: output that is not JSON, a bare JSON
// string, an object without statusCode, a body that is not a string.
func TestInferV1RefusesOtherOutput(t *testing.T) {
	for _, p := range []string{
		`not json`,
		`"Hello from Lambda!"`,
		`{"body":"hello"}`,
		`{"statusCode":200,"body":7}`,
		`{"statusCode":200,"isBase64Encoded":true,"body":"%%%"}`,
	} {
		resp, err := httpevent.InferV1([]byte(p))
		if _, ok := errors.AsType[*httpevent.MalformedError](err); !ok {
			t.Errorf("InferV1(%s) = %+v, %v; want a *MalformedError", p, resp, err)
		}
	}
}

// Headers that frame the exchange are the front's, as for 2.0: a
// function's Content-Length or Transfer-Encoding never reaches the client.
func TestInferV1DropsFramingHeaders(t *testing.T) {
	resp, err := inferV1(t, map[string]any{
		"statusCode": 200, "body": "abc",
		"headers":           map[string]string{"Content-Length": "999", "x-kept": "1"},
		"multiValueHeaders": map[string][]string{"transfer-encoding": {"chunked"}},
	})
	if err != nil {
		t.Fatalf("InferV1: %v", err)
	}
	for _, h := range []string{"Content-Length", "Transfer-Encoding"} {
		if v := resp.Header.Values(h); len(v) != 0 {
			t.Errorf("%s = %q, want it dropped", h, v)
		}
	}
	if resp.Header.Get("X-Kept") != "1" {
		t.Errorf("x-kept = %q, want 1", resp.Header.Get("X-Kept"))
	}
}

// InferV1 answers every byte string with a response or an error, never a
// panic: function output is untrusted.
func TestInferV1NeverPanics(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := rapid.OneOf(
			rapid.SliceOfN(rapid.Byte(), 0, 64),
			rapid.Map(rapid.StringMatching(`\{"statusCode":(200|"x"|null|7\.5),"headers":(null|\{"a":(1|"b"|null)\}),"multiValueHeaders":(null|\{"a":(\["b"\]|"b"|\[1\])\}),"body":(null|"x"|1),"isBase64Encoded":(true|false|"x")\}`), func(s string) []byte { return []byte(s) }),
		).Draw(t, "payload")
		resp, err := httpevent.InferV1(p)
		if err == nil && (resp.Status < 200 || resp.Status > 599) {
			t.Fatalf("InferV1(%s) = status %d without an error", p, resp.Status)
		}
	})
}
