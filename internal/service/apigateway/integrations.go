package apigateway

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/apigateway/patch"
)

// Integrations and integration responses (apigateway-rest.md 3.4, 3.5;
// texts in 2.4, defaults in 2.3, patch paths in 3.8).

// The integration defaults AWS fills (recorded, apigateway-rest.md 2.3).
const (
	defaultTimeoutInMillis     = 29000
	defaultPassthroughBehavior = "WHEN_NO_MATCH"
	// minTimeoutInMillis is the documented lower bound; a buffered
	// integration's timeout is at most maxBufferedTimeoutInMillis and a
	// streamed one's maxStreamTimeoutInMillis (apigateway-rest.md 3.4, 11).
	minTimeoutInMillis         = 50
	maxBufferedTimeoutInMillis = 300000
	maxStreamTimeoutInMillis   = 900000
)

// passthroughBehaviors are the documented passthroughBehavior values
// (apigateway-rest.md 6.2).
func passthroughBehaviors() []string { return []string{"WHEN_NO_MATCH", "WHEN_NO_TEMPLATES", "NEVER"} }

// The recorded PutIntegration refusals (apigateway-rest.md 2.4).
const (
	textNoIntegrationMethod = "Enumeration value for HttpMethod must be non-empty"
	textAWSProxyTarget      = "Integrations of type 'AWS_PROXY' currently only supports Lambda function and Firehose stream invocations."
	textHTTPEndpoint        = "Invalid HTTP endpoint specified for URI"
	textInvalidARN          = "Invalid ARN specified in the request"
	textPathOrAction        = "AWS ARN for integration must contain path or action"
)

// stageVariableRef marks a uri that names a stage variable; it is stored
// verbatim and its shape is not checked (inferred, contract 6b 7.6), and an
// invocation that reaches it answers 501 (spec 6b scope list).
const stageVariableRef = "${stageVariables."

// usesStageVariable reports whether the integration's uri names a stage
// variable.
func (i *Integration) usesStageVariable() bool {
	return strings.Contains(deref(i.URI, ""), stageVariableRef)
}

// check refuses an integration AWS would not accept: a missing integration
// method, a uri of the wrong shape for its type (recorded texts,
// apigateway-rest.md 2.4), a timeout outside its bounds (documented, 11)
// or an undocumented passthroughBehavior (6.2); those two texts are
// inferred. PutIntegration runs it on the request and UpdateIntegration on
// the patched result.
func (i *Integration) check() *awserr.Error {
	if i.Type != apigatewayapi.IntegrationTypeMock && deref(i.HTTPMethod, "") == "" {
		return errBadRequest("%s", textNoIntegrationMethod)
	}
	if err := checkURI(i.Type, deref(i.URI, "")); err != nil {
		return err
	}
	limit := int32(maxBufferedTimeoutInMillis)
	if deref(i.ResponseTransferMode, "") == apigatewayapi.ResponseTransferModeStream {
		limit = maxStreamTimeoutInMillis
	}
	if i.TimeoutInMillis < minTimeoutInMillis || i.TimeoutInMillis > limit {
		return errBadRequest("Timeout should be between %d ms and %d ms", minTimeoutInMillis, limit)
	}
	if !slices.Contains(passthroughBehaviors(), i.PassthroughBehavior) {
		return errBadRequest("Invalid passthrough behavior specified: %s. Valid options are %s", i.PassthroughBehavior, strings.Join(passthroughBehaviors(), ","))
	}
	return nil
}

// checkURI checks a uri's shape for its integration type (recorded,
// apigateway-rest.md 2.4, 3.4). A MOCK integration's uri and a uri naming
// a stage variable are not checked.
func checkURI(t apigatewayapi.IntegrationType, uri string) *awserr.Error {
	if strings.Contains(uri, stageVariableRef) {
		return nil
	}
	switch t {
	case apigatewayapi.IntegrationTypeMock:
		return nil
	case apigatewayapi.IntegrationTypeAwsProxy:
		if _, ok := lambdaFunction(uri); ok {
			return nil
		}
		if a, err := aws.ParseARN(uri); err == nil && a.Service == "apigateway" && a.Account == "firehose" &&
			!strings.HasPrefix(a.Resource, "path/2015-03-31/functions/") {
			// A Firehose stream target is accepted by AWS (the recorded
			// text names it), but its uri form is not recorded: the one
			// recorded Firehose uri, a Lambda-shaped path, is refused
			// (apigateway-rest.md 3.4).
			return refuseUnimplemented("PutIntegration", member{"an AWS_PROXY Firehose stream uri", true})
		}
		return errBadRequest("%s", textAWSProxyTarget)
	case apigatewayapi.IntegrationTypeAws:
		a, err := aws.ParseARN(uri)
		if err != nil {
			return errBadRequest("%s", textInvalidARN)
		}
		if a.Service != "apigateway" || !strings.HasPrefix(a.Resource, "path/") && !strings.HasPrefix(a.Resource, "action/") {
			return errBadRequest("%s", textPathOrAction)
		}
		return nil
	case apigatewayapi.IntegrationTypeHttp, apigatewayapi.IntegrationTypeHttpProxy:
		u, err := url.Parse(uri)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errBadRequest("%s", textHTTPEndpoint)
		}
		return nil
	default:
		// The generated validator refuses a type outside the enum.
		return nil
	}
}

// checkConnectionType refuses VPC_LINK, which needs a VPC link, and VPC
// links are unbound (the unit's conflict row 4).
func checkConnectionType(op string, t apigatewayapi.ConnectionType) *awserr.Error {
	switch t {
	case apigatewayapi.ConnectionTypeInternet:
		return nil
	case apigatewayapi.ConnectionTypeVpcLink:
		return refuseUnimplemented(op, member{"connectionType VPC_LINK", true})
	default:
		// The text is inferred.
		return errBadRequest("Invalid connection type specified: %s. Valid options are INTERNET,VPC_LINK", t)
	}
}

// putIntegration creates or replaces a method's integration
// (apigateway-rest.md 3.4; replacing, which drops the old integration's
// responses, is inferred). It answers the recorded defaults (2.3):
// connectionType INTERNET for HTTP and HTTP_PROXY only, and no httpMethod
// for MOCK even when one was sent.
func (s *Service) putIntegration(_ context.Context, c *kernel.Call, in *apigatewayapi.PutIntegrationInput) (*apigatewayapi.Integration, *awserr.Error) {
	if in.ConnectionType != nil {
		if err := checkConnectionType("PutIntegration", *in.ConnectionType); err != nil {
			return nil, err
		}
	}
	// connectionId names a VPC link, and VPC links are unbound.
	if err := refuseUnimplemented("PutIntegration",
		member{"connectionId", in.ConnectionId != nil},
		member{"tlsConfig", in.TlsConfig != nil},
		member{"integrationTarget", in.IntegrationTarget != nil},
	); err != nil {
		return nil, err
	}
	x := Integration{
		Type: in.Type, HTTPMethod: clonePtr(in.IntegrationHttpMethod), URI: clonePtr(in.Uri), ConnectionType: clonePtr(in.ConnectionType),
		Credentials: clonePtr(in.Credentials), RequestParameters: maps.Clone(in.RequestParameters), RequestTemplates: maps.Clone(in.RequestTemplates),
		PassthroughBehavior: deref(in.PassthroughBehavior, defaultPassthroughBehavior), ContentHandling: clonePtr(in.ContentHandling),
		CacheNamespace: deref(in.CacheNamespace, in.ResourceId), CacheKeyParameters: slices.Clone(in.CacheKeyParameters),
		TimeoutInMillis: deref(in.TimeoutInMillis, defaultTimeoutInMillis), ResponseTransferMode: clonePtr(in.ResponseTransferMode),
	}
	if x.CacheKeyParameters == nil {
		x.CacheKeyParameters = []string{}
	}
	switch x.Type {
	case apigatewayapi.IntegrationTypeMock:
		x.HTTPMethod = nil
	case apigatewayapi.IntegrationTypeHttp, apigatewayapi.IntegrationTypeHttpProxy:
		if x.ConnectionType == nil {
			x.ConnectionType = new(apigatewayapi.ConnectionTypeInternet)
		}
	case apigatewayapi.IntegrationTypeAws, apigatewayapi.IntegrationTypeAwsProxy:
	}
	if err := x.check(); err != nil {
		return nil, err
	}
	return writeMethod(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(_ *RestAPI, m Method) (*Method, *apigatewayapi.Integration, *awserr.Error) {
		m.Integration = new(x.clone())
		return &m, new(x.output()), nil
	})
}

// output is the integration as the integration operations answer it and
// GetMethod embeds it, with its responses when it has them.
func (i *Integration) output() apigatewayapi.Integration {
	keys := slices.Clone(i.CacheKeyParameters)
	if keys == nil {
		keys = []string{}
	}
	out := apigatewayapi.Integration{
		Type: new(i.Type), HttpMethod: clonePtr(i.HTTPMethod), Uri: clonePtr(i.URI), ConnectionType: clonePtr(i.ConnectionType),
		Credentials: clonePtr(i.Credentials), RequestParameters: maps.Clone(i.RequestParameters), RequestTemplates: maps.Clone(i.RequestTemplates),
		PassthroughBehavior: new(i.PassthroughBehavior), ContentHandling: clonePtr(i.ContentHandling), TimeoutInMillis: i.TimeoutInMillis,
		CacheNamespace: new(i.CacheNamespace), CacheKeyParameters: keys, ResponseTransferMode: clonePtr(i.ResponseTransferMode),
	}
	if len(i.Responses) > 0 {
		out.IntegrationResponses = make(map[string]apigatewayapi.IntegrationResponse, len(i.Responses))
		for code, r := range i.Responses {
			out.IntegrationResponses[code] = r.output()
		}
	}
	return out
}

// readIntegration runs fn on the integration of the method ref names, or
// answers the recorded NotFound for what is missing.
func readIntegration[R any](s *Service, c *kernel.Call, ref methodRef, fn func(i *Integration) (R, *awserr.Error)) (R, *awserr.Error) {
	return readMethod(s, c, ref, func(_ *RestAPI, m *Method) (R, *awserr.Error) {
		if m.Integration == nil {
			var zero R
			return zero, errIntegrationNotFound()
		}
		return fn(m.Integration)
	})
}

// writeIntegration runs fn on a copy of the integration of the method ref
// names and stores the copy unless fn refuses; fn returning nil deletes
// it.
func writeIntegration[R any](s *Service, c *kernel.Call, ref methodRef, fn func(i Integration) (*Integration, R, *awserr.Error)) (R, *awserr.Error) {
	return writeMethod(s, c, ref, func(_ *RestAPI, m Method) (*Method, R, *awserr.Error) {
		var zero R
		if m.Integration == nil {
			return nil, zero, errIntegrationNotFound()
		}
		next, v, err := fn(m.Integration.clone())
		if err != nil {
			return nil, zero, err
		}
		m.Integration = next
		return &m, v, nil
	})
}

// getIntegration answers a method's integration.
func (s *Service) getIntegration(_ context.Context, c *kernel.Call, in *apigatewayapi.GetIntegrationInput) (*apigatewayapi.Integration, *awserr.Error) {
	return readIntegration(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(i *Integration) (*apigatewayapi.Integration, *awserr.Error) {
		return new(i.output()), nil
	})
}

// deleteIntegration removes a method's integration with its responses.
func (s *Service) deleteIntegration(_ context.Context, c *kernel.Call, in *apigatewayapi.DeleteIntegrationInput) (*apigatewayapi.DeleteIntegrationOutput, *awserr.Error) {
	return writeIntegration(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(Integration) (*Integration, *apigatewayapi.DeleteIntegrationOutput, *awserr.Error) {
		return nil, &apigatewayapi.DeleteIntegrationOutput{}, nil
	})
}

// updateIntegration applies UpdateIntegration's patch operations, all or
// none, then checks the result as PutIntegration checks a request
// (apigateway-rest.md 3.8).
func (s *Service) updateIntegration(_ context.Context, c *kernel.Call, in *apigatewayapi.UpdateIntegrationInput) (*apigatewayapi.Integration, *awserr.Error) {
	ops := patch.FromModel(in.PatchOperations)
	return writeIntegration(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(i Integration) (*Integration, *apigatewayapi.Integration, *awserr.Error) {
		next, err := integrationPatch().Apply(i, ops)
		if err != nil {
			return nil, nil, err
		}
		if err := next.check(); err != nil {
			return nil, nil, err
		}
		return &next, new(next.output()), nil
	})
}

// integrationPatch is UpdateIntegration's path table (apigateway-rest.md
// 3.8). /type is not patchable, so it is an unknown path; /httpMethod is
// refused on MOCK. An unknown path answers "Invalid patch path <path>"
// and another op than replace on a replace-only path the
// OpsOneOf(replace) text, as for UpdateMethod (recorded there, inferred
// here, contract 6b section 5). The members VPC links, TLS and ALB targets
// need answer NotImplemented, as on PutIntegration.
func integrationPatch() patch.Table[Integration] {
	add, remove, replace := apigatewayapi.OpAdd, apigatewayapi.OpRemove, apigatewayapi.OpReplace
	addRemoveReplace := []apigatewayapi.Op{add, remove, replace}
	only := []apigatewayapi.Op{replace}
	notImplemented := func(name string) func(*Integration, patch.Operation, []string) *awserr.Error {
		return func(*Integration, patch.Operation, []string) *awserr.Error {
			return refuseUnimplemented("UpdateIntegration", member{name, true})
		}
	}
	rules := []patch.Rule[Integration]{
		{Path: "/uri", Ops: only, Apply: func(i *Integration, op patch.Operation, _ []string) *awserr.Error {
			i.URI = clonePtr(op.Value)
			return nil
		}},
		{Path: "/httpMethod", Ops: only, Apply: func(i *Integration, op patch.Operation, _ []string) *awserr.Error {
			if i.Type == apigatewayapi.IntegrationTypeMock {
				return errBadRequest("%s", patch.InvalidPath(op))
			}
			i.HTTPMethod = clonePtr(op.Value)
			return nil
		}},
		{Path: "/passthroughBehavior", Ops: only, Apply: func(i *Integration, op patch.Operation, _ []string) *awserr.Error {
			i.PassthroughBehavior = deref(op.Value, "")
			return nil
		}},
		{Path: "/contentHandling", Ops: only, Apply: func(i *Integration, op patch.Operation, _ []string) *awserr.Error {
			v, err := patchContentHandling(op)
			i.ContentHandling = v
			return err
		}},
		{Path: "/timeoutInMillis", Ops: only, Apply: func(i *Integration, op patch.Operation, _ []string) *awserr.Error {
			n, err := strconv.ParseInt(deref(op.Value, ""), 10, 32)
			if err != nil {
				// The text is inferred.
				return errBadRequest("Invalid patch value for %s: expected an integer", op.Path)
			}
			i.TimeoutInMillis = int32(n)
			return nil
		}},
		{Path: "/cacheNamespace", Ops: only, Apply: func(i *Integration, op patch.Operation, _ []string) *awserr.Error {
			i.CacheNamespace = deref(op.Value, "")
			return nil
		}},
		{Path: "/connectionType", Ops: only, Apply: func(i *Integration, op patch.Operation, _ []string) *awserr.Error {
			t := apigatewayapi.ConnectionType(deref(op.Value, ""))
			if err := checkConnectionType("UpdateIntegration", t); err != nil {
				return err
			}
			i.ConnectionType = &t
			return nil
		}},
		{Path: "/responseTransferMode", Ops: only, Apply: func(i *Integration, op patch.Operation, _ []string) *awserr.Error {
			m := apigatewayapi.ResponseTransferMode(deref(op.Value, ""))
			if !m.Known() {
				// The text is inferred.
				return errBadRequest("Invalid response transfer mode specified: %s. Valid options are BUFFERED,STREAM", m)
			}
			i.ResponseTransferMode = &m
			return nil
		}},
		{Path: "/connectionId", Ops: only, Apply: notImplemented("connectionId")},
		{Path: "/tlsConfig/insecureSkipVerification", Ops: only, Apply: notImplemented("tlsConfig")},
		{Path: "/integrationTarget", Ops: only, Apply: notImplemented("integrationTarget")},
		{Path: "/requestParameters/*", Ops: addRemoveReplace, OpText: patch.OpsAddRemoveReplace, Apply: func(i *Integration, op patch.Operation, wild []string) *awserr.Error {
			i.RequestParameters = patchString(i.RequestParameters, op, wild[0])
			return nil
		}},
		{Path: "/requestTemplates/*", Ops: addRemoveReplace, OpText: patch.OpsAddRemoveReplace, Apply: func(i *Integration, op patch.Operation, wild []string) *awserr.Error {
			i.RequestTemplates = patchString(i.RequestTemplates, op, wild[0])
			return nil
		}},
		{Path: "/cacheKeyParameters/*", Ops: addRemoveReplace, OpText: patch.OpsAddRemoveReplace, Apply: func(i *Integration, op patch.Operation, wild []string) *awserr.Error {
			// The key is the path segment, as for /binaryMediaTypes;
			// replace removes it and appends the value (finding 5's
			// list-as-object rule, inferred here).
			key := wild[0]
			keys := slices.DeleteFunc(slices.Clone(i.CacheKeyParameters), func(k string) bool { return k == key })
			if op.Op == add {
				keys = append(keys, key)
			} else if v := deref(op.Value, ""); op.Op == replace && v != "" {
				keys = append(keys, v)
			}
			if keys == nil {
				keys = []string{}
			}
			i.CacheKeyParameters = keys
			return nil
		}},
	}
	return patch.Table[Integration]{
		Rules:       rules,
		UnknownPath: patch.InvalidPath,
		OpText:      patch.OpsOneOf(replace),
		Clone:       func(i Integration) Integration { return i.clone() },
	}
}

// patchString applies add, replace or remove on key of a string map
// member; removing an absent key changes nothing (inferred).
func patchString(mp map[string]string, op patch.Operation, key string) map[string]string {
	if op.Op == apigatewayapi.OpRemove {
		return deleted(mp, key)
	}
	return with(mp, key, deref(op.Value, ""))
}

// patchContentHandling reads a contentHandling value; an absent or empty
// one clears it (inferred).
func patchContentHandling(op patch.Operation) (*apigatewayapi.ContentHandlingStrategy, *awserr.Error) {
	if deref(op.Value, "") == "" {
		return nil, nil
	}
	v := apigatewayapi.ContentHandlingStrategy(*op.Value)
	if !v.Known() {
		// The text is inferred.
		return nil, errBadRequest("Invalid content handling specified: %s. Valid options are CONVERT_TO_BINARY,CONVERT_TO_TEXT", v)
	}
	return &v, nil
}

// output is the integration response as the integration response
// operations answer it.
func (r *IntegrationResponse) output() apigatewayapi.IntegrationResponse {
	return apigatewayapi.IntegrationResponse{
		StatusCode: new(r.StatusCode), SelectionPattern: clonePtr(r.SelectionPattern), ResponseParameters: maps.Clone(r.ResponseParameters),
		ResponseTemplates: maps.Clone(r.ResponseTemplates), ContentHandling: clonePtr(r.ContentHandling),
	}
}

// putIntegrationResponse creates or replaces an integration response; the
// generated validator has already refused a status outside [1-5]\d\d
// (recorded, apigateway-rest.md 2.4). selectionPattern is echoed
// including "", and responseTemplates as {} when sent empty (recorded,
// 3.5). Whether AWS refuses a status with no method response is not
// recorded (3.5), so none is refused; replacing is inferred.
func (s *Service) putIntegrationResponse(_ context.Context, c *kernel.Call, in *apigatewayapi.PutIntegrationResponseInput) (*apigatewayapi.IntegrationResponse, *awserr.Error) {
	resp := IntegrationResponse{
		StatusCode: in.StatusCode, SelectionPattern: clonePtr(in.SelectionPattern), ResponseParameters: maps.Clone(in.ResponseParameters),
		ResponseTemplates: maps.Clone(in.ResponseTemplates), ContentHandling: clonePtr(in.ContentHandling),
	}
	return writeIntegration(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(i Integration) (*Integration, *apigatewayapi.IntegrationResponse, *awserr.Error) {
		i.Responses = with(i.Responses, resp.StatusCode, resp.clone())
		return &i, new(resp.output()), nil
	})
}

// getIntegrationResponse answers one integration response.
func (s *Service) getIntegrationResponse(_ context.Context, c *kernel.Call, in *apigatewayapi.GetIntegrationResponseInput) (*apigatewayapi.IntegrationResponse, *awserr.Error) {
	return readIntegration(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(i *Integration) (*apigatewayapi.IntegrationResponse, *awserr.Error) {
		r, ok := i.Responses[in.StatusCode]
		if !ok {
			return nil, errResponseNotFound()
		}
		return new(r.output()), nil
	})
}

// updateIntegrationResponse applies UpdateIntegrationResponse's patch
// operations, all or none (apigateway-rest.md 3.8).
func (s *Service) updateIntegrationResponse(_ context.Context, c *kernel.Call, in *apigatewayapi.UpdateIntegrationResponseInput) (*apigatewayapi.IntegrationResponse, *awserr.Error) {
	ops := patch.FromModel(in.PatchOperations)
	return writeIntegration(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(i Integration) (*Integration, *apigatewayapi.IntegrationResponse, *awserr.Error) {
		r, ok := i.Responses[in.StatusCode]
		if !ok {
			return nil, nil, errResponseNotFound()
		}
		r, err := integrationResponsePatch().Apply(r, ops)
		if err != nil {
			return nil, nil, err
		}
		i.Responses = with(i.Responses, in.StatusCode, r)
		return &i, new(r.output()), nil
	})
}

// integrationResponsePatch is UpdateIntegrationResponse's path table
// (apigateway-rest.md 3.8): replace /selectionPattern and
// /contentHandling; add, replace and remove /responseParameters/{key} and
// /responseTemplates/{escaped content type}. The refusal texts are those
// of UpdateMethod's alike paths (inferred here).
func integrationResponsePatch() patch.Table[IntegrationResponse] {
	addRemoveReplace := []apigatewayapi.Op{apigatewayapi.OpAdd, apigatewayapi.OpRemove, apigatewayapi.OpReplace}
	only := []apigatewayapi.Op{apigatewayapi.OpReplace}
	rules := []patch.Rule[IntegrationResponse]{
		{Path: "/selectionPattern", Ops: only, Apply: func(r *IntegrationResponse, op patch.Operation, _ []string) *awserr.Error {
			r.SelectionPattern = clonePtr(op.Value)
			return nil
		}},
		{Path: "/contentHandling", Ops: only, Apply: func(r *IntegrationResponse, op patch.Operation, _ []string) *awserr.Error {
			v, err := patchContentHandling(op)
			r.ContentHandling = v
			return err
		}},
		{Path: "/responseParameters/*", Ops: addRemoveReplace, OpText: patch.OpsAddRemoveReplace, Apply: func(r *IntegrationResponse, op patch.Operation, wild []string) *awserr.Error {
			r.ResponseParameters = patchString(r.ResponseParameters, op, wild[0])
			return nil
		}},
		{Path: "/responseTemplates/*", Ops: addRemoveReplace, OpText: patch.OpsAddRemoveReplace, Apply: func(r *IntegrationResponse, op patch.Operation, wild []string) *awserr.Error {
			r.ResponseTemplates = patchString(r.ResponseTemplates, op, wild[0])
			return nil
		}},
	}
	return patch.Table[IntegrationResponse]{
		Rules:       rules,
		UnknownPath: patch.InvalidPath,
		OpText:      patch.OpsOneOf(apigatewayapi.OpReplace),
		Clone:       func(r IntegrationResponse) IntegrationResponse { return r.clone() },
	}
}

// deleteIntegrationResponse removes one integration response; an unknown
// status answers the recorded NotFound text (apigateway-rest.md 2.4).
func (s *Service) deleteIntegrationResponse(_ context.Context, c *kernel.Call, in *apigatewayapi.DeleteIntegrationResponseInput) (*apigatewayapi.DeleteIntegrationResponseOutput, *awserr.Error) {
	return writeIntegration(s, c, methodRef{in.RestApiId, in.ResourceId, in.HttpMethod}, func(i Integration) (*Integration, *apigatewayapi.DeleteIntegrationResponseOutput, *awserr.Error) {
		if _, ok := i.Responses[in.StatusCode]; !ok {
			return nil, nil, errResponseNotFound()
		}
		i.Responses = deleted(i.Responses, in.StatusCode)
		return &i, &apigatewayapi.DeleteIntegrationResponseOutput{}, nil
	})
}
