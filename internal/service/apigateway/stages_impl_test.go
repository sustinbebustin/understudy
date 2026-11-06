package apigateway_test

import (
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// A deployment is a snapshot: a method added after it is not in its
// apiSummary, a new deployment's is ([S] Deployment$apiSummary;
// apigateway-rest.md 3.6 "a deployment freezes the API").
func TestDeploymentSnapshotIsImmutable(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.mockAPI()
		first := *must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id}).Id
		h.method(id, root, "POST")
		must(h, apigatewayapi.OpPutIntegration, &apigatewayapi.PutIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "POST", Type: apigatewayapi.IntegrationTypeMock})
		second := *must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id}).Id
		summary := func(dep string) map[string]apigatewayapi.MethodSnapshot {
			return must(h, apigatewayapi.OpGetDeployment, &apigatewayapi.GetDeploymentInput{RestApiId: id, DeploymentId: dep, Embed: []string{"apisummary"}}).ApiSummary["/"]
		}
		if got := summary(first); len(got) != 1 || deref(got["GET"].AuthorizationType) != "NONE" {
			t.Errorf("first apiSummary / = %+v, want only GET NONE", got)
		}
		if got := summary(second); len(got) != 2 {
			t.Errorf("second apiSummary / = %+v, want GET and POST", got)
		}
		if got := must(h, apigatewayapi.OpGetDeployment, &apigatewayapi.GetDeploymentInput{RestApiId: id, DeploymentId: first}); got.ApiSummary != nil {
			t.Errorf("GetDeployment without embed has apiSummary %+v", got.ApiSummary)
		}
	})
}

// Deployment operations on an id the API does not hold answer
// NotFoundException (text inferred: identity only).
func TestUnknownDeployment(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.mockAPI()
		wantError(t, fail(h, apigatewayapi.OpGetDeployment, &apigatewayapi.GetDeploymentInput{RestApiId: id, DeploymentId: "nosuch"}), notFound)
		wantError(t, fail(h, apigatewayapi.OpDeleteDeployment, &apigatewayapi.DeleteDeploymentInput{RestApiId: id, DeploymentId: "nosuch"}), notFound)
		wantError(t, fail(h, apigatewayapi.OpUpdateDeployment, &apigatewayapi.UpdateDeploymentInput{
			RestApiId: id, DeploymentId: "nosuch",
			PatchOperations: []apigatewayapi.PatchOperation{op(apigatewayapi.OpReplace, "/description", "d")},
		}), notFound)
	})
}

// GetDeployments pages in creation order with limit and position (2.5).
func TestGetDeploymentsPages(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.mockAPI()
		ids := make([]string, 0, 3)
		for range 3 {
			ids = append(ids, *must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id}).Id)
		}
		var got []string
		var pos *string
		for {
			out := must(h, apigatewayapi.OpGetDeployments, &apigatewayapi.GetDeploymentsInput{RestApiId: id, Limit: new(int32(2)), Position: pos})
			for _, d := range out.Items {
				got = append(got, deref(d.Id))
			}
			if pos = out.Position; pos == nil {
				break
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(ids) {
			t.Errorf("pages = %v, want %v", got, ids)
		}
	})
}

// CreateDeployment's stage members set the stage stageName names
// (API_CreateDeployment).
func TestCreateDeploymentStageMembers(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.mockAPI()
		must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{
			RestApiId: id, StageName: new("s"), CacheClusterEnabled: new(true), CacheClusterSize: new(apigatewayapi.CacheClusterSizeSize0Point5Gb), TracingEnabled: new(true),
		})
		s := must(h, apigatewayapi.OpGetStage, &apigatewayapi.GetStageInput{RestApiId: id, StageName: "s"})
		if !s.CacheClusterEnabled || deref(s.CacheClusterSize) != apigatewayapi.CacheClusterSizeSize0Point5Gb || !s.TracingEnabled {
			t.Errorf("stage = %+v, want the cache and tracing settings stored", s)
		}
		// No cache cluster runs (stored without effect).
		if deref(s.CacheClusterStatus) != apigatewayapi.CacheClusterStatusNotAvailable {
			t.Errorf("cacheClusterStatus = %q, want NOT_AVAILABLE", deref(s.CacheClusterStatus))
		}
		wantError(t, fail(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id, StageName: new("s"), Variables: map[string]string{"bad name": "v"}}), badRequest)
	})
}

// UpdateStage method settings (recorded,
// test_apigateway_common.snapshot.json L412-L460 and L1040-L1070): a new
// entry has the recorded defaults, a later entry starts from */*, a
// setting replaced on */* leaves the others' values, and removing an
// entry that does not exist answers the recorded text.
func TestStageMethodSettings(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.mockAPI()
		h.deploy(id)
		update := func(ops ...apigatewayapi.PatchOperation) (*apigatewayapi.Stage, *awserr.Error) {
			return h.Invoke(h.Context(), apigatewayapi.OpUpdateStage, &apigatewayapi.UpdateStageInput{RestApiId: id, StageName: "s", PatchOperations: ops})
		}
		wantText(t, fail(h, apigatewayapi.OpUpdateStage, &apigatewayapi.UpdateStageInput{RestApiId: id, StageName: "s", PatchOperations: []apigatewayapi.PatchOperation{op(apigatewayapi.OpRemove, "/*/*", "")}}),
			badRequest, "Cannot remove method setting */* because there is no method setting for this method ")
		s, err := update(
			op(apigatewayapi.OpReplace, "/*/*/throttling/burstLimit", "123"),
			op(apigatewayapi.OpReplace, "/*/*/caching/enabled", "true"),
			op(apigatewayapi.OpReplace, "/test/GET/throttling/burstLimit", "124"),
		)
		if err != nil {
			t.Fatal(err)
		}
		all := apigatewayapi.MethodSetting{
			CacheTtlInSeconds: 300, CachingEnabled: true, RequireAuthorizationForCacheControl: true, ThrottlingBurstLimit: 123, ThrottlingRateLimit: 10000,
			UnauthorizedCacheControlHeaderStrategy: new(apigatewayapi.UnauthorizedCacheControlHeaderStrategySucceedWithResponseHeader),
		}
		test := all
		test.ThrottlingBurstLimit = 124
		for key, want := range map[string]apigatewayapi.MethodSetting{"*/*": all, "test/GET": test} {
			if diff := cmp.Diff(want, s.MethodSettings[key]); diff != "" {
				t.Errorf("methodSettings[%s] (-want +got):\n%s", key, diff)
			}
		}
		if s, err = update(op(apigatewayapi.OpReplace, "/*/*/throttling/burstLimit", "100")); err != nil || s.MethodSettings["test/GET"].ThrottlingBurstLimit != 124 {
			t.Errorf("after replacing */* = %+v, %v; want test/GET kept at 124", s, err)
		}
		if s, err = update(op(apigatewayapi.OpRemove, "/*/*", "")); err != nil || len(s.MethodSettings) != 1 {
			t.Errorf("after remove /*/* = %+v, %v; want only test/GET", s, err)
		}
		if s, err = update(op(apigatewayapi.OpReplace, "/~1a~1b/POST/logging/loglevel", "INFO")); err != nil || deref(s.MethodSettings["a/b/POST"].LoggingLevel) != "INFO" {
			t.Errorf("after an escaped resource path = %+v, %v; want a/b/POST logging INFO", s, err)
		}
		wantError(t, fail(h, apigatewayapi.OpUpdateStage, &apigatewayapi.UpdateStageInput{RestApiId: id, StageName: "s", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/*/*/caching/enabled", "yes"),
		}}), badRequest)
	})
}

// A refused UpdateStage changes nothing; the stage members are echoed;
// removing the last variable leaves no variables key (2.3); an unknown
// deployment id is refused.
func TestUpdateStageAllOrNothing(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.mockAPI()
		h.deploy(id)
		wantError(t, fail(h, apigatewayapi.OpUpdateStage, &apigatewayapi.UpdateStageInput{RestApiId: id, StageName: "s", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/description", "changed"),
			op(apigatewayapi.OpReplace, "/deploymentId", "nosuch"),
		}}), badRequest)
		if s := must(h, apigatewayapi.OpGetStage, &apigatewayapi.GetStageInput{RestApiId: id, StageName: "s"}); s.Description != nil {
			t.Errorf("description after a refused update = %q, want none", *s.Description)
		}
		s := must(h, apigatewayapi.OpUpdateStage, &apigatewayapi.UpdateStageInput{RestApiId: id, StageName: "s", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/variables/v", "1"),
			op(apigatewayapi.OpReplace, "/tracingEnabled", "true"),
			op(apigatewayapi.OpReplace, "/accessLogSettings/format", "$context.requestId"),
			op(apigatewayapi.OpReplace, "/clientCertificateId", "abc"),
		}})
		if !s.TracingEnabled || s.AccessLogSettings == nil || deref(s.AccessLogSettings.Format) != "$context.requestId" || deref(s.ClientCertificateId) != "abc" {
			t.Errorf("stage = %+v, want tracing, access log format and client certificate echoed", s)
		}
		s = must(h, apigatewayapi.OpUpdateStage, &apigatewayapi.UpdateStageInput{RestApiId: id, StageName: "s", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpRemove, "/variables/v", ""),
			op(apigatewayapi.OpRemove, "/accessLogSettings", ""),
		}})
		if s.Variables != nil || s.AccessLogSettings != nil {
			t.Errorf("stage = %+v, want no variables and no access log settings", s)
		}
		wantNotImplemented(t, fail(h, apigatewayapi.OpUpdateStage, &apigatewayapi.UpdateStageInput{RestApiId: id, StageName: "s", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/documentationVersion", "v1"),
		}}), "documentationVersion")
		wantError(t, fail(h, apigatewayapi.OpUpdateStage, &apigatewayapi.UpdateStageInput{RestApiId: "zzzzzzzzzz", StageName: "s"}), notFound)
		wantError(t, fail(h, apigatewayapi.OpUpdateStage, &apigatewayapi.UpdateStageInput{RestApiId: id, StageName: "nosuch"}), notFound)
	})
}

// CreateStage limits and refusals: the name texts (APIGW1-06, recorded
// 2026-11-03), 10 stages per API (2.6), documentationVersion
// NotImplemented; DeleteStage of a missing stage NotFoundException.
func TestCreateStageLimits(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.mockAPI()
		dep := *must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id}).Id
		wantText(t, fail(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: "bad.name", DeploymentId: dep}), badRequest,
			"Stage name only allows a-zA-Z0-9_")
		wantText(t, fail(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: strings.Repeat("a", 129), DeploymentId: dep}), badRequest,
			"Stage name must be under 128 characters")
		wantText(t, fail(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: "s", DeploymentId: "nosuch"}), badRequest,
			"Deployment id does not exist")
		wantNotImplemented(t, fail(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: "d", DeploymentId: dep, DocumentationVersion: new("v1")}), "documentationVersion")
		for i := range 10 {
			must(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: fmt.Sprintf("s%d", i), DeploymentId: dep})
		}
		wantError(t, fail(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: "eleven", DeploymentId: dep}), apigatewayapi.IdentityLimitExceededException())
		wantError(t, fail(h, apigatewayapi.OpDeleteStage, &apigatewayapi.DeleteStageInput{RestApiId: id, StageName: "nosuch"}), notFound)
	})
}

// Tags: a stage that does not exist is NotFoundException, an ARN that is
// no API Gateway ARN BadRequestException (texts inferred), and more than
// 50 tags are refused (documented quota, 3.9).
func TestTagRefusals(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.api("t")
		wantError(t, fail(h, apigatewayapi.OpGetTags, &apigatewayapi.GetTagsInput{ResourceArn: apiARN(id) + "/stages/nosuch"}), notFound)
		wantError(t, fail(h, apigatewayapi.OpTagResource, &apigatewayapi.TagResourceInput{ResourceArn: "not-an-arn", Tags: map[string]string{"a": "b"}}), badRequest)
		many := map[string]string{}
		for i := range 51 {
			many[fmt.Sprintf("k%d", i)] = "v"
		}
		wantError(t, fail(h, apigatewayapi.OpTagResource, &apigatewayapi.TagResourceInput{ResourceArn: apiARN(id), Tags: many}), badRequest)
		if got := must(h, apigatewayapi.OpGetTags, &apigatewayapi.GetTagsInput{ResourceArn: apiARN(id)}).Tags; got == nil || len(got) != 0 {
			t.Errorf("GetTags after a refused TagResource = %#v, want {}", got)
		}
		if got := must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id}).Tags; got != nil {
			t.Errorf("GetRestApi tags of an untagged API = %#v, want absent (2.3)", got)
		}
		must(h, apigatewayapi.OpTagResource, &apigatewayapi.TagResourceInput{ResourceArn: apiARN(id), Tags: map[string]string{"a": "b"}})
		if got := must(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id}).Tags; !maps.Equal(got, map[string]string{"a": "b"}) {
			t.Errorf("UpdateRestApi tags = %v, want a=b", got)
		}
	})
}
