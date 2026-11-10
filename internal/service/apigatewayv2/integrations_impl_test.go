package apigatewayv2_test

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// httpProxyIn is a CreateIntegration of an HTTP proxy integration on api.
func httpProxyIn(api string) *apigatewayv2api.CreateIntegrationInput {
	return &apigatewayv2api.CreateIntegrationInput{
		ApiId: api, IntegrationType: apigatewayv2api.IntegrationTypeHttpProxy, IntegrationUri: new("https://example.com/{proxy}"),
		IntegrationMethod: new("ANY"), PayloadFormatVersion: new("1.0"),
	}
}

// subtypeIn is a CreateIntegration of an AWS service integration of
// subtype with request parameters params on api, at payload format 1.0.
func subtypeIn(api, subtype string, params map[string]string) *apigatewayv2api.CreateIntegrationInput {
	return &apigatewayv2api.CreateIntegrationInput{
		ApiId: api, IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationSubtype: new(subtype),
		CredentialsArn: new("arn:aws:iam::000000000000:role/apigw"), RequestParameters: params, PayloadFormatVersion: new("1.0"),
	}
}

// The required request parameters of each of the ten subtypes, the subtype
// reference's table (apigateway-http.md 6.1): with every one configured
// the integration is created; without any one of them it is refused (6.2).
func TestSubtypeRequiredParameters(t *testing.T) {
	required := map[string][]string{
		"EventBridge-PutEvents":            {"Detail", "DetailType", "Source"},
		"SQS-SendMessage":                  {"QueueUrl", "MessageBody"},
		"SQS-ReceiveMessage":               {"QueueUrl"},
		"SQS-DeleteMessage":                {"ReceiptHandle", "QueueUrl"},
		"SQS-PurgeQueue":                   {"QueueUrl"},
		"AppConfig-GetConfiguration":       {"Application", "Environment", "Configuration", "ClientId"},
		"Kinesis-PutRecord":                {"StreamName", "Data", "PartitionKey"},
		"StepFunctions-StartExecution":     {"StateMachineArn"},
		"StepFunctions-StartSyncExecution": {"StateMachineArn"},
		"StepFunctions-StopExecution":      {"ExecutionArn"},
	}
	run(t, func(h *harness) {
		id := h.apiID("t")
		for _, subtype := range slices.Sorted(maps.Keys(required)) {
			all := map[string]string{"Region": "us-east-1"}
			for _, p := range required[subtype] {
				all[p] = "$request.body." + p
			}
			out := must(h, apigatewayv2api.OpCreateIntegration, subtypeIn(id, subtype, all))
			if deref(out.IntegrationSubtype) != subtype || !maps.Equal(out.RequestParameters, all) {
				t.Errorf("%s: CreateIntegration = %+v, want the subtype and parameters as sent", subtype, out)
			}
			for _, p := range required[subtype] {
				missing := maps.Clone(all)
				delete(missing, p)
				wantError(t, fail(h, apigatewayv2api.OpCreateIntegration, subtypeIn(id, subtype, missing)), badRequest)
			}
		}
	})
}

// A service integration is AWS_PROXY at payload format 1.0
// (apigateway-http.md 2.4, 6.1): 2.0 or HTTP_PROXY with a subtype is a bad
// request. An update is checked against the merged parameters, which
// Terraform re-sends whole (6.2; inferred, APIGW2-01 case 22): removing a
// required one is refused and changes nothing, adding an optional one is
// stored.
func TestSubtypeChecks(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		params := map[string]string{"QueueUrl": "https://sqs.us-east-1.amazonaws.com/000000000000/q", "MessageBody": "$request.body"}
		v2 := subtypeIn(id, "SQS-SendMessage", params)
		v2.PayloadFormatVersion = new("2.0")
		wantError(t, fail(h, apigatewayv2api.OpCreateIntegration, v2), badRequest)
		proxy := subtypeIn(id, "SQS-SendMessage", params)
		proxy.IntegrationType = apigatewayv2api.IntegrationTypeHttpProxy
		wantError(t, fail(h, apigatewayv2api.OpCreateIntegration, proxy), badRequest)

		integration := deref(must(h, apigatewayv2api.OpCreateIntegration, subtypeIn(id, "SQS-SendMessage", params)).IntegrationId)
		wantError(t, fail(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{
			ApiId: id, IntegrationId: integration, RequestParameters: map[string]string{"MessageBody": ""},
		}), badRequest)
		got := must(h, apigatewayv2api.OpGetIntegration, &apigatewayv2api.GetIntegrationInput{ApiId: id, IntegrationId: integration})
		if !maps.Equal(got.RequestParameters, params) {
			t.Errorf("RequestParameters after a refused update = %v, want %v", got.RequestParameters, params)
		}
		updated := must(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{
			ApiId: id, IntegrationId: integration, RequestParameters: map[string]string{"DelaySeconds": "5"},
		})
		want := maps.Clone(params)
		want["DelaySeconds"] = "5"
		if !maps.Equal(updated.RequestParameters, want) || updated.IntegrationUri != nil || updated.IntegrationMethod != nil {
			t.Errorf("UpdateIntegration = %+v, want parameters %v and still no URI or method", updated, want)
		}
	})
}

// An update's request parameters merge into the stored ones: a key sent
// with "" is removed, any other key sent is set, and a key not sent is
// kept (the parameter mapping page, apigateway-http.md 3.3).
func TestRequestParametersMergeProperty(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		key := rapid.SampledFrom([]string{"append:header.a", "overwrite:header.b", "remove:querystring.c", "overwrite:path", "append:querystring.d"})
		value := rapid.StringMatching(`[a-z$.'/]{1,8}`)
		rapid.Check(t, func(rt *rapid.T) {
			stored := rapid.MapOf(key, value).Draw(rt, "stored")
			sent := rapid.MapOf(key, rapid.OneOf(value, rapid.Just(""))).Draw(rt, "sent")
			in := httpProxyIn(id)
			if len(stored) > 0 {
				in.RequestParameters = stored
			}
			integration := deref(must(h, apigatewayv2api.OpCreateIntegration, in).IntegrationId)
			must(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{ApiId: id, IntegrationId: integration, RequestParameters: sent})
			want := maps.Clone(stored)
			if want == nil {
				want = map[string]string{}
			}
			for k, v := range sent {
				if v == "" {
					delete(want, k)
				} else {
					want[k] = v
				}
			}
			got := must(h, apigatewayv2api.OpGetIntegration, &apigatewayv2api.GetIntegrationInput{ApiId: id, IntegrationId: integration}).RequestParameters
			if !maps.Equal(got, want) {
				rt.Fatalf("stored %v, sent %v: RequestParameters = %v, want %v", stored, sent, got, want)
			}
			must(h, apigatewayv2api.OpDeleteIntegration, &apigatewayv2api.DeleteIntegrationInput{ApiId: id, IntegrationId: integration})
		})
	})
}

// An update's response parameters replace the mappings of each status they
// carry, and a status sent as {} is removed (the parameter mapping page,
// apigateway-http.md 3.3); statuses not sent are kept.
func TestResponseParametersMerge(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		in := httpProxyIn(id)
		in.ResponseParameters = map[string]map[string]string{
			"200": {"overwrite:header.a": "1", "append:header.b": "2"}, "404": {"overwrite:statuscode": "410"}, "500": {"overwrite:statuscode": "502"},
		}
		integration := deref(must(h, apigatewayv2api.OpCreateIntegration, in).IntegrationId)
		got := must(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{
			ApiId: id, IntegrationId: integration,
			ResponseParameters: map[string]map[string]string{"200": {"overwrite:header.c": "3"}, "500": {}},
		}).ResponseParameters
		if len(got) != 2 || !maps.Equal(got["200"], map[string]string{"overwrite:header.c": "3"}) || !maps.Equal(got["404"], map[string]string{"overwrite:statuscode": "410"}) {
			t.Errorf("ResponseParameters = %v, want 200 replaced, 404 kept and 500 removed", got)
		}
	})
}

// GetIntegrations pages in creation order with no page larger than asked
// (inferred, APIGW2-03; apigateway-http.md 2.2), each item what
// GetIntegration answers.
func TestGetIntegrationsPaging(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		ids := make([]string, 0, 5)
		for range 5 {
			ids = append(ids, deref(must(h, apigatewayv2api.OpCreateIntegration, httpProxyIn(id)).IntegrationId))
		}
		var got []string
		in := &apigatewayv2api.GetIntegrationsInput{ApiId: id, MaxResults: new("2")}
		for {
			out := must(h, apigatewayv2api.OpGetIntegrations, in)
			if len(out.Items) > 2 {
				t.Fatalf("a page of %d integrations, asked for at most 2", len(out.Items))
			}
			for _, item := range out.Items {
				got = append(got, deref(item.IntegrationId))
				one := must(h, apigatewayv2api.OpGetIntegration, &apigatewayv2api.GetIntegrationInput{ApiId: id, IntegrationId: deref(item.IntegrationId)})
				if a, b := mustJSON(t, item), mustJSON(t, one); a != b {
					t.Errorf("GetIntegrations item %s\nwant GetIntegration's %s", a, b)
				}
			}
			if out.NextToken == nil {
				break
			}
			in.NextToken = out.NextToken
		}
		if !slices.Equal(got, ids) {
			t.Errorf("GetIntegrations in pages of 2 = %v, want %v", got, ids)
		}
	})
}

func mustJSON(tb testing.TB, v any) string {
	tb.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		tb.Fatal(err)
	}
	return string(b)
}

// The members an integration stores are returned as sent: a private
// integration's connection, credentials, description, timeout and TLS
// configuration, and a Lambda URI referencing a stage variable verbatim
// (apigateway-http.md 3.3); an update replaces only what it carries, and
// an HTTP proxy integration keeps its method.
func TestIntegrationStoredMembers(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		vpc := apigatewayv2api.ConnectionTypeVpcLink
		out := must(h, apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{
			ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeHttpProxy, IntegrationUri: new("arn:aws:elasticloadbalancing:us-east-1:000000000000:listener/app/a/b/c"),
			IntegrationMethod: new("GET"), PayloadFormatVersion: new("1.0"), ConnectionType: &vpc, ConnectionId: new("vl1"),
			CredentialsArn: new("arn:aws:iam::000000000000:role/r"), Description: new("private"), TimeoutInMillis: new(int32(1000)),
			TlsConfig: &apigatewayv2api.TlsConfigInput{ServerNameToVerify: new("api.example.com")},
		})
		if deref(out.ConnectionId) != "vl1" || deref(out.CredentialsArn) != "arn:aws:iam::000000000000:role/r" || deref(out.Description) != "private" ||
			deref(out.TimeoutInMillis) != 1000 || out.TlsConfig == nil || deref(out.TlsConfig.ServerNameToVerify) != "api.example.com" {
			t.Errorf("CreateIntegration = %+v, want every member as sent", out)
		}
		updated := must(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{ApiId: id, IntegrationId: *out.IntegrationId, Description: new("")})
		if deref(updated.Description) != "" || updated.Description == nil || deref(updated.IntegrationMethod) != "GET" || deref(updated.ConnectionId) != "vl1" {
			t.Errorf("UpdateIntegration of the description = %+v, want it set to \"\" and the rest kept", updated)
		}
		staged := "arn:aws:lambda:us-east-1:000000000000:function:${stageVariables.fn}"
		lambda := must(h, apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{
			ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationUri: new(staged), PayloadFormatVersion: new("2.0"),
		})
		if deref(lambda.IntegrationUri) != staged || deref(lambda.IntegrationMethod) != "POST" {
			t.Errorf("a stage variable URI = %+v, want it verbatim with method POST", lambda)
		}
	})
}

// An update is checked as a create is: a payload format or URI an HTTP API
// does not take is a bad request, an HTTP proxy integration needs a URI,
// and an unknown integration is NotFoundException (apigateway-http.md 2.4,
// 3.3). A refused update changes nothing.
func TestIntegrationChecks(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		fnARN := h.createFunction("fn")
		noURI := httpProxyIn(id)
		noURI.IntegrationUri = nil
		wantError(t, fail(h, apigatewayv2api.OpCreateIntegration, noURI), badRequest)
		integration := h.lambdaIntegration(id, fnARN, "2.0")
		for _, in := range []*apigatewayv2api.UpdateIntegrationInput{
			{PayloadFormatVersion: new("3.0")},
			{IntegrationUri: new("https://example.com")},
			{IntegrationType: new(apigatewayv2api.IntegrationTypeMock)},
			{IntegrationSubtype: new("SNS-Publish")},
		} {
			in.ApiId, in.IntegrationId = id, integration
			wantError(t, fail(h, apigatewayv2api.OpUpdateIntegration, in), badRequest)
		}
		got := must(h, apigatewayv2api.OpGetIntegration, &apigatewayv2api.GetIntegrationInput{ApiId: id, IntegrationId: integration})
		if deref(got.PayloadFormatVersion) != "2.0" || deref(got.IntegrationUri) != fnARN || deref(got.IntegrationType) != apigatewayv2api.IntegrationTypeAwsProxy || got.IntegrationSubtype != nil {
			t.Errorf("GetIntegration after refused updates = %+v, want it unchanged", got)
		}
		wantError(t, fail(h, apigatewayv2api.OpGetIntegration, &apigatewayv2api.GetIntegrationInput{ApiId: id, IntegrationId: "zzzzzzz"}), notFound)
		wantError(t, fail(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{ApiId: id, IntegrationId: "zzzzzzz", Description: new("d")}), notFound)
		wantError(t, fail(h, apigatewayv2api.OpGetIntegrations, &apigatewayv2api.GetIntegrationsInput{ApiId: "zzzzzzzzzz"}), notFound)
	})
}

// Deleting an integration a route targets is refused with
// ConflictException (APIGW2-03, recorded 2026-11-03: step 21) and changes
// nothing; once no route targets it, it is deleted.
func TestDeleteIntegrationARouteTargets(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		integration := h.lambdaIntegration(id, h.createFunction("fn"), "2.0")
		rid := h.createRoute(id, "GET /a", integration)
		wantError(t, fail(h, apigatewayv2api.OpDeleteIntegration, &apigatewayv2api.DeleteIntegrationInput{ApiId: id, IntegrationId: integration}), conflict)
		must(h, apigatewayv2api.OpGetIntegration, &apigatewayv2api.GetIntegrationInput{ApiId: id, IntegrationId: integration})
		must(h, apigatewayv2api.OpDeleteRoute, &apigatewayv2api.DeleteRouteInput{ApiId: id, RouteId: rid})
		must(h, apigatewayv2api.OpDeleteIntegration, &apigatewayv2api.DeleteIntegrationInput{ApiId: id, IntegrationId: integration})
		wantError(t, fail(h, apigatewayv2api.OpDeleteIntegration, &apigatewayv2api.DeleteIntegrationInput{ApiId: id, IntegrationId: integration}), notFound)
	})
}

// UpdateIntegration deploys to the autoDeploy stages before it returns
// (apigateway-http.md 3.5): credentials on a Lambda integration answer 501
// at once without running the function, since understudy does not assume
// the role, and an empty credentialsArn, which names no role, serves
// again.
func TestUpdateIntegrationDeploysAtOnce(t *testing.T) {
	run(t, func(h *harness) {
		id, integration := h.lambdaAPI("2.0", "GET /a")
		must(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{ApiId: id, IntegrationId: integration, CredentialsArn: new("arn:aws:iam::000000000000:role/r")})
		want501(t, h.get(id, "/a"), "a Lambda integration with credentials")
		h.notInvoked("a Lambda integration with credentials")
		must(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{ApiId: id, IntegrationId: integration, CredentialsArn: new("")})
		if rec := h.get(id, "/a"); rec.Code != http.StatusOK {
			t.Errorf("GET /a with credentialsArn \"\" = %d %s, want the function's 200", rec.Code, rec.Body)
		}
	})
}

// Routes and integrations, every stored member included, survive Export,
// Reset and Import (ADR 0009): GetRoutes and GetIntegrations answer as
// before, and new children still get ids and places after the old ones.
func TestRoutesAndIntegrationsSurviveExportAndImport(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		in := httpProxyIn(id)
		in.RequestParameters = map[string]string{"remove:querystring.debug": "''"}
		in.ResponseParameters = map[string]map[string]string{"200": {"overwrite:header.x": "y"}}
		in.TlsConfig = &apigatewayv2api.TlsConfigInput{ServerNameToVerify: new("example.com")}
		integration := deref(must(h, apigatewayv2api.OpCreateIntegration, in).IntegrationId)
		iam := apigatewayv2api.AuthorizationTypeAwsIam
		must(h, apigatewayv2api.OpCreateRoute, &apigatewayv2api.CreateRouteInput{
			ApiId: id, RouteKey: "GET /a", Target: new("integrations/" + integration), AuthorizationType: &iam, ApiKeyRequired: new(true),
			RequestModels: map[string]string{"application/json": "M"}, RequestParameters: map[string]apigatewayv2api.ParameterConstraints{"route.request.header.h": {Required: new(false)}},
		})
		h.createRoute(id, "$default", "")
		read := func() string {
			return mustJSON(t, map[string]any{
				"routes":       h.listRoutes(id, ""),
				"integrations": must(h, apigatewayv2api.OpGetIntegrations, &apigatewayv2api.GetIntegrationsInput{ApiId: id}).Items,
			})
		}
		before := read()
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatalf("Import = %v", err)
		}
		if after := read(); after != before {
			t.Errorf("after the round trip:\n%s\nwant\n%s", after, before)
		}
		last := h.createRoute(id, "GET /b", "")
		if ids := routeIDs(h.listRoutes(id, "")); len(ids) != 3 || ids[2] != last || slices.Contains(ids[:2], last) {
			t.Errorf("GetRoutes after a create = %v, want the new route %s last", ids, last)
		}
		if got := routeIDs(h.listRoutes(id, "1")); len(got) != 3 {
			t.Errorf("GetRoutes in pages of 1 after Import = %v, want 3 routes", got)
		}
	})
}
