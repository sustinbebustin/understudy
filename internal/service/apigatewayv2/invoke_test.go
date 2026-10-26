package apigatewayv2_test

import (
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
)

// invokeAPI is the raw data-plane operation (the unit's rawOperations
// entry).
var invokeAPI = ref.OperationID{Service: "apigatewayv2", Name: "InvokeApi"}

// obj reads the JSON object at key of an event.
func obj(tb testing.TB, ev map[string]any, key string) map[string]any {
	tb.Helper()
	m, ok := ev[key].(map[string]any)
	if !ok {
		tb.Fatalf("event %s = %#v, want an object", key, ev[key])
	}
	return m
}

// strs reads a JSON object of strings.
func strs(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, x := range m {
		if s, ok := x.(string); ok {
			out[k] = s
		}
	}
	return out
}

// The tracer bullet: a GET through the path fallback to a Lambda proxy
// route of the $default stage reaches the function with a payload 2.0
// event, and the function's structured response is the client's: its
// status, headers, body and each cookie as a set-cookie header
// (apigateway-http.md 5.1, 5.3). The answer carries apigw-requestid
// (finding 12) and, on the path form, Content-Security-Policy: sandbox
// (ADR 0020). API Gateway invokes synchronously as apigateway.amazonaws.com
// with the request's execute-api source ARN (5.5).
func TestTracerBullet(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		h.fn.answer(`{"statusCode":201,"headers":{"content-type":"text/plain","x-custom":"v"},"body":"hi","cookies":["a=1","b=2"]}`)
		rec := h.get(id, "/hello")
		if rec.Code != http.StatusCreated || rec.Body.String() != "hi" || rec.Header().Get("X-Custom") != "v" || rec.Header().Get("Content-Type") != "text/plain" {
			t.Errorf("response %d %v %q, want 201 text/plain x-custom: v, hi", rec.Code, rec.Header(), rec.Body)
		}
		if got := rec.Header().Values("Set-Cookie"); !slices.Equal(got, []string{"a=1", "b=2"}) {
			t.Errorf("Set-Cookie = %q, want one per cookie", got)
		}
		if rec.Header().Get("Apigw-Requestid") == "" {
			t.Error("no apigw-requestid header")
		}
		if got := rec.Header().Values("Content-Security-Policy"); !slices.Equal(got, []string{"sandbox"}) {
			t.Errorf("Content-Security-Policy = %q, want sandbox on the path form", got)
		}
		call := h.only()
		ev := call.event(h)
		if ev["version"] != "2.0" || ev["routeKey"] != "GET /hello" || ev["rawPath"] != "/hello" {
			t.Errorf("event version %v, routeKey %v, rawPath %v; want 2.0, GET /hello, /hello", ev["version"], ev["routeKey"], ev["rawPath"])
		}
		rc := obj(h, ev, "requestContext")
		if rc["accountId"] != account || rc["apiId"] != id || rc["stage"] != "$default" || rc["routeKey"] != "GET /hello" {
			t.Errorf("requestContext = %v, want account %s, api %s, stage $default, route GET /hello", rc, account, id)
		}
		if httpCtx := obj(h, rc, "http"); httpCtx["method"] != http.MethodGet || httpCtx["path"] != "/hello" {
			t.Errorf("requestContext.http = %v, want GET /hello", httpCtx)
		}
		if it := call.in.InvocationType; it != nil && *it != lambdaapi.InvocationTypeRequestResponse {
			t.Errorf("InvocationType = %v, want a synchronous invocation", *it)
		}
		if !strings.Contains(call.in.FunctionName, "function:fn") {
			t.Errorf("FunctionName = %q, want function fn", call.in.FunctionName)
		}
		as, ok := call.mode.(kernel.AsService)
		if !ok || as.Name != "apigateway.amazonaws.com" || !strings.HasPrefix(as.Source.String(), h.executionARN(id)+"/") {
			t.Errorf("invoked as %#v, want apigateway.amazonaws.com with a source ARN under %s/", call.mode, h.executionARN(id))
		}
		if call.origin != (ref.Internal{Via: ref.ViaAPIGateway}) {
			t.Errorf("origin = %#v, want an internal call via the API Gateway integration", call.origin)
		}
	})
}

// A Lambda integration's URI may be the function ARN or API Gateway's
// invoke URI wrapping it (Terraform's invoke_arn, apigateway-http.md 3.3,
// 9.7): either way the named function runs, and its own resource policy is
// the one checked (5.5).
func TestInvokeURINamesTheFunction(t *testing.T) {
	run(t, func(h *harness) {
		h.createFunction("fn")
		other := h.createFunction("other")
		id := h.apiID("t")
		integration := deref(must(h, apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{
			ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationUri: new(invokeURI(other)), PayloadFormatVersion: new("2.0"),
		}).IntegrationId)
		h.createRoute(id, "GET /other", integration)
		h.autoStage(id)
		h.allow("fn", "apigateway", h.executionARN(id)+"/*/*")
		wantAnswer(t, h.get(id, "/other"), apigatewayv2.PermissionSwitch().Value.Denial, "a permission on another function only")
		h.notInvoked("a permission on another function only")
		h.allow("other", "apigateway", h.executionARN(id)+"/*/*")
		if rec := h.get(id, "/other"); rec.Code != http.StatusOK {
			t.Fatalf("GET /other = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		if name := h.only().in.FunctionName; !strings.Contains(name, "function:other") {
			t.Errorf("FunctionName = %q, want function other", name)
		}
	})
}

// The InvokeApi call is journaled, and the function's invocation is its
// child, an internal call as apigateway.amazonaws.com (ARCHITECTURE.md 9;
// apigateway-http.md 5.5).
func TestInvocationIsJournaledAsTheCallsChild(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		h.get(id, "/hello")
		var front, invoke *journal.Event
		events := h.Journal.Query(journal.Filter{Kind: journal.KindAPICall, Limit: 1000}).Events
		for i := range events {
			call, ok := events[i].Detail.(journal.APICall)
			if !ok {
				continue
			}
			switch call.Op {
			case invokeAPI:
				front = &events[i]
			case lambdaapi.OpInvoke.Descriptor().ID:
				invoke = &events[i]
			}
		}
		if front == nil || invoke == nil {
			t.Fatalf("journal holds InvokeApi %v and Invoke %v, want both", front, invoke)
		}
		if invoke.Cause.Parent != front.ID {
			t.Errorf("Invoke's parent = %v, want the InvokeApi call %v", invoke.Cause.Parent, front.ID)
		}
		if call, ok := invoke.Detail.(journal.APICall); !ok || call.Mode != "service:apigateway.amazonaws.com" || call.Origin != (ref.Internal{Via: ref.ViaAPIGateway}) {
			t.Errorf("Invoke mode %q, origin %#v; want service:apigateway.amazonaws.com via apigateway-integration", call.Mode, call.Origin)
		}
	})
}

// The 2.0 event's request members (apigateway-http.md 5.1, the guide's
// example): rawQueryString as sent, repeated query values and headers
// joined with commas, header names lowercased, cookies out of the
// headers, a text body as sent with isBase64Encoded false.
// requestContext.requestId equals the apigw-requestid header as the
// apigatewayv2.payload.unrecorded switch says (UNVERIFIED, APIGW2-07).
func TestEventV2Request(t *testing.T) {
	choices := apigatewayv2.PayloadSwitch().Value
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "POST /hello")
		r := h.request(http.MethodPost, pathURL(id, "/hello?a=1&a=2"), "payload")
		r.Header.Add("X-Multi", "1")
		r.Header.Add("X-Multi", "2")
		r.Header.Set("Content-Type", "text/plain")
		r.Header.Set("Cookie", "c1=v1")
		rec := h.send(r)
		ev := h.only().event(h)
		if ev["rawQueryString"] != "a=1&a=2" || strs(ev["queryStringParameters"])["a"] != "1,2" {
			t.Errorf("rawQueryString %v, queryStringParameters %v; want a=1&a=2 and a: 1,2", ev["rawQueryString"], ev["queryStringParameters"])
		}
		headers := strs(ev["headers"])
		if headers["x-multi"] != "1,2" || headers["content-type"] != "text/plain" {
			t.Errorf("headers = %v, want x-multi: 1,2 and content-type: text/plain", headers)
		}
		if _, ok := headers["cookie"]; ok {
			t.Errorf("headers carry cookie %q, want it in cookies", headers["cookie"])
		}
		if cookies, ok := ev["cookies"].([]any); !ok || !slices.Contains(cookies, any("c1=v1")) {
			t.Errorf("cookies = %v, want c1=v1", ev["cookies"])
		}
		if ev["body"] != "payload" || ev["isBase64Encoded"] != false {
			t.Errorf("body %v, isBase64Encoded %v; want the text body and false", ev["body"], ev["isBase64Encoded"])
		}
		requestID := obj(h, ev, "requestContext")["requestId"]
		if header := rec.Header().Get("Apigw-Requestid"); choices.RequestIDIsHeader != (requestID == header) {
			t.Errorf("requestContext.requestId %v, apigw-requestid %q; RequestIDIsHeader is %v", requestID, header, choices.RequestIDIsHeader)
		}
	})
}

// A request without a body has no body member (AWS's sample,
// apigateway-http.md 5.1), and its isBase64Encoded is the
// apigatewayv2.payload.unrecorded switch's BodylessBase64: AWS's sample
// and aws-lambda-go's fixture disagree (12 item 3, APIGW2-07).
func TestEventV2BodylessRequest(t *testing.T) {
	choices := apigatewayv2.PayloadSwitch().Value
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		h.get(id, "/hello")
		ev := h.only().event(h)
		if _, ok := ev["body"]; ok {
			t.Errorf("body = %v, want no body member", ev["body"])
		}
		if ev["isBase64Encoded"] != choices.BodylessBase64 {
			t.Errorf("isBase64Encoded = %v, want BodylessBase64 %v", ev["isBase64Encoded"], choices.BodylessBase64)
		}
	})
}

// Route variables reach the function decoded ("API Gateway decodes
// URL-encoded request parameters", apigateway-http.md 4.4), a greedy
// variable under its name with the rest of the path, and a route without
// variables has no pathParameters member (5.1).
func TestEventV2PathParameters(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /pets/{id}", "GET /files/{proxy+}", "GET /plain")
		h.get(id, "/pets/rex%20dog")
		h.get(id, "/files/a/b.txt")
		h.get(id, "/plain")
		calls := h.fn.invocations()
		if len(calls) != 3 {
			t.Fatalf("%d invocations, want 3", len(calls))
		}
		if got := strs(calls[0].event(h)["pathParameters"]); !maps.Equal(got, map[string]string{"id": "rex dog"}) {
			t.Errorf("GET /pets/rex%%20dog pathParameters = %v, want id: rex dog", got)
		}
		if got := strs(calls[1].event(h)["pathParameters"]); !maps.Equal(got, map[string]string{"proxy": "a/b.txt"}) {
			t.Errorf("GET /files/a/b.txt pathParameters = %v, want proxy: a/b.txt", got)
		}
		if v, ok := calls[2].event(h)["pathParameters"]; ok {
			t.Errorf("GET /plain pathParameters = %v, want none", v)
		}
	})
}

// A named stage is selected by the first path segment and serves its own
// deployment; the function sees the stage segment in rawPath but not in
// routeKey (AWS's sample, apigateway-http.md finding 8), the stage name in
// requestContext.stage and the stage's variables (5.1). Without a
// $default stage, a path that names no stage is Not Found (4.2), the
// switch's NoStage answer.
func TestNamedStage(t *testing.T) {
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id := h.apiID("t")
		h.createRoute(id, "GET /hello", h.lambdaIntegration(id, fnARN, "2.0"))
		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "prod", StageVariables: map[string]string{"color": "blue"}})
		must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, StageName: new("prod")})
		h.allow("fn", "apigateway", h.executionARN(id)+"/*/*")
		if rec := h.get(id, "/prod/hello"); rec.Code != http.StatusOK {
			t.Errorf("GET /prod/hello = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		ev := h.only().event(h)
		if ev["routeKey"] != "GET /hello" || ev["rawPath"] != "/prod/hello" || obj(h, ev, "requestContext")["stage"] != "prod" {
			t.Errorf("routeKey %v, rawPath %v, stage %v; want GET /hello, /prod/hello, prod", ev["routeKey"], ev["rawPath"], obj(h, ev, "requestContext")["stage"])
		}
		if got := strs(ev["stageVariables"]); !maps.Equal(got, map[string]string{"color": "blue"}) {
			t.Errorf("stageVariables = %v, want color: blue", got)
		}
		wantAnswer(t, h.get(id, "/hello"), apigatewayv2.FrontAnswersSwitch().Value.NoStage, "GET /hello with no $default stage")
		if n := len(h.fn.invocations()); n != 1 {
			t.Errorf("%d invocations, want only the staged one", n)
		}
	})
}

// "If the request explicitly matches a stage, API Gateway sends the request
// to that stage" (the routes page, apigateway-http.md 4.2); whether that
// shadows a $default route starting with the stage's name is UNVERIFIED
// (APIGW2-06), so which stage /prod/x reaches follows the switch's
// StageSegmentWins.
func TestStageSegmentFollowsTheSwitch(t *testing.T) {
	wins := apigatewayv2.FrontAnswersSwitch().Value.StageSegmentWins
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /prod/x", "GET /x")
		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "prod"})
		must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, StageName: new("prod")})
		h.get(id, "/prod/x")
		ev := h.only().event(h)
		stage, key := obj(h, ev, "requestContext")["stage"], ev["routeKey"]
		switch {
		case wins && (stage != "prod" || key != "GET /x"):
			t.Errorf("stage %v, routeKey %v; StageSegmentWins wants prod and GET /x", stage, key)
		case !wins && (stage != "$default" || key != "GET /prod/x"):
			t.Errorf("stage %v, routeKey %v; without StageSegmentWins want $default and GET /prod/x", stage, key)
		}
	})
}

// A payload format 1.0 integration gets the 1.0 event (apigateway-http.md
// 5.2) and answers with the documented 1.0 response format (5.4); which
// repeated header and query value the single-value maps keep is the
// apigatewayv2.payload.unrecorded switch's V1Header and V1Query
// (UNVERIFIED, APIGW2-08), and a response without Content-Type gets the
// switch's DefaultContentType (APIGW2-09).
func TestPayloadV1(t *testing.T) {
	choices := apigatewayv2.PayloadSwitch().Value
	pick := func(p apigatewayv2.Pick, first, last string) string {
		if p == apigatewayv2.First {
			return first
		}
		return last
	}
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("1.0", "GET /one")
		h.fn.answer(`{"statusCode":202,"headers":{"x-one":"1"},"multiValueHeaders":{"x-many":["a","b"]},"body":"one","isBase64Encoded":false}`)
		r := h.request(http.MethodGet, pathURL(id, "/one?q=1&q=2"), "")
		r.Header.Add("X-H", "first")
		r.Header.Add("X-H", "last")
		rec := h.send(r)
		if rec.Code != http.StatusAccepted || rec.Body.String() != "one" || rec.Header().Get("X-One") != "1" || !slices.Equal(rec.Header().Values("X-Many"), []string{"a", "b"}) {
			t.Errorf("response %d %v %q, want 202 x-one: 1, x-many: a and b, one", rec.Code, rec.Header(), rec.Body)
		}
		if ct := rec.Header().Get("Content-Type"); ct != choices.DefaultContentType {
			t.Errorf("Content-Type = %q, want DefaultContentType %q", ct, choices.DefaultContentType)
		}
		ev := h.only().event(h)
		if ev["version"] != "1.0" || ev["httpMethod"] != http.MethodGet {
			t.Errorf("version %v, httpMethod %v; want 1.0 and GET", ev["version"], ev["httpMethod"])
		}
		if got := strs(ev["queryStringParameters"])["q"]; got != pick(choices.V1Query, "1", "2") {
			t.Errorf("queryStringParameters.q = %q, want V1Query %v of 1, 2", got, choices.V1Query)
		}
		if got := strs(ev["headers"])["x-h"]; got != pick(choices.V1Header, "first", "last") {
			t.Errorf("headers.x-h = %q, want V1Header %v of first, last", got, choices.V1Header)
		}
		if mv, ok := obj(h, ev, "multiValueQueryStringParameters")["q"].([]any); !ok || !slices.Equal(mv, []any{"1", "2"}) {
			t.Errorf("multiValueQueryStringParameters.q = %v, want [1 2]", mv)
		}
		if rc := obj(h, ev, "requestContext"); rc["accountId"] != account || rc["apiId"] != id || rc["stage"] != "$default" {
			t.Errorf("requestContext = %v, want account %s, api %s, stage $default", rc, account, id)
		}
	})
}

// How a 2.0 response is read (apigateway-http.md 5.3): output without a
// statusCode is a 200 application/json body, a bare string the string
// itself (the unit's conflict row 6); a structured response without a
// Content-Type gets the apigatewayv2.payload.unrecorded switch's
// DefaultContentType (UNVERIFIED, APIGW2-09).
func TestResponseV2(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		h.fn.answer(`"Hello from Lambda!"`)
		if rec := h.get(id, "/hello"); rec.Code != http.StatusOK || rec.Body.String() != "Hello from Lambda!" || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("bare string = %d %q %q, want 200 application/json Hello from Lambda!", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
		}
		h.fn.answer(`{"message":"hi"}`)
		if rec := h.get(id, "/hello"); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("object without statusCode = %d %q, want 200 application/json", rec.Code, rec.Header().Get("Content-Type"))
		}
		h.fn.answer(`{"statusCode":418,"body":"teapot"}`)
		rec := h.get(id, "/hello")
		if rec.Code != http.StatusTeapot || rec.Body.String() != "teapot" || rec.Header().Get("Content-Type") != apigatewayv2.PayloadSwitch().Value.DefaultContentType {
			t.Errorf("structured response = %d %q %q, want 418 teapot with DefaultContentType %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body, apigatewayv2.PayloadSwitch().Value.DefaultContentType)
		}
	})
}

// The answers API Gateway gives before any integration runs, each the
// apigatewayv2.front.answers or route.matching switch's (apigateway-http.md
// 4.5: Not Found for no stage and no route is documented, the rest
// UNVERIFIED, APIGW2-05, APIGW2-06): an unknown API id, no route, no
// stage, a stage without a deployment, a route without a target.
func TestGatewayAnswers(t *testing.T) {
	front := apigatewayv2.FrontAnswersSwitch().Value
	run(t, func(h *harness) {
		wantAnswer(t, h.get("zzzzzzzzzz", "/hello"), front.UnknownAPI, "unknown API id")
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		h.createRoute(id, "GET /bare", "")
		wantAnswer(t, h.get(id, "/nothing"), front.NoRoute, "no route")
		wantAnswer(t, h.get(id, "/bare"), apigatewayv2.RouteMatchingSwitch().Value.NoTarget, "a route without a target")

		noStage := h.apiID("no-stage")
		h.createRoute(noStage, "GET /hello", "")
		wantAnswer(t, h.get(noStage, "/hello"), front.NoStage, "no stage")

		undeployed := h.apiID("undeployed")
		h.createRoute(undeployed, "GET /hello", "")
		must(h, apigatewayv2api.OpCreateStage, stageInput(undeployed, "$default"))
		wantAnswer(t, h.get(undeployed, "/hello"), front.NoDeployment, "a stage without a deployment")
		h.notInvoked("gateway answers")
	})
}

// disableExecuteApiEndpoint stops the default endpoint answering (the
// model's documentation, apigateway-http.md 3.1); its answer and when it
// takes effect are the switch's DisabledEndpoint (UNVERIFIED, APIGW2-06),
// here at once, and re-enabling serves again.
func TestDisabledEndpoint(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		must(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{ApiId: id, DisableExecuteApiEndpoint: new(true)})
		wantAnswer(t, h.get(id, "/hello"), apigatewayv2.FrontAnswersSwitch().Value.DisabledEndpoint, "disabled endpoint")
		h.notInvoked("a disabled endpoint")
		must(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{ApiId: id, DisableExecuteApiEndpoint: new(false)})
		if rec := h.get(id, "/hello"); rec.Code != http.StatusOK {
			t.Errorf("after re-enabling: %d %s, want the function's 200", rec.Code, rec.Body)
		}
	})
}

// HEAD, letter case and a trailing slash match as the
// apigatewayv2.route.matching switch says (UNVERIFIED, apigateway-http.md
// 4.4, APIGW2-05); a request matching nothing is the switch's NoRoute.
func TestRouteMatchingFollowsTheSwitch(t *testing.T) {
	m := apigatewayv2.RouteMatchingSwitch().Value
	noRoute := apigatewayv2.FrontAnswersSwitch().Value.NoRoute
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		for _, c := range []struct {
			what    string
			r       *http.Request
			matches bool
		}{
			{"HEAD /hello", h.request(http.MethodHead, pathURL(id, "/hello"), ""), m.HeadMatchesGet},
			{"GET /HELLO", h.request(http.MethodGet, pathURL(id, "/HELLO"), ""), !m.CaseSensitive},
			{"GET /hello/", h.request(http.MethodGet, pathURL(id, "/hello/"), ""), !m.TrailingSlashDistinct},
		} {
			before := len(h.fn.invocations())
			rec := h.send(c.r)
			invoked := len(h.fn.invocations()) > before
			if invoked != c.matches {
				t.Errorf("%s invoked the function: %v, want %v (switch apigatewayv2.route.matching %+v)", c.what, invoked, c.matches, m)
			}
			if !c.matches && rec.Code != noRoute.Status {
				t.Errorf("%s = %d, want NoRoute's %d", c.what, rec.Code, noRoute.Status)
			}
		}
	})
}

// "You must deploy an API for changes to take effect" (the stages page,
// apigateway-http.md finding 7): a stage without autoDeploy serves only
// its deployment, so a route added later is Not Found there until the
// next deployment.
func TestStageServesOnlyItsDeployment(t *testing.T) {
	noRoute := apigatewayv2.FrontAnswersSwitch().Value.NoRoute
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id := h.apiID("t")
		integration := h.lambdaIntegration(id, fnARN, "2.0")
		h.createRoute(id, "GET /a", integration)
		must(h, apigatewayv2api.OpCreateStage, stageInput(id, "s"))
		must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, StageName: new("s")})
		h.allow("fn", "apigateway", h.executionARN(id)+"/*/*")
		h.createRoute(id, "GET /b", integration)
		wantAnswer(t, h.get(id, "/s/b"), noRoute, "a route added after the deployment")
		if rec := h.get(id, "/s/a"); rec.Code != http.StatusOK {
			t.Errorf("GET /s/a = %d %s, want the deployed route's 200", rec.Code, rec.Body)
		}
		must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, StageName: new("s")})
		if rec := h.get(id, "/s/b"); rec.Code != http.StatusOK {
			t.Errorf("GET /s/b after redeploying = %d %s, want 200", rec.Code, rec.Body)
		}
		if n := len(h.fn.invocations()); n != 2 {
			t.Errorf("%d invocations, want 2", n)
		}
	})
}

// "If you enable automatic deployments, changes to an API are
// automatically released for you" (the stages page, apigateway-http.md
// finding 7): with autoDeploy a new route serves as soon as CreateRoute
// returns, and a deleted one stops as soon as DeleteRoute returns (the
// delay is UNVERIFIED, APIGW2-04).
func TestAutoDeployServesChangesAtOnce(t *testing.T) {
	run(t, func(h *harness) {
		id, integration := h.lambdaAPI("2.0", "GET /a")
		rid := h.createRoute(id, "GET /new", integration)
		if rec := h.get(id, "/new"); rec.Code != http.StatusOK {
			t.Errorf("GET /new right after CreateRoute = %d %s, want 200", rec.Code, rec.Body)
		}
		must(h, apigatewayv2api.OpDeleteRoute, &apigatewayv2api.DeleteRouteInput{ApiId: id, RouteId: rid})
		wantAnswer(t, h.get(id, "/new"), apigatewayv2.FrontAnswersSwitch().Value.NoRoute, "a route deleted on an autoDeploy stage")
	})
}

// enforceModes are the iam.enforce settings the permission check must
// hold under (the unit's enforce matrix): off has no step-8 Authorizer;
// advise and enforce as Root both let every call through step 8. Until
// the evaluator lands they are the step-8 seam with an Authorizer that
// allows every call.
var enforceModes = map[string][]kerneltest.Option{
	"off":     nil,
	"advise":  {kerneltest.Authorizer(allowAll{})},
	"enforce": {kerneltest.Authorizer(allowAll{})},
}

// A Lambda proxy integration needs a resource-policy statement letting
// apigateway.amazonaws.com invoke the function (apigateway-http.md 5.5,
// finding 9): without one the client gets the switch's Denial (body
// documented, status inferred, APIGW2-11) and the function never runs,
// whatever iam.enforce says; with "${execution_arn}/*/*" it runs.
func TestMissingPermissionAnswersTheDenial(t *testing.T) {
	denial := apigatewayv2.PermissionSwitch().Value.Denial
	for mode, opts := range enforceModes {
		t.Run(mode, func(t *testing.T) {
			run(t, func(h *harness) {
				fnARN := h.createFunction("fn")
				id := h.apiID("t")
				h.createRoute(id, "GET /hello", h.lambdaIntegration(id, fnARN, "2.0"))
				h.autoStage(id)
				wantAnswer(t, h.get(id, "/hello"), denial, "no permission")
				h.notInvoked("no permission")
				h.allow("fn", "apigateway", h.executionARN(id)+"/*/*")
				if rec := h.get(id, "/hello"); rec.Code != http.StatusOK {
					t.Errorf("with the permission: %d %s, want the function's 200", rec.Code, rec.Body)
				}
			}, opts...)
		})
	}
}

// The source ARN API Gateway presents: the documented ones are
// <api-id>/$default/$default for the $default stage and route and
// <api-id>/prod/*/test for the prod stage's test route (the HTTP API
// Lambda troubleshooting page, apigateway-http.md 5.5), and
// <api-id>/*/$default for the $default route on any stage (the ARN
// reference, 2.5). A permission for another API, or with another
// aws:SourceAccount (5.5), lets nothing through.
func TestSourceARN(t *testing.T) {
	denial := apigatewayv2.PermissionSwitch().Value.Denial
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id := h.apiID("t")
		integration := h.lambdaIntegration(id, fnARN, "2.0")
		h.createRoute(id, "$default", integration)
		h.createRoute(id, "GET /test", integration)
		h.autoStage(id)
		must(h, apigatewayv2api.OpCreateStage, stageInput(id, "prod"))
		must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, StageName: new("prod")})
		exec := h.executionARN(id)

		h.allow("fn", "other-api", h.executionARN("zzzzzzzzzz")+"/*/*")
		must(h, lambdaapi.OpAddPermission, &lambdaapi.AddPermissionInput{
			FunctionName: "fn", StatementId: "other-account", Action: "lambda:InvokeFunction", Principal: "apigateway.amazonaws.com",
			SourceArn: new(exec + "/*/*"), SourceAccount: new("111111111111"),
		})
		wantAnswer(t, h.get(id, "/anything"), denial, "permissions for another API and another account only")
		h.notInvoked("permissions for another API and another account only")

		h.allow("fn", "default-default", exec+"/$default/$default")
		if rec := h.get(id, "/anything"); rec.Code != http.StatusOK {
			t.Errorf("$default route on $default stage with <api-id>/$default/$default = %d %s, want 200", rec.Code, rec.Body)
		}
		if calls := h.fn.invocations(); len(calls) == 1 {
			if as, ok := calls[0].mode.(kernel.AsService); !ok || as.Source.String() != exec+"/$default/$default" {
				t.Errorf("invoked as %#v, want source ARN %s/$default/$default", calls[0].mode, exec)
			}
		}
		wantAnswer(t, h.get(id, "/prod/test"), denial, "GET /prod/test before its permission")
		h.allow("fn", "prod-test", exec+"/prod/*/test")
		if rec := h.get(id, "/prod/test"); rec.Code != http.StatusOK {
			t.Errorf("GET /prod/test with <api-id>/prod/*/test = %d %s, want 200", rec.Code, rec.Body)
		}
		wantAnswer(t, h.get(id, "/prod/other"), denial, "the $default route on prod before its permission")
		h.allow("fn", "any-default", exec+"/*/$default")
		if rec := h.get(id, "/prod/other"); rec.Code != http.StatusOK {
			t.Errorf("the $default route on prod with <api-id>/*/$default = %d %s, want 200", rec.Code, rec.Body)
		}
	})
}

// A function error, malformed output and any other Lambda refusal answer
// the switch's FunctionError ({"message":"Internal Server Error"} is
// documented, its status UNVERIFIED, apigateway-http.md 5.6, APIGW2-10);
// Lambda's payload limit answers TooLarge; Lambda without Docker answers
// 501 naming Docker, never an AWS-shaped error (the unit's conflict row 3).
func TestFunctionFailures(t *testing.T) {
	fa := apigatewayv2.FailureAnswersSwitch().Value
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		h.fn.answerWith(&lambdaapi.InvokeOutput{StatusCode: 200, FunctionError: new("Unhandled"), Payload: []byte(`{"errorMessage":"boom","errorType":"Error"}`)}, nil)
		wantAnswer(t, h.get(id, "/hello"), fa.FunctionError, "a function error")
		h.fn.answer(`{"statusCode":"teapot"}`)
		wantAnswer(t, h.get(id, "/hello"), fa.FunctionError, "a malformed 2.0 response")
		h.fn.answer(`not json`)
		wantAnswer(t, h.get(id, "/hello"), fa.FunctionError, "output that is not JSON")
		h.fn.answerWith(nil, lambdaapi.ErrServiceException("the service failed"))
		wantAnswer(t, h.get(id, "/hello"), fa.FunctionError, "a Lambda service error")
		h.fn.answerWith(nil, lambdaapi.ErrRequestEntityTooLargeException("the response is too large"))
		wantAnswer(t, h.get(id, "/hello"), fa.TooLarge, "Lambda's payload limit")
		h.fn.answerWith(nil, lambdaapi.ErrNotImplemented("understudy cannot run Lambda functions: Docker is unavailable; nothing was changed. Start Docker"))
		rec := h.get(id, "/hello")
		want501(t, rec, "Lambda without Docker")
		if !strings.Contains(rec.Body.String(), "Docker") {
			t.Errorf("Lambda without Docker = %s, want the message to name Docker", rec.Body)
		}
	})
}

// A 1.0 response has no inference, so output that is not the documented
// format is a function failure (apigateway-http.md 5.4), the switch's
// FunctionError.
func TestMalformedV1Response(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("1.0", "GET /hello")
		h.fn.answer(`"bare"`)
		wantAnswer(t, h.get(id, "/hello"), apigatewayv2.FailureAnswersSwitch().Value.FunctionError, "a bare string under 1.0")
	})
}

// An invocation that runs past the integration's TimeoutInMillis answers
// the switch's Timeout (UNVERIFIED, apigateway-http.md 5.6, APIGW2-10).
func TestIntegrationTimeout(t *testing.T) {
	run(t, func(h *harness) {
		id, integration := h.lambdaAPI("2.0", "GET /slow")
		must(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{ApiId: id, IntegrationId: integration, TimeoutInMillis: new(int32(50))})
		h.fn.hang()
		wantAnswer(t, h.get(id, "/slow"), apigatewayv2.FailureAnswersSwitch().Value.Timeout, "an invocation past its timeout")
		h.fn.releaseAll()
	})
}

// A request body over the payload quota answers the switch's TooLarge
// before anything else, an unknown API id included, and an event over
// Lambda's synchronous limit answers TooLarge without invoking
// (apigateway-http.md 4.7; the sizes are the switch's MaxBody and
// MaxEvent, the answers UNVERIFIED, APIGW2-10).
func TestSizeLimits(t *testing.T) {
	fa := apigatewayv2.FailureAnswersSwitch().Value
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "POST /upload")
		huge := strings.Repeat("x", fa.MaxBody+1)
		wantAnswer(t, h.send(h.request(http.MethodPost, pathURL("zzzzzzzzzz", "/upload"), huge)), fa.TooLarge, "a body over MaxBody to an unknown API")
		wantAnswer(t, h.send(h.request(http.MethodPost, pathURL(id, "/upload"), huge)), fa.TooLarge, "a body over MaxBody")
		if fa.MaxEvent < fa.MaxBody {
			r := h.request(http.MethodPost, pathURL(id, "/upload"), strings.Repeat("x", fa.MaxEvent))
			r.Header.Set("Content-Type", "text/plain")
			wantAnswer(t, h.send(r), fa.TooLarge, "an event over MaxEvent")
		}
		h.notInvoked("oversized requests")
		if rec := h.send(h.request(http.MethodPost, pathURL(id, "/upload"), strings.Repeat("x", 1<<20))); rec.Code != http.StatusOK {
			t.Errorf("a 1 MiB body = %d %s, want the function's 200", rec.Code, rec.Body)
		}
	})
}

// What the plan minimum stores but does not run answers 501 when a request
// reaches it, and the function never runs (the unit's scope and conflict
// row 3): an AWS_IAM route, a CORS request (an Origin header to an API
// with a CORS configuration), HTTP_PROXY and service integrations, and a
// Lambda integration with credentials, parameter mapping or a stage
// variable in its URI.
func TestUnemulatedRequestsAnswer501(t *testing.T) {
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id := h.apiID("t")
		h.allow("fn", "apigateway", h.executionARN(id)+"/*/*")
		lambdaIn := func() *apigatewayv2api.CreateIntegrationInput {
			return &apigatewayv2api.CreateIntegrationInput{ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationUri: new(fnARN), PayloadFormatVersion: new("2.0")}
		}
		integrationOf := func(in *apigatewayv2api.CreateIntegrationInput) string {
			return deref(must(h, apigatewayv2api.OpCreateIntegration, in).IntegrationId)
		}
		plain := integrationOf(lambdaIn())
		iam := apigatewayv2api.AuthorizationTypeAwsIam
		must(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /iam", AuthorizationType: &iam, Target: new("integrations/" + plain)})
		h.createRoute(id, "GET /plain", plain)
		h.createRoute(id, "GET /http", integrationOf(&apigatewayv2api.CreateIntegrationInput{
			ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeHttpProxy, IntegrationUri: new("http://127.0.0.1:9/x"), IntegrationMethod: new("ANY"), PayloadFormatVersion: new("1.0"),
		}))
		h.createRoute(id, "POST /sqs", integrationOf(&apigatewayv2api.CreateIntegrationInput{
			ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationSubtype: new("SQS-SendMessage"), PayloadFormatVersion: new("1.0"),
			CredentialsArn: new("arn:aws:iam::000000000000:role/apigw"), RequestParameters: map[string]string{"QueueUrl": "http://localhost:4566/000000000000/q", "MessageBody": "$request.body"},
		}))
		creds := lambdaIn()
		creds.CredentialsArn = new("arn:aws:iam::000000000000:role/apigw")
		h.createRoute(id, "GET /creds", integrationOf(creds))
		mapped := lambdaIn()
		mapped.RequestParameters = map[string]string{"overwrite:header.x-extra": "1"}
		h.createRoute(id, "GET /mapped", integrationOf(mapped))
		respMapped := lambdaIn()
		respMapped.ResponseParameters = map[string]map[string]string{"200": {"overwrite:header.x-extra": "1"}}
		h.createRoute(id, "GET /respmapped", integrationOf(respMapped))
		staged := lambdaIn()
		staged.IntegrationUri = new("arn:aws:lambda:us-east-1:000000000000:function:${stageVariables.fn}")
		h.createRoute(id, "GET /staged", integrationOf(staged))
		h.autoStage(id)

		for _, path := range []string{"/iam", "/http", "/creds", "/mapped", "/respmapped", "/staged"} {
			want501(t, h.get(id, path), "GET "+path)
		}
		want501(t, h.send(h.request(http.MethodPost, pathURL(id, "/sqs"), "m")), "POST /sqs")
		h.notInvoked("unemulated integrations")

		must(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{ApiId: id, CorsConfiguration: &apigatewayv2api.Cors{AllowOrigins: []string{"*"}}})
		r := h.request(http.MethodGet, pathURL(id, "/plain"), "")
		r.Header.Set("Origin", "https://app.example")
		want501(t, h.send(r), "a CORS request")
		h.notInvoked("a CORS request")
		if rec := h.get(id, "/plain"); rec.Code != http.StatusOK {
			t.Errorf("a request without Origin to an API with CORS = %d %s, want the function's 200", rec.Code, rec.Body)
		}
	})
}

// The execute-api host <api-id>.execute-api.<region>.<base> reaches the API
// as the path fallback does, without the sandbox the shared-origin path
// form carries (ADR 0020); the event's domainName is the Host header's
// name and domainPrefix its first label (the logging variables page,
// apigateway-http.md 5.1). <api-id>.execute-api.<base> serves in the API's
// region; a host naming another region is the switch's UnknownAPI.
func TestHostForm(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		host := id + ".execute-api." + region + ".localhost"
		rec := h.send(h.request(http.MethodGet, "http://"+host+":4566/hello", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET http://%s:4566/hello = %d %s, want the function's 200", host, rec.Code, rec.Body)
		}
		if got := rec.Header().Values("Content-Security-Policy"); len(got) != 0 {
			t.Errorf("Content-Security-Policy = %q, want none on the host form", got)
		}
		rc := obj(h, h.only().event(h), "requestContext")
		if rc["domainName"] != host || rc["domainPrefix"] != id {
			t.Errorf("domainName %v, domainPrefix %v; want %s and %s", rc["domainName"], rc["domainPrefix"], host, id)
		}
		if rec := h.send(h.request(http.MethodGet, "http://"+id+".execute-api.localhost:4566/hello", "")); rec.Code != http.StatusOK {
			t.Errorf("the regionless host = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		wantAnswer(t, h.send(h.request(http.MethodGet, "http://"+id+".execute-api.us-west-2.localhost:4566/hello", "")), apigatewayv2.FrontAnswersSwitch().Value.UnknownAPI, "a host naming another region")
	})
}

// An API of another account is reached by its id alone, as on AWS, and the
// event's accountId is the API owner's (the logging variables page,
// apigateway-http.md 5.1).
func TestAnotherAccountsAPI(t *testing.T) {
	run(t, func(h *harness) {
		owner, err := aws.ParseAccountID("111111111111")
		if err != nil {
			t.Fatal(err)
		}
		h.Scope.Account = owner
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		if rec := h.get(id, "/hello"); rec.Code != http.StatusOK {
			t.Fatalf("GET /hello = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		if got := obj(h, h.only().event(h), "requestContext")["accountId"]; got != "111111111111" {
			t.Errorf("requestContext.accountId = %v, want the owner 111111111111", got)
		}
	})
}
