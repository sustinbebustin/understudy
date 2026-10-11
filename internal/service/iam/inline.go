package iam

import (
	"context"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindInline binds the inline policies of roles and users.
func (s *Service) bindInline(b iamapi.Binder) {
	stored := registry.StoredOnly{Note: "the document is validated and stored, not enforced"}
	b.PutRolePolicy(func(_ context.Context, c *kernel.Call, in *iamapi.PutRolePolicyInput) (*iamapi.PutRolePolicyOutput, *awserr.Error) {
		return unit[iamapi.PutRolePolicyOutput](s.putInline(c, roleHolder(c.Scope().Account, in.RoleName), in.PolicyName, in.PolicyDocument))
	}, stored)
	b.GetRolePolicy(func(_ context.Context, c *kernel.Call, in *iamapi.GetRolePolicyInput) (*iamapi.GetRolePolicyOutput, *awserr.Error) {
		p, err := s.getInline(c, roleHolder(c.Scope().Account, in.RoleName), in.PolicyName)
		if err != nil {
			return nil, err
		}
		return &iamapi.GetRolePolicyOutput{RoleName: in.RoleName, PolicyName: p.Name, PolicyDocument: p.Document}, nil
	}, registry.Full{})
	b.DeleteRolePolicy(func(_ context.Context, c *kernel.Call, in *iamapi.DeleteRolePolicyInput) (*iamapi.DeleteRolePolicyOutput, *awserr.Error) {
		return unit[iamapi.DeleteRolePolicyOutput](s.deleteInline(c, roleHolder(c.Scope().Account, in.RoleName), in.PolicyName))
	}, registry.Full{})
	b.ListRolePolicies(func(_ context.Context, c *kernel.Call, in *iamapi.ListRolePoliciesInput) (*iamapi.ListRolePoliciesOutput, *awserr.Error) {
		p, err := s.listInline(c, roleHolder(c.Scope().Account, in.RoleName), in.Marker, in.MaxItems)
		if err != nil {
			return nil, err
		}
		return &iamapi.ListRolePoliciesOutput{PolicyNames: p.items, IsTruncated: p.truncated, Marker: p.marker}, nil
	}, registry.Full{})
	b.PutUserPolicy(func(_ context.Context, c *kernel.Call, in *iamapi.PutUserPolicyInput) (*iamapi.PutUserPolicyOutput, *awserr.Error) {
		return unit[iamapi.PutUserPolicyOutput](s.putInline(c, userHolder(c.Scope().Account, in.UserName), in.PolicyName, in.PolicyDocument))
	}, stored)
	b.GetUserPolicy(func(_ context.Context, c *kernel.Call, in *iamapi.GetUserPolicyInput) (*iamapi.GetUserPolicyOutput, *awserr.Error) {
		p, err := s.getInline(c, userHolder(c.Scope().Account, in.UserName), in.PolicyName)
		if err != nil {
			return nil, err
		}
		return &iamapi.GetUserPolicyOutput{UserName: in.UserName, PolicyName: p.Name, PolicyDocument: p.Document}, nil
	}, registry.Full{})
	b.DeleteUserPolicy(func(_ context.Context, c *kernel.Call, in *iamapi.DeleteUserPolicyInput) (*iamapi.DeleteUserPolicyOutput, *awserr.Error) {
		return unit[iamapi.DeleteUserPolicyOutput](s.deleteInline(c, userHolder(c.Scope().Account, in.UserName), in.PolicyName))
	}, registry.Full{})
	b.ListUserPolicies(func(_ context.Context, c *kernel.Call, in *iamapi.ListUserPoliciesInput) (*iamapi.ListUserPoliciesOutput, *awserr.Error) {
		p, err := s.listInline(c, userHolder(c.Scope().Account, in.UserName), in.Marker, in.MaxItems)
		if err != nil {
			return nil, err
		}
		return &iamapi.ListUserPoliciesOutput{PolicyNames: p.items, IsTruncated: p.truncated, Marker: p.marker}, nil
	}, registry.Full{})
}

// inlineIndex finds an inline policy by name; names are unique regardless
// of case, like every IAM name.
func inlineIndex(ps []InlinePolicy, name string) int {
	return slices.IndexFunc(ps, func(p InlinePolicy) bool { return strings.EqualFold(p.Name, name) })
}

// putInline adds or replaces an inline policy. The document is kept byte
// for byte, so GetRolePolicy returns exactly what was put.
func (s *Service) putInline(c *kernel.Call, h holder, name, doc string) *awserr.Error {
	if err := checkIdentityPolicy(doc); err != nil {
		return err
	}
	arn, err := kernel.Write(c, s.cells, c.Scope().Account, func(st *State, _ clock.Instant, _ *kernel.Tx) (aws.ARN, *awserr.Error) {
		have, arn, err := h.get(st)
		if err != nil {
			return aws.ARN{}, err
		}
		inline := slices.Clone(have.inline)
		p := InlinePolicy{Name: name, Document: doc}
		if i := inlineIndex(inline, name); i >= 0 {
			inline[i] = p
		} else {
			inline = append(inline, p)
		}
		size := 0
		for _, p := range inline {
			size += policySize(p.Document)
		}
		if size > h.inlineLimit {
			return aws.ARN{}, errInlineTooLarge(h.kind, h.name, h.inlineLimit)
		}
		h.set(st, holdings{inline: inline, attached: have.attached})
		return arn, nil
	})
	if err != nil {
		return err
	}
	c.Touch(arn)
	return nil
}

func (s *Service) getInline(c *kernel.Call, h holder, name string) (InlinePolicy, *awserr.Error) {
	type found struct {
		policy InlinePolicy
		arn    aws.ARN
	}
	f, err := view(s, c.Scope().Account, func(st *State) (found, *awserr.Error) {
		have, arn, err := h.get(st)
		if err != nil {
			return found{}, err
		}
		i := inlineIndex(have.inline, name)
		if i < 0 {
			return found{}, h.noSuchInline(name)
		}
		return found{policy: have.inline[i], arn: arn}, nil
	})
	if err != nil {
		return InlinePolicy{}, err
	}
	c.Touch(f.arn)
	return f.policy, nil
}

func (s *Service) deleteInline(c *kernel.Call, h holder, name string) *awserr.Error {
	arn, err := kernel.Write(c, s.cells, c.Scope().Account, func(st *State, _ clock.Instant, _ *kernel.Tx) (aws.ARN, *awserr.Error) {
		have, arn, err := h.get(st)
		if err != nil {
			return aws.ARN{}, err
		}
		i := inlineIndex(have.inline, name)
		if i < 0 {
			return aws.ARN{}, h.noSuchInline(name)
		}
		inline := slices.Delete(slices.Clone(have.inline), i, i+1)
		if len(inline) == 0 {
			inline = nil
		}
		h.set(st, holdings{inline: inline, attached: have.attached})
		return arn, nil
	})
	if err != nil {
		return err
	}
	c.Touch(arn)
	return nil
}

// listInline pages through the entity's inline policy names in
// alphabetical order regardless of case.
func (s *Service) listInline(c *kernel.Call, h holder, marker *string, maxItems *int32) (page[string], *awserr.Error) {
	type listed struct {
		p   page[string]
		arn aws.ARN
	}
	l, err := view(s, c.Scope().Account, func(st *State) (listed, *awserr.Error) {
		have, arn, err := h.get(st)
		if err != nil {
			return listed{}, err
		}
		names := make([]string, len(have.inline))
		for i, p := range have.inline {
			names[i] = p.Name
		}
		slices.SortFunc(names, func(a, b string) int { return strings.Compare(key(a), key(b)) })
		p, err := paginate(names, key, marker, maxItems)
		return listed{p: p, arn: arn}, err
	})
	if err != nil {
		return page[string]{}, err
	}
	c.Touch(l.arn)
	return l.p, nil
}
