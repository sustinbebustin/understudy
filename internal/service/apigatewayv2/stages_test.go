package apigatewayv2_test

import (
	"maps"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
)

// stageInput is a CreateStage of name with nothing else set.
func stageInput(id, name string) *apigatewayv2api.CreateStageInput {
	return &apigatewayv2api.CreateStageInput{ApiId: id, StageName: name}
}

// CreateStage answers DefaultRouteSettings {"DetailedMetricsEnabled":
// false}, RouteSettings {}, StageVariables {}, Tags {}, CreatedDate and
// LastUpdatedDate, and no DeploymentId until there is one (the
// create-stage CLI example and Terraform's acceptance test,
// apigateway-http.md 3.4; Terraform's empty plan needs DefaultRouteSettings
// present, 9.4).
func TestCreateStageDefaults(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		out := must(h, apigatewayv2api.OpCreateStage, stageInput(id, "prod"))
		if deref(out.StageName) != "prod" || out.CreatedDate == nil || out.LastUpdatedDate == nil {
			t.Errorf("CreateStage = %+v, want the name and both dates", out)
		}
		if out.DefaultRouteSettings == nil || out.DefaultRouteSettings.DetailedMetricsEnabled == nil || *out.DefaultRouteSettings.DetailedMetricsEnabled {
			t.Errorf("DefaultRouteSettings = %+v, want DetailedMetricsEnabled false", out.DefaultRouteSettings)
		}
		if out.RouteSettings == nil || len(out.RouteSettings) != 0 || out.StageVariables == nil || len(out.StageVariables) != 0 || out.Tags == nil || len(out.Tags) != 0 {
			t.Errorf("RouteSettings %v, StageVariables %v, Tags %v; want each {}", out.RouteSettings, out.StageVariables, out.Tags)
		}
		if out.DeploymentId != nil {
			t.Errorf("DeploymentId = %q, want none before a deployment", *out.DeploymentId)
		}
		got := must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "prod"})
		if deref(got.StageName) != "prod" || got.DefaultRouteSettings == nil || got.DeploymentId != nil {
			t.Errorf("GetStage = %+v, want what CreateStage answered", got)
		}
		stages := must(h, apigatewayv2api.OpGetStages, &apigatewayv2api.GetStagesInput{ApiId: id}).Items
		if len(stages) != 1 || stages[0].StageName != "prod" {
			t.Errorf("GetStages = %+v, want prod", stages)
		}
	})
}

// Stage names "can contain only alphanumeric characters, hyphens, and
// underscores, or be $default. Maximum length is 128 characters" (the stage
// API reference, apigateway-http.md 2.4).
func TestStageNames(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		for _, name := range []string{"$default", "a-b_C9", strings.Repeat("s", 128)} {
			must(h, apigatewayv2api.OpCreateStage, stageInput(id, name))
			must(h, apigatewayv2api.OpDeleteStage, &apigatewayv2api.DeleteStageInput{ApiId: id, StageName: name})
		}
		for _, name := range []string{"", "a.b", "a b", "a/b", "$prod", strings.Repeat("s", 129)} {
			wantError(t, fail(h, apigatewayv2api.OpCreateStage, stageInput(id, name)), badRequest)
		}
		rapid.Check(t, func(rt *rapid.T) {
			name := rapid.StringMatching(`[A-Za-z0-9_-]{1,128}`).Draw(rt, "name")
			if _, err := h.Invoke(h.Context(), apigatewayv2api.OpCreateStage, stageInput(id, name)); err != nil {
				rt.Fatalf("CreateStage %q = %s %s", name, err.ID.JSONType, err.Message)
			}
			must(h, apigatewayv2api.OpDeleteStage, &apigatewayv2api.DeleteStageInput{ApiId: id, StageName: name})
		})
	})
}

// A stage name already in the API is a conflict; "Stages per API: 10"
// (the HTTP API quotas page, apigateway-http.md 3.9).
func TestStageConflictAndQuota(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		for i := range 10 {
			must(h, apigatewayv2api.OpCreateStage, stageInput(id, "s"+strconv.Itoa(i)))
		}
		wantError(t, fail(h, apigatewayv2api.OpCreateStage, stageInput(id, "s0")), conflict)
		wantError(t, fail(h, apigatewayv2api.OpCreateStage, stageInput(id, "s10")), badRequest)
		wantError(t, fail(h, apigatewayv2api.OpCreateStage, stageInput("zzzzzzzzzz", "x")), notFound)
	})
}

// Stage variable "names can have alphanumeric and underscore characters"
// (the stage API reference, apigateway-http.md 2.4); a stage holds 100
// variables, keys up to 64 characters and values up to 512 (the HTTP API
// quotas page, 3.9). The documented value pattern [A-Za-z0-9-._~:/?#&=,]+
// is not enforced: AWS accepted "a b" (APIGW2-01, recorded 2026-11-03:
// step 16).
func TestStageVariableRules(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		create := func(vars map[string]string) *awserr.Error {
			_, err := h.Invoke(h.Context(), apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "v", StageVariables: vars})
			if err == nil {
				must(h, apigatewayv2api.OpDeleteStage, &apigatewayv2api.DeleteStageInput{ApiId: id, StageName: "v"})
			}
			return err
		}
		hundred := map[string]string{}
		for i := range 100 {
			hundred["v"+strconv.Itoa(i)] = "x"
		}
		for name, vars := range map[string]map[string]string{
			"every value character": {"fn_Name1": "Aa0-._~:/?#&=,"},
			"a space in a value":    {"k": "a b"},
			"100 variables":         hundred,
			"a 64-character key":    {strings.Repeat("k", 64): "x"},
			"a 512-character value": {"k": strings.Repeat("v", 512)},
		} {
			if err := create(vars); err != nil {
				t.Errorf("%s: %s %s, want it accepted", name, err.ID.JSONType, err.Message)
			}
		}
		hundred["v100"] = "x"
		for name, vars := range map[string]map[string]string{
			"a hyphen in a name":    {"a-b": "x"},
			"an empty value":        {"k": ""},
			"101 variables":         hundred,
			"a 65-character key":    {strings.Repeat("k", 65): "x"},
			"a 513-character value": {"k": strings.Repeat("v", 513)},
		} {
			if err := create(vars); err == nil || err.ID.Shape != badRequest.Shape || err.ID.Status != badRequest.Status {
				t.Errorf("%s: %v, want BadRequestException 400", name, err)
			}
		}
		rapid.Check(t, func(rt *rapid.T) {
			value := rapid.StringMatching(`[A-Za-z0-9\-._~:/?#&=,]{1,40}`).Draw(rt, "value")
			if err := create(map[string]string{"k": value}); err != nil {
				rt.Fatalf("value %q: %s %s, want it accepted", value, err.ID.JSONType, err.Message)
			}
		})
	})
}

// UpdateStage merges stage variables, and a variable sent with value ""
// is removed (Terraform's stage update, apigateway-http.md 3.4).
func TestUpdateStageMergesVariables(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "prod", StageVariables: map[string]string{"a": "1", "c": "3"}})
		must(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "prod", StageVariables: map[string]string{"a": "", "b": "2"}, Description: new("d")})
		got := must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "prod"})
		if want := map[string]string{"b": "2", "c": "3"}; !maps.Equal(got.StageVariables, want) || deref(got.Description) != "d" {
			t.Errorf("after UpdateStage: StageVariables %v, Description %q; want %v and d", got.StageVariables, deref(got.Description), want)
		}
		wantError(t, fail(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "nope", Description: new("x")}), notFound)
	})
}

// An access log format "must include at least $context.requestId" (the
// stage API reference, apigateway-http.md 2.4) and fits the 3 KB access
// log template quota (3.9); the settings are stored, and
// DeleteAccessLogSettings removes them (2.2).
func TestAccessLogSettings(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		dest := "arn:aws:logs:us-east-1:000000000000:log-group:api"
		format := `{"requestId":"$context.requestId","status":"$context.status"}`
		out := must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{
			ApiId: id, StageName: "prod", AccessLogSettings: &apigatewayv2api.AccessLogSettings{DestinationArn: new(dest), Format: new(format)},
		})
		if out.AccessLogSettings == nil || deref(out.AccessLogSettings.Format) != format || deref(out.AccessLogSettings.DestinationArn) != dest {
			t.Errorf("AccessLogSettings = %+v, want them stored", out.AccessLogSettings)
		}
		for _, bad := range []string{`{"status":"$context.status"}`, "$context.requestId " + strings.Repeat("x", 4000)} {
			err := fail(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{
				ApiId: id, StageName: "bad", AccessLogSettings: &apigatewayv2api.AccessLogSettings{DestinationArn: new(dest), Format: new(bad)},
			})
			wantError(t, err, badRequest)
		}
		long := "$context.requestId " + strings.Repeat("x", 2900)
		must(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "prod", AccessLogSettings: &apigatewayv2api.AccessLogSettings{DestinationArn: new(dest), Format: new(long)}})
		must(h, apigatewayv2api.OpDeleteAccessLogSettings, &apigatewayv2api.DeleteAccessLogSettingsInput{ApiId: id, StageName: "prod"})
		if got := must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "prod"}); got.AccessLogSettings != nil {
			t.Errorf("AccessLogSettings after DeleteAccessLogSettings = %+v, want none", got.AccessLogSettings)
		}
	})
}

// Route settings are keyed by route key (the update-stage CLI example,
// apigateway-http.md 3.4), stored and echoed; DeleteRouteSettings removes
// one key (2.2).
func TestRouteSettings(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		settings := map[string]apigatewayv2api.RouteSettings{
			"GET /pets": {ThrottlingBurstLimit: new(int32(100)), ThrottlingRateLimit: new(2000.0)},
			"$default":  {DetailedMetricsEnabled: new(true)},
		}
		out := must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "prod", RouteSettings: settings})
		if got := out.RouteSettings["GET /pets"]; deref(got.ThrottlingBurstLimit) != 100 || deref(got.ThrottlingRateLimit) != 2000 || len(out.RouteSettings) != 2 {
			t.Errorf("RouteSettings = %+v, want both keys stored", out.RouteSettings)
		}
		must(h, apigatewayv2api.OpDeleteRouteSettings, &apigatewayv2api.DeleteRouteSettingsInput{ApiId: id, StageName: "prod", RouteKey: "GET /pets"})
		got := must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "prod"})
		if _, ok := got.RouteSettings["GET /pets"]; ok || len(got.RouteSettings) != 1 {
			t.Errorf("RouteSettings after deleting GET /pets = %+v, want only $default", got.RouteSettings)
		}
	})
}

// ClientCertificateId is WebSocket-only (the stage API reference,
// apigateway-http.md 3.4), a valid member understudy does not emulate, so
// it answers NotImplemented naming it (the unit's conflict row 4).
func TestClientCertificateIsNotImplemented(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		err := fail(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "prod", ClientCertificateId: new("abc123")})
		wantNotImplemented(t, err, "ClientCertificateId")
		if n := len(must(h, apigatewayv2api.OpGetStages, &apigatewayv2api.GetStagesInput{ApiId: id}).Items); n != 0 {
			t.Errorf("GetStages = %d stages, want none: the refusal changes nothing", n)
		}
	})
}

// autoDescription and autoStatus are what an automatic deployment reads:
// Description "Automatic deployment triggered by changes to the Api
// configuration" (get-deployments.rst L13-L17) and the stage's
// LastDeploymentStatusMessage "Successfully deployed stage with deployment
// ID '<id>'" (get-stages.rst L20), apigateway-http.md 3.4, 3.5.
const autoDescription = "Automatic deployment triggered by changes to the Api configuration"

func autoStatus(deployment string) string {
	return "Successfully deployed stage with deployment ID '" + deployment + "'"
}

// A stage with autoDeploy deploys the API before CreateStage returns, and
// again before each route or integration change returns: "If you enable
// automatic deployments, changes to an API are automatically released for
// you" (the stages page, apigateway-http.md finding 7). Each automatic
// deployment reads AutoDeployed true with the documented description and
// DEPLOYED, the stage the documented status message; earlier deployments
// stay listed. How long AWS takes is UNVERIFIED (APIGW2-04); understudy
// completes it within the call.
func TestAutoDeployDeploysBeforeTheCallReturns(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		integration := h.lambdaIntegration(id, h.createFunction("fn"), "2.0")
		h.createRoute(id, "GET /a", integration)
		stage := h.autoStage(id)
		first := deref(stage.DeploymentId)
		if first == "" || deref(stage.LastDeploymentStatusMessage) != autoStatus(first) || !deref(stage.AutoDeploy) {
			t.Fatalf("CreateStage with autoDeploy = %+v, want a deployment and %q", stage, autoStatus(first))
		}
		h.createRoute(id, "GET /b", integration)
		got := must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "$default"})
		second := deref(got.DeploymentId)
		if second == "" || second == first || deref(got.LastDeploymentStatusMessage) != autoStatus(second) {
			t.Errorf("after CreateRoute: DeploymentId %q (was %q), status %q; want a new deployment", second, first, deref(got.LastDeploymentStatusMessage))
		}
		deployments := must(h, apigatewayv2api.OpGetDeployments, &apigatewayv2api.GetDeploymentsInput{ApiId: id}).Items
		byID := map[string]apigatewayv2api.Deployment{}
		for _, d := range deployments {
			byID[deref(d.DeploymentId)] = d
		}
		for _, d := range []string{first, second} {
			dep, ok := byID[d]
			if !ok {
				t.Errorf("GetDeployments lists %v, want %s", deployments, d)
				continue
			}
			if !deref(dep.AutoDeployed) || deref(dep.Description) != autoDescription || deref(dep.DeploymentStatus) != apigatewayv2api.DeploymentStatusDeployed {
				t.Errorf("deployment %s = %+v, want AutoDeployed true, %q, DEPLOYED", d, dep, autoDescription)
			}
		}
	})
}

// wantRule checks a call governed by one of the apigatewayv2.front.answers
// switch's deployment rules, which AWS leaves UNVERIFIED for HTTP APIs
// (apigateway-http.md 3.5, APIGW2-04): Refused is BadRequestException,
// Allowed is success.
func wantRule(tb testing.TB, rule apigatewayv2.DeploymentRule, err *awserr.Error, what string) {
	tb.Helper()
	switch rule {
	case apigatewayv2.Refused:
		if err == nil {
			tb.Errorf("%s succeeded, want BadRequestException (rule %v)", what, rule)
			return
		}
		wantError(tb, err, badRequest)
	case apigatewayv2.Allowed:
		if err != nil {
			tb.Errorf("%s = %s %q, want success (rule %v)", what, err.ID.JSONType, err.Message, rule)
		}
	default:
		tb.Fatalf("unknown deployment rule %v", rule)
	}
}

// With autoDeploy, DeploymentId "Can't be updated" (the stage API
// reference, apigateway-http.md 3.4); what AWS answers is UNVERIFIED
// (APIGW2-04), so CreateStage and UpdateStage with an explicit DeploymentId
// on an automatic stage follow the switch's ExplicitOnAutoDeploy.
func TestExplicitDeploymentOnAutoDeployStage(t *testing.T) {
	rule := apigatewayv2.FrontAnswersSwitch().Value.Deployments.ExplicitOnAutoDeploy
	run(t, func(h *harness) {
		id := h.apiID("t")
		h.createRoute(id, "GET /a", "")
		dep := deref(must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id}).DeploymentId)
		_, err := h.Invoke(h.Context(), apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "auto", AutoDeploy: new(true), DeploymentId: new(dep)})
		wantRule(t, rule, err, "CreateStage with autoDeploy and a DeploymentId")
		h.autoStage(id)
		_, err = h.Invoke(h.Context(), apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "$default", DeploymentId: new(dep)})
		wantRule(t, rule, err, "UpdateStage of an autoDeploy stage with a DeploymentId")
	})
}
