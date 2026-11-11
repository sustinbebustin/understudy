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

// stateVersion is the version of the service's snapshot sections. Version
// 2 dropped the resource and integration-response sequence numbers
// (GetResources sorts by id, apigateway-rest.md 13.5); before the first
// tagged release a section of another version is refused, not migrated
// (ARCHITECTURE.md 13).
const stateVersion = 2

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
	// with a resource policy does.
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
	enforce  EnforceMode
	cells    *store.Partitioned[aws.Scope, State]
	index    apiIndex
}

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// baseNote heads every bound control-plane operation's coverage note
// (apigateway-rest.md 2.4, 3.10).
const baseNote = "an AccessDeniedException body carries message, where AWS sends Message (recorded, APIGW1-22)"

// Coverage notes of the tracer bullet's operations: what each leaves out
// of the documented behavior.
const (
	// restAPINote is CreateRestApi's and UpdateRestApi's note
	// (apigateway-rest.md 12.2), plus the enum-list violation text the
	// golden replay of test_create_rest_api_verify_defaults found.
	restAPINote = "cloneFrom answers NotImplemented; policy is stored and returned escaped, and under iam.enforce=enforce an invocation of an API with a policy answers 501; " +
		"binaryMediaTypes, disableExecuteApiEndpoint and endpointConfiguration PRIVATE are stored and returned, and an invocation of an API that has them answers 501; " +
		"vpcEndpointIds, securityPolicy, endpointAccessMode, minimumCompressionSize and apiKeySource are stored without effect; " +
		"an EDGE API is served as a REGIONAL one (no CloudFront headers in the event); a second API with the same name is allowed (recorded, APIGW1-14); " +
		"the UpdateRestApi texts for an invalid value, a second or removed endpoint type and PRIVATE on ipv4 are inferred; " +
		"a stored policy keeps a one-element Resource list as a list, where AWS rewrites it to a string (recorded, test_apigateway_integrations.snapshot.json:L45-L77)"
	creationOrderNote = "items in creation order (inferred)"
	getResourcesNote  = "items sorted by id and position as AWS sends it (APIGW1-04); the limit member AWS echoes is not sent"
	methodNote        = "AWS_IAM, CUSTOM and COGNITO_USER_POOLS methods answer 501 at invoke; apiKeyRequired methods answer 403 because API keys cannot be created; " +
		"requestValidatorId and requestModels name only the seeded Empty and Error models until validators and models are bound; " +
		"an authorizationType outside NONE, AWS_IAM, CUSTOM and COGNITO_USER_POOLS and UpdateMethod replace /resourceId naming another resource answer NotImplemented"
	methodResponseNote = "responseModels name only the seeded Empty and Error models (text inferred)"
	// emptyResponseTemplate and emptyRequestTemplate board up apigateway-rest.md
	// finding 6: the generated map types cannot carry a null value, so an
	// empty-string template is returned as "" until the generator can
	// write one.
	emptyResponseTemplate = `an empty-string response template is returned as "", where AWS returns null (recorded, apigateway-rest.md finding 6)`
	emptyRequestTemplate  = `an empty-string request template is returned as "", where AWS returns null (recorded, APIGW1-13)`
	integrationNote       = "AWS_PROXY to Lambda and MOCK run; HTTP, HTTP_PROXY and AWS are stored and answer 501 at invoke; " +
		"credentials, a stage variable in the uri, responseTransferMode STREAM and contentHandling are stored and returned, and an invocation that reaches them answers 501; " +
		"connectionType VPC_LINK, connectionId, tlsConfig, integrationTarget and an AWS_PROXY Firehose stream uri answer NotImplemented; " +
		"requestParameters, cacheNamespace and cacheKeyParameters are stored without effect; " +
		"a boolean requestParameters value answers SerializationException with understudy's decode text, not AWS's recorded one; " +
		"the texts for a timeout out of range, an unknown passthroughBehavior and a patch value of the wrong type are inferred; " + emptyRequestTemplate
	getIntegrationNote      = emptyRequestTemplate + "; " + emptyResponseTemplate
	integrationResponseNote = "selectionPattern runs as Go RE2; a Java-only construct (lookaround, backreference, possessive quantifier) answers 501 at invoke naming the pattern; " +
		"only static responseParameters and literal templates run, anything else answers 501; " +
		"contentHandling is stored and an invocation that selects the response answers 501; " + emptyResponseTemplate
	createDeploymentNote   = "canarySettings answers NotImplemented; cacheClusterEnabled, cacheClusterSize and tracingEnabled are stored without effect"
	deploymentNotFoundNote = "the missing-deployment text is inferred"
	stageNameNote          = "a hyphen in a stage name is accepted, as documented, though AWS's refusal text leaves it out (unrecorded, APIGW1-06)"
	stageNote              = "cache settings, methodSettings, accessLogSettings, clientCertificateId, tracingEnabled and webAclArn are stored and echoed without effect; " +
		"canarySettings answers NotImplemented; documentationVersion answers NotImplemented while documentation versions are unbound"
	updateStageNote   = stageNote + "; a new method setting starts from the */* one (inferred)"
	tagsNote          = "the texts for a malformed or unknown resourceArn and the tag key and value lengths are inferred"
	invokeRestAPINote = "AWS_PROXY (payload 1.0) and MOCK with literal templates; " +
		"mapping templates with VTL answer 501; request validation, authorizers, API keys, caching and throttling are not applied; AWS_IAM methods answer 501; apiKeyRequired methods answer 403; " +
		"binaryMediaTypes in the stage's deployment, a disabled default endpoint and PRIVATE APIs answer 501; " +
		"a resource policy is not evaluated: under iam.enforce=advise each invocation records an advisory, under enforce it answers 501; " +
		"HTTP, HTTP_PROXY and AWS integrations, integration credentials, stage-variable uris, responseTransferMode STREAM and contentHandling answer 501; " +
		"unrecorded: the answer to an unknown API id (APIGW1-07), which of GET and ANY serves HEAD (APIGW1-08), and whether the longer or the later-sorting of two matching selection patterns wins (APIGW1-12); " +
		"inferred: a MOCK template with no statusCode or one outside 100..599 selects the default integration response; " +
		"header names reach the event in canonical form (Test-Header), as AWS sent aUThorization as Authorization (APIGW1-16)"
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
		enforce:  o.Enforce,
		cells:    store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newState),
		index:    apiIndex{scopes: map[string]aws.Scope{}},
	}
	b := apigatewayapi.NewBinder(d.Binder)
	b.CreateRestApi(s.createRestAPI, partial(restAPINote),
		kernel.WithResources(resourceAt(func(*apigatewayapi.CreateRestApiInput) string { return "/restapis" })))
	b.GetRestApis(s.getRestAPIs, partial(creationOrderNote),
		kernel.WithResources(resourceAt(func(*apigatewayapi.GetRestApisInput) string { return "/restapis" })))
	b.DeleteRestApi(s.deleteRestAPI, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.DeleteRestApiInput) string { return "/restapis/" + in.RestApiId })))
	b.GetRestApi(s.getRestAPI, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.GetRestApiInput) string { return "/restapis/" + in.RestApiId })))
	b.UpdateRestApi(s.updateRestAPI, partial(restAPINote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.UpdateRestApiInput) string { return "/restapis/" + in.RestApiId })))
	b.CreateResource(s.createResource, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.CreateResourceInput) string {
			return "/restapis/" + in.RestApiId + "/resources/" + in.ParentId
		})))
	b.GetResource(s.getResource, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.GetResourceInput) string {
			return "/restapis/" + in.RestApiId + "/resources/" + in.ResourceId
		})))
	b.GetResources(s.getResources, partial(getResourcesNote+"; "+getIntegrationNote),
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
	b.GetMethod(s.getMethod, partial(getIntegrationNote),
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
	b.PutMethodResponse(s.putMethodResponse, partial(methodResponseNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.PutMethodResponseInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/responses/" + in.StatusCode
		})))
	b.GetMethodResponse(s.getMethodResponse, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.GetMethodResponseInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/responses/" + in.StatusCode
		})))
	b.UpdateMethodResponse(s.updateMethodResponse, partial(methodResponseNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.UpdateMethodResponseInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/responses/" + in.StatusCode
		})))
	b.DeleteMethodResponse(s.deleteMethodResponse, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.DeleteMethodResponseInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/responses/" + in.StatusCode
		})))
	b.PutIntegration(s.putIntegration, partial(integrationNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.PutIntegrationInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/integration"
		})))
	b.GetIntegration(s.getIntegration, partial(getIntegrationNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.GetIntegrationInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/integration"
		})))
	b.UpdateIntegration(s.updateIntegration, partial(integrationNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.UpdateIntegrationInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/integration"
		})))
	b.DeleteIntegration(s.deleteIntegration, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.DeleteIntegrationInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/integration"
		})))
	b.PutIntegrationResponse(s.putIntegrationResponse, partial(integrationResponseNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.PutIntegrationResponseInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/integration/responses/" + in.StatusCode
		})))
	b.GetIntegrationResponse(s.getIntegrationResponse, partial(emptyResponseTemplate),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.GetIntegrationResponseInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/integration/responses/" + in.StatusCode
		})))
	b.UpdateIntegrationResponse(s.updateIntegrationResponse, partial(integrationResponseNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.UpdateIntegrationResponseInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/integration/responses/" + in.StatusCode
		})))
	b.DeleteIntegrationResponse(s.deleteIntegrationResponse, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.DeleteIntegrationResponseInput) string {
			return methodPath(in.RestApiId, in.ResourceId, in.HttpMethod) + "/integration/responses/" + in.StatusCode
		})))
	b.CreateDeployment(s.createDeployment, partial(createDeploymentNote+"; "+stageNameNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.CreateDeploymentInput) string {
			return "/restapis/" + in.RestApiId + "/deployments"
		})))
	b.GetDeployments(s.getDeployments, partial(creationOrderNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.GetDeploymentsInput) string {
			return "/restapis/" + in.RestApiId + "/deployments"
		})))
	b.GetDeployment(s.getDeployment, partial(deploymentNotFoundNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.GetDeploymentInput) string {
			return "/restapis/" + in.RestApiId + "/deployments/" + in.DeploymentId
		})))
	b.UpdateDeployment(s.updateDeployment, partial(deploymentNotFoundNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.UpdateDeploymentInput) string {
			return "/restapis/" + in.RestApiId + "/deployments/" + in.DeploymentId
		})))
	b.DeleteDeployment(s.deleteDeployment, partial(deploymentNotFoundNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.DeleteDeploymentInput) string {
			return "/restapis/" + in.RestApiId + "/deployments/" + in.DeploymentId
		})))
	b.CreateStage(s.createStage, partial(stageNote+"; "+stageNameNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.CreateStageInput) string { return "/restapis/" + in.RestApiId + "/stages" })))
	b.GetStages(s.getStages, partial(creationOrderNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.GetStagesInput) string { return "/restapis/" + in.RestApiId + "/stages" })))
	b.GetStage(s.getStage, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.GetStageInput) string {
			return "/restapis/" + in.RestApiId + "/stages/" + in.StageName
		})))
	b.UpdateStage(s.updateStage, partial(updateStageNote),
		kernel.WithResources(resourceAt(func(in *apigatewayapi.UpdateStageInput) string {
			return "/restapis/" + in.RestApiId + "/stages/" + in.StageName
		})))
	b.DeleteStage(s.deleteStage, registry.Partial{Note: baseNote},
		kernel.WithResources(resourceAt(func(in *apigatewayapi.DeleteStageInput) string {
			return "/restapis/" + in.RestApiId + "/stages/" + in.StageName
		})))
	b.TagResource(s.tagResource, partial(tagsNote),
		kernel.WithResources(tagsResource(func(in *apigatewayapi.TagResourceInput) string { return in.ResourceArn })))
	b.UntagResource(s.untagResource, partial(tagsNote),
		kernel.WithResources(tagsResource(func(in *apigatewayapi.UntagResourceInput) string { return in.ResourceArn })))
	b.GetTags(s.getTags, partial(tagsNote),
		kernel.WithResources(tagsResource(func(in *apigatewayapi.GetTagsInput) string { return in.ResourceArn })))
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
	return store.View(s.cells, scope, func(st *State) []kernel.Resource {
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
