package apigateway_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
)

// paths lists the API's resources by path, with each one's id.
func (h *harness) paths(apiID string) map[string]string {
	h.Helper()
	out := map[string]string{}
	for _, r := range must(h, apigatewayapi.OpGetResources, &apigatewayapi.GetResourcesInput{RestApiId: apiID, Limit: new(int32(500))}).Items {
		out[deref(r.Path)] = deref(r.Id)
	}
	return out
}

// The root resource "/" is created with the API with no parentId and no
// pathPart (aws-recorded, test_apigateway_api.snapshot.json:L558-L566);
// CreateResource answers id, parentId, path (the full path) and pathPart
// (apigateway-rest.md 3.2); GetResource answers the same.
func TestCreateResource(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		r := must(h, apigatewayapi.OpGetResource, &apigatewayapi.GetResourceInput{RestApiId: id, ResourceId: root})
		if deref(r.Path) != "/" || r.ParentId != nil || r.PathPart != nil {
			t.Errorf("root = %+v, want path / with no parentId and no pathPart", r)
		}
		pets := must(h, apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: id, ParentId: root, PathPart: "pets"})
		if deref(pets.ParentId) != root || deref(pets.Path) != "/pets" || deref(pets.PathPart) != "pets" || deref(pets.Id) == "" {
			t.Errorf("CreateResource pets = %+v, want parent root, path /pets", pets)
		}
		child := must(h, apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: id, ParentId: *pets.Id, PathPart: "{id}"})
		if deref(child.Path) != "/pets/{id}" || deref(child.PathPart) != "{id}" {
			t.Errorf("CreateResource {id} = %+v, want path /pets/{id}", child)
		}
		got := must(h, apigatewayapi.OpGetResource, &apigatewayapi.GetResourceInput{RestApiId: id, ResourceId: *child.Id})
		if deref(got.Path) != "/pets/{id}" || deref(got.ParentId) != *pets.Id {
			t.Errorf("GetResource = %+v, want /pets/{id} under pets", got)
		}
		if ids := []string{root, *pets.Id, *child.Id}; len(slices.Compact(slices.Sorted(slices.Values(ids)))) != 3 {
			t.Errorf("resource ids %v are not distinct", ids)
		}
		// A greedy child is accepted (aws-recorded, L1065-L1074).
		must(h, apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: id, ParentId: *pets.Id, PathPart: "x"})
		if got := h.paths(id); !maps.Equal(got, map[string]string{"/": root, "/pets": *pets.Id, "/pets/{id}": *child.Id, "/pets/x": got["/pets/x"]}) || got["/pets/x"] == "" {
			t.Errorf("GetResources paths = %v", got)
		}
	})
}

// Recorded resource refusals (aws-recorded, apigateway-rest.md 2.4 table,
// test_apigateway_api.snapshot.json lines beside each).
func TestCreateResourceRefusals(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.resource(id, root, "pets")
		wantText(t, fail(h, apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: id, ParentId: root, PathPart: "pets"}),
			conflict, "Another resource with the same parent already has this name: pets") // L728-L736
		greedy := h.resource(id, root, "{proxy+}")
		wantText(t, fail(h, apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: id, ParentId: root, PathPart: "{id}"}),
			badRequest, "A sibling ({proxy+}) of this resource already has a variable path part -- only one is allowed") // L1032-L1042
		wantText(t, fail(h, apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: id, ParentId: greedy, PathPart: "child"}),
			badRequest, "Cannot create a child of a resource with a greedy path variable: {proxy+}") // L1043-L1064
		wantText(t, fail(h, apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: id, ParentId: "nosuch", PathPart: "x"}),
			notFound, "Invalid Resource identifier specified") // L1339-L1362
		// A part outside the documented alphabet is refused (apigateway-rest.md
		// 3.2; the text is inferred: identity only).
		wantError(t, fail(h, apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: id, ParentId: root, PathPart: "a{b"}), badRequest)
		wantText(t, fail(h, apigatewayapi.OpGetResource, &apigatewayapi.GetResourceInput{RestApiId: id, ResourceId: "nosuch"}), notFound, "Invalid Resource identifier specified")
	})
}

// DeleteResource deletes the subtree: after deleting /pets only / remains
// although /pets/subpets existed, and deleting the child then answers
// "Invalid Resource identifier specified" (aws-recorded,
// test_apigateway_api.snapshot.json:L555-L670 and L813-L846,
// apigateway-rest.md 3.2).
func TestDeleteResourceDeletesTheSubtree(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		pets := h.resource(id, root, "pets")
		sub := h.resource(id, pets, "subpets")
		must(h, apigatewayapi.OpDeleteResource, &apigatewayapi.DeleteResourceInput{RestApiId: id, ResourceId: pets})
		if got := h.paths(id); !maps.Equal(got, map[string]string{"/": root}) {
			t.Errorf("GetResources after deleting /pets = %v, want only /", got)
		}
		wantText(t, fail(h, apigatewayapi.OpDeleteResource, &apigatewayapi.DeleteResourceInput{RestApiId: id, ResourceId: sub}), notFound, "Invalid Resource identifier specified")
	})
}

// UpdateResource takes replace on /pathPart and /parentId; a move changes
// the path of the whole subtree (aws-doc patch-operations,
// apigateway-rest.md 3.2). Recorded refusals (aws-recorded,
// test_apigateway_api.snapshot.json): an unknown path (L689), another op
// on a known path (L792), a cycle (L739-L748) and the root (L4909-L4935).
func TestUpdateResource(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		a := h.resource(id, root, "a")
		b := h.resource(id, root, "b")
		c := h.resource(id, a, "c")
		d := h.resource(id, c, "d")
		update := func(res string, ops ...apigatewayapi.PatchOperation) *apigatewayapi.Resource {
			return must(h, apigatewayapi.OpUpdateResource, &apigatewayapi.UpdateResourceInput{RestApiId: id, ResourceId: res, PatchOperations: ops})
		}
		if got := update(c, op(apigatewayapi.OpReplace, "/parentId", b)); deref(got.Path) != "/b/c" || deref(got.ParentId) != b {
			t.Errorf("moved c = %+v, want /b/c under b", got)
		}
		if got := h.paths(id); got["/b/c/d"] != d || got["/a/c/d"] != "" {
			t.Errorf("after the move paths = %v, want d at /b/c/d", got)
		}
		if got := update(c, op(apigatewayapi.OpReplace, "/pathPart", "e")); deref(got.Path) != "/b/e" {
			t.Errorf("renamed c = %+v, want /b/e", got)
		}
		if got := h.paths(id); got["/b/e/d"] != d {
			t.Errorf("after the rename paths = %v, want d at /b/e/d", got)
		}
		refuse := func(res string, o apigatewayapi.PatchOperation, text string) {
			t.Helper()
			wantText(t, fail(h, apigatewayapi.OpUpdateResource, &apigatewayapi.UpdateResourceInput{RestApiId: id, ResourceId: res, PatchOperations: []apigatewayapi.PatchOperation{o}}), badRequest, text)
		}
		refuse(a, op(apigatewayapi.OpReplace, "/invalid", "x"), "Invalid patch path  '/invalid' specified for op 'replace'. Must be one of: [/parentId, /pathPart]")
		refuse(a, op(apigatewayapi.OpRemove, "/pathPart"), "Invalid patch path  '/pathPart' specified for op 'remove'. Please choose supported operations")
		refuse(c, op(apigatewayapi.OpReplace, "/parentId", d), "Resources cannot be cyclical.")
		refuse(root, op(apigatewayapi.OpReplace, "/pathPart", "x"), "Root resource cannot update its pathPart.")
		refuse(root, op(apigatewayapi.OpReplace, "/parentId", a), "Root resource cannot update its parentId.")
		if got := h.paths(id); got["/b/e/d"] != d || got["/a"] != a {
			t.Errorf("refused updates changed the tree: %v", got)
		}
	})
}

// GetResources lists resourceMethods as a map of each method to {}, and
// with embed=methods embeds the full Method objects (aws-doc
// API_GetResources and api-gateway-method-settings-method-request,
// apigateway-rest.md 2.5).
func TestGetResourcesEmbed(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.method(id, root, "GET")
		find := func(items []apigatewayapi.Resource) apigatewayapi.Resource {
			for _, r := range items {
				if deref(r.Id) == root {
					return r
				}
			}
			t.Fatalf("GetResources %+v holds no root", items)
			return apigatewayapi.Resource{}
		}
		plain := find(must(h, apigatewayapi.OpGetResources, &apigatewayapi.GetResourcesInput{RestApiId: id}).Items)
		if m, ok := plain.ResourceMethods["GET"]; !ok || m.HttpMethod != nil || m.AuthorizationType != nil {
			t.Errorf("resourceMethods = %+v, want GET mapped to {}", plain.ResourceMethods)
		}
		embedded := find(must(h, apigatewayapi.OpGetResources, &apigatewayapi.GetResourcesInput{RestApiId: id, Embed: []string{"methods"}}).Items)
		if m := embedded.ResourceMethods["GET"]; deref(m.HttpMethod) != "GET" || deref(m.AuthorizationType) != "NONE" {
			t.Errorf("embedded resourceMethods = %+v, want the full GET method", embedded.ResourceMethods)
		}
	})
}
