package apigatewayv2_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
)

// CreateDeployment answers AutoDeployed false, DeploymentStatus DEPLOYED,
// CreatedDate and DeploymentId (the create-deployment CLI example,
// apigateway-http.md 3.5); Terraform waits for DEPLOYED (finding 7, 9.5),
// which GetDeployment and GetDeployments answer at once. UpdateDeployment
// changes the description.
func TestCreateDeployment(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		h.createRoute(id, "GET /a", "")
		out := must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, Description: new("v1")})
		dep := deref(out.DeploymentId)
		if dep == "" || out.AutoDeployed == nil || *out.AutoDeployed || deref(out.DeploymentStatus) != apigatewayv2api.DeploymentStatusDeployed || out.CreatedDate == nil || deref(out.Description) != "v1" {
			t.Errorf("CreateDeployment = %+v, want an id, AutoDeployed false, DEPLOYED, a date and v1", out)
		}
		got := must(h, apigatewayv2api.OpGetDeployment, &apigatewayv2api.GetDeploymentInput{ApiId: id, DeploymentId: dep})
		if deref(got.DeploymentStatus) != apigatewayv2api.DeploymentStatusDeployed || deref(got.Description) != "v1" || deref(got.AutoDeployed) {
			t.Errorf("GetDeployment = %+v, want DEPLOYED v1, not automatic", got)
		}
		must(h, apigatewayv2api.OpUpdateDeployment, &apigatewayv2api.UpdateDeploymentInput{ApiId: id, DeploymentId: dep, Description: new("v1.1")})
		list := must(h, apigatewayv2api.OpGetDeployments, &apigatewayv2api.GetDeploymentsInput{ApiId: id}).Items
		if len(list) != 1 || deref(list[0].DeploymentId) != dep || deref(list[0].Description) != "v1.1" || deref(list[0].DeploymentStatus) != apigatewayv2api.DeploymentStatusDeployed {
			t.Errorf("GetDeployments = %+v, want %s described v1.1", list, dep)
		}
		second := deref(must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id}).DeploymentId)
		if second == dep || second == "" {
			t.Errorf("second DeploymentId = %q, want a new id beside %q", second, dep)
		}
		wantError(t, fail(h, apigatewayv2api.OpGetDeployment, &apigatewayv2api.GetDeploymentInput{ApiId: id, DeploymentId: "zzzzzz"}), notFound)
	})
}

// CreateDeployment with a StageName deploys to that stage, which then
// names the new deployment (the create-deployment reference,
// apigateway-http.md 3.5); a stage created with a DeploymentId names it.
func TestDeploymentToAStage(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		h.createRoute(id, "GET /a", "")
		first := deref(must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id}).DeploymentId)
		stage := must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "prod", DeploymentId: new(first)})
		if deref(stage.DeploymentId) != first {
			t.Errorf("CreateStage DeploymentId = %q, want %q", deref(stage.DeploymentId), first)
		}
		second := deref(must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, StageName: new("prod")}).DeploymentId)
		got := must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "prod"})
		if deref(got.DeploymentId) != second || second == first {
			t.Errorf("stage DeploymentId after CreateDeployment to it = %q, want the new %q", deref(got.DeploymentId), second)
		}
		must(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "prod", DeploymentId: new(first)})
		if got := must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "prod"}); deref(got.DeploymentId) != first {
			t.Errorf("stage DeploymentId after UpdateStage = %q, want %q", deref(got.DeploymentId), first)
		}
	})
}

// Deleting a deployment a stage uses follows the switch's DeleteInUse (the
// REST rule; UNVERIFIED for HTTP APIs, apigateway-http.md 3.5, APIGW2-04);
// once the stage names another deployment, as Terraform's
// create_before_destroy replacement leaves it (9.5), the old one deletes,
// after which GetDeployment is NotFoundException.
func TestDeleteDeploymentInUse(t *testing.T) {
	rule := apigatewayv2.FrontAnswersSwitch().Value.Deployments.DeleteInUse
	run(t, func(h *harness) {
		id := h.apiID("t")
		h.createRoute(id, "GET /a", "")
		old := deref(must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id}).DeploymentId)
		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "prod", DeploymentId: new(old)})
		_, err := h.Invoke(h.Context(), apigatewayv2api.OpDeleteDeployment, &apigatewayv2api.DeleteDeploymentInput{ApiId: id, DeploymentId: old})
		wantRule(t, rule, err, "DeleteDeployment of the deployment stage prod uses")
		if rule == apigatewayv2.Refused {
			if _, err := h.Invoke(h.Context(), apigatewayv2api.OpGetDeployment, &apigatewayv2api.GetDeploymentInput{ApiId: id, DeploymentId: old}); err != nil {
				t.Errorf("the refused deployment is gone: %s %s", err.ID.JSONType, err.Message)
			}
			replacement := deref(must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id}).DeploymentId)
			must(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "prod", DeploymentId: new(replacement)})
			must(h, apigatewayv2api.OpDeleteDeployment, &apigatewayv2api.DeleteDeploymentInput{ApiId: id, DeploymentId: old})
		}
		wantError(t, fail(h, apigatewayv2api.OpGetDeployment, &apigatewayv2api.GetDeploymentInput{ApiId: id, DeploymentId: old}), notFound)
	})
}

// Deploying an API with no routes follows the switch's EmptyAPI
// (UNVERIFIED, apigateway-http.md 3.5, APIGW2-04).
func TestDeployingAnEmptyAPI(t *testing.T) {
	rule := apigatewayv2.FrontAnswersSwitch().Value.Deployments.EmptyAPI
	run(t, func(h *harness) {
		id := h.apiID("empty")
		_, err := h.Invoke(h.Context(), apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id})
		wantRule(t, rule, err, "CreateDeployment of an API with no routes")
	})
}

// CreateDeployment to an automatic stage is an explicit deployment there,
// which the switch's ExplicitOnAutoDeploy governs (apigateway-http.md 3.4:
// DeploymentId "Can't be updated" with autoDeploy; UNVERIFIED, APIGW2-04).
func TestCreateDeploymentToAnAutoDeployStage(t *testing.T) {
	rule := apigatewayv2.FrontAnswersSwitch().Value.Deployments.ExplicitOnAutoDeploy
	run(t, func(h *harness) {
		id := h.apiID("t")
		h.createRoute(id, "GET /a", "")
		h.autoStage(id)
		_, err := h.Invoke(h.Context(), apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, StageName: new("$default")})
		wantRule(t, rule, err, "CreateDeployment to an autoDeploy stage")
	})
}
