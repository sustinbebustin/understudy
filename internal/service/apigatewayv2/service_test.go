package apigatewayv2_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
)

// baseNote heads every bound control-plane operation's coverage note: no
// v2 error text is recorded and the id shapes are inferred
// (apigateway-http.md 2.3, 2.5).
const baseNote = "error message texts and generated ids are inferred (UNVERIFIED, APIGW2-01, APIGW2-03)"

func partial(note string) registry.BindCoverage {
	if note == "" {
		return registry.Partial{Note: baseNote}
	}
	return registry.Partial{Note: baseNote + "; " + note}
}

// The HTTP API operations are bound with the unit's coverage marks and
// notes, verbatim; every other v2 operation (authorizers, import and
// export, models, route and integration responses, domain names, API
// mappings, VPC links, portals, routing rules) stays unbound and answers
// 501 (CLAUDE.md invariant 3). No operation is Full: each emits an
// inferred text, id or behavior.
func TestCoverageMarks(t *testing.T) {
	const (
		apiNote         = "HTTP APIs only: protocolType WEBSOCKET and quick create (Target, RouteKey, CredentialsArn) answer NotImplemented; corsConfiguration is stored and returned, and a data-plane request with an Origin header to an API that has one answers 501 (CORS is not applied); ipAddressType is stored without effect; ApiEndpoint names the emulator (http://<api-id>.execute-api.<region>.localhost:<port>), not amazonaws.com"
		routeNote       = "authorizationType JWT and CUSTOM answer NotImplemented (authorizers are not emulated); a route with AWS_IAM is stored and a request it matches answers 501; WebSocket-only members are stored and echoed without effect"
		integrationNote = "Lambda AWS_PROXY with payload 1.0 and 2.0 runs; integration subtypes, HTTP_PROXY and VPC_LINK connections are stored and a request that reaches them answers 501; a Lambda integration with credentialsArn, requestParameters, responseParameters or a stage variable in its URI is stored and a request that reaches it answers 501; tlsConfig is stored without effect"
		stageNote       = "accessLogSettings, defaultRouteSettings and routeSettings are stored without effect (no access log records, no throttling); an automatic deployment completes before the call returns (UNVERIFIED delay, APIGW2-04); an explicit DeploymentId on an autoDeploy stage is refused (documented as not updatable; identity and text inferred, APIGW2-04)"
		deploymentNote  = "a deployment of an API with no routes succeeds and serves 404 Not Found, and deleting a deployment a stage uses is refused with BadRequestException (the REST rule; both UNVERIFIED for HTTP APIs, APIGW2-04)"
		invokeNote      = "Lambda AWS_PROXY with payload 1.0 and 2.0; stage and route selection as documented; AWS_IAM routes, CORS requests and integrations other than Lambda AWS_PROXY answer 501; parameter mapping, access logging and throttling are not applied; inferred, UNVERIFIED: the 2.0 isBase64Encoded of a bodyless request and default Content-Type (APIGW2-07, APIGW2-09), the 1.0 single-value maps (APIGW2-08)"
		storedNote      = "the settings they remove are stored without effect; " + baseNote
	)
	want := map[string]registry.BindCoverage{
		"CreateApi": partial(apiNote), "UpdateApi": partial(apiNote),
		"GetApi": partial(""), "GetApis": partial(""), "DeleteApi": partial(""), "DeleteCorsConfiguration": partial(""),
		"CreateRoute": partial(routeNote), "UpdateRoute": partial(routeNote),
		"GetRoute": partial(""), "GetRoutes": partial(""), "DeleteRoute": partial(""),
		"CreateIntegration": partial(integrationNote), "UpdateIntegration": partial(integrationNote),
		"GetIntegration": partial(""), "GetIntegrations": partial(""), "DeleteIntegration": partial(""),
		"CreateStage": partial(stageNote), "UpdateStage": partial(stageNote),
		"GetStage": partial(""), "GetStages": partial(""), "DeleteStage": partial(""),
		"CreateDeployment": partial(deploymentNote), "DeleteDeployment": partial(deploymentNote),
		"GetDeployment": partial(""), "GetDeployments": partial(""), "UpdateDeployment": partial(""),
		"TagResource": partial(""), "UntagResource": partial(""), "GetTags": partial(""),
		"DeleteAccessLogSettings": registry.StoredOnly{Note: storedNote}, "DeleteRouteSettings": registry.StoredOnly{Note: storedNote},
		"InvokeApi": registry.Partial{Note: invokeNote},
	}
	run(t, func(h *harness) {
		for _, op := range apigatewayv2api.Service().Operations {
			status := h.Kernel.Bindings().Status(op)
			cov, listed := want[op.ID.Name]
			if !listed {
				if _, unbound := status.(kernel.Unbound); !unbound {
					t.Errorf("%s is %#v, want unbound", op.ID.Name, status)
				}
				continue
			}
			if bound, ok := status.(kernel.Bound); !ok || bound.Coverage != cov {
				t.Errorf("%s is %#v\nwant bound as %#v", op.ID.Name, status, cov)
			}
		}
		for name, err := range map[string]*awserr.Error{
			"CreateAuthorizer": errOf(h.Invoke(h.Context(), apigatewayv2api.OpCreateAuthorizer, &apigatewayv2api.CreateAuthorizerInput{ApiId: "a1b2c3d4e5", Name: "a"})),
			"ImportApi":        errOf(h.Invoke(h.Context(), apigatewayv2api.OpImportApi, &apigatewayv2api.ImportApiInput{Body: "{}"})),
			"GetVpcLinks":      errOf(h.Invoke(h.Context(), apigatewayv2api.OpGetVpcLinks, &apigatewayv2api.GetVpcLinksInput{})),
		} {
			if err == nil || err.ID.Status != http.StatusNotImplemented {
				t.Errorf("%s = %v, want 501", name, err)
			}
		}
	})
}

func errOf[Out any](_ *Out, err *awserr.Error) *awserr.Error { return err }

// The five switches name every behavior a test pins without AWS-recorded
// evidence, in the unit's order, each inferred, with a Ref into
// apigateway-http.md, the recording cases that settle it and no parity
// scenario yet, and Switches lists each constructor's value.
func TestSwitches(t *testing.T) {
	type sw struct {
		value   string
		records []string
		ops     []string
	}
	invoke := "apigatewayv2.InvokeApi"
	want := []struct {
		name string
		sw
	}{
		{"apigatewayv2.lambda.failure-answers", sw{fmt.Sprintf("%v", apigatewayv2.FailureAnswersSwitch().Value), []string{"APIGW2-10"}, []string{invoke}}},
		{"apigatewayv2.lambda.permission", sw{fmt.Sprintf("%v", apigatewayv2.PermissionSwitch().Value), []string{"APIGW2-11"}, []string{invoke}}},
		{"apigatewayv2.route.matching", sw{fmt.Sprintf("%v", apigatewayv2.RouteMatchingSwitch().Value), []string{"APIGW2-05"}, []string{invoke}}},
		{"apigatewayv2.front.answers", sw{fmt.Sprintf("%v", apigatewayv2.FrontAnswersSwitch().Value), []string{"APIGW2-06", "APIGW2-04"}, []string{
			invoke, "apigatewayv2.CreateDeployment", "apigatewayv2.DeleteDeployment", "apigatewayv2.CreateStage", "apigatewayv2.UpdateStage",
		}}},
		{"apigatewayv2.payload.unrecorded", sw{fmt.Sprintf("%v", apigatewayv2.PayloadSwitch().Value), []string{"APIGW2-07", "APIGW2-08", "APIGW2-09"}, []string{invoke}}},
	}
	run(t, func(h *harness) {
		got := h.svc.Switches()
		if len(got) != len(want) {
			t.Fatalf("Switches() = %d entries %+v, want %d", len(got), got, len(want))
		}
		for i, w := range want {
			e := got[i]
			if e.Name != w.name {
				t.Errorf("switch %d = %s, want %s", i, e.Name, w.name)
				continue
			}
			if e.Value != w.value || e.Label != evidence.Inferred || !slices.Equal(e.Ops, w.ops) {
				t.Errorf("%s = value %q, label %s, ops %v\nwant value %q, label inferred, ops %v", e.Name, e.Value, e.Label, e.Ops, w.value, w.ops)
			}
			if !strings.HasPrefix(e.Ref, "docs/research/apigateway-http.md#") {
				t.Errorf("%s Ref = %q, want an anchor in docs/research/apigateway-http.md", e.Name, e.Ref)
			}
			for _, r := range w.records {
				if !strings.Contains(e.Record, r) {
					t.Errorf("%s Record = %q, want it to name %s", e.Name, e.Record, r)
				}
			}
			if _, ok := e.Settles.(fidelity.NoScenario); !ok {
				t.Errorf("%s Settles = %#v, want NoScenario until %v is recorded", e.Name, e.Settles, w.records)
			}
		}
	})
}

// The documented parts of the switch values are AWS's: the function-error
// and denied-permission bodies are {"message":"Internal Server Error"}
// (the HTTP API Lambda troubleshooting page, apigateway-http.md finding 9,
// 4.5), the no-stage and no-route bodies {"message":"Not Found"} (the
// routes page, 4.2, 4.3), and the request body cap is the documented
// 10 MB payload quota in one of its two readings (4.7: which is
// UNVERIFIED, APIGW2-10). Lambda's synchronous event limit is 6 MB (4.7).
func TestSwitchValuesKeepTheDocumentedParts(t *testing.T) {
	fa := apigatewayv2.FailureAnswersSwitch().Value
	if fa.FunctionError.Message != "Internal Server Error" {
		t.Errorf("FunctionError message = %q, want Internal Server Error", fa.FunctionError.Message)
	}
	if fa.MaxBody != 10_000_000 && fa.MaxBody != 10_485_760 {
		t.Errorf("MaxBody = %d, want 10 MB as 10,000,000 or 10,485,760 bytes", fa.MaxBody)
	}
	if fa.MaxEvent != 6_000_000 && fa.MaxEvent != 6_291_456 {
		t.Errorf("MaxEvent = %d, want 6 MB", fa.MaxEvent)
	}
	if d := apigatewayv2.PermissionSwitch().Value.Denial; d.Message != "Internal Server Error" {
		t.Errorf("permission Denial message = %q, want Internal Server Error", d.Message)
	}
	front := apigatewayv2.FrontAnswersSwitch().Value
	if front.NoStage.Message != "Not Found" || front.NoRoute.Message != "Not Found" {
		t.Errorf("NoStage %q, NoRoute %q; want Not Found for both", front.NoStage.Message, front.NoRoute.Message)
	}
	for name, rule := range map[string]apigatewayv2.DeploymentRule{
		"DeleteInUse": front.Deployments.DeleteInUse, "EmptyAPI": front.Deployments.EmptyAPI, "ExplicitOnAutoDeploy": front.Deployments.ExplicitOnAutoDeploy,
	} {
		if rule != apigatewayv2.Refused && rule != apigatewayv2.Allowed {
			t.Errorf("Deployments.%s = %v, want Refused or Allowed", name, rule)
		}
	}
	p := apigatewayv2.PayloadSwitch().Value
	for name, pick := range map[string]apigatewayv2.Pick{"V1Header": p.V1Header, "V1Query": p.V1Query} {
		if pick != apigatewayv2.First && pick != apigatewayv2.Last {
			t.Errorf("%s = %v, want First or Last", name, pick)
		}
	}
}

// The service is apigatewayv2 in the registry, a stateful service whose
// admin view lists one resource per API under its resource ARN
// (apigateway-http.md 2.5) and the scopes that hold APIs.
func TestResourcesAndScopes(t *testing.T) {
	run(t, func(h *harness) {
		if h.svc.Name() != ref.ServiceID("apigatewayv2") {
			t.Errorf("Name() = %q, want apigatewayv2", h.svc.Name())
		}
		if got := h.svc.Resources(h.Scope); len(got) != 0 {
			t.Errorf("Resources before any API = %+v, want none", got)
		}
		id := h.apiID("listed")
		got := h.svc.Resources(h.Scope)
		if len(got) != 1 || got[0].ARN.String() != apiARN(id) || got[0].Summary == "" {
			t.Errorf("Resources = %+v, want one at %s with a summary", got, apiARN(id))
		}
		if scopes := h.svc.Scopes(); !slices.Contains(scopes, h.Scope) {
			t.Errorf("Scopes() = %v, want it to hold %v", scopes, h.Scope)
		}
	})
}
