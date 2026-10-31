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
	// RootResourceID is the id of the root resource, created with the API.
	RootResourceID string       `json:"rootResourceId"`
	Resources      []Resource   `json:"resources"`
	Deployments    []Deployment `json:"deployments"`
	Stages         []Stage      `json:"stages"`
}

// Resource is one resource of a REST API: the root ("/", no parent and no
// path part) or a child. Methods are by HTTP method, ANY included.
type Resource struct {
	ID       string            `json:"id"`
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

// Method is a method of a resource (apigateway-rest.md 3.3). Only the
// members PutMethod accepts so far are held.
type Method struct {
	HTTPMethod string `json:"httpMethod"`
	// Authorization is NONE or AWS_IAM; a request an AWS_IAM method serves
	// answers 501.
	Authorization string       `json:"authorization"`
	Integration   *Integration `json:"integration,omitzero"`
}

// clone is a copy of the method whose integration is its own.
func (m *Method) clone() Method {
	c := *m
	if m.Integration != nil {
		c.Integration = new(m.Integration.clone())
	}
	return c
}

// Integration is a method's integration with the defaults AWS fills
// (apigateway-rest.md 2.3, 3.4). Only AWS_PROXY to a Lambda function is
// accepted so far.
type Integration struct {
	Type apigatewayapi.IntegrationType `json:"type"`
	// HTTPMethod is integrationHttpMethod (the wire's httpMethod).
	HTTPMethod          string   `json:"httpMethod"`
	URI                 string   `json:"uri"`
	PassthroughBehavior string   `json:"passthroughBehavior"`
	CacheNamespace      string   `json:"cacheNamespace"`
	CacheKeyParameters  []string `json:"cacheKeyParameters"`
	TimeoutInMillis     int32    `json:"timeoutInMillis"`
}

// clone is a copy of the integration with slices of its own.
func (i *Integration) clone() Integration {
	c := *i
	c.CacheKeyParameters = slices.Clone(i.CacheKeyParameters)
	return c
}

// Deployment is a snapshot of a REST API's resources, methods and
// integrations at its creation, which a stage serves (apigateway-rest.md
// 12.5, finding 12). Its resources are deep copies no write changes
// afterwards.
type Deployment struct {
	ID          string        `json:"id"`
	Description *string       `json:"description,omitzero"`
	Created     clock.Instant `json:"created"`
	Resources   []Resource    `json:"resources"`
	// BinaryMediaTypes are the API's at the deployment's creation; while
	// any is set, an invocation of a stage serving it answers 501 (spec
	// 6b scope list).
	BinaryMediaTypes []string `json:"binaryMediaTypes,omitzero"`
}

// Stage is one stage of a REST API, serving one deployment.
type Stage struct {
	Name       string            `json:"name"`
	Deployment string            `json:"deployment"`
	Created    clock.Instant     `json:"created"`
	Updated    clock.Instant     `json:"updated"`
	Variables  map[string]string `json:"variables,omitzero"`
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
