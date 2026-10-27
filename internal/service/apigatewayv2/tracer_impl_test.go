package apigatewayv2_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The tracer bullet's API survives Export, Reset and Import (ADR 0009): a
// Reset forgets the API id, so the data plane answers UnknownAPI, and the
// Import brings the API, its autoDeploy stage's deployment and the id back.
func TestTracerAPISurvivesExportAndImport(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		if len(sections) != 1 {
			t.Fatalf("Export wrote %d sections, want one for the scope holding the API", len(sections))
		}
		h.svc.Reset(kernel.ResetScope{})
		wantAnswer(t, h.get(id, "/hello"), apigatewayv2.FrontAnswersSwitch().Value.UnknownAPI, "after Reset")
		if err := h.svc.Import(sections); err != nil {
			t.Fatalf("Import = %v", err)
		}
		if rec := h.get(id, "/hello"); rec.Code != http.StatusOK {
			t.Errorf("GET /hello after Import = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		if n := len(h.fn.invocations()); n != 1 {
			t.Errorf("%d invocations, want only the one after Import", n)
		}
	})
}

// A section of another schema version is refused with
// *store.VersionMismatchError, and the API still serves.
func TestTracerImportRefusesAnotherVersion(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		for i := range sections {
			sections[i].Version = h.svc.SchemaVersion() + 1
		}
		if _, ok := errors.AsType[*store.VersionMismatchError](h.svc.Import(sections)); !ok {
			t.Errorf("Import of version %d sections did not answer *store.VersionMismatchError", h.svc.SchemaVersion()+1)
		}
		if rec := h.get(id, "/hello"); rec.Code != http.StatusOK {
			t.Errorf("GET /hello after a refused Import = %d %s, want the function's 200", rec.Code, rec.Body)
		}
	})
}

// A Reset of another account leaves this account's API served.
func TestTracerResetOfAnotherAccountKeepsTheAPI(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		other, err := aws.ParseAccountID("111111111111")
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{Accounts: []aws.AccountID{other}})
		if rec := h.get(id, "/hello"); rec.Code != http.StatusOK {
			t.Errorf("GET /hello after another account's Reset = %d %s, want the function's 200", rec.Code, rec.Body)
		}
	})
}

// A named stage is addressed by its first path segment (apigateway-http.md
// 3.4, 4.2): with autoDeploy it serves the API's routes, the function sees
// the stage segment in rawPath but not in routeKey (finding 8), and /prod
// and /prod/ both route as /. The documented source ARN pattern
// <api-id>/prod/*/<path> lets the invocation through (5.5).
func TestTracerNamedAutoDeployStage(t *testing.T) {
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id := h.apiID("t")
		integration := h.lambdaIntegration(id, fnARN, "2.0")
		h.createRoute(id, "GET /hello", integration)
		h.createRoute(id, "GET /", integration)
		must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "prod", AutoDeploy: new(true)})
		h.allow("fn", "prod-hello", h.executionARN(id)+"/prod/*/hello")
		if rec := h.get(id, "/prod/hello"); rec.Code != http.StatusOK {
			t.Fatalf("GET /prod/hello = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		ev := h.only().event(h)
		if ev["routeKey"] != "GET /hello" || ev["rawPath"] != "/prod/hello" || obj(h, ev, "requestContext")["stage"] != "prod" {
			t.Errorf("routeKey %v, rawPath %v, stage %v; want GET /hello, /prod/hello, prod", ev["routeKey"], ev["rawPath"], obj(h, ev, "requestContext")["stage"])
		}
		h.allow("fn", "prod-root", h.executionARN(id)+"/prod/GET/")
		for _, path := range []string{"/prod", "/prod/"} {
			if rec := h.get(id, path); rec.Code != http.StatusOK {
				t.Errorf("GET %s = %d %s, want GET / on prod", path, rec.Code, rec.Body)
			}
		}
		wantAnswer(t, h.get(id, "/hello"), apigatewayv2.FrontAnswersSwitch().Value.NoStage, "GET /hello with no $default stage")
	})
}

// The $default route on the $default stage presents the documented source
// ARN <api-id>/$default/$default (apigateway-http.md 5.5) and catches any
// path no other route matches (4.3).
func TestTracerDefaultRouteSourceARN(t *testing.T) {
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id := h.apiID("t")
		h.createRoute(id, "$default", h.lambdaIntegration(id, fnARN, "2.0"))
		h.autoStage(id)
		exec := h.executionARN(id)
		h.allow("fn", "default-default", exec+"/$default/$default")
		if rec := h.get(id, "/any/path"); rec.Code != http.StatusOK {
			t.Fatalf("GET /any/path = %d %s, want the $default route's 200", rec.Code, rec.Body)
		}
		call := h.only()
		if as, ok := call.mode.(kernel.AsService); !ok || as.Source.String() != exec+"/$default/$default" {
			t.Errorf("invoked as %#v, want source ARN %s/$default/$default", call.mode, exec)
		}
		if ev := call.event(h); ev["routeKey"] != "$default" {
			t.Errorf("routeKey = %v, want $default", ev["routeKey"])
		}
	})
}

// With autoDeploy, a route created after the stage serves as soon as
// CreateRoute returns (apigateway-http.md finding 7, 3.5); a stage without
// it has no deployment, so it answers NoDeployment however many routes the
// API gains.
func TestTracerAutoDeployAndUndeployedStages(t *testing.T) {
	run(t, func(h *harness) {
		id, integration := h.lambdaAPI("2.0", "GET /a")
		h.createRoute(id, "GET /later", integration)
		if rec := h.get(id, "/later"); rec.Code != http.StatusOK {
			t.Errorf("GET /later right after CreateRoute = %d %s, want 200", rec.Code, rec.Body)
		}
		stage := must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "manual"})
		if stage.DeploymentId != nil {
			t.Errorf("CreateStage without autoDeploy answered DeploymentId %q, want none", *stage.DeploymentId)
		}
		h.createRoute(id, "GET /b", integration)
		wantAnswer(t, h.get(id, "/manual/b"), apigatewayv2.FrontAnswersSwitch().Value.NoDeployment, "a stage without autoDeploy")
	})
}

// CreateStage with autoDeploy answers the deployment it made and its
// status message (the get-stages CLI example, apigateway-http.md 3.4).
func TestTracerAutoDeployStageOutput(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		out := h.autoStage(id)
		if out.DeploymentId == nil || out.LastDeploymentStatusMessage == nil ||
			*out.LastDeploymentStatusMessage != "Successfully deployed stage with deployment ID '"+*out.DeploymentId+"'" {
			t.Errorf("CreateStage = %+v, want a DeploymentId and its status message", out)
		}
		if deref(out.StageName) != "$default" || !deref(out.AutoDeploy) || out.CreatedDate == nil || out.LastUpdatedDate == nil {
			t.Errorf("CreateStage = %+v, want $default, AutoDeploy and both dates", out)
		}
	})
}

// What the first slice does not store is refused with NotImplemented naming
// it and saying nothing was changed, never stored silently (CLAUDE.md
// invariant 3): WebSocket APIs, authorizers and stage members beyond the
// tracer's.
func TestTracerRefusesWhatItDoesNotStore(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		ws := apigatewayv2api.ProtocolTypeWebsocket
		wantNotImplemented(t, fail(h, apigatewayv2api.OpCreateApi, &apigatewayv2api.CreateApiInput{Name: "w", ProtocolType: ws}), "WEBSOCKET")
		jwt := apigatewayv2api.AuthorizationTypeJwt
		wantNotImplemented(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /a", AuthorizationType: &jwt}), "JWT")
		wantNotImplemented(t, fail(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: id, StageName: "s", StageVariables: map[string]string{"a": "b"}}), "stageVariables")
	})
}

// The constraints the tracer checks (apigateway-http.md 2.4, 3.2, 3.3,
// 13.5) answer the overlay's BadRequestException, ConflictException and
// NotFoundException; the texts are inferred (APIGW2-01), so only the
// identities are asserted.
func TestTracerConstraints(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		fnARN := h.createFunction("fn")
		wantError(t, fail(h, apigatewayv2api.OpCreateApi, &apigatewayv2api.CreateApiInput{Name: "", ProtocolType: apigatewayv2api.ProtocolTypeHttp}), badRequest)
		for _, key := range []string{"get /a", "FETCH /a", "GET a", "GET /a//b", "GET /a{b}"} {
			wantError(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: key}), badRequest)
		}
		h.createRoute(id, "GET /a", "")
		wantError(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /a"}), conflict)
		wantError(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /b", Target: new("integrations/nope123")}), badRequest)
		wantError(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: id, RouteKey: "GET /b", Target: new("nope")}), badRequest)
		wantError(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: "zzzzzzzzzz", RouteKey: "GET /b"}), notFound)
		mock := apigatewayv2api.CreateIntegrationInput{ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeMock, IntegrationUri: new(fnARN), PayloadFormatVersion: new("2.0")}
		wantError(t, fail(h, apigatewayv2api.OpCreateIntegration, &mock), badRequest)
		for _, uri := range []string{"http://example.com", "arn:aws:lambda:us-east-1:000000000000:layer:l", "arn:aws:s3:::bucket"} {
			wantError(t, fail(h, apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{
				ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationUri: new(uri), PayloadFormatVersion: new("2.0"),
			}), badRequest)
		}
		wantError(t, fail(h, apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{
			ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationUri: new(fnARN),
		}), badRequest)
		for _, name := range []string{"", "a.b", "$prod"} {
			wantError(t, fail(h, apigatewayv2api.OpCreateStage, stageInput(id, name)), badRequest)
		}
		must(h, apigatewayv2api.OpCreateStage, stageInput(id, "s"))
		wantError(t, fail(h, apigatewayv2api.OpCreateStage, stageInput(id, "s")), conflict)
		wantError(t, fail(h, apigatewayv2api.OpCreateStage, stageInput("zzzzzzzzzz", "s")), notFound)
	})
}

// A route of another API's integration is not a target of this one.
func TestTracerTargetMustBeTheAPIsOwn(t *testing.T) {
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		first := h.apiID("first")
		integration := h.lambdaIntegration(first, fnARN, "2.0")
		second := h.apiID("second")
		wantError(t, fail(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{ApiId: second, RouteKey: "GET /a", Target: new("integrations/" + integration)}), badRequest)
	})
}
