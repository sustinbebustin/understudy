package scheduler

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The caller's authorization beyond each operation's own action, which
// step 8 asks about on the schedule or group ARN (WithResources), or on
// "*" for the two lists (scheduler.md 9, 14.11).

func schedulerAction(name string) registry.IAMAction {
	return registry.IAMAction{Prefix: "scheduler", Name: name}
}

func passRoleAction() registry.IAMAction { return registry.IAMAction{Prefix: "iam", Name: "PassRole"} }

func assumeRoleAction() registry.IAMAction {
	return registry.IAMAction{Prefix: "sts", Name: "AssumeRole"}
}

// keyActions are what the caller needs on KmsKeyArn at create and update
// (aws-doc encryption-rest; scheduler.md 4.1, 9).
func keyActions() []registry.IAMAction {
	return []registry.IAMAction{
		{Prefix: "kms", Name: "DescribeKey"},
		{Prefix: "kms", Name: "GenerateDataKey"},
		{Prefix: "kms", Name: "Decrypt"},
	}
}

// passedToScheduler is the condition key the caller's iam:PassRole carries:
// AWS accepted a CreateSchedule whose PassRole is allowed only with
// iam:PassedToService scheduler.amazonaws.com (SCH-10
// 10-02-passrole-conditioned, recorded 2026-11-03; scheduler.md 9).
func passedToScheduler() kernel.ConditionKeys {
	return kernel.ConditionKeys{"iam:PassedToService": {principal}}
}

// scheduleResource is the schedule ARN a schedule operation acts on
// (scheduler.md 2.3).
func scheduleResource(c *kernel.Call, group *string, name string) []aws.ARN {
	return []aws.ARN{scheduleRef{scope: c.Scope(), group: groupName(group), name: name}.arn()}
}

// groupResource is the group ARN a group operation acts on.
func groupResource(c *kernel.Call, name string) []aws.ARN {
	return []aws.ARN{groupARN(c.Scope(), name)}
}

// authorizeWrite asks what CreateSchedule and UpdateSchedule need of the
// schedule p, after its members are validated and its group is found and
// before the write, in 14.9.3's order (UNVERIFIED, SCH-04): that
// scheduler.amazonaws.com may assume the execution role, the caller's
// iam:PassRole on it, then the caller's three KMS actions on KmsKeyArn.
//
// The role's trust is evaluated as scheduler.amazonaws.com assuming it
// with aws:SourceArn the schedule group and aws:SourceAccount its account
// (aws-doc cross-service-confused-deputy-prevention; scheduler.md 9). Under
// iam.enforce=enforce a missing or untrusting role answers the captured
// ValidationException; under advise the Authorizer journals the would-be
// denial and the write goes on; under off nothing is asked.
func authorizeWrite(ctx context.Context, c *kernel.Call, r scheduleRef, p *plan) *awserr.Error {
	as := kernel.AsService{Name: principal, Source: groupARN(r.scope, r.group), SourceAccount: r.scope.Account}
	trust := kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: assumeRoleAction(), Resource: kernel.ResourceARN{ARN: p.role}}}}
	if err := kernel.AuthorizeAs(ctx, c, as, trust); err != nil {
		if denied(err) {
			return errRoleUntrusted()
		}
		return err
	}
	if err := kernel.Authorize(ctx, c, passRoleAction(), p.role, passedToScheduler()); err != nil {
		return err
	}
	if p.key == nil {
		return nil
	}
	for _, a := range keyActions() {
		if err := kernel.Authorize(ctx, c, a, *p.key, nil); err != nil {
			return err
		}
	}
	return nil
}

// denied reports whether err is the Authorizer's denial.
func denied(err *awserr.Error) bool {
	_, ok := err.Origin.(awserr.Denial)
	return ok
}

// groupHeld reports whether the scope holds the group name, read before a
// write that checks it again.
func (s *Service) groupHeld(scope aws.Scope, name string) bool {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return name == defaultGroup
	}
	return store.Read(cell, func(st *State) bool { return st.hasGroup(name) })
}

// authorizeMembers asks once for the caller's scheduler:DeleteSchedule on
// arn:<partition>:scheduler:<region>:<account>:schedule/<group>/*, which
// DeleteScheduleGroup needs to delete the group's schedules with it: AWS's
// denial names that resource (SCH-10 10-03-group-delete-denied, recorded
// 2026-11-03; actions.json; scheduler.md 9). A denial deletes nothing. It
// is asked only when the group holds a schedule when the call reads it,
// before its write; whether AWS asks for an empty group is unrecorded
// (SCH-10 recorded one member).
func (s *Service) authorizeMembers(ctx context.Context, c *kernel.Call, group string) *awserr.Error {
	scope := c.Scope()
	held := false
	if cell, ok := s.cells.Lookup(scope); ok {
		held = store.Read(cell, func(st *State) bool { return len(st.Schedules[group]) > 0 })
	}
	if !held {
		return nil
	}
	return kernel.Authorize(ctx, c, schedulerAction("DeleteSchedule"), scheduleRef{scope: scope, group: group, name: "*"}.arn(), nil)
}
