package events

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// maxTags is how many tags a resource may have (API_TagResource).
const maxTags = 50

// bindTags binds the tag operations, which address rules by ARN.
func (s *Service) bindTags(b eventbridgeapi.Binder) {
	const note = "rules only; the default bus takes no tags"
	b.TagResource(s.tagResource, registry.Partial{Note: note},
		taggedRule(func(in *eventbridgeapi.TagResourceInput) string { return in.ResourceARN }))
	b.UntagResource(s.untagResource, registry.Partial{Note: note},
		taggedRule(func(in *eventbridgeapi.UntagResourceInput) string { return in.ResourceARN }))
	b.ListTagsForResource(s.listTagsForResource, registry.Partial{Note: note},
		taggedRule(func(in *eventbridgeapi.ListTagsForResourceInput) string { return in.ResourceARN }))
}

// taggedRule names the resource a tag call addresses when its ARN parses.
func taggedRule[In any](arn func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(_ *kernel.Call, in *In) []aws.ARN {
		a, err := aws.ParseARN(arn(in))
		if err != nil {
			return nil
		}
		return []aws.ARN{a}
	})
}

// taggableRule resolves a tag call's ResourceARN to a rule of the caller's
// default bus. A bus ARN is refused as not implemented; a rule in another
// account or region, like a missing one, is not found.
func taggableRule(c *kernel.Call, resource string) (ruleRef, *awserr.Error) {
	arn, err := aws.ParseARN(resource)
	if err != nil || arn.Service != "events" {
		return ruleRef{}, errInvalidParameter("ResourceARN")
	}
	if strings.HasPrefix(arn.Resource, "event-bus/") {
		return ruleRef{}, errBusTags()
	}
	name, ok := strings.CutPrefix(arn.Resource, "rule/")
	if !ok {
		return ruleRef{}, errInvalidParameter("ResourceARN")
	}
	r := ruleRef{scope: c.Scope(), name: name}
	if strings.Contains(name, "/") || arn != r.arn() {
		return ruleRef{}, errRuleNotFound(name)
	}
	return r, nil
}

func (s *Service) tagResource(_ context.Context, c *kernel.Call, in *eventbridgeapi.TagResourceInput) (*eventbridgeapi.TagResourceOutput, *awserr.Error) {
	err := s.updateTags(c, in.ResourceARN, func(tags map[string]string) {
		for _, t := range in.Tags {
			tags[t.Key] = t.Value
		}
	})
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.TagResourceOutput{}, nil
}

func (s *Service) untagResource(_ context.Context, c *kernel.Call, in *eventbridgeapi.UntagResourceInput) (*eventbridgeapi.UntagResourceOutput, *awserr.Error) {
	err := s.updateTags(c, in.ResourceARN, func(tags map[string]string) {
		for _, k := range in.TagKeys {
			delete(tags, k)
		}
	})
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.UntagResourceOutput{}, nil
}

// updateTags applies change to a copy of the rule's tags and stores it,
// unless the copy holds more than maxTags.
func (s *Service) updateTags(c *kernel.Call, resource string, change func(tags map[string]string)) *awserr.Error {
	r, err := taggableRule(c, resource)
	if err != nil {
		return err
	}
	_, err = kernel.Write(c, s.cells, r.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		rule, ok := st.Rules[r.name]
		if !ok {
			return struct{}{}, errRuleNotFound(r.name)
		}
		tags := maps.Clone(rule.Tags)
		if tags == nil {
			tags = map[string]string{}
		}
		change(tags)
		if len(tags) > maxTags {
			return struct{}{}, errTooManyTags(len(tags))
		}
		rule.Tags = tags
		if len(tags) == 0 {
			rule.Tags = nil
		}
		st.Rules[r.name] = rule
		return struct{}{}, nil
	})
	return err
}

// listTagsForResource lists the rule's tags by key.
func (s *Service) listTagsForResource(_ context.Context, c *kernel.Call, in *eventbridgeapi.ListTagsForResourceInput) (*eventbridgeapi.ListTagsForResourceOutput, *awserr.Error) {
	r, err := taggableRule(c, in.ResourceARN)
	if err != nil {
		return nil, err
	}
	rule, ok := s.rule(r)
	if !ok {
		return nil, errRuleNotFound(r.name)
	}
	tags := make([]eventbridgeapi.Tag, 0, len(rule.Tags))
	for _, k := range slices.Sorted(maps.Keys(rule.Tags)) {
		tags = append(tags, eventbridgeapi.Tag{Key: k, Value: rule.Tags[k]})
	}
	return &eventbridgeapi.ListTagsForResourceOutput{Tags: tags}, nil
}
