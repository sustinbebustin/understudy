package apigateway_test

import (
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

// mockAPI is an API whose root GET is a MOCK integration, deployable.
func (h *harness) mockAPI() (id, root string) {
	h.Helper()
	id, root = h.api("t")
	h.method(id, root, "GET")
	must(h, apigatewayapi.OpPutIntegration, &apigatewayapi.PutIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", Type: apigatewayapi.IntegrationTypeMock})
	return id, root
}

// The DeleteDeployment golden (spec 6b acceptance; aws-recorded,
// localstack@8b9a79f
// tests/aws/services/apigateway/test_apigateway_common.snapshot.json:L604-L643,
// recorded 2023-09-07; apigateway-rest.md finding 12): GetDeployment
// answers createdDate and id; GetStage the recorded stage shape
// (cacheClusterEnabled false, cacheClusterStatus NOT_AVAILABLE,
// methodSettings {}, tracingEnabled false, createdDate, lastUpdatedDate,
// no variables, apigateway-rest.md 2.3); DeleteDeployment while the stage
// points at it is BadRequestException 400 "Active stages pointing to this
// deployment must be moved or deleted" and the deployment remains; after
// DeleteStage it is deleted.
func TestDeleteDeploymentGolden(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.mockAPI()
		dep := must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id})
		if dep.Id == nil || dep.CreatedDate == nil {
			t.Fatalf("CreateDeployment = %+v, want an id and a createdDate", dep)
		}
		got := must(h, apigatewayapi.OpGetDeployment, &apigatewayapi.GetDeploymentInput{RestApiId: id, DeploymentId: *dep.Id})
		if deref(got.Id) != *dep.Id || got.CreatedDate == nil {
			t.Errorf("GetDeployment = %+v, want the id and a createdDate", got)
		}
		stage := must(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: "dev", DeploymentId: *dep.Id})
		for name, s := range map[string]*apigatewayapi.Stage{"CreateStage": stage, "GetStage": must(h, apigatewayapi.OpGetStage, &apigatewayapi.GetStageInput{RestApiId: id, StageName: "dev"})} {
			if deref(s.StageName) != "dev" || deref(s.DeploymentId) != *dep.Id || s.CacheClusterEnabled || deref(s.CacheClusterStatus) != apigatewayapi.CacheClusterStatusNotAvailable ||
				s.MethodSettings == nil || len(s.MethodSettings) != 0 || s.TracingEnabled || s.CreatedDate == nil || s.LastUpdatedDate == nil || s.Variables != nil {
				t.Errorf("%s = %+v, want the recorded stage shape", name, s)
			}
		}
		wantText(t, fail(h, apigatewayapi.OpDeleteDeployment, &apigatewayapi.DeleteDeploymentInput{RestApiId: id, DeploymentId: *dep.Id}),
			badRequest, "Active stages pointing to this deployment must be moved or deleted")
		must(h, apigatewayapi.OpGetDeployment, &apigatewayapi.GetDeploymentInput{RestApiId: id, DeploymentId: *dep.Id})
		must(h, apigatewayapi.OpDeleteStage, &apigatewayapi.DeleteStageInput{RestApiId: id, StageName: "dev"})
		must(h, apigatewayapi.OpDeleteDeployment, &apigatewayapi.DeleteDeploymentInput{RestApiId: id, DeploymentId: *dep.Id})
		wantError(t, fail(h, apigatewayapi.OpGetDeployment, &apigatewayapi.GetDeploymentInput{RestApiId: id, DeploymentId: *dep.Id}), notFound)
	})
}

// CreateDeployment with stageName creates the stage, or repoints it, with
// stageDescription and variables (apigateway-rest.md 3.6, both paths
// recorded, test_apigateway_common.py:L1493-L1541); GetStages lists the
// stages and filters by deploymentId ([S] GetStagesRequest, 2.2);
// GetDeployments lists the deployments.
func TestCreateDeploymentWithStageName(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.mockAPI()
		first := must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{
			RestApiId: id, StageName: new("s"), StageDescription: new("first"), Variables: map[string]string{"fn": "a"}, Description: new("d1"),
		})
		if deref(first.Description) != "d1" {
			t.Errorf("CreateDeployment description = %q, want d1", deref(first.Description))
		}
		s := must(h, apigatewayapi.OpGetStage, &apigatewayapi.GetStageInput{RestApiId: id, StageName: "s"})
		if deref(s.DeploymentId) != *first.Id || deref(s.Description) != "first" || s.Variables["fn"] != "a" {
			t.Errorf("stage s = %+v, want deployment %s, description first, variable fn=a", s, *first.Id)
		}
		second := must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id, StageName: new("s")})
		if s := must(h, apigatewayapi.OpGetStage, &apigatewayapi.GetStageInput{RestApiId: id, StageName: "s"}); deref(s.DeploymentId) != *second.Id {
			t.Errorf("stage s after the second deployment points at %q, want %s", deref(s.DeploymentId), *second.Id)
		}
		if *first.Id == *second.Id {
			t.Errorf("two deployments share id %s", *first.Id)
		}
		must(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: "old", DeploymentId: *first.Id})
		if got := must(h, apigatewayapi.OpGetStages, &apigatewayapi.GetStagesInput{RestApiId: id}).Item; len(got) != 2 {
			t.Errorf("GetStages = %d stages, want 2", len(got))
		}
		if got := must(h, apigatewayapi.OpGetStages, &apigatewayapi.GetStagesInput{RestApiId: id, DeploymentId: first.Id}).Item; len(got) != 1 || deref(got[0].StageName) != "old" {
			t.Errorf("GetStages?deploymentId=%s = %+v, want only old", *first.Id, got)
		}
		if got := must(h, apigatewayapi.OpGetDeployments, &apigatewayapi.GetDeploymentsInput{RestApiId: id}).Items; len(got) != 2 {
			t.Errorf("GetDeployments = %d deployments, want 2", len(got))
		}
		upd := must(h, apigatewayapi.OpUpdateDeployment, &apigatewayapi.UpdateDeploymentInput{RestApiId: id, DeploymentId: *second.Id, PatchOperations: []apigatewayapi.PatchOperation{op(apigatewayapi.OpReplace, "/description", "d2")}})
		if deref(upd.Description) != "d2" {
			t.Errorf("UpdateDeployment description = %q, want d2", deref(upd.Description))
		}
	})
}

// Deploying an API with no methods is refused with BadRequestException
// (texts inferred, APIGW1-05: identity only); canarySettings answers
// NotImplemented on CreateDeployment and CreateStage (spec 6b coverage
// marks).
func TestDeploymentRefusals(t *testing.T) {
	run(t, func(h *harness) {
		empty, _ := h.api("empty")
		wantError(t, fail(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: empty}), badRequest)
		id, root := h.api("t")
		h.method(id, root, "GET")
		wantError(t, fail(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id}), badRequest)
		must(h, apigatewayapi.OpPutIntegration, &apigatewayapi.PutIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", Type: apigatewayapi.IntegrationTypeMock})
		wantNotImplemented(t, fail(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id, CanarySettings: &apigatewayapi.DeploymentCanarySettings{PercentTraffic: 10}}), "canarySettings")
		dep := must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id})
		wantNotImplemented(t, fail(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: "c", DeploymentId: *dep.Id, CanarySettings: &apigatewayapi.CanarySettings{PercentTraffic: 10}}), "canarySettings")
		if got := must(h, apigatewayapi.OpGetDeployments, &apigatewayapi.GetDeploymentsInput{RestApiId: id}).Items; len(got) != 1 {
			t.Errorf("GetDeployments after the refusals = %d, want 1", len(got))
		}
	})
}

// Stage refusals whose texts are inferred (APIGW1-06; identity only): an
// unknown deployment id is BadRequestException, a duplicate stage
// ConflictException, a missing stage NotFoundException (contract 7.6).
func TestStageRefusals(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.mockAPI()
		dep := *must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id}).Id
		wantError(t, fail(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: "s", DeploymentId: "nosuch"}), badRequest)
		must(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: "s", DeploymentId: dep})
		wantError(t, fail(h, apigatewayapi.OpCreateStage, &apigatewayapi.CreateStageInput{RestApiId: id, StageName: "s", DeploymentId: dep}), conflict)
		wantError(t, fail(h, apigatewayapi.OpGetStage, &apigatewayapi.GetStageInput{RestApiId: id, StageName: "nosuch"}), notFound)
	})
}

// UpdateStage: an unknown path answers the recorded text, byte for byte
// (aws-recorded, test_apigateway_common.snapshot.json:L393-L410,
// apigateway-rest.md 2.4); /variables/<name> replace sets and remove
// deletes; /deploymentId repoints the stage (aws-doc patch-operations,
// 3.7).
func TestUpdateStage(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.mockAPI()
		first := h.deploy(id)
		second := *must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id}).Id
		update := func(ops ...apigatewayapi.PatchOperation) *apigatewayapi.Stage {
			return must(h, apigatewayapi.OpUpdateStage, &apigatewayapi.UpdateStageInput{RestApiId: id, StageName: "s", PatchOperations: ops})
		}
		if s := update(op(apigatewayapi.OpReplace, "/variables/fn", "a"), op(apigatewayapi.OpReplace, "/variables/x", "b")); s.Variables["fn"] != "a" || s.Variables["x"] != "b" {
			t.Errorf("after replace /variables = %v, want fn=a x=b", s.Variables)
		}
		if s := update(op(apigatewayapi.OpRemove, "/variables/x")); s.Variables["fn"] != "a" || len(s.Variables) != 1 {
			t.Errorf("after remove /variables/x = %v, want only fn=a", s.Variables)
		}
		if s := update(op(apigatewayapi.OpReplace, "/deploymentId", second), op(apigatewayapi.OpReplace, "/description", "d")); deref(s.DeploymentId) != second || deref(s.Description) != "d" {
			t.Errorf("after replace /deploymentId = %+v, want deployment %s", s, second)
		}
		const text = "Invalid method setting path: /tags/tag1. Must be one of: [/deploymentId, /description, /cacheClusterEnabled, /cacheClusterSize, /clientCertificateId, /accessLogSettings, /accessLogSettings/destinationArn, /accessLogSettings/format, /{resourcePath}/{httpMethod}/metrics/enabled, /{resourcePath}/{httpMethod}/logging/dataTrace, /{resourcePath}/{httpMethod}/logging/loglevel, /{resourcePath}/{httpMethod}/throttling/burstLimit/{resourcePath}/{httpMethod}/throttling/rateLimit/{resourcePath}/{httpMethod}/caching/ttlInSeconds, /{resourcePath}/{httpMethod}/caching/enabled, /{resourcePath}/{httpMethod}/caching/dataEncrypted, /{resourcePath}/{httpMethod}/caching/requireAuthorizationForCacheControl, /{resourcePath}/{httpMethod}/caching/unauthorizedCacheControlHeaderStrategy, /*/*/metrics/enabled, /*/*/logging/dataTrace, /*/*/logging/loglevel, /*/*/throttling/burstLimit /*/*/throttling/rateLimit /*/*/caching/ttlInSeconds, /*/*/caching/enabled, /*/*/caching/dataEncrypted, /*/*/caching/requireAuthorizationForCacheControl, /*/*/caching/unauthorizedCacheControlHeaderStrategy, /variables/{variable_name}, /tracingEnabled]"
		wantText(t, fail(h, apigatewayapi.OpUpdateStage, &apigatewayapi.UpdateStageInput{RestApiId: id, StageName: "s", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/tags/tag1", "v"),
		}}), badRequest, text)
		if s := must(h, apigatewayapi.OpGetStage, &apigatewayapi.GetStageInput{RestApiId: id, StageName: "s"}); deref(s.DeploymentId) != second {
			t.Errorf("stage after a refused update points at %q, want %s (first was %s)", deref(s.DeploymentId), second, first)
		}
		// Method settings are stored and echoed (3.7: the recorded echo keys
		// them by resource path and method, */* for all).
		if s := update(op(apigatewayapi.OpReplace, "/*/*/metrics/enabled", "true")); !s.MethodSettings["*/*"].MetricsEnabled {
			t.Errorf("methodSettings = %+v, want */* metrics enabled", s.MethodSettings)
		}
	})
}

// A stage serves only its deployment snapshot (spec 6b acceptance;
// apigateway-rest.md 3.6: "a deployment freezes the API", aws-doc
// how-to-deploy-api): a resource added after the deployment is not served
// (403 Missing Authentication Token, finding 3) until a new deployment,
// and deleting the live method does not stop the stage serving it.
func TestStageServesOnlyItsDeployment(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI("/a")
		later := h.resource(id, h.rootOf(id), "later")
		h.proxy(id, later, "GET", h.fnARN)
		wantAnswer(t, h.get(id, "/s/later"), apigateway.MissingAuthenticationToken(), "a resource added after the deployment")
		h.notInvoked("a resource added after the deployment")
		paths := h.paths(id)
		must(h, apigatewayapi.OpDeleteMethod, &apigatewayapi.DeleteMethodInput{RestApiId: id, ResourceId: paths["/a"], HttpMethod: "ANY"})
		if rec := h.get(id, "/s/a"); rec.Code != http.StatusOK {
			t.Errorf("GET /s/a after deleting the live method = %d %s, want the deployed method's 200", rec.Code, rec.Body)
		}
		h.deploy(id)
		if rec := h.get(id, "/s/later"); rec.Code != http.StatusOK {
			t.Errorf("GET /s/later after redeploying = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		wantAnswer(t, h.get(id, "/s/a"), apigateway.MissingAuthenticationToken(), "a method deleted before the redeployment")
	})
}

// rootOf reads the API's root resource id.
func (h *harness) rootOf(id string) string {
	h.Helper()
	return deref(must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id}).RootResourceId)
}
