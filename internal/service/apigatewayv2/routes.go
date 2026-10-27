package apigatewayv2

import (
	"context"
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

// createRoute creates a route with a $default or literal route key and an
// optional target, and deploys it to the API's autoDeploy stages.
func (s *Service) createRoute(_ context.Context, c *kernel.Call, in *apigatewayv2api.CreateRouteInput) (*apigatewayv2api.CreateRouteOutput, *awserr.Error) {
	if err := refuseMembers("CreateRoute",
		member{"apiKeyRequired", in.ApiKeyRequired != nil},
		member{"authorizationScopes", in.AuthorizationScopes != nil},
		member{"authorizerId", in.AuthorizerId != nil},
		member{"modelSelectionExpression", in.ModelSelectionExpression != nil},
		member{"operationName", in.OperationName != nil},
		member{"requestModels", in.RequestModels != nil},
		member{"requestParameters", in.RequestParameters != nil},
		member{"routeResponseSelectionExpression", in.RouteResponseSelectionExpression != nil},
	); err != nil {
		return nil, err
	}
	if t := in.AuthorizationType; t != nil && *t != apigatewayv2api.AuthorizationTypeNone {
		return nil, errNotImplemented("CreateRoute", "authorizationType "+string(*t), "Create the route with authorizationType NONE.")
	}
	key, kerr := route.ParseKey(in.RouteKey)
	if kerr != nil {
		return nil, errBadRequest("%s", kerr.Error())
	}
	if k, ok := key.(route.Keyed); ok && k.Variables() {
		return nil, errNotImplemented("CreateRoute", "route keys with path variables ({name} or {name+})", "Use a route key of literal segments, or $default.")
	}
	var integration string
	if in.Target != nil {
		id, ok := strings.CutPrefix(*in.Target, targetPrefix)
		if !ok || id == "" {
			return nil, errBadRequest("target %q is not integrations/<integration-id>", *in.Target)
		}
		integration = id
	}
	r, err := updateAPI(s, c, in.ApiId, func(a *API, now clock.Instant) (Route, *awserr.Error) {
		if slices.ContainsFunc(a.Routes, func(r Route) bool { return r.Key == in.RouteKey }) {
			return Route{}, errConflict("the API already has a route with route key %q", in.RouteKey)
		}
		if _, ok := a.integration(integration); integration != "" && !ok {
			return Route{}, errBadRequest("target %q names no integration of the API", *in.Target)
		}
		r := Route{ID: s.newChildID(a, s.routeID), Key: in.RouteKey, Integration: integration}
		a.Routes = append(slices.Clone(a.Routes), r)
		a.autoDeploy(now, s.deploymentID)
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	out := &apigatewayv2api.CreateRouteOutput{
		RouteId: new(r.ID), RouteKey: new(r.Key),
		ApiKeyRequired: new(false), AuthorizationType: new(apigatewayv2api.AuthorizationTypeNone),
	}
	if r.Integration != "" {
		out.Target = new(targetPrefix + r.Integration)
	}
	return out, nil
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
