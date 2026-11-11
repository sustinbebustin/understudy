package apigateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/httpevent"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/apigateway/resource"
	"github.com/sustinbebustin/understudy/internal/store"
)

// executeAPIPrefix is the execute-api path fallback,
// /_understudy/execute-api/<api-id>/<stage>/<path>.
const executeAPIPrefix = "/_understudy/execute-api/"

// servicePrincipal is whom API Gateway invokes a Lambda proxy integration
// as (apigateway-rest.md 5.4).
const servicePrincipal = "apigateway.amazonaws.com"

// maxEvent is the largest event Lambda accepts for a synchronous
// invocation, 6 MB (apigateway-rest.md 5.3).
const maxEvent = 6_291_456

// invoke is InvokeRestApi, the execute-api data plane of REST APIs
// (apigateway-rest.md 4, 5): it finds the API the host or
// path fallback names, the stage, the resource and method of the stage's
// deployment, checks that the function's resource policy lets API Gateway
// invoke it, invokes it synchronously with the REST 1.0 event and answers
// with the response read from its output. A request understudy cannot
// serve as AWS would answers 501; a failure of the emulator is returned
// for the edge to render.
func (s *Service) invoke(ctx context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	extended := string(ids.APIGatewayRequestIDFor(c.ID()))
	// Every response carries both ids (recorded, apigateway-rest.md 4.8).
	w.Header().Set("X-Amzn-Requestid", string(c.ID()))
	w.Header().Set("X-Amz-Apigw-Id", extended)
	// The size limit comes before any lookup.
	body, tooLarge, err := readBody(r, MaxBody)
	switch {
	case err != nil:
		return err
	case tooLarge:
		return writeAnswer(w, RequestTooLarge())
	}
	addr, ok := s.resolve(r)
	if !ok {
		return writeAnswer(w, RoutingSwitch().Value.UnknownAPI)
	}
	sel, advise := s.selectMethod(addr, r.Method)
	if advise != nil {
		c.Record(ctx, advise.owner, journal.Advisory{Kind: journal.AdvisoryAccessUnevaluated, Resource: restAPIARN(advise.owner, advise.apiID), Text: advise.text()})
	}
	switch sel := sel.(type) {
	case answered:
		return writeAnswer(w, sel.answer)
	case unemulated:
		return apigatewayapi.ErrNotImplemented(sel.message)
	case corrupt:
		return awserr.Internalf(sel.err)
	case toFunction:
		c.Touch(restAPIARN(sel.owner, sel.apiID))
		return s.invokeFunction(ctx, c, r, w, sel, body, extended)
	case toMock:
		c.Touch(restAPIARN(sel.owner, sel.apiID))
		return runMock(w, r, sel, body)
	}
	return awserr.Internalf(errors.New("apigateway: selectMethod returned no selection"))
}

// address is what a request names: an API id, the escaped path the API
// sees (stage segment included) and the host the request named.
type address struct {
	apiID string
	path  string
	// host is the execute-api host, lowercase and without its port; "" on
	// the path fallback.
	host string
	// region is the region the host names; zero when it names none.
	region aws.Region
}

// resolve reads what a request names: the path fallback
// /_understudy/execute-api/<api-id>/<path>, else the host
// <api-id>.execute-api.<region>.<base> or <api-id>.execute-api.<base>.
// The API id is lowercased, as a host name is.
func (s *Service) resolve(r *http.Request) (address, bool) {
	escaped := escapedPath(r)
	if rest, ok := strings.CutPrefix(escaped, executeAPIPrefix); ok {
		id, path, _ := strings.Cut(rest, "/")
		if id == "" {
			return address{}, false
		}
		return address{apiID: strings.ToLower(id), path: "/" + path}, true
	}
	host := hostName(r.Host)
	labels := strings.SplitN(host, ".", 3)
	if len(labels) != 3 || labels[0] == "" || labels[1] != "execute-api" || labels[2] == "" {
		return address{}, false
	}
	addr := address{apiID: labels[0], path: escaped, host: host}
	if addr.path == "" {
		addr.path = "/"
	}
	if first, after, ok := strings.Cut(labels[2], "."); ok && after != "" {
		if region, err := s.regions.Parse(first); err == nil {
			addr.region = region
		}
	}
	return addr, true
}

// domain is the event's domainName: the host the request named, or for the
// path fallback the host form under localhost in the API's region.
func (a address) domain(owner aws.Scope) string {
	if a.host != "" {
		return a.host
	}
	return a.apiID + ".execute-api." + owner.Region.String() + ".localhost"
}

// hostName is a Host header without its port, lowercase.
func hostName(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}

// selection is what a request selects in the state, copied out of it.
//
//sumtype:decl
type selection interface{ isSelection() }

// answered is a request the gateway answers itself.
type answered struct{ answer GatewayAnswer }

// toFunction is a request a method sends to its Lambda proxy integration.
type toFunction struct {
	owner      aws.Scope
	apiID      string
	stage      string
	deployment string
	resourceID string
	// resourcePath is the matched resource's template, such as /.
	resourcePath string
	// methodKey is the method resource that serves: the verb, or ANY.
	methodKey string
	// pathParameters are the resource's bound values, as sent.
	pathParameters map[string]string
	rawPath        string
	domain         string
	stageVariables map[string]string
	integration    Integration
}

// unemulated is a request understudy cannot serve as AWS would, answered
// 501 with message: an AWS-shaped 500 would read as a real configuration
// error.
type unemulated struct{ message string }

// corrupt is state the service cannot have written: an understudy bug.
type corrupt struct{ err error }

func (answered) isSelection()   {}
func (toFunction) isSelection() {}
func (unemulated) isSelection() {}
func (corrupt) isSelection()    {}

// policyAdvisory is an invocation of an API with a resource policy under
// iam.enforce=advise: served, with an access-unevaluated advisory naming
// the API (apigateway-rest.md 12.2).
type policyAdvisory struct {
	owner aws.Scope
	apiID string
}

func (p *policyAdvisory) text() string {
	return "understudy does not evaluate REST API resource policies: REST API " + p.apiID +
		" has a policy, which AWS would evaluate for execute-api:Invoke on this request; iam.enforce is advise, so understudy served the request"
}

// selectMethod finds the API addr names in any account, its stage, and the
// resource and method of the stage's deployment that serve method
// (apigateway-rest.md 4.1-4.4), checking in order the API, its live
// settings, the stage, the deployment snapshot, the resource policy and
// the route. A host naming another region than the API's is the routing
// switch's UnknownAPI. A non-nil advisory is to be recorded whatever the
// selection.
func (s *Service) selectMethod(addr address, method string) (selection, *policyAdvisory) {
	owner, ok := s.index.lookup(addr.apiID)
	if !ok || (!addr.region.IsZero() && addr.region != owner.Region) {
		return answered{RoutingSwitch().Value.UnknownAPI}, nil
	}
	return store.View2(s.cells, owner, func(st *State) (selection, *policyAdvisory) {
		a, ok := st.APIs[addr.apiID]
		if !ok {
			return answered{RoutingSwitch().Value.UnknownAPI}, nil
		}
		target := resource.Normalize(addr.path)
		stage, d, refused := a.front(target.Stage)
		if refused != nil {
			return refused, nil
		}
		var advise *policyAdvisory
		if a.Policy != nil {
			switch s.enforce {
			case EnforceEnforce:
				return unemulated{fmt.Sprintf("understudy does not evaluate REST API resource policies: REST API %s has a policy and iam.enforce is enforce, "+
					"so the request is refused rather than served unchecked; no integration ran and nothing was changed. "+
					"Remove the policy with UpdateRestApi replace /policy, or run with iam.enforce advise or off.", a.ID)}, nil
			case EnforceAdvise:
				advise = &policyAdvisory{owner: owner, apiID: a.ID}
			case EnforceOff:
			}
		}
		return a.route(owner, addr, method, target.Path, stage, d), advise
	})
}

// front checks what applies before routing: the
// live settings understudy cannot serve, the stage, and the stage's
// deployment snapshot. A non-nil selection answers the request.
func (a *RestAPI) front(stageName string) (Stage, Deployment, selection) {
	if a.DisableExecuteAPIEndpoint {
		// No REST source says what AWS answers or when it takes effect, so
		// understudy refuses at once rather than serve.
		return Stage{}, Deployment{}, unemulated{fmt.Sprintf("understudy does not emulate a disabled default endpoint: REST API %s has disableExecuteApiEndpoint true, "+
			"so no integration ran and nothing was changed. Set it to false with UpdateRestApi replace /disableExecuteApiEndpoint.", a.ID)}
	}
	if a.Endpoint.Type == apigatewayapi.EndpointTypePrivate {
		// AWS serves a PRIVATE API only through VPC endpoints, never on a
		// public host.
		return Stage{}, Deployment{}, unemulated{fmt.Sprintf("understudy does not serve PRIVATE APIs: REST API %s has endpoint type PRIVATE, which AWS serves only through VPC endpoints, "+
			"so no integration ran and nothing was changed. Use a REGIONAL or EDGE endpoint configuration.", a.ID)}
	}
	stage, _, ok := a.stage(stageName)
	if !ok {
		return Stage{}, Deployment{}, answered{RoutingSwitch().Value.UnknownStage}
	}
	d, ok := a.deployment(stage.Deployment)
	if !ok {
		return Stage{}, Deployment{}, corrupt{fmt.Errorf("apigateway: stage %q of REST API %s serves deployment %s, which the API does not hold", stage.Name, a.ID, stage.Deployment)}
	}
	if len(d.BinaryMediaTypes) > 0 {
		// They decide the event's isBase64Encoded and whether a response
		// body is decoded (apigateway-rest.md 5.1, 5.2), which understudy
		// does not apply.
		return Stage{}, Deployment{}, unemulated{fmt.Sprintf("understudy does not apply binaryMediaTypes: deployment %s of REST API %s, which stage %s serves, has binaryMediaTypes %v, "+
			"so no integration ran and nothing was changed. Remove the binaryMediaTypes with UpdateRestApi and deploy again.", d.ID, a.ID, stage.Name, d.BinaryMediaTypes)}
	}
	return stage, d, nil
}

// route finds the resource and method of the deployment d, which stage
// serves, for method on path.
func (a *RestAPI) route(owner aws.Scope, addr address, method, path string, stage Stage, d Deployment) selection {
	routing := RoutingSwitch().Value
	res, found, ok := match(d.Resources, method, path, routing.Methods)
	if !ok {
		return answered{MissingAuthenticationToken()}
	}
	key := found.Method
	m := res.Methods[key]
	if !isNoAuthorization(m.Authorization) {
		// AWS evaluates execute-api:Invoke for the caller here
		// (apigateway-rest.md 12.2); serving the request unchecked would fake
		// that.
		return unemulated{fmt.Sprintf("understudy does not evaluate IAM authorization of requests: method %s %s of REST API %s has authorizationType %s, "+
			"so no integration ran and nothing was changed. Set the method's authorizationType to NONE.", key, res.Path, a.ID, m.Authorization)}
	}
	if m.APIKeyRequired {
		// No API key can be created, so no request carries a valid one.
		return answered{APIKeyRequired()}
	}
	if m.Integration == nil {
		return corrupt{fmt.Errorf("apigateway: method %s %s of deployment %s of REST API %s has no integration, which CreateDeployment refuses", key, res.Path, d.ID, a.ID)}
	}
	if what, fix, ok := m.Integration.unserved(); ok {
		return unemulated{fmt.Sprintf("understudy does not %s: method %s %s of REST API %s, deployment %s, reaches it, so no integration ran and nothing was changed. %s",
			what, key, res.Path, a.ID, d.ID, fix)}
	}
	if m.Integration.Type == apigatewayapi.IntegrationTypeMock {
		return toMock{owner: owner, apiID: a.ID, resourcePath: res.Path, methodKey: key, integration: m.Integration.clone()}
	}
	if deref(m.Integration.HTTPMethod, "") != http.MethodPost {
		// A Lambda proxy invoked with another method than POST is a
		// configuration error (recorded, apigateway-rest.md finding 10).
		return answered{ConfigurationError()}
	}
	return toFunction{
		owner: owner, apiID: a.ID, stage: stage.Name, deployment: d.ID, resourceID: res.ID, resourcePath: res.Path, methodKey: key,
		pathParameters: found.PathParameters, rawPath: addr.path, domain: addr.domain(owner), stageVariables: maps.Clone(stage.Variables), integration: *m.Integration,
	}
}

// unserved names what understudy cannot run of an integration, and how to
// change it, or reports false for an AWS_PROXY integration it runs.
// credentials come first: AWS assumes the role instead of checking the
// function policy (apigateway-rest.md 3.4, 5.4), so the answer precedes
// any permission check.
func (i *Integration) unserved() (what, fix string, ok bool) {
	switch {
	case i.Credentials != nil:
		return "assume integration credentials (role " + *i.Credentials + ")",
			"Remove the integration's credentials and allow apigateway.amazonaws.com with lambda add-permission instead.", true
	case i.Type != apigatewayapi.IntegrationTypeAwsProxy && i.Type != apigatewayapi.IntegrationTypeMock:
		return "run " + string(i.Type) + " integrations yet", "Use an AWS_PROXY integration to a Lambda function or a MOCK integration.", true
	case i.usesStageVariable():
		return "resolve stageVariables in an integration uri (" + deref(i.URI, "") + ")", "Name the function in the uri directly.", true
	case deref(i.ResponseTransferMode, "") == apigatewayapi.ResponseTransferModeStream:
		return "stream integration responses (responseTransferMode STREAM)", "Set responseTransferMode to BUFFERED.", true
	case i.ContentHandling != nil:
		return "apply an integration's contentHandling (" + string(*i.ContentHandling) + ")", "Remove contentHandling from the integration.", true
	}
	return "", "", false
}

// match finds the deployed resource and method that serve method on the
// resource path (resource.Find; apigateway-rest.md 4.3, 4.4). Only the
// documented verbs reach a method: ANY serves those and is not itself a
// request verb (4.4).
func match(rs []Resource, method, path string, m resource.Methods) (Resource, resource.Match, bool) {
	if method == "ANY" || !slices.Contains(httpMethods(), method) {
		return Resource{}, resource.Match{}, false
	}
	deployed := make([]resource.Resource, len(rs))
	for i, r := range rs {
		deployed[i] = resource.Resource{ID: r.ID, Path: r.Path, Methods: slices.Collect(maps.Keys(r.Methods))}
	}
	found, ok := resource.Find(deployed, method, path, m)
	if !ok {
		return Resource{}, resource.Match{}, false
	}
	i := slices.IndexFunc(rs, func(r Resource) bool { return r.ID == found.ResourceID })
	if i < 0 {
		return Resource{}, resource.Match{}, false
	}
	return rs[i], found, true
}

// invokeFunction runs a method's Lambda proxy integration: the resource
// policy check, the REST 1.0 event, the synchronous invocation and the
// response read from its output (apigateway-rest.md 5).
func (s *Service) invokeFunction(ctx context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter, t toFunction, body []byte, extended string) *awserr.Error {
	fn, ok := lambdaFunction(deref(t.integration.URI, ""))
	if !ok {
		return awserr.Internalf(fmt.Errorf("apigateway: an integration of REST API %s holds URI %q, which PutIntegration could not have accepted", t.apiID, deref(t.integration.URI, "")))
	}
	fnScope, serr := aws.ScopeOf(fn)
	if serr != nil {
		return awserr.Internalf(fmt.Errorf("apigateway: an integration of REST API %s names function %s: %w", t.apiID, fn, serr))
	}
	source := sourceARN(t.owner, t.apiID, t.stage, t.methodKey, t.resourcePath)
	allowed, err := s.permitted(fn, source, t.owner)
	if err != nil {
		return err
	}
	if !allowed {
		return writeAnswer(w, PermissionSwitch().Value.Denial)
	}
	event, eerr := httpevent.EventV1(eventRequest(c, r, t.rawPath, body), httpevent.Context{
		AccountID:      t.owner.Account.String(),
		APIID:          t.apiID,
		DomainName:     t.domain,
		DomainPrefix:   t.apiID,
		Stage:          t.stage,
		PathParameters: t.pathParameters,
		RequestID:      string(c.ID()),
		StageVariables: t.stageVariables,
		At:             c.Admitted().Wire(),
		TraceID:        c.Trace(),
		Authorizer:     httpevent.NoAuthorizer{},
	}, httpevent.REST{
		ResourceID: t.resourceID, ResourcePath: t.resourcePath, DeploymentID: t.deployment, ExtendedRequestID: extended,
		BodylessEmpty: !ProxyUnrecordedSwitch().Value.BodylessNull, BodylessBase64: ProxyUnrecordedSwitch().Value.BodylessBase64,
	})
	if eerr != nil {
		return awserr.Internalf(fmt.Errorf("apigateway: encoding the request event for REST API %s: %w", t.apiID, eerr))
	}
	if len(event) > maxEvent {
		return writeAnswer(w, EventTooLarge())
	}
	var out *lambdaapi.InvokeOutput
	switch res := s.invokeWithin(ctx, c, time.Duration(t.integration.TimeoutInMillis)*time.Millisecond, kernel.Target{
		Scope: fnScope, As: kernel.AsService{Name: servicePrincipal, Source: source}, Via: ref.ViaAPIGateway,
	}, &lambdaapi.InvokeInput{FunctionName: fn.String(), InvocationType: new(lambdaapi.InvocationTypeRequestResponse), Payload: event}).(type) {
	case clientGone:
		return awserr.RequestAborted()
	case timedOut:
		return writeAnswer(w, ProxyUnrecordedSwitch().Value.Timeout)
	case lambdaRefusal:
		return lambdaRefused(w, t, res.err)
	case lambdaOutput:
		out = res.out
	}
	if out == nil {
		return awserr.Internalf(fmt.Errorf("apigateway: Lambda's Invoke of %s answered neither an output nor an error", fn))
	}
	if out.FunctionError != nil {
		return writeAnswer(w, ProxyFailure())
	}
	resp, perr := httpevent.InferV1For(out.Payload, httpevent.REST{})
	if perr != nil {
		s.k.Log().LogAttrs(ctx, slog.LevelWarn, "API Gateway answered the function's malformed proxy response with 502",
			slog.String("api", t.apiID), slog.String("resource", t.resourcePath), slog.String("problem", perr.Error()))
		return writeAnswer(w, ProxyFailure())
	}
	resp.Write(w)
	return nil
}

// waited is how the wait for an invocation ended.
//
//sumtype:decl
type waited interface{ isWaited() }

// lambdaOutput is Lambda's output for the invocation.
type lambdaOutput struct{ out *lambdaapi.InvokeOutput }

// lambdaRefusal is the error Lambda refused the invocation with.
type lambdaRefusal struct{ err *awserr.Error }

// timedOut is an invocation still running when the integration's timeout
// passed.
type timedOut struct{}

// clientGone is a client that went away before Lambda answered.
type clientGone struct{}

func (lambdaOutput) isWaited()  {}
func (lambdaRefusal) isWaited() {}
func (timedOut) isWaited()      {}
func (clientGone) isWaited()    {}

// invokeWithin invokes the function and waits for Lambda's answer at most
// timeout of wall time: the integration timeout bounds real work in a
// container, so a frozen or scaled emulator clock neither stretches nor
// shortens it (ARCHITECTURE.md 9). The invocation runs as tracked work and
// outlives the wait; past the timeout its answer is dropped (inferred,
// APIGW1-10).
func (s *Service) invokeWithin(ctx context.Context, c *kernel.Call, timeout time.Duration, t kernel.Target, in *lambdaapi.InvokeInput) waited {
	done := make(chan waited, 1)
	s.k.Go(kernel.InvocationRunning, func(work context.Context) { //nolint:contextcheck // the invocation outlives the request as tracked work of the kernel
		out, err := kernel.Invoke(work, s.k, c, lambdaapi.OpInvoke, t, in)
		if err != nil {
			done <- lambdaRefusal{err}
			return
		}
		done <- lambdaOutput{out}
	})
	wait, stop := context.WithCancel(ctx)
	defer stop()
	clk := s.k.Clock()
	expired := clk.Until(wait, clk.WallDeadline(clk.Wall().Add(timeout)))
	select {
	case r := <-done:
		return r
	case <-expired:
		if ctx.Err() != nil {
			return clientGone{}
		}
		return timedOut{}
	}
}

// lambdaRefused answers an invocation Lambda refused: Lambda without
// Docker is understudy's limit, so 501 naming it; any other refusal is the
// proxy failure 502 (apigateway-rest.md 5.3). An emulator failure is
// returned for the edge to render.
func lambdaRefused(w http.ResponseWriter, t toFunction, err *awserr.Error) *awserr.Error {
	if _, internal := err.Origin.(awserr.Internal); internal {
		return err
	}
	if err.ID.Status == http.StatusNotImplemented {
		return apigatewayapi.ErrNotImplemented(fmt.Sprintf(
			"understudy could not run the Lambda proxy integration of REST API %s, method %s %s: the function did not run and nothing was changed. Lambda answered: %s",
			t.apiID, t.methodKey, t.resourcePath, err.Message))
	}
	return writeAnswer(w, ProxyFailure())
}

// sourceARN is the aws:SourceArn API Gateway presents to the function's
// resource policy (switch apigateway.lambda.permission, apigateway-rest.md
// 5.4): arn:<partition>:execute-api:<api region>:<api account>:<api-id>/
// <stage>/<METHOD>/<resource path template without its leading slash>.
func sourceARN(owner aws.Scope, apiID, stage, method, resourcePath string) aws.ARN {
	return aws.ARN{
		Partition: owner.Partition.String(), Service: "execute-api", Region: owner.Region.String(), Account: owner.Account.String(),
		Resource: apiID + "/" + stage + "/" + method + "/" + strings.TrimPrefix(resourcePath, "/"),
	}
}

// invokeFunctionAction is the action the function's resource policy must
// allow API Gateway (apigateway-rest.md 5.4).
func invokeFunctionAction() registry.IAMAction {
	return registry.IAMAction{Prefix: "lambda", Name: "InvokeFunction"}
}

// errNoFunctionPolicies is a service built without Options.FunctionPolicies.
var errNoFunctionPolicies = errors.New("apigateway: no Options.FunctionPolicies was given, so no function's resource policy can be read; wire.go passes the Lambda service")

// permitted reports whether fn's resource policy lets API Gateway invoke
// it for source (apigateway-rest.md 5.4). It applies under every
// iam.enforce mode; a function that does not exist allows nothing.
func (s *Service) permitted(fn, source aws.ARN, owner aws.Scope) (bool, *awserr.Error) {
	if s.policies == nil {
		return false, awserr.Internalf(errNoFunctionPolicies)
	}
	doc, ok := s.policies.ResourcePolicy(fn)
	if !ok {
		return false, nil
	}
	_, allowed := authz.CheckResource(doc, authz.ResourceRequest{
		Caller:   policydoc.ServiceCaller{Name: servicePrincipal},
		Action:   invokeFunctionAction(),
		Resource: fn.String(),
		Keys:     map[string]string{"aws:SourceArn": source.String(), "aws:SourceAccount": owner.Account.String()},
	}).(policydoc.Allowed)
	return allowed, nil
}

// readBody reads a request's body, reporting one over limit bytes as too
// large without keeping it.
func readBody(r *http.Request, limit int) (body []byte, tooLarge bool, err *awserr.Error) {
	if r.Body == nil {
		return nil, false, nil
	}
	b, rerr := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if rerr != nil {
		return nil, false, awserr.RequestAborted()
	}
	if len(b) > limit {
		return nil, true, nil
	}
	return b, false, nil
}

// eventRequest is the request as the event reports it, rawPath being the
// escaped path the API sees, stage segment included.
func eventRequest(c *kernel.Call, r *http.Request, rawPath string, body []byte) httpevent.Request {
	e := c.Endpoint()
	scheme := e.Scheme
	if scheme == "" {
		scheme = "http"
	}
	port := "80"
	if scheme == "https" {
		port = "443"
	}
	host := e.Host
	if host == "" {
		host = r.Host
	}
	if _, p, err := net.SplitHostPort(host); err == nil && p != "" {
		port = p
	}
	source := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		source = h
	}
	return httpevent.Request{
		Method: r.Method, RawPath: rawPath, RawQuery: r.URL.RawQuery, Protocol: r.Proto,
		Host: r.Host, Header: r.Header, SourceIP: source, Scheme: scheme, Port: port, Body: body,
	}
}

// escapedPath is the escaped path exactly as the client sent it: the
// request target up to its query.
func escapedPath(r *http.Request) string {
	if strings.HasPrefix(r.RequestURI, "/") {
		path, _, _ := strings.Cut(r.RequestURI, "?")
		return path
	}
	return r.URL.EscapedPath()
}

// writeAnswer writes a response the gateway gives itself:
// the answer's status and exact body, Content-Type application/json and
// x-amzn-ErrorType when the answer has one (apigateway-rest.md 4.7, 4.8).
func writeAnswer(w http.ResponseWriter, a GatewayAnswer) *awserr.Error {
	w.Header().Set("Content-Type", "application/json")
	if a.ErrorType != "" {
		w.Header().Set("X-Amzn-Errortype", a.ErrorType)
	}
	w.WriteHeader(a.Status)
	// A failed write means the client went away; nothing else can be sent.
	_, _ = io.WriteString(w, a.Body) //nolint:errcheck // the client is gone when this fails
	return nil
}
