package enforce

import (
	"sync/atomic"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// Mode is the iam.enforce mode an Authorizer runs.
type Mode uint8

// Mode values.
const (
	// Off builds no kernel authorizer; Evaluate still answers.
	Off Mode = iota
	// Advise evaluates every call and journals an Advisory for each denial
	// AWS would issue, answering as Off does.
	Advise
	// Enforce answers those denials.
	Enforce
)

// Options configures New.
type Options struct{ Mode Mode }

// Sources are what an Authorizer reads policies and time from, bound once
// the services exist, since the kernel is built before them.
type Sources struct {
	Identities authz.IdentitySource
	Resources  authz.StoredPolicyReader
	Clock      *clock.Clock
}

// Authorizer is the kernel.Authorizer of iam.enforce advise and enforce.
type Authorizer struct {
	mode Mode
	// sources is nil until Bind.
	sources atomic.Pointer[Sources]
}

// The kernel asks it at pipeline step 8 and through kernel.Authorize.
var _ kernel.Authorizer = (*Authorizer)(nil)

// New returns an unbound Authorizer running o.Mode.
func New(o Options) *Authorizer {
	return &Authorizer{mode: o.Mode}
}

// Bind supplies the sources. Binding twice is a *BindError.
func (a *Authorizer) Bind(s Sources) error {
	if !a.sources.CompareAndSwap(nil, &s) {
		return &BindError{}
	}
	return nil
}

// Mode is the mode New was given.
func (a *Authorizer) Mode() Mode { return a.mode }

// EvaluateRequest is one POST /iam/evaluate.
type EvaluateRequest struct {
	// Principal is a root, user or role ARN; a role is evaluated as a
	// session named "understudy" with no session policy.
	Principal aws.ARN
	Identity  IdentityOverride
	Pairs     []kernel.Pair
}

// IdentityOverride says which identity-based policies Evaluate reads.
//
//sumtype:decl
type IdentityOverride interface{ isIdentityOverride() }

// StoredIdentity reads the principal's stored policies.
type StoredIdentity struct{}

// ReplacedIdentity replaces the principal's identity-based policies with
// Policies; the permissions boundary and the resource policies stay as
// stored.
type ReplacedIdentity struct{ Policies []authz.Policy }

func (StoredIdentity) isIdentityOverride()   {}
func (ReplacedIdentity) isIdentityOverride() {}

// Evaluation is one pair's answer.
type Evaluation struct {
	Pair    kernel.Pair
	Outcome authz.Outcome
	// Message is the denial message in the grammar of the action's service;
	// "" when allowed.
	Message string
}

// BindError is a second Bind of one Authorizer.
type BindError struct{}

func (*BindError) Error() string {
	return "enforce: Bind called twice; wire.go binds the authorizer once"
}

// UnboundError is an Evaluate before Bind: the policy sources do not exist
// yet, so nothing can be decided.
type UnboundError struct{}

func (*UnboundError) Error() string {
	return "enforce: Evaluate called before Bind; wire.go binds the authorizer once the services exist"
}

// UnknownPrincipalError is an Evaluate principal that does not exist.
type UnknownPrincipalError struct{ ARN string }

func (e *UnknownPrincipalError) Error() string {
	return "enforce: " + e.ARN + " does not exist"
}

// UnevaluableError is a pair Evaluate or Authorize cannot decide.
type UnevaluableError struct {
	Pair  kernel.Pair
	Cause error
}

func (e *UnevaluableError) Error() string {
	return "enforce: cannot evaluate " + e.Pair.Action.String() + " on " + resourceText(e.Pair) + ": " + e.Cause.Error()
}

// Unwrap returns Cause.
func (e *UnevaluableError) Unwrap() error { return e.Cause }

// UnreadablePolicyError is a resource's stored policy that does not parse.
// SQS and S3 store any JSON object as a policy, so it is the caller's text,
// and it is never read as no policy (iam-enforcement-surfaces.md 12.10).
type UnreadablePolicyError struct {
	Resource aws.ARN
	Err      error
}

func (e *UnreadablePolicyError) Error() string {
	return "the stored policy of " + e.Resource.String() + " does not parse (" + e.Err.Error() + "), so understudy cannot tell what AWS would decide; " +
		"replace it with a valid policy document"
}

// Unwrap returns Err.
func (e *UnreadablePolicyError) Unwrap() error { return e.Err }
