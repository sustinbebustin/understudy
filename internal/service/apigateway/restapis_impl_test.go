package apigateway_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
)

// More of UpdateRestApi's patch table: remove /description clears
// it; an unknown path answers "Invalid patch path <path>" (finding 5); an
// op other than add, remove or replace on /description or a binary media
// type answers the three-op text (L4774's shape, inferred here); an empty
// name and a replace without a value are refused (texts inferred, tested
// by status only); every refusal leaves the API as it was
// (apigateway-rest.md 3.8, 12.4).
func TestUpdateRestApiTable(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.api("t")
		update := func(ops ...apigatewayapi.PatchOperation) *apigatewayapi.UpdateRestApiInput {
			return &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: ops}
		}
		must(h, apigatewayapi.OpUpdateRestApi, update(op(apigatewayapi.OpAdd, "/description", "d"), op(apigatewayapi.OpAdd, "/binaryMediaTypes/image~1png")))
		out := must(h, apigatewayapi.OpUpdateRestApi, update(op(apigatewayapi.OpRemove, "/description")))
		if out.Description != nil || !slices.Equal(out.BinaryMediaTypes, []string{"image/png"}) {
			t.Errorf("after remove /description = %+v, want no description and image/png kept", out)
		}
		keep := func(o apigatewayapi.PatchOperation) *apigatewayapi.UpdateRestApiInput {
			return update(op(apigatewayapi.OpReplace, "/name", "changed"), o)
		}
		wantText(t, fail(h, apigatewayapi.OpUpdateRestApi, keep(op(apigatewayapi.OpReplace, "/nope", "x"))), badRequest, "Invalid patch path /nope")
		wantText(t, fail(h, apigatewayapi.OpUpdateRestApi, keep(op(apigatewayapi.OpCopy, "/description", "x"))), badRequest,
			"Invalid patch operation specified. Must be 'add'|'remove'|'replace'")
		wantText(t, fail(h, apigatewayapi.OpUpdateRestApi, keep(op(apigatewayapi.OpMove, "/binaryMediaTypes/image~1png"))), badRequest,
			"Invalid patch operation specified. Must be 'add'|'remove'|'replace'")
		wantText(t, fail(h, apigatewayapi.OpUpdateRestApi, keep(op(apigatewayapi.OpAdd, "/name", "x"))), badRequest,
			"Invalid patch operation specified. Must be one of: [replace]")
		wantError(t, fail(h, apigatewayapi.OpUpdateRestApi, keep(op(apigatewayapi.OpReplace, "/name", ""))), badRequest)
		wantError(t, fail(h, apigatewayapi.OpUpdateRestApi, keep(op(apigatewayapi.OpReplace, "/binaryMediaTypes/image~1png"))), badRequest)
		got := must(h, apigatewayapi.OpGetRestApi, &apigatewayapi.GetRestApiInput{RestApiId: id})
		if deref(got.Name) != "t" || !slices.Equal(got.BinaryMediaTypes, []string{"image/png"}) {
			t.Errorf("after refused updates = %+v, want name t and [image/png]", got)
		}
	})
}

// binaryMediaTypes added by UpdateRestApi reach the data plane only through
// a new deployment, which then answers 501 naming them, journaled, without
// running the function (the binaryMediaTypes half of
// TestSnapshotAndLiveSettings).
func TestBinaryMediaTypesFollowTheDeployment(t *testing.T) {
	run(t, func(h *harness) {
		id := h.proxyRoot(&apigatewayapi.CreateRestApiInput{Name: "t"}, nil)
		h.deploy(id)
		must(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpAdd, "/binaryMediaTypes/image~1png"),
		}})
		if rec := h.get(id, "/s/"); rec.Code != http.StatusOK {
			t.Fatalf("before redeploying: GET = %d %s, want the old snapshot served", rec.Code, rec.Body)
		}
		h.deploy(id)
		want501(t, h.get(id, "/s/"), "binaryMediaTypes", "after redeploying")
		h.wantJournaled501("binaryMediaTypes")
	})
}

// An unknown API answers the recorded NotFoundException (apigateway-rest.md
// 2.4).
func TestUpdateUnknownRestApi(t *testing.T) {
	run(t, func(h *harness) {
		wantText(t, fail(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: "nope", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/name", "x"),
		}}), notFound, "Invalid API identifier specified "+account+":nope")
	})
}
