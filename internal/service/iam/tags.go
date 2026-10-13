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
	"github.com/sustinbebustin/understudy/internal/paging"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// tagKeys is how the tag keys of one kind of entity compare. The IAM User
// Guide's tagging rules make the keys of roles and users case-insensitive
// and those of customer managed policies and instance profiles
// case-sensitive, so Costcenter and costcenter are two tags of a policy but
// one of a role.
type tagKeys int

const (
	// foldedKeys match regardless of case: roles and users.
	foldedKeys tagKeys = iota + 1
	// exactKeys match only byte for byte: managed policies and instance
	// profiles.
	exactKeys
)

// same reports whether a and b name the same tag.
func (m tagKeys) same(a, b string) bool {
	switch m {
	case foldedKeys:
		return strings.EqualFold(a, b)
	case exactKeys:
		return a == b
	}
	return a == b
}

// reservedTagPrefix begins the tag keys and values AWS keeps for itself, in
// any case (IAM User Guide, rules for tagging).
const reservedTagPrefix = "aws:"

func reserved(s string) bool {
	return len(s) >= len(reservedTagPrefix) && strings.EqualFold(s[:len(reservedTagPrefix)], reservedTagPrefix)
}

// newTags reads the Tags of a create request, whose keys compare the way m
// says. An empty list is nil.
func newTags(m tagKeys, in []iamapi.Tag) ([]Tag, *awserr.Error) {
	return mergeTags(m, nil, in)
}

// mergeTags adds or replaces tags by key, compared the way m says, keeping
// the position of a replaced tag. A request naming a key twice is refused,
// and so are a key or value with the reserved prefix and a result over the
// quota.
func mergeTags(m tagKeys, have []Tag, add []iamapi.Tag) ([]Tag, *awserr.Error) {
	out := slices.Clone(have)
	for i, t := range add {
		if reserved(t.Key) || reserved(t.Value) {
			return nil, errReservedTag(t.Key)
		}
		if slices.ContainsFunc(add[:i], func(u iamapi.Tag) bool { return m.same(u.Key, t.Key) }) {
			return nil, errDuplicateTagKeys(m)
		}
		if j := tagIndex(m, out, t.Key); j >= 0 {
			out[j] = Tag{Key: t.Key, Value: t.Value}
		} else {
			out = append(out, Tag{Key: t.Key, Value: t.Value})
		}
	}
	if len(out) > maxTags {
		return nil, errTooManyTags()
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// dropTags removes the tags with the given keys, compared the way m says;
// a key no tag has is ignored.
func dropTags(m tagKeys, have []Tag, keys []string) []Tag {
	out := slices.DeleteFunc(slices.Clone(have), func(t Tag) bool {
		return slices.ContainsFunc(keys, func(k string) bool { return m.same(k, t.Key) })
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

func tagIndex(m tagKeys, tags []Tag, k string) int {
	return slices.IndexFunc(tags, func(t Tag) bool { return m.same(t.Key, k) })
}

// apiTags is tags on the wire, nil when there are none.
func apiTags(tags []Tag) []iamapi.Tag {
	if len(tags) == 0 {
		return nil
	}
	out := make([]iamapi.Tag, len(tags))
	for i, t := range tags {
		out[i] = iamapi.Tag{Key: t.Key, Value: t.Value}
	}
	return out
}

// tagsOf reads the tags of one entity of a State with the entity's ARN, or
// the error a missing entity answers.
type tagsOf func(st *State) ([]Tag, aws.ARN, *awserr.Error)

// tagged reaches the tags of one entity of a State: keys is how its tag
// keys compare; get reads them; set stores the entity again with new tags.
type tagged struct {
	keys tagKeys
	get  tagsOf
	set  func(st *State, tags []Tag)
}

func roleTagged(account aws.AccountID, name string) tagged {
	return tagged{
		keys: foldedKeys,
		get: func(st *State) ([]Tag, aws.ARN, *awserr.Error) {
			r, ok := st.Roles[key(name)]
			if !ok {
				return nil, aws.ARN{}, errNoSuchRole(name)
			}
			return r.Tags, roleARN(account, r.Path, r.Name), nil
		},
		set: func(st *State, tags []Tag) {
			r := st.Roles[key(name)]
			r.Tags = tags
			st.Roles[key(name)] = r
		},
	}
}

func userTagged(account aws.AccountID, name string) tagged {
	return tagged{
		keys: foldedKeys,
		get: func(st *State) ([]Tag, aws.ARN, *awserr.Error) {
			u, ok := st.Users[key(name)]
			if !ok {
				return nil, aws.ARN{}, errNoSuchUser(name)
			}
			return u.Tags, userARN(account, u.Path, u.Name), nil
		},
		set: func(st *State, tags []Tag) {
			u := st.Users[key(name)]
			u.Tags = tags
			st.Users[key(name)] = u
		},
	}
}

func instanceProfileTagged(account aws.AccountID, name string) tagged {
	return tagged{
		keys: exactKeys,
		get: func(st *State) ([]Tag, aws.ARN, *awserr.Error) {
			p, ok := st.InstanceProfiles[key(name)]
			if !ok {
				return nil, aws.ARN{}, errNoSuchInstanceProfile(name)
			}
			return p.Tags, instanceProfileARN(account, p.Path, p.Name), nil
		},
		set: func(st *State, tags []Tag) {
			p := st.InstanceProfiles[key(name)]
			p.Tags = tags
			st.InstanceProfiles[key(name)] = p
		},
	}
}

// policyTagged reaches a customer managed policy; AWS managed policies
// carry no tags of the account's, so tagging one is NoSuchEntity.
func policyTagged(account aws.AccountID, arn string) tagged {
	return tagged{
		keys: exactKeys,
		get: func(st *State) ([]Tag, aws.ARN, *awserr.Error) {
			p, ref, err := st.localPolicy(account, arn)
			if err != nil {
				return nil, aws.ARN{}, err
			}
			return p.Tags, ref.arn, nil
		},
		set: func(st *State, tags []Tag) {
			ref, _ := parsePolicyARN(arn)
			p := st.Policies[key(ref.name)]
			p.Tags = tags
			st.Policies[key(ref.name)] = p
		},
	}
}

// policyTags reads the tags ListPolicyTags shows: a customer managed
// policy's, or none for an AWS managed policy, which exists for every
// account (unknownAWSManaged).
func policyTags(account aws.AccountID, arn string) tagsOf {
	if ref, ok := parsePolicyARN(arn); ok && ref.awsManaged() {
		return func(*State) ([]Tag, aws.ARN, *awserr.Error) { return nil, ref.arn, nil }
	}
	return policyTagged(account, arn).get
}

// bindTags binds the tag operations of roles, users, managed policies and
// instance profiles.
func (s *Service) bindTags(b iamapi.Binder) {
	b.TagRole(func(_ context.Context, c *kernel.Call, in *iamapi.TagRoleInput) (*iamapi.TagRoleOutput, *awserr.Error) {
		return unit[iamapi.TagRoleOutput](s.tag(c, roleTagged(c.Scope().Account, in.RoleName), in.Tags))
	}, registry.Full{})
	b.UntagRole(func(_ context.Context, c *kernel.Call, in *iamapi.UntagRoleInput) (*iamapi.UntagRoleOutput, *awserr.Error) {
		return unit[iamapi.UntagRoleOutput](s.untag(c, roleTagged(c.Scope().Account, in.RoleName), in.TagKeys))
	}, registry.Full{})
	b.ListRoleTags(func(_ context.Context, c *kernel.Call, in *iamapi.ListRoleTagsInput) (*iamapi.ListRoleTagsOutput, *awserr.Error) {
		p, err := s.listTags(c, "role-tags", roleTagged(c.Scope().Account, in.RoleName).get, in.Marker, in.MaxItems)
		if err != nil {
			return nil, err
		}
		return &iamapi.ListRoleTagsOutput{Tags: p.Items, IsTruncated: p.Truncated, Marker: p.Next}, nil
	}, registry.Full{})
	b.TagUser(func(_ context.Context, c *kernel.Call, in *iamapi.TagUserInput) (*iamapi.TagUserOutput, *awserr.Error) {
		return unit[iamapi.TagUserOutput](s.tag(c, userTagged(c.Scope().Account, in.UserName), in.Tags))
	}, registry.Full{})
	b.UntagUser(func(_ context.Context, c *kernel.Call, in *iamapi.UntagUserInput) (*iamapi.UntagUserOutput, *awserr.Error) {
		return unit[iamapi.UntagUserOutput](s.untag(c, userTagged(c.Scope().Account, in.UserName), in.TagKeys))
	}, registry.Full{})
	b.ListUserTags(func(_ context.Context, c *kernel.Call, in *iamapi.ListUserTagsInput) (*iamapi.ListUserTagsOutput, *awserr.Error) {
		p, err := s.listTags(c, "user-tags", userTagged(c.Scope().Account, in.UserName).get, in.Marker, in.MaxItems)
		if err != nil {
			return nil, err
		}
		return &iamapi.ListUserTagsOutput{Tags: p.Items, IsTruncated: p.Truncated, Marker: p.Next}, nil
	}, registry.Full{})
	b.TagPolicy(func(_ context.Context, c *kernel.Call, in *iamapi.TagPolicyInput) (*iamapi.TagPolicyOutput, *awserr.Error) {
		return unit[iamapi.TagPolicyOutput](s.tag(c, policyTagged(c.Scope().Account, in.PolicyArn), in.Tags))
	}, registry.Full{})
	b.UntagPolicy(func(_ context.Context, c *kernel.Call, in *iamapi.UntagPolicyInput) (*iamapi.UntagPolicyOutput, *awserr.Error) {
		return unit[iamapi.UntagPolicyOutput](s.untag(c, policyTagged(c.Scope().Account, in.PolicyArn), in.TagKeys))
	}, registry.Full{})
	b.ListPolicyTags(func(_ context.Context, c *kernel.Call, in *iamapi.ListPolicyTagsInput) (*iamapi.ListPolicyTagsOutput, *awserr.Error) {
		p, err := s.listTags(c, "policy-tags", policyTags(c.Scope().Account, in.PolicyArn), in.Marker, in.MaxItems)
		if err != nil {
			return nil, err
		}
		return &iamapi.ListPolicyTagsOutput{Tags: p.Items, IsTruncated: p.Truncated, Marker: p.Next}, nil
	}, registry.Full{})
	b.TagInstanceProfile(func(_ context.Context, c *kernel.Call, in *iamapi.TagInstanceProfileInput) (*iamapi.TagInstanceProfileOutput, *awserr.Error) {
		return unit[iamapi.TagInstanceProfileOutput](s.tag(c, instanceProfileTagged(c.Scope().Account, in.InstanceProfileName), in.Tags))
	}, registry.Full{})
	b.UntagInstanceProfile(func(_ context.Context, c *kernel.Call, in *iamapi.UntagInstanceProfileInput) (*iamapi.UntagInstanceProfileOutput, *awserr.Error) {
		return unit[iamapi.UntagInstanceProfileOutput](s.untag(c, instanceProfileTagged(c.Scope().Account, in.InstanceProfileName), in.TagKeys))
	}, registry.Full{})
	b.ListInstanceProfileTags(func(_ context.Context, c *kernel.Call, in *iamapi.ListInstanceProfileTagsInput) (*iamapi.ListInstanceProfileTagsOutput, *awserr.Error) {
		p, err := s.listTags(c, "instance-profile-tags", instanceProfileTagged(c.Scope().Account, in.InstanceProfileName).get, in.Marker, in.MaxItems)
		if err != nil {
			return nil, err
		}
		return &iamapi.ListInstanceProfileTagsOutput{Tags: p.Items, IsTruncated: p.Truncated, Marker: p.Next}, nil
	}, registry.Full{})
}

// tag adds or replaces tags on the entity e reaches.
func (s *Service) tag(c *kernel.Call, e tagged, add []iamapi.Tag) *awserr.Error {
	arn, err := kernel.Write(c, s.cells, c.Scope().Account, func(st *State, _ clock.Instant, _ *kernel.Tx) (aws.ARN, *awserr.Error) {
		have, arn, err := e.get(st)
		if err != nil {
			return aws.ARN{}, err
		}
		tags, err := mergeTags(e.keys, have, add)
		if err != nil {
			return aws.ARN{}, err
		}
		e.set(st, tags)
		return arn, nil
	})
	if err == nil {
		c.Touch(arn)
	}
	return err
}

// untag removes tags from the entity e reaches.
func (s *Service) untag(c *kernel.Call, e tagged, keys []string) *awserr.Error {
	arn, err := kernel.Write(c, s.cells, c.Scope().Account, func(st *State, _ clock.Instant, _ *kernel.Tx) (aws.ARN, *awserr.Error) {
		have, arn, err := e.get(st)
		if err != nil {
			return aws.ARN{}, err
		}
		e.set(st, dropTags(e.keys, have, keys))
		return arn, nil
	})
	if err == nil {
		c.Touch(arn)
	}
	return err
}

// listTags pages through the tags get reads, as listing, by key bounded
// with boundedSortKey: a tag key is up to 128 characters of any script, so
// its Marker can pass the 320 characters the Marker validator allows.
func (s *Service) listTags(c *kernel.Call, listing paging.Listing, get tagsOf, marker *string, maxItems *int32) (paging.Page[iamapi.Tag], *awserr.Error) {
	type listed struct {
		p   paging.Page[iamapi.Tag]
		arn aws.ARN
	}
	l, err := view(s, c.Scope().Account, func(st *State) (listed, *awserr.Error) {
		have, arn, err := get(st)
		if err != nil {
			return listed{}, err
		}
		key := func(t iamapi.Tag) string { return boundedSortKey(listing, t.Key) }
		tags := slices.SortedFunc(slices.Values(apiTags(have)), func(a, b iamapi.Tag) int { return strings.Compare(key(a), key(b)) })
		p, err := paginate(listing, tags, key, marker, maxItems)
		return listed{p: p, arn: arn}, err
	})
	if err != nil {
		return paging.Page[iamapi.Tag]{}, err
	}
	c.Touch(l.arn)
	return l.p, nil
}

// unit is the answer of an operation without output: its empty output, or
// err alone.
func unit[Out any](err *awserr.Error) (*Out, *awserr.Error) {
	if err != nil {
		return nil, err
	}
	return new(Out), nil
}
