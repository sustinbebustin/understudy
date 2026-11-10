package apigateway

import (
	"net/http"
	"strings"

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
	// PatternFullMatch requires a selectionPattern to match the whole
	// status text; false finds it anywhere.
	PatternFullMatch bool
	// PatternOrder is the order patterned integration responses are tried
	// in, the default last.
	PatternOrder PatternOrder
}

// PatternOrder is an order of selection patterns. APIGW1-12 ruled out
// ascending status and creation order, and fits both of these
// (apigateway-rest.md 6.7).
type PatternOrder uint8

// The pattern orders.
const (
	// PatternsSortingLastFirst tries the pattern that sorts last first.
	PatternsSortingLastFirst PatternOrder = iota + 1
	// PatternsLongestFirst tries the longest pattern first.
	PatternsLongestFirst
)

// String names the order as the coverage report shows it.
func (o PatternOrder) String() string {
	switch o {
	case PatternsSortingLastFirst:
		return "sorting-last-first"
	case PatternsLongestFirst:
		return "longest-first"
	}
	return "PatternOrder(?)"
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
// be created (apigateway-rest.md 12.2); the body is inferred.
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

// recorded is the Settles of a switch the named cases recorded: the
// replayer does not route execute-api requests, so no scenario replays
// them.
func recorded(cases string) fidelity.Settles {
	return fidelity.NoScenario{Reason: cases + " recorded 2026-11-03 (testdata/parity/apigateway/APIGW1); the replayer does not route execute-api requests"}
}

// APIStatusSwitch is apigateway.restapi.api-status (3.1, finding 2):
// AVAILABLE throughout, as APIGW1-03 recorded.
func APIStatusSwitch() fidelity.Switch[APIStatus] {
	return fidelity.Switch[APIStatus]{
		Name:    "apigateway.restapi.api-status",
		Value:   APIStatus{AfterCreate: apigatewayapi.ApiStatusAvailable, AfterUpdate: apigatewayapi.ApiStatusAvailable},
		Label:   evidence.AWSRecorded,
		Ref:     researchRef + "31-restapi",
		Record:  "APIGW1-03",
		Ops:     []string{"apigateway.CreateRestApi", "apigateway.UpdateRestApi", "apigateway.GetRestApi", "apigateway.GetRestApis"},
		Settles: recorded("APIGW1-03"),
	}
}

// RoutingSwitch is apigateway.invoke.routing (4.4, 4.7). APIGW1-07 and
// APIGW1-08 recorded the unknown-stage answer and the method choices; the
// answer to an unknown API id cannot be recorded (the host does not
// resolve), so the switch stays inferred.
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
		Settles: fidelity.NoScenario{Reason: "unrecorded: the answer to an unknown API id, whose host does not resolve (APIGW1-07 recorded the rest)"},
	}
}

// PermissionSwitch is apigateway.lambda.permission (5.4, 3.10). APIGW1-15
// recorded the denial; its permission named the API with a wildcard, so
// the SourceARN form stays inferred.
func PermissionSwitch() fidelity.Switch[Permission] {
	return fidelity.Switch[Permission]{
		Name: "apigateway.lambda.permission",
		Value: Permission{
			SourceARN: "<api-id>/<stage>/<METHOD>/<resource path template without its leading slash>; ANY as written for an ANY method; an empty last segment for the root resource",
			Denial:    GatewayAnswer{Status: http.StatusInternalServerError, Body: `{"message": "Internal server error"}`, ErrorType: "InternalServerErrorException"},
		},
		Label:   evidence.Inferred,
		Ref:     researchRef + "54-how-api-gateway-invokes-the-function",
		Record:  "APIGW1-15",
		Ops:     []string{invokeRestAPI},
		Settles: fidelity.NoScenario{Reason: "unrecorded: the SourceARN form; APIGW1-15 granted <api-id>/* and recorded only the denial"},
	}
}

// MockSelectionSwitch is apigateway.mock.selection (6.2, 6.7). APIGW1-09,
// APIGW1-11 and APIGW1-12 recorded on 2026-11-03 the 415 answer, media
// type essence, the response template (which no longer needs a choice:
// responseTemplate) and full-match anchoring; of two matching patterns
// AWS took the one that is both longer and sorts last, so PatternOrder
// stays inferred.
func MockSelectionSwitch() fidelity.Switch[MockSelection] {
	return fidelity.Switch[MockSelection]{
		Name: "apigateway.mock.selection",
		Value: MockSelection{
			UnsupportedMediaType: GatewayAnswer{Status: http.StatusUnsupportedMediaType, Body: `{"message": "Unsupported Media Type"}`, ErrorType: "BadRequestException"},
			MediaTypeEssence:     true,
			PatternFullMatch:     true,
			PatternOrder:         PatternsSortingLastFirst,
		},
		Label:   evidence.Inferred,
		Ref:     researchRef + "62-passthrough-behavior-and-415",
		Record:  "APIGW1-09, APIGW1-11, APIGW1-12",
		Ops:     []string{invokeRestAPI},
		Settles: fidelity.NoScenario{Reason: "unrecorded: which of 'longest first' and 'sorting last first' AWS applies; APIGW1-12's three ties fit both (apigateway-rest.md 6.7)"},
	}
}

// ProxyUnrecordedSwitch is apigateway.proxy.unrecorded (5.1, 5.3), named
// before APIGW1-10 and APIGW1-16 recorded every part of it.
func ProxyUnrecordedSwitch() fidelity.Switch[ProxyUnrecorded] {
	return fidelity.Switch[ProxyUnrecorded]{
		Name: "apigateway.proxy.unrecorded",
		Value: ProxyUnrecorded{
			Timeout:        GatewayAnswer{Status: http.StatusGatewayTimeout, Body: `{"message": "Endpoint request timed out"}`, ErrorType: "InternalServerErrorException"},
			BodylessNull:   true,
			BodylessBase64: false,
		},
		Label:   evidence.AWSRecorded,
		Ref:     researchRef + "53-failures",
		Record:  "APIGW1-10, APIGW1-16",
		Ops:     []string{invokeRestAPI},
		Settles: recorded("APIGW1-10 and APIGW1-16"),
	}
}

// Switches lists the service's fidelity switches (apigateway-rest.md
// 12.3), each labelled by the evidence it now has.
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
// that resource path and method serve (switch apigateway.lambda.permission),
// in the aws partition: method is the method resource that serves (ANY as
// written), resourcePath the resource's template, whose leading slash is
// dropped, so the root resource gives an empty last segment
// (apigateway-rest.md 5.4, 3.10).
func SourceARN(region, account, apiID, stage, method, resourcePath string) string {
	return "arn:aws:execute-api:" + region + ":" + account + ":" + apiID + "/" + stage + "/" + method + "/" + strings.TrimPrefix(resourcePath, "/")
}
