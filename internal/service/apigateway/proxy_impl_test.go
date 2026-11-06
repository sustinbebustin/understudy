package apigateway_test

import (
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

// An ANY method presents ANY as the method segment of its source ARN, and
// the root resource an empty last segment, as switch
// apigateway.lambda.permission describes (APIGW1-15, apigateway-rest.md
// 5.4): a permission naming the request's verb instead does not let a GET
// through an ANY method.
func TestSourceARNOfAnyMethodAndRoot(t *testing.T) {
	denial := apigateway.PermissionSwitch().Value.Denial
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id, root := h.api("t")
		items := h.resource(id, root, "items")
		h.proxy(id, items, "ANY", fnARN)
		h.proxy(id, root, http.MethodGet, fnARN)
		h.deploy(id)

		h.allow("verb", apigateway.SourceARN(region, account, id, "s", http.MethodGet, "/items"))
		wantAnswer(t, h.get(id, "/s/items"), denial, "a GET permission for an ANY method")
		h.notInvoked("a GET permission for an ANY method")

		h.allow("any", apigateway.SourceARN(region, account, id, "s", "ANY", "/items"))
		if rec := h.get(id, "/s/items"); rec.Code != http.StatusOK {
			t.Errorf("GET /s/items with a permission for ANY /items = %d %s, want 200", rec.Code, rec.Body)
		}

		wantAnswer(t, h.get(id, "/s/"), denial, "the root without a permission")
		h.allow("root", h.executionARN(id)+"/s/GET/")
		if rec := h.get(id, "/s/"); rec.Code != http.StatusOK {
			t.Errorf("GET /s/ with a permission for s/GET/ = %d %s, want 200", rec.Code, rec.Body)
		}
	})
}

// With several unserved integration settings, credentials answer first:
// AWS assumes the role instead of checking the function policy
// (apigateway-rest.md 3.4, 5.4), so the answer names the role whatever
// else is set, and the function never runs.
func TestCredentialsAnswerBeforeOtherSettings(t *testing.T) {
	run(t, func(h *harness) {
		id := h.proxyRoot(&apigatewayapi.CreateRestApiInput{Name: "t"}, func(in *apigatewayapi.PutIntegrationInput) {
			in.Credentials = new("arn:aws:iam::" + account + ":role/apigw")
			in.Uri = new("arn:aws:apigateway:" + region + ":lambda:path/2015-03-31/functions/arn:aws:lambda:" + region + ":" + account + ":function:${stageVariables.fn}/invocations")
			in.ContentHandling = new(apigatewayapi.ContentHandlingStrategyConvertToText)
		})
		h.deploy(id)
		want501(t, h.get(id, "/s/"), "credentials", "credentials with a stage-variable uri and contentHandling", "role/apigw")
		h.notInvoked("credentials with a stage-variable uri and contentHandling")
	})
}
