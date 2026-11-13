package kernel

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/access"
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
	// ActionKeys are condition keys supplied for the pairs of one action
	// alone (WithConditionKeysFor), applied over Keys; nil when the binding
	// names none and for a pair a handler asks about through Authorize.
	// KeysFor reads a pair's keys.
	ActionKeys ActionKeys
	// Refused are condition keys the call's binding refuses to have
	// evaluated as absent (RefuseConditionKeys); nil when it names none and
	// for a pair a handler asks about through Authorize.
	Refused []string
}

// Pair is one action on one resource.
type Pair struct {
	Action   registry.IAMAction
	Resource ResourceRef
	// DenialResource is the resource a denial of the pair names when it is
	// not Resource's ARN (WithDenialResource); "" names Resource. It is
	// text only: the pair is evaluated, and access-recorded, on Resource.
	DenialResource string
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

// ActionKeys are condition keys by the action whose pairs alone carry them,
// such as the SSE keys of S3's s3:PutObject that a CopyObject's source read
// does not carry (WithConditionKeysFor).
type ActionKeys map[registry.IAMAction]ConditionKeys

// KeysFor returns the condition keys a pair of action carries: q.Keys, with
// q.ActionKeys' keys for action replacing them key by key. The result is
// read only: it may be q.Keys itself.
func (q AuthzQuery) KeysFor(action registry.IAMAction) ConditionKeys {
	own, ok := q.ActionKeys[action]
	if !ok {
		return q.Keys
	}
	out := make(ConditionKeys, len(q.Keys)+len(own))
	maps.Copy(out, q.Keys)
	maps.Copy(out, own)
	return out
}

// Verdict is an Authorizer's answer.
//
//sumtype:decl
type Verdict interface{ isVerdict() }

// Allowed lets the call go on.
type Allowed struct{}

// Denied ends the call with its service's denial identity
// (registry.Service.Denial).
type Denied struct {
	// Message is the text the call answers with, on every protocol
	// (iam-enforcement-surfaces.md 4.3, IAM-ENF-02); it may be empty.
	Message string
}

// Unevaluable is an Authorizer that could not decide, such as for a stored
// policy it cannot read: the call ends Internal with Cause in the journal,
// never as if no policy existed (iam-enforcement-surfaces.md 12.10).
type Unevaluable struct{ Cause error }

// Unsupported is a call whose decision rests on what understudy cannot
// evaluate as AWS would, such as a condition on a key the binding refuses
// (RefuseConditionKeys): the call ends 501 NotImplemented with Message,
// never decided as if the condition were absent (CLAUDE.md invariant 3).
type Unsupported struct{ Message string }

func (Allowed) isVerdict()     {}
func (Denied) isVerdict()      {}
func (Unevaluable) isVerdict() {}
func (Unsupported) isVerdict() {}

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
	// afterResolveForDeliveries asks an AsService call as afterResolve
	// does and every other call as atStep8 does
	// (AuthorizedAfterResolveForDeliveries).
	afterResolveForDeliveries
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

func (cfg *bindConfig) setAuthorization(a authorization) { cfg.authorization = a }

// refusedKeysOption is the option RefuseConditionKeys returns. It fits
// every input type.
type refusedKeysOption []string

// refuses is a binding configuration that can record refused condition
// keys.
type refuses interface{ addRefused(keys []string) }

func (o refusedKeysOption) apply(cfg any) bool {
	c, ok := cfg.(refuses)
	if ok {
		c.addRefused(o)
	}
	return ok
}

func (cfg *bindConfig) addRefused(keys []string) { cfg.refused = append(cfg.refused, keys...) }

// RefuseConditionKeys names condition keys AWS sends with the operation's
// request that understudy cannot supply and must not evaluate as absent:
// under the absent-key rule (iam-evaluation.md 6.14) a Deny on a positive
// condition never applies, so a call AWS denies would go through. Step 8
// passes them as AuthzQuery.Refused, and an Authorizer that enforces
// answers Unsupported when a statement matching a pair tests one. Several
// options add up.
func RefuseConditionKeys(keys ...string) BindOption { return refusedKeysOption(slices.Clone(keys)) }

// fixedKeysOption is the option WithFixedConditionKeys returns. It fits
// every input type.
type fixedKeysOption ConditionKeys

// suppliesKeys is a binding configuration that can record a
// WithConditionKeys supply.
type suppliesKeys interface {
	addKeys(supply func(*Call, any) ConditionKeys)
}

func (o fixedKeysOption) apply(cfg any) bool {
	c, ok := cfg.(suppliesKeys)
	if ok {
		c.addKeys(func(*Call, any) ConditionKeys { return ConditionKeys(o) })
	}
	return ok
}

func (cfg *bindConfig) addKeys(supply func(*Call, any) ConditionKeys) {
	cfg.erased.keys = append(cfg.erased.keys, supply)
}

// WithFixedConditionKeys supplies request condition keys whose values do
// not depend on the input, such as the API version every operation of a
// service carries, as WithConditionKeys does: it joins their union in
// option order. Unlike WithConditionKeys it fits every input type, so a
// service helper can give it to each of its bindings.
func WithFixedConditionKeys(keys ConditionKeys) BindOption {
	return fixedKeysOption(maps.Clone(keys))
}

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
// AuthorizedAfterResolve binding is asked later (authorizeDeferred), and so
// is an AuthorizedAfterResolveForDeliveries binding's AsService call. keys
// are the binding's WithConditionKeys for the input.
//
// Whether or not an Authorizer runs, the resources the call touched when
// its pairs are formed here (or, deferred, in authorizeDeferred) are the
// ones the access record pairs its actions with (pairingResources), so the
// record holds what step 8 asks under every iam.enforce mode, not a
// resource the handler creates afterwards (ARCHITECTURE.md 17).
func (k *Kernel) authorize(ctx context.Context, c *Call, bnd *binding, used secondaries, keys ConditionKeys) *awserr.Error {
	if _, platform := c.mode.(AsPlatform); bnd.raw || platform {
		return nil
	}
	actions := c.authorizedActions(used)
	switch bnd.authorization {
	case atStep8:
	case inHandler:
		if primary, ok := c.primary(); ok {
			actions = slices.DeleteFunc(actions, func(a registry.IAMAction) bool { return a == primary })
		}
	case never:
		return nil
	case afterResolve:
		c.deferred = &deferred{ctx: ctx, used: used, keys: keys, refused: bnd.refused}
		return nil
	case afterResolveForDeliveries:
		if _, delivery := c.mode.(AsService); delivery {
			c.deferred = &deferred{ctx: ctx, used: used, keys: keys, refused: bnd.refused}
			return nil
		}
	}
	pairs := c.named(pairsOf(actions, c.op.Service.IAMPrefix, c.fixPairing(), c.scoped))
	if k.authorizer == nil || len(pairs) == 0 {
		return nil
	}
	return verdictError(c, k.authorizer.Authorize(ctx, c, AuthzQuery{Pairs: pairs, Keys: keys, ActionKeys: c.actionKeys, Refused: bnd.refused}))
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
	case Unsupported:
		return &awserr.Error{ID: awserr.NotImplemented(), Message: v.Message, Origin: awserr.Generic{}}
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
	return &awserr.Error{ID: svc.Denial.Identity, Message: d.Message, Origin: awserr.Denial{}}
}

// authorizedActions returns the actions step 8 asks about, in order and
// without duplicates: the call's primary action, then the actions its
// binding declared for the input that the operation lists; for an internal
// operation, which lists none, the declared actions alone. Unlike the
// access record, a binding that declares nothing contributes only the
// primary: the generated list holds every action that may be checked, not
// every one required, so asking about the whole list would deny calls AWS
// allows (dynamodb:ReadDataForReplication on a Scan).
func (c *Call) authorizedActions(used secondaries) []registry.IAMAction {
	op := c.op
	var out []registry.IAMAction
	if primary, ok := c.primary(); ok {
		out = append(out, primary)
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
// condition keys only the handler knows, and the access record keeps an
// iam:PassRole's iam:PassedToService from them. A question about the
// call's primary action carries the binding's WithDenialResource text,
// as at step 8. Like step 8 it asks nothing when
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
	pair := c.named([]Pair{{Action: action, Resource: ResourceARN{ARN: resource}}})[0]
	c.Touch(resource)
	c.asked(pair, keys)
	if _, platform := c.mode.(AsPlatform); c.k.authorizer == nil || platform {
		return nil
	}
	return verdictError(c, c.k.authorizer.Authorize(ctx, c, AuthzQuery{Pairs: []Pair{pair}, Keys: keys}))
}

// AuthorizeAs asks the Authorizer whether the principal as resolves to,
// rather than c's caller, may perform q's pairs: for a handler that checks
// a principal the request names, such as Step Functions asking whether
// states.amazonaws.com may assume the execution role a state machine is
// given (AsService), or whether that role may deliver its logs
// (AsExecutionRole). The question is asked within c: an Authorizer's
// journal entries are c's, and a denial is c's service's denial identity,
// which the handler may reword. Nothing is touched, and c's caller's
// access record does not take the pairs, since the caller does not use
// them. A question about an execution role (AsExecutionRole) is
// access-recorded as that role's own use, whatever the verdict and whether
// or not an Authorizer runs, as Authorize records the caller's: the
// policy rendered for the role then grants what the role was asked about,
// such as sqs:SendMessage on a Lambda destination
// (iam-enforcement-surfaces.md 8.5, 12.6). A question about a service
// principal is not recorded: what it needs is a grant in a trust or
// resource policy, not an identity policy. Like Authorize it asks nothing
// when authorization is off, about a platform principal (AsPlatform),
// which no policy grants, or about no pair. Asking while holding a cell
// lock, without a mode, or for a call that names no modeled operation is a
// caller bug: Internal, and the Authorizer is not asked.
func AuthorizeAs(ctx context.Context, c *Call, as PrincipalMode, q AuthzQuery) *awserr.Error {
	var action registry.IAMAction
	if len(q.Pairs) > 0 {
		action = q.Pairs[0].Action
	}
	switch {
	case c.holding.Load():
		return awserr.Internalf(&HoldingError{Attempted: "kernel.AuthorizeAs of " + action.String(), Holder: c.label})
	case as == nil:
		return awserr.Internalf(&AuthorizeError{Op: c.opID, Action: action, Reason: "the principal mode is nil; say whose permission is asked about"})
	case c.op == nil:
		return awserr.Internalf(&AuthorizeError{Op: c.opID, Action: action, Reason: "the call names no modeled operation, so it has no denial identity"})
	}
	if role, ok := as.(AsExecutionRole); ok && len(q.Pairs) > 0 {
		c.askedAsRole(executionRole(role), q)
	}
	if _, platform := as.(AsPlatform); c.k.authorizer == nil || platform || len(q.Pairs) == 0 {
		return nil
	}
	asked := &Call{
		k:         c.k,
		id:        c.id,
		scope:     c.scope,
		principal: resolvePrincipal(c, as),
		origin:    c.origin,
		mode:      as,
		op:        c.op,
		opID:      c.opID,
		label:     c.label,
		event:     c.event,
		cause:     c.cause,
		admitted:  c.admitted,
		endpoint:  c.endpoint,
		trace:     c.trace,
		elapsed:   c.elapsed,
	}
	return verdictError(asked, c.k.authorizer.Authorize(ctx, asked, q))
}

// AuthorizeBefore asks the Authorizer, before an internal call of op that
// t describes, about what the principal t.As resolves to needs beyond
// op's own action: for a delivery whose sender checks it before calling
// the target, such as a Scheduler invocation asking whether
// scheduler.amazonaws.com may still assume the execution role and whether
// the role session may decrypt with the schedule's key (scheduler.md 9,
// 14.11). It is for parents that name no modeled operation, such as a
// timer callback, where AuthorizeAs has no denial identity to answer
// with: the question is asked as the internal call of op would be, in
// t.Scope, so a denial is op's service's denial identity, which the
// caller may reword, and an Authorizer's journal entries continue
// t.Resume, else the parent. Nothing is touched; op is not called. As in
// AuthorizeAs, a question about an execution role (AsExecutionRole) is
// access-recorded as that role's own use, whatever the verdict and whether
// or not an Authorizer runs, so the policy rendered for the role grants
// it, such as kms:Decrypt on a schedule's key (scheduler.md 14.11); a
// question about a service principal is not. Like AuthorizeAs it asks
// nothing when authorization is off, and asks and records nothing about a
// platform principal (AsPlatform) or about no pair. A nil
// parent, op or t.As, a t.Scope with no account, or a parent holding a
// cell lock is a caller bug: Internal, and the Authorizer is not asked.
func AuthorizeBefore(ctx context.Context, k *Kernel, parent *Call, op *registry.Operation, t Target, q AuthzQuery) *awserr.Error {
	var action registry.IAMAction
	if len(q.Pairs) > 0 {
		action = q.Pairs[0].Action
	}
	var id ref.OperationID
	if op != nil {
		id = op.ID
	}
	switch {
	case parent == nil:
		return awserr.Internalf(&AuthorizeError{Op: id, Action: action, Reason: "no parent call; ask on behalf of a call or a timer"})
	case parent.holding.Load():
		return awserr.Internalf(&HoldingError{Attempted: "kernel.AuthorizeBefore of " + action.String(), Holder: parent.label})
	case op == nil:
		return awserr.Internalf(&AuthorizeError{Action: action, Reason: "no operation; name the internal call the question comes before"})
	case t.As == nil:
		return awserr.Internalf(&AuthorizeError{Op: id, Action: action, Reason: "Target.As is nil; say whose permission is asked about"})
	case t.Scope.Account.IsZero():
		return awserr.Internalf(&AuthorizeError{Op: id, Action: action, Reason: "Target.Scope has no account; take it from the target resource"})
	}
	if _, platform := t.As.(AsPlatform); platform || len(q.Pairs) == 0 {
		return nil
	}
	var err *awserr.Error
	if k.authorizer != nil {
		c := k.internalCall(parent, id, t)
		if c.op == nil {
			return awserr.Internalf(&AuthorizeError{Op: id, Action: action, Reason: "the operation is not in the kernel's registry, so it has no denial identity"})
		}
		// The call is never dispatched, so it has no event of its own: what
		// the Authorizer journals is a child of the event it continues.
		c.event = c.cause.Parent
		err = verdictError(c, k.authorizer.Authorize(ctx, c, q))
	}
	if role, ok := t.As.(AsExecutionRole); ok && k.access != nil {
		var outcome access.Outcome = access.Succeeded{}
		if err != nil {
			outcome = access.Failed{Code: err.ID.Shape.Name()}
		}
		at := t.At
		if at.IsZero() {
			at = parent.admitted
		}
		k.recordRoleUse(ctx, op, parent.id, roleAsk{principal: executionRole(role), query: q}, outcome, at.Wire())
	}
	return err
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
// bound AuthorizedAfterResolve or ResolveFirst: a handler error before the
// deferred question, such as the not-found error of a resource looked up
// by name, or a ResolveFirst check's not-found error, answers the call
// before it is authorized (iam-enforcement-surfaces.md 5.3, 12.8).
func (b *Bindings) ResolvedFirstOps(svc *registry.Service) []string {
	var out []string
	for _, op := range svc.Operations {
		bnd, ok := b.lookup(op)
		if !ok || op.Kind != registry.Modeled || bnd.raw || (bnd.authorization != afterResolve && len(bnd.exists) == 0) {
			continue
		}
		out = append(out, op.ID.String())
	}
	return out
}

// NamedDenialOps lists, as AuthorizedOps does, the operations of svc bound
// WithDenialResource: a denial names the resource by a text other than the
// ARN its pair is evaluated on, such as the name an IAM role or a Secrets
// Manager secret is given by (iam-enforcement-surfaces.md 5.3, 12.3).
func (b *Bindings) NamedDenialOps(svc *registry.Service) []string {
	var out []string
	for _, op := range svc.Operations {
		bnd, ok := b.lookup(op)
		if !ok || op.Kind != registry.Modeled || bnd.raw || bnd.authorization == never || bnd.denialResource == nil {
			continue
		}
		out = append(out, op.ID.String())
	}
	return out
}

// DeliveryResolvedFirstOps lists, as AuthorizedOps does, the operations of
// svc bound AuthorizedAfterResolveForDeliveries: a delivery to a target
// that does not exist answers the target's not-found error before it is
// authorized (sns.md 7.1; iam-enforcement-surfaces.md 8.2, 12.8).
func (b *Bindings) DeliveryResolvedFirstOps(svc *registry.Service) []string {
	var out []string
	for _, op := range svc.Operations {
		bnd, ok := b.lookup(op)
		if !ok || op.Kind != registry.Modeled || bnd.raw || bnd.authorization != afterResolveForDeliveries {
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
