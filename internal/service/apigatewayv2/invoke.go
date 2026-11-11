package apigatewayv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/httpevent"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/lambdaproxy"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2/route"
	"github.com/sustinbebustin/understudy/internal/store"
)

// executeAPIPrefix is the execute-api path fallback,
// /_understudy/execute-api/<api-id>/<path> (apigateway-http.md 11.3).
const executeAPIPrefix = "/_understudy/execute-api/"

// invoke is InvokeApi, the execute-api data plane (apigateway-http.md
// 13.3): it finds the API the host or path fallback names, selects the stage and
// the route of the stage's deployment, checks that the function's resource
// policy lets API Gateway invoke it, invokes it synchronously with the
// event of the integration's payload format and answers with the response
// read from its output. The answers the gateway gives itself are the switches'. A
// request understudy cannot serve as AWS would is answered 501, and a
// failure of the emulator is returned for the edge to render.
func (s *Service) invoke(ctx context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	requestID := string(ids.APIGatewayRequestIDFor(c.ID()))
	w.Header().Set("Apigw-Requestid", requestID)
	failures := FailureAnswersSwitch().Value
	// The payload quota comes first, before the API is looked up
	// (apigateway-http.md 4.7).
	body, tooLarge, err := lambdaproxy.ReadBody(r, failures.MaxBody)
	switch {
	case err != nil:
		return err
	case tooLarge:
		return writeAnswer(w, failures.TooLarge)
	}
	addr, ok := s.resolve(r)
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

// address is what a request names: an API id, the escaped path the API
// sees and the host the request named.
type address struct {
	apiID string
	// path is the path after the path fallback's prefix and API id, or the
	// host form's path, as normalize leaves it, each segment escaped; "/"
	// when there is none. decoded is the same path unescaped, which the
	// event reports.
	path, decoded string
	// host is the execute-api host the request named, lowercase and without
	// its port; "" on the path fallback.
	host string
	// region is the region the host names; zero when it names none (the
	// path fallback, or <api-id>.execute-api.<base>), and the API serves in
	// its own.
	region aws.Region
}

// resolve reads what a request names (apigateway-http.md 11.3, 13.1.3): the
// path fallback /_understudy/execute-api/<api-id>/<path>, which the edge
// claims before the host (ARCHITECTURE.md 4.1 rule 1), else the host
// <api-id>.execute-api.<region>.<base> or <api-id>.execute-api.<base>.
// The third label is a region only when the region table accepts it and
// labels follow it, as the edge claims the host. The API id is lowercased,
// as a host name is.
func (s *Service) resolve(r *http.Request) (address, bool) {
	escaped := lambdaproxy.EscapedPath(r)
	if rest, ok := strings.CutPrefix(escaped, executeAPIPrefix); ok {
		id, path, _ := strings.Cut(rest, "/")
		if id == "" {
			return address{}, false
		}
		addr := address{apiID: strings.ToLower(id)}
		addr.path, addr.decoded = normalize("/" + path)
		return addr, true
	}
	host := lambdaproxy.HostName(r.Host)
	labels := strings.SplitN(host, ".", 3)
	if len(labels) != 3 || labels[0] == "" || labels[1] != "execute-api" || labels[2] == "" {
		return address{}, false
	}
	addr := address{apiID: labels[0], host: host}
	if escaped == "" {
		escaped = "/"
	}
	addr.path, addr.decoded = normalize(escaped)
	if first, after, ok := strings.Cut(labels[2], "."); ok && after != "" {
		if region, err := s.regions.Parse(first); err == nil {
			addr.region = region
		}
	}
	return addr, true
}

// normalize is the escaped path as an HTTP API routes it: percent-decoded
// first, so %2F is a slash, then with each run of slashes collapsed to one
// (APIGW2-05, recorded 2026-11-03: steps 13 and 14; apigateway-http.md
// 4.4). It returns the result with each segment escaped again, which the
// stage and route matchers read, and unescaped, which the event reports as
// rawPath (APIGW2-07 steps 11 and 12). A path whose escapes do not decode
// is returned as sent; no route matches it.
func normalize(escaped string) (path, decoded string) {
	d, err := url.PathUnescape(escaped)
	if err != nil {
		return escaped, escaped
	}
	kept := slices.DeleteFunc(strings.Split(d, "/"), func(p string) bool { return p == "" })
	// A trailing slash stays: /a/ is not /a (APIGW2-05 step 12).
	if len(kept) > 0 && strings.HasSuffix(d, "/") {
		kept = append(kept, "")
	}
	decoded = "/" + strings.Join(kept, "/")
	for i, p := range kept {
		kept[i] = url.PathEscape(p)
	}
	return "/" + strings.Join(kept, "/"), decoded
}

// domain is the domain name the event reports: the host the request named,
// or for the path fallback the host form under localhost in the API's
// region (apigateway-http.md 13.1.3), as the function URL fallback reports
// its own.
func (a address) domain(owner aws.Scope) string {
	if a.host != "" {
		return a.host
	}
	return a.apiID + ".execute-api." + owner.Region.String() + ".localhost"
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
	// rawPath is the decoded path the API sees (address.decoded), a named
	// stage's segment included (apigateway-http.md finding 8).
	rawPath string
	// domain is the event's domainName (address.domain).
	domain string
	params map[string]string
	// stageVariables are the stage's (apigateway-http.md 5.1) as it holds
	// them now: they are no part of a deployment, so a change serves at once
	// (inferred).
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

// selectRoute finds the API addr names, its stage and the route of the
// stage's deployment a request with method matches; origin is whether the
// request carries an Origin header. The API is found by its id in any
// account (apigateway-http.md 4.1), and a host naming a region other than
// the API's is the front.answers switch's UnknownAPI, as an unknown id is
// (APIGW2-06). The API's DisableExecuteApiEndpoint and CORS configuration
// are read live, so they hold from the call that set them
// (apigateway-http.md 3.1, the front.answers switch).
func (s *Service) selectRoute(addr address, method string, origin bool) selection {
	owner, ok := s.index.lookup(addr.apiID)
	if !ok || (!addr.region.IsZero() && addr.region != owner.Region) {
		return answered{FrontAnswersSwitch().Value.UnknownAPI}
	}
	return store.View(s.cells, owner, func(st *State) selection {
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
	k := slices.IndexFunc(d.Routes, func(r Route) bool { return r.ID == m.RouteID })
	if i < 0 || k < 0 {
		return corrupt{fmt.Errorf("apigatewayv2: route.Table.Match selected route %s, which deployment %s of API %s does not hold", m.RouteID, d.ID, a.ID)}
	}
	rt, key := d.Routes[k], table[i].Key
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
		rawPath: addr.decoded, domain: addr.domain(owner), params: m.PathParameters, stageVariables: maps.Clone(stage.Variables), integration: integration,
	}
}

// unemulated names what of the integration understudy cannot run as AWS
// would, and what to change instead; what is "" for a Lambda proxy
// integration with nothing a request would need applied (these are
// stored and answer 501 when a request reaches them).
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

// table is the deployment's routes that have a target as route.Table, in
// creation order: a route without one takes no part in routing, so a
// request it would match falls to the next route (APIGW2-05, recorded
// 2026-11-03: step 17; apigateway-http.md 3.2).
func (d *Deployment) table() (route.Table, error) {
	t := make(route.Table, 0, len(d.Routes))
	for _, r := range d.Routes {
		if r.Integration == "" {
			continue
		}
		k, err := route.ParseKey(r.Key)
		if err != nil {
			return nil, fmt.Errorf("apigatewayv2: route %s of deployment %s holds a key CreateRoute could not have accepted: %w", r.ID, d.ID, err)
		}
		t = append(t, route.Route{ID: r.ID, Key: k})
	}
	return t, nil
}

// invokeFunction runs a request's Lambda proxy integration: the resource
// policy check, the event of the integration's payload format, the
// synchronous invocation and the response read from its output.
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
	source := t.sourceARN(fnScope.Region)
	allowed, err := lambdaproxy.Permitted(s.policies, fn, source, t.owner)
	if err != nil {
		return err
	}
	if !allowed {
		return writeAnswer(w, PermissionSwitch().Value.Denial)
	}
	failures := FailureAnswersSwitch().Value
	event, eerr := t.event(lambdaproxy.EventRequest(c, r, t.rawPath, body), httpevent.Context{
		AccountID:    t.owner.Account.String(),
		APIID:        t.apiID,
		DomainName:   t.domain,
		DomainPrefix: t.apiID,
		RouteKey:     t.routeKey,
		Stage:        t.stage,
		// The apigatewayv2.payload.unrecorded switch's RequestIDIsHeader:
		// requestId is the apigw-requestid header's value (APIGW2-07).
		RequestID:      requestID,
		PathParameters: t.params,
		StageVariables: t.stageVariables,
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
	var out *lambdaapi.InvokeOutput
	switch r := lambdaproxy.InvokeWithin(ctx, s.k, c, t.integration.timeout(), lambdaproxy.Invocation{
		Function: fn, Scope: fnScope, Source: source, Event: event,
	}).(type) {
	case lambdaproxy.ClientGone:
		return awserr.RequestAborted()
	case lambdaproxy.TimedOut:
		return writeAnswer(w, failures.Timeout)
	case lambdaproxy.Refusal:
		return lambdaRefused(w, t, r.Err)
	case lambdaproxy.Output:
		out = r.Out
	}
	if out == nil {
		return awserr.Internalf(fmt.Errorf("apigatewayv2: Lambda's Invoke of %s answered neither an output nor an error", fn))
	}
	if out.FunctionError != nil {
		return writeAnswer(w, failures.FunctionError)
	}
	resp, perr := t.response(out.Payload)
	if perr != nil {
		s.k.Log().LogAttrs(ctx, slog.LevelWarn, "API Gateway answered the function's malformed response with its function-error answer",
			slog.String("api", t.apiID), slog.String("route", t.routeKey), slog.String("payload_format", t.integration.PayloadFormatVersion), slog.String("problem", perr.Error()))
		return writeAnswer(w, failures.FunctionError)
	}
	resp.Write(w)
	return nil
}

// timeout is how long API Gateway waits for the integration
// (TimeoutInMillis).
func (i *Integration) timeout() time.Duration {
	return time.Duration(i.TimeoutInMillis) * time.Millisecond
}

// event is the request event of the integration's payload format: 1.0 as
// an HTTP API reports it (apigateway-http.md 5.2), else 2.0 (5.1).
func (t toFunction) event(r httpevent.Request, c httpevent.Context) ([]byte, error) {
	if t.integration.PayloadFormatVersion == payloadV1 {
		return httpevent.EventV1(r, c, httpevent.HTTPAPI{})
	}
	return httpevent.EventV2For(r, c, httpevent.HTTPAPI{})
}

// response reads the function's output under the integration's payload
// format (apigateway-http.md 5.3, 5.4). A response that sets no
// Content-Type gets the apigatewayv2.payload.unrecorded switch's
// DefaultContentType: a structured 2.0 response, and a 1.0 response,
// since an HTTP API has one default for both (4.5; inferred, APIGW2-09).
func (t toFunction) response(payload []byte) (httpevent.Response, error) {
	contentType := PayloadSwitch().Value.DefaultContentType
	if t.integration.PayloadFormatVersion != payloadV1 {
		resp, err := httpevent.InferV2With(payload, httpevent.HTTPAPIDefaults(contentType))
		if err != nil {
			return httpevent.Response{}, err
		}
		// The cookies member's Set-Cookie headers come before a set-cookie
		// in headers (APIGW2-09, recorded 2026-11-03: 2.0-cookies).
		resp.Cookies = append(resp.Cookies, resp.Header.Values("Set-Cookie")...)
		resp.Header.Del("Set-Cookie")
		return resp, nil
	}
	resp, err := httpevent.InferV1(payload)
	if err != nil {
		return httpevent.Response{}, err
	}
	if resp.Header.Get("Content-Type") == "" {
		resp.Header.Set("Content-Type", contentType)
	}
	return resp, nil
}

// lambdaRefused answers an invocation Lambda refused: Lambda without
// Docker is understudy's limit, so 501 naming it; Lambda's payload limit
// is the TooLarge answer; any other refusal is the function-error answer
// (apigateway-http.md 5.6). An emulator failure is returned for the edge
// to render.
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
// APIGW2-11). Its region is fnRegion, the function's: "If your function and
// API are in different AWS Regions, the Region identifier in the source ARN
// must match the Region of the function" (5.5).
func (t toFunction) sourceARN(fnRegion aws.Region) aws.ARN {
	resource := t.apiID + "/" + t.stage + "/"
	switch k := t.key.(type) {
	case route.Default:
		resource += "$default"
	case route.Keyed:
		resource += string(k.Method) + k.Path()
	}
	return aws.ARN{
		Partition: t.owner.Partition.String(), Service: "execute-api", Region: fnRegion.String(),
		Account: t.owner.Account.String(), Resource: resource,
	}
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
