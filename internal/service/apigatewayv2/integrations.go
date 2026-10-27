package apigatewayv2

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// Payload format versions (apigateway-http.md 5.1, 5.2).
const (
	payloadV1 = "1.0"
	payloadV2 = "2.0"
)

// stageVariableRef starts a stage variable reference in an integration's
// URI (apigateway-http.md 3.3).
const stageVariableRef = "${stageVariables."

// createIntegration creates a Lambda proxy integration of payload format
// 2.0 and deploys it to the API's autoDeploy stages.
func (s *Service) createIntegration(_ context.Context, c *kernel.Call, in *apigatewayv2api.CreateIntegrationInput) (*apigatewayv2api.CreateIntegrationOutput, *awserr.Error) {
	if err := checkIntegration(in); err != nil {
		return nil, err
	}
	i, err := writeAPI(s, c, in.ApiId, func(a *API, now clock.Instant) (Integration, *awserr.Error) {
		i := Integration{ID: s.newChildID(a, s.integrationID), URI: *in.IntegrationUri, PayloadFormatVersion: *in.PayloadFormatVersion}
		a.Integrations = append(slices.Clone(a.Integrations), i)
		a.autoDeploy(now, s.deploymentID)
		return i, nil
	})
	if err != nil {
		return nil, err
	}
	return &apigatewayv2api.CreateIntegrationOutput{
		IntegrationId: new(i.ID), IntegrationType: new(apigatewayv2api.IntegrationTypeAwsProxy),
		IntegrationUri: new(i.URI), PayloadFormatVersion: new(i.PayloadFormatVersion),
	}, nil
}

// checkIntegration refuses what the first slice does not store: anything
// but a Lambda proxy integration of payload format 2.0 with a function ARN
// or invoke URI and no other member.
func checkIntegration(in *apigatewayv2api.CreateIntegrationInput) *awserr.Error {
	const op = "CreateIntegration"
	if err := refuseMembers(op,
		member{"connectionId", in.ConnectionId != nil},
		member{"connectionType", in.ConnectionType != nil},
		member{"contentHandlingStrategy", in.ContentHandlingStrategy != nil},
		member{"credentialsArn", in.CredentialsArn != nil},
		member{"description", in.Description != nil},
		member{"integrationMethod", in.IntegrationMethod != nil},
		member{"integrationSubtype", in.IntegrationSubtype != nil},
		member{"passthroughBehavior", in.PassthroughBehavior != nil},
		member{"requestParameters", in.RequestParameters != nil},
		member{"requestTemplates", in.RequestTemplates != nil},
		member{"responseParameters", in.ResponseParameters != nil},
		member{"templateSelectionExpression", in.TemplateSelectionExpression != nil},
		member{"timeoutInMillis", in.TimeoutInMillis != nil},
		member{"tlsConfig", in.TlsConfig != nil},
	); err != nil {
		return err
	}
	switch in.IntegrationType {
	case apigatewayv2api.IntegrationTypeAwsProxy:
	case apigatewayv2api.IntegrationTypeHttpProxy:
		return errNotImplemented(op, "integrationType HTTP_PROXY", "Use a Lambda proxy integration (AWS_PROXY).")
	case apigatewayv2api.IntegrationTypeAws, apigatewayv2api.IntegrationTypeHttp, apigatewayv2api.IntegrationTypeMock:
		return errBadRequest("integrationType %s is for WebSocket APIs; an HTTP API takes AWS_PROXY or HTTP_PROXY", in.IntegrationType)
	}
	switch v := in.PayloadFormatVersion; {
	case v == nil:
		return errBadRequest("payloadFormatVersion is required: 1.0 or 2.0 for a Lambda proxy integration")
	case *v == payloadV1:
		return errNotImplemented(op, "payloadFormatVersion 1.0", "Use payloadFormatVersion 2.0.")
	case *v != payloadV2:
		return errBadRequest("payloadFormatVersion %q is not 1.0 or 2.0", *v)
	}
	switch uri := in.IntegrationUri; {
	case uri == nil:
		return errBadRequest("integrationUri is required for a Lambda proxy integration: a function ARN or its invoke URI")
	case strings.Contains(*uri, stageVariableRef):
		return errNotImplemented(op, "a stage variable in integrationUri", "Name the function in integrationUri.")
	default:
		if _, ok := lambdaFunction(*uri); !ok {
			return errBadRequest("integrationUri %q is neither a Lambda function ARN nor arn:aws:apigateway:<region>:lambda:path/2015-03-31/functions/<function-arn>/invocations", *uri)
		}
	}
	return nil
}

// invokeURI is API Gateway's invoke URI of a function, Terraform's
// invoke_arn (apigateway-http.md 3.3); the group is the function ARN.
var invokeURI = regexp.MustCompile(`^arn:[a-z-]+:apigateway:[a-z0-9-]+:lambda:path/2015-03-31/functions/(.+)/invocations$`)

// lambdaFunction is the function a Lambda integration's URI names: the
// function ARN itself, or the one inside API Gateway's invoke URI
// arn:<partition>:apigateway:<region>:lambda:path/2015-03-31/functions/
// <function-arn>/invocations (apigateway-http.md 3.3), qualifier kept.
func lambdaFunction(uri string) (aws.ARN, bool) {
	text := uri
	if m := invokeURI.FindStringSubmatch(uri); m != nil {
		text = m[1]
	}
	fn, err := aws.ParseARN(text)
	if err != nil || fn.Service != "lambda" || fn.Region == "" || fn.Account == "" {
		return aws.ARN{}, false
	}
	name, qualifier, qualified := strings.Cut(strings.TrimPrefix(fn.Resource, "function:"), ":")
	if !strings.HasPrefix(fn.Resource, "function:") || name == "" || qualified && (qualifier == "" || strings.Contains(qualifier, ":")) {
		return aws.ARN{}, false
	}
	return fn, true
}
