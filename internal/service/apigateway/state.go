package apigateway

import (
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// State is the REST APIs of one account and region. Records are values: a
// write copies an API out, clones what it changes and stores it back, so
// nothing read out of a cell aliases it.
type State struct {
	// APIs are by REST API id.
	APIs map[string]RestAPI `json:"apis"`
	// NextSeq is the Seq the next REST API created in the scope gets.
	NextSeq uint64 `json:"nextSeq"`
}

func newState(aws.Scope) State { return State{APIs: map[string]RestAPI{}} }

// IsZero reports a scope without REST APIs, which snapshots skip.
func (s *State) IsZero() bool { return len(s.APIs) == 0 }

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

func stateCodec() store.Codec {
	c := jsonx.NewCodec()
	return store.Codec{Marshal: c.Marshal, Unmarshal: c.Unmarshal}
}

// RestAPI is one REST API (apigateway-rest.md 3.1). Its resources are in
// creation order, the root first. Optional members are omitzero, so a set
// "" survives a snapshot.
type RestAPI struct {
	ID string `json:"id"`
	// Seq orders the scope's REST APIs by creation.
	Seq         uint64        `json:"seq"`
	Name        string        `json:"name"`
	Description *string       `json:"description,omitzero"`
	Version     *string       `json:"version,omitzero"`
	Created     clock.Instant `json:"created"`
	// BinaryMediaTypes are in the order UpdateRestApi left them
	// (apigateway-rest.md finding 5); a deployment snapshots them.
	BinaryMediaTypes []string `json:"binaryMediaTypes,omitzero"`
	// Endpoint, DisableExecuteAPIEndpoint and Policy are read live at
	// invoke (contract 7.1): a PRIVATE endpoint and a disabled default
	// endpoint answer 501, and a policy, which understudy does not
	// evaluate, follows the enforce matrix's resource-policy row (spec 6b).
	Endpoint                  Endpoint `json:"endpoint"`
	DisableExecuteAPIEndpoint bool     `json:"disableExecuteApiEndpoint,omitzero"`
	// Policy is the resource policy as AWS stores it: execute-api:/
	// resources rewritten to the API's ARN (policyStored).
	Policy *string `json:"policy,omitzero"`
	// APIKeySource, MinimumCompressionSize, SecurityPolicy and
	// EndpointAccessMode are stored without effect (spec 6b coverage
	// note): API keys cannot be created, so an apiKeyRequired method
	// answers 403 whatever the source; understudy never compresses; it
	// serves no TLS of its own per API.
	APIKeySource           apigatewayapi.ApiKeySourceType    `json:"apiKeySource"`
	MinimumCompressionSize *int32                            `json:"minimumCompressionSize,omitzero"`
	SecurityPolicy         *apigatewayapi.SecurityPolicy     `json:"securityPolicy,omitzero"`
	EndpointAccessMode     *apigatewayapi.EndpointAccessMode `json:"endpointAccessMode,omitzero"`
	// RootResourceID is the id of the root resource, created with the API.
	RootResourceID string     `json:"rootResourceId"`
	Resources      []Resource `json:"resources"`
	// NextResourceSeq is the Seq the next resource created in the API
	// gets; the root has 0.
	NextResourceSeq uint64 `json:"nextResourceSeq"`
	// Models are the API's model names: the seeded Empty and Error
	// (apigateway-rest.md 3.9) until models are bound.
	Models []string `json:"models"`
	// Deployments are in creation order; NextDeploymentSeq is the Seq the
	// next one gets, so GetDeployments pages follow on deletions.
	Deployments       []Deployment `json:"deployments"`
	NextDeploymentSeq uint64       `json:"nextDeploymentSeq"`
	// Stages are in creation order.
	Stages []Stage `json:"stages"`
	// Tags are the API's tags (apigateway-rest.md 3.9).
	Tags map[string]string `json:"tags,omitzero"`
}

// Endpoint is a REST API's endpoint configuration (apigateway-rest.md 2.3):
// one type, an IP address type, and VPC endpoint ids, which are stored
// without effect (meaningful only for PRIVATE, which answers 501). An EDGE
// API is served as a REGIONAL one.
type Endpoint struct {
	Type           apigatewayapi.EndpointType  `json:"type"`
	IPAddressType  apigatewayapi.IpAddressType `json:"ipAddressType"`
	VPCEndpointIDs []string                    `json:"vpcEndpointIds,omitzero"`
}

// Resource is one resource of a REST API: the root ("/", no parent and no
// path part) or a child. Methods are by HTTP method, ANY included.
type Resource struct {
	ID string `json:"id"`
	// Seq orders the API's resources by creation; GetResources pages by
	// it.
	Seq      uint64            `json:"seq"`
	ParentID string            `json:"parentId,omitempty"`
	PathPart string            `json:"pathPart,omitempty"`
	Path     string            `json:"path"`
	Methods  map[string]Method `json:"methods,omitzero"`
}

// clone is a copy of the resource whose methods are its own.
func (r *Resource) clone() Resource {
	c := *r
	if r.Methods != nil {
		c.Methods = make(map[string]Method, len(r.Methods))
		for verb, m := range r.Methods {
			c.Methods[verb] = m.clone()
		}
	}
	return c
}

// Method is a method of a resource (apigateway-rest.md 3.3). Maps and
// lists keep nil apart from empty, so an empty one sent is echoed.
type Method struct {
	HTTPMethod string `json:"httpMethod"`
	// Authorization is NONE or AWS_IAM; a request an AWS_IAM method serves
	// answers 501.
	Authorization string `json:"authorization"`
	// APIKeyRequired methods answer 403 at invoke: no API key can be
	// created.
	APIKeyRequired bool    `json:"apiKeyRequired,omitzero"`
	OperationName  *string `json:"operationName,omitzero"`
	// RequestParameters and RequestModels act only through a request
	// validator, and none can exist; AuthorizationScopes act only on a
	// COGNITO_USER_POOLS method, which PutMethod refuses (apigateway-rest.md
	// 3.3).
	RequestParameters   map[string]bool   `json:"requestParameters,omitzero"`
	RequestModels       map[string]string `json:"requestModels,omitzero"`
	AuthorizationScopes []string          `json:"authorizationScopes,omitzero"`
	// Responses are the method responses by status code.
	Responses   map[string]MethodResponse `json:"responses,omitzero"`
	Integration *Integration              `json:"integration,omitzero"`
}

// clone is a copy of the method whose maps, lists and integration are its
// own.
func (m *Method) clone() Method {
	c := *m
	c.OperationName = clonePtr(m.OperationName)
	c.RequestParameters = maps.Clone(m.RequestParameters)
	c.RequestModels = maps.Clone(m.RequestModels)
	c.AuthorizationScopes = slices.Clone(m.AuthorizationScopes)
	if m.Responses != nil {
		c.Responses = make(map[string]MethodResponse, len(m.Responses))
		for code, r := range m.Responses {
			c.Responses[code] = r.clone()
		}
	}
	if m.Integration != nil {
		c.Integration = new(m.Integration.clone())
	}
	return c
}

// MethodResponse is one method response (apigateway-rest.md 3.5).
type MethodResponse struct {
	StatusCode         string            `json:"statusCode"`
	ResponseParameters map[string]bool   `json:"responseParameters,omitzero"`
	ResponseModels     map[string]string `json:"responseModels,omitzero"`
}

// clone is a copy of the response with maps of its own.
func (r *MethodResponse) clone() MethodResponse {
	return MethodResponse{StatusCode: r.StatusCode, ResponseParameters: maps.Clone(r.ResponseParameters), ResponseModels: maps.Clone(r.ResponseModels)}
}

// Integration is a method's integration with the defaults AWS fills
// (apigateway-rest.md 2.3, 3.4). Members are stored as sent, so Terraform
// reads back what it wrote (9.3 item 4).
type Integration struct {
	Type apigatewayapi.IntegrationType `json:"type"`
	// HTTPMethod is integrationHttpMethod (the wire's httpMethod); nil for
	// MOCK, which drops it (recorded, apigateway-rest.md 2.3).
	HTTPMethod     *string                       `json:"httpMethod,omitzero"`
	URI            *string                       `json:"uri,omitzero"`
	ConnectionType *apigatewayapi.ConnectionType `json:"connectionType,omitzero"`
	// Credentials, a stage variable in URI, ResponseTransferMode STREAM
	// and ContentHandling are stored and returned; an invocation that
	// reaches them answers 501 (spec 6b scope list).
	Credentials          *string                                `json:"credentials,omitzero"`
	RequestParameters    map[string]string                      `json:"requestParameters,omitzero"`
	RequestTemplates     map[string]string                      `json:"requestTemplates,omitzero"`
	PassthroughBehavior  string                                 `json:"passthroughBehavior"`
	ContentHandling      *apigatewayapi.ContentHandlingStrategy `json:"contentHandling,omitzero"`
	CacheNamespace       string                                 `json:"cacheNamespace"`
	CacheKeyParameters   []string                               `json:"cacheKeyParameters"`
	TimeoutInMillis      int32                                  `json:"timeoutInMillis"`
	ResponseTransferMode *apigatewayapi.ResponseTransferMode    `json:"responseTransferMode,omitzero"`
	// Responses are the integration responses by status code.
	Responses map[string]IntegrationResponse `json:"responses,omitzero"`
}

// clone is a copy of the integration with maps and slices of its own.
func (i *Integration) clone() Integration {
	c := *i
	c.HTTPMethod, c.URI, c.ConnectionType = clonePtr(i.HTTPMethod), clonePtr(i.URI), clonePtr(i.ConnectionType)
	c.Credentials, c.ContentHandling, c.ResponseTransferMode = clonePtr(i.Credentials), clonePtr(i.ContentHandling), clonePtr(i.ResponseTransferMode)
	c.RequestParameters = maps.Clone(i.RequestParameters)
	c.RequestTemplates = maps.Clone(i.RequestTemplates)
	c.CacheKeyParameters = slices.Clone(i.CacheKeyParameters)
	if i.Responses != nil {
		c.Responses = make(map[string]IntegrationResponse, len(i.Responses))
		for code, r := range i.Responses {
			c.Responses[code] = r.clone()
		}
	}
	return c
}

// IntegrationResponse is one integration response (apigateway-rest.md
// 3.5): selectionPattern kept apart from absent, so "" is echoed.
type IntegrationResponse struct {
	StatusCode         string                                 `json:"statusCode"`
	SelectionPattern   *string                                `json:"selectionPattern,omitzero"`
	ResponseParameters map[string]string                      `json:"responseParameters,omitzero"`
	ResponseTemplates  map[string]string                      `json:"responseTemplates,omitzero"`
	ContentHandling    *apigatewayapi.ContentHandlingStrategy `json:"contentHandling,omitzero"`
	// Seq orders the integration's responses by creation (a replacing put
	// counts as a new creation, inferred), for switch
	// apigateway.mock.selection's AscendingStatus false.
	Seq uint64 `json:"seq,omitzero"`
}

// clone is a copy of the response with maps of its own.
func (r *IntegrationResponse) clone() IntegrationResponse {
	c := *r
	c.SelectionPattern, c.ContentHandling = clonePtr(r.SelectionPattern), clonePtr(r.ContentHandling)
	c.ResponseParameters = maps.Clone(r.ResponseParameters)
	c.ResponseTemplates = maps.Clone(r.ResponseTemplates)
	return c
}

// Deployment is a snapshot of a REST API's resources, methods and
// integrations at its creation, which a stage serves (apigateway-rest.md
// 12.5, finding 12). Its resources are deep copies no write changes
// afterwards.
type Deployment struct {
	ID string `json:"id"`
	// Seq orders the API's deployments by creation; GetDeployments pages
	// by it.
	Seq         uint64        `json:"seq"`
	Description *string       `json:"description,omitzero"`
	Created     clock.Instant `json:"created"`
	Resources   []Resource    `json:"resources"`
	// BinaryMediaTypes are the API's at the deployment's creation; while
	// any is set, an invocation of a stage serving it answers 501 (spec
	// 6b scope list).
	BinaryMediaTypes []string `json:"binaryMediaTypes,omitzero"`
}

// Stage is one stage of a REST API, serving one deployment
// (apigateway-rest.md 3.7). The cache, tracing, client certificate, access
// log and method settings are stored and echoed without effect; the
// variables reach the data plane live.
type Stage struct {
	Name                string                          `json:"name"`
	Deployment          string                          `json:"deployment"`
	Description         *string                         `json:"description,omitzero"`
	CacheClusterEnabled bool                            `json:"cacheClusterEnabled,omitzero"`
	CacheClusterSize    *apigatewayapi.CacheClusterSize `json:"cacheClusterSize,omitzero"`
	TracingEnabled      bool                            `json:"tracingEnabled,omitzero"`
	ClientCertificateID *string                         `json:"clientCertificateId,omitzero"`
	AccessLog           *AccessLog                      `json:"accessLog,omitzero"`
	// MethodSettings are by "<resource path without its leading slash>/
	// <HTTP method>", "*/*" for every method (recorded, apigateway-rest.md
	// 3.7).
	MethodSettings map[string]MethodSetting `json:"methodSettings,omitzero"`
	Variables      map[string]string        `json:"variables,omitzero"`
	Tags           map[string]string        `json:"tags,omitzero"`
	Created        clock.Instant            `json:"created"`
	Updated        clock.Instant            `json:"updated"`
}

// clone is a copy of the stage whose pointers and maps are its own.
func (st *Stage) clone() Stage {
	c := *st
	c.Description, c.CacheClusterSize, c.ClientCertificateID = clonePtr(st.Description), clonePtr(st.CacheClusterSize), clonePtr(st.ClientCertificateID)
	if st.AccessLog != nil {
		c.AccessLog = &AccessLog{Format: clonePtr(st.AccessLog.Format), DestinationARN: clonePtr(st.AccessLog.DestinationARN)}
	}
	c.MethodSettings = maps.Clone(st.MethodSettings)
	c.Variables = maps.Clone(st.Variables)
	c.Tags = maps.Clone(st.Tags)
	return c
}

// AccessLog is a stage's access log settings, stored without effect.
type AccessLog struct {
	Format         *string `json:"format,omitzero"`
	DestinationARN *string `json:"destinationArn,omitzero"`
}

// MethodSetting is one entry of a stage's method settings, stored without
// effect (apigateway-rest.md 3.7). LoggingLevel is absent until set
// (recorded).
type MethodSetting struct {
	MetricsEnabled                         bool                                                 `json:"metricsEnabled"`
	LoggingLevel                           *string                                              `json:"loggingLevel,omitzero"`
	DataTraceEnabled                       bool                                                 `json:"dataTraceEnabled"`
	ThrottlingBurstLimit                   int32                                                `json:"throttlingBurstLimit"`
	ThrottlingRateLimit                    float64                                              `json:"throttlingRateLimit"`
	CachingEnabled                         bool                                                 `json:"cachingEnabled"`
	CacheTTLInSeconds                      int32                                                `json:"cacheTtlInSeconds"`
	CacheDataEncrypted                     bool                                                 `json:"cacheDataEncrypted"`
	RequireAuthorizationForCacheControl    bool                                                 `json:"requireAuthorizationForCacheControl"`
	UnauthorizedCacheControlHeaderStrategy apigatewayapi.UnauthorizedCacheControlHeaderStrategy `json:"unauthorizedCacheControlHeaderStrategy"`
}

// resource finds the resource id names.
func (a *RestAPI) resource(id string) (Resource, int, bool) {
	i := slices.IndexFunc(a.Resources, func(r Resource) bool { return r.ID == id })
	if i < 0 {
		return Resource{}, -1, false
	}
	return a.Resources[i], i, true
}

// hasChild reports whether id is taken by a resource or a deployment of the
// API, so a new child id is redrawn.
func (a *RestAPI) hasChild(id string) bool {
	return slices.ContainsFunc(a.Resources, func(r Resource) bool { return r.ID == id }) ||
		slices.ContainsFunc(a.Deployments, func(d Deployment) bool { return d.ID == id })
}

// stage finds the stage name names.
func (a *RestAPI) stage(name string) (Stage, int, bool) {
	i := slices.IndexFunc(a.Stages, func(s Stage) bool { return s.Name == name })
	if i < 0 {
		return Stage{}, -1, false
	}
	return a.Stages[i], i, true
}

// deployment finds the deployment id names.
func (a *RestAPI) deployment(id string) (Deployment, bool) {
	i := slices.IndexFunc(a.Deployments, func(d Deployment) bool { return d.ID == id })
	if i < 0 {
		return Deployment{}, false
	}
	return a.Deployments[i], true
}

// withResource is a copy of the API with the resource at index i replaced
// by r, the resource slice its own.
func (a *RestAPI) withResource(i int, r Resource) RestAPI {
	c := *a
	c.Resources = slices.Clone(a.Resources)
	c.Resources[i] = r
	return c
}

// cloneResources deep-copies resources, as a deployment holds them.
func cloneResources(rs []Resource) []Resource {
	out := make([]Resource, len(rs))
	for i := range rs {
		out[i] = rs[i].clone()
	}
	return out
}

// withMethods is a copy of the resource with a methods map of its own.
func (r *Resource) withMethods() Resource {
	c := *r
	c.Methods = maps.Clone(r.Methods)
	if c.Methods == nil {
		c.Methods = map[string]Method{}
	}
	return c
}

// clonePtr is a copy of what p points at, or nil.
func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// deref is the value p points at, or def when p is nil.
func deref[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}
