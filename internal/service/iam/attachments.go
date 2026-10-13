package iam

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/paging"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindAttachments binds the managed policy attachments of roles and users.
func (s *Service) bindAttachments(b iamapi.Binder) {
	stored := registry.Partial{Note: "the attachment is stored, not enforced; " + unknownAWSManaged}
	b.AttachRolePolicy(func(_ context.Context, c *kernel.Call, in *iamapi.AttachRolePolicyInput) (*iamapi.AttachRolePolicyOutput, *awserr.Error) {
		return unit[iamapi.AttachRolePolicyOutput](s.attach(c, roleHolder(c.Scope().Account, in.RoleName), in.PolicyArn))
	}, stored)
	b.DetachRolePolicy(func(_ context.Context, c *kernel.Call, in *iamapi.DetachRolePolicyInput) (*iamapi.DetachRolePolicyOutput, *awserr.Error) {
		return unit[iamapi.DetachRolePolicyOutput](s.detach(c, roleHolder(c.Scope().Account, in.RoleName), in.PolicyArn))
	}, registry.Full{})
	b.ListAttachedRolePolicies(func(_ context.Context, c *kernel.Call, in *iamapi.ListAttachedRolePoliciesInput) (*iamapi.ListAttachedRolePoliciesOutput, *awserr.Error) {
		p, err := s.listAttached(c, "attached-role-policies", roleHolder(c.Scope().Account, in.RoleName), in.PathPrefix, in.Marker, in.MaxItems)
		if err != nil {
			return nil, err
		}
		return &iamapi.ListAttachedRolePoliciesOutput{AttachedPolicies: p.Items, IsTruncated: p.Truncated, Marker: p.Next}, nil
	}, registry.Full{})
	b.AttachUserPolicy(func(_ context.Context, c *kernel.Call, in *iamapi.AttachUserPolicyInput) (*iamapi.AttachUserPolicyOutput, *awserr.Error) {
		return unit[iamapi.AttachUserPolicyOutput](s.attach(c, userHolder(c.Scope().Account, in.UserName), in.PolicyArn))
	}, stored)
	b.DetachUserPolicy(func(_ context.Context, c *kernel.Call, in *iamapi.DetachUserPolicyInput) (*iamapi.DetachUserPolicyOutput, *awserr.Error) {
		return unit[iamapi.DetachUserPolicyOutput](s.detach(c, userHolder(c.Scope().Account, in.UserName), in.PolicyArn))
	}, registry.Full{})
	b.ListAttachedUserPolicies(func(_ context.Context, c *kernel.Call, in *iamapi.ListAttachedUserPoliciesInput) (*iamapi.ListAttachedUserPoliciesOutput, *awserr.Error) {
		p, err := s.listAttached(c, "attached-user-policies", userHolder(c.Scope().Account, in.UserName), in.PathPrefix, in.Marker, in.MaxItems)
		if err != nil {
			return nil, err
		}
		return &iamapi.ListAttachedUserPoliciesOutput{AttachedPolicies: p.Items, IsTruncated: p.Truncated, Marker: p.Next}, nil
	}, registry.Full{})
}

// attach attaches a managed policy; attaching one already attached
// succeeds and changes nothing.
func (s *Service) attach(c *kernel.Call, h holder, arn string) *awserr.Error {
	account := c.Scope().Account
	touched, err := kernel.Write(c, s.cells, account, func(st *State, _ clock.Instant, _ *kernel.Tx) (aws.ARN, *awserr.Error) {
		have, entity, err := h.get(st)
		if err != nil {
			return aws.ARN{}, err
		}
		if _, err := st.attachable(account, arn); err != nil {
			return aws.ARN{}, err
		}
		if slices.Contains(have.attached, arn) {
			return entity, nil
		}
		if len(have.attached) >= h.attachLimit {
			return aws.ARN{}, errQuota(h.attachQuota, h.attachLimit)
		}
		h.set(st, holdings{inline: have.inline, attached: append(slices.Clone(have.attached), arn)})
		return entity, nil
	})
	if err != nil {
		return err
	}
	c.Touch(touched, policyTouch(arn))
	return nil
}

func (s *Service) detach(c *kernel.Call, h holder, arn string) *awserr.Error {
	touched, err := kernel.Write(c, s.cells, c.Scope().Account, func(st *State, _ clock.Instant, _ *kernel.Tx) (aws.ARN, *awserr.Error) {
		have, entity, err := h.get(st)
		if err != nil {
			return aws.ARN{}, err
		}
		i := slices.Index(have.attached, arn)
		if i < 0 {
			return aws.ARN{}, errNotAttached(arn)
		}
		attached := slices.Delete(slices.Clone(have.attached), i, i+1)
		if len(attached) == 0 {
			attached = nil
		}
		h.set(st, holdings{inline: have.inline, attached: attached})
		return entity, nil
	})
	if err != nil {
		return err
	}
	c.Touch(touched)
	return nil
}

// listAttached pages through the entity's attached policies by ARN, as
// listing, keeping those whose path starts with pathPrefix. The sort key is
// the ARN bounded by boundedSortKey, since an ARN under a long path is too
// long to be a Marker.
func (s *Service) listAttached(c *kernel.Call, listing paging.Listing, h holder, pathPrefix, marker *string, maxItems *int32) (paging.Page[iamapi.AttachedPolicy], *awserr.Error) {
	type listed struct {
		p   paging.Page[iamapi.AttachedPolicy]
		arn aws.ARN
	}
	key := func(r policyRef) string { return boundedSortKey(listing, r.arn.String()) }
	l, err := view(s, c.Scope().Account, func(st *State) (listed, *awserr.Error) {
		have, arn, err := h.get(st)
		if err != nil {
			return listed{}, err
		}
		var refs []policyRef
		for _, a := range have.attached {
			ref, ok := parsePolicyARN(a)
			if !ok {
				return listed{}, awserr.Internalf(&attachedARNError{entity: arn, stored: a})
			}
			if underPrefix(ref.path, pathPrefix) {
				refs = append(refs, ref)
			}
		}
		slices.SortFunc(refs, func(a, b policyRef) int { return strings.Compare(key(a), key(b)) })
		p, err := paginate(listing, refs, key, marker, maxItems)
		return listed{p: paging.Page[iamapi.AttachedPolicy]{Items: apiAttached(p.Items), Next: p.Next, Truncated: p.Truncated}, arn: arn}, err
	})
	if err != nil {
		return paging.Page[iamapi.AttachedPolicy]{}, err
	}
	c.Touch(l.arn)
	return l.p, nil
}

// attachedARNError is a stored attachment that is not a managed policy ARN,
// which only a hand-edited snapshot can hold.
type attachedARNError struct {
	entity aws.ARN
	stored string
}

func (e *attachedARNError) Error() string {
	return fmt.Sprintf("iam: %s has a stored policy attachment %q that is not a managed policy ARN; its snapshot section was edited or corrupted, and its attached policies cannot be listed until the ARN is fixed or detached", e.entity, e.stored)
}
