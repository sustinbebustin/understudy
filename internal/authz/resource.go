package authz

import (
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
// 2.10). It applies policydoc.Allows: an explicit Deny wins, then any
// Allow. That is not yet IAM's handling of absent keys. A condition clause
// on a key r.Keys lacks, or with an operator policydoc does not evaluate,
// is assumed to hold in an Allow (Allowed.Assumed) and keeps a Deny from
// denying (Allowed.Unsettled), where AWS reads an absent key by the truth
// table of iam-evaluation.md 6.14 (iam-enforcement-surfaces.md 12.2). So a
// caller supplies every key AWS sends with the request, as function URLs
// do. It does not read iam.enforce: each caller decides when its refusal
// applies.
func CheckResource(doc *policydoc.Document, r ResourceRequest) policydoc.Decision {
	if doc == nil {
		return policydoc.ImplicitDeny{}
	}
	return policydoc.Allows(*doc, policydoc.Query{
		Principal: r.Caller,
		Action:    r.Action.String(),
		Resource:  r.Resource,
		Context:   r.Keys,
	})
}
