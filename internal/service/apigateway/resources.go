package apigateway

import (
	"context"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/apigateway/patch"
	"github.com/sustinbebustin/understudy/internal/service/apigateway/resource"
	"github.com/sustinbebustin/understudy/internal/store"
)

// maxResources is the documented resources-per-API quota
// (apigateway-rest.md 2.6), the root included (inferred).
const maxResources = 300

// createResource creates a child of parentId (apigateway-rest.md 3.2;
// refusal texts recorded, 2.4).
func (s *Service) createResource(_ context.Context, c *kernel.Call, in *apigatewayapi.CreateResourceInput) (*apigatewayapi.Resource, *awserr.Error) {
	scope := c.Scope()
	r, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (Resource, *awserr.Error) {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return Resource{}, errAPINotFound(scope, in.RestApiId)
		}
		parent, _, ok := a.resource(in.ParentId)
		if !ok {
			return Resource{}, errResourceNotFound()
		}
		if _, perr := resource.ParsePart(in.PathPart); perr != nil {
			// The alphabet is documented for imports and the text is
			// inferred (apigateway-rest.md 3.2).
			return Resource{}, errBadRequest("%s", perr.Error())
		}
		if len(a.Resources) >= maxResources {
			// The quota is documented; the identity and text are inferred.
			return Resource{}, apigatewayapi.ErrLimitExceededException(
				"the REST API already holds the maximum of 300 resources; delete a resource before creating another")
		}
		id := string(s.ids.APIGatewayResourceID())
		for a.hasChild(id) {
			id = string(s.ids.APIGatewayResourceID())
		}
		r := Resource{ID: id, ParentID: parent.ID, PathPart: in.PathPart, Path: childPath(parent.Path, in.PathPart)}
		if err := a.placeable(r); err != nil {
			return Resource{}, err
		}
		a.Resources = append(slices.Clone(a.Resources), r)
		st.APIs[a.ID] = a
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	return new(r.output(false)), nil
}

// getResource answers one resource; embed=methods embeds its full methods
// (apigateway-rest.md 2.5).
func (s *Service) getResource(_ context.Context, c *kernel.Call, in *apigatewayapi.GetResourceInput) (*apigatewayapi.Resource, *awserr.Error) {
	scope := c.Scope()
	return readResult(s, scope, func(st *State) (*apigatewayapi.Resource, *awserr.Error) {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return nil, errAPINotFound(scope, in.RestApiId)
		}
		r, _, ok := a.resource(in.ResourceId)
		if !ok {
			return nil, errResourceNotFound()
		}
		return new(r.output(embedsMethods(in.Embed))), nil
	})
}

// getResources lists the API's resources sorted by id, the root among
// them (APIGW1-04, recorded 2026-11-03), paged by limit and position
// (apigateway-rest.md 2.5).
func (s *Service) getResources(_ context.Context, c *kernel.Call, in *apigatewayapi.GetResourcesInput) (*apigatewayapi.GetResourcesOutput, *awserr.Error) {
	scope := c.Scope()
	embed := embedsMethods(in.Embed)
	return readResult(s, scope, func(st *State) (*apigatewayapi.GetResourcesOutput, *awserr.Error) {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return nil, errAPINotFound(scope, in.RestApiId)
		}
		items, next, err := pageByID(a.Resources, func(r Resource) string { return r.ID }, in.Limit, in.Position)
		if err != nil {
			return nil, err
		}
		out := &apigatewayapi.GetResourcesOutput{Items: make([]apigatewayapi.Resource, 0, len(items)), Position: next}
		for i := range items {
			out.Items = append(out.Items, items[i].output(embed))
		}
		return out, nil
	})
}

// embedsMethods reports whether a Get resource request asks for the full
// methods: "The query parameter value must be a single-valued list and
// contain the "methods" string" (documented, apigateway-rest.md 2.5).
// Other values embed nothing, as no other embedding exists (inferred).
func embedsMethods(embed []string) bool { return slices.Contains(embed, "methods") }

// updateResource applies replace /parentId and /pathPart, all or none,
// then moves or renames the resource with its subtree (apigateway-rest.md
// 3.2, 3.8; refusal texts recorded, 2.4).
func (s *Service) updateResource(_ context.Context, c *kernel.Call, in *apigatewayapi.UpdateResourceInput) (*apigatewayapi.Resource, *awserr.Error) {
	scope := c.Scope()
	ops := patch.FromModel(in.PatchOperations)
	r, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (Resource, *awserr.Error) {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return Resource{}, errAPINotFound(scope, in.RestApiId)
		}
		r, i, ok := a.resource(in.ResourceId)
		if !ok {
			return Resource{}, errResourceNotFound()
		}
		edit, err := resourcePatch().Apply(resourceEdit{api: &a, id: r.ID, ParentID: r.ParentID, PathPart: r.PathPart}, ops)
		if err != nil {
			return Resource{}, err
		}
		if edit.ParentID == r.ParentID && edit.PathPart == r.PathPart {
			return r, nil
		}
		r.ParentID, r.PathPart = edit.ParentID, edit.PathPart
		// The sibling and greedy-parent rules of CreateResource hold for
		// the new place too (inferred).
		if err := a.placeable(r); err != nil {
			return Resource{}, err
		}
		a = a.withResource(i, r)
		a.repath()
		st.APIs[a.ID] = a
		moved, _, _ := a.resource(r.ID)
		return moved, nil
	})
	if err != nil {
		return nil, err
	}
	return new(r.output(false)), nil
}

// resourceEdit is what UpdateResource's patch operations change: the
// resource's parent and path part. api is the stored API, read only.
type resourceEdit struct {
	api      *RestAPI
	id       string
	ParentID string
	PathPart string
}

// resourcePatch is UpdateResource's path table: replace on /parentId and
// /pathPart only (documented, apigateway-rest.md 3.8). An unknown path and
// another op answer the recorded texts (2.4, test_apigateway_api.snapshot.json
// L689, L792).
func resourcePatch() patch.Table[resourceEdit] {
	replace := []apigatewayapi.Op{apigatewayapi.OpReplace}
	return patch.Table[resourceEdit]{
		Rules: []patch.Rule[resourceEdit]{
			{Path: "/parentId", Ops: replace, Apply: func(e *resourceEdit, op patch.Operation, _ []string) *awserr.Error {
				if e.id == e.api.RootResourceID {
					return errBadRequest("Root resource cannot update its parentId.") // recorded, 2.4
				}
				parent := deref(op.Value, "")
				if _, _, ok := e.api.resource(parent); !ok {
					// The text is inferred: the one an unknown resource id
					// answers elsewhere.
					return errResourceNotFound()
				}
				if parent == e.id || e.api.descends(parent, e.id) {
					return errBadRequest("Resources cannot be cyclical.") // recorded, 2.4
				}
				e.ParentID = parent
				return nil
			}},
			{Path: "/pathPart", Ops: replace, Apply: func(e *resourceEdit, op patch.Operation, _ []string) *awserr.Error {
				if e.id == e.api.RootResourceID {
					return errBadRequest("Root resource cannot update its pathPart.") // recorded, 2.4
				}
				part := deref(op.Value, "")
				if _, perr := resource.ParsePart(part); perr != nil {
					return errBadRequest("%s", perr.Error()) // inferred, as on CreateResource
				}
				e.PathPart = part
				return nil
			}},
		},
		UnknownPath: patch.MustBeOneOf("/parentId", "/pathPart"),
		OpText:      patch.ChooseSupported,
		Clone:       func(e resourceEdit) resourceEdit { return e },
	}
}

// deleteResource deletes the resource and its subtree (recorded,
// apigateway-rest.md 3.2). The root cannot be deleted (inferred text).
func (s *Service) deleteResource(_ context.Context, c *kernel.Call, in *apigatewayapi.DeleteResourceInput) (*apigatewayapi.DeleteResourceOutput, *awserr.Error) {
	scope := c.Scope()
	_, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return struct{}{}, errAPINotFound(scope, in.RestApiId)
		}
		if _, _, ok := a.resource(in.ResourceId); !ok {
			return struct{}{}, errResourceNotFound()
		}
		if in.ResourceId == a.RootResourceID {
			return struct{}{}, errBadRequest("The root resource cannot be deleted; delete the REST API instead")
		}
		a.Resources = slices.DeleteFunc(slices.Clone(a.Resources), func(r Resource) bool {
			return r.ID == in.ResourceId || a.descends(r.ID, in.ResourceId)
		})
		st.APIs[a.ID] = a
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &apigatewayapi.DeleteResourceOutput{}, nil
}

// readResult runs fn on the state of scope without creating its cell.
func readResult[R any](s *Service, scope aws.Scope, fn func(st *State) (R, *awserr.Error)) (R, *awserr.Error) {
	return store.View2(s.cells, scope, fn)
}

// childPath is the full path of a child with part under a parent at
// parentPath.
func childPath(parentPath, part string) string {
	if parentPath == "/" {
		return "/" + part
	}
	return parentPath + "/" + part
}

// descends reports whether the resource id lies below ancestor. The tree
// has no cycles: UpdateResource refuses one.
func (a *RestAPI) descends(id, ancestor string) bool {
	for {
		r, _, ok := a.resource(id)
		if !ok || r.ParentID == "" {
			return false
		}
		if r.ParentID == ancestor {
			return true
		}
		id = r.ParentID
	}
}

// placeable checks r against its parent and siblings (recorded,
// apigateway-rest.md 2.4): a greedy resource has no children, a name is
// unique among siblings, and at most one sibling is a path variable. A
// rename to a greedy part is refused while r has children, with the same
// text (inferred): the matcher binds a greedy part last.
func (a *RestAPI) placeable(r Resource) *awserr.Error {
	parent, _, _ := a.resource(r.ParentID)
	if isGreedy(parent.PathPart) {
		return errBadRequest("Cannot create a child of a resource with a greedy path variable: %s", parent.PathPart)
	}
	if isGreedy(r.PathPart) && slices.ContainsFunc(a.Resources, func(c Resource) bool { return c.ParentID == r.ID }) {
		return errBadRequest("Cannot create a child of a resource with a greedy path variable: %s", r.PathPart)
	}
	variable := isVariable(r.PathPart)
	for _, sib := range a.Resources {
		if sib.ParentID != r.ParentID || sib.ID == r.ID {
			continue
		}
		if sib.PathPart == r.PathPart {
			return apigatewayapi.ErrConflictException("Another resource with the same parent already has this name: " + r.PathPart)
		}
		if variable && isVariable(sib.PathPart) {
			return errBadRequest("A sibling (%s) of this resource already has a variable path part -- only one is allowed", sib.PathPart)
		}
	}
	return nil
}

// isVariable reports whether a stored path part is {name} or {name+}.
// Stored parts were parsed on the way in; the root's "" is neither.
func isVariable(part string) bool {
	p, err := resource.ParsePart(part)
	if err != nil {
		return false
	}
	switch p.(type) {
	case resource.Var, resource.Greedy:
		return true
	case resource.Literal:
		return false
	}
	return false
}

// isGreedy reports whether a stored path part is {name+}; the root's ""
// is not.
func isGreedy(part string) bool {
	p, err := resource.ParsePart(part)
	if err != nil {
		return false
	}
	_, ok := p.(resource.Greedy)
	return ok
}

// repath recomputes every resource's path from its parent and path part,
// after a move or a rename changed a subtree.
func (a *RestAPI) repath() {
	byID := make(map[string]int, len(a.Resources))
	for i, r := range a.Resources {
		byID[r.ID] = i
	}
	paths := map[string]string{}
	var pathOf func(id string) string
	pathOf = func(id string) string {
		if p, ok := paths[id]; ok {
			return p
		}
		r := a.Resources[byID[id]]
		p := "/"
		if r.ParentID != "" {
			p = childPath(pathOf(r.ParentID), r.PathPart)
		}
		paths[id] = p
		return p
	}
	for i := range a.Resources {
		a.Resources[i].Path = pathOf(a.Resources[i].ID)
	}
}

// output is the resource as the resource operations answer it:
// resourceMethods maps each method to {} or, with embed, to the full
// method (documented, apigateway-rest.md 2.5); the root has no parentId
// and no pathPart (recorded, 3.2).
func (r *Resource) output(embed bool) apigatewayapi.Resource {
	out := apigatewayapi.Resource{Id: new(r.ID), Path: new(r.Path)}
	if r.ParentID != "" {
		out.ParentId, out.PathPart = new(r.ParentID), new(r.PathPart)
	}
	if len(r.Methods) > 0 {
		out.ResourceMethods = make(map[string]apigatewayapi.Method, len(r.Methods))
		for verb, m := range r.Methods {
			if embed {
				out.ResourceMethods[verb] = m.output()
			} else {
				out.ResourceMethods[verb] = apigatewayapi.Method{}
			}
		}
	}
	return out
}
