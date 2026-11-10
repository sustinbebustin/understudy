package apigateway_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
)

// PutIntegration answers the recorded defaults: cacheKeyParameters [],
// cacheNamespace the resource id, passthroughBehavior WHEN_NO_MATCH,
// timeoutInMillis 29000; HTTP and HTTP_PROXY add connectionType INTERNET,
// AWS, AWS_PROXY and MOCK omit it; MOCK omits httpMethod even when POST was
// sent (aws-recorded, test_apigateway_integrations.snapshot.json:L268-L380
// and L1062-L1083; apigateway-rest.md 2.3).
func TestPutIntegrationDefaults(t *testing.T) {
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id, root := h.api("t")
		cases := map[string]*apigatewayapi.PutIntegrationInput{
			"AWS_PROXY":  proxyInput(id, "", "GET", fnARN),
			"HTTP_PROXY": {Type: apigatewayapi.IntegrationTypeHttpProxy, IntegrationHttpMethod: new("GET"), Uri: new("http://example.com/{proxy}")},
			"HTTP":       {Type: apigatewayapi.IntegrationTypeHttp, IntegrationHttpMethod: new("GET"), Uri: new("http://example.com")},
			"AWS":        {Type: apigatewayapi.IntegrationTypeAws, IntegrationHttpMethod: new("POST"), Uri: new("arn:aws:apigateway:" + region + ":sqs:path/" + account + "/q")},
			"MOCK":       {Type: apigatewayapi.IntegrationTypeMock, IntegrationHttpMethod: new("POST")},
		}
		for name, in := range cases {
			res := h.resource(id, root, "r"+map[string]string{"AWS_PROXY": "1", "HTTP_PROXY": "2", "HTTP": "3", "AWS": "4", "MOCK": "5"}[name])
			h.method(id, res, "GET")
			in.RestApiId, in.ResourceId, in.HttpMethod = id, res, "GET"
			out := must(h, apigatewayapi.OpPutIntegration, in)
			if out.CacheKeyParameters == nil || len(out.CacheKeyParameters) != 0 || deref(out.CacheNamespace) != res ||
				deref(out.PassthroughBehavior) != "WHEN_NO_MATCH" || out.TimeoutInMillis != 29000 || deref(out.Type) != in.Type {
				t.Errorf("%s = %+v, want cacheKeyParameters [], cacheNamespace %s, WHEN_NO_MATCH, 29000", name, out, res)
			}
			wantConn := name == "HTTP" || name == "HTTP_PROXY"
			if (out.ConnectionType != nil) != wantConn || (wantConn && *out.ConnectionType != apigatewayapi.ConnectionTypeInternet) {
				t.Errorf("%s connectionType = %v, want INTERNET only for HTTP and HTTP_PROXY", name, out.ConnectionType)
			}
			if name == "MOCK" && out.HttpMethod != nil {
				t.Errorf("MOCK httpMethod = %q, want none", *out.HttpMethod)
			}
			if name != "MOCK" && deref(out.HttpMethod) != deref(in.IntegrationHttpMethod) {
				t.Errorf("%s httpMethod = %q, want %q", name, deref(out.HttpMethod), deref(in.IntegrationHttpMethod))
			}
			got := must(h, apigatewayapi.OpGetIntegration, &apigatewayapi.GetIntegrationInput{RestApiId: id, ResourceId: res, HttpMethod: "GET"})
			if deref(got.Type) != in.Type || deref(got.Uri) != deref(out.Uri) {
				t.Errorf("GetIntegration %s = %+v, want what PutIntegration answered", name, got)
			}
		}
	})
}

// Recorded PutIntegration refusals (aws-recorded, apigateway-rest.md 2.4
// table, sources beside each).
func TestPutIntegrationRefusals(t *testing.T) {
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id, root := h.api("t")
		in := func(typ apigatewayapi.IntegrationType, method, uri *string) *apigatewayapi.PutIntegrationInput {
			return &apigatewayapi.PutIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", Type: typ, IntegrationHttpMethod: method, Uri: uri}
		}
		// No method yet (test_apigateway_api.snapshot.json:L1104-L1153).
		wantText(t, fail(h, apigatewayapi.OpPutIntegration, proxyInput(id, root, "GET", fnARN)), notFound, "Invalid Method identifier specified")
		h.method(id, root, "GET")
		// test_apigateway_api.snapshot.json:L3084-L3098.
		wantText(t, fail(h, apigatewayapi.OpPutIntegration, in("HTTPS_PROXY", new("GET"), new("http://example.com"))), validation,
			"1 validation error detected: Value 'HTTPS_PROXY' at 'putIntegrationInput.type' failed to satisfy constraint: Member must satisfy enum value set: [HTTP, MOCK, AWS_PROXY, HTTP_PROXY, AWS]")
		// test_apigateway_integrations.snapshot.json:L271-L303.
		for _, typ := range []apigatewayapi.IntegrationType{apigatewayapi.IntegrationTypeAwsProxy, apigatewayapi.IntegrationTypeHttp, apigatewayapi.IntegrationTypeHttpProxy, apigatewayapi.IntegrationTypeAws} {
			wantText(t, fail(h, apigatewayapi.OpPutIntegration, in(typ, nil, new(invokeURI(fnARN)))), badRequest, "Enumeration value for HttpMethod must be non-empty")
		}
		// test_apigateway_integrations.snapshot.json:L383-L393;
		// test_apigateway_lambda.snapshot.json:L1799-L1857.
		wantText(t, fail(h, apigatewayapi.OpPutIntegration, in(apigatewayapi.IntegrationTypeAwsProxy, new("POST"), new("arn:aws:apigateway:"+region+":lambda:path/random/value/"+fnARN))),
			badRequest, "Integrations of type 'AWS_PROXY' currently only supports Lambda function and Firehose stream invocations.")
		// test_apigateway_integrations.snapshot.json:L394-L437.
		wantText(t, fail(h, apigatewayapi.OpPutIntegration, in(apigatewayapi.IntegrationTypeHttp, new("GET"), new("not a url"))), badRequest, "Invalid HTTP endpoint specified for URI")
		wantText(t, fail(h, apigatewayapi.OpPutIntegration, in(apigatewayapi.IntegrationTypeAws, new("POST"), new("bad-arn:aws:apigateway"))), badRequest, "Invalid ARN specified in the request")
		wantText(t, fail(h, apigatewayapi.OpPutIntegration, in(apigatewayapi.IntegrationTypeAws, new("POST"), new(fnARN))), badRequest, "AWS ARN for integration must contain path or action")
		wantText(t, fail(h, apigatewayapi.OpGetIntegration, &apigatewayapi.GetIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"}),
			notFound, "Invalid Integration identifier specified") // test_apigateway_api.snapshot.json:L4113-L4128
	})
}

// connectionType VPC_LINK, tlsConfig and integrationTarget answer
// NotImplemented naming the member.
func TestPutIntegrationNotImplemented(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.method(id, root, "GET")
		base := func() *apigatewayapi.PutIntegrationInput {
			return &apigatewayapi.PutIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", Type: apigatewayapi.IntegrationTypeHttpProxy, IntegrationHttpMethod: new("GET"), Uri: new("http://example.com")}
		}
		vpc := base()
		vpc.ConnectionType, vpc.ConnectionId = new(apigatewayapi.ConnectionTypeVpcLink), new("abc123")
		wantNotImplemented(t, fail(h, apigatewayapi.OpPutIntegration, vpc), "VPC_LINK")
		tls := base()
		tls.TlsConfig = &apigatewayapi.TlsConfig{InsecureSkipVerification: true}
		wantNotImplemented(t, fail(h, apigatewayapi.OpPutIntegration, tls), "tlsConfig")
		target := base()
		target.IntegrationTarget = new("arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/x/1")
		wantNotImplemented(t, fail(h, apigatewayapi.OpPutIntegration, target), "integrationTarget")
		wantText(t, fail(h, apigatewayapi.OpGetIntegration, &apigatewayapi.GetIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"}), notFound, "Invalid Integration identifier specified")
	})
}

// credentials, responseTransferMode, contentHandling, a stage variable in
// the uri and requestParameters are stored and echoed verbatim
// (apigateway-rest.md 9.3 item 4, 3.4: what Terraform reads back).
func TestPutIntegrationEchoesStoredMembers(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.method(id, root, "GET")
		uri := "arn:aws:apigateway:" + region + ":lambda:path/2015-03-31/functions/arn:aws:lambda:" + region + ":" + account + ":function:${stageVariables.fn}/invocations"
		in := &apigatewayapi.PutIntegrationInput{
			RestApiId: id, ResourceId: root, HttpMethod: "GET", Type: apigatewayapi.IntegrationTypeAwsProxy, IntegrationHttpMethod: new("POST"), Uri: new(uri),
			Credentials: new("arn:aws:iam::" + account + ":role/apigw"), ResponseTransferMode: new(apigatewayapi.ResponseTransferModeStream),
			ContentHandling: new(apigatewayapi.ContentHandlingStrategyConvertToText), RequestParameters: map[string]string{"integration.request.header.x": "'static'"},
			TimeoutInMillis: new(int32(1000)),
		}
		out := must(h, apigatewayapi.OpPutIntegration, in)
		got := must(h, apigatewayapi.OpGetIntegration, &apigatewayapi.GetIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"})
		for name, i := range map[string]*apigatewayapi.Integration{"PutIntegration": out, "GetIntegration": got} {
			if deref(i.Uri) != uri || deref(i.Credentials) != deref(in.Credentials) || deref(i.ResponseTransferMode) != apigatewayapi.ResponseTransferModeStream ||
				deref(i.ContentHandling) != apigatewayapi.ContentHandlingStrategyConvertToText || i.RequestParameters["integration.request.header.x"] != "'static'" || i.TimeoutInMillis != 1000 {
				t.Errorf("%s = %+v, want every member as sent", name, i)
			}
		}
	})
}

// UpdateIntegration patches the documented paths (aws-doc
// patch-operations, apigateway-rest.md 3.8); DeleteIntegration removes it.
func TestUpdateAndDeleteIntegration(t *testing.T) {
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		other := h.createFunction("other")
		id, root := h.api("t")
		h.proxy(id, root, "GET", fnARN)
		out := must(h, apigatewayapi.OpUpdateIntegration, &apigatewayapi.UpdateIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/uri", invokeURI(other)), op(apigatewayapi.OpReplace, "/timeoutInMillis", "5000"),
			op(apigatewayapi.OpAdd, "/cacheKeyParameters/method.request.path.id"),
		}})
		if deref(out.Uri) != invokeURI(other) || out.TimeoutInMillis != 5000 || !slices.Contains(out.CacheKeyParameters, "method.request.path.id") {
			t.Errorf("UpdateIntegration = %+v, want the new uri, timeout and cache key", out)
		}
		wantError(t, fail(h, apigatewayapi.OpUpdateIntegration, &apigatewayapi.UpdateIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/type", "MOCK"),
		}}), badRequest)
		must(h, apigatewayapi.OpDeleteIntegration, &apigatewayapi.DeleteIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"})
		wantText(t, fail(h, apigatewayapi.OpGetIntegration, &apigatewayapi.GetIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"}), notFound, "Invalid Integration identifier specified")
	})
}

// Integration responses: a status outside [1-5]\d\d answers the recorded
// ValidationException (aws-recorded, test_apigateway_api.snapshot.json:L4062-L4086);
// selectionPattern is echoed including "" and responseTemplates as {} when
// sent empty (aws-recorded, test_apigateway_integrations.snapshot.json:L113-L130;
// apigateway-rest.md 3.5); an unknown status answers "Invalid Response
// status code specified" (L4129-L4144); UpdateIntegrationResponse replaces
// selectionPattern (3.8); DeleteIntegrationResponse removes it.
func TestIntegrationResponses(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.method(id, root, "GET")
		must(h, apigatewayapi.OpPutIntegration, &apigatewayapi.PutIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", Type: apigatewayapi.IntegrationTypeMock})
		wantText(t, fail(h, apigatewayapi.OpPutIntegrationResponse, &apigatewayapi.PutIntegrationResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "wrong"}), validation,
			`1 validation error detected: Value 'wrong' at 'statusCode' failed to satisfy constraint: Member must satisfy regular expression pattern: [1-5]\d\d`)
		out := must(h, apigatewayapi.OpPutIntegrationResponse, &apigatewayapi.PutIntegrationResponseInput{
			RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200", SelectionPattern: new(""), ResponseTemplates: map[string]string{},
			ResponseParameters: map[string]string{"method.response.header.Access-Control-Allow-Origin": "'*'"}, ContentHandling: new(apigatewayapi.ContentHandlingStrategyConvertToText),
		})
		if deref(out.StatusCode) != "200" || out.SelectionPattern == nil || *out.SelectionPattern != "" || out.ResponseTemplates == nil || len(out.ResponseTemplates) != 0 ||
			out.ResponseParameters["method.response.header.Access-Control-Allow-Origin"] != "'*'" || deref(out.ContentHandling) != apigatewayapi.ContentHandlingStrategyConvertToText {
			t.Errorf("PutIntegrationResponse = %+v, want selectionPattern \"\", responseTemplates {}, the parameter and contentHandling", out)
		}
		upd := must(h, apigatewayapi.OpUpdateIntegrationResponse, &apigatewayapi.UpdateIntegrationResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/selectionPattern", `2\d{2}`),
		}})
		if deref(upd.SelectionPattern) != `2\d{2}` {
			t.Errorf("UpdateIntegrationResponse selectionPattern = %q, want 2\\d{2}", deref(upd.SelectionPattern))
		}
		wantText(t, fail(h, apigatewayapi.OpGetIntegrationResponse, &apigatewayapi.GetIntegrationResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "404"}), notFound, "Invalid Response status code specified")
		must(h, apigatewayapi.OpDeleteIntegrationResponse, &apigatewayapi.DeleteIntegrationResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200"})
		wantText(t, fail(h, apigatewayapi.OpGetIntegrationResponse, &apigatewayapi.GetIntegrationResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200"}), notFound, "Invalid Response status code specified")
	})
}
