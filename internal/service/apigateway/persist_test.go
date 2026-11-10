package apigateway_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
	"github.com/sustinbebustin/understudy/internal/store"
)

// read is what the control plane reads back about one API, as JSON, so
// instants compare by value.
func (h *harness) read(id string) string {
	h.Helper()
	b, err := json.Marshal(map[string]any{
		"api":         must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id}),
		"resources":   must(h, apigatewayapi.OpGetResources, &apigatewayapi.GetResourcesInput{RestApiId: id, Embed: []string{"methods"}}),
		"stages":      must(h, apigatewayapi.OpGetStages, &apigatewayapi.GetStagesInput{RestApiId: id}),
		"deployments": must(h, apigatewayapi.OpGetDeployments, &apigatewayapi.GetDeploymentsInput{RestApiId: id}),
		"tags":        must(h, apigatewayapi.OpGetTags, &apigatewayapi.GetTagsInput{ResourceArn: apiARN(id)}),
	})
	if err != nil {
		h.Fatal(err)
	}
	return string(b)
}

// State survives Export, Reset and Import (ADR 0009): every
// read answers as before, a stage still serves its deployment and not a
// later change, and the data plane finds the API by id again.
func TestExportImportRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI("/a")
		must(h, apigatewayapi.OpTagResource, &apigatewayapi.TagResourceInput{ResourceArn: apiARN(id), Tags: map[string]string{"k": "v"}})
		later := h.resource(id, h.rootOf(id), "later")
		h.proxy(id, later, "GET", h.fnARN)
		before := h.read(id)

		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		wantAnswer(t, h.get(id, "/s/a"), apigateway.RoutingSwitch().Value.UnknownAPI, "after Reset")
		if err := h.svc.Import(sections); err != nil {
			t.Fatalf("Import = %v", err)
		}
		if after := h.read(id); after != before {
			t.Errorf("after the round trip:\n%s\nwant\n%s", after, before)
		}
		if !h.svc.OwnsExecuteAPI(id) {
			t.Errorf("OwnsExecuteAPI(%s) after Import = false", id)
		}
		if rec := h.get(id, "/s/a"); rec.Code != http.StatusOK {
			t.Errorf("GET /s/a after Import = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		wantAnswer(t, h.get(id, "/s/later"), apigateway.MissingAuthenticationToken(), "a resource the deployment does not hold, after Import")
	})
}

// A section of another schema version is refused with
// *store.VersionMismatchError, leaving the state as it was.
func TestImportRefusesAnotherVersion(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.api("t")
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
		must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id})
	})
}

// The state format is version 2: version 1 sections hold resource and
// integration-response sequence numbers that resources no longer carry
// (GetResources sorts by id, APIGW1-04), so a version 1 section is refused
// with *store.VersionMismatchError naming both versions, not decoded into
// an unknown-member error (ARCHITECTURE.md 13).
func TestStateVersionTwo(t *testing.T) {
	run(t, func(h *harness) {
		if v := h.svc.SchemaVersion(); v != 2 {
			t.Errorf("SchemaVersion = %d, want 2", v)
		}
		id, _ := h.api("kept")
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		for i := range sections {
			sections[i].Version = 1
		}
		mismatch, ok := errors.AsType[*store.VersionMismatchError](h.svc.Import(sections))
		if !ok || mismatch.Section != 1 || mismatch.Running != 2 {
			t.Errorf("Import of version 1 sections = %v, want a VersionMismatchError of section 1 against 2", mismatch)
		}
		must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id})
	})
}

// Reset of one account drops its APIs and their ids.
func TestResetDropsTheScopesAPIs(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.api("mine")
		h.svc.Reset(kernel.ResetScope{Accounts: []aws.AccountID{h.Scope.Account}})
		wantError(t, fail(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id}), notFound)
		if apis := must(h, apigatewayapi.OpGetRestApis, &apigatewayapi.GetRestApisInput{}).Items; len(apis) != 0 {
			t.Errorf("GetRestApis after Reset = %+v, want none", apis)
		}
		if h.svc.OwnsExecuteAPI(id) {
			t.Errorf("OwnsExecuteAPI(%s) after Reset = true", id)
		}
	})
}
