package ssm

import (
	"context"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// addTags is tags with add applied in order: a new key is appended, an
// existing key takes the new value (AddTagsToResource "Adds or
// overwrites", ssm-parameters.md 7.9). tags is not modified.
func addTags(tags []Tag, add []ssmapi.Tag) []Tag {
	out := slices.Clone(tags)
	for _, t := range add {
		if i := slices.IndexFunc(out, func(o Tag) bool { return o.Key == t.Key }); i >= 0 {
			out[i].Value = t.Value
			continue
		}
		out = append(out, Tag{Key: t.Key, Value: t.Value})
	}
	return out
}

// tagTarget is the parameter a tag operation names (ssm-parameters.md 7.9):
// ResourceType Parameter with a parameter name as ResourceId. Any other
// resource type names nothing understudy stores (coverage note), and a
// ResourceId that is not a valid name names no parameter; both are
// InvalidResourceId, as a missing parameter is.
func tagTarget(op string, rt ssmapi.ResourceTypeForTagging, id string, scope aws.Scope) (name, *awserr.Error) {
	if err := checkReserved(op, id); err != nil {
		return name{}, err
	}
	if rt != ssmapi.ResourceTypeForTaggingParameter {
		return name{}, ssmapi.ErrInvalidResourceId()
	}
	n, err := parseName(id, scope)
	if err != nil {
		return name{}, ssmapi.ErrInvalidResourceId()
	}
	return n, nil
}

// retag replaces the tags of the parameter n with change's result. A
// missing parameter is InvalidResourceId; when change fails, or leaves the
// tags as they were, nothing is written.
func (s *Service) retag(c *kernel.Call, n name, change func([]Tag) ([]Tag, *awserr.Error)) *awserr.Error {
	scope := c.Scope()
	_, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		p, ok := st.Parameters[n.canonical]
		if !ok {
			tx.Unchanged()
			return struct{}{}, ssmapi.ErrInvalidResourceId()
		}
		tags, err := change(p.Tags)
		if err != nil || slices.Equal(tags, p.Tags) {
			tx.Unchanged()
			return struct{}{}, err
		}
		p.Tags = tags
		st.Parameters[n.canonical] = p
		return struct{}{}, nil
	})
	if err != nil {
		return err
	}
	c.Touch(parameterARN(scope, n.canonical))
	return nil
}

// addTagsToResource adds tags to a parameter or overwrites the values of
// keys it has (ssm-parameters.md 7.9). A result over 50 tags is
// TooManyTagsError and nothing is applied.
func (s *Service) addTagsToResource(_ context.Context, c *kernel.Call, in *ssmapi.AddTagsToResourceInput) (*ssmapi.AddTagsToResourceOutput, *awserr.Error) {
	n, err := tagTarget("AddTagsToResource", in.ResourceType, in.ResourceId, c.Scope())
	if err != nil {
		return nil, err
	}
	err = s.retag(c, n, func(tags []Tag) ([]Tag, *awserr.Error) {
		out := addTags(tags, in.Tags)
		if len(out) > maxTags {
			return nil, ssmapi.ErrTooManyTagsError()
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return &ssmapi.AddTagsToResourceOutput{}, nil
}

// removeTagsFromResource removes tag keys from a parameter.
// UNVERIFIED (SSM-25), ssm-parameters.md 7.9: a key the parameter does not
// carry is ignored.
func (s *Service) removeTagsFromResource(_ context.Context, c *kernel.Call, in *ssmapi.RemoveTagsFromResourceInput) (*ssmapi.RemoveTagsFromResourceOutput, *awserr.Error) {
	n, err := tagTarget("RemoveTagsFromResource", in.ResourceType, in.ResourceId, c.Scope())
	if err != nil {
		return nil, err
	}
	err = s.retag(c, n, func(tags []Tag) ([]Tag, *awserr.Error) {
		return slices.DeleteFunc(slices.Clone(tags), func(t Tag) bool { return slices.Contains(in.TagKeys, t.Key) }), nil
	})
	if err != nil {
		return nil, err
	}
	return &ssmapi.RemoveTagsFromResourceOutput{}, nil
}

// listTagsForResource lists a parameter's tags in the order they were
// first set, [] when it has none. UNVERIFIED (SSM-25), ssm-parameters.md
// 7.9: the empty list rather than an absent one.
func (s *Service) listTagsForResource(_ context.Context, c *kernel.Call, in *ssmapi.ListTagsForResourceInput) (*ssmapi.ListTagsForResourceOutput, *awserr.Error) {
	scope := c.Scope()
	n, err := tagTarget("ListTagsForResource", in.ResourceType, in.ResourceId, scope)
	if err != nil {
		return nil, err
	}
	p, ok := s.lookup(scope, n.canonical)
	if !ok {
		return nil, ssmapi.ErrInvalidResourceId()
	}
	tags := make([]ssmapi.Tag, 0, len(p.Tags))
	for _, t := range p.Tags {
		tags = append(tags, ssmapi.Tag{Key: t.Key, Value: t.Value})
	}
	c.Touch(parameterARN(scope, p.Name))
	return &ssmapi.ListTagsForResourceOutput{TagList: tags}, nil
}
