package apigatewayv2

import (
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
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
	// to invoke it (apigateway-http.md 5.5); wire.go passes the Lambda
	// service.
	FunctionPolicies authz.PolicyProvider
	// Regions is the region table an execute-api host's region label is
	// read against, as the edge's; the zero value is AWS's.
	Regions aws.Regions
}

// Service is Amazon API Gateway's HTTP APIs (apigatewayv2): one state cell
// per account and region holding its APIs, and the execute-api data plane.
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

// baseNote heads every bound control-plane operation's coverage note: no
// v2 error text is recorded, and the id shapes are inferred
// (apigateway-http.md 2.3, 2.5).
const baseNote = "error message texts and generated ids are inferred (UNVERIFIED, APIGW2-01, APIGW2-03)"

// Coverage notes of the operations bound so far: what each leaves out of
// the documented behavior.
const (
	apiNote = "HTTP APIs only: protocolType WEBSOCKET and quick create (Target, RouteKey, CredentialsArn) answer NotImplemented; " +
		"corsConfiguration is stored and returned, and a data-plane request with an Origin header to an API that has one answers 501 (CORS is not applied); " +
		"ipAddressType is stored without effect; " +
		"ApiEndpoint names the emulator (http://<api-id>.execute-api.<region>.localhost:<port>), not amazonaws.com"
	routeNote = "authorizationType JWT and CUSTOM answer NotImplemented (authorizers are not emulated); " +
		"a route with AWS_IAM is stored and a request it matches answers 501; " +
		"WebSocket-only members are stored and echoed without effect"
	integrationNote = "Lambda AWS_PROXY with payload 2.0 runs; " +
		"a Lambda integration of payload 1.0, integration subtypes, HTTP_PROXY and VPC_LINK connections are stored and a request that reaches them answers 501; " +
		"a Lambda integration with credentialsArn, requestParameters, responseParameters or a stage variable in its URI is stored and a request that reaches it answers 501; " +
		"timeoutInMillis is stored without effect (a request waits for the function however long it runs); " +
		"tlsConfig is stored without effect"
	stageNote = "accessLogSettings, defaultRouteSettings and routeSettings are stored without effect (no access log records, no throttling); " +
		"an automatic deployment completes before the call returns (UNVERIFIED delay, APIGW2-04); " +
		"an explicit DeploymentId on an autoDeploy stage is refused (documented as not updatable; identity and text inferred, APIGW2-04)"
	deploymentNote = "a deployment of an API with no routes succeeds and serves 404 Not Found, " +
		"and deleting a deployment a stage uses is refused with BadRequestException (the REST rule; both UNVERIFIED for HTTP APIs, APIGW2-04)"
	// storedOnlyNote heads the StoredOnly mark of the two calls that remove
	// stage settings understudy stores without effect.
	storedOnlyNote = "the settings they remove are stored without effect"
	invokeAPINote  = "Lambda AWS_PROXY with payload format 2.0; stage and route selection as documented; " +
		"where the documents leave the order of two matching routes open it is inferred (APIGW2-05): " +
		"a full match on ANY before a greedy match on the method, the greedy route with more segments before its variable first, then the leftmost literal segment; " +
		"AWS_IAM routes, CORS requests (an Origin header to an API with a corsConfiguration), payload format 1.0 and integrations other than Lambda AWS_PROXY answer 501, " +
		"as does a Lambda integration with credentialsArn, requestParameters, responseParameters or a stage variable in its URI; " +
		"the integration timeout, access logging and throttling are not applied; " +
		"inferred, UNVERIFIED: the gateway's own answers and their status (APIGW2-05, APIGW2-06, APIGW2-10, APIGW2-11), " +
		"the 2.0 isBase64Encoded of a bodyless request and default Content-Type (APIGW2-07, APIGW2-09)"
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
	b := apigatewayv2api.NewBinder(d.Binder)
	b.CreateApi(s.createAPI, partial(apiNote), kernel.WithCheck(checkCreateAPI),
		kernel.WithResources(apisCollection[apigatewayv2api.CreateApiInput]))
	b.GetApis(s.getAPIs, registry.Partial{Note: baseNote},
		kernel.WithResources(apisCollection[apigatewayv2api.GetApisInput]))
	b.GetApi(s.getAPI, registry.Partial{Note: baseNote},
		kernel.WithResources(apiItem(func(in *apigatewayv2api.GetApiInput) string { return in.ApiId })))
	b.UpdateApi(s.updateAPI, partial(apiNote), kernel.WithCheck(checkUpdateAPI),
		kernel.WithResources(apiItem(func(in *apigatewayv2api.UpdateApiInput) string { return in.ApiId })))
	b.DeleteApi(s.deleteAPI, registry.Partial{Note: baseNote},
		kernel.WithResources(apiItem(func(in *apigatewayv2api.DeleteApiInput) string { return in.ApiId })))
	b.DeleteCorsConfiguration(s.deleteCORS, registry.Partial{Note: baseNote},
		kernel.WithResources(childCollection(func(in *apigatewayv2api.DeleteCorsConfigurationInput) string { return in.ApiId }, "cors")))
	b.TagResource(s.tagResource, registry.Partial{Note: baseNote},
		kernel.WithCheck(func(in *apigatewayv2api.TagResourceInput) *awserr.Error { return checkTags(in.Tags) }),
		kernel.WithResources(tagsResource(func(in *apigatewayv2api.TagResourceInput) string { return in.ResourceArn })))
	b.UntagResource(s.untagResource, registry.Partial{Note: baseNote},
		kernel.WithResources(tagsResource(func(in *apigatewayv2api.UntagResourceInput) string { return in.ResourceArn })))
	b.GetTags(s.getTags, registry.Partial{Note: baseNote},
		kernel.WithResources(tagsResource(func(in *apigatewayv2api.GetTagsInput) string { return in.ResourceArn })))
	b.CreateRoute(s.createRoute, partial(routeNote), kernel.WithCheck(checkCreateRoute),
		kernel.WithResources(childCollection(func(in *apigatewayv2api.CreateRouteInput) string { return in.ApiId }, "routes")))
	b.GetRoutes(s.getRoutes, registry.Partial{Note: baseNote},
		kernel.WithResources(childCollection(func(in *apigatewayv2api.GetRoutesInput) string { return in.ApiId }, "routes")))
	b.GetRoute(s.getRoute, registry.Partial{Note: baseNote},
		kernel.WithResources(childItem(func(in *apigatewayv2api.GetRouteInput) (string, string) { return in.ApiId, in.RouteId }, "routes")))
	b.UpdateRoute(s.updateRoute, partial(routeNote), kernel.WithCheck(checkUpdateRoute),
		kernel.WithResources(childItem(func(in *apigatewayv2api.UpdateRouteInput) (string, string) { return in.ApiId, in.RouteId }, "routes")))
	b.DeleteRoute(s.deleteRoute, registry.Partial{Note: baseNote},
		kernel.WithResources(childItem(func(in *apigatewayv2api.DeleteRouteInput) (string, string) { return in.ApiId, in.RouteId }, "routes")))
	b.CreateIntegration(s.createIntegration, partial(integrationNote), kernel.WithCheck(checkCreateIntegration),
		kernel.WithResources(childCollection(func(in *apigatewayv2api.CreateIntegrationInput) string { return in.ApiId }, "integrations")))
	b.GetIntegrations(s.getIntegrations, registry.Partial{Note: baseNote},
		kernel.WithResources(childCollection(func(in *apigatewayv2api.GetIntegrationsInput) string { return in.ApiId }, "integrations")))
	b.GetIntegration(s.getIntegration, registry.Partial{Note: baseNote},
		kernel.WithResources(childItem(func(in *apigatewayv2api.GetIntegrationInput) (string, string) { return in.ApiId, in.IntegrationId }, "integrations")))
	b.UpdateIntegration(s.updateIntegration, partial(integrationNote), kernel.WithCheck(checkUpdateIntegration),
		kernel.WithResources(childItem(func(in *apigatewayv2api.UpdateIntegrationInput) (string, string) { return in.ApiId, in.IntegrationId }, "integrations")))
	b.DeleteIntegration(s.deleteIntegration, registry.Partial{Note: baseNote},
		kernel.WithResources(childItem(func(in *apigatewayv2api.DeleteIntegrationInput) (string, string) { return in.ApiId, in.IntegrationId }, "integrations")))
	b.CreateStage(s.createStage, partial(stageNote), kernel.WithCheck(checkCreateStage),
		kernel.WithResources(childCollection(func(in *apigatewayv2api.CreateStageInput) string { return in.ApiId }, "stages")))
	b.GetStages(s.getStages, registry.Partial{Note: baseNote},
		kernel.WithResources(childCollection(func(in *apigatewayv2api.GetStagesInput) string { return in.ApiId }, "stages")))
	b.GetStage(s.getStage, registry.Partial{Note: baseNote},
		kernel.WithResources(childItem(func(in *apigatewayv2api.GetStageInput) (string, string) { return in.ApiId, in.StageName }, "stages")))
	b.UpdateStage(s.updateStage, partial(stageNote), kernel.WithCheck(checkUpdateStage),
		kernel.WithResources(childItem(func(in *apigatewayv2api.UpdateStageInput) (string, string) { return in.ApiId, in.StageName }, "stages")))
	b.DeleteStage(s.deleteStage, registry.Partial{Note: baseNote},
		kernel.WithResources(childItem(func(in *apigatewayv2api.DeleteStageInput) (string, string) { return in.ApiId, in.StageName }, "stages")))
	b.DeleteAccessLogSettings(s.deleteAccessLogSettings, registry.StoredOnly{Note: storedOnlyNote + "; " + baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayv2api.DeleteAccessLogSettingsInput) string {
			return "/apis/" + in.ApiId + "/stages/" + in.StageName + "/accesslogsettings"
		})))
	b.DeleteRouteSettings(s.deleteRouteSettings, registry.StoredOnly{Note: storedOnlyNote + "; " + baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayv2api.DeleteRouteSettingsInput) string {
			// The route key as sent; whether AWS writes it so or
			// percent-encoded is UNVERIFIED (apigateway-http.md 8.1, APIGW2-15).
			return "/apis/" + in.ApiId + "/stages/" + in.StageName + "/routesettings/" + in.RouteKey
		})))
	b.CreateDeployment(s.createDeployment, partial(deploymentNote),
		kernel.WithResources(childCollection(func(in *apigatewayv2api.CreateDeploymentInput) string { return in.ApiId }, "deployments")))
	b.GetDeployments(s.getDeployments, registry.Partial{Note: baseNote},
		kernel.WithResources(childCollection(func(in *apigatewayv2api.GetDeploymentsInput) string { return in.ApiId }, "deployments")))
	b.GetDeployment(s.getDeployment, registry.Partial{Note: baseNote},
		kernel.WithResources(childItem(func(in *apigatewayv2api.GetDeploymentInput) (string, string) { return in.ApiId, in.DeploymentId }, "deployments")))
	b.UpdateDeployment(s.updateDeployment, registry.Partial{Note: baseNote},
		kernel.WithResources(childItem(func(in *apigatewayv2api.UpdateDeploymentInput) (string, string) { return in.ApiId, in.DeploymentId }, "deployments")))
	b.DeleteDeployment(s.deleteDeployment, partial(deploymentNote),
		kernel.WithResources(childItem(func(in *apigatewayv2api.DeleteDeploymentInput) (string, string) { return in.ApiId, in.DeploymentId }, "deployments")))
	b.InvokeApi(s.invoke, registry.Partial{Note: invokeAPINote})
	return s
}

// Name returns "apigatewayv2", the service's registry id; its ARNs use the
// "apigateway" namespace.
func (*Service) Name() ref.ServiceID { return "apigatewayv2" }

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
	s.reindex()
	return nil
}

// Reset drops the scopes rs covers, and their APIs' ids from the index.
func (s *Service) Reset(rs kernel.ResetScope) {
	s.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
	s.reindex()
}

// Rearm rebuilds timers; the service has none.
func (*Service) Rearm(clock.Instant) {}

// Resources lists the APIs of scope, one per API under its resource ARN.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	return readState(s, scope, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.APIs))
		for _, id := range slices.Sorted(maps.Keys(st.APIs)) {
			a := st.APIs[id]
			out = append(out, kernel.Resource{ARN: apiARN(scope, id), Summary: fmt.Sprintf("%d routes, %d stages", len(a.Routes), len(a.Stages))})
		}
		return out
	})
}

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }

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

// reindex rebuilds the API id index from the cells.
func (s *Service) reindex() {
	scopes := map[string]aws.Scope{}
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		for _, id := range readState(s, scope, func(st *State) []string { return slices.Collect(maps.Keys(st.APIs)) }) {
			scopes[id] = scope
		}
	}
	s.index.replace(scopes)
}

// apiIndex maps every API id to the account and region that hold it: the
// data plane finds an API by its id alone, in any account
// (apigateway-http.md 4.1), so ids are unique across accounts. An entry
// may outlive its API by a moment around a Reset; a lookup reads the cell
// it names, which then has no such API.
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

// replace makes scopes the whole index.
func (x *apiIndex) replace(scopes map[string]aws.Scope) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.scopes = scopes
}
