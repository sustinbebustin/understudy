package authz

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Request is one (action, resource) pair a principal asks for.
type Request struct {
	Principal identity.Principal
	Action    registry.IAMAction
	Resource  Resource
	Context   *policydoc.Context
}

// Resource is what a pair is on.
//
//sumtype:decl
type Resource interface{ isResource() }

// OnResource is one resource and the account that owns it.
type OnResource struct {
	ARN   aws.ARN
	Owner aws.AccountID
}

// AnyResource is a pair on no resource ("*"), asked in Account.
type AnyResource struct{ Account aws.AccountID }

func (OnResource) isResource()  {}
func (AnyResource) isResource() {}

// Policy is one policy document and what names it in a denial.
type Policy struct {
	// ARN is a managed policy's ARN; "" for an inline policy, whose denials
	// carry no ARN (iam-enforcement-surfaces.md 12.3).
	ARN string
	// Name is the inline policy's name or the managed policy's name.
	Name     string
	Document policydoc.Document
}

// Boundary is the permissions boundary of a user or of a session's role.
//
//sumtype:decl
type Boundary interface{ isBoundary() }

// NoBoundary is an entity without a permissions boundary.
type NoBoundary struct{}

// BoundedBy is an entity whose permissions boundary is Policy.
type BoundedBy struct{ Policy Policy }

func (NoBoundary) isBoundary() {}
func (BoundedBy) isBoundary()  {}

// ResourcePolicy is the resource-based policy of a pair's resource and how
// AWS reads it (iam-evaluation.md 2.2 step 3).
//
//sumtype:decl
type ResourcePolicy interface{ isResourcePolicy() }

// NoResourcePolicy is a resource without a resource-based policy, or a pair
// on no resource.
type NoResourcePolicy struct{}

// Generic is a queue, topic, bucket, function or secret policy: within one
// account, an Allow in it or in the identity policy is enough.
type Generic struct{ Document policydoc.Document }

// Trust is a role's trust policy, which must allow explicitly
// (iam-evaluation.md 3.1).
type Trust struct{ Document policydoc.Document }

// KeyPolicy is a KMS key policy, which must allow the caller or the
// caller's account (iam-evaluation.md 4).
type KeyPolicy struct{ Document policydoc.Document }

func (NoResourcePolicy) isResourcePolicy() {}
func (Generic) isResourcePolicy()          {}
func (Trust) isResourcePolicy()            {}
func (KeyPolicy) isResourcePolicy()        {}

// Policies are the policies that apply to a pair.
type Policies struct {
	// Identity are the identity-based policies of the user or of the
	// session's role: inline, then attached managed, in stored order.
	Identity []Policy
	Boundary Boundary
	// Session are a role session's session policies, inline then the managed
	// PolicyArns; empty when none were passed, which allows
	// (iam-evaluation.md 2.2 step 6).
	Session  []Policy
	Resource ResourcePolicy
}

// PolicyType is the policy type a denial names (iam-evaluation.md 2.11).
type PolicyType uint8

// PolicyType values.
const (
	// IdentityBased is "identity-based policy".
	IdentityBased PolicyType = iota + 1
	// ResourceBased is "resource-based policy"; a KMS key policy denial
	// names it too (inferred, IAM-EVAL-28).
	ResourceBased
	// PermissionsBoundary is "permissions boundary".
	PermissionsBoundary
	// SessionPolicy is "session policy".
	SessionPolicy
	// RoleTrust is "role trust policy".
	RoleTrust
)

// String returns the type as the denial grammar writes it.
func (t PolicyType) String() string {
	switch t {
	case IdentityBased:
		return "identity-based policy"
	case ResourceBased:
		return "resource-based policy"
	case PermissionsBoundary:
		return "permissions boundary"
	case SessionPolicy:
		return "session policy"
	case RoleTrust:
		return "role trust policy"
	default:
		return fmt.Sprintf("PolicyType(%d)", uint8(t))
	}
}

// Decision is the answer for one pair.
//
//sumtype:decl
type Decision interface{ isDecision() }

// Allow is a pair AWS allows.
type Allow struct{}

// ImplicitDeny is a pair no policy allows; PolicyType is the step of
// iam-evaluation.md 2.2 that failed.
type ImplicitDeny struct{ PolicyType PolicyType }

// ExplicitDeny is a pair a Deny statement matches.
type ExplicitDeny struct {
	PolicyType PolicyType
	// PolicyARN is the denying managed policy's ARN; "" for an inline policy
	// or a resource-based policy.
	PolicyARN string
}

func (Allow) isDecision()        {}
func (ImplicitDeny) isDecision() {}
func (ExplicitDeny) isDecision() {}

// Outcome is Decide's answer.
type Outcome struct {
	Decision   Decision
	Unsupplied []Unsupplied
}

// Unsupplied is a condition key a policy tested that understudy could not
// supply (iam-enforcement-surfaces.md 12.2).
type Unsupplied struct {
	Key        string
	PolicyType PolicyType
	// Policy is the managed policy's ARN or the inline policy's name; "" for
	// a resource-based policy.
	Policy    string
	Statement policydoc.StatementRef
}

// Decide is the multi-policy decision for one pair (iam-evaluation.md 2.2,
// 2.4, 2.6, 3 and 4.2): explicit deny first, then the root, service-principal
// and anonymous rules, then the resource policy by the principal form it
// names, the identity policies, the permissions boundary and the session
// policies, with both sides required across accounts.
//
// Not implemented yet: it returns the zero Outcome, whose Decision is nil.
func Decide(_ Request, _ Policies) Outcome {
	return Outcome{}
}
