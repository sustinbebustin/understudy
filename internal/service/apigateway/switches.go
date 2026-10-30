package apigateway

import (
	"net/http"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/service/apigateway/resource"
)

// GatewayAnswer is a response API Gateway writes itself
// (apigateway-rest.md 4.7): Status, the exact Body bytes, and an
// x-amzn-ErrorType header when ErrorType is not empty. Bodies are kept as
// bytes because the recorded ones differ in spacing.
type GatewayAnswer struct {
	Status    int
	Body      string
	ErrorType string
}

// APIStatus is the value of apigateway.restapi.api-status: the apiStatus
// a REST API reports after CreateRestApi and UpdateRestApi
// (apigateway-rest.md 3.1, finding 2).
type APIStatus struct {
	AfterCreate apigatewayapi.ApiStatus
	AfterUpdate apigatewayapi.ApiStatus
}

// Routing is the value of apigateway.invoke.routing: the answers to a
// request that names no stage or no API, and the method choices of the
// resource matcher (apigateway-rest.md 4.4, 4.7).
type Routing struct {
	// UnknownStage answers a first path segment that names no stage.
	UnknownStage GatewayAnswer
	// UnknownAPI answers an id no REST API holds, or a host naming another
	// region than the API's.
	UnknownAPI GatewayAnswer
	Methods    resource.Methods
}

// Permission is the value of apigateway.lambda.permission: the source ARN
// the data plane presents to a function's resource policy, and the answer
// when the policy does not allow the invocation (apigateway-rest.md 5.4).
type Permission struct {
	// SourceARN describes the resource part of aws:SourceArn after
	// arn:<partition>:execute-api:<region>:<account>:; SourceARN the
	// function builds it.
	SourceARN string
	Denial    GatewayAnswer
}

// MockSelection is the value of apigateway.mock.selection: how a MOCK
// integration picks its request template, integration response and
// response template (apigateway-rest.md 6.2, 6.7).
type MockSelection struct {
	// UnsupportedMediaType answers the 415 cells of the passthrough table.
	UnsupportedMediaType GatewayAnswer
	// MediaTypeEssence compares type/subtype only, case-insensitively;
	// false compares the whole header value byte for byte.
	MediaTypeEssence bool
	// TemplateByAccept picks the response template by the first Accept
	// media type, else application/json, else the only one; false takes
	// application/json, else the only one.
	TemplateByAccept bool
	// PatternFullMatch requires a selectionPattern to match the whole
	// status text; false finds it anywhere.
	PatternFullMatch bool
	// AscendingStatus tries patterned integration responses in ascending
	// status order, the default last; false in creation order.
	AscendingStatus bool
}

// ProxyUnrecorded is the value of apigateway.proxy.unrecorded: the Lambda
// proxy answers and event members no recording fixes (apigateway-rest.md
// 5.1, 5.3).
type ProxyUnrecorded struct {
	// Timeout answers an invocation that outlasts timeoutInMillis.
	Timeout GatewayAnswer
	// BodylessNull gives a request without a body the event body null;
	// false gives "".
	BodylessNull bool
	// BodylessBase64 is the isBase64Encoded of a request without a body.
	BodylessBase64 bool
}

// Fixed answers the data plane writes, which are not switches.

// MissingAuthenticationToken answers a request no resource and method
// serve (apigateway-rest.md finding 3, recorded).
func MissingAuthenticationToken() GatewayAnswer {
	return GatewayAnswer{Status: http.StatusForbidden, Body: `{"message":"Missing Authentication Token"}`, ErrorType: "MissingAuthenticationTokenException"}
}

// ProxyFailure answers a function error or malformed proxy output
// (apigateway-rest.md 5.2, 5.3, recorded).
func ProxyFailure() GatewayAnswer {
	return GatewayAnswer{Status: http.StatusBadGateway, Body: `{"message": "Internal server error"}`}
}

// ConfigurationError answers an integration understudy can tell AWS would
// refuse at invoke, such as AWS_PROXY with integrationHttpMethod GET
// (apigateway-rest.md 4.7, finding 10, recorded).
func ConfigurationError() GatewayAnswer {
	return GatewayAnswer{Status: http.StatusInternalServerError, Body: `{"message":"Internal server error"}`, ErrorType: "InternalServerErrorException"}
}

// RequestTooLarge answers a body over MaxBody; the text is documented, its
// body form inferred (apigateway-rest.md 2.6, 4.7).
func RequestTooLarge() GatewayAnswer {
	return GatewayAnswer{Status: http.StatusRequestEntityTooLarge, Body: `{"message":"HTTP content length exceeded 10485760 bytes"}`}
}

// APIKeyRequired answers a method with apiKeyRequired, since no API key can
// be created; the status follows the spec, the body is inferred.
func APIKeyRequired() GatewayAnswer {
	return GatewayAnswer{Status: http.StatusForbidden, Body: `{"message":"Forbidden"}`, ErrorType: "ForbiddenException"}
}

// EventTooLarge answers an event over Lambda's 6 MB synchronous limit as a
// refused invocation (inferred).
func EventTooLarge() GatewayAnswer { return ProxyFailure() }

// MaxBody is the largest request body the data plane accepts
// (apigateway-rest.md 2.6).
const MaxBody = 10_485_760

// researchRef is the research note every switch points into.
const researchRef = "docs/research/apigateway-rest.md#"

// invokeRestAPI is the data plane's operation id.
const invokeRestAPI = "apigateway.InvokeRestApi"

// unrecorded is the Settles of a switch the named recording cases settle.
func unrecorded(cases string) fidelity.Settles {
	return fidelity.NoScenario{Reason: "unrecorded: " + cases + " (apigateway-rest.md 13) records it"}
}

// APIStatusSwitch is apigateway.restapi.api-status (3.1, finding 2).
func APIStatusSwitch() fidelity.Switch[APIStatus] {
	return fidelity.Switch[APIStatus]{
		Name:    "apigateway.restapi.api-status",
		Value:   APIStatus{AfterCreate: apigatewayapi.ApiStatusAvailable, AfterUpdate: apigatewayapi.ApiStatusAvailable},
		Label:   evidence.Inferred,
		Ref:     researchRef + "31-restapi",
		Record:  "APIGW1-03",
		Ops:     []string{"apigateway.CreateRestApi", "apigateway.UpdateRestApi", "apigateway.GetRestApi", "apigateway.GetRestApis"},
		Settles: unrecorded("APIGW1-03"),
	}
}

// RoutingSwitch is apigateway.invoke.routing (4.4, 4.7).
func RoutingSwitch() fidelity.Switch[Routing] {
	forbidden := GatewayAnswer{Status: http.StatusForbidden, Body: `{"message":"Forbidden"}`, ErrorType: "ForbiddenException"}
	return fidelity.Switch[Routing]{
		Name: "apigateway.invoke.routing",
		Value: Routing{
			UnknownStage: forbidden,
			UnknownAPI:   forbidden,
			Methods:      resource.Methods{ExplicitBeforeAny: true, HeadMatchesGet: false},
		},
		Label:   evidence.Inferred,
		Ref:     researchRef + "47-gateway-responses-and-error-bodies",
		Record:  "APIGW1-07, APIGW1-08",
		Ops:     []string{invokeRestAPI},
		Settles: unrecorded("APIGW1-07 and APIGW1-08"),
	}
}

// PermissionSwitch is apigateway.lambda.permission (5.4, 3.10).
func PermissionSwitch() fidelity.Switch[Permission] {
	return fidelity.Switch[Permission]{
		Name: "apigateway.lambda.permission",
		Value: Permission{
			SourceARN: "<api-id>/<stage>/<METHOD>/<resource path template without its leading slash>; ANY as written for an ANY method; an empty last segment for the root resource",
			Denial:    GatewayAnswer{Status: http.StatusInternalServerError, Body: `{"message": "Internal server error"}`},
		},
		Label:   evidence.Inferred,
		Ref:     researchRef + "54-how-api-gateway-invokes-the-function",
		Record:  "APIGW1-15",
		Ops:     []string{invokeRestAPI},
		Settles: unrecorded("APIGW1-15"),
	}
}

// MockSelectionSwitch is apigateway.mock.selection (6.2, 6.7).
func MockSelectionSwitch() fidelity.Switch[MockSelection] {
	return fidelity.Switch[MockSelection]{
		Name: "apigateway.mock.selection",
		Value: MockSelection{
			UnsupportedMediaType: GatewayAnswer{Status: http.StatusUnsupportedMediaType, Body: `{"message":"Unsupported Media Type"}`},
			MediaTypeEssence:     true,
			TemplateByAccept:     true,
			PatternFullMatch:     true,
			AscendingStatus:      true,
		},
		Label:   evidence.Inferred,
		Ref:     researchRef + "62-passthrough-behavior-and-415",
		Record:  "APIGW1-09, APIGW1-11, APIGW1-12",
		Ops:     []string{invokeRestAPI},
		Settles: unrecorded("APIGW1-09, APIGW1-11 and APIGW1-12"),
	}
}

// ProxyUnrecordedSwitch is apigateway.proxy.unrecorded (5.1, 5.3).
func ProxyUnrecordedSwitch() fidelity.Switch[ProxyUnrecorded] {
	return fidelity.Switch[ProxyUnrecorded]{
		Name: "apigateway.proxy.unrecorded",
		Value: ProxyUnrecorded{
			Timeout:        GatewayAnswer{Status: http.StatusGatewayTimeout, Body: `{"message": "Endpoint request timed out"}`},
			BodylessNull:   true,
			BodylessBase64: false,
		},
		Label:   evidence.Inferred,
		Ref:     researchRef + "53-failures",
		Record:  "APIGW1-10, APIGW1-16",
		Ops:     []string{invokeRestAPI},
		Settles: unrecorded("APIGW1-10 and APIGW1-16"),
	}
}

// Switches lists the behaviors the service chose without AWS-recorded
// evidence, in the order of the spec's switch table.
func (*Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{
		APIStatusSwitch().Entry(),
		RoutingSwitch().Entry(),
		PermissionSwitch().Entry(),
		MockSelectionSwitch().Entry(),
		ProxyUnrecordedSwitch().Entry(),
	}
}

// SourceARN is the aws:SourceArn the data plane presents for a request
// that resource path and method serve (switch apigateway.lambda.permission).
// It is not implemented yet and returns "".
func SourceARN(_, _, _, _, _, _ string) string { return "" }
