package apigateway_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
)

// PutMethod answers apiKeyRequired false, authorizationType and httpMethod;
// GetMethod adds methodIntegration and methodResponses keyed by status
// (aws-recorded, test_apigateway_integrations.snapshot.json:L131-L160,
// apigateway-rest.md 2.3). DeleteMethod removes it: GetMethod then answers
// "Invalid Method identifier specified" (aws-recorded,
// test_apigateway_api.snapshot.json:L1104-L1153).
func TestMethodLifecycle(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		out := must(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", AuthorizationType: "NONE"})
		if out.ApiKeyRequired == nil || *out.ApiKeyRequired || deref(out.AuthorizationType) != "NONE" || deref(out.HttpMethod) != "GET" {
			t.Errorf("PutMethod = %+v, want apiKeyRequired false, NONE, GET", out)
		}
		must(h, apigatewayapi.OpPutMethodResponse, &apigatewayapi.PutMethodResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200"})
		must(h, apigatewayapi.OpPutIntegration, &apigatewayapi.PutIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", Type: apigatewayapi.IntegrationTypeMock})
		must(h, apigatewayapi.OpPutIntegrationResponse, &apigatewayapi.PutIntegrationResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200"})
		got := must(h, apigatewayapi.OpGetMethod, &apigatewayapi.GetMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"})
		if _, ok := got.MethodResponses["200"]; !ok || got.MethodIntegration == nil || deref(got.MethodIntegration.Type) != apigatewayapi.IntegrationTypeMock {
			t.Errorf("GetMethod = %+v, want methodResponses[200] and the MOCK methodIntegration", got)
		} else if _, ok := got.MethodIntegration.IntegrationResponses["200"]; !ok {
			t.Errorf("methodIntegration.integrationResponses = %v, want 200", got.MethodIntegration.IntegrationResponses)
		}
		must(h, apigatewayapi.OpDeleteMethod, &apigatewayapi.DeleteMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"})
		wantText(t, fail(h, apigatewayapi.OpGetMethod, &apigatewayapi.GetMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"}), notFound, "Invalid Method identifier specified")
	})
}

// Recorded PutMethod refusals (aws-recorded, apigateway-rest.md 2.4
// table, test_apigateway_api.snapshot.json lines beside each). An
// existing model, Empty, is accepted (3.9: every API carries Empty).
func TestPutMethodRefusals(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		put := func(in apigatewayapi.PutMethodInput) *apigatewayapi.PutMethodInput {
			in.RestApiId, in.ResourceId = id, root
			if in.HttpMethod == "" {
				in.HttpMethod = "GET"
			}
			if in.AuthorizationType == "" {
				in.AuthorizationType = "NONE"
			}
			return &in
		}
		wantText(t, fail(h, apigatewayapi.OpPutMethod, put(apigatewayapi.PutMethodInput{HttpMethod: "WRONG"})),
			badRequest, "Invalid HttpMethod specified. Valid options are GET,PUT,POST,DELETE,PATCH,OPTIONS,HEAD,ANY") // L1364-L1373
		for _, auth := range []string{"CUSTOM", "COGNITO_USER_POOLS"} {
			wantText(t, fail(h, apigatewayapi.OpPutMethod, put(apigatewayapi.PutMethodInput{AuthorizationType: auth})),
				badRequest, "Invalid authorizer ID specified. Setting the authorization type to CUSTOM or COGNITO_USER_POOLS requires a valid authorizer.") // L1374-L1383
		}
		wantText(t, fail(h, apigatewayapi.OpPutMethod, put(apigatewayapi.PutMethodInput{RequestParameters: map[string]bool{
			"method.request.querystring.foo": true, "method.request.header.foo": false,
		}})), badRequest, "Parameter names must be unique across querystring, header and path") // L1188-L1197
		wantText(t, fail(h, apigatewayapi.OpPutMethod, put(apigatewayapi.PutMethodInput{RequestValidatorId: new("abc123")})),
			badRequest, "Invalid Request Validator identifier specified") // L1385-L1394
		wantText(t, fail(h, apigatewayapi.OpPutMethod, put(apigatewayapi.PutMethodInput{RequestModels: map[string]string{"application/json": "petModel"}})),
			badRequest, "Invalid model identifier specified: petModel") // L1396-L1405
		wantText(t, fail(h, apigatewayapi.OpGetMethod, &apigatewayapi.GetMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"}), notFound, "Invalid Method identifier specified")
		must(h, apigatewayapi.OpPutMethod, put(apigatewayapi.PutMethodInput{RequestModels: map[string]string{"application/json": "Empty"}}))
		// A method call on an unknown resource (aws-recorded, L1339-L1362).
		wantText(t, fail(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: "nosuch", HttpMethod: "GET", AuthorizationType: "NONE"}),
			notFound, "Invalid Resource identifier specified")
	})
}

// UpdateMethod: an op other than replace on a replace-only path (L1531)
// and unknown paths (L1553 /httpMethod; L1575 an unescaped / in a content
// type) are refused with the recorded texts (aws-recorded,
// test_apigateway_api.snapshot.json; apigateway-rest.md 3.8); replace
// /apiKeyRequired applies.
func TestUpdateMethod(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.method(id, root, "GET")
		out := must(h, apigatewayapi.OpUpdateMethod, &apigatewayapi.UpdateMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/apiKeyRequired", "true"),
		}})
		if out.ApiKeyRequired == nil || !*out.ApiKeyRequired {
			t.Errorf("replace /apiKeyRequired = %+v, want true", out)
		}
		refuse := func(o apigatewayapi.PatchOperation, text string) {
			t.Helper()
			wantText(t, fail(h, apigatewayapi.OpUpdateMethod, &apigatewayapi.UpdateMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", PatchOperations: []apigatewayapi.PatchOperation{o}}), badRequest, text)
		}
		refuse(op(apigatewayapi.OpAdd, "/apiKeyRequired", "true"), "Invalid patch operation specified. Must be one of: [replace]")
		refuse(op(apigatewayapi.OpReplace, "/httpMethod", "POST"), "Invalid patch path /httpMethod")
		refuse(op(apigatewayapi.OpAdd, "/requestModels/application/json", "Empty"), "Invalid patch path /requestModels/application/json")
	})
}

// Method responses: PutMethodResponse 201 with the status; GetMethodResponse
// of an unknown status answers "Invalid Response status code specified"
// (aws-recorded, test_apigateway_api.snapshot.json:L4129-L4144);
// UpdateMethodResponse on an unknown path answers "Invalid patch path
// <path>" (L4398); a status outside [1-5]\d\d is ValidationException
// ([S] StatusCode pattern, apigateway-rest.md 3.5). DeleteMethodResponse
// removes it.
func TestMethodResponses(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.method(id, root, "GET")
		out := must(h, apigatewayapi.OpPutMethodResponse, &apigatewayapi.PutMethodResponseInput{
			RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200",
			ResponseParameters: map[string]bool{"method.response.header.Access-Control-Allow-Origin": true}, ResponseModels: map[string]string{"application/json": "Empty"},
		})
		if deref(out.StatusCode) != "200" || !out.ResponseParameters["method.response.header.Access-Control-Allow-Origin"] || out.ResponseModels["application/json"] != "Empty" {
			t.Errorf("PutMethodResponse = %+v", out)
		}
		upd := must(h, apigatewayapi.OpUpdateMethodResponse, &apigatewayapi.UpdateMethodResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpAdd, "/responseParameters/method.response.header.X-Custom", "false"),
		}})
		if _, ok := upd.ResponseParameters["method.response.header.X-Custom"]; !ok {
			t.Errorf("UpdateMethodResponse = %+v, want the added parameter", upd)
		}
		wantText(t, fail(h, apigatewayapi.OpUpdateMethodResponse, &apigatewayapi.UpdateMethodResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/statusCode", "201"),
		}}), badRequest, "Invalid patch path /statusCode")
		wantText(t, fail(h, apigatewayapi.OpGetMethodResponse, &apigatewayapi.GetMethodResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "404"}), notFound, "Invalid Response status code specified")
		wantError(t, fail(h, apigatewayapi.OpPutMethodResponse, &apigatewayapi.PutMethodResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "wrong"}), validation)
		must(h, apigatewayapi.OpDeleteMethodResponse, &apigatewayapi.DeleteMethodResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200"})
		wantText(t, fail(h, apigatewayapi.OpGetMethodResponse, &apigatewayapi.GetMethodResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200"}), notFound, "Invalid Response status code specified")
	})
}
