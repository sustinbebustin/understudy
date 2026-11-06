package apigateway_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// gate is a step-8 Authorizer that records every question by operation and
// denies the apigateway operations in deny.
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
	if id.Service == "apigateway" && g.deny[id.Name] {
		return kernel.Denied{Message: "User: arn:aws:iam::000000000000:user/u is not authorized to perform: apigateway:POST"}
	}
	return kernel.Allowed{}
}

// pairs lists the (action, resource ARN) pairs asked about op.
func (g *gate) pairs(op string) [][2]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out [][2]string
	for _, q := range g.asked[ref.OperationID{Service: "apigateway", Name: op}] {
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

// Step 8 asks about each control-plane call as apigateway:<the method of
// its HTTP binding> on the resource of the apigateway-rest.md 3.10 table:
// "The API-managing Action expression has the format apigateway:{action},
// where {action} is one of ... GET, POST, PUT, DELETE, PATCH" and
// "arn:aws:apigateway:{region}::{resource-path-specifier}" (aws-doc
// https://docs.aws.amazon.com/apigateway/latest/developerguide/security_iam_service-with-iam.html);
// the resource is the ARN whose path is the operation's URI (aws-doc
// https://docs.aws.amazon.com/apigateway/latest/developerguide/arn-format-reference.html).
// The tag operations ask about the tagged API itself, GET to read and
// PATCH to change, as AWS's denials name them (APIGW1-22, recorded
// 2026-11-03: steps 13-16; apigateway-rest.md 3.10).
func TestStep8AsksAboutTheDocumentedPairs(t *testing.T) {
	g := newGate()
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id, root := h.api("t")
		api := apiARN(id)
		res := h.resource(id, root, "items")
		h.proxy(id, res, "GET", fnARN)
		must(h, apigatewayapi.OpPutMethodResponse, &apigatewayapi.PutMethodResponseInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", StatusCode: "200"})
		must(h, apigatewayapi.OpPutIntegrationResponse, &apigatewayapi.PutIntegrationResponseInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", StatusCode: "200"})
		dep := *must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id}).Id
		must(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: "prod", DeploymentId: dep})
		m := func(o apigatewayapi.PatchOperation) []apigatewayapi.PatchOperation {
			return []apigatewayapi.PatchOperation{o}
		}
		must(h, apigatewayapi.OpGetRestApis, &apigatewayapi.GetRestApisInput{})
		must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id})
		must(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: m(op(apigatewayapi.OpReplace, "/description", "d"))})
		must(h, apigatewayapi.OpGetResources, &apigatewayapi.GetResourcesInput{RestApiId: id})
		must(h, apigatewayapi.OpGetResource, &apigatewayapi.GetResourceInput{RestApiId: id, ResourceId: res})
		must(h, apigatewayapi.OpUpdateResource, &apigatewayapi.UpdateResourceInput{RestApiId: id, ResourceId: res, PatchOperations: m(op(apigatewayapi.OpReplace, "/pathPart", "things"))})
		must(h, apigatewayapi.OpGetMethod, &apigatewayapi.GetMethodInput{RestApiId: id, ResourceId: res, HttpMethod: "GET"})
		must(h, apigatewayapi.OpUpdateMethod, &apigatewayapi.UpdateMethodInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", PatchOperations: m(op(apigatewayapi.OpReplace, "/operationName", "x"))})
		must(h, apigatewayapi.OpGetMethodResponse, &apigatewayapi.GetMethodResponseInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", StatusCode: "200"})
		must(h, apigatewayapi.OpUpdateMethodResponse, &apigatewayapi.UpdateMethodResponseInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", StatusCode: "200", PatchOperations: m(op(apigatewayapi.OpAdd, "/responseModels/application~1json", "Empty"))})
		must(h, apigatewayapi.OpGetIntegration, &apigatewayapi.GetIntegrationInput{RestApiId: id, ResourceId: res, HttpMethod: "GET"})
		must(h, apigatewayapi.OpUpdateIntegration, &apigatewayapi.UpdateIntegrationInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", PatchOperations: m(op(apigatewayapi.OpReplace, "/timeoutInMillis", "1000"))})
		must(h, apigatewayapi.OpGetIntegrationResponse, &apigatewayapi.GetIntegrationResponseInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", StatusCode: "200"})
		must(h, apigatewayapi.OpUpdateIntegrationResponse, &apigatewayapi.UpdateIntegrationResponseInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", StatusCode: "200", PatchOperations: m(op(apigatewayapi.OpReplace, "/selectionPattern", "2\\d{2}"))})
		must(h, apigatewayapi.OpGetDeployments, &apigatewayapi.GetDeploymentsInput{RestApiId: id})
		must(h, apigatewayapi.OpGetDeployment, &apigatewayapi.GetDeploymentInput{RestApiId: id, DeploymentId: dep})
		must(h, apigatewayapi.OpUpdateDeployment, &apigatewayapi.UpdateDeploymentInput{RestApiId: id, DeploymentId: dep, PatchOperations: m(op(apigatewayapi.OpReplace, "/description", "d"))})
		must(h, apigatewayapi.OpGetStages, &apigatewayapi.GetStagesInput{RestApiId: id})
		must(h, apigatewayapi.OpGetStage, &apigatewayapi.GetStageInput{RestApiId: id, StageName: "prod"})
		must(h, apigatewayapi.OpUpdateStage, &apigatewayapi.UpdateStageInput{RestApiId: id, StageName: "prod", PatchOperations: m(op(apigatewayapi.OpReplace, "/description", "d"))})
		must(h, apigatewayapi.OpTagResource, &apigatewayapi.TagResourceInput{ResourceArn: api, Tags: map[string]string{"a": "b"}})
		must(h, apigatewayapi.OpGetTags, &apigatewayapi.GetTagsInput{ResourceArn: api})
		must(h, apigatewayapi.OpUntagResource, &apigatewayapi.UntagResourceInput{ResourceArn: api, TagKeys: []string{"a"}})
		must(h, apigatewayapi.OpDeleteStage, &apigatewayapi.DeleteStageInput{RestApiId: id, StageName: "prod"})
		must(h, apigatewayapi.OpDeleteDeployment, &apigatewayapi.DeleteDeploymentInput{RestApiId: id, DeploymentId: dep})
		must(h, apigatewayapi.OpDeleteIntegrationResponse, &apigatewayapi.DeleteIntegrationResponseInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", StatusCode: "200"})
		must(h, apigatewayapi.OpDeleteIntegration, &apigatewayapi.DeleteIntegrationInput{RestApiId: id, ResourceId: res, HttpMethod: "GET"})
		must(h, apigatewayapi.OpDeleteMethodResponse, &apigatewayapi.DeleteMethodResponseInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", StatusCode: "200"})
		must(h, apigatewayapi.OpDeleteMethod, &apigatewayapi.DeleteMethodInput{RestApiId: id, ResourceId: res, HttpMethod: "GET"})
		must(h, apigatewayapi.OpDeleteResource, &apigatewayapi.DeleteResourceInput{RestApiId: id, ResourceId: res})
		must(h, apigatewayapi.OpDeleteRestApi, &apigatewayapi.DeleteRestApiInput{RestApiId: id})

		apis := "arn:aws:apigateway:" + region + "::/restapis"
		r := api + "/resources/" + res
		method := r + "/methods/GET"
		want := map[string][2]string{
			"CreateRestApi": {"apigateway:POST", apis}, "GetRestApis": {"apigateway:GET", apis},
			"GetRestApi": {"apigateway:GET", api}, "UpdateRestApi": {"apigateway:PATCH", api}, "DeleteRestApi": {"apigateway:DELETE", api},
			"GetResources":   {"apigateway:GET", api + "/resources"},
			"CreateResource": {"apigateway:POST", api + "/resources/" + root},
			"GetResource":    {"apigateway:GET", r}, "UpdateResource": {"apigateway:PATCH", r}, "DeleteResource": {"apigateway:DELETE", r},
			"PutMethod": {"apigateway:PUT", method}, "GetMethod": {"apigateway:GET", method}, "UpdateMethod": {"apigateway:PATCH", method}, "DeleteMethod": {"apigateway:DELETE", method},
			"PutMethodResponse": {"apigateway:PUT", method + "/responses/200"}, "GetMethodResponse": {"apigateway:GET", method + "/responses/200"},
			"UpdateMethodResponse": {"apigateway:PATCH", method + "/responses/200"}, "DeleteMethodResponse": {"apigateway:DELETE", method + "/responses/200"},
			"PutIntegration": {"apigateway:PUT", method + "/integration"}, "GetIntegration": {"apigateway:GET", method + "/integration"},
			"UpdateIntegration": {"apigateway:PATCH", method + "/integration"}, "DeleteIntegration": {"apigateway:DELETE", method + "/integration"},
			"PutIntegrationResponse": {"apigateway:PUT", method + "/integration/responses/200"}, "GetIntegrationResponse": {"apigateway:GET", method + "/integration/responses/200"},
			"UpdateIntegrationResponse": {"apigateway:PATCH", method + "/integration/responses/200"}, "DeleteIntegrationResponse": {"apigateway:DELETE", method + "/integration/responses/200"},
			"CreateDeployment": {"apigateway:POST", api + "/deployments"}, "GetDeployments": {"apigateway:GET", api + "/deployments"},
			"GetDeployment": {"apigateway:GET", api + "/deployments/" + dep}, "UpdateDeployment": {"apigateway:PATCH", api + "/deployments/" + dep},
			"DeleteDeployment": {"apigateway:DELETE", api + "/deployments/" + dep},
			"CreateStage":      {"apigateway:POST", api + "/stages"}, "GetStages": {"apigateway:GET", api + "/stages"},
			"GetStage": {"apigateway:GET", api + "/stages/prod"}, "UpdateStage": {"apigateway:PATCH", api + "/stages/prod"}, "DeleteStage": {"apigateway:DELETE", api + "/stages/prod"},
		}
		for o, w := range want {
			got := g.pairs(o)
			if len(got) == 0 || got[0] != w {
				t.Errorf("%s asked about %v, want first %v", o, got, w)
			}
		}
		for o, action := range map[string]string{"TagResource": "apigateway:PATCH", "GetTags": "apigateway:GET", "UntagResource": "apigateway:PATCH"} {
			if got := g.pairs(o); len(got) == 0 || got[0] != [2]string{action, api} {
				t.Errorf("%s asked about %v, want first [%s %s]", o, got, action, api)
			}
		}
	}, kerneltest.Authorizer(g))
}

// A denied control-plane call answers the service's denial identity
// (AccessDeniedException 403, inferred: the enforce matrix, case
// APIGW1-22), read from the descriptor, and changes nothing.
func TestDeniedControlPlaneCall(t *testing.T) {
	g := newGate("CreateRestApi")
	run(t, func(h *harness) {
		denial := apigatewayapi.Service().Denial.Identity
		if denial.Shape == "" {
			t.Fatal("apigateway names no denial identity")
		}
		err := fail(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "t"})
		if err.ID.Shape != denial.Shape || err.ID.Status != denial.Status {
			t.Errorf("denied CreateRestApi = %s %d, want %s %d", err.ID.JSONType, err.ID.Status, denial.JSONType, denial.Status)
		}
		if apis := must(h, apigatewayapi.OpGetRestApis, &apigatewayapi.GetRestApisInput{}).Items; len(apis) != 0 {
			t.Errorf("GetRestApis after a denied CreateRestApi = %+v, want none", apis)
		}
	}, kerneltest.Authorizer(g))
}

// A request on a NONE method is never put to step 8: InvokeRestApi
// declares no IAM action (contract 1, 7.3; apigateway-rest.md 12.2), and
// the request reaches the function.
func TestNoneMethodIsNotAuthorized(t *testing.T) {
	g := newGate()
	run(t, func(h *harness) {
		id, _ := h.proxyAPI()
		if rec := h.get(id, "/s/"); rec.Code != http.StatusOK {
			t.Fatalf("GET /s/ = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		if g.askedAbout(invokeRestAPI) {
			t.Errorf("step 8 was asked about InvokeRestApi: %v", g.pairs("InvokeRestApi"))
		}
	}, kerneltest.Authorizer(g))
}
