package apigateway

import (
	"errors"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
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
}

// Service is Amazon API Gateway's REST APIs (apigateway): one state cell
// per account and region holding its REST APIs, and the execute-api data
// plane. It is not implemented yet: New binds nothing, so every operation
// answers 501.
type Service struct{}

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// New binds the operations understudy implements. Every other operation
// stays unbound and answers 501. It binds nothing yet.
func New(_ kernel.ServiceDeps, _ Options) *Service { return &Service{} }

// Name returns "apigateway", the service's registry id and ARN namespace.
func (*Service) Name() ref.ServiceID { return "apigateway" }

// SchemaVersion returns the version of the service's state.
func (*Service) SchemaVersion() int { return stateVersion }

// Export serializes every non-empty scope to one section. The service
// holds no state yet, so it exports none.
func (*Service) Export() ([]store.Section, error) { return nil, nil }

// errImportNotImplemented refuses sections until Import is implemented.
var errImportNotImplemented = errors.New("apigateway: snapshot import is not implemented yet; no API Gateway REST API state was loaded")

// Import loads sections written by Export of this schema version. It is
// not implemented yet and refuses any section.
func (*Service) Import(ss []store.Section) error {
	if len(ss) == 0 {
		return nil
	}
	return errImportNotImplemented
}

// Reset drops the scopes rs covers. There is no state to drop yet.
func (*Service) Reset(kernel.ResetScope) {}

// Rearm rebuilds timers; the service has none.
func (*Service) Rearm(clock.Instant) {}

// Resources lists the REST APIs of scope. There are none yet.
func (*Service) Resources(aws.Scope) []kernel.Resource { return nil }

// Scopes lists the scopes that hold state. There are none yet.
func (*Service) Scopes() []aws.Scope { return nil }
