package kernel

import (
	"github.com/sustinbebustin/understudy/internal/aws"
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
//
// Not implemented yet: the option is accepted and has no effect.
func AuthorizedAfterResolve() BindOption { return pendingOption{} }

// WithConditionKeys supplies, from a call's valid input, request condition
// keys AWS sends with the call, such as sts:ExternalId, aws:RequestTag/<key>
// and aws:TagKeys. Step 8, and an AuthorizedAfterResolve binding's
// authorization, pass the union of every option's keys as AuthzQuery.Keys,
// later options replacing earlier ones key by key. A key with no values is
// one the request does not carry.
//
// Not implemented yet: the option is accepted and has no effect.
func WithConditionKeys[In any](_ func(c *Call, in *In) ConditionKeys) BindOption {
	return pendingOption{}
}
