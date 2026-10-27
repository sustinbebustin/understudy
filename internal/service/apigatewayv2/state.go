package apigatewayv2

import (
	"fmt"
	"slices"

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
	Routes        []Route           `json:"routes"`
	Integrations  []Integration     `json:"integrations"`
	Stages        []Stage           `json:"stages"`
	Deployments   []Deployment      `json:"deployments"`
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

// Route is one route of an API.
type Route struct {
	ID string `json:"id"`
	// Key is the route key as sent, which route.ParseKey accepted.
	Key string `json:"key"`
	// Integration is the id of the integration the route targets
	// ("integrations/<id>"); empty for a route without a target.
	Integration string `json:"integration,omitempty"`
}

// Integration is a Lambda proxy integration (AWS_PROXY with no subtype)
// of payload format 2.0, the only kind stored yet.
type Integration struct {
	ID string `json:"id"`
	// URI is the IntegrationUri as sent: a function ARN or API Gateway's
	// invoke URI wrapping one (lambdaFunction).
	URI                  string `json:"uri"`
	PayloadFormatVersion string `json:"payloadFormatVersion"`
}

// Stage is one stage of an API.
type Stage struct {
	Name       string        `json:"name"`
	AutoDeploy bool          `json:"autoDeploy"`
	Created    clock.Instant `json:"created"`
	Updated    clock.Instant `json:"updated"`
	// Deployment is the id of the deployment the stage serves; empty
	// before its first.
	Deployment string `json:"deployment,omitempty"`
	// LastDeploymentStatusMessage is set by an automatic deployment.
	LastDeploymentStatusMessage string            `json:"lastDeploymentStatusMessage,omitempty"`
	Tags                        map[string]string `json:"tags,omitempty"`
}

// Deployment is a snapshot of an API's routes and integrations at its
// creation, which a stage serves (apigateway-http.md finding 7). Its
// slices are copies that no write changes afterwards; a member that adds
// a map or a slice to Route or Integration must be copied deeply here.
type Deployment struct {
	ID           string        `json:"id"`
	AutoDeployed bool          `json:"autoDeployed"`
	Description  string        `json:"description,omitempty"`
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
	id := newID()
	for a.hasChild(id) {
		id = newID()
	}
	a.Deployments = append(slices.Clone(a.Deployments), Deployment{
		ID: id, AutoDeployed: true, Description: autoDeployDescription, Created: now,
		Routes: slices.Clone(a.Routes), Integrations: slices.Clone(a.Integrations),
	})
	a.Stages = slices.Clone(a.Stages)
	a.Stages[i].Deployment = id
	a.Stages[i].LastDeploymentStatusMessage = fmt.Sprintf("Successfully deployed stage with deployment ID '%s'", id)
}
