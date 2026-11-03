package apigatewayv2

import (
	"context"
	"maps"
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

// The defaults AWS fills on an integration that leaves them out: POST for
// a Lambda integration's method, an INTERNET connection and a 30 second
// timeout (the create-integration CLI example and Terraform's acceptance
// test against AWS, apigateway-http.md 3.3).
const (
	defaultLambdaMethod = "POST"
	defaultTimeout      = 30000
)

// maxIntegrations is "Integrations per API: 300" (the HTTP API quotas
// page, apigateway-http.md 3.9).
const maxIntegrations = 300

// stageVariableRef starts a stage variable reference in an integration's
// URI (apigateway-http.md 3.3).
const stageVariableRef = "${stageVariables."

// subtypeRequired is the required request parameters of an integration
// subtype, false for a name that is none of the ten there are: no SNS
// subtype exists (the subtype reference, apigateway-http.md 6.1).
func subtypeRequired(subtype string) ([]string, bool) {
	switch subtype {
	case "AppConfig-GetConfiguration":
		return []string{"Application", "Environment", "Configuration", "ClientId"}, true
	case "EventBridge-PutEvents":
		return []string{"Detail", "DetailType", "Source"}, true
	case "Kinesis-PutRecord":
		return []string{"StreamName", "Data", "PartitionKey"}, true
	case "SQS-DeleteMessage":
		return []string{"ReceiptHandle", "QueueUrl"}, true
	case "SQS-PurgeQueue", "SQS-ReceiveMessage":
		return []string{"QueueUrl"}, true
	case "SQS-SendMessage":
		return []string{"QueueUrl", "MessageBody"}, true
	case "StepFunctions-StartExecution", "StepFunctions-StartSyncExecution":
		return []string{"StateMachineArn"}, true
	case "StepFunctions-StopExecution":
		return []string{"ExecutionArn"}, true
	}
	return nil, false
}

// checkCreateIntegration refuses, at step 7, a CreateIntegration
// understudy cannot store.
func checkCreateIntegration(in *apigatewayv2api.CreateIntegrationInput) *awserr.Error {
	_, err := integrationIn(in)
	return err
}

// checkUpdateIntegration refuses, at step 7, the WebSocket-only members of
// an UpdateIntegration; the merged integration is checked in the write.
func checkUpdateIntegration(in *apigatewayv2api.UpdateIntegrationInput) *awserr.Error {
	return refuseWebSocketIntegrationMembers("UpdateIntegration", in.RequestTemplates, in.TemplateSelectionExpression, in.ContentHandlingStrategy)
}

// refuseWebSocketIntegrationMembers answers NotImplemented for the members
// that change what a WebSocket API's integration does (apigateway-http.md
// 2.4): understudy does not emulate them, and on an HTTP API storing them
// without effect would hide that (the unit's conflict row 4).
func refuseWebSocketIntegrationMembers(op string, templates map[string]string, selection *string, handling *apigatewayv2api.ContentHandlingStrategy) *awserr.Error {
	for _, m := range []member{{"requestTemplates", templates != nil}, {"templateSelectionExpression", selection != nil}, {"contentHandlingStrategy", handling != nil}} {
		if m.set {
			return errNotImplemented(op, m.name+" (a WebSocket API member)", "Send the request without "+m.name+".")
		}
	}
	return nil
}

// integrationIn is the integration a CreateIntegration stores, its
// defaults filled, or why it cannot be stored. PassthroughBehavior is
// dropped: AWS returns none for an HTTP API although Terraform always
// sends it (apigateway-http.md 3.3, 9.3).
func integrationIn(in *apigatewayv2api.CreateIntegrationInput) (Integration, *awserr.Error) {
	if err := refuseWebSocketIntegrationMembers("CreateIntegration", in.RequestTemplates, in.TemplateSelectionExpression, in.ContentHandlingStrategy); err != nil {
		return Integration{}, err
	}
	// A WebSocket integration type is refused before a missing payload
	// format version (APIGW2-01 step 26 sent MOCK without one).
	if err := checkType(in.IntegrationType); err != nil {
		return Integration{}, err
	}
	if in.PayloadFormatVersion == nil {
		// "Required for HTTP APIs" (the integrations API reference,
		// apigateway-http.md 2.4); AWS's text (APIGW2-01 step 19).
		return Integration{}, errBadRequest("PayloadFormatVersion is a required parameter for integration")
	}
	i := Integration{
		Type: in.IntegrationType, Subtype: clonePtr(in.IntegrationSubtype),
		URI: clonePtr(in.IntegrationUri), Method: clonePtr(in.IntegrationMethod),
		PayloadFormatVersion: *in.PayloadFormatVersion,
		ConnectionType:       deref(in.ConnectionType, apigatewayv2api.ConnectionTypeInternet),
		ConnectionID:         clonePtr(in.ConnectionId),
		CredentialsARN:       clonePtr(in.CredentialsArn),
		Description:          clonePtr(in.Description),
		TimeoutInMillis:      deref(in.TimeoutInMillis, defaultTimeout),
		RequestParameters:    maps.Clone(in.RequestParameters),
		ResponseParameters:   cloneResponseParameters(in.ResponseParameters),
		TLSConfig:            tlsIn(in.TlsConfig),
	}
	i.fillDefaults()
	err := i.check()
	return i, err
}

// fillDefaults gives a Lambda integration without a method POST.
func (i *Integration) fillDefaults() {
	if i.lambda() && i.Method == nil {
		i.Method = new(defaultLambdaMethod)
	}
}

// lambda reports a Lambda proxy integration: AWS_PROXY without a subtype.
func (i *Integration) lambda() bool {
	return i.Type == apigatewayv2api.IntegrationTypeAwsProxy && i.Subtype == nil
}

// checkType refuses an integration type an HTTP API cannot have: AWS,
// HTTP and MOCK are "Supported only for WebSocket APIs" (the model's
// IntegrationType documentation, apigateway-http.md 2.4), refused with
// AWS's text (APIGW2-01 step 26). A value outside the enum, which only
// CreateIntegration's validation leaves to the handler, is dropped by AWS
// and reported missing (step 17b).
func checkType(t apigatewayv2api.IntegrationType) *awserr.Error {
	switch t {
	case apigatewayv2api.IntegrationTypeAwsProxy, apigatewayv2api.IntegrationTypeHttpProxy:
		return nil
	case apigatewayv2api.IntegrationTypeAws, apigatewayv2api.IntegrationTypeHttp, apigatewayv2api.IntegrationTypeMock:
		return errBadRequest("Currently, an API with a protocol type of HTTP may only be associated with proxy integrations (AWS_PROXY, HTTP_PROXY)")
	}
	return errBadRequest("IntegrationType is a required parameter for creating integration.")
}

// check refuses an integration an HTTP API cannot have (apigateway-http.md
// 2.4, 3.3, 6.1, 13.5), with AWS's text where APIGW2-01 recorded one.
func (i *Integration) check() *awserr.Error {
	if err := checkType(i.Type); err != nil {
		return err
	}
	switch {
	case i.Subtype != nil:
		return i.checkSubtype()
	case i.Type == apigatewayv2api.IntegrationTypeHttpProxy:
		if err := i.checkPayloadV1("an HTTP proxy integration"); err != nil {
			return err
		}
		if i.URI == nil {
			return errBadRequest("integrationUri is required for an HTTP proxy integration")
		}
		return nil
	default:
		return i.checkLambda()
	}
}

// checkLambda checks a Lambda proxy integration: payload format 1.0 or
// 2.0, and a URI naming a function by its ARN or API Gateway's invoke URI
// (apigateway-http.md 3.3), or referencing a stage variable, which is
// stored and answers 501 when a request reaches it.
func (i *Integration) checkLambda() *awserr.Error {
	if v := i.PayloadFormatVersion; v != payloadV1 && v != payloadV2 {
		return errBadRequest("Unsupported PayloadFormatVersion: %s", v)
	}
	switch uri := i.URI; {
	case uri == nil:
		return errBadRequest("integrationUri is required for a Lambda proxy integration: a function ARN or its invoke URI")
	case strings.Contains(*uri, stageVariableRef):
		return nil
	default:
		if _, ok := lambdaFunction(*uri); !ok {
			return errBadRequest("integrationUri %q is neither a Lambda function ARN nor arn:aws:apigateway:<region>:lambda:path/2015-03-31/functions/<function-arn>/invocations", *uri)
		}
		return nil
	}
}

// checkSubtype checks an AWS service integration: AWS_PROXY at payload
// format 1.0, one of the ten subtypes, and every one of its required
// request parameters ("You must configure all required parameters to
// create an integration", apigateway-http.md 6.2). On update the merged
// parameters are checked, as Terraform re-sends them all (6.2; inferred,
// APIGW2-01 case 22).
func (i *Integration) checkSubtype() *awserr.Error {
	if i.Type != apigatewayv2api.IntegrationTypeAwsProxy {
		return errBadRequest("integrationSubtype %q needs integrationType AWS_PROXY, not %s", *i.Subtype, i.Type)
	}
	required, ok := subtypeRequired(*i.Subtype)
	if !ok {
		// AWS's text (APIGW2-01 step 21, SNS-Publish).
		return errBadRequest("Operation: %s is not supported.", *i.Subtype)
	}
	if err := i.checkPayloadV1("an AWS service integration"); err != nil {
		return err
	}
	for _, p := range required {
		if _, ok := i.RequestParameters[p]; !ok {
			// AWS's text (APIGW2-01 step 20).
			return errBadRequest("Operation: %s requires enabling passthrough, or defining all of the following parameters in the parameter mapping: [%s]", *i.Subtype, strings.Join(required, ", "))
		}
	}
	return nil
}

// checkPayloadV1 refuses a payload format other than 1.0 on what, which
// is not a Lambda integration (apigateway-http.md 2.4).
func (i *Integration) checkPayloadV1(what string) *awserr.Error {
	if i.PayloadFormatVersion != payloadV1 {
		return errBadRequest("payloadFormatVersion %q is not valid for %s: only a Lambda integration takes 2.0, everything else 1.0", i.PayloadFormatVersion, what)
	}
	return nil
}

// createIntegration creates an integration and deploys it to the API's
// autoDeploy stages.
func (s *Service) createIntegration(_ context.Context, c *kernel.Call, in *apigatewayv2api.CreateIntegrationInput) (*apigatewayv2api.CreateIntegrationOutput, *awserr.Error) {
	i, err := integrationIn(in)
	if err != nil {
		return nil, err
	}
	i, err = writeAPI(s, c, in.ApiId, func(a *API, now clock.Instant) (Integration, *awserr.Error) {
		if len(a.Integrations) >= maxIntegrations {
			return Integration{}, errBadRequest("API %s already has %d integrations, the most an HTTP API may have", a.ID, maxIntegrations)
		}
		i.ID, i.Seq = s.newChildID(a, s.integrationID), a.nextChild()
		a.Integrations = append(slices.Clone(a.Integrations), i)
		a.autoDeploy(now, s.deploymentID)
		return i, nil
	})
	if err != nil {
		return nil, err
	}
	out := apigatewayv2api.CreateIntegrationOutput(i.output())
	return &out, nil
}

// updateIntegration replaces the members an UpdateIntegration carries,
// checks the merged integration as CreateIntegration checks one, and
// deploys the change.
func (s *Service) updateIntegration(_ context.Context, c *kernel.Call, in *apigatewayv2api.UpdateIntegrationInput) (*apigatewayv2api.UpdateIntegrationOutput, *awserr.Error) {
	i, err := writeAPI(s, c, in.ApiId, func(a *API, now clock.Instant) (Integration, *awserr.Error) {
		k, err := a.integrationIndex(in.IntegrationId)
		if err != nil {
			return Integration{}, err
		}
		i := a.Integrations[k].merged(in)
		if err := i.check(); err != nil {
			return Integration{}, err
		}
		a.Integrations = slices.Clone(a.Integrations)
		a.Integrations[k] = i
		a.autoDeploy(now, s.deploymentID)
		return i, nil
	})
	if err != nil {
		return nil, err
	}
	out := apigatewayv2api.UpdateIntegrationOutput(i.output())
	return &out, nil
}

// merged is the integration with an UpdateIntegration's members applied:
// a member present replaces the stored one. A request parameter sent with
// the value "" is removed, and a response parameter status sent with {}
// has its mappings removed; the others sent replace or add theirs (the
// parameter mapping page, apigateway-http.md 3.3).
func (i *Integration) merged(in *apigatewayv2api.UpdateIntegrationInput) Integration {
	n := i.clone()
	n.Type = deref(in.IntegrationType, n.Type)
	n.PayloadFormatVersion = deref(in.PayloadFormatVersion, n.PayloadFormatVersion)
	n.ConnectionType = deref(in.ConnectionType, n.ConnectionType)
	n.TimeoutInMillis = deref(in.TimeoutInMillis, n.TimeoutInMillis)
	for _, m := range []struct {
		stored **string
		sent   *string
	}{
		{&n.Subtype, in.IntegrationSubtype},
		{&n.URI, in.IntegrationUri},
		{&n.Method, in.IntegrationMethod},
		{&n.ConnectionID, in.ConnectionId},
		{&n.CredentialsARN, in.CredentialsArn},
		{&n.Description, in.Description},
	} {
		if m.sent != nil {
			*m.stored = clonePtr(m.sent)
		}
	}
	if in.TlsConfig != nil {
		n.TLSConfig = tlsIn(in.TlsConfig)
	}
	for k, v := range in.RequestParameters {
		if n.RequestParameters == nil {
			n.RequestParameters = map[string]string{}
		}
		if v == "" {
			delete(n.RequestParameters, k)
		} else {
			n.RequestParameters[k] = v
		}
	}
	for status, m := range in.ResponseParameters {
		if n.ResponseParameters == nil {
			n.ResponseParameters = map[string]map[string]string{}
		}
		if len(m) == 0 {
			delete(n.ResponseParameters, status)
		} else {
			n.ResponseParameters[status] = maps.Clone(m)
		}
	}
	if len(n.RequestParameters) == 0 {
		n.RequestParameters = nil
	}
	if len(n.ResponseParameters) == 0 {
		n.ResponseParameters = nil
	}
	n.fillDefaults()
	return n
}

// deleteIntegration removes an integration no route targets and deploys
// the change. Deleting one a route targets is refused naming the routes:
// AWS's answer is unrecorded (inferred, APIGW2-01), and a route left
// targeting no integration would be served nothing.
func (s *Service) deleteIntegration(_ context.Context, c *kernel.Call, in *apigatewayv2api.DeleteIntegrationInput) (*apigatewayv2api.DeleteIntegrationOutput, *awserr.Error) {
	if _, err := writeAPI(s, c, in.ApiId, func(a *API, now clock.Instant) (struct{}, *awserr.Error) {
		k, err := a.integrationIndex(in.IntegrationId)
		if err != nil {
			return struct{}{}, err
		}
		var targeting []string
		for _, r := range a.Routes {
			if r.Integration == in.IntegrationId {
				targeting = append(targeting, r.ID)
			}
		}
		if len(targeting) > 0 {
			return struct{}{}, errBadRequest("integration %s of API %s is the target of routes %s; delete them or change their targets first", in.IntegrationId, a.ID, strings.Join(targeting, ", "))
		}
		a.Integrations = slices.Delete(slices.Clone(a.Integrations), k, k+1)
		a.autoDeploy(now, s.deploymentID)
		return struct{}{}, nil
	}); err != nil {
		return nil, err
	}
	return &apigatewayv2api.DeleteIntegrationOutput{}, nil
}

// getIntegration answers one integration of an API.
func (s *Service) getIntegration(_ context.Context, c *kernel.Call, in *apigatewayv2api.GetIntegrationInput) (*apigatewayv2api.GetIntegrationOutput, *awserr.Error) {
	i, err := readAPI(s, c.Scope(), in.ApiId, func(a *API) (Integration, *awserr.Error) {
		k, err := a.integrationIndex(in.IntegrationId)
		if err != nil {
			return Integration{}, err
		}
		return a.Integrations[k], nil
	})
	if err != nil {
		return nil, err
	}
	return new(i.output()), nil
}

// getIntegrations lists an API's integrations in creation order (inferred,
// APIGW2-03), a page at a time.
func (s *Service) getIntegrations(_ context.Context, c *kernel.Call, in *apigatewayv2api.GetIntegrationsInput) (*apigatewayv2api.GetIntegrationsOutput, *awserr.Error) {
	all, err := readAPI(s, c.Scope(), in.ApiId, func(a *API) ([]Integration, *awserr.Error) { return slices.Clone(a.Integrations), nil })
	if err != nil {
		return nil, err
	}
	items, next, err := page(all, func(i Integration) uint64 { return i.Seq }, in.MaxResults, in.NextToken)
	if err != nil {
		return nil, err
	}
	out := &apigatewayv2api.GetIntegrationsOutput{Items: make([]apigatewayv2api.Integration, 0, len(items)), NextToken: next}
	for k := range items {
		out.Items = append(out.Items, apigatewayv2api.Integration(items[k].output()))
	}
	return out, nil
}

// integrationIndex is the index of the integration id names; an id the
// API does not hold is NotFoundException.
func (a *API) integrationIndex(id string) (int, *awserr.Error) {
	k := slices.IndexFunc(a.Integrations, func(i Integration) bool { return i.ID == id })
	if k < 0 {
		return 0, errNotFound("Invalid Integration identifier specified") // APIGW2-01 step 04
	}
	return k, nil
}

// output is the integration as GetIntegration answers it: the stored
// members, ConnectionType and TimeoutInMillis always, and no
// PassthroughBehavior, IntegrationResponseSelectionExpression or
// ApiGatewayManaged (apigateway-http.md 3.3, 9.3).
func (i *Integration) output() apigatewayv2api.GetIntegrationOutput {
	return apigatewayv2api.GetIntegrationOutput{
		ConnectionId:         clonePtr(i.ConnectionID),
		ConnectionType:       new(i.ConnectionType),
		CredentialsArn:       clonePtr(i.CredentialsARN),
		Description:          clonePtr(i.Description),
		IntegrationId:        new(i.ID),
		IntegrationMethod:    clonePtr(i.Method),
		IntegrationSubtype:   clonePtr(i.Subtype),
		IntegrationType:      new(i.Type),
		IntegrationUri:       clonePtr(i.URI),
		PayloadFormatVersion: new(i.PayloadFormatVersion),
		RequestParameters:    maps.Clone(i.RequestParameters),
		ResponseParameters:   cloneResponseParameters(i.ResponseParameters),
		TimeoutInMillis:      new(i.TimeoutInMillis),
		TlsConfig:            i.TLSConfig.wire(),
	}
}

// tlsIn is a request's TLS configuration as stored.
func tlsIn(t *apigatewayv2api.TlsConfigInput) *TLSConfig {
	if t == nil {
		return nil
	}
	return &TLSConfig{ServerNameToVerify: clonePtr(t.ServerNameToVerify)}
}

// wire is the stored TLS configuration as an output carries it.
func (t *TLSConfig) wire() *apigatewayv2api.TlsConfig {
	if t == nil {
		return nil
	}
	return &apigatewayv2api.TlsConfig{ServerNameToVerify: clonePtr(t.ServerNameToVerify)}
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
