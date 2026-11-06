package events

import (
	"context"
	"errors"
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

// bindTags binds the tag operations, which address buses and rules by ARN
// (eventbridge-buses.md 5.5).
func (s *Service) bindTags(b eventbridgeapi.Binder) {
	b.TagResource(s.tagResource, registry.Full{},
		taggedResource(func(in *eventbridgeapi.TagResourceInput) string { return in.ResourceARN }))
	b.UntagResource(s.untagResource, registry.Full{},
		taggedResource(func(in *eventbridgeapi.UntagResourceInput) string { return in.ResourceARN }))
	b.ListTagsForResource(s.listTagsForResource, registry.Full{},
		taggedResource(func(in *eventbridgeapi.ListTagsForResourceInput) string { return in.ResourceARN }))
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

// taggable is the resource a tag call's ResourceARN names in the caller's
// scope: a bus or a rule.
//
//sumtype:decl
type taggable interface{ isTaggable() }

// busTags is the bus name of the caller's scope, the default bus included.
type busTags struct{ name string }

// ruleTags is a rule of the caller's scope, on any of its buses.
type ruleTags struct{ rule ruleRef }

func (busTags) isTaggable()  {}
func (ruleTags) isTaggable() {}

// errNilTaggable is the cause of a tag call on a taggable that is nil, a
// bug in whatever built it.
var errNilTaggable = errors.New("events: the tagged resource is nil, so no tags were read or changed; parseTaggable builds every taggable")

// parseTaggable reads a tag call's ResourceARN: an event-bus/<name> or a
// rule ARN of either form (eventbridge-buses.md 5.2). A bus or rule of
// another account or region is not found by the texts of a missing one,
// which is inferred: AWS's answer to tagging another scope's resource is
// unrecorded, and a bus policy can grant another account only PutEvents,
// PutRule and PutTargets (eventbridge-buses.md 4.4). Any other ARN is not
// valid.
func parseTaggable(c *kernel.Call, resource string) (taggable, *awserr.Error) {
	arn, err := aws.ParseARN(resource)
	if err != nil || arn.Service != "events" {
		return nil, errInvalidParameter("ResourceARN")
	}
	if name, isBus := strings.CutPrefix(arn.Resource, "event-bus/"); isBus && name != "" {
		if at, err := aws.ScopeOf(arn); err != nil || at != c.Scope() {
			return nil, errBusNotFound(name)
		}
		return busTags{name: name}, nil
	}
	r, ok := parseRuleARN(resource)
	if !ok {
		return nil, errInvalidParameter("ResourceARN")
	}
	if r.scope != c.Scope() {
		return nil, errRuleNotFound(r.name, r.bus)
	}
	return ruleTags{rule: r}, nil
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

// updateTags applies change to a copy of the resource's tags and stores it,
// unless the copy holds more than maxTags or equals the tags it had. The
// first change to the default bus's tags materializes it
// (eventbridge-buses.md 10.2.2). A tag call changes no bus's
// LastModifiedTime, which is inferred.
func (s *Service) updateTags(c *kernel.Call, resource string, change func(tags map[string]string)) *awserr.Error {
	res, err := parseTaggable(c, resource)
	if err != nil {
		return err
	}
	_, err = kernel.Write(c, s.cells, c.Scope(), func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		changed, err := st.retag(res, change, c.Admitted())
		if err == nil && !changed {
			tx.Unchanged()
		}
		return struct{}{}, err
	})
	return err
}

// retag applies change to a copy of res's tags and, when that changes them,
// stores it as a write admitted at at; it reports whether it did.
func (s *State) retag(res taggable, change func(map[string]string), at clock.Instant) (bool, *awserr.Error) {
	switch r := res.(type) {
	case busTags:
		bus, ok := s.bus(r.name, at)
		if !ok {
			return false, errBusNotFound(r.name)
		}
		tags, err := changedTags(bus.Tags, change)
		if err != nil || maps.Equal(tags, bus.Tags) {
			return false, err
		}
		bus.Tags = tags
		s.setBus(bus, at)
		return true, nil
	case ruleTags:
		rule, ok := s.rule(r.rule.bus, r.rule.name)
		if !ok {
			return false, errRuleNotFound(r.rule.name, r.rule.bus)
		}
		tags, err := changedTags(rule.Tags, change)
		if err != nil || maps.Equal(tags, rule.Tags) {
			return false, err
		}
		rule.Tags = tags
		s.setRule(r.rule.bus, rule)
		s.used(at)
		return true, nil
	}
	return false, awserr.Internalf(errNilTaggable)
}

// changedTags is tags with change applied to a copy, nil when it leaves
// none; more than maxTags is refused.
func changedTags(tags map[string]string, change func(map[string]string)) (map[string]string, *awserr.Error) {
	out := maps.Clone(tags)
	if out == nil {
		out = map[string]string{}
	}
	change(out)
	if len(out) > maxTags {
		return nil, errTooManyTags(len(out))
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// listTagsForResource lists the resource's tags by key.
func (s *Service) listTagsForResource(_ context.Context, c *kernel.Call, in *eventbridgeapi.ListTagsForResourceInput) (*eventbridgeapi.ListTagsForResourceOutput, *awserr.Error) {
	res, err := parseTaggable(c, in.ResourceARN)
	if err != nil {
		return nil, err
	}
	var tags map[string]string
	switch r := res.(type) {
	case busTags:
		bus, ok := s.lookupBus(c.Scope(), r.name, c.Admitted())
		if !ok {
			return nil, errBusNotFound(r.name)
		}
		tags = bus.Tags
	case ruleTags:
		rule, ok := s.rule(r.rule)
		if !ok {
			return nil, errRuleNotFound(r.rule.name, r.rule.bus)
		}
		tags = rule.Tags
	default:
		return nil, awserr.Internalf(errNilTaggable)
	}
	out := make([]eventbridgeapi.Tag, 0, len(tags))
	for _, k := range slices.Sorted(maps.Keys(tags)) {
		out = append(out, eventbridgeapi.Tag{Key: k, Value: tags[k]})
	}
	return &eventbridgeapi.ListTagsForResourceOutput{Tags: out}, nil
}
