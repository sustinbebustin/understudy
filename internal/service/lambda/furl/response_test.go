package furl_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/lambda/furl"
)

// TestInferResponseMatrix is the response inference of lambda-events.md
// 7.4 and lambda-api.md 10: the shapes recorded against AWS
// (test_lambda_url_invocation, test_lambda_url_headers_and_status) and
// the guide's structured example.
func TestInferResponseMatrix(t *testing.T) {
	const json200 = "application/json"
	for _, c := range []struct {
		name        string
		payload     string
		status      int
		contentType string
		header      map[string]string
		cookies     []string
		body        string
		// absent are headers the response must not carry.
		absent []string
	}{
		{name: "JSON string is the bare string", payload: `"hello"`, status: 200, contentType: json200, body: "hello"},
		{name: "object without statusCode is re-serialized", payload: `{"hello": "world"}`, status: 200, contentType: json200, body: `{"hello":"world"}`},
		{name: "object with body but no statusCode is the whole body", payload: `{"body": "x"}`, status: 200, contentType: json200, body: `{"body":"x"}`},
		{name: "list", payload: `["hello", 3, true]`, status: 200, contentType: json200, body: `["hello",3,true]`},
		{name: "integer", payload: `3`, status: 200, contentType: json200, body: `3`},
		{name: "null", payload: `null`, status: 200, contentType: json200, body: `null`},
		{
			name:        "structured with headers and cookies",
			payload:     `{"statusCode": 201, "headers": {"Content-Type": "text/html", "My-Custom-Header": "Custom Value"}, "body": "{ \"message\": \"Hello, world!\" }", "cookies": ["Cookie_1=Value1; Expires=21 Oct 2021 07:48 GMT", "Cookie_2=Value2; Max-Age=78000"], "isBase64Encoded": false}`,
			status:      201,
			contentType: "text/html",
			header:      map[string]string{"My-Custom-Header": "Custom Value"},
			cookies:     []string{"Cookie_1=Value1; Expires=21 Oct 2021 07:48 GMT", "Cookie_2=Value2; Max-Age=78000"},
			body:        `{ "message": "Hello, world!" }`,
		},
		{name: "structured status and body keep application/json", payload: `{"statusCode": 418, "body": "i'm a teapot"}`, status: 418, contentType: json200, body: "i'm a teapot"},
		{name: "base64 body is decoded", payload: `{"statusCode": 200, "body": "aGk=", "isBase64Encoded": true}`, status: 200, contentType: json200, body: "hi"},
		{name: "no body", payload: `{"statusCode": 204}`, status: 204, contentType: json200},
		{name: "null body", payload: `{"statusCode": 200, "body": null}`, status: 200, contentType: json200},
		{
			name:        "framing headers are dropped",
			payload:     `{"statusCode": 200, "headers": {"Content-Length": "3", "transfer-encoding": "chunked", "Connection": "close", "Keep-Alive": "timeout=5", "Upgrade": "h2c", "Trailer": "X-T", "X-Kept": "yes"}, "body": "hello world"}`,
			status:      200,
			contentType: json200,
			header:      map[string]string{"X-Kept": "yes"},
			body:        "hello world",
			absent:      []string{"Content-Length", "Transfer-Encoding", "Connection", "Keep-Alive", "Upgrade", "Trailer"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := furl.Infer([]byte(c.payload))
			if err != nil {
				t.Fatalf("Infer: %v", err)
			}
			if r.Status != c.status || r.Header.Get("Content-Type") != c.contentType || string(r.Body) != c.body || !slices.Equal(r.Cookies, c.cookies) {
				t.Errorf("Infer = %d %q %q cookies %q\nwant    %d %q %q cookies %q", r.Status, r.Header.Get("Content-Type"), r.Body, r.Cookies, c.status, c.contentType, c.body, c.cookies)
			}
			for name, value := range c.header {
				if got := r.Header.Get(name); got != value {
					t.Errorf("header %s = %q, want %q", name, got, value)
				}
			}
			for _, name := range c.absent {
				if got, ok := r.Header[http.CanonicalHeaderKey(name)]; ok {
					t.Errorf("header %s = %q, want it absent", name, got)
				}
			}
		})
	}
}

// A Content-Length the function computed wrongly, here on the base64 form
// of its body, does not truncate or break the exchange: the length sent is
// the decoded body's.
func TestWriteFramesTheBodyItself(t *testing.T) {
	r, err := furl.Infer([]byte(`{"statusCode": 200, "headers": {"Content-Length": "16"}, "body": "aGVsbG8gd29ybGQ=", "isBase64Encoded": true}`))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { r.Write(w) }))
	defer srv.Close()
	resp, err := http.Get(srv.URL) //nolint:noctx // a test server on loopback
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "hello world" || resp.ContentLength != int64(len("hello world")) {
		t.Errorf("read %q (Content-Length %d), %v; want \"hello world\" (11) without error", body, resp.ContentLength, err)
	}
}

func TestInferRefusesMalformedResponses(t *testing.T) {
	for _, payload := range []string{
		`not json`,
		`{"statusCode": "200"}`,
		`{"statusCode": 200.5}`,
		`{"statusCode": 600}`,
		`{"statusCode": 100}`,
		`{"statusCode": null}`,
		`{"statusCode": 200, "headers": {"X-Count": 1}}`,
		`{"statusCode": 200, "headers": []}`,
		`{"statusCode": 200, "cookies": "a=b"}`,
		`{"statusCode": 200, "body": {"a": 1}}`,
		`{"statusCode": 200, "isBase64Encoded": "yes"}`,
		`{"statusCode": 200, "body": "%%%", "isBase64Encoded": true}`,
	} {
		_, err := furl.Infer([]byte(payload))
		if m, ok := errors.AsType[*furl.MalformedError](err); !ok || m.Problem == "" {
			t.Errorf("Infer(%s) = %v, want a MalformedError naming the problem", payload, err)
		}
	}
}

func TestWriteSendsEachCookieAsASetCookieHeader(t *testing.T) {
	r, err := furl.Infer([]byte(`{"statusCode": 201, "headers": {"X-A": "1"}, "cookies": ["a=1", "b=2"], "body": "ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.Write(rec)
	if rec.Code != 201 || rec.Body.String() != "ok" || rec.Header().Get("X-A") != "1" || !slices.Equal(rec.Header().Values("Set-Cookie"), []string{"a=1", "b=2"}) {
		t.Errorf("wrote %d %q headers %v", rec.Code, rec.Body, rec.Header())
	}
}

func TestErrorAnswers(t *testing.T) {
	for _, c := range []struct {
		name        string
		r           furl.Response
		status      int
		contentType string
		errorType   string
		body        string
	}{
		{"bad gateway", furl.BadGateway(), http.StatusBadGateway, "text/plain", "", "Internal Server Error"},
		{"unknown url", furl.UnknownURL(), http.StatusForbidden, "application/json", "AccessDeniedException", `{"Message":null}`},
		{
			"forbidden", furl.Forbidden(), http.StatusForbidden, "application/json", "AccessDeniedException",
			`{"Message":"Forbidden. For troubleshooting Function URL authorization issues, see: https://docs.aws.amazon.com/lambda/latest/dg/urls-auth.html"}`,
		},
	} {
		rec := httptest.NewRecorder()
		c.r.Write(rec)
		h := rec.Header()
		if rec.Code != c.status || h.Get("Content-Type") != c.contentType || h.Get("X-Amzn-Errortype") != c.errorType || rec.Body.String() != c.body {
			t.Errorf("%s: %d %q %q %s\nwant %d %q %q %s", c.name, rec.Code, h.Get("Content-Type"), h.Get("X-Amzn-Errortype"), rec.Body, c.status, c.contentType, c.errorType, c.body)
		}
	}
}

// Any output either becomes a response or is refused as malformed, and a
// JSON object without statusCode is always the compact body of a 200.
func TestInferProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		payload := rapid.SliceOfN(rapid.Byte(), 0, 64).Draw(t, "payload")
		if _, err := furl.Infer(payload); err != nil {
			if m, ok := errors.AsType[*furl.MalformedError](err); !ok || m.Problem == "" {
				t.Fatalf("Infer(%q) = %v, want a response or a MalformedError naming the problem", payload, err)
			}
		}
		obj := rapid.MapOfN(rapid.StringMatching(`[a-zA-Z]{1,6}`).Filter(func(k string) bool { return k != "statusCode" }), rapid.Int(), 0, 4).Draw(t, "object")
		raw, err := json.MarshalIndent(obj, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		r, err := furl.Infer(raw)
		if err != nil {
			t.Fatal(err)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			t.Fatal(err)
		}
		if r.Status != http.StatusOK || !bytes.Equal(r.Body, compact.Bytes()) {
			t.Fatalf("Infer(%s) = %d %s, want 200 with the compact object", raw, r.Status, r.Body)
		}
	})
}
