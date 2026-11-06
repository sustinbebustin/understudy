package apigateway_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/restjson"
)

// requestBody is a smithy.Body over a test request's body.
type requestBody struct{ r *http.Request }

func (b requestBody) Open() (io.ReadCloser, error) { return b.r.Body, nil }
func (b requestBody) Len() (int64, bool)           { return b.r.ContentLength, b.r.ContentLength >= 0 }
func (requestBody) Trailer(string) (string, bool)  { return "", false }

// A boolean requestParameters value does not decode: SerializationException
// 400 (aws-recorded, test_apigateway_api.snapshot.json:L3822-L3869;
// apigateway-rest.md 2.4, 3.4). The recorded text is Java's, which
// understudy's decoder does not reproduce, so only the identity is
// asserted (PutIntegration's coverage note says so).
func TestPutIntegrationBooleanRequestParameter(t *testing.T) {
	op, ok := apigatewayapi.Service().Operation("PutIntegration")
	if !ok {
		t.Fatal("no PutIntegration operation")
	}
	decode := func(value string) *awserr.Error {
		body := `{"type":"HTTP_PROXY","httpMethod":"GET","uri":"http://example.com","requestParameters":{"integration.request.header.x":` + value + `}}`
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/restapis/a1b2c3d4e5/resources/abc123/methods/GET/integration", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return restjson.Codec{}.Decode(r, op, requestBody{r}, &apigatewayapi.PutIntegrationInput{})
	}
	if err := decode(`"'static'"`); err != nil {
		t.Fatalf("Decode with a string value = %+v, want nil", err)
	}
	if err := decode("true"); err == nil || err.ID != apigatewayapi.IdentitySerializationException() || err.ID.Status != http.StatusBadRequest {
		t.Errorf("Decode with a boolean value = %+v, want SerializationException 400", err)
	}
}

// AWS_PROXY accepts a Lambda function uri, qualified or not, and a uri
// naming a stage variable verbatim; an AWS uri needs path or action after
// the service; an HTTP uri must be an absolute http(s) URL (aws-recorded
// texts, apigateway-rest.md 2.4, 3.4; the stage-variable rule is inferred,
// contract 6b 7.6).
func TestPutIntegrationURIShapes(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.method(id, root, "GET")
		put := func(typ apigatewayapi.IntegrationType, uri string) (*apigatewayapi.Integration, *awserr.Error) {
			return h.Invoke(h.Context(), apigatewayapi.OpPutIntegration, &apigatewayapi.PutIntegrationInput{
				RestApiId: id, ResourceId: root, HttpMethod: "GET", Type: typ, IntegrationHttpMethod: new("POST"), Uri: new(uri),
			})
		}
		fn := "arn:aws:lambda:" + region + ":" + account + ":function:fn"
		for _, uri := range []string{
			invokeURI(fn), invokeURI(fn + ":live"),
			"arn:aws:apigateway:" + region + ":lambda:path/2015-03-31/functions/arn:aws:lambda:" + region + ":" + account + ":function:${stageVariables.fn}/invocations",
		} {
			if out, err := put(apigatewayapi.IntegrationTypeAwsProxy, uri); err != nil || deref(out.Uri) != uri {
				t.Errorf("AWS_PROXY %s = %+v, %v; want it stored verbatim", uri, out, err)
			}
		}
		for _, uri := range []string{"arn:aws:apigateway:" + region + ":sqs:path/" + account + "/q", "arn:aws:apigateway:" + region + ":sqs:action/SendMessage"} {
			if _, err := put(apigatewayapi.IntegrationTypeAws, uri); err != nil {
				t.Errorf("AWS %s = %v, want accepted", uri, err)
			}
		}
		_, err := put(apigatewayapi.IntegrationTypeAws, "arn:aws:apigateway:"+region+":sqs:queue/q")
		wantText(t, err, badRequest, "AWS ARN for integration must contain path or action")
		_, err = put(apigatewayapi.IntegrationTypeAwsProxy, "arn:aws:apigateway:"+region+":firehose:path/2015-03-31/functions/"+fn+"/invocations")
		wantText(t, err, badRequest, "Integrations of type 'AWS_PROXY' currently only supports Lambda function and Firehose stream invocations.")
		for _, uri := range []string{"example.com/x", "ftp://example.com", "http://"} {
			_, err := put(apigatewayapi.IntegrationTypeHttpProxy, uri)
			wantText(t, err, badRequest, "Invalid HTTP endpoint specified for URI")
		}
	})
}

// A refused request changes nothing: a PutIntegration refusal leaves the
// method without an integration, and an UpdateIntegration whose later
// operation is refused leaves the earlier one unapplied (all or nothing,
// inferred, apigateway-rest.md 12.4).
func TestIntegrationRefusalsChangeNothing(t *testing.T) {
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id, root := h.api("t")
		h.method(id, root, "GET")
		bad := proxyInput(id, root, "GET", fnARN)
		bad.TimeoutInMillis = new(int32(10))
		wantError(t, fail(h, apigatewayapi.OpPutIntegration, bad), badRequest)
		wantText(t, fail(h, apigatewayapi.OpGetIntegration, &apigatewayapi.GetIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"}), notFound, "Invalid Integration identifier specified")
		h.proxy(id, root, "GET", fnARN)
		wantError(t, fail(h, apigatewayapi.OpUpdateIntegration, &apigatewayapi.UpdateIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", PatchOperations: []apigatewayapi.PatchOperation{
			op(apigatewayapi.OpReplace, "/timeoutInMillis", "1000"), op(apigatewayapi.OpReplace, "/uri", "not a uri"),
		}}), badRequest)
		got := must(h, apigatewayapi.OpGetIntegration, &apigatewayapi.GetIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"})
		if got.TimeoutInMillis != 29000 || deref(got.Uri) != invokeURI(fnARN) {
			t.Errorf("after a refused update = %+v, want the integration unchanged", got)
		}
	})
}

// UpdateIntegration refuses /type (not patchable) and /httpMethod on a
// MOCK integration, and answers NotImplemented for the members of VPC
// links, TLS and load balancer targets (apigateway-rest.md 3.8; the unit's
// conflict row 4). Templates and parameters are added and removed by key,
// the content type escaped.
func TestUpdateIntegrationPaths(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.method(id, root, "GET")
		must(h, apigatewayapi.OpPutIntegration, &apigatewayapi.PutIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", Type: apigatewayapi.IntegrationTypeMock})
		update := func(ops ...apigatewayapi.PatchOperation) (*apigatewayapi.Integration, *awserr.Error) {
			return h.Invoke(h.Context(), apigatewayapi.OpUpdateIntegration, &apigatewayapi.UpdateIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", PatchOperations: ops})
		}
		_, err := update(op(apigatewayapi.OpReplace, "/httpMethod", "POST"))
		wantError(t, err, badRequest)
		for path, member := range map[string]string{"/connectionId": "connectionId", "/tlsConfig/insecureSkipVerification": "tlsConfig", "/integrationTarget": "integrationTarget"} {
			_, err := update(op(apigatewayapi.OpReplace, path, "x"))
			wantNotImplemented(t, err, member)
		}
		_, err = update(op(apigatewayapi.OpReplace, "/connectionType", "VPC_LINK"))
		wantNotImplemented(t, err, "VPC_LINK")
		out, err := update(
			op(apigatewayapi.OpAdd, "/requestTemplates/application~1json", `{"statusCode": 200}`),
			op(apigatewayapi.OpAdd, "/requestParameters/integration.request.header.x", "'v'"),
			op(apigatewayapi.OpReplace, "/passthroughBehavior", "NEVER"),
		)
		if err != nil || out.RequestTemplates["application/json"] != `{"statusCode": 200}` || out.RequestParameters["integration.request.header.x"] != "'v'" ||
			deref(out.PassthroughBehavior) != "NEVER" || out.HttpMethod != nil {
			t.Fatalf("UpdateIntegration = %+v, %v; want the template, the parameter, NEVER and still no httpMethod", out, err)
		}
		out, err = update(op(apigatewayapi.OpRemove, "/requestTemplates/application~1json"), op(apigatewayapi.OpRemove, "/requestParameters/integration.request.header.x"))
		if err != nil || len(out.RequestTemplates) != 0 || len(out.RequestParameters) != 0 {
			t.Errorf("UpdateIntegration remove = %+v, %v; want no template and no parameter", out, err)
		}
	})
}

// GetMethod embeds the integration with its responses, and
// DeleteIntegration removes both; a response of a method with no
// integration answers the integration's NotFound (aws-recorded texts,
// apigateway-rest.md 2.3, 2.4).
func TestIntegrationResponsesLiveWithTheIntegration(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.method(id, root, "GET")
		wantText(t, fail(h, apigatewayapi.OpPutIntegrationResponse, &apigatewayapi.PutIntegrationResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200"}),
			notFound, "Invalid Integration identifier specified")
		must(h, apigatewayapi.OpPutIntegration, &apigatewayapi.PutIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", Type: apigatewayapi.IntegrationTypeMock})
		must(h, apigatewayapi.OpPutIntegrationResponse, &apigatewayapi.PutIntegrationResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200"})
		must(h, apigatewayapi.OpPutIntegrationResponse, &apigatewayapi.PutIntegrationResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "400", SelectionPattern: new("4\\d{2}")})
		m := must(h, apigatewayapi.OpGetMethod, &apigatewayapi.GetMethodInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"})
		if m.MethodIntegration == nil || !slices.Equal(sortedKeys(m.MethodIntegration.IntegrationResponses), []string{"200", "400"}) {
			t.Fatalf("GetMethod methodIntegration = %+v, want integrationResponses 200 and 400", m.MethodIntegration)
		}
		if m.MethodIntegration.IntegrationResponses["200"].SelectionPattern != nil {
			t.Errorf("200 selectionPattern = %q, want none (not sent)", *m.MethodIntegration.IntegrationResponses["200"].SelectionPattern)
		}
		must(h, apigatewayapi.OpDeleteIntegration, &apigatewayapi.DeleteIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"})
		wantText(t, fail(h, apigatewayapi.OpGetIntegrationResponse, &apigatewayapi.GetIntegrationResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200"}),
			notFound, "Invalid Integration identifier specified")
		wantText(t, fail(h, apigatewayapi.OpDeleteIntegration, &apigatewayapi.DeleteIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"}),
			notFound, "Invalid Integration identifier specified")
	})
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Stored integration members survive an export and import (contract 6b
// 7.1).
func TestIntegrationRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		h.method(id, root, "GET")
		in := &apigatewayapi.PutIntegrationInput{
			RestApiId: id, ResourceId: root, HttpMethod: "GET", Type: apigatewayapi.IntegrationTypeHttpProxy, IntegrationHttpMethod: new("GET"),
			Uri: new("http://example.com/{proxy}"), Credentials: new("arn:aws:iam::" + account + ":role/r"), RequestTemplates: map[string]string{"application/json": ""},
			ContentHandling: new(apigatewayapi.ContentHandlingStrategyConvertToBinary), CacheKeyParameters: []string{"method.request.path.proxy"},
		}
		want := must(h, apigatewayapi.OpPutIntegration, in)
		must(h, apigatewayapi.OpPutIntegrationResponse, &apigatewayapi.PutIntegrationResponseInput{RestApiId: id, ResourceId: root, HttpMethod: "GET", StatusCode: "200", SelectionPattern: new("")})
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		got := must(h, apigatewayapi.OpGetIntegration, &apigatewayapi.GetIntegrationInput{RestApiId: id, ResourceId: root, HttpMethod: "GET"})
		if deref(got.Uri) != deref(want.Uri) || deref(got.Credentials) != deref(in.Credentials) || deref(got.ContentHandling) != *in.ContentHandling ||
			!slices.Equal(got.CacheKeyParameters, in.CacheKeyParameters) || got.RequestTemplates == nil || deref(got.ConnectionType) != apigatewayapi.ConnectionTypeInternet {
			t.Errorf("GetIntegration after an import = %+v, want %+v", got, want)
		}
		if r, ok := got.IntegrationResponses["200"]; !ok || r.SelectionPattern == nil || *r.SelectionPattern != "" {
			t.Errorf("integrationResponses after an import = %+v, want 200 with selectionPattern \"\"", got.IntegrationResponses)
		}
	})
}
