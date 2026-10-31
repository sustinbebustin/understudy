package apigateway_test

import (
	"maps"
	"net/http"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
)

// GetResources pages by limit and position (documented: limit 1 to 500,
// apigateway-rest.md 2.5); the items come in creation order, root first
// (the GetResources coverage note, inferred, APIGW1-04), and every one
// appears once across the pages.
func TestGetResourcesPages(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		parts := []string{"d", "c", "b", "a"}
		want := make([]string, 0, len(parts)+1)
		want = append(want, root)
		for _, part := range parts {
			want = append(want, h.resource(id, root, part))
		}
		var got []string
		var position *string
		for range want {
			page := must(h, apigatewayapi.OpGetResources, &apigatewayapi.GetResourcesInput{RestApiId: id, Limit: new(int32(2)), Position: position})
			if len(page.Items) > 2 {
				t.Fatalf("a page of limit 2 held %d items", len(page.Items))
			}
			for _, r := range page.Items {
				got = append(got, deref(r.Id))
			}
			if position = page.Position; position == nil {
				break
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("pages listed %v, want %v", got, want)
		}
		for _, limit := range []int32{0, 501} {
			wantError(t, fail(h, apigatewayapi.OpGetResources, &apigatewayapi.GetResourcesInput{RestApiId: id, Limit: new(limit)}), badRequest)
		}
		wantError(t, fail(h, apigatewayapi.OpGetResources, &apigatewayapi.GetResourcesInput{RestApiId: id, Position: new("not a position")}), badRequest)
		wantText(t, fail(h, apigatewayapi.OpGetResources, &apigatewayapi.GetResourcesInput{RestApiId: "nosuch0001"}),
			notFound, "Invalid API identifier specified "+account+":nosuch0001")
	})
}

// A move or a rename keeps CreateResource's rules (recorded texts,
// apigateway-rest.md 2.4; that UpdateResource applies them is inferred),
// a resource cannot become its own parent, an unknown parent or an invalid
// part is refused, and a refused update changes nothing.
func TestUpdateResourceKeepsTheTreeRules(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		a := h.resource(id, root, "a")
		v := h.resource(id, a, "{x}")
		b := h.resource(id, root, "b")
		w := h.resource(id, b, "{y}")
		g := h.resource(id, root, "{proxy+}")
		before := h.paths(id)
		update := func(res string, o apigatewayapi.PatchOperation) *apigatewayapi.UpdateResourceInput {
			return &apigatewayapi.UpdateResourceInput{RestApiId: id, ResourceId: res, PatchOperations: []apigatewayapi.PatchOperation{o}}
		}
		wantText(t, fail(h, apigatewayapi.OpUpdateResource, update(w, op(apigatewayapi.OpReplace, "/parentId", a))),
			badRequest, "A sibling ({x}) of this resource already has a variable path part -- only one is allowed")
		wantText(t, fail(h, apigatewayapi.OpUpdateResource, update(b, op(apigatewayapi.OpReplace, "/parentId", g))),
			badRequest, "Cannot create a child of a resource with a greedy path variable: {proxy+}")
		wantText(t, fail(h, apigatewayapi.OpUpdateResource, update(b, op(apigatewayapi.OpReplace, "/pathPart", "a"))),
			conflict, "Another resource with the same parent already has this name: a")
		wantText(t, fail(h, apigatewayapi.OpUpdateResource, update(a, op(apigatewayapi.OpReplace, "/parentId", a))),
			badRequest, "Resources cannot be cyclical.")
		wantText(t, fail(h, apigatewayapi.OpUpdateResource, update(a, op(apigatewayapi.OpReplace, "/parentId", "nosuch"))),
			notFound, "Invalid Resource identifier specified")
		wantError(t, fail(h, apigatewayapi.OpUpdateResource, update(a, op(apigatewayapi.OpReplace, "/pathPart", "a/b"))), badRequest)
		wantText(t, fail(h, apigatewayapi.OpUpdateResource, update("nosuch", op(apigatewayapi.OpReplace, "/pathPart", "z"))),
			notFound, "Invalid Resource identifier specified")
		// All or nothing: a valid rename followed by a refused op changes
		// nothing (apigateway-rest.md 12.4, inferred).
		wantError(t, fail(h, apigatewayapi.OpUpdateResource, &apigatewayapi.UpdateResourceInput{RestApiId: id, ResourceId: a, PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/pathPart", "z"), op(apigatewayapi.OpRemove, "/pathPart"),
		}}), badRequest)
		if got := h.paths(id); !maps.Equal(got, before) {
			t.Errorf("refused updates changed the tree: %v, want %v", got, before)
		}
		// Two ops in one request apply in order: rename, then move.
		c := h.resource(id, root, "c")
		got := must(h, apigatewayapi.OpUpdateResource, &apigatewayapi.UpdateResourceInput{RestApiId: id, ResourceId: v, PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/pathPart", "{z}"), op(apigatewayapi.OpReplace, "/parentId", c),
		}})
		if deref(got.Path) != "/c/{z}" || deref(got.ParentId) != c || deref(got.PathPart) != "{z}" {
			t.Errorf("renamed and moved {x} = %+v, want /c/{z} under c", got)
		}
	})
}

// The root resource cannot be deleted (inferred: the API's root has no
// parent to hold it, apigateway-rest.md 3.2); it stays, and GetResource
// with embed=methods embeds the full methods as GetResources does
// (documented, 2.5).
func TestRootResourceStays(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		wantError(t, fail(h, apigatewayapi.OpDeleteResource, &apigatewayapi.DeleteResourceInput{RestApiId: id, ResourceId: root}), badRequest)
		h.method(id, root, "GET")
		got := must(h, apigatewayapi.OpGetResource, &apigatewayapi.GetResourceInput{RestApiId: id, ResourceId: root, Embed: []string{"methods"}})
		if m := got.ResourceMethods["GET"]; deref(got.Path) != "/" || deref(m.HttpMethod) != "GET" {
			t.Errorf("GetResource root with embed = %+v, want / with the full GET method", got)
		}
		plain := must(h, apigatewayapi.OpGetResource, &apigatewayapi.GetResourceInput{RestApiId: id, ResourceId: root})
		if m, ok := plain.ResourceMethods["GET"]; !ok || m.HttpMethod != nil {
			t.Errorf("GetResource root = %+v, want GET mapped to {}", plain)
		}
	})
}

// Until resource matching lands (spec 6b ticket 8), a request the root
// does not serve, on a deployment that holds other resources, answers 501
// instead of a 403 AWS might not give; the root is still served.
func TestInvokeBeyondTheRootAnswers501(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.proxyAPI("/pets")
		want501(t, h.get(id, "/s/pets"), "resources other than the root", "GET /s/pets")
		h.wantJournaled501("GET /s/pets")
		h.notInvoked("GET /s/pets")
		if rec := h.get(id, "/s/"); rec.Code != http.StatusOK {
			t.Errorf("GET /s/ = %d %s, want the function's 200", rec.Code, rec.Body)
		}
	})
}
