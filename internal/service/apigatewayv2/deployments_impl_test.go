package apigatewayv2_test

import (
	"net/http"
	"slices"
	"strconv"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
)

// A deployment snapshots the integrations too (apigateway-http.md finding
// 7): an integration changed after the deployment keeps calling the old
// function on the stage until the next deployment.
func TestDeploymentSnapshotsIntegrations(t *testing.T) {
	run(t, func(h *harness) {
		oldFn, newFn := h.createFunction("old"), h.createFunction("new")
		id := h.apiID("t")
		integration := h.lambdaIntegration(id, oldFn, "2.0")
		h.createRoute(id, "GET /a", integration)
		must(h, apigatewayv2api.OpCreateStage, stageInput(id, "s"))
		must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, StageName: new("s")})
		h.allow("old", "apigateway", h.executionARN(id)+"/*/*")
		h.allow("new", "apigateway", h.executionARN(id)+"/*/*")
		must(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{ApiId: id, IntegrationId: integration, IntegrationUri: new(newFn)})
		h.get(id, "/s/a")
		must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, StageName: new("s")})
		h.get(id, "/s/a")
		calls := h.fn.invocations()
		if len(calls) != 2 || calls[0].in.FunctionName != oldFn || calls[1].in.FunctionName != newFn {
			names := make([]string, 0, len(calls))
			for _, c := range calls {
				names = append(names, c.in.FunctionName)
			}
			t.Errorf("invoked %v, want %s before the redeployment and %s after", names, oldFn, newFn)
		}
	})
}

// A stage serving a deployment of an API with no routes answers the
// switch's NoRoute, where its EmptyAPI rule allows the deployment
// (UNVERIFIED, apigateway-http.md 3.5, APIGW2-04).
func TestEmptyDeploymentServesNoRoute(t *testing.T) {
	front := apigatewayv2.FrontAnswersSwitch().Value
	run(t, func(h *harness) {
		id := h.apiID("empty")
		must(h, apigatewayv2api.OpCreateStage, stageInput(id, "prod"))
		_, err := h.Invoke(h.Context(), apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, StageName: new("prod")})
		wantRule(t, front.Deployments.EmptyAPI, err, "CreateDeployment of an API with no routes")
		if front.Deployments.EmptyAPI == apigatewayv2.Allowed {
			wantAnswer(t, h.get(id, "/prod/x"), front.NoRoute, "a stage serving an empty deployment")
		}
	})
}

// A deployment no stage serves deletes, automatic ones included, while
// the one an autoDeploy stage serves follows the switch's DeleteInUse
// (the REST rule; UNVERIFIED for HTTP APIs, apigateway-http.md 3.5,
// APIGW2-04); the stage keeps serving it when the rule refuses.
func TestDeleteAutomaticDeployments(t *testing.T) {
	rule := apigatewayv2.FrontAnswersSwitch().Value.Deployments.DeleteInUse
	run(t, func(h *harness) {
		id, integration := h.lambdaAPI("2.0", "GET /a")
		first := deref(must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "$default"}).DeploymentId)
		h.createRoute(id, "GET /b", integration)
		current := deref(must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "$default"}).DeploymentId)
		must(h, apigatewayv2api.OpDeleteDeployment, &apigatewayv2api.DeleteDeploymentInput{ApiId: id, DeploymentId: first})
		wantError(t, fail(h, apigatewayv2api.OpGetDeployment, &apigatewayv2api.GetDeploymentInput{ApiId: id, DeploymentId: first}), notFound)
		_, err := h.Invoke(h.Context(), apigatewayv2api.OpDeleteDeployment, &apigatewayv2api.DeleteDeploymentInput{ApiId: id, DeploymentId: current})
		wantRule(t, rule, err, "DeleteDeployment of the deployment the $default stage serves")
		if rule == apigatewayv2.Refused {
			if rec := h.get(id, "/b"); rec.Code != http.StatusOK {
				t.Errorf("GET /b after the refused delete = %d %s, want the function's 200", rec.Code, rec.Body)
			}
		}
		wantError(t, fail(h, apigatewayv2api.OpDeleteDeployment, &apigatewayv2api.DeleteDeploymentInput{ApiId: id, DeploymentId: first}), notFound)
		wantError(t, fail(h, apigatewayv2api.OpUpdateDeployment, &apigatewayv2api.UpdateDeploymentInput{ApiId: id, DeploymentId: first, Description: new("x")}), notFound)
	})
}

// listDeployments pages through GetDeployments of api with maxResults.
func (h *harness) listDeployments(api, maxResults string) []string {
	h.Helper()
	var ids []string
	in := &apigatewayv2api.GetDeploymentsInput{ApiId: api, MaxResults: new(maxResults)}
	for {
		out := must(h, apigatewayv2api.OpGetDeployments, in)
		if n, err := strconv.Atoi(maxResults); err == nil && len(out.Items) > n {
			h.Fatalf("GetDeployments page of %d items, asked for at most %d", len(out.Items), n)
		}
		for i := range out.Items {
			ids = append(ids, deref(out.Items[i].DeploymentId))
		}
		if out.NextToken == nil {
			return ids
		}
		in.NextToken = out.NextToken
	}
}

// Paging through GetDeployments with any page size lists every deployment
// once, in creation order (inferred, APIGW2-03), no page larger than asked
// (apigateway-http.md 2.2).
func TestGetDeploymentsPagingProperty(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		h.createRoute(id, "GET /a", "")
		rapid.Check(t, func(rt *rapid.T) {
			made := rapid.IntRange(0, 8).Draw(rt, "deployments")
			size := rapid.IntRange(1, 4).Draw(rt, "page size")
			ids := make([]string, 0, made)
			for range made {
				ids = append(ids, deref(must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id}).DeploymentId))
			}
			if paged := h.listDeployments(id, strconv.Itoa(size)); !slices.Equal(paged, ids) {
				rt.Fatalf("GetDeployments in pages of %d = %v, want %v", size, paged, ids)
			}
			for _, d := range ids {
				must(h, apigatewayv2api.OpDeleteDeployment, &apigatewayv2api.DeleteDeploymentInput{ApiId: id, DeploymentId: d})
			}
		})
	})
}
