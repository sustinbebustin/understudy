package kernel

import (
	"context"
	"fmt"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Authorizer decides whether a call's principal may perform actions on
// resources (iam-enforcement-surfaces.md 12.1). The kernel asks it at
// pipeline step 8 and through Authorize. It must be safe for concurrent use
// and must not call Write or Invoke.
type Authorizer interface {
	Authorize(ctx context.Context, c *Call, q AuthzQuery) Verdict
}

// AuthzQuery is one authorization question about a call.
type AuthzQuery struct {
	// Pairs are every (action, resource) the call needs. A call is allowed
	// only when every pair is (iam-evaluation.md 2.1). The kernel does not
	// ask about a call with no pair.
	Pairs []Pair
	// Keys are condition keys the asker supplies beyond what an Authorizer
	// derives from the call itself; nil at step 8.
	Keys ConditionKeys
}

// Pair is one action on one resource.
type Pair struct {
	Action   registry.IAMAction
	Resource ResourceRef
}

// ResourceRef is what a pair's action is performed on.
//
//sumtype:decl
type ResourceRef interface{ isResourceRef() }

// ResourceARN is one resource.
type ResourceARN struct{ ARN aws.ARN }

// AnyResource is an action of the operation's own service whose call names
// no resource of that service, such as sqs:ListQueues; a policy grants it
// on "*".
type AnyResource struct{}

func (ResourceARN) isResourceRef() {}
func (AnyResource) isResourceRef() {}

// ConditionKeys are request condition keys by name as AWS spells them, each
// with its values in order.
type ConditionKeys map[string][]string

// Verdict is an Authorizer's answer.
//
//sumtype:decl
type Verdict interface{ isVerdict() }

// Allowed lets the call go on.
type Allowed struct{}

// Denied ends the call with its service's denial identity
// (registry.Service.Denial).
type Denied struct {
	// Message is the text the call answers with; it may be empty.
	Message string
	// Messages override Message on one protocol: SQS over Query names the
	// queue URL (iam-enforcement-surfaces.md 4.3).
	Messages []awserr.ProtocolMessage
}

func (Allowed) isVerdict() {}
func (Denied) isVerdict()  {}

// DenialIdentityError is a denial of a call whose service descriptor names
// no denial identity, so the kernel cannot render it as AWS would.
type DenialIdentityError struct {
	Service ref.ServiceID
	// Message is the denial text that could not be rendered.
	Message string
}

func (e *DenialIdentityError) Error() string {
	return fmt.Sprintf("kernel: the %s descriptor names no denial identity (dev.understudy#denialError), so the denial %q cannot be rendered; "+
		"add the trait to codegen/overlays/%s.json and run just generate", e.Service, e.Message, e.Service)
}

// Authorize asks the Authorizer whether c's principal may perform action on
// resource, for a handler that learns its resource itself: an IAM role or
// user by name, a KMS key behind an alias, a Secrets Manager secret by name,
// the role STS assumes, an EventBridge bus. Call it after resolving and
// before Write, holding no cell lock. It returns nil when the call may go
// on, else the error the handler returns.
//
// It is not implemented yet: it asks nothing and returns nil.
func Authorize(_ context.Context, _ *Call, _ registry.IAMAction, _ aws.ARN, _ ConditionKeys) *awserr.Error {
	return nil
}
