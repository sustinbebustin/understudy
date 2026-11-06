package apigatewayv2

import (
	"fmt"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// State is the HTTP APIs of one account and region. Records are values: a
// write copies an API out, clones each slice it changes, and stores the API
// back, so nothing read out of a cell aliases it.
type State struct {
	// APIs are by API id.
	APIs map[string]API `json:"apis"`
	// NextSeq is the Seq the next API created in the scope gets.
	NextSeq uint64 `json:"nextSeq"`
}

func newState(aws.Scope) State { return State{APIs: map[string]API{}} }

// IsZero reports a scope without APIs, which snapshots skip.
func (s *State) IsZero() bool { return len(s.APIs) == 0 }

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

func stateCodec() store.Codec {
	c := jsonx.NewCodec()
	return store.Codec{Marshal: c.Marshal, Unmarshal: c.Unmarshal}
}

// API is one HTTP API. Its routes, integrations, stages and deployments are
// in creation order. A pointer member is optional, absent when nil; no
// write changes what one points at. Optional members are omitzero, not
// omitempty, which would drop a set "" (encoding/json/v2 omits a field
// whose JSON is empty).
type API struct {
	ID string `json:"id"`
	// Seq orders the scope's APIs by creation, as GetApis lists them.
	Seq         uint64  `json:"seq"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitzero"`
	Version     *string `json:"version,omitzero"`
	// RouteSelectionExpression and APIKeySelectionExpression are as sent,
	// or the defaults (apigateway-http.md 3.1, 12 item 5).
	RouteSelectionExpression  string `json:"routeSelectionExpression"`
	APIKeySelectionExpression string `json:"apiKeySelectionExpression"`
	// CORS is the CORS configuration as sent. understudy does not apply
	// it: a data-plane request with an Origin header answers 501.
	CORS *CORS `json:"cors,omitzero"`
	// DisableExecuteAPIEndpoint turns the default endpoint off, from the
	// call that sets it.
	DisableExecuteAPIEndpoint bool `json:"disableExecuteApiEndpoint"`
	// DisableSchemaValidation is stored and returned; it concerns models,
	// which HTTP APIs do not validate (WebSocket only).
	DisableSchemaValidation *bool `json:"disableSchemaValidation,omitzero"`
	// IPAddressType is stored without effect: ipv4 or dualstack.
	IPAddressType string            `json:"ipAddressType"`
	Tags          map[string]string `json:"tags,omitempty"`
	Created       clock.Instant     `json:"created"`
	// NextChild is the Seq the next route, integration, stage or deployment
	// of the API gets.
	NextChild    uint64        `json:"nextChild"`
	Routes       []Route       `json:"routes"`
	Integrations []Integration `json:"integrations"`
	Stages       []Stage       `json:"stages"`
	Deployments  []Deployment  `json:"deployments"`
}

// CORS is an API's CORS configuration, kept as sent: a nil list was not
// sent, an empty one was sent empty (apigateway-http.md 3.7). omitzero
// keeps the difference: encoding/json/v2 writes a nil slice as [].
type CORS struct {
	AllowCredentials *bool    `json:"allowCredentials,omitzero"`
	AllowHeaders     []string `json:"allowHeaders,omitzero"`
	AllowMethods     []string `json:"allowMethods,omitzero"`
	AllowOrigins     []string `json:"allowOrigins,omitzero"`
	ExposeHeaders    []string `json:"exposeHeaders,omitzero"`
	MaxAge           *int32   `json:"maxAge,omitzero"`
}

// Route is one route of an API. The members after Authorization act only
// on WebSocket APIs (apigateway-http.md 3.2): they are stored and echoed
// without effect, as Terraform reads them back.
type Route struct {
	ID string `json:"id"`
	// Seq orders the API's routes by creation, as GetRoutes pages them.
	Seq uint64 `json:"seq"`
	// Key is the route key as sent, which route.ParseKey accepted.
	Key string `json:"key"`
	// Integration is the id of the integration the route targets
	// ("integrations/<id>"); empty for a route without a target.
	Integration string `json:"integration,omitempty"`
	// Authorization is NONE or AWS_IAM. JWT and CUSTOM name authorizers,
	// which understudy does not emulate, so no route holds them; a request
	// an AWS_IAM route matches answers 501.
	Authorization                    apigatewayv2api.AuthorizationType `json:"authorization"`
	APIKeyRequired                   bool                              `json:"apiKeyRequired"`
	ModelSelectionExpression         *string                           `json:"modelSelectionExpression,omitzero"`
	OperationName                    *string                           `json:"operationName,omitzero"`
	RequestModels                    map[string]string                 `json:"requestModels,omitzero"`
	RequestParameters                map[string]RouteParameter         `json:"requestParameters,omitzero"`
	RouteResponseSelectionExpression *string                           `json:"routeResponseSelectionExpression,omitzero"`
}

// RouteParameter is a route request parameter's constraints, as sent.
type RouteParameter struct {
	Required *bool `json:"required,omitzero"`
}

// clone is a copy of the route with maps of its own.
func (r *Route) clone() Route {
	c := *r
	c.RequestModels = maps.Clone(r.RequestModels)
	c.RequestParameters = maps.Clone(r.RequestParameters)
	return c
}

// Integration is one integration of an API, as stored with the defaults
// AWS fills (apigateway-http.md 3.3). Only a Lambda proxy integration
// (AWS_PROXY without a subtype) of payload format 2.0 runs; a request that
// reaches any other answers 501 (Integration.unemulated).
type Integration struct {
	ID string `json:"id"`
	// Seq orders the API's integrations by creation, as GetIntegrations
	// pages them.
	Seq uint64 `json:"seq"`
	// Type is AWS_PROXY or HTTP_PROXY, the two an HTTP API takes.
	Type    apigatewayv2api.IntegrationType `json:"type"`
	Subtype *string                         `json:"subtype,omitzero"`
	// URI is the IntegrationUri as sent: for a Lambda integration a
	// function ARN, API Gateway's invoke URI wrapping one
	// (lambdaFunction), or text referencing a stage variable.
	URI *string `json:"uri,omitzero"`
	// Method is the IntegrationMethod: as sent, or POST for a Lambda
	// integration that sent none.
	Method               *string                        `json:"method,omitzero"`
	PayloadFormatVersion string                         `json:"payloadFormatVersion"`
	ConnectionType       apigatewayv2api.ConnectionType `json:"connectionType"`
	ConnectionID         *string                        `json:"connectionId,omitzero"`
	CredentialsARN       *string                        `json:"credentialsArn,omitzero"`
	Description          *string                        `json:"description,omitzero"`
	TimeoutInMillis      int32                          `json:"timeoutInMillis"`
	// RequestParameters and ResponseParameters are as sent, byte for byte
	// (apigateway-http.md 3.3).
	RequestParameters  map[string]string            `json:"requestParameters,omitzero"`
	ResponseParameters map[string]map[string]string `json:"responseParameters,omitzero"`
	// TLSConfig is stored without effect: it configures private
	// integrations, which answer 501.
	TLSConfig *TLSConfig `json:"tlsConfig,omitzero"`
}

// TLSConfig is an integration's TLS configuration, as sent.
type TLSConfig struct {
	ServerNameToVerify *string `json:"serverNameToVerify,omitzero"`
}

// clone is a copy of the integration with maps of its own.
func (i *Integration) clone() Integration {
	c := *i
	c.RequestParameters = maps.Clone(i.RequestParameters)
	c.ResponseParameters = cloneResponseParameters(i.ResponseParameters)
	return c
}

// cloneResponseParameters copies response parameters, each status's
// mappings included; nil stays nil.
func cloneResponseParameters(p map[string]map[string]string) map[string]map[string]string {
	if p == nil {
		return nil
	}
	out := make(map[string]map[string]string, len(p))
	for status, m := range p {
		out[status] = maps.Clone(m)
	}
	return out
}

// cloned copies each of xs with its clone method; nil stays nil.
func cloned[T any, P interface {
	*T
	clone() T
}](xs []T) []T {
	if xs == nil {
		return nil
	}
	out := make([]T, len(xs))
	for i := range xs {
		out[i] = P(&xs[i]).clone()
	}
	return out
}

// Stage is one stage of an API. Its variables reach the function in the
// event; its access log and route settings are stored without effect (no
// access log records, no throttling or metrics).
type Stage struct {
	Name string `json:"name"`
	// Seq orders the API's stages by creation, as GetStages pages them.
	Seq         uint64        `json:"seq"`
	AutoDeploy  bool          `json:"autoDeploy"`
	Description *string       `json:"description,omitzero"`
	Created     clock.Instant `json:"created"`
	Updated     clock.Instant `json:"updated"`
	// Deployment is the id of the deployment the stage serves; empty
	// before its first.
	Deployment string `json:"deployment,omitempty"`
	// LastDeploymentStatusMessage is set by an automatic deployment and
	// cleared by an explicit one, whose status it would misstate.
	LastDeploymentStatusMessage string                   `json:"lastDeploymentStatusMessage,omitempty"`
	Variables                   map[string]string        `json:"variables,omitzero"`
	AccessLog                   *AccessLogSettings       `json:"accessLog,omitzero"`
	DefaultRouteSettings        *RouteSettings           `json:"defaultRouteSettings,omitzero"`
	RouteSettings               map[string]RouteSettings `json:"routeSettings,omitzero"`
	Tags                        map[string]string        `json:"tags,omitempty"`
}

// AccessLogSettings is a stage's access log settings, as sent.
type AccessLogSettings struct {
	DestinationARN *string `json:"destinationArn,omitzero"`
	Format         *string `json:"format,omitzero"`
}

// RouteSettings is a stage's default settings, or those of one route key,
// as sent. DataTraceEnabled and LoggingLevel act only on WebSocket APIs
// (apigateway-http.md 3.4).
type RouteSettings struct {
	DataTraceEnabled       *bool                         `json:"dataTraceEnabled,omitzero"`
	DetailedMetricsEnabled *bool                         `json:"detailedMetricsEnabled,omitzero"`
	LoggingLevel           *apigatewayv2api.LoggingLevel `json:"loggingLevel,omitzero"`
	ThrottlingBurstLimit   *int32                        `json:"throttlingBurstLimit,omitzero"`
	ThrottlingRateLimit    *float64                      `json:"throttlingRateLimit,omitzero"`
}

// clone is a copy of the stage with maps of its own.
func (st *Stage) clone() Stage {
	c := *st
	c.Variables = maps.Clone(st.Variables)
	c.RouteSettings = maps.Clone(st.RouteSettings)
	c.Tags = maps.Clone(st.Tags)
	return c
}

// Deployment is a snapshot of an API's routes and integrations at its
// creation, which a stage serves (apigateway-http.md finding 7). Its
// slices are deep copies (cloned) that no write changes afterwards; a map
// or a slice added to Route or Integration must be copied by its clone
// method.
type Deployment struct {
	ID string `json:"id"`
	// Seq orders the API's deployments by creation, as GetDeployments
	// pages them.
	Seq          uint64        `json:"seq"`
	AutoDeployed bool          `json:"autoDeployed"`
	Description  *string       `json:"description,omitzero"`
	Created      clock.Instant `json:"created"`
	Routes       []Route       `json:"routes"`
	Integrations []Integration `json:"integrations"`
}

// autoDeployDescription is an automatic deployment's Description
// (apigateway-http.md 3.5, the get-deployments CLI example).
const autoDeployDescription = "Automatic deployment triggered by changes to the Api configuration"

// integration finds the integration id names.
func (a *API) integration(id string) (Integration, bool) {
	i := slices.IndexFunc(a.Integrations, func(x Integration) bool { return x.ID == id })
	if i < 0 {
		return Integration{}, false
	}
	return a.Integrations[i], true
}

// nextChild is the Seq of a new route or integration, which it takes.
func (a *API) nextChild() uint64 {
	seq := a.NextChild
	a.NextChild++
	return seq
}

// stage finds the stage name names.
func (a *API) stage(name string) (Stage, bool) {
	i := slices.IndexFunc(a.Stages, func(s Stage) bool { return s.Name == name })
	if i < 0 {
		return Stage{}, false
	}
	return a.Stages[i], true
}

// deployment finds the deployment id names.
func (a *API) deployment(id string) (Deployment, bool) {
	i := slices.IndexFunc(a.Deployments, func(d Deployment) bool { return d.ID == id })
	if i < 0 {
		return Deployment{}, false
	}
	return a.Deployments[i], true
}

// hasChild reports whether id is taken by a route, an integration or a
// deployment of the API, so a new child id is redrawn.
func (a *API) hasChild(id string) bool {
	return slices.ContainsFunc(a.Routes, func(r Route) bool { return r.ID == id }) ||
		slices.ContainsFunc(a.Integrations, func(i Integration) bool { return i.ID == id }) ||
		slices.ContainsFunc(a.Deployments, func(d Deployment) bool { return d.ID == id })
}

// autoDeploy deploys the API's current routes and integrations to every
// stage with AutoDeploy, inside the write that changed them, so the change
// serves before the call returns (apigateway-http.md 3.5; the delay is
// UNVERIFIED, APIGW2-04). newID draws a deployment id.
func (a *API) autoDeploy(now clock.Instant, newID func() string) {
	for i := range a.Stages {
		if a.Stages[i].AutoDeploy {
			a.deployTo(i, now, newID)
		}
	}
}

// deployTo deploys the API automatically to its stage at index i: a new
// deployment the stage then serves, with the status message the
// get-stages CLI example shows (apigateway-http.md 3.4).
func (a *API) deployTo(i int, now clock.Instant, newID func() string) {
	d := a.deploy(now, newID, true, new(autoDeployDescription))
	a.serve(i, d.ID, now)
	a.Stages[i].LastDeploymentStatusMessage = fmt.Sprintf("Successfully deployed stage with deployment ID '%s'", d.ID)
}

// deploy adds a deployment of the API's current routes and integrations
// and returns it; newID draws its id.
func (a *API) deploy(now clock.Instant, newID func() string, auto bool, description *string) Deployment {
	id := newID()
	for a.hasChild(id) {
		id = newID()
	}
	d := Deployment{
		ID: id, Seq: a.nextChild(), AutoDeployed: auto, Description: clonePtr(description), Created: now,
		Routes: cloned(a.Routes), Integrations: cloned(a.Integrations),
	}
	a.Deployments = append(slices.Clone(a.Deployments), d)
	return d
}

// serve makes the stage at index i serve the deployment id from now on.
// The status message describes an automatic deployment only, so it goes.
func (a *API) serve(i int, id string, now clock.Instant) {
	a.Stages = slices.Clone(a.Stages)
	a.Stages[i].Deployment = id
	a.Stages[i].LastDeploymentStatusMessage = ""
	a.Stages[i].Updated = now
}
