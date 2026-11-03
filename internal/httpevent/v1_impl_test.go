package httpevent_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/httpevent"
)

// members decodes the JSON object raw into its raw members.
func members(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return m
}

// resource and requestContext.resourcePath are the route key's path, and
// $default for the $default route (inferred, apigateway-http.md 5.2,
// APIGW2-08); requestContext.extendedRequestId is the request id.
func TestEventV1ResourceIsTheRoutePath(t *testing.T) {
	for key, want := range map[string]string{"GET /pets/{id}": "/pets/{id}", "ANY /{proxy+}": "/{proxy+}", "$default": "$default"} {
		c := v1Context()
		c.RouteKey = key
		raw, ev := eventV1(t, v1Request(), c)
		if ev.Resource != want || ev.RequestContext.ResourcePath != want {
			t.Errorf("route %q: resource %q, resourcePath %q; want %q", key, ev.Resource, ev.RequestContext.ResourcePath, want)
		}
		var rc struct {
			ExtendedRequestID string `json:"extendedRequestId"`
		}
		if err := json.Unmarshal(members(t, raw)["requestContext"], &rc); err != nil || rc.ExtendedRequestID != c.RequestID {
			t.Errorf("extendedRequestId = %q, %v; want the request id %q", rc.ExtendedRequestID, err, c.RequestID)
		}
	}
}

// Without an authorizer, requestContext has no authorizer and every
// identity member is null but sourceIp and userAgent; under IAMAuthorizer
// identity names the signer (apigateway-http.md 5.2; what AWS sends on
// these routes is UNVERIFIED, APIGW2-08).
func TestEventV1Identity(t *testing.T) {
	identityOf := func(c httpevent.Context) (map[string]any, bool) {
		t.Helper()
		raw, _ := eventV1(t, v1Request(), c)
		var rc map[string]json.RawMessage
		if err := json.Unmarshal(members(t, raw)["requestContext"], &rc); err != nil {
			t.Fatal(err)
		}
		var identity map[string]any
		if err := json.Unmarshal(rc["identity"], &identity); err != nil {
			t.Fatal(err)
		}
		_, hasAuthorizer := rc["authorizer"]
		return identity, hasAuthorizer
	}
	identity, hasAuthorizer := identityOf(v1Context())
	if hasAuthorizer {
		t.Error("requestContext.authorizer is present without an authorizer")
	}
	for name, v := range identity {
		switch name {
		case "sourceIp", "userAgent":
		default:
			if v != nil {
				t.Errorf("identity.%s = %v, want null without an authorizer", name, v)
			}
		}
	}

	c := v1Context()
	c.Authorizer = httpevent.IAMAuthorizer{AccessKey: "AKIA1", AccountID: "111122223333", CallerID: "AIDA1", UserARN: "arn:aws:iam::111122223333:user/u", UserID: "AIDA1"}
	identity, hasAuthorizer = identityOf(c)
	if hasAuthorizer {
		t.Error("requestContext.authorizer is present under IAMAuthorizer, want the signer in identity only")
	}
	want := map[string]any{"accessKey": "AKIA1", "accountId": "111122223333", "caller": "AIDA1", "user": "AIDA1", "userArn": "arn:aws:iam::111122223333:user/u"}
	for name, v := range want {
		if identity[name] != v {
			t.Errorf("identity.%s = %v, want %v", name, identity[name], v)
		}
	}
}

// Cookie stays a header under 1.0 (the guide's example has no cookies
// member), and the front's own headers are set over the client's in both
// header maps.
func TestEventV1Headers(t *testing.T) {
	r := v1Request()
	r.Header.Set("Cookie", "a=1; b=2")
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	c := v1Context()
	c.TraceID = "Root=1-5e5fe6fe-0123456789abcdef01234567"
	raw, ev := eventV1(t, r, c)
	if _, ok := members(t, raw)["cookies"]; ok {
		t.Errorf("event has a cookies member: %s", raw)
	}
	if ev.Headers["Cookie"] != "a=1; b=2" || !slices.Equal(ev.MultiValueHeaders["Cookie"], []string{"a=1; b=2"}) {
		t.Errorf("Cookie header %q, multi %q; want a=1; b=2 as sent", ev.Headers["Cookie"], ev.MultiValueHeaders["Cookie"])
	}
	for name, want := range map[string]string{
		"Host": r.Host, "X-Forwarded-For": r.SourceIP, "X-Forwarded-Proto": "http", "X-Forwarded-Port": "4566", "X-Amzn-Trace-Id": c.TraceID,
		"Content-Length": "18",
	} {
		if ev.Headers[name] != want || !slices.Equal(ev.MultiValueHeaders[name], []string{want}) {
			t.Errorf("%s = %q, multi %q; want the front's %q in both", name, ev.Headers[name], ev.MultiValueHeaders[name], want)
		}
	}
}

// A request without a body, query, route or stage variables writes each as
// null (apigateway-http.md 5.2: absent maps are null, not left out); an
// empty map is absent too. headers and multiValueHeaders are always
// objects. A query whose only pair is malformed has no parameters.
func TestEventV1NullMembers(t *testing.T) {
	r := v1Request()
	r.Body, r.RawQuery = nil, "%zz=1"
	c := v1Context()
	c.PathParameters, c.StageVariables = map[string]string{}, map[string]string{}
	raw, ev := eventV1(t, r, c)
	m := members(t, raw)
	for _, name := range []string{"body", "queryStringParameters", "multiValueQueryStringParameters", "pathParameters", "stageVariables"} {
		if string(m[name]) != "null" {
			t.Errorf("%s = %s, want null", name, m[name])
		}
	}
	for _, name := range []string{"headers", "multiValueHeaders"} {
		if !bytes.HasPrefix(m[name], []byte("{")) {
			t.Errorf("%s = %s, want an object", name, m[name])
		}
	}
	if ev.IsBase64Encoded {
		t.Error("isBase64Encoded = true for a bodyless request")
	}
}

// The event is compact, has no trailing newline and keeps &, < and > as
// sent, as EventV2's.
func TestEventV1Encoding(t *testing.T) {
	r := v1Request()
	r.Body = []byte("<a>&</a>")
	raw, _ := eventV1(t, r, v1Context())
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil || !bytes.Equal(compact.Bytes(), raw) {
		t.Errorf("event is not compact JSON: %s", raw)
	}
	if bytes.HasSuffix(raw, []byte("\n")) {
		t.Error("event ends with a newline")
	}
	if !bytes.Contains(raw, []byte(`"body":"<a>&</a>"`)) {
		t.Errorf("event escapes &, < or >: %s", raw)
	}
}

// headers and multiValueHeaders merge per name, case-insensitively: the
// multi values first, then the single value unless already among them
// (REST's recorded merge, apigateway-rest.md 5.2; UNVERIFIED for HTTP
// APIs, APIGW2-09).
func TestInferV1MergesHeaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		want   []string
	}{
		{"distinct values", `{"statusCode":200,"headers":{"x-a":"solo"},"multiValueHeaders":{"x-a":["multi"]}}`, []string{"multi", "solo"}},
		{"repeated value", `{"statusCode":200,"headers":{"x-a":"v"},"multiValueHeaders":{"x-a":["v","w"]}}`, []string{"v", "w"}},
		{"names in two cases", `{"statusCode":200,"headers":{"x-a":"2"},"multiValueHeaders":{"X-A":["1"]}}`, []string{"1", "2"}},
		{"single only", `{"statusCode":200,"headers":{"x-a":"1"}}`, []string{"1"}},
	} {
		resp, err := httpevent.InferV1([]byte(tc.output))
		if err != nil {
			t.Errorf("%s: InferV1: %v", tc.name, err)
			continue
		}
		if got := resp.Header.Values("X-A"); !slices.Equal(got, tc.want) {
			t.Errorf("%s: x-a = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Output that is not an object with a final statusCode and the documented
// member types is malformed; keys outside the format, cookies included,
// are ignored (apigateway-http.md 5.4).
func TestInferV1Members(t *testing.T) {
	for _, p := range []string{
		`null`, `[]`, `7`,
		`{"statusCode":100}`, `{"statusCode":"200"}`,
		`{"statusCode":200,"headers":{"a":1}}`, `{"statusCode":200,"headers":[]}`,
		`{"statusCode":200,"multiValueHeaders":{"a":"b"}}`, `{"statusCode":200,"multiValueHeaders":{"a":[1]}}`,
		`{"statusCode":200,"isBase64Encoded":"yes"}`,
	} {
		if _, err := httpevent.InferV1([]byte(p)); !isMalformed(err) {
			t.Errorf("InferV1(%s) = %v, want a *MalformedError", p, err)
		}
	}
	resp, err := httpevent.InferV1([]byte(`{"statusCode":200,"cookies":["a=1"],"extra":true,"body":"x"}`))
	if err != nil || resp.Status != http.StatusOK || string(resp.Body) != "x" || len(resp.Cookies) != 0 || resp.Header.Get("Set-Cookie") != "" {
		t.Errorf("other keys = %+v, %v; want 200 x without cookies", resp, err)
	}
}

func isMalformed(err error) bool {
	_, ok := errors.AsType[*httpevent.MalformedError](err)
	return ok
}
