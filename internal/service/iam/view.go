package iam

import (
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/store"
)

// view runs fn on the account's state without creating its cell; an
// account without one reads as empty. fn must not change the state, and
// its result must not alias it.
func view[R any](s *Service, account aws.AccountID, fn func(st *State) (R, *awserr.Error)) (R, *awserr.Error) {
	cell, ok := s.cells.Lookup(account)
	if !ok {
		empty := newState(account)
		return fn(&empty)
	}
	type result struct {
		r   R
		err *awserr.Error
	}
	res := store.Read(cell, func(st *State) result {
		r, err := fn(st)
		return result{r: r, err: err}
	})
	return res.r, res.err
}

// sortedWhere returns the records of m that keep accepts, in key order,
// the order IAM listings page through.
func sortedWhere[T any](m map[string]T, keep func(*T) bool) []T {
	var out []T
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if v := m[k]; keep(&v) {
			out = append(out, v)
		}
	}
	return out
}

// apiRole is a role as ListRoles shows it, without the tags, permissions
// boundary and last use that only GetRole returns (iam-sts-kms.md 3.2).
func apiRole(account aws.AccountID, r Role) iamapi.Role {
	out := iamapi.Role{
		Path:                     r.Path,
		RoleName:                 r.Name,
		RoleId:                   string(r.ID),
		Arn:                      roleARN(account, r.Path, r.Name).String(),
		CreateDate:               r.Created.Wire(),
		AssumeRolePolicyDocument: new(r.TrustPolicy),
		MaxSessionDuration:       new(r.MaxSessionDuration),
	}
	if r.Description != "" {
		out.Description = new(r.Description)
	}
	return out
}

// apiRoleDetail is a role as GetRole, CreateRole and an instance profile
// show it: everything, with an empty RoleLastUsed, since no session use is
// tracked.
func apiRoleDetail(account aws.AccountID, r Role) iamapi.Role {
	out := apiRole(account, r)
	out.Tags = apiTags(r.Tags)
	out.PermissionsBoundary = apiBoundary(r.Boundary)
	out.RoleLastUsed = &iamapi.RoleLastUsed{}
	return out
}

// apiUser is a user as ListUsers shows it.
func apiUser(account aws.AccountID, u User) iamapi.User {
	return iamapi.User{
		Path:       u.Path,
		UserName:   u.Name,
		UserId:     string(u.ID),
		Arn:        userARN(account, u.Path, u.Name).String(),
		CreateDate: u.Created.Wire(),
	}
}

// apiUserDetail is a user as GetUser and CreateUser show it, tags and
// permissions boundary included: Terraform reads a user's tags from GetUser
// (tf-remaining.md 3.1).
func apiUserDetail(account aws.AccountID, u User) iamapi.User {
	out := apiUser(account, u)
	out.Tags = apiTags(u.Tags)
	out.PermissionsBoundary = apiBoundary(u.Boundary)
	return out
}

// apiBoundary is a permissions boundary, nil when none is set.
func apiBoundary(arn string) *iamapi.AttachedPermissionsBoundary {
	if arn == "" {
		return nil
	}
	return &iamapi.AttachedPermissionsBoundary{
		PermissionsBoundaryType: new(iamapi.PermissionsBoundaryAttachmentTypePolicy),
		PermissionsBoundaryArn:  new(arn),
	}
}

// apiInstanceProfile is an instance profile with its role in full, as
// Terraform reads it (tf-remaining.md 3.5).
func apiInstanceProfile(account aws.AccountID, st *State, p InstanceProfile) iamapi.InstanceProfile {
	roles := []iamapi.Role{}
	if r, ok := st.Roles[p.Role]; ok {
		roles = append(roles, apiRoleDetail(account, r))
	}
	return iamapi.InstanceProfile{
		Path:                p.Path,
		InstanceProfileName: p.Name,
		InstanceProfileId:   string(p.ID),
		Arn:                 instanceProfileARN(account, p.Path, p.Name).String(),
		CreateDate:          p.Created.Wire(),
		Roles:               roles,
		Tags:                apiTags(p.Tags),
	}
}

// apiAttached lists attached policies as AttachedPolicy entries, the name
// being the ARN's last segment.
func apiAttached(refs []policyRef) []iamapi.AttachedPolicy {
	out := make([]iamapi.AttachedPolicy, 0, len(refs))
	for i := range refs {
		out = append(out, iamapi.AttachedPolicy{PolicyName: new(refs[i].name), PolicyArn: new(refs[i].arn.String())})
	}
	return out
}
