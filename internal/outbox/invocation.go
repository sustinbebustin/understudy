package outbox

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// schedulerPrincipal is the service principal that assumes a schedule's
// execution role (scheduler.md 9).
const schedulerPrincipal = "scheduler.amazonaws.com"

// roleUnassumableCode is the ERROR_CODE of a Scheduler dead letter whose
// execution role could not be assumed at invocation: missing, or no longer
// trusting scheduler.amazonaws.com. STS answers such an AssumeRole with
// AccessDenied (iam-enforcement-surfaces.md 4.3). UNVERIFIED (SCH-08 step
// x5), scheduler.md 15: what Scheduler puts in the dead letter is
// unrecorded.
const roleUnassumableCode = "AccessDenied"

// eventsPrincipal is the service principal that assumes an EventBridge
// target's RoleArn (iam-enforcement-surfaces.md 8.4).
const eventsPrincipal = "events.amazonaws.com"

// targetRoleUnassumableCode is the ERROR_CODE of an EventBridge dead letter
// whose target's RoleArn could not be assumed (eb-rule-dlq.html;
// iam-enforcement-surfaces.md 8.4).
const targetRoleUnassumableCode = "FAILED_TO_ASSUME_ROLE"

// keyDeniedCode is the ERROR_CODE of a Scheduler dead letter whose role
// session may not decrypt with the schedule's key: KMS's denial,
// AccessDeniedException (iam-enforcement-surfaces.md 12.3). UNVERIFIED
// (SCH-04 04-kms-missing), scheduler.md 14.11: unrecorded.
const keyDeniedCode = "AccessDeniedException"

// authorizeInvocation asks, before a Scheduler delivery's target call,
// what Scheduler itself checks first (scheduler.md 9, 14.11; spec "Enforce
// matrix"): that scheduler.amazonaws.com may assume the execution role,
// with aws:SourceArn the schedule group and aws:SourceAccount its account,
// and, for a schedule with a KmsKeyArn, that the role session may
// kms:Decrypt with the key. Under iam.enforce=advise the Authorizer
// journals a would-deny and the attempt goes on; under enforce a denial
// is a permanent failure, dead-lettered with a named ERROR_CODE, and the
// target is never called; under off nothing is asked. done is false when
// the attempt may call its target.
func (a *attempt) authorizeInvocation(ctx context.Context) (outcome journal.DeliveryOutcome, ok, done bool) {
	req := a.entry.Request
	inv := req.Invocation
	role, isRole := req.As.(delivery.ExecutionRole)
	op := operationOf(req.Target)
	if req.Mechanism == delivery.EventsToEvents && isRole && op != nil {
		return a.authorizeTargetRole(ctx, op, role)
	}
	if inv == nil || !isRole || op == nil {
		return nil, true, false
	}
	session := a.as(a.scope)
	trust := session
	trust.As = kernel.AsService{Name: schedulerPrincipal, Source: inv.GroupARN(), SourceAccount: accountOf(inv.ScheduleARN)}
	assume := kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: registry.IAMAction{Prefix: "sts", Name: "AssumeRole"}, Resource: kernel.ResourceARN{ARN: role.Role}}}}
	if err := kernel.AuthorizeBefore(ctx, a.e.k, a.c, op, trust, assume); err != nil {
		outcome, ok := a.refusedBefore(ctx, err, roleUnassumableCode)
		return outcome, ok, true
	}
	if inv.Key == nil {
		return nil, true, false
	}
	decrypt := kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: registry.IAMAction{Prefix: "kms", Name: "Decrypt"}, Resource: kernel.ResourceARN{ARN: *inv.Key}}}}
	if err := kernel.AuthorizeBefore(ctx, a.e.k, a.c, op, session, decrypt); err != nil {
		outcome, ok := a.refusedBefore(ctx, err, keyDeniedCode)
		return outcome, ok, true
	}
	return nil, true, false
}

// authorizeTargetRole asks, before an event bus target's PutEvents, that
// events.amazonaws.com may assume the target's RoleArn: the role's trust
// must allow events.amazonaws.com (iam-enforcement-surfaces.md 8.4). The
// question is asked in the rule's account and region, where the role is
// passed. Under enforce a denial is a permanent failure, dead-lettered
// with ERROR_CODE FAILED_TO_ASSUME_ROLE, a documented sample value
// (eb-rule-dlq.html; 8.4), and the bus is never called. UNVERIFIED: that
// the trust request carries aws:SourceArn the rule and aws:SourceAccount
// its account is inferred from EventBridge's resource-policy deliveries
// (8.1), as Scheduler's is for its group.
func (a *attempt) authorizeTargetRole(ctx context.Context, op *registry.Operation, role delivery.ExecutionRole) (outcome journal.DeliveryOutcome, ok, done bool) {
	rule := a.entry.Request.Source
	trust := a.as(a.scope)
	trust.As = kernel.AsService{Name: eventsPrincipal, Source: rule, SourceAccount: accountOf(rule)}
	if scope, err := scopeOf(rule); err == nil {
		trust.Scope = scope
	}
	assume := kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: registry.IAMAction{Prefix: "sts", Name: "AssumeRole"}, Resource: kernel.ResourceARN{ARN: role.Role}}}}
	if err := kernel.AuthorizeBefore(ctx, a.e.k, a.c, op, trust, assume); err != nil {
		outcome, ok := a.refusedBefore(ctx, err, targetRoleUnassumableCode)
		return outcome, ok, true
	}
	return nil, true, false
}

// refusedBefore ends an attempt whose pre-target check answered err: a
// denial is a permanent failure with code as its ERROR_CODE; anything
// else, such as an Authorizer that could not decide, ends the attempt as
// the target's error would.
func (a *attempt) refusedBefore(ctx context.Context, err *awserr.Error, code string) (journal.DeliveryOutcome, bool) {
	if !denied(err) {
		return a.end(ctx, err)
	}
	if ctx.Err() != nil {
		return nil, false
	}
	return a.permanent(ctx, senderRefusal{code: code, message: errorMessage(err)})
}

// accountOf is the account a's ARN names, zero when it names none.
func accountOf(a aws.ARN) aws.AccountID {
	id, err := aws.ParseAccountID(a.Account)
	if err != nil {
		return aws.AccountID{}
	}
	return id
}
