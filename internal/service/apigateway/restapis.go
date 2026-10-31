package apigateway

import (
	"context"
	"fmt"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/apigateway/patch"
)

// updateRestAPI applies UpdateRestApi's patch operations to the API, all
// or none (apigateway-rest.md 3.8, 12.4). The generated validator has
// already refused an op outside the enum.
func (s *Service) updateRestAPI(_ context.Context, c *kernel.Call, in *apigatewayapi.UpdateRestApiInput) (*apigatewayapi.RestApi, *awserr.Error) {
	scope := c.Scope()
	ops := patch.FromModel(in.PatchOperations)
	api, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (RestAPI, *awserr.Error) {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return RestAPI{}, errAPINotFound(scope, in.RestApiId)
		}
		a, err := restAPIPatch().Apply(a, ops)
		if err != nil {
			return RestAPI{}, err
		}
		st.APIs[a.ID] = a
		return a, nil
	})
	if err != nil {
		return nil, err
	}
	out := api.output(APIStatusSwitch().Value.AfterUpdate)
	// UpdateRestApi answers "tags": {} when the API has none (recorded,
	// apigateway-rest.md 2.3).
	out.Tags = tagsOut(api.Tags)
	return &out, nil
}

// restAPIPatch is UpdateRestApi's path table (apigateway-rest.md 3.8). An
// unknown path answers "Invalid patch path <path>" (recorded, finding 5);
// an op other than replace on a replace-only path answers the recorded
// OpsOneOf(replace) text (recorded for /minimumCompressionSize, inferred
// for the others). The documented paths whose effect understudy does not
// hold yet are known paths that answer NotImplemented, so their op
// refusals stay AWS's.
func restAPIPatch() patch.Table[RestAPI] {
	add, remove, replace := apigatewayapi.OpAdd, apigatewayapi.OpRemove, apigatewayapi.OpReplace
	addRemoveReplace := []apigatewayapi.Op{add, remove, replace}
	rules := []patch.Rule[RestAPI]{
		{Path: "/name", Ops: []apigatewayapi.Op{replace}, Apply: func(a *RestAPI, op patch.Operation, _ []string) *awserr.Error {
			if op.Value == nil || *op.Value == "" {
				// name is required on create (apigateway-rest.md 3.1); the
				// text is inferred.
				return errBadRequest("Invalid patch value for /name: a REST API name cannot be empty")
			}
			a.Name = *op.Value
			return nil
		}},
		{Path: "/description", Ops: addRemoveReplace, OpText: patch.OpsAddRemoveReplace, Apply: func(a *RestAPI, op patch.Operation, _ []string) *awserr.Error {
			if op.Op == remove {
				a.Description = nil
				return nil
			}
			a.Description = clonePtr(op.Value)
			return nil
		}},
		{Path: "/binaryMediaTypes/*", Ops: addRemoveReplace, OpText: patch.OpsAddRemoveReplace, Apply: patchBinaryMediaType},
		notYet("/endpointConfiguration/types/*", patch.OpsAddRemoveReplace, add, remove, replace),
		notYet("/endpointConfiguration/vpcEndpointIds", patch.OpsOneOf(add, remove), add, remove),
		notYet("/apiKeySource", nil, replace),
		notYet("/disableExecuteApiEndpoint", nil, replace),
		notYet("/minimumCompressionSize", nil, replace),
		notYet("/policy", nil, replace),
		notYet("/endpointConfiguration/ipAddressType", nil, replace),
		notYet("/endpointAccessMode", nil, replace),
		notYet("/securityPolicy", nil, replace),
	}
	return patch.Table[RestAPI]{
		Rules:       rules,
		UnknownPath: patch.InvalidPath,
		OpText:      patch.OpsOneOf(replace),
		Clone:       cloneRestAPIForPatch,
	}
}

// patchBinaryMediaType applies add, remove or replace
// /binaryMediaTypes/<escaped type>: the list is addressed as an object
// whose key is the type, add needs no value, and replace removes the type
// and appends the value (recorded, apigateway-rest.md finding 5, 3.8).
// Adding a type already listed and removing one not listed change nothing
// (inferred).
func patchBinaryMediaType(a *RestAPI, op patch.Operation, wild []string) *awserr.Error {
	key := wild[0]
	if op.Op == apigatewayapi.OpAdd {
		if !slices.Contains(a.BinaryMediaTypes, key) {
			a.BinaryMediaTypes = append(slices.Clone(a.BinaryMediaTypes), key)
		}
		return nil
	}
	types := slices.DeleteFunc(slices.Clone(a.BinaryMediaTypes), func(t string) bool { return t == key })
	if op.Op == apigatewayapi.OpReplace {
		if op.Value == nil || *op.Value == "" {
			// The text is inferred.
			return errBadRequest("Invalid patch value for %s: replace needs the new media type as its value", op.Path)
		}
		if !slices.Contains(types, *op.Value) {
			types = append(types, *op.Value)
		}
	}
	a.BinaryMediaTypes = types
	return nil
}

// notYet is a documented UpdateRestApi path whose effect understudy does
// not hold yet: it takes the documented ops and answers NotImplemented
// naming the path, nothing changed.
func notYet(path string, opText func(patch.Operation) string, ops ...apigatewayapi.Op) patch.Rule[RestAPI] {
	return patch.Rule[RestAPI]{Path: path, Ops: ops, OpText: opText, Apply: func(_ *RestAPI, op patch.Operation, _ []string) *awserr.Error {
		return apigatewayapi.ErrNotImplemented(fmt.Sprintf(
			"understudy does not implement %s %s on UpdateRestApi yet; nothing was changed. Send the request without that patch operation.", op.Op, op.Path))
	}}
}

// cloneRestAPIForPatch copies the members restAPIPatch writes, so a refused
// request leaves the stored API as it was. The other members are shared:
// the table never writes them.
func cloneRestAPIForPatch(a RestAPI) RestAPI {
	c := a
	c.Description = clonePtr(a.Description)
	c.BinaryMediaTypes = slices.Clone(a.BinaryMediaTypes)
	return c
}
