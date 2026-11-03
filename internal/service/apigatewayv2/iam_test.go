package apigatewayv2_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// allowAll is a step-8 Authorizer that lets every call through, as
// iam.enforce advise does and enforce does for Root.
type allowAll struct{}

func (allowAll) Authorize(context.Context, *kernel.Call, kernel.AuthzQuery) kernel.Verdict {
	return kernel.Allowed{}
}

// gate is a step-8 Authorizer that records every question by operation and
// denies the operations in deny.
type gate struct {
	mu    sync.Mutex
	deny  map[string]bool
	asked map[ref.OperationID][]kernel.AuthzQuery
}

func newGate(deny ...string) *gate {
	g := &gate{deny: map[string]bool{}, asked: map[ref.OperationID][]kernel.AuthzQuery{}}
	for _, op := range deny {
		g.deny[op] = true
	}
	return g
}

func (g *gate) Authorize(_ context.Context, c *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	id := c.OperationID()
	g.asked[id] = append(g.asked[id], q)
	if id.Service == "apigatewayv2" && g.deny[id.Name] {
		return kernel.Denied{Message: "User: arn:aws:iam::000000000000:user/u is not authorized to perform: apigateway:POST"}
	}
	return kernel.Allowed{}
}

// pairs lists the (action, resource ARN) pairs asked about op.
func (g *gate) pairs(op string) [][2]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out [][2]string
	for _, q := range g.asked[ref.OperationID{Service: "apigatewayv2", Name: op}] {
		for _, p := range q.Pairs {
			res := "*"
			if r, ok := p.Resource.(kernel.ResourceARN); ok {
				res = r.ARN.String()
			}
			out = append(out, [2]string{p.Action.Prefix + ":" + p.Action.Name, res})
		}
	}
	return out
}

func (g *gate) askedAbout(id ref.OperationID) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.asked[id]) > 0
}

// Step 8 asks about each control-plane call as apigateway:<the operation's
// HTTP method> on the resource it acts on: "The API-managing Action
// expression has the format apigateway:{action}, where {action} is one of
// ... GET, POST, PUT, DELETE, PATCH"
// (https://docs.aws.amazon.com/apigateway/latest/developerguide/security_iam_service-with-iam.html),
// the method each operation's HTTP binding names (apigateway-http.md 2.2),
// and the resource the ARN reference gives it: the Apis, Routes,
// Integrations, Stages and Deployments collections for creates and lists,
// the Api, Route, Integration, Stage and Deployment items otherwise
// (https://docs.aws.amazon.com/apigateway/latest/developerguide/arn-format-reference.html,
// apigateway-http.md 2.5, 8.1), and the tagged resource for the tag
// operations (APIGW2-15, recorded).
func TestStep8AsksAboutTheDocumentedPairs(t *testing.T) {
	g := newGate()
	run(t, func(h *harness) {
		id := h.apiID("t")
		api := apiARN(id)
		integration := h.lambdaIntegration(id, h.createFunction("fn"), "2.0")
		rid := h.createRoute(id, "GET /a", integration)
		must(h, apigatewayv2api.OpCreateStage, stageInput(id, "prod"))
		dep := deref(must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id}).DeploymentId)
		must(h, apigatewayv2api.OpGetApis, &apigatewayv2api.GetApisInput{})
		must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id})
		must(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{ApiId: id, Description: new("d")})
		must(h, apigatewayv2api.OpGetRoutes, &apigatewayv2api.GetRoutesInput{ApiId: id})
		must(h, apigatewayv2api.OpGetRoute, &apigatewayv2api.GetRouteInput{ApiId: id, RouteId: rid})
		must(h, apigatewayv2api.OpUpdateRoute, &apigatewayv2api.UpdateRouteInput{ApiId: id, RouteId: rid, OperationName: new("A")})
		must(h, apigatewayv2api.OpGetIntegrations, &apigatewayv2api.GetIntegrationsInput{ApiId: id})
		must(h, apigatewayv2api.OpGetIntegration, &apigatewayv2api.GetIntegrationInput{ApiId: id, IntegrationId: integration})
		must(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{ApiId: id, IntegrationId: integration, Description: new("d")})
		must(h, apigatewayv2api.OpGetStages, &apigatewayv2api.GetStagesInput{ApiId: id})
		must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "prod"})
		must(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "prod", Description: new("d")})
		must(h, apigatewayv2api.OpGetDeployments, &apigatewayv2api.GetDeploymentsInput{ApiId: id})
		must(h, apigatewayv2api.OpGetDeployment, &apigatewayv2api.GetDeploymentInput{ApiId: id, DeploymentId: dep})
		must(h, apigatewayv2api.OpUpdateDeployment, &apigatewayv2api.UpdateDeploymentInput{ApiId: id, DeploymentId: dep, Description: new("d")})
		must(h, apigatewayv2api.OpTagResource, &apigatewayv2api.TagResourceInput{ResourceArn: api, Tags: map[string]string{"a": "b"}})
		must(h, apigatewayv2api.OpGetTags, &apigatewayv2api.GetTagsInput{ResourceArn: api})
		must(h, apigatewayv2api.OpUntagResource, &apigatewayv2api.UntagResourceInput{ResourceArn: api, TagKeys: []string{"a"}})
		must(h, apigatewayv2api.OpDeleteDeployment, &apigatewayv2api.DeleteDeploymentInput{ApiId: id, DeploymentId: dep})
		must(h, apigatewayv2api.OpDeleteStage, &apigatewayv2api.DeleteStageInput{ApiId: id, StageName: "prod"})
		must(h, apigatewayv2api.OpDeleteRoute, &apigatewayv2api.DeleteRouteInput{ApiId: id, RouteId: rid})
		must(h, apigatewayv2api.OpDeleteIntegration, &apigatewayv2api.DeleteIntegrationInput{ApiId: id, IntegrationId: integration})
		must(h, apigatewayv2api.OpDeleteApi, &apigatewayv2api.DeleteApiInput{ApiId: id})

		apis := "arn:aws:apigateway:" + region + "::/apis"
		want := map[string][2]string{
			"CreateApi": {"apigateway:POST", apis}, "GetApis": {"apigateway:GET", apis},
			"GetApi": {"apigateway:GET", api}, "UpdateApi": {"apigateway:PATCH", api}, "DeleteApi": {"apigateway:DELETE", api},
			"CreateRoute": {"apigateway:POST", api + "/routes"}, "GetRoutes": {"apigateway:GET", api + "/routes"},
			"GetRoute": {"apigateway:GET", api + "/routes/" + rid}, "UpdateRoute": {"apigateway:PATCH", api + "/routes/" + rid}, "DeleteRoute": {"apigateway:DELETE", api + "/routes/" + rid},
			"CreateIntegration": {"apigateway:POST", api + "/integrations"}, "GetIntegrations": {"apigateway:GET", api + "/integrations"},
			"GetIntegration": {"apigateway:GET", api + "/integrations/" + integration}, "UpdateIntegration": {"apigateway:PATCH", api + "/integrations/" + integration},
			"DeleteIntegration": {"apigateway:DELETE", api + "/integrations/" + integration},
			"CreateStage":       {"apigateway:POST", api + "/stages"}, "GetStages": {"apigateway:GET", api + "/stages"},
			"GetStage": {"apigateway:GET", api + "/stages/prod"}, "UpdateStage": {"apigateway:PATCH", api + "/stages/prod"}, "DeleteStage": {"apigateway:DELETE", api + "/stages/prod"},
			"CreateDeployment": {"apigateway:POST", api + "/deployments"}, "GetDeployments": {"apigateway:GET", api + "/deployments"},
			"GetDeployment": {"apigateway:GET", api + "/deployments/" + dep}, "UpdateDeployment": {"apigateway:PATCH", api + "/deployments/" + dep},
			"DeleteDeployment": {"apigateway:DELETE", api + "/deployments/" + dep},
		}
		for op, w := range want {
			got := g.pairs(op)
			if len(got) != 1 || got[0] != w {
				t.Errorf("%s asked about %v, want exactly %v", op, got, w)
			}
		}
		// The tag operations are authorized on the tagged resource itself,
		// GET to read and PATCH to change (APIGW2-15, recorded 2026-11-03:
		// steps 13-15; apigateway-http.md 8.1).
		for op, method := range map[string]string{"TagResource": "apigateway:PATCH", "GetTags": "apigateway:GET", "UntagResource": "apigateway:PATCH"} {
			if got := g.pairs(op); len(got) != 1 || got[0] != [2]string{method, api} {
				t.Errorf("%s asked about %v, want exactly [%s %s]", op, got, method, api)
			}
		}
	}, kerneltest.Authorizer(g))
}

// A denied control-plane call answers the service's denial identity
// (AccessDeniedException 403, inferred: the unit's enforce matrix, case
// APIGW2-15), read from the descriptor, and changes nothing.
func TestDeniedControlPlaneCall(t *testing.T) {
	g := newGate("CreateApi")
	run(t, func(h *harness) {
		denial := apigatewayv2api.Service().Denial.Identity
		if denial.Shape == "" {
			t.Fatal("apigatewayv2 names no denial identity")
		}
		err := fail(h, apigatewayv2api.OpCreateApi, &apigatewayv2api.CreateApiInput{Name: "t", ProtocolType: apigatewayv2api.ProtocolTypeHttp})
		if err.ID.Shape != denial.Shape || err.ID.Status != denial.Status {
			t.Errorf("denied CreateApi = %s %d, want %s %d", err.ID.JSONType, err.ID.Status, denial.JSONType, denial.Status)
		}
		if apis := must(h, apigatewayv2api.OpGetApis, &apigatewayv2api.GetApisInput{}).Items; len(apis) != 0 {
			t.Errorf("GetApis after a denied CreateApi = %+v, want none", apis)
		}
	}, kerneltest.Authorizer(g))
}

// A request on a NONE route is never put to IAM: AWS evaluates no
// execute-api permission there (apigateway-http.md 8.2, 8.3: IAM applies
// to AWS_IAM routes, and resource policies are not supported for HTTP
// APIs), so step 8 is never asked about InvokeApi while the request
// reaches the function.
func TestNoneRouteIsNotAuthorized(t *testing.T) {
	g := newGate()
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		if rec := h.get(id, "/hello"); rec.Code != http.StatusOK {
			t.Fatalf("GET /hello = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		if g.askedAbout(invokeAPI) {
			t.Errorf("step 8 was asked about InvokeApi: %v", g.pairs("InvokeApi"))
		}
	}, kerneltest.Authorizer(g))
}
