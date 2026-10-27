package apigatewayv2_test

import (
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
)

// hostURL is the execute-api host form URL of path on host
// (apigateway-http.md 11.3).
func hostURL(host, path string) string { return "http://" + host + ":4566" + path }

// An API in another region is served on its own region's host and on the
// regionless host, never on a host naming another region (the
// front.answers switch's UnknownAPI, APIGW2-06); the event's domainName is
// the host the request named and the source ARN carries the API's region
// (apigateway-http.md 2.5, 5.5).
func TestHostFormServesTheAPIsRegion(t *testing.T) {
	run(t, func(h *harness) {
		west, err := aws.KnownRegions().Parse("us-west-2")
		if err != nil {
			t.Fatal(err)
		}
		h.Scope.Region = west
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		own := id + ".execute-api.us-west-2.localhost"
		if rec := h.send(h.request(http.MethodGet, hostURL(own, "/hello"), "")); rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s, want the function's 200", hostURL(own, "/hello"), rec.Code, rec.Body)
		}
		call := h.only()
		if got := obj(h, call.event(h), "requestContext")["domainName"]; got != own {
			t.Errorf("domainName = %v, want %s", got, own)
		}
		as, ok := call.mode.(kernel.AsService)
		if !ok || !strings.HasPrefix(as.Source.String(), "arn:aws:execute-api:us-west-2:"+account+":"+id+"/") {
			t.Errorf("invoked as %#v, want a source ARN in us-west-2", call.mode)
		}
		if rec := h.send(h.request(http.MethodGet, hostURL(id+".execute-api.localhost", "/hello"), "")); rec.Code != http.StatusOK {
			t.Errorf("the regionless host = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		wantAnswer(t, h.send(h.request(http.MethodGet, hostURL(id+".execute-api."+region+".localhost", "/hello"), "")),
			apigatewayv2.FrontAnswersSwitch().Value.UnknownAPI, "a host naming us-east-1 for an API in us-west-2")
		if n := len(h.fn.invocations()); n != 2 {
			t.Errorf("%d invocations, want 2", n)
		}
	})
}

// The host form routes as the path fallback does (apigateway-http.md 4.2,
// 4.4): a named stage's first segment selects it and stays in rawPath, the
// path reaches the event escaped as sent and pathParameters decoded, and a
// Host in upper case names the same API, reported in lower case as a host
// name is.
func TestHostFormStagesAndPaths(t *testing.T) {
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id := h.apiID("t")
		h.createRoute(id, "GET /pets/{name}", h.lambdaIntegration(id, fnARN, "2.0"))
		must(h, apigatewayv2api.OpCreateStage, stageInput(id, "prod"))
		must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, StageName: new("prod")})
		h.allow("fn", "apigateway", h.executionARN(id)+"/*/*")
		host := id + ".execute-api." + region + ".localhost"
		if rec := h.send(h.request(http.MethodGet, hostURL(strings.ToUpper(host), "/prod/pets/rex%20dog"), "")); rec.Code != http.StatusOK {
			t.Fatalf("GET /prod/pets/rex%%20dog on the host form = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		ev := h.only().event(h)
		rc := obj(h, ev, "requestContext")
		if ev["rawPath"] != "/prod/pets/rex%20dog" || rc["stage"] != "prod" || ev["routeKey"] != "GET /pets/{name}" {
			t.Errorf("rawPath %v, stage %v, routeKey %v; want /prod/pets/rex%%20dog, prod, GET /pets/{name}", ev["rawPath"], rc["stage"], ev["routeKey"])
		}
		if got := strs(ev["pathParameters"]); !maps.Equal(got, map[string]string{"name": "rex dog"}) {
			t.Errorf("pathParameters = %v, want name: rex dog", got)
		}
		if rc["domainName"] != host || rc["domainPrefix"] != id {
			t.Errorf("domainName %v, domainPrefix %v; want %s and %s", rc["domainName"], rc["domainPrefix"], host, id)
		}
		wantAnswer(t, h.send(h.request(http.MethodGet, hostURL(host, "/pets/x"), "")), apigatewayv2.FrontAnswersSwitch().Value.NoStage, "a path naming no stage, with no $default stage")
	})
}

// The host form is the API's default endpoint, so the gateway's own
// answers hold there as on the path fallback: the payload quota before the
// API is looked up, an unknown id, and a disabled default endpoint (the
// front.answers and failure-answers switches, APIGW2-06, APIGW2-10); none
// carries the path fallback's sandbox (ADR 0020).
func TestHostFormGatewayAnswers(t *testing.T) {
	front := apigatewayv2.FrontAnswersSwitch().Value
	fa := apigatewayv2.FailureAnswersSwitch().Value
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "POST /hello")
		host := id + ".execute-api." + region + ".localhost"
		unknown := "zzzzzzzzzz.execute-api." + region + ".localhost"
		huge := strings.Repeat("x", fa.MaxBody+1)
		answers := []struct {
			what string
			r    *http.Request
			want apigatewayv2.GatewayAnswer
		}{
			{"a body over MaxBody to an unknown id", h.request(http.MethodPost, hostURL(unknown, "/hello"), huge), fa.TooLarge},
			{"an unknown id", h.request(http.MethodPost, hostURL(unknown, "/hello"), "x"), front.UnknownAPI},
		}
		for _, a := range answers {
			rec := h.send(a.r)
			wantAnswer(t, rec, a.want, a.what)
			if got := rec.Header().Values("Content-Security-Policy"); len(got) != 0 {
				t.Errorf("%s: Content-Security-Policy = %q, want none on the host form", a.what, got)
			}
		}
		must(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{ApiId: id, DisableExecuteApiEndpoint: new(true)})
		wantAnswer(t, h.send(h.request(http.MethodPost, hostURL(host, "/hello"), "x")), front.DisabledEndpoint, "a disabled default endpoint")
		h.notInvoked("gateway answers on the host form")
	})
}

// An API of another account is reached on the host form by its id alone,
// as on the path fallback (apigateway-http.md 4.1), and the event's
// accountId is the owner's (5.1).
func TestHostFormReachesAnotherAccountsAPI(t *testing.T) {
	run(t, func(h *harness) {
		owner, err := aws.ParseAccountID("111111111111")
		if err != nil {
			t.Fatal(err)
		}
		h.Scope.Account = owner
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		if rec := h.send(h.request(http.MethodGet, hostURL(id+".execute-api."+region+".localhost", "/hello"), "")); rec.Code != http.StatusOK {
			t.Fatalf("GET /hello on the host form = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		if got := obj(h, h.only().event(h), "requestContext")["accountId"]; got != "111111111111" {
			t.Errorf("requestContext.accountId = %v, want the owner 111111111111", got)
		}
	})
}
