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
		kernel.WithResourceARN(func(in *cloudwatchapi.TagResourceInput) string { return in.ResourceARN }))
	b.UntagResource(s.untagResource, registry.Partial{Note: note},
		kernel.WithResourceARN(func(in *cloudwatchapi.UntagResourceInput) string { return in.ResourceARN }))
	b.ListTagsForResource(s.listTagsForResource, registry.Partial{Note: note + "; a deleted alarm lists no tags for as long as its history is held, 30 days of emulator time, which is inferred"},
		kernel.WithResourceARN(func(in *cloudwatchapi.ListTagsForResourceInput) string { return in.ResourceARN }))
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
	if len(out) == 0 {
		return nil
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

// listTagsForResource lists an alarm's tags by key. An ARN that names an
// alarm that was never created is ResourceNotFoundException without a
// message (AWS-recorded, cloudwatch/R07/cw-ListTagsForResource.json). A
// deleted alarm has an empty list (third-party-recorded,
// secrets-logs-events-alarms.md 5.1). The two are told apart by the
// alarm's history, which outlives it: a gone alarm whose history is still
// held is a deleted one. How long AWS answers [] for a deleted alarm is
// unknown; understudy does so while it holds the history, historyRetention
// (inferred).
func (s *Service) listTagsForResource(_ context.Context, c *kernel.Call, in *cloudwatchapi.ListTagsForResourceInput) (*cloudwatchapi.ListTagsForResourceOutput, *awserr.Error) {
	ref, ok := taggableAlarm(c, in.ResourceARN)
	if !ok {
		return nil, errListTagsTargetNotFound()
	}
	if cell, ok := s.cells.Lookup(ref.scope); ok {
		listed := store.Read(cell, func(st *State) alarmTags {
			a, ok := st.Alarms[ref.name]
			list := make([]cloudwatchapi.Tag, 0, len(a.Tags))
			for _, k := range slices.Sorted(maps.Keys(a.Tags)) {
				list = append(list, cloudwatchapi.Tag{Key: k, Value: a.Tags[k]})
			}
			return alarmTags{list: list, found: ok}
		})
		if listed.found {
			return &cloudwatchapi.ListTagsForResourceOutput{Tags: listed.list}, nil
		}
	}
	if s.holdsHistory(ref, c.Admitted()) {
		return &cloudwatchapi.ListTagsForResourceOutput{Tags: []cloudwatchapi.Tag{}}, nil
	}
	return nil, errListTagsTargetNotFound()
}

// alarmTags is an alarm's tags by key; found is false when the scope
// stores no such alarm.
type alarmTags struct {
	list  []cloudwatchapi.Tag
	found bool
}

// holdsHistory reports whether the alarm ref has a history item not older
// than historyRetention at now.
func (s *Service) holdsHistory(ref alarmRef, now clock.Instant) bool {
	cell, ok := s.history.Lookup(ref.scope)
	if !ok {
		return false
	}
	cutoff := now.Add(-historyRetention)
	return store.Read(cell, func(h *History) bool {
		return slices.ContainsFunc(h.Items[ref.name], func(it HistoryItem) bool { return !it.At.Before(cutoff) })
	})
}
