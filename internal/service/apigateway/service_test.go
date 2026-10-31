package apigateway_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// baseNote heads every bound control-plane operation's coverage note
// (spec 6b "Coverage marks": the X-Amzn-Errortype value and the id shapes
// are inferred, apigateway-rest.md 2.4, 3.10).
const baseNote = "the X-Amzn-Errortype value and generated ids are inferred (UNVERIFIED, APIGW1-01, APIGW1-02)"

func partial(note string) registry.BindCoverage {
	if note == "" {
		return registry.Partial{Note: baseNote}
	}
	return registry.Partial{Note: baseNote + "; " + note}
}

// The REST API operations are bound with the spec's marks and notes as
// amended by the test-owner rulings of 2026-10-31 (disputes.md 6b: each
// inferred text, order or deviation the code emits is named); every other
// v1 operation stays unbound and answers 501 (CLAUDE.md invariant 3). No
// operation is Full.
func TestCoverageMarks(t *testing.T) {
	const (
		restAPINote        = "cloneFrom answers NotImplemented; policy is stored and returned escaped, and under iam.enforce=enforce an invocation of an API with a policy answers 501; binaryMediaTypes, disableExecuteApiEndpoint and endpointConfiguration PRIVATE are stored and returned, and an invocation of an API that has them answers 501; vpcEndpointIds, securityPolicy, endpointAccessMode, minimumCompressionSize and apiKeySource are stored without effect; an EDGE API is served as a REGIONAL one (no CloudFront headers in the event); a second API with the same name is allowed (inferred, APIGW1-14); an endpointConfiguration types value outside the enum answers a ValidationException naming the list member, where AWS names the whole list (recorded, test_apigateway_api.snapshot.json:L4693-L4703); the UpdateRestApi texts for an invalid value, a second or removed endpoint type and PRIVATE on ipv4 are inferred; a stored policy keeps a one-element Resource list as a list, where AWS rewrites it to a string (recorded, test_apigateway_integrations.snapshot.json:L45-L77)"
		creationOrder      = "items in creation order (inferred)"
		methodNote         = "AWS_IAM, CUSTOM and COGNITO_USER_POOLS methods answer 501 at invoke; apiKeyRequired methods answer 403 because API keys cannot be created; requestValidatorId and requestModels name only the seeded Empty and Error models until validators and models are bound; an authorizationType outside NONE, AWS_IAM, CUSTOM and COGNITO_USER_POOLS and UpdateMethod replace /resourceId naming another resource answer NotImplemented"
		methodResponseNote = "responseModels name only the seeded Empty and Error models (text inferred)"
		// The generated template maps cannot carry null (disputes.md 6b
		// ticket 6 ruling), so "" is returned and declared.
		emptyRequest    = `an empty-string request template is returned as "" (AWS's answer is UNVERIFIED, APIGW1-13)`
		emptyResponse   = `an empty-string response template is returned as "", where AWS returns null (recorded, apigateway-rest.md finding 6)`
		emptyTemplates  = emptyRequest + "; " + emptyResponse
		integrationNote = "AWS_PROXY to Lambda and MOCK run; HTTP, HTTP_PROXY and AWS are stored and answer 501 at invoke; credentials, a stage variable in the uri, responseTransferMode STREAM and contentHandling are stored and returned, and an invocation that reaches them answers 501; connectionType VPC_LINK, connectionId, tlsConfig, integrationTarget and an AWS_PROXY Firehose stream uri answer NotImplemented; requestParameters, cacheNamespace and cacheKeyParameters are stored without effect; a boolean requestParameters value answers SerializationException with understudy's decode text, not AWS's recorded one; the texts for a timeout out of range, an unknown passthroughBehavior and a patch value of the wrong type are inferred; " + emptyRequest
		responseNote    = "selectionPattern runs as Go RE2; a Java-only construct (lookaround, backreference, possessive quantifier) answers 501 at invoke naming the pattern; only static responseParameters and literal templates run, anything else answers 501; contentHandling is stored and an invocation that selects the response answers 501; " + emptyResponse
		resourcesNote   = "items in creation order, root first (inferred, APIGW1-04)"
		deploymentNote  = "canarySettings answers NotImplemented; cacheClusterEnabled, cacheClusterSize and tracingEnabled are stored without effect; the texts for deploying an API with no methods or a method with no integration are inferred (APIGW1-05)"
		deploymentText  = "the missing-deployment text is inferred"
		stageTexts      = "the duplicate-stage and missing-stage texts are inferred (APIGW1-06)"
		stageNote       = "cache settings, methodSettings, accessLogSettings, clientCertificateId, tracingEnabled and webAclArn are stored and echoed without effect; canarySettings answers NotImplemented; " + stageTexts + "; documentationVersion answers NotImplemented while documentation versions are unbound"
		tagsNote        = "the texts for a malformed or unknown resourceArn and the tag key and value lengths are inferred"
		invokeNote      = "AWS_PROXY (payload 1.0) and MOCK with literal templates; mapping templates with VTL answer 501; request validation, authorizers, API keys, caching and throttling are not applied; AWS_IAM methods answer 501; apiKeyRequired methods answer 403; binaryMediaTypes in the stage's deployment, a disabled default endpoint and PRIVATE APIs answer 501; a resource policy is not evaluated: under iam.enforce=advise each invocation records an advisory, under enforce it answers 501; HTTP, HTTP_PROXY and AWS integrations, integration credentials, stage-variable uris, responseTransferMode STREAM and contentHandling answer 501; inferred, UNVERIFIED: unknown stage, ANY against explicit methods, HEAD (APIGW1-07, -08), the 415 text (APIGW1-09), timeout (APIGW1-10), empty-body event (APIGW1-16); inferred: a MOCK template with no statusCode or one outside 100..599 selects the default integration response, and several response templates none of which matches Accept or application/json answer 500; header names reach the event in canonical form (Test-Header), not as the client wrote them"
	)
	want := map[string]registry.BindCoverage{
		"CreateRestApi": partial(restAPINote), "UpdateRestApi": partial(restAPINote), "GetRestApis": partial(creationOrder),
		"PutMethod": partial(methodNote), "UpdateMethod": partial(methodNote), "GetMethod": partial(emptyTemplates),
		"PutMethodResponse": partial(methodResponseNote), "UpdateMethodResponse": partial(methodResponseNote),
		"PutIntegration": partial(integrationNote), "UpdateIntegration": partial(integrationNote), "GetIntegration": partial(emptyTemplates),
		"PutIntegrationResponse": partial(responseNote), "UpdateIntegrationResponse": partial(responseNote), "GetIntegrationResponse": partial(emptyResponse),
		"GetResources":     partial(resourcesNote + "; " + emptyTemplates),
		"CreateDeployment": partial(deploymentNote), "GetDeployments": partial(creationOrder),
		"GetDeployment": partial(deploymentText), "UpdateDeployment": partial(deploymentText), "DeleteDeployment": partial(deploymentText),
		"CreateStage": partial(stageNote), "UpdateStage": partial(stageNote + "; a new method setting starts from the */* one (inferred)"),
		"GetStage": partial(stageTexts), "DeleteStage": partial(stageTexts), "GetStages": partial(creationOrder),
		"TagResource": partial(tagsNote), "UntagResource": partial(tagsNote), "GetTags": partial(tagsNote),
		"InvokeRestApi": registry.Partial{Note: invokeNote},
	}
	for _, name := range []string{
		"GetRestApi", "DeleteRestApi", "CreateResource", "GetResource", "UpdateResource", "DeleteResource",
		"DeleteMethod", "GetMethodResponse", "DeleteMethodResponse",
		"DeleteIntegration", "DeleteIntegrationResponse",
	} {
		want[name] = partial("")
	}
	run(t, func(h *harness) {
		for _, op := range apigatewayapi.Service().Operations {
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
			"PutRestApi":     errOf(h.Invoke(h.Context(), apigatewayapi.OpPutRestApi, &apigatewayapi.PutRestApiInput{RestApiId: "a1b2c3d4e5", Body: []byte("{}")})),
			"ImportRestApi":  errOf(h.Invoke(h.Context(), apigatewayapi.OpImportRestApi, &apigatewayapi.ImportRestApiInput{Body: []byte("{}")})),
			"GetAccount":     errOf(h.Invoke(h.Context(), apigatewayapi.OpGetAccount, &apigatewayapi.GetAccountInput{})),
			"CreateApiKey":   errOf(h.Invoke(h.Context(), apigatewayapi.OpCreateApiKey, &apigatewayapi.CreateApiKeyInput{})),
			"GetAuthorizers": errOf(h.Invoke(h.Context(), apigatewayapi.OpGetAuthorizers, &apigatewayapi.GetAuthorizersInput{RestApiId: "a1b2c3d4e5"})),
		} {
			if err == nil || err.ID.Status != http.StatusNotImplemented {
				t.Errorf("%s = %v, want 501", name, err)
			}
		}
	})
}

// The service is apigateway in the registry, a stateful service whose
// admin view lists one resource per REST API under its ARN
// arn:aws:apigateway:<region>::/restapis/<id> (apigateway-rest.md 3.9)
// and the scopes that hold APIs.
func TestResourcesAndScopes(t *testing.T) {
	run(t, func(h *harness) {
		if h.svc.Name() != ref.ServiceID("apigateway") {
			t.Errorf("Name() = %q, want apigateway", h.svc.Name())
		}
		if got := h.svc.Resources(h.Scope); len(got) != 0 {
			t.Errorf("Resources before any API = %+v, want none", got)
		}
		id, _ := h.api("listed")
		got := h.svc.Resources(h.Scope)
		if len(got) != 1 || got[0].ARN.String() != apiARN(id) || got[0].Summary == "" {
			t.Errorf("Resources = %+v, want one at %s with a summary", got, apiARN(id))
		}
		if scopes := h.svc.Scopes(); !slices.Contains(scopes, h.Scope) {
			t.Errorf("Scopes() = %v, want it to hold %v", scopes, h.Scope)
		}
		if !h.svc.OwnsExecuteAPI(id) || h.svc.OwnsExecuteAPI("zzzzzzzzzz") {
			t.Errorf("OwnsExecuteAPI(%s) = %v, OwnsExecuteAPI(zzzzzzzzzz) = %v; want true, false", id, h.svc.OwnsExecuteAPI(id), h.svc.OwnsExecuteAPI("zzzzzzzzzz"))
		}
	})
}
