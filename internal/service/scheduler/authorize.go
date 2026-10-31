package scheduler

import (
	"context"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The caller's authorization beyond each operation's own action, which
// step 8 asks about on the schedule or group ARN (WithResources), or on
// "*" for the two lists (scheduler.md 9, 14.11; spec "Enforce matrix").

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

// passedToScheduler is the condition key the caller's iam:PassRole carries.
// Inferred by analogy with the services that pass roles (spec conflict 8;
// iam-enforcement-surfaces.md 10.2): AWS's table lists no condition key
// for CreateSchedule. UNVERIFIED (SCH-10 step 02), scheduler.md 9.
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

// taggedResource is a tag operation's ResourceArn, which the model's
// pattern holds to a schedule group's ARN; nothing when it does not parse.
func taggedResource(raw string) []aws.ARN {
	a, err := aws.ParseARN(raw)
	if err != nil {
		return nil
	}
	return []aws.ARN{a}
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

// authorizeMembers asks for the caller's scheduler:DeleteSchedule on each
// schedule of the group, which DeleteScheduleGroup deletes with it
// (actions.json; scheduler.md 9). The resource, each member's ARN, is
// inferred: UNVERIFIED (SCH-10 step 03). A denial deletes nothing. The
// members are those the group holds when the call reads them, before its
// write; a schedule created in the group between that read and the write
// is deleted without being asked about.
func (s *Service) authorizeMembers(ctx context.Context, c *kernel.Call, group string) *awserr.Error {
	scope := c.Scope()
	var members []string
	if cell, ok := s.cells.Lookup(scope); ok {
		members = store.Read(cell, func(st *State) []string { return slices.Sorted(maps.Keys(st.Schedules[group])) })
	}
	for _, name := range members {
		if err := kernel.Authorize(ctx, c, schedulerAction("DeleteSchedule"), scheduleRef{scope: scope, group: group, name: name}.arn(), nil); err != nil {
			return err
		}
	}
	return nil
}
