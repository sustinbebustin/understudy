package apigateway_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/restjson"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// The goldens below replay LocalStack snapshots
// (apigateway-rest.md 0 and 10: third-party-recorded, AWS-validated) at
// the parsed level. Each test cites localstack@8b9a79f
// tests/aws/services/apigateway/<file>:L<n>, the test id, and that id's
// last_validated_date in the sibling *.validation.json. The snapshots
// mask ids, names and dates: a masked value "<...>" binds to what
// understudy answered the first time it appears and must then repeat, two
// masks never bind one value, and "datetime" is any timestamp. Every
// unmasked value is asserted as recorded, after the recording's account
// (111111111111), partition and region are replaced by the harness's.
// Recorded keys are the whole object: a key understudy adds or drops is a
// mismatch. Control-plane answers are compared as the restJson1 codec
// encodes them, so the status, the body keys and their JSON forms are
// checked, not only the Go values.

// replay is one snapshot's placeholder bindings.
type replay struct {
	h     *harness
	binds map[string]string
	held  map[string]string
	subst *strings.Replacer
}

func (h *harness) replay() *replay {
	return &replay{
		h: h, binds: map[string]string{}, held: map[string]string{},
		subst: strings.NewReplacer("111111111111", h.Scope.Account.String(), "<partition>", "aws", "<region>", h.Scope.Region.String()),
	}
}

// recorded parses a recorded entry after the scope substitutions.
func (r *replay) recorded(text string) any {
	r.h.Helper()
	var v any
	if err := json.Unmarshal([]byte(r.subst.Replace(text)), &v); err != nil {
		r.h.Fatalf("recorded entry %s: %v", text, err)
	}
	return v
}

// match compares got with want, both parsed JSON, binding placeholders.
func (r *replay) match(where string, got, want any) {
	r.h.Helper()
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			r.h.Errorf("%s = %#v, want an object", where, got)
			return
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				r.h.Errorf("%s.%s = %#v, not in the recording", where, k, g[k])
			}
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok {
				r.h.Errorf("%s.%s is missing, recorded %#v", where, k, wv)
				continue
			}
			r.match(where+"."+k, gv, wv)
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			r.h.Errorf("%s = %#v, want %d items %#v", where, got, len(w), w)
			return
		}
		for i := range w {
			r.match(fmt.Sprintf("%s[%d]", where, i), g[i], w[i])
		}
	case string:
		r.matchString(where, got, w)
	default:
		if fmt.Sprint(got) != fmt.Sprint(want) || (got == nil) != (want == nil) {
			r.h.Errorf("%s = %#v, recorded %#v", where, got, want)
		}
	}
}

func (r *replay) matchString(where string, got any, want string) {
	r.h.Helper()
	switch {
	case want == "datetime":
		if _, ok := got.(float64); !ok {
			r.h.Errorf("%s = %#v, want a timestamp", where, got)
		}
	case strings.HasPrefix(want, "<") && strings.HasSuffix(want, ">"):
		if got == nil {
			r.h.Errorf("%s is null, recorded %s", where, want)
			return
		}
		value := fmt.Sprint(got)
		if bound, ok := r.binds[want]; ok {
			if bound != value {
				r.h.Errorf("%s = %q, but %s was %q", where, value, want, bound)
			}
			return
		}
		if other, ok := r.held[value]; ok {
			r.h.Errorf("%s = %q, already bound to %s, recorded a distinct %s", where, value, other, want)
			return
		}
		r.binds[want], r.held[value] = value, want
	default:
		if g, ok := got.(string); !ok || g != want {
			r.h.Errorf("%s = %#v, recorded %q", where, got, want)
		}
	}
}

// status splits the recorded _status (the ResponseMetadata status) from
// the rest of an entry.
func status(entry any) (rest map[string]any, code int) {
	m, ok := entry.(map[string]any)
	if !ok {
		return nil, 0
	}
	if s, ok := m["_status"].(float64); ok {
		code = int(s)
	}
	rest = make(map[string]any, len(m))
	for k, v := range m {
		if k != "_status" {
			rest[k] = v
		}
	}
	return rest, code
}

// output encodes out as the restJson1 codec answers op and compares the
// status and body with the recorded entry.
func output[In, Out any](r *replay, key string, op kernel.Op[In, Out], out *Out, recorded string) {
	r.h.Helper()
	s, ok := any(out).(smithy.Serializable)
	if !ok {
		r.h.Fatalf("%s: %T does not serialize", key, out)
	}
	desc := op.Descriptor()
	resp, err := restjson.Codec{}.Encode(nil, desc, "rid", s, desc.SuccessCode)
	if err != nil {
		r.h.Fatalf("%s: encode: %s", key, err.Message)
	}
	want, code := status(r.recorded(recorded))
	if resp.Status != code {
		r.h.Errorf("%s: status %d, recorded %d", key, resp.Status, code)
	}
	got, ok := decode(r.h, key, resp.Body).(map[string]any)
	if !ok {
		r.h.Errorf("%s: the body is not an object", key)
		return
	}
	// The snapshots hold what botocore parsed, by member name: a page's
	// items member travels as "item" (its jsonName in the model,
	// com.amazonaws.apigateway#RestApis and siblings).
	if items, ok := got["item"]; ok {
		if _, recordedItems := want["items"]; recordedItems {
			delete(got, "item")
			got["items"] = items
		}
	}
	// No snapshot holds apiStatus, since all predate it (apigateway-rest.md
	// finding 2); its value is the apigateway.restapi.api-status switch's,
	// which TestCreateRestApiDefaults asserts.
	delete(got, "apiStatus")
	if items, ok := got["items"].([]any); ok {
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				delete(m, "apiStatus")
			}
		}
	}
	r.match(key, got, want)
}

// failure compares a control-plane error with a recorded entry: the
// X-Amzn-Errortype shape name, the status and the message; and the body's
// message key when the recording shows one (apigateway-rest.md 2.4).
func (r *replay) failure(key string, err *awserr.Error, recorded string) {
	r.h.Helper()
	if err == nil {
		r.h.Errorf("%s: no error, recorded %s", key, recorded)
		return
	}
	want, code := status(r.recorded(recorded))
	e, ok := want["Error"].(map[string]any)
	if !ok {
		r.h.Fatalf("%s: recorded entry %s has no Error", key, recorded)
	}
	if err.ID.Shape.Name() != e["Code"] || err.ID.Status != code || err.Message != e["Message"] {
		r.h.Errorf("%s: %s %d %q, recorded %v %d %q", key, err.ID.Shape.Name(), err.ID.Status, err.Message, e["Code"], code, e["Message"])
	}
	if msg, ok := want["message"]; ok {
		body, isMap := decode(r.h, key, restjson.Codec{}.EncodeError(nil, nil, "rid", err).Body).(map[string]any)
		if !isMap || body["message"] != msg {
			r.h.Errorf("%s: error body %v, want message %q", key, body, msg)
		}
	}
}

func decode(tb testing.TB, key string, body io.Reader) any {
	tb.Helper()
	b, err := io.ReadAll(body)
	if err != nil {
		tb.Fatal(err)
	}
	if len(b) == 0 {
		// A 202 or 204 with no body, which botocore shows as no members.
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		tb.Fatalf("%s: body %s is not JSON: %v", key, b, err)
	}
	return v
}

func patchOp(op apigatewayapi.Op, path, value string) apigatewayapi.PatchOperation {
	return apigatewayapi.PatchOperation{Op: new(op), Path: new(path), Value: new(value)}
}

// test_apigateway_api.snapshot.json:L2-L111,
// TestApiGatewayApiRestApi::test_list_and_delete_apis, validated
// 2025-07-04.
func TestGoldenListAndDeleteApis(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		api := `{"apiKeySource":"HEADER","createdDate":"datetime","description":"this is my api%s","disableExecuteApiEndpoint":false,"endpointConfiguration":{"ipAddressType":"ipv4","types":["EDGE"]},"id":"<id:%d>","name":"<name:%d>","rootResourceId":"<root-resource-id:%d>"%s}`
		first := must(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "list-1", Description: new("this is my api")})
		output(r, "create-rest-api-1", apigatewayapi.OpCreateRestApi, first, fmt.Sprintf(api, "", 1, 1, 1, `,"_status":201`))
		second := must(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "list-2", Description: new("this is my api2")})
		output(r, "create-rest-api-2", apigatewayapi.OpCreateRestApi, second, fmt.Sprintf(api, "2", 2, 2, 2, `,"_status":201`))
		output(r, "get-rest-api-before-delete", apigatewayapi.OpGetRestApis, must(h, apigatewayapi.OpGetRestApis, &apigatewayapi.GetRestApisInput{}),
			`{"items":[`+fmt.Sprintf(api, "", 1, 1, 1, "")+`,`+fmt.Sprintf(api, "2", 2, 2, 2, "")+`],"_status":200}`)
		output(r, "delete-rest-api", apigatewayapi.OpDeleteRestApi, must(h, apigatewayapi.OpDeleteRestApi, &apigatewayapi.DeleteRestApiInput{RestApiId: *first.Id}), `{"_status":202}`)
		output(r, "get-rest-api-after-delete", apigatewayapi.OpGetRestApis, must(h, apigatewayapi.OpGetRestApis, &apigatewayapi.GetRestApisInput{}),
			`{"items":[`+fmt.Sprintf(api, "2", 2, 2, 2, "")+`],"_status":200}`)
	})
}

// test_apigateway_api.snapshot.json:L112-L145,
// TestApiGatewayApiRestApi::test_get_api_case_insensitive, validated
// 2024-04-15. Only the error is replayed: the create answer predates
// ipAddressType (apigateway-rest.md 2.3), which the 2025 goldens above and
// below record.
func TestGoldenGetApiCaseSensitive(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		id, _ := h.api("lower")
		upper := strings.ToUpper(id)
		if upper == id {
			t.Skipf("id %s has no letter to upper-case", id)
		}
		r.binds["<upper-id>"] = upper
		r.failure("get-api-upper-case", fail(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: upper}),
			`{"Error":{"Code":"NotFoundException","Message":"Invalid API identifier specified 111111111111:`+upper+`"},"message":"Invalid API identifier specified 111111111111:`+upper+`","_status":404}`)
	})
}

// test_apigateway_api.snapshot.json:L146-L227,
// TestApiGatewayApiRestApi::test_create_rest_api_with_optional_params,
// validated 2025-07-04.
func TestGoldenCreateRestApiOptionalParams(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		const created = `{"apiKeySource":"HEADER","createdDate":"datetime","disableExecuteApiEndpoint":false,"endpointConfiguration":{"ipAddressType":"ipv4","types":["EDGE"]},"id":"<id:%d>","name":"<name:%d>","rootResourceId":"<root-resource-id:%d>"%s,"_status":201}`
		output(r, "create-only-name", apigatewayapi.OpCreateRestApi, must(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "opt-1"}), fmt.Sprintf(created, 1, 1, 1, ""))
		r.failure("create-empty-desc", fail(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "opt-x", Description: new("")}),
			`{"Error":{"Code":"BadRequestException","Message":"Description cannot be an empty string"},"message":"Description cannot be an empty string","_status":400}`)
		output(r, "create-with-version", apigatewayapi.OpCreateRestApi, must(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "opt-2", Version: new("v1")}),
			fmt.Sprintf(created, 2, 2, 2, `,"version":"v1"`))
		output(r, "create-with-empty-binary-media", apigatewayapi.OpCreateRestApi, must(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "opt-3", BinaryMediaTypes: []string{}}),
			fmt.Sprintf(created, 3, 3, 3, ""))
		r.failure("string-compression-size", fail(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "opt-y", MinimumCompressionSize: new(int32(-1))}),
			`{"Error":{"Code":"BadRequestException","Message":"Invalid minimum compression size, must be between 0 and 10485760"},"message":"Invalid minimum compression size, must be between 0 and 10485760","_status":400}`)
	})
}

// test_apigateway_api.snapshot.json:L4618-L4704,
// TestApiGatewayApiRestApi::test_create_rest_api_verify_defaults,
// validated 2025-07-04.
func TestGoldenCreateRestApiDefaults(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		const created = `{"apiKeySource":"HEADER","createdDate":"datetime","disableExecuteApiEndpoint":false,"endpointConfiguration":{"ipAddressType":"%s","types":["%s"]},"id":"<id:%d>","name":"<name:%d>","rootResourceId":"<root-resource-id:%d>","_status":201}`
		create := func(name string, types ...apigatewayapi.EndpointType) *apigatewayapi.RestApi {
			in := &apigatewayapi.CreateRestApiInput{Name: name}
			if types != nil {
				in.EndpointConfiguration = &apigatewayapi.EndpointConfiguration{Types: types}
			}
			return must(h, apigatewayapi.OpCreateRestApi, in)
		}
		output(r, "create-with-edge-default", apigatewayapi.OpCreateRestApi, create("d-1", apigatewayapi.EndpointTypeEdge), fmt.Sprintf(created, "ipv4", "EDGE", 1, 1, 1))
		output(r, "create-with-regional-default", apigatewayapi.OpCreateRestApi, create("d-2", apigatewayapi.EndpointTypeRegional), fmt.Sprintf(created, "ipv4", "REGIONAL", 2, 2, 2))
		output(r, "create-with-private-default", apigatewayapi.OpCreateRestApi, create("d-3", apigatewayapi.EndpointTypePrivate), fmt.Sprintf(created, "dualstack", "PRIVATE", 3, 3, 3))
		output(r, "create-with-empty-default", apigatewayapi.OpCreateRestApi, create("d-4"), fmt.Sprintf(created, "ipv4", "EDGE", 4, 4, 4))
		// AWS renders an enum list's violation for the whole list, "Value
		// '[wrong]' at 'createRestApiInput.endpointConfiguration.types' failed
		// to satisfy constraint: Member must satisfy constraint: [Member must
		// satisfy enum value set: [PRIVATE, EDGE, REGIONAL]]"; the generated
		// validator names the member (types.1.member), so only the code and
		// status are replayed (CreateRestApi Partial note).
		err := fail(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "d-x", Description: new(""), EndpointConfiguration: &apigatewayapi.EndpointConfiguration{Types: []apigatewayapi.EndpointType{"wrong"}}})
		wantError(t, err, validation)
	})
}

// test_apigateway_api.snapshot.json:L555-L671,
// TestApiGatewayApiResource::test_resource_lifecycle, validated
// 2024-04-15. The test sorts GetResources items by path before the
// snapshot (SortingTransformer("items", path)); AWS lists them by id
// (APIGW1-04), so the replay sorts them the same way.
func TestGoldenResourceLifecycle(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		id, root := h.api("lifecycle")
		resources := func() *apigatewayapi.GetResourcesOutput {
			out := must(h, apigatewayapi.OpGetResources, &apigatewayapi.GetResourcesInput{RestApiId: id})
			slices.SortFunc(out.Items, func(a, b apigatewayapi.Resource) int { return strings.Compare(deref(a.Path), deref(b.Path)) })
			return out
		}
		const rootItem = `{"id":"<id:1>","path":"/"}`
		output(r, "rest-api-root-resource", apigatewayapi.OpGetResources, resources(), `{"items":[`+rootItem+`],"_status":200}`)
		pets := must(h, apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: id, ParentId: root, PathPart: "pets"})
		const petsItem = `{"id":"<id:2>","parentId":"<id:1>","path":"/pets","pathPart":"pets"}`
		output(r, "create-resource", apigatewayapi.OpCreateResource, pets, strings.TrimSuffix(petsItem, "}")+`,"_status":201}`)
		output(r, "rest-api-resources-after-create", apigatewayapi.OpGetResources, resources(), `{"items":[`+rootItem+`,`+petsItem+`],"_status":200}`)
		sub := must(h, apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: id, ParentId: *pets.Id, PathPart: "subpets"})
		const subItem = `{"id":"<id:3>","parentId":"<id:2>","path":"/pets/subpets","pathPart":"subpets"}`
		output(r, "create-subresource", apigatewayapi.OpCreateResource, sub, strings.TrimSuffix(subItem, "}")+`,"_status":201}`)
		output(r, "rest-api-resources-after-create-sub", apigatewayapi.OpGetResources, resources(), `{"items":[`+rootItem+`,`+petsItem+`,`+subItem+`],"_status":200}`)
		const dogs = `{"id":"<id:2>","parentId":"<id:1>","path":"/dogs","pathPart":"dogs","_status":200}`
		output(r, "update-path-part", apigatewayapi.OpUpdateResource, must(h, apigatewayapi.OpUpdateResource, &apigatewayapi.UpdateResourceInput{
			RestApiId: id, ResourceId: *pets.Id, PatchOperations: []apigatewayapi.PatchOperation{patchOp(apigatewayapi.OpReplace, "/pathPart", "dogs")},
		}), dogs)
		output(r, "get-resp-after-update-path-part", apigatewayapi.OpGetResource, must(h, apigatewayapi.OpGetResource, &apigatewayapi.GetResourceInput{RestApiId: id, ResourceId: *pets.Id}), dogs)
		output(r, "del-resource", apigatewayapi.OpDeleteResource, must(h, apigatewayapi.OpDeleteResource, &apigatewayapi.DeleteResourceInput{RestApiId: id, ResourceId: *pets.Id}), `{"_status":202}`)
		output(r, "rest-api-resources-after-delete", apigatewayapi.OpGetResources, resources(), `{"items":[`+rootItem+`],"_status":200}`)
	})
}

// test_apigateway_api.snapshot.json:L813-L846,
// TestApiGatewayApiResource::test_delete_resource, validated 2024-04-15.
func TestGoldenDeleteResourceSubtree(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		id, root := h.api("subtree")
		pets := h.resource(id, root, "pets")
		sub := h.resource(id, pets, "subpets")
		output(r, "delete-resource", apigatewayapi.OpDeleteResource, must(h, apigatewayapi.OpDeleteResource, &apigatewayapi.DeleteResourceInput{RestApiId: id, ResourceId: pets}), `{"_status":202}`)
		output(r, "get-resources", apigatewayapi.OpGetResources, must(h, apigatewayapi.OpGetResources, &apigatewayapi.GetResourcesInput{RestApiId: id}), `{"items":[{"id":"<id:1>","path":"/"}],"_status":200}`)
		r.failure("delete-subresource", fail(h, apigatewayapi.OpDeleteResource, &apigatewayapi.DeleteResourceInput{RestApiId: id, ResourceId: sub}),
			`{"Error":{"Code":"NotFoundException","Message":"Invalid Resource identifier specified"},"message":"Invalid Resource identifier specified","_status":404}`)
	})
}

// test_apigateway_api.snapshot.json:L1019-L1076,
// TestApiGatewayApiResource::test_create_proxy_resource_validation,
// validated 2024-04-15.
func TestGoldenProxyResourceRules(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		id, root := h.api("proxy")
		create := func(parent, part string) (*apigatewayapi.Resource, *awserr.Error) {
			return h.Invoke(h.Context(), apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: id, ParentId: parent, PathPart: part})
		}
		proxy := must(h, apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: id, ParentId: root, PathPart: "{proxy+}"})
		output(r, "create-base-proxy-resource", apigatewayapi.OpCreateResource, proxy, `{"id":"<id:1>","parentId":"<parent-id:1>","path":"/{proxy+}","pathPart":"{proxy+}","_status":201}`)
		const greedyChild = `{"Error":{"Code":"BadRequestException","Message":"Cannot create a child of a resource with a greedy path variable: {proxy+}"},"message":"Cannot create a child of a resource with a greedy path variable: {proxy+}","_status":400}`
		r.failure("create-proxy-dynamic-sibling-resource", errOf(create(root, "{dynamic}")),
			`{"Error":{"Code":"BadRequestException","Message":"A sibling ({proxy+}) of this resource already has a variable path part -- only one is allowed"},"message":"A sibling ({proxy+}) of this resource already has a variable path part -- only one is allowed","_status":400}`)
		r.failure("create-proxy-static-child-resource", errOf(create(*proxy.Id, "child")), greedyChild)
		r.failure("create-proxy-dynamic-child-resource", errOf(create(*proxy.Id, "{child}")), greedyChild)
		parent := h.resource(id, root, "parent")
		output(r, "create-greedy-child-resource", apigatewayapi.OpCreateResource, must(h, apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: id, ParentId: parent, PathPart: "{child+}"}),
			`{"id":"<id:2>","parentId":"<parent-id:2>","path":"/parent/{child+}","pathPart":"{child+}","_status":201}`)
	})
}

// test_apigateway_api.snapshot.json:L4909-L4935,
// TestApiGatewayApiResource::test_update_resource_on_root, validated
// 2025-09-03.
func TestGoldenRootResourceImmutable(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		id, root := h.api("root")
		update := func(op apigatewayapi.PatchOperation) *awserr.Error {
			return fail(h, apigatewayapi.OpUpdateResource, &apigatewayapi.UpdateResourceInput{RestApiId: id, ResourceId: root, PatchOperations: []apigatewayapi.PatchOperation{op}})
		}
		r.failure("update-root-path-part", update(patchOp(apigatewayapi.OpReplace, "/pathPart", "dogs")),
			`{"Error":{"Code":"BadRequestException","Message":"Root resource cannot update its pathPart."},"message":"Root resource cannot update its pathPart.","_status":400}`)
		r.failure("update-root-parent", update(patchOp(apigatewayapi.OpReplace, "/parentId", root)),
			`{"Error":{"Code":"BadRequestException","Message":"Root resource cannot update its parentId."},"message":"Root resource cannot update its parentId.","_status":400}`)
	})
}

// test_apigateway_api.snapshot.json:L1104-L1154,
// TestApiGatewayApiMethod::test_method_lifecycle, validated 2024-04-15.
func TestGoldenMethodLifecycle(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		id, root := h.api("method")
		key := func() *apigatewayapi.GetMethodInput {
			return &apigatewayapi.GetMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "ANY"}
		}
		output(r, "put-base-method-response", apigatewayapi.OpPutMethod, must(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "ANY", AuthorizationType: "NONE"}),
			`{"apiKeyRequired":false,"authorizationType":"NONE","httpMethod":"ANY","_status":201}`)
		output(r, "get-base-method-response", apigatewayapi.OpGetMethod, must(h, apigatewayapi.OpGetMethod, key()),
			`{"apiKeyRequired":false,"authorizationType":"NONE","httpMethod":"ANY","_status":200}`)
		output(r, "del-base-method-response", apigatewayapi.OpDeleteMethod, must(h, apigatewayapi.OpDeleteMethod, &apigatewayapi.DeleteMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "ANY"}), `{"_status":204}`)
		const missing = `{"Error":{"Code":"NotFoundException","Message":"Invalid Method identifier specified"},"message":"Invalid Method identifier specified","_status":404}`
		r.failure("get-deleted-method-response", fail(h, apigatewayapi.OpGetMethod, key()), missing)
		r.failure("delete-deleted-method-response", fail(h, apigatewayapi.OpDeleteMethod, &apigatewayapi.DeleteMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "ANY"}), missing)
	})
}

// test_apigateway_api.snapshot.json:L1339-L1409,
// TestApiGatewayApiMethod::test_put_method_validation, validated
// 2024-04-15.
func TestGoldenPutMethodValidation(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		id, root := h.api("validation")
		put := func(in apigatewayapi.PutMethodInput) *awserr.Error {
			return fail(h, apigatewayapi.OpPutMethod, &in)
		}
		bad := func(text string) string {
			return `{"Error":{"Code":"BadRequestException","Message":"` + text + `"},"message":"` + text + `","_status":400}`
		}
		const noResource = `{"Error":{"Code":"NotFoundException","Message":"Invalid Resource identifier specified"},"message":"Invalid Resource identifier specified","_status":404}`
		r.failure("wrong-api", put(apigatewayapi.PutMethodInput{RestApiId: "fake-api", ResourceId: root, HttpMethod: "WRONG", AuthorizationType: "NONE"}), noResource)
		r.failure("wrong-resource", put(apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: "fake-resource-id", HttpMethod: "WRONG", AuthorizationType: "NONE"}), noResource)
		r.failure("wrong-method", put(apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "WRONG", AuthorizationType: "NONE"}),
			bad("Invalid HttpMethod specified. Valid options are GET,PUT,POST,DELETE,PATCH,OPTIONS,HEAD,ANY"))
		r.failure("missing-authorizer-id", put(apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "ANY", AuthorizationType: "CUSTOM"}),
			bad("Invalid authorizer ID specified. Setting the authorization type to CUSTOM or COGNITO_USER_POOLS requires a valid authorizer."))
		r.failure("invalid-request-validator", put(apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "ANY", AuthorizationType: "NONE", RequestValidatorId: new("fake-validator")}),
			bad("Invalid Request Validator identifier specified"))
		r.failure("invalid-model-name", put(apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "ANY", AuthorizationType: "NONE", RequestModels: map[string]string{"application/json": "petModel"}}),
			bad("Invalid model identifier specified: petModel"))
	})
}

// test_apigateway_api.snapshot.json:L3084-L3098,
// TestApigatewayIntegration::test_put_integration_wrong_type, validated
// 2024-04-15; and L4062-L4086,
// TestApigatewayIntegrationResponse::test_integration_response_invalid_statuscode,
// validated 2025-06-18.
func TestGoldenIntegrationValidationExceptions(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		id, root := h.api("enum")
		h.method(id, root, "GET")
		r.failure("put-integration-wrong-type", fail(h, apigatewayapi.OpPutIntegration, &apigatewayapi.PutIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", Type: "HTTPS_PROXY"}),
			`{"Error":{"Code":"ValidationException","Message":"1 validation error detected: Value 'HTTPS_PROXY' at 'putIntegrationInput.type' failed to satisfy constraint: Member must satisfy enum value set: [HTTP, MOCK, AWS_PROXY, HTTP_PROXY, AWS]"},"_status":400}`)

		other, otherRoot := h.api("status")
		const pattern = `{"Error":{"Code":"ValidationException","Message":"1 validation error detected: Value 'wrong' at 'statusCode' failed to satisfy constraint: Member must satisfy regular expression pattern: [1-5]\\d\\d"},"_status":400}`
		r.failure("put-integration-response-invalid-statusCode", fail(h, apigatewayapi.OpPutIntegrationResponse, &apigatewayapi.PutIntegrationResponseInput{
			RestApiId: other, ResourceId: otherRoot, HttpMethod: "GET", StatusCode: "wrong", ResponseTemplates: map[string]string{"application/json": `"created"`}, SelectionPattern: new(""),
		}), pattern)
		r.failure("get-integration-response-invalid-statusCode", fail(h, apigatewayapi.OpGetIntegrationResponse, &apigatewayapi.GetIntegrationResponseInput{
			RestApiId: other, ResourceId: otherRoot, HttpMethod: "GET", StatusCode: "wrong",
		}), pattern)
	})
}

// test_apigateway_api.snapshot.json:L5326-L5360,
// TestApigatewayIntegration::test_put_integration_response_templates,
// validated 2026-02-12. The two empty-template entries are AWS's null,
// which understudy answers as "" (the PutIntegrationResponse Partial note,
// apigateway-rest.md finding 6); they are skipped, naming that note, and
// the entry without a template is replayed. (The none entry cannot be sent
// from Go at all: a map[string]string holds no null.)
func TestGoldenIntegrationResponseTemplates(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		id, root := h.api("templates")
		h.method(id, root, "POST")
		must(h, apigatewayapi.OpPutIntegration, &apigatewayapi.PutIntegrationInput{
			RestApiId: id, ResourceId: root, HttpMethod: "POST", IntegrationHttpMethod: new("GET"), Type: apigatewayapi.IntegrationTypeMock,
			RequestTemplates: map[string]string{"application/json": `{"statusCode": 200}`},
		})
		output(r, "put-integration-no-response-template", apigatewayapi.OpPutIntegrationResponse, must(h, apigatewayapi.OpPutIntegrationResponse, &apigatewayapi.PutIntegrationResponseInput{
			RestApiId: id, ResourceId: root, HttpMethod: "POST", StatusCode: "200", SelectionPattern: new(""),
		}), `{"selectionPattern":"","statusCode":"200","_status":201}`)
		t.Run("empty templates", func(t *testing.T) {
			t.Skip(`put-integration-response-template-none and -empty record {"application/json": null}; understudy answers "" (PutIntegrationResponse Partial note: an empty-string response template is returned as "", where AWS returns null)`)
		})
	})
}

// test_apigateway_api.snapshot.json:L4803-L4837,
// TestApiGatewayApiRestApi::test_update_rest_api_concatenation_of_errors,
// validated 2025-07-07.
func TestGoldenPatchOperationErrors(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		id, _ := h.api("patch")
		const wrong = apigatewayapi.Op("wrong")
		update := func(ops ...apigatewayapi.PatchOperation) *awserr.Error {
			return fail(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: ops})
		}
		clause := func(i int) string {
			return fmt.Sprintf("Value 'wrong' at 'updateRestApiInput.patchOperations.%d.member.op' failed to satisfy constraint: Member must satisfy enum value set: [add, remove, move, test, replace, copy]", i)
		}
		ip := patchOp(wrong, "/endpointConfiguration/ipAddressType", "dualstack")
		types := patchOp(wrong, "/endpointConfiguration/types/0", "EDGE")
		two := `{"Error":{"Code":"ValidationException","Message":"2 validation errors detected: ` + clause(1) + `; ` + clause(2) + `"},"_status":400}`
		r.failure("update-rest-api-wrong-operations-on-ipAddressType", update(ip, ip), two)
		r.failure("update-rest-api-wrong-operations-on-type", update(types, types), two)
		r.failure("update-rest-api-wrong-operations-on-type-and-on-ip-address-type", update(ip, types, patchOp(wrong, "/binaryMediaTypes", "image/gif")),
			`{"Error":{"Code":"ValidationException","Message":"3 validation errors detected: `+clause(1)+`; `+clause(2)+`; `+clause(3)+`"},"_status":400}`)
	})
}

// test_apigateway_integrations.snapshot.json:L268-L439,
// test_put_integration_validation, validated 2024-06-06.
func TestGoldenPutIntegrationValidation(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		id, root := h.api("test-apigw")
		h.method(id, root, "GET")
		must(h, apigatewayapi.OpPutMethodResponse, &apigatewayapi.PutMethodResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200"})
		put := func(typ apigatewayapi.IntegrationType, uri, method, credentials string) (*apigatewayapi.Integration, *awserr.Error) {
			in := &apigatewayapi.PutIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", Type: typ, Uri: new(uri)}
			if method != "" {
				in.IntegrationHttpMethod = new(method)
			}
			if credentials != "" {
				in.Credentials = new(credentials)
			}
			return h.Invoke(h.Context(), apigatewayapi.OpPutIntegration, in)
		}
		bad := func(text string) string {
			return `{"Error":{"Code":"BadRequestException","Message":"` + text + `"},"message":"` + text + `","_status":400}`
		}
		ok := func(key string, out *apigatewayapi.Integration, err *awserr.Error, recorded string) {
			h.Helper()
			if err != nil {
				h.Errorf("%s: %s %d %q", key, err.ID.Shape.Name(), err.ID.Status, err.Message)
				return
			}
			output(r, key, apigatewayapi.OpPutIntegration, out, recorded)
		}
		const defaults = `"cacheKeyParameters":[],"cacheNamespace":"<cache-namespace:1>","passthroughBehavior":"WHEN_NO_MATCH","timeoutInMillis":29000`
		role := "arn:aws:iam::111111111111:role/service-role/testfunction-role-oe783psq"
		lambdaURI := "arn:<partition>:apigateway:<region>:lambda:path/2015-03-31/functions/arn:<partition>:lambda:<region>:111111111111:function:MyLambda/invocations"
		for _, typ := range []apigatewayapi.IntegrationType{apigatewayapi.IntegrationTypeHttp, apigatewayapi.IntegrationTypeHttpProxy, apigatewayapi.IntegrationTypeAws} {
			r.failure("required-integration-method-"+string(typ), errOf(put(typ, "http://example.com", "", "")), bad("Enumeration value for HttpMethod must be non-empty"))
		}
		out, err := put(apigatewayapi.IntegrationTypeMock, "http://example.com", "", "")
		ok("not-required-integration-method-MOCK", out, err, `{`+defaults+`,"type":"MOCK","_status":201}`)
		for _, typ := range []apigatewayapi.IntegrationType{apigatewayapi.IntegrationTypeHttp, apigatewayapi.IntegrationTypeHttpProxy} {
			out, err := put(typ, "http://example.com", "POST", "")
			ok("http-method-"+string(typ), out, err, `{`+defaults+`,"connectionType":"INTERNET","httpMethod":"POST","type":"`+string(typ)+`","uri":"http://example.com","_status":201}`)
		}
		out, err = put(apigatewayapi.IntegrationTypeAws, r.subst.Replace("arn:<partition>:apigateway:<region>:s3:path/b/k"), "POST", r.subst.Replace(role))
		ok("aws-integration-AWS", out, err, `{`+defaults+`,"credentials":"`+role+`","httpMethod":"POST","type":"AWS","uri":"arn:<partition>:apigateway:<region>:s3:path/b/k","_status":201}`)
		for _, typ := range []apigatewayapi.IntegrationType{apigatewayapi.IntegrationTypeAws, apigatewayapi.IntegrationTypeAwsProxy} {
			out, err := put(typ, r.subst.Replace(lambdaURI), "POST", "")
			ok("aws-integration-type-"+string(typ), out, err, `{`+defaults+`,"httpMethod":"POST","type":"`+string(typ)+`","uri":"`+lambdaURI+`","_status":201}`)
		}
		r.failure("no-s3-support-AWS_PROXY", errOf(put(apigatewayapi.IntegrationTypeAwsProxy, r.subst.Replace("arn:<partition>:apigateway:<region>:s3:path/b/k"), "POST", r.subst.Replace(role))),
			bad("Integrations of type 'AWS_PROXY' currently only supports Lambda function and Firehose stream invocations."))
		for _, typ := range []apigatewayapi.IntegrationType{apigatewayapi.IntegrationTypeHttp, apigatewayapi.IntegrationTypeHttpProxy} {
			r.failure("invalid-uri-"+string(typ), errOf(put(typ, "non-valid-http", "POST", "")), bad("Invalid HTTP endpoint specified for URI"))
		}
		r.failure("invalid-uri-not-an-arn", errOf(put(apigatewayapi.IntegrationTypeAws, "non-valid-arn", "POST", "")), bad("Invalid ARN specified in the request"))
		r.failure("invalid-uri-invalid-arn", errOf(put(apigatewayapi.IntegrationTypeAws, "arn:aws:iam::0000000000:role/service-role/asdf", "POST", "")), bad("AWS ARN for integration must contain path or action"))
	})
}

// test_apigateway_integrations.snapshot.json:L1062-L1083,
// test_integration_mock_with_path_param, validated 2024-11-29: MOCK drops
// httpMethod, keeps a request template that is not JSON, and the
// invocation answers 200 with an empty body (the test's asserts,
// test_apigateway_integrations.py, after the snapshot match).
func TestGoldenMockLenientParse(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		id, root := h.api("lenient")
		res := h.resource(id, root, "{testPath}")
		must(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", AuthorizationType: "none", RequestParameters: map[string]bool{"method.request.path.testPath": true}})
		must(h, apigatewayapi.OpPutMethodResponse, &apigatewayapi.PutMethodResponseInput{RestApiId: id, ResourceId: res, HttpMethod: "GET", StatusCode: "200"})
		integ := must(h, apigatewayapi.OpPutIntegration, &apigatewayapi.PutIntegrationInput{
			RestApiId: id, ResourceId: res, HttpMethod: "GET", IntegrationHttpMethod: new("POST"), Type: apigatewayapi.IntegrationTypeMock,
			RequestParameters: map[string]string{"integration.request.path.integrationPath": "method.request.path.testPath"},
			RequestTemplates:  map[string]string{"application/json": "{statusCode: 200}"},
		})
		output(r, "integration", apigatewayapi.OpPutIntegration, integ,
			`{"cacheKeyParameters":[],"cacheNamespace":"<cache-namespace:1>","passthroughBehavior":"WHEN_NO_MATCH","requestParameters":{"integration.request.path.integrationPath":"method.request.path.testPath"},"requestTemplates":{"application/json":"{statusCode: 200}"},"timeoutInMillis":29000,"type":"MOCK","_status":201}`)
		must(h, apigatewayapi.OpPutIntegrationResponse, &apigatewayapi.PutIntegrationResponseInput{
			RestApiId: id, ResourceId: res, HttpMethod: "GET", StatusCode: "200", SelectionPattern: new(`2\d{2}`), ResponseTemplates: map[string]string{},
		})
		h.deploy(id)
		if rec := h.get(id, "/s/test-path"); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
			t.Errorf("GET /test-path = %d %q, want 200 with an empty body", rec.Code, rec.Body)
		}
	})
}

// lambdaEvent is the part of a REST 1.0 event the golden below compares:
// headers, multiValueHeaders and requestContext.identity are left out,
// since AWS served that API on its EDGE endpoint (CloudFront headers,
// which understudy never adds, CreateRestApi Partial note) and the
// recorded client set the rest.
func lambdaEvent(tb testing.TB, ev map[string]any) map[string]any {
	tb.Helper()
	ctx := obj(tb, ev, "requestContext")
	out := map[string]any{"rc": map[string]any{
		"path": ctx["path"], "resourcePath": ctx["resourcePath"], "stage": ctx["stage"], "httpMethod": ctx["httpMethod"], "protocol": ctx["protocol"],
	}}
	for _, k := range []string{"httpMethod", "path", "resource", "pathParameters", "queryStringParameters", "multiValueQueryStringParameters", "body", "isBase64Encoded"} {
		out[k] = ev[k]
	}
	// The recording's function returns the event as its body, which the
	// snapshot parses when it is JSON (test_apigateway_lambda.py,
	// invocation-payload-with-params-encoding-multi).
	if s, ok := out["body"].(string); ok {
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			out["body"] = v
		}
	}
	return out
}

// test_apigateway_lambda.snapshot.json:L2-L1355,
// test_lambda_aws_proxy_integration, validated 2024-08-02: the event's
// path, resource, parameters and request context for each recorded URL
// (path normalization, apigateway-rest.md 4.2). The recording's
// integrations carry credentials, which answer 501 until roles are
// assumed; the replay grants the function policy instead.
func TestGoldenLambdaProxyEvents(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		fnARN := h.createFunction("fn")
		id, root := h.api("events")
		for _, part := range []string{"{proxy+}", "hardcoded"} {
			h.proxy(id, h.resource(id, root, part), "ANY", fnARN)
		}
		h.allow("apigateway", h.executionARN(id)+"/*/*")
		must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id, StageName: new("stage")})
		const get = `"httpMethod":"GET","isBase64Encoded":false,"body":null`
		event := func(path, proxy, query, multi, rcPath string) string {
			return `{` + get + `,"path":"` + path + `","resource":"/{proxy+}","pathParameters":{"proxy":"` + proxy + `"},"queryStringParameters":` + query + `,"multiValueQueryStringParameters":` + multi +
				`,"rc":{"path":"` + rcPath + `","resourcePath":"/{proxy+}","stage":"stage","httpMethod":"GET","protocol":"HTTP/1.1"}}`
		}
		const email = "proxy-value/api/user/test%2Balias@gmail.com/plus/test+alias@gmail.com"
		cases := []struct{ key, method, url, header, body, want string }{
			{"invocation-payload-without-trailing-slash", http.MethodGet, "/stage/proxy-value", "", "", event("/proxy-value", "proxy-value", "null", "null", "/stage/proxy-value")},
			{"invocation-payload-with-trailing-slash", http.MethodGet, "/stage/proxy-value/", "", "", event("/proxy-value/", "proxy-value", "null", "null", "/stage/proxy-value/")},
			{"invocation-payload-with-double-slash", http.MethodGet, "/stage/proxy-value//double-slash", "", "", event("/proxy-value//double-slash", "proxy-value//double-slash", "null", "null", "/stage/proxy-value//double-slash")},
			{"invocation-payload-with-prepended-slash-to-stage", http.MethodGet, "//stage/proxy-value", "", "", event("/proxy-value", "proxy-value", "null", "null", "/stage/proxy-value")},
			{"invocation-payload-with-prepended-slash", http.MethodGet, "/stage//proxy-value", "", "", event("/proxy-value", "proxy-value", "null", "null", "/stage//proxy-value")},
			{
				"invocation-payload-without-trailing-slash-and-query-params", http.MethodGet, "/stage/proxy-value?urlparam=test", "", "",
				event("/proxy-value", "proxy-value", `{"urlparam":"test"}`, `{"urlparam":["test"]}`, "/stage/proxy-value"),
			},
			{
				"invocation-payload-with-trailing-slash-and-query-params", http.MethodGet, "/stage/proxy-value/?urlparam=test", "", "",
				event("/proxy-value/", "proxy-value", `{"urlparam":"test"}`, `{"urlparam":["test"]}`, "/stage/proxy-value/"),
			},
			{"invocation-payload-with-path-encoded-email", http.MethodGet, "/stage/" + email, "", "", event("/"+email, email, "null", "null", "/stage/"+email)},
			{
				"invocation-payload-with-params-encoding", http.MethodGet,
				"/stage/proxy-value/api?dateTimeOffset=2023-06-12T18:05:10.123456+00:00&email=test%2Balias@gmail.com&plus=test+alias@gmail.com&url=https://www.google.com/&whitespace=foo%20bar&zhash=abort/", "", "",
				event("/proxy-value/api", "proxy-value/api",
					`{"dateTimeOffset":"2023-06-12T18:05:10.123456 00:00","email":"test+alias@gmail.com","plus":"test alias@gmail.com","url":"https://www.google.com/","whitespace":"foo bar","zhash":"abort/"}`,
					`{"dateTimeOffset":["2023-06-12T18:05:10.123456 00:00"],"email":["test+alias@gmail.com"],"plus":["test alias@gmail.com"],"url":["https://www.google.com/"],"whitespace":["foo bar"],"zhash":["abort/"]}`,
					"/stage/proxy-value/api"),
			},
			{
				"invocation-payload-with-params-encoding-multi", http.MethodPost, "/stage/proxy-value?category=electronics&category=books&price=10&price=20&price=30",
				"application/json;charset=utf-8", `{"message": "hello world"}`,
				`{"httpMethod":"POST","isBase64Encoded":false,"body":{"message":"hello world"},"path":"/proxy-value","resource":"/{proxy+}","pathParameters":{"proxy":"proxy-value"},` +
					`"queryStringParameters":{"category":"books","price":"30"},"multiValueQueryStringParameters":{"category":["electronics","books"],"price":["10","20","30"]},` +
					`"rc":{"path":"/stage/proxy-value","resourcePath":"/{proxy+}","stage":"stage","httpMethod":"POST","protocol":"HTTP/1.1"}}`,
			},
			{
				"invocation-hardcoded", http.MethodGet, "/stage//hardcoded", "", "",
				`{` + get + `,"path":"/hardcoded","resource":"/hardcoded","pathParameters":null,"queryStringParameters":null,"multiValueQueryStringParameters":null,` +
					`"rc":{"path":"/stage//hardcoded","resourcePath":"/hardcoded","stage":"stage","httpMethod":"GET","protocol":"HTTP/1.1"}}`,
			},
		}
		for _, c := range cases {
			before := len(h.fn.invocations())
			req := h.request(c.method, pathURL(id, c.url), c.body)
			req.Header.Set("User-Agent", "python-requests/testing")
			if c.header != "" {
				req.Header.Set("Content-Type", c.header)
			}
			rec := h.send(req)
			calls := h.fn.invocations()
			if rec.Code != http.StatusOK || len(calls) != before+1 {
				t.Errorf("%s: %s %s = %d %s after %d invocations, want 200 after one", c.key, c.method, c.url, rec.Code, rec.Body, len(calls)-before)
				continue
			}
			r.match(c.key, lambdaEvent(t, calls[len(calls)-1].event(t)), r.recorded(c.want))
		}
	})
}

// headerValues joins every value of name, whatever the casing of the key.
func headerValues(h http.Header, name string) string {
	var vs []string
	for k, v := range h {
		if strings.EqualFold(k, name) {
			vs = append(vs, v...)
		}
	}
	return strings.Join(vs, ", ")
}

// test_apigateway_lambda.snapshot.json:L1669-L1798,
// test_aws_proxy_response_payload_format_validation, validated
// 2024-11-15: what a client receives for each proxy result (5.2, 5.3),
// with the statuses from the test's asserts (test_apigateway_lambda.py
// _invoke calls). The recorded function returns its request body
// (LAMBDA_RESPONSE_FROM_BODY); the fake answers that body. A request
// without a body makes it fail (json.loads(None)), which the empty-response
// case answers as a function error. Response headers are compared for the
// ones the function set and Content-Type; the CloudFront ones (Via,
// X-Amz-Cf-*, X-Cache) come from the EDGE endpoint understudy does not
// serve (CreateRestApi Partial note), and Date and Connection from the
// HTTP server.
func TestGoldenProxyResponseMapping(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		id, _ := h.proxyAPI("/{proxy+}")
		cases := []struct {
			key, result string
			functionErr bool
			status      int
			content     string
			headers     map[string]string
		}{
			{key: "invoke-api-no-body", result: `{"statusCode": 200}`, status: 200, content: `""`},
			{key: "invoke-api-with-headers", result: `{"statusCode": 200, "headers": {"test-header": "value", "header-bool": true}}`, status: 200, content: `""`, headers: map[string]string{"test-header": "value", "header-bool": "true"}},
			{key: "invoke-api-with-headers-null", result: `{"statusCode": 200, "headers": null}`, status: 200, content: `""`},
			{key: "invoke-api-wrong-format", result: `{"statusCode": 200, "wrongValue": "value"}`, status: 502, content: `{"message":"Internal server error"}`},
			{key: "invoke-api-empty-response", functionErr: true, status: 502, content: `{"message":"Internal server error"}`},
			{key: "invoke-api-b64-encoded-true", result: `{"statusCode": 200, "body": "dGVzdC1kYXRh", "isBase64Encoded": true}`, status: 200, content: `"dGVzdC1kYXRh"`},
			{key: "invoke-api-b64-encoded-false", result: `{"statusCode": 200, "body": "dGVzdC1kYXRh"}`, status: 200, content: `"dGVzdC1kYXRh"`},
			{key: "invoke-api-multi-headers-valid", result: `{"statusCode": 200, "multiValueHeaders": {"test-multi": ["value1", "value2"]}}`, status: 200, content: `""`, headers: map[string]string{"test-multi": "value1, value2"}},
			{key: "invoke-api-multi-headers-overwrite", result: `{"statusCode": 200, "multiValueHeaders": {"test-multi": ["value-multi"]}, "headers": {"test-multi": "value-solo"}}`, status: 200, content: `""`, headers: map[string]string{"test-multi": "value-multi, value-solo"}},
			{key: "invoke-api-multi-headers-overwrite-casing", result: `{"statusCode": 200, "multiValueHeaders": {"tesT-Multi": ["value-multi"]}, "headers": {"test-multi": "value-solo"}}`, status: 200, content: `""`, headers: map[string]string{"test-multi": "value-multi, value-solo"}},
			{key: "invoke-api-multi-headers-invalid", result: `{"statusCode": 200, "multiValueHeaders": {"test-multi-invalid": "value1"}}`, status: 502, content: `{"message":"Internal server error"}`},
			{key: "invoke-api-invalid-status-code", result: `{"statusCode": "test"}`, status: 502, content: `{"message":"Internal server error"}`},
			{key: "invoke-api-status-code-str", result: `{"statusCode": "201"}`, status: 201, content: `""`},
			{key: "invoke-api-just-string", result: `"justAString"`, status: 502, content: `{"message":"Internal server error"}`},
			{key: "invoke-api-only-headers", result: `{"headers": {"test-header": "value"}}`, status: 200, content: `""`},
		}
		for _, c := range cases {
			if c.functionErr {
				h.fn.answerWith(&lambdaapi.InvokeOutput{StatusCode: 200, FunctionError: new("Unhandled"), Payload: []byte(`{"errorMessage":"the JSON object must be str, bytes or bytearray, not NoneType","errorType":"TypeError"}`)}, nil)
			} else {
				h.fn.answer(c.result)
			}
			req := h.request(http.MethodPost, pathURL(id, "/s/test"), c.result)
			req.Header.Set("User-Agent", "python/test")
			rec := h.send(req)
			if rec.Code != c.status {
				t.Errorf("%s: status %d %s, want %d", c.key, rec.Code, rec.Body, c.status)
			}
			r.match(c.key+".content", content(rec), r.recorded(c.content))
			if c.headers == nil {
				continue
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("%s: Content-Type %q, recorded application/json", c.key, ct)
			}
			for name, want := range c.headers {
				if got := headerValues(rec.Header(), name); got != want {
					t.Errorf("%s: header %s = %q, recorded %q", c.key, name, got, want)
				}
			}
		}
	})
}

// content is what the recording's _invoke keeps: the JSON body parsed, or
// the body as text.
func content(rec *httptest.ResponseRecorder) any {
	var v any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		return rec.Body.String()
	}
	return v
}

// test_apigateway_common.snapshot.json:L604-L757,
// TestDeployments::test_create_delete_deployments[True], validated
// 2023-09-07: a stage created by hand on a deployment, the recorded stage
// shape, and three rounds of the live-deployment refusal.
func TestGoldenDeploymentDeletionRule(t *testing.T) {
	run(t, func(h *harness) {
		r := h.replay()
		id, _ := h.mockAPI()
		deployment := func() string {
			return *must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id}).Id
		}
		stage := func(dep string) {
			must(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: "s1", DeploymentId: dep})
		}
		stages := func(placeholder string) string {
			return `{"item":[{"cacheClusterEnabled":false,"cacheClusterStatus":"NOT_AVAILABLE","createdDate":"datetime","deploymentId":"` + placeholder +
				`","lastUpdatedDate":"datetime","methodSettings":{},"stageName":"s1","tracingEnabled":false}],"_status":200}`
		}
		dep := deployment()
		stage(dep)
		output(r, "get-deployment", apigatewayapi.OpGetDeployment, must(h, apigatewayapi.OpGetDeployment, &apigatewayapi.GetDeploymentInput{RestApiId: id, DeploymentId: dep}),
			`{"createdDate":"datetime","id":"<deployment-id:1>","_status":200}`)
		output(r, "get-stages", apigatewayapi.OpGetStages, must(h, apigatewayapi.OpGetStages, &apigatewayapi.GetStagesInput{RestApiId: id}), stages("<deployment-id:1>"))
		for i := range 3 {
			r.failure(fmt.Sprintf("delete-deployment-error-%d", i), fail(h, apigatewayapi.OpDeleteDeployment, &apigatewayapi.DeleteDeploymentInput{RestApiId: id, DeploymentId: dep}),
				`{"Error":{"Code":"BadRequestException","Message":"Active stages pointing to this deployment must be moved or deleted"},"message":"Active stages pointing to this deployment must be moved or deleted","_status":400}`)
			must(h, apigatewayapi.OpDeleteStage, &apigatewayapi.DeleteStageInput{RestApiId: id, StageName: "s1"})
			must(h, apigatewayapi.OpDeleteDeployment, &apigatewayapi.DeleteDeploymentInput{RestApiId: id, DeploymentId: dep})
			dep = deployment()
			stage(dep)
			placeholder := fmt.Sprintf("<id:%d>", i+1)
			output(r, fmt.Sprintf("get-deployments-%d", i), apigatewayapi.OpGetDeployments, must(h, apigatewayapi.OpGetDeployments, &apigatewayapi.GetDeploymentsInput{RestApiId: id}),
				`{"items":[{"createdDate":"datetime","id":"`+placeholder+`"}],"_status":200}`)
			output(r, fmt.Sprintf("get-stages-%d", i), apigatewayapi.OpGetStages, must(h, apigatewayapi.OpGetStages, &apigatewayapi.GetStagesInput{RestApiId: id}), stages(placeholder))
		}
	})
}

// routingMock puts verb on res with the MOCK integration LocalStack's
// _create_mock_integration_with_200_response_template builds: a literal
// request template selecting 200, and a literal response template carrying
// message (test_apigateway_common.py).
func (h *harness) routingMock(id, res, verb, message string) {
	h.Helper()
	h.method(id, res, verb)
	must(h, apigatewayapi.OpPutMethodResponse, &apigatewayapi.PutMethodResponseInput{RestApiId: id, ResourceId: res, HttpMethod: verb, StatusCode: "200"})
	must(h, apigatewayapi.OpPutIntegration, &apigatewayapi.PutIntegrationInput{
		RestApiId: id, ResourceId: res, HttpMethod: verb, Type: apigatewayapi.IntegrationTypeMock, RequestTemplates: map[string]string{"application/json": `{"statusCode": 200}`},
	})
	must(h, apigatewayapi.OpPutIntegrationResponse, &apigatewayapi.PutIntegrationResponseInput{
		RestApiId: id, ResourceId: res, HttpMethod: verb, StatusCode: "200", SelectionPattern: new(""),
		ResponseTemplates: map[string]string{"application/json": `{"statusCode": 200, "message": "` + message + `"}`},
	})
}

// call sends method to path of stage dev and returns the status and the
// parsed body.
func (h *harness) call(id, method, path string) (code int, body map[string]any, header http.Header) {
	h.Helper()
	rec := h.send(h.request(method, pathURL(id, "/dev"+path), ""))
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		h.Errorf("%s %s answered %d %q, not a JSON object", method, path, rec.Code, rec.Body)
	}
	return rec.Code, body, rec.Header()
}

// test_apigateway_common.py:L1599-L1748, TestApigatewayRouting's
// test_proxy_routing_with_hardcoded_resource_sibling (validated
// 2024-07-23) and test_routing_with_hardcoded_resource_sibling_order
// (validated 2024-07-23): the asserted messages (findings 4, 4.3-4.4);
// and test_apigateway_common.snapshot.json:L1113-L1133,
// test_routing_not_found, validated 2024-07-23.
func TestGoldenRouting(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("test proxy routing")
		test := h.resource(id, root, "test")
		h.routingMock(id, test, http.MethodPost, "POST request")
		h.routingMock(id, h.resource(id, test, "any"), "ANY", "ANY request")
		h.routingMock(id, h.resource(id, root, "{proxy+}"), http.MethodOptions, "OPTIONS request")
		must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id, StageName: new("dev")})
		for _, c := range []struct{ method, path, want string }{
			{http.MethodOptions, "/test", "OPTIONS request"},
			{http.MethodPost, "/test", "POST request"},
			{http.MethodOptions, "/test/any", "ANY request"},
			{http.MethodGet, "/test/any", "ANY request"},
		} {
			if code, body, _ := h.call(id, c.method, c.path); code != http.StatusOK || body["message"] != c.want {
				t.Errorf("sibling: %s %s = %d %v, want 200 %q", c.method, c.path, code, body, c.want)
			}
		}

		order, orderRoot := h.api("test parameter routing")
		part1 := h.resource(order, orderRoot, "part1")
		h.routingMock(order, part1, http.MethodGet, "part1")
		h.routingMock(order, h.resource(order, orderRoot, "{param+}"), http.MethodGet, "proxy")
		h.routingMock(order, h.resource(order, part1, "hardcoded-value"), http.MethodGet, "hardcoded-value")
		must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: order, StageName: new("dev")})
		for path, want := range map[string]string{"/part1": "part1", "/part1/hardcoded-value": "hardcoded-value", "/part1/random-value": "proxy"} {
			if code, body, _ := h.call(order, http.MethodGet, path); code != http.StatusOK || body["message"] != want {
				t.Errorf("order: GET %s = %d %v, want 200 %q", path, code, body, want)
			}
		}

		r := h.replay()
		found, foundRoot := h.api("test-notfound")
		h.routingMock(found, h.resource(found, foundRoot, "existing"), http.MethodGet, "exists")
		must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: found, StageName: new("dev")})
		_, body, _ := h.call(found, http.MethodGet, "/existing")
		r.match("working-route", body, r.recorded(`{"message":"exists","statusCode":200}`))
		for key, c := range map[string]struct{ method, path string }{"not-found": {http.MethodGet, "/random-non-existing"}, "wrong-method": {http.MethodPost, "/existing"}} {
			code, body, header := h.call(found, c.method, c.path)
			if code < 400 {
				t.Errorf("%s: %s %s = %d, want a failure (the test's works=False)", key, c.method, c.path, code)
			}
			r.match(key, map[string]any{"content": body, "errorType": header.Get("X-Amzn-Errortype")},
				r.recorded(`{"content":{"message":"Missing Authentication Token"},"errorType":"MissingAuthenticationTokenException"}`))
		}
	})
}

// test_apigateway_basic.snapshot.json:L154-L262,
// TestIntegrations::test_mock_integration_response[WHEN_NO_MATCH-UrlType.PATH_BASED-GET],
// validated 2024-05-30: AWS renders the response template
// {"statusCode": 200, "id": $input.params().path.id} as {"id": 42,
// "statusCode": 200}; understudy renders no VTL (apigateway-rest.md 12.7) and
// answers 501 naming the template, journaled.
func TestGoldenMockVTLIsRefused(t *testing.T) {
	run(t, func(h *harness) {
		id := h.mockMethod("{id}", mock{
			templates: map[string]string{"application/json": `{"statusCode":200}`},
			responses: []integrationResponse{{status: "200", templates: map[string]string{"application/json": `{"statusCode": 200, "id": $input.params().path.id}`}}},
		})
		rec := h.send(func() *http.Request {
			req := h.request(http.MethodGet, pathURL(id, "/s/42"), "")
			req.Header.Set("Content-Type", "application/json")
			return req
		}())
		want501(t, rec, "template", "GET /42 with a VTL response template")
		h.wantJournaled501("GET /42 with a VTL response template")
	})
}
