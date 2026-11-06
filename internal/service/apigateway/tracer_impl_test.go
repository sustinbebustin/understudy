package apigateway_test

import (
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

// Members the tracer bullet's operations do not accept yet are refused
// with 501 naming them, so none is stored without effect (CLAUDE.md
// invariant 3; the unit's conflict row 4).
func TestTracerRefusesUnacceptedMembers(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.proxy(id, root, "GET", "arn:aws:lambda:us-east-1:000000000000:function:fn")
		wantNotImplemented(t, fail(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{
			RestApiId: id, StageName: new("s"), CanarySettings: &apigatewayapi.DeploymentCanarySettings{PercentTraffic: 1},
		}), "canarySettings")
	})
}

// The recorded control-plane refusals the tracer's operations give
// (apigateway-rest.md 2.4): an unknown API, resource and method, an empty
// description, an invalid HTTP method, a CUSTOM authorizer, a missing
// integration method and a URI that names no Lambda function.
func TestTracerRecordedRefusals(t *testing.T) {
	run(t, func(h *harness) {
		wantText(t, fail(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: "nosuch0001"}), notFound, "Invalid API identifier specified "+account+":nosuch0001")
		wantText(t, fail(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "t", Description: new("")}), badRequest, "Description cannot be an empty string")
		id, root := h.api("t")
		wantText(t, fail(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: "nosuch", HttpMethod: "GET", AuthorizationType: "NONE"}),
			notFound, "Invalid Resource identifier specified")
		wantText(t, fail(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "FETCH", AuthorizationType: "NONE"}),
			badRequest, "Invalid HttpMethod specified. Valid options are GET,PUT,POST,DELETE,PATCH,OPTIONS,HEAD,ANY")
		wantText(t, fail(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", AuthorizationType: "CUSTOM"}),
			badRequest, "Invalid authorizer ID specified. Setting the authorization type to CUSTOM or COGNITO_USER_POOLS requires a valid authorizer.")
		fnARN := "arn:aws:lambda:us-east-1:000000000000:function:fn"
		wantText(t, fail(h, apigatewayapi.OpPutIntegration, proxyInput(id, root, "GET", fnARN)), notFound, "Invalid Method identifier specified")
		h.method(id, root, "GET")
		in := proxyInput(id, root, "GET", fnARN)
		in.IntegrationHttpMethod = nil
		wantText(t, fail(h, apigatewayapi.OpPutIntegration, in), badRequest, "Enumeration value for HttpMethod must be non-empty")
		in = proxyInput(id, root, "GET", fnARN)
		in.Uri = new("arn:aws:apigateway:us-east-1:s3:path/bucket/key")
		wantText(t, fail(h, apigatewayapi.OpPutIntegration, in), badRequest, "Integrations of type 'AWS_PROXY' currently only supports Lambda function and Firehose stream invocations.")
	})
}

// PutIntegration answers the recorded defaults (apigateway-rest.md 2.3):
// timeoutInMillis 29000, passthroughBehavior WHEN_NO_MATCH, cacheNamespace
// the resource id and no cache key parameters.
func TestTracerIntegrationDefaults(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.method(id, root, "GET")
		out := must(h, apigatewayapi.OpPutIntegration, proxyInput(id, root, "GET", "arn:aws:lambda:us-east-1:000000000000:function:fn"))
		if out.TimeoutInMillis != 29000 || deref(out.PassthroughBehavior) != "WHEN_NO_MATCH" || deref(out.CacheNamespace) != root ||
			out.CacheKeyParameters == nil || len(out.CacheKeyParameters) != 0 || deref(out.HttpMethod) != "POST" {
			t.Errorf("PutIntegration = %+v", out)
		}
	})
}

// Deploying an API with no methods, or with a method that has no
// integration, is refused with BadRequestException (texts inferred,
// APIGW1-05, so only code and status are asserted).
func TestTracerDeploymentRefusals(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		wantError(t, fail(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id, StageName: new("s")}), badRequest)
		h.method(id, root, "GET")
		wantError(t, fail(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id, StageName: new("s")}), badRequest)
	})
}

// The root resource's ANY method serves a verb the root has no explicit
// method for (apigateway-rest.md 4.4); HEAD is not served by GET (switch
// apigateway.invoke.routing, HeadMatchesGet false); an AWS_IAM method
// answers 501 at invoke, without invoking (spec 6b scope).
func TestTracerRootMethods(t *testing.T) {
	routing := apigateway.RoutingSwitch().Value
	run(t, func(h *harness) {
		id, _ := h.proxyAPI()
		root := h.rootOf(id)
		if rec := h.send(h.request(http.MethodHead, pathURL(id, "/s/"), "")); !routing.Methods.HeadMatchesGet {
			wantAnswer(t, rec, apigateway.MissingAuthenticationToken(), "HEAD on a GET-only root")
		}
		h.notInvoked("HEAD on a GET-only root")
		h.proxy(id, root, "ANY", h.fnARN)
		h.deploy(id)
		if rec := h.send(h.request(http.MethodPost, pathURL(id, "/s"), "x")); rec.Code != http.StatusOK {
			t.Errorf("POST /s on the root's ANY = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		if ev := h.only().event(h); ev["httpMethod"] != http.MethodPost || ev["resource"] != "/" {
			t.Errorf("event httpMethod %v, resource %v; want POST, /", ev["httpMethod"], ev["resource"])
		}
		must(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "DELETE", AuthorizationType: "AWS_IAM"})
		must(h, apigatewayapi.OpPutIntegration, proxyInput(id, root, "DELETE", h.fnARN))
		h.deploy(id)
		want501(t, h.send(h.request(http.MethodDelete, pathURL(id, "/s/"), "")), "AWS_IAM", "an AWS_IAM method")
		h.wantJournaled501("an AWS_IAM method")
		if n := len(h.fn.invocations()); n != 1 {
			t.Errorf("the function ran %d times, want once", n)
		}
	})
}

// A stage serves the deployment it points at: a method added after it is
// not served until CreateDeployment repoints the stage (apigateway-rest.md
// 12.5, finding 12).
func TestTracerStageServesItsSnapshot(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI()
		h.proxy(id, h.rootOf(id), "POST", h.fnARN)
		wantAnswer(t, h.send(h.request(http.MethodPost, pathURL(id, "/s/"), "x")), apigateway.MissingAuthenticationToken(), "POST before redeploying")
		h.deploy(id)
		if rec := h.send(h.request(http.MethodPost, pathURL(id, "/s/"), "x")); rec.Code != http.StatusOK {
			t.Errorf("POST after redeploying = %d %s, want the function's 200", rec.Code, rec.Body)
		}
	})
}

// The API id index survives an export and import, so the data plane still
// finds the API (contract 7.1), and Reset drops it.
func TestTracerIndexRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI()
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		if !h.svc.OwnsExecuteAPI(id) {
			t.Fatalf("OwnsExecuteAPI(%s) = false after an import", id)
		}
		if rec := h.get(id, "/s/"); rec.Code != http.StatusOK {
			t.Errorf("GET after an import = %d %s, want the function's 200", rec.Code, rec.Body)
		}
	})
}
