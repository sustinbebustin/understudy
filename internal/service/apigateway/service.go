package apigateway

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the version of the service's snapshot sections.
const stateVersion = 1

// Options configure the service.
type Options struct {
	// FunctionPolicies reads the resource policy of the function a Lambda
	// proxy integration names, which must allow apigateway.amazonaws.com
	// to invoke it (apigateway-rest.md 5.4); wire.go passes the Lambda
	// service.
	FunctionPolicies authz.PolicyProvider
	// Regions is the region table an execute-api host's region label is
	// read against, as the edge's; the zero value is AWS's.
	Regions aws.Regions
	// Enforce is iam.enforce, which decides what an invocation of an API
	// with a resource policy does (the spec's enforce matrix). No API can
	// hold a policy yet, so it is not read yet.
	Enforce EnforceMode
}

// EnforceMode is the iam.enforce mode, as the data plane needs it for a
// REST API's resource policy, which it does not evaluate: off ignores it,
// advise records an advisory and serves, enforce answers 501.
type EnforceMode uint8

// The modes; the zero value is off.
const (
	EnforceOff EnforceMode = iota
	EnforceAdvise
	EnforceEnforce
)

// Service is Amazon API Gateway's REST APIs (apigateway): one state cell
// per account and region holding its REST APIs, and the execute-api data
// plane.
type Service struct {
	k        *kernel.Kernel
	ids      *ids.Source
	policies authz.PolicyProvider
	regions  aws.Regions
	cells    *store.Partitioned[aws.Scope, State]
	index    apiIndex
}

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// baseNote heads every bound control-plane operation's coverage note
// (apigateway-rest.md 2.4, 3.10).
const baseNote = "the X-Amzn-Errortype value and generated ids are inferred (UNVERIFIED, APIGW1-01, APIGW1-02)"

// Coverage notes of the tracer bullet's operations: what each leaves out
// of the documented behavior until its own ticket.
const (
	createRestAPINote = "only name, description and version are accepted, and every other member answers NotImplemented; " +
		"apiKeySource, endpointConfiguration and disableExecuteApiEndpoint are reported at their recorded defaults"
	getRestAPINote    = "apiKeySource, endpointConfiguration and disableExecuteApiEndpoint are reported at their recorded defaults"
	updateRestAPINote = "replace /name, add, replace and remove /description, and add, replace and remove /binaryMediaTypes/{type} apply; " +
		"every other documented path answers NotImplemented; an invocation of a stage whose deployment has binaryMediaTypes answers 501; " +
		"operations apply in order and a refused one changes nothing (inferred); adding a listed type or removing an unlisted one changes nothing, " +
		"and the texts for an empty name or a replace without a value are inferred; " +
		"apiKeySource, endpointConfiguration and disableExecuteApiEndpoint are reported at their recorded defaults"
	getResourcesNote = "items in creation order, root first (inferred, APIGW1-04)"
	methodNote       = "AWS_IAM, CUSTOM and COGNITO_USER_POOLS methods answer 501 at invoke; apiKeyRequired methods answer 403 because API keys cannot be created; " +
		"requestValidatorId and requestModels name only the seeded Empty and Error models until validators and models are bound"
	putIntegrationNote = "only type AWS_PROXY to a Lambda function with integrationHttpMethod, uri and timeoutInMillis is accepted, " +
		"and every other type and member answers NotImplemented"
	createDeploymentNote = "only stageName and description are accepted, and every other member answers NotImplemented; " +
		"the texts for deploying an API with no methods or a method with no integration are inferred (APIGW1-05)"
	invokeRestAPINote = "AWS_PROXY (payload 1.0) on the root resource only; a request the root does not serve answers 501 while the deployment holds other resources; " +
		"mapping templates, request validation, authorizers, API keys, caching and throttling are not applied; AWS_IAM methods answer 501; apiKeyRequired methods answer 403; " +
		"binaryMediaTypes in the stage's deployment answer 501; " +
		"inferred, UNVERIFIED: unknown stage, ANY against explicit methods, HEAD (APIGW1-07, -08), timeout (APIGW1-10), empty-body event (APIGW1-16); " +
		"header names reach the event in canonical form (Test-Header), not as the client wrote them"
)

// partial is a Partial mark: the base note, then note.
func partial(note string) registry.Partial {
	return registry.Partial{Note: baseNote + "; " + note}
}

// New binds the operations understudy implements. Every other operation
// stays unbound and answers 501.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{
		k:        d.Kernel,
		ids:      d.IDs,
		policies: o.FunctionPolicies,
		regions:  o.Regions,
		cells:    store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newState),
		index:    apiIndex{scopes: map[string]aws.Scope{}},
	}
	b := apigatewayapi.NewBinder(d.Binder)
	b.CreateRestApi(s.createRestAPI, partial(createRestAPINote),
		kernel.WithResources(resourceAt(func(*apigatewayapi.CreateRestApiInput) string { return "/restapis" })))
	b.GetRestApi(s.getRestAPI, partial(getRestAPINote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.GetRestApiInput) string { return "/restapis/" + in.RestApiId })))
	b.UpdateRestApi(s.updateRestAPI, partial(updateRestAPINote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.UpdateRestApiInput) string { return "/restapis/" + in.RestApiId })))
	b.CreateResource(s.createResource, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.CreateResourceInput) string {
			return "/restapis/" + in.RestApiId + "/resources/" + in.ParentId
		})))
	b.GetResource(s.getResource, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.GetResourceInput) string {
			return "/restapis/" + in.RestApiId + "/resources/" + in.ResourceId
		})))
	b.GetResources(s.getResources, partial(getResourcesNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.GetResourcesInput) string { return "/restapis/" + in.RestApiId + "/resources" })))
	b.UpdateResource(s.updateResource, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.UpdateResourceInput) string {
			return "/restapis/" + in.RestApiId + "/resources/" + in.ResourceId
		})))
	b.DeleteResource(s.deleteResource, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.DeleteResourceInput) string {
			return "/restapis/" + in.RestApiId + "/resources/" + in.ResourceId
		})))
	b.PutMethod(s.putMethod, partial(methodNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.PutMethodInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod)
		})))
	b.GetMethod(s.getMethod, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.GetMethodInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod)
		})))
	b.UpdateMethod(s.updateMethod, partial(methodNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.UpdateMethodInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod)
		})))
	b.DeleteMethod(s.deleteMethod, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.DeleteMethodInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod)
		})))
	b.PutMethodResponse(s.putMethodResponse, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.PutMethodResponseInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/responses/" + in.StatusCode
		})))
	b.GetMethodResponse(s.getMethodResponse, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.GetMethodResponseInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/responses/" + in.StatusCode
		})))
	b.UpdateMethodResponse(s.updateMethodResponse, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.UpdateMethodResponseInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/responses/" + in.StatusCode
		})))
	b.DeleteMethodResponse(s.deleteMethodResponse, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.DeleteMethodResponseInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/responses/" + in.StatusCode
		})))
	b.PutIntegration(s.putIntegration, partial(putIntegrationNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.PutIntegrationInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/integration"
		})))
	b.CreateDeployment(s.createDeployment, partial(createDeploymentNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.CreateDeploymentInput) string {
			return "/restapis/" + in.RestApiId + "/deployments"
		})))
	b.InvokeRestApi(s.invoke, registry.Partial{Note: invokeRestAPINote})
	return s
}

// Name returns "apigateway", the service's registry id and ARN namespace.
func (*Service) Name() ref.ServiceID { return "apigateway" }

// SchemaVersion returns the version of the service's state.
func (*Service) SchemaVersion() int { return stateVersion }

// Export serializes every non-empty scope to one section.
func (s *Service) Export() ([]store.Section, error) {
	return s.cells.Export(store.ScopeKeys(), stateVersion)
}

// Import loads sections written by Export of this schema version, then
// rebuilds the API id index from the loaded cells.
func (s *Service) Import(ss []store.Section) error {
	if err := s.cells.Import(store.ScopeKeys(), stateVersion, ss); err != nil {
		return err
	}
	return s.reindex()
}

// Reset drops the scopes rs covers, and their APIs' ids from the index. It
// runs under the gate's exclusive side, so it reads no cell.
func (s *Service) Reset(rs kernel.ResetScope) {
	s.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
	s.index.drop(rs.Covers)
}

// Rearm rebuilds timers; the service has none.
func (*Service) Rearm(clock.Instant) {}

// Resources lists the REST APIs of scope, one per API under its resource
// ARN.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	return readState(s, scope, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.APIs))
		for _, id := range slices.Sorted(maps.Keys(st.APIs)) {
			a := st.APIs[id]
			out = append(out, kernel.Resource{ARN: restAPIARN(scope, id), Summary: fmt.Sprintf("%d resources, %d stages", len(a.Resources), len(a.Stages))})
		}
		return out
	})
}

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }

// OwnsExecuteAPI reports whether any account holds a REST API with id, so
// the edge sends its execute-api requests to InvokeRestApi (ADR 0019).
func (s *Service) OwnsExecuteAPI(id string) bool {
	_, ok := s.index.lookup(id)
	return ok
}

// readState runs fn on the state of scope without creating its cell; a
// scope with no cell reads as empty.
func readState[R any](s *Service, scope aws.Scope, fn func(st *State) R) R {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		st := newState(scope)
		return fn(&st)
	}
	return store.Read(cell, fn)
}

// reindex rebuilds the API id index from the cells' sections, without
// taking the gate; the caller holds its exclusive side. A failure is a bug
// in this package, and the index is then left as it was.
func (s *Service) reindex() error {
	sections, err := s.cells.Export(store.ScopeKeys(), stateVersion)
	if err != nil {
		return fmt.Errorf("rebuilding the apigateway REST API id index: %w", err)
	}
	scopes := map[string]aws.Scope{}
	codec := stateCodec()
	for _, sec := range sections {
		scope, err := store.ScopeKeys().Decode(strings.TrimSuffix(sec.Path, ".json"))
		if err != nil {
			return fmt.Errorf("rebuilding the apigateway REST API id index: section %q names no scope: %w", sec.Path, err)
		}
		st := newState(scope)
		if err := codec.Unmarshal(sec.Data, &st); err != nil {
			return fmt.Errorf("rebuilding the apigateway REST API id index: decoding section %q: %w", sec.Path, err)
		}
		for id := range st.APIs {
			scopes[id] = scope
		}
	}
	s.index.replace(scopes)
	return nil
}

// apiIndex maps every REST API id to the account and region that hold it:
// the data plane finds an API by its id alone, in any account
// (apigateway-rest.md 4.1).
type apiIndex struct {
	mu     sync.Mutex
	scopes map[string]aws.Scope
}

// reserve claims id for scope, false when an API already holds it.
func (x *apiIndex) reserve(id string, scope aws.Scope) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if _, taken := x.scopes[id]; taken {
		return false
	}
	x.scopes[id] = scope
	return true
}

// set records that scope holds id.
func (x *apiIndex) set(id string, scope aws.Scope) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.scopes[id] = scope
}

// release drops id.
func (x *apiIndex) release(id string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.scopes, id)
}

// lookup returns the scope that holds id.
func (x *apiIndex) lookup(id string) (aws.Scope, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	scope, ok := x.scopes[id]
	return scope, ok
}

// drop removes every id whose scope covered reports true.
func (x *apiIndex) drop(covered func(aws.Scope) bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	maps.DeleteFunc(x.scopes, func(_ string, scope aws.Scope) bool { return covered(scope) })
}

// replace makes scopes the whole index.
func (x *apiIndex) replace(scopes map[string]aws.Scope) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.scopes = scopes
}
