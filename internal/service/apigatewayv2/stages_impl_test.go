package apigatewayv2_test

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
)

// Turning autoDeploy on with UpdateStage deploys the API to the stage
// before the call returns, as creating the stage with it does: "If you
// enable automatic deployments, changes to an API are automatically
// released for you" (the stages page, apigateway-http.md finding 7; the
// delay is UNVERIFIED, APIGW2-04).
func TestUpdateStageTurningAutoDeployOnDeploys(t *testing.T) {
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id := h.apiID("t")
		h.createRoute(id, "GET /a", h.lambdaIntegration(id, fnARN, "2.0"))
		must(h, apigatewayv2api.OpCreateStage, stageInput(id, "s"))
		h.allow("fn", "apigateway", h.executionARN(id)+"/*/*")
		wantAnswer(t, h.get(id, "/s/a"), apigatewayv2.FrontAnswersSwitch().Value.NoDeployment, "a stage without a deployment")
		out := must(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "s", AutoDeploy: new(true)})
		dep := deref(out.DeploymentId)
		if dep == "" || deref(out.LastDeploymentStatusMessage) != autoStatus(dep) || !deref(out.AutoDeploy) {
			t.Errorf("UpdateStage turning autoDeploy on = %+v, want a deployment and %q", out, autoStatus(dep))
		}
		if rec := h.get(id, "/s/a"); rec.Code != http.StatusOK {
			t.Errorf("GET /s/a after turning autoDeploy on = %d %s, want the function's 200", rec.Code, rec.Body)
		}
	})
}

// A stage's variables reach the function as the event's stageVariables,
// and a variable UpdateStage sends empty is removed, so the event then
// leaves stageVariables out (apigateway-http.md 3.4, 5.1).
func TestStageVariablesReachTheEvent(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /a")
		must(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "$default", StageVariables: map[string]string{"color": "blue"}})
		h.get(id, "/a")
		must(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "$default", StageVariables: map[string]string{"color": ""}})
		h.get(id, "/a")
		calls := h.fn.invocations()
		if len(calls) != 2 {
			t.Fatalf("%d invocations, want 2", len(calls))
		}
		if got := strs(calls[0].event(h)["stageVariables"]); !maps.Equal(got, map[string]string{"color": "blue"}) {
			t.Errorf("stageVariables = %v, want color: blue", got)
		}
		if v, ok := calls[1].event(h)["stageVariables"]; ok {
			t.Errorf("stageVariables after removing color = %v, want none", v)
		}
	})
}

// Any sequence of UpdateStage variable changes leaves the variables a
// merge gives: a value sets its variable, an empty value removes it
// (apigateway-http.md 3.4).
func TestUpdateStageVariablesMergeProperty(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		rapid.Check(t, func(rt *rapid.T) {
			must(h, apigatewayv2api.OpCreateStage, stageInput(id, "v"))
			want := map[string]string{}
			for range rapid.IntRange(1, 6).Draw(rt, "updates") {
				update := rapid.MapOf(rapid.SampledFrom([]string{"a", "b", "c_1"}), rapid.SampledFrom([]string{"", "1", "x/y", "k=v"})).Draw(rt, "update")
				must(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "v", StageVariables: update})
				for name, v := range update {
					if v == "" {
						delete(want, name)
						continue
					}
					want[name] = v
				}
			}
			got := must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "v"}).StageVariables
			must(h, apigatewayv2api.OpDeleteStage, &apigatewayv2api.DeleteStageInput{ApiId: id, StageName: "v"})
			if !maps.Equal(got, want) {
				rt.Fatalf("StageVariables = %v, want %v", got, want)
			}
		})
	})
}

// A merge that would leave a stage more than 100 variables is refused
// ("Stage variables per stage: 100", the HTTP API quotas page,
// apigateway-http.md 3.9), and changes nothing.
func TestUpdateStageVariableQuota(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		hundred := map[string]string{}
		for i := range 100 {
			hundred["v"+strconv.Itoa(i)] = "x"
		}
		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "s", StageVariables: hundred})
		wantError(t, fail(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "s", StageVariables: map[string]string{"extra": "x"}}), badRequest)
		must(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "s", StageVariables: map[string]string{"v0": "", "extra": "x"}})
		got := must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "s"}).StageVariables
		if _, ok := got["v0"]; ok || got["extra"] != "x" || len(got) != 100 {
			t.Errorf("StageVariables after swapping v0 for extra: %d variables, v0 present %v, extra %q; want 100, false, x", len(got), ok, got["extra"])
		}
	})
}

// A DeploymentId given to CreateStage or UpdateStage, or a StageName given
// to CreateDeployment, must name a deployment or a stage of the API
// (NotFoundException; the text is inferred, APIGW2-01), and the refused
// call changes nothing.
func TestStageAndDeploymentReferencesMustExist(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		h.createRoute(id, "GET /a", "")
		wantError(t, fail(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "s", DeploymentId: new("zzzzzz")}), notFound)
		if n := len(must(h, apigatewayv2api.OpGetStages, &apigatewayv2api.GetStagesInput{ApiId: id}).Items); n != 0 {
			t.Errorf("GetStages after a refused CreateStage = %d stages, want none", n)
		}
		must(h, apigatewayv2api.OpCreateStage, stageInput(id, "s"))
		wantError(t, fail(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{ApiId: id, StageName: "s", DeploymentId: new("zzzzzz")}), notFound)
		wantError(t, fail(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, StageName: new("nope")}), notFound)
		if n := len(must(h, apigatewayv2api.OpGetDeployments, &apigatewayv2api.GetDeploymentsInput{ApiId: id}).Items); n != 0 {
			t.Errorf("GetDeployments after a refused CreateDeployment = %d deployments, want none", n)
		}
		wantError(t, fail(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "nope"}), notFound)
		wantError(t, fail(h, apigatewayv2api.OpDeleteStage, &apigatewayv2api.DeleteStageInput{ApiId: id, StageName: "nope"}), notFound)
		wantError(t, fail(h, apigatewayv2api.OpDeleteRouteSettings, &apigatewayv2api.DeleteRouteSettingsInput{ApiId: id, StageName: "s", RouteKey: "GET /a"}), notFound)
		wantError(t, fail(h, apigatewayv2api.OpDeleteAccessLogSettings, &apigatewayv2api.DeleteAccessLogSettingsInput{ApiId: id, StageName: "nope"}), notFound)
	})
}

// DefaultRouteSettings come back as sent, with DetailedMetricsEnabled
// false when it was not sent (the create-stage CLI example,
// apigateway-http.md 3.4); UpdateStage replaces them.
func TestDefaultRouteSettings(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		out := must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{
			ApiId: id, StageName: "s", DefaultRouteSettings: &apigatewayv2api.RouteSettings{ThrottlingBurstLimit: new(int32(5))},
		})
		if d := out.DefaultRouteSettings; d == nil || deref(d.ThrottlingBurstLimit) != 5 || d.DetailedMetricsEnabled == nil || *d.DetailedMetricsEnabled {
			t.Errorf("DefaultRouteSettings = %+v, want ThrottlingBurstLimit 5 and DetailedMetricsEnabled false", out.DefaultRouteSettings)
		}
		must(h, apigatewayv2api.OpUpdateStage, &apigatewayv2api.UpdateStageInput{
			ApiId: id, StageName: "s", DefaultRouteSettings: &apigatewayv2api.RouteSettings{DetailedMetricsEnabled: new(true)},
		})
		got := must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "s"}).DefaultRouteSettings
		if got == nil || !deref(got.DetailedMetricsEnabled) || got.ThrottlingBurstLimit != nil {
			t.Errorf("DefaultRouteSettings after UpdateStage = %+v, want only DetailedMetricsEnabled true", got)
		}
	})
}

// listStages pages through GetStages of api with maxResults ("" for none).
func (h *harness) listStages(api, maxResults string) []string {
	h.Helper()
	var names []string
	in := &apigatewayv2api.GetStagesInput{ApiId: api}
	if maxResults != "" {
		in.MaxResults = new(maxResults)
	}
	for {
		out := must(h, apigatewayv2api.OpGetStages, in)
		if n, err := strconv.Atoi(maxResults); err == nil && len(out.Items) > n {
			h.Fatalf("GetStages page of %d items, asked for at most %d", len(out.Items), n)
		}
		for i := range out.Items {
			names = append(names, out.Items[i].StageName)
		}
		if out.NextToken == nil {
			return names
		}
		in.NextToken = out.NextToken
	}
}

// Paging through GetStages with any page size lists every stage once, in
// creation order (inferred, APIGW2-03), no page larger than asked
// (apigateway-http.md 2.2); a deleted stage frees its place under the
// quota of 10 (3.9).
func TestGetStagesPagingProperty(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		rapid.Check(t, func(rt *rapid.T) {
			made := rapid.IntRange(0, 10).Draw(rt, "stages")
			size := rapid.IntRange(1, 4).Draw(rt, "page size")
			names := make([]string, 0, made)
			for i := range made {
				name := "s" + strconv.Itoa(i)
				must(h, apigatewayv2api.OpCreateStage, stageInput(id, name))
				names = append(names, name)
			}
			if all := h.listStages(id, ""); !slices.Equal(all, names) {
				rt.Fatalf("GetStages = %v, want %v", all, names)
			}
			if paged := h.listStages(id, strconv.Itoa(size)); !slices.Equal(paged, names) {
				rt.Fatalf("GetStages in pages of %d = %v, want %v", size, paged, names)
			}
			for _, name := range names {
				must(h, apigatewayv2api.OpDeleteStage, &apigatewayv2api.DeleteStageInput{ApiId: id, StageName: name})
			}
		})
	})
}

// Every stage member understudy stores survives Export, Reset and Import
// (ADR 0009): GetStage answers the same.
func TestStageSettingsSurviveExportImport(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		h.createRoute(id, "GET /a", "")
		dep := deref(must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, Description: new("")}).DeploymentId)
		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{
			ApiId: id, StageName: "s", DeploymentId: new(dep), Description: new("d"),
			AccessLogSettings:    &apigatewayv2api.AccessLogSettings{DestinationArn: new("arn:aws:logs:us-east-1:000000000000:log-group:g"), Format: new("$context.requestId")},
			DefaultRouteSettings: &apigatewayv2api.RouteSettings{ThrottlingRateLimit: new(1.5)},
			RouteSettings:        map[string]apigatewayv2api.RouteSettings{"GET /a": {ThrottlingBurstLimit: new(int32(3))}},
			StageVariables:       map[string]string{"k": "v"},
			Tags:                 map[string]string{"t": "1"},
		})
		read := func() string {
			b, err := json.Marshal(map[string]any{
				"stage":      must(h, apigatewayv2api.OpGetStage, &apigatewayv2api.GetStageInput{ApiId: id, StageName: "s"}),
				"deployment": must(h, apigatewayv2api.OpGetDeployment, &apigatewayv2api.GetDeploymentInput{ApiId: id, DeploymentId: dep}),
			})
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		before := read()
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatalf("Import = %v", err)
		}
		if after := read(); after != before {
			t.Errorf("after the round trip:\n%s\nwant\n%s", after, before)
		}
	})
}

// DeleteAccessLogSettings and DeleteRouteSettings are asked about at step
// 8 as apigateway:DELETE on the AccessLogSettings and RouteSettings
// resources the ARN reference lists (apigateway-http.md 8.1); the route
// key is written as sent, which is UNVERIFIED (APIGW2-15).
func TestStageSettingsDeletesAskAboutTheirResources(t *testing.T) {
	g := newGate()
	run(t, func(h *harness) {
		id := h.apiID("t")
		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{
			ApiId: id, StageName: "prod", RouteSettings: map[string]apigatewayv2api.RouteSettings{"GET /pets": {ThrottlingBurstLimit: new(int32(1))}},
		})
		must(h, apigatewayv2api.OpDeleteAccessLogSettings, &apigatewayv2api.DeleteAccessLogSettingsInput{ApiId: id, StageName: "prod"})
		must(h, apigatewayv2api.OpDeleteRouteSettings, &apigatewayv2api.DeleteRouteSettingsInput{ApiId: id, StageName: "prod", RouteKey: "GET /pets"})
		stage := apiARN(id) + "/stages/prod"
		for op, want := range map[string][2]string{
			"DeleteAccessLogSettings": {"apigateway:DELETE", stage + "/accesslogsettings"},
			"DeleteRouteSettings":     {"apigateway:DELETE", stage + "/routesettings/GET /pets"},
		} {
			if got := g.pairs(op); len(got) != 1 || got[0] != want {
				t.Errorf("%s asked about %v, want exactly %v", op, got, want)
			}
		}
	}, kerneltest.Authorizer(g))
}
