package kernel

import (
	"context"
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Target says where and as whom an internal call runs.
type Target struct {
	// Scope is the target resource's account and region.
	Scope aws.Scope
	// As is whom AWS would authorize the call as (F12).
	As  PrincipalMode
	Via ref.Via
	// At is the admission instant of deferred work: a timer's due instant.
	// Zero means the parent call's admission instant.
	At clock.Instant
	// Resume is a stored cause the call continues, such as an outbox
	// entry's source call (ARCHITECTURE.md 10: state carries causes across
	// time). The call's journal parent and trace come from it instead of
	// from the parent call, whose TimerFired event only scheduled the work.
	// Zero means the parent call's event and trace.
	Resume ref.Cause
}

// PrincipalMode says whom AWS authorizes an internal call as.
//
//sumtype:decl
type PrincipalMode interface{ isPrincipalMode() }

// AsCaller is a forward-access call charged to the original principal: S3,
// Secrets Manager and SQS calling KMS.
type AsCaller struct{}

// AsExecutionRole is a call made as a function's execution role: event
// source mapping polls and Lambda log writes.
type AsExecutionRole struct{ Role aws.ARN }

// AsService is a delivery a service principal makes, gated by the target's
// resource policy. Source is the resource the delivery comes from (the
// topic, bucket or rule), which the access recorder pairs with the target's
// policy.
type AsService struct {
	Name   string // "sns.amazonaws.com"
	Source aws.ARN
}

func (AsCaller) isPrincipalMode()        {}
func (AsExecutionRole) isPrincipalMode() {}
func (AsService) isPrincipalMode()       {}

// HoldingError is an Invoke or a nested Write made while a call holds a cell
// lock (ARCHITECTURE.md 8 rule 1).
type HoldingError struct {
	// Attempted is what the call tried: "kernel.Invoke of sqs.SendMessage"
	// or "kernel.Write".
	Attempted string
	// Holder is the call holding the lock.
	Holder string
}

func (e *HoldingError) Error() string {
	return fmt.Sprintf("%s from %s while %s holds a cell lock; a handler holds at most one cell lock and calls other services before kernel.Write or as an effect after it",
		e.Attempted, e.Holder, e.Holder)
}

// TargetError is an internal call the kernel cannot build.
type TargetError struct {
	Op     ref.OperationID
	Reason string
}

func (e *TargetError) Error() string {
	return fmt.Sprintf("kernel.Invoke of %s: %s", e.Op, e.Reason)
}

// Invoke runs an internal call through the full pipeline minus the wire
// codec and auth: journal, validation, faults, the handler, access record.
// The call is the parent's child in the journal, runs in t.Scope as the
// principal t.As resolves to, and is admitted at t.At, or at the parent's
// admission instant when t.At is zero. It returns Internal, never panics,
// when parent holds a cell lock.
func Invoke[In, Out any](ctx context.Context, k *Kernel, parent *Call, op Op[In, Out], t Target, in *In) (*Out, *awserr.Error) {
	var id ref.OperationID
	if op.desc != nil {
		id = op.desc.ID
	}
	switch {
	case parent == nil:
		return nil, awserr.Internalf(&TargetError{Op: id, Reason: "no parent call; internal calls run on behalf of a call or a timer"})
	case parent.holding.Load():
		return nil, awserr.Internalf(&HoldingError{Attempted: "kernel.Invoke of " + id.String(), Holder: parent.label})
	case op.desc == nil:
		return nil, awserr.Internalf(&TargetError{Reason: "the Op has no descriptor"})
	case t.As == nil:
		return nil, awserr.Internalf(&TargetError{Op: id, Reason: "Target.As is nil; say whom AWS authorizes the call as"})
	case t.Scope.Account.IsZero():
		return nil, awserr.Internalf(&TargetError{Op: id, Reason: "Target.Scope has no account; take it from the target resource"})
	}
	c := k.internalCall(parent, op.desc.ID, t)
	switch r := k.Dispatch(ctx, c, Ok{In: in, Wire: Synthesized{}}).(type) {
	case Output:
		out, ok := r.Out.(*Out)
		if !ok {
			return nil, awserr.Internalf(&TargetError{Op: id, Reason: fmt.Sprintf("the handler's output is %T, want %T", r.Out, (*Out)(nil))})
		}
		return out, nil
	case Failure:
		return nil, r.Err
	case StatusOKFailure:
		return nil, r.Err
	}
	return nil, awserr.Internalf(&TargetError{Op: id, Reason: "Dispatch returned no result"})
}

// internalCall builds the call Invoke dispatches.
func (k *Kernel) internalCall(parent *Call, id ref.OperationID, t Target) *Call {
	op, _ := k.reg.Operation(id)
	at := t.At
	if at.IsZero() {
		at = parent.admitted
	}
	cause := t.Resume
	if cause == (ref.Cause{}) {
		cause = ref.Cause{Parent: parent.event, Trace: parent.cause.Trace}
	}
	return &Call{
		k:         k,
		id:        k.ids.RequestID(),
		scope:     t.Scope,
		principal: resolvePrincipal(parent, t.As),
		origin:    ref.Internal{Via: t.Via},
		mode:      t.As,
		op:        op,
		opID:      id,
		label:     id.String(),
		cause:     cause,
		admitted:  at,
		endpoint:  parent.endpoint,
		trace:     parent.trace,
		elapsed:   k.clock.Stopwatch(),
	}
}

// resolvePrincipal returns whom an internal call runs as: the parent's
// principal for a forward-access call, a session of the execution role, or
// the service principal.
func resolvePrincipal(parent *Call, m PrincipalMode) identity.Principal {
	switch m := m.(type) {
	case AsCaller:
		return parent.principal
	case AsExecutionRole:
		return executionRole(m.Role)
	case AsService:
		return identity.ServicePrincipal{Name: m.Name}
	}
	return identity.Anonymous{}
}

// executionRole is the session a function's execution role runs internal
// calls as: the role from its ARN (arn:aws:iam::<account>:role<path><name>)
// under the session name "understudy", since the access recorder keys on
// the role and no session of a real environment is involved. An ARN that
// names no role is anonymous.
func executionRole(role aws.ARN) identity.Principal {
	account, err := aws.ParseAccountID(role.Account)
	rest, ok := strings.CutPrefix(role.Resource, "role/")
	if err != nil || !ok || rest == "" {
		return identity.Anonymous{}
	}
	path, name := "/", rest
	if i := strings.LastIndexByte(rest, '/'); i >= 0 {
		path, name = "/"+rest[:i+1], rest[i+1:]
	}
	return identity.AssumedRole{
		Account: account,
		Role:    identity.RoleRef{Name: name, Path: path},
		Session: "understudy",
		Source:  identity.FromAssumeRole{},
	}
}

// modeName is the journal's rendering of how an internal call is
// authorized; empty for external and timer calls.
func modeName(m PrincipalMode) string {
	switch m := m.(type) {
	case AsCaller:
		return "caller"
	case AsExecutionRole:
		return "execution-role"
	case AsService:
		return "service:" + m.Name
	}
	return ""
}
