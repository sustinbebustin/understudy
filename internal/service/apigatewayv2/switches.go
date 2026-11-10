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
// choices (apigateway-http.md 4.4). A route without a target takes no part
// in routing (APIGW2-05, recorded), which needs no choice.
type RouteMatching struct {
	route.Matching
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

// recorded is the Settles of a switch the named recording cases recorded:
// their fixtures stay evidence only, since the replayer routes neither
// restJson1 nor execute-api requests (testdata/README.md, Replay
// scenarios).
func recorded(cases string) fidelity.Settles {
	return fidelity.NoScenario{Reason: cases + " recorded 2026-11-03 (testdata/parity/apigatewayv2); the replayer does not route restJson1 or execute-api requests"}
}

// FailureAnswersSwitch is apigatewayv2.lambda.failure-answers, recorded by
// APIGW2-10 on 2026-11-03 (5.6, 4.7): a function error 500 Internal Server
// Error, a timeout 503 Service Unavailable, and 413 Request Entity Too
// Large for an event over Lambda's 6 MB and for a body over the payload
// quota. MaxBody's value is inferred, not recorded: the 10 MB quota read
// as 10,000,000 bytes. APIGW2-10's 10,000,001-byte body (step 04) is also
// over the 6 MB event limit and got the same answer as the 7,000,000-byte
// body (step 06), so it does not tell 10,000,000 from 10,485,760; a Lambda
// integration cannot (4.7). AWS answers a body over 10 MiB from a front
// layer without Content-Type or apigw-requestid; understudy's 413 carries
// both at every size.
func FailureAnswersSwitch() fidelity.Switch[FailureAnswers] {
	return fidelity.Switch[FailureAnswers]{
		Name: "apigatewayv2.lambda.failure-answers",
		Value: FailureAnswers{
			FunctionError: GatewayAnswer{Status: http.StatusInternalServerError, Message: "Internal Server Error"},
			Timeout:       GatewayAnswer{Status: http.StatusServiceUnavailable, Message: "Service Unavailable"},
			TooLarge:      GatewayAnswer{Status: http.StatusRequestEntityTooLarge, Message: "Request Entity Too Large"},
			MaxBody:       10_000_000,
			MaxEvent:      6_291_456,
		},
		Label:   evidence.AWSRecorded,
		Ref:     researchRef + "56-failures-function-errors-malformed-output-timeouts",
		Record:  "APIGW2-10",
		Ops:     []string{invokeAPI},
		Settles: recorded("APIGW2-10"),
	}
}

// PermissionSwitch is apigatewayv2.lambda.permission, recorded by
// APIGW2-11 on 2026-11-03 (5.5, 8.4): the source ARN uses the route key's
// template and ANY as written, a named stage its name, and a denial
// answers 500 Internal Server Error without running the function.
func PermissionSwitch() fidelity.Switch[Permission] {
	return fidelity.Switch[Permission]{
		Name: "apigatewayv2.lambda.permission",
		Value: Permission{
			SourceARN: "<api-id>/<stage>/<METHOD>/<route path without its leading slash>, the route key's template and ANY as written; <api-id>/<stage>/$default for the $default route",
			Denial:    GatewayAnswer{Status: http.StatusInternalServerError, Message: "Internal Server Error"},
		},
		Label:   evidence.AWSRecorded,
		Ref:     researchRef + "55-invocation-credentials-and-the-resource-policy",
		Record:  "APIGW2-11",
		Ops:     []string{invokeAPI},
		Settles: recorded("APIGW2-11"),
	}
}

// RouteMatchingSwitch is apigatewayv2.route.matching (4.4, 12 item 9).
// APIGW2-05 recorded on 2026-11-03 that literal segments match
// case-sensitively and /a/ does not match /a; whether HEAD reaches a GET
// route stays unrecorded (the HEAD answer has no body to show the route),
// so the switch stays inferred.
func RouteMatchingSwitch() fidelity.Switch[RouteMatching] {
	return fidelity.Switch[RouteMatching]{
		Name: "apigatewayv2.route.matching",
		Value: RouteMatching{
			Matching: route.Matching{CaseSensitive: true, TrailingSlashDistinct: true, HeadMatchesGet: false},
		},
		Label:   evidence.Inferred,
		Ref:     researchRef + "44-methods-head-trailing-slashes-case-and-decoding",
		Record:  "APIGW2-05",
		Ops:     []string{invokeAPI},
		Settles: fidelity.NoScenario{Reason: "unrecorded: APIGW2-05 step 07 (HEAD) answered without a body, so the route it reached is unknown; the case and trailing-slash choices are recorded"},
	}
}

// FrontAnswersSwitch is apigatewayv2.front.answers (3.1, 3.5, 4.2, 4.5).
// APIGW2-06 and APIGW2-04 recorded on 2026-11-03 every choice but one: a
// stage segment shadows a $default route, a disabled default endpoint,
// no stage, a stage without a deployment and no route answer 404
// {"message":"Not Found"} with Content-Type application/json, deploying
// an API with no routes, deleting a deployment a stage serves and setting
// a deployment on an autoDeploy stage are refused. An unknown API id has
// no answer on AWS, whose execute-api name for it does not resolve;
// understudy's 403 Forbidden is its own, so the switch stays inferred.
func FrontAnswersSwitch() fidelity.Switch[FrontAnswers] {
	notFound := GatewayAnswer{Status: http.StatusNotFound, Message: "Not Found"}
	return fidelity.Switch[FrontAnswers]{
		Name: "apigatewayv2.front.answers",
		Value: FrontAnswers{
			StageSegmentWins: true,
			UnknownAPI:       GatewayAnswer{Status: http.StatusForbidden, Message: "Forbidden"},
			DisabledEndpoint: notFound,
			NoStage:          notFound,
			NoDeployment:     notFound,
			NoRoute:          notFound,
			ContentType:      "application/json",
			Deployments:      Deployments{DeleteInUse: Refused, EmptyAPI: Refused, ExplicitOnAutoDeploy: Refused},
		},
		Label:  evidence.Inferred,
		Ref:    researchRef + "45-responses-api-gateway-generates-itself",
		Record: "APIGW2-06, APIGW2-04",
		Ops: []string{
			invokeAPI, "apigatewayv2.CreateDeployment", "apigatewayv2.DeleteDeployment",
			"apigatewayv2.CreateStage", "apigatewayv2.UpdateStage",
		},
		Settles: fidelity.NoScenario{Reason: "unrecordable: an unknown API id's execute-api name does not resolve on AWS (APIGW2-06 step 08); the other choices are recorded (APIGW2-06, APIGW2-04)"},
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
// evidence.
func (*Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{
		FailureAnswersSwitch().Entry(),
		PermissionSwitch().Entry(),
		RouteMatchingSwitch().Entry(),
		FrontAnswersSwitch().Entry(),
		PayloadSwitch().Entry(),
	}
}
