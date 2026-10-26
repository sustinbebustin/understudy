package authz

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// IdentitySource reads IAM principals' identity-based policies.
type IdentitySource interface {
	// Identity returns p's identity-based policies, permissions boundary,
	// session policies and principal tags. A root, a service principal and
	// an anonymous caller have an empty set. It returns a
	// *NoSuchPrincipalError when p's user or role does not exist, and an
	// *UnreadablePolicyError when a stored document does not parse or an
	// attached ARN does not resolve.
	Identity(p identity.Principal) (IdentitySet, error)
	// PrincipalOf returns the principal an IAM ARN names: a root, a user
	// with its ID, or a role as a session named "understudy" with no
	// attributes. It is false when the user or role does not exist.
	PrincipalOf(arn aws.ARN) (identity.Principal, bool)
}

// IdentitySet is a principal's identity-based side of a decision.
type IdentitySet struct {
	Policies []Policy
	Boundary Boundary
	Session  []Policy
	// Tags are the principal tags: the user's, or the role's overridden by
	// the session's tags, keys compared case-insensitively (S-TAGS).
	Tags []Tag
}

// Tag is one principal tag.
type Tag struct{ Key, Value string }

// NoSuchPrincipalError is a principal whose user or role does not exist.
type NoSuchPrincipalError struct{ Principal string }

func (e *NoSuchPrincipalError) Error() string {
	return "authz: " + e.Principal + " does not exist, so it has no identity-based policy"
}

// UnreadablePolicyError is a stored identity-based policy that cannot be
// read.
type UnreadablePolicyError struct {
	Principal string
	// Policy is the policy's ARN or inline name.
	Policy string
	Err    error
}

func (e *UnreadablePolicyError) Error() string {
	return "authz: the policy " + e.Policy + " of " + e.Principal + " cannot be read: " + e.Err.Error() +
		"; IAM validated it when it was written, so this is a bug in its storage"
}

// Unwrap returns Err.
func (e *UnreadablePolicyError) Unwrap() error { return e.Err }

// StoredPolicyReader reads a resource's stored policy as the evaluator needs
// it: who owns the resource, and whether its policy text reads.
type StoredPolicyReader interface {
	// StoredPolicy returns target's owner and policy. It is false when
	// target does not exist or its kind has no resource policy, as
	// PolicyProvider.ResourcePolicy answers.
	StoredPolicy(target aws.ARN) (StoredPolicy, bool)
}

// StoredPolicy is a resource's owner and stored policy.
type StoredPolicy struct {
	Owner  aws.AccountID
	Policy Stored
}

// Stored is the state of a resource's stored policy text.
//
//sumtype:decl
type Stored interface{ isStored() }

// NoPolicy is a resource without a stored policy.
type NoPolicy struct{}

// Parsed is a stored policy that reads as Document.
type Parsed struct{ Document policydoc.Document }

// Unparsed is a stored text policydoc cannot parse.
type Unparsed struct {
	Text string
	Err  error
}

func (NoPolicy) isStored() {}
func (Parsed) isStored()   {}
func (Unparsed) isStored() {}
