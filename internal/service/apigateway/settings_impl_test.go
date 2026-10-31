package apigateway_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

// CreateRestApi stores the settings it accepts and returns them: a
// REGIONAL endpoint on dualstack with VPC endpoint ids, minimumCompressionSize
// at the recorded bound, apiKeySource, securityPolicy, endpointAccessMode
// (apigateway-rest.md 2.3, 3.1; stored without effect per the spec 6b
// note).
func TestCreateRestApiStoresSettings(t *testing.T) {
	run(t, func(h *harness) {
		out := must(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{
			Name: "t", MinimumCompressionSize: new(int32(10_485_760)), ApiKeySource: new(apigatewayapi.ApiKeySourceTypeAuthorizer),
			EndpointConfiguration: &apigatewayapi.EndpointConfiguration{
				Types: []apigatewayapi.EndpointType{apigatewayapi.EndpointTypeRegional}, IpAddressType: new(apigatewayapi.IpAddressTypeDualstack), VpcEndpointIds: []string{"vpce-1"},
			},
			SecurityPolicy: new(apigatewayapi.SecurityPolicyTls12), EndpointAccessMode: new(apigatewayapi.EndpointAccessModeStrict),
			BinaryMediaTypes: []string{"image/png"}, DisableExecuteApiEndpoint: true,
		})
		got := must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: *out.Id})
		ec := got.EndpointConfiguration
		if ec == nil || !slices.Equal(ec.Types, []apigatewayapi.EndpointType{apigatewayapi.EndpointTypeRegional}) ||
			deref(ec.IpAddressType) != apigatewayapi.IpAddressTypeDualstack || !slices.Equal(ec.VpcEndpointIds, []string{"vpce-1"}) {
			t.Errorf("endpointConfiguration = %+v, want REGIONAL dualstack [vpce-1]", ec)
		}
		if deref(got.MinimumCompressionSize) != 10_485_760 || deref(got.ApiKeySource) != apigatewayapi.ApiKeySourceTypeAuthorizer ||
			deref(got.SecurityPolicy) != apigatewayapi.SecurityPolicyTls12 || deref(got.EndpointAccessMode) != apigatewayapi.EndpointAccessModeStrict ||
			!got.DisableExecuteApiEndpoint || !slices.Equal(got.BinaryMediaTypes, []string{"image/png"}) {
			t.Errorf("GetRestApi = %+v, want the settings sent", got)
		}
	})
}

// Every documented UpdateRestApi path applies (apigateway-rest.md 3.8):
// Terraform's replace /endpointConfiguration/types/0, the VPC endpoint id
// list, the replace-only members, "" unsetting minimumCompressionSize and
// the policy; the policy is rewritten and escaped as on create (2.3).
func TestUpdateRestApiPathsApply(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.api("t")
		update := func(ops ...apigatewayapi.PatchOperation) *apigatewayapi.RestApi {
			h.Helper()
			return must(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: ops})
		}
		out := update(
			op(apigatewayapi.OpReplace, "/endpointConfiguration/types/0", "REGIONAL"),
			op(apigatewayapi.OpReplace, "/endpointConfiguration/ipAddressType", "dualstack"),
			op(apigatewayapi.OpAdd, "/endpointConfiguration/vpcEndpointIds", "vpce-1"),
			op(apigatewayapi.OpAdd, "/endpointConfiguration/vpcEndpointIds", "vpce-2"),
			op(apigatewayapi.OpRemove, "/endpointConfiguration/vpcEndpointIds", "vpce-1"),
			op(apigatewayapi.OpReplace, "/apiKeySource", "AUTHORIZER"),
			op(apigatewayapi.OpReplace, "/minimumCompressionSize", "10"),
			op(apigatewayapi.OpReplace, "/securityPolicy", "TLS_1_2"),
			op(apigatewayapi.OpReplace, "/endpointAccessMode", "STRICT"),
			op(apigatewayapi.OpReplace, "/disableExecuteApiEndpoint", "true"),
			op(apigatewayapi.OpReplace, "/policy", `{"Statement":[{"Resource":"execute-api:/s/GET/"}]}`),
		)
		ec := out.EndpointConfiguration
		if ec == nil || !slices.Equal(ec.Types, []apigatewayapi.EndpointType{apigatewayapi.EndpointTypeRegional}) ||
			deref(ec.IpAddressType) != apigatewayapi.IpAddressTypeDualstack || !slices.Equal(ec.VpcEndpointIds, []string{"vpce-2"}) {
			t.Errorf("endpointConfiguration = %+v, want REGIONAL dualstack [vpce-2]", ec)
		}
		if deref(out.ApiKeySource) != apigatewayapi.ApiKeySourceTypeAuthorizer || deref(out.MinimumCompressionSize) != 10 ||
			deref(out.SecurityPolicy) != apigatewayapi.SecurityPolicyTls12 || deref(out.EndpointAccessMode) != apigatewayapi.EndpointAccessModeStrict || !out.DisableExecuteApiEndpoint {
			t.Errorf("UpdateRestApi = %+v, want every replaced member", out)
		}
		want := `{\"Statement\":[{\"Resource\":\"` + h.executionARN(id) + `/s/GET/\"}]}`
		if got := deref(out.Policy); got != want {
			t.Errorf("policy = %s, want %s", got, want)
		}
		out = update(op(apigatewayapi.OpReplace, "/minimumCompressionSize", ""), op(apigatewayapi.OpReplace, "/policy", ""))
		if out.MinimumCompressionSize != nil || out.Policy != nil {
			t.Errorf("after replace with \"\": minimumCompressionSize %v, policy %v; want both unset", out.MinimumCompressionSize, out.Policy)
		}
	})
}

// UpdateRestApi refusals change nothing: a minimumCompressionSize out of
// range answers the recorded text (apigateway-rest.md 2.4); a PRIVATE type
// on ipv4 the recorded CreateRestApi text; a second endpoint type the
// recorded multiple-types text; an unknown enum value and a
// disableExecuteApiEndpoint that is not a boolean are refused (texts
// inferred, status only).
func TestUpdateRestApiSettingRefusals(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.api("t")
		try := func(o apigatewayapi.PatchOperation) *apigatewayapi.UpdateRestApiInput {
			return &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: []apigatewayapi.PatchOperation{op(apigatewayapi.OpReplace, "/name", "changed"), o}}
		}
		for _, v := range []string{"-1", "10485761", "x"} {
			wantText(t, fail(h, apigatewayapi.OpUpdateRestApi, try(op(apigatewayapi.OpReplace, "/minimumCompressionSize", v))), badRequest,
				"Invalid minimum compression size, must be between 0 and 10485760")
		}
		wantText(t, fail(h, apigatewayapi.OpUpdateRestApi, try(op(apigatewayapi.OpReplace, "/endpointConfiguration/types/0", "PRIVATE"))), badRequest,
			"Only dualstack ipAddressType is supported for Private APIs.")
		wantText(t, fail(h, apigatewayapi.OpUpdateRestApi, try(op(apigatewayapi.OpAdd, "/endpointConfiguration/types/REGIONAL"))), badRequest,
			"Cannot create an api with multiple Endpoint Types.")
		wantError(t, fail(h, apigatewayapi.OpUpdateRestApi, try(op(apigatewayapi.OpRemove, "/endpointConfiguration/types/EDGE"))), badRequest)
		wantError(t, fail(h, apigatewayapi.OpUpdateRestApi, try(op(apigatewayapi.OpReplace, "/apiKeySource", "BODY"))), badRequest)
		wantError(t, fail(h, apigatewayapi.OpUpdateRestApi, try(op(apigatewayapi.OpReplace, "/disableExecuteApiEndpoint", "yes"))), badRequest)
		got := must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id})
		if deref(got.Name) != "t" || got.MinimumCompressionSize != nil || got.DisableExecuteApiEndpoint ||
			!slices.Equal(got.EndpointConfiguration.Types, []apigatewayapi.EndpointType{apigatewayapi.EndpointTypeEdge}) {
			t.Errorf("after refused updates = %+v, want the API unchanged", got)
		}
		// PRIVATE with dualstack in the same request is accepted.
		out := must(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/endpointConfiguration/types/0", "PRIVATE"), op(apigatewayapi.OpReplace, "/endpointConfiguration/ipAddressType", "dualstack"),
		}})
		if !slices.Equal(out.EndpointConfiguration.Types, []apigatewayapi.EndpointType{apigatewayapi.EndpointTypePrivate}) {
			t.Errorf("endpointConfiguration = %+v, want PRIVATE", out.EndpointConfiguration)
		}
	})
}

// The live settings come before the stage (contract 7.7 steps 3-4): a
// disabled default endpoint or a PRIVATE API answers 501 even for a stage
// that does not exist; clearing disableExecuteApiEndpoint serves again at
// once, without a deployment.
func TestLiveSettingsPrecedeTheStage(t *testing.T) {
	run(t, func(h *harness) {
		id := h.proxyRoot(&apigatewayapi.CreateRestApiInput{Name: "t", DisableExecuteApiEndpoint: true}, nil)
		h.deploy(id)
		want501(t, h.get(id, "/nostage/"), "disableExecuteApiEndpoint", "an unknown stage of a disabled API")
		must(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/disableExecuteApiEndpoint", "false"),
		}})
		if rec := h.get(id, "/s/"); rec.Code != http.StatusOK {
			t.Errorf("after enabling the endpoint: GET = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		private := must(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: "p", EndpointConfiguration: &apigatewayapi.EndpointConfiguration{
			Types: []apigatewayapi.EndpointType{apigatewayapi.EndpointTypePrivate},
		}})
		want501(t, h.get(*private.Id, "/s/"), "PRIVATE", "a PRIVATE API without stages")
		h.wantJournaled501("PRIVATE")
	})
}

// The resource policy is read live and comes before routing (contract 7.7
// step 6): under enforce a path no resource serves still answers 501, and
// removing the policy serves at once; under advise the advisory names the
// API's ARN and is recorded even when routing then answers 403.
func TestResourcePolicyIsLiveAndPrecedesRouting(t *testing.T) {
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"execute-api:Invoke","Resource":"execute-api:/*"}]}`
	runMode(t, apigateway.EnforceEnforce, func(h *harness) {
		id := h.proxyRoot(&apigatewayapi.CreateRestApiInput{Name: "t", Policy: new(policy)}, nil)
		h.deploy(id)
		want501(t, h.get(id, "/s/nowhere"), "policy", "an unrouted path under enforce")
		must(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/policy", ""),
		}})
		if rec := h.get(id, "/s/"); rec.Code != http.StatusOK {
			t.Errorf("after removing the policy: GET = %d %s, want the function's 200", rec.Code, rec.Body)
		}
	})
	runMode(t, apigateway.EnforceAdvise, func(h *harness) {
		id := h.proxyRoot(&apigatewayapi.CreateRestApiInput{Name: "t", Policy: new(policy)}, nil)
		h.deploy(id)
		wantAnswer(t, h.get(id, "/s/nowhere"), apigateway.MissingAuthenticationToken(), "an unrouted path under advise")
		var resources []string
		for _, e := range h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory, Limit: 1000}).Events {
			if a, ok := e.Detail.(journal.Advisory); ok && a.Kind == journal.AdvisoryAccessUnevaluated {
				resources = append(resources, a.Resource.String())
			}
		}
		if !slices.Equal(resources, []string{apiARN(id)}) {
			t.Errorf("access-unevaluated advisories on %v, want one on %s", resources, apiARN(id))
		}
	})
}

// DeleteRestApi drops the id from the data plane and the edge's owner
// lookup (apigateway-rest.md 3.1, 4.1), and GetRestApis no longer lists it.
func TestDeletedRestApiLeavesTheDataPlane(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI("/a")
		must(h, apigatewayapi.OpDeleteRestApi, &apigatewayapi.DeleteRestApiInput{RestApiId: id})
		if h.svc.OwnsExecuteAPI(id) {
			t.Errorf("OwnsExecuteAPI(%s) after DeleteRestApi = true", id)
		}
		wantAnswer(t, h.get(id, "/s/a"), apigateway.RoutingSwitch().Value.UnknownAPI, "after DeleteRestApi")
		if got := must(h, apigatewayapi.OpGetRestApis, &apigatewayapi.GetRestApisInput{}).Items; len(got) != 0 {
			t.Errorf("GetRestApis after DeleteRestApi = %+v, want none", got)
		}
	})
}

// The settings survive Export and Import (contract 7.1).
func TestSettingsSurviveExportImport(t *testing.T) {
	run(t, func(h *harness) {
		out := must(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{
			Name: "t", Policy: new(`{"Statement":[]}`), MinimumCompressionSize: new(int32(5)), DisableExecuteApiEndpoint: true,
			EndpointConfiguration: &apigatewayapi.EndpointConfiguration{Types: []apigatewayapi.EndpointType{apigatewayapi.EndpointTypeRegional}, VpcEndpointIds: []string{"vpce-1"}},
		})
		get := func() string {
			b, err := json.Marshal(must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: *out.Id}))
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		before := get()
		if !strings.Contains(before, "vpce-1") {
			t.Fatalf("GetRestApi = %s, want the settings", before)
		}
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		if after := get(); after != before {
			t.Errorf("after the round trip %s, want %s", after, before)
		}
	})
}
