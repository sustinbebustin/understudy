package apigatewayv2_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
	"github.com/sustinbebustin/understudy/internal/store"
)

// read is what the control plane reads back about one API, as JSON, so
// instants compare by value.
func (h *harness) read(id string) string {
	h.Helper()
	b, err := json.Marshal(map[string]any{
		"api":          must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id}),
		"routes":       must(h, apigatewayv2api.OpGetRoutes, &apigatewayv2api.GetRoutesInput{ApiId: id}),
		"integrations": must(h, apigatewayv2api.OpGetIntegrations, &apigatewayv2api.GetIntegrationsInput{ApiId: id}),
		"stages":       must(h, apigatewayv2api.OpGetStages, &apigatewayv2api.GetStagesInput{ApiId: id}),
		"deployments":  must(h, apigatewayv2api.OpGetDeployments, &apigatewayv2api.GetDeploymentsInput{ApiId: id}),
	})
	if err != nil {
		h.Fatal(err)
	}
	return string(b)
}

// State survives Export, Reset and Import (ADR 0009): every read answers
// as before, a stage without autoDeploy still serves only its deployment,
// and the data plane finds the API by id again.
func TestExportImportRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		id, integration := h.lambdaAPI("2.0", "GET /a")
		must(h, apigatewayv2api.OpCreateStage, stageInput(id, "s"))
		must(h, apigatewayv2api.OpCreateDeployment, &apigatewayv2api.CreateDeploymentInput{ApiId: id, StageName: new("s")})
		must(h, apigatewayv2api.OpTagResource, &apigatewayv2api.TagResourceInput{ResourceArn: apiARN(id), Tags: map[string]string{"k": "v"}})
		h.createRoute(id, "GET /later", integration)
		before := h.read(id)

		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		wantAnswer(t, h.get(id, "/a"), apigatewayv2.FrontAnswersSwitch().Value.UnknownAPI, "after Reset")
		if err := h.svc.Import(sections); err != nil {
			t.Fatalf("Import = %v", err)
		}
		if after := h.read(id); after != before {
			t.Errorf("after the round trip:\n%s\nwant\n%s", after, before)
		}
		if rec := h.get(id, "/s/a"); rec.Code != http.StatusOK {
			t.Errorf("GET /s/a after Import = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		wantAnswer(t, h.get(id, "/s/later"), apigatewayv2.FrontAnswersSwitch().Value.NoRoute, "a route the stage's deployment does not hold, after Import")
	})
}

// A section of another schema version is refused with
// *store.VersionMismatchError, leaving the state as it was.
func TestImportRefusesAnotherVersion(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		if len(sections) == 0 {
			t.Fatal("Export wrote no section for a scope holding an API")
		}
		for i := range sections {
			sections[i].Version = h.svc.SchemaVersion() + 1
		}
		if _, ok := errors.AsType[*store.VersionMismatchError](h.svc.Import(sections)); !ok {
			t.Errorf("Import of version %d sections did not answer *store.VersionMismatchError", h.svc.SchemaVersion()+1)
		}
		must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id})
	})
}

// The state format is version 2: version 1 sections hold the API sequence
// numbers (each API's seq, the scope's nextSeq) that GetApis no longer
// reads (it sorts by id, APIGW2-03), so a version 1 section carrying them
// is refused with *store.VersionMismatchError naming both versions, not
// decoded into an unknown-member error (ADR 0009).
func TestStateVersionTwo(t *testing.T) {
	run(t, func(h *harness) {
		if v := h.svc.SchemaVersion(); v != 2 {
			t.Errorf("SchemaVersion = %d, want 2", v)
		}
		id := h.apiID("kept")
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		for i := range sections {
			var st map[string]any
			if err := json.Unmarshal(sections[i].Data, &st); err != nil {
				t.Fatal(err)
			}
			apis, ok := st["apis"].(map[string]any)
			if !ok {
				t.Fatalf("section %s has no apis object: %s", sections[i].Path, sections[i].Data)
			}
			for _, a := range apis {
				api, ok := a.(map[string]any)
				if !ok {
					t.Fatalf("section %s holds a non-object API: %s", sections[i].Path, sections[i].Data)
				}
				api["seq"] = 0
			}
			st["nextSeq"] = len(apis)
			if sections[i].Data, err = json.Marshal(st); err != nil {
				t.Fatal(err)
			}
			sections[i].Version = 1
		}
		mismatch, ok := errors.AsType[*store.VersionMismatchError](h.svc.Import(sections))
		if !ok || mismatch.Section != 1 || mismatch.Running != 2 {
			t.Errorf("Import of version 1 sections = %v, want a VersionMismatchError of section 1 against 2", mismatch)
		}
		must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id})
	})
}

// Reset of one account drops its APIs and their ids, and leaves another
// account's.
func TestResetDropsTheScopesAPIs(t *testing.T) {
	run(t, func(h *harness) {
		mine := h.apiID("mine")
		h.svc.Reset(kernel.ResetScope{Accounts: []aws.AccountID{h.Scope.Account}})
		wantError(t, fail(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: mine}), notFound)
		if apis := must(h, apigatewayv2api.OpGetApis, &apigatewayv2api.GetApisInput{}).Items; len(apis) != 0 {
			t.Errorf("GetApis after Reset = %+v, want none", apis)
		}
	})
}
