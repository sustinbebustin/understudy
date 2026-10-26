package enforce

import (
	"context"
	"errors"

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
}

// The kernel asks it at pipeline step 8 and through kernel.Authorize.
var _ kernel.Authorizer = (*Authorizer)(nil)

// New returns an unbound Authorizer running o.Mode.
func New(o Options) *Authorizer {
	return &Authorizer{mode: o.Mode}
}

// Bind supplies the sources. Binding twice is a *BindError.
//
// Not implemented yet: it returns errNotImplemented.
func (a *Authorizer) Bind(_ Sources) error {
	return errNotImplemented
}

// Mode is the mode New was given.
func (a *Authorizer) Mode() Mode { return a.mode }

// Authorize implements kernel.Authorizer: each pair of q is decided by
// authz.Decide, unsupplied keys are journaled, and the first denying pair
// ends the call under Enforce or is journaled under Advise. Unbound, it
// answers a nil Verdict, which the kernel reports as VerdictError.
//
// Not implemented yet: it answers a nil Verdict.
func (a *Authorizer) Authorize(_ context.Context, _ *kernel.Call, _ kernel.AuthzQuery) kernel.Verdict {
	return nil
}

// Evaluate decides r's pairs as Authorize would, without a call: nothing is
// journaled, the context has no request keys, and the clock is
// Sources.Clock. It returns an *UnknownPrincipalError for a principal that
// does not exist and an *UnevaluableError for a pair it cannot decide.
//
// Not implemented yet: it returns errNotImplemented.
func (a *Authorizer) Evaluate(_ context.Context, _ EvaluateRequest) ([]Evaluation, error) {
	return nil, errNotImplemented
}

// errNotImplemented is what the stubs of this package return until
// enforcement is built.
var errNotImplemented = errors.New("enforce: not implemented yet")

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
	return "enforce: cannot evaluate " + e.Pair.Action.String() + ": " + e.Cause.Error()
}

// Unwrap returns Cause.
func (e *UnevaluableError) Unwrap() error { return e.Cause }
