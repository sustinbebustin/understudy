package apigatewayv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/httpevent"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2/route"
)

// executeAPIPrefix is the execute-api path fallback,
// /_understudy/execute-api/<api-id>/<path> (apigateway-http.md 11.3).
const executeAPIPrefix = "/_understudy/execute-api/"

// servicePrincipal is whom API Gateway invokes a Lambda proxy integration
// as (apigateway-http.md 5.5).
const servicePrincipal = "apigateway.amazonaws.com"

// invoke is InvokeApi, the execute-api data plane (apigateway-http.md
// 13.3): it finds the API the path fallback names, selects the stage and
// the route of the stage's deployment, checks that the function's resource
// policy lets API Gateway invoke it, invokes it synchronously with the
// payload format 2.0 event and answers with the response read from its
// output. The answers the gateway gives itself are the switches'. A
// request understudy cannot serve as AWS would is answered 501, and a
// failure of the emulator is returned for the edge to render.
func (s *Service) invoke(ctx context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	requestID := string(ids.APIGatewayRequestIDFor(c.ID()))
	w.Header().Set("Apigw-Requestid", requestID)
	failures := FailureAnswersSwitch().Value
	// The payload quota comes first, before the API is looked up
	// (apigateway-http.md 4.7).
	body, tooLarge, err := readBody(r, failures.MaxBody)
	switch {
	case err != nil:
		return err
	case tooLarge:
		return writeAnswer(w, failures.TooLarge)
	}
	addr, ok := resolvePath(escapedPath(r))
	if !ok {
		return writeAnswer(w, FrontAnswersSwitch().Value.UnknownAPI)
	}
	switch sel := s.selectRoute(addr, r.Method, r.Header.Get("Origin") != "").(type) {
	case answered:
		return writeAnswer(w, sel.answer)
	case unemulated:
		return apigatewayv2api.ErrNotImplemented(sel.message)
	case corrupt:
		return awserr.Internalf(sel.err)
	case toFunction:
		c.Touch(apiARN(sel.owner, sel.apiID))
		return s.invokeFunction(ctx, c, r, w, sel, body, requestID)
	}
	return awserr.Internalf(errors.New("apigatewayv2: selectRoute returned no selection"))
}

// address is what the path fallback names: an API id and the escaped path
// after it, "/" when there is none.
type address struct {
	apiID string
	path  string
}

// resolvePath reads the path fallback; the API id is lowercased, as a
// host name is.
func resolvePath(escaped string) (address, bool) {
	rest, ok := strings.CutPrefix(escaped, executeAPIPrefix)
	if !ok {
		return address{}, false
	}
	id, path, _ := strings.Cut(rest, "/")
	if id == "" {
		return address{}, false
	}
	return address{apiID: strings.ToLower(id), path: "/" + path}, true
}

// selection is what a request selects in the state, copied out of it.
//
//sumtype:decl
type selection interface{ isSelection() }

// answered is a request the gateway answers itself.
type answered struct{ answer GatewayAnswer }

// toFunction is a request a route sends to its Lambda proxy integration.
type toFunction struct {
	// owner is the API's account and region.
	owner aws.Scope
	apiID string
	stage string
	// key is the route's parsed key, routeKey the key as stored.
	key      route.Key
	routeKey string
	// rawPath is the escaped path after the API id, a named stage's
	// segment included (apigateway-http.md finding 8).
	rawPath     string
	params      map[string]string
	integration Integration
}

// unemulated is a request understudy cannot serve as AWS would, answered
// 501 with message (the unit's conflict row 3).
type unemulated struct{ message string }

// corrupt is state the service cannot have written: an understudy bug.
type corrupt struct{ err error }

func (answered) isSelection()   {}
func (toFunction) isSelection() {}
func (unemulated) isSelection() {}
func (corrupt) isSelection()    {}

// selectRoute finds the API addr names, its stage and the route of the
// stage's deployment a request with method matches; origin is whether the
// request carries an Origin header. The API's DisableExecuteApiEndpoint
// and CORS configuration are read live, so they hold from the call that
// set them (apigateway-http.md 3.1, the front.answers switch).
func (s *Service) selectRoute(addr address, method string, origin bool) selection {
	owner, ok := s.index.lookup(addr.apiID)
	if !ok {
		return answered{FrontAnswersSwitch().Value.UnknownAPI}
	}
	return readState(s, owner, func(st *State) selection {
		a, ok := st.APIs[addr.apiID]
		if !ok {
			return answered{FrontAnswersSwitch().Value.UnknownAPI}
		}
		return a.selectRoute(owner, addr, method, origin)
	})
}

// selectRoute is Service.selectRoute on the API addr names, which owner
// holds.
func (a *API) selectRoute(owner aws.Scope, addr address, method string, origin bool) selection {
	front := FrontAnswersSwitch().Value
	if refused := a.refuse(origin); refused != nil {
		return refused
	}
	stage, path, ok := a.selectStage(addr.path, method)
	if !ok {
		return answered{front.NoStage}
	}
	if stage.Deployment == "" {
		return answered{front.NoDeployment}
	}
	d, ok := a.deployment(stage.Deployment)
	if !ok {
		return corrupt{fmt.Errorf("apigatewayv2: stage %q of API %s serves deployment %s, which the API does not hold", stage.Name, a.ID, stage.Deployment)}
	}
	table, err := d.table()
	if err != nil {
		return corrupt{err}
	}
	m, ok := table.Match(method, path, RouteMatchingSwitch().Value.Matching)
	if !ok {
		return answered{front.NoRoute}
	}
	i := slices.IndexFunc(table, func(r route.Route) bool { return r.ID == m.RouteID })
	if i < 0 {
		return corrupt{fmt.Errorf("apigatewayv2: route.Table.Match selected route %s, which deployment %s of API %s does not hold", m.RouteID, d.ID, a.ID)}
	}
	// table holds d.Routes' keys parsed, in d.Routes' order.
	rt, key := d.Routes[i], table[i].Key
	if rt.Integration == "" {
		return answered{RouteMatchingSwitch().Value.NoTarget}
	}
	if rt.Authorization == apigatewayv2api.AuthorizationTypeAwsIam {
		// AWS evaluates execute-api:Invoke for the caller here
		// (apigateway-http.md 8.3); serving the request unchecked would fake
		// that.
		return unemulated{fmt.Sprintf("understudy does not evaluate IAM authorization of requests: route %q of API %s has authorizationType AWS_IAM, "+
			"so no integration ran and nothing was changed. Set the route's authorizationType to NONE.", rt.Key, a.ID)}
	}
	j := slices.IndexFunc(d.Integrations, func(x Integration) bool { return x.ID == rt.Integration })
	if j < 0 {
		return corrupt{fmt.Errorf("apigatewayv2: route %s of deployment %s of API %s targets integration %s, which the deployment does not hold", rt.ID, d.ID, a.ID, rt.Integration)}
	}
	integration := d.Integrations[j]
	if what, instead := integration.unemulated(); what != "" {
		return unemulated{fmt.Sprintf("understudy does not run %s: route %q of API %s targets integration %s, so no integration ran and nothing was changed. %s",
			what, rt.Key, a.ID, integration.ID, instead)}
	}
	return toFunction{
		owner: owner, apiID: a.ID, stage: stage.Name, key: key, routeKey: rt.Key,
		rawPath: addr.path, params: m.PathParameters, integration: integration,
	}
}

// unemulated names what of the integration understudy cannot run as AWS
// would, and what to change instead; what is "" for a Lambda proxy
// integration of payload format 2.0 with nothing a request would need
// applied (the unit's scope: these are stored and answer 501 when a
// request reaches them).
func (i *Integration) unemulated() (what, instead string) {
	switch {
	case i.Type == apigatewayv2api.IntegrationTypeHttpProxy:
		return "HTTP proxy integrations (integrationType HTTP_PROXY)", "Target a Lambda proxy integration (AWS_PROXY) instead."
	case i.Subtype != nil:
		return "AWS service integrations (integrationSubtype " + *i.Subtype + ")", "Target a Lambda proxy integration that calls the service instead."
	case i.ConnectionType == apigatewayv2api.ConnectionTypeVpcLink:
		return "private integrations (connectionType VPC_LINK)", "Target a Lambda proxy integration with connectionType INTERNET instead."
	case deref(i.CredentialsARN, "") != "":
		// An empty credentialsArn names no role: it is what Terraform sends
		// to remove one (apigateway-http.md 9.3).
		return "integration credentials (credentialsArn)",
			"Remove the integration's credentialsArn and let API Gateway invoke the function through its resource policy (lambda add-permission)."
	case len(i.RequestParameters) > 0:
		return "parameter mapping (requestParameters)", "Remove the integration's requestParameters."
	case len(i.ResponseParameters) > 0:
		return "parameter mapping (responseParameters)", "Remove the integration's responseParameters."
	case i.URI != nil && strings.Contains(*i.URI, stageVariableRef):
		return "stage variables in integrationUri", "Name the function in the integration's integrationUri."
	case i.PayloadFormatVersion == payloadV1:
		return "payload format 1.0 yet", "Use payloadFormatVersion 2.0."
	}
	return "", ""
}

// refuse is how the API's own settings answer a request before any stage
// is selected, nil when they let it through: a disabled default endpoint
// is the front.answers switch's DisabledEndpoint, and a request with an
// Origin header (origin) to an API with a CORS configuration answers 501.
func (a *API) refuse(origin bool) selection {
	switch {
	case a.DisableExecuteAPIEndpoint:
		return answered{FrontAnswersSwitch().Value.DisabledEndpoint}
	case a.CORS != nil && origin:
		// AWS answers a CORS request from the API's configuration
		// (apigateway-http.md 4.6); understudy does not apply it, so serving
		// the request would fake its CORS headers.
		return unemulated{fmt.Sprintf("understudy does not apply CORS: API %s has a corsConfiguration and the request carries an Origin header, "+
			"so no integration ran and nothing was changed. Send the request without an Origin header, or remove the API's corsConfiguration (DeleteCorsConfiguration).", a.ID)}
	}
	return nil
}

// selectStage selects the stage a request path names and the path its
// routes see (apigateway-http.md 4.2): a first segment equal to a named
// stage's name selects it and is consumed, so /prod and /prod/ route as /;
// otherwise the $default stage serves the whole path. Whether a named
// stage's segment shadows a $default route of the same path is the
// apigatewayv2.front.answers switch's StageSegmentWins (APIGW2-06).
func (a *API) selectStage(path, method string) (Stage, string, bool) {
	def, hasDefault := a.stage(defaultStage)
	first, rest, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	if named, ok := a.stage(first); ok && first != defaultStage {
		if FrontAnswersSwitch().Value.StageSegmentWins || !hasDefault || !a.serves(def, path, method) {
			return named, "/" + rest, true
		}
	}
	return def, path, hasDefault
}

// serves reports whether a route of the stage's deployment matches a
// request.
func (a *API) serves(st Stage, path, method string) bool {
	d, ok := a.deployment(st.Deployment)
	if !ok {
		return false
	}
	table, err := d.table()
	if err != nil {
		return false
	}
	_, ok = table.Match(method, path, RouteMatchingSwitch().Value.Matching)
	return ok
}

// table is the deployment's routes as route.Table, in creation order.
func (d *Deployment) table() (route.Table, error) {
	t := make(route.Table, 0, len(d.Routes))
	for _, r := range d.Routes {
		k, err := route.ParseKey(r.Key)
		if err != nil {
			return nil, fmt.Errorf("apigatewayv2: route %s of deployment %s holds a key CreateRoute could not have accepted: %w", r.ID, d.ID, err)
		}
		t = append(t, route.Route{ID: r.ID, Key: k})
	}
	return t, nil
}

// invokeFunction runs a request's Lambda proxy integration: the resource
// policy check, the payload format 2.0 event, the synchronous invocation
// and the response read from its output.
func (s *Service) invokeFunction(ctx context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter, t toFunction, body []byte, requestID string) *awserr.Error {
	uri := deref(t.integration.URI, "")
	fn, ok := lambdaFunction(uri)
	if !ok {
		return awserr.Internalf(fmt.Errorf("apigatewayv2: integration %s of API %s holds URI %q, which CreateIntegration could not have accepted", t.integration.ID, t.apiID, uri))
	}
	fnScope, serr := aws.ScopeOf(fn)
	if serr != nil {
		return awserr.Internalf(fmt.Errorf("apigatewayv2: integration %s of API %s names function %s: %w", t.integration.ID, t.apiID, fn, serr))
	}
	source := t.sourceARN()
	allowed, err := s.permitted(fn, source, t.owner)
	if err != nil {
		return err
	}
	if !allowed {
		return writeAnswer(w, PermissionSwitch().Value.Denial)
	}
	failures := FailureAnswersSwitch().Value
	event, eerr := httpevent.EventV2(eventRequest(c, r, t.rawPath, body), httpevent.Context{
		AccountID:    t.owner.Account.String(),
		APIID:        t.apiID,
		DomainName:   t.apiID + ".execute-api." + t.owner.Region.String() + ".localhost",
		DomainPrefix: t.apiID,
		RouteKey:     t.routeKey,
		Stage:        t.stage,
		// The apigatewayv2.payload.unrecorded switch's RequestIDIsHeader:
		// requestId is the apigw-requestid header's value (APIGW2-07).
		RequestID:      requestID,
		PathParameters: t.params,
		At:             c.Admitted().Wire(),
		TraceID:        c.Trace(),
		Authorizer:     httpevent.NoAuthorizer{},
	})
	if eerr != nil {
		return awserr.Internalf(fmt.Errorf("apigatewayv2: encoding the request event for API %s: %w", t.apiID, eerr))
	}
	if len(event) > failures.MaxEvent {
		return writeAnswer(w, failures.TooLarge)
	}
	out, ierr := kernel.Invoke(ctx, s.k, c, lambdaapi.OpInvoke, kernel.Target{
		Scope: fnScope, As: kernel.AsService{Name: servicePrincipal, Source: source}, Via: ref.ViaAPIGateway,
	}, &lambdaapi.InvokeInput{FunctionName: fn.String(), InvocationType: new(lambdaapi.InvocationTypeRequestResponse), Payload: event})
	if ierr != nil {
		return lambdaRefused(w, t, ierr)
	}
	if out.FunctionError != nil {
		return writeAnswer(w, failures.FunctionError)
	}
	resp, perr := httpevent.InferV2With(out.Payload, httpevent.V2Defaults{ContentType: PayloadSwitch().Value.DefaultContentType})
	if perr != nil {
		s.k.Log().LogAttrs(ctx, slog.LevelWarn, "API Gateway answered the function's malformed response with its function-error answer",
			slog.String("api", t.apiID), slog.String("route", t.routeKey), slog.String("problem", perr.Error()))
		return writeAnswer(w, failures.FunctionError)
	}
	resp.Write(w)
	return nil
}

// lambdaRefused answers an invocation Lambda refused: Lambda without
// Docker is understudy's limit, so 501 naming it (the unit's conflict row
// 3); Lambda's payload limit is the TooLarge answer; any other refusal is
// the function-error answer (apigateway-http.md 5.6). An emulator failure
// is returned for the edge to render.
func lambdaRefused(w http.ResponseWriter, t toFunction, err *awserr.Error) *awserr.Error {
	failures := FailureAnswersSwitch().Value
	if _, internal := err.Origin.(awserr.Internal); internal {
		return err
	}
	switch {
	case err.ID.Status == http.StatusNotImplemented:
		return apigatewayv2api.ErrNotImplemented(fmt.Sprintf(
			"understudy could not run the Lambda proxy integration of API %s, route %q: the function did not run and nothing was changed. Lambda answered: %s",
			t.apiID, t.routeKey, err.Message))
	case err.ID.Shape == lambdaapi.IdentityRequestEntityTooLargeException().Shape:
		return writeAnswer(w, failures.TooLarge)
	}
	return writeAnswer(w, failures.FunctionError)
}

// sourceARN is the aws:SourceArn API Gateway presents to the function's
// resource policy, as the apigatewayv2.lambda.permission switch describes
// it: <api-id>/<stage>/<METHOD>/<route path without its leading slash>,
// the route key as written, or <api-id>/<stage>/$default for the $default
// route (apigateway-http.md 5.5; inferred beyond the documented forms,
// APIGW2-11).
func (t toFunction) sourceARN() aws.ARN {
	resource := t.apiID + "/" + t.stage + "/"
	switch k := t.key.(type) {
	case route.Default:
		resource += "$default"
	case route.Keyed:
		resource += string(k.Method) + k.Path()
	}
	return aws.ARN{
		Partition: t.owner.Partition.String(), Service: "execute-api", Region: t.owner.Region.String(),
		Account: t.owner.Account.String(), Resource: resource,
	}
}

// invokeFunctionAction is the action the function's resource policy must
// allow API Gateway (apigateway-http.md 8.4).
func invokeFunctionAction() registry.IAMAction {
	return registry.IAMAction{Prefix: "lambda", Name: "InvokeFunction"}
}

// errNoFunctionPolicies is a service built without Options.FunctionPolicies.
var errNoFunctionPolicies = errors.New("apigatewayv2: no Options.FunctionPolicies was given, so no function's resource policy can be read; wire.go passes the Lambda service")

// permitted reports whether fn's resource policy lets API Gateway invoke
// it for source, the 5a resource-policy check (apigateway-http.md finding
// 9, 13.3.1). It applies under every iam.enforce mode; a function that
// does not exist allows nothing.
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
// path after the path fallback's prefix and API id.
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

// writeAnswer writes a response the gateway gives itself: the answer's
// status, the body {"message":"<Message>"} and the Content-Type of the
// apigatewayv2.front.answers switch (apigateway-http.md 4.5).
func writeAnswer(w http.ResponseWriter, a GatewayAnswer) *awserr.Error {
	body, err := json.Marshal(struct {
		Message string `json:"message"`
	}{a.Message})
	if err != nil {
		return awserr.Internalf(err)
	}
	w.Header().Set("Content-Type", FrontAnswersSwitch().Value.ContentType)
	w.WriteHeader(a.Status)
	// A failed write means the client went away; nothing else can be sent.
	_, _ = w.Write(body) //nolint:errcheck // the client is gone when this fails
	return nil
}
