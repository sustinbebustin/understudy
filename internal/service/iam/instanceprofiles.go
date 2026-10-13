package iam

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/paging"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindInstanceProfiles binds instance profiles and their one role.
func (s *Service) bindInstanceProfiles(b iamapi.Binder) {
	b.CreateInstanceProfile(s.createInstanceProfile, registry.Full{})
	b.GetInstanceProfile(s.getInstanceProfile, registry.Full{})
	b.DeleteInstanceProfile(s.deleteInstanceProfile, registry.Full{})
	b.AddRoleToInstanceProfile(s.addRoleToInstanceProfile, registry.Full{})
	b.RemoveRoleFromInstanceProfile(s.removeRoleFromInstanceProfile, registry.Full{})
	b.ListInstanceProfiles(s.listInstanceProfiles, registry.Full{})
	b.ListInstanceProfilesForRole(s.listInstanceProfilesForRole, registry.Full{})
}

// apiInstanceProfiles lists the profiles keep accepts, by name, as the
// listings show them.
func (st *State) apiInstanceProfiles(account aws.AccountID, keep func(*InstanceProfile) bool) []iamapi.InstanceProfile {
	profiles := sortedWhere(st.InstanceProfiles, keep)
	out := make([]iamapi.InstanceProfile, len(profiles))
	for i := range profiles {
		out[i] = apiListedInstanceProfile(account, st, profiles[i])
	}
	return out
}

// instanceProfile finds an instance profile by name.
func (st *State) instanceProfile(name string) (InstanceProfile, *awserr.Error) {
	p, ok := st.InstanceProfiles[key(name)]
	if !ok {
		return InstanceProfile{}, errNoSuchInstanceProfile(name)
	}
	return p, nil
}

func (s *Service) createInstanceProfile(_ context.Context, c *kernel.Call, in *iamapi.CreateInstanceProfileInput) (*iamapi.CreateInstanceProfileOutput, *awserr.Error) {
	if err := checkPath("path", in.Path); err != nil {
		return nil, err
	}
	tags, err := newTags(exactKeys, in.Tags)
	if err != nil {
		return nil, err
	}
	account := c.Scope().Account
	p := InstanceProfile{Name: in.InstanceProfileName, Path: pathOrRoot(in.Path), Tags: tags}
	out, err := kernel.Write(c, s.cells, account, func(st *State, now clock.Instant, _ *kernel.Tx) (iamapi.InstanceProfile, *awserr.Error) {
		if _, ok := st.InstanceProfiles[key(p.Name)]; ok {
			return iamapi.InstanceProfile{}, errInstanceProfileExists(p.Name)
		}
		p.ID = s.ids.InstanceProfileID()
		p.Created = now
		st.InstanceProfiles[key(p.Name)] = p
		return apiInstanceProfile(account, st, p), nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(instanceProfileARN(account, p.Path, p.Name))
	return &iamapi.CreateInstanceProfileOutput{InstanceProfile: out}, nil
}

func (s *Service) getInstanceProfile(_ context.Context, c *kernel.Call, in *iamapi.GetInstanceProfileInput) (*iamapi.GetInstanceProfileOutput, *awserr.Error) {
	account := c.Scope().Account
	out, err := view(s, account, func(st *State) (iamapi.InstanceProfile, *awserr.Error) {
		p, err := st.instanceProfile(in.InstanceProfileName)
		if err != nil {
			return iamapi.InstanceProfile{}, err
		}
		return apiInstanceProfile(account, st, p), nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(instanceProfileARN(account, out.Path, out.InstanceProfileName))
	return &iamapi.GetInstanceProfileOutput{InstanceProfile: out}, nil
}

// deleteInstanceProfile deletes a profile without a role: IAM removes
// nothing itself.
func (s *Service) deleteInstanceProfile(_ context.Context, c *kernel.Call, in *iamapi.DeleteInstanceProfileInput) (*iamapi.DeleteInstanceProfileOutput, *awserr.Error) {
	account := c.Scope().Account
	p, err := kernel.Write(c, s.cells, account, func(st *State, _ clock.Instant, _ *kernel.Tx) (InstanceProfile, *awserr.Error) {
		p, err := st.instanceProfile(in.InstanceProfileName)
		if err != nil {
			return InstanceProfile{}, err
		}
		if p.Role != "" {
			return InstanceProfile{}, errMustRemoveFromProfiles()
		}
		delete(st.InstanceProfiles, key(p.Name))
		return p, nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(instanceProfileARN(account, p.Path, p.Name))
	return &iamapi.DeleteInstanceProfileOutput{}, nil
}

// changeProfileRole applies change to an instance profile and an existing
// role under the account's cell and touches both. The profile is looked up
// first, then the role.
func (s *Service) changeProfileRole(c *kernel.Call, profile, role string, change func(p InstanceProfile, r Role) (InstanceProfile, *awserr.Error)) *awserr.Error {
	account := c.Scope().Account
	type changed struct {
		profile InstanceProfile
		role    Role
	}
	out, err := kernel.Write(c, s.cells, account, func(st *State, _ clock.Instant, _ *kernel.Tx) (changed, *awserr.Error) {
		p, err := st.instanceProfile(profile)
		if err != nil {
			return changed{}, err
		}
		r, err := st.role(role)
		if err != nil {
			return changed{}, err
		}
		if p, err = change(p, r); err != nil {
			return changed{}, err
		}
		st.InstanceProfiles[key(p.Name)] = p
		return changed{profile: p, role: r}, nil
	})
	if err != nil {
		return err
	}
	c.Touch(instanceProfileARN(account, out.profile.Path, out.profile.Name), roleARN(account, out.role.Path, out.role.Name))
	return nil
}

// addRoleToInstanceProfile puts a role in a profile that has none: an
// instance profile holds at most one role, a quota AWS does not raise.
func (s *Service) addRoleToInstanceProfile(_ context.Context, c *kernel.Call, in *iamapi.AddRoleToInstanceProfileInput) (*iamapi.AddRoleToInstanceProfileOutput, *awserr.Error) {
	return unit[iamapi.AddRoleToInstanceProfileOutput](s.changeProfileRole(c, in.InstanceProfileName, in.RoleName, func(p InstanceProfile, r Role) (InstanceProfile, *awserr.Error) {
		if p.Role != "" {
			return InstanceProfile{}, errQuota("InstanceSessionsPerInstanceProfile", 1)
		}
		p.Role = key(r.Name)
		return p, nil
	}))
}

func (s *Service) removeRoleFromInstanceProfile(_ context.Context, c *kernel.Call, in *iamapi.RemoveRoleFromInstanceProfileInput) (*iamapi.RemoveRoleFromInstanceProfileOutput, *awserr.Error) {
	return unit[iamapi.RemoveRoleFromInstanceProfileOutput](s.changeProfileRole(c, in.InstanceProfileName, in.RoleName, func(p InstanceProfile, r Role) (InstanceProfile, *awserr.Error) {
		if p.Role != key(r.Name) {
			return InstanceProfile{}, errRoleNotInProfile(r.Name, p.Name)
		}
		p.Role = ""
		return p, nil
	}))
}

func (s *Service) listInstanceProfiles(_ context.Context, c *kernel.Call, in *iamapi.ListInstanceProfilesInput) (*iamapi.ListInstanceProfilesOutput, *awserr.Error) {
	account := c.Scope().Account
	p, err := view(s, account, func(st *State) (paging.Page[iamapi.InstanceProfile], *awserr.Error) {
		all := st.apiInstanceProfiles(account, func(p *InstanceProfile) bool { return underPrefix(p.Path, in.PathPrefix) })
		return paginate("instance-profiles", all, func(p iamapi.InstanceProfile) string { return key(p.InstanceProfileName) }, in.Marker, in.MaxItems)
	})
	if err != nil {
		return nil, err
	}
	return &iamapi.ListInstanceProfilesOutput{InstanceProfiles: p.Items, IsTruncated: p.Truncated, Marker: p.Next}, nil
}

// listInstanceProfilesForRole lists the profiles that hold a role, the call
// Terraform makes before every role delete (tf-compute.md 3.1).
func (s *Service) listInstanceProfilesForRole(_ context.Context, c *kernel.Call, in *iamapi.ListInstanceProfilesForRoleInput) (*iamapi.ListInstanceProfilesForRoleOutput, *awserr.Error) {
	account := c.Scope().Account
	type listed struct {
		role Role
		p    paging.Page[iamapi.InstanceProfile]
	}
	l, err := view(s, account, func(st *State) (listed, *awserr.Error) {
		r, err := st.role(in.RoleName)
		if err != nil {
			return listed{}, err
		}
		all := st.apiInstanceProfiles(account, func(p *InstanceProfile) bool { return p.Role == key(r.Name) })
		p, err := paginate("instance-profiles-for-role", all, func(p iamapi.InstanceProfile) string { return key(p.InstanceProfileName) }, in.Marker, in.MaxItems)
		return listed{role: r, p: p}, err
	})
	if err != nil {
		return nil, err
	}
	c.Touch(roleARN(account, l.role.Path, l.role.Name))
	return &iamapi.ListInstanceProfilesForRoleOutput{InstanceProfiles: l.p.Items, IsTruncated: l.p.Truncated, Marker: l.p.Next}, nil
}
