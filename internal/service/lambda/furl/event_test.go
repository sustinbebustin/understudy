package furl_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/lambda/furl"
)

// guideRequest is the request behind the Lambda guide's payload 2.0
// example (lambda-events.md 7.3), sent over HTTPS on port 443. The guide's
// time and timeEpoch disagree; the golden keeps timeEpoch and reports the
// time it stands for.
func guideRequest() furl.Request {
	return furl.Request{
		Method:   http.MethodPost,
		RawPath:  "/my/path",
		RawQuery: "parameter1=value1&parameter1=value2&parameter2=value",
		Protocol: "HTTP/1.1",
		Host:     "abcdefghij0123456789klmnopqrstuv.lambda-url.us-west-2.on.aws",
		Header: http.Header{
			"Content-Type": {"text/plain"},
			"Header1":      {"value1"},
			"Header2":      {"value1", "value2"},
			"Cookie":       {"cookie1", "cookie2"},
			"User-Agent":   {"agent"},
		},
		SourceIP: "123.123.123.123",
		Scheme:   "https",
		Port:     "443",
		Body:     []byte("Hello from client!"),
	}
}

func guideContext() furl.Context {
	return furl.Context{
		URLID:      "abcdefghij0123456789klmnopqrstuv",
		DomainName: "abcdefghij0123456789klmnopqrstuv.lambda-url.us-west-2.on.aws",
		RequestID:  "id",
		At:         time.UnixMilli(1583348638390),
		Caller:     furl.Public{},
	}
}

// golden reads a JSON file of testdata in its compact form.
func golden(tb testing.TB, name string) []byte {
	tb.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		tb.Fatal(err)
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		tb.Fatal(err)
	}
	return b.Bytes()
}

func event(tb testing.TB, r furl.Request, c furl.Context) map[string]any {
	tb.Helper()
	raw, err := furl.Event(r, c)
	if err != nil {
		tb.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		tb.Fatalf("event %s is not a JSON object: %v", raw, err)
	}
	return out
}

func TestEventMatchesTheGuideExample(t *testing.T) {
	got, err := furl.Event(guideRequest(), guideContext())
	if err != nil {
		t.Fatal(err)
	}
	if want := golden(t, "request-guide.json"); !bytes.Equal(got, want) {
		t.Errorf("event\n%s\nwant\n%s", got, want)
	}
}

func TestEventDecodesQueryValuesButKeepsRawPathAndQueryAsSent(t *testing.T) {
	r := guideRequest()
	r.RawPath, r.RawQuery = "/pa2%Fth/1", "multiQuery=foo&multiQuery=%2Fbar&plus=a+b"
	ev := event(t, r, guideContext())
	qs, ok := ev["queryStringParameters"].(map[string]any)
	if !ok || qs["multiQuery"] != "foo,/bar" || qs["plus"] != "a b" {
		t.Errorf("queryStringParameters = %v, want multiQuery foo,/bar and plus %q (LocalStack's recorded echo)", ev["queryStringParameters"], "a b")
	}
	if ev["rawPath"] != "/pa2%Fth/1" || ev["rawQueryString"] != r.RawQuery {
		t.Errorf("rawPath %v, rawQueryString %v; want them as sent", ev["rawPath"], ev["rawQueryString"])
	}
}

func TestEventWithoutBodyQueryOrCookiesLeavesThemOut(t *testing.T) {
	r := guideRequest()
	r.Method, r.RawQuery, r.Body = http.MethodGet, "", nil
	r.Header.Del("Cookie")
	ev := event(t, r, guideContext())
	for _, key := range []string{"body", "queryStringParameters", "cookies"} {
		if v, ok := ev[key]; ok {
			t.Errorf("%s = %v, want no %s key", key, v, key)
		}
	}
	if ev["isBase64Encoded"] != false || ev["rawQueryString"] != "" {
		t.Errorf("isBase64Encoded %v, rawQueryString %q; want false and empty", ev["isBase64Encoded"], ev["rawQueryString"])
	}
}

func TestEventBase64EncodesBinaryBodies(t *testing.T) {
	for _, c := range []struct {
		contentType string
		body        string
		base64      bool
	}{
		{"text/plain; charset=utf-8", "hello", false},
		{"application/json", `{"a":1}`, false},
		{"application/vnd.api+json", `{}`, false},
		{"application/xml", "<a/>", false},
		{"multipart/form-data; boundary=x", "--x\r\n", true},
		{"application/x-www-form-urlencoded", "a=b", true},
		{"application/octet-stream", "hello", true},
		{"", "hello", true},
		{"text/plain", "\xff\xfe", true},
	} {
		r := guideRequest()
		r.Header.Set("Content-Type", c.contentType)
		r.Body = []byte(c.body)
		ev := event(t, r, guideContext())
		want := c.body
		if c.base64 {
			want = base64.StdEncoding.EncodeToString([]byte(c.body))
		}
		if ev["body"] != want || ev["isBase64Encoded"] != c.base64 {
			t.Errorf("%q body: body %v, isBase64Encoded %v; want %q, %v", c.contentType, ev["body"], ev["isBase64Encoded"], want, c.base64)
		}
	}
}

func TestEventReportsAnIAMCallerUnderAuthorizer(t *testing.T) {
	c := guideContext()
	c.Caller = furl.IAMCaller{
		AccessKey: "AKIAEXAMPLE", AccountID: "111122223333", ID: "AIDAEXAMPLE",
		UserARN: "arn:aws:iam::111122223333:user/example-user",
	}
	c.TraceID = "Root=1-5e5fe6fe-0123456789abcdef01234567"
	raw, err := furl.Event(guideRequest(), c)
	if err != nil {
		t.Fatal(err)
	}
	var ev struct {
		Headers        map[string]string `json:"headers"`
		RequestContext struct {
			AccountID  string          `json:"accountId"`
			Authorizer json.RawMessage `json:"authorizer"`
		} `json:"requestContext"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	const want = `{"iam":{"accessKey":"AKIAEXAMPLE","accountId":"111122223333","callerId":"AIDAEXAMPLE","cognitoIdentity":null,"principalOrgId":null,"userArn":"arn:aws:iam::111122223333:user/example-user","userId":"AIDAEXAMPLE"}}`
	if string(ev.RequestContext.Authorizer) != want || ev.RequestContext.AccountID != "111122223333" {
		t.Errorf("accountId %s, authorizer %s; want the caller's account and\n%s", ev.RequestContext.AccountID, ev.RequestContext.Authorizer, want)
	}
	if ev.Headers["x-amzn-trace-id"] != c.TraceID {
		t.Errorf("x-amzn-trace-id = %q, want %q", ev.Headers["x-amzn-trace-id"], c.TraceID)
	}
}

// Whatever the client sends, header names reach the function lowercase,
// cookies never sit among the headers, and the body decodes to the bytes
// sent.
func TestEventHeadersAndBodyProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := guideRequest()
		r.Header = http.Header{}
		name := rapid.StringMatching(`[A-Za-z][A-Za-z0-9-]{0,10}`).Filter(func(s string) bool { return !strings.EqualFold(s, "cookie") })
		for _, name := range rapid.SliceOfN(name, 0, 6).Draw(t, "names") {
			r.Header.Add(name, rapid.StringMatching(`[ -~]{0,12}`).Draw(t, "value"))
		}
		r.Header.Set("Content-Type", rapid.SampledFrom([]string{"text/plain", "application/json", "image/png", ""}).Draw(t, "type"))
		cookies := rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,5}=[a-z0-9]{0,5}`), 0, 4).Draw(t, "cookies")
		if len(cookies) > 0 {
			r.Header.Set("Cookie", strings.Join(cookies, "; "))
		}
		r.Body = rapid.SliceOfN(rapid.Byte(), 0, 64).Draw(t, "body")
		raw, err := furl.Event(r, guideContext())
		if err != nil {
			t.Fatal(err)
		}
		var ev struct {
			Cookies         []string          `json:"cookies"`
			Headers         map[string]string `json:"headers"`
			Body            *string           `json:"body"`
			IsBase64Encoded bool              `json:"isBase64Encoded"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatal(err)
		}
		for name := range ev.Headers {
			if name != strings.ToLower(name) || name == "cookie" {
				t.Fatalf("header %q reached the function", name)
			}
		}
		if strings.Join(ev.Cookies, "; ") != strings.Join(cookies, "; ") {
			t.Fatalf("cookies = %q, want %q", ev.Cookies, cookies)
		}
		var body []byte
		switch {
		case ev.Body == nil:
		case ev.IsBase64Encoded:
			if body, err = base64.StdEncoding.DecodeString(*ev.Body); err != nil {
				t.Fatalf("base64 body %q: %v", *ev.Body, err)
			}
		default:
			body = []byte(*ev.Body)
		}
		if !bytes.Equal(body, r.Body) {
			t.Fatalf("body decodes to %q, want %q", body, r.Body)
		}
	})
}
