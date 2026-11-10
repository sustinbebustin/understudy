package apigateway_test

import (
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

// PutMethod stores and echoes the members it accepts, GetMethod answers
// them with the method responses keyed by status (recorded,
// apigateway-rest.md 2.3), and PutMethod on an existing method replaces it
// with its responses (inferred, 3.3).
func TestPutMethodStoresAndReplaces(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		in := &apigatewayapi.PutMethodInput{
			RestApiId: id, ResourceId: root, HttpMethod: "POST", AuthorizationType: "AWS_IAM", ApiKeyRequired: true,
			OperationName: new("createPet"), RequestParameters: map[string]bool{"method.request.querystring.page": true, "method.request.header.x-trace": false},
			RequestModels: map[string]string{"application/json": "Empty", "$default": "Error"}, AuthorizationScopes: []string{"read"},
		}
		want := apigatewayapi.Method{
			HttpMethod: new("POST"), AuthorizationType: new("AWS_IAM"), ApiKeyRequired: new(true), OperationName: new("createPet"),
			RequestParameters: in.RequestParameters, RequestModels: in.RequestModels, AuthorizationScopes: in.AuthorizationScopes,
		}
		if diff := cmp.Diff(want, *must(h, apigatewayapi.OpPutMethod, in)); diff != "" {
			t.Errorf("PutMethod (-want +got):\n%s", diff)
		}
		must(h, apigatewayapi.OpPutMethodResponse, &apigatewayapi.PutMethodResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "POST", StatusCode: "201"})
		want.MethodResponses = map[string]apigatewayapi.MethodResponse{"201": {StatusCode: new("201")}}
		if diff := cmp.Diff(want, *must(h, apigatewayapi.OpGetMethod, &apigatewayapi.GetMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "POST"})); diff != "" {
			t.Errorf("GetMethod (-want +got):\n%s", diff)
		}
		h.method(id, root, "POST")
		got := must(h, apigatewayapi.OpGetMethod, &apigatewayapi.GetMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "POST"})
		if got.MethodResponses != nil || got.RequestModels != nil || *got.ApiKeyRequired || *got.AuthorizationType != "NONE" {
			t.Errorf("GetMethod after a second PutMethod = %+v, want the replacing method alone", got)
		}
	})
}

// No authorizer can exist, so an authorizerId is refused with the
// recorded CUSTOM text (inferred for this case, apigateway-rest.md 2.4);
// the multi-value locations are outside the uniqueness rule, which names
// only querystring, header and path (recorded text).
func TestPutMethodAuthorizerAndParameters(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		wantText(t, fail(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", AuthorizationType: "NONE", AuthorizerId: new("abc123")}),
			badRequest, "Invalid authorizer ID specified. Setting the authorization type to CUSTOM or COGNITO_USER_POOLS requires a valid authorizer.")
		wantText(t, fail(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{
			RestApiId: id, ResourceId: root, HttpMethod: "GET", AuthorizationType: "NONE",
			RequestParameters: map[string]bool{"method.request.path.id": true, "method.request.header.id": true},
		}),
			badRequest, "Parameter names must be unique across querystring, header and path")
		must(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{
			RestApiId: id, ResourceId: root, HttpMethod: "GET", AuthorizationType: "NONE",
			RequestParameters: map[string]bool{"method.request.querystring.id": true, "method.request.multivaluequerystring.id": true},
		})
	})
}

// UpdateMethod checks each value as PutMethod does, applies all or none
// (apigateway-rest.md 3.8, 12.4), and accepts replace /resourceId naming
// the method's own resource, as Terraform sends it (3.8, inferred).
func TestUpdateMethodPaths(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.method(id, root, "GET")
		update := func(ops ...apigatewayapi.PatchOperation) *apigatewayapi.UpdateMethodInput {
			return &apigatewayapi.UpdateMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", PatchOperations: ops}
		}
		out := must(h, apigatewayapi.OpUpdateMethod, update(
			op(apigatewayapi.OpReplace, "/resourceId", root),
			op(apigatewayapi.OpReplace, "/authorizationType", "AWS_IAM"),
			op(apigatewayapi.OpReplace, "/operationName", "listPets"),
			op(apigatewayapi.OpAdd, "/requestParameters/method.request.querystring.page", "true"),
			op(apigatewayapi.OpAdd, "/requestModels/application~1json", "Error"),
			op(apigatewayapi.OpAdd, "/authorizationScopes", "read"),
		))
		if deref(out.AuthorizationType) != "AWS_IAM" || deref(out.OperationName) != "listPets" || !out.RequestParameters["method.request.querystring.page"] ||
			out.RequestModels["application/json"] != "Error" || len(out.AuthorizationScopes) != 1 {
			t.Errorf("UpdateMethod = %+v", out)
		}
		refuse := func(o apigatewayapi.PatchOperation, text string) {
			t.Helper()
			wantText(t, fail(h, apigatewayapi.OpUpdateMethod, update(op(apigatewayapi.OpRemove, "/requestModels/application~1json", ""), o)), badRequest, text)
		}
		refuse(op(apigatewayapi.OpReplace, "/authorizationType", "CUSTOM"),
			"Invalid authorizer ID specified. Setting the authorization type to CUSTOM or COGNITO_USER_POOLS requires a valid authorizer.")
		refuse(op(apigatewayapi.OpAdd, "/requestParameters/method.request.header.page", "false"), "Parameter names must be unique across querystring, header and path")
		refuse(op(apigatewayapi.OpReplace, "/requestValidatorId", "abc123"), "Invalid Request Validator identifier specified")
		refuse(op(apigatewayapi.OpAdd, "/requestModels/text~1plain", "petModel"), "Invalid model identifier specified: petModel")
		refuse(op(apigatewayapi.OpReplace, "/requestParameters/method.request.querystring.page", "yes"), "Invalid patch value for /requestParameters/method.request.querystring.page: expected true or false")
		got := must(h, apigatewayapi.OpGetMethod, &apigatewayapi.GetMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"})
		if got.RequestModels["application/json"] != "Error" {
			t.Errorf("a refused UpdateMethod changed the method: requestModels %v", got.RequestModels)
		}
		other := h.resource(id, root, "other")
		wantNotImplemented(t, fail(h, apigatewayapi.OpUpdateMethod, update(op(apigatewayapi.OpReplace, "/resourceId", other))), "/resourceId")
		wantText(t, fail(h, apigatewayapi.OpUpdateMethod, &apigatewayapi.UpdateMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "PUT"}),
			notFound, "Invalid Method identifier specified")
	})
}

// Method responses: PutMethodResponse replaces a status it already has,
// responseModels name only the seeded models (inferred text), and an
// unknown status answers the recorded NotFound text on update and delete
// (apigateway-rest.md 2.4). DeleteMethod of an unknown method answers
// "Invalid Method identifier specified" (recorded).
func TestMethodResponsesImpl(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.method(id, root, "GET")
		put := func(models map[string]string) *apigatewayapi.PutMethodResponseInput {
			return &apigatewayapi.PutMethodResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200", ResponseModels: models}
		}
		must(h, apigatewayapi.OpPutMethodResponse, put(map[string]string{"application/json": "Empty"}))
		must(h, apigatewayapi.OpPutMethodResponse, put(nil))
		got := must(h, apigatewayapi.OpGetMethodResponse, &apigatewayapi.GetMethodResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200"})
		if got.ResponseModels != nil {
			t.Errorf("GetMethodResponse after a replacing put = %+v, want no responseModels", got)
		}
		wantText(t, fail(h, apigatewayapi.OpPutMethodResponse, put(map[string]string{"application/json": "Pet"})), badRequest, "Invalid model identifier specified: Pet")
		wantText(t, fail(h, apigatewayapi.OpUpdateMethodResponse, &apigatewayapi.UpdateMethodResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "500"}),
			notFound, "Invalid Response status code specified")
		wantText(t, fail(h, apigatewayapi.OpDeleteMethodResponse, &apigatewayapi.DeleteMethodResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "500"}),
			notFound, "Invalid Response status code specified")
		upd := must(h, apigatewayapi.OpUpdateMethodResponse, &apigatewayapi.UpdateMethodResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpAdd, "/responseModels/application~1json", "Error"),
		}})
		if upd.ResponseModels["application/json"] != "Error" {
			t.Errorf("UpdateMethodResponse = %+v, want the Error model", upd)
		}
		wantText(t, fail(h, apigatewayapi.OpDeleteMethod, &apigatewayapi.DeleteMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "PATCH"}),
			notFound, "Invalid Method identifier specified")
	})
}

// A method's members and responses survive an export and import.
func TestMethodRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		must(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{
			RestApiId: id, ResourceId: root, HttpMethod: "GET", AuthorizationType: "NONE",
			OperationName: new("get"), RequestParameters: map[string]bool{"method.request.path.id": true},
		})
		must(h, apigatewayapi.OpPutMethodResponse, &apigatewayapi.PutMethodResponseInput{
			RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200",
			ResponseParameters: map[string]bool{"method.response.header.X": false},
		})
		get := &apigatewayapi.GetMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"}
		before := must(h, apigatewayapi.OpGetMethod, get)
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(before, must(h, apigatewayapi.OpGetMethod, get)); diff != "" {
			t.Errorf("GetMethod after a round trip (-before +after):\n%s", diff)
		}
	})
}

// An apiKeyRequired method answers 403 at invoke, since no API key can be
// created, and the function does not run.
func TestAPIKeyRequiredOnTheRoot(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI()
		root := h.rootOf(id)
		must(h, apigatewayapi.OpUpdateMethod, &apigatewayapi.UpdateMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/apiKeyRequired", "true"),
		}})
		h.deploy(id)
		wantAnswer(t, h.send(h.request(http.MethodGet, pathURL(id, "/s/"), "")), apigateway.APIKeyRequired(), "an apiKeyRequired root method")
		h.notInvoked("an apiKeyRequired root method")
	})
}
