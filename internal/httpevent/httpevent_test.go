package httpevent_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/httpevent"
	"github.com/sustinbebustin/understudy/internal/service/lambda/furl"
)

// genRequest draws a request as the function URL front end hands one on:
// mixed-case and repeated headers, cookies, a raw path and query with
// escapes, and a text or binary body.
func genRequest() *rapid.Generator[furl.Request] {
	return rapid.Custom(func(t *rapid.T) furl.Request {
		header := http.Header{}
		name := rapid.StringMatching(`[A-Za-z][A-Za-z0-9-]{0,10}`).Filter(func(s string) bool { return !strings.EqualFold(s, "cookie") })
		for i, n := range rapid.SliceOfN(name, 0, 6).Draw(t, "header names") {
			header.Add(n, rapid.StringMatching(`[ -~]{0,12}`).Draw(t, fmt.Sprintf("header %d value", i)))
		}
		if ct := rapid.SampledFrom([]string{"", "text/plain", "application/json", "image/png", "multipart/form-data; boundary=x"}).Draw(t, "content type"); ct != "" {
			header.Set("Content-Type", ct)
		}
		if cookies := rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,5}=[a-z0-9]{0,5}`), 0, 4).Draw(t, "cookies"); len(cookies) > 0 {
			header.Set("Cookie", strings.Join(cookies, "; "))
		}
		if rapid.Bool().Draw(t, "trace header") {
			header.Set("X-Amzn-Trace-Id", "Root=1-5e5fe6fe-0123456789abcdef01234567")
		}
		segments := rapid.SliceOfN(rapid.StringMatching(`[a-z0-9]{0,4}(%2F|%20)?`), 0, 3).Draw(t, "path")
		query := rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,3}=[a-z0-9]{0,3}(%2F)?`), 0, 3).Draw(t, "query")
		return furl.Request{
			Method:   rapid.SampledFrom([]string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete}).Draw(t, "method"),
			RawPath:  "/" + strings.Join(segments, "/"),
			RawQuery: strings.Join(query, "&"),
			Protocol: "HTTP/1.1",
			Host:     "abc123.lambda-url.us-east-1.localhost:4566",
			Header:   header,
			SourceIP: "127.0.0.1",
			Scheme:   rapid.SampledFrom([]string{"http", "https"}).Draw(t, "scheme"),
			Port:     rapid.SampledFrom([]string{"4566", "443"}).Draw(t, "port"),
			Body:     rapid.SliceOfN(rapid.Byte(), 0, 32).Draw(t, "body"),
		}
	})
}

// An HTTP API's event reports the matched route and stage, the decoded
// route variables and the stage variables; route and stage variables are
// omitted when there are none (apigateway-http.md 5.1).
func TestEventV2ReportsTheRoute(t *testing.T) {
	r := httpevent.Request{
		Method: http.MethodGet, RawPath: "/prod/pets/1", Protocol: "HTTP/1.1", Host: "a1b2c3.execute-api.us-east-1.localhost:4566",
		Header: http.Header{"User-Agent": {"curl/8.0"}}, SourceIP: "127.0.0.1", Scheme: "http", Port: "4566",
	}
	c := httpevent.Context{
		AccountID: "000000000000", APIID: "a1b2c3", DomainName: "a1b2c3.execute-api.us-east-1.localhost", DomainPrefix: "a1b2c3",
		RouteKey: "GET /pets/{id}", Stage: "prod", PathParameters: map[string]string{"id": "1"}, StageVariables: map[string]string{"color": "blue"},
		RequestID: "JKJaXmPLvHcESHA=", At: time.UnixMilli(1583817383220), Authorizer: httpevent.NoAuthorizer{},
	}
	raw, err := httpevent.EventV2(r, c)
	if err != nil {
		t.Fatalf("EventV2: %v", err)
	}
	type event struct {
		Version        string            `json:"version"`
		RouteKey       string            `json:"routeKey"`
		RawPath        string            `json:"rawPath"`
		PathParameters map[string]string `json:"pathParameters"`
		StageVariables map[string]string `json:"stageVariables"`
		RequestContext struct {
			AccountID    string          `json:"accountId"`
			APIID        string          `json:"apiId"`
			DomainName   string          `json:"domainName"`
			DomainPrefix string          `json:"domainPrefix"`
			RouteKey     string          `json:"routeKey"`
			Stage        string          `json:"stage"`
			RequestID    string          `json:"requestId"`
			Authorizer   json.RawMessage `json:"authorizer"`
		} `json:"requestContext"`
	}
	var ev event
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("event %s: %v", raw, err)
	}
	want := event{Version: "2.0", RouteKey: "GET /pets/{id}", RawPath: "/prod/pets/1", PathParameters: map[string]string{"id": "1"}, StageVariables: map[string]string{"color": "blue"}}
	want.RequestContext.AccountID, want.RequestContext.APIID = "000000000000", "a1b2c3"
	want.RequestContext.DomainName, want.RequestContext.DomainPrefix = "a1b2c3.execute-api.us-east-1.localhost", "a1b2c3"
	want.RequestContext.RouteKey, want.RequestContext.Stage, want.RequestContext.RequestID = "GET /pets/{id}", "prod", "JKJaXmPLvHcESHA="
	if !reflect.DeepEqual(ev, want) {
		t.Errorf("event = %+v\nwant    %+v\nfrom %s", ev, want, raw)
	}

	c.PathParameters, c.StageVariables = nil, nil
	raw, err = httpevent.EventV2(r, c)
	if err != nil {
		t.Fatalf("EventV2: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("event %s: %v", raw, err)
	}
	for _, k := range []string{"pathParameters", "stageVariables"} {
		if v, ok := keys[k]; ok {
			t.Errorf("%s = %s, want it omitted when empty", k, v)
		}
	}
}

// An HTTP API's 2.0 event adds content-length, "0" without a body, and
// appends the caller's address to a client's own x-forwarded-for
// (APIGW2-07, recorded 2026-11-03: 01-get and 14-client-xff;
// apigateway-http.md 5.1). A function URL's bodyless GET carries no
// content-length (R13 01-logged-events); what it does with a client's
// x-forwarded-for is unrecorded, so that is not asserted.
func TestEventV2ForAnHTTPAPIAsRecorded(t *testing.T) {
	r := httpevent.Request{
		Method: http.MethodGet, RawPath: "/e", Protocol: "HTTP/1.1", Host: "roj3rnqmie.execute-api.us-east-2.amazonaws.com",
		Header:   http.Header{"User-Agent": {"curl/8.23.0"}, "Accept": {"*/*"}, "X-Forwarded-For": {"203.0.113.3"}},
		SourceIP: "198.51.100.2", Scheme: "https", Port: "443",
	}
	c := httpevent.Context{
		AccountID: "123456789012", APIID: "roj3rnqmie", RouteKey: "ANY /{proxy+}", Stage: "$default",
		RequestID: "GPqjqiQaiYcEJTw=", At: time.UnixMilli(1793677232000), TraceID: "Root=1-6ae957b0-48c100c431cc595356e7aa45",
		Authorizer: httpevent.NoAuthorizer{},
	}
	raw, err := httpevent.EventV2For(r, c, httpevent.HTTPAPI{})
	if err != nil {
		t.Fatalf("EventV2For: %v", err)
	}
	var ev struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("event %s: %v", raw, err)
	}
	want := map[string]string{
		"accept": "*/*", "content-length": "0", "host": "roj3rnqmie.execute-api.us-east-2.amazonaws.com", "user-agent": "curl/8.23.0",
		"x-amzn-trace-id": c.TraceID, "x-forwarded-for": "203.0.113.3, 198.51.100.2", "x-forwarded-port": "443", "x-forwarded-proto": "https",
	}
	if !reflect.DeepEqual(ev.Headers, want) {
		t.Errorf("headers = %v\nwant      %v", ev.Headers, want)
	}
	fromURL, err := httpevent.EventV2For(r, c, httpevent.FunctionURL{})
	if err != nil {
		t.Fatalf("EventV2For: %v", err)
	}
	var urlEv struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(fromURL, &urlEv); err != nil {
		t.Fatalf("event %s: %v", fromURL, err)
	}
	if v, ok := urlEv.Headers["content-length"]; ok {
		t.Errorf("function URL headers content-length = %q, want no key for a bodyless GET", v)
	}
}
