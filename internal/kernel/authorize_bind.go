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
// call touched, at the first of the call's Write, its first Invoke, its
// AuthorizeResolved or the handler's successful return, and once only. Those resources are what the
// binding's WithResources resolved at step 7, before the handler ran, not
// what the handler resolves again under its own read or write: a change
// that lands between the two, such as an UpdateAlias retargeting the alias,
// leaves the call asked about the resource step 7 resolved. A denial is
// what that Write, Invoke or AuthorizeResolved returns, before it takes a
// lock or calls; at the return it replaces the handler's output. A handler error
// before any of those is answered unauthorized, existence first. With several
// of AuthorizedInHandler, NeverAuthorized and AuthorizedAfterResolve, the
// last applies. A handler that maps errors from Invoke passes an
// awserr.Denial through unchanged.
func AuthorizedAfterResolve() BindOption { return authorizationOption(afterResolve) }

// ResolveFirst names the not-found answer of a call whose resource does not
// exist, which AWS gives before it authorizes: an SQS queue, an S3 bucket
// or key (iam-enforcement-surfaces.md 5.3, IAM-ENF-14). Once the input is
// valid and its resources touched, and before step 8 asks anything, the
// kernel runs exists; a non-nil error ends the call, unauthorized, and the
// handler does not run. exists returns nil for a resource that exists, and
// for any answer that is not the resource's absence, which step 8 and the
// handler then decide as usual. It runs only when an Authorizer is set and
// the call is not a platform call: without authorization the handler
// answers the missing resource itself. exists may ask kernel.Authorize, as
// S3 asks s3:ListBucket about a missing key. Several options run in order;
// the first error ends the call.
func ResolveFirst[In any](exists func(ctx context.Context, c *Call, in *In) *awserr.Error) BindOption {
	return existsOf[*In](exists)
}

func existsOf[P any](exists func(context.Context, *Call, P) *awserr.Error) BindOption {
	var token P
	f := func(ctx context.Context, c *Call, in any) *awserr.Error {
		typed, ok := in.(P)
		if !ok {
			return nil
		}
		return exists(ctx, c, typed)
	}
	return typedOption{in: token, add: func(c *bindConfig) { c.exists = append(c.exists, f) }}
}

// resolveFirst runs the binding's ResolveFirst options over the valid input
// in, before step 8, and returns the error the call ends with; nil when the
// call goes on to step 8, and always without an Authorizer, for a raw HTTP
// binding and for a platform call.
func (k *Kernel) resolveFirst(ctx context.Context, c *Call, bnd *binding, in any) *awserr.Error {
	if _, platform := c.mode.(AsPlatform); k.authorizer == nil || bnd.raw || platform {
		return nil
	}
	for _, exists := range bnd.exists {
		if err := exists(ctx, c, in); err != nil {
			return err
		}
	}
	return nil
}

// AuthorizedAfterResolveForDeliveries asks a delivery (an AsService call)
// as AuthorizedAfterResolve does, existence first, and every other call at
// step 8, deny-first. It is for an operation that receives deliveries to a
// target named by name or URL, such as SQS SendMessage: AWS fails a
// delivery to a deleted target as the missing target, which has no policy
// left to deny it (sns.md 7.1; iam-enforcement-surfaces.md 8.4 keeps the
// missing target apart from the missing permission), while for a direct
// caller the order stays unrecorded (iam-enforcement-surfaces.md 5.3).
// With several of AuthorizedInHandler, NeverAuthorized,
// AuthorizedAfterResolve and this, the last applies.
func AuthorizedAfterResolveForDeliveries() BindOption {
	return authorizationOption(afterResolveForDeliveries)
}

// WithConditionKeys supplies, from a call's valid input, request condition
// keys AWS sends with the call, such as sts:ExternalId, aws:RequestTag/<key>
// and aws:TagKeys. Step 8, and an AuthorizedAfterResolve binding's
// authorization, pass the union of every option's keys as AuthzQuery.Keys,
// later options replacing earlier ones key by key. A key with no values is
// one the request does not carry.
func WithConditionKeys[In any](supply func(c *Call, in *In) ConditionKeys) BindOption {
	return keysOf[*In](supply)
}

// WithDenialResource names, from a call's valid input, the resource a
// denial of the operation's primary action names in its message when that
// is not the ARN the pair is evaluated on, such as the wildcard a list
// operation's denial names while its pair is on any resource
// (iam-enforcement-surfaces.md 12.3). It sets Pair.DenialResource on the
// primary action's pairs that step 8, a deferred authorization, or the
// handler's Authorize forms, such as IAM GetRole's "role <name>"; the evaluation and the access record keep the pair's resource. "" names
// the pair's resource. With several, the last applies.
func WithDenialResource[In any](name func(c *Call, in *In) string) BindOption {
	return denialResourceOf[*In](name)
}

// WithPrimaryAction chooses, from a call's valid input, the action the
// call is authorized and access-recorded as in place of the operation's
// primary (the first of Operation.IAM), when the input decides which
// action AWS evaluates: SES v2 SendEmail with Raw content is
// ses:SendRawEmail, an action its generated list does not hold (SES-28).
// The chosen action is the primary everywhere the generated one would be:
// step 8, a deferred authorization, AuthorizedInHandler, WithDenialResource
// and the access record. The zero action keeps the generated primary.
// With several, the last applies.
func WithPrimaryAction[In any](choose func(c *Call, in *In) registry.IAMAction) BindOption {
	return primaryOf[*In](choose)
}

// primary returns the call's primary action: what the binding's
// WithPrimaryAction chose for the input, else the first of the
// operation's generated list; false for an internal operation or one
// that lists none.
func (c *Call) primary() (registry.IAMAction, bool) {
	switch {
	case c.op.Kind == registry.Internal || len(c.op.IAM) == 0:
		return registry.IAMAction{}, false
	case c.chosenPrimary != registry.IAMAction{}:
		return c.chosenPrimary, true
	}
	return c.op.IAM[0], true
}

// named returns pairs with the call's denial resource text on each pair of
// the call's primary action; pairs unchanged without one.
func (c *Call) named(pairs []Pair) []Pair {
	primary, ok := c.primary()
	if c.denialResource == "" || !ok {
		return pairs
	}
	for i := range pairs {
		if pairs[i].Action == primary {
			pairs[i].DenialResource = c.denialResource
		}
	}
	return pairs
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

// scopedKeys is one WithConditionKeysFor option, type-erased as
// scopedResources is: the actions it names and how to supply their keys
// from a valid input.
type scopedKeys struct {
	actions []registry.IAMAction
	supply  func(c *Call, in any) ConditionKeys
}

// WithConditionKeysFor supplies, from a call's valid input, request
// condition keys AWS sends only with the named actions, such as the SSE
// keys of s3:PutObject, which a CopyObject's read of its source does not
// carry (iam-enforcement-surfaces.md 9.2). Step 8, and an
// AuthorizedAfterResolve binding's authorization, pass them as
// AuthzQuery.ActionKeys under each named action, later options replacing
// earlier ones key by key; a pair of a named action carries them over the
// WithConditionKeys keys (AuthzQuery.KeysFor). A key with no values is one
// the request does not carry. Every other action's pairs see the
// WithConditionKeys keys alone, and a key neither supplies stays as the
// Authorizer would have it without the option.
func WithConditionKeysFor[In any](actions []registry.IAMAction, supply func(c *Call, in *In) ConditionKeys) BindOption {
	return actionKeysOf[*In](actions, supply)
}

// actionConditionKeys returns, by action, the union of every
// WithConditionKeysFor option's keys in scoped naming it, for the valid
// input in, a later option's key replacing an earlier one's; nil without
// an option.
func actionConditionKeys(scoped []scopedKeys, c *Call, in any) ActionKeys {
	if len(scoped) == 0 {
		return nil
	}
	out := ActionKeys{}
	for _, o := range scoped {
		keys := o.supply(c, in)
		for _, a := range o.actions {
			if out[a] == nil {
				out[a] = ConditionKeys{}
			}
			maps.Copy(out[a], keys)
		}
	}
	return out
}

// deferred is the authorization an AuthorizedAfterResolve binding's call
// owes: step 8 leaves it on the call, and the call's first Write, its first
// Invoke, its AuthorizeResolved or the handler's successful return asks it,
// once.
type deferred struct {
	// ctx is the pipeline's context, since Write takes none.
	ctx     context.Context //nolint:containedctx // Write has no context to pass; the call lives within the pipeline's.
	used    secondaries
	keys    ConditionKeys
	refused []string

	once sync.Once
	err  *awserr.Error
}

// AuthorizeResolved asks c's deferred authorization now, as its first
// Write or Invoke would, and returns the error the call ends with; nil
// when c may go on or owes no deferred question (it was asked at step 8,
// or authorization is off). It is for an AuthorizedAfterResolve or
// AuthorizedAfterResolveForDeliveries handler that has resolved its
// resource and whose next work is neither, such as Lambda Invoke running
// a function, which must not run for a call that is denied. The call is
// still asked once only.
func AuthorizeResolved(c *Call) *awserr.Error { return c.authorizeDeferred() }

// authorizeDeferred asks the call's deferred authorization, the first time
// only, and returns the error the call ends with; nil when the call owes
// none or may go on.
func (c *Call) authorizeDeferred() *awserr.Error {
	d := c.deferred
	if d == nil {
		return nil
	}
	d.once.Do(func() {
		pairs := c.named(pairsOf(c.authorizedActions(d.used), c.op.Service.IAMPrefix, c.fixPairing(), c.scoped))
		if c.k.authorizer == nil || len(pairs) == 0 {
			return
		}
		d.err = verdictError(c, c.k.authorizer.Authorize(d.ctx, c, AuthzQuery{Pairs: pairs, Keys: d.keys, ActionKeys: c.actionKeys, Refused: d.refused}))
	})
	return d.err
}
