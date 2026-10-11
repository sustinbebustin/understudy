package cloudwatch

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// maxTags is the tag quota of one resource (secrets-logs-events-alarms.md
// 5.1).
const maxTags = 50

// bindTags binds the tag operations, which address alarms by ARN.
func (s *Service) bindTags(b cloudwatchapi.Binder) {
	const note = "alarms only: insight rules and metric streams are never stored"
	b.TagResource(s.tagResource, registry.Partial{Note: note},
		taggedResource(func(in *cloudwatchapi.TagResourceInput) string { return in.ResourceARN }))
	b.UntagResource(s.untagResource, registry.Partial{Note: note},
		taggedResource(func(in *cloudwatchapi.UntagResourceInput) string { return in.ResourceARN }))
	b.ListTagsForResource(s.listTagsForResource, registry.Partial{Note: note},
		taggedResource(func(in *cloudwatchapi.ListTagsForResourceInput) string { return in.ResourceARN }))
}

// taggedResource names the resource a tag call addresses when its ARN
// parses.
func taggedResource[In any](arn func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(_ *kernel.Call, in *In) []aws.ARN {
		a, err := aws.ParseARN(arn(in))
		if err != nil {
			return nil
		}
		return []aws.ARN{a}
	})
}

// taggableAlarm is the alarm a tag call's ResourceARN names in the
// caller's scope; ok is false for any other ARN, which names nothing
// understudy stores.
func taggableAlarm(c *kernel.Call, resource string) (alarmRef, bool) {
	arn, err := aws.ParseARN(resource)
	if err != nil {
		return alarmRef{}, false
	}
	name, ok := strings.CutPrefix(arn.Resource, "alarm:")
	if !ok {
		return alarmRef{}, false
	}
	ref := named(c, name)
	return ref, arn == ref.arn()
}

// tagMap returns existing with tags set over it, as a new map; nil when
// the result is empty.
func tagMap(existing map[string]string, tags []cloudwatchapi.Tag) map[string]string {
	out := maps.Clone(existing)
	for _, t := range tags {
		if out == nil {
			out = make(map[string]string, len(tags))
		}
		out[t.Key] = t.Value
	}
	return out
}

func (s *Service) tagResource(_ context.Context, c *kernel.Call, in *cloudwatchapi.TagResourceInput) (*cloudwatchapi.TagResourceOutput, *awserr.Error) {
	err := s.updateTags(c, in.ResourceARN, func(tags map[string]string) (map[string]string, *awserr.Error) {
		merged := tagMap(tags, in.Tags)
		if len(merged) > maxTags {
			return nil, errTooManyTags()
		}
		return merged, nil
	})
	if err != nil {
		return nil, err
	}
	return &cloudwatchapi.TagResourceOutput{}, nil
}

func (s *Service) untagResource(_ context.Context, c *kernel.Call, in *cloudwatchapi.UntagResourceInput) (*cloudwatchapi.UntagResourceOutput, *awserr.Error) {
	err := s.updateTags(c, in.ResourceARN, func(tags map[string]string) (map[string]string, *awserr.Error) {
		kept := maps.Clone(tags)
		for _, k := range in.TagKeys {
			delete(kept, k)
		}
		if len(kept) == 0 {
			return nil, nil
		}
		return kept, nil
	})
	if err != nil {
		return nil, err
	}
	return &cloudwatchapi.UntagResourceOutput{}, nil
}

// updateTags stores what change makes of the alarm's tags; change gets
// the stored map and must not write to it.
func (s *Service) updateTags(c *kernel.Call, resource string, change func(tags map[string]string) (map[string]string, *awserr.Error)) *awserr.Error {
	ref, ok := taggableAlarm(c, resource)
	if !ok {
		return errTagTargetNotFound(resource)
	}
	if _, ok := s.cells.Lookup(ref.scope); !ok {
		return errTagTargetNotFound(resource)
	}
	_, err := kernel.Write(c, s.cells, ref.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		a, ok := st.Alarms[ref.name]
		if !ok {
			return struct{}{}, errTagTargetNotFound(resource)
		}
		tags, err := change(a.Tags)
		if err != nil {
			return struct{}{}, err
		}
		a.Tags = tags
		st.Alarms[ref.name] = a
		return struct{}{}, nil
	})
	return err
}

// listTagsForResource lists an alarm's tags by key. An ARN that names no
// alarm, a deleted one included, has an empty list (AWS-recorded,
// secrets-logs-events-alarms.md 5.1).
func (s *Service) listTagsForResource(_ context.Context, c *kernel.Call, in *cloudwatchapi.ListTagsForResourceInput) (*cloudwatchapi.ListTagsForResourceOutput, *awserr.Error) {
	out := &cloudwatchapi.ListTagsForResourceOutput{Tags: []cloudwatchapi.Tag{}}
	ref, ok := taggableAlarm(c, in.ResourceARN)
	if !ok {
		return out, nil
	}
	cell, ok := s.cells.Lookup(ref.scope)
	if !ok {
		return out, nil
	}
	out.Tags = store.Read(cell, func(st *State) []cloudwatchapi.Tag {
		tags := st.Alarms[ref.name].Tags
		list := make([]cloudwatchapi.Tag, 0, len(tags))
		for _, k := range slices.Sorted(maps.Keys(tags)) {
			list = append(list, cloudwatchapi.Tag{Key: k, Value: tags[k]})
		}
		return list
	})
	return out, nil
}
