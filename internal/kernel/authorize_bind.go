package kernel

import (
	"context"
	"maps"
	"sync"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// pendingOption is a bind option this build accepts for every input type and
// does not apply yet.
type pendingOption struct{}

func (pendingOption) apply(any) bool { return true }

// WithResourcesFor declares, from a call's valid input, the resources the
// named actions apply to, such as CopyObject's source for s3:GetObject and
// its destination for s3:PutObject. The ARNs are touched as WithResources
// touches them. Step 8 and the access record pair each named action with
// exactly the resources its WithResourcesFor options resolved, of the
// action's service: none gives AnyResource for an action of the operation's
// own service and no pair otherwise. Every other action keeps the pairing
// with every touched resource of its service (iam-enforcement-surfaces.md
// 12.6).
//
// Not implemented yet: the option is accepted and has no effect.
func WithResourcesFor[In any](_ []registry.IAMAction, _ func(c *Call, in *In) []aws.ARN) BindOption {
	return pendingOption{}
}

// AuthorizedAfterResolve moves step 8 for the operation into its handler,
// for a handler that resolves its resource by name: the kernel asks about the
// binding's pairs over the resources the call touched, at the first of the
// call's Write, its first Invoke or the handler's successful return, and once
// only. A denial is what that Write or Invoke returns, before it takes a lock
// or calls; at the return it replaces the handler's output. A handler error
// before any of those is answered unauthorized, existence first. With several
// of AuthorizedInHandler, NeverAuthorized and AuthorizedAfterResolve, the
// last applies. A handler that maps errors from Invoke passes an
// awserr.Denial through unchanged.
func AuthorizedAfterResolve() BindOption { return authorizationOption(afterResolve) }

type conditionKeysOption[In any] func(*Call, *In) ConditionKeys

func (o conditionKeysOption[In]) apply(cfg any) bool {
	c, ok := cfg.(*bindConfig[In])
	if ok {
		c.keys = append(c.keys, o)
	}
	return ok
}

// WithConditionKeys supplies, from a call's valid input, request condition
// keys AWS sends with the call, such as sts:ExternalId, aws:RequestTag/<key>
// and aws:TagKeys. Step 8, and an AuthorizedAfterResolve binding's
// authorization, pass the union of every option's keys as AuthzQuery.Keys,
// later options replacing earlier ones key by key. A key with no values is
// one the request does not carry.
func WithConditionKeys[In any](supply func(c *Call, in *In) ConditionKeys) BindOption {
	return conditionKeysOption[In](supply)
}

// conditionKeys returns the union of every WithConditionKeys option's keys
// for the input in, a later option's key replacing an earlier one's; nil
// without an option.
func (cfg *bindConfig[In]) conditionKeys(c *Call, in *In) ConditionKeys {
	if len(cfg.keys) == 0 {
		return nil
	}
	out := ConditionKeys{}
	for _, supply := range cfg.keys {
		maps.Copy(out, supply(c, in))
	}
	return out
}

// erasedKeys is conditionKeys for the type-erased binding; nil without a
// WithConditionKeys option.
func (cfg *bindConfig[In]) erasedKeys() func(c *Call, in any) ConditionKeys {
	if len(cfg.keys) == 0 {
		return nil
	}
	return func(c *Call, in any) ConditionKeys {
		typed, ok := in.(*In)
		if !ok {
			return nil
		}
		return cfg.conditionKeys(c, typed)
	}
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
		pairs := pairsOf(authorizedActions(c.op, d.used), c.op.Service.IAMPrefix, c.Touched())
		if len(pairs) == 0 {
			return
		}
		d.err = verdictError(c, c.k.authorizer.Authorize(d.ctx, c, AuthzQuery{Pairs: pairs, Keys: d.keys}))
	})
	return d.err
}
