package kernel

import (
	"context"
	"fmt"
	"slices"

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

// VerdictError is an Authorizer that answered a call with a nil Verdict, so
// the kernel cannot tell whether the call may go on.
type VerdictError struct {
	Op ref.OperationID
}

func (e *VerdictError) Error() string {
	return fmt.Sprintf("kernel: the Authorizer answered the %s call with no verdict; return kernel.Allowed{} or kernel.Denied{}", e.Op)
}

// authorize is pipeline step 8's authorization, after validation and before
// the request-phase fault rules (iam-enforcement-surfaces.md 12.1). It asks
// the Authorizer about the pairs the call's binding declares and returns
// the error the call ends with, nil when it may go on. It asks nothing when
// authorization is off, for a raw HTTP binding (which authorizes itself
// through Authorize or authz.CheckResource), for a platform call (which no
// policy grants, so recordAccess leaves it out too) and for a call with no
// pair.
func (k *Kernel) authorize(ctx context.Context, c *Call, bnd *binding, used secondaries) *awserr.Error {
	if _, platform := c.mode.(AsPlatform); k.authorizer == nil || bnd.raw || platform {
		return nil
	}
	pairs := pairsOf(authorizedActions(c.op, used), c.op.Service.IAMPrefix, c.Touched())
	if len(pairs) == 0 {
		return nil
	}
	return verdictError(c, k.authorizer.Authorize(ctx, c, AuthzQuery{Pairs: pairs}))
}

// verdictError returns the error c ends with for v, nil for Allowed.
func verdictError(c *Call, v Verdict) *awserr.Error {
	switch v := v.(type) {
	case Allowed:
		return nil
	case Denied:
		return denial(c, v)
	case nil:
	}
	return awserr.Internalf(&VerdictError{Op: c.opID})
}

// denial renders d as the denial identity of c's service
// (iam-enforcement-surfaces.md 12.3). A service whose descriptor names none
// gets Internal, never a generic identity (CLAUDE.md invariant 3).
func denial(c *Call, d Denied) *awserr.Error {
	svc := c.op.Service
	if svc.Denial == (registry.Denial{}) {
		return awserr.Internalf(&DenialIdentityError{Service: svc.ID, Message: d.Message})
	}
	return &awserr.Error{ID: svc.Denial.Identity, Message: d.Message, Messages: d.Messages, Origin: awserr.Denial{}}
}

// authorizedActions returns the actions step 8 asks about, in order and
// without duplicates: the operation's primary action, then the actions its
// binding declared for the input that the operation lists; for an internal
// operation, which lists none, the declared actions alone. Unlike the
// access record, a binding that declares nothing contributes only the
// primary: the generated list holds every action that may be checked, not
// every one required, so asking about the whole list would deny calls AWS
// allows (dynamodb:ReadDataForReplication on a Scan).
func authorizedActions(op *registry.Operation, used secondaries) []registry.IAMAction {
	var out []registry.IAMAction
	if op.Kind != registry.Internal && len(op.IAM) > 0 {
		out = append(out, op.IAM[0])
	}
	switch used := used.(type) {
	case undeclared:
	case declared:
		for _, a := range used {
			if op.Kind == registry.Internal || slices.Contains(op.IAM, a) {
				out = append(out, a)
			}
		}
	}
	return compactActions(out)
}

// pairsOf pairs each action with every touched resource of its service, in
// first-touch order (an sts action with IAM roles); an action with none is
// paired with any resource when it is of the operation's own service
// (opService), and is not asked about otherwise. It is the access
// recorder's pairing (ARCHITECTURE.md 17) without its S3 bucket folding: an
// S3 object stays the object.
func pairsOf(actions []registry.IAMAction, opService string, touched []aws.ARN) []Pair {
	var pairs []Pair
	for _, a := range actions {
		service := a.Prefix
		if service == "sts" {
			service = "iam"
		}
		before := len(pairs)
		for _, r := range touched {
			if r.Service == service {
				pairs = append(pairs, Pair{Action: a, Resource: ResourceARN{ARN: r}})
			}
		}
		if len(pairs) == before && a.Prefix == opService {
			pairs = append(pairs, Pair{Action: a, Resource: AnyResource{}})
		}
	}
	return pairs
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
