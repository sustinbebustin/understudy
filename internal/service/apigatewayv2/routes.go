package apigatewayv2

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2/route"
)

// targetPrefix starts a route's Target (apigateway-http.md 3.2).
const targetPrefix = "integrations/"

// maxRoutes is "Routes per API: 300" (the HTTP API quotas page,
// apigateway-http.md 3.9).
const maxRoutes = 300

// checkCreateRoute refuses, at step 7, a CreateRoute understudy cannot
// store: see checkRouteMembers.
func checkCreateRoute(in *apigatewayv2api.CreateRouteInput) *awserr.Error {
	return checkRouteMembers("CreateRoute", &in.RouteKey, in.Target, in.AuthorizationType, in.AuthorizerId, in.AuthorizationScopes)
}

// checkUpdateRoute is checkCreateRoute for the members an UpdateRoute
// carries.
func checkUpdateRoute(in *apigatewayv2api.UpdateRouteInput) *awserr.Error {
	return checkRouteMembers("UpdateRoute", in.RouteKey, in.Target, in.AuthorizationType, in.AuthorizerId, in.AuthorizationScopes)
}

// checkRouteMembers checks the route members that are present: JWT and
// CUSTOM authorization, an authorizer id and authorization scopes name or
// configure authorizers, which understudy does not emulate, so they answer
// NotImplemented (the unit's conflict row 4); a route key must parse
// (route.ParseKey, apigateway-http.md 2.4, 3.2) and a target must read
// integrations/<integration-id>. The texts are inferred (APIGW2-01).
func checkRouteMembers(op string, key, target *string, auth *apigatewayv2api.AuthorizationType, authorizerID *string, scopes []string) *awserr.Error {
	if auth != nil && *auth != apigatewayv2api.AuthorizationTypeNone && *auth != apigatewayv2api.AuthorizationTypeAwsIam {
		return errNotImplemented(op, "authorizationType "+string(*auth)+" (authorizers are not emulated)",
			"Use authorizationType NONE, or AWS_IAM (a request such a route matches answers 501).")
	}
	if authorizerID != nil && *authorizerID != "" {
		return errNotImplemented(op, "authorizerId (authorizers are not emulated)", "Send the request without authorizerId.")
	}
	if len(scopes) > 0 {
		return errNotImplemented(op, "authorizationScopes (authorizers are not emulated)", "Send the request without authorizationScopes.")
	}
	if key != nil {
		if _, kerr := route.ParseKey(*key); kerr != nil {
			return errBadRequest("%s", kerr.Error())
		}
	}
	if target != nil {
		if _, err := targetIntegration(*target); err != nil {
			return err
		}
	}
	return nil
}

// targetIntegration is the integration id a route's Target names.
func targetIntegration(target string) (string, *awserr.Error) {
	id, ok := strings.CutPrefix(target, targetPrefix)
	if !ok || id == "" {
		return "", errBadRequest("target %q is not integrations/<integration-id>", target)
	}
	return id, nil
}

// createRoute creates a route, NONE unless AWS_IAM is asked for, and
// deploys it to the API's autoDeploy stages.
func (s *Service) createRoute(_ context.Context, c *kernel.Call, in *apigatewayv2api.CreateRouteInput) (*apigatewayv2api.CreateRouteOutput, *awserr.Error) {
	r, err := writeAPI(s, c, in.ApiId, func(a *API, now clock.Instant) (Route, *awserr.Error) {
		if len(a.Routes) >= maxRoutes {
			return Route{}, errBadRequest("the API already has %d routes, the most an HTTP API may have", maxRoutes)
		}
		r := Route{
			ID: s.newChildID(a, s.routeID), Seq: a.nextChild(), Key: in.RouteKey,
			Authorization:                    deref(in.AuthorizationType, apigatewayv2api.AuthorizationTypeNone),
			APIKeyRequired:                   deref(in.ApiKeyRequired, false),
			ModelSelectionExpression:         clonePtr(in.ModelSelectionExpression),
			OperationName:                    clonePtr(in.OperationName),
			RequestModels:                    maps.Clone(in.RequestModels),
			RequestParameters:                routeParametersIn(in.RequestParameters),
			RouteResponseSelectionExpression: clonePtr(in.RouteResponseSelectionExpression),
		}
		if err := a.setTarget(&r, in.Target); err != nil {
			return Route{}, err
		}
		if err := a.checkKeyFree(r); err != nil {
			return Route{}, err
		}
		a.Routes = append(slices.Clone(a.Routes), r)
		a.autoDeploy(now, s.deploymentID)
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	out := apigatewayv2api.CreateRouteOutput(r.output())
	return &out, nil
}

// updateRoute replaces the members an UpdateRoute carries, with
// CreateRoute's checks, and deploys the change.
func (s *Service) updateRoute(_ context.Context, c *kernel.Call, in *apigatewayv2api.UpdateRouteInput) (*apigatewayv2api.UpdateRouteOutput, *awserr.Error) {
	r, err := writeAPI(s, c, in.ApiId, func(a *API, now clock.Instant) (Route, *awserr.Error) {
		i, err := a.routeIndex(in.RouteId)
		if err != nil {
			return Route{}, err
		}
		r := a.Routes[i].clone()
		r.Key = deref(in.RouteKey, r.Key)
		r.Authorization = deref(in.AuthorizationType, r.Authorization)
		r.APIKeyRequired = deref(in.ApiKeyRequired, r.APIKeyRequired)
		if in.ModelSelectionExpression != nil {
			r.ModelSelectionExpression = clonePtr(in.ModelSelectionExpression)
		}
		if in.OperationName != nil {
			r.OperationName = clonePtr(in.OperationName)
		}
		if in.RequestModels != nil {
			r.RequestModels = maps.Clone(in.RequestModels)
		}
		if in.RequestParameters != nil {
			r.RequestParameters = routeParametersIn(in.RequestParameters)
		}
		if in.RouteResponseSelectionExpression != nil {
			r.RouteResponseSelectionExpression = clonePtr(in.RouteResponseSelectionExpression)
		}
		if err := a.setTarget(&r, in.Target); err != nil {
			return Route{}, err
		}
		if err := a.checkKeyFree(r); err != nil {
			return Route{}, err
		}
		a.Routes = slices.Clone(a.Routes)
		a.Routes[i] = r
		a.autoDeploy(now, s.deploymentID)
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	out := apigatewayv2api.UpdateRouteOutput(r.output())
	return &out, nil
}

// deleteRoute removes a route and deploys the change.
func (s *Service) deleteRoute(_ context.Context, c *kernel.Call, in *apigatewayv2api.DeleteRouteInput) (*apigatewayv2api.DeleteRouteOutput, *awserr.Error) {
	if _, err := writeAPI(s, c, in.ApiId, func(a *API, now clock.Instant) (struct{}, *awserr.Error) {
		i, err := a.routeIndex(in.RouteId)
		if err != nil {
			return struct{}{}, err
		}
		a.Routes = slices.Delete(slices.Clone(a.Routes), i, i+1)
		a.autoDeploy(now, s.deploymentID)
		return struct{}{}, nil
	}); err != nil {
		return nil, err
	}
	return &apigatewayv2api.DeleteRouteOutput{}, nil
}

// getRoute answers one route of an API.
func (s *Service) getRoute(_ context.Context, c *kernel.Call, in *apigatewayv2api.GetRouteInput) (*apigatewayv2api.GetRouteOutput, *awserr.Error) {
	r, err := readAPI(s, c.Scope(), in.ApiId, func(a *API) (Route, *awserr.Error) {
		i, err := a.routeIndex(in.RouteId)
		if err != nil {
			return Route{}, err
		}
		return a.Routes[i], nil
	})
	if err != nil {
		return nil, err
	}
	return new(r.output()), nil
}

// getRoutes lists an API's routes in creation order (inferred, APIGW2-03),
// a page at a time.
func (s *Service) getRoutes(_ context.Context, c *kernel.Call, in *apigatewayv2api.GetRoutesInput) (*apigatewayv2api.GetRoutesOutput, *awserr.Error) {
	routes, err := readAPI(s, c.Scope(), in.ApiId, func(a *API) ([]Route, *awserr.Error) { return slices.Clone(a.Routes), nil })
	if err != nil {
		return nil, err
	}
	items, next, err := page(routes, func(r Route) uint64 { return r.Seq }, in.MaxResults, in.NextToken)
	if err != nil {
		return nil, err
	}
	out := &apigatewayv2api.GetRoutesOutput{Items: make([]apigatewayv2api.Route, 0, len(items)), NextToken: next}
	for i := range items {
		out.Items = append(out.Items, items[i].item())
	}
	return out, nil
}

// routeIndex is the index of the route id names; an id the API does not
// hold is NotFoundException.
func (a *API) routeIndex(id string) (int, *awserr.Error) {
	i := slices.IndexFunc(a.Routes, func(r Route) bool { return r.ID == id })
	if i < 0 {
		return 0, errNotFound("API %s has no route %s", a.ID, id)
	}
	return i, nil
}

// setTarget points r at the integration target names, when the request
// sends one; it must be an integration of the API.
func (a *API) setTarget(r *Route, target *string) *awserr.Error {
	if target == nil {
		return nil
	}
	id, err := targetIntegration(*target)
	if err != nil {
		return err
	}
	if _, ok := a.integration(id); !ok {
		return errBadRequest("target %q names no integration of API %s", *target, a.ID)
	}
	r.Integration = id
	return nil
}

// checkKeyFree refuses r when another route of the API has its key: "a
// key already in the API" is a ConflictException (apigateway-http.md
// 13.5), by string equality.
func (a *API) checkKeyFree(r Route) *awserr.Error {
	if slices.ContainsFunc(a.Routes, func(x Route) bool { return x.Key == r.Key && x.ID != r.ID }) {
		return errConflict("API %s already has a route with route key %q", a.ID, r.Key)
	}
	return nil
}

// output is the route as GetRoute answers it: ApiKeyRequired and
// AuthorizationType always, which Terraform's empty plan needs
// (apigateway-http.md 3.2, 9.2), Target when set, the stored WebSocket-only
// members as sent, and no ApiGatewayManaged, which only quick create sets.
func (r *Route) output() apigatewayv2api.GetRouteOutput {
	out := apigatewayv2api.GetRouteOutput{
		ApiKeyRequired:                   new(r.APIKeyRequired),
		AuthorizationType:                new(r.Authorization),
		ModelSelectionExpression:         clonePtr(r.ModelSelectionExpression),
		OperationName:                    clonePtr(r.OperationName),
		RequestModels:                    maps.Clone(r.RequestModels),
		RequestParameters:                routeParametersOut(r.RequestParameters),
		RouteId:                          new(r.ID),
		RouteKey:                         new(r.Key),
		RouteResponseSelectionExpression: clonePtr(r.RouteResponseSelectionExpression),
	}
	if r.Integration != "" {
		out.Target = new(targetPrefix + r.Integration)
	}
	return out
}

// item is the route as GetRoutes lists it, the members of output.
func (r *Route) item() apigatewayv2api.Route {
	o := r.output()
	return apigatewayv2api.Route{
		ApiKeyRequired: o.ApiKeyRequired, AuthorizationType: o.AuthorizationType,
		ModelSelectionExpression: o.ModelSelectionExpression, OperationName: o.OperationName,
		RequestModels: o.RequestModels, RequestParameters: o.RequestParameters, RouteId: o.RouteId,
		RouteKey: r.Key, RouteResponseSelectionExpression: o.RouteResponseSelectionExpression, Target: o.Target,
	}
}

// routeParametersIn is a request's route request parameters as stored.
func routeParametersIn(p map[string]apigatewayv2api.ParameterConstraints) map[string]RouteParameter {
	if p == nil {
		return nil
	}
	out := make(map[string]RouteParameter, len(p))
	for k, v := range p {
		out[k] = RouteParameter{Required: clonePtr(v.Required)}
	}
	return out
}

// routeParametersOut is stored route request parameters as an output
// carries them.
func routeParametersOut(p map[string]RouteParameter) map[string]apigatewayv2api.ParameterConstraints {
	if p == nil {
		return nil
	}
	out := make(map[string]apigatewayv2api.ParameterConstraints, len(p))
	for k, v := range p {
		out[k] = apigatewayv2api.ParameterConstraints{Required: clonePtr(v.Required)}
	}
	return out
}

func (s *Service) routeID() string       { return string(s.ids.APIGatewayRouteID()) }
func (s *Service) integrationID() string { return string(s.ids.APIGatewayIntegrationID()) }
func (s *Service) deploymentID() string  { return string(s.ids.APIGatewayDeploymentID()) }

// newChildID draws an id with draw until no child of a holds it.
func (*Service) newChildID(a *API, draw func() string) string {
	id := draw()
	for a.hasChild(id) {
		id = draw()
	}
	return id
}
