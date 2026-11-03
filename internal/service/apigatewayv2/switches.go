package apigatewayv2

import (
	"net/http"

	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2/route"
)

// GatewayAnswer is a response API Gateway writes itself
// (apigateway-http.md 4.5): Status with the body {"message":"<Message>"}.
type GatewayAnswer struct {
	Status  int
	Message string
}

// FailureAnswers is the value of apigatewayv2.lambda.failure-answers: how
// the data plane answers a Lambda proxy integration that fails, and the
// size limits it answers TooLarge for (apigateway-http.md 4.7, 5.6).
type FailureAnswers struct {
	// FunctionError answers a function error, malformed function output,
	// or an invocation Lambda refuses.
	FunctionError GatewayAnswer
	// Timeout answers an invocation that outlasts the integration's
	// TimeoutInMillis.
	Timeout GatewayAnswer
	// TooLarge answers a request body over MaxBody bytes, or an event over
	// MaxEvent bytes.
	TooLarge GatewayAnswer
	MaxBody  int
	// MaxEvent is Lambda's synchronous request limit (lambda-api.md 8.2).
	MaxEvent int
}

// Permission is the value of apigatewayv2.lambda.permission: the source
// ARN the data plane presents to a function's resource policy, and the
// answer when the policy does not allow the invocation
// (apigateway-http.md 5.5, 8.4).
type Permission struct {
	// SourceARN describes the resource part of aws:SourceArn after
	// arn:<partition>:execute-api:<region>:<account>:.
	SourceARN string
	Denial    GatewayAnswer
}

// RouteMatching is the value of apigatewayv2.route.matching: the matcher's
// choices, and the answer to a matched route without a target
// (apigateway-http.md 3.2, 4.4).
type RouteMatching struct {
	route.Matching
	NoTarget GatewayAnswer
}

// DeploymentRule is how the control plane meets one of the deployment
// cases apigateway-http.md 3.5 leaves open.
type DeploymentRule uint8

// The deployment rules.
const (
	// Refused answers BadRequestException, 400.
	Refused DeploymentRule = iota + 1
	// Allowed lets the call succeed.
	Allowed
)

// String names the rule as the coverage report shows it.
func (r DeploymentRule) String() string {
	switch r {
	case Refused:
		return "refused"
	case Allowed:
		return "allowed"
	}
	return "DeploymentRule(?)"
}

// Deployments are the three deployment cases of apigateway-http.md 3.5.
type Deployments struct {
	// DeleteInUse is DeleteDeployment of a deployment a stage serves: the
	// REST rule (apigateway-rest.md 2.4).
	DeleteInUse DeploymentRule
	// EmptyAPI is CreateDeployment of an API with no routes; a stage
	// serving the deployment answers NoRoute.
	EmptyAPI DeploymentRule
	// ExplicitOnAutoDeploy is a DeploymentId given to CreateStage or
	// UpdateStage for a stage with AutoDeploy, or CreateDeployment naming
	// such a stage ("Can't be updated", 3.4).
	ExplicitOnAutoDeploy DeploymentRule
}

// FrontAnswers is the value of apigatewayv2.front.answers: stage
// selection and the answers the gateway gives before any route runs
// (apigateway-http.md 3.1, 3.5, 4.2, 4.5).
type FrontAnswers struct {
	// StageSegmentWins selects a named stage by a first path segment equal
	// to its name, even where the $default stage has a route for the path.
	StageSegmentWins bool
	// UnknownAPI answers an API id no account holds, or a host naming
	// another region than the API's.
	UnknownAPI GatewayAnswer
	// DisabledEndpoint answers an API with DisableExecuteApiEndpoint, from
	// the call that sets it.
	DisabledEndpoint GatewayAnswer
	// NoStage answers a request no stage serves; NoDeployment one to a
	// stage without a deployment; NoRoute one no route of the deployment
	// matches.
	NoStage      GatewayAnswer
	NoDeployment GatewayAnswer
	NoRoute      GatewayAnswer
	// ContentType is the Content-Type of every GatewayAnswer.
	ContentType string
	Deployments Deployments
}

// Pick is which value of a repeated header or query parameter a payload
// format 1.0 single-value map keeps.
type Pick uint8

// The picks.
const (
	First Pick = iota + 1
	Last
)

// String names the pick as the coverage report shows it.
func (p Pick) String() string {
	switch p {
	case First:
		return "first"
	case Last:
		return "last"
	}
	return "Pick(?)"
}

// PayloadChoices is the value of apigatewayv2.payload.unrecorded: what the
// payload formats leave unrecorded (apigateway-http.md 5.1-5.4, 12 items 3,
// 6 and 10).
type PayloadChoices struct {
	// BodylessBase64 is isBase64Encoded in the 2.0 event of a request
	// without a body.
	BodylessBase64 bool
	// RequestIDIsHeader makes requestContext.requestId the apigw-requestid
	// header's value.
	RequestIDIsHeader bool
	// DefaultContentType is the Content-Type of a structured 2.0 response
	// or a 1.0 response that sets none.
	DefaultContentType string
	// V2MultiValueHeaders applies multiValueHeaders in a 2.0 response;
	// false ignores it.
	V2MultiValueHeaders bool
	// V1Header and V1Query are what the 1.0 headers and
	// queryStringParameters maps keep of a repeated name.
	V1Header Pick
	V1Query  Pick
}

// researchRef is the research note every switch points into.
const researchRef = "docs/research/apigateway-http.md#"

// invokeAPI is the data plane's operation id, which every switch governs.
const invokeAPI = "apigatewayv2.InvokeApi"

// unrecorded is the Settles of a switch the named recording cases settle.
func unrecorded(cases string) fidelity.Settles {
	return fidelity.NoScenario{Reason: "unrecorded: " + cases + " (apigateway-http.md 14) records it"}
}

// recorded is the Settles of a switch the named recording cases recorded:
// their fixtures stay evidence only, since the replayer routes neither
// restJson1 nor execute-api requests (testdata/README.md, Replay
// scenarios).
func recorded(cases string) fidelity.Settles {
	return fidelity.NoScenario{Reason: cases + " recorded 2026-11-03 (testdata/parity/apigatewayv2); the replayer does not route restJson1 or execute-api requests"}
}

// FailureAnswersSwitch is apigatewayv2.lambda.failure-answers. The HTTP
// API troubleshooting page documents the 500 body for malformed output;
// its status, the timeout and size answers are inferred (5.6, 4.7, 12 item
// 1).
func FailureAnswersSwitch() fidelity.Switch[FailureAnswers] {
	return fidelity.Switch[FailureAnswers]{
		Name: "apigatewayv2.lambda.failure-answers",
		Value: FailureAnswers{
			FunctionError: GatewayAnswer{Status: http.StatusInternalServerError, Message: "Internal Server Error"},
			Timeout:       GatewayAnswer{Status: http.StatusServiceUnavailable, Message: "Service Unavailable"},
			TooLarge:      GatewayAnswer{Status: http.StatusRequestEntityTooLarge, Message: "Request Entity Too Large"},
			MaxBody:       10_485_760,
			MaxEvent:      6_291_456,
		},
		Label:   evidence.Inferred,
		Ref:     researchRef + "56-failures-function-errors-malformed-output-timeouts",
		Record:  "APIGW2-10",
		Ops:     []string{invokeAPI},
		Settles: unrecorded("APIGW2-10"),
	}
}

// PermissionSwitch is apigatewayv2.lambda.permission. The $default form
// and the denial body are documented; the other form and the status are
// inferred (5.5, 8.4).
func PermissionSwitch() fidelity.Switch[Permission] {
	return fidelity.Switch[Permission]{
		Name: "apigatewayv2.lambda.permission",
		Value: Permission{
			SourceARN: "<api-id>/<stage>/<METHOD>/<route path without its leading slash>, the route key's template and ANY as written; <api-id>/<stage>/$default for the $default route",
			Denial:    GatewayAnswer{Status: http.StatusInternalServerError, Message: "Internal Server Error"},
		},
		Label:   evidence.Inferred,
		Ref:     researchRef + "55-invocation-credentials-and-the-resource-policy",
		Record:  "APIGW2-11",
		Ops:     []string{invokeAPI},
		Settles: unrecorded("APIGW2-11"),
	}
}

// RouteMatchingSwitch is apigatewayv2.route.matching (3.2, 4.4, 12 item
// 9).
func RouteMatchingSwitch() fidelity.Switch[RouteMatching] {
	return fidelity.Switch[RouteMatching]{
		Name: "apigatewayv2.route.matching",
		Value: RouteMatching{
			Matching: route.Matching{CaseSensitive: true, TrailingSlashDistinct: true, HeadMatchesGet: false},
			NoTarget: GatewayAnswer{Status: http.StatusNotFound, Message: "Not Found"},
		},
		Label:   evidence.Inferred,
		Ref:     researchRef + "44-methods-head-trailing-slashes-case-and-decoding",
		Record:  "APIGW2-05",
		Ops:     []string{invokeAPI},
		Settles: unrecorded("APIGW2-05"),
	}
}

// FrontAnswersSwitch is apigatewayv2.front.answers. The Not Found bodies
// are documented, their status and the rest inferred (3.1, 3.5, 4.2, 4.5).
func FrontAnswersSwitch() fidelity.Switch[FrontAnswers] {
	forbidden := GatewayAnswer{Status: http.StatusForbidden, Message: "Forbidden"}
	notFound := GatewayAnswer{Status: http.StatusNotFound, Message: "Not Found"}
	return fidelity.Switch[FrontAnswers]{
		Name: "apigatewayv2.front.answers",
		Value: FrontAnswers{
			StageSegmentWins: true,
			UnknownAPI:       forbidden,
			DisabledEndpoint: forbidden,
			NoStage:          notFound,
			NoDeployment:     notFound,
			NoRoute:          notFound,
			ContentType:      "application/json",
			Deployments:      Deployments{DeleteInUse: Refused, EmptyAPI: Allowed, ExplicitOnAutoDeploy: Refused},
		},
		Label:  evidence.Inferred,
		Ref:    researchRef + "45-responses-api-gateway-generates-itself",
		Record: "APIGW2-06, APIGW2-04",
		Ops: []string{
			invokeAPI, "apigatewayv2.CreateDeployment", "apigatewayv2.DeleteDeployment",
			"apigatewayv2.CreateStage", "apigatewayv2.UpdateStage",
		},
		Settles: unrecorded("APIGW2-06 and APIGW2-04"),
	}
}

// PayloadSwitch is apigatewayv2.payload.unrecorded (5.1-5.4, 12 items 3, 6
// and 10). APIGW2-07, APIGW2-08 and APIGW2-09 recorded every choice on
// 2026-11-03: a bodyless 2.0 event's isBase64Encoded is false, requestId is
// the apigw-requestid header, a response without a Content-Type gets
// text/plain; charset=utf-8 under both formats, a 2.0 response's
// multiValueHeaders is ignored, and the 1.0 single-value maps keep a
// repeated header's and a repeated query parameter's last value (the
// guide's example keeps the first query value). The replayer does not
// route data-plane requests, so no scenario replays them.
func PayloadSwitch() fidelity.Switch[PayloadChoices] {
	return fidelity.Switch[PayloadChoices]{
		Name: "apigatewayv2.payload.unrecorded",
		Value: PayloadChoices{
			BodylessBase64:      false,
			RequestIDIsHeader:   true,
			DefaultContentType:  "text/plain; charset=utf-8",
			V2MultiValueHeaders: false,
			V1Header:            Last,
			V1Query:             Last,
		},
		Label:   evidence.AWSRecorded,
		Ref:     researchRef + "51-payload-format-20-request-event",
		Record:  "APIGW2-07, APIGW2-08, APIGW2-09",
		Ops:     []string{invokeAPI},
		Settles: recorded("APIGW2-07, APIGW2-08 and APIGW2-09"),
	}
}

// Switches lists the behaviors the service chose without AWS-recorded
// evidence, in the order of the spec's switch table.
func (*Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{
		FailureAnswersSwitch().Entry(),
		PermissionSwitch().Entry(),
		RouteMatchingSwitch().Entry(),
		FrontAnswersSwitch().Entry(),
		PayloadSwitch().Entry(),
	}
}
