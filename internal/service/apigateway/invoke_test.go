package apigateway_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

func obj(tb testing.TB, m map[string]any, key string) map[string]any {
	tb.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		tb.Fatalf("%s = %#v, want an object", key, m[key])
	}
	return v
}

// The tracer bullet (spec 6b): GET /<stage>/ on a root GET with an
// AWS_PROXY integration reaches the function and answers its response.
// The event has the recorded REST 1.0 shape (aws-recorded,
// test_apigateway_lambda.snapshot.json:L5-L121, apigateway-rest.md 5.1):
// no version key, resource and path, the real verb, null maps for a GET
// with no query, parameters or stage variables, body null,
// isBase64Encoded false, and a requestContext with the owner account, api
// id, deployment id, domain prefix, the stage, the resource id and path,
// requestContext.path with the stage, and no authorizer for NONE. The
// function runs synchronously as apigateway.amazonaws.com with the
// request's source ARN (5.4, switch apigateway.lambda.permission), via the
// API Gateway integration.
func TestTracerBullet(t *testing.T) {
	run(t, func(h *harness) {
		id, dep := h.proxyAPI()
		root := h.rootOf(id)
		rec := h.get(id, "/s/")
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Fatalf("GET /s/ = %d %s, want the function's 200 ok", rec.Code, rec.Body)
		}
		// No Content-Type from the function: application/json (aws-recorded
		// and aws-doc, apigateway-rest.md 4.8).
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		wantRequestIDs(t, rec, "GET /s/")
		call := h.only()
		ev := call.event(h)
		if _, has := ev["version"]; has {
			t.Errorf("event has a version key: %v", ev["version"])
		}
		if ev["resource"] != "/" || ev["path"] != "/" || ev["httpMethod"] != http.MethodGet || ev["isBase64Encoded"] != false {
			t.Errorf("event resource %v, path %v, httpMethod %v, isBase64Encoded %v; want /, /, GET, false", ev["resource"], ev["path"], ev["httpMethod"], ev["isBase64Encoded"])
		}
		for _, k := range []string{"queryStringParameters", "multiValueQueryStringParameters", "pathParameters", "stageVariables", "body"} {
			if v, has := ev[k]; !has || v != nil {
				t.Errorf("event %s = %#v (present %v), want null", k, v, has)
			}
		}
		if _, ok := ev["headers"].(map[string]any); !ok {
			t.Errorf("event headers = %#v, want an object", ev["headers"])
		}
		rc := obj(h, ev, "requestContext")
		want := map[string]any{
			"accountId": account, "apiId": id, "deploymentId": dep, "domainPrefix": id, "stage": "s",
			"resourceId": root, "resourcePath": "/", "httpMethod": http.MethodGet, "path": "/s/",
		}
		for k, v := range want {
			if rc[k] != v {
				t.Errorf("requestContext.%s = %#v, want %#v", k, rc[k], v)
			}
		}
		for _, k := range []string{"requestId", "extendedRequestId", "requestTime", "protocol", "domainName"} {
			if s, _ := rc[k].(string); s == "" {
				t.Errorf("requestContext.%s = %#v, want a value", k, rc[k])
			}
		}
		if _, ok := rc["requestTimeEpoch"].(float64); !ok {
			t.Errorf("requestContext.requestTimeEpoch = %#v, want milliseconds", rc["requestTimeEpoch"])
		}
		if _, has := rc["authorizer"]; has {
			t.Errorf("requestContext.authorizer = %#v, want no key for NONE", rc["authorizer"])
		}
		if identity := obj(h, rc, "identity"); identity["sourceIp"] == nil || identity["caller"] != nil || identity["user"] != nil {
			t.Errorf("requestContext.identity = %v, want sourceIp set and the rest null", identity)
		}
		if it := call.in.InvocationType; it != nil && *it != lambdaapi.InvocationTypeRequestResponse {
			t.Errorf("InvocationType = %v, want a synchronous invocation", *it)
		}
		if !strings.Contains(call.in.FunctionName, "function:fn") {
			t.Errorf("FunctionName = %q, want function fn", call.in.FunctionName)
		}
		source := apigateway.SourceARN(region, account, id, "s", http.MethodGet, "/")
		if as, ok := call.mode.(kernel.AsService); !ok || as.Name != "apigateway.amazonaws.com" || as.Source.String() != source {
			t.Errorf("invoked as %#v, want apigateway.amazonaws.com with source %s", call.mode, source)
		}
		if call.origin != (ref.Internal{Via: ref.ViaAPIGateway}) {
			t.Errorf("origin = %#v, want an internal call via the API Gateway integration", call.origin)
		}
	})
}

// A greedy resource's event (aws-recorded,
// test_apigateway_lambda.snapshot.json, apigateway-rest.md 4.2, 4.5, 5.1):
// path keeps interior slashes and is not decoded, the parameter holds the
// rest, query values are form-decoded with the last value single and all
// values in the multi map, the front's Host and X-Forwarded-* headers
// replace any sent, and an ANY method reports the real verb.
func TestGreedyProxyEvent(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI("/{proxy+}")
		r := h.request(http.MethodGet, pathURL(id, "/s/proxy-value//double-slash?category=toys&category=books&email=test%2Balias@gmail.com&plus=test+alias@gmail.com"), "")
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		r.Header.Add("X-Multi", "a")
		r.Header.Add("X-Multi", "b")
		if rec := h.send(r); rec.Code != http.StatusOK {
			t.Fatalf("GET = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		ev := h.only().event(h)
		if ev["resource"] != "/{proxy+}" || ev["path"] != "/proxy-value//double-slash" || ev["httpMethod"] != http.MethodGet {
			t.Errorf("event resource %v, path %v, httpMethod %v", ev["resource"], ev["path"], ev["httpMethod"])
		}
		if p := obj(h, ev, "pathParameters"); p["proxy"] != "proxy-value//double-slash" {
			t.Errorf("pathParameters = %v, want proxy proxy-value//double-slash", p)
		}
		q := obj(h, ev, "queryStringParameters")
		if q["category"] != "books" || q["email"] != "test+alias@gmail.com" || q["plus"] != "test alias@gmail.com" {
			t.Errorf("queryStringParameters = %v, want the last category and form-decoded values", q)
		}
		if mq := obj(h, ev, "multiValueQueryStringParameters"); !slices.Equal(anyStrings(mq["category"]), []string{"toys", "books"}) {
			t.Errorf("multiValueQueryStringParameters.category = %v, want [toys books]", mq["category"])
		}
		rc := obj(h, ev, "requestContext")
		if rc["resourcePath"] != "/{proxy+}" || rc["path"] != "/s/proxy-value//double-slash" {
			t.Errorf("requestContext resourcePath %v, path %v", rc["resourcePath"], rc["path"])
		}
		headers := obj(h, ev, "headers")
		for _, k := range []string{"Host", "X-Forwarded-For", "X-Forwarded-Port", "X-Forwarded-Proto"} {
			if s, _ := headers[k].(string); s == "" {
				t.Errorf("headers.%s = %#v, want the front's value", k, headers[k])
			}
		}
		if headers["X-Forwarded-For"] == "203.0.113.9" {
			t.Errorf("headers.X-Forwarded-For kept the client's value %v", headers["X-Forwarded-For"])
		}
		if mh := obj(h, ev, "multiValueHeaders"); !slices.Equal(anyStrings(mh["X-Multi"]), []string{"a", "b"}) {
			t.Errorf("multiValueHeaders.X-Multi = %v, want [a b]", mh["X-Multi"])
		}
		for _, k := range []string{"Via", "X-Amz-Cf-Id", "CloudFront-Viewer-Country"} {
			if _, has := headers[k]; has {
				t.Errorf("headers.%s present; an API is served as REGIONAL (spec 6b scope)", k)
			}
		}
		// A trailing slash stays in path but not in the parameter (4.2).
		h.get(id, "/s/proxy-value/")
		calls := h.fn.invocations()
		ev = calls[len(calls)-1].event(h)
		if ev["path"] != "/proxy-value/" || obj(h, ev, "pathParameters")["proxy"] != "proxy-value" {
			t.Errorf("trailing slash: path %v, pathParameters %v", ev["path"], ev["pathParameters"])
		}
	})
}

func anyStrings(v any) []string {
	xs, _ := v.([]any)
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// A POST body arrives as the raw string (aws-recorded, apigateway-rest.md
// 5.1); a bodyless POST's body and isBase64Encoded follow switch
// apigateway.proxy.unrecorded (APIGW1-16).
func TestBodyInTheEvent(t *testing.T) {
	sw := apigateway.ProxyUnrecordedSwitch().Value
	run(t, func(h *harness) {
		id, _ := h.proxyAPI("/items")
		h.send(h.request(http.MethodPost, pathURL(id, "/s/items"), `{"a":1}`))
		h.send(h.request(http.MethodPost, pathURL(id, "/s/items"), ""))
		calls := h.fn.invocations()
		if len(calls) != 2 {
			t.Fatalf("invoked %d times, want 2", len(calls))
		}
		if ev := calls[0].event(h); ev["body"] != `{"a":1}` || ev["httpMethod"] != http.MethodPost {
			t.Errorf("POST body = %#v, method %v; want the raw string", ev["body"], ev["httpMethod"])
		}
		ev := calls[1].event(h)
		if sw.BodylessNull && ev["body"] != nil || !sw.BodylessNull && ev["body"] != "" {
			t.Errorf("bodyless POST body = %#v, want null per BodylessNull %v (switch apigateway.proxy.unrecorded)", ev["body"], sw.BodylessNull)
		}
		if ev["isBase64Encoded"] != sw.BodylessBase64 {
			t.Errorf("bodyless POST isBase64Encoded = %v, want %v (switch apigateway.proxy.unrecorded)", ev["isBase64Encoded"], sw.BodylessBase64)
		}
	})
}

// Response mapping, the recorded outcomes (aws-recorded,
// test_apigateway_lambda.snapshot.json:L1669-L1797,
// test_apigateway_lambda.py:L1113-L1186; apigateway-rest.md 5.2).
func TestResponseMapping(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI()
		for _, c := range []struct {
			name, out string
			status    int
			body      string
			header    map[string]string
		}{
			{"status only", `{"statusCode": 200}`, 200, "", map[string]string{"Content-Type": "application/json"}},
			{"non-string header", `{"statusCode": 200, "headers": {"test-header": "value", "header-bool": true}}`, 200, "", map[string]string{"Test-Header": "value", "Header-Bool": "true"}},
			{"null headers", `{"statusCode": 200, "headers": null}`, 200, "", nil},
			{"base64 not decoded", `{"statusCode": 200, "body": "dGVzdC1kYXRh", "isBase64Encoded": true}`, 200, "dGVzdC1kYXRh", nil},
			{"multi", `{"statusCode": 200, "multiValueHeaders": {"test-multi": ["value1", "value2"]}}`, 200, "", map[string]string{"Test-Multi": "value1, value2"}},
			{"merge", `{"statusCode": 200, "multiValueHeaders": {"test-multi": ["value-multi"]}, "headers": {"test-multi": "value-solo"}}`, 200, "", map[string]string{"Test-Multi": "value-multi, value-solo"}},
			{"merge across case", `{"statusCode": 200, "multiValueHeaders": {"tesT-Multi": ["value-multi"]}, "headers": {"test-multi": "value-solo"}}`, 200, "", map[string]string{"Test-Multi": "value-multi, value-solo"}},
			{"string status", `{"statusCode": "201"}`, 201, "", nil},
			{"no status", `{"headers": {"test-header": "value"}}`, 200, "", map[string]string{"Test-Header": "value"}},
			{"body", `{"statusCode": 200, "body": "hello"}`, 200, "hello", nil},
		} {
			h.fn.answer(c.out)
			rec := h.get(id, "/s/")
			if rec.Code != c.status || rec.Body.String() != c.body {
				t.Errorf("%s: response %d %q, want %d %q", c.name, rec.Code, rec.Body, c.status, c.body)
			}
			for k, v := range c.header {
				if got := strings.Join(rec.Header().Values(k), ", "); got != v {
					t.Errorf("%s: header %s = %q, want %q", c.name, k, got, v)
				}
			}
		}
		for name, out := range map[string]string{
			"an extra key":         `{"statusCode": 200, "wrongValue": "value"}`,
			"multi value not list": `{"statusCode": 200, "multiValueHeaders": {"test-multi-invalid": "value1"}}`,
			"a non-numeric status": `{"statusCode": "test"}`,
			"a bare string":        `"justAString"`,
		} {
			h.fn.answer(out)
			wantAnswer(t, h.get(id, "/s/"), apigateway.ProxyFailure(), name)
		}
	})
}

// A function error, a Lambda error other than NotImplemented, answers the
// proxy failure 502 (aws-recorded, apigateway-rest.md 5.3); Lambda's
// NotImplemented (Docker unavailable) answers 501 naming Docker (the
// unit's conflict row 3).
func TestFunctionFailures(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI()
		h.fn.answerWith(&lambdaapi.InvokeOutput{StatusCode: 200, FunctionError: new("Unhandled"), Payload: []byte(`{"errorMessage":"boom"}`)}, nil)
		wantAnswer(t, h.get(id, "/s/"), apigateway.ProxyFailure(), "a function error")
		h.fn.answerWith(nil, lambdaapi.ErrServiceException("failed"))
		wantAnswer(t, h.get(id, "/s/"), apigateway.ProxyFailure(), "a Lambda error")
		h.fn.answerWith(nil, lambdaapi.ErrNotImplemented("running functions needs Docker, which is not available"))
		want501(t, h.get(id, "/s/"), "Docker", "Docker unavailable")
		h.wantJournaled501("Docker unavailable")
	})
}

// An AWS_PROXY integration with integration method GET answers the
// configuration error 500 (aws-recorded, test_apigateway_lambda.py:L455,
// apigateway-rest.md finding 10), without invoking.
func TestIntegrationMethodGETIsAConfigurationError(t *testing.T) {
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id, root := h.api("t")
		h.method(id, root, "GET")
		in := proxyInput(id, root, "GET", fnARN)
		in.IntegrationHttpMethod = new("GET")
		must(h, apigatewayapi.OpPutIntegration, in)
		h.allow("fn", "apigateway", h.executionARN(id)+"/*/*")
		h.deploy(id, "s")
		wantAnswer(t, h.get(id, "/s/"), apigateway.ConfigurationError(), "integration method GET")
		h.notInvoked("integration method GET")
	})
}

// Without a function-policy statement for apigateway.amazonaws.com and the
// request's source ARN, the invoke answers switch
// apigateway.lambda.permission's Denial and the function never runs,
// under every iam.enforce mode (spec 6b enforce matrix; conflict row 2;
// apigateway-rest.md 5.4). A statement whose source ARN names the method
// and resource template exactly lets it through.
func TestMissingPermission(t *testing.T) {
	denial := apigateway.PermissionSwitch().Value.Denial
	for name, mode := range modes {
		t.Run(name, func(t *testing.T) {
			runMode(t, mode, func(h *harness) {
				fnARN := h.createFunction("fn")
				id, root := h.api("t")
				items := h.resource(id, root, "items")
				item := h.resource(id, items, "{id}")
				h.proxy(id, item, "GET", fnARN)
				h.proxy(id, items, "GET", fnARN)
				h.deploy(id, "s")
				wantAnswer(t, h.get(id, "/s/items/1"), denial, "no permission")
				h.notInvoked("no permission")
				h.allow("fn", "item", apigateway.SourceARN(region, account, id, "s", "GET", "/items/{id}"))
				if rec := h.get(id, "/s/items/1"); rec.Code != http.StatusOK {
					t.Errorf("GET /s/items/1 with a permission for its source ARN = %d %s, want 200", rec.Code, rec.Body)
				}
				wantAnswer(t, h.get(id, "/s/items"), denial, "a permission for another resource only")
				if n := len(h.fn.invocations()); n != 1 {
					t.Errorf("the function ran %d times, want once", n)
				}
			})
		})
	}
}

// Stage and route selection (apigateway-rest.md 4.1, 4.3, 4.7): an unknown
// API id and an unknown stage answer switch apigateway.invoke.routing's
// UnknownAPI and UnknownStage (APIGW1-07), a path with no stage the
// latter; an unmatched resource or method answers 403 Missing
// Authentication Token (aws-recorded, finding 3). None invokes.
func TestRouting(t *testing.T) {
	routing := apigateway.RoutingSwitch().Value
	run(t, func(h *harness) {
		id, _ := h.proxyAPI("/items")
		wantAnswer(t, h.get("zzzzzzzzzz", "/s/"), routing.UnknownAPI, "an unknown API id")
		wantAnswer(t, h.get(id, "/nosuch/"), routing.UnknownStage, "an unknown stage")
		wantAnswer(t, h.get(id, "/"), routing.UnknownStage, "no stage")
		wantAnswer(t, h.get(id, "/s/nosuch"), apigateway.MissingAuthenticationToken(), "an unknown resource")
		wantAnswer(t, h.send(h.request(http.MethodPost, pathURL(id, "/s/"), "x")), apigateway.MissingAuthenticationToken(), "a method the root lacks")
		h.notInvoked("unrouted requests")
		if rec := h.send(h.request(http.MethodDelete, pathURL(id, "/s/items"), "")); rec.Code != http.StatusOK {
			t.Errorf("DELETE /s/items on ANY = %d %s, want the function's 200 (ANY covers DELETE, 4.4)", rec.Code, rec.Body)
		}
	})
}

// The execute-api host form serves the same API (contract 6;
// apigateway-rest.md 4.1: {api-id}.execute-api.{region}.<base>/{stage}/);
// a host naming another region answers switch apigateway.invoke.routing's
// UnknownAPI.
func TestHostForm(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI()
		r := h.request(http.MethodGet, "http://localhost:4566/s/", "")
		r.Host = id + ".execute-api." + region + ".localhost:4566"
		if rec := h.send(r); rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Errorf("host form = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		r = h.request(http.MethodGet, "http://localhost:4566/s/", "")
		r.Host = id + ".execute-api.eu-west-1.localhost:4566"
		wantAnswer(t, h.send(r), apigateway.RoutingSwitch().Value.UnknownAPI, "a host in another region")
	})
}

// A body over 10485760 bytes answers REQUEST_TOO_LARGE 413 before any
// lookup, an unknown API included (aws-doc, apigateway-rest.md 2.6, 4.7;
// contract 7.7 step 1); an event over Lambda's 6 MB limit is refused
// without invoking (status only: inferred).
func TestSizeLimits(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI("/upload")
		huge := strings.Repeat("x", apigateway.MaxBody+1)
		for _, target := range []string{"zzzzzzzzzz", id} {
			rec := h.send(h.request(http.MethodPost, pathURL(target, "/s/upload"), huge))
			if want := apigateway.RequestTooLarge(); rec.Code != want.Status || rec.Body.String() != want.Body {
				t.Errorf("a body over MaxBody to %s = %d %s, want %d %s", target, rec.Code, rec.Body, want.Status, want.Body)
			}
		}
		rec := h.send(h.request(http.MethodPost, pathURL(id, "/s/upload"), strings.Repeat("x", 6_291_457)))
		if rec.Code != apigateway.EventTooLarge().Status {
			t.Errorf("an event over 6 MB = %d, want %d", rec.Code, apigateway.EventTooLarge().Status)
		}
		h.notInvoked("oversized requests")
	})
}

// An invocation past the integration's timeoutInMillis answers switch
// apigateway.proxy.unrecorded's Timeout (APIGW1-10).
func TestIntegrationTimeout(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI()
		must(h, apigatewayapi.OpUpdateIntegration, &apigatewayapi.UpdateIntegrationInput{RestApiId: id, ResourceId: h.rootOf(id), HttpMethod: "GET", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/timeoutInMillis", "50"),
		}})
		h.deploy(id, "s")
		h.fn.hang()
		wantAnswer(t, h.get(id, "/s/"), apigateway.ProxyUnrecordedSwitch().Value.Timeout, "an invocation past its timeout")
		h.fn.releaseAll()
	})
}

// The InvokeRestApi call is journaled, and the function's invocation is
// its child, an internal call as apigateway.amazonaws.com (ARCHITECTURE.md
// 9; apigateway-rest.md 5.4).
func TestInvocationIsJournaledAsTheCallsChild(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI()
		h.get(id, "/s/")
		var front, invoke *journal.Event
		events := h.Journal.Query(journal.Filter{Kind: journal.KindAPICall, Limit: 1000}).Events
		for i := range events {
			call, ok := events[i].Detail.(journal.APICall)
			if !ok {
				continue
			}
			switch call.Op {
			case invokeRestAPI:
				front = &events[i]
			case lambdaapi.OpInvoke.Descriptor().ID:
				invoke = &events[i]
			}
		}
		if front == nil || invoke == nil {
			t.Fatalf("journal holds InvokeRestApi %v and Invoke %v, want both", front, invoke)
		}
		if invoke.Cause.Parent != front.ID {
			t.Errorf("Invoke's parent = %v, want the InvokeRestApi call %v", invoke.Cause.Parent, front.ID)
		}
		if call, ok := invoke.Detail.(journal.APICall); !ok || call.Mode != "service:apigateway.amazonaws.com" || call.Origin != (ref.Internal{Via: ref.ViaAPIGateway}) {
			t.Errorf("Invoke mode %q, origin %#v; want service:apigateway.amazonaws.com via the API Gateway integration", call.Mode, call.Origin)
		}
	})
}
