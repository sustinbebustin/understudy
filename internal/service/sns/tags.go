package sns

import (
	"context"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// maxTags is the tag count above which a topic refuses more (sns.md 2.3).
const maxTags = 50

// bindTags binds the tagging operations, whose resource is a topic.
func (s *Service) bindTags(b snsapi.Binder) {
	b.TagResource(s.tagResource, registry.Full{},
		arnResource(func(in *snsapi.TagResourceInput) string { return in.ResourceArn }))
	b.UntagResource(s.untagResource, registry.Full{},
		arnResource(func(in *snsapi.UntagResourceInput) string { return in.ResourceArn }))
	b.ListTagsForResource(s.listTagsForResource, registry.Full{},
		arnResource(func(in *snsapi.ListTagsForResourceInput) string { return in.ResourceArn }))
}

// tagMap returns tags by key; a key given twice is refused (sns.md 2.3).
func tagMap(tags []snsapi.Tag) (map[string]string, *awserr.Error) {
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		if _, dup := out[t.Key]; dup {
			return nil, errInvalidParameter("Duplicated keys are not allowed.")
		}
		out[t.Key] = t.Value
	}
	return out, nil
}

// tagResource adds tags to a topic, replacing the value of a key it has.
func (s *Service) tagResource(_ context.Context, c *kernel.Call, in *snsapi.TagResourceInput) (*snsapi.TagResourceOutput, *awserr.Error) {
	ref, err := resolveTopic(c, "ResourceArn", in.ResourceArn)
	if err != nil {
		return nil, err
	}
	tags, err := tagMap(in.Tags)
	if err != nil {
		return nil, err
	}
	_, err = writeTaggedTopic(s, c, ref, func(t *Topic) *awserr.Error {
		merged := maps.Clone(t.Tags)
		if merged == nil {
			merged = map[string]string{}
		}
		maps.Copy(merged, tags)
		if len(merged) > maxTags {
			return errTagLimit()
		}
		t.Tags = merged
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &snsapi.TagResourceOutput{}, nil
}

// untagResource removes keys from a topic; a key it lacks is ignored.
func (s *Service) untagResource(_ context.Context, c *kernel.Call, in *snsapi.UntagResourceInput) (*snsapi.UntagResourceOutput, *awserr.Error) {
	ref, err := resolveTopic(c, "ResourceArn", in.ResourceArn)
	if err != nil {
		return nil, err
	}
	_, err = writeTaggedTopic(s, c, ref, func(t *Topic) *awserr.Error {
		for _, k := range in.TagKeys {
			delete(t.Tags, k)
		}
		if len(t.Tags) == 0 {
			t.Tags = nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &snsapi.UntagResourceOutput{}, nil
}

// listTagsForResource lists a topic's tags by key.
func (s *Service) listTagsForResource(_ context.Context, c *kernel.Call, in *snsapi.ListTagsForResourceInput) (*snsapi.ListTagsForResourceOutput, *awserr.Error) {
	ref, err := resolveTopic(c, "ResourceArn", in.ResourceArn)
	if err != nil {
		return nil, err
	}
	t, ok := s.readTopic(ref)
	if !ok {
		return nil, errResourceNotFound()
	}
	out := &snsapi.ListTagsForResourceOutput{Tags: make([]snsapi.Tag, 0, len(t.Tags))}
	for _, k := range slices.Sorted(maps.Keys(t.Tags)) {
		out.Tags = append(out.Tags, snsapi.Tag{Key: k, Value: t.Tags[k]})
	}
	return out, nil
}

// writeTaggedTopic is writeTopic for the tag operations, whose missing
// resource is ResourceNotFound rather than NotFound.
func writeTaggedTopic(s *Service, c *kernel.Call, ref topicRef, fn func(t *Topic) *awserr.Error) (struct{}, *awserr.Error) {
	return kernel.Write(c, s.cells, ref.scope, func(st *State, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		t, ok := st.writable(ref.name, now)
		if !ok {
			return struct{}{}, errResourceNotFound()
		}
		if err := fn(&t); err != nil {
			return struct{}{}, err
		}
		st.Topics[ref.name] = t
		return struct{}{}, nil
	})
}
