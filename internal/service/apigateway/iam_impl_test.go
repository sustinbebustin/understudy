package apigateway_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// resourceTable is the control-plane IAM table of apigateway-rest.md 3.10:
// per bound operation, the method of apigateway:<method> and the resource
// path after arn:aws:apigateway:<region>::, written as the operation's URI
// with its labels in braces.
var resourceTable = map[string][2]string{
	"CreateRestApi": {"POST", "/restapis"}, "GetRestApis": {"GET", "/restapis"},
	"GetRestApi": {"GET", "/restapis/{restApiId}"}, "UpdateRestApi": {"PATCH", "/restapis/{restApiId}"}, "DeleteRestApi": {"DELETE", "/restapis/{restApiId}"},
	"GetResources":   {"GET", "/restapis/{restApiId}/resources"},
	"CreateResource": {"POST", "/restapis/{restApiId}/resources/{parentId}"},
	"GetResource":    {"GET", "/restapis/{restApiId}/resources/{resourceId}"}, "UpdateResource": {"PATCH", "/restapis/{restApiId}/resources/{resourceId}"}, "DeleteResource": {"DELETE", "/restapis/{restApiId}/resources/{resourceId}"},
	"PutMethod": {"PUT", methodTemplate}, "GetMethod": {"GET", methodTemplate}, "UpdateMethod": {"PATCH", methodTemplate}, "DeleteMethod": {"DELETE", methodTemplate},
	"PutMethodResponse": {"PUT", methodTemplate + "/responses/{statusCode}"}, "GetMethodResponse": {"GET", methodTemplate + "/responses/{statusCode}"},
	"UpdateMethodResponse": {"PATCH", methodTemplate + "/responses/{statusCode}"}, "DeleteMethodResponse": {"DELETE", methodTemplate + "/responses/{statusCode}"},
	"PutIntegration": {"PUT", methodTemplate + "/integration"}, "GetIntegration": {"GET", methodTemplate + "/integration"},
	"UpdateIntegration": {"PATCH", methodTemplate + "/integration"}, "DeleteIntegration": {"DELETE", methodTemplate + "/integration"},
	"PutIntegrationResponse": {"PUT", methodTemplate + "/integration/responses/{statusCode}"}, "GetIntegrationResponse": {"GET", methodTemplate + "/integration/responses/{statusCode}"},
	"UpdateIntegrationResponse": {"PATCH", methodTemplate + "/integration/responses/{statusCode}"}, "DeleteIntegrationResponse": {"DELETE", methodTemplate + "/integration/responses/{statusCode}"},
	"CreateDeployment": {"POST", "/restapis/{restApiId}/deployments"}, "GetDeployments": {"GET", "/restapis/{restApiId}/deployments"},
	"GetDeployment": {"GET", "/restapis/{restApiId}/deployments/{deploymentId}"}, "UpdateDeployment": {"PATCH", "/restapis/{restApiId}/deployments/{deploymentId}"},
	"DeleteDeployment": {"DELETE", "/restapis/{restApiId}/deployments/{deploymentId}"},
	"CreateStage":      {"POST", "/restapis/{restApiId}/stages"}, "GetStages": {"GET", "/restapis/{restApiId}/stages"},
	"GetStage": {"GET", "/restapis/{restApiId}/stages/{stageName}"}, "UpdateStage": {"PATCH", "/restapis/{restApiId}/stages/{stageName}"}, "DeleteStage": {"DELETE", "/restapis/{restApiId}/stages/{stageName}"},
	"TagResource": {"PUT", "/tags/{resourceArn}"}, "GetTags": {"GET", "/tags/{resourceArn}"}, "UntagResource": {"DELETE", "/tags/{resourceArn}"},
}

const methodTemplate = "/restapis/{restApiId}/resources/{resourceId}/methods/{httpMethod}"

// uriTemplate renders an HTTP binding's path as a URI template.
func uriTemplate(tb testing.TB, b *registry.HTTPBinding) string {
	tb.Helper()
	var s strings.Builder
	for _, seg := range b.Path {
		s.WriteByte('/')
		switch seg := seg.(type) {
		case registry.Literal:
			s.WriteString(seg.Value)
		case registry.Label:
			s.WriteString("{" + seg.Name + "}")
		case registry.Greedy:
			s.WriteString("{" + seg.Name + "+}")
		}
	}
	return s.String()
}

// The generated descriptor agrees with the 3.10 table: each bound
// control-plane operation's primary IAM action is apigateway:<the method
// of its HTTP binding> and its URI is the table's resource path
// (apigateway-rest.md 3.10, 2.2), and the bound operations are exactly the
// table's rows plus InvokeRestApi, which declares no action (12.2).
func TestGeneratedPairsMatchTheResourceTable(t *testing.T) {
	ops := map[string]*registry.Operation{}
	for _, op := range apigatewayapi.Service().Operations {
		ops[op.ID.Name] = op
	}
	for name, row := range resourceTable {
		op, ok := ops[name]
		if !ok {
			t.Errorf("the 3.10 table names %s, which the model lacks", name)
			continue
		}
		want := registry.IAMAction{Prefix: "apigateway", Name: row[0]}
		if len(op.IAM) == 0 || op.IAM[0] != want {
			t.Errorf("%s primary action = %v, want %v first", name, op.IAM, want)
		}
		if op.HTTP == nil {
			t.Errorf("%s has no HTTP binding", name)
			continue
		}
		if op.HTTP.Method != row[0] {
			t.Errorf("%s HTTP method = %s, want %s", name, op.HTTP.Method, row[0])
		}
		if got := uriTemplate(t, op.HTTP); got != row[1] {
			t.Errorf("%s URI = %s, want the 3.10 resource %s", name, got, row[1])
		}
	}
	if op := ops["InvokeRestApi"]; op == nil || len(op.IAM) != 0 {
		t.Errorf("InvokeRestApi = %+v, want a raw operation with no IAM action", op)
	}
	run(t, func(h *harness) {
		for _, op := range apigatewayapi.Service().Operations {
			_, bound := h.Kernel.Bindings().Status(op).(kernel.Bound)
			_, listed := resourceTable[op.ID.Name]
			if bound != listed && op.ID.Name != "InvokeRestApi" {
				t.Errorf("%s bound = %v, in the 3.10 table = %v; want them equal", op.ID.Name, bound, listed)
			}
		}
	})
}

// The control-plane row of the enforce matrix (spec 6b) on the step-8
// seam, with 5b's evaluator not merged: off installs no Authorizer and
// nothing is asked; advise and enforce as Root let every call through,
// each asked about once with its 3.10 pair. The call's result is the same
// under every mode.
func TestEnforceMatrixControlPlaneRow(t *testing.T) {
	for name, mode := range modes {
		t.Run(name, func(t *testing.T) {
			var g *gate
			var opts []kerneltest.Option
			if name != "off" {
				g = newGate()
				opts = append(opts, kerneltest.Authorizer(g))
			}
			runMode(t, mode, func(h *harness) {
				id, root := h.api("t")
				res := h.resource(id, root, "items")
				h.method(id, res, "GET")
				if got := must(h, apigatewayapi.OpGetResources, &apigatewayapi.GetResourcesInput{RestApiId: id}).Items; len(got) != 2 {
					t.Errorf("GetResources under %s = %d items, want 2", name, len(got))
				}
				if g == nil {
					return
				}
				for op, w := range map[string][2]string{
					"CreateRestApi":  {"apigateway:POST", "arn:aws:apigateway:" + region + "::/restapis"},
					"CreateResource": {"apigateway:POST", apiARN(id) + "/resources/" + root},
					"PutMethod":      {"apigateway:PUT", apiARN(id) + "/resources/" + res + "/methods/GET"},
					"GetResources":   {"apigateway:GET", apiARN(id) + "/resources"},
				} {
					if got := g.pairs(op); len(got) != 1 || got[0] != w {
						t.Errorf("under %s %s asked about %v, want exactly %v", name, op, got, w)
					}
				}
			}, opts...)
		})
	}
}

// Under enforce a denied control-plane call answers the denial identity
// (AccessDeniedException 403, inferred, APIGW1-22) and changes nothing,
// while the data plane, never put to IAM, keeps serving: a gate that
// denies the mutating calls and InvokeRestApi still lets a NONE method
// reach the function (apigateway-rest.md 3.10, 12.2; the enforce matrix).
func TestEnforceDeniesTheControlPlaneOnly(t *testing.T) {
	g := newGate()
	denyOnly := func(ops ...string) {
		g.mu.Lock()
		defer g.mu.Unlock()
		clear(g.deny)
		for _, op := range ops {
			g.deny[op] = true
		}
	}
	runMode(t, modes["enforce"], func(h *harness) {
		id, dep := h.proxyAPI()
		root := deref(must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id}).RootResourceId)
		denyOnly("UpdateRestApi", "DeleteRestApi", "CreateResource", "PutMethod", "DeleteIntegration", "CreateDeployment", "DeleteStage", "TagResource", "InvokeRestApi")
		denial := apigatewayapi.Service().Denial.Identity
		replace := []apigatewayapi.PatchOperation{op(apigatewayapi.OpReplace, "/name", "renamed")}
		for name, err := range map[string]*awserr.Error{
			"UpdateRestApi":     errOf(h.Invoke(h.Context(), apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: replace})),
			"DeleteRestApi":     errOf(h.Invoke(h.Context(), apigatewayapi.OpDeleteRestApi, &apigatewayapi.DeleteRestApiInput{RestApiId: id})),
			"CreateResource":    errOf(h.Invoke(h.Context(), apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: id, ParentId: root, PathPart: "new"})),
			"PutMethod":         errOf(h.Invoke(h.Context(), apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "POST", AuthorizationType: "NONE"})),
			"DeleteIntegration": errOf(h.Invoke(h.Context(), apigatewayapi.OpDeleteIntegration, &apigatewayapi.DeleteIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"})),
			"CreateDeployment":  errOf(h.Invoke(h.Context(), apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id})),
			"DeleteStage":       errOf(h.Invoke(h.Context(), apigatewayapi.OpDeleteStage, &apigatewayapi.DeleteStageInput{RestApiId: id, StageName: "s"})),
			"TagResource":       errOf(h.Invoke(h.Context(), apigatewayapi.OpTagResource, &apigatewayapi.TagResourceInput{ResourceArn: apiARN(id), Tags: map[string]string{"k": "v"}})),
		} {
			if err == nil {
				t.Errorf("denied %s succeeded", name)
				continue
			}
			if err.ID.Shape != denial.Shape || err.ID.Status != http.StatusForbidden {
				t.Errorf("denied %s = %s %d, want %s 403", name, err.ID.JSONType, err.ID.Status, denial.JSONType)
			}
		}
		if rec := h.get(id, "/s/"); rec.Code != http.StatusOK {
			t.Errorf("GET /s/ with the control plane and InvokeRestApi denied = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		if g.askedAbout(invokeRestAPI) {
			t.Errorf("step 8 was asked about InvokeRestApi: %v", g.pairs("InvokeRestApi"))
		}

		denyOnly()
		if api := must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id}); deref(api.Name) != "t" {
			t.Errorf("after a denied UpdateRestApi the API is named %q, want t", deref(api.Name))
		}
		if items := must(h, apigatewayapi.OpGetResources, &apigatewayapi.GetResourcesInput{RestApiId: id}).Items; len(items) != 1 {
			t.Errorf("after a denied CreateResource the API has %d resources, want the root only", len(items))
		}
		if fail(h, apigatewayapi.OpGetMethod, &apigatewayapi.GetMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "POST"}).ID.Status != http.StatusNotFound {
			t.Error("after a denied PutMethod the root has a POST method")
		}
		must(h, apigatewayapi.OpGetIntegration, &apigatewayapi.GetIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"})
		if items := must(h, apigatewayapi.OpGetDeployments, &apigatewayapi.GetDeploymentsInput{RestApiId: id}).Items; len(items) != 1 || deref(items[0].Id) != dep {
			t.Errorf("after a denied CreateDeployment the deployments are %+v, want only %s", items, dep)
		}
		must(h, apigatewayapi.OpGetStage, &apigatewayapi.GetStageInput{RestApiId: id, StageName: "s"})
		if tags := must(h, apigatewayapi.OpGetTags, &apigatewayapi.GetTagsInput{ResourceArn: apiARN(id)}).Tags; len(tags) != 0 {
			t.Errorf("after a denied TagResource the API has tags %v, want none", tags)
		}
	}, kerneltest.Authorizer(g))
}
