package apigatewayv2_test

import (
	"maps"
	"net/http"
	"slices"
	"strconv"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
)

// listRoutes pages through GetRoutes of api with maxResults ("" for none),
// failing the test on a page larger than asked.
func (h *harness) listRoutes(api, maxResults string) []apigatewayv2api.Route {
	h.Helper()
	var all []apigatewayv2api.Route
	in := &apigatewayv2api.GetRoutesInput{ApiId: api}
	if maxResults != "" {
		in.MaxResults = new(maxResults)
	}
	for {
		out := must(h, apigatewayv2api.OpGetRoutes, in)
		if n, err := strconv.Atoi(maxResults); err == nil && len(out.Items) > n {
			h.Fatalf("GetRoutes page of %d items, asked for at most %d", len(out.Items), n)
		}
		all = append(all, out.Items...)
		if out.NextToken == nil {
			return all
		}
		in.NextToken = out.NextToken
	}
}

func routeIDs(routes []apigatewayv2api.Route) []string {
	out := make([]string, 0, len(routes))
	for i := range routes {
		out = append(out, deref(routes[i].RouteId))
	}
	return out
}

// Paging through GetRoutes with any page size lists every route once, in
// creation order (inferred, APIGW2-03), with no page larger than asked
// (apigateway-http.md 2.2); a token no page answered is
// BadRequestException.
func TestGetRoutesPagingProperty(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		rapid.Check(t, func(rt *rapid.T) {
			made := rapid.IntRange(0, 12).Draw(rt, "routes")
			size := rapid.IntRange(1, 6).Draw(rt, "page size")
			ids := make([]string, 0, made)
			for i := range made {
				ids = append(ids, h.createRoute(id, "GET /p"+strconv.Itoa(i), ""))
			}
			if all := routeIDs(h.listRoutes(id, "")); !slices.Equal(all, ids) {
				rt.Fatalf("GetRoutes = %v, want the routes in creation order, %v", all, ids)
			}
			if paged := routeIDs(h.listRoutes(id, strconv.Itoa(size))); !slices.Equal(paged, ids) {
				rt.Fatalf("GetRoutes in pages of %d = %v, want %v", size, paged, ids)
			}
			for _, rid := range ids {
				must(h, apigatewayv2api.OpDeleteRoute, &apigatewayv2api.DeleteRouteInput{ApiId: id, RouteId: rid})
			}
		})
		wantError(t, fail(h, apigatewayv2api.OpGetRoutes, &apigatewayv2api.GetRoutesInput{ApiId: id, NextToken: new("not a token!")}), badRequest)
	})
}

// A GetRoutes token still continues the listing after the route it would
// have started with is deleted, and an updated route keeps its place.
func TestGetRoutesTokenSurvivesADelete(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		ids := []string{h.createRoute(id, "GET /a", ""), h.createRoute(id, "GET /b", ""), h.createRoute(id, "GET /c", "")}
		must(h, apigatewayv2api.OpUpdateRoute, &apigatewayv2api.UpdateRouteInput{ApiId: id, RouteId: ids[0], RouteKey: new("GET /z")})
		first := must(h, apigatewayv2api.OpGetRoutes, &apigatewayv2api.GetRoutesInput{ApiId: id, MaxResults: new("1")})
		if got := routeIDs(first.Items); !slices.Equal(got, ids[:1]) || first.NextToken == nil {
			t.Fatalf("first page = %v with token %v, want [%s] and a token", got, first.NextToken, ids[0])
		}
		must(h, apigatewayv2api.OpDeleteRoute, &apigatewayv2api.DeleteRouteInput{ApiId: id, RouteId: ids[1]})
		rest := must(h, apigatewayv2api.OpGetRoutes, &apigatewayv2api.GetRoutesInput{ApiId: id, NextToken: first.NextToken})
		if got := routeIDs(rest.Items); !slices.Equal(got, ids[2:]) || rest.NextToken != nil {
			t.Errorf("after deleting %s the next page = %v with token %v, want [%s] and no token", ids[1], got, rest.NextToken, ids[2])
		}
	})
}

// UpdateRoute checks what it carries as CreateRoute does (apigateway-http.md
// 3.2, 13.5) and changes nothing when it refuses: another route's key is a
// conflict, the route's own key is not, a target must be an integration of
// the API, and authorizer members answer NotImplemented (the unit's
// conflict row 4). An unknown route is NotFoundException.
func TestUpdateRouteChecks(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		h.createRoute(id, "GET /a", "")
		b := h.createRoute(id, "GET /b", "")
		update := func(in *apigatewayv2api.UpdateRouteInput) *apigatewayv2api.UpdateRouteInput {
			in.ApiId, in.RouteId = id, b
			return in
		}
		wantError(t, fail(h, apigatewayv2api.OpUpdateRoute, update(&apigatewayv2api.UpdateRouteInput{RouteKey: new("GET /a")})), conflict)
		wantError(t, fail(h, apigatewayv2api.OpUpdateRoute, update(&apigatewayv2api.UpdateRouteInput{Target: new("integrations/zzzzzzz")})), badRequest)
		wantError(t, fail(h, apigatewayv2api.OpUpdateRoute, update(&apigatewayv2api.UpdateRouteInput{Target: new("zzzzzzz")})), badRequest)
		wantNotImplemented(t, fail(h, apigatewayv2api.OpUpdateRoute, update(&apigatewayv2api.UpdateRouteInput{AuthorizerId: new("abc123")})), "authorizerId")
		wantNotImplemented(t, fail(h, apigatewayv2api.OpUpdateRoute, update(&apigatewayv2api.UpdateRouteInput{AuthorizationScopes: []string{"read"}})), "authorizationScopes")
		wantError(t, fail(h, apigatewayv2api.OpUpdateRoute, &apigatewayv2api.UpdateRouteInput{ApiId: id, RouteId: "zzzzzzz", OperationName: new("x")}), notFound)
		got := must(h, apigatewayv2api.OpGetRoute, &apigatewayv2api.GetRouteInput{ApiId: id, RouteId: b})
		if deref(got.RouteKey) != "GET /b" || got.Target != nil || got.OperationName != nil {
			t.Errorf("GetRoute after refused updates = %+v, want GET /b unchanged", got)
		}
		same := must(h, apigatewayv2api.OpUpdateRoute, update(&apigatewayv2api.UpdateRouteInput{RouteKey: new("GET /b"), OperationName: new("B")}))
		if deref(same.RouteKey) != "GET /b" || deref(same.OperationName) != "B" {
			t.Errorf("UpdateRoute with the route's own key = %+v, want it accepted", same)
		}
	})
}

// The WebSocket-only members of a route are stored as sent and echoed by
// GetRoute and GetRoutes without effect (apigateway-http.md 3.2), and an
// UpdateRoute replaces only the members it carries.
func TestRouteWebSocketMembersRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		params := map[string]apigatewayv2api.ParameterConstraints{"route.request.header.x-a": {Required: new(true)}, "route.request.querystring.q": {}}
		out := must(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{
			ApiId: id, RouteKey: "POST /m", RequestModels: map[string]string{"application/json": "M"}, RequestParameters: params,
			ModelSelectionExpression: new("$request.body.type"), RouteResponseSelectionExpression: new("$default"),
		})
		check := func(what string, models map[string]string, got map[string]apigatewayv2api.ParameterConstraints, model, response *string) {
			t.Helper()
			if !maps.Equal(models, map[string]string{"application/json": "M"}) || deref(model) != "$request.body.type" || deref(response) != "$default" {
				t.Errorf("%s: RequestModels %v, ModelSelectionExpression %v, RouteResponseSelectionExpression %v; want them as sent", what, models, model, response)
			}
			if len(got) != 2 || !deref(got["route.request.header.x-a"].Required) || got["route.request.querystring.q"].Required != nil {
				t.Errorf("%s: RequestParameters %+v, want them as sent", what, got)
			}
		}
		check("CreateRoute", out.RequestModels, out.RequestParameters, out.ModelSelectionExpression, out.RouteResponseSelectionExpression)
		got := must(h, apigatewayv2api.OpGetRoute, &apigatewayv2api.GetRouteInput{ApiId: id, RouteId: *out.RouteId})
		check("GetRoute", got.RequestModels, got.RequestParameters, got.ModelSelectionExpression, got.RouteResponseSelectionExpression)
		item := h.listRoutes(id, "")[0]
		check("GetRoutes", item.RequestModels, item.RequestParameters, item.ModelSelectionExpression, item.RouteResponseSelectionExpression)
		updated := must(h, apigatewayv2api.OpUpdateRoute, &apigatewayv2api.UpdateRouteInput{ApiId: id, RouteId: *out.RouteId, OperationName: new("Op")})
		check("UpdateRoute", updated.RequestModels, updated.RequestParameters, updated.ModelSelectionExpression, updated.RouteResponseSelectionExpression)
		if deref(updated.OperationName) != "Op" || deref(updated.ApiKeyRequired) || deref(updated.AuthorizationType) != apigatewayv2api.AuthorizationTypeNone {
			t.Errorf("UpdateRoute = %+v, want OperationName Op and the defaults kept", updated)
		}
	})
}

// "Routes per API: 300" counts the routes an API holds: after a delete
// there is room for one more (apigateway-http.md 3.9).
func TestRouteQuotaCountsHeldRoutes(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		var last string
		for i := range 300 {
			last = h.createRoute(id, "GET /r"+strconv.Itoa(i), "")
		}
		must(h, apigatewayv2api.OpDeleteRoute, &apigatewayv2api.DeleteRouteInput{ApiId: id, RouteId: last})
		h.createRoute(id, "GET /again", "")
		wantError(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /over"}), badRequest)
	})
}

// UpdateRoute deploys to the autoDeploy stages before it returns
// (apigateway-http.md 3.5): a new route key serves at once and the old one
// stops; a route switched to AWS_IAM answers 501 without running the
// function, since understudy does not evaluate IAM on requests (8.3), and
// serves again once switched back to NONE.
func TestUpdateRouteDeploysAtOnce(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /old")
		rid := routeIDs(h.listRoutes(id, ""))[0]
		must(h, apigatewayv2api.OpUpdateRoute, &apigatewayv2api.UpdateRouteInput{ApiId: id, RouteId: rid, RouteKey: new("GET /new")})
		if rec := h.get(id, "/new"); rec.Code != http.StatusOK {
			t.Errorf("GET /new after UpdateRoute = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		wantAnswer(t, h.get(id, "/old"), apigatewayv2.FrontAnswersSwitch().Value.NoRoute, "the route's old key")
		iam := apigatewayv2api.AuthorizationTypeAwsIam
		must(h, apigatewayv2api.OpUpdateRoute, &apigatewayv2api.UpdateRouteInput{ApiId: id, RouteId: rid, AuthorizationType: &iam})
		before := len(h.fn.invocations())
		want501(t, h.get(id, "/new"), "an AWS_IAM route")
		if n := len(h.fn.invocations()); n != before {
			t.Errorf("an AWS_IAM route ran the function %d times, want never", n-before)
		}
		none := apigatewayv2api.AuthorizationTypeNone
		must(h, apigatewayv2api.OpUpdateRoute, &apigatewayv2api.UpdateRouteInput{ApiId: id, RouteId: rid, AuthorizationType: &none})
		if rec := h.get(id, "/new"); rec.Code != http.StatusOK {
			t.Errorf("GET /new back on NONE = %d %s, want the function's 200", rec.Code, rec.Body)
		}
	})
}

// Step 8 asks about each route and integration operation as
// apigateway:<its HTTP method> on the resource the 8.1 table gives it: the
// API's routes or integrations collection for creates and lists, the item
// /apis/<api-id>/routes/<route-id> or /integrations/<integration-id>
// otherwise (apigateway-http.md 8.1).
func TestStep8AsksAboutTheRouteAndIntegrationPairs(t *testing.T) {
	g := newGate()
	run(t, func(h *harness) {
		id := h.apiID("t")
		api := apiARN(id)
		integration := h.lambdaIntegration(id, h.createFunction("fn"), "2.0")
		rid := h.createRoute(id, "GET /a", integration)
		must(h, apigatewayv2api.OpGetRoutes, &apigatewayv2api.GetRoutesInput{ApiId: id})
		must(h, apigatewayv2api.OpGetRoute, &apigatewayv2api.GetRouteInput{ApiId: id, RouteId: rid})
		must(h, apigatewayv2api.OpUpdateRoute, &apigatewayv2api.UpdateRouteInput{ApiId: id, RouteId: rid, OperationName: new("A")})
		must(h, apigatewayv2api.OpGetIntegrations, &apigatewayv2api.GetIntegrationsInput{ApiId: id})
		must(h, apigatewayv2api.OpGetIntegration, &apigatewayv2api.GetIntegrationInput{ApiId: id, IntegrationId: integration})
		must(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{ApiId: id, IntegrationId: integration, Description: new("d")})
		must(h, apigatewayv2api.OpDeleteRoute, &apigatewayv2api.DeleteRouteInput{ApiId: id, RouteId: rid})
		must(h, apigatewayv2api.OpDeleteIntegration, &apigatewayv2api.DeleteIntegrationInput{ApiId: id, IntegrationId: integration})
		routes, integrations := api+"/routes", api+"/integrations"
		for op, w := range map[string][2]string{
			"CreateRoute": {"apigateway:POST", routes}, "GetRoutes": {"apigateway:GET", routes},
			"GetRoute": {"apigateway:GET", routes + "/" + rid}, "UpdateRoute": {"apigateway:PATCH", routes + "/" + rid},
			"DeleteRoute":       {"apigateway:DELETE", routes + "/" + rid},
			"CreateIntegration": {"apigateway:POST", integrations}, "GetIntegrations": {"apigateway:GET", integrations},
			"GetIntegration":    {"apigateway:GET", integrations + "/" + integration},
			"UpdateIntegration": {"apigateway:PATCH", integrations + "/" + integration},
			"DeleteIntegration": {"apigateway:DELETE", integrations + "/" + integration},
		} {
			if got := g.pairs(op); len(got) != 1 || got[0] != w {
				t.Errorf("%s asked about %v, want exactly %v", op, got, w)
			}
		}
	}, kerneltest.Authorizer(g))
}
