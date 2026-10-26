package httpevent_test

import (
	"bytes"
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

func genCaller() *rapid.Generator[furl.Caller] {
	return rapid.OneOf(
		rapid.Just[furl.Caller](furl.Public{}),
		rapid.Custom(func(t *rapid.T) furl.Caller {
			return furl.IAMCaller{
				AccessKey: rapid.StringMatching(`AKIA[A-Z0-9]{16}`).Draw(t, "access key"),
				AccountID: rapid.StringMatching(`[0-9]{12}`).Draw(t, "account"),
				ID:        rapid.StringMatching(`AIDA[A-Z0-9]{8}`).Draw(t, "id"),
				UserARN:   "arn:aws:iam::111122223333:user/example-user",
			}
		}),
	)
}

// functionURLContext is the context a function URL's event has (lambda-events.md
// 7.3): routeKey and stage are the placeholder $default, apiId and
// domainPrefix the url id, accountId "anonymous" for AuthType NONE and the
// signer's account under AWS_IAM, which the authorizer's iam member reports.
func functionURLContext(c furl.Context) httpevent.Context {
	hc := httpevent.Context{
		AccountID: "anonymous", APIID: c.URLID, DomainName: c.DomainName, DomainPrefix: c.URLID,
		RouteKey: "$default", Stage: "$default", RequestID: c.RequestID, At: c.At, TraceID: c.TraceID,
		Authorizer: httpevent.NoAuthorizer{},
	}
	if iam, ok := c.Caller.(furl.IAMCaller); ok {
		hc.AccountID = iam.AccountID
		hc.Authorizer = httpevent.IAMAuthorizer{AccessKey: iam.AccessKey, AccountID: iam.AccountID, CallerID: iam.ID, UserARN: iam.UserARN, UserID: iam.ID}
	}
	return hc
}

// The shared builder writes, for a function URL's context, exactly the
// event a function URL sends today, whose furl tests are pinned to AWS's
// (lambda-events.md 7.3): function URLs and HTTP APIs share payload format
// 2.0 (apigateway-http.md 5.1).
func TestEventV2MatchesFunctionURLEvents(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := genRequest().Draw(t, "request")
		c := furl.Context{
			URLID: "abc123", DomainName: "abc123.lambda-url.us-east-1.localhost",
			RequestID: rapid.StringMatching(`[0-9a-f]{8}`).Draw(t, "request id"),
			At:        time.UnixMilli(rapid.Int64Range(0, 4102444800000).Draw(t, "at")),
			Caller:    genCaller().Draw(t, "caller"),
		}
		if rapid.Bool().Draw(t, "traced") {
			c.TraceID = "Root=1-5e5fe6fe-0123456789abcdef01234567"
		}
		want, werr := furl.Event(r, c)
		got, gerr := httpevent.EventV2(httpevent.Request(r), functionURLContext(c))
		if (werr == nil) != (gerr == nil) || !bytes.Equal(got, want) {
			t.Fatalf("EventV2 = %s, %v\nfurl.Event = %s, %v", got, gerr, want, werr)
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

// The shared response inference answers exactly as a function URL's does
// today (lambda-events.md 7.4), errors included.
func TestInferV2MatchesFunctionURLResponses(t *testing.T) {
	payload := rapid.OneOf(
		rapid.SliceOfN(rapid.Byte(), 0, 32),
		rapid.Map(rapid.StringMatching(`"[ -~]{0,12}"`), func(s string) []byte { return []byte(s) }),
		rapid.Custom(func(t *rapid.T) []byte {
			fields := map[string]any{}
			if rapid.Bool().Draw(t, "has status") {
				fields["statusCode"] = rapid.SampledFrom([]any{200, 201, 404, 599, 600, 99, "200", "teapot", 2.5}).Draw(t, "status")
			}
			if rapid.Bool().Draw(t, "has headers") {
				fields["headers"] = rapid.SampledFrom([]any{nil, map[string]any{"content-type": "text/html", "X-One": "1"}, map[string]any{"x": 1}}).Draw(t, "headers")
			}
			if rapid.Bool().Draw(t, "has body") {
				fields["body"] = rapid.SampledFrom([]any{"hello", "aGVsbG8=", 7, nil}).Draw(t, "body")
			}
			if rapid.Bool().Draw(t, "has base64") {
				fields["isBase64Encoded"] = rapid.SampledFrom([]any{true, false, "true"}).Draw(t, "base64")
			}
			if rapid.Bool().Draw(t, "has cookies") {
				fields["cookies"] = rapid.SampledFrom([]any{nil, []any{"a=1", "b=2"}, []any{1}}).Draw(t, "cookies")
			}
			b, err := json.Marshal(fields)
			if err != nil {
				t.Fatalf("marshal %v: %v", fields, err)
			}
			return b
		}),
	)
	rapid.Check(t, func(t *rapid.T) {
		p := payload.Draw(t, "payload")
		want, werr := furl.Infer(p)
		got, gerr := httpevent.InferV2(p)
		if (werr == nil) != (gerr == nil) || (werr != nil && werr.Error() != gerr.Error()) {
			t.Fatalf("InferV2(%s) error = %v, furl.Infer error = %v", p, gerr, werr)
		}
		if werr != nil {
			return
		}
		if got.Status != want.Status || !reflect.DeepEqual(got.Header, want.Header) || !reflect.DeepEqual(got.Cookies, want.Cookies) || !bytes.Equal(got.Body, want.Body) {
			t.Fatalf("InferV2(%s) = %+v, furl.Infer = %+v", p, got, want)
		}
	})
}
