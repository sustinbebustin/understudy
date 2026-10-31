package apigateway

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/apigateway/patch"
)

// Methods and method responses (apigateway-rest.md 3.3, 3.5; texts in
// 2.4).

// httpMethods are the methods PutMethod accepts (recorded,
// apigateway-rest.md 2.4).
func httpMethods() []string {
	return []string{"DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT", "ANY"}
}

// seededModels are the models every new REST API carries
// (apigateway-rest.md 3.9: Empty documented and recorded; Error inferred,
// APIGW1-02).
func seededModels() []string { return []string{"Empty", "Error"} }

// The recorded PutMethod refusals (apigateway-rest.md 2.4).
const (
	textNoAuthorizer       = "Invalid authorizer ID specified. Setting the authorization type to CUSTOM or COGNITO_USER_POOLS requires a valid authorizer."
	textUniqueParameters   = "Parameter names must be unique across querystring, header and path"
	textNoRequestValidator = "Invalid Request Validator identifier specified"
)

// checkAuthorizationType refuses an authorizationType understudy cannot
// hold. CUSTOM and COGNITO_USER_POOLS need an authorizer, and none can
// exist since authorizers are unbound (recorded text, apigateway-rest.md
// 2.4). What AWS answers for another value is not recorded (3.3), so it
// answers NotImplemented naming it.
func checkAuthorizationType(op, t string) *awserr.Error {
	switch t {
	case "NONE", "AWS_IAM":
		return nil
	case "CUSTOM", "COGNITO_USER_POOLS":
		return errBadRequest("%s", textNoAuthorizer)
	default:
		return refuseUnimplemented(op, member{"authorizationType " + t, true})
	}
}

// checkRequestParameters refuses a parameter name used in two of the
// querystring, header and path locations (recorded, apigateway-rest.md
// 2.4). Keys are method.request.<location>.<name> (3.3); other keys are
// stored as sent.
func checkRequestParameters(params map[string]bool) *awserr.Error {
	seen := map[string]string{}
	for _, key := range slices.Sorted(maps.Keys(params)) {
		rest, ok := strings.CutPrefix(key, "method.request.")
		if !ok {
			continue
		}
		location, name, ok := strings.Cut(rest, ".")
		if !ok || (location != "querystring" && location != "header" && location != "path") {
			continue
		}
		if other, dup := seen[name]; dup && other != location {
			return errBadRequest("%s", textUniqueParameters)
		}
		seen[name] = location
	}
	return nil
}

// checkModels refuses a model name the API does not hold: only the seeded
// Empty and Error until models are bound. The text is recorded for
// PutMethod's requestModels (apigateway-rest.md 2.4) and inferred for a
// method response's responseModels.
func (a *RestAPI) checkModels(models map[string]string) *awserr.Error {
	for _, contentType := range slices.Sorted(maps.Keys(models)) {
		if name := models[contentType]; !slices.Contains(a.Models, name) {
			return errBadRequest("Invalid model identifier specified: %s", name)
		}
	}
	return nil
}

// putMethod creates or replaces a method of a resource (apigateway-rest.md
// 3.3; replacing is inferred). The replaced method's responses and
// integration go with it.
func (s *Service) putMethod(_ context.Context, c *kernel.Call, in *apigatewayapi.PutMethodInput) (*apigatewayapi.Method, *awserr.Error) {
	if !slices.Contains(httpMethods(), in.HttpMethod) {
		return nil, errBadRequest("Invalid HttpMethod specified. Valid options are GET,PUT,POST,DELETE,PATCH,OPTIONS,HEAD,ANY")
	}
	if err := checkAuthorizationType("PutMethod", in.AuthorizationType); err != nil {
		return nil, err
	}
	if deref(in.AuthorizerId, "") != "" {
		// No authorizer can exist; the text is the recorded one for a
		// CUSTOM method without one (inferred for this case).
		return nil, errBadRequest("%s", textNoAuthorizer)
	}
	if err := checkRequestParameters(in.RequestParameters); err != nil {
		return nil, err
	}
	if deref(in.RequestValidatorId, "") != "" {
		// No request validator can exist (recorded, apigateway-rest.md 2.4).
		return nil, errBadRequest("%s", textNoRequestValidator)
	}
	m := Method{
		HTTPMethod: in.HttpMethod, Authorization: in.AuthorizationType, APIKeyRequired: in.ApiKeyRequired,
		OperationName: clonePtr(in.OperationName), RequestParameters: maps.Clone(in.RequestParameters),
		RequestModels: maps.Clone(in.RequestModels), AuthorizationScopes: slices.Clone(in.AuthorizationScopes),
	}
	scope := c.Scope()
	_, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return struct{}{}, errAPINotFound(scope, in.RestApiId)
		}
		r, i, ok := a.resource(in.ResourceId)
		if !ok {
			return struct{}{}, errResourceNotFound()
		}
		if err := a.checkModels(m.RequestModels); err != nil {
			return struct{}{}, err
		}
		r = r.withMethods()
		r.Methods[in.HttpMethod] = m.clone()
		st.APIs[a.ID] = a.withResource(i, r)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return new(m.output()), nil
}

// methodRef names one method of one resource of one REST API.
type methodRef struct{ api, resource, verb string }

// readMethod runs fn on the method ref names, or answers the recorded
// NotFound for the first of the API, resource and method that is missing.
func readMethod[R any](s *Service, c *kernel.Call, ref methodRef, fn func(a *RestAPI, m *Method) (R, *awserr.Error)) (R, *awserr.Error) {
	scope := c.Scope()
	type result struct {
		v   R
		err *awserr.Error
	}
	res := readState(s, scope, func(st *State) result {
		a, ok := st.APIs[ref.api]
		if !ok {
			return result{err: errAPINotFound(scope, ref.api)}
		}
		r, _, ok := a.resource(ref.resource)
		if !ok {
			return result{err: errResourceNotFound()}
		}
		m, ok := r.Methods[ref.verb]
		if !ok {
			return result{err: errMethodNotFound()}
		}
		v, err := fn(&a, &m)
		return result{v, err}
	})
	return res.v, res.err
}

// writeMethod runs fn on a copy of the method ref names and stores the
// copy unless fn refuses; fn returning a nil method deletes it.
func writeMethod[R any](s *Service, c *kernel.Call, ref methodRef, fn func(a *RestAPI, m Method) (*Method, R, *awserr.Error)) (R, *awserr.Error) {
	scope := c.Scope()
	return kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (R, *awserr.Error) {
		var zero R
		a, ok := st.APIs[ref.api]
		if !ok {
			return zero, errAPINotFound(scope, ref.api)
		}
		r, i, ok := a.resource(ref.resource)
		if !ok {
			return zero, errResourceNotFound()
		}
		m, ok := r.Methods[ref.verb]
		if !ok {
			return zero, errMethodNotFound()
		}
		next, v, err := fn(&a, m.clone())
		if err != nil {
			return zero, err
		}
		r = r.withMethods()
		if next == nil {
			delete(r.Methods, ref.verb)
		} else {
			r.Methods[ref.verb] = *next
		}
		st.APIs[a.ID] = a.withResource(i, r)
		return v, nil
	})
}

// getMethod answers a method with its responses and integration
// (recorded, apigateway-rest.md 2.3).
func (s *Service) getMethod(_ context.Context, c *kernel.Call, in *apigatewayapi.GetMethodInput) (*apigatewayapi.Method, *awserr.Error) {
	return readMethod(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(_ *RestAPI, m *Method) (*apigatewayapi.Method, *awserr.Error) {
		return new(m.output()), nil
	})
}

// deleteMethod removes a method with its responses and integration
// (inferred, apigateway-rest.md 3.3).
func (s *Service) deleteMethod(_ context.Context, c *kernel.Call, in *apigatewayapi.DeleteMethodInput) (*apigatewayapi.DeleteMethodOutput, *awserr.Error) {
	return writeMethod(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(*RestAPI, Method) (*Method, *apigatewayapi.DeleteMethodOutput, *awserr.Error) {
		return nil, &apigatewayapi.DeleteMethodOutput{}, nil
	})
}

// updateMethod applies UpdateMethod's patch operations, all or none
// (apigateway-rest.md 3.8).
func (s *Service) updateMethod(_ context.Context, c *kernel.Call, in *apigatewayapi.UpdateMethodInput) (*apigatewayapi.Method, *awserr.Error) {
	ops := patch.FromModel(in.PatchOperations)
	return writeMethod(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(a *RestAPI, m Method) (*Method, *apigatewayapi.Method, *awserr.Error) {
		next, err := methodPatch(a, in.ResourceId).Apply(m, ops)
		if err != nil {
			return nil, nil, err
		}
		return &next, new(next.output()), nil
	})
}

// methodPatch is UpdateMethod's path table (apigateway-rest.md 3.8) for a
// method of resource resourceID of a. An unknown path answers "Invalid
// patch path <path>" and an op other than replace on a replace-only path
// the OpsOneOf(replace) text (both recorded, contract 6b section 5); the
// op texts of the map and list paths are the recorded ones of other
// resources' alike paths (inferred). Each value is checked as PutMethod
// checks it.
func methodPatch(a *RestAPI, resourceID string) patch.Table[Method] {
	add, remove, replace := apigatewayapi.OpAdd, apigatewayapi.OpRemove, apigatewayapi.OpReplace
	addRemoveReplace := []apigatewayapi.Op{add, remove, replace}
	rules := []patch.Rule[Method]{
		{Path: "/authorizationType", Ops: []apigatewayapi.Op{replace}, Apply: func(m *Method, op patch.Operation, _ []string) *awserr.Error {
			t := deref(op.Value, "")
			if err := checkAuthorizationType("UpdateMethod", t); err != nil {
				return err
			}
			m.Authorization = t
			return nil
		}},
		{Path: "/authorizerId", Ops: []apigatewayapi.Op{replace}, Apply: func(_ *Method, op patch.Operation, _ []string) *awserr.Error {
			if deref(op.Value, "") != "" {
				// No authorizer can exist; the text is inferred, as on
				// PutMethod.
				return errBadRequest("%s", textNoAuthorizer)
			}
			return nil
		}},
		{Path: "/apiKeyRequired", Ops: []apigatewayapi.Op{replace}, Apply: func(m *Method, op patch.Operation, _ []string) *awserr.Error {
			v, err := patchBool(op)
			m.APIKeyRequired = v
			return err
		}},
		{Path: "/operationName", Ops: []apigatewayapi.Op{replace}, Apply: func(m *Method, op patch.Operation, _ []string) *awserr.Error {
			m.OperationName = clonePtr(op.Value)
			return nil
		}},
		{Path: "/requestValidatorId", Ops: []apigatewayapi.Op{replace}, Apply: func(_ *Method, op patch.Operation, _ []string) *awserr.Error {
			if deref(op.Value, "") != "" {
				// No request validator can exist (recorded text on
				// PutMethod, apigateway-rest.md 2.4).
				return errBadRequest("%s", textNoRequestValidator)
			}
			return nil
		}},
		{Path: "/requestParameters/*", Ops: addRemoveReplace, OpText: patch.OpsAddRemoveReplace, Apply: func(m *Method, op patch.Operation, wild []string) *awserr.Error {
			if op.Op == remove {
				m.RequestParameters = deleted(m.RequestParameters, wild[0])
				return nil
			}
			v, err := patchBool(op)
			if err != nil {
				return err
			}
			m.RequestParameters = with(m.RequestParameters, wild[0], v)
			return checkRequestParameters(m.RequestParameters)
		}},
		{Path: "/requestModels/*", Ops: addRemoveReplace, OpText: patch.OpsAddRemoveReplace, Apply: func(m *Method, op patch.Operation, wild []string) *awserr.Error {
			if op.Op == remove {
				m.RequestModels = deleted(m.RequestModels, wild[0])
				return nil
			}
			models := map[string]string{wild[0]: deref(op.Value, "")}
			if err := a.checkModels(models); err != nil {
				return err
			}
			m.RequestModels = with(m.RequestModels, wild[0], models[wild[0]])
			return nil
		}},
		{Path: "/authorizationScopes", Ops: []apigatewayapi.Op{add, remove}, OpText: patch.OpsOneOf(add, remove), Apply: func(m *Method, op patch.Operation, _ []string) *awserr.Error {
			scope := deref(op.Value, "")
			if scope == "" {
				// The text is inferred.
				return errBadRequest("Invalid patch value for /authorizationScopes: %s needs the scope as its value", op.Op)
			}
			m.AuthorizationScopes = slices.DeleteFunc(slices.Clone(m.AuthorizationScopes), func(s string) bool { return s == scope })
			if op.Op == add {
				m.AuthorizationScopes = append(m.AuthorizationScopes, scope)
			}
			return nil
		}},
		// Terraform sends replace /resourceId with the method's own
		// resource (apigateway-rest.md 3.8); that changes nothing
		// (inferred). Moving a method to another resource is not
		// documented, so it answers NotImplemented.
		{Path: "/resourceId", Ops: []apigatewayapi.Op{replace}, Apply: func(_ *Method, op patch.Operation, _ []string) *awserr.Error {
			if deref(op.Value, "") == resourceID {
				return nil
			}
			return refuseUnimplemented("UpdateMethod", member{"replace /resourceId with another resource", true})
		}},
	}
	return patch.Table[Method]{
		Rules:       rules,
		UnknownPath: patch.InvalidPath,
		OpText:      patch.OpsOneOf(replace),
		Clone:       func(m Method) Method { return m.clone() },
	}
}

// patchBool reads a patch operation's boolean value, "true" or "false";
// the text for another is inferred.
func patchBool(op patch.Operation) (bool, *awserr.Error) {
	switch deref(op.Value, "") {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errBadRequest("Invalid patch value for %s: expected true or false", op.Path)
	}
}

// with is a copy of mp with key set to v.
func with[V any](mp map[string]V, key string, v V) map[string]V {
	c := maps.Clone(mp)
	if c == nil {
		c = map[string]V{}
	}
	c[key] = v
	return c
}

// deleted is a copy of mp without key; removing an absent key changes
// nothing (inferred).
func deleted[V any](mp map[string]V, key string) map[string]V {
	c := maps.Clone(mp)
	delete(c, key)
	return c
}

// output is the method as the method operations answer it and
// GetResources embeds it: apiKeyRequired always, the stored members, and
// its responses and integration when it has them (recorded,
// apigateway-rest.md 2.3).
func (m *Method) output() apigatewayapi.Method {
	out := apigatewayapi.Method{
		HttpMethod: new(m.HTTPMethod), AuthorizationType: new(m.Authorization), ApiKeyRequired: new(m.APIKeyRequired),
		OperationName: clonePtr(m.OperationName), RequestParameters: maps.Clone(m.RequestParameters),
		RequestModels: maps.Clone(m.RequestModels), AuthorizationScopes: slices.Clone(m.AuthorizationScopes),
	}
	if len(m.Responses) > 0 {
		out.MethodResponses = make(map[string]apigatewayapi.MethodResponse, len(m.Responses))
		for code, r := range m.Responses {
			out.MethodResponses[code] = r.output()
		}
	}
	if m.Integration != nil {
		out.MethodIntegration = new(m.Integration.output())
	}
	return out
}

// output is the method response as the method response operations answer
// it.
func (r *MethodResponse) output() apigatewayapi.MethodResponse {
	return apigatewayapi.MethodResponse{StatusCode: new(r.StatusCode), ResponseParameters: maps.Clone(r.ResponseParameters), ResponseModels: maps.Clone(r.ResponseModels)}
}

// putMethodResponse creates or replaces a method response; the generated
// validator has already refused a status outside [1-5]\d\d
// (apigateway-rest.md 3.5). Replacing is inferred, and keeps Terraform
// clear of a ConflictException (12.8).
func (s *Service) putMethodResponse(_ context.Context, c *kernel.Call, in *apigatewayapi.PutMethodResponseInput) (*apigatewayapi.MethodResponse, *awserr.Error) {
	resp := MethodResponse{StatusCode: in.StatusCode, ResponseParameters: maps.Clone(in.ResponseParameters), ResponseModels: maps.Clone(in.ResponseModels)}
	return writeMethod(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(a *RestAPI, m Method) (*Method, *apigatewayapi.MethodResponse, *awserr.Error) {
		if err := a.checkModels(resp.ResponseModels); err != nil {
			return nil, nil, err
		}
		m.Responses = with(m.Responses, resp.StatusCode, resp.clone())
		return &m, new(resp.output()), nil
	})
}

// getMethodResponse answers one method response.
func (s *Service) getMethodResponse(_ context.Context, c *kernel.Call, in *apigatewayapi.GetMethodResponseInput) (*apigatewayapi.MethodResponse, *awserr.Error) {
	return readMethod(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(_ *RestAPI, m *Method) (*apigatewayapi.MethodResponse, *awserr.Error) {
		r, ok := m.Responses[in.StatusCode]
		if !ok {
			return nil, errResponseNotFound()
		}
		return new(r.output()), nil
	})
}

// updateMethodResponse applies UpdateMethodResponse's patch operations,
// all or none; it answers 201 (the model's code, recorded,
// apigateway-rest.md 3.5).
func (s *Service) updateMethodResponse(_ context.Context, c *kernel.Call, in *apigatewayapi.UpdateMethodResponseInput) (*apigatewayapi.MethodResponse, *awserr.Error) {
	ops := patch.FromModel(in.PatchOperations)
	return writeMethod(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(a *RestAPI, m Method) (*Method, *apigatewayapi.MethodResponse, *awserr.Error) {
		r, ok := m.Responses[in.StatusCode]
		if !ok {
			return nil, nil, errResponseNotFound()
		}
		r, err := methodResponsePatch(a).Apply(r, ops)
		if err != nil {
			return nil, nil, err
		}
		m.Responses = with(m.Responses, in.StatusCode, r)
		return &m, new(r.output()), nil
	})
}

// methodResponsePatch is UpdateMethodResponse's path table
// (apigateway-rest.md 3.8): add, replace and remove on
// /responseParameters/{key} and /responseModels/{escaped content type}.
// An unknown path answers "Invalid patch path <path>" (recorded, contract
// 6b section 5); the op text is the recorded one for paths that take
// those three ops (inferred here).
func methodResponsePatch(a *RestAPI) patch.Table[MethodResponse] {
	addRemoveReplace := []apigatewayapi.Op{apigatewayapi.OpAdd, apigatewayapi.OpRemove, apigatewayapi.OpReplace}
	rules := []patch.Rule[MethodResponse]{
		{Path: "/responseParameters/*", Ops: addRemoveReplace, Apply: func(r *MethodResponse, op patch.Operation, wild []string) *awserr.Error {
			if op.Op == apigatewayapi.OpRemove {
				r.ResponseParameters = deleted(r.ResponseParameters, wild[0])
				return nil
			}
			v, err := patchBool(op)
			if err != nil {
				return err
			}
			r.ResponseParameters = with(r.ResponseParameters, wild[0], v)
			return nil
		}},
		{Path: "/responseModels/*", Ops: addRemoveReplace, Apply: func(r *MethodResponse, op patch.Operation, wild []string) *awserr.Error {
			if op.Op == apigatewayapi.OpRemove {
				r.ResponseModels = deleted(r.ResponseModels, wild[0])
				return nil
			}
			name := deref(op.Value, "")
			if err := a.checkModels(map[string]string{wild[0]: name}); err != nil {
				return err
			}
			r.ResponseModels = with(r.ResponseModels, wild[0], name)
			return nil
		}},
	}
	return patch.Table[MethodResponse]{
		Rules:       rules,
		UnknownPath: patch.InvalidPath,
		OpText:      patch.OpsAddRemoveReplace,
		Clone:       func(r MethodResponse) MethodResponse { return r.clone() },
	}
}

// deleteMethodResponse removes one method response; an unknown status
// answers the recorded NotFound text (apigateway-rest.md 2.4).
func (s *Service) deleteMethodResponse(_ context.Context, c *kernel.Call, in *apigatewayapi.DeleteMethodResponseInput) (*apigatewayapi.DeleteMethodResponseOutput, *awserr.Error) {
	return writeMethod(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(_ *RestAPI, m Method) (*Method, *apigatewayapi.DeleteMethodResponseOutput, *awserr.Error) {
		if _, ok := m.Responses[in.StatusCode]; !ok {
			return nil, nil, errResponseNotFound()
		}
		m.Responses = deleted(m.Responses, in.StatusCode)
		return &m, &apigatewayapi.DeleteMethodResponseOutput{}, nil
	})
}
