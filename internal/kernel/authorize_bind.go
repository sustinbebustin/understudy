package kernel

import (
	"context"
	"maps"
	"slices"
	"sync"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// scopedResources is one WithResourcesFor option, type-erased: the actions it
// names and how to resolve the resources they apply to from a valid input.
// It is erased where the option is built, not per binding, so operations
// without the option stamp out no code for it (ADR 0001's binary budget).
type scopedResources struct {
	actions []registry.IAMAction
	resolve func(c *Call, in any) []aws.ARN
}

// WithResourcesFor declares, from a call's valid input, the resources the
// named actions apply to, such as CopyObject's source for s3:GetObject and
// its destination for s3:PutObject. The ARNs are touched as WithResources
// touches them. Step 8 and the access record pair each named action with
// exactly the resources its WithResourcesFor options resolved, of the
// action's service: none gives AnyResource for an action of the operation's
// own service and no pair otherwise. Every other action keeps the pairing
// with every touched resource of its service (iam-enforcement-surfaces.md
// 12.6).
func WithResourcesFor[In any](actions []registry.IAMAction, resolve func(c *Call, in *In) []aws.ARN) BindOption {
	return scopedOf[*In](actions, resolve)
}

// scoping is what a call's WithResourcesFor options resolved: for each
// named action, the resources it applies to, without repeats; nil for a
// binding without the option.
type scoping map[registry.IAMAction][]aws.ARN

// resources returns every resource s holds, in the order its actions were
// resolved, without repeats.
func (s scoping) resources(order []registry.IAMAction) []aws.ARN {
	var out []aws.ARN
	for _, a := range order {
		for _, r := range s[a] {
			if !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	return out
}

// scope resolves every WithResourcesFor option in scoped for the valid input
// in: the resources each named action applies to, and all of them in order
// to touch; nil without an option.
func scope(scoped []scopedResources, c *Call, in any) (scoping, []aws.ARN) {
	if len(scoped) == 0 {
		return nil, nil
	}
	out := scoping{}
	var order []registry.IAMAction
	for _, o := range scoped {
		arns := o.resolve(c, in)
		for _, a := range o.actions {
			if _, seen := out[a]; !seen {
				out[a] = nil
				order = append(order, a)
			}
			for _, r := range arns {
				if !slices.Contains(out[a], r) {
					out[a] = append(out[a], r)
				}
			}
		}
	}
	return out, out.resources(order)
}

// AuthorizedAfterResolve moves step 8 for the operation into its handler,
// for an operation whose resource is named indirectly (an alias, a secret
// name): the kernel asks about the binding's pairs over the resources the
// call touched, at the first of the call's Write, its first Invoke or the
// handler's successful return, and once only. Those resources are what the
// binding's WithResources resolved at step 7, before the handler ran, not
// what the handler resolves again under its own read or write: a change
// that lands between the two, such as an UpdateAlias retargeting the alias,
// leaves the call asked about the resource step 7 resolved. A denial is what that Write or Invoke returns, before it takes a lock
// or calls; at the return it replaces the handler's output. A handler error
// before any of those is answered unauthorized, existence first. With several
// of AuthorizedInHandler, NeverAuthorized and AuthorizedAfterResolve, the
// last applies. A handler that maps errors from Invoke passes an
// awserr.Denial through unchanged.
func AuthorizedAfterResolve() BindOption { return authorizationOption(afterResolve) }

// WithConditionKeys supplies, from a call's valid input, request condition
// keys AWS sends with the call, such as sts:ExternalId, aws:RequestTag/<key>
// and aws:TagKeys. Step 8, and an AuthorizedAfterResolve binding's
// authorization, pass the union of every option's keys as AuthzQuery.Keys,
// later options replacing earlier ones key by key. A key with no values is
// one the request does not carry.
func WithConditionKeys[In any](supply func(c *Call, in *In) ConditionKeys) BindOption {
	return keysOf[*In](supply)
}

// conditionKeys returns the union of every WithConditionKeys option's keys
// in keys for the valid input in, a later option's key replacing an earlier
// one's; nil without an option.
func conditionKeys(keys []func(*Call, any) ConditionKeys, c *Call, in any) ConditionKeys {
	if len(keys) == 0 {
		return nil
	}
	out := ConditionKeys{}
	for _, supply := range keys {
		maps.Copy(out, supply(c, in))
	}
	return out
}

// deferred is the authorization an AuthorizedAfterResolve binding's call
// owes: step 8 leaves it on the call, and the call's first Write, its first
// Invoke or the handler's successful return asks it, once.
type deferred struct {
	// ctx is the pipeline's context, since Write takes none.
	ctx  context.Context //nolint:containedctx // Write has no context to pass; the call lives within the pipeline's.
	used secondaries
	keys ConditionKeys

	once sync.Once
	err  *awserr.Error
}

// authorizeDeferred asks the call's deferred authorization, the first time
// only, and returns the error the call ends with; nil when the call owes
// none or may go on.
func (c *Call) authorizeDeferred() *awserr.Error {
	d := c.deferred
	if d == nil {
		return nil
	}
	d.once.Do(func() {
		pairs := pairsOf(authorizedActions(c.op, d.used), c.op.Service.IAMPrefix, c.Touched(), c.scoped)
		if len(pairs) == 0 {
			return
		}
		d.err = verdictError(c, c.k.authorizer.Authorize(d.ctx, c, AuthzQuery{Pairs: pairs, Keys: d.keys}))
	})
	return d.err
}
