package iam

import (
	"context"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/paging"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// defaultMaxSessionDuration is a role's MaxSessionDuration when CreateRole
// sets none: one hour, which Terraform expects back (iam-sts-kms.md 3.2).
const defaultMaxSessionDuration = 3600

// bindRoles binds the role control plane and the internal ResolveRole.
func (s *Service) bindRoles(b iamapi.Binder) {
	stored := registry.StoredOnly{Note: "the trust policy is validated and stored, not enforced"}
	b.CreateRole(s.createRole, registry.Partial{Note: stored.Note + "; as PermissionsBoundary, " + unknownAWSManaged})
	b.GetRole(s.getRole, registry.Full{})
	b.UpdateRole(s.updateRole, registry.Full{})
	b.UpdateRoleDescription(s.updateRoleDescription, registry.Full{})
	b.UpdateAssumeRolePolicy(s.updateAssumeRolePolicy, stored)
	b.DeleteRole(s.deleteRole, registry.Full{})
	b.ListRoles(s.listRoles, registry.Full{})
	b.PutRolePermissionsBoundary(s.putRolePermissionsBoundary, registry.Partial{Note: boundaryNote})
	b.DeleteRolePermissionsBoundary(s.deleteRolePermissionsBoundary, registry.Full{})
	b.ResolveRole(s.resolveRole, registry.Full{})
}

// role finds a role by name.
func (st *State) role(name string) (Role, *awserr.Error) {
	r, ok := st.Roles[key(name)]
	if !ok {
		return Role{}, errNoSuchRole(name)
	}
	return r, nil
}

// boundaryNote is the coverage note of PutRolePermissionsBoundary and
// PutUserPermissionsBoundary.
const boundaryNote = "the boundary is stored, not enforced; " + unknownAWSManaged

// boundary checks a permissions boundary ARN: an AWS managed policy,
// existing or not (unknownAWSManaged), or a customer managed one of the
// account.
func (st *State) boundary(account aws.AccountID, arn string) *awserr.Error {
	ref, ok := parsePolicyARN(arn)
	if !ok {
		return errInvalidPolicyARN(arn)
	}
	if ref.awsManaged() {
		return nil
	}
	if _, _, err := st.localPolicy(account, arn); err != nil {
		return errNoSuchBoundary(arn)
	}
	return nil
}

func (s *Service) createRole(_ context.Context, c *kernel.Call, in *iamapi.CreateRoleInput) (*iamapi.CreateRoleOutput, *awserr.Error) {
	if err := checkPath("path", in.Path); err != nil {
		return nil, err
	}
	if err := checkTrustPolicy(in.AssumeRolePolicyDocument); err != nil {
		return nil, err
	}
	tags, err := newTags(foldedKeys, in.Tags)
	if err != nil {
		return nil, err
	}
	account := c.Scope().Account
	r := Role{
		Name:               in.RoleName,
		Path:               pathOrRoot(in.Path),
		TrustPolicy:        in.AssumeRolePolicyDocument,
		MaxSessionDuration: defaultMaxSessionDuration,
		Tags:               tags,
	}
	if in.Description != nil {
		r.Description = *in.Description
	}
	if in.MaxSessionDuration != nil {
		r.MaxSessionDuration = *in.MaxSessionDuration
	}
	if in.PermissionsBoundary != nil {
		r.Boundary = *in.PermissionsBoundary
	}
	out, err := kernel.Write(c, s.cells, account, func(st *State, now clock.Instant, _ *kernel.Tx) (iamapi.Role, *awserr.Error) {
		if _, ok := st.Roles[key(r.Name)]; ok {
			return iamapi.Role{}, errRoleExists(r.Name)
		}
		if r.Boundary != "" {
			if err := st.boundary(account, r.Boundary); err != nil {
				return iamapi.Role{}, err
			}
		}
		r.ID = s.ids.RoleID()
		r.Created = now
		st.Roles[key(r.Name)] = r
		// CreateRole shows neither RoleLastUsed nor MaxSessionDuration,
		// which GetRole and ListRoles return (LocalStack's AWS-recorded
		// tests/aws/services/iam/test_iam.snapshot.json at 8b9a79f0, lines
		// 117 to 200, recorded 2025-03-06).
		created := apiRoleDetail(account, r)
		created.RoleLastUsed, created.MaxSessionDuration = nil, nil
		return created, nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(roleARN(account, r.Path, r.Name))
	return &iamapi.CreateRoleOutput{Role: out}, nil
}

func (s *Service) getRole(_ context.Context, c *kernel.Call, in *iamapi.GetRoleInput) (*iamapi.GetRoleOutput, *awserr.Error) {
	account := c.Scope().Account
	r, err := view(s, account, func(st *State) (Role, *awserr.Error) { return st.role(in.RoleName) })
	if err != nil {
		return nil, err
	}
	c.Touch(roleARN(account, r.Path, r.Name))
	return &iamapi.GetRoleOutput{Role: apiRoleDetail(account, r)}, nil
}

// changeRole applies change to the named role under the account's cell
// and touches it.
func (s *Service) changeRole(c *kernel.Call, name string, change func(st *State, r Role) (Role, *awserr.Error)) (Role, *awserr.Error) {
	account := c.Scope().Account
	r, err := kernel.Write(c, s.cells, account, func(st *State, _ clock.Instant, _ *kernel.Tx) (Role, *awserr.Error) {
		r, err := st.role(name)
		if err != nil {
			return Role{}, err
		}
		if r, err = change(st, r); err != nil {
			return Role{}, err
		}
		st.Roles[key(name)] = r
		return r, nil
	})
	if err != nil {
		return Role{}, err
	}
	c.Touch(roleARN(account, r.Path, r.Name))
	return r, nil
}

func (s *Service) updateRole(_ context.Context, c *kernel.Call, in *iamapi.UpdateRoleInput) (*iamapi.UpdateRoleOutput, *awserr.Error) {
	_, err := s.changeRole(c, in.RoleName, func(_ *State, r Role) (Role, *awserr.Error) {
		if in.Description != nil {
			r.Description = *in.Description
		}
		if in.MaxSessionDuration != nil {
			r.MaxSessionDuration = *in.MaxSessionDuration
		}
		return r, nil
	})
	return unit[iamapi.UpdateRoleOutput](err)
}

func (s *Service) updateRoleDescription(_ context.Context, c *kernel.Call, in *iamapi.UpdateRoleDescriptionInput) (*iamapi.UpdateRoleDescriptionOutput, *awserr.Error) {
	r, err := s.changeRole(c, in.RoleName, func(_ *State, r Role) (Role, *awserr.Error) {
		r.Description = in.Description
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	return &iamapi.UpdateRoleDescriptionOutput{Role: new(apiRoleDetail(c.Scope().Account, r))}, nil
}

func (s *Service) updateAssumeRolePolicy(_ context.Context, c *kernel.Call, in *iamapi.UpdateAssumeRolePolicyInput) (*iamapi.UpdateAssumeRolePolicyOutput, *awserr.Error) {
	if err := checkTrustPolicy(in.PolicyDocument); err != nil {
		return nil, err
	}
	_, err := s.changeRole(c, in.RoleName, func(_ *State, r Role) (Role, *awserr.Error) {
		r.TrustPolicy = in.PolicyDocument
		return r, nil
	})
	return unit[iamapi.UpdateAssumeRolePolicyOutput](err)
}

func (s *Service) putRolePermissionsBoundary(_ context.Context, c *kernel.Call, in *iamapi.PutRolePermissionsBoundaryInput) (*iamapi.PutRolePermissionsBoundaryOutput, *awserr.Error) {
	account := c.Scope().Account
	_, err := s.changeRole(c, in.RoleName, func(st *State, r Role) (Role, *awserr.Error) {
		if err := st.boundary(account, in.PermissionsBoundary); err != nil {
			return Role{}, err
		}
		r.Boundary = in.PermissionsBoundary
		return r, nil
	})
	return unit[iamapi.PutRolePermissionsBoundaryOutput](err)
}

func (s *Service) deleteRolePermissionsBoundary(_ context.Context, c *kernel.Call, in *iamapi.DeleteRolePermissionsBoundaryInput) (*iamapi.DeleteRolePermissionsBoundaryOutput, *awserr.Error) {
	_, err := s.changeRole(c, in.RoleName, func(_ *State, r Role) (Role, *awserr.Error) {
		r.Boundary = ""
		return r, nil
	})
	return unit[iamapi.DeleteRolePermissionsBoundaryOutput](err)
}

// deleteRole deletes a role that nothing depends on: IAM detaches nothing
// itself, so instance profile membership, attached policies and inline
// policies each refuse the delete, checked in moto's order.
func (s *Service) deleteRole(_ context.Context, c *kernel.Call, in *iamapi.DeleteRoleInput) (*iamapi.DeleteRoleOutput, *awserr.Error) {
	account := c.Scope().Account
	r, err := kernel.Write(c, s.cells, account, func(st *State, _ clock.Instant, _ *kernel.Tx) (Role, *awserr.Error) {
		r, err := st.role(in.RoleName)
		if err != nil {
			return Role{}, err
		}
		for _, p := range st.InstanceProfiles {
			if p.Role == key(r.Name) {
				return Role{}, errMustRemoveFromProfiles()
			}
		}
		switch {
		case len(r.Attached) > 0:
			return Role{}, errMustDetachPolicies()
		case len(r.Inline) > 0:
			return Role{}, errMustDeletePolicies()
		}
		delete(st.Roles, key(r.Name))
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(roleARN(account, r.Path, r.Name))
	return &iamapi.DeleteRoleOutput{}, nil
}

func (s *Service) listRoles(_ context.Context, c *kernel.Call, in *iamapi.ListRolesInput) (*iamapi.ListRolesOutput, *awserr.Error) {
	account := c.Scope().Account
	p, err := view(s, account, func(st *State) (paging.Page[Role], *awserr.Error) {
		roles := sortedWhere(st.Roles, func(r *Role) bool { return underPrefix(r.Path, in.PathPrefix) })
		return paginate("roles", roles, func(r Role) string { return key(r.Name) }, in.Marker, in.MaxItems)
	})
	if err != nil {
		return nil, err
	}
	out := &iamapi.ListRolesOutput{Roles: make([]iamapi.Role, len(p.Items)), IsTruncated: p.Truncated, Marker: p.Next}
	for i := range p.Items {
		out.Roles[i] = apiRole(account, p.Items[i])
	}
	return out, nil
}

// resolveRole is the internal operation STS reads a role through: the
// role the ARN names, in the call's account, whose path matches the ARN's.
func (s *Service) resolveRole(_ context.Context, c *kernel.Call, in *iamapi.ResolveRoleInput) (*iamapi.ResolveRoleOutput, *awserr.Error) {
	account := c.Scope().Account
	notFound := iamapi.ErrNoSuchEntityException("The role " + in.RoleArn + " cannot be found.")
	arn, err := aws.ParseARN(in.RoleArn)
	if err != nil || arn.Service != "iam" || arn.Account != account.String() {
		return nil, notFound
	}
	rest, ok := strings.CutPrefix(arn.Resource, "role/")
	if !ok {
		return nil, notFound
	}
	path, name := aws.SplitIAMPath(rest)
	out, aerr := view(s, account, func(st *State) (iamapi.Role, *awserr.Error) {
		r, ok := st.Roles[key(name)]
		if !ok || r.Name != name || r.Path != path {
			return iamapi.Role{}, notFound
		}
		return apiRoleDetail(account, r), nil
	})
	if aerr != nil {
		return nil, aerr
	}
	return &iamapi.ResolveRoleOutput{Role: out}, nil
}
