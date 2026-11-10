package apigatewayv2_test

import (
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// matrixModes are the iam.enforce settings on the step-8 seam, with the
// policy evaluator not merged: off installs no Authorizer, so nothing is
// asked; advise and enforce as Root let every call through, here a gate
// that records what it is asked.
func matrixModes() map[string]*gate {
	return map[string]*gate{"off": nil, "advise": newGate(), "enforce": newGate()}
}

func gateOption(g *gate) []kerneltest.Option {
	if g == nil {
		return nil
	}
	return []kerneltest.Option{kerneltest.Authorizer(g)}
}

// The data plane answers the same under every iam.enforce mode, and
// nothing asks step 8 about InvokeApi: a NONE route reaches the
// function as apigateway.amazonaws.com (apigateway-http.md 8.2, 5.5); an
// AWS_IAM route, a Lambda integration with credentialsArn and a CORS
// request answer 501 without running it; JWT and CUSTOM routes are
// refused at create with 501.
func TestEnforceMatrixDataPlaneRows(t *testing.T) {
	for mode, g := range matrixModes() {
		t.Run(mode, func(t *testing.T) {
			run(t, func(h *harness) {
				id, plain := h.lambdaAPI("2.0", "GET /none")
				if rec := h.get(id, "/none"); rec.Code != http.StatusOK {
					t.Fatalf("GET /none = %d %s, want the function's 200", rec.Code, rec.Body)
				}
				if as, ok := h.only().mode.(kernel.AsService); !ok || as.Name != "apigateway.amazonaws.com" {
					t.Errorf("the NONE route invoked the function as %#v, want AsService apigateway.amazonaws.com", h.only().mode)
				}

				iam := apigatewayv2api.AuthorizationTypeAwsIam
				must(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /iam", AuthorizationType: &iam, Target: new("integrations/" + plain)})
				creds := must(h, apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{
					ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationUri: new("arn:aws:lambda:" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":function:fn"),
					PayloadFormatVersion: new("2.0"), CredentialsArn: new("arn:aws:iam::000000000000:role/apigw"),
				})
				h.createRoute(id, "GET /creds", deref(creds.IntegrationId))
				want501(t, h.get(id, "/iam"), "an AWS_IAM route under "+mode)
				want501(t, h.get(id, "/creds"), "a credentialsArn integration under "+mode)
				for _, typ := range []apigatewayv2api.AuthorizationType{apigatewayv2api.AuthorizationTypeJwt, apigatewayv2api.AuthorizationTypeCustom} {
					wantNotImplemented(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /" + string(typ), AuthorizationType: &typ}), "authorizationType")
				}
				must(h, apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{ApiId: id, CorsConfiguration: &apigatewayv2api.Cors{AllowOrigins: []string{"*"}}})
				r := h.request(http.MethodGet, pathURL(id, "/none"), "")
				r.Header.Set("Origin", "https://app.example")
				want501(t, h.send(r), "a CORS request under "+mode)
				if n := len(h.fn.invocations()); n != 1 {
					t.Errorf("the function ran %d times, want once (the NONE route only)", n)
				}
				if g != nil && g.askedAbout(invokeAPI) {
					t.Errorf("step 8 was asked about InvokeApi under %s: %v", mode, g.pairs("InvokeApi"))
				}
			}, gateOption(g)...)
		})
	}
}

// Under enforce a denied control-plane call answers the denial identity
// and changes nothing, while the data plane, never put to IAM, keeps
// serving: a gate that denies every apigatewayv2 operation, InvokeApi
// included, still lets a NONE route reach the function (apigateway-http.md
// 8.2, 8.3).
func TestEnforceDeniesTheControlPlaneOnly(t *testing.T) {
	g := newGate()
	denyOnly := func(ops ...string) {
		g.mu.Lock()
		defer g.mu.Unlock()
		clear(g.deny)
		for _, op := range ops {
			g.deny[op] = true
		}
	}
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		denyOnly("GetApi", "UpdateApi", "DeleteApi", "CreateRoute", "InvokeApi")
		denial := apigatewayv2api.Service().Denial.Identity
		for name, err := range map[string]*awserr.Error{
			"GetApi":      errOf(h.Invoke(h.Context(), apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id})),
			"UpdateApi":   errOf(h.Invoke(h.Context(), apigatewayv2api.OpUpdateApi, &apigatewayv2api.UpdateApiInput{ApiId: id, Name: new("renamed")})),
			"DeleteApi":   errOf(h.Invoke(h.Context(), apigatewayv2api.OpDeleteApi, &apigatewayv2api.DeleteApiInput{ApiId: id})),
			"CreateRoute": errOf(h.Invoke(h.Context(), apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /new"})),
		} {
			if err == nil {
				t.Errorf("denied %s succeeded", name)
				continue
			}
			wantError(t, err, denial)
		}
		if rec := h.get(id, "/hello"); rec.Code != http.StatusOK {
			t.Errorf("GET /hello with the control plane and InvokeApi denied = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		denyOnly()
		if got := must(h, apigatewayv2api.OpGetApi, &apigatewayv2api.GetApiInput{ApiId: id}); got.Name == nil || *got.Name != "t" {
			t.Errorf("after a denied UpdateApi and DeleteApi the API is %+v, want it named t", got)
		}
		if routes := must(h, apigatewayv2api.OpGetRoutes, &apigatewayv2api.GetRoutesInput{ApiId: id}).Items; len(routes) != 1 {
			t.Errorf("after a denied CreateRoute the API has %d routes, want 1", len(routes))
		}
	}, kerneltest.Authorizer(g))
}
