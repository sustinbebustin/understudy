package apigatewayv2_test

import (
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
)

// A 1.0 event on a named stage (apigateway-http.md 5.2): resource and
// resourcePath are the route's path template and path the request's path
// with the stage segment (both recorded, APIGW2-08 04-named-stage), route variables
// decoded, the stage's variables and name, the domain the path fallback
// reports (13.1.3), and requestId and extendedRequestId the
// apigw-requestid header's value (the apigatewayv2.payload.unrecorded
// switch's RequestIDIsHeader).
func TestPayloadV1OnANamedStage(t *testing.T) {
	choices := apigatewayv2.PayloadSwitch().Value
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id := h.apiID("t")
		h.createRoute(id, "GET /pets/{id}", h.lambdaIntegration(id, fnARN, "1.0"))
		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "prod", StageVariables: map[string]string{"color": "blue"}})
		must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, StageName: new("prod")})
		h.allow("fn", "apigateway", h.executionARN(id)+"/*/*")
		rec := h.get(id, "/prod/pets/rex%20dog")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /prod/pets/rex%%20dog = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		ev := h.only().event(h)
		rc := obj(h, ev, "requestContext")
		if ev["resource"] != "/pets/{id}" || rc["resourcePath"] != "/pets/{id}" {
			t.Errorf("resource %v, resourcePath %v; want the route's path /pets/{id}", ev["resource"], rc["resourcePath"])
		}
		// Decoded as the 2.0 rawPath is (APIGW2-07; inferred for 1.0, whose
		// recording sent no escape).
		if ev["path"] != "/prod/pets/rex dog" || rc["path"] != ev["path"] {
			t.Errorf("path %v, requestContext.path %v; want /prod/pets/rex dog in both", ev["path"], rc["path"])
		}
		if got := strs(ev["pathParameters"]); !maps.Equal(got, map[string]string{"id": "rex dog"}) {
			t.Errorf("pathParameters = %v, want id: rex dog", got)
		}
		if got := strs(ev["stageVariables"]); !maps.Equal(got, map[string]string{"color": "blue"}) {
			t.Errorf("stageVariables = %v, want color: blue", got)
		}
		if domain := id + ".execute-api." + region + ".localhost"; rc["stage"] != "prod" || rc["domainName"] != domain || rc["domainPrefix"] != id {
			t.Errorf("stage %v, domainName %v, domainPrefix %v; want prod, %s, %s", rc["stage"], rc["domainName"], rc["domainPrefix"], domain, id)
		}
		header := rec.Header().Get("Apigw-Requestid")
		if choices.RequestIDIsHeader != (rc["requestId"] == header) || rc["extendedRequestId"] != rc["requestId"] {
			t.Errorf("requestId %v, extendedRequestId %v, apigw-requestid %q; RequestIDIsHeader is %v", rc["requestId"], rc["extendedRequestId"], header, choices.RequestIDIsHeader)
		}
	})
}

// The $default route's 1.0 resource is $default (inferred, APIGW2-08); a
// 1.0 response's own Content-Type is kept, the switch's DefaultContentType
// applying only to one that sets none; a function error under 1.0 is the
// failure-answers switch's FunctionError, and an event over MaxEvent its
// TooLarge, as under 2.0 (apigateway-http.md 5.6, 4.7).
func TestPayloadV1Responses(t *testing.T) {
	fa := apigatewayv2.FailureAnswersSwitch().Value
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("1.0", "$default")
		h.fn.answer(`{"statusCode":200,"headers":{"Content-Type":"text/csv"},"body":"a,b"}`)
		rec := h.get(id, "/anything")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/csv" || rec.Body.String() != "a,b" {
			t.Errorf("response %d %q %q, want 200 text/csv a,b", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
		}
		if got := h.only().event(h)["resource"]; got != "$default" {
			t.Errorf("resource = %v, want $default", got)
		}
		h.fn.answerWith(&lambdaapi.InvokeOutput{StatusCode: 200, FunctionError: new("Unhandled"), Payload: []byte(`{"errorMessage":"boom"}`)}, nil)
		wantAnswer(t, h.get(id, "/anything"), fa.FunctionError, "a function error under 1.0")
		h.fn.answer(`{"statusCode":200,"headers":{"a":1}}`)
		wantAnswer(t, h.get(id, "/anything"), fa.FunctionError, "a 1.0 header that is not a string")

		calls := len(h.fn.invocations())
		if fa.MaxEvent < fa.MaxBody {
			r := h.request(http.MethodPost, pathURL(id, "/upload"), strings.Repeat("x", fa.MaxEvent))
			r.Header.Set("Content-Type", "text/plain")
			wantAnswer(t, h.send(r), fa.TooLarge, "a 1.0 event over MaxEvent")
		}
		if n := len(h.fn.invocations()); n != calls {
			t.Errorf("%d invocations after an oversized 1.0 event, want %d", n, calls)
		}
	})
}

// A 2.0 response's multiValueHeaders reach the client only when the
// apigatewayv2.payload.unrecorded switch's V2MultiValueHeaders says so
// (UNVERIFIED, apigateway-http.md 5.3, APIGW2-09); the format documents no
// such member.
func TestV2MultiValueHeadersFollowTheSwitch(t *testing.T) {
	applied := apigatewayv2.PayloadSwitch().Value.V2MultiValueHeaders
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		h.fn.answer(`{"statusCode":200,"headers":{"x-one":"1"},"multiValueHeaders":{"x-many":["a","b"]},"body":"hi"}`)
		rec := h.get(id, "/hello")
		if rec.Code != http.StatusOK || rec.Header().Get("X-One") != "1" {
			t.Fatalf("response %d %v, want 200 with x-one: 1", rec.Code, rec.Header())
		}
		if got := rec.Header().Values("X-Many"); applied != slices.Equal(got, []string{"a", "b"}) || !applied && len(got) != 0 {
			t.Errorf("x-many = %q; V2MultiValueHeaders is %v", got, applied)
		}
	})
}
