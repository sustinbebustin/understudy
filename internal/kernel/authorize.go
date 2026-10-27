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
// on "*". An operation whose handler resolves its resource itself is bound
// AuthorizedInHandler, so its primary action is never paired with it.
type AnyResource struct{}

func (ResourceARN) isResourceRef() {}
func (AnyResource) isResourceRef() {}

// ConditionKeys are request condition keys by name as AWS spells them, each
// with its values in order. A key with no values is one the request does not
// carry.
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

// Unevaluable is an Authorizer that could not decide, such as for a stored
// policy it cannot read: the call ends Internal with Cause in the journal,
// never as if no policy existed (iam-enforcement-surfaces.md 12.10).
type Unevaluable struct{ Cause error }

func (Allowed) isVerdict()     {}
func (Denied) isVerdict()      {}
func (Unevaluable) isVerdict() {}

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

// authorization is what step 8 asks about a binding's calls.
type authorization uint8

const (
	// atStep8 asks about every pair the binding declares.
	atStep8 authorization = iota
	// inHandler leaves the operation's primary action to the handler
	// (AuthorizedInHandler).
	inHandler
	// never asks nothing (NeverAuthorized).
	never
	// afterResolve asks about every pair the binding declares once the
	// handler resolved its resources (AuthorizedAfterResolve).
	afterResolve
)

// authorizationOption is the option AuthorizedInHandler, NeverAuthorized
// and AuthorizedAfterResolve return. It fits every input type.
type authorizationOption authorization

// authorizes is a binding configuration that can record an authorization.
type authorizes interface{ setAuthorization(authorization) }

func (o authorizationOption) apply(cfg any) bool {
	c, ok := cfg.(authorizes)
	if ok {
		c.setAuthorization(authorization(o))
	}
	return ok
}

func (cfg *bindConfig[In]) setAuthorization(a authorization) { cfg.authorization = a }

// AuthorizedInHandler marks an operation whose handler resolves its
// resource itself and asks about the operation's primary action on it
// through Authorize, such as IAM GetRole on the role it reads. Step 8
// leaves the primary action out: before the handler nothing names the
// resource, so step 8 would ask about the action on AnyResource, which a
// policy granting it only on that resource does not allow, though AWS
// evaluates the call against the resource (iam-evaluation.md 2.1). The
// other actions the binding declares are asked about at step 8 as usual.
// With several of AuthorizedInHandler, NeverAuthorized and
// AuthorizedAfterResolve, the last applies.
func AuthorizedInHandler() BindOption { return authorizationOption(inHandler) }

// NeverAuthorized marks an operation AWS evaluates no policy for, not even
// an explicit Deny: sts:GetCallerIdentity (iam-enforcement-surfaces.md 5.1,
// 12.10). Step 8 never asks about its calls.
func NeverAuthorized() BindOption { return authorizationOption(never) }

// authorize is pipeline step 8's authorization, after validation and before
// the request-phase fault rules (iam-enforcement-surfaces.md 12.1). It asks
// the Authorizer about the pairs the call's binding declares and returns
// the error the call ends with, nil when it may go on. It asks nothing when
// authorization is off, for a raw HTTP binding (not authorized at all
// unless its handler calls Authorize or authz.CheckResource), for a
// platform call (which no policy grants, so recordAccess leaves it out
// too), for a NeverAuthorized binding and for a call with no pair. An
// AuthorizedInHandler binding is not asked about its primary action; an
// AuthorizedAfterResolve binding is asked later (authorizeDeferred). keys
// are the binding's WithConditionKeys for the input.
func (k *Kernel) authorize(ctx context.Context, c *Call, bnd *binding, used secondaries, keys ConditionKeys) *awserr.Error {
	if _, platform := c.mode.(AsPlatform); k.authorizer == nil || bnd.raw || platform {
		return nil
	}
	actions := authorizedActions(c.op, used)
	switch bnd.authorization {
	case atStep8:
	case inHandler:
		if len(c.op.IAM) > 0 {
			actions = slices.DeleteFunc(actions, func(a registry.IAMAction) bool { return a == c.op.IAM[0] })
		}
	case never:
		return nil
	case afterResolve:
		c.deferred = &deferred{ctx: ctx, used: used, keys: keys}
		return nil
	}
	pairs := pairsOf(actions, c.op.Service.IAMPrefix, c.Touched(), c.scoped)
	if len(pairs) == 0 {
		return nil
	}
	return verdictError(c, k.authorizer.Authorize(ctx, c, AuthzQuery{Pairs: pairs, Keys: keys}))
}

// verdictError returns the error c ends with for v, nil for Allowed.
func verdictError(c *Call, v Verdict) *awserr.Error {
	switch v := v.(type) {
	case Allowed:
		return nil
	case Denied:
		return denial(c, v)
	case Unevaluable:
		return awserr.Internalf(v.Cause)
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

// pairsOf pairs each action with the resources of its service it applies
// to (an sts action with IAM roles): those its WithResourcesFor options
// resolved when scoped names it, else every touched one, in first-touch
// order (iam-enforcement-surfaces.md 12.6). An action with none is paired
// with any resource when it is of the operation's own service (opService),
// and is not asked about otherwise. It is the access recorder's pairing
// (ARCHITECTURE.md 17) without its S3 bucket folding: an S3 object stays
// the object.
func pairsOf(actions []registry.IAMAction, opService string, touched []aws.ARN, scoped scoping) []Pair {
	var pairs []Pair
	for _, a := range actions {
		resources, named := scoped[a]
		if !named {
			resources = touched
		}
		service := a.Prefix
		if service == "sts" {
			service = "iam"
		}
		before := len(pairs)
		for _, r := range resources {
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
// The resource is touched whatever the verdict, so the journal lists it,
// and the access record keeps the action on it, whatever the verdict and
// whether or not an Authorizer runs, so a policy rendered from the record
// grants what the handler asked about (iam-enforcement-surfaces.md 12.6).
// Action may be any service's (iam:PassRole from Lambda); keys are
// condition keys only the handler knows. Like step 8 it asks nothing when
// authorization is off or the call is a platform call.
// Asking while holding a cell lock, about the zero ARN, or for a call that
// names no modeled operation is a caller bug: Internal, and the Authorizer
// is not asked.
func Authorize(ctx context.Context, c *Call, action registry.IAMAction, resource aws.ARN, keys ConditionKeys) *awserr.Error {
	switch {
	case c.holding.Load():
		return awserr.Internalf(&HoldingError{Attempted: "kernel.Authorize of " + action.String(), Holder: c.label})
	case resource == aws.ARN{}:
		return awserr.Internalf(&AuthorizeError{Op: c.opID, Action: action, Reason: "the resource is the zero ARN; resolve the resource before asking about it"})
	case c.op == nil:
		return awserr.Internalf(&AuthorizeError{Op: c.opID, Action: action, Reason: "the call names no modeled operation, so it has no denial identity"})
	}
	pair := Pair{Action: action, Resource: ResourceARN{ARN: resource}}
	c.Touch(resource)
	c.asked(pair)
	if _, platform := c.mode.(AsPlatform); c.k.authorizer == nil || platform {
		return nil
	}
	return verdictError(c, c.k.authorizer.Authorize(ctx, c, AuthzQuery{Pairs: []Pair{pair}, Keys: keys}))
}

// AuthorizedOps lists, as ref.OperationID strings in registry order, the
// modeled operations of svc bound in this kernel whose calls the Authorizer
// may deny with svc's denial identity: every bound one but a raw HTTP
// binding and a NeverAuthorized one. A service's denial switches govern
// them (iam-enforcement-surfaces.md 12.8).
func (b *Bindings) AuthorizedOps(svc *registry.Service) []string {
	var out []string
	for _, op := range svc.Operations {
		bnd, ok := b.lookup(op)
		if !ok || op.Kind != registry.Modeled || bnd.raw || bnd.authorization == never {
			continue
		}
		out = append(out, op.ID.String())
	}
	return out
}

// ResolvedFirstOps lists, as AuthorizedOps does, the operations of svc
// bound AuthorizedAfterResolve: a handler error before the deferred
// question, such as the not-found error of a resource looked up by name,
// answers the call before it is authorized (iam-enforcement-surfaces.md
// 5.3, 12.8).
func (b *Bindings) ResolvedFirstOps(svc *registry.Service) []string {
	var out []string
	for _, op := range svc.Operations {
		bnd, ok := b.lookup(op)
		if !ok || op.Kind != registry.Modeled || bnd.raw || bnd.authorization != afterResolve {
			continue
		}
		out = append(out, op.ID.String())
	}
	return out
}

// AuthorizeError is a kernel.Authorize the kernel refuses to ask about.
type AuthorizeError struct {
	Op     ref.OperationID
	Action registry.IAMAction
	Reason string
}

func (e *AuthorizeError) Error() string {
	return fmt.Sprintf("kernel.Authorize of %s from %s: %s", e.Action, e.Op, e.Reason)
}
