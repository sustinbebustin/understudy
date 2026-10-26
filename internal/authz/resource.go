package authz

import (
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// PolicyProvider reads the stored resource policy of resources in one ARN
// namespace: a queue's or topic's Policy, a bucket policy, a function's
// permissions, a key policy, a secret's resource policy, a role's trust
// policy.
//
// NOTE: a stored text policydoc cannot parse reads as nil, the same as no
// policy (policydoc.ParseStored). S3 PutBucketPolicy and SQS
// SetQueueAttributes check only a policy's JSON form, so they can store
// one. That is harmless while stored policies only feed advisories, but an
// explicit Deny in such a text would vanish under enforcement, so the
// result must tell "unreadable" apart from "none" before an evaluator
// reads it (iam-enforcement-surfaces.md 12.10).
type PolicyProvider interface {
	// ResourcePolicy returns target's stored policy, nil when it has none.
	// ok is false when target does not exist or its kind has no resource
	// policy.
	ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool)
}

// ResourceRequest asks whether a resource policy lets Caller call Action on
// Resource.
type ResourceRequest struct {
	// Caller is who calls: a policydoc.AWSCaller, ServiceCaller or
	// Anonymous; nil is Anonymous.
	Caller policydoc.Caller
	Action registry.IAMAction
	// Resource is the resource's ARN text, as the policy's Resource element
	// matches it.
	Resource string
	// Keys are the request's condition keys: aws:SourceArn, and
	// aws:SourceAccount when the delivery sends it, for a service principal
	// acting for a source; lambda:FunctionUrlAuthType for a function URL.
	Keys map[string]string
}

// CheckResource answers whether r's caller may call r's action on r's
// resource from doc, the resource's stored policy alone; a nil doc is a
// resource with no policy, which allows nothing (iam-evaluation.md 2.9,
// 2.10). It evaluates doc as a resource-based policy with policydoc.Evaluate:
// an applying Deny gives ExplicitDeny naming the first, else an applying
// Allow gives Allowed, else ImplicitDeny. A key r.Keys lacks reads by the
// truth table of iam-evaluation.md 6.14, never as holding, so Allowed.Assumed
// and Allowed.Unsettled are always empty; a key of the action's own service
// that r.Keys lacks is one AWS would send, which evaluates as absent too. A
// caller supplies every key AWS sends with the request, as function URLs do.
// A caller ARN maps onto a requester as policydoc.RequesterOf says, so a role
// ARN in the policy names that role's sessions (iam-evaluation.md 2.3). It
// does not read iam.enforce: each caller decides when its refusal applies.
func CheckResource(doc *policydoc.Document, r ResourceRequest) policydoc.Decision {
	if doc == nil {
		return policydoc.ImplicitDeny{}
	}
	ctx := policydoc.NewContext(r.Action.Prefix)
	for _, key := range slices.Sorted(maps.Keys(r.Keys)) {
		ctx.Set(key, r.Keys[key])
	}
	res := policydoc.Evaluate(*doc, policydoc.ResourcePolicy, policydoc.Request{
		Requester: policydoc.RequesterOf(r.Caller),
		Action:    r.Action.String(),
		Resource:  r.Resource,
		Context:   ctx,
	})
	switch {
	case len(res.Denies) > 0:
		return policydoc.ExplicitDeny{Statement: res.Denies[0].Statement}
	case len(res.Allows) > 0:
		return policydoc.Allowed{}
	default:
		return policydoc.ImplicitDeny{}
	}
}
