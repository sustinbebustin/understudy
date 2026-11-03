package apigatewayv2_test

import (
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
