package apigateway_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

// CreateRestApi answers the recorded defaults: apiKeySource HEADER,
// disableExecuteApiEndpoint false, endpointConfiguration ipv4 EDGE when
// none is given, and no description, binaryMediaTypes or policy
// (aws-recorded, test_apigateway_api.snapshot.json:L4618-L4692,
// apigateway-rest.md 2.3); an id, a createdDate, a root resource id
// (3.1) and apiStatus per switch apigateway.restapi.api-status. GetRestApi
// answers the same.
func TestCreateRestApiDefaults(t *testing.T) {
	run(t, func(h *harness) {
		out := h.createAPI("t")
		status := apigateway.APIStatusSwitch().Value
		for name, api := range map[string]*apigatewayapi.RestApi{"CreateRestApi": out, "GetRestApi": must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: *out.Id})} {
			if deref(api.Name) != "t" || deref(api.Id) != *out.Id || api.CreatedDate == nil || deref(api.RootResourceId) != *out.RootResourceId {
				t.Errorf("%s = %+v, want name t, the id, a createdDate and the root resource id", name, api)
			}
			if deref(api.ApiKeySource) != apigatewayapi.ApiKeySourceTypeHeader || api.DisableExecuteApiEndpoint {
				t.Errorf("%s apiKeySource %v, disableExecuteApiEndpoint %v; want HEADER, false", name, deref(api.ApiKeySource), api.DisableExecuteApiEndpoint)
			}
			ec := api.EndpointConfiguration
			if ec == nil || deref(ec.IpAddressType) != apigatewayapi.IpAddressTypeIpv4 || !slices.Equal(ec.Types, []apigatewayapi.EndpointType{apigatewayapi.EndpointTypeEdge}) {
				t.Errorf("%s endpointConfiguration = %+v, want ipv4 [EDGE]", name, ec)
			}
			if api.Description != nil || api.BinaryMediaTypes != nil || api.Policy != nil {
				t.Errorf("%s description %v, binaryMediaTypes %v, policy %v; want none", name, api.Description, api.BinaryMediaTypes, api.Policy)
			}
			if deref(api.ApiStatus) != status.AfterCreate {
				t.Errorf("%s apiStatus = %v, want %v (switch apigateway.restapi.api-status)", name, deref(api.ApiStatus), status.AfterCreate)
			}
		}
	})
}

// A PRIVATE API defaults to dualstack (aws-recorded,
// test_apigateway_api.snapshot.json:L4618-L4692, apigateway-rest.md 2.3).
func TestCreatePrivateDefaultsToDualstack(t *testing.T) {
	run(t, func(h *harness) {
		out := must(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "p", EndpointConfiguration: &apigatewayapi.EndpointConfiguration{Types: []apigatewayapi.EndpointType{apigatewayapi.EndpointTypePrivate}}})
		if ec := out.EndpointConfiguration; ec == nil || deref(ec.IpAddressType) != apigatewayapi.IpAddressTypeDualstack {
			t.Errorf("endpointConfiguration = %+v, want dualstack", ec)
		}
	})
}

// Recorded CreateRestApi refusals (aws-recorded, apigateway-rest.md 2.4
// table, test_apigateway_api.snapshot.json lines beside each), and
// cloneFrom's NotImplemented (the unit's conflict row 4).
func TestCreateRestApiRefusals(t *testing.T) {
	run(t, func(h *harness) {
		wantText(t, fail(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "t", Description: new("")}),
			badRequest, "Description cannot be an empty string") // L146-L227
		for _, size := range []int32{-1, 10_485_761} {
			wantText(t, fail(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "t", MinimumCompressionSize: new(size)}),
				badRequest, "Invalid minimum compression size, must be between 0 and 10485760") // L378-L477
		}
		wantText(t, fail(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "t", EndpointConfiguration: &apigatewayapi.EndpointConfiguration{
			Types: []apigatewayapi.EndpointType{apigatewayapi.EndpointTypeEdge, apigatewayapi.EndpointTypeRegional},
		}}), badRequest, "Cannot create an api with multiple Endpoint Types.") // L4457-L4580
		wantText(t, fail(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "t", EndpointConfiguration: &apigatewayapi.EndpointConfiguration{
			Types: []apigatewayapi.EndpointType{apigatewayapi.EndpointTypePrivate}, IpAddressType: new(apigatewayapi.IpAddressTypeIpv4),
		}}), badRequest, "Only dualstack ipAddressType is supported for Private APIs.") // L4581-L4617
		other, _ := h.api("source")
		wantNotImplemented(t, fail(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "t", CloneFrom: new(other)}), "cloneFrom")
		if got := must(h, apigatewayapi.OpGetRestApis, &apigatewayapi.GetRestApisInput{}).Items; len(got) != 1 {
			t.Errorf("GetRestApis after the refusals = %d APIs, want only the source", len(got))
		}
	})
}

// An unknown REST API id answers NotFoundException "Invalid API identifier
// specified <account>:<id>", the caller's account; ids are case-sensitive
// (aws-recorded, test_apigateway_api.snapshot.json:L112-L144,
// apigateway-rest.md 2.4). DeleteRestApi removes the API, and GetRestApi
// then answers the same 404 (3.1: Terraform waits on it).
func TestUnknownAndDeletedRestApi(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.api("t")
		upper := strings.ToUpper(id)
		if upper != id {
			wantText(t, fail(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: upper}), notFound, "Invalid API identifier specified "+account+":"+upper)
		}
		must(h, apigatewayapi.OpDeleteRestApi, &apigatewayapi.DeleteRestApiInput{RestApiId: id})
		wantText(t, fail(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id}), notFound, "Invalid API identifier specified "+account+":"+id)
		wantText(t, fail(h, apigatewayapi.OpDeleteRestApi, &apigatewayapi.DeleteRestApiInput{RestApiId: id}), notFound, "Invalid API identifier specified "+account+":"+id)
	})
}

// GetRestApis pages with limit and position ([S] @paginated,
// apigateway-rest.md 2.5); every API appears once across the pages (the
// order is not asserted).
func TestGetRestApisPages(t *testing.T) {
	run(t, func(h *harness) {
		want := map[string]bool{}
		for _, n := range []string{"a", "b", "c"} {
			id, _ := h.api(n)
			want[id] = true
		}
		seen := map[string]bool{}
		var position *string
		for range 4 {
			page := must(h, apigatewayapi.OpGetRestApis, &apigatewayapi.GetRestApisInput{Limit: new(int32(2)), Position: position})
			if len(page.Items) > 2 {
				t.Fatalf("a page of limit 2 held %d items", len(page.Items))
			}
			for _, api := range page.Items {
				if seen[deref(api.Id)] {
					t.Errorf("API %s listed twice", deref(api.Id))
				}
				seen[deref(api.Id)] = true
			}
			if position = page.Position; position == nil {
				break
			}
		}
		if len(seen) != len(want) {
			t.Errorf("pages listed %v, want %v", seen, want)
		}
	})
}

func op(o apigatewayapi.Op, path string, value ...string) apigatewayapi.PatchOperation {
	p := apigatewayapi.PatchOperation{Op: &o, Path: &path}
	if len(value) > 0 {
		p.Value = &value[0]
	}
	return p
}

// UpdateRestApi's binaryMediaTypes is a list addressed as an object
// (aws-recorded, test_apigateway_api.snapshot.json:L303-L377 and
// L478-L537, apigateway-rest.md finding 5 and 3.8): add of an escaped type
// with no value appends it; add /binaryMediaTypes/- with a value appends
// the literal "-"; replace removes the type and appends the value; add
// /binaryMediaTypes is refused with "Invalid patch path /binaryMediaTypes".
// Responses carry "tags": {} (L324, 2.3).
func TestUpdateRestApiBinaryMediaTypes(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.api("t")
		update := func(ops ...apigatewayapi.PatchOperation) *apigatewayapi.RestApi {
			return must(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: ops})
		}
		out := update(op(apigatewayapi.OpAdd, "/binaryMediaTypes/image~1png"), op(apigatewayapi.OpAdd, "/binaryMediaTypes/image~1jpeg"))
		if !slices.Equal(out.BinaryMediaTypes, []string{"image/png", "image/jpeg"}) {
			t.Errorf("after two adds binaryMediaTypes = %q, want [image/png image/jpeg]", out.BinaryMediaTypes)
		}
		if out.Tags == nil || len(out.Tags) != 0 {
			t.Errorf("UpdateRestApi tags = %#v, want an empty map", out.Tags)
		}
		out = update(op(apigatewayapi.OpReplace, "/binaryMediaTypes/image~1png", "image/gif"))
		if !slices.Equal(out.BinaryMediaTypes, []string{"image/jpeg", "image/gif"}) {
			t.Errorf("after replace binaryMediaTypes = %q, want [image/jpeg image/gif]", out.BinaryMediaTypes)
		}
		out = update(op(apigatewayapi.OpAdd, "/binaryMediaTypes/-", "image/png"))
		if !slices.Contains(out.BinaryMediaTypes, "-") || slices.Contains(out.BinaryMediaTypes, "image/png") {
			t.Errorf("after add /binaryMediaTypes/- binaryMediaTypes = %q, want the literal - appended", out.BinaryMediaTypes)
		}
		out = update(op(apigatewayapi.OpRemove, "/binaryMediaTypes/image~1gif"))
		if slices.Contains(out.BinaryMediaTypes, "image/gif") {
			t.Errorf("after remove binaryMediaTypes = %q, want image/gif gone", out.BinaryMediaTypes)
		}
		before := must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id}).BinaryMediaTypes
		wantText(t, fail(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/description", "d"), op(apigatewayapi.OpAdd, "/binaryMediaTypes", "image/png"),
		}}), badRequest, "Invalid patch path /binaryMediaTypes")
		after := must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id})
		if !slices.Equal(after.BinaryMediaTypes, before) || after.Description != nil {
			t.Errorf("a refused update changed the API to %+v", after)
		}
	})
}

// Recorded UpdateRestApi refusals and the patch op enum: an op other than
// replace on /minimumCompressionSize (L468), an op other than add, remove
// or replace on /endpointConfiguration/types/* (L4774), and ops outside
// the enum, one 1-based clause per bad entry (L4803-L4837) (aws-recorded,
// test_apigateway_api.snapshot.json; apigateway-rest.md 3.8). replace
// /name and /description apply, and apiStatus after an update follows
// switch apigateway.restapi.api-status.
func TestUpdateRestApi(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.api("t")
		out := must(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/name", "renamed"), op(apigatewayapi.OpReplace, "/description", "d"),
		}})
		if deref(out.Name) != "renamed" || deref(out.Description) != "d" || deref(out.ApiStatus) != apigateway.APIStatusSwitch().Value.AfterUpdate {
			t.Errorf("UpdateRestApi = name %q, description %q, apiStatus %v; want renamed, d, the switch's AfterUpdate", deref(out.Name), deref(out.Description), deref(out.ApiStatus))
		}
		wantText(t, fail(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpAdd, "/minimumCompressionSize", "10"),
		}}), badRequest, "Invalid patch operation specified. Must be one of: [replace]")
		wantText(t, fail(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpMove, "/endpointConfiguration/types/EDGE", "REGIONAL"),
		}}), badRequest, "Invalid patch operation specified. Must be 'add'|'remove'|'replace'")
		clause := func(i string) string {
			return "Value 'wrong' at 'updateRestApiInput.patchOperations." + i + ".member.op' failed to satisfy constraint: Member must satisfy enum value set: [add, remove, move, test, replace, copy]"
		}
		wantText(t, fail(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: []apigatewayapi.PatchOperation{
			op("wrong", "/name", "a"), op("wrong", "/name", "b"),
		}}), validation, "2 validation errors detected: "+clause("1")+"; "+clause("2"))
		wantText(t, fail(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/name", "a"), op("wrong", "/name", "b"),
		}}), validation, "1 validation error detected: "+clause("2"))
		if got := must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id}); deref(got.Name) != "renamed" {
			t.Errorf("name after refused updates = %q, want renamed", deref(got.Name))
		}
	})
}

// The resource policy is returned as a string with its quotes
// backslash-escaped, and a Resource of execute-api:/* comes back as the
// API's execute-api ARN (aws-recorded,
// test_apigateway_integrations.snapshot.json:L45-L77; apigateway-rest.md
// 2.3).
func TestPolicyIsReturnedEscaped(t *testing.T) {
	run(t, func(h *harness) {
		policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"execute-api:Invoke","Resource":["execute-api:/*"]}]}`
		out := must(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "t", Policy: new(policy)})
		got := deref(must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: *out.Id}).Policy)
		if !strings.Contains(got, `\"Version\"`) || strings.Contains(got, `{"Version"`) {
			t.Errorf("policy = %s, want its quotes backslash-escaped", got)
		}
		if !strings.Contains(got, h.executionARN(*out.Id)+"/*") || strings.Contains(got, "execute-api:/*") {
			t.Errorf("policy = %s, want execute-api:/* rewritten to %s/*", got, h.executionARN(*out.Id))
		}
	})
}
