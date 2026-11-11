package iam

import (
	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// byName is the coverage note of the operations that name a user, role or
// instance profile by name. AWS denies GetRole of a missing role before
// saying it is missing, naming it "role <name>" (iam-enforcement-surfaces.md
// 3.2, 5.3; IAM-ENF-10, IAM-ENF-14); the other operations follow it by
// inference, and which ARN AWS evaluates for a missing entity is
// unrecorded, so understudy asks about the one at missingPath.
const byName = "under iam.enforce, a missing user, role or instance profile is denied before it answers NoSuchEntity, evaluated on its ARN at the path / and named \"<type> <name>\"; " +
	"recorded for GetRole, inferred for the other operations and for the evaluated ARN (iam.enforce.missing-resource.by-name)"

// byNamePartial is the coverage of an operation on an entity named by
// name that is otherwise fully implemented.
func byNamePartial() registry.Partial { return registry.Partial{Note: byName} }

// withByName is note followed by byName.
func withByName(note string) string { return note + "; " + byName }

// missingPath is the path of the ARN a call naming a missing user, role or
// instance profile is asked about: the request carries no path, and "/" is
// the one CreateRole, CreateUser and CreateInstanceProfile default to, so a
// caller allowed on role/*, user/* or "*" goes on to NoSuchEntity
// (inferred; iam-enforcement-surfaces.md 5.3).
const missingPath = "/"

// declareAuthorization states what each IAM operation is authorized on
// (iam-evaluation.md 2.1): an operation that names a user, role or instance
// profile by name is asked about at step 8 on its ARN, the path read from
// IAM's state, or missingPath for one IAM does not hold, so a missing
// entity is denied first (iam.enforce.missing-resource.by-name); one that
// carries a managed policy's ARN, or creates an entity whose path and name
// it carries, is asked about that ARN at step 8. GetRole authorizes in its
// handler (kernel.AuthorizedInHandler). It runs after the bind sites, whose
// bindings it extends.
func (s *Service) declareAuthorization(b *kernel.Binder) {
	s.declareUsers(b)
	s.declareRoles(b)
	// Adding or removing a role is authorized on the profile; iam:PassRole
	// on the role is another question.
	kernel.Declare(b, iamapi.OpGetInstanceProfile, profileNamed(s, func(in *iamapi.GetInstanceProfileInput) string { return in.InstanceProfileName })...)
	kernel.Declare(b, iamapi.OpDeleteInstanceProfile, profileNamed(s, func(in *iamapi.DeleteInstanceProfileInput) string { return in.InstanceProfileName })...)
	kernel.Declare(b, iamapi.OpAddRoleToInstanceProfile, profileNamed(s, func(in *iamapi.AddRoleToInstanceProfileInput) string { return in.InstanceProfileName })...)
	// Adding a role passes it: the caller needs iam:PassRole on the role,
	// with iam:PassedToService ec2.amazonaws.com, the SAR's fixed value,
	// and iam:AddRoleToInstanceProfile on the profile alone, never crossed
	// (iam-enforcement-surfaces.md 10.2, 12.6; the AddRoleToInstanceProfile
	// API reference).
	kernel.Declare(b, iamapi.OpAddRoleToInstanceProfile,
		kernel.WithActions(func(*kernel.Call, *iamapi.AddRoleToInstanceProfileInput) []registry.IAMAction {
			return []registry.IAMAction{iamAction("PassRole")}
		}),
		kernel.WithResourcesFor([]registry.IAMAction{iamAction("AddRoleToInstanceProfile")}, func(c *kernel.Call, in *iamapi.AddRoleToInstanceProfileInput) []aws.ARN {
			return found(s.profileARN(c, in.InstanceProfileName))
		}),
		kernel.WithResourcesFor([]registry.IAMAction{iamAction("PassRole")}, func(c *kernel.Call, in *iamapi.AddRoleToInstanceProfileInput) []aws.ARN {
			return found(s.roleARN(c, in.RoleName))
		}),
		kernel.WithConditionKeys(func(*kernel.Call, *iamapi.AddRoleToInstanceProfileInput) kernel.ConditionKeys {
			return kernel.ConditionKeys{"iam:PassedToService": {"ec2.amazonaws.com"}}
		}))
	kernel.Declare(b, iamapi.OpRemoveRoleFromInstanceProfile, profileNamed(s, func(in *iamapi.RemoveRoleFromInstanceProfileInput) string { return in.InstanceProfileName })...)
	kernel.Declare(b, iamapi.OpTagInstanceProfile, profileNamed(s, func(in *iamapi.TagInstanceProfileInput) string { return in.InstanceProfileName })...)
	kernel.Declare(b, iamapi.OpUntagInstanceProfile, profileNamed(s, func(in *iamapi.UntagInstanceProfileInput) string { return in.InstanceProfileName })...)
	kernel.Declare(b, iamapi.OpListInstanceProfileTags, profileNamed(s, func(in *iamapi.ListInstanceProfileTagsInput) string { return in.InstanceProfileName })...)

	kernel.Declare(b, iamapi.OpGetPolicy, policyNamed(func(in *iamapi.GetPolicyInput) string { return in.PolicyArn }))
	kernel.Declare(b, iamapi.OpDeletePolicy, policyNamed(func(in *iamapi.DeletePolicyInput) string { return in.PolicyArn }))
	kernel.Declare(b, iamapi.OpCreatePolicyVersion, policyNamed(func(in *iamapi.CreatePolicyVersionInput) string { return in.PolicyArn }))
	kernel.Declare(b, iamapi.OpGetPolicyVersion, policyNamed(func(in *iamapi.GetPolicyVersionInput) string { return in.PolicyArn }))
	kernel.Declare(b, iamapi.OpListPolicyVersions, policyNamed(func(in *iamapi.ListPolicyVersionsInput) string { return in.PolicyArn }))
	kernel.Declare(b, iamapi.OpDeletePolicyVersion, policyNamed(func(in *iamapi.DeletePolicyVersionInput) string { return in.PolicyArn }))
	kernel.Declare(b, iamapi.OpSetDefaultPolicyVersion, policyNamed(func(in *iamapi.SetDefaultPolicyVersionInput) string { return in.PolicyArn }))
	kernel.Declare(b, iamapi.OpListEntitiesForPolicy, policyNamed(func(in *iamapi.ListEntitiesForPolicyInput) string { return in.PolicyArn }))
	kernel.Declare(b, iamapi.OpTagPolicy, policyNamed(func(in *iamapi.TagPolicyInput) string { return in.PolicyArn }))
	kernel.Declare(b, iamapi.OpUntagPolicy, policyNamed(func(in *iamapi.UntagPolicyInput) string { return in.PolicyArn }))
	kernel.Declare(b, iamapi.OpListPolicyTags, policyNamed(func(in *iamapi.ListPolicyTagsInput) string { return in.PolicyArn }))

	kernel.Declare(b, iamapi.OpCreateUser, created(func(account aws.AccountID, in *iamapi.CreateUserInput) aws.ARN {
		return userARN(account, pathOrRoot(in.Path), in.UserName)
	}))
	kernel.Declare(b, iamapi.OpCreateRole, created(func(account aws.AccountID, in *iamapi.CreateRoleInput) aws.ARN {
		return roleARN(account, pathOrRoot(in.Path), in.RoleName)
	}))
	kernel.Declare(b, iamapi.OpCreatePolicy, created(func(account aws.AccountID, in *iamapi.CreatePolicyInput) aws.ARN {
		return localPolicyARN(account, pathOrRoot(in.Path), in.PolicyName)
	}))
	kernel.Declare(b, iamapi.OpCreateInstanceProfile, created(func(account aws.AccountID, in *iamapi.CreateInstanceProfileInput) aws.ARN {
		return instanceProfileARN(account, pathOrRoot(in.Path), in.InstanceProfileName)
	}))
	// A create needs its tag action only when it sends tags
	// (iam-enforcement-surfaces.md 10.2, 10.4).
	kernel.Declare(b, iamapi.OpCreateUser, taggedWith("TagUser", func(in *iamapi.CreateUserInput) []iamapi.Tag { return in.Tags }))
	kernel.Declare(b, iamapi.OpCreateRole, taggedWith("TagRole", func(in *iamapi.CreateRoleInput) []iamapi.Tag { return in.Tags }))
	kernel.Declare(b, iamapi.OpCreatePolicy, taggedWith("TagPolicy", func(in *iamapi.CreatePolicyInput) []iamapi.Tag { return in.Tags }))
	kernel.Declare(b, iamapi.OpCreateInstanceProfile, taggedWith("TagInstanceProfile", func(in *iamapi.CreateInstanceProfileInput) []iamapi.Tag { return in.Tags }))
}

// taggedWith declares the IAM tag action a create uses when its input
// sends tags.
func taggedWith[In any](action string, tags func(*In) []iamapi.Tag) kernel.BindOption {
	return kernel.WithActions(func(_ *kernel.Call, in *In) []registry.IAMAction {
		if len(tags(in)) == 0 {
			return nil
		}
		return []registry.IAMAction{iamAction(action)}
	})
}

// declareUsers declares the operations on a user named, or defaulting to
// the caller.
func (s *Service) declareUsers(b *kernel.Binder) {
	kernel.Declare(b, iamapi.OpGetUser, userNamed(s, func(in *iamapi.GetUserInput) *string { return in.UserName })...)
	kernel.Declare(b, iamapi.OpUpdateUser, userNamed(s, func(in *iamapi.UpdateUserInput) *string { return &in.UserName })...)
	kernel.Declare(b, iamapi.OpDeleteUser, userNamed(s, func(in *iamapi.DeleteUserInput) *string { return &in.UserName })...)
	kernel.Declare(b, iamapi.OpPutUserPermissionsBoundary, userNamed(s, func(in *iamapi.PutUserPermissionsBoundaryInput) *string { return &in.UserName })...)
	kernel.Declare(b, iamapi.OpDeleteUserPermissionsBoundary, userNamed(s, func(in *iamapi.DeleteUserPermissionsBoundaryInput) *string { return &in.UserName })...)
	kernel.Declare(b, iamapi.OpListGroupsForUser, userNamed(s, func(in *iamapi.ListGroupsForUserInput) *string { return &in.UserName })...)
	kernel.Declare(b, iamapi.OpCreateAccessKey, userNamed(s, func(in *iamapi.CreateAccessKeyInput) *string { return in.UserName })...)
	kernel.Declare(b, iamapi.OpListAccessKeys, userNamed(s, func(in *iamapi.ListAccessKeysInput) *string { return in.UserName })...)
	kernel.Declare(b, iamapi.OpUpdateAccessKey, userNamed(s, func(in *iamapi.UpdateAccessKeyInput) *string { return in.UserName })...)
	kernel.Declare(b, iamapi.OpDeleteAccessKey, userNamed(s, func(in *iamapi.DeleteAccessKeyInput) *string { return in.UserName })...)
	kernel.Declare(b, iamapi.OpPutUserPolicy, userNamed(s, func(in *iamapi.PutUserPolicyInput) *string { return &in.UserName })...)
	kernel.Declare(b, iamapi.OpGetUserPolicy, userNamed(s, func(in *iamapi.GetUserPolicyInput) *string { return &in.UserName })...)
	kernel.Declare(b, iamapi.OpDeleteUserPolicy, userNamed(s, func(in *iamapi.DeleteUserPolicyInput) *string { return &in.UserName })...)
	kernel.Declare(b, iamapi.OpListUserPolicies, userNamed(s, func(in *iamapi.ListUserPoliciesInput) *string { return &in.UserName })...)
	kernel.Declare(b, iamapi.OpAttachUserPolicy, userNamed(s, func(in *iamapi.AttachUserPolicyInput) *string { return &in.UserName })...)
	kernel.Declare(b, iamapi.OpDetachUserPolicy, userNamed(s, func(in *iamapi.DetachUserPolicyInput) *string { return &in.UserName })...)
	kernel.Declare(b, iamapi.OpListAttachedUserPolicies, userNamed(s, func(in *iamapi.ListAttachedUserPoliciesInput) *string { return &in.UserName })...)
	kernel.Declare(b, iamapi.OpTagUser, userNamed(s, func(in *iamapi.TagUserInput) *string { return &in.UserName })...)
	kernel.Declare(b, iamapi.OpUntagUser, userNamed(s, func(in *iamapi.UntagUserInput) *string { return &in.UserName })...)
	kernel.Declare(b, iamapi.OpListUserTags, userNamed(s, func(in *iamapi.ListUserTagsInput) *string { return &in.UserName })...)
}

// declareRoles declares the operations on a role named; GetRole
// authorizes in its handler.
func (s *Service) declareRoles(b *kernel.Binder) {
	kernel.Declare(b, iamapi.OpUpdateRole, roleNamed(s, func(in *iamapi.UpdateRoleInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpUpdateRoleDescription, roleNamed(s, func(in *iamapi.UpdateRoleDescriptionInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpUpdateAssumeRolePolicy, roleNamed(s, func(in *iamapi.UpdateAssumeRolePolicyInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpDeleteRole, roleNamed(s, func(in *iamapi.DeleteRoleInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpPutRolePermissionsBoundary, roleNamed(s, func(in *iamapi.PutRolePermissionsBoundaryInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpDeleteRolePermissionsBoundary, roleNamed(s, func(in *iamapi.DeleteRolePermissionsBoundaryInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpPutRolePolicy, roleNamed(s, func(in *iamapi.PutRolePolicyInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpGetRolePolicy, roleNamed(s, func(in *iamapi.GetRolePolicyInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpDeleteRolePolicy, roleNamed(s, func(in *iamapi.DeleteRolePolicyInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpListRolePolicies, roleNamed(s, func(in *iamapi.ListRolePoliciesInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpAttachRolePolicy, roleNamed(s, func(in *iamapi.AttachRolePolicyInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpDetachRolePolicy, roleNamed(s, func(in *iamapi.DetachRolePolicyInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpListAttachedRolePolicies, roleNamed(s, func(in *iamapi.ListAttachedRolePoliciesInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpTagRole, roleNamed(s, func(in *iamapi.TagRoleInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpUntagRole, roleNamed(s, func(in *iamapi.UntagRoleInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpListRoleTags, roleNamed(s, func(in *iamapi.ListRoleTagsInput) string { return in.RoleName })...)
	kernel.Declare(b, iamapi.OpListInstanceProfilesForRole, roleNamed(s, func(in *iamapi.ListInstanceProfilesForRoleInput) string { return in.RoleName })...)
}

// exists answers the error find returns for a missing entity, read before
// a write that resolves the entity again under the cell's lock, so a
// missing entity answers without taking it. It does not order the answer
// against authorization: step 8 asks about a missing entity before the
// handler runs (missingPath).
func (s *Service) exists(c *kernel.Call, find func(st *State) *awserr.Error) *awserr.Error {
	_, err := store.View2(s.cells, c.Scope().Account, func(st *State) (struct{}, *awserr.Error) { return struct{}{}, find(st) })
	return err
}

// userExists is exists for the user name.
func (s *Service) userExists(c *kernel.Call, name string) *awserr.Error {
	return s.exists(c, func(st *State) *awserr.Error {
		_, err := st.user(name)
		return err
	})
}

// roleExists is exists for the role name.
func (s *Service) roleExists(c *kernel.Call, name string) *awserr.Error {
	return s.exists(c, func(st *State) *awserr.Error {
		_, err := st.role(name)
		return err
	})
}

// profileExists is exists for the instance profile name.
func (s *Service) profileExists(c *kernel.Call, name string) *awserr.Error {
	return s.exists(c, func(st *State) *awserr.Error {
		_, err := st.instanceProfile(name)
		return err
	})
}

// holderExists is exists for the role or user whose policies h holds.
func (s *Service) holderExists(c *kernel.Call, h holder) *awserr.Error {
	return s.exists(c, func(st *State) *awserr.Error {
		_, _, err := h.get(st)
		return err
	})
}

// taggedExists is exists for the entity whose tags e reaches.
func (s *Service) taggedExists(c *kernel.Call, e tagged) *awserr.Error {
	return s.exists(c, func(st *State) *awserr.Error {
		_, _, err := e.get(st)
		return err
	})
}

// deniedFirst authorizes an operation at step 8 on the entity resolve
// names from the state before the handler, false for an input that names
// none, and names it in a denial as text gives it, "" for its ARN
// (kernel.WithDenialResource).
func deniedFirst[In any](resolve func(c *kernel.Call, in *In) (aws.ARN, bool), text func(in *In) string) []kernel.BindOption {
	return []kernel.BindOption{
		kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
			if arn, ok := resolve(c, in); ok {
				return []aws.ARN{arn}
			}
			return nil
		}),
		kernel.WithDenialResource(func(_ *kernel.Call, in *In) string { return text(in) }),
	}
}

// userNamed authorizes an operation on the user name names, or on the
// caller when it names none, as IAM defaults UserName: the root's ARN for a
// root caller, else the user's ARN, at missingPath for a user IAM does not
// hold. A denial names a user the input names "user <name>".
func userNamed[In any](s *Service, name func(*In) *string) []kernel.BindOption {
	return deniedFirst(func(c *kernel.Call, in *In) (aws.ARN, bool) {
		who, err := callerUser(c, name(in))
		if err != nil {
			return aws.ARN{}, false
		}
		account := c.Scope().Account
		switch who := who.(type) {
		case accountRoot:
			return rootARN(account), true
		case namedUser:
			u, err := store.View2(s.cells, account, func(st *State) (User, *awserr.Error) { return st.user(who.name) })
			if err != nil {
				return userARN(account, missingPath, who.name), true
			}
			return userARN(account, u.Path, u.Name), true
		}
		return aws.ARN{}, false
	}, func(in *In) string {
		if n := name(in); n != nil {
			return "user " + *n
		}
		return ""
	})
}

// roleNamed authorizes an operation on the role name names, named
// "role <name>" as GetRole names it (IAM-ENF-10).
func roleNamed[In any](s *Service, name func(*In) string) []kernel.BindOption {
	return deniedFirst(func(c *kernel.Call, in *In) (aws.ARN, bool) {
		if arn, ok := s.roleARN(c, name(in)); ok {
			return arn, true
		}
		return roleARN(c.Scope().Account, missingPath, name(in)), true
	}, func(in *In) string { return "role " + name(in) })
}

// profileNamed authorizes an operation on the instance profile name names,
// named "instance-profile <name>" by inference from GetRole.
func profileNamed[In any](s *Service, name func(*In) string) []kernel.BindOption {
	return deniedFirst(func(c *kernel.Call, in *In) (aws.ARN, bool) {
		if arn, ok := s.profileARN(c, name(in)); ok {
			return arn, true
		}
		return instanceProfileARN(c.Scope().Account, missingPath, name(in)), true
	}, func(in *In) string { return "instance-profile " + name(in) })
}

// roleARN is the ARN of the role name names in the caller's account, false
// when there is none.
func (s *Service) roleARN(c *kernel.Call, name string) (aws.ARN, bool) {
	account := c.Scope().Account
	r, err := store.View2(s.cells, account, func(st *State) (Role, *awserr.Error) { return st.role(name) })
	return roleARN(account, r.Path, r.Name), err == nil
}

// profileARN is the ARN of the instance profile name names in the caller's
// account, false when there is none.
func (s *Service) profileARN(c *kernel.Call, name string) (aws.ARN, bool) {
	account := c.Scope().Account
	p, err := store.View2(s.cells, account, func(st *State) (InstanceProfile, *awserr.Error) { return st.instanceProfile(name) })
	return instanceProfileARN(account, p.Path, p.Name), err == nil
}

// found is arn alone when ok, else nothing.
func found(arn aws.ARN, ok bool) []aws.ARN {
	if !ok {
		return nil
	}
	return []aws.ARN{arn}
}

// policyNamed asks at step 8 about the managed policy whose ARN the input
// carries; an ARN that is no policy's names nothing, and the handler
// answers it.
func policyNamed[In any](arn func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(_ *kernel.Call, in *In) []aws.ARN {
		ref, ok := parsePolicyARN(arn(in))
		if !ok {
			return nil
		}
		return []aws.ARN{ref.arn}
	})
}

// created asks at step 8 about the ARN a creation names: its path, or "/",
// and its name. The generated validator has already refused a malformed
// path, since every creation's Path targets pathType or policyPathType,
// both anchored on the whole value.
func created[In any](arn func(account aws.AccountID, in *In) aws.ARN) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		return []aws.ARN{arn(c.Scope().Account, in)}
	})
}
