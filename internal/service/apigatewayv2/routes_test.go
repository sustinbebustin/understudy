package apigatewayv2_test

import (
	"strconv"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
)

// CreateRoute echoes ApiKeyRequired false and AuthorizationType NONE when
// they are not sent (the create-route CLI example, apigateway-http.md 3.2;
// Terraform's empty plan needs both and Target, 9.2), the route key as
// sent and the target integrations/<id> (the get-routes example); GetRoute
// and GetRoutes answer the same route.
func TestCreateRouteEchoesDefaults(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		integration := h.lambdaIntegration(id, h.createFunction("fn"), "2.0")
		out := must(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /pets/{id}", Target: new("integrations/" + integration)})
		if out.ApiKeyRequired == nil || *out.ApiKeyRequired {
			t.Errorf("ApiKeyRequired = %v, want false", out.ApiKeyRequired)
		}
		if out.AuthorizationType == nil || *out.AuthorizationType != apigatewayv2api.AuthorizationTypeNone {
			t.Errorf("AuthorizationType = %v, want NONE", out.AuthorizationType)
		}
		if deref(out.RouteKey) != "GET /pets/{id}" || deref(out.Target) != "integrations/"+integration || deref(out.RouteId) == "" {
			t.Errorf("CreateRoute = key %q, target %q, id %q; want the key and target as sent and an id", deref(out.RouteKey), deref(out.Target), deref(out.RouteId))
		}
		got := must(h, apigatewayv2api.OpGetRoute, &apigatewayv2api.GetRouteInput{ApiId: id, RouteId: *out.RouteId})
		if deref(got.RouteKey) != "GET /pets/{id}" || deref(got.Target) != deref(out.Target) || deref(got.AuthorizationType) != apigatewayv2api.AuthorizationTypeNone {
			t.Errorf("GetRoute = %+v, want what CreateRoute answered", got)
		}
		routes := must(h, apigatewayv2api.OpGetRoutes, &apigatewayv2api.GetRoutesInput{ApiId: id}).Items
		if len(routes) != 1 || deref(routes[0].RouteId) != *out.RouteId || routes[0].RouteKey != "GET /pets/{id}" {
			t.Errorf("GetRoutes = %+v, want the one route", routes)
		}
		untargeted := must(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "$default"})
		if untargeted.Target != nil {
			t.Errorf("a route created without a target answers Target %q", *untargeted.Target)
		}
	})
}

// Route ids are distinct within an API.
func TestRouteIDsAreDistinct(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		seen := map[string]bool{}
		for i := range 20 {
			rid := h.createRoute(id, "GET /r"+strconv.Itoa(i), "")
			if seen[rid] {
				t.Fatalf("RouteId %q handed out twice", rid)
			}
			seen[rid] = true
		}
	})
}

// A route key already in the API is a conflict, and a key the docs exclude
// (a greedy variable before the end, the routes page, apigateway-http.md
// 2.4) a bad request; neither creates a route.
func TestCreateRouteRefusals(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		h.createRoute(id, "GET /pets", "")
		wantError(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /pets"}), conflict)
		wantError(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /{proxy+}/x"}), badRequest)
		wantError(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /x", Target: new("integrations/zzzzzzz")}), badRequest)
		wantError(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: "zzzzzzzzzz", RouteKey: "GET /x"}), notFound)
		if routes := must(h, apigatewayv2api.OpGetRoutes, &apigatewayv2api.GetRoutesInput{ApiId: id}).Items; len(routes) != 1 {
			t.Errorf("GetRoutes = %+v, want only the first route", routes)
		}
	})
}

// AWS_IAM routes are stored (a request to one answers 501, invoke tests);
// JWT and CUSTOM routes, an authorizer id and authorization scopes name
// authorizers, which are not emulated, so they answer NotImplemented
// (apigateway-http.md 3.2, 3.8).
func TestRouteAuthorization(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		iam := apigatewayv2api.AuthorizationTypeAwsIam
		out := must(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /iam", AuthorizationType: &iam})
		if deref(out.AuthorizationType) != iam {
			t.Errorf("AuthorizationType = %v, want AWS_IAM stored", out.AuthorizationType)
		}
		for _, typ := range []apigatewayv2api.AuthorizationType{apigatewayv2api.AuthorizationTypeJwt, apigatewayv2api.AuthorizationTypeCustom} {
			err := fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /" + string(typ), AuthorizationType: &typ})
			wantNotImplemented(t, err, "authorizationType")
		}
		wantNotImplemented(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /a", AuthorizerId: new("abc123")}), "AuthorizerId")
		wantNotImplemented(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /s", AuthorizationScopes: []string{"read"}}), "AuthorizationScopes")
		jwt := apigatewayv2api.AuthorizationTypeJwt
		wantNotImplemented(t, fail(h, apigatewayv2api.OpUpdateRoute, &apigatewayv2api.UpdateRouteInput{ApiId: id, RouteId: *out.RouteId, AuthorizationType: &jwt}), "authorizationType")
	})
}

// Members that only act on WebSocket APIs are stored and echoed without
// effect (apigateway-http.md 3.2: ApiKeyRequired and the model and
// parameter members are WebSocket-only), as Terraform reads them back.
func TestWebSocketOnlyRouteMembersAreEchoed(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		out := must(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "POST /x", ApiKeyRequired: new(true), OperationName: new("CreateX")})
		if !deref(out.ApiKeyRequired) || deref(out.OperationName) != "CreateX" {
			t.Errorf("ApiKeyRequired %v, OperationName %q; want true and CreateX echoed", out.ApiKeyRequired, deref(out.OperationName))
		}
	})
}

// "Routes per API: 300" (the HTTP API quotas page, apigateway-http.md 3.9).
func TestRouteQuota(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		for i := range 300 {
			h.createRoute(id, "GET /r"+strconv.Itoa(i), "")
		}
		wantError(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /one-more"}), badRequest)
	})
}

// UpdateRoute replaces the members it carries; DeleteRoute removes the
// route, after which GetRoute is NotFoundException.
func TestUpdateAndDeleteRoute(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		integration := h.lambdaIntegration(id, h.createFunction("fn"), "2.0")
		rid := h.createRoute(id, "GET /old", "")
		must(h, apigatewayv2api.OpUpdateRoute, &apigatewayv2api.UpdateRouteInput{ApiId: id, RouteId: rid, RouteKey: new("POST /new"), Target: new("integrations/" + integration)})
		got := must(h, apigatewayv2api.OpGetRoute, &apigatewayv2api.GetRouteInput{ApiId: id, RouteId: rid})
		if deref(got.RouteKey) != "POST /new" || deref(got.Target) != "integrations/"+integration || deref(got.AuthorizationType) != apigatewayv2api.AuthorizationTypeNone {
			t.Errorf("GetRoute after UpdateRoute = %+v, want POST /new targeting %s, still NONE", got, integration)
		}
		wantError(t, fail(h, apigatewayv2api.OpUpdateRoute, &apigatewayv2api.UpdateRouteInput{ApiId: id, RouteId: rid, RouteKey: new("GET /{a+}/b")}), badRequest)
		must(h, apigatewayv2api.OpDeleteRoute, &apigatewayv2api.DeleteRouteInput{ApiId: id, RouteId: rid})
		wantError(t, fail(h, apigatewayv2api.OpGetRoute, &apigatewayv2api.GetRouteInput{ApiId: id, RouteId: rid}), notFound)
		wantError(t, fail(h, apigatewayv2api.OpDeleteRoute, &apigatewayv2api.DeleteRouteInput{ApiId: id, RouteId: rid}), notFound)
	})
}
