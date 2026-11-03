package apigatewayv2_test

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// wantText checks an error's identity, status and exact message: the texts
// AWS sent in APIGW2-01 (recorded 2026-11-03, apigateway-http.md 2.3).
func wantText(tb testing.TB, err *awserr.Error, want awserr.Identity, text string) {
	tb.Helper()
	wantError(tb, err, want)
	if err != nil && err.Message != text {
		tb.Errorf("message = %q\nwant      %q", err.Message, text)
	}
}

// The control plane's refusals carry the texts AWS sent (APIGW2-01,
// recorded 2026-11-03: steps 03-15, 17-21 and 26; apigateway-http.md 2.3,
// 3.2, 6.1). A route key with a trailing slash is refused for its empty
// last part (14.1's setup, "GET /slash/").
func TestControlPlaneTextsAsRecorded(t *testing.T) {
	const keyFormat = `The provided route key is not formatted properly for HTTP protocol. Format should be "[HTTP METHOD] /[RESOURCE PATH]" or "$default"`
	run(t, func(h *harness) {
		id := h.apiID("t")
		fnARN := h.createFunction("fn")
		h.createRoute(id, "GET /any", "")
		route := func(key string, target *string) *awserr.Error {
			return fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: key, Target: target})
		}
		wantText(t, route("FETCH /x", nil), badRequest, keyFormat)
		wantText(t, route("GET x", nil), badRequest, keyFormat)
		wantText(t, route("GET /{proxy+}/x", nil), badRequest, "Greedy variables may only be in last position of route key")
		wantText(t, route("GET /a{b}", nil), badRequest, `One or more path parts appear to be invalid. Parts are validated against this regular expression: ^(\{[\w.:-]+\+?\}|[a-zA-Z0-9.:_-]+)$`)
		wantText(t, route("GET /slash/", nil), badRequest, "Part of the given route key path is empty")
		wantText(t, route("GET /any", nil), conflict, "Route with key GET /any already exists for this API")
		wantText(t, route("GET /dangling", new("integrations/0000000")), badRequest, "Unable to find route target of type integrations with ID 0000000")

		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "dup"})
		wantText(t, fail(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "dup"}), conflict, "Stage already exists")
		wantText(t, fail(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "bad.name"}), badRequest, "Stage name only allows a-zA-Z0-9_")

		integration := func(in apigatewayv2api.CreateIntegrationInput) *awserr.Error {
			in.ApiId = id
			return fail(h, apigatewayv2api.OpCreateIntegration, &in)
		}
		lambda := apigatewayv2api.CreateIntegrationInput{IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationUri: new(fnARN), PayloadFormatVersion: new("2.0")}
		over := lambda
		over.TimeoutInMillis = new(int32(30001))
		wantText(t, integration(over), badRequest, "Timeout should be between 50 ms and 30000 ms")
		unknown := lambda
		unknown.IntegrationType = "HTTPS_PROXY"
		wantText(t, integration(unknown), badRequest, "IntegrationType is a required parameter for creating integration.")
		badVersion := lambda
		badVersion.PayloadFormatVersion = new("3.0")
		wantText(t, integration(badVersion), badRequest, "Unsupported PayloadFormatVersion: 3.0")
		noVersion := lambda
		noVersion.PayloadFormatVersion = nil
		wantText(t, integration(noVersion), badRequest, "PayloadFormatVersion is a required parameter for integration")
		wantText(t, integration(apigatewayv2api.CreateIntegrationInput{IntegrationType: apigatewayv2api.IntegrationTypeMock}), badRequest,
			"Currently, an API with a protocol type of HTTP may only be associated with proxy integrations (AWS_PROXY, HTTP_PROXY)")
		role := "arn:aws:iam::" + account + ":role/gw-int"
		wantText(t, integration(apigatewayv2api.CreateIntegrationInput{
			IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationSubtype: new("SNS-Publish"), CredentialsArn: new(role),
			PayloadFormatVersion: new("1.0"), RequestParameters: map[string]string{"Message": "x"},
		}), badRequest, "Operation: SNS-Publish is not supported.")
		wantText(t, integration(apigatewayv2api.CreateIntegrationInput{
			IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationSubtype: new("SQS-SendMessage"), CredentialsArn: new(role),
			PayloadFormatVersion: new("1.0"), RequestParameters: map[string]string{"QueueUrl": "https://sqs.us-east-2.amazonaws.com/" + account + "/q"},
		}), badRequest, "Operation: SQS-SendMessage requires enabling passthrough, or defining all of the following parameters in the parameter mapping: [QueueUrl, MessageBody]")
	})
}

// The deployment refusals carry AWS's texts (APIGW2-04, recorded
// 2026-11-03: steps 01, 08 and 09; apigateway-http.md 3.5): deploying an
// API with no routes, setting a deployment on an autoDeploy stage, and
// deleting a deployment a stage serves.
func TestDeploymentTextsAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		wantText(t, fail(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id}), badRequest,
			"Unable to deploy API because no valid routes exist in this API")
		h.createRoute(id, "GET /a", h.lambdaIntegration(id, h.createFunction("fn"), "2.0"))
		dep := must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id})
		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "dev", DeploymentId: dep.DeploymentId})
		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "auto", AutoDeploy: new(true)})
		wantText(t, fail(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "auto", DeploymentId: dep.DeploymentId}), badRequest,
			"Deployment ID cannot be set on this stage "+account+":"+id+" because AutoDeploy is enabled.")
		wantText(t, fail(h, apigatewayv2api.OpDeleteDeployment, &apigatewayv2api.DeleteDeploymentInput{ApiId: id, DeploymentId: *dep.DeploymentId}), badRequest,
			"Active stages pointing to this deployment must be moved or deleted")
	})
}

// UpdateApi answers 201, not the model's 200 (APIGW2-03, recorded
// 2026-11-03: steps 09 and 13; apigateway-http.md 2.2).
func TestUpdateAPIAnswers201(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		if status, body := h.wire(http.MethodPatch, "/v2/apis/"+id, `{"description":"x"}`); status != http.StatusCreated {
			t.Errorf("PATCH /v2/apis/%s = %d %s, want 201", id, status, body)
		}
	})
}

// AWS stores CORS lists normalized: origins and headers lower case,
// methods upper case (APIGW2-03, recorded 2026-11-03: steps 09 and 10). An
// UpdateApi's corsConfiguration merges into the stored one, so a wildcard
// origin against stored credentials is refused with AWS's text (APIGW2-13
// steps 10 and 11; apigateway-http.md 3.7).
func TestCORSAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		must(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{ApiId: id, CorsConfiguration: &apigatewayv2api.Cors{
			AllowOrigins: []string{"https://A.example"}, AllowMethods: []string{"get", "post"},
			AllowHeaders: []string{"Content-Type", "X-Up"}, ExposeHeaders: []string{"X-E"}, AllowCredentials: new(true),
		}})
		got := must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id}).CorsConfiguration
		want := &apigatewayv2api.Cors{
			AllowOrigins: []string{"https://a.example"}, AllowMethods: []string{"GET", "POST"},
			AllowHeaders: []string{"content-type", "x-up"}, ExposeHeaders: []string{"x-e"}, AllowCredentials: new(true),
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("corsConfiguration = %+v\nwant                %+v", got, want)
		}
		must(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{ApiId: id, CorsConfiguration: &apigatewayv2api.Cors{MaxAge: new(int32(300))}})
		want.MaxAge = new(int32(300))
		if got := must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id}).CorsConfiguration; !reflect.DeepEqual(got, want) {
			t.Errorf("after a merge: corsConfiguration = %+v\nwant                %+v", got, want)
		}
		wantText(t, fail(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{ApiId: id, CorsConfiguration: &apigatewayv2api.Cors{AllowOrigins: []string{"*"}}}),
			badRequest, "allow-credentials is not supported if 'allow-origin' is *")
	})
}

// An integration a route targets is not deleted: ConflictException with
// AWS's text naming the routes (APIGW2-03, recorded 2026-11-03: step 21).
func TestDeleteTargetedIntegrationAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		integration := h.lambdaIntegration(id, h.createFunction("fn"), "2.0")
		rid := h.createRoute(id, "GET /a", integration)
		wantText(t, fail(h, apigatewayv2api.OpDeleteIntegration, &apigatewayv2api.DeleteIntegrationInput{ApiId: id, IntegrationId: integration}), conflict,
			"Cannot delete Integration because it is referenced by the following Routes with Ids: ["+rid+"]")
	})
}

// The tag operations answer a missing API with "Resource was not found"
// and refuse a key over 128 or a value over 256 characters with the Coral
// text naming addedTags (APIGW2-01, recorded 2026-11-03: steps 23 to 25;
// apigateway-http.md 3.6).
func TestTagTextsAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		wantText(t, fail(h, apigatewayv2api.OpGetTags, &apigatewayv2api.GetTagsInput{ResourceArn: apiARN("0000000000")}), notFound, "Resource was not found")
		api := apiARN(h.apiID("t"))
		key := strings.Repeat("k", 129)
		wantText(t, fail(h, apigatewayv2api.OpTagResource, &apigatewayv2api.TagResourceInput{ResourceArn: api, Tags: map[string]string{key: "v"}}), badRequest,
			"1 validation error detected: Value '{"+key+"=v}' at 'addedTags' failed to satisfy constraint: Map keys must satisfy constraint: "+
				"[Member must have length less than or equal to 128, Member must have length greater than or equal to 1]")
		value := strings.Repeat("v", 257)
		wantText(t, fail(h, apigatewayv2api.OpTagResource, &apigatewayv2api.TagResourceInput{ResourceArn: api, Tags: map[string]string{"k": value}}), badRequest,
			"1 validation error detected: Value '{k="+value+"}' at 'addedTags' failed to satisfy constraint: Map value must satisfy constraint: "+
				"[Member must have length less than or equal to 256, Member must have length greater than or equal to 0]")
	})
}

// A missing resource is NotFoundException with AWS's text per resource
// (APIGW2-01, recorded 2026-11-03: steps 01-06; apigateway-http.md 2.3).
func TestNotFoundTextsAsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		const missing = "0000000000"
		wantText(t, fail(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: missing}), notFound, "Invalid API identifier specified "+account+":"+missing)
		wantText(t, fail(h, apigatewayv2api.OpDeleteApi, &apigatewayv2api.DeleteApiInput{ApiId: missing}), notFound, "Invalid API identifier specified "+account+":"+missing)
		id := h.apiID("t")
		wantText(t, fail(h, apigatewayv2api.OpGetRoute, &apigatewayv2api.GetRouteInput{ApiId: id, RouteId: "0000000"}), notFound, "Invalid route identifier specified")
		wantText(t, fail(h, apigatewayv2api.OpGetIntegration, &apigatewayv2api.GetIntegrationInput{ApiId: id, IntegrationId: "0000000"}), notFound, "Invalid Integration identifier specified")
		wantText(t, fail(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "nope"}), notFound, "Invalid stage identifier specified")
		wantText(t, fail(h, apigatewayv2api.OpGetDeployment, &apigatewayv2api.GetDeploymentInput{ApiId: id, DeploymentId: "000000"}), notFound, "Invalid Deployment identifier specified")
	})
}
