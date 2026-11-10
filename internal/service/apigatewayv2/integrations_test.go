package apigatewayv2_test

import (
	"maps"
	"strconv"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// invokeURI is the API Gateway invoke URI of a function ARN, Terraform's
// invoke_arn (apigateway-http.md 3.3, 9.7).
func invokeURI(fnARN string) string {
	return "arn:aws:apigateway:" + region + ":lambda:path/2015-03-31/functions/" + fnARN + "/invocations"
}

// A Lambda proxy integration answers IntegrationMethod POST, ConnectionType
// INTERNET and TimeoutInMillis 30000 when they are not sent (the
// create-integration CLI example and Terraform's acceptance test against
// AWS, apigateway-http.md 3.3), the URI verbatim, function ARN or invoke
// URI (Terraform pairs integration_uri with invoke_arn, 3.3), and no
// PassthroughBehavior or IntegrationResponseSelectionExpression even when
// Terraform sends WHEN_NO_MATCH (3.3, 9.3).
func TestLambdaIntegrationDefaults(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		fnARN := h.createFunction("fn")
		for _, uri := range []string{fnARN, invokeURI(fnARN)} {
			pass := apigatewayv2api.PassthroughBehaviorWhenNoMatch
			out := must(h, apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{
				ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationUri: new(uri),
				PayloadFormatVersion: new("2.0"), PassthroughBehavior: &pass,
			})
			if deref(out.IntegrationMethod) != "POST" || deref(out.ConnectionType) != apigatewayv2api.ConnectionTypeInternet || deref(out.TimeoutInMillis) != 30000 {
				t.Errorf("%s: IntegrationMethod %q, ConnectionType %v, TimeoutInMillis %v; want POST, INTERNET, 30000", uri, deref(out.IntegrationMethod), out.ConnectionType, out.TimeoutInMillis)
			}
			if deref(out.IntegrationUri) != uri || deref(out.PayloadFormatVersion) != "2.0" || deref(out.IntegrationType) != apigatewayv2api.IntegrationTypeAwsProxy || deref(out.IntegrationId) == "" {
				t.Errorf("CreateIntegration = %+v, want the URI, type and version as sent and an id", out)
			}
			if out.PassthroughBehavior != nil || out.IntegrationResponseSelectionExpression != nil {
				t.Errorf("PassthroughBehavior %v, IntegrationResponseSelectionExpression %v; want neither for an HTTP API", out.PassthroughBehavior, out.IntegrationResponseSelectionExpression)
			}
			got := must(h, apigatewayv2api.OpGetIntegration, &apigatewayv2api.GetIntegrationInput{ApiId: id, IntegrationId: *out.IntegrationId})
			if deref(got.IntegrationUri) != uri || deref(got.TimeoutInMillis) != 30000 || got.PassthroughBehavior != nil {
				t.Errorf("GetIntegration = %+v, want what CreateIntegration answered", got)
			}
		}
	})
}

// payloadFormatVersion is "Required for HTTP APIs": 1.0 or 2.0 for a Lambda
// integration and 1.0 for everything else (the integrations API reference,
// apigateway-http.md 2.4); AWS, HTTP and MOCK integrations are "Supported
// only for WebSocket APIs" (the model's IntegrationType documentation,
// 2.4).
func TestIntegrationTypesAndPayloadVersions(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		fnARN := h.createFunction("fn")
		lambdaIn := func(version *string) *apigatewayv2api.CreateIntegrationInput {
			return &apigatewayv2api.CreateIntegrationInput{ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationUri: new(fnARN), PayloadFormatVersion: version}
		}
		httpIn := func(version string) *apigatewayv2api.CreateIntegrationInput {
			return &apigatewayv2api.CreateIntegrationInput{ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeHttpProxy, IntegrationUri: new("https://example.com/{proxy}"), IntegrationMethod: new("ANY"), PayloadFormatVersion: new(version)}
		}
		must(h, apigatewayv2api.OpCreateIntegration, lambdaIn(new("1.0")))
		must(h, apigatewayv2api.OpCreateIntegration, lambdaIn(new("2.0")))
		must(h, apigatewayv2api.OpCreateIntegration, httpIn("1.0"))
		wantError(t, fail(h, apigatewayv2api.OpCreateIntegration, lambdaIn(new("3.0"))), badRequest)
		wantError(t, fail(h, apigatewayv2api.OpCreateIntegration, lambdaIn(nil)), badRequest)
		wantError(t, fail(h, apigatewayv2api.OpCreateIntegration, httpIn("2.0")), badRequest)
		for _, typ := range []apigatewayv2api.IntegrationType{apigatewayv2api.IntegrationTypeAws, apigatewayv2api.IntegrationTypeHttp, apigatewayv2api.IntegrationTypeMock} {
			err := fail(h, apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{ApiId: id, IntegrationType: typ, IntegrationUri: new(fnARN), PayloadFormatVersion: new("1.0")})
			wantError(t, err, badRequest)
		}
		if n := len(must(h, apigatewayv2api.OpGetIntegrations, &apigatewayv2api.GetIntegrationsInput{ApiId: id}).Items); n != 3 {
			t.Errorf("GetIntegrations = %d items, want the 3 accepted", n)
		}
	})
}

// TimeoutInMillis is 50..30000 by the model's @range and its documentation
// ("between 50 and 30,000 milliseconds for HTTP APIs", apigateway-http.md
// 2.4).
func TestIntegrationTimeoutRange(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		fnARN := h.createFunction("fn")
		create := func(ms int32) (*apigatewayv2api.CreateIntegrationOutput, *awserr.Error) {
			return h.Invoke(h.Context(), apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{
				ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationUri: new(fnARN), PayloadFormatVersion: new("2.0"), TimeoutInMillis: new(ms),
			})
		}
		rapid.Check(t, func(rt *rapid.T) {
			ms := rapid.Int32Range(50, 30000).Draw(rt, "accepted")
			out, err := create(ms)
			if err != nil || deref(out.TimeoutInMillis) != ms {
				rt.Fatalf("TimeoutInMillis %d: %+v, %v; want it stored", ms, out, err)
			}
			must(h, apigatewayv2api.OpDeleteIntegration, &apigatewayv2api.DeleteIntegrationInput{ApiId: id, IntegrationId: *out.IntegrationId})
			bad := rapid.OneOf(rapid.Int32Range(-1000, 49), rapid.Int32Range(30001, 120000)).Draw(rt, "refused")
			if out, err := create(bad); err == nil || err.ID.Shape != badRequest.Shape {
				rt.Fatalf("TimeoutInMillis %d: %+v, %v; want BadRequestException", bad, out, err)
			}
		})
	})
}

// An HTTP proxy integration keeps its method as sent and its URI, with
// payload format 1.0 and ConnectionType INTERNET by default (Terraform's
// acceptance test, apigateway-http.md 3.3, 9.3); a VPC link integration is
// stored with its connection id. Neither runs a request (invoke tests).
func TestHTTPProxyIntegration(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		out := must(h, apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{
			ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeHttpProxy, IntegrationUri: new("https://example.com/pets"), IntegrationMethod: new("GET"), PayloadFormatVersion: new("1.0"),
		})
		if deref(out.IntegrationMethod) != "GET" || deref(out.IntegrationUri) != "https://example.com/pets" || deref(out.ConnectionType) != apigatewayv2api.ConnectionTypeInternet {
			t.Errorf("CreateIntegration = %+v, want GET, the URI and INTERNET", out)
		}
		vpc := apigatewayv2api.ConnectionTypeVpcLink
		link := must(h, apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{
			ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeHttpProxy, IntegrationUri: new("arn:aws:elasticloadbalancing:us-east-1:000000000000:listener/app/a/b/c"),
			IntegrationMethod: new("ANY"), PayloadFormatVersion: new("1.0"), ConnectionType: &vpc, ConnectionId: new("abc123"),
		})
		if deref(link.ConnectionType) != vpc || deref(link.ConnectionId) != "abc123" {
			t.Errorf("VPC link integration = %+v, want VPC_LINK with connection abc123 stored", link)
		}
	})
}

// A service integration is AWS_PROXY with a subtype, credentials and
// request parameters at payload format 1.0; AWS returns no
// IntegrationMethod and no IntegrationUri for it (Terraform's acceptance
// test, apigateway-http.md 3.3) and the parameters as sent. Every
// required parameter must be configured ("You must configure all required
// parameters to create an integration", 6.2; the required set is 6.1's
// table), and only the ten subtypes of 6.1 exist: there is no SNS one.
func TestSubtypeIntegration(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		params := map[string]string{"QueueUrl": "https://sqs.us-east-1.amazonaws.com/000000000000/q", "MessageBody": "$request.body"}
		in := func(subtype string, p map[string]string) *apigatewayv2api.CreateIntegrationInput {
			return &apigatewayv2api.CreateIntegrationInput{
				ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationSubtype: new(subtype),
				CredentialsArn: new("arn:aws:iam::000000000000:role/apigw"), RequestParameters: p, PayloadFormatVersion: new("1.0"),
			}
		}
		out := must(h, apigatewayv2api.OpCreateIntegration, in("SQS-SendMessage", params))
		if out.IntegrationMethod != nil || out.IntegrationUri != nil {
			t.Errorf("IntegrationMethod %v, IntegrationUri %v; want neither for a service integration", out.IntegrationMethod, out.IntegrationUri)
		}
		if !maps.Equal(out.RequestParameters, params) || deref(out.IntegrationSubtype) != "SQS-SendMessage" {
			t.Errorf("RequestParameters %v, subtype %q; want them as sent", out.RequestParameters, deref(out.IntegrationSubtype))
		}
		wantError(t, fail(h, apigatewayv2api.OpCreateIntegration, in("SQS-SendMessage", map[string]string{"QueueUrl": params["QueueUrl"]})), badRequest)
		wantError(t, fail(h, apigatewayv2api.OpCreateIntegration, in("SNS-Publish", map[string]string{"TopicArn": "arn:aws:sns:us-east-1:000000000000:t", "Message": "m"})), badRequest)
		for subtype, p := range map[string]map[string]string{
			"EventBridge-PutEvents":        {"Detail": "$request.body", "DetailType": "t", "Source": "s"},
			"StepFunctions-StartExecution": {"StateMachineArn": "arn:aws:states:us-east-1:000000000000:stateMachine:m"},
		} {
			must(h, apigatewayv2api.OpCreateIntegration, in(subtype, p))
		}
	})
}

// Request parameters are returned exactly, the two-character removal value
// ” included (Terraform's acceptance test, apigateway-http.md 3.3); on
// update a mapping sent with value "" is removed, and a response mapping
// sent as its status key with {} is removed (the parameter mapping page,
// 3.3).
func TestRequestParametersByteForByte(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		req := map[string]string{"append:header.x-from": "$request.header.host", "remove:querystring.debug": "''", "overwrite:path": "/v2$request.path"}
		resp := map[string]map[string]string{"200": {"overwrite:header.x-served": "understudy"}, "500": {"overwrite:statuscode": "502"}}
		out := must(h, apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{
			ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeHttpProxy, IntegrationUri: new("https://example.com"), IntegrationMethod: new("ANY"),
			PayloadFormatVersion: new("1.0"), RequestParameters: req, ResponseParameters: resp,
		})
		if !maps.Equal(out.RequestParameters, req) || len(out.ResponseParameters) != 2 || !maps.Equal(out.ResponseParameters["200"], resp["200"]) {
			t.Errorf("parameters = %v, %v; want %v, %v", out.RequestParameters, out.ResponseParameters, req, resp)
		}
		must(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{
			ApiId: id, IntegrationId: *out.IntegrationId, RequestParameters: map[string]string{"remove:querystring.debug": ""},
			ResponseParameters: map[string]map[string]string{"500": {}},
		})
		got := must(h, apigatewayv2api.OpGetIntegration, &apigatewayv2api.GetIntegrationInput{ApiId: id, IntegrationId: *out.IntegrationId})
		wantReq := map[string]string{"append:header.x-from": "$request.header.host", "overwrite:path": "/v2$request.path"}
		if !maps.Equal(got.RequestParameters, wantReq) {
			t.Errorf("RequestParameters after removing one = %v, want %v", got.RequestParameters, wantReq)
		}
		if _, ok := got.ResponseParameters["500"]; ok || !maps.Equal(got.ResponseParameters["200"], resp["200"]) {
			t.Errorf("ResponseParameters after removing 500 = %v, want only 200", got.ResponseParameters)
		}
	})
}

// Members that only act on WebSocket APIs answer NotImplemented naming
// them (apigateway-http.md 2.4 lists them as WebSocket-only).
func TestWebSocketOnlyIntegrationMembers(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		fnARN := h.createFunction("fn")
		base := func() *apigatewayv2api.CreateIntegrationInput {
			return &apigatewayv2api.CreateIntegrationInput{ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationUri: new(fnARN), PayloadFormatVersion: new("2.0")}
		}
		templates := base()
		templates.RequestTemplates = map[string]string{"application/json": "{}"}
		wantNotImplemented(t, fail(h, apigatewayv2api.OpCreateIntegration, templates), "RequestTemplates")
		selection := base()
		selection.TemplateSelectionExpression = new("\\$default")
		wantNotImplemented(t, fail(h, apigatewayv2api.OpCreateIntegration, selection), "TemplateSelectionExpression")
		handling := base()
		strategy := apigatewayv2api.ContentHandlingStrategyConvertToText
		handling.ContentHandlingStrategy = &strategy
		wantNotImplemented(t, fail(h, apigatewayv2api.OpCreateIntegration, handling), "ContentHandlingStrategy")
	})
}

// "Integrations per API: 300" (the HTTP API quotas page, apigateway-http.md
// 3.9).
func TestIntegrationQuota(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		for i := range 300 {
			must(h, apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{
				ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeHttpProxy, IntegrationUri: new("https://example.com/" + strconv.Itoa(i)),
				IntegrationMethod: new("GET"), PayloadFormatVersion: new("1.0"),
			})
		}
		err := fail(h, apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{
			ApiId: id, IntegrationType: apigatewayv2api.IntegrationTypeHttpProxy, IntegrationUri: new("https://example.com/more"), IntegrationMethod: new("GET"), PayloadFormatVersion: new("1.0"),
		})
		wantError(t, err, badRequest)
	})
}

// UpdateIntegration replaces what it carries; DeleteIntegration removes
// the integration, after which GetIntegration is NotFoundException.
func TestUpdateAndDeleteIntegration(t *testing.T) {
	run(t, func(h *harness) {
		id := h.apiID("t")
		integration := h.lambdaIntegration(id, h.createFunction("fn"), "1.0")
		must(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{ApiId: id, IntegrationId: integration, PayloadFormatVersion: new("2.0"), TimeoutInMillis: new(int32(5000)), Description: new("d")})
		got := must(h, apigatewayv2api.OpGetIntegration, &apigatewayv2api.GetIntegrationInput{ApiId: id, IntegrationId: integration})
		if deref(got.PayloadFormatVersion) != "2.0" || deref(got.TimeoutInMillis) != 5000 || deref(got.Description) != "d" || deref(got.IntegrationMethod) != "POST" {
			t.Errorf("GetIntegration after update = %+v, want 2.0, 5000 ms, description d, method still POST", got)
		}
		wantError(t, fail(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{ApiId: id, IntegrationId: integration, TimeoutInMillis: new(int32(30001))}), badRequest)
		must(h, apigatewayv2api.OpDeleteIntegration, &apigatewayv2api.DeleteIntegrationInput{ApiId: id, IntegrationId: integration})
		wantError(t, fail(h, apigatewayv2api.OpGetIntegration, &apigatewayv2api.GetIntegrationInput{ApiId: id, IntegrationId: integration}), notFound)
	})
}
